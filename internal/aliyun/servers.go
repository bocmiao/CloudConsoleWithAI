package aliyun

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Server kinds.
const (
	KindECS  = "ecs"  // 云服务器 ECS
	KindSWAS = "swas" // 轻量应用服务器 (Simple Application Server)
)

// KindOf tells the product from an instance ID: ECS IDs start with "i-",
// Simple Application Server IDs are 32 hex digits.
func KindOf(id string) string {
	if strings.HasPrefix(id, "i-") {
		return KindECS
	}
	return KindSWAS
}

// Region is one region where a product is sold.
type Region struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Endpoint string `json:"endpoint,omitempty"`
}

// Regions lists the regions of a server kind: for ECS those the account
// can use, for Simple Application Server all where it is sold.
func (c *Client) Regions(ctx context.Context, kind string) ([]Region, error) {
	type region struct {
		RegionID       string `json:"RegionId"`
		LocalName      string `json:"LocalName"`
		RegionEndpoint string `json:"RegionEndpoint"`
	}
	var list []region
	if kind == KindSWAS {
		var out struct {
			Regions []region `json:"Regions"`
		}
		if err := c.Call(ctx, ProductSWAS, VersionSWAS, "ListRegions", defaultRegion, map[string]string{"AcceptLanguage": "zh-CN"}, &out); err != nil {
			return nil, err
		}
		list = out.Regions
	} else {
		var out struct {
			Regions struct {
				Region []region `json:"Region"`
			} `json:"Regions"`
		}
		if err := c.Call(ctx, ProductECS, VersionECS, "DescribeRegions", defaultRegion, map[string]string{"AcceptLanguage": "zh-CN"}, &out); err != nil {
			return nil, err
		}
		list = out.Regions.Region
	}
	regions := make([]Region, 0, len(list))
	for _, r := range list {
		regions = append(regions, Region{ID: r.RegionID, Name: r.LocalName, Endpoint: r.RegionEndpoint})
	}
	return regions, nil
}

// Server is an ECS or Simple Application Server instance.
type Server struct {
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	Name       string `json:"name"`
	Region     string `json:"region"`
	RegionName string `json:"regionName"`
	Zone       string `json:"zone,omitempty"` // ECS only
	// State is the API's status in capitals: PENDING, STARTING, RUNNING,
	// STOPPING, STOPPED, and for Simple Application Server also RESETTING,
	// UPGRADING and DISABLED.
	State         string   `json:"state"`
	Spec          string   `json:"spec"` // ECS instance type or Simple Application Server plan
	CPU           int      `json:"cpu"`
	MemoryGB      float64  `json:"memoryGB"` // may be 0.5
	DiskGB        int      `json:"diskGB"`   // system disk
	SystemDiskID  string   `json:"systemDiskId"`
	BandwidthMbps int      `json:"bandwidthMbps"`
	PublicIPs     []string `json:"publicIPs"`
	PrivateIPs    []string `json:"privateIPs"`
	OS            string   `json:"os"`
	ChargeType    string   `json:"chargeType"`            // PREPAID or POSTPAID
	ExpiredTime   string   `json:"expiredTime,omitempty"` // RFC 3339; empty when it never expires
	// AutoRenew is only known for ECS subscriptions.
	AutoRenew      *bool    `json:"autoRenew,omitempty"`
	SecurityGroups []string `json:"securityGroups,omitempty"` // ECS only
	// This month's traffic package of a Simple Application Server, in bytes.
	TrafficUsed  int64 `json:"trafficUsed,omitempty"`
	TrafficTotal int64 `json:"trafficTotal,omitempty"`
}

func chargeType(s string) string {
	switch strings.ToLower(s) {
	case "prepaid":
		return "PREPAID"
	case "postpaid":
		return "POSTPAID"
	}
	return strings.ToUpper(s)
}

// expiry turns the API's expiry time into RFC 3339. Pay-as-you-go ECS
// instances say 2099-12-31, which means never.
func expiry(s string) string {
	if s == "" || strings.HasPrefix(s, "2099-") {
		return ""
	}
	return rfc3339(s)
}

func appendNew(list []string, ips ...string) []string {
	for _, ip := range ips {
		if ip == "" {
			continue
		}
		seen := false
		for _, x := range list {
			seen = seen || x == ip
		}
		if !seen {
			list = append(list, ip)
		}
	}
	return list
}

type ipList struct {
	IPAddress []string `json:"IpAddress"`
}

