package app

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// The 解析 page: DNSPod domains and their records, with what EdgeOne does
// for each name. Every change it offers becomes a checklist, like the
// AI's, so it is confirmed first, logged and can be undone.

// EOZoneView is the EdgeOne site a DNSPod domain belongs to.
type EOZoneView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"` // partial (CNAME access), full (NS access)
	Status string `json:"status"`
	Paused bool   `json:"paused"`
}

// DNSDomainView is one DNSPod domain.
type DNSDomainView struct {
	Name    string      `json:"name"`
	Status  string      `json:"status"`  // ENABLE, PAUSE, SPAM
	DNSOK   bool        `json:"dnsOk"`   // its name servers are DNSPod's
	Grade   string      `json:"grade"`   // DNSPod plan
	Records uint64      `json:"records"` // number of records
	EdgeOne *EOZoneView `json:"edgeone"` // its EdgeOne site, if any
}

// DNSDomainsView lists the domains; EOError says why EdgeOne could not be
// asked (no permission, say), the rest still works.
type DNSDomainsView struct {
	Domains []DNSDomainView `json:"domains"`
	EOError string          `json:"eoError,omitempty"`
}

// EORecordView is what EdgeOne does for a record's name.
type EORecordView struct {
	Status   string `json:"status"` // online, offline, process…
	Cname    string `json:"cname"`
	Origin   string `json:"origin"`
	Protocol string `json:"protocol"`
	HTTPS    string `json:"https"`  // certificate mode: eofreecert, disable…
	Points   bool   `json:"points"` // this record sends the name to EdgeOne
}

// DNSRecordView is one record.
type DNSRecordView struct {
	ID        uint64        `json:"id"`
	Name      string        `json:"name"`
	Full      string        `json:"full"`
	Type      string        `json:"type"`
	Value     string        `json:"value"`
	Line      string        `json:"line"`
	TTL       uint64        `json:"ttl"`
	MX        uint64        `json:"mx,omitempty"`
	Weight    *uint64       `json:"weight,omitempty"`
	Enabled   bool          `json:"enabled"`
	Remark    string        `json:"remark,omitempty"`
	System    bool          `json:"system,omitempty"` // DNSPod's own NS records
	UpdatedOn string        `json:"updatedOn,omitempty"`
	EdgeOne   *EORecordView `json:"edgeone,omitempty"`
}

// EOPendingView is an EdgeOne acceleration domain whose name does not
// resolve to EdgeOne yet, so EdgeOne serves nobody for it.
type EOPendingView struct {
	Name    string `json:"name"`
	Sub     string `json:"sub"`
	Cname   string `json:"cname"`
	Origin  string `json:"origin"`
	Status  string `json:"status"`
	Current string `json:"current,omitempty"` // what the name resolves to now
}

// DNSRecordsView is one domain's records.
type DNSRecordsView struct {
	Domain  string          `json:"domain"`
	Records []DNSRecordView `json:"records"`
	EdgeOne *EOZoneView     `json:"edgeone"`
	Pending []EOPendingView `json:"pending"`
	EOError string          `json:"eoError,omitempty"`
}

func needTencent(a *App) (*tencent.Client, error) {
	c := a.tencentClient()
	if c == nil {
		return nil, userErr("请先在「设置 → 腾讯云」填写密钥")
	}
	return c, nil
}

func zoneView(z tencent.Zone) *EOZoneView {
	return &EOZoneView{ID: z.ZoneID, Name: z.ZoneName, Type: z.Type, Status: z.Status, Paused: z.Paused}
}

