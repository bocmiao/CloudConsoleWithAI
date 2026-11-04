package aliyuntest

import (
	"strconv"
	"strings"
)

// dnsLines are code, name and the name shown (paid editions' regions
// show their parent too).
var dnsLines = [][3]string{{"default", "默认", "默认"}, {"telecom", "电信", "电信"}, {"unicom", "联通", "联通"}, {"mobile", "移动", "移动"},
	{"edu", "教育网", "教育网"}, {"oversea", "境外", "境外"}, {"baidu", "百度", "百度"}, {"biying", "必应", "必应"}, {"google", "谷歌", "谷歌"},
	{"cn_region_xibei", "西北", "中国地区_西北"}}

var recordTypes = map[string]bool{"A": true, "AAAA": true, "CNAME": true, "MX": true, "TXT": true, "NS": true, "SRV": true,
	"CAA": true, "REDIRECT_URL": true, "FORWARD_URL": true}

func (f *Cloud) domain(name string) *DNSDomain {
	for _, d := range f.Domains {
		if d.Name == name {
			return d
		}
	}
	return nil
}

// findRecord finds a record and its domain by ID.
func (f *Cloud) findRecord(id string) (*DNSRecord, *DNSDomain) {
	for _, d := range f.Domains {
		for _, r := range f.Records[d.Name] {
			if r.ID == id {
				return r, d
			}
		}
	}
	return nil, nil
}

// recordFrom reads a record from AddDomainRecord or UpdateDomainRecord,
// checking it the way Alidns does.
func recordFrom(q map[string]string, d *DNSDomain) (DNSRecord, *apiErr) {
	r := DNSRecord{RR: q["RR"], Type: q["Type"], Value: q["Value"], Line: q["Line"], TTL: 600, Status: "ENABLE"}
	if r.Line == "" {
		r.Line = "default"
	}
	if !recordTypes[r.Type] {
		return r, refuse(400, "InvalidRecordType", "The specified record type %q is not valid.", r.Type)
	}
	known := false
	for _, l := range dnsLines {
		known = known || l[0] == r.Line
	}
	if !known {
		return r, refuse(400, "InvalidLine", "The specified line %q is not valid.", r.Line)
	}
	if s := q["TTL"]; s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < d.MinTTL || n > 86400 {
			return r, refuse(400, "InvalidTTL", "The TTL must be between %d and 86400 for this edition.", d.MinTTL)
		}
		r.TTL = n
	}
	if r.Type == "MX" {
		n, err := strconv.Atoi(q["Priority"])
		if err != nil || n < 1 || n > 50 {
			return r, refuse(400, "InvalidPriority", "An MX record needs a Priority from 1 to 50.")
		}
		r.Priority = n
	}
	return r, nil
}

// clash applies Alidns's rules: no two identical records, and a CNAME
// shares its name and line with nothing but other lines' records.
func (f *Cloud) clash(domain string, r DNSRecord, except string) *apiErr {
	for _, o := range f.Records[domain] {
		if o.ID == except || o.RR != r.RR || o.Line != r.Line {
			continue
		}
		if o.Type == r.Type && o.Value == r.Value {
			return refuse(400, "DomainRecordDuplicate", "The DNS record already exists.")
		}
		if (o.Type == "CNAME") != (r.Type == "CNAME") || (o.Type == "CNAME" && r.Type == "CNAME") {
			return refuse(400, "DomainRecordConflict", "The DNS record conflicts with other records.")
		}
	}
	return nil
}

func record(r *DNSRecord, domain string, status string) map[string]any {
	m := map[string]any{"RecordId": r.ID, "RR": r.RR, "Type": r.Type, "Value": r.Value, "Line": r.Line, "TTL": r.TTL,
		"Status": status, "Locked": false, "DomainName": domain}
	if r.Type == "MX" {
		m["Priority"] = r.Priority
	}
	if r.Remark != "" {
		m["Remark"] = r.Remark
	}
	return m
}

