package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// ---- EdgeOne Web protection ----
//
// Rules are written in EdgeOne's expression syntax, e.g.
// ${http.request.ip} in ['1.2.3.4','5.6.7.0/24']. Rule lists are replaced
// as a whole, so every change reads the current list, edits only Miao
// Panel's own rule, and sends the rest back untouched.

// BlockRuleName is the custom rule that holds the IPs Miao Panel blocked.
const BlockRuleName = "Miao Panel 封禁 IP"

// maxBlocked keeps the block rule's expression a sensible size.
const maxBlocked = 500

var quotedRe = regexp.MustCompile(`'([^']*)'`)

// parseIPList checks a list of IPv4/IPv6 addresses and networks separated
// by commas or spaces, refusing networks so wide they would block most of
// the internet.
func parseIPList(s string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '，' || r == ';' }) {
		ip := net.ParseIP(f)
		switch {
		case ip != nil:
			f = ip.String()
		default:
			_, n, err := net.ParseCIDR(f)
			if err != nil {
				return nil, fmt.Errorf("%q 不是 IP 地址或网段", f)
			}
			ones, bits := n.Mask.Size()
			if (bits == 32 && ones < 8) || (bits == 128 && ones < 32) {
				return nil, fmt.Errorf("网段 %s 太大了，会拦截大量正常访问", f)
			}
			f = n.String()
		}
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没有填 IP")
	}
	return out, nil
}

func ipCondition(ips []string) string {
	return "${http.request.ip} in ['" + strings.Join(ips, "','") + "']"
}

// ruleIPs reads the IPs back from a rule's condition.
func ruleIPs(rule map[string]any) []string {
	var out []string
	for _, m := range quotedRe.FindAllStringSubmatch(tencent.RuleString(rule, "Condition"), -1) {
		out = append(out, m[1])
	}
	return out
}

func findRule(rules []map[string]any, name string) int {
	for i, r := range rules {
		if tencent.RuleString(r, "Name") == name {
			return i
		}
	}
	return -1
}

// withBlocked returns the custom rules with Miao Panel's block rule set to
// ips, or removed when ips is empty.
func withBlocked(rules []map[string]any, ips []string) []map[string]any {
	out := make([]map[string]any, 0, len(rules)+1)
	i := findRule(rules, BlockRuleName)
	for j, r := range rules {
		if j != i {
			out = append(out, r)
		}
	}
	if len(ips) == 0 {
		return out
	}
	rule := map[string]any{"Name": BlockRuleName, "Condition": ipCondition(ips), "Action": map[string]any{"Name": "Deny"},
		"Enabled": "on", "RuleType": "BasicAccessRule"}
	if i >= 0 {
		rule["Id"] = rules[i]["Id"]
	}
	return append(out, rule)
}

func policyFor(ctx context.Context, c *tencent.Client, domain string) (tencent.Zone, tencent.SecurityPolicy, error) {
	z, err := findZone(ctx, c, domain)
	if err != nil {
		return z, tencent.SecurityPolicy{}, err
	}
	p, err := c.SecurityPolicy(ctx, z.ZoneID)
	if err != nil {
		return z, p, fmt.Errorf("读取 EdgeOne 站点 %s 的安全策略失败：%w", z.ZoneName, err)
	}
	return z, p, nil
}

