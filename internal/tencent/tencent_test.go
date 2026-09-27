package tencent_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent/tencenttest"
)

func TestSignedCallsAndErrors(t *testing.T) {
	f := tencenttest.Start(t)
	ctx := context.Background()
	c := f.Client()
	var traced []string
	c.Trace = func(service, action string, body []byte) {
		traced = append(traced, service+" "+action+" "+string(body))
	}
	domains, err := c.Domains(ctx)
	if err != nil || len(domains) != 1 || domains[0].Name != "example.com" {
		t.Fatalf("domains = %+v, %v", domains, err)
	}
	if len(traced) != 1 || strings.Contains(traced[0], tencenttest.SecretKey) {
		t.Fatalf("trace = %v", traced)
	}
	recs, err := c.Records(ctx, "example.com", "blog")
	if err != nil || len(recs) != 1 || recs[0].Value != "1.2.3.4" {
		t.Fatalf("records = %+v, %v", recs, err)
	}

	bad := tencent.New(tencenttest.SecretID, "wrong-key")
	bad.Endpoint = f.Endpoint
	if _, err := bad.Domains(ctx); err == nil || !strings.Contains(err.Error(), "SecretKey 不对") {
		t.Fatalf("wrong key: %v", err)
	}
	f.DeniedService = "teo"
	if _, err := c.Zones(ctx); err == nil || !strings.Contains(err.Error(), "没有 EdgeOne 的权限") {
		t.Fatalf("denied: %v", err)
	}
}

func TestZoneFor(t *testing.T) {
	zones := []tencent.Zone{{ZoneID: "a", ZoneName: "example.com"}, {ZoneID: "b", ZoneName: "cn.example.com"}}
	for name, want := range map[string]string{
		"example.com": "a", "blog.example.com": "a", "x.cn.example.com": "b", "cn.example.com": "b",
		"notexample.com": "", "example.com.evil.net": "",
	} {
		z, _ := tencent.ZoneFor(zones, name)
		if z.ZoneID != want {
			t.Errorf("ZoneFor(%s) = %q, want %q", name, z.ZoneID, want)
		}
	}
}

// Every page of records is read.
func TestRecordsPaging(t *testing.T) {
	f := tencenttest.Start(t)
	f.PageSize = 3
	var recs []tencenttest.Record
	for i := 0; i < 8; i++ {
		recs = append(recs, tencenttest.Record{RecordID: uint64(i + 1), Name: fmt.Sprintf("n%d", i), Type: "A", Value: "1.2.3.4", Line: "默认", TTL: 600, Status: "ENABLE"})
	}
	f.Records["paged.example"] = recs
	c := tencent.New(tencenttest.SecretID, tencenttest.SecretKey)
	c.Endpoint = f.Endpoint
	got, err := c.Records(context.Background(), "paged.example", "")
	if err != nil || len(got) != 8 {
		t.Fatalf("records = %d %v", len(got), err)
	}
}

func TestSendSMS(t *testing.T) {
	f := tencenttest.Start(t)
	c := tencent.New(tencenttest.SecretID, tencenttest.SecretKey)
	c.Endpoint = f.Endpoint
	s := tencent.SMS{AppID: "1400000000", Sign: "喵面板", Template: "123456", Params: 2}
	if err := c.SendSMS(context.Background(), s, "+8613800138000", "654321", 10); err != nil {
		t.Fatal(err)
	}
	if len(f.SMS) != 1 || f.SMS[0].Phone != "+8613800138000" || strings.Join(f.SMS[0].Params, ",") != "654321,10" || f.SMS[0].Region != "ap-guangzhou" {
		t.Fatalf("sent %+v", f.SMS)
	}
	s.Params = 1
	if err := c.SendSMS(context.Background(), s, "+8613800138000", "654321", 10); err == nil || !strings.Contains(err.Error(), "变量的个数") {
		t.Fatalf("wrong variable count: %v", err)
	}
	s.Params, s.Sign = 2, "别人的签名"
	if err := c.SendSMS(context.Background(), s, "+8613800138000", "654321", 10); err == nil || !strings.Contains(err.Error(), "签名") {
		t.Fatalf("wrong signature: %v", err)
	}
}
