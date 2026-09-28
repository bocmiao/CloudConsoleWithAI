package aliyun_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun"
	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun/aliyuntest"
)

func TestDomainsAndRecordsPaging(t *testing.T) {
	f := aliyuntest.StartCloud(t)
	c := f.Client()
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		name := fmt.Sprintf("site%d.cn", i)
		f.Domains = append(f.Domains, &aliyuntest.DNSDomain{ID: fmt.Sprintf("dom-%d", i), Name: name, Edition: "免费版", EditionCode: "mianfei", MinTTL: 600})
	}
	for i := 0; i < 7; i++ {
		f.Records["site0.cn"] = append(f.Records["site0.cn"], &aliyuntest.DNSRecord{ID: fmt.Sprint(100 + i), RR: fmt.Sprintf("n%d", i),
			Type: "A", Value: "1.2.3.4", Line: "default", Status: "ENABLE", TTL: 600})
	}
	f.PageSize = 2
	domains, err := c.Domains(ctx)
	if err != nil || len(domains) != 5 {
		t.Fatalf("domains = %+v, %v", domains, err)
	}
	if d := domains[0]; d.Name != "example.com" || d.RecordCount != 2 || d.Edition != "免费版" || len(d.DNSServers) != 2 || d.ID == "" {
		t.Errorf("example.com = %+v", d)
	}
	recs, err := c.Records(ctx, "site0.cn")
	if err != nil || len(recs) != 7 || recs[6].Name != "n6" || recs[6].Status != "enabled" {
		t.Fatalf("records = %+v, %v", recs, err)
	}
	recs, _ = c.Records(ctx, "example.com")
	if len(recs) != 2 || recs[1].Type != "MX" || recs[1].Priority != 5 || recs[1].Name != "@" || recs[0].Priority != 0 || recs[0].TTL != 600 || recs[0].Line != "default" {
		t.Fatalf("example.com records = %+v", recs)
	}
	if _, err := c.Records(ctx, "not-mine.com"); !aliyun.IsCode(err, "InvalidDomainName.NoExist") || !strings.Contains(err.Error(), "不在这个阿里云账号") {
		t.Fatalf("unknown domain: %v", err)
	}
	info, err := c.DomainInfo(ctx, "example.com")
	if err != nil || info.MinTTL != 600 || info.EditionCode != "mianfei" || len(info.Lines) != len(aliyun.Lines) || info.Lines[1] != (aliyun.Line{Code: "telecom", Name: "电信"}) {
		t.Fatalf("info = %+v, %v", info, err)
	}
}

func TestRecordChanges(t *testing.T) {
	f := aliyuntest.StartCloud(t)
	c := f.Client()
	ctx := context.Background()
	id, err := c.AddRecord(ctx, "example.com", aliyun.Record{Name: "api", Type: "A", Value: "47.96.1.3", Remark: "接口", Status: "disabled"})
	if err != nil || id == "" {
		t.Fatalf("add: %q %v", id, err)
	}
	if r := f.Record("example.com", id); r == nil || r.Remark != "接口" || r.Status != "DISABLE" || r.TTL != 600 || r.Line != "default" {
		t.Fatalf("stored %+v", r)
	}
	got, err := c.Record(ctx, id)
	if err != nil || got.Name != "api" || got.Status != "disabled" || got.Remark != "接口" {
		t.Fatalf("record = %+v, %v", got, err)
	}
	if _, err := c.AddRecord(ctx, "example.com", aliyun.Record{Name: "@", Type: "mx", Value: "mx2.example.net"}); err != nil {
		t.Fatalf("MX without priority: %v", err)
	}
	if _, err := c.AddRecord(ctx, "example.com", aliyun.Record{Name: "api", Type: "A", Value: "47.96.1.3"}); !aliyun.IsCode(err, "DomainRecordDuplicate") || !strings.Contains(err.Error(), "完全一样") {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := c.AddRecord(ctx, "example.com", aliyun.Record{Name: "www", Type: "CNAME", Value: "cdn.example.com.w.kunlunsl.com"}); !aliyun.IsCode(err, "DomainRecordConflict") {
		t.Fatalf("CNAME next to A: %v", err)
	}
	if _, err := c.AddRecord(ctx, "example.com", aliyun.Record{Name: "fast", Type: "A", Value: "1.1.1.1", TTL: 60}); !aliyun.IsCode(err, "InvalidTTL") {
		t.Fatalf("TTL under the edition's minimum: %v", err)
	}

	// Only the remark changes: the record itself is not re-sent.
	got.Remark = "新接口"
	if err := c.UpdateRecord(ctx, got); err != nil {
		t.Fatal(err)
	}
	got.Value, got.TTL, got.Line, got.Status = "47.96.1.9", 1800, "telecom", "enabled"
	if err := c.UpdateRecord(ctx, got); err != nil {
		t.Fatal(err)
	}
	if r := f.Record("example.com", id); r.Value != "47.96.1.9" || r.TTL != 1800 || r.Line != "telecom" || r.Remark != "新接口" || r.Status != "ENABLE" {
		t.Fatalf("after update %+v", r)
	}
	// Left out, line and TTL stay as they are.
	if err := c.UpdateRecord(ctx, aliyun.Record{ID: id, Name: "api", Type: "A", Value: "47.96.1.10", Remark: "新接口"}); err != nil {
		t.Fatal(err)
	}
	if r := f.Record("example.com", id); r.Value != "47.96.1.10" || r.TTL != 1800 || r.Line != "telecom" {
		t.Fatalf("after partial update %+v", r)
	}
	if err := c.SetRecordStatus(ctx, id, false); err != nil || f.Record("example.com", id).Status != "DISABLE" {
		t.Fatalf("pause: %v", err)
	}
	if err := c.SetRecordStatus(ctx, id, true); err != nil || f.Record("example.com", id).Status != "ENABLE" {
		t.Fatalf("resume: %v", err)
	}
	if err := c.SetRecordRemark(ctx, id, ""); err != nil || f.Record("example.com", id).Remark != "" {
		t.Fatalf("clear remark: %v", err)
	}
	if err := c.DeleteRecord(ctx, id); err != nil || f.Record("example.com", id) != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := c.DeleteRecord(ctx, id); !aliyun.IsCode(err, "DomainRecordNotBelongToUser") {
		t.Fatalf("delete twice: %v", err)
	}
}
