package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
)

// 阿里云 云解析 on the 解析 page: its domains next to DNSPod's, their
// records, and the same record changes as checklists. EdgeOne belongs to
// Tencent Cloud, so none of it applies here.

func (a *App) aliDomains(ctx context.Context, c *aliyun.Client, out *DNSDomainsView) {
	ds, err := c.Domains(ctx)
	if err != nil {
		out.AliError = err.Error()
		return
	}
	have := map[string]bool{}
	for _, d := range out.Domains {
		have[strings.ToLower(d.Name)] = true
	}
	for _, d := range ds {
		if have[strings.ToLower(d.Name)] {
			continue // on both: DNSPod's is listed
		}
		out.Domains = append(out.Domains, DNSDomainView{Name: d.Name, Status: "ENABLE", DNSOK: true, Grade: d.Edition,
			Records: uint64(d.RecordCount), Provider: "alidns"})
	}
}

func needAliyun(a *App) (*aliyun.Client, error) {
	c := a.aliyunClient()
	if c == nil {
		return nil, userErr("请先在「设置 → 阿里云」填写 AccessKey")
	}
	return c, nil
}

func aliRecordView(r aliyun.Record, domain string) DNSRecordView {
	v := DNSRecordView{RID: r.ID, Name: r.Name, Full: fullName(r.Name, domain), Type: r.Type, Value: r.Value,
		Line: actions.AliLineName(r.Line), TTL: uint64(r.TTL), Enabled: r.Status != "disabled", Remark: r.Remark}
	if r.Type == "MX" {
		v.MX = uint64(r.Priority)
	}
	if r.Weight > 0 {
		w := uint64(r.Weight)
		v.Weight = &w
	}
	return v
}