// DNSDomains lists the DNSPod domains and their EdgeOne sites.
func (a *App) DNSDomains(ctx context.Context) (DNSDomainsView, error) {
	c, err := needTencent(a)
	if err != nil {
		return DNSDomainsView{}, err
	}
	ds, err := c.Domains(ctx)
	if err != nil {
		return DNSDomainsView{}, err
	}
	out := DNSDomainsView{Domains: []DNSDomainView{}}
	zones, zerr := c.Zones(ctx)
	if zerr != nil {
		out.EOError = zerr.Error()
	}
	for _, d := range ds {
		v := DNSDomainView{Name: d.Name, Status: d.Status, DNSOK: d.DNSStatus != "DNSERROR", Grade: d.Grade, Records: d.RecordCount}
		if z, ok := tencent.ZoneFor(zones, d.Name); ok && strings.EqualFold(z.ZoneName, d.Name) {
			v.EdgeOne = zoneView(z)
		}
		out.Domains = append(out.Domains, v)
	}
	sort.Slice(out.Domains, func(i, j int) bool { return out.Domains[i].Name < out.Domains[j].Name })
	return out, nil
}

func isEOCname(v string) bool {
	v = strings.ToLower(strings.TrimSuffix(v, "."))
	return strings.Contains(v, ".eo.dnse") || strings.HasSuffix(v, ".edgeone.app")
}

