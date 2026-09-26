package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// cloudPoll is how often cloud operations check on progress; tests shorten
// it through Env.PollInterval.
var cloudPoll = 10 * time.Second

// waitFor calls check until it reports done, at most tries times.
func waitFor(ctx context.Context, env *Env, tries int, check func() (bool, error)) (bool, error) {
	every := cloudPoll
	if env.PollInterval > 0 {
		every = env.PollInterval
	}
	var lastErr error
	for i := 0; i < tries; i++ {
		ok, err := check()
		if ok {
			return true, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(every):
		}
	}
	return false, lastErr
}

// traceCloud records every Tencent Cloud call an operation makes.
func traceCloud(env *Env, run func() Outcome) Outcome {
	var cmds []string
	env.Cloud.Trace = func(service, action string, body []byte) {
		cmds = append(cmds, service+" "+action+" "+string(body))
	}
	defer func() { env.Cloud.Trace = nil }()
	out := run()
	out.Commands = append([]string{"# 调用腾讯云 API 3.0（请求带 TC3 签名，密钥不记录）"}, cmds...)
	return out
}

func applyCloud(ctx context.Context, env *Env, r Resolved, progress Progress) Outcome {
	out := &Outcome{Undo: map[string]string{}}
	if env.Cloud == nil {
		out.Status = StatusRefused
		out.logf("还没有配置腾讯云密钥（设置 → 腾讯云）")
		return *out
	}
	report := func(format string, args ...any) {
		out.logf(format, args...)
		if progress != nil {
			progress(out.Log)
		}
	}
	return traceCloud(env, func() Outcome {
		if strings.HasPrefix(r.Impl.Cloud, "cos_") {
			return applyCOS(ctx, env, r.Impl.Cloud, r.Values, out, report)
		}
		switch r.Impl.Cloud {
		case "dns_record_set":
			return applyDNSRecord(ctx, env, r.Values, out, report)
		case "dns_record_add":
			return applyRecordAdd(ctx, env, r.Values, out, report)
		case "dns_record_modify":
			return applyRecordModify(ctx, env, r.Values, out, report)
		case "dns_record_delete":
			return applyRecordDelete(ctx, env, r.Values, out, report)
		case "dns_record_status":
			return applyRecordStatus(ctx, env, r.Values, out, report)
		case "eo_domain_add":
			return applyEODomain(ctx, env, r.Values, out, report)
		case "eo_https":
			return applyEOHTTPS(ctx, env, r.Values, out, report)
		case "firewall_open":
			return applyFirewallOpen(ctx, env, r.Values, out, report)
		case "firewall_close":
			return applyFirewallClose(ctx, env, r.Values, out, report)
		case "snapshot":
			return applySnapshot(ctx, env, r.Values, out, report)
		case "power_start", "power_stop", "power_reboot":
			return applyPower(ctx, env, strings.TrimPrefix(r.Impl.Cloud, "power_"), r.Values, out, report)
		case "eo_purge":
			return applyPurge(ctx, env, r.Values, out, report)
		case "eo_prefetch":
			return applyPrefetch(ctx, env, r.Values, out, report)
		case "eo_status":
			return applyDomainStatus(ctx, env, r.Values, out, report)
		case "eo_origin":
			return applyOrigin(ctx, env, r.Values, out, report)
		case "eo_zone_create":
			return applyZoneCreate(ctx, env, r.Values, out, report)
		case "eo_ip_block", "eo_ip_unblock":
			return applyIPBlock(ctx, env, r.Impl.Cloud == "eo_ip_block", r.Values, out, report)
		case "eo_ratelimit":
			return applyRateLimit(ctx, env, r.Values, out, report)
		case "eo_ratelimit_remove":
			return applyRateLimitRemove(ctx, env, r.Values, out, report)
		case "eo_cc":
			return applyCC(ctx, env, r.Values, out, report)
		case "eo_clientip":
			return applyClientIP(ctx, env, r.Values, out, report)
		}
		out.Status = StatusFailed
		out.logf("未知的云操作 %s", r.Impl.Cloud)
		return *out
	})
}

