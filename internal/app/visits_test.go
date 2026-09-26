package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx/sshtest"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent/tencenttest"
	"github.com/bocmiao/CloudConsoleWithAI/internal/visits"
)

const (
	chromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/128 Safari/537.36"
	phoneUA  = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Mobile/15E148 Safari/604.1"
	googleUA = "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"
)

func logLine(t time.Time, ip, req string, status, size int, ref, ua, xff string) string {
	return fmt.Sprintf("%s - - [%s] %q %d %d %q %q %q\n", ip, t.Format("02/Jan/2006:15:04:05 -0700"), req, status, size, ref, ua, xff)
}

// fakeSiteLogs lays out a 1Panel server's website logs under dir: a blog
// whose visitors' IPs are forwarded, a shop whose log only has EdgeOne's
// node, and yesterday's blog log in one of 1Panel's log-cutting archives.
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
	blog := logLine(now, "10.0.0.1", "GET /index.html?x=1 HTTP/1.1", 200, 5000, "https://www.google.com/search?q=a", chromeUA, "61.135.211.75, 10.0.0.1") +
		logLine(now, "10.0.0.1", "GET /style.css HTTP/1.1", 200, 800, "https://blog.example.com/", chromeUA, "61.135.211.75") +
		logLine(now, "10.0.0.1", "GET /about HTTP/1.1", 200, 3000, "https://blog.example.com/index.html", chromeUA, "61.135.211.75") +
		logLine(now, "10.0.0.2", "GET /about HTTP/1.1", 304, 0, "-", phoneUA, "223.5.5.5") +
		logLine(now, "66.249.66.1", "GET / HTTP/1.1", 200, 4000, "-", googleUA, "-") +
		logLine(now, "5.6.7.8", "GET /posts/1 HTTP/1.1", 200, 4000, "-", googleUA, "-") +
		logLine(now, "10.0.0.4", "GET /wp-login.php HTTP/1.1", 404, 100, "-", "curl/8.0", "-") +
		logLine(now, "10.0.0.5", "POST /api/login HTTP/1.1", 500, 100, "-", chromeUA, "8.8.8.8") +
		logLine(now.AddDate(0, 0, -40), "9.9.9.9", "GET / HTTP/1.1", 200, 1, "-", chromeUA, "-") +
		"not a log line\n"
	write(filepath.Join(dir, "1panel/www/sites/blog.example.com/log/access.log"), blog)
	shop := logLine(now, "43.157.9.9", "GET / HTTP/1.1", 200, 1000, "-", chromeUA, "-") +
		logLine(now, "43.157.9.9", "GET /cart HTTP/1.1", 200, 1000, "-", phoneUA, "-")
	write(filepath.Join(dir, "1panel/www/sites/shop.example.com/log/access.log"), shop)

	y := now.AddDate(0, 0, -1)
	old := logLine(y, "10.0.0.1", "GET / HTTP/1.1", 200, 5000, "-", chromeUA, "61.135.211.75") +
		logLine(y, "10.0.0.1", "GET /post/1 HTTP/1.1", 200, 5000, "https://blog.example.com/", chromeUA, "1.1.1.1")
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

