package app

import (
	"context"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/visits"
)

// Checklist steps that change Miao Panel's own settings: what is
// monitored, automatic blocking and which notices are made. Each step
// remembers the old value of only what it changed, so undoing it leaves
// later changes alone.

type localSettings struct{ a *App }

// baseEnv is what steps that do not work on a server run with.
func (a *App) baseEnv() *actions.Env {
	return &actions.Env{Cloud: a.tencentClient(), Aliyun: a.aliyunClient(), Local: localSettings{a}, PollInterval: a.PollInterval}
}

func (l localSettings) ApplyLocal(_ context.Context, op string, v map[string]string) (map[string]string, []string, error) {
	return l.a.editLocal(op, v)
}

func (l localSettings) UndoLocal(_ context.Context, op string, undo map[string]string) ([]string, error) {
	_, log, err := l.a.editLocal(op, undo)
	return log, err
}

func (a *App) editLocal(op string, v map[string]string) (map[string]string, []string, error) {
	switch op {
	case "monitor":
		return a.editMonitor(v)
	case "autoblock":
		return a.editAutoBlock(v)
	case "notices":
		return a.editNotices(v)
	}
	return nil, nil, fmt.Errorf("未知的设置 %s", op)
}

// settingsEdit applies parameter values to settings fields and keeps the
// old value of each field it changed, under the same name: applying the
// undo map the same way puts them back.
type settingsEdit struct {
	v, undo map[string]string
	log     []string
}

func newEdit(v map[string]string) *settingsEdit {
	return &settingsEdit{v: v, undo: map[string]string{}}
}

func (e *settingsEdit) flag(key, label string, f *bool) {
	s := e.v[key]
	if s != "on" && s != "off" {
		return
	}
	on := s == "on"
	if *f == on {
		e.log = append(e.log, label+"本来就是"+kaiGuan(on))
		return
	}
	e.undo[key] = onOff(*f)
	*f = on
	e.log = append(e.log, fmt.Sprintf("%s：%s → %s", label, kaiGuan(!on), kaiGuan(on)))
}

func (e *settingsEdit) num(key, label string, f *int, show func(int) string) {
	s := e.v[key]
	if s == "" {
		return
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return
	}
	if *f == n {
		e.log = append(e.log, label+"本来就是"+show(n))
		return
	}
	e.undo[key] = strconv.Itoa(*f)
	e.log = append(e.log, fmt.Sprintf("%s：%s → %s", label, show(*f), show(n)))
	*f = n
}

// text sets a string field; none stands for empty.
func (e *settingsEdit) text(key, label string, f *string, show func(string) string) {
	s := e.v[key]
	if s == "" {
		return
	}
	if s == "none" {
		s = ""
	}
	if *f == s {
		e.log = append(e.log, label+"本来就是"+show(s))
		return
	}
	e.undo[key] = *f
	if *f == "" {
		e.undo[key] = "none"
	}
	e.log = append(e.log, fmt.Sprintf("%s：%s → %s", label, show(*f), show(s)))
	*f = s
}

// list adds and removes entries of a list field. The undo map gets
// key+"_added" and key+"_removed", which apply the other way round.
func (e *settingsEdit) list(key, label string, f *[]string, add, remove []string) {
	var added, removed []string
	for _, x := range add {
		if !contains(*f, x) {
			*f = append(*f, x)
			added = append(added, x)
		}
	}
	for _, x := range remove {
		if contains(*f, x) {
			*f = without(*f, x)
			removed = append(removed, x)
		}
	}
	if len(added) > 0 {
		e.undo[key+"_removed"] = strings.Join(added, "\n")
		e.log = append(e.log, label+"加入："+strings.Join(added, "、"))
	}
	if len(removed) > 0 {
		e.undo[key+"_added"] = strings.Join(removed, "\n")
		e.log = append(e.log, label+"移出："+strings.Join(removed, "、"))
	}
}

func (e *settingsEdit) listOf(key string) []string { return strings.Fields(e.v[key]) }

