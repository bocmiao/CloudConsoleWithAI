package app

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// EdgeOne's 防护 section: a site's rate limits, its CC protection and its
// acceleration domains, each change a checklist (the same capabilities
// the AI proposes).

// EORateRule is a rate limiting rule of a site.
type EORateRule struct {
	Name      string `json:"name"`
	Host      string `json:"host,omitempty"` // the one domain it counts, if not the whole site
	Path      string `json:"path,omitempty"` // the path it counts, if any
	Condition string `json:"condition"`      // as EdgeOne writes it, when not one of the above
	Threshold int    `json:"threshold"`
	Period    string `json:"period"`
	Action    string `json:"action"` // Deny, JSChallenge, Monitor, ...
	Duration  string `json:"duration"`
	Enabled   bool   `json:"enabled"`
	Mine      bool   `json:"mine"` // made by Miao Panel
}

// EODomainState is an acceleration domain as the 防护 section shows it.
type EODomainState struct {
	Name   string `json:"name"`
	Status string `json:"status"` // online, offline, process, init
	Origin string `json:"origin"`
	Cname  string `json:"cname"`
	HTTPS  bool   `json:"https"`
}

// EOProtectView is a site's 防护.
type EOProtectView struct {
	Zone        string          `json:"zone"`
	Rules       []EORateRule    `json:"rules"`
	CC          EOCC            `json:"cc"`
	Blocked     int             `json:"blocked"` // IPs in Miao Panel's block list
	Domains     []EODomainState `json:"domains"`
	PolicyError string          `json:"policyError,omitempty"`
	DomainError string          `json:"domainError,omitempty"`
}

// EOCC is a site's CC protection (adaptive frequency control).
type EOCC struct {
	Enabled     bool   `json:"enabled"`
	Sensitivity string `json:"sensitivity,omitempty"` // Loose, Moderate, Strict
	Action      string `json:"action,omitempty"`
}

var (
	condPath = regexp.MustCompile(`\$\{http\.request\.uri\.path\} contain \['([^']*)'\]`)
	condHost = regexp.MustCompile(`\$\{http\.request\.host\} in \['([^']*)'\]`)
)

// EOProtection reads the 防护 of the site a domain is in.
func (a *App) EOProtection(ctx context.Context, domain string) (EOProtectView, error) {
	c := a.tencentClient()
	if c == nil {
		return EOProtectView{}, userErr("还没有配置腾讯云密钥（设置 → 腾讯云）")
	}
	zones, err := c.Zones(ctx)
	if err != nil {
		return EOProtectView{}, err
	}
	z, ok := tencent.ZoneFor(zones, strings.ToLower(strings.TrimSpace(domain)))
	if !ok {
		return EOProtectView{}, userErr("EdgeOne 里没有 %s 所在的站点", domain)
	}
	v := EOProtectView{Zone: z.ZoneName, Rules: []EORateRule{}, Domains: []EODomainState{}}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		p, err := c.SecurityPolicy(ctx, z.ZoneID)
		if err != nil {
			v.PolicyError = err.Error()
			return
		}
		for _, r := range p.RateLimitingRules {
			cond := tencent.RuleString(r, "Condition")
			rule := EORateRule{Name: tencent.RuleString(r, "Name"), Condition: cond, Period: tencent.RuleString(r, "CountingPeriod"),
				Action: tencent.ActionName(r), Duration: tencent.RuleString(r, "ActionDuration"), Enabled: tencent.RuleString(r, "Enabled") == "on",
				Mine: strings.HasPrefix(tencent.RuleString(r, "Name"), "Miao Panel ")}
			rule.Threshold, _ = strconv.Atoi(fmt.Sprint(r["MaxRequestThreshold"]))
			if m := condPath.FindStringSubmatch(cond); m != nil && m[1] != "/" {
				rule.Path = m[1]
			}
			if m := condHost.FindStringSubmatch(cond); m != nil {
				rule.Host = m[1]
			}
			v.Rules = append(v.Rules, rule)
		}
		if m, _ := p.HTTPDDoS["AdaptiveFrequencyControl"].(map[string]any); m != nil {
			v.CC = EOCC{Enabled: tencent.RuleString(m, "Enabled") == "on", Sensitivity: tencent.RuleString(m, "Sensitivity"), Action: tencent.ActionName(m)}
		}
		v.Blocked = len(actions.BlockedIPs(p))
	}()
	go func() {
		defer wg.Done()
		list, err := c.AccelerationDomains(ctx, z.ZoneID, "")
		if err != nil {
			v.DomainError = err.Error()
			return
		}
		for _, d := range list {
			v.Domains = append(v.Domains, EODomainState{Name: d.DomainName, Status: d.DomainStatus, Origin: d.OriginDetail.Origin, Cname: d.Cname,
				HTTPS: d.Certificate.Mode != "" && d.Certificate.Mode != "disable"})
		}
	}()
	wg.Wait()
	return v, nil
}

// EOProtectionPage is the 防护 of a site, kept ready.
func (a *App) EOProtectionPage(ctx context.Context, domain string, read PageRead) (EOProtectView, PageMeta, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if len(domain) > 253 || !hostnameRe.MatchString(domain) {
		return EOProtectView{}, PageMeta{}, userErr("%q 不是域名", domain)
	}
	return page(ctx, a, "page_eoprot_"+domain, read, func(ctx context.Context) (EOProtectView, error) { return a.EOProtection(ctx, domain) })
}

