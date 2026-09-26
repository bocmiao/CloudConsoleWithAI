package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/internal/visits"
	"github.com/bocmiao/CloudConsoleWithAI/scripts"
)

// SiteVisitsView is one server's website visits, counted from its access
// logs by sitelogs.sh.
type SiteVisitsView struct {
	visits.Report
	ServerID   int64  `json:"serverId"`
	ServerName string `json:"serverName"`
	CheckedAt  string `json:"checkedAt"`
	// Refreshing: this is an older report and a new one is on its way.
	Refreshing bool `json:"refreshing,omitempty"`
}

// visitsTTL is how long a report counts as current. Counting reads the
// logs on the server, so it is not repeated more often than this.
const visitsTTL = 3 * time.Minute

const maxVisitsOutput = 512 << 10

// visitsCache keeps the latest report per server and range, and the run
// in progress, like certCache.
type visitsCache struct {
	mu      sync.Mutex
	entries map[string]*visitsEntry
}

type visitsEntry struct {
	v       SiteVisitsView
	loaded  bool
	at      time.Time
	err     error
	running chan struct{}
}

func visitsKey(id int64, days int) string { return fmt.Sprintf("visits_%d_%d", id, days) }

func (a *App) visitsEntry(id int64, days int) *visitsEntry {
	if a.visits.entries == nil {
		a.visits.entries = map[string]*visitsEntry{}
	}
	key := visitsKey(id, days)
	e := a.visits.entries[key]
	if e == nil {
		e = &visitsEntry{}
		a.visits.entries[key] = e
	}
	if !e.loaded {
		if raw, err := a.Store.Setting(key); err == nil && raw != "" {
			var v SiteVisitsView
			if json.Unmarshal([]byte(raw), &v) == nil && v.CheckedAt != "" {
				e.v, e.loaded = v, true
			}
		}
	}
	return e
}

func checkVisitDays(days int) error {
	if days < 1 || days > 31 {
		return userErr("统计天数要在 1 到 31 之间")
	}
	return nil
}

// SiteVisits returns a current report: one from the last few minutes
// unless refresh, or else a new one.
func (a *App) SiteVisits(ctx context.Context, id int64, days int, refresh bool) (SiteVisitsView, error) {
	if err := checkVisitDays(days); err != nil {
		return SiteVisitsView{}, err
	}
	if _, err := a.Store.GetServer(id); err != nil {
		return SiteVisitsView{}, userErr("找不到这台服务器（编号 %d）", id)
	}
	a.visits.mu.Lock()
	e := a.visitsEntry(id, days)
	if !refresh && e.running == nil && !e.at.IsZero() && time.Since(e.at) < visitsTTL {
		defer a.visits.mu.Unlock()
		return e.v, nil
	}
	done := a.refreshVisitsLocked(ctx, e, id, days)
	a.visits.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		return SiteVisitsView{}, ctx.Err()
	}
	a.visits.mu.Lock()
	defer a.visits.mu.Unlock()
	return e.v, e.err
}

// LatestSiteVisits answers at once with the last report, even one saved
// before Miao Panel restarted, refreshing it in the background when it is
// out of date (Refreshing says so). Only with nothing to show does it wait.
func (a *App) LatestSiteVisits(ctx context.Context, id int64, days int) (SiteVisitsView, error) {
	if err := checkVisitDays(days); err != nil {
		return SiteVisitsView{}, err
	}
	if _, err := a.Store.GetServer(id); err != nil {
		return SiteVisitsView{}, userErr("找不到这台服务器（编号 %d）", id)
	}
	a.visits.mu.Lock()
	e := a.visitsEntry(id, days)
	if !e.loaded {
		a.visits.mu.Unlock()
		return a.SiteVisits(ctx, id, days, false)
	}
	defer a.visits.mu.Unlock()
	v := e.v
	if e.running != nil || e.at.IsZero() || time.Since(e.at) >= visitsTTL {
		a.refreshVisitsLocked(ctx, e, id, days)
		v.Refreshing = true
	}
	return v, nil
}

func (a *App) refreshVisitsLocked(ctx context.Context, e *visitsEntry, id int64, days int) chan struct{} {
	if e.running != nil {
		return e.running
	}
	done := make(chan struct{})
	e.running = done
	origin := originOf(ctx)
	go func() {
		ctx, cancel := context.WithTimeout(withOrigin(context.Background(), origin), 3*time.Minute)
		defer cancel()
		started := time.Now()
		v, err := a.gatherVisits(ctx, id, days)
		a.visits.mu.Lock()
		defer a.visits.mu.Unlock()
		e.err = err
		if err == nil {
			e.v, e.loaded, e.at = v, true, started
			if raw, err := json.Marshal(v); err == nil {
				_ = a.Store.SetSetting(visitsKey(id, days), string(raw))
			}
		}
		e.running = nil
		close(done)
	}()
	return done
}

