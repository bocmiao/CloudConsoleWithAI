package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun"
	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun/aliyuntest"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent/tencenttest"
)

func tencentApp(t *testing.T) (*App, *tencenttest.Fake) {
	t.Helper()
	a := newApp(t)
	f := tencenttest.Start(t)
	a.TencentEndpoint = f.Endpoint
	a.PollInterval = 5 * time.Millisecond
	if _, err := a.SaveTencent(tencenttest.SecretID, tencenttest.SecretKey); err != nil {
		t.Fatal(err)
	}
	return a, f
}

// runPlanAll executes every step of a checklist and waits for it.
func runPlanAll(t *testing.T, a *App, v PlanView) PlanView {
	t.Helper()
	idx := make([]int, len(v.StepList))
	for i := range idx {
		idx[i] = i
	}
	if _, err := a.ExecutePlan(v.ID, idx); err != nil {
		t.Fatal(err)
	}
	return waitPlan(t, a, v.ID)
}

// Rolling back from the page: the checklist snapshots the disk first.
func TestRollbackFromPage(t *testing.T) {
	a, f := tencentApp(t)
	ctx := context.Background()
	f.Snaps["lhsnap-old1"] = &tencent.Snapshot{ID: "lhsnap-old1", Name: "升级前|lhins-abc12345", State: "NORMAL"}

	v, err := a.ProposeCloud(ctx, CloudRequest{Instance: "lhins-abc12345", Region: "ap-guangzhou", Op: "rollback", Snapshot: "lhsnap-old1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.StepList) != 2 || v.StepList[0].Capability != "cloud.snapshot.create" || v.StepList[1].Capability != "cloud.snapshot.rollback" ||
		!strings.Contains(v.Reason, "丢失") || !strings.Contains(v.StepList[1].Summary, "升级前（lhsnap-old1）") {
		t.Fatalf("plan = %+v", v)
	}
	done := runPlanAll(t, a, v)
	if done.Status != core.PlanDone || len(f.Rollbacks) != 1 {
		t.Fatalf("ran: %s %v", done.Status, f.Rollbacks)
	}
	before := 0
	for _, sn := range f.Snaps {
		if strings.HasPrefix(sn.Name, "before-rollback-") {
			before++
		}
	}
	if before != 1 {
		t.Errorf("no snapshot of the disk as it was: %v", f.Snaps)
	}
	if _, err := a.ProposeCloud(ctx, CloudRequest{Instance: "lhins-abc12345", Region: "ap-guangzhou", Op: "rollback", Snapshot: "lhsnap-nope"}); err == nil {
		t.Error("an unknown snapshot was accepted")
	}
}

// The CDN page lists both clouds' domains; its buttons make checklists.
func TestCDNPage(t *testing.T) {
	a, f := tencentApp(t)
	ctx := context.Background()
	f.CDN = []*tencent.CDNDomain{{Domain: "static.example.com", Cname: "static.example.com.cdn.dnsv1.com", Status: "online", ServiceType: "web",
		Area: "mainland", Origins: []string{"1.2.3.4"}, OriginType: "ip"}}
	af := aliyuntest.StartCloud(t)
	a.AliyunCloudEndpoint = af.Endpoint
	if _, err := a.SaveAliyun(af.Client().ID, af.Client().Secret); err != nil {
		t.Fatal(err)
	}

	v, err := a.CDN(ctx)
	if err != nil || len(v.Errors) != 0 {
		t.Fatalf("cdn = %+v %v", v, err)
	}
	var tc, ali int
	for _, d := range v.Domains {
		switch d.Provider {
		case "tencent":
			tc++
		case "aliyun":
			ali++
			if d.Area == "" || len(d.Origins) == 0 {
				t.Errorf("aliyun domain = %+v", d)
			}
		}
	}
	if tc != 1 || ali != len(af.CDN) || len(v.Certs) != 1 || v.Certs[0].ID != "ssl-abc" {
		t.Fatalf("domains = %+v certs = %+v", v.Domains, v.Certs)
	}

	// Purging paths: taken as on the domain.
	p, err := a.ProposeCDN(ctx, CDNRequest{Provider: "tencent", Domain: "static.example.com", Op: "purge", Targets: []string{"/app.js", "https://static.example.com/css/"}})
	if err != nil || p.StepList[0].Capability != "cdn.cache.purge" || p.StepList[0].Params["targets"] != "https://static.example.com/app.js\nhttps://static.example.com/css/" {
		t.Fatalf("purge plan = %+v %v", p.StepList, err)
	}
	if done := runPlanAll(t, a, p); done.Status != core.PlanDone || len(f.CDNTasks) != 2 {
		t.Fatalf("purge: %s %v", done.Status, f.CDNTasks)
	}
	if _, err := a.ProposeCDN(ctx, CDNRequest{Provider: "tencent", Domain: "static.example.com", Op: "purge", Targets: []string{"https://other.com/x"}}); err == nil {
		t.Error("another domain's address was accepted")
	}
	// HTTPS with a new free certificate, then the page shows it.
	p, err = a.ProposeCDN(ctx, CDNRequest{Provider: "tencent", Domain: "static.example.com", Op: "https", Cert: "new"})
	if err != nil || p.StepList[0].Capability != "ssl.cert.apply" {
		t.Fatalf("https plan = %+v %v", p.StepList, err)
	}
	if done := runPlanAll(t, a, p); done.Status != core.PlanDone {
		t.Fatalf("https: %s %v", done.Status, done.StepList[0].Log)
	}
	v, _, _ = a.CDNPage(ctx, PageLatest)
	for _, d := range v.Domains {
		if d.Domain == "static.example.com" && (!d.HTTPS || d.CertID == "") {
			t.Errorf("after the certificate: %+v", d)
		}
	}
	// Stopping an 阿里云 domain.
	if len(af.CDN) > 0 {
		p, err = a.ProposeCDN(ctx, CDNRequest{Provider: "aliyun", Domain: af.CDN[0].Name, Op: "off"})
		if err != nil || p.StepList[0].Capability != "aliyun.cdn.status" {
			t.Fatalf("aliyun plan = %+v %v", p.StepList, err)
		}
		if _, err := a.ProposeCDN(ctx, CDNRequest{Provider: "aliyun", Domain: af.CDN[0].Name, Op: "https", Cert: "ssl-abc"}); err == nil {
			t.Error("阿里云 CDN certificates are set in its console")
		}
	}

	// The 证书 page's free certificate.
	if p, err := a.ProposeFreeCert(ctx, "blog.example.com"); err != nil || p.StepList[0].Params["domain"] != "blog.example.com" {
		t.Fatalf("free cert = %+v %v", p, err)
	}
	if _, err := a.ProposeFreeCert(ctx, "*.example.com"); err == nil {
		t.Error("a wildcard was accepted")
	}
	out, err := a.toolCDN(ctx, nil)
	if err != nil || !strings.Contains(out, "腾讯云 CDN static.example.com 状态=已启用") || !strings.Contains(out, "HTTPS=开") {
		t.Errorf("tool = %s %v", out, err)
	}
}

// Both clouds' alarms: going on first; 阿里云's log entries grouped into
// the episode going on.
func TestCloudAlarms(t *testing.T) {
	a, f := tencentApp(t)
	ctx := context.Background()
	at := func(ago time.Duration) string { return time.Now().Add(-ago).UTC().Format(time.RFC3339) }
	f.Alarms = []tencent.Alarm{
		{ID: "a1", Object: "blog (10.0.0.3)", Content: "CPU利用率 > 90%", Policy: "默认", Status: "ALARM", Level: "Serious", First: at(2 * time.Hour), Last: at(time.Minute)},
		{ID: "a2", Object: "shop (10.0.0.4)", Content: "内存利用率 > 85%", Status: "OK", Level: "Warn", First: at(30 * time.Hour), Last: at(29 * time.Hour)},
		{ID: "old", Object: "x", Content: "y", Status: "OK", First: at(10 * 24 * time.Hour), Last: at(10 * 24 * time.Hour)},
	}
	af := aliyuntest.StartCloud(t)
	a.AliyunCloudEndpoint = af.Endpoint
	if _, err := a.SaveAliyun(af.Client().ID, af.Client().Secret); err != nil {
		t.Fatal(err)
	}
	disk := aliyun.Alarm{RuleID: "r1", Rule: "磁盘", Instance: "i-bp1shop", InstanceName: "shop", Metric: "diskusage_utilization", Expression: "磁盘使用率 > 90%"}
	ev := func(ago time.Duration, level, change string) aliyun.Alarm {
		x := disk
		x.ID, x.Time, x.Level, x.Change = fmt.Sprint(ago), at(ago), level, change
		return x
	}
	// Went off, recovered, went off again an hour ago and still is.
	af.AlertLogs = []aliyun.Alarm{ev(5*time.Hour, "P3", "OK->P3"), ev(4*time.Hour, "OK", "P3->OK"), ev(time.Hour, "P3", "OK->P3"), ev(30*time.Minute, "P3", "P3->P3")}

	v, err := a.CloudAlarms(ctx)
	if err != nil || len(v.Errors) != 0 || len(v.Alarms) != 3 {
		t.Fatalf("alarms = %+v %v", v, err)
	}
	if x := v.Alarms[0]; x.Provider != "tencent" || !x.Active || x.Level != "crit" {
		t.Errorf("first = %+v", x)
	}
	if x := v.Alarms[1]; x.Provider != "aliyun" || !x.Active || x.Level != "warn" || x.First != at(time.Hour) || x.Object != "shop（i-bp1shop）" {
		t.Errorf("aliyun = %+v (want it to start %s)", x, at(time.Hour))
	}
	if x := v.Alarms[2]; x.Active || x.Object != "shop (10.0.0.4)" {
		t.Errorf("recovered = %+v", x)
	}

	ov, err := a.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, it := range ov.Todo {
		if it.Kind == "alarm" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("overview alarms = %d: %+v", n, ov.Todo)
	}
	a.alert(ctx)
	notes := a.Notices().Notices
	if len(notes) != 1 || !strings.Contains(notes[0].Text, "云监控告警") || !strings.Contains(notes[0].Text, "磁盘使用率 > 90%") ||
		strings.Contains(notes[0].Text, "内存利用率") {
		t.Fatalf("alert = %+v", notes)
	}
	a.alert(ctx)
	if len(a.Notices().Notices) != 1 {
		t.Error("alerted twice")
	}
	out, err := a.toolCloudAlarms(ctx, nil)
	if err != nil || !strings.Contains(out, "[腾讯云 严重 未恢复] blog (10.0.0.3)：CPU利用率 > 90%") || !strings.Contains(out, "已恢复") {
		t.Errorf("tool = %s %v", out, err)
	}
}

// EdgeOne's 防护: rate limits, CC protection and acceleration domains, as
// checklists from the page.
func TestEOProtection(t *testing.T) {
	a, f := tencentApp(t)
	ctx := context.Background()
	f.Domains = append(f.Domains, &tencenttest.Domain{Zone: "zone-abc", Name: "blog.example.com", Status: "online", Cname: "blog.example.com.eo.dnse1.com", Origin: "1.2.3.4"})

	v, err := a.EOProtection(ctx, "blog.example.com")
	if err != nil || v.Zone != "example.com" || v.PolicyError != "" || len(v.Domains) != 1 || v.Domains[0].Status != "online" {
		t.Fatalf("view = %+v %v", v, err)
	}
	run := func(req EOProtectRequest) {
		t.Helper()
		p, err := a.ProposeEOProtect(ctx, req)
		if err != nil {
			t.Fatalf("%+v: %v", req, err)
		}
		if done := runPlanAll(t, a, p); done.Status != core.PlanDone {
			t.Fatalf("%+v: %s %v", req, done.Status, done.StepList[0].Log)
		}
	}
	run(EOProtectRequest{Domain: "example.com", Op: "ratelimit_set", Path: "/wp-login.php", Threshold: 20, Period: "1m", Action: "challenge", Duration: "10m"})
	run(EOProtectRequest{Domain: "example.com", Op: "cc", Enabled: true, Sensitivity: "Moderate", Action: "challenge"})
	run(EOProtectRequest{Domain: "example.com", Op: "domain_off", Name: "blog.example.com"})

	v, _, err = a.EOProtectionPage(ctx, "example.com", PageLatest)
	if err != nil || len(v.Rules) != 1 || !v.CC.Enabled || v.CC.Sensitivity != "Moderate" || v.Domains[0].Status != "offline" {
		t.Fatalf("after = %+v %v", v, err)
	}
	if r := v.Rules[0]; r.Path != "/wp-login.php" || r.Threshold != 20 || r.Period != "1m" || !r.Mine || !r.Enabled {
		t.Errorf("rule = %+v", r)
	}
	run(EOProtectRequest{Domain: "example.com", Op: "ratelimit_remove", Name: v.Rules[0].Name})
	if v, _, _ = a.EOProtectionPage(ctx, "example.com", PageLatest); len(v.Rules) != 0 {
		t.Errorf("rule not removed: %+v", v.Rules)
	}
	if _, err := a.ProposeEOProtect(ctx, EOProtectRequest{Domain: "example.com", Op: "domain_on", Name: "nope.example.com"}); err == nil {
		t.Error("an unknown domain was accepted")
	}
	if _, err := a.ProposeEOProtect(ctx, EOProtectRequest{Domain: "example.com", Op: "ratelimit_set"}); err == nil {
		t.Error("a rule without a threshold was accepted")
	}
}
