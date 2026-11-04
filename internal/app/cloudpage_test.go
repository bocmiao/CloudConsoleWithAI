package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent/tencenttest"
)

func TestCloudPage(t *testing.T) {
	a := newApp(t)
	f := tencenttest.Start(t)
	a.TencentEndpoint = f.Endpoint
	a.PollInterval = 10 * time.Millisecond
	ctx := context.Background()
	if _, err := a.CloudDetail(ctx, "ap-guangzhou", "lhins-abc12345"); err == nil {
		t.Fatal("works without credentials")
	}
	if _, err := a.SaveTencent(tencenttest.SecretID, tencenttest.SecretKey); err != nil {
		t.Fatal(err)
	}
	web, _ := a.Store.AddServer(store.Server{Name: "web", Host: "81.68.79.253", Port: 22, Username: "root", AuthKind: "password"})
	// Reached with the automation agent: matched by instance, not by IP.
	shop, _ := a.Store.AddServer(store.Server{Name: "shop", Host: "10.0.0.8", AuthKind: "tat", InstanceID: "ins-xyz98765", Region: "ap-guangzhou"})

	list, err := a.TencentServers(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, s := range list.Servers {
		ids[s.ID] = s.ServerID
	}
	if ids["lhins-abc12345"] != web.ID || ids["ins-xyz98765"] != shop.ID {
		t.Fatalf("matched servers = %v", ids)
	}

	lh, err := a.CloudDetail(ctx, "ap-guangzhou", "lhins-abc12345")
	if err != nil {
		t.Fatal(err)
	}
	if lh.Instance.ServerID != web.ID || lh.Group != "" || len(lh.Firewall) != 2 || lh.FirewallError != "" || lh.SnapshotError != "" {
		t.Fatalf("lighthouse detail = %+v", lh)
	}
	if lh.Instance.RegionName == "" || lh.Instance.TrafficTotal == 0 {
		t.Errorf("instance = %+v", lh.Instance)
	}
	if r := lh.Firewall[1]; r.Port != "80" || r.Protocol != "TCP" || !r.Everyone || r.Source != "0.0.0.0/0" {
		t.Errorf("rule = %+v", r)
	}
	cvm, err := a.CloudDetail(ctx, "ap-guangzhou", "ins-xyz98765")
	if err != nil {
		t.Fatal(err)
	}
	if cvm.Group != "sg-abc" || len(cvm.Firewall) != 1 || cvm.Instance.ServerID != shop.ID {
		t.Fatalf("cvm detail = %+v", cvm)
	}
	for _, bad := range [][2]string{{"ap-guangzhou", "lhins-nothere1"}, {"ap-guangzhou", "../x"}, {"AP;x", "lhins-abc12345"}} {
		if _, err := a.CloudDetail(ctx, bad[0], bad[1]); err == nil {
			t.Errorf("%v: no error", bad)
		}
	}

	run := func(req CloudRequest) PlanView {
		t.Helper()
		v, err := a.ProposeCloud(ctx, req)
		if err != nil {
			t.Fatalf("%+v: %v", req, err)
		}
		if _, err := a.ExecutePlan(v.ID, []int{0}); err != nil {
			t.Fatal(err)
		}
		done := waitPlan(t, a, v.ID)
		if done.Status != core.PlanDone {
			t.Fatalf("%s: %s", done.Title, done.Status)
		}
		return done
	}

	// A port opened for one address, then closed again.
	v := run(CloudRequest{Instance: "lhins-abc12345", Region: "ap-guangzhou", Op: "firewall_open", Port: "8080", CIDR: "203.0.113.7/32", Description: "admin"})
	if v.ServerID != web.ID || v.StepList[0].Capability != "cloud.firewall.open" || !strings.Contains(v.StepList[0].Summary, "203.0.113.7/32") {
		t.Errorf("open plan = %+v", v)
	}
	if rules := f.Firewall["lhins-abc12345"]; len(rules) != 3 || rules[2].Port != "8080" || rules[2].CidrBlock != "203.0.113.7/32" {
		t.Fatalf("firewall after open = %+v", rules)
	}
	run(CloudRequest{Instance: "lhins-abc12345", Region: "ap-guangzhou", Op: "firewall_close", Port: "8080", CIDR: "203.0.113.7/32"})
	if rules := f.Firewall["lhins-abc12345"]; len(rules) != 2 {
		t.Fatalf("firewall after close = %+v", rules)
	}

	run(CloudRequest{Instance: "lhins-abc12345", Region: "ap-guangzhou", Op: "snapshot", Name: "before-upgrade"})
	if len(f.Snaps) != 1 {
		t.Fatalf("snapshots = %v", f.Snaps)
	}
	if d, _ := a.CloudDetail(ctx, "ap-guangzhou", "lhins-abc12345"); len(d.Snapshots) != 1 {
		t.Errorf("snapshots on the page = %+v", d.Snapshots)
	}

	stop, err := a.ProposeCloud(ctx, CloudRequest{Instance: "ins-xyz98765", Region: "ap-guangzhou", Op: "stop"})
	if err != nil {
		t.Fatal(err)
	}
	if s := stop.StepList[0]; s.Capability != "cloud.server.stop" || s.Risk != core.R3 || stop.ServerID != shop.ID {
		t.Errorf("stop plan = %+v", stop)
	}

	for _, bad := range []CloudRequest{
		{Instance: "lhins-abc12345", Region: "ap-guangzhou", Op: "explode"},
		{Instance: "lhins-abc12345", Region: "ap-guangzhou", Op: "firewall_open"},
		{Instance: "lhins-abc12345", Region: "ap-guangzhou", Op: "firewall_open", Port: "80;rm"},
		{Instance: "lhins-abc12345", Region: "ap-guangzhou", Op: "firewall_close", Port: "22"},
		{Instance: "lhins-nothere1", Region: "ap-guangzhou", Op: "start"},
	} {
		if _, err := a.ProposeCloud(ctx, bad); err == nil {
			t.Errorf("%+v: no error", bad)
		}
	}
}
