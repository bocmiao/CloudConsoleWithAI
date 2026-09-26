package visits

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestParseProblem(t *testing.T) {
	r := Parse("N\t2026-09-26\t30\t+0800\nE\t没有找到网站访问日志\n")
	if r.Problem == "" || r.Today != "2026-09-26" || r.Range(7) == nil {
		t.Fatalf("no logs: %+v", r)
	}
}

// The script marks each regex with "# rule <name>"; they must be the ones
// the Go counter uses.
func TestScriptRulesMatch(t *testing.T) {
	script, err := os.ReadFile("../../scripts/sitelogs.sh")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"bot": botPattern, "botName": botNamePattern, "static": staticPattern, "mobile": mobilePattern,
		"sensitive": sensitivePattern, "secret": secretPattern, "inject": injectPattern, "login": loginPattern, "wordpress": wordpressPattern,
	}
	lit := regexp.MustCompile(`(?:~|match\(ua,)\s*/((?:[^/\\]|\\.)+)/`)
	seen := map[string]bool{}
	for _, line := range strings.Split(string(script), "\n") {
		_, names, ok := strings.Cut(line, "# rule ")
		if !ok {
			continue
		}
		found := lit.FindAllStringSubmatch(line, -1)
		list := strings.Fields(names)
		if len(found) != len(list) {
			t.Fatalf("rules %v: found %d regexes in %q", list, len(found), line)
		}
		for i, name := range list {
			got := strings.ReplaceAll(found[i][1], `\/`, `/`)
			if got != want[name] {
				t.Errorf("rule %s differs:\n script %s\n go     %s", name, got, want[name])
			}
			seen[name] = true
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("rule %s not marked in the script", name)
		}
	}
}

func line(t time.Time, ip, req string, status, size int, ref, ua, xff string) string {
	return fmt.Sprintf("%s - - [%s] %q %d %d %q %q %q\n", ip, t.Format("02/Jan/2006:15:04:05 -0700"), req, status, size, ref, ua, xff)
}

const (
	chrome = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/128 Safari/537.36"
	phone  = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Mobile/15E148 Safari/604.1"
)

