package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx/sshtest"
	"github.com/bocmiao/CloudConsoleWithAI/internal/visits"
)

const (
	chromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/128 Safari/537.36"
	phoneUA  = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Mobile/15E148 Safari/604.1"
)

func logLine(t time.Time, ip, req string, status, size int, ref, ua, xff string) string {
	return fmt.Sprintf("%s - - [%s] %q %d %d %q %q %q\n", ip, t.Format("02/Jan/2006:15:04:05 -0700"), req, status, size, ref, ua, xff)
}

// fakeSiteLogs lays out a 1Panel server's website logs under dir: two
// sites, and yesterday's log in one of 1Panel's log-cutting archives.
func fakeSiteLogs(t *testing.T, dir string) {
	t.Helper()
	now := time.Now()
	write := func(path, data string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "1pctl"), "BASE_DIR="+dir+"\n")
	blog := logLine(now, "10.0.0.1", "GET /index.html?x=1 HTTP/1.1", 200, 5000, "https://www.google.com/search?q=a", chromeUA, "1.1.1.1, 10.0.0.1") +
		logLine(now, "10.0.0.1", "GET /style.css HTTP/1.1", 200, 800, "https://blog.example.com/", chromeUA, "1.1.1.1") +
		logLine(now, "10.0.0.1", "GET /about HTTP/1.1", 200, 3000, "https://blog.example.com/index.html", chromeUA, "1.1.1.1") +
		logLine(now, "10.0.0.2", "GET /about HTTP/1.1", 304, 0, "-", phoneUA, "2.2.2.2") +
		logLine(now, "10.0.0.3", "GET / HTTP/1.1", 200, 4000, "-", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", "66.249.1.1") +
		logLine(now, "10.0.0.4", "GET /wp-login.php HTTP/1.1", 404, 100, "-", "curl/8.0", "-") +
		logLine(now, "10.0.0.5", "POST /api/login HTTP/1.1", 500, 100, "-", chromeUA, "3.3.3.3") +
		logLine(now.AddDate(0, 0, -40), "9.9.9.9", "GET / HTTP/1.1", 200, 1, "-", chromeUA, "-") +
		"not a log line\n"
	write(filepath.Join(dir, "1panel/www/sites/blog.example.com/log/access.log"), blog)
	write(filepath.Join(dir, "1panel/www/sites/shop.example.com/log/access.log"), logLine(now, "1.1.1.1", "GET / HTTP/1.1", 200, 1000, "-", chromeUA, "-"))

	y := now.AddDate(0, 0, -1)
	old := logLine(y, "10.0.0.1", "GET / HTTP/1.1", 200, 5000, "-", chromeUA, "1.1.1.1") +
		logLine(y, "10.0.0.1", "GET /post/1 HTTP/1.1", 200, 5000, "https://blog.example.com/", chromeUA, "4.4.4.4")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range map[string]string{"access.log": old, "error.log": ""} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))})
		_, _ = tw.Write([]byte(data))
	}
	_ = tw.Close()
	_ = gz.Close()
	write(filepath.Join(dir, "1panel/backup/log/website/blog.example.com/blog.example.com_log_20260101000000.gz"), buf.String())
}