func applyIPBlock(ctx context.Context, env *Env, block bool, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	ips, err := parseIPList(v["ips"])
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	z, p, err := policyFor(ctx, c, v["domain"])
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	var current []string
	if i := findRule(p.CustomRules, BlockRuleName); i >= 0 {
		current = ruleIPs(p.CustomRules[i])
	}
	has := map[string]bool{}
	for _, ip := range current {
		has[ip] = true
	}
	next := append([]string(nil), current...)
	var changed []string
	if block {
		for _, ip := range ips {
			if !has[ip] {
				next = append(next, ip)
				changed = append(changed, ip)
			}
		}
		if len(next) > maxBlocked {
			out.Status = StatusRefused
			out.logf("Miao Panel 的封禁规则最多放 %d 个 IP，现在已有 %d 个。大范围的攻击建议开启 CC 防护（eo.cc.set）或速率限制", maxBlocked, len(current))
			return *out
		}
	} else {
		drop := map[string]bool{}
		for _, ip := range ips {
			if has[ip] {
				drop[ip] = true
				changed = append(changed, ip)
			}
		}
		next = next[:0]
		for _, ip := range current {
			if !drop[ip] {
				next = append(next, ip)
			}
		}
	}
	if len(changed) == 0 {
		if block {
			report("这些 IP 已经在站点 %s 的封禁规则里了，不需要修改", z.ZoneName)
		} else {
			report("Miao Panel 的封禁规则里没有这些 IP，不需要修改")
			for _, r := range p.CustomRules {
				if cond := tencent.RuleString(r, "Condition"); tencent.RuleString(r, "Name") != BlockRuleName && containsAny(cond, ips) {
					report("规则「%s」里有这些 IP，它不是 Miao Panel 建的，请在 EdgeOne 控制台修改", tencent.RuleString(r, "Name"))
				}
			}
		}
		out.Status, out.Undo = StatusDone, map[string]string{}
		return *out
	}
	if block {
		report("正在 EdgeOne 站点 %s 拦截 %s（整个站点生效）", z.ZoneName, strings.Join(changed, "、"))
	} else {
		report("正在解除站点 %s 对 %s 的拦截", z.ZoneName, strings.Join(changed, "、"))
	}
	if err := c.SetCustomRules(ctx, z.ZoneID, withBlocked(p.CustomRules, next)); err != nil {
		out.Status = StatusRefused
		out.logf("修改失败：%v", err)
		return *out
	}
	// Undo touches only these IPs, so blocks made after this step stay.
	out.Undo["zone_id"], out.Undo["changed"] = z.ZoneID, strings.Join(changed, ",")
	out.Undo["mode"] = map[bool]string{true: "added", false: "removed"}[block]
	report("完成：封禁规则里现在有 %d 个 IP，几十秒内在各节点生效", len(next))
	out.Status = StatusDone
	return *out
}

func containsAny(s string, list []string) bool {
	for _, x := range list {
		if strings.Contains(s, "'"+x+"'") {
			return true
		}
	}
	return false
}

func undoIPBlock(ctx context.Context, c *tencent.Client, undo map[string]string) error {
	p, err := c.SecurityPolicy(ctx, undo["zone_id"])
	if err != nil {
		return err
	}
	split := func(v string) []string {
		if v == "" {
			return nil
		}
		return strings.Split(v, ",")
	}
	if undo["mode"] == "" { // recorded by an older version: the whole list before
		return c.SetCustomRules(ctx, undo["zone_id"], withBlocked(p.CustomRules, split(undo["ips"])))
	}
	var current []string
	if i := findRule(p.CustomRules, BlockRuleName); i >= 0 {
		current = ruleIPs(p.CustomRules[i])
	}
	changed := map[string]bool{}
	for _, ip := range split(undo["changed"]) {
		changed[ip] = true
	}
	var next []string
	for _, ip := range current {
		if !changed[ip] {
			next = append(next, ip)
		}
	}
	if undo["mode"] == "removed" {
		next = append(next, split(undo["changed"])...)
	}
	return c.SetCustomRules(ctx, undo["zone_id"], withBlocked(p.CustomRules, next))
}

// ---- rate limiting ----

var durationRe = regexp.MustCompile(`^([0-9]{1,3})([smhd])$`)

func checkDuration(s string) error {
	m := durationRe.FindStringSubmatch(s)
	if m == nil {
		return fmt.Errorf("时长 %q 的格式不对，例如 10m、1h", s)
	}
	n, _ := strconv.Atoi(m[1])
	limit := map[string]int{"s": 120, "m": 120, "h": 48, "d": 30}[m[2]]
	if n < 1 || n > limit {
		return fmt.Errorf("时长 %s 超出范围（秒、分钟最多 120，小时最多 48，天最多 30）", s)
	}
	return nil
}

