package tencent

import (
	"context"
	"sort"
	"strings"
	"sync"
)

const (
	lighthouseVersion = "2020-03-24"
	cvmVersion        = "2017-03-12"
	vpcVersion        = "2017-03-12"
	cbsVersion        = "2017-03-12"
	monitorVersion    = "2018-07-24"
)

// Server kinds.
const (
	Lighthouse = "lighthouse" // 轻量应用服务器
	CVM        = "cvm"        // 云服务器
)

// KindOf tells the product from an instance ID.
func KindOf(id string) string {
	if strings.HasPrefix(id, "lhins-") {
		return Lighthouse
	}
	return CVM
}

// Region is one region of a product.
type Region struct {
	Region     string `json:"Region"`
	RegionName string `json:"RegionName"`
}

// Regions lists the regions where a product (lighthouse or cvm) is sold.
func (c *Client) Regions(ctx context.Context, product string) ([]Region, error) {
	version := cvmVersion
	if product == Lighthouse {
		version = lighthouseVersion
	}
	var out struct {
		RegionSet []Region `json:"RegionSet"`
	}
	err := c.CallRegion(ctx, product, version, "DescribeRegions", "ap-guangzhou", map[string]any{}, &out)
	return out.RegionSet, err
}

// Server is a Lighthouse or CVM instance.
type Server struct {
	Kind          string   `json:"kind"`
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Region        string   `json:"region"`
	RegionName    string   `json:"regionName"`
	Zone          string   `json:"zone"`
	State         string   `json:"state"` // RUNNING, STOPPED, ...
	CPU           int      `json:"cpu"`
	MemoryGB      int      `json:"memoryGB"`
	DiskGB        int      `json:"diskGB"`
	SystemDiskID  string   `json:"systemDiskId"`
	BandwidthMbps int      `json:"bandwidthMbps"`
	PublicIPs     []string `json:"publicIPs"`
	PrivateIPs    []string `json:"privateIPs"`
	OS            string   `json:"os"`
	ChargeType    string   `json:"chargeType"`
	ExpiredTime   string   `json:"expiredTime"`
	RenewFlag     string   `json:"renewFlag"`
	Groups        []string `json:"securityGroups,omitempty"` // CVM security groups
	// Lighthouse monthly traffic package, in bytes.
	TrafficUsed  int64 `json:"trafficUsed,omitempty"`
	TrafficTotal int64 `json:"trafficTotal,omitempty"`
}

type sysDisk struct {
	DiskID   string `json:"DiskId"`
	DiskSize int    `json:"DiskSize"`
}

type internet struct {
	InternetMaxBandwidthOut int `json:"InternetMaxBandwidthOut"`
}

type lighthouseInstance struct {
	InstanceID         string   `json:"InstanceId"`
	InstanceName       string   `json:"InstanceName"`
	InstanceState      string   `json:"InstanceState"`
	CPU                int      `json:"CPU"`
	Memory             int      `json:"Memory"`
	SystemDisk         sysDisk  `json:"SystemDisk"`
	PublicAddresses    []string `json:"PublicAddresses"`
	PrivateAddresses   []string `json:"PrivateAddresses"`
	InternetAccessible internet `json:"InternetAccessible"`
	OsName             string   `json:"OsName"`
	Zone               string   `json:"Zone"`
	InstanceChargeType string   `json:"InstanceChargeType"`
	ExpiredTime        string   `json:"ExpiredTime"`
	RenewFlag          string   `json:"RenewFlag"`
}