// DNSRecords lists a domain's records, marking the names EdgeOne serves.
func (a *App) DNSRecords(ctx context.Context, domain string) (DNSRecordsView, error) {
	c, err := needTencent(a)
	if err != nil {
		return DNSRecordsView{}, err
	}
	domain = strings.ToLower(strings.TrimSpace(domain))
	recs, err := c.Records(ctx, domain, "")
	if err != nil {
		return DNSRecordsView{}, err
	}
	out := DNSRecordsView{Domain: domain, Records: []DNSRecordView{}, Pending: []EOPendingView{}}
	eo := map[string]tencent.AccelerationDomain{}
	if zones, err := c.Zones(ctx); err != nil {
		out.EOError = err.Error()
	} else if z, ok := tencent.ZoneFor(zones, domain); ok && strings.EqualFold(z.ZoneName, domain) {
		out.EdgeOne = zoneView(z)
		if z.Type == "partial" && z.Status != "initializing" {
			list, err := c.AccelerationDomains(ctx, z.ZoneID, "")
			if err != nil {
				out.EOError = err.Error()
			}
			for _, d := range list {
				eo[strings.ToLower(d.DomainName)] = d
			}
		}
	}
	pointed := map[string]bool{}
	current := map[string][]string{}
	for _, r := range recs {
		v := DNSRecordView{ID: r.RecordID, Name: r.Name, Full: fullName(r.Name, domain), Type: r.Type, Value: r.Value, Line: r.Line,
			TTL: r.TTL, MX: r.MX, Weight: r.Weight, Enabled: r.Status != "DISABLE", Remark: r.Remark, System: r.DefaultNS, UpdatedOn: r.UpdatedOn}
		if d, ok := eo[strings.ToLower(v.Full)]; ok && (r.Type == "A" || r.Type == "AAAA" || r.Type == "CNAME") {
			points := r.Type == "CNAME" && strings.EqualFold(strings.TrimSuffix(r.Value, "."), d.Cname)
			v.EdgeOne = &EORecordView{Status: d.DomainStatus, Cname: d.Cname, Origin: d.OriginDetail.Origin, Protocol: d.OriginProtocol,
				HTTPS: d.Certificate.Mode, Points: points}
			if points && v.Enabled {
				pointed[strings.ToLower(v.Full)] = true
			}
		}
		if r.Line == tencent.DefaultLine && v.Enabled && (r.Type == "A" || r.Type == "AAAA" || r.Type == "CNAME") {
			current[strings.ToLower(v.Full)] = append(current[strings.ToLower(v.Full)], r.Type+" "+r.Value)
		}
		out.Records = append(out.Records, v)
	}
	sort.SliceStable(out.Records, func(i, j int) bool {
		a, b := out.Records[i], out.Records[j]
		if (a.Name == "@") != (b.Name == "@") {
			return a.Name == "@"
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.Line < b.Line
	})
	for name, d := range eo {
		if pointed[name] {
			continue
		}
		sub := subOf(name, domain)
		if sub == "" {
			continue
		}
		out.Pending = append(out.Pending, EOPendingView{Name: d.DomainName, Sub: sub, Cname: d.Cname, Origin: d.OriginDetail.Origin,
			Status: d.DomainStatus, Current: strings.Join(current[name], "、")})
	}
	sort.Slice(out.Pending, func(i, j int) bool { return out.Pending[i].Name < out.Pending[j].Name })
	return out, nil
}

// subOf is the host record of name under domain: "@" for the domain
// itself, "" when name is not under it.
func subOf(name, domain string) string {
	name, domain = strings.ToLower(name), strings.ToLower(domain)
	if name == domain {
		return "@"
	}
	if strings.HasSuffix(name, "."+domain) {
		return strings.TrimSuffix(name, "."+domain)
	}
	return ""
}

func fullName(sub, domain string) string {
	if sub == "@" || sub == "" {
		return domain
	}
	return sub + "." + domain
}

// dnsLines remembers each domain's resolution lines.
type dnsLines struct {
	mu    sync.Mutex
	lines map[string][]string
}

// DNSLines lists the resolution lines a domain's DNSPod plan offers.
func (a *App) DNSLines(ctx context.Context, domain string) ([]string, error) {
	c, err := needTencent(a)
	if err != nil {
		return nil, err
	}
	domain = strings.ToLower(domain)
	a.lines.mu.Lock()
	if l, ok := a.lines.lines[domain]; ok {
		a.lines.mu.Unlock()
		return l, nil
	}
	a.lines.mu.Unlock()
	grade := "DP_FREE"
	if ds, err := c.Domains(ctx); err == nil {
		for _, d := range ds {
			if strings.EqualFold(d.Name, domain) && d.Grade != "" {
				grade = d.Grade
			}
		}
	}
	lines, err := c.RecordLines(ctx, domain, grade)
	if err != nil || len(lines) == 0 {
		return []string{tencent.DefaultLine}, nil // editing on the default line still works
	}
	a.lines.mu.Lock()
	if a.lines.lines == nil {
		a.lines.lines = map[string][]string{}
	}
	a.lines.lines[domain] = lines
	a.lines.mu.Unlock()
	return lines, nil
}

// DNSRequest is a change asked for on the 解析 page.
type DNSRequest struct {
	// Op: add, modify, delete, status (one record); quick (make a name
	// point at a server, an IP or a host, through EdgeOne if asked);
	// eo_point (send a name to its EdgeOne acceleration domain); eo_off
	// (send a name that goes through EdgeOne straight to its origin).
	Op     string `json:"op"`
	Domain string `json:"domain"`
	ID     uint64 `json:"id"`
	Sub    string `json:"sub"`
	Type   string `json:"type"`
	Value  string `json:"value"`
	Line   string `json:"line"`
	TTL    int    `json:"ttl"`
	MX     int    `json:"mx"`
	Remark string `json:"remark"`
	Status string `json:"status"` // enable, disable

	Target   string `json:"target"` // quick: server, ip, host
	ServerID int64  `json:"serverId"`
	EdgeOne  bool   `json:"edgeone"`
	HTTPS    bool   `json:"https"`    // quick with EdgeOne: a free certificate
	Protocol string `json:"protocol"` // quick with EdgeOne: HTTP or HTTPS to the origin
	Area     string `json:"area"`     // quick with EdgeOne, new site: mainland, overseas, global
}

func recordText(typ, value string, mx int) string {
	if typ == "MX" {
		return fmt.Sprintf("MX %d %s", mx, value)
	}
	return typ + " " + value
}

func lineText(line string) string {
	if line == "" || line == tencent.DefaultLine {
		return ""
	}
	return "（" + line + "线路）"
}

// ProposeDNS turns a change on the 解析 page into a checklist.
func (a *App) ProposeDNS(ctx context.Context, req DNSRequest) (PlanView, error) {
	c, err := needTencent(a)
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
	var title, reason string
	var steps []core.Step
	switch req.Op {
	case "add", "modify":
		title, reason, steps, err = a.dnsEdit(ctx, c, req)
	case "delete", "status":
		title, reason, steps, err = a.dnsRecordOp(ctx, c, req)
	case "quick":
		title, reason, steps, err = a.dnsQuick(ctx, c, req)
	case "eo_point":
		title, reason, steps, err = a.dnsEOPoint(ctx, c, req)
	case "eo_off":
		title, reason, steps, err = a.dnsEOOff(ctx, c, req)
	default:
		return PlanView{}, userErr("不支持的操作 %q", req.Op)
	}
	if err != nil {
		return PlanView{}, err
	}
	p, _, err := a.proposePlan(ctx, "user", 0, title, reason, steps)
	if err != nil {
		return PlanView{}, err
	}
	return a.Plan(p.ID)
}

func (a *App) lookupRecord(ctx context.Context, c *tencent.Client, domain string, id uint64) (tencent.Record, error) {
	all, err := c.Records(ctx, domain, "")
	if err != nil {
		return tencent.Record{}, err
	}
	for _, r := range all {
		if r.RecordID == id {
			return r, nil
		}
	}
	return tencent.Record{}, userErr("找不到这条记录，可能已经在别处修改或删除了，请刷新")
}

func (a *App) dnsEdit(ctx context.Context, c *tencent.Client, req DNSRequest) (string, string, []core.Step, error) {
	req.Type = strings.ToUpper(req.Type)
	if err := actions.CheckRecord(req.Type, req.Value); err != nil {
		return "", "", nil, userErr("%v", err)
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
	name := fullName(req.Sub, req.Domain)
	now := recordText(req.Type, req.Value, req.MX)
	if req.Op == "add" {
		return "添加解析：" + name, "给 " + name + " 添加一条 " + now + " 记录" + lineText(req.Line) + "。执行后按 TTL 几分钟内在各地生效，可以一键撤销（删除这条记录）。",
			[]core.Step{{Capability: "dns.record.add", Summary: fmt.Sprintf("添加 %s 的 %s 记录%s", name, now, lineText(req.Line)), Params: params}}, nil
	}
	orig, err := a.lookupRecord(ctx, c, req.Domain, req.ID)
	if err != nil {
		return "", "", nil, err
	}
	if orig.DefaultNS {
		return "", "", nil, userErr("这是 DNSPod 给域名自带的 NS 记录，不能修改")
	}
	params["record_id"] = req.ID
	before := recordText(orig.Type, orig.Value, int(orig.MX))
	reason := fmt.Sprintf("把 %s 的 %s 改为 %s %s。执行后按 TTL 几分钟内在各地生效，可以一键改回。", fullName(orig.Name, req.Domain), before, name, now)
	if orig.Type == "CNAME" && isEOCname(orig.Value) && !isEOCname(req.Value) {
		reason += "\n注意：这条记录现在把网站交给 EdgeOne，改掉以后访客不再经过 EdgeOne（缓存、防护和 EdgeOne 的证书都不起作用了）。"
	}
	return "修改解析：" + name, reason,
		[]core.Step{{Capability: "dns.record.modify", Summary: fmt.Sprintf("把 %s 的 %s 改为 %s", fullName(orig.Name, req.Domain), before, now), Params: params}}, nil
}

func (a *App) dnsRecordOp(ctx context.Context, c *tencent.Client, req DNSRequest) (string, string, []core.Step, error) {
	orig, err := a.lookupRecord(ctx, c, req.Domain, req.ID)
	if err != nil {
		return "", "", nil, err
	}
	if orig.DefaultNS {
		return "", "", nil, userErr("这是 DNSPod 给域名自带的 NS 记录，不能修改")
	}
	name := fullName(orig.Name, req.Domain)
	text := recordText(orig.Type, orig.Value, int(orig.MX)) + lineText(orig.Line)
	params := map[string]any{"domain": req.Domain, "record_id": req.ID}
	if req.Op == "delete" {
		reason := fmt.Sprintf("删除 %s 的 %s。删除后按 TTL 几分钟内在各地失效，可以一键加回来。", name, text)
		switch {
		case orig.Type == "MX":
			reason += "\n注意：这是收邮件用的记录，删掉后发到这个域名的邮件可能收不到。"
		case orig.Type == "CNAME" && isEOCname(orig.Value):
			reason += "\n注意：这条记录把网站交给 EdgeOne，删掉后这个网址可能打不开。"
		case orig.Type == "A" || orig.Type == "AAAA" || orig.Type == "CNAME":
			reason += "\n注意：如果这是网站在用的地址，删掉后这个网址会打不开。"
		}
		return "删除解析：" + name, reason, []core.Step{{Capability: "dns.record.delete", Summary: fmt.Sprintf("删除 %s 的 %s", name, text), Params: params}}, nil
	}
	status := strings.ToLower(req.Status)
	if status != "enable" && status != "disable" {
		return "", "", nil, userErr("状态只能是 enable 或 disable")
	}
	params["status"] = status
	verb := map[string]string{"enable": "启用", "disable": "暂停"}[status]
	reason := fmt.Sprintf("%s %s 的 %s。", verb, name, text)
	if status == "disable" {
		reason += "暂停后记录还在，但不再生效，随时可以启用。"
	}
	return verb + "解析：" + name, reason, []core.Step{{Capability: "dns.record.status", Summary: fmt.Sprintf("%s %s 的 %s", verb, name, text), Params: params}}, nil
}

// targetType is the record type that points a name at value.
func targetType(value string) string {
	if ip := net.ParseIP(value); ip != nil {
		if ip.To4() != nil {
			return "A"
		}
		return "AAAA"
	}
	return "CNAME"
}

func publicIP(value string) bool {
	ip := net.ParseIP(value)
	return ip == nil || !(ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified())
}

func (a *App) dnsQuick(ctx context.Context, c *tencent.Client, req DNSRequest) (string, string, []core.Step, error) {
	value, via := req.Value, ""
	switch req.Target {
	case "server":
		sv, err := a.Store.GetServer(req.ServerID)
		if err != nil {
			return "", "", nil, userErr("找不到这台服务器")
		}
		value, via = sv.Host, "服务器 "+sv.Name+"（"+sv.Host+"）"
		if !publicIP(value) {
			return "", "", nil, userErr("服务器 %s 的地址 %s 是内网地址，外面访问不到。请改用它的公网 IP", sv.Name, value)
		}
	case "ip":
		if net.ParseIP(value) == nil {
			return "", "", nil, userErr("%q 不是 IP 地址", value)
		}
		via = value
	case "host":
		value = strings.ToLower(strings.TrimSuffix(value, "."))
		if err := actions.CheckRecord("CNAME", value); err != nil {
			return "", "", nil, userErr("%q 不是域名", value)
		}
		via = value
	default:
		return "", "", nil, userErr("请选择要解析到哪里")
	}
	typ := targetType(value)
	name := fullName(req.Sub, req.Domain)
	if name == value {
		return "", "", nil, userErr("不能解析到它自己")
	}
	if !req.EdgeOne {
		return "解析 " + name + " → " + via,
			fmt.Sprintf("让 %s 直接解析到 %s（%s 记录，默认线路）。这个名字原来的 A、AAAA、CNAME 记录会被替换；执行后按 TTL 几分钟内在各地生效，可以一键恢复原来的解析。", name, via, typ),
			[]core.Step{{Capability: "dns.record.set", Summary: fmt.Sprintf("把 %s 解析到 %s %s", name, typ, value),
				Params: map[string]any{"domain": req.Domain, "subdomain": req.Sub, "type": typ, "value": value}}}, nil
	}

	if !publicIP(value) {
		return "", "", nil, userErr("EdgeOne 要能从外面访问到源站，%s 是内网地址", value)
	}
	proto := strings.ToUpper(req.Protocol)
	if proto != "HTTPS" {
		proto = "HTTP"
	}
	zones, err := c.Zones(ctx)
	if err != nil {
		return "", "", nil, fmt.Errorf("查询 EdgeOne 站点失败：%w", err)
	}
	var steps []core.Step
	z, ok := tencent.ZoneFor(zones, name)
	notes := []string{}
	switch {
	case ok && z.Type == "full":
		return "", "", nil, userErr("%s 是用 NS 方式接入 EdgeOne 的，解析在 EdgeOne 里管理，不在 DNSPod", z.ZoneName)
	case ok && z.Paused:
		return "", "", nil, userErr("EdgeOne 站点 %s 已停用，请先在 EdgeOne 控制台启用", z.ZoneName)
	case ok && z.Status == "initializing":
		return "", "", nil, userErr("EdgeOne 站点 %s 还没有绑定套餐，请先在 EdgeOne 控制台绑定", z.ZoneName)
	case !ok:
		area := req.Area
		if area != "overseas" && area != "global" {
			area = "mainland"
		}
		plans, err := c.Plans(ctx)
		if err != nil {
			return "", "", nil, fmt.Errorf("查询 EdgeOne 套餐失败：%w", err)
		}
		bindable := false
		for _, p := range plans {
			if p.Bindable == "true" {
				bindable = true
			}
		}
		if !bindable {
			return "", "", nil, userErr("%s 还没有接入 EdgeOne，而账号里没有能再绑定站点的 EdgeOne 套餐。购买套餐涉及计费，需要你在腾讯云控制台操作；或者先不经过 EdgeOne，直接解析", req.Domain)
		}
		steps = append(steps, core.Step{Capability: "eo.zone.create", Summary: fmt.Sprintf("把 %s 接入 EdgeOne（CNAME 方式，用账号里能绑定的套餐；需要时自动添加验证记录）", req.Domain),
			Params: map[string]any{"domain": req.Domain, "area": area}})
		if area == "mainland" || area == "global" {
			notes = append(notes, "加速区域包含中国大陆时，域名需要已经备案。")
		}
	}
	existing := false
	if ok {
		d, found, err := c.AccelerationDomain(ctx, z.ZoneID, name)
		if err != nil {
			return "", "", nil, fmt.Errorf("查询 EdgeOne 加速域名失败：%w", err)
		}
		if found {
			existing = true
			if !strings.EqualFold(d.OriginDetail.Origin, value) || !strings.EqualFold(d.OriginProtocol, proto) {
				steps = append(steps, core.Step{Capability: "eo.origin.set", Summary: fmt.Sprintf("把 EdgeOne 里 %s 的源站从 %s 改为 %s（%s 回源）", name, d.OriginDetail.Origin, value, proto),
					Params: map[string]any{"domain": name, "origin": value, "origin_protocol": proto}})
			}
		}
	}
	if !existing {
		steps = append(steps, core.Step{Capability: "eo.domain.add", Summary: fmt.Sprintf("在 EdgeOne 添加加速域名 %s，回源到 %s（%s）", name, value, proto),
			Params: map[string]any{"domain": name, "origin": value, "origin_protocol": proto}})
	}
	steps = append(steps, core.Step{Capability: "dns.record.set", Summary: fmt.Sprintf("把 %s 的解析改成 EdgeOne 分配的 CNAME", name),
		Params: map[string]any{"domain": req.Domain, "subdomain": req.Sub, "point_to": "eo"}})
	if req.HTTPS {
		steps = append(steps, core.Step{Capability: "eo.https.set", Summary: fmt.Sprintf("给 %s 申请 EdgeOne 免费证书，开启 HTTPS（自动续签）", name),
			Params: map[string]any{"domain": name, "mode": "eofreecert"}})
		notes = append(notes, "免费证书要等解析生效后才能申请下来；如果这一步提示还没生效，过几分钟再单独执行它就行。")
	}
	if proto == "HTTP" {
		notes = append(notes, "EdgeOne 用 HTTP 回源，服务器上不需要证书。")
	} else {
		notes = append(notes, "EdgeOne 用 HTTPS 回源，服务器上这个网站要有有效的证书。")
	}
	reason := fmt.Sprintf("让 %s 经过 EdgeOne：访客先到 EdgeOne 节点，再由 EdgeOne 回源到 %s。这个名字原来的 A、AAAA、CNAME 记录会被替换成 EdgeOne 的 CNAME，按 TTL 几分钟内在各地生效。每一步都可以撤销。\n%s",
		name, via, strings.Join(notes, ""))
	return "解析 " + name + " → " + via + "（经过 EdgeOne）", reason, steps, nil
}

func (a *App) eoDomainFor(ctx context.Context, c *tencent.Client, name string) (tencent.AccelerationDomain, error) {
	zones, err := c.Zones(ctx)
	if err != nil {
		return tencent.AccelerationDomain{}, fmt.Errorf("查询 EdgeOne 站点失败：%w", err)
	}
	z, ok := tencent.ZoneFor(zones, name)
	if !ok {
		return tencent.AccelerationDomain{}, userErr("EdgeOne 里没有 %s 所在的站点", name)
	}
	d, found, err := c.AccelerationDomain(ctx, z.ZoneID, name)
	if err != nil {
		return d, fmt.Errorf("查询 EdgeOne 加速域名失败：%w", err)
	}
	if !found {
		return d, userErr("EdgeOne 里没有加速域名 %s", name)
	}
	return d, nil
}

func (a *App) dnsEOPoint(ctx context.Context, c *tencent.Client, req DNSRequest) (string, string, []core.Step, error) {
	name := fullName(req.Sub, req.Domain)
	d, err := a.eoDomainFor(ctx, c, name)
	if err != nil {
		return "", "", nil, err
	}
	if d.Cname == "" {
		return "", "", nil, userErr("EdgeOne 还没有给 %s 分配 CNAME，稍后再试", name)
	}
	return "把 " + name + " 解析到 EdgeOne",
		fmt.Sprintf("EdgeOne 里已经有加速域名 %s（回源到 %s），但解析还没指向它，所以访客没有经过 EdgeOne。把解析改成 CNAME %s 后，按 TTL 几分钟内生效；这个名字原来的 A、AAAA、CNAME 记录会被替换，可以一键恢复。",
			name, d.OriginDetail.Origin, d.Cname),
		[]core.Step{{Capability: "dns.record.set", Summary: fmt.Sprintf("把 %s 解析到 EdgeOne（CNAME %s）", name, d.Cname),
			Params: map[string]any{"domain": req.Domain, "subdomain": req.Sub, "point_to": "eo"}}}, nil
}

func (a *App) dnsEOOff(ctx context.Context, c *tencent.Client, req DNSRequest) (string, string, []core.Step, error) {
	orig, err := a.lookupRecord(ctx, c, req.Domain, req.ID)
	if err != nil {
		return "", "", nil, err
	}
	name := fullName(orig.Name, req.Domain)
	d, err := a.eoDomainFor(ctx, c, name)
	if err != nil {
		return "", "", nil, err
	}
	origin := strings.ToLower(d.OriginDetail.Origin)
	if origin == "" || !publicIP(origin) {
		return "", "", nil, userErr("EdgeOne 里 %s 的源站是 %q，不能直接解析过去", name, d.OriginDetail.Origin)
	}
	typ := targetType(origin)
	return "让 " + name + " 不再经过 EdgeOne",
		fmt.Sprintf("把 %s 的解析从 EdgeOne（CNAME %s）改为直接指向源站 %s。之后访客直接连服务器：EdgeOne 的缓存、防护和证书都不再起作用，服务器的 IP 也会公开。"+
			"如果网站用 HTTPS，服务器上要有有效的证书（可以在「证书」页检查）。EdgeOne 里的加速域名保留，随时可以切回来；这一步也可以一键撤销。", name, d.Cname, origin),
		[]core.Step{{Capability: "dns.record.set", Summary: fmt.Sprintf("把 %s 直接解析到源站 %s %s", name, typ, origin),
			Params: map[string]any{"domain": req.Domain, "subdomain": orig.Name, "type": typ, "value": origin}}}, nil
}