type ecsInstance struct {
	InstanceID         string `json:"InstanceId"`
	InstanceName       string `json:"InstanceName"`
	RegionID           string `json:"RegionId"`
	ZoneID             string `json:"ZoneId"`
	Status             string `json:"Status"`
	InstanceType       string `json:"InstanceType"`
	CPU                int    `json:"Cpu"`
	Memory             int    `json:"Memory"` // MiB
	OSName             string `json:"OSName"`
	InstanceChargeType string `json:"InstanceChargeType"`
	ExpiredTime        string `json:"ExpiredTime"`
	InternetMaxOut     int    `json:"InternetMaxBandwidthOut"`
	PublicIPAddress    ipList `json:"PublicIpAddress"`
	InnerIPAddress     ipList `json:"InnerIpAddress"`
	EIPAddress         struct {
		IPAddress string `json:"IpAddress"`
		Bandwidth int    `json:"Bandwidth"`
	} `json:"EipAddress"`
	VpcAttributes struct {
		PrivateIPAddress ipList `json:"PrivateIpAddress"`
	} `json:"VpcAttributes"`
	SecurityGroupIDs struct {
		SecurityGroupID []string `json:"SecurityGroupId"`
	} `json:"SecurityGroupIds"`
}

// ecsServers lists the ECS instances of a region, or only those in ids.
// The system disks and auto-renewal come from two more calls; when those
// fail the list is still returned, just without them.
func (c *Client) ecsServers(ctx context.Context, r Region, ids []string) ([]Server, error) {
	var all []ecsInstance
	for token := ""; ; {
		in := map[string]string{"MaxResults": "100"}
		if len(ids) > 0 {
			in["InstanceIds"] = jsonList(ids)
		}
		if token != "" {
			in["NextToken"] = token
		}
		var out struct {
			Instances struct {
				Instance []ecsInstance `json:"Instance"`
			} `json:"Instances"`
			NextToken string `json:"NextToken"`
		}
		if err := c.Call(ctx, ProductECS, VersionECS, "DescribeInstances", r.ID, in, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Instances.Instance...)
		if out.NextToken == "" || out.NextToken == token || len(out.Instances.Instance) == 0 {
			break
		}
		token = out.NextToken
	}
	list := make([]Server, 0, len(all))
	var prepaid []string
	for _, i := range all {
		s := Server{
			Kind: KindECS, ID: i.InstanceID, Name: i.InstanceName, Region: r.ID, RegionName: r.Name, Zone: i.ZoneID,
			State: strings.ToUpper(i.Status), Spec: i.InstanceType, CPU: i.CPU, MemoryGB: float64(i.Memory) / 1024,
			BandwidthMbps: max(i.InternetMaxOut, i.EIPAddress.Bandwidth),
			PublicIPs:     appendNew(nil, append(i.PublicIPAddress.IPAddress, i.EIPAddress.IPAddress)...),
			PrivateIPs:    appendNew(nil, append(i.VpcAttributes.PrivateIPAddress.IPAddress, i.InnerIPAddress.IPAddress...)...),
			OS:            i.OSName, ChargeType: chargeType(i.InstanceChargeType), ExpiredTime: expiry(i.ExpiredTime),
			SecurityGroups: i.SecurityGroupIDs.SecurityGroupID,
		}
		if s.Region == "" {
			s.Region = i.RegionID
		}
		if s.ChargeType == "PREPAID" {
			prepaid = append(prepaid, s.ID)
		}
		list = append(list, s)
	}
	if len(list) == 0 {
		return list, nil
	}
	one := "" // a single instance's disk, or the whole region's
	if len(ids) == 1 {
		one = ids[0]
	}
	found, _ := c.ecsDisks(ctx, r.ID, one)
	disks := map[string]disk{}
	for _, d := range found {
		disks[d.instance] = d
	}
	renew := c.ecsAutoRenew(ctx, r.ID, prepaid)
	for i := range list {
		if d, ok := disks[list[i].ID]; ok {
			list[i].DiskGB, list[i].SystemDiskID = d.sizeGB, d.id
		}
		if v, ok := renew[list[i].ID]; ok {
			list[i].AutoRenew = &v
		}
	}
	return list, nil
}

type disk struct {
	id, instance string
	sizeGB       int
}

