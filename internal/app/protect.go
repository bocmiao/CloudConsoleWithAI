package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/ai"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
	"github.com/bocmiao/CloudConsoleWithAI/internal/visits"
)

// Blocking IPs from the statistics page goes through a checklist like
// everything else: the page proposes it, the user confirms, and it can be
// undone. Blocking is done by EdgeOne, which stands in front of the sites.

// BlockedZone is an EdgeOne site and the IPs Miao Panel blocked on it.
type BlockedZone struct {
	Zone string   `json:"zone"`
	IPs  []string `json:"ips"`
}

// Blocked lists the IPs Miao Panel blocked on each EdgeOne site.
func (a *App) Blocked(ctx context.Context) ([]BlockedZone, error) {
	c := a.tencentClient()
	out := []BlockedZone{}
	if c == nil {
		return out, nil
	}
	zones, err := c.Zones(ctx)
	if err != nil {
		return nil, err
	}
	for _, z := range zones {
		p, err := c.SecurityPolicy(ctx, z.ZoneID)
		if err != nil {
			return nil, fmt.Errorf("读取 EdgeOne 站点 %s 的安全策略失败：%w", z.ZoneName, err)
		}
		if ips := actions.BlockedIPs(p); len(ips) > 0 {
			out = append(out, BlockedZone{Zone: z.ZoneName, IPs: ips})
		}
	}
	return out, nil
}

// profiles finds what a report knows about each IP, preferring the
// longest range.
func profiles(v VisitsView) map[string]visits.IPProfile {
	out := map[string]visits.IPProfile{}
	for _, n := range []int{1, 7, 30} {
		if rg := v.Range(n); rg != nil {
			for _, p := range rg.IPs {
				out[p.IP] = p
			}
		}
	}
	return out
}

// proposePlan stores a checklist; actor is who proposed it (ai or user).
// It returns the steps as checked.
func (a *App) proposePlan(ctx context.Context, actor string, serverID int64, title, reason string, steps []core.Step) (store.Plan, []core.Step, error) {
	sv, err := a.planServer(serverID)
	if err != nil {
		return store.Plan{}, nil, fmt.Errorf("找不到服务器 %d", serverID)
	}
	for i := range steps {
		steps[i].Free = nil // only Miao Panel's own checks may fill this in
		if steps[i].Capability == freeCapability {
			a.vetFree(ctx, sv, steps, i)
		}
	}
	steps = prepareSteps(steps, sv.Adapter)
	data, err := json.Marshal(steps)
	if err != nil {
		return store.Plan{}, nil, err
	}
	p, err := a.Store.AddPlan(store.Plan{ServerID: serverID, Title: title, Reason: reason, Steps: string(data), Status: core.PlanProposed})
	if err != nil {
		return p, nil, err
	}
	_ = a.Store.Audit(actor, "plan.propose", title, fmt.Sprintf("服务器 %d，%d 步", serverID, len(steps)))
	return p, steps, nil
}

// ProposeBlock makes a checklist that blocks IPs on the EdgeOne sites
// they visited. EdgeOne's nodes, private addresses and verified search
// engine crawlers are refused. IPs the automatic rule blocked for a while
// become the user's own, kept until unblocked by hand.
func (a *App) ProposeBlock(ctx context.Context, source string, ips []string) (PlanView, error) {
	a.forgetAutoBlocked(ips)
	return a.blockPlan(ctx, "user", source, ips, "", "根据访问日志：")
}