func securityAction(name string) map[string]any {
	switch name {
	case "challenge":
		return map[string]any{"Name": "Challenge", "ChallengeActionParameters": map[string]any{"ChallengeOption": "JSChallenge"}}
	case "monitor":
		return map[string]any{"Name": "Monitor"}
	}
	return map[string]any{"Name": "Deny"}
}

var actionText = map[string]string{"deny": "拦截", "challenge": "JavaScript 挑战（真人浏览器能自动通过）", "monitor": "只记录不拦截"}

// rateRule builds the rule: which requests it counts (a host and/or a
// path) and what happens to a client IP above the threshold.
func rateRule(z tencent.Zone, v map[string]string) (string, map[string]any) {
	var cond []string
	host, path := v["domain"], v["path"]
	if host != z.ZoneName {
		cond = append(cond, "${http.request.host} in ['"+host+"']")
	}
	scope := host
	if path != "" && path != "/" {
		cond = append(cond, "${http.request.uri.path} contain ['"+path+"']")
		scope += path
	}
	if len(cond) == 0 {
		cond = append(cond, "${http.request.uri.path} contain ['/']")
		scope = "整个站点 " + z.ZoneName
	}
	threshold, _ := strconv.Atoi(v["threshold"])
	name := "Miao Panel 限速 " + scope
	if r := []rune(name); len(r) > 60 {
		name = string(r[:60])
	}
	rule := map[string]any{
		"Name": name, "Condition": strings.Join(cond, " and "),
		"CountBy": []string{"http.request.ip"}, "MaxRequestThreshold": threshold, "CountingPeriod": v["period"],
		"Mode": "Block", "ActionDuration": v["duration"], "Action": securityAction(v["action"]), "Enabled": "on",
	}
	return scope, rule
}

func applyRateLimit(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	z, p, err := policyFor(ctx, c, v["domain"])
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	scope, rule := rateRule(z, v)
	name := tencent.RuleString(rule, "Name")
	rules := append([]map[string]any(nil), p.RateLimitingRules...)
	i := findRule(rules, name)
	if i >= 0 {
		old, _ := json.Marshal(rules[i])
		out.Undo["previous"] = string(old)
		rule["Id"] = rules[i]["Id"]
		rules[i] = rule
	} else {
		rules = append(rules, rule)
	}
	report("正在给 %s 设置速率限制：同一个 IP %s 内超过 %s 次请求，%s %s", scope, v["period"], v["threshold"], actionText[v["action"]], v["duration"])
	if err := c.SetRateLimitingRules(ctx, z.ZoneID, rules); err != nil {
		out.Status = StatusRefused
		out.Undo = map[string]string{}
		out.logf("修改失败：%v", err)
		return *out
	}
	out.Undo["zone_id"], out.Undo["name"] = z.ZoneID, name
	report("完成：规则「%s」已生效", name)
	out.Status = StatusDone
	return *out
}

func applyRateLimitRemove(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	z, p, err := policyFor(ctx, c, v["domain"])
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	i := findRule(p.RateLimitingRules, v["name"])
	if i < 0 {
		report("站点 %s 没有叫「%s」的速率限制规则，不需要修改", z.ZoneName, v["name"])
		out.Status, out.Undo = StatusDone, map[string]string{}
		return *out
	}
	old, _ := json.Marshal(p.RateLimitingRules[i])
	rules := append(append([]map[string]any(nil), p.RateLimitingRules[:i]...), p.RateLimitingRules[i+1:]...)
	report("正在删除站点 %s 的速率限制规则「%s」", z.ZoneName, v["name"])
	if err := c.SetRateLimitingRules(ctx, z.ZoneID, rules); err != nil {
		out.Status = StatusRefused
		out.logf("删除失败：%v", err)
		return *out
	}
	out.Undo["zone_id"], out.Undo["name"], out.Undo["previous"] = z.ZoneID, v["name"], string(old)
	report("完成：已删除")
	out.Status = StatusDone
	return *out
}

