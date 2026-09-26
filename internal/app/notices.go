package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/visits"
)

// Notices are Miao Panel telling the user something without being asked:
// a daily report of the websites, and alerts when something needs a look
// (a new high-risk IP, a secret file downloaded, a certificate about to
// expire, what automatic blocking did). They are kept on the 通知 page and,
// when a webhook is set, pushed to a group robot or a push service.

// NoticeSettings says which notices are made. Webhook is shown masked.
type NoticeSettings struct {
	Daily      bool   `json:"daily"`
	DailyAt    string `json:"dailyAt"` // "09:00", local time
	AlertRisk  bool   `json:"alertRisk"`
	AlertLeak  bool   `json:"alertLeak"`
	AlertCert  bool   `json:"alertCert"`
	AlertBlock bool   `json:"alertBlock"`

	Webhook     string `json:"webhook"`     // masked; empty when none
	WebhookKind string `json:"webhookKind"` // in words
	HasSecret   bool   `json:"hasSecret"`
}

// Notice is one message.
type Notice struct {
	ID    int64  `json:"id"`
	At    string `json:"at"`
	Kind  string `json:"kind"` // report, alert, test
	Title string `json:"title"`
	Text  string `json:"text"` // simple Markdown
	Read  bool   `json:"read"`
	Push  string `json:"push,omitempty"` // "" not pushed, "ok", or why it failed
}

// NoticesView is the 通知 page.
type NoticesView struct {
	Settings NoticeSettings `json:"settings"`
	Notices  []Notice       `json:"notices"`
	Unread   int            `json:"unread"`
}

type noticeState struct {
	Settings  NoticeSettings    `json:"settings"`
	Notices   []Notice          `json:"notices"`
	NextID    int64             `json:"nextId"`
	Seen      map[string]string `json:"seen"`      // alert key -> when it was sent
	LastDaily string            `json:"lastDaily"` // date of the last daily report
	LastEvent string            `json:"lastEvent"` // the last automatic blocking event alerted
}

const (
	noticesKey       = "notices"
	noticeWebhookKey = "notice_webhook"
	noticeSecretKey  = "notice_webhook_secret"
	noticesKept      = 100
	realertAfter     = 7 * 24 * time.Hour // an unchanged problem is mentioned again after a week
	riskRealert      = 24 * time.Hour
)

var noticeMu sync.Mutex

func defaultNoticeSettings() NoticeSettings {
	return NoticeSettings{Daily: true, DailyAt: "09:00", AlertRisk: true, AlertLeak: true, AlertCert: true, AlertBlock: true}
}

func (a *App) loadNotices() noticeState {
	st := noticeState{Settings: defaultNoticeSettings(), NextID: 1}
	if v, _ := a.Store.Setting(noticesKey); v != "" {
		_ = json.Unmarshal([]byte(v), &st)
	}
	if st.Seen == nil {
		st.Seen = map[string]string{}
	}
	if st.Notices == nil {
		st.Notices = []Notice{}
	}
	return st
}

func (a *App) saveNotices(st noticeState) error {
	if len(st.Notices) > noticesKept {
		st.Notices = st.Notices[len(st.Notices)-noticesKept:]
	}
	for k, at := range st.Seen {
		if t, err := time.Parse(time.RFC3339, at); err != nil || time.Since(t) > 2*realertAfter {
			delete(st.Seen, k)
		}
	}
	st.Settings.Webhook, st.Settings.WebhookKind, st.Settings.HasSecret = "", "", false
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return a.Store.SetSetting(noticesKey, string(data))
}

func (a *App) webhook() (hook, secret string) {
	hook, _ = a.Secrets.Get(noticeWebhookKey)
	secret, _ = a.Secrets.Get(noticeSecretKey)
	return hook, secret
}

func (a *App) noticeSettings(st noticeState) NoticeSettings {
	s := st.Settings
	hook, secret := a.webhook()
	if hook != "" {
		s.Webhook, s.WebhookKind, s.HasSecret = maskURL(hook), hookNames[webhookKind(hook)], secret != ""
	}
	return s
}

