package tencenttest

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// serveEO handles EdgeOne sites, plans and security policies.
func (f *Fake) serveEO(w http.ResponseWriter, service, action string, in map[string]any) bool {
	str := func(k string) string { s, _ := in[k].(string); return s }
	zone := func(id string) *tencent.Zone {
		for i := range f.Zones {
			if f.Zones[i].ZoneID == id {
				return &f.Zones[i]
			}
		}
		return nil
	}
	switch service + " " + action {
	case "ssl DescribeCertificates":
		end := time.Now().Add(10 * 24 * time.Hour).In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")
		ok(w, map[string]any{"TotalCount": 1, "Certificates": []map[string]any{{
			"CertificateId": "ssl-abc", "Domain": "api.example.com", "SubjectAltName": []string{"api.example.com"}, "From": "trustasia",
			"ProductZhName": "TrustAsia 免费版", "Status": 1, "StatusName": "已通过", "CertEndTime": end, "IsDv": true, "HostingStatus": -1,
		}}})
	case "teo DownloadL7Logs":
		zones, _ := in["ZoneIds"].([]any)
		start, _ := time.Parse(time.RFC3339, str("StartTime"))
		end, _ := time.Parse(time.RFC3339, str("EndTime"))
		if len(zones) != 1 || start.IsZero() || end.IsZero() {
			fail(w, "InvalidParameter", "bad DownloadL7Logs request")
			return true
		}
		var data []map[string]any
		for _, p := range f.L7Logs[fmt.Sprint(zones[0])] {
			if p.Start.Before(start.Add(-time.Hour)) || p.Start.After(end) {
				continue
			}
			data = append(data, map[string]any{"Domain": p.Domain, "Area": "mainland", "LogPacketName": p.Name, "Url": f.URL + "/eolog/" + p.Name,
				"LogStartTime": p.Start.UTC().Format(time.RFC3339), "LogEndTime": p.Start.Add(time.Hour).UTC().Format(time.RFC3339), "Size": len(p.Lines)})
		}
		off, _ := in["Offset"].(float64)
		lim, _ := in["Limit"].(float64)
		total := len(data)
		data = data[min(int(off), total):min(int(off+lim), total)]
		ok(w, map[string]any{"TotalCount": total, "Data": data})
	case "teo DescribeIPRegion":
		ips, _ := in["IPs"].([]any)
		if len(ips) > 100 {
			fail(w, "InvalidParameter.IPsTooMany", "at most 100 IPs")
			return true
		}
		var info []map[string]any
		for _, ip := range ips {
			yes := "no"
			if f.EdgeOneNodes[fmt.Sprint(ip)] {
				yes = "yes"
			}
			info = append(info, map[string]any{"IP": ip, "IsEdgeOneIP": yes})
		}
		ok(w, map[string]any{"IPRegionInfo": info})
	case "teo DescribeSecurityPolicy":
		if str("Entity") != "ZoneDefaultPolicy" {
			fail(w, "InvalidParameter", "fake only has site policies")
			return true
		}
		p := f.Policies[str("ZoneId")]
		if p == nil {
			p = map[string]any{}
			f.Policies[str("ZoneId")] = p
		}
		ok(w, map[string]any{"SecurityPolicy": p})
	case "teo ModifySecurityPolicy":
		p := f.Policies[str("ZoneId")]
		sp, _ := in["SecurityPolicy"].(map[string]any)
		if p == nil || str("Entity") != "ZoneDefaultPolicy" || sp == nil {
			fail(w, "InvalidParameter.Security", "bad policy request")
			return true
		}
		for _, module := range []string{"CustomRules", "RateLimitingRules"} {
			m, present := sp[module].(map[string]any)
			if !present {
				continue // left out: unchanged
			}
			rules, _ := m["Rules"].([]any)
			for _, r := range rules {
				rule := r.(map[string]any)
				if rule["Name"] == "" || rule["Condition"] == nil || !strings.Contains(rule["Condition"].(string), "${") {
					fail(w, "InvalidParameter.Security", "rule needs a name and a condition")
					return true
				}
				if id, _ := rule["Id"].(string); id == "" {
					f.nextID++
					rule["Id"] = fmt.Sprintf("rule-%d", f.nextID)
				}
			}
			p[module] = map[string]any{"Rules": rules} // a list replaces the old one
		}
		if d, present := sp["HttpDDoSProtection"].(map[string]any); present {
			cur, _ := p["HttpDDoSProtection"].(map[string]any)
			if cur == nil {
				cur = map[string]any{}
			}
			for k, sub := range d {
				m := sub.(map[string]any)
				if _, has := m["Id"]; has {
					fail(w, "InvalidParameter.Security", "Id is output only")
					return true
				}
				m["Id"] = k + "-id"
				cur[k] = m
			}
			p["HttpDDoSProtection"] = cur
		}
		ok(w, nil)
	case "teo DescribePlans":
		ok(w, map[string]any{"TotalCount": len(f.Plans), "Plans": f.Plans})
	case "teo CreateZone":
		name := str("ZoneName")
		if str("Type") != "partial" || strings.Count(name, ".") != 1 {
			fail(w, "InvalidParameterValue.ZoneNameNotSupportSubDomain", "站点名称不支持子域名。")
			return true
		}
		for _, z := range f.Zones {
			if z.ZoneName == name {
				fail(w, "ResourceInUse.Others", "站点已存在。")
				return true
			}
		}
		for i, p := range f.Plans {
			if p.PlanID == str("PlanId") {
				if p.Bindable != "true" {
					fail(w, "LimitExceeded.ZoneBindPlan", "套餐可绑定站点数已达上限。")
					return true
				}
				f.Plans[i].Bindable = "false"
				f.nextID++
				id := fmt.Sprintf("zone-%d", f.nextID)
				dv := &tencent.DNSVerification{Subdomain: "_eo-verification", RecordType: "TXT", RecordValue: "verify-" + id}
				z := tencent.Zone{ZoneID: id, ZoneName: name, Type: "partial", Status: "pending", Area: str("Area"), CnameStatus: "pending"}
				z.CNAMEDetail = &tencent.CNAMEDetail{OwnershipVerification: &tencent.Ownership{DNSVerification: dv}}
				f.Zones = append(f.Zones, z)
				f.Policies[id] = map[string]any{}
				ok(w, map[string]any{"ZoneId": id, "OwnershipVerification": map[string]any{"DnsVerification": dv}})
				return true
			}
		}
		fail(w, "InvalidParameter.PlanNotFound", "套餐不存在。")
	case "teo VerifyOwnership":
		for i, z := range f.Zones {
			if z.ZoneName != str("Domain") {
				continue
			}
			dv := z.Verification()
			if dv == nil {
				ok(w, map[string]any{"Status": "success"})
				return true
			}
			for _, r := range f.Records[z.ZoneName] {
				if r.Name == dv.Subdomain && r.Type == "TXT" && r.Value == dv.RecordValue {
					f.Zones[i].CnameStatus, f.Zones[i].Status = "finished", "active"
					ok(w, map[string]any{"Status": "success"})
					return true
				}
			}
			ok(w, map[string]any{"Status": "fail", "Result": "没有找到验证记录"})
			return true
		}
		fail(w, "ResourceNotFound", "站点不存在。")
	case "teo ModifyZoneStatus":
		z := zone(str("ZoneId"))
		if z == nil {
			fail(w, "ResourceNotFound", "站点不存在。")
			return true
		}
		z.Paused, _ = in["Paused"].(bool)
		ok(w, nil)
	case "teo DeleteZone":
		for i, z := range f.Zones {
			if z.ZoneID == str("ZoneId") {
				if !z.Paused {
					fail(w, "OperationDenied.DisableZoneNotCompleted", "请先停用站点。")
					return true
				}
				f.Zones = append(f.Zones[:i:i], f.Zones[i+1:]...)
				for j := range f.Plans {
					if f.Plans[j].PlanID == "edgeone-free2" {
						f.Plans[j].Bindable = "true"
					}
				}
				ok(w, nil)
				return true
			}
		}
		fail(w, "ResourceNotFound", "站点不存在。")
	default:
		return false
	}
	return true
}