// ecsDisks lists the system disks of a region, or of one instance.
func (c *Client) ecsDisks(ctx context.Context, region, instance string) ([]disk, error) {
	var disks []disk
	for token := ""; ; {
		in := map[string]string{"DiskType": "system", "MaxResults": "500"}
		if instance != "" {
			in["InstanceId"] = instance
		}
		if token != "" {
			in["NextToken"] = token
		}
		var out struct {
			Disks struct {
				Disk []struct {
					DiskID     string `json:"DiskId"`
					InstanceID string `json:"InstanceId"`
					Size       int    `json:"Size"`
				} `json:"Disk"`
			} `json:"Disks"`
			NextToken string `json:"NextToken"`
		}
		if err := c.Call(ctx, ProductECS, VersionECS, "DescribeDisks", region, in, &out); err != nil {
			return disks, err
		}
		for _, d := range out.Disks.Disk {
			disks = append(disks, disk{id: d.DiskID, instance: d.InstanceID, sizeGB: d.Size})
		}
		if out.NextToken == "" || out.NextToken == token || len(out.Disks.Disk) == 0 {
			return disks, nil
		}
		token = out.NextToken
	}
}

// ecsAutoRenew reads whether subscriptions renew themselves, 100 at a
// time, skipping what it cannot read.
func (c *Client) ecsAutoRenew(ctx context.Context, region string, ids []string) map[string]bool {
	renew := map[string]bool{}
	for len(ids) > 0 {
		page := ids[:min(len(ids), 100)]
		ids = ids[len(page):]
		var out struct {
			InstanceRenewAttributes struct {
				InstanceRenewAttribute []struct {
					InstanceID       string `json:"InstanceId"`
					AutoRenewEnabled bool   `json:"AutoRenewEnabled"`
				} `json:"InstanceRenewAttribute"`
			} `json:"InstanceRenewAttributes"`
		}
		if c.Call(ctx, ProductECS, VersionECS, "DescribeInstanceAutoRenewAttribute", region,
			map[string]string{"InstanceId": strings.Join(page, ","), "PageSize": "100"}, &out) != nil {
			continue
		}
		for _, a := range out.InstanceRenewAttributes.InstanceRenewAttribute {
			renew[a.InstanceID] = a.AutoRenewEnabled
		}
	}
	return renew
}

type swasInstance struct {
	InstanceID      string `json:"InstanceId"`
	InstanceName    string `json:"InstanceName"`
	RegionID        string `json:"RegionId"`
	Status          string `json:"Status"`
	PlanID          string `json:"PlanId"`
	ChargeType      string `json:"ChargeType"`
	ExpiredTime     string `json:"ExpiredTime"`
	PublicIPAddress string `json:"PublicIpAddress"`
	InnerIPAddress  string `json:"InnerIpAddress"`
	ResourceSpec    struct {
		CPU       int    `json:"Cpu"`
		Memory    number `json:"Memory"` // GiB
		DiskSize  int    `json:"DiskSize"`
		Bandwidth int    `json:"Bandwidth"`
	} `json:"ResourceSpec"`
	Image struct {
		ImageName    string `json:"ImageName"`
		ImageVersion string `json:"ImageVersion"`
		OsType       string `json:"OsType"`
	} `json:"Image"`
	Disks []struct {
		DiskID   string `json:"DiskId"`
		DiskType string `json:"DiskType"`
		Size     int    `json:"Size"`
	} `json:"Disks"`
	NetworkAttributes []struct {
		PublicIPAddress  string `json:"PublicIpAddress"`
		PrivateIPAddress string `json:"PrivateIpAddress"`
	} `json:"NetworkAttributes"`
}

