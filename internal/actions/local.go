package actions

import (
	"context"
	"fmt"
	"net"

	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
)

// Miao Panel's own settings (what is monitored, automatic blocking, which
// notices are made) change like everything else: a checklist step the
// user confirms, logged and undone. The app does the work; this package
// only checks the parameters and says what the step does.

// Local changes Miao Panel's own settings. op is the Impl's Local name;
// the undo it returns reverses only what the step changed.
type Local interface {
	ApplyLocal(ctx context.Context, op string, v map[string]string) (undo map[string]string, log []string, err error)
	UndoLocal(ctx context.Context, op string, undo map[string]string) (log []string, err error)
}

// IsLocal reports whether a capability changes Miao Panel's own settings.
func IsLocal(capability string) bool {
	c, ok := registry[capability]
	return ok && c.Impls["*"].Local != ""
}

func applyLocal(ctx context.Context, env *Env, r Resolved) Outcome {
	out := Outcome{Commands: []string{"# 修改 Miao Panel 自己的设置（" + r.Cap.Title + "），不连接服务器和云"}}
	if env.Local == nil {
		return refused(&out, "这里不能修改 Miao Panel 的设置")
	}
	undo, log, err := env.Local.ApplyLocal(ctx, r.Impl.Local, r.Values)
	out.Log = log
	if err != nil {
		out.Status = StatusFailed
		out.logf("%v", err)
		return out
	}
	out.Status, out.Undo = StatusDone, undo
	return out
}

func undoLocal(ctx context.Context, env *Env, r Resolved, undo map[string]string) Outcome {
	out := Outcome{Commands: []string{"# 把 Miao Panel 的设置改回原来的值（" + r.Cap.Title + "）"}}
	if env.Local == nil {
		return refused(&out, "这里不能修改 Miao Panel 的设置")
	}
	log, err := env.Local.UndoLocal(ctx, r.Impl.Local, undo)
	out.Log = log
	if err != nil {
		out.Status = StatusFailed
		out.logf("%v", err)
		return out
	}
	out.Status = StatusUndone
	return out
}

var onOffEnum = []string{"on", "off"}

// anySet fails when none of the named parameters is given.
func anySet(v map[string]string, names ...string) error {
	for _, n := range names {
		if v[n] != "" {
			return nil
		}
	}
	return fmt.Errorf("至少要改一项设置")
}

