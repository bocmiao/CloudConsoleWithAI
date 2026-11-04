package app

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx/sshtest"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

func noticeTitles(a *App) []string {
	var out []string
	for _, n := range a.Notices().Notices {
		out = append(out, n.Title)
	}
	return out
}

func TestMonitorSites(t *testing.T) {
	a := newApp(t)
	ctx := context.Background()
	var down atomic.Bool
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.UserAgent(), "MiaoPanel-Monitor") {
			t.Errorf("user agent %q", r.UserAgent())
		}
		if down.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer site.Close()
	s := defaultMonitorSettings()
	s.Auto, s.Servers, s.Extra = false, false, []string{site.URL}
	if _, err := a.SaveMonitorSettings(s); err != nil {
		t.Fatal(err)
	}

	a.monitorRound(ctx, false)
	v, err := a.MonitorPage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Sites) != 1 || v.Sites[0].Up == nil || !*v.Sites[0].Up || v.Sites[0].URL != site.URL+"/" || v.Sites[0].Source != "手动添加" {
		t.Fatalf("sites = %+v", v.Sites)
	}

	// One failure is not an outage yet; two are, when they are apart
	// (立即检查 right after a failure is not a second one).
	down.Store(true)
	a.monitorRound(ctx, false)
	if len(noticeTitles(a)) != 0 {
		t.Fatalf("alerted after one failure: %v", noticeTitles(a))
	}
	a.monitorRound(ctx, false)
	if len(noticeTitles(a)) != 0 {
		t.Fatalf("alerted on two failures a moment apart: %v", noticeTitles(a))
	}
	a.mon.mu.Lock()
	a.mon.gap = time.Nanosecond
	a.mon.mu.Unlock()
	a.monitorRound(ctx, false)
	titles := noticeTitles(a)
	if len(titles) != 1 || !strings.HasPrefix(titles[0], "网站打不开") {
		t.Fatalf("notices = %v", titles)
	}
	if n := a.Notices().Notices[0]; !strings.Contains(n.Text, "服务器返回 502") {
		t.Errorf("alert text = %q", n.Text)
	}
	a.monitorRound(ctx, false) // still down: no second alert
	if len(noticeTitles(a)) != 1 {
		t.Fatalf("alerted again: %v", noticeTitles(a))
	}
	v, _ = a.MonitorPage(ctx)
	if sm := v.Sites[0]; *sm.Up || sm.Since == "" || sm.Status != 502 || *sm.Uptime != 20 || len(sm.Recent) != 5 || sm.Recent[4] != -1 {
		t.Fatalf("down site = %+v", sm)
	}
	if len(v.Incidents) != 1 || v.Incidents[0].EndedAt != "" || v.Incidents[0].Kind != "site" {
		t.Fatalf("incidents = %+v", v.Incidents)
	}

	down.Store(false)
	a.monitorRound(ctx, false)
	titles = noticeTitles(a)
	if len(titles) != 2 || !strings.HasPrefix(titles[0], "网站恢复了") { // newest first
		t.Fatalf("notices = %v", titles)
	}
	v, _ = a.MonitorPage(ctx)
	if v.Incidents[0].EndedAt == "" || v.Sites[0].Since != "" {
		t.Fatalf("incident not ended: %+v", v.Incidents)
	}
	hist, err := a.SiteHistory(site.URL+"/", 24)
	if err != nil || len(hist) == 0 {
		t.Fatalf("history = %+v, %v", hist, err)
	}
	var n, fails int
	for _, p := range hist {
		n, fails = n+p.N, fails+p.Fails
	}
	if n != 6 || fails != 4 {
		t.Errorf("history counts %d checks, %d failures", n, fails)
	}

	// Down again, then set aside: the outage ends, since it cannot recover.
	down.Store(true)
	a.monitorRound(ctx, false)
	a.monitorRound(ctx, false)
	s.Skip = []string{site.URL}
	if _, err := a.SaveMonitorSettings(s); err != nil {
		t.Fatal(err)
	}
	if v, err = a.CheckNow(ctx); err != nil || len(v.Sites) != 0 {
		t.Fatalf("after skipping: %+v %v", v.Sites, err)
	}
	for _, in := range v.Incidents {
		if in.EndedAt == "" {
			t.Fatalf("still open: %+v", in)
		}
	}

	// A closed port says so.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	if c := a.probe(ctx, "http://"+addr+"/"); c.OK || !strings.Contains(c.Error, "连接被拒绝") {
		t.Errorf("closed port = %+v", c)
	}
}

