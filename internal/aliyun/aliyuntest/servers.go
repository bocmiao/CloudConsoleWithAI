package aliyuntest

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// look returns an instance's status as a list shows it now; a power
// action shows its transition once and then reaches its final state.
func look(status, pending *string) string {
	now := *status
	if *pending != "" {
		*status, *pending = *pending, ""
	}
	return now
}

// ids reads a JSON array of IDs, as the …Ids parameters take them.
func ids(s string) ([]string, *apiErr) {
	if s == "" {
		return nil, nil
	}
	var list []string
	if json.Unmarshal([]byte(s), &list) != nil {
		return nil, refuse(400, "InvalidParameter.Malformed", "The specified parameter %q is not a JSON array.", s)
	}
	if len(list) > 100 {
		return nil, refuse(400, "InvalidParameter", "At most 100 IDs are allowed.")
	}
	return list, nil
}

func in(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// power applies StartInstance, StopInstance or RebootInstance to a status.
func power(action string, status, pending *string) *apiErr {
	switch {
	case action == "StartInstance" && *status == "Stopped":
		*status, *pending = "Starting", "Running"
	case action == "StopInstance" && *status == "Running":
		*status, *pending = "Stopping", "Stopped"
	case action == "RebootInstance" && *status == "Running":
		*status, *pending = "Starting", "Running"
	default:
		return refuse(403, "IncorrectInstanceStatus", "The current status of the resource does not support this operation.")
	}
	return nil
}

var ecsSnapName = regexp.MustCompile(`^\pL[\pL\pN:_.\-]{1,127}$`)
var swasSnapName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9:_.\-]{1,49}$`)

func (f *Cloud) snapshot(s *Snap) map[string]any {
	status, progress := "accomplished", "100%"
	if !s.looked {
		status, progress, s.looked = "progressing", "50%", true
	}
	if s.Kind == "swas" {
		status = strings.ToUpper(status[:1]) + status[1:]
	}
	m := map[string]any{"SnapshotId": s.ID, "SnapshotName": s.Name, "Status": status, "Progress": progress,
		"CreationTime": s.Created.UTC().Format("2006-01-02T15:04:05Z"), "SourceDiskId": s.DiskID, "SourceDiskType": "system", "RegionId": s.Region}
	if s.Kind == "ecs" {
		m["SourceDiskSize"] = strconv.Itoa(s.SizeGB)
		m["Category"], m["Available"], m["SnapshotType"], m["Usage"] = "standard", status == "accomplished", "user", "none"
	} else {
		m["InstanceId"], m["Remark"] = s.Instance, ""
	}
	return m
}

func (f *Cloud) serveECS(action, region string, q map[string]string) (map[string]any, *apiErr) {
	here := func(id string) (*ECSInstance, *apiErr) {
		if i := f.ECS[id]; i != nil && i.Region == region {
			return i, nil
		}
		return nil, refuse(404, "InvalidInstanceId.NotFound", "The specified InstanceId does not exist.")
	}
	groupRegion := func(g string) string {
		for _, i := range f.ECS {
			if i.Group == g {
				return i.Region
			}
		}
		return ""
	}
	switch action {
	case "DescribeRegions":
		var list []map[string]any
		for _, r := range f.ECSRegions {
			list = append(list, map[string]any{"RegionId": r.ID, "LocalName": r.Name, "RegionEndpoint": r.Endpoint, "Status": "available"})
		}
		return map[string]any{"Regions": map[string]any{"Region": list}}, nil
	case "DescribeInstances":
		want, err := ids(q["InstanceIds"])
		if err != nil {
			return nil, err
		}
		var match []*ECSInstance
		for _, id := range sortedKeys(f.ECS) {
			if i := f.ECS[id]; i.Region == region && (want == nil || in(want, id)) {
				match = append(match, i)
			}
		}
		from, to, next := f.pageToken(q, "MaxResults", 10, len(match))
		list := []map[string]any{}
		for _, i := range match[from:to] {
			pub := []string{}
			if i.PublicIP != "" {
				pub = append(pub, i.PublicIP)
			}
			list = append(list, map[string]any{
				"InstanceId": i.ID, "InstanceName": i.Name, "RegionId": i.Region, "ZoneId": i.Zone, "Status": look(&i.Status, &i.pending),
				"InstanceType": i.Type, "Cpu": i.CPU, "Memory": i.MemoryMiB, "OSName": i.OS, "OSType": "linux",
				"InstanceChargeType": i.ChargeType, "ExpiredTime": i.ExpiredTime, "InternetMaxBandwidthOut": i.Bandwidth,
				"InternetChargeType": "PayByTraffic", "InstanceNetworkType": "vpc", "CreationTime": "2025-01-02T03:04Z",
				"PublicIpAddress": map[string]any{"IpAddress": pub}, "InnerIpAddress": map[string]any{"IpAddress": []string{}},
				"EipAddress": map[string]any{"IpAddress": "", "AllocationId": "", "Bandwidth": 0},
				"VpcAttributes": map[string]any{"VpcId": "vpc-fake", "VSwitchId": "vsw-fake", "NatIpAddress": "",
					"PrivateIpAddress": map[string]any{"IpAddress": []string{i.PrivateIP}}},
				"SecurityGroupIds": map[string]any{"SecurityGroupId": []string{i.Group}},
			})
		}
		return map[string]any{"Instances": map[string]any{"Instance": list}, "NextToken": next, "TotalCount": len(match)}, nil
	case "DescribeDisks":
		if t := q["DiskType"]; t != "" && t != "system" && t != "data" && t != "all" {
			return nil, refuse(400, "InvalidDiskType.ValueNotSupported", "The specified DiskType is not supported.")
		}
		var match []*ECSInstance
		for _, id := range sortedKeys(f.ECS) {
			if i := f.ECS[id]; i.Region == region && (q["InstanceId"] == "" || q["InstanceId"] == id) {
				match = append(match, i)
			}
		}
		from, to, next := f.pageToken(q, "MaxResults", 10, len(match))
		list := []map[string]any{}
		for _, i := range match[from:to] {
			list = append(list, map[string]any{"DiskId": i.DiskID, "InstanceId": i.ID, "Size": i.DiskGB, "Type": "system",
				"Category": "cloud_essd", "Status": "In_use", "RegionId": i.Region, "ZoneId": i.Zone, "DiskName": "", "Device": "/dev/xvda"})
		}
		return map[string]any{"Disks": map[string]any{"Disk": list}, "NextToken": next, "TotalCount": len(match)}, nil
	case "DescribeInstanceAutoRenewAttribute":
		if q["InstanceId"] == "" && q["RenewalStatus"] == "" {
			return nil, refuse(400, "InvalidParameter", "InstanceId or RenewalStatus must be specified.")
		}
		list := []map[string]any{}
		for _, id := range strings.Split(q["InstanceId"], ",") {
			i := f.ECS[id]
			if i == nil || i.Region != region || i.ChargeType != "PrePaid" {
				continue
			}
			status := "Normal"
			if i.AutoRenew {
				status = "AutoRenewal"
			}
			list = append(list, map[string]any{"InstanceId": id, "AutoRenewEnabled": i.AutoRenew, "Duration": 1, "PeriodUnit": "Month", "RenewalStatus": status})
		}
		return map[string]any{"InstanceRenewAttributes": map[string]any{"InstanceRenewAttribute": list}, "TotalCount": len(list), "PageNumber": 1, "PageSize": 100}, nil
	case "StartInstance", "StopInstance", "RebootInstance":
		i, err := here(q["InstanceId"])
		if err != nil {
			return nil, err
		}
		if m := q["StoppedMode"]; m != "" && m != "KeepCharging" && m != "StopCharging" {
			return nil, refuse(400, "InvalidStoppedMode", "The specified StoppedMode is not valid.")
		}
		return nil, power(action, &i.Status, &i.pending)
	case "DescribeSecurityGroupAttribute":
		g := q["SecurityGroupId"]
		if groupRegion(g) != region {
			return nil, refuse(404, "InvalidSecurityGroupId.NotFound", "The specified SecurityGroupId does not exist.")
		}
		if d := q["Direction"]; d != "" && d != "ingress" && d != "egress" && d != "all" {
			return nil, refuse(400, "InvalidDirection.Malformed", "The specified Direction is not valid.")
		}
		rules := f.Groups[g]
		from, to, next := f.pageToken(q, "MaxResults", 500, len(rules))
		list := []map[string]any{}
		for _, r := range rules[from:to] {
			m := map[string]any{"SecurityGroupRuleId": r.ID, "Direction": "ingress", "IpProtocol": r.Protocol, "PortRange": r.PortRange,
				"Policy": r.Policy, "Priority": strconv.Itoa(r.Priority), "Description": r.Description, "NicType": "intranet",
				"SourceCidrIp": "", "Ipv6SourceCidrIp": "", "SourceGroupId": "", "SourcePrefixListId": "", "DestCidrIp": "", "CreateTime": "2025-01-02T03:04:05Z"}
			switch {
			case strings.HasPrefix(r.Source, "sg-"):
				m["SourceGroupId"] = r.Source
			case strings.HasPrefix(r.Source, "pl-"):
				m["SourcePrefixListId"] = r.Source
			case strings.Contains(r.Source, ":"):
				m["Ipv6SourceCidrIp"] = r.Source
			default:
				m["SourceCidrIp"] = r.Source
			}
			list = append(list, m)
		}
		return map[string]any{"SecurityGroupId": g, "SecurityGroupName": g, "RegionId": region, "VpcId": "vpc-fake", "Description": "",
			"InnerAccessPolicy": "Accept", "Permissions": map[string]any{"Permission": list}, "NextToken": next}, nil
	case "AuthorizeSecurityGroup":
		g := q["SecurityGroupId"]
		if groupRegion(g) != region {
			return nil, refuse(404, "InvalidSecurityGroupId.NotFound", "The specified SecurityGroupId does not exist.")
		}
		var add []GroupRule
		for n := 1; q["Permissions."+strconv.Itoa(n)+".IpProtocol"] != "" || q["Permissions."+strconv.Itoa(n)+".PortRange"] != ""; n++ {
			p := func(k string) string { return q["Permissions."+strconv.Itoa(n)+"."+k] }
			r, err := groupRule(p)
			if err != nil {
				return nil, err
			}
			r.ID = f.id("sgr-")
			add = append(add, r)
		}
		if len(add) == 0 {
			return nil, refuse(400, "MissingParameter", "Permissions is mandatory for this action.")
		}
		f.Groups[g] = append(f.Groups[g], add...)
		return nil, nil
	case "RevokeSecurityGroup":
		g := q["SecurityGroupId"]
		if groupRegion(g) != region {
			return nil, refuse(404, "InvalidSecurityGroupId.NotFound", "The specified SecurityGroupId does not exist.")
		}
		var gone []string
		for n := 1; q["SecurityGroupRuleId."+strconv.Itoa(n)] != ""; n++ {
			gone = append(gone, q["SecurityGroupRuleId."+strconv.Itoa(n)])
		}
		if len(gone) == 0 {
			return nil, refuse(400, "MissingParameter", "SecurityGroupRuleId or Permissions is mandatory for this action.")
		}
		for _, id := range gone {
			found := false
			for _, r := range f.Groups[g] {
				found = found || r.ID == id
			}
			if !found {
				return nil, refuse(404, "InvalidSecurityGroupRuleId.NotFound", "The specified SecurityGroupRuleId %s does not exist.", id)
			}
		}
		var keep []GroupRule
		for _, r := range f.Groups[g] {
			if !in(gone, r.ID) {
				keep = append(keep, r)
			}
		}
		f.Groups[g] = keep
		return nil, nil
	case "DescribeSnapshots":
		var match []*Snap
		for _, s := range f.Snapshots {
			if s.Kind == "ecs" && s.Region == region && (q["DiskId"] == "" || q["DiskId"] == s.DiskID) && (q["InstanceId"] == "" || q["InstanceId"] == s.Instance) {
				match = append(match, s)
			}
		}
		from, to, next := f.pageToken(q, "MaxResults", 10, len(match))
		list := []map[string]any{}
		for _, s := range match[from:to] {
			list = append(list, f.snapshot(s))
		}
		return map[string]any{"Snapshots": map[string]any{"Snapshot": list}, "NextToken": next, "TotalCount": len(match)}, nil
	case "CreateSnapshot":
		var owner *ECSInstance
		for _, i := range f.ECS {
			if i.DiskID == q["DiskId"] && i.Region == region {
				owner = i
			}
		}
		if owner == nil {
			return nil, refuse(404, "InvalidDiskId.NotFound", "The specified disk does not exist.")
		}
		name := q["SnapshotName"]
		if name != "" && (!ecsSnapName.MatchString(name) || strings.HasPrefix(name, "auto") || strings.HasPrefix(name, "http")) {
			return nil, refuse(400, "InvalidSnapshotName.Malformed", "Specified snapshot name is not valid.")
		}
		s := &Snap{ID: f.id("s-bp1fake"), Name: name, DiskID: owner.DiskID, Instance: owner.ID, Region: region, Kind: "ecs", Created: f.Now(), SizeGB: owner.DiskGB}
		f.Snapshots = append(f.Snapshots, s)
		return map[string]any{"SnapshotId": s.ID}, nil
	}
	return nil, refuse(404, "InvalidAction.NotFound", "Specified api is not found.")
}

var (
	portRange = regexp.MustCompile(`^(\d+)/(\d+)$`)
	swasPort  = regexp.MustCompile(`^\d+(/\d+)?$`)
)

// groupRule reads one Permissions.N entry as ECS checks it.
func groupRule(p func(string) string) (GroupRule, *apiErr) {
	r := GroupRule{Protocol: strings.ToUpper(p("IpProtocol")), PortRange: p("PortRange"), Description: p("Description"), Priority: 1}
	switch r.Protocol {
	case "TCP", "UDP":
		m := portRange.FindStringSubmatch(r.PortRange)
		if m == nil {
			return r, refuse(400, "InvalidPortRange.Malformed", "The specified PortRange %q is not valid.", r.PortRange)
		}
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		if a < 1 || b > 65535 || a > b {
			return r, refuse(400, "InvalidPortRange.Malformed", "The specified PortRange %q is not valid.", r.PortRange)
		}
	case "ICMP", "ICMPV6", "GRE", "ALL":
		if r.PortRange != "-1/-1" {
			return r, refuse(400, "InvalidPortRange.Malformed", "The PortRange of %s must be -1/-1.", r.Protocol)
		}
	default:
		return r, refuse(400, "InvalidIpProtocol.Malformed", "The specified IpProtocol %q is not valid.", p("IpProtocol"))
	}
	for _, k := range []string{"SourceCidrIp", "Ipv6SourceCidrIp", "SourceGroupId", "SourcePrefixListId"} {
		if r.Source == "" {
			r.Source = p(k)
		}
	}
	if r.Source == "" {
		return r, refuse(400, "MissingParameter", "One of SourceCidrIp, Ipv6SourceCidrIp, SourceGroupId and SourcePrefixListId is mandatory.")
	}
	switch strings.ToLower(p("Policy")) {
	case "", "accept":
		r.Policy = "Accept"
	case "drop":
		r.Policy = "Drop"
	default:
		return r, refuse(400, "InvalidPolicy.Malformed", "The specified Policy is not valid.")
	}
	if s := p("Priority"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 100 {
			return r, refuse(400, "InvalidPriority.Malformed", "The specified Priority is not valid.")
		}
		r.Priority = n
	}
	return r, nil
}

func (f *Cloud) serveSWAS(action, region string, q map[string]string) (map[string]any, *apiErr) {
	here := func(id string) (*SWASInstance, *apiErr) {
		if i := f.SWAS[id]; i != nil && i.Region == region {
			return i, nil
		}
		return nil, refuse(404, "InvalidInstanceId.NotFound", "The specified instance does not exist.")
	}
	switch action {
	case "ListRegions":
		var list []map[string]any
		for _, r := range f.SWASRegions {
			list = append(list, map[string]any{"RegionId": r.ID, "LocalName": r.Name, "RegionEndpoint": r.Endpoint})
		}
		return map[string]any{"Regions": list}, nil
	case "ListInstances":
		want, err := ids(q["InstanceIds"])
		if err != nil {
			return nil, err
		}
		var match []*SWASInstance
		for _, id := range sortedKeys(f.SWAS) {
			if i := f.SWAS[id]; i.Region == region && (want == nil || in(want, id)) {
				match = append(match, i)
			}
		}
		from, to := f.pageNumber(q, 10, len(match))
		list := []map[string]any{}
		for _, i := range match[from:to] {
			list = append(list, map[string]any{
				"InstanceId": i.ID, "InstanceName": i.Name, "RegionId": i.Region, "Status": look(&i.Status, &i.pending), "PlanId": i.Plan,
				"ChargeType": "PrePaid", "ExpiredTime": i.ExpiredTime, "CreationTime": "2025-01-02T03:04:05Z", "BusinessStatus": "Normal",
				"DdosStatus": "Normal", "PublicIpAddress": i.PublicIP, "InnerIpAddress": i.PrivateIP, "ImageId": "fe9c66133a9d4688872869726b52fake",
				"Uuid": "41f30524-5df7-49c9-9c6e-32fake489001", "Combination": false, "CombinationInstanceId": "", "DisableReason": "",
				"ResourceSpec": map[string]any{"Cpu": i.CPU, "Memory": i.MemoryGB, "DiskSize": i.DiskGB, "Bandwidth": i.Bandwidth,
					"DiskCategory": "ESSD", "Flow": float64(i.TrafficTotal >> 30)},
				"Image": map[string]any{"ImageName": i.ImageName, "ImageVersion": i.ImageVersion, "OsType": "Linux", "ImageType": "system",
					"ImageIconUrl": "", "ImageContact": ""},
				"Disks": []map[string]any{{"DiskId": i.DiskID, "DiskType": "system", "Size": i.DiskGB, "Category": "ESSD", "Status": "In_use",
					"Device": "/dev/xvda", "DiskName": "SystemDisk", "RegionId": i.Region, "DiskChargeType": "PrePaid", "CreationTime": "2025-01-02T03:04:05Z"}},
				"NetworkAttributes": []map[string]any{{"PublicIpAddress": i.PublicIP, "PrivateIpAddress": i.PrivateIP, "PeakBandwidth": i.Bandwidth * 10,
					"PublicIpDdosStatus": "Normal"}},
				"Tags": []any{},
			})
		}
		return map[string]any{"Instances": list, "TotalCount": len(match), "PageNumber": 1, "PageSize": len(list)}, nil
	case "ListInstancesTrafficPackages":
		want, err := ids(q["InstanceIds"])
		if err != nil {
			return nil, err
		}
		list := []map[string]any{}
		for _, id := range want {
			if i := f.SWAS[id]; i != nil && i.Region == region {
				list = append(list, map[string]any{"InstanceId": id, "TrafficUsed": i.TrafficUsed, "TrafficPackageTotal": i.TrafficTotal,
					"TrafficPackageRemaining": i.TrafficTotal - i.TrafficUsed, "TrafficOverflow": 0})
			}
		}
		return map[string]any{"InstanceTrafficPackageUsages": list}, nil
	case "StartInstance", "StopInstance", "RebootInstance":
		i, err := here(q["InstanceId"])
		if err != nil {
			return nil, err
		}
		return nil, power(action, &i.Status, &i.pending)
	case "ListFirewallRules":
		if _, err := here(q["InstanceId"]); err != nil {
			return nil, err
		}
		rules := f.SWASRules[q["InstanceId"]]
		from, to := f.pageNumber(q, 10, len(rules))
		list := []map[string]any{}
		for _, r := range rules[from:to] {
			list = append(list, map[string]any{"RuleId": r.ID, "RuleProtocol": r.Protocol, "Port": r.Port, "SourceCidrIp": r.Source,
				"Policy": r.Policy, "Remark": r.Remark, "Tags": []any{}})
		}
		return map[string]any{"FirewallRules": list, "TotalCount": len(rules), "PageNumber": 1, "PageSize": len(list)}, nil
	case "CreateFirewallRules":
		if _, err := here(q["InstanceId"]); err != nil {
			return nil, err
		}
		var rules []map[string]string
		dec := json.NewDecoder(strings.NewReader(q["FirewallRules"]))
		if err := dec.Decode(&rules); err != nil || len(rules) == 0 {
			return nil, refuse(400, "InvalidParameter.FirewallRules", "FirewallRules must be a JSON array of rules.")
		}
		var made []string
		for _, r := range rules {
			for k := range r {
				if k != "RuleProtocol" && k != "Port" && k != "SourceCidrIp" && k != "Remark" {
					return nil, refuse(400, "InvalidParameter.FirewallRules", "A firewall rule has no field %s.", k)
				}
			}
			proto := strings.ToUpper(r["RuleProtocol"])
			switch proto {
			case "TCP", "UDP", "TCP+UDP":
				if !swasPort.MatchString(r["Port"]) {
					return nil, refuse(400, "InvalidParameter.Port", "The specified port %q is not valid.", r["Port"])
				}
			case "ICMP":
				if r["Port"] != "-1/-1" {
					return nil, refuse(400, "InvalidParameter.Port", "ICMP takes the port -1/-1.")
				}
			default:
				return nil, refuse(400, "InvalidParameter.RuleProtocol", "The specified protocol %q is not valid.", r["RuleProtocol"])
			}
			src := r["SourceCidrIp"]
			if src == "" {
				src = "0.0.0.0/0"
			}
			id := f.id("fwrule")
			f.SWASRules[q["InstanceId"]] = append(f.SWASRules[q["InstanceId"]], SWASRule{ID: id, Protocol: proto, Port: r["Port"], Source: src, Policy: "accept", Remark: r["Remark"]})
			made = append(made, id)
		}
		return map[string]any{"FirewallRuleIds": made}, nil
	case "DeleteFirewallRules":
		if _, err := here(q["InstanceId"]); err != nil {
			return nil, err
		}
		gone := strings.Split(q["RuleIds"], ",")
		for _, id := range gone {
			found := false
			for _, r := range f.SWASRules[q["InstanceId"]] {
				found = found || r.ID == id
			}
			if !found {
				return nil, refuse(404, "InvalidFirewallRuleId.NotFound", "The specified firewall rule %q does not exist.", id)
			}
		}
		var keep []SWASRule
		for _, r := range f.SWASRules[q["InstanceId"]] {
			if !in(gone, r.ID) {
				keep = append(keep, r)
			}
		}
		f.SWASRules[q["InstanceId"]] = keep
		return nil, nil
	case "ListDisks":
		var match []*SWASInstance
		for _, id := range sortedKeys(f.SWAS) {
			if i := f.SWAS[id]; i.Region == region && (q["InstanceId"] == "" || q["InstanceId"] == id) {
				match = append(match, i)
			}
		}
		from, to := f.pageNumber(q, 10, len(match))
		list := []map[string]any{}
		for _, i := range match[from:to] {
			list = append(list, map[string]any{"DiskId": i.DiskID, "DiskType": "system", "Size": i.DiskGB, "InstanceId": i.ID, "InstanceName": i.Name,
				"Category": "ESSD", "Status": "In_use", "Device": "/dev/xvda", "DiskName": "SystemDisk", "RegionId": i.Region, "DiskChargeType": "PrePaid"})
		}
		return map[string]any{"Disks": list, "TotalCount": len(match), "PageNumber": 1, "PageSize": len(list)}, nil
	case "ListSnapshots":
		var match []*Snap
		for _, s := range f.Snapshots {
			if s.Kind == "swas" && s.Region == region && (q["DiskId"] == "" || q["DiskId"] == s.DiskID) && (q["InstanceId"] == "" || q["InstanceId"] == s.Instance) {
				match = append(match, s)
			}
		}
		from, to := f.pageNumber(q, 10, len(match))
		list := []map[string]any{}
		for _, s := range match[from:to] {
			list = append(list, f.snapshot(s))
		}
		return map[string]any{"Snapshots": list, "TotalCount": len(match), "PageNumber": 1, "PageSize": len(list)}, nil
	case "CreateSnapshot":
		var owner *SWASInstance
		for _, i := range f.SWAS {
			if i.DiskID == q["DiskId"] && i.Region == region {
				owner = i
			}
		}
		if owner == nil {
			return nil, refuse(404, "InvalidDiskId.NotFound", "The specified disk does not exist.")
		}
		if !swasSnapName.MatchString(q["SnapshotName"]) || strings.HasPrefix(q["SnapshotName"], "http") {
			return nil, refuse(400, "InvalidSnapshotName.Malformed", "The specified snapshot name is not valid.")
		}
		s := &Snap{ID: f.id("s-bp1swas"), Name: q["SnapshotName"], DiskID: owner.DiskID, Instance: owner.ID, Region: region, Kind: "swas", Created: f.Now()}
		f.Snapshots = append(f.Snapshots, s)
		return map[string]any{"SnapshotId": s.ID}, nil
	case "DescribeMonitorData":
		i, err := here(q["InstanceId"])
		if err != nil {
			return nil, err
		}
		metric := q["MetricName"]
		switch metric {
		case "MEMORY_ACTUALUSEDSPACE", "DISKUSAGE_USED", "CPU_UTILIZATION", "VPC_PUBLICIP_INTERNETOUT_RATE", "VPC_PUBLICIP_INTERNETIN_RATE",
			"DISK_READ_IOPS", "DISK_WRITE_IOPS":
			if p := q["Period"]; p != "60" && p != "300" && p != "900" {
				return nil, refuse(400, "InvalidParameter.Period", "The specified Period is not valid.")
			}
		case "FLOW_USED":
			if q["Period"] != "3600" {
				return nil, refuse(400, "InvalidParameter.Period", "FLOW_USED takes a Period of 3600.")
			}
		default:
			return nil, refuse(400, "InvalidParameter.MetricName", "The specified MetricName is not valid.")
		}
		start, e1 := time.Parse("2006-01-02T15:04:05Z", q["StartTime"])
		end, e2 := time.Parse("2006-01-02T15:04:05Z", q["EndTime"])
		if e1 != nil || e2 != nil || !start.Before(end) || end.Sub(start) > 31*24*time.Hour {
			return nil, refuse(400, "InvalidParameter.Time", "StartTime and EndTime must be ISO 8601 times at most 31 days apart.")
		}
		return f.samples(q, i.ID+" "+metric, start, end, "Length")
	}
	return nil, refuse(404, "InvalidAction.NotFound", "Specified api is not found.")
}

// samples answers a monitoring query from the fake's samples, as the
// JSON string both monitoring APIs return.
func (f *Cloud) samples(q map[string]string, key string, start, end time.Time, sizeKey string) (map[string]any, *apiErr) {
	var match []Sample
	for _, s := range f.Metrics[key] {
		if s.T.After(start) && !s.T.After(end) {
			match = append(match, s)
		}
	}
	sort.Slice(match, func(i, j int) bool { return match[i].T.Before(match[j].T) })
	from, to, next := f.pageToken(q, sizeKey, 1440, len(match))
	id, _, _ := strings.Cut(key, " ")
	list := []map[string]any{}
	for _, s := range match[from:to] {
		list = append(list, map[string]any{"timestamp": s.T.UnixMilli(), "instanceId": id, "userId": "1234567890123456",
			"Average": s.V, "Maximum": s.V, "Minimum": s.V})
	}
	b, _ := json.Marshal(list)
	return map[string]any{"Datapoints": string(b), "NextToken": next, "Period": q["Period"]}, nil
}

func (f *Cloud) serveCMS(action, region string, q map[string]string) (map[string]any, *apiErr) {
	if q["Namespace"] != "acs_ecs_dashboard" {
		// CloudMonitor answers these in the body, with HTTP 200.
		return map[string]any{"Code": "404", "Success": false, "Message": "The specified resource is not found."}, nil
	}
	var dims []map[string]string
	if json.Unmarshal([]byte(q["Dimensions"]), &dims) != nil || len(dims) != 1 || dims[0]["instanceId"] == "" {
		return nil, refuse(400, "InvalidParameter", "Dimensions must be a JSON array of objects.")
	}
	switch q["Period"] {
	case "", "15", "60", "300", "900", "3600":
	default:
		return nil, refuse(400, "InvalidParameter", "The specified Period is not valid.")
	}
	ms := func(s string) (time.Time, bool) {
		n, err := strconv.ParseInt(s, 10, 64)
		return time.UnixMilli(n), err == nil
	}
	start, ok1 := ms(q["StartTime"])
	end, ok2 := ms(q["EndTime"])
	if !ok1 || !ok2 || !start.Before(end) {
		return nil, refuse(400, "InvalidParameter", "StartTime and EndTime must be times in milliseconds.")
	}
	key := dims[0]["instanceId"] + " " + q["MetricName"]
	// CloudMonitor has nothing for a resource of another region, or for an
	// IP the instance does not have.
	if i := f.ECS[dims[0]["instanceId"]]; i == nil || i.Region != region || (dims[0]["ip"] != "" && dims[0]["ip"] != i.PublicIP) {
		key = ""
	}
	resp, err := f.samples(q, key, start, end, "Length")
	if resp != nil {
		resp["Code"], resp["Success"] = "200", true
	}
	return resp, err
}