// gatherVisits runs sitelogs.sh on the server and logs the run.
func (a *App) gatherVisits(ctx context.Context, id int64, days int) (SiteVisitsView, error) {
	sv, c, err := a.connect(ctx, id)
	if err != nil {
		return SiteVisitsView{}, err
	}
	defer c.Close()
	ex := a.startExec(store.ExecLog{ServerID: sv.ID, ServerName: sv.Name, Adapter: sv.Adapter, Origin: originOf(ctx),
		Kind: store.ExecRead, Title: fmt.Sprintf("统计网站访问日志（%d 天）", days), Via: "只读脚本", ScriptName: "sitelogs.sh"})
	res, err := c.RunScript(ctx, sv.Username, scripts.SiteLogs, []string{fmt.Sprint(days)}, maxVisitsOutput)
	ex.Commands = "# 把只读统计脚本 sitelogs.sh 通过标准输入交给服务器执行（全文见「脚本」）\n" + res.Command
	switch {
	case err != nil:
		a.finishExec(&ex, actions.StatusFailed, err.Error())
		return SiteVisitsView{}, friendlySSHError(err)
	case strings.TrimSpace(res.Stdout) == "":
		msg := fmt.Sprintf("没有输出（退出码 %d）：%s", res.ExitCode, strings.TrimSpace(res.Stderr))
		a.finishExec(&ex, actions.StatusFailed, msg)
		return SiteVisitsView{}, userErr("统计访问日志失败：%s", msg)
	}
	rep := visits.Parse(res.Stdout)
	summary := fmt.Sprintf("%d 个网站", max(0, len(rep.Sites)-1))
	if all, ok := rep.Site(visits.All); ok {
		summary += fmt.Sprintf("，PV %d，UV %d，IP %d，请求 %d", all.Total.PV, all.Total.UV, all.Total.IP, all.Total.Requests)
	}
	if rep.Problem != "" {
		summary = rep.Problem
	}
	a.finishExec(&ex, actions.StatusDone, summary)
	return SiteVisitsView{Report: rep, ServerID: sv.ID, ServerName: sv.Name, CheckedAt: now()}, nil
}

var visitTopNames = map[string]string{"page": "受访页面", "referer": "来源", "ip": "访客 IP", "status": "状态码", "bot": "爬虫和程序", "device": "设备"}

func countsText(c visits.Counts) string {
	return fmt.Sprintf("PV %d，UV %d，IP %d，请求 %d（爬虫 %d），流量 %s，4xx %d，5xx %d",
		c.PV, c.UV, c.IP, c.Requests, c.Bots, humanBytes(float64(c.Bytes)), c.E4xx, c.E5xx)
}

func (a *App) toolSiteVisits(ctx context.Context, raw json.RawMessage) (string, error) {
	var arg struct {
		ServerID int64  `json:"server_id"`
		Days     int    `json:"days"`
		Site     string `json:"site"`
		Refresh  bool   `json:"refresh"`
	}
	if err := parseArgs(raw, &arg); err != nil {
		return "", err
	}
	if arg.Days == 0 {
		arg.Days = 7
	}
	v, err := a.SiteVisits(ctx, arg.ServerID, arg.Days, arg.Refresh)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "服务器 %s，最近 %d 天（按服务器时间，今天是 %s，时区 %s），统计于 %s。\n", v.ServerName, v.Days, v.Today, v.Zone, v.CheckedAt)
	if v.Problem != "" {
		b.WriteString(v.Problem + "\n")
		return b.String(), nil
	}
	b.WriteString("口径：PV 是浏览器打开网页的次数（不含爬虫、静态文件、接口和错误）；UV 是不同的 IP+浏览器；IP 是不同的访客 IP。\n")
	want := visits.All
	if arg.Site != "" {
		want = ""
		for _, s := range v.Sites {
			if s.Name == arg.Site || strings.Contains(s.Name, arg.Site) {
				want = s.Name
				break
			}
		}
		if want == "" {
			var names []string
			for _, s := range v.Sites {
				if s.Name != visits.All {
					names = append(names, s.Name)
				}
			}
			return "", fmt.Errorf("这台服务器的访问日志里没有 %s，有这些网站：%s", arg.Site, strings.Join(names, "、"))
		}
	}
	s, _ := v.Site(want)
	title := s.Name
	if want == visits.All {
		title = "全部网站"
	}
	fmt.Fprintf(&b, "\n%s合计：%s\n每天：\n", title, countsText(s.Total))
	for _, d := range s.Days {
		fmt.Fprintf(&b, "- %s %s\n", d.Date, countsText(d.Counts))
	}
	if want == visits.All {
		b.WriteString("\n各网站：\n")
		for _, x := range v.Sites {
			if x.Name != visits.All {
				fmt.Fprintf(&b, "- %s：%s\n", x.Name, countsText(x.Total))
			}
		}
	}
	for _, k := range visits.Kinds {
		items := s.Top[k]
		if len(items) == 0 {
			continue
		}
		var parts []string
		for i, it := range items {
			if i == 15 {
				break
			}
			parts = append(parts, fmt.Sprintf("%s %d", it.Value, it.Count))
		}
		fmt.Fprintf(&b, "%s：%s\n", visitTopNames[k], strings.Join(parts, "；"))
	}
	if s.Lines > 0 && s.Forwarded == 0 {
		b.WriteString("注意：日志里没有 X-Forwarded-For，如果网站经过 EdgeOne 或 CDN，访客 IP 可能都是节点 IP，UV 和 IP 会偏少。\n")
	}
	if s.Unparsed > 0 {
		fmt.Fprintf(&b, "有 %d 行日志格式无法识别，没有计入。\n", s.Unparsed)
	}
	return b.String(), nil
}