func TestMonitorSettings(t *testing.T) {
	a := newApp(t)
	s := a.MonitorSettings()
	if !s.Enabled || !s.Auto || s.DiskPct != 90 {
		t.Fatalf("defaults = %+v", s)
	}
	s.Extra = []string{"example.com", " https://Shop.Example.com/health ", "example.com"}
	s.Email = "me@example.com"
	got, err := a.SaveMonitorSettings(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Extra, " ") != "http://example.com/ https://shop.example.com/health" {
		t.Errorf("extra = %v", got.Extra)
	}
	for _, bad := range []MonitorSettings{
		{Extra: []string{"ftp://example.com"}, DiskPct: 90, MemPct: 95, CPUPct: 95},
		{Extra: []string{"http://"}, DiskPct: 90, MemPct: 95, CPUPct: 95},
		{DiskPct: 10, MemPct: 95, CPUPct: 95},
		{DiskPct: 90, MemPct: 95, CPUPct: 95, Email: "not-mail"},
	} {
		if _, err := a.SaveMonitorSettings(bad); err == nil {
			t.Errorf("%+v: no error", bad)
		}
	}
}

func TestMonitorServers(t *testing.T) {
	a := newApp(t)
	ctx := context.Background()
	srv := sshtest.Start(t, "root", "pw")
	sv := addTestServer(t, a, srv, "pw")
	s := defaultMonitorSettings()
	s.Auto = false
	if _, err := a.SaveMonitorSettings(s); err != nil {
		t.Fatal(err)
	}
	a.monitorRound(ctx, true)
	time.Sleep(1100 * time.Millisecond)
	a.monitorRound(ctx, true)
	list, err := a.Store.ServerSamples(sv.ID, "")
	if err != nil || len(list) != 2 {
		t.Fatalf("samples = %+v, %v", list, err)
	}
	m := list[1]
	if !m.OK || m.Mem <= 0 || m.Mem > 100 || m.Disk <= 0 || m.CPU < 0 || m.CPU > 100 {
		t.Fatalf("sample = %+v", m)
	}
	a.mon.mu.Lock()
	kept := a.mon.conns[sv.ID] != nil
	a.mon.mu.Unlock()
	if !kept {
		t.Error("the connection was not kept for the next sample")
	}
	v, _ := a.MonitorPage(ctx)
	if len(v.Servers) != 1 || v.Servers[0].Latest == nil || !v.Servers[0].Sampled || v.Servers[0].Down {
		t.Fatalf("servers = %+v", v.Servers)
	}
	if h, err := a.ServerHistory(sv.ID, 24); err != nil || len(h) != 2 {
		t.Fatalf("history = %+v, %v", h, err)
	}

	// A server that cannot be reached three times in a row is an outage.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	gone, err := a.Store.AddServer(store.Server{Name: "gone", Host: "127.0.0.1", Port: port, Username: "root", AuthKind: "password"})
	if err != nil {
		t.Fatal(err)
	}
	_ = a.Secrets.Set(secretKey(gone.ID, "password"), "pw")
	for i := 0; i < 3; i++ {
		a.monitorRound(ctx, true)
	}
	titles := noticeTitles(a)
	if len(titles) != 1 || titles[0] != "服务器连不上：gone" {
		t.Fatalf("notices = %v", titles)
	}
	v, _ = a.MonitorPage(ctx)
	for _, x := range v.Servers {
		if x.ID == gone.ID && (!x.Down || x.Latest != nil) {
			t.Errorf("gone = %+v", x)
		}
	}
}

func TestParseSample(t *testing.T) {
	out := "0.50 0.40 0.30 1/200 999\n---\n2\n---\ncpu  100 0 100 700 100 0 0 0 0 0\n---\n4000 1000\n---\n47%\n---\n123456 654321\n"
	now := time.Now()
	m, tot, err := parseSample(3, out, now)
	if err != nil {
		t.Fatal(err)
	}
	if m.ServerID != 3 || !m.OK || m.Load1 != 0.5 || m.CPU != 25 || m.Mem != 75 || m.Disk != 47 {
		t.Errorf("sample = %+v", m)
	}
	if tot.total != 1000 || tot.busy != 200 || tot.rx != 123456 || tot.tx != 654321 {
		t.Errorf("totals = %+v", tot)
	}
	if _, _, err := parseSample(3, "garbage", now); err == nil {
		t.Error("garbage parsed")
	}
}
