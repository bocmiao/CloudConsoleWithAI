package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/internal/visits"
	"github.com/bocmiao/CloudConsoleWithAI/scripts"
)

// Website visits are counted from access logs: EdgeOne's (every request,
// real visitor IPs; for sites behind EdgeOne) or a server's own (counted
// on the server by sitelogs.sh; real time, every site on it).

// VisitsView is one source's visits report.
type VisitsView struct {
	visits.Report
	Source     string `json:"sourceKey"` // edgeone, server:<id>
	Title      string `json:"title"`
	ServerID   int64  `json:"serverId,omitempty"`
	CheckedAt  string `json:"checkedAt"`
	Refreshing bool   `json:"refreshing,omitempty"`
}

// VisitSource is where visits can be counted from.
type VisitSource struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Note  string `json:"note"`
}

// visitsTTL is how long a report counts as current; counting reads the
// logs, so it is not repeated more often than this.
const visitsTTL = 5 * time.Minute

const maxVisitsOutput = 16 << 20

// VisitSources lists EdgeOne (when Tencent Cloud is set up) and each server.
func (a *App) VisitSources() ([]VisitSource, error) {
	var out []VisitSource
	if a.tencentClient() != nil {
		out = append(out, VisitSource{Key: "edgeone", Title: "EdgeOne 日志", Note: "经过 EdgeOne 的网站：每个请求都在，访客 IP 真实，最准；有十几分钟到一小时的延迟"})
	}
	servers, err := a.Store.ListServers()
	if err != nil {
		return nil, err
	}
	for _, sv := range servers {
		out = append(out, VisitSource{Key: fmt.Sprintf("server:%d", sv.ID), Title: "服务器 " + sv.Name + " 的日志",
			Note: "服务器上的所有网站，实时；被 EdgeOne 缓存的请求不在里面，经过 EdgeOne 时要日志记下真实 IP"})
	}
	if out == nil {
		out = []VisitSource{}
	}
	return out, nil
}

func visitsKey(source string) string { return "visits_" + strings.ReplaceAll(source, ":", "_") }

// gatherer returns what counts a source's visits.
func (a *App) gatherer(source string) (func(context.Context) (VisitsView, error), error) {
	if source == "edgeone" {
		if a.tencentClient() == nil {
			return nil, userErr("还没有填写腾讯云密钥，不能读取 EdgeOne 日志")
		}
		return a.gatherEdgeOneVisits, nil
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(source, "server:"), 10, 64)
	if err != nil || !strings.HasPrefix(source, "server:") {
		return nil, userErr("不认识的数据来源：%s", source)
	}
	if _, err := a.Store.GetServer(id); err != nil {
		return nil, userErr("找不到这台服务器（编号 %d）", id)
	}
	return func(ctx context.Context) (VisitsView, error) { return a.gatherServerVisits(ctx, id) }, nil
}

// Visits returns a current report for a source, counting again when the
// last one is older than a few minutes (or when refresh).
func (a *App) Visits(ctx context.Context, source string, refresh bool) (VisitsView, error) {
	g, err := a.gatherer(source)
	if err != nil {
		return VisitsView{}, err
	}
	return a.visits.get(ctx, a, visitsKey(source), visitsTTL, refresh, g)
}

// LatestVisits answers at once with the last report, even one saved before
// Miao Panel restarted, refreshing it in the background when old.
func (a *App) LatestVisits(ctx context.Context, source string) (VisitsView, error) {
	g, err := a.gatherer(source)
	if err != nil {
		return VisitsView{}, err
	}
	v, refreshing, err := a.visits.latest(ctx, a, visitsKey(source), visitsTTL, g)
	v.Refreshing = refreshing
	return v, err
}