func (f *Cloud) serveDNS(action string, q map[string]string) (map[string]any, *apiErr) {
	switch action {
	case "DescribeDomains":
		from, to := f.pageNumber(q, 20, len(f.Domains))
		list := []map[string]any{}
		for _, d := range f.Domains[from:to] {
			list = append(list, map[string]any{"DomainId": d.ID, "DomainName": d.Name, "PunyCode": d.Name, "RecordCount": len(f.Records[d.Name]),
				"VersionCode": d.EditionCode, "VersionName": d.Edition, "AliDomain": false, "Remark": "", "Starmark": false,
				"DnsServers": map[string]any{"DnsServer": []string{"dns1.hichina.com", "dns2.hichina.com"}},
				"CreateTime": "2025-01-02T03:04Z", "CreateTimestamp": 1735787040000, "InstanceId": "", "InstanceEndTime": "", "InstanceExpired": false})
		}
		return map[string]any{"Domains": map[string]any{"Domain": list}, "TotalCount": len(f.Domains), "PageNumber": 1, "PageSize": len(list)}, nil
	case "DescribeDomainInfo":
		d := f.domain(q["DomainName"])
		if d == nil {
			return nil, refuse(400, "InvalidDomainName.NoExist", "The specified domain name does not exist.")
		}
		m := map[string]any{"DomainId": d.ID, "DomainName": d.Name, "PunyCode": d.Name, "VersionCode": d.EditionCode, "VersionName": d.Edition,
			"DnsServers": map[string]any{"DnsServer": []string{"dns1.hichina.com", "dns2.hichina.com"}}, "AliDomain": false}
		if q["NeedDetailAttributes"] == "true" {
			var lines []map[string]any
			for _, l := range dnsLines {
				lines = append(lines, map[string]any{"LineCode": l[0], "LineName": l[1], "LineDisplayName": l[2], "FatherCode": ""})
			}
			m["MinTtl"], m["LineType"], m["RecordLines"] = d.MinTTL, "region_province", map[string]any{"RecordLine": lines}
			m["AvailableTtls"] = map[string]any{"AvailableTtl": []string{strconv.Itoa(d.MinTTL), "1800", "3600", "86400"}}
		}
		return m, nil
	case "DescribeDomainRecords":
		d := f.domain(q["DomainName"])
		if d == nil {
			return nil, refuse(400, "InvalidDomainName.NoExist", "The specified domain name does not exist.")
		}
		recs := f.Records[d.Name]
		from, to := f.pageNumber(q, 20, len(recs))
		list := []map[string]any{}
		for _, r := range recs[from:to] {
			list = append(list, record(r, d.Name, r.Status))
		}
		return map[string]any{"DomainRecords": map[string]any{"Record": list}, "TotalCount": len(recs), "PageNumber": 1, "PageSize": len(list)}, nil
	case "DescribeDomainRecordInfo":
		r, d := f.findRecord(q["RecordId"])
		if r == nil {
			return nil, refuse(400, "DomainRecordNotBelongToUser", "The DNS record does not belong to you.")
		}
		// This one spells the status Enable/Disable.
		return record(r, d.Name, strings.ToUpper(r.Status[:1])+strings.ToLower(r.Status[1:])), nil
	case "AddDomainRecord":
		d := f.domain(q["DomainName"])
		if d == nil {
			return nil, refuse(400, "InvalidDomainName.NoExist", "The specified domain name does not exist.")
		}
		r, err := recordFrom(q, d)
		if err != nil {
			return nil, err
		}
		if err := f.clash(d.Name, r, ""); err != nil {
			return nil, err
		}
		r.ID = f.id("90000")
		f.Records[d.Name] = append(f.Records[d.Name], &r)
		return map[string]any{"RecordId": r.ID}, nil
	case "UpdateDomainRecord":
		cur, d := f.findRecord(q["RecordId"])
		if cur == nil {
			return nil, refuse(400, "DomainRecordNotBelongToUser", "The DNS record does not belong to you.")
		}
		r, err := recordFrom(q, d)
		if err != nil {
			return nil, err
		}
		if r.RR == cur.RR && r.Type == cur.Type && r.Value == cur.Value && r.Line == cur.Line && r.TTL == cur.TTL && r.Priority == cur.Priority {
			return nil, refuse(400, "DomainRecordDuplicate", "The DNS record already exists.")
		}
		if err := f.clash(d.Name, r, cur.ID); err != nil {
			return nil, err
		}
		cur.RR, cur.Type, cur.Value, cur.Line, cur.TTL, cur.Priority = r.RR, r.Type, r.Value, r.Line, r.TTL, r.Priority
		return map[string]any{"RecordId": cur.ID}, nil
	case "DeleteDomainRecord":
		r, d := f.findRecord(q["RecordId"])
		if r == nil {
			return nil, refuse(400, "DomainRecordNotBelongToUser", "The DNS record does not belong to you.")
		}
		var keep []*DNSRecord
		for _, o := range f.Records[d.Name] {
			if o.ID != r.ID {
				keep = append(keep, o)
			}
		}
		f.Records[d.Name] = keep
		return map[string]any{"RecordId": r.ID}, nil
	case "SetDomainRecordStatus":
		r, _ := f.findRecord(q["RecordId"])
		if r == nil {
			return nil, refuse(400, "DomainRecordNotBelongToUser", "The DNS record does not belong to you.")
		}
		switch q["Status"] {
		case "Enable", "Disable":
			r.Status = strings.ToUpper(q["Status"])
		default:
			return nil, refuse(400, "InvalidStatus", "The Status must be Enable or Disable.")
		}
		return map[string]any{"RecordId": r.ID, "Status": q["Status"]}, nil
	case "UpdateDomainRecordRemark":
		r, _ := f.findRecord(q["RecordId"])
		if r == nil {
			return nil, refuse(400, "DomainRecordNotBelongToUser", "The DNS record does not belong to you.")
		}
		r.Remark = q["Remark"]
		return nil, nil
	}
	return nil, refuse(404, "InvalidAction.NotFound", "Specified api is not found.")
}
