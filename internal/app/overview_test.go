package app

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/profile"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/internal/visits"
)

func TestOverview(t *testing.T) {
	a := newApp(t)
	ctx := context.Background()
	fresh, err := a.Store.AddServer(store.Server{Name: "new", Host: "10.0.0.2", Port: 22, Username: "root", AuthKind: "password"})
	if err != nil {
		t.Fatal(err)
	}
	blog, _ := a.Store.AddServer(store.Server{Name: "blog", Host: "10.0.0.1", Port: 22, Username: "root", AuthKind: "password"})
	raw, err := os.ReadFile("../profile/testdata/1panel.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Store.SaveProfile(blog.ID, string(raw), "1panel"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Store.AddPlan(store.Plan{ServerID: blog.ID, Title: "加 swap", Steps: "[]", Status: core.PlanProposed}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Store.AddPlan(store.Plan{ServerID: blog.ID, Title: "已经执行的", Steps: "[]", Status: core.PlanDone}); err != nil {
		t.Fatal(err)
	}

	v, err := a.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Servers) != 2 || v.Pending != 1 {
		t.Fatalf("overview %+v", v)
	}
	for _, s := range v.Servers {
		switch s.ID {
		case fresh.ID:
			if s.Level != "unknown" || s.Note != "还没有识别环境" {
				t.Fatalf("a server never profiled: %+v", s)
			}
		case blog.ID:
			p := profile.Parse(string(raw))
			if s.MemPct == nil || s.DiskPct == nil || s.Level == "unknown" || (len(p.Findings) > 0 && s.Level == "ok" && s.Note == "") {
				t.Fatalf("profiled server: %+v", s)
			}
		}
	}
	var sawPlan bool
	for _, it := range v.Todo {
		sawPlan = sawPlan || (it.Kind == "plan" && strings.Contains(it.Title, "加 swap"))
		if strings.Contains(it.Title, "已经执行的") {
			t.Fatal("a checklist already run is not waiting")
		}
	}
	if !sawPlan || v.Visits != nil || v.Certs != nil {
		t.Fatalf("todo %+v", v)
	}
}

func TestVisitsOverviewHours(t *testing.T) {
	today := "2020-01-02"
	v := VisitsView{}
	v.Today = today
	v.Days = map[string][]visits.Day{visits.All: {{Date: "2020-01-01", Counts: visits.Counts{PV: 2400}}, {Date: today, Counts: visits.Counts{PV: 300}}}}
	v.Hours = map[string][]visits.Hour{visits.All: {{Hour: 0, PV: 100}, {Hour: 2, PV: 200}, {Hour: 3, PV: 0}}}
	o := visitsOverview(v)
	// Not today any more: every hour listed counts, and yesterday is
	// compared up to the last one.
	if o.PV != 300 || len(o.Hours) != 4 || o.Hours[1] != 0 || o.PVBefore != 2400*4/24 {
		t.Fatalf("overview %+v", o)
	}
	if visitsOverview(VisitsView{}) != nil {
		t.Fatal("no data should give nothing")
	}
}

func TestPageNote(t *testing.T) {
	if got := pageNote("  网站 blog.example.com\n（服务器 blog）  · HTTPS "); got != "网站 blog.example.com （服务器 blog） · HTTPS" {
		t.Fatalf("note %q", got)
	}
	if got := pageNote(strings.Repeat("长", 300)); len([]rune(got)) != 120 {
		t.Fatalf("long note kept %d runes", len([]rune(got)))
	}
}

// The 总览 counts certificates as the 证书 page does.
func TestCertsOverview(t *testing.T) {
	d := func(n int) *int { return &n }
	c := CertOverview{
		Groups: []CertGroup{
			// In use and fine, plus an old copy nobody uses.
			{Domain: "a.example.net", Level: "ok", Certs: []Cert{
				{InUse: true, Level: "ok", DaysLeft: d(80)},
				{Level: "crit", DaysLeft: d(-30)},
			}},
			// In use and expiring soon without renewal.
			{Domain: "b.example.net", Level: "warn", Certs: []Cert{
				{InUse: true, Level: "warn", DaysLeft: d(8), UsedBy: []string{"b.example.net"}},
			}},
			// Nothing in use; its failed request still needs a look.
			{Domain: "c.example.net", Level: "crit", Certs: []Cert{{Level: "crit"}}},
		},
		Live: []LiveCert{
			{Domain: "b.example.net", Level: "warn"},                  // explained by its certificate
			{Domain: "d.example.net", Level: "crit", DaysLeft: d(-2)}, // only seen when visited
			{Domain: "e.example.net", Level: "ok"},
		},
	}
	o := certsOverview(c)
	if o.Total != 2 || o.Attention != 3 || o.Level != "crit" || o.Soonest != "d.example.net" || o.Days == nil || *o.Days != -2 {
		t.Fatalf("certs overview: %+v", o)
	}
	if o := certsOverview(CertOverview{}); o.Total != 0 || o.Attention != 0 || o.Level != "ok" {
		t.Fatalf("empty: %+v", o)
	}
}