func (c *Client) lighthouseServers(ctx context.Context, r Region) ([]Server, error) {
	var out struct {
		InstanceSet []lighthouseInstance `json:"InstanceSet"`
		TotalCount  int                  `json:"TotalCount"`
	}
	var all []lighthouseInstance
	for offset := 0; ; {
		out.InstanceSet, out.TotalCount = nil, 0
		if err := c.CallRegion(ctx, Lighthouse, lighthouseVersion, "DescribeInstances", r.Region, map[string]any{"Offset": offset, "Limit": 100}, &out); err != nil {
			return nil, err
		}
		all = append(all, out.InstanceSet...)
		offset += len(out.InstanceSet)
		if len(out.InstanceSet) == 0 || offset >= out.TotalCount {
			break
		}
	}
	var list []Server
	var ids []string
	for _, i := range all {
		list = append(list, Server{
			Kind: Lighthouse, ID: i.InstanceID, Name: i.InstanceName, Region: r.Region, RegionName: r.RegionName, Zone: i.Zone,
			State: i.InstanceState, CPU: i.CPU, MemoryGB: i.Memory, DiskGB: i.SystemDisk.DiskSize, SystemDiskID: i.SystemDisk.DiskID,
			BandwidthMbps: i.InternetAccessible.InternetMaxBandwidthOut, PublicIPs: i.PublicAddresses, PrivateIPs: i.PrivateAddresses,
			OS: i.OsName, ChargeType: i.InstanceChargeType, ExpiredTime: i.ExpiredTime, RenewFlag: i.RenewFlag,
		})
		ids = append(ids, i.InstanceID)
	}
	for len(ids) > 0 { // at most 100 IDs a call
		page := ids
		if len(page) > 100 {
			page = page[:100]
		}
		ids = ids[len(page):]
		var tp struct {
			InstanceTrafficPackageSet []struct {
				InstanceID        string `json:"InstanceId"`
				TrafficPackageSet []struct {
					TrafficUsed         int64 `json:"TrafficUsed"`
					TrafficPackageTotal int64 `json:"TrafficPackageTotal"`
				} `json:"TrafficPackageSet"`
			} `json:"InstanceTrafficPackageSet"`
		}
		if err := c.CallRegion(ctx, Lighthouse, lighthouseVersion, "DescribeInstancesTrafficPackages", r.Region,
			map[string]any{"InstanceIds": page, "Limit": 100}, &tp); err == nil {
			for _, p := range tp.InstanceTrafficPackageSet {
				for i := range list {
					if list[i].ID == p.InstanceID {
						for _, pkg := range p.TrafficPackageSet {
							list[i].TrafficUsed += pkg.TrafficUsed
							list[i].TrafficTotal += pkg.TrafficPackageTotal
						}
					}
				}
			}
		}
	}
	return list, nil
}

type cvmInstance struct {
	InstanceID         string   `json:"InstanceId"`
	InstanceName       string   `json:"InstanceName"`
	InstanceState      string   `json:"InstanceState"`
	CPU                int      `json:"CPU"`
	Memory             int      `json:"Memory"`
	SystemDisk         sysDisk  `json:"SystemDisk"`
	PublicIPAddresses  []string `json:"PublicIpAddresses"`
	PrivateIPAddresses []string `json:"PrivateIpAddresses"`
	InternetAccessible internet `json:"InternetAccessible"`
	OsName             string   `json:"OsName"`
	Placement          struct {
		Zone string `json:"Zone"`
	} `json:"Placement"`
	InstanceChargeType string   `json:"InstanceChargeType"`
	ExpiredTime        string   `json:"ExpiredTime"`
	RenewFlag          string   `json:"RenewFlag"`
	SecurityGroupIDs   []string `json:"SecurityGroupIds"`
}

func (c *Client) cvmServers(ctx context.Context, r Region) ([]Server, error) {
	var out struct {
		InstanceSet []cvmInstance `json:"InstanceSet"`
		TotalCount  int           `json:"TotalCount"`
	}
	var all []cvmInstance
	for offset := 0; ; {
		out.InstanceSet, out.TotalCount = nil, 0
		if err := c.CallRegion(ctx, CVM, cvmVersion, "DescribeInstances", r.Region, map[string]any{"Offset": offset, "Limit": 100}, &out); err != nil {
			return nil, err
		}
		all = append(all, out.InstanceSet...)
		offset += len(out.InstanceSet)
		if len(out.InstanceSet) == 0 || offset >= out.TotalCount {
			break
		}
	}
	var list []Server
	for _, i := range all {
		list = append(list, Server{
			Kind: CVM, ID: i.InstanceID, Name: i.InstanceName, Region: r.Region, RegionName: r.RegionName, Zone: i.Placement.Zone,
			State: i.InstanceState, CPU: i.CPU, MemoryGB: i.Memory, DiskGB: i.SystemDisk.DiskSize, SystemDiskID: i.SystemDisk.DiskID,
			BandwidthMbps: i.InternetAccessible.InternetMaxBandwidthOut, PublicIPs: i.PublicIPAddresses, PrivateIPs: i.PrivateIPAddresses,
			OS: i.OsName, ChargeType: i.InstanceChargeType, ExpiredTime: i.ExpiredTime, RenewFlag: i.RenewFlag, Groups: i.SecurityGroupIDs,
		})
	}
	return list, nil
}