func TestSiteVisits(t *testing.T) {
	dir := t.TempDir()
	fakeSiteLogs(t, dir)
	t.Setenv("ONEPANEL_CTL", filepath.Join(dir, "1pctl"))
	a := newApp(t)
	srv := sshtest.Start(t, "root", "pw")
	sv := addTestServer(t, a, srv, "pw")
	ctx := context.Background()

	v, err := a.SiteVisits(ctx, sv.ID, 7, false)
	if err != nil {
		t.Fatal(err)
	}
	all, _ := v.Site(visits.All)
	blog, _ := v.Site("blog.example.com")
	want := visits.Counts{Requests: 10, PV: 6, UV: 3, IP: 3, Bots: 2, Bytes: 24000, E4xx: 1, E5xx: 1}
	if all.Total != want {
		t.Fatalf("all sites = %+v, want %+v", all.Total, want)
	}
	if blog.Total.PV != 5 || blog.Total.UV != 3 || blog.Total.IP != 3 || blog.Lines != 11 || blog.Unparsed != 1 || len(blog.Files) != 2 {
		t.Fatalf("blog = %+v", blog)
	}
	today := blog.Days[len(blog.Days)-1]
	yesterday := blog.Days[len(blog.Days)-2]
	if len(blog.Days) != 7 || today.Date != v.Today || today.PV != 3 || today.UV != 2 || yesterday.PV != 2 || len(blog.Hours) != 24 {
		t.Fatalf("days = %+v", blog.Days)
	}
	top := func(s visits.Site, kind string) string {
		var parts []string
		for _, it := range s.Top[kind] {
			parts = append(parts, fmt.Sprintf("%s=%d", it.Value, it.Count))
		}
		return strings.Join(parts, " ")
	}
	if got := top(blog, "page"); got != "/about=2 /=1 /index.html=1 /post/1=1" {
		t.Fatalf("pages = %s", got)
	}
	if got := top(blog, "bot"); !strings.Contains(got, "Googlebot=1") || !strings.Contains(got, "curl=1") {
		t.Fatalf("bots = %s", got)
	}
	if got := top(all, "page"); !strings.Contains(got, "shop.example.com/=1") {
		t.Fatalf("all pages = %s", got)
	}
	if v.Sites[0].Name != visits.All || v.Sites[1].Name != "blog.example.com" {
		t.Fatalf("order = %s, %s", v.Sites[0].Name, v.Sites[1].Name)
	}
	logs, _ := a.Store.ListExec(false, 10)
	if len(logs) == 0 || logs[0].Title != "统计网站访问日志（7 天）" || logs[0].ScriptName != "sitelogs.sh" {
		t.Fatalf("exec log = %+v", logs)
	}

	text, err := a.toolSiteVisits(ctx, json.RawMessage(fmt.Sprintf(`{"server_id":%d,"site":"blog"}`, sv.ID)))
	if err != nil || !strings.Contains(text, "blog.example.com合计：PV 5，UV 3，IP 3") || !strings.Contains(text, "受访页面：/about 2") {
		t.Fatalf("tool = %s %v", text, err)
	}
	if _, err := a.toolSiteVisits(ctx, json.RawMessage(fmt.Sprintf(`{"server_id":%d,"site":"nope.org"}`, sv.ID))); err == nil || !strings.Contains(err.Error(), "shop.example.com") {
		t.Fatalf("unknown site: %v", err)
	}
	if one, err := a.SiteVisits(ctx, sv.ID, 1, false); err != nil || len(one.Sites[0].Days) != 1 || one.Sites[0].Total.PV != 4 {
		t.Fatalf("today only = %+v %v", one, err)
	}
	if _, err := a.SiteVisits(ctx, sv.ID, 40, false); err == nil {
		t.Fatal("40 days accepted")
	}

	// The page gets the last report at once, even after a restart.
	if latest, err := a.LatestSiteVisits(ctx, sv.ID, 7); err != nil || latest.Refreshing || latest.CheckedAt != v.CheckedAt {
		t.Fatalf("latest while current = %+v %v", latest.Refreshing, err)
	}
	b := New(a.Store, a.Secrets)
	b.Dial = a.Dial
	latest, err := b.LatestSiteVisits(ctx, sv.ID, 7)
	if err != nil || !latest.Refreshing || latest.Sites[0].Total != want {
		t.Fatalf("after restart = %+v %v", latest.Refreshing, err)
	}
	fresh, err := b.SiteVisits(ctx, sv.ID, 7, false)
	if err != nil || fresh.Refreshing || fresh.Sites[0].Total != want {
		t.Fatalf("fresh = %+v %v", fresh, err)
	}
}
