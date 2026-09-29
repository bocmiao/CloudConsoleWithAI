package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/btpanel/btpaneltest"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx/sshtest"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent/tencenttest"
)

// Miao Panel's own settings change through a checklist, and undoing a
// step puts back only what that step changed.
func TestLocalSettingsPlan(t *testing.T) {
	a, _ := tencentApp(t)
	ctx := context.Background()
	if _, err := a.SaveMonitorSettings(MonitorSettings{Enabled: true, Auto: true, Servers: true, DiskPct: 90, MemPct: 95, CPUPct: 95,
		Extra: []string{"https://old.example.com/"}}); err != nil {
		t.Fatal(err)
	}
	steps := []core.Step{
		{Capability: "monitor.settings.set", Summary: "磁盘提醒线降到 80%", Params: map[string]any{"disk_pct": 80,
			"watch": "https://shop.example.com\nhttps://old.example.com/", "unwatch": "https://old.example.com/\nhttps://blog.example.com/"}},
		{Capability: "autoblock.set", Summary: "开启自动封禁", Params: map[string]any{"enabled": "on", "level": "medium", "hours": 48, "allow_add": "203.0.113.7"}},
		{Capability: "notice.settings.set", Summary: "日报改到 8:30", Params: map[string]any{"daily_at": "8:30", "alert_cloud": "off"}},
	}
	p, _, err := a.proposePlan(ctx, "ai", 0, "调整 Miao Panel 设置", "测试", steps)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := a.Plan(p.ID)
	for _, st := range v.StepList {
		if !st.Executable || !st.Reversible {
			t.Fatalf("step %s: executable=%v reversible=%v %s", st.Capability, st.Executable, st.Reversible, st.Blocked)
		}
	}
	if v.StepList[1].Risk != core.R2 || v.StepList[0].Risk != core.R1 {
		t.Errorf("risks = %v %v", v.StepList[0].Risk, v.StepList[1].Risk)
	}
	done := runPlanAll(t, a, v)
	if done.Status != core.PlanDone {
		t.Fatalf("plan: %s %+v", done.Status, done.StepList)
	}
	m := a.MonitorSettings()
	if m.DiskPct != 80 || !slices.Contains(m.Extra, "https://shop.example.com/") || slices.Contains(m.Extra, "https://old.example.com/") ||
		!slices.Contains(m.Skip, "https://blog.example.com/") || slices.Contains(m.Skip, "https://old.example.com/") {
		t.Fatalf("monitor = %+v", m)
	}
	ab := a.AutoBlock().Settings
	if !ab.Enabled || ab.Level != "medium" || ab.Hours != 48 || !slices.Contains(ab.Allow, "203.0.113.7") {
		t.Fatalf("autoblock = %+v", ab)
	}
	ns := a.Notices().Settings
	if ns.DailyAt != "08:30" || ns.AlertCloud || !ns.AlertRisk {
		t.Fatalf("notices = %+v", ns)
	}
	if e, err := a.Store.GetExec(done.StepList[0].LogID); err != nil || e.ServerName != "Miao Panel" || !strings.Contains(e.Output, "磁盘提醒线：90% → 80%") {
		t.Fatalf("log = %+v, %v", e, err)
	}

	// Someone changes the CPU line afterwards; undoing the step leaves it.
	m.CPUPct = 70
	if _, err := a.SaveMonitorSettings(m); err != nil {
		t.Fatal(err)
	}
	for i := range done.StepList {
		if _, err := a.UndoStep(ctx, done.ID, i); err != nil {
			t.Fatalf("undo %d: %v", i, err)
		}
	}
	m = a.MonitorSettings()
	if m.DiskPct != 90 || m.CPUPct != 70 || !slices.Equal(m.Extra, []string{"https://old.example.com/"}) || len(m.Skip) != 0 {
		t.Fatalf("monitor after undo = %+v", m)
	}
	if ab := a.AutoBlock().Settings; ab.Enabled || ab.Level != "high" || ab.Hours != 24 || len(ab.Allow) != 0 {
		t.Fatalf("autoblock after undo = %+v", ab)
	}
	if ns := a.Notices().Settings; ns.DailyAt != "09:00" || !ns.AlertCloud {
		t.Fatalf("notices after undo = %+v", ns)
	}

	for _, bad := range []map[string]any{{}, {"daily_at": "25:00"}} {
		if _, err := actions.Resolve("notice.settings.set", bad, noServer); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if _, err := actions.Resolve("autoblock.set", map[string]any{"allow_add": "not-an-ip"}, noServer); err == nil {
		t.Error("a bad allow list was accepted")
	}
}

// The AI never blocks EdgeOne's nodes or private addresses.
func TestAIBlockScreened(t *testing.T) {
	a, f := tencentApp(t)
	f.EdgeOneNodes = map[string]bool{"43.175.1.1": true}
	ctx := context.Background()
	raw, _ := json.Marshal(map[string]any{"server_id": 0, "title": "封禁", "reason": "刷接口", "steps": []map[string]any{
		{"capability": "eo.ip.block", "summary": "封禁", "params": map[string]any{"domain": "example.com", "ips": "43.175.1.1, 10.0.0.5, 198.51.100.9"}},
	}})
	out, err := a.toolProposePlan(ctx, raw)
	if err != nil || !strings.Contains(out, "43.175.1.1：EdgeOne 的节点") || !strings.Contains(out, "10.0.0.5：内网地址") {
		t.Fatalf("out = %s, %v", out, err)
	}
	plans, _ := a.Store.ListPlans(5)
	v, _ := a.Plan(plans[0].ID)
	if ips := v.StepList[0].Params["ips"]; ips != "198.51.100.9" {
		t.Fatalf("ips = %v", ips)
	}
	raw, _ = json.Marshal(map[string]any{"server_id": 0, "title": "封禁", "reason": "x", "steps": []map[string]any{
		{"capability": "eo.ip.block", "summary": "封禁", "params": map[string]any{"domain": "example.com", "ips": "43.175.1.1"}},
	}})
	if _, err := a.toolProposePlan(ctx, raw); err == nil {
		t.Error("a checklist blocking only an EdgeOne node was saved")
	}
	// IPs as a list are not dropped quietly: the step says what is wrong.
	raw, _ = json.Marshal(map[string]any{"server_id": 0, "title": "封禁", "reason": "x", "steps": []map[string]any{
		{"capability": "eo.ip.block", "summary": "封禁", "params": map[string]any{"domain": "example.com", "ips": []string{"198.51.100.9"}}},
	}})
	if out, err := a.toolProposePlan(ctx, raw); err != nil || !strings.Contains(out, "不能自动执行") {
		t.Errorf("ips as a list = %s, %v", out, err)
	}
}

// The AI reads files with secrets hidden, and not the files that are
// nothing but secrets.
func TestServerFilesTool(t *testing.T) {
	for p, bad := range map[string]bool{
		"/etc/shadow": true, "/root/.ssh/authorized_keys": true, "/home/u/.ssh": true, "/etc/ssl/private/site.key": true,
		"/root/id_ed25519": true, "/root/id_ed25519.pub": false, "/etc/nginx/nginx.conf": false, "/www/wwwroot/a/.env": false,
		"/proc/1/environ": true, "/etc/letsencrypt/live/a/privkey.pem": true, "/etc/letsencrypt/live/a/fullchain.pem": false,
	} {
		if (secretFile(p) != "") != bad {
			t.Errorf("secretFile(%s) = %q", p, secretFile(p))
		}
	}
	in := "DB_PASSWORD=hunter2\ndefine( 'AUTH_KEY', 'abc$def' );\nmysql -uroot -psecret db\nurl=https://u:pw@example.com/x\nkeep=me\n" +
		"-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----\n"
	out := redactText(in)
	for _, leak := range []string{"hunter2", "abc$def", "secret", ":pw@", "MIIE"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q left in %s", leak, out)
		}
	}
	if !strings.Contains(out, "keep=me") {
		t.Errorf("too much hidden: %s", out)
	}
}