// Servers lists Lighthouse and CVM instances in every region. A product
// the credentials cannot see is reported in errs and skipped.
func (c *Client) Servers(ctx context.Context) (list []Server, errs []error) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, product := range []string{Lighthouse, CVM} {
		regions, err := c.Regions(ctx, product)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, r := range regions {
			wg.Add(1)
			go func(product string, r Region) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				var got []Server
				var err error
				if product == Lighthouse {
					got, err = c.lighthouseServers(ctx, r)
				} else {
					got, err = c.cvmServers(ctx, r)
				}
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					// Regions a product is not open in answer with errors; only
					// permission problems matter to the user.
					if IsCode(err, "UnauthorizedOperation") || IsCode(err, "AuthFailure") {
						errs = append(errs, err)
					}
					return
				}
				list = append(list, got...)
			}(product, r)
		}
	}
	wg.Wait()
	sort.Slice(list, func(i, j int) bool {
		if list[i].Kind != list[j].Kind {
			return list[i].Kind < list[j].Kind
		}
		return list[i].Name < list[j].Name
	})
	return list, dedupe(errs)
}

func dedupe(errs []error) []error {
	seen := map[string]bool{}
	var out []error
	for _, e := range errs {
		if !seen[e.Error()] {
			seen[e.Error()] = true
			out = append(out, e)
		}
	}
	return out
}

// Instance finds one instance in a region.
func (c *Client) Instance(ctx context.Context, region, id string) (Server, bool, error) {
	var list []Server
	var err error
	r := Region{Region: region}
	if KindOf(id) == Lighthouse {
		list, err = c.lighthouseServers(ctx, r)
	} else {
		list, err = c.cvmServers(ctx, r)
	}
	for _, s := range list {
		if s.ID == id {
			return s, true, err
		}
	}
	return Server{}, false, err
}

// Power starts, stops or reboots an instance: action is StartInstances,
// StopInstances or RebootInstances.
func (c *Client) Power(ctx context.Context, region, id, action string) error {
	in := map[string]any{"InstanceIds": []string{id}}
	if KindOf(id) == Lighthouse {
		// Lighthouse takes only the IDs (it always shuts down cleanly);
		// anything else is refused as an unknown parameter.
		return c.CallRegion(ctx, Lighthouse, lighthouseVersion, action, region, in, nil)
	}
	if action != "StartInstances" {
		in["StopType"] = "SOFT_FIRST"
	}
	return c.CallRegion(ctx, CVM, cvmVersion, action, region, in, nil)
}

// FirewallRule is an inbound rule of a Lighthouse firewall or a CVM
// security group.
type FirewallRule struct {
	Protocol    string `json:"Protocol"`
	Port        string `json:"Port"`
	CidrBlock   string `json:"CidrBlock"`
	Action      string `json:"Action"`
	Description string `json:"Description,omitempty"`
	Index       int64  `json:"PolicyIndex,omitempty"` // CVM security group rules only
	// Sources other than an IPv4 range: a rule with one of these does not
	// allow everyone even though its CidrBlock is empty.
	Ipv6     string `json:"Ipv6CidrBlock,omitempty"`
	Group    string `json:"SecurityGroupId,omitempty"` // CVM only
	Template string `json:"AddressTemplate,omitempty"` // CVM only: an address template or template group ID
}