// gatherServerVisits runs sitelogs.sh on a server and logs the run.
func (a *App) gatherServerVisits(ctx context.Context, id int64) (VisitsView, error) {
	sv, c, err := a.connect(ctx, id)
	if err != nil {
		return VisitsView{}, err
	}
	defer c.Close()
	ex := a.startExec(store.ExecLog{ServerID: sv.ID, ServerName: sv.Name, Adapter: sv.Adapter, Origin: originOf(ctx),
		Kind: store.ExecRead, Title: "统计网站访问日志", Via: "只读脚本", ScriptName: "sitelogs.sh"})
	res, err := c.RunScript(ctx, sv.Username, scripts.SiteLogs, nil, maxVisitsOutput)
	ex.Commands = "# 把只读统计脚本 sitelogs.sh 通过标准输入交给服务器执行（全文见「脚本」）\n" + res.Command
	switch {
	case err != nil:
		a.finishExec(&ex, actions.StatusFailed, err.Error())
		return VisitsView{}, friendlySSHError(err)
	case strings.TrimSpace(res.Stdout) == "":
		msg := fmt.Sprintf("没有输出（退出码 %d）：%s", res.ExitCode, strings.TrimSpace(res.Stderr))
		a.finishExec(&ex, actions.StatusFailed, msg)
		return VisitsView{}, userErr("统计访问日志失败：%s", msg)
	}
	rep := visits.Parse(res.Stdout)
	if res.Truncated {
		rep.Notes = append(rep.Notes, "统计结果太长被截断了，部分排行可能不全")
	}
	summary := rep.Problem
	if month := rep.Range(30); summary == "" && month != nil && month.Sites[visits.All] != nil {
		t := month.Sites[visits.All].Total
		summary = fmt.Sprintf("%d 个网站，30 天 PV %d，UV %d，IP %d，请求 %d", len(rep.SiteNames()), t.PV, t.UV, t.IP, t.Requests)
	}
	a.finishExec(&ex, actions.StatusDone, summary)
	a.enrichVisits(ctx, &rep)
	return VisitsView{Report: rep, Source: fmt.Sprintf("server:%d", sv.ID), Title: "服务器 " + sv.Name + " 的日志", ServerID: sv.ID, CheckedAt: now()}, nil
}

// enrichVisits adds places, what is known about the IPs, risk ratings and
// warnings about the numbers.
func (a *App) enrichVisits(ctx context.Context, rep *visits.Report) {
	rep.Locate()
	facts := a.facts(ctx, rep)
	rep.Assess(facts)
	month := rep.Range(30)
	if month == nil || rep.Source != "server" {
		return
	}
	for i := range rep.Sites {
		s := &rep.Sites[i]
		sr := month.Sites[s.Name]
		if s.Name == visits.All || sr == nil {
			continue
		}
		var total, viaEdge int64
		for _, it := range sr.Top["ip"] {
			total += it.Count
			if facts[it.Value].EdgeOne {
				viaEdge += it.Count
			}
		}
		switch {
		case total > 0 && viaEdge*2 >= total:
			s.Warning = "访客 IP 大多是 EdgeOne 的节点：服务器日志没有记下真实访客 IP，这个网站的 UV、IP、地区和风险 IP 都不准。请看「EdgeOne 日志」，或者让服务器日志记下真实 IP"
		case s.Lines > 0 && s.Forwarded == 0:
			s.Warning = "日志里没有 X-Forwarded-For：如果网站经过 CDN 或反向代理，这里的访客 IP 可能是代理的地址"
		}
	}
}

var visitTopNames = map[string]string{
	"page": "受访页面", "dir": "访问目录", "referer": "来源", "ip": "访客 IP", "status": "状态码", "bot": "爬虫和程序",
	"device": "设备", "errpage": "出错的地址", "leak": "不该能访问却返回了 200 的敏感文件", "region": "访客地区（按 IP 数）", "isp": "运营商（按 IP 数）",
}

func countsText(c visits.Counts) string {
	return fmt.Sprintf("PV %d，UV %d，IP %d，请求 %d（爬虫 %d），流量 %s，4xx %d，5xx %d",
		c.PV, c.UV, c.IP, c.Requests, c.Bots, humanBytes(float64(c.Bytes)), c.E4xx, c.E5xx)
}

var riskNames = map[string]string{visits.RiskHigh: "高风险", visits.RiskMedium: "中风险", visits.RiskLow: "低风险", visits.RiskNone: "正常"}

