package app

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent/tencenttest"
)

// A server added from Tencent Cloud with the automation agent: no
// password, everything else works as over SSH. The fake agent runs the
// commands with the local sh, like sshtest does.
func TestServerThroughTAT(t *testing.T) {
	t.Cleanup(actions.UseTestScripts(t.TempDir(), func(string) (string, error) { return fakeSwap, nil }))
	a := newApp(t)
	f := tencenttest.Start(t)
	a.TencentEndpoint = f.Endpoint
	a.PollInterval = 5 * time.Millisecond
	ctx := context.Background()

	req := AddServerRequest{Name: "blog", Host: "81.68.79.253", AuthKind: "tat", InstanceID: "lhins-abc12345", Region: "ap-guangzhou"}
	if _, err := a.AddServer(req); err == nil || !strings.Contains(err.Error(), "腾讯云") {
		t.Fatalf("needs Tencent Cloud keys first: %v", err)
	}
	if _, err := a.SaveTencent(tencenttest.SecretID, tencenttest.SecretKey); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AddServer(AddServerRequest{Host: "1.2.3.4", AuthKind: "tat", InstanceID: "bad"}); err == nil {
		t.Fatal("accepted a server without an instance")
	}
	sv, err := a.AddServer(req)
	if err != nil {
		t.Fatal(err)
	}
	if sv.Username != "root" || sv.InstanceID != "lhins-abc12345" {
		t.Fatalf("server = %+v", sv)
	}

	res, err := a.TestConnection(ctx, sv.ID)
	if err != nil || res.Output == "" {
		t.Fatalf("test connection: %+v %v", res, err)
	}
	if _, prof, err := a.Discover(ctx, sv.ID, nil); err != nil || prof == nil || prof.OS == "" {
		t.Fatalf("discover: %+v %v", prof, err)
	}

	args, _ := json.Marshal(map[string]any{
		"server_id": sv.ID, "title": "加 swap", "reason": "内存不够",
		"steps": []map[string]any{{"capability": "swap.set", "summary": "添加 1G swap", "params": map[string]any{"size_gb": 1}}},
	})
	collector := &planCollector{}
	if _, err := a.toolProposePlan(context.WithValue(ctx, planCollectorKey{}, collector), args); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ExecutePlan(collector.ids[0], []int{0}); err != nil {
		t.Fatal(err)
	}
	st := waitPlan(t, a, collector.ids[0]).StepList[0]
	if st.Status != actions.StatusDone {
		t.Fatalf("step = %+v", st)
	}
	if _, err := a.Rollback(ctx, st.LogID); err != nil {
		t.Fatal(err)
	}
	logs, _ := a.ExecLogs(false)
	sawVia := false
	for _, e := range logs {
		sawVia = sawVia || (e.Title == "测试连接" && e.Via == "腾讯云自动化助手")
	}
	if !sawVia {
		t.Fatal("the log does not say the connection went through TAT")
	}

	f.AgentOnline["lhins-abc12345"] = false
	if _, err := a.TestConnection(ctx, sv.ID); err == nil || !strings.Contains(err.Error(), "不在线") {
		t.Fatalf("agent offline: %v", err)
	}
}

// Over TAT, 1Panel's API is called by curl on the server.
func TestOnePanelThroughTAT(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	a := newApp(t)
	f := tencenttest.Start(t)
	a.TencentEndpoint = f.Endpoint
	a.PollInterval = 5 * time.Millisecond
	if _, err := a.SaveTencent(tencenttest.SecretID, tencenttest.SecretKey); err != nil {
		t.Fatal(err)
	}
	var sawToken bool
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawToken = r.Header.Get("1Panel-Token") != ""
		_, _ = io.WriteString(w, `{"code":200,"data":{"hostname":"blog","os":"ubuntu","platformVersion":"24.04"}}`)
	}))
	defer panel.Close()
	_, port, _ := net.SplitHostPort(panel.Listener.Addr().String())
	p, _ := strconv.Atoi(port)

	sv, err := a.AddServer(AddServerRequest{Name: "blog", Host: "81.68.79.253", AuthKind: "tat", InstanceID: "lhins-abc12345", Region: "ap-guangzhou"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveOnePanel(sv.ID, OnePanelSettings{Port: p}, "key"); err != nil {
		t.Fatal(err)
	}
	info, err := a.TestOnePanel(context.Background(), sv.ID)
	if err != nil || info != "blog ubuntu 24.04" || !sawToken {
		t.Fatalf("1Panel over TAT: %q %v", info, err)
	}
}

func TestEdgeOneSecurityAndPlanTools(t *testing.T) {
	a := newApp(t)
	f := tencenttest.Start(t)
	a.TencentEndpoint = f.Endpoint
	if _, err := a.SaveTencent(tencenttest.SecretID, tencenttest.SecretKey); err != nil {
		t.Fatal(err)
	}
	ctx := withOrigin(context.Background(), OriginAI)
	out, err := a.toolTencentEOSecurity(ctx, json.RawMessage(`{"domain":"blog.example.com"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"「屏蔽海外」", "动作=Deny", "速率限制：\n  没有", "自适应频控（eo.cc.set） 开启=off", "智能客户端过滤 开启=on 动作=Monitor"} {
		if !strings.Contains(out, want) {
			t.Fatalf("security is missing %q:\n%s", want, out)
		}
	}
	out, err = a.toolTencentEO(ctx, json.RawMessage(`{"domain":"new-site.org"}`))
	if err != nil || !strings.Contains(out, "eo.zone.create") || !strings.Contains(out, "套餐 edgeone-free2 基础版 区域=global 状态=normal 还能绑定站点") {
		t.Fatalf("eo without a site: %q %v", out, err)
	}
}