func without(list []string, v string) []string {
	out := []string{}
	for _, x := range list {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

func (e *settingsEdit) result() []string {
	if len(e.log) == 0 {
		e.log = []string{"没有需要修改的设置"}
	}
	return e.log
}

func percent(n int) string { return strconv.Itoa(n) + "%" }

func (a *App) editMonitor(v map[string]string) (map[string]string, []string, error) {
	s := a.MonitorSettings()
	e := newEdit(v)
	e.flag("enabled", "监控", &s.Enabled)
	e.flag("auto", "自动监控所有网站", &s.Auto)
	e.flag("servers", "采集服务器状态", &s.Servers)
	e.num("disk_pct", "磁盘提醒线", &s.DiskPct, percent)
	e.num("mem_pct", "内存提醒线", &s.MemPct, percent)
	e.num("cpu_pct", "CPU 提醒线", &s.CPUPct, percent)
	e.text("email", "告警邮箱", &s.Email, func(s string) string { return orDash(s) })
	norm := func(list []string) ([]string, error) {
		out := []string{}
		for _, x := range list {
			u, err := normTarget(x)
			if err != nil {
				return nil, err
			}
			out = append(out, u)
		}
		return out, nil
	}
	watch, err := norm(e.listOf("watch"))
	if err != nil {
		return nil, nil, err
	}
	unwatch, err := norm(e.listOf("unwatch"))
	if err != nil {
		return nil, nil, err
	}
	// An address no longer watched goes on the skip list when it was not
	// one added by hand: it was found on its own and would come back.
	var skip []string
	for _, u := range unwatch {
		if !contains(s.Extra, u) {
			skip = append(skip, u)
		}
	}
	e.list("extra", "监控的网址", &s.Extra, watch, unwatch)
	e.list("skip", "不监控的网址", &s.Skip, skip, watch)
	// The undo map's lists.
	e.list("extra", "监控的网址", &s.Extra, e.listOf("extra_added"), e.listOf("extra_removed"))
	e.list("skip", "不监控的网址", &s.Skip, e.listOf("skip_added"), e.listOf("skip_removed"))
	if _, err := a.SaveMonitorSettings(s); err != nil {
		return nil, nil, err
	}
	return e.undo, e.result(), nil
}

func (a *App) editAutoBlock(v map[string]string) (map[string]string, []string, error) {
	s := a.AutoBlock().Settings
	e := newEdit(v)
	e.flag("enabled", "自动封禁", &s.Enabled)
	e.text("level", "封禁范围", &s.Level, func(l string) string {
		if l == visits.RiskMedium {
			return "高风险和中风险 IP"
		}
		return "高风险 IP"
	})
	e.flag("require_ai", "只封 AI 也建议封禁的", &s.RequireAI)
	e.num("hours", "封禁时长", &s.Hours, durationText)
	e.list("allow", "白名单", &s.Allow, e.listOf("allow_add"), e.listOf("allow_remove"))
	e.list("allow", "白名单", &s.Allow, e.listOf("allow_added"), e.listOf("allow_removed"))
	if _, err := a.SaveAutoBlock(s); err != nil {
		return nil, nil, err
	}
	return e.undo, e.result(), nil
}

func (a *App) editNotices(v map[string]string) (map[string]string, []string, error) {
	if t := v["daily_at"]; t != "" {
		at, err := time.Parse("15:04", t)
		if err != nil {
			return nil, nil, userErr("日报时间要写成 09:00 这样")
		}
		v = maps.Clone(v)
		v["daily_at"] = at.Format("15:04") // 9:00 is 09:00
	}
	s := a.Notices().Settings
	e := newEdit(v)
	e.flag("daily", "日报", &s.Daily)
	e.text("daily_at", "日报时间", &s.DailyAt, func(s string) string { return s })
	e.flag("alert_risk", "高风险 IP 提醒", &s.AlertRisk)
	e.flag("alert_leak", "敏感文件被下载提醒", &s.AlertLeak)
	e.flag("alert_cert", "证书到期提醒", &s.AlertCert)
	e.flag("alert_block", "自动封禁提醒", &s.AlertBlock)
	e.flag("alert_renew", "续费和余额提醒", &s.AlertRenew)
	e.flag("alert_cloud", "云监控告警提醒", &s.AlertCloud)
	if _, err := a.SaveNoticeSettings(s); err != nil {
		return nil, nil, err
	}
	return e.undo, e.result(), nil
}