// Source is who the rule is about, in words for logs.
func (r FirewallRule) Source() string {
	switch {
	case r.CidrBlock != "":
		return r.CidrBlock
	case r.Ipv6 != "":
		return r.Ipv6
	case r.Group != "":
		return "安全组 " + r.Group
	case r.Template != "":
		return "参数模板 " + r.Template
	}
	return "0.0.0.0/0"
}

// Same compares what a rule allows, ignoring its description.
func (r FirewallRule) Same(o FirewallRule) bool {
	norm := func(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }
	return norm(r.Protocol) == norm(o.Protocol) && norm(r.Port) == norm(o.Port) &&
		r.Source() == o.Source() && r.Ipv6 == o.Ipv6 && r.Group == o.Group && r.Template == o.Template && norm(r.Action) == norm(o.Action)
}

// LighthouseFirewall lists a Lighthouse instance's firewall rules.
func (c *Client) LighthouseFirewall(ctx context.Context, region, id string) ([]FirewallRule, error) {
	var out struct {
		FirewallRuleSet []struct {
			Protocol    string `json:"Protocol"`
			Port        string `json:"Port"`
			CidrBlock   string `json:"CidrBlock"`
			Ipv6        string `json:"Ipv6CidrBlock"`
			Action      string `json:"Action"`
			Description string `json:"FirewallRuleDescription"`
		} `json:"FirewallRuleSet"`
	}
	err := c.CallRegion(ctx, Lighthouse, lighthouseVersion, "DescribeFirewallRules", region, map[string]any{"InstanceId": id, "Limit": 100}, &out)
	var rules []FirewallRule
	for _, r := range out.FirewallRuleSet {
		rules = append(rules, FirewallRule{Protocol: r.Protocol, Port: r.Port, CidrBlock: r.CidrBlock, Ipv6: r.Ipv6, Action: r.Action, Description: r.Description})
	}
	return rules, err
}

func lighthouseRule(r FirewallRule) map[string]any {
	m := map[string]any{"Protocol": r.Protocol, "Port": r.Port, "Action": r.Action}
	if r.Ipv6 != "" {
		m["Ipv6CidrBlock"] = r.Ipv6
	} else {
		m["CidrBlock"] = r.CidrBlock
	}
	if r.Description != "" {
		m["FirewallRuleDescription"] = r.Description
	}
	return m
}

// AddLighthouseFirewall adds a rule to a Lighthouse firewall.
func (c *Client) AddLighthouseFirewall(ctx context.Context, region, id string, r FirewallRule) error {
	return c.CallRegion(ctx, Lighthouse, lighthouseVersion, "CreateFirewallRules", region,
		map[string]any{"InstanceId": id, "FirewallRules": []any{lighthouseRule(r)}}, nil)
}

// DeleteLighthouseFirewall removes a rule from a Lighthouse firewall.
func (c *Client) DeleteLighthouseFirewall(ctx context.Context, region, id string, r FirewallRule) error {
	r.Description = ""
	return c.CallRegion(ctx, Lighthouse, lighthouseVersion, "DeleteFirewallRules", region,
		map[string]any{"InstanceId": id, "FirewallRules": []any{lighthouseRule(r)}}, nil)
}