// swasServers lists the Simple Application Servers of a region, or only
// those in ids, with this month's traffic package.
func (c *Client) swasServers(ctx context.Context, r Region, ids []string) ([]Server, error) {
	var all []swasInstance
	for page := 1; ; page++ {
		in := map[string]string{"PageSize": "100", "PageNumber": strconv.Itoa(page)}
		if len(ids) > 0 {
			in["InstanceIds"] = jsonList(ids)
		}
		var out struct {
			Instances  []swasInstance `json:"Instances"`
			TotalCount int            `json:"TotalCount"`
		}
		if err := c.Call(ctx, ProductSWAS, VersionSWAS, "ListInstances", r.ID, in, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Instances...)
		if len(out.Instances) == 0 || len(all) >= out.TotalCount {
			break
		}
	}
	list := make([]Server, 0, len(all))
	for _, i := range all {
		os := i.Image.ImageName
		if v := i.Image.ImageVersion; v != "" && !strings.Contains(os, v) {
			os = strings.TrimSpace(os + " " + v)
		}
		s := Server{
			Kind: KindSWAS, ID: i.InstanceID, Name: i.InstanceName, Region: r.ID, RegionName: r.Name,
			State: strings.ToUpper(i.Status), Spec: i.PlanID, CPU: i.ResourceSpec.CPU, MemoryGB: float64(i.ResourceSpec.Memory),
			DiskGB: i.ResourceSpec.DiskSize, BandwidthMbps: i.ResourceSpec.Bandwidth,
			PublicIPs: appendNew(nil, i.PublicIPAddress), PrivateIPs: appendNew(nil, i.InnerIPAddress),
			OS: os, ChargeType: chargeType(i.ChargeType), ExpiredTime: expiry(i.ExpiredTime),
		}
		if s.Region == "" {
			s.Region = i.RegionID
		}
		for _, n := range i.NetworkAttributes {
			s.PublicIPs = appendNew(s.PublicIPs, n.PublicIPAddress)
			s.PrivateIPs = appendNew(s.PrivateIPs, n.PrivateIPAddress)
		}
		for _, d := range i.Disks {
			if strings.EqualFold(d.DiskType, "system") {
				s.SystemDiskID = d.DiskID
				if s.DiskGB == 0 {
					s.DiskGB = d.Size
				}
			}
		}
		list = append(list, s)
	}
	for start := 0; start < len(list); start += 100 {
		page := list[start:min(len(list), start+100)]
		pids := make([]string, len(page))
		for i, s := range page {
			pids[i] = s.ID
		}
		var tp struct {
			Usages []struct {
				InstanceID          string `json:"InstanceId"`
				TrafficUsed         int64  `json:"TrafficUsed"`
				TrafficPackageTotal int64  `json:"TrafficPackageTotal"`
			} `json:"InstanceTrafficPackageUsages"`
		}
		if c.Call(ctx, ProductSWAS, VersionSWAS, "ListInstancesTrafficPackages", r.ID, map[string]string{"InstanceIds": jsonList(pids)}, &tp) != nil {
			continue
		}
		for _, u := range tp.Usages {
			for i := range page {
				if page[i].ID == u.InstanceID {
					page[i].TrafficUsed, page[i].TrafficTotal = u.TrafficUsed, u.TrafficPackageTotal
				}
			}
		}
	}
	return list, nil
}

// Servers lists the ECS and Simple Application Server instances of every
// region, several regions at a time. What could not be read is returned
// in errs, once per distinct error, next to what could; a region that
// just does not offer the product is skipped quietly.
func (c *Client) Servers(ctx context.Context) (list []Server, errs []error) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, kind := range []string{KindECS, KindSWAS} {
		regions, err := c.Regions(ctx, kind)
		if err != nil {
			mu.Lock() // the first kind's regions are being read already
			errs = append(errs, err)
			mu.Unlock()
			continue
		}
		for _, r := range regions {
			wg.Add(1)
			go func(kind string, r Region) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				var got []Server
				var err error
				if kind == KindSWAS {
					got, err = c.swasServers(ctx, r, nil)
				} else {
					got, err = c.ecsServers(ctx, r, nil)
				}
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					if !IsCode(err, "InvalidRegionId") {
						errs = append(errs, err)
					}
					return
				}
				list = append(list, got...)
			}(kind, r)
		}
	}
	wg.Wait()
	sort.Slice(list, func(i, j int) bool {
		if list[i].Kind != list[j].Kind {
			return list[i].Kind < list[j].Kind
		}
		if list[i].Name != list[j].Name {
			return list[i].Name < list[j].Name
		}
		return list[i].ID < list[j].ID
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

// Instance finds one instance in a region; the kind comes from its ID.
func (c *Client) Instance(ctx context.Context, region, id string) (Server, bool, error) {
	var list []Server
	var err error
	r := Region{ID: region}
	if KindOf(id) == KindECS {
		list, err = c.ecsServers(ctx, r, []string{id})
	} else {
		list, err = c.swasServers(ctx, r, []string{id})
	}
	for _, s := range list {
		if s.ID == id {
			return s, true, err
		}
	}
	return Server{}, false, err
}

// Power starts, stops or reboots an instance: action is "start", "stop"
// or "reboot". Both products shut down cleanly (no force). A stopped
// pay-as-you-go ECS instance keeps being charged, so that it keeps its
// public IP and can surely start again.
func (c *Client) Power(ctx context.Context, region, id, action string) error {
	name := map[string]string{"start": "StartInstance", "stop": "StopInstance", "reboot": "RebootInstance"}[action]
	if name == "" {
		return fmt.Errorf("不认识的开关机操作：%q（只能是 start、stop 或 reboot）", action)
	}
	in := map[string]string{"InstanceId": id}
	if KindOf(id) == KindSWAS {
		return c.Call(ctx, ProductSWAS, VersionSWAS, name, region, in, nil)
	}
	if action == "stop" {
		in["StoppedMode"] = "KeepCharging"
	}
	return c.Call(ctx, ProductECS, VersionECS, name, region, in, nil)
}
