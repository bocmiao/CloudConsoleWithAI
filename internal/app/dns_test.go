package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent/tencenttest"
)

func caps(v PlanView) string {
	var out []string
	for _, s := range v.StepList {
		out = append(out, s.Capability)
	}
	return strings.Join(out, " ")
}

func TestDNSPage(t *testing.T) {
	a := newApp(t)
	f := tencenttest.Start(t)
	a.TencentEndpoint = f.Endpoint
	a.PollInterval = 10 * time.Millisecond
	ctx := context.Background()
	if _, err := a.DNSDomains(ctx); err == nil {
		t.Fatal("works without credentials")
	}
	if _, err := a.SaveTencent(tencenttest.SecretID, tencenttest.SecretKey); err != nil {
		t.Fatal(err)
	}
	web, _ := a.Store.AddServer(store.Server{Name: "web", Host: "81.68.79.253", Port: 22, Username: "root", AuthKind: "password"})
	lan, _ := a.Store.AddServer(store.Server{Name: "lan", Host: "192.168.1.5", Port: 22, Username: "root", AuthKind: "password"})

	ds, err := a.DNSDomains(ctx)
	if err != nil || len(ds.Domains) != 1 || ds.Domains[0].Name != "example.com" || ds.Domains[0].EdgeOne == nil || ds.Domains[0].EdgeOne.Type != "partial" {
		t.Fatalf("domains = %+v %v", ds, err)
	}
	if lines, _ := a.DNSLines(ctx, "example.com"); len(lines) < 3 || lines[0] != tencent.DefaultLine || !strings.Contains(strings.Join(lines, ","), "电信") {
		t.Fatalf("lines = %v", lines)
	}

	run := func(v PlanView) PlanView {
		t.Helper()
		var all []int
		for i := range v.StepList {
			if !v.StepList[i].Executable {
				t.Fatalf("step %d not executable: %+v", i+1, v.StepList[i])
			}
			all = append(all, i)
		}
		if _, err := a.ExecutePlan(v.ID, all); err != nil {
			t.Fatal(err)
		}
		done := waitPlan(t, a, v.ID)
		for i, s := range done.StepList {
			if s.Status != actions.StatusDone {
				t.Fatalf("step %d = %+v", i+1, s)
			}
		}
		return done
	}

	// One click: www straight to an IP.
	v, err := a.ProposeDNS(ctx, DNSRequest{Op: "quick", Domain: "example.com", Sub: "www", Target: "ip", Value: "5.6.7.8"})
	if err != nil || caps(v) != "dns.record.set" || v.ServerID != 0 || !strings.Contains(v.Title, "www.example.com") {
		t.Fatalf("quick = %+v %v", v, err)
	}
	run(v)
	if r := f.Lookup("example.com", "www"); len(r) != 1 || r[0].Type != "A" || r[0].Value != "5.6.7.8" {
		t.Fatalf("www = %+v", r)
	}

	// One click: blog to the web server, through EdgeOne, with HTTPS.
	v, err = a.ProposeDNS(ctx, DNSRequest{Op: "quick", Domain: "example.com", Sub: "blog", Target: "server", ServerID: web.ID, EdgeOne: true, HTTPS: true})
	if err != nil || caps(v) != "eo.domain.add dns.record.set eo.https.set" || !strings.Contains(v.Reason, "81.68.79.253") {
		t.Fatalf("quick EdgeOne = %+v %v", v, err)
	}
	run(v)
	d := f.Domain("blog.example.com")
	if r := f.Lookup("example.com", "blog"); len(r) != 1 || r[0].Type != "CNAME" || d == nil || r[0].Value != d.Cname || d.Origin != "81.68.79.253" || d.CertMode != "eofreecert" {
		t.Fatalf("blog = %+v domain=%+v", r, d)
	}
	recs, err := a.DNSRecords(ctx, "example.com")
	var blog DNSRecordView
	for _, r := range recs.Records {
		if r.Name == "blog" {
			blog = r
		}
	}
	if err != nil || blog.EdgeOne == nil || !blog.EdgeOne.Points || blog.EdgeOne.Origin != "81.68.79.253" || len(recs.Pending) != 0 {
		t.Fatalf("records = %+v %v", recs, err)
	}

	// The same name to another origin changes EdgeOne's origin instead.
	if v, err := a.ProposeDNS(ctx, DNSRequest{Op: "quick", Domain: "example.com", Sub: "blog", Target: "ip", Value: "9.9.9.9", EdgeOne: true}); err != nil ||
		caps(v) != "eo.origin.set dns.record.set" {
		t.Fatalf("new origin = %+v %v", v, err)
	}
	// Straight to the origin again, keeping the EdgeOne domain.
	v, err = a.ProposeDNS(ctx, DNSRequest{Op: "eo_off", Domain: "example.com", ID: blog.ID})
	if err != nil || caps(v) != "dns.record.set" || v.StepList[0].Params["value"] != "81.68.79.253" || v.StepList[0].Params["type"] != "A" {
		t.Fatalf("eo_off = %+v %v", v, err)
	}
	run(v)
	if r := f.Lookup("example.com", "blog"); len(r) != 1 || r[0].Value != "81.68.79.253" || f.Domain("blog.example.com") == nil {
		t.Fatalf("after eo_off: %+v", r)
	}
	// Now EdgeOne has a domain the DNS does not send anyone to.
	recs, _ = a.DNSRecords(ctx, "example.com")
	if len(recs.Pending) != 1 || recs.Pending[0].Sub != "blog" || recs.Pending[0].Current != "A 81.68.79.253" {
		t.Fatalf("pending = %+v", recs.Pending)
	}
	v, err = a.ProposeDNS(ctx, DNSRequest{Op: "eo_point", Domain: "example.com", Sub: "blog"})
	if err != nil || caps(v) != "dns.record.set" || v.StepList[0].Params["point_to"] != "eo" {
		t.Fatalf("eo_point = %+v %v", v, err)
	}
	run(v)
	if recs, _ = a.DNSRecords(ctx, "example.com"); len(recs.Pending) != 0 {
		t.Fatalf("still pending: %+v", recs.Pending)
	}

	// Single records: add a mail record, change and pause www, delete it.
	v, err = a.ProposeDNS(ctx, DNSRequest{Op: "add", Domain: "example.com", Sub: "@", Type: "mx", Value: "mx.example.net"})
	if err != nil || caps(v) != "dns.record.add" || !strings.Contains(v.StepList[0].Summary, "MX 10 mx.example.net") {
		t.Fatalf("add = %+v %v", v, err)
	}
	run(v)
	www := f.Lookup("example.com", "www")[0]
	v, err = a.ProposeDNS(ctx, DNSRequest{Op: "modify", Domain: "example.com", ID: www.RecordID, Sub: "www", Type: "A", Value: "6.6.6.6", Line: "电信"})
	if err != nil || caps(v) != "dns.record.modify" || !strings.Contains(v.StepList[0].Summary, "A 5.6.7.8") {
		t.Fatalf("modify = %+v %v", v, err)
	}
	run(v)
	if r := f.Lookup("example.com", "www")[0]; r.Value != "6.6.6.6" || r.Line != "电信" {
		t.Fatalf("www = %+v", r)
	}
	run(must(a.ProposeDNS(ctx, DNSRequest{Op: "status", Domain: "example.com", ID: www.RecordID, Status: "disable"})))
	if r := f.Lookup("example.com", "www")[0]; r.Status != "DISABLE" {
		t.Fatalf("www = %+v", r)
	}
	v, err = a.ProposeDNS(ctx, DNSRequest{Op: "delete", Domain: "example.com", ID: www.RecordID})
	if err != nil || caps(v) != "dns.record.delete" || !strings.Contains(v.Reason, "打不开") {
		t.Fatalf("delete = %+v %v", v, err)
	}
	run(v)
	if len(f.Lookup("example.com", "www")) != 0 {
		t.Fatal("www not deleted")
	}
	if _, err := a.UndoPlan(ctx, v.ID); err != nil || len(f.Lookup("example.com", "www")) != 1 {
		t.Fatalf("undo delete: %v %+v", err, f.Lookup("example.com", "www"))
	}

	// What cannot be asked for.
	f.Records["example.com"] = append(f.Records["example.com"], tencent.Record{RecordID: 77, Name: "@", Type: "NS", Value: "f1g1ns1.dnspod.net.", Line: tencent.DefaultLine, DefaultNS: true})
	for _, req := range []DNSRequest{
		{Op: "add", Domain: "example.com", Sub: "x", Type: "A", Value: "1.2.3"},
		{Op: "add", Domain: "example.com", Sub: "x", Type: "CAA", Value: "letsencrypt.org"},
		{Op: "delete", Domain: "example.com", ID: 77},
		{Op: "delete", Domain: "example.com", ID: 12345},
		{Op: "quick", Domain: "example.com", Sub: "lan", Target: "server", ServerID: lan.ID},
		{Op: "quick", Domain: "example.com", Sub: "x", Target: "ip", Value: "10.0.0.1", EdgeOne: true},
		{Op: "quick", Domain: "example.com", Sub: "x", Target: "host", Value: "not a host"},
		{Op: "eo_point", Domain: "example.com", Sub: "nothing"},
		{Op: "drop", Domain: "example.com"},
	} {
		if _, err := a.ProposeDNS(ctx, req); err == nil || !isUserErr(err) {
			t.Errorf("%+v accepted: %v", req, err)
		}
	}

	// A domain not in EdgeOne yet gets its site first, if a plan can take it.
	f.Records["other.com"] = []tencent.Record{}
	if v, err := a.ProposeDNS(ctx, DNSRequest{Op: "quick", Domain: "other.com", Sub: "@", Target: "ip", Value: "8.8.4.4", EdgeOne: true, Area: "overseas"}); err != nil ||
		caps(v) != "eo.zone.create eo.domain.add dns.record.set" || v.StepList[0].Params["area"] != "overseas" {
		t.Fatalf("new site = %+v %v", v, err)
	}
	f.Plans = nil
	if _, err := a.ProposeDNS(ctx, DNSRequest{Op: "quick", Domain: "other.com", Sub: "@", Target: "ip", Value: "8.8.4.4", EdgeOne: true}); err == nil || !strings.Contains(err.Error(), "套餐") {
		t.Fatalf("no plan to bind: %v", err)
	}
	// A site on EdgeOne's own name servers is managed there.
	f.Zones = append(f.Zones, tencent.Zone{ZoneID: "zone-ns", ZoneName: "ns.com", Type: "full", Status: "active"})
	f.Records["ns.com"] = []tencent.Record{}
	if _, err := a.ProposeDNS(ctx, DNSRequest{Op: "quick", Domain: "ns.com", Sub: "www", Target: "ip", Value: "8.8.4.4", EdgeOne: true}); err == nil || !strings.Contains(err.Error(), "NS") {
		t.Fatalf("NS access: %v", err)
	}
}

func must(v PlanView, err error) PlanView {
	if err != nil {
		panic(err)
	}
	return v
}

func isUserErr(err error) bool {
	_, ok := err.(*UserError)
	return ok
}