// fixture writes two sites' logs in a 1Panel layout and returns them by
// site, in the order the script reads them.
func fixture(t *testing.T, dir string) map[string][]string {
	now := time.Now().Truncate(time.Second)
	at := func(daysAgo int, clock string) time.Time {
		d := now.AddDate(0, 0, -daysAgo)
		c, _ := time.ParseInLocation("15:04:05", clock, time.Local)
		return time.Date(d.Year(), d.Month(), d.Day(), c.Hour(), c.Minute(), c.Second(), 0, time.Local)
	}
	var blog []string
	// A visitor behind EdgeOne, reading pages, on a phone and a computer.
	for i, p := range []string{"/", "/posts/a?ref=x", "/posts/b", "/about", "/posts/a"} {
		blog = append(blog, line(at(0, fmt.Sprintf("08:0%d:10", i)), "43.157.1.1", "GET "+p+" HTTP/1.1", 200, 5000, "https://www.baidu.com/s?wd=a", chrome, "1.1.1.1, 43.157.1.1"))
	}
	blog = append(blog,
		line(at(0, "08:05:00"), "43.157.1.1", "GET /assets/app.css HTTP/1.1", 200, 900, "https://blog.example.com/", chrome, "1.1.1.1"),
		line(at(0, "08:06:00"), "43.157.1.1", "GET /api/list HTTP/1.1", 200, 300, "https://blog.example.com/", chrome, "1.1.1.1"),
		line(at(0, "09:00:00"), "43.157.1.2", "GET /about HTTP/1.1", 304, 0, "-", phone, "2.2.2.2"),
		line(at(0, "09:10:00"), "66.249.1.1", "GET / HTTP/1.1", 200, 4000, "-", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", "-"),
		line(at(0, "09:11:00"), "5.5.5.5", "GET /x HTTP/1.1", 200, 10, "-", "-", "-"),
		line(at(0, "09:12:00"), "6.6.6.6", "GET /y HTTP/1.1", 200, 10, "-", "python-requests/2.31", "-"),
		// The site's own secrets leaking.
		line(at(0, "09:13:00"), "7.7.7.7", "GET /.env HTTP/1.1", 200, 120, "-", chrome, "-"),
		line(at(0, "10:00:00"), "2602:80d:1005::18", "GET /posts/b HTTP/1.1", 200, 800, "https://t.co/x", phone, "-"),
		"garbage without a bracket\n",
		line(at(40, "10:00:00"), "9.9.9.9", "GET /old HTTP/1.1", 200, 1, "-", chrome, "-"),
		line(at(10, "11:00:00"), "8.8.8.8", "GET /posts/c HTTP/1.1", 200, 100, "-", chrome, "-"),
	)
	// A scanner: probes, payloads, many 404s in one minute.
	for i, p := range []string{"/.env", "/.git/config", "/wp-login.php", "/phpmyadmin/", "/admin/../../etc/passwd", "/search?q=1%20union%20select%201", "/cgi-bin/luci", "/backup.sql"} {
		blog = append(blog, line(at(0, fmt.Sprintf("11:30:%02d", i)), "45.148.10.2", "GET "+p+" HTTP/1.1", 404, 150, "-", "Mozilla/5.0 zgrab/0.x", "-"))
	}
	for i := 0; i < 70; i++ {
		blog = append(blog, line(at(0, fmt.Sprintf("11:31:%02d", i%60)), "45.148.10.2", fmt.Sprintf("GET /probe/%d HTTP/1.1", i), 404, 150, "-", "Mozilla/5.0", "-"))
	}
	// Someone guessing WordPress passwords, and a refused API login.
	for i := 0; i < 6; i++ {
		blog = append(blog, line(at(0, fmt.Sprintf("12:00:%02d", i)), "3.3.3.3", "POST /wp-login.php HTTP/1.1", 200, 3000, "-", chrome, "-"))
	}
	blog = append(blog, line(at(0, "12:01:00"), "3.3.3.4", "POST /api/login HTTP/1.1", 401, 50, "-", chrome, "-"),
		line(at(0, "12:02:00"), "3.3.3.5", "GET /search?id=1%20and%20sleep(5) HTTP/1.1", 400, 50, "-", "sqlmap/1.8.3#stable (https://sqlmap.org)", "-"),
		// Dead links: a link on the site's own page, and one elsewhere;
		// a crawler, a probe and a POST with a referer are not.
		line(at(0, "12:05:00"), "12.12.12.12", "GET /posts/gone HTTP/1.1", 404, 90, "https://blog.example.com/about?from=menu#top", chrome, "-"),
		line(at(0, "12:05:30"), "12.12.12.13", "GET /old-guide?utm=1 HTTP/1.1", 410, 90, "https://www.zhihu.com/question/1", phone, "-"),
		line(at(0, "12:06:00"), "66.249.1.1", "GET /gone-too HTTP/1.1", 404, 90, "https://blog.example.com/", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", "-"),
		line(at(0, "12:06:10"), "12.12.12.14", "GET /.git/config HTTP/1.1", 404, 90, "https://blog.example.com/", chrome, "-"),
		line(at(0, "12:06:20"), "12.12.12.15", "POST /form HTTP/1.1", 404, 90, "https://blog.example.com/contact", chrome, "-"),
		line(at(0, "12:06:30"), "12.12.12.16", "GET /x HTTP/1.1", 404, 90, "blog.example.com", chrome, "-"))

	shop := []string{
		line(at(0, "13:00:00"), "1.1.1.1", "GET / HTTP/1.1", 200, 1000, "-", chrome, "-"),
		line(at(0, "13:01:00"), "4.4.4.4", "GET /cart HTTP/1.1", 500, 100, "https://shop.example.com/", chrome, "-"),
	}
	archived := []string{
		line(at(1, "20:00:00"), "10.0.0.1", "GET / HTTP/1.1", 200, 5000, "-", chrome, "1.1.1.1"),
		line(at(1, "20:01:00"), "10.0.0.1", "GET /posts/d HTTP/1.1", 200, 5000, "https://blog.example.com/", chrome, "4.4.4.4"),
		line(at(3, "20:02:00"), "10.0.0.1", "GET /posts/e HTTP/1.1", 200, 5000, "https://www.google.com/", phone, "4.4.4.5"),
	}

	write := func(path, data string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "1pctl"), "BASE_DIR="+dir+"\n")
	write(filepath.Join(dir, "1panel/www/sites/blog.example.com/log/access.log"), strings.Join(blog, ""))
	write(filepath.Join(dir, "1panel/www/sites/shop.example.com/log/access.log"), strings.Join(shop, ""))
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	data := strings.Join(archived, "")
	_ = tw.WriteHeader(&tar.Header{Name: "access.log", Mode: 0o644, Size: int64(len(data))})
	_, _ = tw.Write([]byte(data))
	_ = tw.Close()
	_ = gz.Close()
	write(filepath.Join(dir, "1panel/backup/log/website/blog.example.com/blog.example.com_log_20260101000000.gz"), buf.String())
	return map[string][]string{"blog.example.com": append(blog, archived...), "shop.example.com": shop}
}

func runScript(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("sh", "../../scripts/sitelogs.sh")
	cmd.Env = append(os.Environ(), "ONEPANEL_CTL="+filepath.Join(dir, "1pctl"))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	return string(out)
}

// The same logs counted on a server by sitelogs.sh and here by Counter
// must give the same report.
func TestScriptMatchesCounter(t *testing.T) {
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("no awk")
	}
	dir := t.TempDir()
	logs := fixture(t, dir)
	server := Parse(runScript(t, dir))

	c := NewCounter("server", time.Now())
	for _, site := range []string{"blog.example.com", "shop.example.com"} {
		for _, l := range logs[site] {
			c.AddNginxLine(site, strings.TrimSuffix(l, "\n"))
		}
	}
	local := c.Report()
	for i := range server.Sites {
		server.Sites[i].Files = nil
	}
	a, _ := json.MarshalIndent(server, "", " ")
	b, _ := json.MarshalIndent(local, "", " ")
	if string(a) != string(b) {
		diffLines(t, string(a), string(b))
	}
	if fmt.Sprint(server.visits) != fmt.Sprint(local.visits) {
		t.Fatalf("visitors differ:\n%v\n%v", server.visits, local.visits)
	}

	// And the numbers are right.
	today := server.Range(1).Sites[All]
	blog := server.Range(1).Sites["blog.example.com"]
	if blog.Total.PV != 8 || blog.Total.UV != 4 || blog.Total.IP != 4 || today.Total.PV != 9 || today.Total.IP != 4 {
		t.Fatalf("today: blog %+v, all %+v", blog.Total, today.Total)
	}
	var scanner, guesser *IPProfile
	for i, p := range server.Range(1).IPs {
		switch p.IP {
		case "45.148.10.2":
			scanner = &server.Range(1).IPs[i]
		case "3.3.3.3":
			guesser = &server.Range(1).IPs[i]
		}
	}
	if scanner == nil || scanner.Sensitive < 6 || scanner.Inject != 2 || scanner.PeakMin != 70 || scanner.E4xx != 78 || scanner.Paths != 78 {
		t.Fatalf("scanner = %+v", scanner)
	}
	if guesser == nil || guesser.Login != 6 || guesser.Posts != 6 {
		t.Fatalf("password guesser = %+v", guesser)
	}
	// Programs are named by the word that gave them away.
	for _, name := range []string{"zgrab", "sqlmap", "python-requests", "Googlebot", "（没有浏览器标识）"} {
		if !hasItem(blog.Top["bot"], name) {
			t.Errorf("bot %q missing from %+v", name, blog.Top["bot"])
		}
	}
	if got := blog.Top["dead"]; len(got) != 3 || !hasItem(got, "/posts/gone ← /about") || !hasItem(got, "/old-guide ← www.zhihu.com") || !hasItem(got, "/x ← /") {
		t.Fatalf("dead links = %+v", got)
	}
	if got := today.Top["dead"]; !hasItem(got, "blog.example.com/posts/gone ← /about") {
		t.Fatalf("dead links, all sites = %+v", got)
	}
	if got := server.Range(1).Sites[All].Top["leak"]; len(got) != 1 || got[0].Value != "200 blog.example.com/.env" {
		t.Fatalf("leak = %+v", got)
	}
	if got := blog.Top["dir"]; got[0].Value != "/probe/" || got[0].Count != 70 || !hasItem(got, "/") || !hasItem(got, "/posts/") {
		t.Fatalf("dirs = %+v", got)
	}
	if got := server.Range(7).Sites["blog.example.com"].Total.PV; got != 11 {
		t.Fatalf("7-day PV = %d", got)
	}
	if got := server.Range(30).Sites["blog.example.com"].Total.PV; got != 12 {
		t.Fatalf("30-day PV = %d", got)
	}
	if len(server.Days[All]) != 30 || len(server.Hours[All]) != 24 {
		t.Fatalf("history: %d days, %d hours", len(server.Days[All]), len(server.Hours[All]))
	}
}