func undoCloud(ctx context.Context, env *Env, r Resolved, undo map[string]string) Outcome {
	out := Outcome{}
	if len(undo) == 0 {
		out.Status = StatusUndone
		out.logf("这一步当时没有做任何修改，不需要撤销")
		return out
	}
	if env.Cloud == nil {
		out.Status = StatusRefused
		out.logf("还没有配置腾讯云密钥（设置 → 腾讯云）")
		return out
	}
	return traceCloud(env, func() Outcome {
		var err error
		switch r.Impl.Cloud {
		case "cos_create", "cos_acl", "cos_referer", "cos_cors", "cos_lifecycle", "cos_versioning", "cos_encryption", "cos_website", "cos_policy":
			err = undoCOS(ctx, env, r.Impl.Cloud, undo)
		case "dns_record_set", "dns_record_add", "dns_record_modify", "dns_record_delete":
			err = undoDNSRecord(ctx, env.Cloud, undo)
		case "dns_record_status":
			err = undoRecordStatus(ctx, env.Cloud, undo)
		case "eo_domain_add":
			err = undoEODomain(ctx, env, undo)
		case "firewall_open", "firewall_close":
			err = undoFirewall(ctx, env.Cloud, undo)
		case "power_start", "power_stop":
			op := "StopInstances"
			if r.Impl.Cloud == "power_stop" {
				op = "StartInstances"
			}
			err = env.Cloud.Power(ctx, undo["region"], undo["instance"], op)
		case "eo_status":
			err = env.Cloud.SetAccelerationDomainStatus(ctx, undo["zone_id"], []string{undo["domain"]}, undo["status"])
		case "eo_origin":
			err = undoOrigin(ctx, env.Cloud, undo)
		case "eo_zone_create":
			err = undoZoneCreate(ctx, env, undo)
		case "eo_ip_block", "eo_ip_unblock":
			err = undoIPBlock(ctx, env.Cloud, undo)
		case "eo_ratelimit", "eo_ratelimit_remove":
			err = undoRateLimit(ctx, env.Cloud, undo)
		case "eo_cc":
			err = undoCC(ctx, env.Cloud, undo)
		case "eo_clientip":
			err = env.Cloud.SetClientIPHeader(ctx, undo["zone_id"], undo["switch"] == "on", undo["header"])
		case "eo_https":
			var ids []string
			if undo["cert_ids"] != "" {
				ids = strings.Split(undo["cert_ids"], ",")
			}
			err = env.Cloud.SetCertificate(ctx, undo["zone_id"], undo["domain"], undo["mode"], ids)
		default:
			err = fmt.Errorf("未知的云操作 %s", r.Impl.Cloud)
		}
		if err != nil {
			out.Status = StatusFailed
			out.logf("撤销失败：%v", err)
			return out
		}
		out.Status = StatusUndone
		out.logf("已撤销：恢复了修改前的设置")
		return out
	})
}

// ---- DNSPod ----

var hostnameRe = regexp.MustCompile(`^([A-Za-z0-9_]([A-Za-z0-9_-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,63}\.?$`)

func checkRecordValue(typ, value string) error {
	ip := net.ParseIP(value)
	switch typ {
	case "A":
		if ip == nil || ip.To4() == nil {
			return fmt.Errorf("A 记录的值要是 IPv4 地址，%q 不是", value)
		}
	case "AAAA":
		if ip == nil || ip.To4() != nil {
			return fmt.Errorf("AAAA 记录的值要是 IPv6 地址，%q 不是", value)
		}
	case "CNAME":
		if !hostnameRe.MatchString(value) {
			return fmt.Errorf("CNAME 记录的值要是域名，%q 不是", value)
		}
	}
	return nil
}

func sameValue(a, b string) bool {
	return strings.EqualFold(strings.TrimSuffix(a, "."), strings.TrimSuffix(b, "."))
}

func fullName(sub, domain string) string {
	if sub == "@" {
		return domain
	}
	return sub + "." + domain
}

func recordText(r tencent.Record) string { return r.Type + " " + r.Value }

