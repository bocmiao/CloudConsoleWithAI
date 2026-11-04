package aliyun_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun"
	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun/aliyuntest"
)

const (
	shop = "i-bp1shop000000000001"
	db   = "i-2zedb000000000000002"
	blog = "2ad1ae67295445f598017499dc000001"
)

func TestServers(t *testing.T) {
	f := aliyuntest.StartCloud(t)
	list, errs := f.Client().Servers(context.Background())
	if len(errs) > 0 || len(list) != 3 {
		t.Fatalf("servers = %+v, %v", list, errs)
	}
	if list[0].ID != db || list[1].ID != shop || list[2].ID != blog {
		t.Fatalf("order: %s %s %s", list[0].ID, list[1].ID, list[2].ID)
	}
	s := list[1]
	if s.Kind != aliyun.KindECS || s.Name != "shop" || s.Region != "cn-hangzhou" || s.RegionName != "华东1（杭州）" || s.Zone != "cn-hangzhou-i" ||
		s.State != "RUNNING" || s.Spec != "ecs.e-c1m2.large" || s.CPU != 2 || s.MemoryGB != 4 || s.DiskGB != 40 || s.SystemDiskID != "d-bp1shopsys" ||
		s.BandwidthMbps != 5 || strings.Join(s.PublicIPs, ",") != "47.96.1.2" || strings.Join(s.PrivateIPs, ",") != "172.16.0.10" ||
		s.ChargeType != "PREPAID" || s.AutoRenew == nil || !*s.AutoRenew || strings.Join(s.SecurityGroups, ",") != "sg-shop" {
		t.Errorf("shop = %+v", s)
	}
	if exp, err := time.Parse(time.RFC3339, s.ExpiredTime); err != nil || time.Until(exp) < 29*24*time.Hour {
		t.Errorf("shop expires %q", s.ExpiredTime)
	}
	if d := list[0]; d.State != "STOPPED" || d.ChargeType != "POSTPAID" || d.ExpiredTime != "" || d.AutoRenew != nil || d.MemoryGB != 16 || len(d.PublicIPs) != 0 {
		t.Errorf("db = %+v", d)
	}
	b := list[2]
	if b.Kind != aliyun.KindSWAS || b.Name != "blog" || b.State != "RUNNING" || b.Spec != "swas.s.c2m2s50b4t1" || b.CPU != 2 || b.MemoryGB != 2 ||
		b.DiskGB != 50 || b.SystemDiskID != "d-swasblogsys" || b.BandwidthMbps != 4 || strings.Join(b.PublicIPs, ",") != "121.40.1.2" ||
		b.OS != "Ubuntu 22.04" || b.ChargeType != "PREPAID" || b.TrafficUsed != 300<<30 || b.TrafficTotal != 1024<<30 || b.ExpiredTime == "" {
		t.Errorf("blog = %+v", b)
	}
}

// Every page of instances is read, however small the pages.
func TestServersPaging(t *testing.T) {
	f := aliyuntest.StartCloud(t)
	f.PageSize = 1
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("i-bp1more%012d", i)
		f.ECS[id] = &aliyuntest.ECSInstance{ID: id, Name: fmt.Sprintf("web%d", i), Region: "cn-hangzhou", Status: "Running", CPU: 1, MemoryMiB: 1024,
			DiskGB: 20, DiskID: fmt.Sprintf("d-more%d", i), Group: "sg-shop", ChargeType: "PostPaid", ExpiredTime: "2099-12-31T15:59Z"}
		sid := fmt.Sprintf("3ad1ae67295445f598017499dc00000%d", i)
		f.SWAS[sid] = &aliyuntest.SWASInstance{ID: sid, Name: fmt.Sprintf("site%d", i), Region: "cn-shanghai", Status: "Running", CPU: 1, MemoryGB: 0.5,
			DiskGB: 40, DiskID: fmt.Sprintf("d-site%d", i), TrafficTotal: 200 << 30}
	}
	list, errs := f.Client().Servers(context.Background())
	if len(errs) > 0 || len(list) != 9 {
		t.Fatalf("got %d servers, %v", len(list), errs)
	}
	for _, s := range list {
		if s.Kind == aliyun.KindECS && s.DiskGB == 0 {
			t.Errorf("%s has no disk size: system disks were not paged", s.Name)
		}
		if s.Name == "site1" && (s.MemoryGB != 0.5 || s.TrafficTotal != 200<<30) {
			t.Errorf("site1 = %+v", s)
		}
	}
}