// blockPlan stores a block checklist proposed by actor; title is made up
// when empty, lead starts the reason.
func (a *App) blockPlan(ctx context.Context, actor, source string, ips []string, title, lead string) (PlanView, error) {
	c := a.tencentClient()
	if c == nil {
		return PlanView{}, userErr("封禁要通过 EdgeOne 进行，请先在「设置 → 腾讯云」填写密钥")
	}
	v, err := a.Visits(ctx, source, false)
	if err != nil {
		return PlanView{}, err
	}
	known := profiles(v)
	zones, err := c.Zones(ctx)
	if err != nil {
		return PlanView{}, err
	}
	byZone := map[string][]string{}
	var skipped, notes []string
	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		p, ok := known[ip]
		switch {
		case net.ParseIP(ip) == nil:
			skipped = append(skipped, ip+"：不是 IP 地址")
			continue
		case visits.Private(ip):
			skipped = append(skipped, ip+"：内网地址")
			continue
		case ok && p.EdgeOne:
			skipped = append(skipped, ip+"：EdgeOne 的节点，封了会挡住所有经过它的访客")
			continue
		case ok && p.Crawler != "":
			skipped = append(skipped, ip+"：已验证的搜索引擎爬虫（"+p.Crawler+"）")
			continue
		}
		sites := []string{}
		for _, s := range p.Sites {
			sites = append(sites, s.Value)
		}
		if len(sites) == 0 {
			sites = v.SiteNames()
		}
		placed := false
		for _, site := range sites {
			if z, ok := tencent.ZoneFor(zones, site); ok {
				if !contains(byZone[z.ZoneName], ip) {
					byZone[z.ZoneName] = append(byZone[z.ZoneName], ip)
				}
				placed = true
			}
		}
		if !placed {
			skipped = append(skipped, ip+"：访问的网站（"+strings.Join(sites, "、")+"）没有经过 EdgeOne，不能在 EdgeOne 封禁")
			continue
		}
		why := strings.Join(p.Reasons, "；")
		if why == "" {
			why = "由你选择封禁"
		}
		notes = append(notes, fmt.Sprintf("%s（%s）：%s", ip, orDash(strings.TrimSpace(p.Place+" "+p.ISP)), why))
	}
	if len(byZone) == 0 {
		return PlanView{}, userErr("没有可以封禁的 IP：%s", strings.Join(skipped, "；"))
	}
	names := make([]string, 0, len(byZone))
	for z := range byZone {
		names = append(names, z)
	}
	sort.Strings(names)
	var steps []core.Step
	for _, z := range names {
		steps = append(steps, core.Step{Capability: "eo.ip.block", Summary: fmt.Sprintf("在 EdgeOne 站点 %s 封禁 %d 个 IP", z, len(byZone[z])),
			Params: map[string]any{"domain": z, "ips": strings.Join(byZone[z], ",")}})
	}
	reason := lead + "\n" + strings.Join(notes, "\n")
	if len(skipped) > 0 {
		reason += "\n没有加入：\n" + strings.Join(skipped, "\n")
	}
	if title == "" {
		title = fmt.Sprintf("封禁 %d 个可疑 IP", countUnique(byZone))
	}
	p, _, err := a.proposePlan(ctx, actor, v.ServerID, title, reason, steps)
	if err != nil {
		return PlanView{}, err
	}
	return a.Plan(p.ID)
}

// ProposeUnblock makes a checklist that lifts Miao Panel's block on IPs
// in an EdgeOne site.
func (a *App) ProposeUnblock(ctx context.Context, zone string, ips []string) (PlanView, error) {
	a.forgetAutoBlocked(ips)
	return a.unblockPlan(ctx, "user", zone, ips, "由你在网站统计页选择解封")
}