// EOProtectRequest is a change asked for in the 防护 section.
type EOProtectRequest struct {
	Domain string `json:"domain"`
	Op     string `json:"op"` // ratelimit_set, ratelimit_remove, cc, domain_on, domain_off
	// Rate limiting.
	Name      string `json:"name,omitempty"` // the rule to remove
	Host      string `json:"host,omitempty"` // count one domain only
	Path      string `json:"path,omitempty"`
	Threshold int    `json:"threshold,omitempty"`
	Period    string `json:"period,omitempty"`
	Action    string `json:"action,omitempty"` // challenge, deny, monitor
	Duration  string `json:"duration,omitempty"`
	// CC protection.
	Enabled     bool   `json:"enabled,omitempty"`
	Sensitivity string `json:"sensitivity,omitempty"`
}

// ProposeEOProtect turns a change in the 防护 section into a checklist.
func (a *App) ProposeEOProtect(ctx context.Context, req EOProtectRequest) (PlanView, error) {
	v, _, err := a.EOProtectionPage(ctx, req.Domain, PageWait)
	if err != nil {
		return PlanView{}, err
	}
	params := map[string]any{"domain": v.Zone}
	var capability, title, summary, reason string
	switch req.Op {
	case "ratelimit_set":
		host := strings.ToLower(strings.TrimSpace(req.Host))
		if host != "" {
			params["domain"] = host
		}
		if p := strings.TrimSpace(req.Path); p != "" {
			params["path"] = p
		}
		if req.Threshold <= 0 {
			return PlanView{}, userErr("请填写阈值：同一个 IP 在统计周期内最多请求多少次")
		}
		params["threshold"] = req.Threshold
		for k, val := range map[string]string{"period": req.Period, "action": req.Action, "duration": req.Duration} {
			if val != "" {
				params[k] = val
			}
		}
		capability, title = "eo.ratelimit.set", "EdgeOne 限速："+v.Zone
		scope := orDash(host)
		if host == "" {
			scope = "整个站点"
		}
		if p := strings.TrimSpace(req.Path); p != "" {
			scope += "、路径含 " + p
		}
		period := req.Period
		if period == "" {
			period = "1m"
		}
		action := req.Action
		if action == "" {
			action = "challenge"
		}
		summary = fmt.Sprintf("%s：同一个 IP %s内超过 %d 次请求就%s", scope, periodText(period), req.Threshold,
			map[string]string{"challenge": "进行 JavaScript 挑战", "deny": "拦截", "monitor": "记录下来"}[action])
		reason = "按访客 IP 限制请求频率，挡住刷接口、猜密码、爬虫高频抓取。阈值要比正常访客高得多，先用「挑战」观察一段时间。可以撤销。"
	case "ratelimit_remove":
		capability, title = "eo.ratelimit.remove", "删除 EdgeOne 限速规则："+req.Name
		params["name"] = req.Name
		summary = "删除站点 " + v.Zone + " 的速率限制规则「" + req.Name + "」"
		reason = "删除后这条限制不再生效。可以撤销。"
	case "cc":
		capability = "eo.cc.set"
		if req.Enabled {
			params["enabled"] = "on"
			if req.Sensitivity != "" {
				params["sensitivity"] = req.Sensitivity
			}
			if req.Action != "" {
				params["action"] = req.Action
			}
			title, summary = "开启 EdgeOne CC 防护："+v.Zone, "开启站点 "+v.Zone+" 的 CC 防护（自适应频控）"
			reason = "EdgeOne 学习网站平时的访问基线，自动识别异常的高频访问（CC 攻击）并处理。先用「适中」+「挑战」，严格模式可能误伤正常访客。可以撤销。"
		} else {
			params["enabled"] = "off"
			title, summary = "关闭 EdgeOne CC 防护："+v.Zone, "关闭站点 "+v.Zone+" 的 CC 防护"
			reason = "关闭后 EdgeOne 不再自动识别和处理 CC 攻击。可以撤销。"
		}
	case "domain_on", "domain_off":
		name := strings.ToLower(strings.TrimSpace(req.Name))
		found := false
		for _, d := range v.Domains {
			found = found || d.Name == name
		}
		if !found {
			return PlanView{}, userErr("站点 %s 里没有加速域名 %s", v.Zone, name)
		}
		capability = "eo.domain.status"
		params["domain"] = name
		if req.Op == "domain_on" {
			params["status"] = "online"
			title, summary = "启用 EdgeOne 加速域名："+name, "启用 "+name+" 的 EdgeOne 加速"
			reason = "启用后访问这个域名会经过 EdgeOne（解析要指向 EdgeOne 分配的 CNAME）。可以撤销。"
		} else {
			params["status"] = "offline"
			title, summary = "停用 EdgeOne 加速域名："+name, "停用 "+name+" 的 EdgeOne 加速"
			reason = "停用后解析还指向 EdgeOne 的访客会打不开，要先在「解析」页把这个名字改回直接解析到服务器（可以一键「不走 EdgeOne」）。可以撤销。"
		}
	default:
		return PlanView{}, userErr("不支持的操作 %q", req.Op)
	}
	if _, err := actions.Resolve(capability, params, noServer); err != nil {
		return PlanView{}, userErr("%v", err)
	}
	p, _, err := a.proposePlan(ctx, "user", 0, title, reason, []core.Step{{Capability: capability, Summary: summary, Params: params}})
	if err != nil {
		return PlanView{}, err
	}
	return a.Plan(p.ID)
}

// periodText says a counting period (10s, 1m, 1h) in words.
func periodText(p string) string {
	n, unit := strings.TrimRight(p, "smh"), p[len(strings.TrimRight(p, "smh")):]
	if w, ok := map[string]string{"s": " 秒", "m": " 分钟", "h": " 小时"}[unit]; ok && n != "" {
		return n + w
	}
	return p
}
