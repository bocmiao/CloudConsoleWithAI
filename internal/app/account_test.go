package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun"
	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun/aliyuntest"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent/tencenttest"
)

func day(days int) string { return time.Now().AddDate(0, 0, days).Format("2006-01-02") }

// accountApp has both clouds: on 腾讯云 ¥123.45, a Lighthouse server
// expiring in 3 days and a CVM in 10 (both renewed by hand), and three
// registered domains; on 阿里云 ¥5 owed, an ECS that renews itself, a
// lightweight server, and one domain in 10 days.
func accountApp(t *testing.T) (*App, *tencenttest.Fake, *aliyuntest.Cloud) {
	t.Helper()
	a := newApp(t)
	tf := tencenttest.Start(t)
	a.TencentEndpoint = tf.Endpoint
	a.PollInterval = 10 * time.Millisecond
	if _, err := a.SaveTencent(tencenttest.SecretID, tencenttest.SecretKey); err != nil {
		t.Fatal(err)
	}
	tf.BalanceFen = 12345
	tf.Instances["lhins-abc12345"].ExpiresIn = 3*24*time.Hour + time.Hour
	tf.Registered = []tencent.RegisteredDomain{
		{Name: "example.com", Expires: day(20)},
		{Name: "auto.com", Expires: day(5), AutoRenew: true},
		{Name: "far.com", Expires: day(200)},
	}
	af := aliyuntest.StartCloud(t)
	a.AliyunCloudEndpoint = af.Endpoint
	if _, err := a.SaveAliyun(af.Client().ID, af.Client().Secret); err != nil {
		t.Fatal(err)
	}
	af.Balance = -5
	af.Registered = []aliyun.RegisteredDomain{{Name: "ali.com", Expires: day(10)}}
	return a, tf, af
}

