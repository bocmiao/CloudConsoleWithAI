package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

var planTypeName = map[string]string{
	"plan-trial": "试用版", "plan-personal": "个人版", "plan-basic": "基础版", "plan-standard": "标准版", "plan-enterprise": "企业版",
}

// verifyText notes a CNAME site that has not proved the domain is yours.
func verifyText(z tencent.Zone) string {
	if dv := z.Verification(); dv != nil {
		return fmt.Sprintf(" 归属验证=未完成（要加 %s 记录 %s = %s，再执行一次 eo.zone.create 会自动完成）", dv.RecordType, dv.Subdomain, dv.RecordValue)
	}
	return ""
}

// plansText lists the account's EdgeOne plans and which can take a site.
func plansText(ctx context.Context, c *tencent.Client) string {
	plans, err := c.Plans(ctx)
	if err != nil {
		return fmt.Sprintf("查询套餐失败：%v\n", err)
	}
	if len(plans) == 0 {
		return "账号里没有 EdgeOne 套餐：新建站点前要先在 EdgeOne 控制台购买或领取套餐（涉及计费，由用户自己操作）。\n"
	}
	var b strings.Builder
	for _, p := range plans {
		var zones []string
		for _, z := range p.ZonesInfo {
			zones = append(zones, z.ZoneName)
		}
		bind := "不能再绑定站点"
		if p.Bindable == "true" {
			bind = "还能绑定站点"
		}
		fmt.Fprintf(&b, "套餐 %s %s 区域=%s 状态=%s %s 到期=%s 已绑定=%s\n", p.PlanID, orDash(planTypeName[p.PlanType]), p.Area, p.Status, bind,
			orDash(p.ExpiredTime), orDash(strings.Join(zones, "、")))
	}
	return b.String()
}

func (a *App) toolTencentEOSecurity(ctx context.Context, raw json.RawMessage) (string, error) {
	var arg struct {
		Domain string `json:"domain"`
	}
	if err := parseArgs(raw, &arg); err != nil {
		return "", err
	}
	domain := strings.ToLower(strings.TrimSpace(arg.Domain))
	if domain == "" {
		return "", userErr("请填写 domain")
	}
	return a.cloudRead(ctx, "查询 EdgeOne 安全防护："+domain, func(c *tencent.Client) (string, error) {
		zones, err := c.Zones(ctx)
		if err != nil {
			return "", err
		}
		z, ok := tencent.ZoneFor(zones, domain)
		if !ok {
			return fmt.Sprintf("EdgeOne 里没有 %s 所在的站点", domain), nil
		}
		p, err := c.SecurityPolicy(ctx, z.ZoneID)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "站点 %s 的站点级安全策略：\n", z.ZoneName)
		b.WriteString("自定义规则（封禁/放行）：\n")
		if len(p.CustomRules) == 0 {
			b.WriteString("  没有\n")
		}
		for _, r := range p.CustomRules {
			mine := ""
			if tencent.RuleString(r, "Name") == actions.BlockRuleName {
				mine = "（Miao Panel 的封禁列表，用 eo.ip.block / eo.ip.unblock 修改）"
			}
			fmt.Fprintf(&b, "  「%s」%s 类型=%s 条件=%s 动作=%s 开启=%s\n", tencent.RuleString(r, "Name"), mine, tencent.RuleString(r, "RuleType"),
				clipText(tencent.RuleString(r, "Condition"), 600), tencent.ActionName(r), tencent.RuleString(r, "Enabled"))
		}
		b.WriteString("速率限制：\n")
		if len(p.RateLimitingRules) == 0 {
			b.WriteString("  没有\n")
		}
		for _, r := range p.RateLimitingRules {
			fmt.Fprintf(&b, "  「%s」条件=%s 统计=%v 阈值=%v 次/%s 动作=%s 持续=%s 开启=%s\n", tencent.RuleString(r, "Name"), clipText(tencent.RuleString(r, "Condition"), 300),
				r["CountBy"], r["MaxRequestThreshold"], tencent.RuleString(r, "CountingPeriod"), tencent.ActionName(r), tencent.RuleString(r, "ActionDuration"), tencent.RuleString(r, "Enabled"))
		}
		b.WriteString("CC 防护：\n")
		names := map[string]string{"AdaptiveFrequencyControl": "自适应频控（eo.cc.set）", "ClientFiltering": "智能客户端过滤",
			"BandwidthAbuseDefense": "流量防盗刷", "SlowAttackDefense": "慢速攻击防护"}
		for _, k := range []string{"AdaptiveFrequencyControl", "ClientFiltering", "BandwidthAbuseDefense", "SlowAttackDefense"} {
			m, _ := p.HTTPDDoS[k].(map[string]any)
			if m == nil {
				continue
			}
			line := fmt.Sprintf("  %s 开启=%s", names[k], orDash(tencent.RuleString(m, "Enabled")))
			if s := tencent.RuleString(m, "Sensitivity"); s != "" {
				line += " 灵敏度=" + s
			}
			if n := tencent.ActionName(m); n != "" {
				line += " 动作=" + n
			}
			b.WriteString(line + "\n")
		}
		if m, _ := p.Raw["ManagedRules"].(map[string]any); m != nil {
			fmt.Fprintf(&b, "托管规则（Web 攻击防护）：开启=%s 仅观察=%s\n", orDash(tencent.RuleString(m, "Enabled")), orDash(tencent.RuleString(m, "DetectionOnly")))
		}
		b.WriteString("说明：这里是站点级策略；如果某个域名单独配置了域名级策略，站点级规则对它不生效，需要在控制台查看。")
		return b.String(), nil
	})
}