// applyDNSRecord makes one host resolve to one value on the default line:
// it changes a conflicting record in place (A → CNAME keeps the name
// resolving throughout), removes the rest, or adds a record.
func applyDNSRecord(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	domain, sub := strings.ToLower(v["domain"]), v["subdomain"]
	name := fullName(sub, domain)
	typ, value := strings.ToUpper(v["type"]), v["value"]
	if v["point_to"] == "eo" {
		zones, err := c.Zones(ctx)
		if err != nil {
			out.Status = StatusRefused
			out.logf("查询 EdgeOne 站点失败：%v", err)
			return *out
		}
		z, ok := tencent.ZoneFor(zones, name)
		if !ok {
			out.Status = StatusRefused
			out.logf("EdgeOne 里没有 %s 所在的站点", name)
			return *out
		}
		d, ok, err := c.AccelerationDomain(ctx, z.ZoneID, name)
		switch {
		case err != nil:
			out.Status = StatusRefused
			out.logf("查询 EdgeOne 加速域名失败：%v", err)
			return *out
		case !ok:
			out.Status = StatusRefused
			out.logf("EdgeOne 里还没有加速域名 %s，需要先添加", name)
			return *out
		case d.Cname == "":
			out.Status = StatusRefused
			out.logf("EdgeOne 还没有给 %s 分配 CNAME（状态 %s）", name, d.DomainStatus)
			return *out
		}
		typ, value = "CNAME", d.Cname
	}
	if err := checkRecordValue(typ, value); err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	ttl, _ := strconv.Atoi(v["ttl"])

	all, err := c.Records(ctx, domain, sub)
	if err != nil {
		out.Status = StatusRefused
		out.logf("读取 %s 的解析记录失败：%v", domain, err)
		return *out
	}
	var conflicts []tencent.Record
	for _, r := range all {
		if r.Name != sub || r.Line != tencent.DefaultLine {
			continue
		}
		if r.Type == typ && sameValue(r.Value, value) {
			report("%s 已经解析到 %s %s，不需要修改", name, typ, value)
			out.Status = StatusDone
			out.Undo = map[string]string{}
			return *out
		}
		switch {
		case typ == "TXT":
		case typ == "CNAME" && (r.Type == "A" || r.Type == "AAAA" || r.Type == "CNAME"):
			conflicts = append(conflicts, r)
		case (typ == "A" || typ == "AAAA") && (r.Type == typ || r.Type == "CNAME"):
			conflicts = append(conflicts, r)
		}
	}
	same := 0
	for _, r := range conflicts {
		if r.Type == typ {
			same++
		}
	}
	if typ != "CNAME" && same > 1 {
		out.Status = StatusRefused
		out.logf("%s 有 %d 条 %s 记录（可能在做负载均衡），为了安全不自动修改", name, same, typ)
		return *out
	}

	want := tencent.Record{Name: sub, Type: typ, Value: value, Line: tencent.DefaultLine, TTL: uint64(ttl)}
	out.Undo["domain"] = domain
	undoRestore := func(why string) Outcome {
		report("%s，正在恢复原来的解析", why)
		if err := undoDNSRecord(ctx, c, out.Undo); err != nil {
			out.Status = StatusFailed
			out.logf("恢复也失败了：%v，请在 DNSPod 控制台检查 %s 的解析", err, name)
			return *out
		}
		out.Status = StatusRolledBack
		return *out
	}
	if len(conflicts) == 0 {
		if want.TTL == 0 {
			want.TTL = 600
		}
		report("正在给 %s 添加 %s 记录：%s", name, typ, value)
		id, err := c.CreateRecord(ctx, domain, want)
		if err != nil {
			out.Status = StatusRefused
			out.logf("添加失败：%v", err)
			return *out
		}
		out.Undo["created"] = strconv.FormatUint(id, 10)
	} else {
		// Remove the extra records first, so the one changed in place does
		// not conflict with them; the name keeps resolving throughout.
		keep := conflicts[len(conflicts)-1]
		var deleted []tencent.Record
		for _, r := range conflicts[:len(conflicts)-1] {
			report("删除冲突的记录 %s", recordText(r))
			if err := c.DeleteRecord(ctx, domain, r.RecordID); err != nil {
				return undoRestore(fmt.Sprintf("删除 %s 失败：%v", recordText(r), err))
			}
			deleted = append(deleted, r)
			data, _ := json.Marshal(deleted)
			out.Undo["deleted"] = string(data)
		}
		want.RecordID = keep.RecordID
		if want.TTL == 0 {
			want.TTL = keep.TTL
		}
		report("正在把 %s 的 %s 改为 %s %s", name, recordText(keep), typ, value)
		if err := c.ModifyRecord(ctx, domain, want); err != nil {
			if len(deleted) == 0 {
				out.Status = StatusRefused
				out.Undo = map[string]string{}
				out.logf("修改失败：%v", err)
				return *out
			}
			return undoRestore(fmt.Sprintf("修改失败：%v", err))
		}
		orig, _ := json.Marshal(keep)
		out.Undo["modified"] = string(orig)
	}
	report("完成：%s 已解析到 %s %s，按 TTL 几分钟内在各地生效", name, typ, value)
	out.Status = StatusDone
	return *out
}