// A failing region or product is reported next to what could be read.
func TestServersPartial(t *testing.T) {
	f := aliyuntest.StartCloud(t)
	f.FailRegions = map[string]bool{"ecs cn-beijing": true}
	f.Denied = map[string]bool{aliyun.ProductSWAS: true}
	list, errs := f.Client().Servers(context.Background())
	if len(list) != 1 || list[0].ID != shop {
		t.Fatalf("servers = %+v", list)
	}
	var text []string
	for _, e := range errs {
		text = append(text, e.Error())
	}
	joined := strings.Join(text, "\n")
	if len(errs) != 2 || !strings.Contains(joined, "InternalError") || !strings.Contains(joined, "AliyunSWASFullAccess") {
		t.Fatalf("errs = %s", joined)
	}
}

func TestInstanceAndPower(t *testing.T) {
	f := aliyuntest.StartCloud(t)
	c := f.Client()
	ctx := context.Background()
	s, ok, err := c.Instance(ctx, "cn-hangzhou", shop)
	if err != nil || !ok || s.Name != "shop" || s.DiskGB != 40 || s.AutoRenew == nil {
		t.Fatalf("instance = %+v %v %v", s, ok, err)
	}
	if _, ok, err := c.Instance(ctx, "cn-beijing", shop); ok || err != nil {
		t.Fatalf("shop is not in Beijing: %v %v", ok, err)
	}
	if err := c.Power(ctx, "cn-hangzhou", shop, "stop"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"STOPPING", "STOPPED"} {
		if s, _, _ := c.Instance(ctx, "cn-hangzhou", shop); s.State != want {
			t.Fatalf("after stop: %s, want %s", s.State, want)
		}
	}
	if err := c.Power(ctx, "cn-beijing", db, "start"); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := c.Instance(ctx, "cn-beijing", db); s.State != "STARTING" {
		t.Fatalf("db = %s", s.State)
	}
	if err := c.Power(ctx, "cn-hangzhou", blog, "reboot"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"STARTING", "RUNNING"} {
		if s, _, _ := c.Instance(ctx, "cn-hangzhou", blog); s.State != want {
			t.Fatalf("after reboot: %s, want %s", s.State, want)
		}
	}
	if err := c.Power(ctx, "cn-hangzhou", blog, "start"); !aliyun.IsCode(err, "IncorrectInstanceStatus") || !strings.Contains(err.Error(), "状态") {
		t.Fatalf("start a running server: %v", err)
	}
	if err := c.Power(ctx, "cn-hangzhou", blog, "halt"); err == nil {
		t.Fatal("unknown action accepted")
	}
}

func TestSWASFirewall(t *testing.T) {
	f := aliyuntest.StartCloud(t)
	c := f.Client()
	ctx := context.Background()
	s, _, _ := c.Instance(ctx, "cn-hangzhou", blog)
	rules, err := c.Firewall(ctx, s)
	if err != nil || len(rules) != 2 || rules[0].Port != "22" || rules[0].Source != "0.0.0.0/0" || rules[0].Policy != "accept" || rules[0].Description != "SSH" {
		t.Fatalf("rules = %+v, %v", rules, err)
	}
	ids, err := c.AddSWASFirewall(ctx, "cn-hangzhou", blog,
		aliyun.FirewallRule{Protocol: "tcp", Port: "8000-9000", Source: "1.2.3.0/24", Description: "app"},
		aliyun.FirewallRule{Protocol: "ICMP"})
	if err != nil || len(ids) != 2 {
		t.Fatalf("add: %v %v", ids, err)
	}
	f.PageSize = 2
	rules, _ = c.Firewall(ctx, s)
	if len(rules) != 4 || rules[2].Port != "8000-9000" || rules[2].Protocol != "TCP" || rules[2].Source != "1.2.3.0/24" || rules[2].ID != ids[0] || rules[3].Port != "" {
		t.Fatalf("after add: %+v", rules)
	}
	if err := c.DeleteFirewallRule(ctx, s, rules[2]); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteSWASFirewall(ctx, "cn-hangzhou", blog, rules[3].ID, rules[1].ID); err != nil {
		t.Fatal(err)
	}
	if rules, _ = c.Firewall(ctx, s); len(rules) != 1 || rules[0].Port != "22" {
		t.Fatalf("after delete: %+v", rules)
	}
	if err := c.AddFirewallRule(ctx, s, aliyun.FirewallRule{Protocol: "TCP", Port: "25", Policy: "drop"}); err == nil {
		t.Fatal("drop rule accepted")
	}
	if err := c.DeleteSWASFirewall(ctx, "cn-hangzhou", blog, "no-such-rule"); err == nil {
		t.Fatal("deleted a rule that does not exist")
	}
}