// Exec and checklist details, Miao Panel's own state and the blocked
// list are there for the AI.
func TestToolsForMiaoPanel(t *testing.T) {
	a, _ := tencentApp(t)
	ctx := context.Background()
	e := a.startExec(store.ExecLog{ServerName: "腾讯云", Kind: store.ExecChange, Title: "改解析", Capability: "dns.record.set",
		Params: map[string]any{"domain": "example.com"}, Origin: OriginPlan})
	a.finishExec(&e, "failed", "第一步\nAPI 返回：记录已存在 password=abc")
	out, err := a.toolRecentChanges(ctx, json.RawMessage(`{"id":`+itoa(e.ID)+`}`))
	if err != nil || !strings.Contains(out, "记录已存在") || strings.Contains(out, "abc") || !strings.Contains(out, "dns.record.set") {
		t.Fatalf("detail = %s, %v", out, err)
	}
	p, _, err := a.proposePlan(ctx, "ai", 0, "关掉日报", "用户不看", []core.Step{{Capability: "notice.settings.set", Summary: "关日报", Params: map[string]any{"daily": "off"}}})
	if err != nil {
		t.Fatal(err)
	}
	out, err = a.toolRecentChanges(ctx, json.RawMessage(`{"plan_id":`+itoa(p.ID)+`}`))
	if err != nil || !strings.Contains(out, "关掉日报") || !strings.Contains(out, "notice.settings.set 关日报：没有执行") {
		t.Fatalf("plan = %s, %v", out, err)
	}
	out, err = a.toolMiaoPanel(ctx, nil)
	if err != nil || !strings.Contains(out, "腾讯云密钥 已配置") || !strings.Contains(out, "自动封禁：关") || !strings.Contains(out, "AI 自由命令（free_command）：关") {
		t.Fatalf("miao_panel = %s, %v", out, err)
	}
	out, err = a.toolBlockedIPs(ctx, nil)
	if err != nil || !strings.Contains(out, "eo.ip.unblock") {
		t.Fatalf("blocked = %s, %v", out, err)
	}
	out, err = a.toolMonitorStatus(ctx, json.RawMessage(`{"server_id": 99}`))
	if err == nil {
		t.Errorf("an unknown server's history = %s", out)
	}
	out, err = a.toolMonitorStatus(ctx, nil)
	if err != nil || !strings.Contains(out, "提醒线 磁盘 90%") {
		t.Fatalf("monitor = %s, %v", out, err)
	}
	// The AI is told what cannot run yet, and nothing it can run is in
	// that list.
	for _, n := range actions.Pending() {
		if _, err := actions.Resolve(n, nil, "1panel"); err == nil {
			t.Errorf("%s is pending but runs", n)
		}
	}
	if d := actions.Describe(); !strings.Contains(d, "aliyun.dns.record.set（") || strings.Contains(d, "阿里云") == false ||
		!strings.Contains(d, "monitor.settings.set（修改监控设置；Miao Panel 自己的设置") {
		t.Errorf("describe does not say where things run:\n%s", d)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

// Over SFTP: a folder, a file read in parts, a link to a secret.
func TestServerFilesOverSSH(t *testing.T) {
	a := newApp(t)
	srv := sshtest.Start(t, "root", "pw")
	sv := addTestServer(t, a, srv, "pw")
	ctx := context.Background()
	root := t.TempDir()
	var conf strings.Builder
	conf.WriteString("server {\n  # DB_PASSWORD=hunter2\n")
	for i := 0; i < 3000; i++ {
		conf.WriteString("  location /x { return 200 'ok ok ok ok'; }\n")
	}
	conf.WriteString("}\n")
	_ = os.WriteFile(filepath.Join(root, "site.conf"), []byte(conf.String()), 0o644)
	_ = os.MkdirAll(filepath.Join(root, "keys"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "keys", "server.key"), []byte("SECRET"), 0o600)
	_ = os.Symlink(filepath.Join(root, "keys", "server.key"), filepath.Join(root, "innocent.txt"))
	_ = os.WriteFile(filepath.Join(root, "app.bin"), []byte{0x7f, 'E', 'L', 'F', 0, 0, 1}, 0o755)
	run := func(p string, from int) (string, error) {
		raw, _ := json.Marshal(map[string]any{"server_id": sv.ID, "path": p, "from_line": from})
		return a.toolServerFiles(ctx, raw)
	}

	out, err := run(root, 0)
	if err != nil || !strings.Contains(out, "site.conf") || !strings.Contains(out, "keys/") || !strings.Contains(out, "innocent.txt -> ") {
		t.Fatalf("list = %s, %v", out, err)
	}
	out, err = run(root+"/site.conf", 0)
	if err != nil || strings.Contains(out, "hunter2") || !strings.Contains(out, "第 1~") || !strings.Contains(out, "from_line=") {
		t.Fatalf("read = %.300s, %v", out, err)
	}
	// Read on until the end, a part at a time.
	parts := 1
	for i := strings.Index(out, "from_line="); i >= 0; i = strings.Index(out, "from_line=") {
		n, _ := strconv.Atoi(strings.Fields(out[i+len("from_line="):])[0])
		if out, err = run(root+"/site.conf", n); err != nil || !strings.Contains(out, "共 3003 行") || !strings.Contains(out, "第 "+strconv.Itoa(n)+"~") {
			t.Fatalf("part from %d = %.300s…, %v", n, out, err)
		}
		parts++
	}
	if parts < 3 || !strings.HasSuffix(strings.TrimSpace(out), "}") || !strings.Contains(out, "~3003 行") {
		t.Fatalf("%d parts, last = %.300s", parts, out)
	}
	if out, err := run(root+"/innocent.txt", 0); err == nil || strings.Contains(out, "SECRET") {
		t.Fatalf("followed a link to a key: %s %v", out, err)
	}
	_ = os.MkdirAll(filepath.Join(root, "home", ".ssh"), 0o700)
	_ = os.WriteFile(filepath.Join(root, "home", ".ssh", "authorized_keys"), []byte("ssh-ed25519 AAAA"), 0o600)
	_ = os.Symlink("home/.ssh", filepath.Join(root, "pub"))
	if out, err := run(root+"/pub/authorized_keys", 0); err == nil || strings.Contains(out, "AAAA") {
		t.Fatalf("followed a folder link into .ssh: %s %v", out, err)
	}
	if out, err := run(root+"/app.bin", 0); err != nil || !strings.Contains(out, "二进制") {
		t.Fatalf("binary = %s, %v", out, err)
	}
}

// Panel tools say plainly what a server without the panel has, and the
// backups tool reads 宝塔 too.
func TestPanelToolsByServerKind(t *testing.T) {
	a := newApp(t)
	ctx := context.Background()
	arg := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

	sv, f := btServer(t, a)
	s := f.AddSite("blog.example.com")
	f.Backups = append(f.Backups, &btpaneltest.Backup{ID: 1, Type: 0, PID: s.ID, Name: "web_blog_20260928.tar.gz",
		Filename: "/www/backup/site/web_blog_20260928.tar.gz", Size: 2 << 20, AddTime: "2026-09-28 03:00:00"})
	out, err := a.toolPanelBackups(ctx, arg(map[string]any{"server_id": sv.ID, "website": "blog.example.com"}))
	if err != nil || !strings.Contains(out, "web_blog_20260928.tar.gz") || !strings.Contains(out, "（宝塔）") {
		t.Fatalf("bt backups = %s, %v", out, err)
	}
	if out, err := a.toolPanelDatabases(ctx, arg(map[string]any{"server_id": sv.ID})); err != nil || !strings.Contains(out, "只支持 1Panel") {
		t.Fatalf("bt databases = %s, %v", out, err)
	}

	srv := sshtest.Start(t, "root", "pw")
	plain := addTestServer(t, a, srv, "pw")
	if err := a.Store.SaveProfile(plain.ID, "", "linux"); err != nil {
		t.Fatal(err)
	}
	for name, run := range map[string]func(context.Context, json.RawMessage) (string, error){
		"websites": a.toolPanelWebsites, "website": a.toolPanelWebsite, "backups": a.toolPanelBackups, "databases": a.toolPanelDatabases,
	} {
		out, err := run(ctx, arg(map[string]any{"server_id": plain.ID, "website": "a.example.com"}))
		if err != nil || !strings.Contains(out, "纯 Linux") || !strings.Contains(out, "server_files") {
			t.Errorf("%s on plain Linux = %s, %v", name, out, err)
		}
	}
}

// History of one server and one website, DNS lines and the full COS
// rules are there for the AI.
func TestMoreReadsForAI(t *testing.T) {
	a, f := tencentApp(t)
	ctx := context.Background()

	srv := sshtest.Start(t, "root", "pw")
	sv := addTestServer(t, a, srv, "pw")
	now := time.Now().UTC()
	for i, m := range []store.ServerSample{
		{OK: true, CPU: 20, Mem: 50, Disk: 40, Load1: 0.5}, {OK: true, CPU: 90, Mem: 70, Disk: 40, Load1: 3}, {OK: false, Error: "timeout"},
	} {
		m.ServerID, m.At = sv.ID, now.Add(time.Duration(i-3)*time.Hour).Format(time.RFC3339)
		if err := a.Store.AddServerSample(m); err != nil {
			t.Fatal(err)
		}
	}
	out, err := a.toolMonitorStatus(ctx, json.RawMessage(`{"server_id":`+itoa(sv.ID)+`,"hours":12}`))
	if err != nil || !strings.Contains(out, "CPU 90%/90%") || !strings.Contains(out, "连不上（1 次）") {
		t.Fatalf("server history = %s, %v", out, err)
	}

	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer web.Close()
	if _, err := a.SaveMonitorSettings(MonitorSettings{Enabled: true, Auto: false, DiskPct: 90, MemPct: 95, CPUPct: 95, Extra: []string{web.URL}}); err != nil {
		t.Fatal(err)
	}
	out, err = a.toolMonitorStatus(ctx, json.RawMessage(`{"refresh":true,"site":"`+web.URL+`/"}`))
	if err != nil || !strings.Contains(out, "检查 1 次") || !strings.Contains(out, "可用率 100.00%") {
		t.Fatalf("site history = %s, %v", out, err)
	}

	out, err = a.toolTencentDNS(ctx, json.RawMessage(`{"domain":"example.com"}`))
	if err != nil || !strings.Contains(out, "可以用的线路") || !strings.Contains(out, "电信") {
		t.Fatalf("dns = %s, %v", out, err)
	}

	bucket := "img-" + tencenttest.COSAppID
	b := f.AddBucket(bucket, "ap-guangzhou", "private")
	b.CORS = []tencent.CORSRule{{Methods: []string{"GET"}, Origins: []string{"https://a.example.com"}, MaxAge: 600}}
	for i := 0; i < 120; i++ {
		b.PutFile(fmt.Sprintf("p/%03d.jpg", i), []byte("x"))
	}
	out, err = a.toolTencentCOS(ctx, json.RawMessage(`{"bucket":"`+bucket+`","prefix":"p/"}`))
	if err != nil || !strings.Contains(out, `"origins":["https://a.example.com"]`) || !strings.Contains(out, "marker=") || strings.Contains(out, "p/119.jpg") {
		t.Fatalf("cos = %s, %v", out, err)
	}
	marker := strings.Fields(out[strings.Index(out, "marker=")+len("marker="):])[0]
	out, err = a.toolTencentCOS(ctx, json.RawMessage(`{"bucket":"`+bucket+`","prefix":"p/","marker":"`+marker+`"}`))
	if err != nil || !strings.Contains(out, "p/119.jpg") || strings.Contains(out, "p/000.jpg") {
		t.Fatalf("cos next page = %s, %v", out, err)
	}
}