func undoDNSRecord(ctx context.Context, c *tencent.Client, undo map[string]string) error {
	domain := undo["domain"]
	var errs []error
	if id, err := strconv.ParseUint(undo["created"], 10, 64); err == nil && id > 0 {
		// Already gone is as good as deleted.
		if err := c.DeleteRecord(ctx, domain, id); err != nil && !tencent.IsCode(err, "InvalidParameter.RecordIdInvalid") {
			errs = append(errs, fmt.Errorf("删除新增的记录失败：%w", err))
		}
	}
	if undo["modified"] != "" {
		var r tencent.Record
		if err := json.Unmarshal([]byte(undo["modified"]), &r); err == nil {
			if err := c.ModifyRecord(ctx, domain, r); err != nil {
				errs = append(errs, fmt.Errorf("改回 %s 失败：%w", recordText(r), err))
			}
		}
	}
	if undo["deleted"] != "" {
		var rs []tencent.Record
		if err := json.Unmarshal([]byte(undo["deleted"]), &rs); err == nil {
			for _, r := range rs {
				if _, err := c.CreateRecord(ctx, domain, r); err != nil {
					errs = append(errs, fmt.Errorf("加回 %s 失败：%w", recordText(r), err))
				}
			}
		}
	}
	return errors.Join(errs...)
}

// ---- EdgeOne ----

func findZone(ctx context.Context, c *tencent.Client, name string) (tencent.Zone, error) {
	zones, err := c.Zones(ctx)
	if err != nil {
		return tencent.Zone{}, fmt.Errorf("查询 EdgeOne 站点失败：%w", err)
	}
	z, ok := tencent.ZoneFor(zones, name)
	if !ok {
		return z, fmt.Errorf("EdgeOne 里没有 %s 所在的站点。可以先用 eo.zone.create 新建站点（账号里要有能绑定站点的套餐；购买套餐涉及计费，需要你在控制台操作）", name)
	}
	return z, nil
}

func applyEODomain(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	name := strings.ToLower(v["domain"])
	z, err := findZone(ctx, c, name)
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	switch {
	case z.Status == "initializing":
		out.Status = StatusRefused
		out.logf("EdgeOne 站点 %s 还没有绑定套餐，请先在控制台绑定", z.ZoneName)
		return *out
	case z.Paused:
		out.Status = StatusRefused
		out.logf("EdgeOne 站点 %s 已停用", z.ZoneName)
		return *out
	}
	if d, ok, err := c.AccelerationDomain(ctx, z.ZoneID, name); err == nil && ok {
		report("EdgeOne 里已经有 %s 了（状态 %s，回源 %s，CNAME %s），不需要再添加", name, d.DomainStatus, d.OriginDetail.Origin, d.Cname)
		out.Status = StatusDone
		out.Undo = map[string]string{}
		return *out
	}
	httpPort, _ := strconv.Atoi(v["http_port"])
	httpsPort, _ := strconv.Atoi(v["https_port"])
	report("正在给 EdgeOne 站点 %s 添加加速域名 %s，回源到 %s（%s）", z.ZoneName, name, v["origin"], v["origin_protocol"])
	verify, err := c.CreateAccelerationDomain(ctx, tencent.NewDomain{
		ZoneID: z.ZoneID, Name: name, Origin: v["origin"], Protocol: v["origin_protocol"], HTTPPort: httpPort, HTTPSPort: httpsPort,
	})
	if err != nil {
		out.Status = StatusRefused
		out.logf("添加失败：%v", err)
		return *out
	}
	out.Undo["zone_id"], out.Undo["domain"] = z.ZoneID, name
	if verify != nil {
		report("EdgeOne 要求先验证域名归属：添加 %s 记录，主机记录 %s，值 %s（可以让 AI 用 dns.record.set 添加）",
			verify.RecordType, verify.Subdomain, verify.RecordValue)
	}
	var cname string
	_, _ = waitFor(ctx, env, 6, func() (bool, error) {
		d, ok, err := c.AccelerationDomain(ctx, z.ZoneID, name)
		cname = d.Cname
		return ok && d.Cname != "", err
	})
	if cname == "" {
		report("完成：已添加 %s，EdgeOne 还在分配 CNAME", name)
	} else {
		report("完成：已添加 %s。EdgeOne 分配的 CNAME 是 %s，DNS 解析到它之后才会生效", name, cname)
	}
	out.Status = StatusDone
	return *out
}