func (a *App) unblockPlan(ctx context.Context, actor, zone string, ips []string, reason string) (PlanView, error) {
	if a.tencentClient() == nil || zone == "" || len(ips) == 0 {
		return PlanView{}, userErr("要给出 EdgeOne 站点和要解封的 IP")
	}
	step := core.Step{Capability: "eo.ip.unblock", Summary: fmt.Sprintf("在 EdgeOne 站点 %s 解除封禁 %d 个 IP", zone, len(ips)),
		Params: map[string]any{"domain": zone, "ips": strings.Join(ips, ",")}}
	p, _, err := a.proposePlan(ctx, actor, 0, fmt.Sprintf("解除封禁 %d 个 IP", len(ips)), reason, []core.Step{step})
	if err != nil {
		return PlanView{}, err
	}
	return a.Plan(p.ID)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func countUnique(m map[string][]string) int {
	seen := map[string]bool{}
	for _, list := range m {
		for _, ip := range list {
			seen[ip] = true
		}
	}
	return len(seen)
}

// IPVerdict is the AI's call on one IP.
type IPVerdict struct {
	IP     string `json:"ip"`
	Action string `json:"action"` // block, watch, ignore
	Reason string `json:"reason"`
}

// Judgement is the AI's reading of a set of IPs.
type Judgement struct {
	Summary  string      `json:"summary"`
	Verdicts []IPVerdict `json:"verdicts"`
	Cost     float64     `json:"cost"`
	Currency string      `json:"currency"`
}

const judgeSystem = `你是网站安全分析员。根据网站访问日志的统计，判断每个 IP 是正常访客、搜索引擎、监控程序，还是扫描、攻击、刷量，并给出处理建议。
只输出一个 JSON 对象，不要输出别的：{"summary": "一两句总体结论", "ips": [{"ip": "…", "action": "block|watch|ignore", "reason": "一句话理由，说出依据的数字或访问的地址"}]}
- block：有明确恶意迹象（探测后台或密钥文件、请求带攻击代码、猜密码、异常高频、冒充搜索引擎），建议封禁；
- watch：可疑但证据不足，先观察；
- ignore：正常访客、已验证的搜索引擎、监控或 EdgeOne 节点、内网地址，不用处理。EdgeOne 节点和已验证的搜索引擎绝不能 block。
每个给出的 IP 都要有结论，理由用中文、面向不懂技术的网站主人。`

// describeIP is a profile in one line, for the AI.
func describeIP(p visits.IPProfile) string {
	var paths, sites []string
	for _, it := range p.TopPaths {
		paths = append(paths, fmt.Sprintf("%s %d", it.Value, it.Count))
	}
	for _, it := range p.Sites {
		sites = append(sites, fmt.Sprintf("%s %d", it.Value, it.Count))
	}
	facts := []string{}
	if p.EdgeOne {
		facts = append(facts, "EdgeOne 节点")
	}
	if p.Crawler != "" {
		facts = append(facts, "已验证的"+p.Crawler)
	}
	if p.FakeCrawler {
		facts = append(facts, "冒充搜索引擎")
	}
	return fmt.Sprintf("%s [%s %d 分] %s %s；请求 %d，PV %d，404/403 %d，5xx %d，POST %d，%d 个不同地址，一分钟最多 %d 次，%s 到 %s；网站 %s；常访问 %s；UA %s；已知：%s；规则判断：%s",
		p.IP, riskNames[p.Risk], p.Score, orDash(p.Place), p.ISP, p.Requests, p.PV, p.E4xx, p.E5xx, p.Posts, p.Paths, p.PeakMin, p.First, p.Last,
		strings.Join(sites, "、"), strings.Join(paths, "、"), clipText(p.UA, 160), orDash(strings.Join(facts, "、")), orDash(strings.Join(p.Reasons, "；")))
}

// JudgeIPs asks the AI about IPs from a source's report over days (1, 7
// or 30); with no IPs given, the riskiest ones.
func (a *App) JudgeIPs(ctx context.Context, source string, days int, ips []string) (Judgement, error) {
	v, err := a.Visits(ctx, source, false)
	if err != nil {
		return Judgement{}, err
	}
	rg := v.Range(days)
	if rg == nil {
		return Judgement{}, userErr("天数只能是 1、7 或 30")
	}
	want := map[string]bool{}
	for _, ip := range ips {
		want[strings.TrimSpace(ip)] = true
	}
	var lines []string
	for _, p := range rg.IPs {
		if (len(want) == 0 && p.Risk != visits.RiskNone) || want[p.IP] {
			lines = append(lines, "- "+describeIP(p))
		}
		if len(lines) == 25 {
			break
		}
	}
	if len(lines) == 0 {
		return Judgement{Summary: "这段时间没有需要研判的 IP", Verdicts: []IPVerdict{}}, nil
	}
	prompt := fmt.Sprintf("数据来源：%s（统计于 %s），最近 %d 天，网站：%s。\n口径：PV 是浏览器打开网页的次数；404/403 多、访问大量不同地址、带攻击代码是扫描的迹象。\n要判断的 IP：\n%s",
		v.Title, v.CheckedAt, days, strings.Join(v.SiteNames(), "、"), strings.Join(lines, "\n"))
	var text string
	var usage ai.Usage
	settings, _ := a.AISettings()
	if a.Analyst != nil {
		if text, err = a.Analyst(ctx, prompt); err != nil {
			return Judgement{}, err
		}
	} else {
		cfg, s, err := a.sessionConfig(nil, judgeSystem)
		if err != nil {
			return Judgement{}, err
		}
		settings = s
		cfg.MaxTokens = 3000
		sess, err := ai.NewSession(cfg)
		if err != nil {
			return Judgement{}, err
		}
		sess.AddUser(prompt)
		turn, err := sess.Next(ctx, nil)
		usage = turn.Usage
		if usage.Input+usage.Output > 0 {
			_ = a.Store.AddUsage(store.Usage{Model: s.Model, InputTokens: usage.Input, CachedTokens: usage.CachedInput,
				OutputTokens: usage.Output, Cost: s.Cost(usage), Currency: s.Currency})
		}
		if err != nil {
			return Judgement{}, userErr("AI 研判失败：%v", err)
		}
		text = turn.Text
	}
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	var out struct {
		Summary string      `json:"summary"`
		IPs     []IPVerdict `json:"ips"`
	}
	if start < 0 || end < start || json.Unmarshal([]byte(text[start:end+1]), &out) != nil {
		return Judgement{}, userErr("AI 的回答看不懂：%.200s", text)
	}
	j := Judgement{Summary: out.Summary, Verdicts: []IPVerdict{}, Cost: settings.Cost(usage), Currency: settings.Currency}
	known := profiles(v)
	for _, verdict := range out.IPs {
		p, ok := known[verdict.IP]
		if !ok {
			continue // not one we asked about
		}
		switch verdict.Action {
		case "block", "watch", "ignore":
		default:
			verdict.Action = "watch"
		}
		// Never suggest blocking what must not be blocked.
		if verdict.Action == "block" && (p.EdgeOne || p.Crawler != "" || visits.Private(p.IP)) {
			verdict.Action, verdict.Reason = "ignore", verdict.Reason+"（EdgeOne 节点、已验证的搜索引擎和内网地址不能封禁）"
		}
		j.Verdicts = append(j.Verdicts, verdict)
	}
	_ = a.Store.Audit("ai", "visits.judge", v.Title, fmt.Sprintf("%d 个 IP：%s", len(j.Verdicts), clipText(j.Summary, 200)))
	return j, nil
}
