package tencenttest

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// serveMore handles the server, monitoring and EdgeOne analytics calls.
func (f *Fake) serveMore(w http.ResponseWriter, service, action, region string, in map[string]any) bool {
	str := func(k string) string { s, _ := in[k].(string); return s }
	f.Regions = append(f.Regions, region)
	here := region == "ap-guangzhou"
	// instance returns what a look at the instance shows now; a power
	// action shows its transition once and then reaches its final state.
	instance := func(id string) *Instance {
		i := f.Instances[id]
		if i == nil || !here {
			return nil
		}
		now := *i
		if i.pending != "" {
			i.State, i.pending = i.pending, ""
		}
		return &now
	}
	firstID := func() string {
		if ids, _ := in["InstanceIds"].([]any); len(ids) > 0 {
			s, _ := ids[0].(string)
			return s
		}
		return str("InstanceId")
	}
	rules := func(key string, raw any) []tencent.FirewallRule {
		var out []tencent.FirewallRule
		list, _ := raw.([]any)
		for _, x := range list {
			m, _ := x.(map[string]any)
			s := func(k string) string { v, _ := m[k].(string); return v }
			out = append(out, tencent.FirewallRule{Protocol: s("Protocol"), Port: s("Port"), CidrBlock: s("CidrBlock"), Action: s("Action")})
		}
		return out
	}
	switch service + " " + action {
	case "lighthouse DescribeRegions", "cvm DescribeRegions":
		ok(w, map[string]any{"RegionSet": []map[string]string{
			{"Region": "ap-guangzhou", "RegionName": "华南地区(广州)"}, {"Region": "ap-shanghai", "RegionName": "华东地区(上海)"}}})
	case "lighthouse DescribeInstances", "cvm DescribeInstances":
		var list []map[string]any
		for _, i := range f.Instances {
			if !here || (service == "lighthouse") != strings.HasPrefix(i.ID, "lhins-") {
				continue
			}
			inst := instance(i.ID)
			m := map[string]any{"InstanceId": inst.ID, "InstanceName": inst.Name, "InstanceState": inst.State, "CPU": 2, "Memory": 4,
				"SystemDisk": map[string]any{"DiskId": inst.DiskID, "DiskSize": 60}, "InternetAccessible": map[string]any{"InternetMaxBandwidthOut": 6},
				"OsName": "Ubuntu Server 24.04 LTS 64bit", "ExpiredTime": time.Now().Add(10 * 24 * time.Hour).UTC().Format(time.RFC3339),
				"RenewFlag": "NOTIFY_AND_MANUAL_RENEW", "InstanceChargeType": "PREPAID"}
			if service == "lighthouse" {
				m["PublicAddresses"], m["Zone"] = []string{inst.IP}, "ap-guangzhou-3"
			} else {
				m["PublicIpAddresses"], m["Placement"], m["SecurityGroupIds"] = []string{inst.IP}, map[string]string{"Zone": "ap-guangzhou-6"}, []string{inst.Group}
			}
			list = append(list, m)
		}
		ok(w, map[string]any{"TotalCount": len(list), "InstanceSet": list})
	case "lighthouse DescribeInstancesTrafficPackages":
		ok(w, map[string]any{"InstanceTrafficPackageSet": []map[string]any{{"InstanceId": "lhins-abc12345",
			"TrafficPackageSet": []map[string]any{{"TrafficUsed": 900 << 30, "TrafficPackageTotal": 1000 << 30}}}}})
	case "lighthouse StartInstances", "lighthouse StopInstances", "lighthouse RebootInstances",
		"cvm StartInstances", "cvm StopInstances", "cvm RebootInstances":
		i := f.Instances[firstID()]
		if i == nil || !here {
			fail(w, "InvalidInstanceId.NotFound", "实例不存在。")
			return true
		}
		switch action {
		case "StartInstances":
			i.State, i.pending = "STARTING", "RUNNING"
		case "StopInstances":
			i.State, i.pending = "STOPPING", "STOPPED"
		default:
			i.State, i.pending = "REBOOTING", "RUNNING"
		}
		ok(w, nil)
	case "lighthouse DescribeFirewallRules":
		var out []map[string]string
		for _, r := range f.Firewall[str("InstanceId")] {
			out = append(out, map[string]string{"Protocol": r.Protocol, "Port": r.Port, "CidrBlock": r.CidrBlock, "Action": r.Action, "FirewallRuleDescription": r.Description})
		}
		ok(w, map[string]any{"TotalCount": len(out), "FirewallRuleSet": out, "FirewallVersion": 3})
	case "lighthouse CreateFirewallRules":
		for _, r := range rules("", in["FirewallRules"]) {
			for _, o := range f.Firewall[str("InstanceId")] {
				if o.Same(r) {
					fail(w, "FailedOperation.FirewallRulesExist", "防火墙规则已存在。")
					return true
				}
			}
			f.Firewall[str("InstanceId")] = append(f.Firewall[str("InstanceId")], r)
		}
		ok(w, nil)
	case "lighthouse DeleteFirewallRules":
		f.Firewall[str("InstanceId")] = without(f.Firewall[str("InstanceId")], rules("", in["FirewallRules"]))
		ok(w, nil)
	case "vpc DescribeSecurityGroupPolicies":
		var out []map[string]any
		for i, r := range f.Firewall[str("SecurityGroupId")] {
			out = append(out, map[string]any{"PolicyIndex": i, "Protocol": r.Protocol, "Port": r.Port, "CidrBlock": r.CidrBlock, "Action": r.Action})
		}
		ok(w, map[string]any{"SecurityGroupPolicySet": map[string]any{"Ingress": out, "Version": "5"}})
	case "vpc CreateSecurityGroupPolicies":
		set, _ := in["SecurityGroupPolicySet"].(map[string]any)
		f.Firewall[str("SecurityGroupId")] = append(rules("", set["Ingress"]), f.Firewall[str("SecurityGroupId")]...)
		ok(w, nil)
	case "vpc DeleteSecurityGroupPolicies":
		set, _ := in["SecurityGroupPolicySet"].(map[string]any)
		f.Firewall[str("SecurityGroupId")] = without(f.Firewall[str("SecurityGroupId")], rules("", set["Ingress"]))
		ok(w, nil)
	case "lighthouse CreateInstanceSnapshot", "cbs CreateSnapshot":
		f.nextID++
		id := fmt.Sprintf("snap-%d", f.nextID)
		owner := str("InstanceId")
		if owner == "" {
			owner = str("DiskId")
		}
		f.Snaps[id] = &tencent.Snapshot{ID: id, Name: str("SnapshotName") + "|" + owner, State: "CREATING", Percent: 40}
		ok(w, map[string]any{"SnapshotId": id})
	case "lighthouse DescribeSnapshots", "cbs DescribeSnapshots":
		var out []map[string]any
		want := ""
		if fl, _ := in["Filters"].([]any); len(fl) > 0 {
			if v, _ := fl[0].(map[string]any)["Values"].([]any); len(v) > 0 {
				want, _ = v[0].(string)
			}
		}
		for _, sn := range f.Snaps {
			name, owner, _ := strings.Cut(sn.Name, "|")
			if owner != want {
				continue
			}
			created := map[string]any{"SnapshotId": sn.ID, "SnapshotName": name, "SnapshotState": sn.State, "Percent": sn.Percent, "DiskSize": 60}
			out = append(out, created)
			sn.State, sn.Percent = "NORMAL", 100 // finished by the next look
		}
		ok(w, map[string]any{"TotalCount": len(out), "SnapshotSet": out})
	case "monitor DescribeBaseMetrics":
		ok(w, map[string]any{"MetricSet": []map[string]any{
			{"MetricName": "CpuUsage", "MetricCName": "CPU利用率", "Unit": "%", "Period": []int{60, 300}},
			{"MetricName": "MemUsage", "MetricCName": "内存利用率", "Unit": "%", "Period": []int{60, 300}},
			{"MetricName": "LighthouseOuttraffic", "MetricCName": "外网出带宽", "Unit": "Mbps", "Period": []int{60, 300}},
			{"MetricName": "DiskUsage", "Unit": "%", "Period": []int{60}},
		}})
	case "monitor GetMonitorData":
		now := time.Now().Unix()
		var ts, vs []float64
		for i := 0; i < 12; i++ {
			ts = append(ts, float64(now-int64(12-i)*300))
			vs = append(vs, float64(20+i*5))
		}
		ok(w, map[string]any{"MetricName": str("MetricName"), "DataPoints": []map[string]any{{"Timestamps": ts, "Values": vs}}})
	case "teo DescribeTimingL7AnalysisData", "teo DescribeTimingL7CacheData":
		metrics, _ := in["MetricNames"].([]any)
		hit := strings.Contains(fmt.Sprint(in["Filters"]), "hit")
		var tv []map[string]any
		now := time.Now().Unix()
		for _, m := range metrics {
			name, _ := m.(string)
			var detail []map[string]int64
			var sum, peak int64
			for i := 0; i < 24; i++ {
				v := int64(100 + i*10)
				if i == 20 {
					v = 5000 // a spike
				}
				if hit {
					v /= 2
				}
				detail = append(detail, map[string]int64{"Timestamp": now - int64(24-i)*3600, "Value": v})
				sum += v
				peak = max(peak, v)
			}
			tv = append(tv, map[string]any{"MetricName": name, "Sum": sum, "Max": peak, "Avg": sum / 24, "Detail": detail})
		}
		ok(w, map[string]any{"Data": []map[string]any{{"TypeKey": "zone-abc", "TypeValue": tv}}, "TotalCount": 1})
	case "teo DescribeTopL7AnalysisData":
		ok(w, map[string]any{"Data": []map[string]any{{"TypeKey": "zone-abc", "DetailData": []map[string]any{
			{"Key": "/wp-login.php", "Value": 4200}, {"Key": "/", "Value": 900}, {"Key": "/feed", "Value": 120}}}}})
	case "teo CreatePurgeTask":
		ok(w, map[string]any{"JobId": "purge-1"})
	case "teo CreatePrefetchTask":
		ok(w, map[string]any{"JobId": "prefetch-1"})
	case "teo ModifyAccelerationDomain":
		for _, d := range f.Domains {
			if d.Name == str("DomainName") {
				origin, _ := in["OriginInfo"].(map[string]any)
				d.Origin, _ = origin["Origin"].(string)
				if p := str("OriginProtocol"); p != "" {
					d.Protocol = p
				}
			}
		}
		ok(w, nil)
	default:
		return f.serveEO(w, service, action, in) || f.serveTAT(w, service, action, region, in)
	}
	return true
}

func without(list, remove []tencent.FirewallRule) []tencent.FirewallRule {
	var out []tencent.FirewallRule
	for _, r := range list {
		keep := true
		for _, x := range remove {
			if r.Same(x) {
				keep = false
			}
		}
		if keep {
			out = append(out, r)
		}
	}
	return out
}