func (a *App) toolSiteVisits(ctx context.Context, raw json.RawMessage) (string, error) {
	var arg struct {
		Source   string `json:"source"`
		ServerID int64  `json:"server_id"`
		Days     int    `json:"days"`
		Site     string `json:"site"`
		Refresh  bool   `json:"refresh"`
	}
	if err := parseArgs(raw, &arg); err != nil {
		return "", err
	}
	if arg.Source == "" {
		if arg.ServerID > 0 {
			arg.Source = fmt.Sprintf("server:%d", arg.ServerID)
		} else if a.tencentClient() != nil {
			arg.Source = "edgeone"
		} else {
			return "", fmt.Errorf("请给出 server_id，或者 source=edgeone")
		}
	}
	switch arg.Days {
	case 0:
		arg.Days = 7
	case 1, 7, 30:
	default:
		return "", fmt.Errorf("days 只能是 1（今天）、7 或 30")
	}
	v, err := a.Visits(ctx, arg.Source, arg.Refresh)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s，统计于 %s（今天是 %s，时区 %s），最近 %d 天。\n", v.Title, v.CheckedAt, v.Today, v.Zone, arg.Days)
	if v.Problem != "" {
		b.WriteString(v.Problem + "\n")
		return b.String(), nil
	}
	b.WriteString("口径：PV 是浏览器打开网页的次数（不含爬虫、静态文件、接口和出错的请求）；UV 是不同的 IP+浏览器；IP 是不同的访客 IP。\n")
	want := visits.All
	if arg.Site != "" {
		want = ""
		for _, n := range v.SiteNames() {
			if n == arg.Site || strings.Contains(n, arg.Site) {
				want = n
				break
			}
		}
		if want == "" {
			return "", fmt.Errorf("%s 里没有 %s，有这些网站：%s", v.Title, arg.Site, strings.Join(v.SiteNames(), "、"))
		}
	}
	rg := v.Range(arg.Days)
	s := rg.Sites[want]
	if s == nil {
		s = &visits.SiteRange{}
	}
	title := want
	if want == visits.All {
		title = "全部网站"
	}
	fmt.Fprintf(&b, "\n%s合计：%s\n", title, countsText(s.Total))
	if arg.Days > 1 {
		b.WriteString("每天：\n")
		days := v.Days[want]
		for _, d := range days[max(0, len(days)-arg.Days):] {
			fmt.Fprintf(&b, "- %s %s\n", d.Date, countsText(d.Counts))
		}
	} else {
		var hours []string
		for _, h := range v.Hours[want] {
			if h.Requests > 0 {
				hours = append(hours, fmt.Sprintf("%d点 PV %d/请求 %d", h.Hour, h.PV, h.Requests))
			}
		}
		fmt.Fprintf(&b, "今天每小时：%s\n", strings.Join(hours, "；"))
	}
	if want == visits.All {
		b.WriteString("\n各网站：\n")
		for _, n := range v.SiteNames() {
			if x := rg.Sites[n]; x != nil {
				fmt.Fprintf(&b, "- %s：%s\n", n, countsText(x.Total))
			}
		}
	}
	for _, info := range v.Sites {
		if info.Warning != "" && (want == visits.All || info.Name == want) {
			fmt.Fprintf(&b, "注意 %s：%s\n", info.Name, info.Warning)
		}
	}
	for _, k := range visits.Kinds {
		items := s.Top[k]
		if len(items) == 0 {
			continue
		}
		var parts []string
		for i, it := range items {
			if i == 12 {
				break
			}
			p := fmt.Sprintf("%s %d", it.Value, it.Count)
			if it.Note != "" {
				p += "（" + it.Note + "）"
			}
			parts = append(parts, p)
		}
		fmt.Fprintf(&b, "%s：%s\n", visitTopNames[k], strings.Join(parts, "；"))
	}
	b.WriteString("\n值得注意的 IP（按风险排序，最多 15 个）：\n")
	shown := 0
	for _, p := range rg.IPs {
		if want != visits.All && !visitedSite(p, want) {
			continue
		}
		if shown == 15 {
			break
		}
		shown++
		var paths []string
		for _, it := range p.TopPaths {
			paths = append(paths, fmt.Sprintf("%s %d", it.Value, it.Count))
		}
		fmt.Fprintf(&b, "- %s [%s %d 分] %s %s：请求 %d，PV %d，404/403 %d，POST %d，%d 个不同地址，一分钟最多 %d 次，%s 到 %s；常访问 %s；UA %s",
			p.IP, riskNames[p.Risk], p.Score, p.Place, p.ISP, p.Requests, p.PV, p.E4xx, p.Posts, p.Paths, p.PeakMin, p.First, p.Last,
			strings.Join(paths, "、"), clipText(p.UA, 120))
		if len(p.Reasons) > 0 {
			fmt.Fprintf(&b, "；判断：%s", strings.Join(p.Reasons, "；"))
		}
		b.WriteString("\n")
	}
	if shown == 0 {
		b.WriteString("（没有）\n")
	}
	for _, n := range v.Notes {
		b.WriteString(n + "\n")
	}
	return b.String(), nil
}

func visitedSite(p visits.IPProfile, site string) bool {
	for _, s := range p.Sites {
		if s.Value == site {
			return true
		}
	}
	return false
}
