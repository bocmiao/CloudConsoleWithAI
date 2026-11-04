package tencenttest

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// serveCDN handles CDN domains and the SSL certificate service: the
// certificates are kept, so an applied one shows as being validated once
// and then issued.
func (f *Fake) serveCDN(w http.ResponseWriter, service, action string, in map[string]any) bool {
	str := func(k string) string { s, _ := in[k].(string); return s }
	strs := func(k string) []string {
		var out []string
		list, _ := in[k].([]any)
		for _, v := range list {
			s, _ := v.(string)
			out = append(out, s)
		}
		return out
	}
	domain := func(name string) *tencent.CDNDomain {
		for _, d := range f.CDN {
			if d.Domain == strings.ToLower(name) {
				return d
			}
		}
		return nil
	}
	// online checks that every address is on an accelerated domain that
	// is serving.
	online := func(list []string) bool {
		if len(list) == 0 {
			fail(w, "InvalidParameter.CdnParamError", "参数错误，请参考文档中示例参数填充。")
			return false
		}
		for _, raw := range list {
			u, err := url.Parse(raw)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
				fail(w, "InvalidParameter.CdnUrlInvalidParam", "URL 格式错误，需要包含协议头部 http:// 或 https://")
				return false
			}
			d := domain(u.Hostname())
			switch {
			case d == nil:
				fail(w, "ResourceNotFound.CdnHostNotExists", "未查询到该域名，请确认域名是否正确。")
				return false
			case d.Status != "online":
				fail(w, "ResourceUnavailable.CdnHostIsNotOnline", "域名未启动加速服务。")
				return false
			}
		}
		return true
	}
	task := func(kind string, list []string) {
		f.nextID++
		for _, x := range list {
			f.CDNTasks = append(f.CDNTasks, kind+" "+x)
		}
		ok(w, map[string]any{"TaskId": fmt.Sprintf("task-%d", f.nextID)})
	}
	switch service + " " + action {
	case "cdn DescribeDomainsConfig":
		offset, limit := num(in["Offset"]), num(in["Limit"])
		if limit <= 0 || limit > 100 {
			limit = 100
		}
		list := []map[string]any{}
		for i, d := range f.CDN {
			if i < offset || i >= offset+limit {
				continue
			}
			https := map[string]any{"Switch": "off", "Http2": "off", "SslStatus": "closed"}
			if d.HTTPS {
				https = map[string]any{"Switch": "on", "Http2": "on", "SslStatus": "deployed",
					"CertInfo": map[string]any{"CertId": d.CertID, "ExpireTime": d.CertExpires, "From": "cloud"}}
			}
			list = append(list, map[string]any{"ResourceId": "cdn-" + d.Domain, "Domain": d.Domain, "Cname": d.Cname, "Status": d.Status,
				"ServiceType": d.ServiceType, "Area": d.Area, "Disable": "normal", "Readonly": "normal", "Product": "cdn",
				"Origin": map[string]any{"Origins": d.Origins, "OriginType": d.OriginType, "OriginPullProtocol": "http"},
				"Https":  https, "ForceRedirect": map[string]any{"Switch": "off", "RedirectType": "http", "RedirectStatusCode": 302}})
		}
		ok(w, map[string]any{"Domains": list, "TotalNumber": len(f.CDN)})
	case "cdn PurgeUrlsCache":
		if urls := strs("Urls"); online(urls) {
			task("purge-url", urls)
		}
	case "cdn PurgePathCache":
		paths := strs("Paths")
		if t := str("FlushType"); t != "flush" && t != "delete" {
			fail(w, "InvalidParameter.CdnParamError", "FlushType 取值错误。")
			return true
		}
		for _, p := range paths {
			if !strings.HasSuffix(p, "/") {
				fail(w, "InvalidParameter.CdnUrlInvalidParam", "目录需要以 / 结尾。")
				return true
			}
		}
		if online(paths) {
			task("purge-dir", paths)
		}
	case "cdn PushUrlsCache":
		if urls := strs("Urls"); online(urls) {
			task("push", urls)
		}
	case "cdn StartCdnDomain", "cdn StopCdnDomain":
		d := domain(str("Domain"))
		if d == nil {
			fail(w, "ResourceNotFound.CdnHostNotExists", "未查询到该域名，请确认域名是否正确。")
			return true
		}
		from, to := "offline", "online"
		if action == "StopCdnDomain" {
			from, to = "online", "offline"
		}
		if d.Status != from {
			fail(w, "InvalidParameter.CDNStatusInvalidDomain", "域名状态不合法。")
			return true
		}
		d.Status = to
		ok(w, nil)
	case "cdn UpdateDomainConfig":
		d := domain(str("Domain"))
		if d == nil {
			fail(w, "ResourceNotFound.CdnHostNotExists", "未查询到该域名，请确认域名是否正确。")
			return true
		}
		for k := range in {
			if k != "Domain" && k != "Https" {
				fail(w, "InvalidParameter", "the fake only changes Https, not "+k)
				return true
			}
		}
		https, _ := in["Https"].(map[string]any)
		if https["Switch"] != "on" {
			d.HTTPS, d.CertID, d.CertExpires = false, "", ""
			ok(w, nil)
			return true
		}
		info, _ := https["CertInfo"].(map[string]any)
		id, _ := info["CertId"].(string)
		c := f.sslCert(id)
		if c == nil || c.Status != 1 || !certCovers(c, d.Domain) {
			fail(w, "InvalidParameter.CdnCertInfoNotFound", "证书信息不存在或不匹配。")
			return true
		}
		d.HTTPS, d.CertID, d.CertExpires = true, c.ID, c.EndTime
		ok(w, nil)

	case "monitor DescribeAlarmHistories":
		if str("Module") != "monitor" {
			fail(w, "InvalidParameter", "Module 取值错误。")
			return true
		}
		start, end := int64(num(in["StartTime"])), int64(num(in["EndTime"]))
		var match []map[string]any
		for _, a := range f.Alarms {
			first, _ := time.Parse(time.RFC3339, a.First)
			last, _ := time.Parse(time.RFC3339, a.Last)
			if first.Unix() < start || first.Unix() > end {
				continue
			}
			match = append(match, map[string]any{"AlarmId": a.ID, "MonitorType": "MT_QCE", "Namespace": a.Namespace, "AlarmObject": a.Object,
				"Content": a.Content, "FirstOccurTime": first.Unix(), "LastOccurTime": last.Unix(), "AlarmStatus": a.Status, "PolicyName": a.Policy,
				"AlarmLevel": a.Level, "Region": a.Region, "MetricName": a.Metric, "PolicyExists": 1, "NoticeWays": []string{"EMAIL"}})
		}
		page, size := max(num(in["PageNumber"]), 1), num(in["PageSize"])
		if size <= 0 || size > 100 {
			fail(w, "InvalidParameter", "PageSize 取值 1~100。")
			return true
		}
		from, to := min((page-1)*size, len(match)), min(page*size, len(match))
		ok(w, map[string]any{"TotalCount": len(match), "Histories": match[from:to]})
	case "ssl DescribeCertificates":
		list := []tencent.SSLCert{}
		for _, c := range f.SSLCerts {
			list = append(list, *c)
		}
		ok(w, map[string]any{"TotalCount": len(list), "Certificates": list})
	case "ssl ApplyCertificate":
		name := strings.ToLower(str("DomainName"))
		switch {
		case str("DvAuthMethod") != "DNS_AUTO" && str("DvAuthMethod") != "DNS" && str("DvAuthMethod") != "FILE":
			fail(w, "InvalidParameter", "DvAuthMethod 取值错误。")
			return true
		case name == "" || strings.HasPrefix(name, "*."):
			fail(w, "FailedOperation.InvalidParam", "免费证书不支持泛域名。")
			return true
		case str("DvAuthMethod") == "DNS_AUTO" && f.dnspodZone(name) == "":
			fail(w, "FailedOperation.InvalidParam", "域名未托管在腾讯云 DNS 解析，不能自动添加验证记录。")
			return true
		case f.FreeCertsLeft == 0:
			fail(w, "FailedOperation.ExceedsFreeLimit", "免费证书申请数量已达上限。")
			return true
		}
		if f.FreeCertsLeft > 0 {
			f.FreeCertsLeft--
		}
		f.nextID++
		c := &tencent.SSLCert{ID: fmt.Sprintf("ssl-new%d", f.nextID), Domain: name, SANs: []string{name}, Alias: str("Alias"), From: "trustasia",
			Product: "TrustAsia C1 DV Free", Status: 0, StatusName: "审核中", IsDV: true, HostingStatus: intPtr(-1)}
		f.SSLCerts = append(f.SSLCerts, c)
		f.SSLApplied = append(f.SSLApplied, name+" "+str("DvAuthMethod"))
		ok(w, map[string]any{"CertificateId": c.ID})
	case "ssl DescribeCertificate":
		c := f.sslCert(str("CertificateId"))
		if c == nil {
			fail(w, "FailedOperation.CertificateNotFound", "证书不存在。")
			return true
		}
		out := map[string]any{"CertificateId": c.ID, "Domain": c.Domain, "SubjectAltName": c.SANs, "From": c.From, "Alias": c.Alias,
			"Status": c.Status, "StatusName": c.StatusName, "VerifyType": "DNS_AUTO", "CertBeginTime": c.BeginTime, "CertEndTime": c.EndTime}
		ok(w, out)
		// Validated by the next look.
		if c.Status == 0 {
			cst := time.FixedZone("CST", 8*3600)
			c.Status, c.StatusName = 1, "已通过"
			c.BeginTime = time.Now().In(cst).Format("2006-01-02 15:04:05")
			c.EndTime = time.Now().Add(90 * 24 * time.Hour).In(cst).Format("2006-01-02 15:04:05")
		}
	default:
		return false
	}
	return true
}

func (f *Fake) sslCert(id string) *tencent.SSLCert {
	for _, c := range f.SSLCerts {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// dnspodZone is the DNSPod domain a name belongs to, if any.
func (f *Fake) dnspodZone(name string) string {
	for zone := range f.Records {
		if name == zone || strings.HasSuffix(name, "."+zone) {
			return zone
		}
	}
	return ""
}

func certCovers(c *tencent.SSLCert, host string) bool {
	for _, n := range append([]string{c.Domain}, c.SANs...) {
		if n == host || (strings.HasPrefix(n, "*.") && strings.HasSuffix(host, n[1:]) && !strings.Contains(strings.TrimSuffix(host, n[1:]), ".")) {
			return true
		}
	}
	return false
}

func num(v any) int {
	n, _ := v.(float64)
	return int(n)
}

func intPtr(n int) *int { return &n }