func undoEODomain(ctx context.Context, env *Env, undo map[string]string) error {
	c, zone, name := env.Cloud, undo["zone_id"], undo["domain"]
	if err := c.SetAccelerationDomainStatus(ctx, zone, []string{name}, "offline"); err != nil {
		return fmt.Errorf("停用 %s 失败：%w", name, err)
	}
	var err error
	ok, _ := waitFor(ctx, env, 12, func() (bool, error) {
		err = c.DeleteAccelerationDomains(ctx, zone, []string{name})
		return err == nil, err
	})
	if !ok {
		return fmt.Errorf("删除 %s 失败：%w", name, err)
	}
	return nil
}

func applyEOHTTPS(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	name, mode := strings.ToLower(v["domain"]), v["mode"]
	z, err := findZone(ctx, c, name)
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	d, ok, err := c.AccelerationDomain(ctx, z.ZoneID, name)
	if err != nil || !ok {
		out.Status = StatusRefused
		out.logf("EdgeOne 里没有加速域名 %s（%v）", name, err)
		return *out
	}
	deployed := func(d tencent.AccelerationDomain) bool {
		for _, cert := range d.Certificate.List {
			if cert.Status == "deployed" {
				return true
			}
		}
		return false
	}
	if d.Certificate.Mode == mode && (mode == "disable" || deployed(d)) {
		report("%s 的证书设置已经是 %s，不需要修改", name, mode)
		out.Status = StatusDone
		out.Undo = map[string]string{}
		return *out
	}
	if mode == "eofreecert" && z.Type == "partial" {
		// A free certificate is validated through the domain's CNAME.
		report("正在确认 %s 已经解析到 EdgeOne（免费证书要通过它验证）", name)
		var status string
		active, _ := waitFor(ctx, env, 18, func() (bool, error) {
			s, err := c.CnameStatus(ctx, z.ZoneID, name)
			status = s
			return s == "active", err
		})
		if !active {
			out.Status = StatusRefused
			out.logf("%s 还没有解析到 EdgeOne（CNAME 状态：%s），免费证书申请不下来。解析生效后再执行这一步", name, status)
			return *out
		}
	}
	var ids []string
	for _, cert := range d.Certificate.List {
		if cert.CertID != "" {
			ids = append(ids, cert.CertID)
		}
	}
	prev := d.Certificate.Mode
	if prev == "" {
		prev = "disable"
	}
	out.Undo["zone_id"], out.Undo["domain"], out.Undo["mode"], out.Undo["cert_ids"] = z.ZoneID, name, prev, strings.Join(ids, ",")
	if mode == "disable" {
		report("正在关闭 %s 的 HTTPS 证书", name)
	} else {
		report("正在为 %s 申请并部署 EdgeOne 免费证书（到期自动续签）", name)
	}
	if err := c.SetCertificate(ctx, z.ZoneID, name, mode, nil); err != nil {
		out.Status = StatusRefused
		out.Undo = map[string]string{}
		out.logf("设置失败：%v", err)
		return *out
	}
	if mode == "disable" {
		report("完成：已关闭 %s 的 HTTPS 证书", name)
		out.Status = StatusDone
		return *out
	}
	var state string
	_, _ = waitFor(ctx, env, 30, func() (bool, error) {
		d, _, err := c.AccelerationDomain(ctx, z.ZoneID, name)
		state = ""
		for _, cert := range d.Certificate.List {
			state = cert.Status
		}
		return deployed(d) || state == "failed", err
	})
	switch state {
	case "deployed":
		report("完成：证书已部署，现在可以用 https://%s 访问", name)
	case "failed":
		report("免费证书申请失败，正在恢复原来的证书设置")
		ids := []string(nil)
		if out.Undo["cert_ids"] != "" {
			ids = strings.Split(out.Undo["cert_ids"], ",")
		}
		if err := c.SetCertificate(ctx, z.ZoneID, name, prev, ids); err != nil {
			out.Status = StatusFailed
			out.logf("恢复也失败了：%v", err)
			return *out
		}
		out.Status = StatusRolledBack
		out.logf("可以在 EdgeOne 控制台的「证书」里查看申请失败的原因")
		return *out
	default:
		report("完成：已提交免费证书申请（当前状态：%s），一般几分钟内签发，可以稍后让 AI 查一下", state)
	}
	out.Status = StatusDone
	return *out
}