func hasItem(items []Item, v string) bool {
	for _, it := range items {
		if it.Value == v {
			return true
		}
	}
	return false
}

func diffLines(t *testing.T, a, b string) {
	t.Helper()
	x, y := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] != y[i] {
			lo := max(0, i-6)
			t.Fatalf("script and counter differ at line %d:\nscript:\n%s\ncounter:\n%s", i, strings.Join(x[lo:min(len(x), i+4)], "\n"), strings.Join(y[lo:min(len(y), i+4)], "\n"))
		}
	}
	t.Fatalf("script and counter differ in length: %d vs %d lines", len(x), len(y))
}

func TestLocateAndAssess(t *testing.T) {
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("no awk")
	}
	dir := t.TempDir()
	fixture(t, dir)
	r := Parse(runScript(t, dir))
	r.Locate()
	r.Assess(map[string]Facts{"66.249.1.1": {Crawler: "Googlebot"}, "43.157.1.1": {EdgeOne: true}})
	today := r.Range(1)
	byIP := map[string]IPProfile{}
	for _, p := range today.IPs {
		byIP[p.IP] = p
	}
	if p := byIP["45.148.10.2"]; p.Risk != RiskHigh || len(p.Reasons) < 3 || today.IPs[0].IP != "45.148.10.2" {
		t.Fatalf("scanner: %+v (first %s)", p, today.IPs[0].IP)
	}
	if p := byIP["3.3.3.3"]; p.Risk != RiskMedium || !strings.Contains(strings.Join(p.Reasons, ""), "登录失败 6 次") {
		t.Fatalf("password guesser: %+v", p)
	}
	if p := byIP["66.249.1.1"]; p.Risk != RiskNone || !strings.Contains(p.Reasons[0], "Googlebot") {
		t.Fatalf("crawler: %+v", p)
	}
	if p := byIP["1.1.1.1"]; p.Place != "澳大利亚" || p.Risk != RiskNone {
		t.Fatalf("visitor: %+v", p)
	}
	regions := today.Sites[All].Top["region"]
	// The IPv6 visitor (2602:80d:1005::18) is located too.
	if len(regions) == 0 || !hasItem(regions, "澳大利亚") || !hasItem(regions, "美国") || hasItem(regions, "IPv6（未知）") {
		t.Fatalf("regions = %+v", regions)
	}
	for _, it := range today.Sites["blog.example.com"].Top["ip"] {
		if it.Value == "45.148.10.2" && it.Note == "" {
			t.Fatalf("ranking without place: %+v", it)
		}
	}
	if name, _, ok := ClaimedCrawler("Mozilla/5.0 (compatible; Baiduspider/2.0)"); !ok || name != "百度蜘蛛" {
		t.Fatal("baidu claim")
	}
}