func TestSecurityGroup(t *testing.T) {
	f := aliyuntest.StartCloud(t)
	c := f.Client()
	ctx := context.Background()
	s, _, _ := c.Instance(ctx, "cn-hangzhou", shop)
	rules, err := c.Firewall(ctx, s)
	if err != nil || len(rules) != 1 || rules[0].Port != "22" || rules[0].Protocol != "TCP" || rules[0].Group != "sg-shop" ||
		rules[0].Policy != "accept" || rules[0].Priority != 1 || rules[0].ID != "sgr-shop22" {
		t.Fatalf("rules = %+v, %v", rules, err)
	}
	for _, r := range []aliyun.FirewallRule{
		{Protocol: "TCP", Port: "443", Description: "HTTPS"},
		{Protocol: "UDP", Port: "3000-3100", Source: "10.0.0.0/8", Priority: 5},
		{Protocol: "ALL", Source: "sg-db", Policy: "drop", Priority: 100},
		{Protocol: "TCP", Port: "80", Source: "2001:db8::/32"},
	} {
		if err := c.AddFirewallRule(ctx, s, r); err != nil {
			t.Fatalf("add %+v: %v", r, err)
		}
	}
	f.PageSize = 2
	rules, err = c.Firewall(ctx, s)
	if err != nil || len(rules) != 5 {
		t.Fatalf("after add: %+v, %v", rules, err)
	}
	got := map[string]aliyun.FirewallRule{}
	for _, r := range rules {
		got[r.Protocol+" "+r.Port] = r
	}
	if r := got["TCP 443"]; r.Source != "0.0.0.0/0" || r.Description != "HTTPS" || r.ID == "" {
		t.Errorf("443 = %+v", r)
	}
	if r := got["UDP 3000-3100"]; r.Source != "10.0.0.0/8" || r.Priority != 5 {
		t.Errorf("udp = %+v", r)
	}
	if r := got["ALL "]; r.Source != "sg-db" || r.Policy != "drop" || r.Priority != 100 {
		t.Errorf("all = %+v", r)
	}
	if r := got["TCP 80"]; r.Source != "2001:db8::/32" {
		t.Errorf("ipv6 = %+v", r)
	}
	if err := c.DeleteFirewallRule(ctx, s, got["TCP 443"]); err != nil {
		t.Fatal(err)
	}
	if err := c.RevokeIngress(ctx, "cn-hangzhou", "sg-shop", got["ALL "].ID, got["TCP 80"].ID); err != nil {
		t.Fatal(err)
	}
	if rules, _ := c.SecurityGroupIngress(ctx, "cn-hangzhou", "sg-shop"); len(rules) != 2 {
		t.Fatalf("after delete: %+v", rules)
	}
	if err := c.AuthorizeIngress(ctx, "cn-hangzhou", "sg-shop", aliyun.FirewallRule{Protocol: "TCP", Port: "70000"}); !aliyun.IsCode(err, "InvalidPortRange") {
		t.Fatalf("bad port: %v", err)
	}
	if err := c.DeleteFirewallRule(ctx, s, aliyun.FirewallRule{}); err == nil {
		t.Fatal("deleted a rule without an ID")
	}
}