func init() {
	register(&Capability{
		Name: "monitor.settings.set", Title: "修改监控设置", Risk: core.R1, Reversible: true,
		Params: []Param{
			{Name: "enabled", Kind: "enum", Enum: onOffEnum, Desc: "网站和服务器监控的总开关"},
			{Name: "auto", Kind: "enum", Enum: onOffEnum, Desc: "自动监控 Miao Panel 知道的所有网站"},
			{Name: "servers", Kind: "enum", Enum: onOffEnum, Desc: "每两分钟采集服务器的 CPU、内存、磁盘和网络"},
			{Name: "disk_pct", Kind: "int", Min: 50, Max: 100, Desc: "磁盘用到百分之多少时提醒"},
			{Name: "mem_pct", Kind: "int", Min: 50, Max: 100, Desc: "内存持续 6 分钟超过百分之多少时提醒"},
			{Name: "cpu_pct", Kind: "int", Min: 50, Max: 100, Desc: "CPU 持续 10 分钟超过百分之多少时提醒"},
			{Name: "watch", Kind: "lines", Desc: "要加入监控的网址，一行一个（也会从「不监控」名单里移出）"},
			{Name: "unwatch", Kind: "lines", Desc: "不再监控的网址，一行一个（自动发现的网站会加入「不监控」名单）"},
			{Name: "email", Kind: "text", Desc: "告警同时发到这个邮箱（要先在设置里配置邮件）；填 none 表示不再发邮件"},
		},
		Impls: map[string]Impl{"*": {Via: "Miao Panel 设置", Local: "monitor", Downtime: "不影响网站和服务器",
			Undo: "把这一步改过的监控设置改回原来的值"}},
		Check: func(v map[string]string) error {
			return anySet(v, "enabled", "auto", "servers", "disk_pct", "mem_pct", "cpu_pct", "watch", "unwatch", "email")
		},
		RiskFor: func(v map[string]string) core.Risk {
			if v["enabled"] == "off" || v["servers"] == "off" {
				return core.R2 // problems would go unnoticed
			}
			return core.R1
		},
	})
	register(&Capability{
		Name: "autoblock.set", Title: "修改自动封禁规则", Risk: core.R2, Reversible: true,
		Params: []Param{
			{Name: "enabled", Kind: "enum", Enum: onOffEnum, Desc: "开启或关闭自动封禁（通过 EdgeOne，不用确认）"},
			{Name: "level", Kind: "enum", Enum: []string{"high", "medium"}, Desc: "high 只封高风险 IP；medium 封高风险和中风险 IP"},
			{Name: "require_ai", Kind: "enum", Enum: onOffEnum, Desc: "只封 AI 也建议封禁的 IP"},
			{Name: "hours", Kind: "int", Min: 0, Max: 24 * 365, Desc: "每次封禁多少小时，0 表示一直封到手动解封"},
			{Name: "allow_add", Kind: "lines", Desc: "加入白名单（永不自动封禁）的 IP 或网段，一行一个"},
			{Name: "allow_remove", Kind: "lines", Desc: "从白名单移出的 IP 或网段，一行一个"},
		},
		Impls: map[string]Impl{"*": {Via: "Miao Panel 设置", Local: "autoblock", Downtime: "不影响网站",
			Undo: "把这一步改过的自动封禁规则改回原来的值（已经封禁的 IP 按原来的时间解封）"}},
		Check: func(v map[string]string) error {
			for _, k := range []string{"allow_add", "allow_remove"} {
				for _, f := range lines(v[k]) {
					if net.ParseIP(f) == nil {
						if _, _, err := net.ParseCIDR(f); err != nil {
							return fmt.Errorf("参数 %s 里的「%s」不是 IP 地址或网段", k, f)
						}
					}
				}
			}
			return anySet(v, "enabled", "level", "require_ai", "hours", "allow_add", "allow_remove")
		},
	})
	register(&Capability{
		Name: "notice.settings.set", Title: "修改通知设置", Risk: core.R1, Reversible: true,
		Params: []Param{
			{Name: "daily", Kind: "enum", Enum: onOffEnum, Desc: "每天的网站日报"},
			{Name: "daily_at", Kind: "text", Desc: "日报时间，24 小时制，例如 09:00"},
			{Name: "alert_risk", Kind: "enum", Enum: onOffEnum, Desc: "出现新的高风险 IP 时提醒"},
			{Name: "alert_leak", Kind: "enum", Enum: onOffEnum, Desc: "敏感文件被下载时提醒"},
			{Name: "alert_cert", Kind: "enum", Enum: onOffEnum, Desc: "证书快到期时提醒"},
			{Name: "alert_block", Kind: "enum", Enum: onOffEnum, Desc: "自动封禁做了什么时提醒"},
			{Name: "alert_renew", Kind: "enum", Enum: onOffEnum, Desc: "云服务器、域名快到期或余额不足时提醒"},
			{Name: "alert_cloud", Kind: "enum", Enum: onOffEnum, Desc: "腾讯云、阿里云云监控告警时提醒"},
		},
		Impls: map[string]Impl{"*": {Via: "Miao Panel 设置", Local: "notices", Downtime: "不影响网站",
			Undo: "把这一步改过的通知设置改回原来的值"}},
		Check: func(v map[string]string) error {
			if t := v["daily_at"]; t != "" && !clockRe.MatchString(t) {
				return fmt.Errorf("参数 daily_at 要是 24 小时制的时间，例如 09:00，%q 不是", t)
			}
			return anySet(v, "daily", "daily_at", "alert_risk", "alert_leak", "alert_cert", "alert_block", "alert_renew", "alert_cloud")
		},
	})
}