// undoRateLimit puts the named rule back the way it was: removed if it was
// new, restored from its saved copy otherwise.
func undoRateLimit(ctx context.Context, c *tencent.Client, undo map[string]string) error {
	p, err := c.SecurityPolicy(ctx, undo["zone_id"])
	if err != nil {
		return err
	}
	var rules []map[string]any
	for _, r := range p.RateLimitingRules {
		if tencent.RuleString(r, "Name") != undo["name"] {
			rules = append(rules, r)
		}
	}
	if undo["previous"] != "" {
		var prev map[string]any
		if err := json.Unmarshal([]byte(undo["previous"]), &prev); err != nil {
			return err
		}
		delete(prev, "Id") // it comes back as a new rule when it was deleted
		for _, r := range p.RateLimitingRules {
			if tencent.RuleString(r, "Name") == undo["name"] {
				prev["Id"] = r["Id"]
			}
		}
		rules = append(rules, prev)
	}
	return c.SetRateLimitingRules(ctx, undo["zone_id"], rules)
}

// ---- CC protection (adaptive frequency control) ----

func applyCC(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	z, p, err := policyFor(ctx, c, v["domain"])
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	ddos := p.HTTPDDoS
	if ddos == nil {
		ddos = map[string]any{}
	}
	prev, _ := ddos["AdaptiveFrequencyControl"].(map[string]any)
	want := map[string]any{"Enabled": v["enabled"]}
	if v["enabled"] == "on" {
		want["Sensitivity"] = v["sensitivity"]
		want["Action"] = securityAction(v["action"])
	}
	if tencent.RuleString(prev, "Enabled") == v["enabled"] && (v["enabled"] == "off" ||
		(tencent.RuleString(prev, "Sensitivity") == v["sensitivity"] && tencent.ActionName(prev) == tencent.ActionName(want))) {
		report("站点 %s 的 CC 防护已经是这个设置，不需要修改", z.ZoneName)
		out.Status, out.Undo = StatusDone, map[string]string{}
		return *out
	}
	next := map[string]any{}
	for k, sub := range ddos {
		next[k] = sub
	}
	next["AdaptiveFrequencyControl"] = want
	if v["enabled"] == "on" {
		report("正在开启站点 %s 的 CC 防护（自适应频控）：%s，%s",
			z.ZoneName, map[string]string{"Loose": "宽松", "Moderate": "适中", "Strict": "严格"}[v["sensitivity"]], actionText[v["action"]])
	} else {
		report("正在关闭站点 %s 的 CC 防护（自适应频控）", z.ZoneName)
	}
	if err := c.SetHTTPDDoS(ctx, z.ZoneID, next); err != nil {
		out.Status = StatusRefused
		out.logf("修改失败：%v（个人版等套餐可能不支持这项防护）", err)
		return *out
	}
	if prev == nil {
		prev = map[string]any{"Enabled": "off"}
	}
	old, _ := json.Marshal(prev)
	out.Undo["zone_id"], out.Undo["previous"] = z.ZoneID, string(old)
	report("完成")
	out.Status = StatusDone
	return *out
}

func undoCC(ctx context.Context, c *tencent.Client, undo map[string]string) error {
	p, err := c.SecurityPolicy(ctx, undo["zone_id"])
	if err != nil {
		return err
	}
	var prev map[string]any
	if err := json.Unmarshal([]byte(undo["previous"]), &prev); err != nil {
		return err
	}
	next := map[string]any{}
	for k, sub := range p.HTTPDDoS {
		next[k] = sub
	}
	next["AdaptiveFrequencyControl"] = prev
	return c.SetHTTPDDoS(ctx, undo["zone_id"], next)
}

// BlockedIPs lists the IPs Miao Panel has blocked in a site's policy.
func BlockedIPs(p tencent.SecurityPolicy) []string {
	if i := findRule(p.CustomRules, BlockRuleName); i >= 0 {
		return ruleIPs(p.CustomRules[i])
	}
	return nil
}