// SecurityGroupIngress lists the inbound rules of a CVM security group.
func (c *Client) SecurityGroupIngress(ctx context.Context, region, group string) ([]FirewallRule, error) {
	var out struct {
		SecurityGroupPolicySet struct {
			Ingress []struct {
				PolicyIndex     int64  `json:"PolicyIndex"`
				Protocol        string `json:"Protocol"`
				Port            string `json:"Port"`
				CidrBlock       string `json:"CidrBlock"`
				Ipv6CidrBlock   string `json:"Ipv6CidrBlock"`
				SecurityGroupID string `json:"SecurityGroupId"`
				AddressTemplate struct {
					AddressID      string `json:"AddressId"`
					AddressGroupID string `json:"AddressGroupId"`
				} `json:"AddressTemplate"`
				Action            string `json:"Action"`
				PolicyDescription string `json:"PolicyDescription"`
			} `json:"Ingress"`
		} `json:"SecurityGroupPolicySet"`
	}
	err := c.CallRegion(ctx, "vpc", vpcVersion, "DescribeSecurityGroupPolicies", region, map[string]any{"SecurityGroupId": group}, &out)
	var rules []FirewallRule
	for _, r := range out.SecurityGroupPolicySet.Ingress {
		tpl := r.AddressTemplate.AddressID
		if tpl == "" {
			tpl = r.AddressTemplate.AddressGroupID
		}
		rules = append(rules, FirewallRule{Protocol: r.Protocol, Port: r.Port, CidrBlock: r.CidrBlock, Ipv6: r.Ipv6CidrBlock,
			Group: r.SecurityGroupID, Template: tpl, Action: r.Action, Description: r.PolicyDescription, Index: r.PolicyIndex})
	}
	return rules, err
}

func groupRule(r FirewallRule) map[string]any {
	m := map[string]any{"Protocol": r.Protocol, "Port": r.Port, "Action": r.Action}
	switch {
	case r.Ipv6 != "":
		m["Ipv6CidrBlock"] = r.Ipv6
	case r.Group != "":
		m["SecurityGroupId"] = r.Group
	case strings.HasPrefix(r.Template, "ipmg-"):
		m["AddressTemplate"] = map[string]string{"AddressGroupId": r.Template}
	case r.Template != "":
		m["AddressTemplate"] = map[string]string{"AddressId": r.Template}
	default:
		m["CidrBlock"] = r.CidrBlock
	}
	if r.Description != "" {
		m["PolicyDescription"] = r.Description
	}
	return m
}

// AddSecurityGroupIngress adds an inbound rule at the top of a security
// group, or at r.Index when set (putting a removed rule back where it was:
// rules are checked in order, so the place matters).
func (c *Client) AddSecurityGroupIngress(ctx context.Context, region, group string, r FirewallRule) error {
	m := groupRule(r)
	if r.Index > 0 {
		m["PolicyIndex"] = r.Index
	}
	return c.CallRegion(ctx, "vpc", vpcVersion, "CreateSecurityGroupPolicies", region, map[string]any{
		"SecurityGroupId": group, "SecurityGroupPolicySet": map[string]any{"Ingress": []any{m}},
	}, nil)
}

// DeleteSecurityGroupIngress removes the inbound rules that match r.
func (c *Client) DeleteSecurityGroupIngress(ctx context.Context, region, group string, r FirewallRule) error {
	r.Description = ""
	return c.CallRegion(ctx, "vpc", vpcVersion, "DeleteSecurityGroupPolicies", region, map[string]any{
		"SecurityGroupId": group, "SecurityGroupPolicySet": map[string]any{"Ingress": []any{groupRule(r)}},
	}, nil)
}

// Snapshot is a disk snapshot.
type Snapshot struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	State   string `json:"state"` // NORMAL, CREATING, ...
	Percent int    `json:"percent"`
	Created string `json:"created"`
	SizeGB  int    `json:"sizeGB"`
}

