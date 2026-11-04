package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

// The AI reads what the pages show: databases, recent changes, notices.
func TestToolsForPages(t *testing.T) {
	a := newApp(t)
	sv, f := panelServer(t, a)
	f.MySQL = true
	f.DBs = append(f.DBs, map[string]any{"id": 900, "name": "halo", "username": "halo", "permission": "%", "database": "mysql", "createdAt": "2026-09-01T10:00:00+08:00"})
	ctx := context.Background()
	arg := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

	out, err := a.toolPanelDatabases(ctx, arg(map[string]any{"server_id": sv.ID}))
	if err != nil || !strings.Contains(out, "halo 用户=halo 允许从=任何地址 创建于 2026-09-01") {
		t.Fatalf("databases = %s, %v", out, err)
	}

	// A change that failed and one that worked; a checklist waiting.
	e := a.startExec(store.ExecLog{ServerID: sv.ID, ServerName: sv.Name, Kind: store.ExecChange, Title: "调整 PHP-FPM 进程数",
		Capability: "php_fpm.set", Origin: OriginPlan})
	a.finishExec(&e, "rolled_back", "检查配置……\nnginx -t 没有通过，已恢复")
	e2 := a.startExec(store.ExecLog{ServerName: "腾讯云", Kind: store.ExecChange, Title: "开启自动续费", Capability: "cloud.renew.set", Origin: OriginUser})
	a.finishExec(&e2, "done", "")
	if _, _, err := a.proposePlan(ctx, "ai", sv.ID, "加 swap", "内存不够", []core.Step{{Capability: "swap.set", Params: map[string]any{"size_gb": 2}}}); err != nil {
		t.Fatal(err)
	}
	out, err = a.toolRecentChanges(ctx, nil)
	if err != nil || !strings.Contains(out, "开启自动续费") || !strings.Contains(out, "结果=失败，已自动恢复") ||
		!strings.Contains(out, "原因：nginx -t 没有通过，已恢复") || !strings.Contains(out, "加 swap") || !strings.Contains(out, "发起=清单") {
		t.Fatalf("changes = %s, %v", out, err)
	}
	// One server: the cloud change is not its.
	if out, _ := a.toolRecentChanges(ctx, arg(map[string]any{"server_id": sv.ID})); strings.Contains(out, "开启自动续费") || !strings.Contains(out, "PHP-FPM") {
		t.Errorf("one server = %s", out)
	}

	a.addNotice(ctx, "alert", "提醒：续费和余额", "**续费和余额**\n- 域名 example.com 3 天后到期")
	out, err = a.toolReminders(ctx, nil)
	if err != nil || !strings.Contains(out, "未读 1 条") || !strings.Contains(out, "提醒：续费和余额（未读）") || !strings.Contains(out, "续费和余额 开") {
		t.Fatalf("reminders = %s, %v", out, err)
	}

	// Every tool is offered to the model and named in the instructions.
	tools := a.tools()
	for _, name := range []string{"cloud_account", "panel_databases", "recent_changes", "reminders"} {
		if _, ok := tools[name]; !ok {
			t.Errorf("%s is not offered", name)
		}
		if !strings.Contains(systemPrompt, name) {
			t.Errorf("%s is not in the instructions", name)
		}
	}
	for _, c := range []string{"cloud.renew.set", "aliyun.renew.set", "cloud.firewall.tighten", "nginx.realip", "eo.clientip.header"} {
		if !strings.Contains(systemPrompt, c) {
			t.Errorf("%s is not in the instructions", c)
		}
	}
}

// ssh.harden keeps the account Miao Panel logs in with; the AI does not
// have to know it.
func TestSSHHardenFromAI(t *testing.T) {
	a := newApp(t)
	sv, _ := a.Store.AddServer(store.Server{Name: "web", Host: "203.0.113.9", Port: 22, Username: "deploy", AuthKind: "key", Adapter: "linux"})
	_, steps, err := a.proposePlan(context.Background(), "ai", sv.ID, "加固 SSH", "", []core.Step{{Capability: "ssh.harden", Summary: "关闭密码登录"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(steps[0].Params["login_user"]); got != "deploy" || steps[0].Blocked != "" && strings.Contains(steps[0].Blocked, "login_user") {
		t.Fatalf("step = %+v", steps[0])
	}
}