// Notices returns the 通知 page, newest first.
func (a *App) Notices() NoticesView {
	noticeMu.Lock()
	st := a.loadNotices()
	noticeMu.Unlock()
	v := NoticesView{Settings: a.noticeSettings(st), Notices: make([]Notice, 0, len(st.Notices))}
	for i := len(st.Notices) - 1; i >= 0; i-- {
		v.Notices = append(v.Notices, st.Notices[i])
		if !st.Notices[i].Read {
			v.Unread++
		}
	}
	return v
}

// UnreadNotices counts the notices not seen yet.
func (a *App) UnreadNotices() int {
	noticeMu.Lock()
	defer noticeMu.Unlock()
	n := 0
	for _, x := range a.loadNotices().Notices {
		if !x.Read {
			n++
		}
	}
	return n
}

// MarkNoticesRead marks every notice read.
func (a *App) MarkNoticesRead() {
	noticeMu.Lock()
	defer noticeMu.Unlock()
	st := a.loadNotices()
	for i := range st.Notices {
		st.Notices[i].Read = true
	}
	_ = a.saveNotices(st)
}

// SaveNoticeSettings changes which notices are made (not the webhook).
func (a *App) SaveNoticeSettings(s NoticeSettings) (NoticesView, error) {
	if _, err := time.Parse("15:04", s.DailyAt); err != nil {
		return NoticesView{}, userErr("日报时间要写成 09:00 这样")
	}
	noticeMu.Lock()
	st := a.loadNotices()
	st.Settings = NoticeSettings{Daily: s.Daily, DailyAt: s.DailyAt, AlertRisk: s.AlertRisk, AlertLeak: s.AlertLeak, AlertCert: s.AlertCert, AlertBlock: s.AlertBlock}
	err := a.saveNotices(st)
	noticeMu.Unlock()
	if err != nil {
		return NoticesView{}, err
	}
	_ = a.Store.Audit("user", "settings.notices", fmt.Sprintf("日报 %v %s", s.Daily, s.DailyAt), "")
	return a.Notices(), nil
}

// SaveWebhook sets where notices are pushed; an empty address stops it.
// secret is the signing secret of a DingTalk or Feishu robot, if set.
func (a *App) SaveWebhook(hook, secret string) (NoticesView, error) {
	hook, secret = strings.TrimSpace(hook), strings.TrimSpace(secret)
	if hook == "" {
		_ = a.Secrets.Delete(noticeWebhookKey)
		_ = a.Secrets.Delete(noticeSecretKey)
		_ = a.Store.Audit("user", "settings.webhook", "清除", "")
		return a.Notices(), nil
	}
	u, err := url.Parse(hook)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return NoticesView{}, userErr("推送地址要以 https:// 开头，从机器人或推送服务的设置里复制")
	}
	if err := a.Secrets.Set(noticeWebhookKey, hook); err != nil {
		return NoticesView{}, err
	}
	if secret == "" {
		_ = a.Secrets.Delete(noticeSecretKey)
	} else if err := a.Secrets.Set(noticeSecretKey, secret); err != nil {
		return NoticesView{}, err
	}
	_ = a.Store.Audit("user", "settings.webhook", hookNames[webhookKind(hook)], maskURL(hook))
	return a.Notices(), nil
}

var hookClient = &http.Client{Timeout: 20 * time.Second}

// TestWebhook sends a test message.
func (a *App) TestWebhook(ctx context.Context) error {
	hook, secret := a.webhook()
	if hook == "" {
		return userErr("还没有填写推送地址")
	}
	if err := sendWebhook(ctx, hookClient, hook, secret, "Miao Panel 测试消息", "推送设置好了，以后的日报和提醒会发到这里。"); err != nil {
		return userErr("%v", err)
	}
	return nil
}

// addNotice keeps a notice and pushes it when a webhook is set.
func (a *App) addNotice(ctx context.Context, kind, title, text string) Notice {
	n := Notice{Kind: kind, At: now(), Title: title, Text: text}
	if hook, secret := a.webhook(); hook != "" {
		if err := sendWebhook(ctx, hookClient, hook, secret, title, text); err != nil {
			n.Push = err.Error()
		} else {
			n.Push = "ok"
		}
	}
	noticeMu.Lock()
	defer noticeMu.Unlock()
	st := a.loadNotices()
	n.ID = st.NextID
	st.NextID++
	st.Notices = append(st.Notices, n)
	_ = a.saveNotices(st)
	return n
}

