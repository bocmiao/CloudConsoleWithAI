package app

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent/tencenttest"
)

func TestOriginProtocolPlanKeepsAddress(t *testing.T) {
	a := newApp(t)
	f := tencenttest.Start(t)
	a.TencentEndpoint = f.Endpoint
	if _, err := a.SaveTencent(tencenttest.SecretID, tencenttest.SecretKey); err != nil {
		t.Fatal(err)
	}
	f.Domains = append(f.Domains, &tencenttest.Domain{Zone: "zone-abc", Name: "blog.example.com", Origin: "81.68.79.253", Protocol: "HTTPS", Status: "online"})
	v, err := a.ProposeDNS(context.Background(), DNSRequest{Op: "eo_origin", Domain: "example.com", Sub: "blog", Protocol: "HTTP"})
	if err != nil || caps(v) != "eo.origin.set" || v.StepList[0].Params["origin"] != "81.68.79.253" || !strings.Contains(v.Reason, "不再使用 TLS") {
		t.Fatalf("origin plan: %+v %v", v, err)
	}
	if _, err := a.ProposeDNS(context.Background(), DNSRequest{Op: "eo_origin", Domain: "example.com", Sub: "blog", Protocol: "HTTPS"}); err == nil {
		t.Fatal("same protocol should not create a plan")
	}
}

func TestServerFirewallPlanChecksGroup(t *testing.T) {
	a := newApp(t)
	f := tencenttest.Start(t)
	a.TencentEndpoint = f.Endpoint
	if _, err := a.SaveTencent(tencenttest.SecretID, tencenttest.SecretKey); err != nil {
		t.Fatal(err)
	}
	sv, err := a.Store.AddServer(store.Server{Name: "shop", Host: "43.1.2.3", Port: 2222, Username: "admin", AuthKind: "key"})
	if err != nil {
		t.Fatal(err)
	}
	f.Firewall["sg-abc"] = []tencent.FirewallRule{{Protocol: "ALL", Port: "ALL", CidrBlock: "0.0.0.0/0", Action: "ACCEPT"}}
	v, err := a.ProposeServerSecurity(context.Background(), sv.ID, SecurityRequest{Op: "firewall", AdminCIDR: "203.0.113.4/32"})
	if err != nil || caps(v) != "cloud.firewall.tighten" || fmt.Sprint(v.StepList[0].Params["ssh_port"]) != "2222" {
		t.Fatalf("firewall plan: %+v %v", v, err)
	}
	if _, err := a.ProposeServerSecurity(context.Background(), sv.ID, SecurityRequest{Op: "firewall", AdminCIDR: "0.0.0.0/0"}); err == nil {
		t.Fatal("unrestricted admin source accepted")
	}
}

func TestServerBackupPlanOrdersDatabaseHaloSnapshot(t *testing.T) {
	a := newApp(t)
	f := tencenttest.Start(t)
	a.TencentEndpoint = f.Endpoint
	if _, err := a.SaveTencent(tencenttest.SecretID, tencenttest.SecretKey); err != nil {
		t.Fatal(err)
	}
	sv, err := a.Store.AddServer(store.Server{Name: "blog", Host: "81.68.79.253", Port: 22, Username: "admin", AuthKind: "key"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Store.SaveProfile(sv.ID, "== panel ==\n1panel: yes\n1panel_apps: mysql/mysql halo/halo\n", "1panel"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveOnePanel(sv.ID, OnePanelSettings{Port: 13940}, "test-key"); err != nil {
		t.Fatal(err)
	}
	v, err := a.ProposeServerSecurity(context.Background(), sv.ID, SecurityRequest{Op: "backup", DatabaseApp: "mysql", HaloApp: "halo"})
	if err != nil || caps(v) != "backup.create backup.create cloud.snapshot.create" {
		t.Fatalf("backup plan: %+v %v", v, err)
	}
	for _, step := range v.StepList {
		if !step.Executable {
			t.Fatalf("blocked backup step: %+v", step)
		}
	}
	snap, err := a.ProposeServerSecurity(context.Background(), sv.ID, SecurityRequest{Op: "snapshot"})
	if err != nil || caps(snap) != "cloud.snapshot.create" {
		t.Fatalf("snapshot only: %+v %v", snap, err)
	}
}