func TestCloudAccount(t *testing.T) {
	a, tf, af := accountApp(t)
	ctx := context.Background()
	web, _ := a.Store.AddServer(store.Server{Name: "web", Host: "81.68.79.253", Port: 22, Username: "root", AuthKind: "password"})

	v, err := a.CloudAccount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Errors) != 0 || len(v.Balances) != 2 {
		t.Fatalf("account = %+v", v)
	}
	if b := v.Balances[0]; b.Provider != "tencent" || b.Available != 123.45 || b.Owed != 0 || b.Error != "" {
		t.Errorf("tencent balance = %+v", b)
	}
	if b := v.Balances[1]; b.Provider != "aliyun" || b.Available != -5 || b.Owed != 5 {
		t.Errorf("aliyun balance = %+v", b)
	}
	// Prepaid servers only (the pay-as-you-go ECS is left out), soonest first.
	var got []string
	for _, s := range v.Servers {
		got = append(got, s.ID+":"+s.Level)
	}
	if strings.Join(got, " ") != "lhins-abc12345:crit ins-xyz98765:warn i-bp1shop000000000001:ok 2ad1ae67295445f598017499dc000001:ok" {
		t.Fatalf("servers = %v", got)
	}
	lh := v.Servers[0]
	if lh.Days != 3 || lh.AutoRenew || !lh.CanSwitch || lh.ServerID != web.ID || lh.Kind != "轻量应用服务器" || lh.Note != "不会自动续费" {
		t.Errorf("lighthouse = %+v", lh)
	}
	if ecs := v.Servers[2]; !ecs.AutoRenew || !ecs.CanSwitch {
		t.Errorf("ecs = %+v", ecs)
	}
	if swas := v.Servers[3]; swas.CanSwitch || swas.AutoRenew {
		t.Errorf("simple application server = %+v", swas)
	}
	got = nil
	for _, d := range v.Domains {
		got = append(got, d.Name+":"+d.Level)
	}
	// A domain that renews itself is fine; 阿里云's are taken as renewed
	// by hand.
	if strings.Join(got, " ") != "auto.com:ok ali.com:warn example.com:warn far.com:ok" {
		t.Fatalf("domains = %v", got)
	}

	// 总览 lists money owed and what needs renewing, a few at most.
	ov, err := a.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var todo []string
	for _, it := range ov.Todo {
		if it.Kind == "account" {
			todo = append(todo, it.Title)
		}
	}
	if len(todo) != 4 || todo[0] != "阿里云账户欠费 ¥5.00" || !strings.Contains(strings.Join(todo, "|"), "服务器 blog 3 天后到期") ||
		todo[3] != "还有 2 项续费提醒" {
		t.Errorf("todo = %q", todo)
	}

	// An alert says so once; the next check has nothing new.
	a.alert(ctx)
	n := a.Notices()
	if len(n.Notices) != 1 || !strings.Contains(n.Notices[0].Title, "续费和余额") || !strings.Contains(n.Notices[0].Text, "阿里云账户欠费 ¥5.00") ||
		!strings.Contains(n.Notices[0].Text, "域名 example.com（腾讯云）20 天后到期") || strings.Contains(n.Notices[0].Text, "far.com") {
		t.Fatalf("alert = %+v", n.Notices)
	}
	a.alert(ctx)
	if len(a.Notices().Notices) != 1 {
		t.Error("alerted twice")
	}
	if r := a.reportText(ctx, time.Now().AddDate(0, 0, -1)); !strings.Contains(r, "**云账号**") || !strings.Contains(r, "腾讯云余额 ¥123.45") {
		t.Errorf("report = %s", r)
	}

	// The AI reads the same.
	out, err := a.toolCloudAccount(ctx, json.RawMessage(`{}`))
	if err != nil || !strings.Contains(out, "腾讯云：可用 ¥123.45") || !strings.Contains(out, "欠费 ¥5.00") ||
		!strings.Contains(out, "id=lhins-abc12345 region=ap-guangzhou") || !strings.Contains(out, "（自动续费要在云控制台设置）") {
		t.Fatalf("tool = %s, %v", out, err)
	}

	// Turning automatic renewal on from the page: the list shows it after.
	p, err := a.ProposeCloud(ctx, CloudRequest{Instance: "lhins-abc12345", Region: "ap-guangzhou", Op: "renew_on"})
	if err != nil || len(p.StepList) != 1 || p.StepList[0].Capability != "cloud.renew.set" || !p.StepList[0].Executable {
		t.Fatalf("plan = %+v, %v", p, err)
	}
	if _, err := a.ExecutePlan(p.ID, []int{0}); err != nil {
		t.Fatal(err)
	}
	if done := waitPlan(t, a, p.ID); done.Status != core.PlanDone {
		t.Fatalf("plan %s", done.Status)
	}
	if tf.Instances["lhins-abc12345"].RenewFlag != tencent.RenewAuto {
		t.Fatal("not renewing itself")
	}
	v, _, err = a.CloudAccountPage(ctx, PageLatest)
	if err != nil || v.Servers[0].ID != "lhins-abc12345" || !v.Servers[0].AutoRenew || v.Servers[0].Level != "ok" {
		t.Fatalf("after turning it on: %+v %v", v.Servers, err)
	}
	p, err = a.ProposeAliyun(ctx, CloudRequest{Instance: "i-bp1shop000000000001", Region: "cn-hangzhou", Op: "renew_off"})
	if err != nil || p.StepList[0].Capability != "aliyun.renew.set" || !strings.Contains(p.Title, "关闭自动续费") {
		t.Fatalf("aliyun plan = %+v, %v", p, err)
	}
	// Not for pay-as-you-go servers, nor 阿里云's lightweight ones.
	if _, err := a.ProposeAliyun(ctx, CloudRequest{Instance: "i-2zedb000000000000002", Region: "cn-beijing", Op: "renew_on"}); err == nil {
		t.Error("a pay-as-you-go server was offered renewal")
	}
	if _, err := a.ProposeAliyun(ctx, CloudRequest{Instance: "2ad1ae67295445f598017499dc000001", Region: "cn-hangzhou", Op: "renew_on"}); err == nil {
		t.Error("a simple application server was offered renewal")
	}

	// A part that cannot be read says why; the rest is still there.
	tf.FailAction = "billing DescribeAccountBalance"
	af.Registered = nil
	v, _, err = a.CloudAccountPage(ctx, PageRefresh)
	if err != nil || v.Balances[0].Error == "" || v.Balances[1].Owed != 5 || len(v.Servers) != 4 || len(v.Domains) != 3 {
		t.Fatalf("with a failure: %+v %v", v, err)
	}

	// Turned off, no alert.
	st := a.Notices().Settings
	st.AlertRenew = false
	if _, err := a.SaveNoticeSettings(st); err != nil {
		t.Fatal(err)
	}
	if a.Notices().Settings.AlertRenew {
		t.Fatal("still on")
	}
}

// Nothing to read without a cloud account; the tool says how to set one up.
func TestCloudAccountWithoutKeys(t *testing.T) {
	a := newApp(t)
	ctx := context.Background()
	if _, err := a.CloudAccount(ctx); err == nil {
		t.Error("read an account without keys")
	}
	if out, err := a.toolCloudAccount(ctx, nil); err != nil || !strings.Contains(out, "设置") {
		t.Errorf("tool = %q %v", out, err)
	}
	if ov, err := a.Overview(ctx); err != nil || len(ov.Todo) != 0 {
		t.Errorf("overview = %+v %v", ov.Todo, err)
	}
	if !a.Notices().Settings.AlertRenew {
		t.Error("renewal alerts are off by default")
	}
}

func TestDueLevel(t *testing.T) {
	for _, c := range []struct {
		days        int
		auto, broke bool
		want        string
	}{
		{-1, true, false, "crit"}, {3, false, false, "crit"}, {10, false, false, "warn"}, {20, false, false, "ok"},
		{3, true, false, "ok"}, {3, true, true, "warn"}, {20, true, true, "ok"},
	} {
		if got, _ := dueLevel(c.days, serverSoonDays, c.auto, c.broke); got != c.want {
			t.Errorf("dueLevel(%d, auto=%v, broke=%v) = %s, want %s", c.days, c.auto, c.broke, got, c.want)
		}
	}
}