// checkNotices runs after each background refresh: alerts for what is new,
// and the daily report once its time has come.
func (a *App) checkNotices(ctx context.Context) {
	a.alert(ctx)
	noticeMu.Lock()
	st := a.loadNotices()
	noticeMu.Unlock()
	s := st.Settings
	nowT := time.Now()
	at, err := time.ParseInLocation("15:04", s.DailyAt, time.Local)
	today := nowT.Format("2006-01-02")
	if !s.Daily || err != nil || st.LastDaily == today || nowT.Hour()*60+nowT.Minute() < at.Hour()*60+at.Minute() {
		return
	}
	noticeMu.Lock()
	st = a.loadNotices()
	st.LastDaily = today
	_ = a.saveNotices(st)
	noticeMu.Unlock()
	a.DailyReport(ctx)
}

// alert sends one notice listing what is new since the last check.
func (a *App) alert(ctx context.Context) {
	noticeMu.Lock()
	st := a.loadNotices()
	noticeMu.Unlock()
	s := st.Settings
	nowT := time.Now()
	fresh := func(key string, again time.Duration) bool {
		t, err := time.Parse(time.RFC3339, st.Seen[key])
		return err != nil || nowT.Sub(t) > again
	}
	var sections []string
	var keys []string
	var titles []string

	blocked := map[string]bool{}
	if s.AlertRisk && a.tencentClient() != nil {
		if list, err := a.Blocked(ctx); err == nil {
			for _, z := range list {
				for _, ip := range z.IPs {
					blocked[ip] = true
				}
			}
		}
	}
	if s.AlertRisk || s.AlertLeak {
		sources, _ := a.VisitSources()
		var risky, leaks []string
		for _, src := range sources {
			v, err := a.LatestVisits(ctx, src.Key)
			if err != nil || v.Range(1) == nil {
				continue
			}
			if s.AlertRisk {
				for _, p := range v.Range(1).IPs {
					key := "risk:" + p.IP
					if p.Risk != visits.RiskHigh || blocked[p.IP] || p.EdgeOne || p.Crawler != "" || !fresh(key, riskRealert) {
						continue
					}
					keys = append(keys, key)
					why := ""
					if len(p.Reasons) > 0 {
						why = "：" + p.Reasons[0]
					}
					risky = append(risky, fmt.Sprintf("- %s（%s）%s", p.IP, orDash(strings.TrimSpace(p.Place+" "+p.ISP)), why))
				}
			}
			if s.AlertLeak {
				if all := v.Range(1).Sites[visits.All]; all != nil {
					for _, it := range all.Top["leak"] {
						key := "leak:" + it.Value
						if !fresh(key, realertAfter) {
							continue
						}
						keys = append(keys, key)
						leaks = append(leaks, fmt.Sprintf("- %s（%d 次，%s）", strings.TrimPrefix(it.Value, "200 "), it.Count, src.Title))
					}
				}
			}
		}
		if len(risky) > 0 {
			if len(risky) > 10 {
				risky = append(risky[:10], fmt.Sprintf("- 还有 %d 个", len(risky)-10))
			}
			titles = append(titles, fmt.Sprintf("%d 个新的高风险 IP", len(keysWith(keys, "risk:"))))
			sections = append(sections, "**新的高风险 IP**（还没有封禁，可以在「网站统计 → 安全」里封禁）\n"+strings.Join(risky, "\n"))
		}
		if len(leaks) > 0 {
			titles = append(titles, "敏感文件被下载")
			sections = append(sections, "**敏感文件被下载**（里面的密钥或代码可能已经泄露，请删除或禁止访问，并更换其中的密码）\n"+strings.Join(leaks, "\n"))
		}
	}
	if s.AlertCert {
		if ov, err := a.LatestCertificates(ctx); err == nil {
			var lines []string
			for _, g := range ov.Groups {
				for _, c := range g.Certs {
					if !(c.Level == "crit" || c.Level == "warn") || !(c.InUse || !anyInUse(g.Certs)) {
						continue
					}
					key := "cert:" + g.Domain + ":" + c.NotAfter + ":" + c.Level
					if !fresh(key, realertAfter) {
						continue
					}
					keys = append(keys, key)
					lines = append(lines, fmt.Sprintf("- %s：%s", g.Domain, c.Status))
				}
			}
			for _, l := range ov.Live {
				key := "live:" + l.Domain + ":" + l.Status
				if l.Level != "crit" || !fresh(key, realertAfter) {
					continue
				}
				keys = append(keys, key)
				lines = append(lines, fmt.Sprintf("- https://%s：%s", l.Domain, l.Status))
			}
			if len(lines) > 0 {
				titles = append(titles, "证书需要处理")
				sections = append(sections, "**证书**（在「证书」页可以让 AI 续签或处理）\n"+strings.Join(lines, "\n"))
			}
		}
	}
	lastEvent := st.LastEvent
	if s.AlertBlock {
		var lines []string
		for _, e := range a.AutoBlock().Events {
			if e.At > st.LastEvent {
				lines = append(lines, "- "+e.Text)
				lastEvent = e.At
			}
		}
		if len(lines) > 0 {
			titles = append(titles, "自动封禁")
			sections = append(sections, "**自动封禁**\n"+strings.Join(lines, "\n"))
		}
	}
	noticeMu.Lock()
	st = a.loadNotices()
	for _, k := range keys {
		st.Seen[k] = nowT.UTC().Format(time.RFC3339)
	}
	st.LastEvent = lastEvent
	_ = a.saveNotices(st)
	noticeMu.Unlock()
	if len(sections) > 0 {
		a.addNotice(ctx, "alert", "提醒："+strings.Join(titles, "、"), strings.Join(sections, "\n\n"))
	}
}

