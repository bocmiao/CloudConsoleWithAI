package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent/tencenttest"
)

func TestAlidnsPage(t *testing.T) {
	a, f := aliyunApp(t)
	ctx := context.Background()
	if _, err := a.SaveAliyun(f.Client().ID, f.Client().Secret); err != nil {
		t.Fatal(err)
	}
	// 阿里云 alone.
	v, err := a.DNSDomains(ctx)
	if err != nil || len(v.Domains) != 1 || v.Domains[0].Provider != "alidns" || v.Domains[0].Name != "example.com" {
		t.Fatalf("domains = %+v, %v", v, err)
	}
	recs, err := a.DNSRecords(ctx, "example.com", "alidns")
	if err != nil || len(recs.Records) != 2 || recs.Provider != "alidns" {
		t.Fatalf("records = %+v, %v", recs, err)
	}
	var www DNSRecordView
	for _, r := range recs.Records {
		if r.Name == "www" {
			www = r
		}
	}
	if www.RID == "" || www.Line != "默认" || !www.Enabled {
		t.Fatalf("www = %+v", www)
	}
	if lines, _ := a.DNSLines(ctx, "example.com", "alidns"); len(lines) < 2 || lines[0] != "默认" {
		t.Errorf("lines = %v", lines)
	}

	run := func(req DNSRequest) PlanView {
		t.Helper()
		req.Domain, req.Provider = "example.com", "alidns"
		p, err := a.ProposeDNS(ctx, req)
		if err != nil {
			t.Fatalf("%s: %v", req.Op, err)
		}
		if _, err := a.ExecutePlan(p.ID, []int{0}); err != nil {
			t.Fatal(err)
		}
		done := waitPlan(t, a, p.ID)
		if done.StepList[0].Status != actions.StatusDone {
			t.Fatalf("%s: %+v", req.Op, done.StepList[0])
		}
		return done
	}
	run(DNSRequest{Op: "add", Sub: "api", Type: "A", Value: "203.0.113.5", Line: "电信"})
	run(DNSRequest{Op: "status", RID: www.RID, Status: "disable"})
	p := run(DNSRequest{Op: "quick", Sub: "blog", Target: "ip", Value: "203.0.113.6"})
	if p.StepList[0].Capability != "aliyun.dns.record.set" {
		t.Errorf("quick step = %s", p.StepList[0].Capability)
	}
	recs, _ = a.DNSRecords(ctx, "example.com", "alidns")
	names := []string{}
	for _, r := range recs.Records {
		names = append(names, r.Name+":"+r.Line+":"+strings.ToLower(map[bool]string{true: "on", false: "off"}[r.Enabled]))
	}
	got := strings.Join(names, " ")
	for _, want := range []string{"api:电信:on", "www:默认:off", "blog:默认:on"} {
		if !strings.Contains(got, want) {
			t.Errorf("records %s lack %s", got, want)
		}
	}
	if _, err := a.ProposeDNS(ctx, DNSRequest{Domain: "example.com", Provider: "alidns", Op: "quick", Target: "ip", Value: "203.0.113.6", EdgeOne: true}); err == nil {
		t.Error("EdgeOne on an Alidns domain was proposed")
	}
	if _, err := a.ProposeDNS(ctx, DNSRequest{Domain: "example.com", Provider: "alidns", Op: "eo_point"}); err == nil {
		t.Error("eo_point on an Alidns domain was proposed")
	}
	logs, _ := a.Store.ListExec(false, 5)
	if len(logs) == 0 || logs[0].ServerName != "阿里云" {
		t.Errorf("logged under %q", logs[0].ServerName)
	}
	raw, _ := json.Marshal(map[string]string{"domain": "example.com"})
	if text, err := a.toolAliyunDNS(ctx, raw); err != nil || !strings.Contains(text, "api A 203.0.113.5 线路=电信") {
		t.Errorf("tool = %s, %v", text, err)
	}

	// With Tencent Cloud as well: DNSPod's example.com is the one listed.
	tf := tencenttest.Start(t)
	a.TencentEndpoint = tf.Endpoint
	if _, err := a.SaveTencent(tencenttest.SecretID, tencenttest.SecretKey); err != nil {
		t.Fatal(err)
	}
	v, err = a.DNSDomains(ctx)
	if err != nil || len(v.Domains) != 1 || v.Domains[0].Provider != "dnspod" {
		t.Fatalf("both = %+v, %v", v, err)
	}
}