func TestSnapshots(t *testing.T) {
	f := aliyuntest.StartCloud(t)
	c := f.Client()
	ctx := context.Background()
	for _, s := range []aliyun.Server{
		{Kind: aliyun.KindSWAS, ID: blog, Region: "cn-hangzhou"},                                      // system disk found by ListDisks
		{Kind: aliyun.KindECS, ID: shop, Region: "cn-hangzhou"},                                       // … by DescribeDisks
		{Kind: aliyun.KindECS, ID: db, Region: "cn-beijing", SystemDiskID: "d-2zedbsys", DiskGB: 100}, // as listed
	} {
		id, err := c.CreateSnapshot(ctx, s, "")
		if err != nil || id == "" {
			t.Fatalf("%s: create: %q %v", s.ID, id, err)
		}
		if _, err := c.CreateSnapshot(ctx, s, "before-upgrade"); err != nil {
			t.Fatalf("%s: named: %v", s.ID, err)
		}
		f.PageSize = 1
		list, err := c.Snapshots(ctx, s)
		f.PageSize = 0
		if err != nil || len(list) != 2 || list[0].ID != id || !strings.HasPrefix(list[0].Name, "miao-") || list[1].Name != "before-upgrade" ||
			list[0].State != "PROGRESSING" || list[0].Progress != 50 || list[0].Created == "" || list[0].DiskID == "" {
			t.Fatalf("%s: snapshots = %+v, %v", s.ID, list, err)
		}
		list, _ = c.Snapshots(ctx, s)
		if list[0].State != "ACCOMPLISHED" || list[0].Progress != 100 {
			t.Fatalf("%s: later = %+v", s.ID, list[0])
		}
		if s.Kind == aliyun.KindECS && list[0].SizeGB == 0 {
			t.Errorf("%s: no size", s.ID)
		}
	}
	if _, err := c.CreateSnapshot(ctx, aliyun.Server{Kind: aliyun.KindSWAS, ID: blog, Region: "cn-hangzhou"}, "升级前"); !aliyun.IsCode(err, "InvalidSnapshotName") {
		t.Fatalf("SWAS takes ASCII names only: %v", err)
	}
	if _, err := c.CreateSnapshot(ctx, aliyun.Server{Kind: aliyun.KindECS, ID: shop, Region: "cn-hangzhou"}, "auto-1"); !aliyun.IsCode(err, "InvalidSnapshotName") {
		t.Fatalf("ECS names cannot start with auto: %v", err)
	}
}

func TestMonitorData(t *testing.T) {
	f := aliyuntest.StartCloud(t)
	c := f.Client()
	ctx := context.Background()
	end := time.Now().Truncate(time.Minute)
	start := end.Add(-10 * time.Minute)
	for i := 1; i <= 5; i++ {
		at := start.Add(time.Duration(i) * time.Minute)
		f.Metrics[shop+" CPUUtilization"] = append(f.Metrics[shop+" CPUUtilization"], aliyuntest.Sample{T: at, V: float64(10 * i)})
		f.Metrics[shop+" VPC_PublicIP_InternetOutRate"] = append(f.Metrics[shop+" VPC_PublicIP_InternetOutRate"], aliyuntest.Sample{T: at, V: 8e6})
		f.Metrics[blog+" CPU_UTILIZATION"] = append(f.Metrics[blog+" CPU_UTILIZATION"], aliyuntest.Sample{T: at, V: 3})
		f.Metrics[blog+" MEMORY_ACTUALUSEDSPACE"] = append(f.Metrics[blog+" MEMORY_ACTUALUSEDSPACE"], aliyuntest.Sample{T: at, V: 1 << 30})
	}
	f.Metrics[shop+" CPUUtilization"] = append(f.Metrics[shop+" CPUUtilization"], aliyuntest.Sample{T: start.Add(-time.Minute), V: 99}) // before the window
	f.PageSize = 2
	list, _ := c.Servers(ctx)
	byID := map[string]aliyun.Server{}
	for _, s := range list {
		byID[s.ID] = s
	}
	pts, err := c.MonitorData(ctx, byID[shop], aliyun.MetricCPU, 60, start, end)
	if err != nil || len(pts) != 5 || pts[0].T != start.Add(time.Minute).Unix() || pts[0].V != 10 || pts[4].V != 50 {
		t.Fatalf("ECS cpu = %+v, %v", pts, err)
	}
	// No classic-network samples: it reads the VPC public IP's instead.
	if pts, err := c.MonitorData(ctx, byID[shop], aliyun.MetricNetOut, 60, start, end); err != nil || len(pts) != 5 || pts[0].V != 8e6 {
		t.Fatalf("ECS net out = %+v, %v", pts, err)
	}
	if pts, err := c.MonitorData(ctx, byID[shop], aliyun.MetricMemory, 60, start, end); err != nil || len(pts) != 0 {
		t.Fatalf("ECS memory without the agent = %+v, %v", pts, err)
	}
	if pts, err := c.MonitorData(ctx, byID[blog], aliyun.MetricCPU, 0, start, end); err != nil || len(pts) != 5 || pts[2].V != 3 {
		t.Fatalf("SWAS cpu = %+v, %v", pts, err)
	}
	if pts, err := c.MonitorData(ctx, byID[blog], aliyun.MetricMemory, 120, start, end); err != nil || len(pts) != 5 || pts[0].V != 50 {
		t.Fatalf("SWAS memory = %+v, %v", pts, err)
	}
	if _, err := c.MonitorData(ctx, byID[blog], "NO_SUCH_METRIC", 60, start, end); !aliyun.IsCode(err, "InvalidParameter") {
		t.Fatalf("unknown metric: %v", err)
	}
}