// fakeDNS answers reverse lookups for the real Googlebot only.
func fakeDNS(t *testing.T) {
	oldAddr, oldHost := lookupAddr, lookupHost
	t.Cleanup(func() { lookupAddr, lookupHost = oldAddr, oldHost })
	lookupAddr = func(_ context.Context, ip string) ([]string, error) {
		if ip == "66.249.66.1" {
			return []string{"crawl-66-249-66-1.googlebot.com."}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: ip, IsNotFound: true}
	}
	lookupHost = func(_ context.Context, host string) ([]string, error) {
		if host == "crawl-66-249-66-1.googlebot.com" {
			return []string{"66.249.66.1"}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
}

func TestServerVisits(t *testing.T) {
	dir := t.TempDir()
	fakeSiteLogs(t, dir)
	fakeDNS(t)
	t.Setenv("ONEPANEL_CTL", filepath.Join(dir, "1pctl"))
	a := newApp(t)
	f := tencenttest.Start(t)
	f.EdgeOneNodes = map[string]bool{"43.157.9.9": true}
	a.TencentEndpoint = f.Endpoint
	if _, err := a.SaveTencent(tencenttest.SecretID, tencenttest.SecretKey); err != nil {
		t.Fatal(err)
	}
	srv := sshtest.Start(t, "root", "pw")
	sv := addTestServer(t, a, srv, "pw")
	ctx := context.Background()
	source := fmt.Sprintf("server:%d", sv.ID)

	sources, err := a.VisitSources()
	if err != nil || len(sources) != 2 || sources[0].Key != "edgeone" || sources[1].Key != source {
		t.Fatalf("sources = %+v %v", sources, err)
	}
	v, err := a.Visits(ctx, source, false)
	if err != nil {
		t.Fatal(err)
	}
	week := v.Range(7)
	all, blog := week.Sites[visits.All].Total, week.Sites["blog.example.com"].Total
	if blog.PV != 5 || blog.UV != 3 || blog.IP != 3 || blog.Bots != 3 || all.PV != 7 || v.Range(1).Sites["blog.example.com"].Total.PV != 3 {
		t.Fatalf("blog week %+v, all %+v", blog, all)
	}
	byIP := map[string]visits.IPProfile{}
	for _, p := range week.IPs {
		byIP[p.IP] = p
	}
	if p := byIP["66.249.66.1"]; p.Crawler != "Googlebot" || p.Risk != visits.RiskNone {
		t.Fatalf("real Googlebot: %+v", p)
	}
	if p := byIP["5.6.7.8"]; !p.FakeCrawler || p.Risk != visits.RiskMedium {
		t.Fatalf("fake Googlebot: %+v", p)
	}
	if p := byIP["61.135.211.75"]; p.Place != "北京" || p.ISP != "联通" {
		t.Fatalf("placed visitor: %+v", p)
	}
	if p := byIP["43.157.9.9"]; !p.EdgeOne || p.Risk != visits.RiskNone {
		t.Fatalf("EdgeOne node: %+v", p)
	}
	var shopWarning string
	for _, s := range v.Sites {
		if s.Name == "shop.example.com" {
			shopWarning = s.Warning
		}
	}
	if !strings.Contains(shopWarning, "EdgeOne 的节点") {
		t.Fatalf("shop warning = %q", shopWarning)
	}
	if regions := week.Sites[visits.All].Top["region"]; len(regions) == 0 {
		t.Fatal("no regions")
	}
	logs, _ := a.Store.ListExec(false, 10)
	if len(logs) == 0 || logs[0].Title != "统计网站访问日志" || logs[0].ScriptName != "sitelogs.sh" {
		t.Fatalf("exec log = %+v", logs)
	}

	text, err := a.toolSiteVisits(ctx, json.RawMessage(fmt.Sprintf(`{"server_id":%d,"site":"blog","days":7}`, sv.ID)))
	if err != nil || !strings.Contains(text, "blog.example.com合计：PV 5，UV 3，IP 3") || !strings.Contains(text, "冒充爬虫") || !strings.Contains(text, "访客地区") {
		t.Fatalf("tool = %s %v", text, err)
	}
	if _, err := a.toolSiteVisits(ctx, json.RawMessage(fmt.Sprintf(`{"server_id":%d,"site":"nope.org"}`, sv.ID))); err == nil || !strings.Contains(err.Error(), "shop.example.com") {
		t.Fatalf("unknown site: %v", err)
	}
	if _, err := a.toolSiteVisits(ctx, json.RawMessage(fmt.Sprintf(`{"server_id":%d,"days":3}`, sv.ID))); err == nil {
		t.Fatal("3 days accepted")
	}

	// The page gets the last report at once, even after a restart.
	if latest, err := a.LatestVisits(ctx, source); err != nil || latest.Refreshing || latest.CheckedAt != v.CheckedAt {
		t.Fatalf("latest while current = %+v %v", latest.Refreshing, err)
	}
	b := New(a.Store, a.Secrets)
	b.Dial, b.TencentEndpoint = a.Dial, a.TencentEndpoint
	latest, err := b.LatestVisits(ctx, source)
	if err != nil || !latest.Refreshing || latest.Range(7).Sites[visits.All].Total != all {
		t.Fatalf("after restart = %+v %v", latest.Refreshing, err)
	}
	if fresh, err := b.Visits(ctx, source, false); err != nil || fresh.Refreshing || fresh.Range(7).Sites[visits.All].Total != all {
		t.Fatalf("fresh = %+v %v", fresh, err)
	}
	if _, err := a.Visits(ctx, "server:999", false); err == nil {
		t.Fatal("unknown server accepted")
	}
}

func eoRecord(t time.Time, ip, host, method, url, query string, status int, ua, ref string) string {
	data, _ := json.Marshal(map[string]any{
		"RequestTime": t.UTC().Format(time.RFC3339), "ClientIP": ip, "ClientRegion": "CN", "RequestHost": host, "RequestMethod": method,
		"RequestUrl": url, "RequestUrlQueryString": query, "EdgeResponseStatusCode": status, "EdgeResponseBytes": 1000,
		"RequestUA": ua, "RequestReferer": ref, "EdgeCacheStatus": "hit",
	})
	return string(data) + "\n"
}

func TestEdgeOneVisits(t *testing.T) {
	a := newApp(t)
	a.CacheDir = t.TempDir()
	f := tencenttest.Start(t)
	a.TencentEndpoint = f.Endpoint
	if _, err := a.SaveTencent(tencenttest.SecretID, tencenttest.SecretKey); err != nil {
		t.Fatal(err)
	}
	fakeDNS(t)
	now := time.Now()
	hour := now.Truncate(time.Hour).Add(-time.Hour)
	f.L7Logs = map[string][]tencenttest.LogPackage{"zone-abc": {
		{Domain: "blog.example.com", Name: "blog-today.gz", Start: hour, Lines: eoRecord(hour.Add(time.Minute), "61.135.211.75", "blog.example.com", "GET", "/posts/1", "-", 200, chromeUA, "-") +
			eoRecord(hour.Add(2*time.Minute), "61.135.211.75", "blog.example.com", "GET", "/posts/2", "utm=x", 200, chromeUA, "https://www.baidu.com/s") +
			eoRecord(hour.Add(3*time.Minute), "223.5.5.5", "blog.example.com", "GET", "/", "-", 200, phoneUA, "-") +
			eoRecord(hour.Add(4*time.Minute), "45.148.10.2", "blog.example.com", "GET", "/search", "q=1 union select 2", 404, chromeUA, "-") +
			"{broken json\n"},
		{Domain: "blog.example.com", Name: "blog-last-week.gz", Start: hour.AddDate(0, 0, -5), Lines: eoRecord(hour.AddDate(0, 0, -5), "1.1.1.1", "blog.example.com", "GET", "/", "-", 200, chromeUA, "-")},
		{Domain: "blog.example.com", Name: "too-old.gz", Start: hour.AddDate(0, 0, -40), Lines: eoRecord(hour.AddDate(0, 0, -40), "9.9.9.9", "blog.example.com", "GET", "/", "-", 200, chromeUA, "-")},
	}}
	ctx := context.Background()
	v, err := a.Visits(ctx, "edgeone", false)
	if err != nil {
		t.Fatal(err)
	}
	today, month := v.Range(1).Sites["blog.example.com"], v.Range(30).Sites["blog.example.com"]
	if v.Source != "edgeone" || today.Total.PV != 3 || today.Total.UV != 2 || month.Total.PV != 4 || month.Total.IP != 3 {
		t.Fatalf("today %+v month %+v", today.Total, month.Total)
	}
	if got := today.Top["region"]; len(got) != 2 {
		t.Fatalf("regions = %+v", got)
	}
	var scanner visits.IPProfile
	for _, p := range v.Range(1).IPs {
		if p.IP == "45.148.10.2" {
			scanner = p
		}
	}
	if scanner.Inject != 1 || scanner.Risk != visits.RiskHigh {
		t.Fatalf("scanner = %+v", scanner)
	}
	var info visits.SiteInfo
	for _, s := range v.Sites {
		if s.Name == "blog.example.com" {
			info = s
		}
	}
	if info.Unparsed != 1 || info.Warning != "" {
		t.Fatalf("site info = %+v", info)
	}
	for _, c := range f.Calls {
		if c == "teo DescribeIPRegion" {
			t.Fatal("EdgeOne's own logs need no node check")
		}
	}
	// Downloaded packages are kept: counting again fetches nothing.
	gets := func() int {
		n := 0
		for _, c := range f.Calls {
			if strings.HasPrefix(c, "GET ") {
				n++
			}
		}
		return n
	}
	before := gets()
	if _, err := a.Visits(ctx, "edgeone", true); err != nil || gets() != before || before != 2 {
		t.Fatalf("downloads: %d then %d (%v)", before, gets(), err)
	}
}

func TestKeepWarm(t *testing.T) {
	dir := t.TempDir()
	fakeSiteLogs(t, dir)
	fakeDNS(t)
	t.Setenv("ONEPANEL_CTL", filepath.Join(dir, "1pctl"))
	a := newApp(t)
	srv := sshtest.Start(t, "root", "pw")
	sv := addTestServer(t, a, srv, "pw")
	source := fmt.Sprintf("server:%d", sv.ID)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go a.KeepWarm(ctx, time.Hour)
	// Wait for the background count, without asking for the page (which
	// would start a count of its own).
	deadline := time.Now().Add(20 * time.Second)
	for {
		a.visits.mu.Lock()
		e := a.visits.entries[visitsKey(source)]
		ready := e != nil && !e.at.IsZero()
		a.visits.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("not warmed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	v, err := a.LatestVisits(context.Background(), source)
	if err != nil || v.Refreshing || v.Range(7).Sites[visits.All].Total.PV == 0 {
		t.Fatalf("page after warming: refreshing=%v %v", v.Refreshing, err)
	}
	logs, _ := a.Store.ListExec(false, 10)
	if len(logs) != 1 || logs[0].Origin != OriginAuto {
		t.Fatalf("background run logged as %+v", logs)
	}
}

func TestJudgeAndBlock(t *testing.T) {
	a := newApp(t)
	a.CacheDir = t.TempDir()
	f := tencenttest.Start(t)
	a.TencentEndpoint = f.Endpoint
	if _, err := a.SaveTencent(tencenttest.SecretID, tencenttest.SecretKey); err != nil {
		t.Fatal(err)
	}
	fakeDNS(t)
	hour := time.Now().Truncate(time.Hour).Add(-time.Hour)
	var lines string
	for i, p := range []string{"/.env", "/.git/config", "/wp-login.php", "/phpmyadmin/"} {
		lines += eoRecord(hour.Add(time.Duration(i)*time.Second), "45.148.10.2", "blog.example.com", "GET", p, "-", 404, "Mozilla/5.0 zgrab/0.x", "-")
	}
	lines += eoRecord(hour, "66.249.66.1", "blog.example.com", "GET", "/", "-", 200, googleUA, "-") +
		eoRecord(hour, "61.135.211.75", "blog.example.com", "GET", "/", "-", 200, chromeUA, "-")
	f.L7Logs = map[string][]tencenttest.LogPackage{"zone-abc": {{Domain: "blog.example.com", Name: "p1.gz", Start: hour, Lines: lines}}}
	ctx := context.Background()

	var asked string
	a.Analyst = func(_ context.Context, prompt string) (string, error) {
		asked = prompt
		return `结论如下：{"summary": "有一个扫描器", "ips": [
			{"ip": "45.148.10.2", "action": "block", "reason": "4 次探测 /.env、/.git 等"},
			{"ip": "66.249.66.1", "action": "block", "reason": "爬得太多"},
			{"ip": "9.9.9.9", "action": "block", "reason": "not asked"}]}`, nil
	}
	j, err := a.JudgeIPs(ctx, "edgeone", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(asked, "45.148.10.2") || strings.Contains(asked, "61.135.211.75") {
		t.Fatalf("asked about the wrong IPs:\n%s", asked)
	}
	if j.Summary != "有一个扫描器" || len(j.Verdicts) != 2 || j.Verdicts[0].Action != "block" || j.Verdicts[1].Action != "ignore" {
		t.Fatalf("judgement = %+v", j)
	}

	plan, err := a.ProposeBlock(ctx, "edgeone", []string{"45.148.10.2", "66.249.66.1", "10.0.0.1", "nonsense"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.StepList) != 1 || plan.StepList[0].Capability != "eo.ip.block" || plan.StepList[0].Params["ips"] != "45.148.10.2" ||
		plan.StepList[0].Params["domain"] != "example.com" || !plan.StepList[0].Executable ||
		!strings.Contains(plan.Reason, "Googlebot") || !strings.Contains(plan.Reason, "内网地址") || !strings.Contains(plan.Reason, "探测后台") {
		t.Fatalf("plan = %+v / %s", plan.StepList, plan.Reason)
	}
	if _, err := a.ProposeBlock(ctx, "edgeone", []string{"66.249.66.1"}); err == nil || !strings.Contains(err.Error(), "没有可以封禁的 IP") {
		t.Fatalf("only a crawler: %v", err)
	}
	// Nothing is blocked when EdgeOne cannot say which IPs are its nodes.
	f.FailAction = "teo DescribeIPRegion"
	if _, err := a.ProposeBlock(ctx, "edgeone", []string{"45.148.10.9"}); err == nil || !strings.Contains(err.Error(), "没能向 EdgeOne 核对") {
		t.Fatalf("unchecked block: %v", err)
	}
	f.FailAction = ""
	f.EdgeOneNodes = map[string]bool{"45.148.10.9": true}
	if _, err := a.ProposeBlock(ctx, "edgeone", []string{"45.148.10.9"}); err == nil || !strings.Contains(err.Error(), "EdgeOne 的节点") {
		t.Fatalf("EdgeOne node blocked: %v", err)
	}
	if _, err := a.ExecutePlan(plan.ID, []int{0}); err != nil {
		t.Fatal(err)
	}
	if st := waitPlan(t, a, plan.ID).StepList[0]; st.Status != "done" {
		t.Fatalf("block step = %+v", st)
	}
	blocked, err := a.Blocked(ctx)
	if err != nil || len(blocked) != 1 || blocked[0].Zone != "example.com" || strings.Join(blocked[0].IPs, ",") != "45.148.10.2" {
		t.Fatalf("blocked = %+v %v", blocked, err)
	}
	un, err := a.ProposeUnblock(ctx, "example.com", []string{"45.148.10.2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ExecutePlan(un.ID, []int{0}); err != nil {
		t.Fatal(err)
	}
	waitPlan(t, a, un.ID)
	if blocked, _ := a.Blocked(ctx); len(blocked) != 0 {
		t.Fatalf("still blocked: %+v", blocked)
	}
}

func TestAutoBlock(t *testing.T) {
	a := newApp(t)
	a.CacheDir = t.TempDir()
	f := tencenttest.Start(t)
	a.TencentEndpoint = f.Endpoint
	if _, err := a.SaveTencent(tencenttest.SecretID, tencenttest.SecretKey); err != nil {
		t.Fatal(err)
	}
	fakeDNS(t)
	hour := time.Now().Truncate(time.Hour).Add(-time.Hour)
	var lines string
	// A scanner (blocked), an attacker the AI only wants watched, one on the
	// allow list, and a verified crawler.
	for i := 0; i < 35; i++ {
		lines += eoRecord(hour.Add(time.Duration(i)*time.Second), "45.148.10.2", "blog.example.com", "GET", []string{"/.env", "/.git/config", "/wp-login.php"}[i%3], "-", 404, "Mozilla/5.0 zgrab/0.x", "-")
	}
	lines += eoRecord(hour, "185.220.101.47", "blog.example.com", "GET", "/search", "q=1 union select 2", 400, chromeUA, "-") +
		eoRecord(hour, "91.234.56.12", "blog.example.com", "GET", "/search", "q=1 union select 2", 400, chromeUA, "-") +
		eoRecord(hour, "66.249.66.1", "blog.example.com", "GET", "/", "-", 200, googleUA, "-")
	f.L7Logs = map[string][]tencenttest.LogPackage{"zone-abc": {{Domain: "blog.example.com", Name: "p1.gz", Start: hour, Lines: lines}}}
	ctx := context.Background()
	if _, err := a.Visits(ctx, "edgeone", false); err != nil {
		t.Fatal(err)
	}

	if note := a.RunAutoBlock(ctx); note != "自动封禁没有开启" {
		t.Fatalf("off: %q", note)
	}
	if _, err := a.SaveAutoBlock(AutoBlockSettings{Enabled: true, Level: "high", RequireAI: true, Hours: 24, Allow: []string{"bad"}}); err == nil {
		t.Fatal("accepted a bad allow list")
	}
	if _, err := a.SaveAutoBlock(AutoBlockSettings{Enabled: true, Level: "high", RequireAI: true, Hours: 24, Allow: []string{"91.234.56.0/24"}}); err != nil {
		t.Fatal(err)
	}
	asked := 0
	a.Analyst = func(_ context.Context, prompt string) (string, error) {
		asked++
		if strings.Contains(prompt, "91.234.56.12") || strings.Contains(prompt, "66.249.66.1") {
			t.Errorf("asked about an allowed IP or a crawler:\n%s", prompt)
		}
		return `{"summary": "x", "ips": [{"ip": "45.148.10.2", "action": "block", "reason": "扫描"}, {"ip": "185.220.101.47", "action": "watch", "reason": "只有一次"}]}`, nil
	}
	note := a.RunAutoBlock(ctx)
	blocked, _ := a.Blocked(ctx)
	if !strings.Contains(note, "封禁了 1 个 IP") || len(blocked) != 1 || strings.Join(blocked[0].IPs, ",") != "45.148.10.2" || asked != 1 {
		t.Fatalf("first run: %q, blocked %+v, asked %d", note, blocked, asked)
	}
	st := a.AutoBlock()
	if len(st.Blocked) != 1 || st.Blocked[0].IP != "45.148.10.2" || st.Blocked[0].Zone != "example.com" || st.Blocked[0].Until == "" ||
		!strings.Contains(st.Blocked[0].Reason, "AI：扫描") || st.LastNote != note {
		t.Fatalf("state = %+v", st)
	}
	if plans, _ := a.Store.ListPlans(10); len(plans) == 0 || !strings.HasPrefix(plans[0].Title, "自动封禁 1 个高风险 IP") {
		t.Fatalf("plans = %+v", plans)
	}

	// Nothing new: no checklist, and the AI is not asked again today.
	if note := a.RunAutoBlock(ctx); note != "没有需要封禁的 IP" || asked != 1 {
		t.Fatalf("second run: %q, asked %d", note, asked)
	}

	// The block runs out and is lifted.
	autoBlockMu.Lock()
	s := a.loadAutoBlock()
	s.Blocked[0].Until = time.Now().UTC().Add(-time.Minute).Format(autoBlockUntil)
	_ = a.saveAutoBlock(s)
	autoBlockMu.Unlock()
	note = a.RunAutoBlock(ctx)
	if blocked, _ := a.Blocked(ctx); len(blocked) != 0 || !strings.Contains(note, "到期解封 1 个 IP") || len(a.AutoBlock().Blocked) != 0 {
		t.Fatalf("expiry: %q, blocked %+v", note, blocked)
	}

	if note := a.RunAutoBlock(ctx); note != "没有需要封禁的 IP" {
		t.Fatalf("blocked again with nothing new since the block ran out: %q", note)
	}

	// It comes back (seen after the block ran out), is blocked again, then
	// the user takes it over: kept for good.
	autoBlockMu.Lock()
	s = a.loadAutoBlock()
	s.Lifted["45.148.10.2"] = "2000-01-01 00:00:00"
	_ = a.saveAutoBlock(s)
	autoBlockMu.Unlock()
	a.RunAutoBlock(ctx)
	if len(a.AutoBlock().Blocked) != 1 {
		t.Fatal("not blocked again")
	}
	if _, err := a.ProposeBlock(ctx, "edgeone", []string{"45.148.10.2"}); err != nil {
		t.Fatal(err)
	}
	if st := a.AutoBlock(); len(st.Blocked) != 0 {
		t.Fatalf("still the rule's: %+v", st.Blocked)
	}
}

func TestCrawlerChecks(t *testing.T) {
	oldAddr, oldHost := lookupAddr, lookupHost
	t.Cleanup(func() { lookupAddr, lookupHost = oldAddr, oldHost })
	ptr := map[string]string{
		"66.249.79.205": "crawl-66-249-79-205.googlebot.com.", // real, but DNS answers are forged
		"5.6.7.10":      "crawl-5-6-7-10.googlebot.com.",      // its name, not its address
		"5.6.7.9":       "9.7.6.5.bc.googleusercontent.com.",  // a Google Cloud machine
	}
	lookupAddr = func(_ context.Context, ip string) ([]string, error) {
		if n, ok := ptr[ip]; ok {
			return []string{n}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: ip, IsNotFound: true}
	}
	lookupHost = func(_ context.Context, host string) ([]string, error) {
		if host == "9.7.6.5.bc.googleusercontent.com" {
			return []string{"5.6.7.9"}, nil
		}
		return []string{"31.13.64.1"}, nil // what a polluted resolver says
	}
	a := newApp(t)
	gb := "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"
	got := a.checkCrawlers(context.Background(), map[string]string{
		"66.249.79.205":        gb,
		"8.231.157.220":        gb, // Google's network, outside the crawler ranges, no reverse name
		"5.6.7.10":             gb,
		"5.6.7.9":              gb,
		"5.6.7.8":              gb,
		"157.55.39.1":          "Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)",
		"2001:4860:4801:10::1": gb,
	})
	want := map[string]crawlerCheck{
		"66.249.79.205": {name: "Googlebot"}, "2001:4860:4801:10::1": {name: "Googlebot"}, "157.55.39.1": {name: "Bingbot"},
		"5.6.7.9": {fake: true}, "5.6.7.8": {fake: true},
	}
	for ip, w := range want {
		if got[ip] != w {
			t.Errorf("%s = %+v, want %+v", ip, got[ip], w)
		}
	}
	for _, ip := range []string{"8.231.157.220", "5.6.7.10"} { // cannot tell: neither verified nor fake
		if c, ok := got[ip]; ok {
			t.Errorf("%s = %+v, want unknown", ip, c)
		}
	}
}