func (a *App) aliRecords(ctx context.Context, domain string) (DNSRecordsView, error) {
	c, err := needAliyun(a)
	if err != nil {
		return DNSRecordsView{}, err
	}
	domain = strings.ToLower(strings.TrimSpace(domain))
	recs, err := c.Records(ctx, domain)
	if err != nil {
		return DNSRecordsView{}, err
	}
	out := DNSRecordsView{Domain: domain, Provider: "alidns", Records: []DNSRecordView{}, Pending: []EOPendingView{}}
	for _, r := range recs {
		out.Records = append(out.Records, aliRecordView(r, domain))
	}
	sort.SliceStable(out.Records, func(i, j int) bool {
		a, b := out.Records[i], out.Records[j]
		if (a.Name == "@") != (b.Name == "@") {
			return a.Name == "@"
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Type < b.Type
	})
	return out, nil
}

// aliLines lists the line names a domain's edition offers, or the usual
// ones when it cannot be read.
func (a *App) aliLines(ctx context.Context, domain string) []string {
	out := []string{}
	if c := a.aliyunClient(); c != nil {
		if info, err := c.DomainInfo(ctx, domain); err == nil {
			for _, l := range info.Lines {
				out = append(out, l.Name)
			}
		}
	}
	if len(out) == 0 {
		for _, l := range aliyun.Lines[:6] {
			out = append(out, l.Name)
		}
	}
	return out
}

func (a *App) aliLookup(ctx context.Context, c *aliyun.Client, domain, id string) (aliyun.Record, error) {
	all, err := c.Records(ctx, domain)
	if err != nil {
		return aliyun.Record{}, err
	}
	for _, r := range all {
		if r.ID == id {
			return r, nil
		}
	}
	return aliyun.Record{}, userErr("找不到这条记录，可能已经在别处修改或删除了，请刷新")
}

func aliText(r aliyun.Record) string {
	s := recordText(r.Type, r.Value, r.Priority)
	if r.Line != "" && r.Line != aliyun.DefaultLine {
		s += "（" + actions.AliLineName(r.Line) + "线路）"
	}
	return s
}

// proposeAliDNS turns a change to an Alidns domain into a checklist.
func (a *App) proposeAliDNS(ctx context.Context, req DNSRequest) (PlanView, error) {
	c, err := needAliyun(a)
	if err != nil {
		return PlanView{}, err
	}
	req.Domain = strings.ToLower(strings.TrimSpace(req.Domain))
	req.Sub = strings.ToLower(strings.TrimSpace(req.Sub))
	req.Value = strings.TrimSpace(req.Value)
	if req.Sub == "" {
		req.Sub = "@"
	}
	if req.Domain == "" {
		return PlanView{}, userErr("请选择域名")
	}
	if req.RID == "" && req.ID > 0 {
		req.RID = strconv.FormatUint(req.ID, 10)
	}
	name := fullName(req.Sub, req.Domain)
	var title, reason string
	var step core.Step
	switch req.Op {
	case "add", "modify":
		req.Type = strings.ToUpper(req.Type)
		if err := actions.CheckRecord(req.Type, req.Value); err != nil {
			return PlanView{}, userErr("%v", err)
		}
		if req.Type == "MX" && req.MX <= 0 {
			req.MX = 10
		}
		params := map[string]any{"domain": req.Domain, "subdomain": req.Sub, "type": req.Type, "value": req.Value}
		if req.Line != "" {
			params["line"] = req.Line
		}
		if req.TTL > 0 {
			params["ttl"] = req.TTL
		}
		if req.Type == "MX" {
			params["mx"] = req.MX
		}
		if req.Remark != "" {
			params["remark"] = req.Remark
		}
		now := recordText(req.Type, req.Value, req.MX)
		line := ""
		if l := actions.AliLineCode(req.Line); l != aliyun.DefaultLine {
			line = "（" + actions.AliLineName(l) + "线路）"
		}
		if req.Op == "add" {
			title = "添加解析：" + name
			reason = "给 " + name + " 添加一条 " + now + " 记录" + line + "。执行后按 TTL 几分钟内在各地生效，可以一键撤销（删除这条记录）。"
			step = core.Step{Capability: "aliyun.dns.record.add", Summary: fmt.Sprintf("添加 %s 的 %s 记录%s", name, now, line), Params: params}
			break
		}
		orig, err := a.aliLookup(ctx, c, req.Domain, req.RID)
		if err != nil {
			return PlanView{}, err
		}
		params["record_id"] = orig.ID
		if req.Remark == "" && orig.Remark != "" {
			params["clear_remark"] = "yes" // the page sends the remark it shows
		}
		title = "修改解析：" + name
		reason = fmt.Sprintf("把 %s 的 %s 改为 %s %s。执行后按 TTL 几分钟内在各地生效，可以一键改回。", fullName(orig.Name, req.Domain), aliText(orig), name, now)
		step = core.Step{Capability: "aliyun.dns.record.modify", Summary: fmt.Sprintf("把 %s 的 %s 改为 %s", fullName(orig.Name, req.Domain), aliText(orig), now), Params: params}
	case "delete", "status":
		orig, err := a.aliLookup(ctx, c, req.Domain, req.RID)
		if err != nil {
			return PlanView{}, err
		}
		name = fullName(orig.Name, req.Domain)
		params := map[string]any{"domain": req.Domain, "record_id": orig.ID}
		if req.Op == "delete" {
			title = "删除解析：" + name
			reason = fmt.Sprintf("删除 %s 的 %s。删除后按 TTL 几分钟内在各地失效，可以一键加回来。", name, aliText(orig))
			switch orig.Type {
			case "MX":
				reason += "\n注意：这是收邮件用的记录，删掉后发到这个域名的邮件可能收不到。"
			case "A", "AAAA", "CNAME":
				reason += "\n注意：如果这是网站在用的地址，删掉后这个网址会打不开。"
			}
			step = core.Step{Capability: "aliyun.dns.record.delete", Summary: fmt.Sprintf("删除 %s 的 %s", name, aliText(orig)), Params: params}
			break
		}
		status := strings.ToLower(req.Status)
		if status != "enable" && status != "disable" {
			return PlanView{}, userErr("状态只能是 enable 或 disable")
		}
		params["status"] = status
		verb := map[string]string{"enable": "启用", "disable": "暂停"}[status]
		title = verb + "解析：" + name
		reason = fmt.Sprintf("%s %s 的 %s。", verb, name, aliText(orig))
		if status == "disable" {
			reason += "暂停后记录还在，但不再生效，随时可以启用。"
		}
		step = core.Step{Capability: "aliyun.dns.record.status", Summary: fmt.Sprintf("%s %s 的 %s", verb, name, aliText(orig)), Params: params}
	case "quick":
		if req.EdgeOne {
			return PlanView{}, userErr("EdgeOne 是腾讯云的服务，阿里云云解析里的域名不能在这里接入")
		}
		value, via := req.Value, ""
		switch req.Target {
		case "server":
			sv, err := a.Store.GetServer(req.ServerID)
			if err != nil {
				return PlanView{}, userErr("找不到这台服务器")
			}
			value, via = sv.Host, "服务器 "+sv.Name+"（"+sv.Host+"）"
			if !publicIP(value) {
				return PlanView{}, userErr("服务器 %s 的地址 %s 是内网地址，外面访问不到。请改用它的公网 IP", sv.Name, value)
			}
		case "ip":
			if !isIP(value) {
				return PlanView{}, userErr("%q 不是 IP 地址", value)
			}
			via = value
		case "host":
			value = strings.ToLower(strings.TrimSuffix(value, "."))
			if err := actions.CheckRecord("CNAME", value); err != nil {
				return PlanView{}, userErr("%q 不是域名", value)
			}
			via = value
		default:
			return PlanView{}, userErr("请选择要解析到哪里")
		}
		if name == value {
			return PlanView{}, userErr("不能解析到它自己")
		}
		typ := targetType(value)
		title = "解析 " + name + " → " + via
		reason = fmt.Sprintf("让 %s 直接解析到 %s（%s 记录，默认线路）。这个名字原来默认线路的 A、AAAA、CNAME 记录会被替换；执行后按 TTL 几分钟内在各地生效，可以一键恢复原来的解析。", name, via, typ)
		step = core.Step{Capability: "aliyun.dns.record.set", Summary: fmt.Sprintf("把 %s 解析到 %s %s", name, typ, value),
			Params: map[string]any{"domain": req.Domain, "subdomain": req.Sub, "type": typ, "value": value}}
	default:
		return PlanView{}, userErr("阿里云云解析的域名不支持这个操作（%s）", req.Op)
	}
	if _, err := actions.Resolve(step.Capability, step.Params, noServer); err != nil {
		return PlanView{}, userErr("%v", err)
	}
	p, _, err := a.proposePlan(ctx, "user", 0, title, reason, []core.Step{step})
	if err != nil {
		return PlanView{}, err
	}
	return a.Plan(p.ID)
}

func isIP(s string) bool { return targetType(s) != "CNAME" }

// toolAliyunDNS is the AI's look at 阿里云 云解析.
func (a *App) toolAliyunDNS(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Domain string `json:"domain"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return "", err
		}
	}
	c, err := needAliyun(a)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if in.Domain == "" {
		ds, err := c.Domains(ctx)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "阿里云云解析里有 %d 个域名：\n", len(ds))
		for _, d := range ds {
			fmt.Fprintf(&b, "- %s（%s，%d 条记录，DNS 服务器 %s）\n", d.Name, d.Edition, d.RecordCount, strings.Join(d.DNSServers, "、"))
		}
		return b.String(), nil
	}
	v, err := a.aliRecords(ctx, in.Domain)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "%s 的解析记录（阿里云云解析）：\n", v.Domain)
	for _, r := range v.Records {
		fmt.Fprintf(&b, "- id=\"%s\" %s %s %s 线路=%s TTL=%d", r.RID, r.Name, r.Type, r.Value, r.Line, r.TTL)
		if r.Type == "MX" {
			fmt.Fprintf(&b, " 优先级=%d", r.MX)
		}
		if !r.Enabled {
			b.WriteString(" 已暂停")
		}
		if r.Remark != "" {
			fmt.Fprintf(&b, " 备注=%s", r.Remark)
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// toolAliyunCDN is the AI's look at the 阿里云 CDN domains.
func (a *App) toolAliyunCDN(ctx context.Context, _ json.RawMessage) (string, error) {
	c, err := needAliyun(a)
	if err != nil {
		return "", err
	}
	ds, err := c.CDNDomains(ctx)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "阿里云 CDN 加速域名 %d 个：\n", len(ds))
	for _, d := range ds {
		var src []string
		for _, s := range d.Sources {
			src = append(src, s.Content)
		}
		fmt.Fprintf(&b, "- %s 状态=%s CNAME=%s 源站=%s HTTPS=%s\n", d.Name, d.Status, d.CNAME, strings.Join(src, ","), yesNoText(d.HTTPS))
	}
	return b.String(), nil
}