// Snapshots lists the snapshots of an instance's system disk.
func (c *Client) Snapshots(ctx context.Context, region string, s Server) ([]Snapshot, error) {
	var list []Snapshot
	if s.Kind == Lighthouse {
		var out struct {
			SnapshotSet []struct {
				SnapshotID    string `json:"SnapshotId"`
				SnapshotName  string `json:"SnapshotName"`
				SnapshotState string `json:"SnapshotState"`
				Percent       int    `json:"Percent"`
				CreatedTime   string `json:"CreatedTime"`
				DiskSize      int    `json:"DiskSize"`
			} `json:"SnapshotSet"`
		}
		err := c.CallRegion(ctx, Lighthouse, lighthouseVersion, "DescribeSnapshots", region, map[string]any{
			"Filters": []map[string]any{{"Name": "instance-id", "Values": []string{s.ID}}}, "Limit": 100,
		}, &out)
		for _, x := range out.SnapshotSet {
			list = append(list, Snapshot{ID: x.SnapshotID, Name: x.SnapshotName, State: x.SnapshotState, Percent: x.Percent, Created: x.CreatedTime, SizeGB: x.DiskSize})
		}
		return list, err
	}
	var out struct {
		SnapshotSet []struct {
			SnapshotID    string `json:"SnapshotId"`
			SnapshotName  string `json:"SnapshotName"`
			SnapshotState string `json:"SnapshotState"`
			Percent       int    `json:"Percent"`
			CreateTime    string `json:"CreateTime"`
			DiskSize      int    `json:"DiskSize"`
		} `json:"SnapshotSet"`
	}
	if s.SystemDiskID == "" {
		return nil, nil
	}
	err := c.CallRegion(ctx, "cbs", cbsVersion, "DescribeSnapshots", region, map[string]any{
		"Filters": []map[string]any{{"Name": "disk-id", "Values": []string{s.SystemDiskID}}}, "Limit": 100,
	}, &out)
	for _, x := range out.SnapshotSet {
		list = append(list, Snapshot{ID: x.SnapshotID, Name: x.SnapshotName, State: x.SnapshotState, Percent: x.Percent, Created: x.CreateTime, SizeGB: x.DiskSize})
	}
	return list, err
}

// CreateSnapshot snapshots an instance's system disk.
func (c *Client) CreateSnapshot(ctx context.Context, region string, s Server, name string) (string, error) {
	var out struct {
		SnapshotID string `json:"SnapshotId"`
	}
	var err error
	if s.Kind == Lighthouse {
		err = c.CallRegion(ctx, Lighthouse, lighthouseVersion, "CreateInstanceSnapshot", region,
			map[string]any{"InstanceId": s.ID, "SnapshotName": name}, &out)
	} else {
		err = c.CallRegion(ctx, "cbs", cbsVersion, "CreateSnapshot", region,
			map[string]any{"DiskId": s.SystemDiskID, "SnapshotName": name}, &out)
	}
	return out.SnapshotID, err
}

// Metric is a Cloud Monitor metric of a product.
type Metric struct {
	Name    string `json:"MetricName"`
	CName   string `json:"MetricCName"`
	Unit    string `json:"Unit"`
	Periods []int  `json:"Period"`
}

// Namespace returns the Cloud Monitor namespace of an instance.
func Namespace(kind string) string {
	if kind == Lighthouse {
		return "QCE/LIGHTHOUSE"
	}
	return "QCE/CVM"
}

// Metrics lists the metrics Cloud Monitor keeps for a namespace.
func (c *Client) Metrics(ctx context.Context, region, namespace string) ([]Metric, error) {
	var out struct {
		MetricSet []Metric `json:"MetricSet"`
	}
	err := c.CallRegion(ctx, "monitor", monitorVersion, "DescribeBaseMetrics", region, map[string]any{"Namespace": namespace}, &out)
	return out.MetricSet, err
}

// Point is one sample of a time series.
type Point struct {
	T int64   `json:"t"` // unix seconds
	V float64 `json:"v"`
}

// MonitorData reads one metric of one instance.
func (c *Client) MonitorData(ctx context.Context, region, namespace, metric, instanceID string, period int, start, end string) ([]Point, error) {
	var out struct {
		DataPoints []struct {
			Timestamps []float64 `json:"Timestamps"`
			Values     []float64 `json:"Values"`
		} `json:"DataPoints"`
	}
	err := c.CallRegion(ctx, "monitor", monitorVersion, "GetMonitorData", region, map[string]any{
		"Namespace": namespace, "MetricName": metric, "Period": period, "StartTime": start, "EndTime": end,
		"Instances": []map[string]any{{"Dimensions": []map[string]string{{"Name": "InstanceId", "Value": instanceID}}}},
	}, &out)
	var pts []Point
	for _, dp := range out.DataPoints {
		for i, t := range dp.Timestamps {
			if i < len(dp.Values) {
				pts = append(pts, Point{T: int64(t), V: dp.Values[i]})
			}
		}
	}
	return pts, err
}