func clipText(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// toolPanelWebsites lists a 1Panel server's websites and the apps a new
// site could proxy to.
func (a *App) toolPanelWebsites(ctx context.Context, raw json.RawMessage) (string, error) {
	var arg serverArg
	if err := parseArgs(raw, &arg); err != nil {
		return "", err
	}
	sv, c, err := a.connect(ctx, arg.ServerID)
	if err != nil {
		return "", err
	}
	defer c.Close()
	op, err := a.onePanelClient(sv.ID, c)
	if err != nil {
		return "", err
	}
	if op == nil {
		return "这台服务器还没有配置 1Panel 接口（服务器页面 → 1Panel 接口），看不到 1Panel 的网站列表。", nil
	}
	e := a.startExec(store.ExecLog{ServerID: sv.ID, ServerName: sv.Name, Adapter: sv.Adapter, Origin: originOf(ctx),
		Kind: store.ExecRead, Title: "查询 1Panel 网站", Via: "1Panel 接口"})
	var cmds []string
	op.Trace = func(method, path string, body []byte) { cmds = append(cmds, method+" "+path+" "+string(body)) }
	var b strings.Builder
	sites, err := op.Websites(ctx)
	if err == nil {
		if len(sites) == 0 {
			b.WriteString("1Panel 里还没有网站。\n")
		}
		for _, s := range sites {
			fmt.Fprintf(&b, "网站 %s 类型=%s 状态=%s 代理到=%s 目录=%s\n", s.PrimaryDomain, s.Type, s.Status, orDash(s.Proxy), orDash(s.SitePath))
		}
		if installed, aerr := op.InstalledApps(ctx); aerr != nil {
			fmt.Fprintf(&b, "读取已安装应用失败：%v\n", aerr)
		} else {
			var apps []string
			for _, ap := range installed {
				apps = append(apps, fmt.Sprintf("%s（%s，%s，端口 %d）", ap.Name, ap.AppKey, ap.Status, ap.HTTPPort))
			}
			fmt.Fprintf(&b, "已安装的应用：%s\n", orDash(strings.Join(apps, "、")))
		}
		b.WriteString("新建网站需要已安装并运行 OpenResty；反向代理网站可以用 site.create 的 app 参数指向上面的应用。")
	}
	e.Commands = strings.Join(append([]string{"# 在服务器本机调用 1Panel 接口（只读）"}, cmds...), "\n")
	if err != nil {
		a.finishExec(&e, actions.StatusFailed, err.Error())
		return "", err
	}
	a.finishExec(&e, actions.StatusDone, b.String())
	return b.String(), nil
}
