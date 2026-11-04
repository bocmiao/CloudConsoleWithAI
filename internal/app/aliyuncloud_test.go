package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun/aliyuntest"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

func aliyunApp(t *testing.T) (*App, *aliyuntest.Cloud) {
	t.Helper()
	a := newApp(t)
	f := aliyuntest.StartCloud(t)
	a.AliyunCloudEndpoint = f.Endpoint
	return a, f
}

func TestAliyunSettings(t *testing.T) {
	a, f := aliyunApp(t)
	ctx := context.Background()
	if a.Aliyun().Configured {
		t.Fatal("configured before saving")
	}
	if _, err := a.AliyunServers(ctx, false); err == nil {
		t.Fatal("listed servers without a key")
	}
	for _, bad := range [][2]string{{"short", "0123456789abcdef"}, {"LTAI5tAbCdEfGhIjKlMn", ""}, {"LTAI5t-with-dash-xx", "0123456789abcdef"}} {
		if _, err := a.SaveAliyun(bad[0], bad[1]); err == nil {
			t.Errorf("%v saved", bad)
		}
	}
	id, secret := f.Client().ID, f.Client().Secret
	s, err := a.SaveAliyun(" "+id+" ", secret)
	if err != nil || !s.Configured || strings.Contains(s.AccessKeyID, id[6:len(id)-4]) {
		t.Fatalf("saved = %+v, %v", s, err)
	}
	info, err := a.TestAliyun(ctx)
	if err != nil || !strings.Contains(info, "服务器：3 台") || !strings.Contains(info, "云解析：1 个域名") || !strings.Contains(info, "CDN：1 个加速域名") {
		t.Fatalf("test = %q, %v", info, err)
	}
	if a.ClearAliyun().Configured || a.aliyunClient() != nil {
		t.Fatal("still configured after clearing")
	}
}

func TestAliyunServersPage(t *testing.T) {
	a, f := aliyunApp(t)
	ctx := context.Background()
	if _, err := a.SaveAliyun(f.Client().ID, f.Client().Secret); err != nil {
		t.Fatal(err)
	}
	// The Miao Panel server at the ECS instance's public IP.
	sv, err := a.Store.AddServer(store.Server{Name: "shop", Host: "47.96.1.2", Port: 22, Username: "root", AuthKind: "password"})
	if err != nil {
		t.Fatal(err)
	}
	srv := sv.ID

	list, err := a.AliyunServers(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Servers) != 3 {
		t.Fatalf("servers = %+v", list)
	}
	var shop AliServer
	for _, s := range list.Servers {
		if s.ID == "i-bp1shop000000000001" {
			shop = s
		}
		if s.Provider != "aliyun" {
			t.Errorf("provider = %q", s.Provider)
		}
	}
	if shop.ServerID != srv || shop.RenewFlag != "NOTIFY_AND_AUTO_RENEW" || shop.State != "RUNNING" || shop.RegionName == "" {
		t.Fatalf("shop = %+v", shop)
	}

	d, err := a.AliyunDetail(ctx, "cn-hangzhou", shop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Group != "sg-shop" || len(d.Firewall) != 1 || d.Firewall[0].Port != "22" || !d.Firewall[0].Everyone || d.FirewallError != "" || d.Instance.ServerID != srv {
		t.Fatalf("detail = %+v", d)
	}
	if len(d.Metrics) != 4 {
		t.Errorf("metrics = %+v (%s)", d.Metrics, d.MetricError)
	}
	if _, err := a.AliyunDetail(ctx, "cn-hangzhou", "lhins-abc12345"); err == nil {
		t.Error("a Tencent id was read")
	}

	// A checklist: open 8080 on the Simple Application Server, which is no
	// Miao Panel server, so it is logged as 阿里云.
	p, err := a.ProposeAliyun(ctx, CloudRequest{Instance: "2ad1ae67295445f598017499dc000001", Region: "cn-hangzhou", Op: "firewall_open", Port: "8080"})
	if err != nil {
		t.Fatal(err)
	}
	if p.ServerID != 0 || p.StepList[0].Capability != "aliyun.firewall.open" || !p.StepList[0].Executable {
		t.Fatalf("plan = %+v", p)
	}
	if _, err := a.ExecutePlan(p.ID, []int{0}); err != nil {
		t.Fatal(err)
	}
	done := waitPlan(t, a, p.ID)
	if done.StepList[0].Status != actions.StatusDone {
		t.Fatalf("step = %+v", done.StepList[0])
	}
	logs, _ := a.Store.ListExec(false, 10)
	if len(logs) == 0 || logs[0].ServerName != "阿里云" || logs[0].Via != "阿里云接口" {
		t.Fatalf("log = %+v", logs)
	}
	if _, err := a.ProposeAliyun(ctx, CloudRequest{Instance: shop.ID, Region: "cn-hangzhou", Op: "firewall_close", Port: "22"}); err == nil {
		t.Error("closing SSH was proposed")
	}
	if _, err := a.ProposeAliyun(ctx, CloudRequest{Instance: shop.ID, Region: "cn-hangzhou", Op: "firewall_open", Port: "80,443"}); err == nil {
		t.Error("a port list was proposed")
	}

	text, err := a.toolAliyunServers(ctx, nil)
	if err != nil || !strings.Contains(text, "id=i-bp1shop000000000001") || !strings.Contains(text, "自动续费") || !strings.Contains(text, "流量包") {
		t.Fatalf("tool list = %s, %v", text, err)
	}
	raw, _ := json.Marshal(map[string]string{"instance": shop.ID, "region": "cn-hangzhou"})
	if text, err = a.toolAliyunServers(ctx, raw); err != nil || !strings.Contains(text, "安全组 sg-shop") || !strings.Contains(text, "CPU 使用率") {
		t.Fatalf("tool detail = %s, %v", text, err)
	}
}