func keysWith(keys []string, prefix string) []string {
	var out []string
	for _, k := range keys {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out
}

func anyInUse(certs []Cert) bool {
	for _, c := range certs {
		if c.InUse {
			return true
		}
	}
	return false
}

var weekdays = []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

// DailyReport makes the report of yesterday now and returns it.
func (a *App) DailyReport(ctx context.Context) Notice {
	day := time.Now().AddDate(0, 0, -1)
	title := fmt.Sprintf("日报 · %d月%d日（%s）", day.Month(), day.Day(), weekdays[day.Weekday()])
	text := a.reportText(ctx, day)
	return a.addNotice(ctx, "report", title, text)
}

func (a *App) reportText(ctx context.Context, day time.Time) string {
	var parts []string
	date := day.Format("2006-01-02")
	sources, _ := a.VisitSources()
	for _, src := range sources {
		v, err := a.LatestVisits(ctx, src.Key)
		if err != nil {
			parts = append(parts, fmt.Sprintf("**%s**\n- 统计失败：%v", src.Title, err))
			continue
		}
		parts = append(parts, visitsReport(v, src.Title, date))
	}
	if ov, err := a.LatestCertificates(ctx); err == nil && len(ov.Groups) > 0 {
		var bad, soon []string
		for _, g := range ov.Groups {
			for _, c := range g.Certs {
				switch {
				case (c.Level == "crit" || c.Level == "warn") && (c.InUse || !anyInUse(g.Certs)):
					bad = append(bad, fmt.Sprintf("- %s：%s", g.Domain, c.Status))
				case c.InUse && c.AutoRenew && c.DaysLeft != nil && *c.DaysLeft < 30:
					soon = append(soon, g.Domain)
				}
			}
		}
		lines := []string{"**证书**"}
		if len(bad) == 0 {
			lines = append(lines, "- 在用的证书都没有问题")
		}
		lines = append(lines, bad...)
		if len(soon) > 0 {
			lines = append(lines, fmt.Sprintf("- %s 30 天内到期，会自动续签", strings.Join(soon, "、")))
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	if len(parts) == 0 {
		return "还没有可以统计的网站：添加服务器或者填写腾讯云密钥后，这里会有每天的访问情况。"
	}
	return strings.Join(parts, "\n\n")
}

// visitsReport is one source's day: the numbers and how they changed, the
// busiest sites and pages, and what needs a look.
func visitsReport(v VisitsView, name, date string) string {
	lines := []string{"**" + name + "**"}
	days := v.Days[visits.All]
	idx := -1
	for i, d := range days {
		if d.Date == date {
			idx = i
		}
	}
	if idx < 0 {
		return strings.Join(append(lines, "- 这一天没有访问记录"), "\n")
	}
	d := days[idx].Counts
	cmp := func(prev int) string {
		if prev < 0 || days[prev].PV == 0 {
			return ""
		}
		return pctChange(d.PV, days[prev].PV)
	}
	var ch []string
	if c := cmp(idx - 1); c != "" {
		ch = append(ch, "比前一天 "+c)
	}
	if c := cmp(idx - 7); c != "" {
		ch = append(ch, "比上周同一天 "+c)
	}
	pv := "PV " + fmtInt(d.PV)
	if len(ch) > 0 {
		pv += "（" + strings.Join(ch, "，") + "）"
	}
	errs := "—"
	if d.Requests > 0 {
		errs = fmt.Sprintf("%.1f%%", float64(d.E4xx+d.E5xx)*100/float64(d.Requests))
	}
	lines = append(lines, fmt.Sprintf("- %s，UV %s，IP %s，请求 %s，流量 %s，错误率 %s", pv, fmtInt(d.UV), fmtInt(d.IP), fmtInt(d.Requests), bytesText(d.Bytes), errs))

	type sitePV struct {
		name string
		pv   int64
	}
	var sites []sitePV
	for n, list := range v.Days {
		if n == visits.All {
			continue
		}
		for _, x := range list {
			if x.Date == date && x.PV > 0 {
				sites = append(sites, sitePV{n, x.PV})
			}
		}
	}
	sort.Slice(sites, func(i, j int) bool {
		return sites[i].pv > sites[j].pv || sites[i].pv == sites[j].pv && sites[i].name < sites[j].name
	})
	if len(sites) > 1 {
		var s []string
		for i, x := range sites {
			if i == 4 {
				break
			}
			s = append(s, fmt.Sprintf("%s %s", x.name, fmtInt(x.pv)))
		}
		lines = append(lines, "- 各网站 PV："+strings.Join(s, "，"))
	}
	if week := v.Range(7); week != nil {
		if all := week.Sites[visits.All]; all != nil {
			if pages := topValues(all.Top["page"], 3); pages != "" {
				lines = append(lines, "- 近 7 天热门页面："+pages)
			}
			if regions := topValues(all.Top["region"], 3); regions != "" {
				lines = append(lines, "- 近 7 天访客地区："+regions)
			}
			if dead := all.Top["dead"]; len(dead) > 0 {
				lines = append(lines, fmt.Sprintf("- 死链 %d 个，比如 %s", len(dead), dead[0].Value))
			}
			for _, it := range all.Top["leak"] {
				lines = append(lines, "- ⚠ 敏感文件被下载："+strings.TrimPrefix(it.Value, "200 "))
			}
		}
		high := 0
		for _, p := range week.IPs {
			if p.Risk == visits.RiskHigh && p.Last >= date {
				high++
			}
		}
		if high > 0 {
			lines = append(lines, fmt.Sprintf("- 高风险 IP %d 个（从这一天起有活动的）", high))
		}
	}
	return strings.Join(lines, "\n")
}

func topValues(items []visits.Item, n int) string {
	var out []string
	for i, it := range items {
		if i == n {
			break
		}
		out = append(out, fmt.Sprintf("%s %s", it.Value, fmtInt(it.Count)))
	}
	return strings.Join(out, "、")
}

func pctChange(now, before int64) string {
	d := float64(now-before) * 100 / float64(before)
	switch {
	case d >= 0.5:
		return fmt.Sprintf("↑%.0f%%", d)
	case d <= -0.5:
		return fmt.Sprintf("↓%.0f%%", -d)
	}
	return "持平"
}

// fmtInt writes 12345 as 12,345.
func fmtInt(n int64) string {
	s := fmt.Sprint(n)
	if n < 0 {
		return "-" + fmtInt(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func bytesText(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}
