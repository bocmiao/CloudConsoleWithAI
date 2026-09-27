package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// ---- Tencent Cloud servers ----

func findInstance(ctx context.Context, c *tencent.Client, region, id string) (tencent.Server, error) {
	s, ok, err := c.Instance(ctx, region, id)
	switch {
	case err != nil:
		return s, fmt.Errorf("查询实例 %s 失败：%w", id, err)
	case !ok:
		return s, fmt.Errorf("在地域 %s 找不到实例 %s", region, id)
	}
	return s, nil
}

func serverText(s tencent.Server) string {
	kind := map[string]string{tencent.Lighthouse: "轻量服务器", tencent.CVM: "云服务器"}[s.Kind]
	ip := strings.Join(s.PublicIPs, ",")
	return fmt.Sprintf("%s %s（%s，%s）", kind, s.Name, s.ID, ip)
}

// portCovers reports whether a port spec (ALL, 80, 80,443, 8000-8100)
// includes port.
func portCovers(spec string, port int) bool {
	if strings.EqualFold(spec, "ALL") {
		return true
	}
	for _, part := range strings.Split(spec, ",") {
		lo, hi, isRange := strings.Cut(strings.TrimSpace(part), "-")
		a, _ := strconv.Atoi(lo)
		b := a
		if isRange {
			b, _ = strconv.Atoi(hi)
		}
		if port >= a && port <= b {
			return true
		}
	}
	return false
}

func firewallRule(v map[string]string) tencent.FirewallRule {
	desc := v["description"]
	if desc == "" {
		desc = "Miao Panel"
	}
	return tencent.FirewallRule{Protocol: v["protocol"], Port: strings.ToUpper(v["port"]), CidrBlock: v["cidr"], Action: "ACCEPT", Description: desc}
}

// cloudRules lists the rules protecting an instance: the Lighthouse
// firewall, or a CVM instance's first security group.
func cloudRules(ctx context.Context, c *tencent.Client, s tencent.Server) (string, []tencent.FirewallRule, error) {
	if s.Kind == tencent.Lighthouse {
		rules, err := c.LighthouseFirewall(ctx, s.Region, s.ID)
		return "", rules, err
	}
	if len(s.Groups) == 0 {
		return "", nil, fmt.Errorf("实例 %s 没有绑定安全组", s.ID)
	}
	rules, err := c.SecurityGroupIngress(ctx, s.Region, s.Groups[0])
	return s.Groups[0], rules, err
}

func addRule(ctx context.Context, c *tencent.Client, s tencent.Server, group string, r tencent.FirewallRule) error {
	if s.Kind == tencent.Lighthouse {
		return c.AddLighthouseFirewall(ctx, s.Region, s.ID, r)
	}
	return c.AddSecurityGroupIngress(ctx, s.Region, group, r)
}

func deleteRule(ctx context.Context, c *tencent.Client, s tencent.Server, group string, r tencent.FirewallRule) error {
	if s.Kind == tencent.Lighthouse {
		return c.DeleteLighthouseFirewall(ctx, s.Region, s.ID, r)
	}
	return c.DeleteSecurityGroupIngress(ctx, s.Region, group, r)
}

func ruleText(r tencent.FirewallRule) string {
	return fmt.Sprintf("%s %s 来源 %s %s", r.Protocol, r.Port, r.Source(), r.Action)
}

func applyFirewallOpen(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	s, err := findInstance(ctx, c, v["region"], v["instance"])
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	group, rules, err := cloudRules(ctx, c, s)
	if err != nil {
		out.Status = StatusRefused
		out.logf("读取防火墙规则失败：%v", err)
		return *out
	}
	rule := firewallRule(v)
	for _, r := range rules {
		if r.Same(rule) {
			report("%s 已经放行 %s，不需要修改", serverText(s), ruleText(rule))
			out.Status, out.Undo = StatusDone, map[string]string{}
			return *out
		}
	}
	if group != "" {
		report("CVM 的防火墙是安全组 %s；绑定同一个安全组的其他服务器也会放行这个端口", group)
	}
	report("正在给 %s 放行 %s", serverText(s), ruleText(rule))
	if err := addRule(ctx, c, s, group, rule); err != nil {
		out.Status = StatusRefused
		out.logf("添加规则失败：%v", err)
		return *out
	}
	data, _ := json.Marshal(rule)
	out.Undo["region"], out.Undo["instance"], out.Undo["group"], out.Undo["rule"] = s.Region, s.ID, group, string(data)
	report("完成：已放行 %s", ruleText(rule))
	out.Status = StatusDone
	return *out
}

func applyFirewallClose(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	s, err := findInstance(ctx, c, v["region"], v["instance"])
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	want := firewallRule(v)
	for _, p := range []int{22, 3389} {
		if portCovers(want.Port, p) {
			out.Status = StatusRefused
			out.logf("端口 %d 是远程登录用的，关掉会连不上服务器，不允许在这里关闭", p)
			return *out
		}
	}
	group, rules, err := cloudRules(ctx, c, s)
	if err != nil {
		out.Status = StatusRefused
		out.logf("读取防火墙规则失败：%v", err)
		return *out
	}
	var removed []tencent.FirewallRule
	for _, r := range rules {
		if !r.Same(want) {
			continue
		}
		report("正在删除 %s 的规则 %s", serverText(s), ruleText(r))
		if err := deleteRule(ctx, c, s, group, r); err != nil {
			for _, back := range removed {
				_ = addRule(ctx, c, s, group, back)
			}
			out.Status = StatusRolledBack
			out.logf("删除失败：%v（已删除的规则已加回）", err)
			return *out
		}
		removed = append(removed, r)
	}
	if len(removed) == 0 {
		report("%s 没有放行 %s 的规则，不需要修改", serverText(s), ruleText(want))
		out.Status, out.Undo = StatusDone, map[string]string{}
		return *out
	}
	data, _ := json.Marshal(removed)
	out.Undo["region"], out.Undo["instance"], out.Undo["group"], out.Undo["removed"] = s.Region, s.ID, group, string(data)
	report("完成：已关闭 %s", ruleText(want))
	out.Status = StatusDone
	return *out
}

func undoFirewall(ctx context.Context, c *tencent.Client, undo map[string]string) error {
	s := tencent.Server{ID: undo["instance"], Region: undo["region"], Kind: tencent.KindOf(undo["instance"])}
	if undo["rule"] != "" {
		var r tencent.FirewallRule
		if err := json.Unmarshal([]byte(undo["rule"]), &r); err != nil {
			return err
		}
		return deleteRule(ctx, c, s, undo["group"], r)
	}
	var rules []tencent.FirewallRule
	if err := json.Unmarshal([]byte(undo["removed"]), &rules); err != nil {
		return err
	}
	// Back in their places, lowest first, so each lands where it was.
	sort.Slice(rules, func(i, j int) bool { return rules[i].Index < rules[j].Index })
	for _, r := range rules {
		if err := addRule(ctx, c, s, undo["group"], r); err != nil {
			return err
		}
	}
	return nil
}

func publicSensitiveRule(r tencent.FirewallRule, sshPort int) bool {
	if !strings.EqualFold(r.Action, "ACCEPT") || (r.CidrBlock != "0.0.0.0/0" && r.Ipv6 != "::/0") {
		return false
	}
	for _, p := range []int{sshPort, 13940, 8090, 8080, 3306} {
		if portCovers(r.Port, p) {
			return true
		}
	}
	return false
}

func privateRuleSource(r tencent.FirewallRule) bool {
	source := r.CidrBlock
	if source == "" {
		source = r.Ipv6
	}
	p, err := netip.ParsePrefix(source)
	if err != nil {
		return false
	}
	p = p.Masked()
	for _, cidr := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16", "fc00::/7", "fe80::/10", "::1/128"} {
		private := netip.MustParsePrefix(cidr)
		if p.Addr().Is4() == private.Addr().Is4() && p.Bits() >= private.Bits() && private.Contains(p.Addr()) {
			return true
		}
	}
	return false
}

func unapprovedSensitiveRule(r tencent.FirewallRule, sshPort int, adminCIDR string) bool {
	if !strings.EqualFold(r.Action, "ACCEPT") {
		return false
	}
	if portCovers(r.Port, 3306) {
		return !privateRuleSource(r)
	}
	for _, port := range []int{sshPort, 13940, 8090, 8080} {
		if portCovers(r.Port, port) {
			return !privateRuleSource(r) && r.CidrBlock != adminCIDR
		}
	}
	return false
}

// FirewallTightenRules rejects ingress that would remain public after tightening.
// Proposal and execution both use it so the checklist matches what can run.
func FirewallTightenRules(rules []tencent.FirewallRule, sshPort int, adminCIDR string) ([]tencent.FirewallRule, error) {
	var remove []tencent.FirewallRule
	broad := 0
	for _, r := range rules {
		if strings.EqualFold(r.Action, "ACCEPT") && strings.EqualFold(r.Protocol, "ALL") && strings.EqualFold(r.Port, "ALL") && r.CidrBlock == "0.0.0.0/0" {
			broad++
			remove = append(remove, r)
			continue
		}
		if !publicSensitiveRule(r, sshPort) {
			if unapprovedSensitiveRule(r, sshPort, adminCIDR) {
				return nil, fmt.Errorf("另有非管理来源可访问管理端口或 3306：%s，请先单独检查它", ruleText(r))
			}
			continue
		}
		allowedExact := false
		for _, p := range []int{sshPort, 13940, 8090, 8080, 3306} {
			if r.Port == strconv.Itoa(p) {
				allowedExact = true
				break
			}
		}
		if !allowedExact {
			return nil, fmt.Errorf("另有覆盖管理端口或 3306 的宽范围规则 %s，请先单独检查它", ruleText(r))
		}
		remove = append(remove, r)
	}
	if broad != 1 {
		return nil, fmt.Errorf("需要恰好一条 ALL ALL 0.0.0.0/0 放行规则；当前有 %d 条", broad)
	}
	return remove, nil
}

// applyFirewallTighten is one atomic operation: allow known service ports
// first, remove exactly one broad rule, and restore it if anything fails.
func applyFirewallTighten(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	s, err := findInstance(ctx, c, v["region"], v["instance"])
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	if s.Kind != tencent.CVM || len(s.Groups) != 1 || s.Groups[0] != v["group"] {
		out.Status = StatusRefused
		out.logf("安全组绑定情况已变化，或实例绑定了多个安全组；请重新检查后生成清单")
		return *out
	}
	ip, network, err := net.ParseCIDR(v["admin_cidr"])
	if err != nil || ip.To4() == nil || network.String() == "0.0.0.0/0" {
		out.Status = StatusRefused
		out.logf("管理来源必须是明确的 IPv4 网段，例如 203.0.113.4/32")
		return *out
	}
	sshPort, err := strconv.Atoi(v["ssh_port"])
	if err != nil || sshPort < 1 || sshPort > 65535 {
		out.Status = StatusRefused
		out.logf("SSH 端口无效，请重新生成清单")
		return *out
	}
	rules, err := c.SecurityGroupIngress(ctx, s.Region, v["group"])
	if err != nil {
		out.Status = StatusRefused
		out.logf("读取安全组失败：%v", err)
		return *out
	}
	remove, err := FirewallTightenRules(rules, sshPort, network.String())
	if err != nil {
		out.Status = StatusRefused
		out.logf("安全组检查失败：%v", err)
		return *out
	}
	var added, deleted []tencent.FirewallRule
	broadIndex := int64(0)
	for _, r := range remove {
		if strings.EqualFold(r.Protocol, "ALL") && strings.EqualFold(r.Port, "ALL") && r.CidrBlock == "0.0.0.0/0" {
			broadIndex = r.Index
			break
		}
	}
	restore := func() error {
		var first error
		sort.Slice(deleted, func(i, j int) bool { return deleted[i].Index < deleted[j].Index })
		for _, r := range deleted {
			if err := c.InsertSecurityGroupIngress(ctx, s.Region, v["group"], r); err != nil && first == nil {
				first = err
			}
		}
		for _, r := range added {
			if err := c.DeleteSecurityGroupIngress(ctx, s.Region, v["group"], r); err != nil && first == nil {
				first = err
			}
		}
		return first
	}
	for _, spec := range []struct{ port, source string }{{"80", "0.0.0.0/0"}, {"443", "0.0.0.0/0"}, {strconv.Itoa(sshPort), network.String()}, {"13940", network.String()}, {"8090", network.String()}, {"8080", network.String()}} {
		r := tencent.FirewallRule{Protocol: "TCP", Port: spec.port, CidrBlock: spec.source, Action: "ACCEPT", Description: "Miao Panel security tightening", Index: broadIndex}
		found := false
		for _, old := range rules {
			if old.Same(r) {
				found = true
				break
			}
		}
		if found {
			continue
		}
		report("先放行 TCP %s 来源 %s", spec.port, spec.source)
		if err := c.InsertSecurityGroupIngress(ctx, s.Region, v["group"], r); err != nil {
			if backErr := restore(); backErr != nil {
				out.Status = StatusFailed
				out.logf("添加替代规则失败：%v；清理已添加规则也失败：%v", err, backErr)
				return *out
			}
			out.Status = StatusRefused
			out.logf("添加替代规则失败，原规则未删除：%v", err)
			return *out
		}
		added = append(added, r)
	}
	for _, r := range remove {
		report("删除 %s 的公开规则 %s", v["group"], ruleText(r))
		if err := c.DeleteSecurityGroupIngress(ctx, s.Region, v["group"], r); err != nil {
			if backErr := restore(); backErr != nil {
				out.Status = StatusFailed
				out.logf("删除规则失败：%v；恢复也失败：%v", err, backErr)
				return *out
			}
			out.Status = StatusRolledBack
			out.logf("删除规则失败：%v；已恢复原规则", err)
			return *out
		}
		deleted = append(deleted, r)
	}
	after, err := c.SecurityGroupIngress(ctx, s.Region, v["group"])
	if err == nil {
		for _, r := range after {
			if unapprovedSensitiveRule(r, sshPort, network.String()) {
				err = fmt.Errorf("管理端口或 3306 仍可被非授权来源访问：%s", ruleText(r))
				break
			}
		}
	}
	if err != nil {
		if backErr := restore(); backErr != nil {
			out.Status = StatusFailed
			out.logf("复核失败：%v；恢复原规则也失败：%v", err, backErr)
			return *out
		}
		out.Status = StatusRolledBack
		out.logf("复核失败：%v；已恢复原规则", err)
		return *out
	}
	b, _ := json.Marshal(deleted)
	a, _ := json.Marshal(added)
	out.Undo = map[string]string{"region": s.Region, "instance": s.ID, "group": v["group"], "removed": string(b), "added": string(a)}
	out.Status = StatusDone
	report("完成：3306 不再通过这条安全组规则对外开放")
	return *out
}

func undoFirewallTighten(ctx context.Context, c *tencent.Client, undo map[string]string) error {
	var removed []tencent.FirewallRule
	var added []tencent.FirewallRule
	if err := json.Unmarshal([]byte(undo["removed"]), &removed); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(undo["added"]), &added); err != nil {
		return err
	}
	sort.Slice(removed, func(i, j int) bool { return removed[i].Index < removed[j].Index })
	for _, r := range removed {
		if err := c.InsertSecurityGroupIngress(ctx, undo["region"], undo["group"], r); err != nil {
			return err
		}
	}
	for _, r := range added {
		if err := c.DeleteSecurityGroupIngress(ctx, undo["region"], undo["group"], r); err != nil {
			return err
		}
	}
	return nil
}

func applySnapshot(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	s, err := findInstance(ctx, c, v["region"], v["instance"])
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	name := v["name"]
	if name == "" {
		name = "miaopanel-" + time.Now().Format("20060102-1504")
	}
	report("正在给 %s 的系统盘创建快照 %s", serverText(s), name)
	id, err := c.CreateSnapshot(ctx, s.Region, s, name)
	if err != nil {
		out.Status = StatusRefused
		out.logf("创建快照失败：%v", err)
		return *out
	}
	var last tencent.Snapshot
	ok, waitErr := waitFor(ctx, env, 60, func() (bool, error) {
		list, err := c.Snapshots(ctx, s.Region, s)
		for _, sn := range list {
			if sn.ID == id {
				last = sn
			}
		}
		return err == nil && last.State == "NORMAL", err
	})
	if !ok {
		out.Status = StatusFailed
		if waitErr != nil {
			out.logf("快照 %s（%s）已提交，但查询可用状态失败：%v；请在腾讯云控制台确认", name, id, waitErr)
		} else {
			out.logf("快照 %s（%s）已提交，但尚未确认可用（状态 %s，进度 %d%%）；请在腾讯云控制台确认", name, id, last.State, last.Percent)
		}
		return *out
	}
	report("完成：快照 %s（%s）已创建好，出问题时可以在腾讯云控制台用它回滚整台服务器", name, id)
	out.Status = StatusDone
	out.Undo = map[string]string{}
	return *out
}

var powerAction = map[string]struct{ call, want, doing, done string }{
	"start":  {"StartInstances", "RUNNING", "开机", "已开机"},
	"stop":   {"StopInstances", "STOPPED", "关机", "已关机"},
	"reboot": {"RebootInstances", "RUNNING", "重启", "已重启并正常运行"},
}

func applyPower(ctx context.Context, env *Env, op string, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	p := powerAction[op]
	s, err := findInstance(ctx, c, v["region"], v["instance"])
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	switch {
	case op != "reboot" && s.State == p.want:
		report("%s 现在已经是%s状态，不需要操作", serverText(s), map[string]string{"RUNNING": "运行", "STOPPED": "关机"}[s.State])
		out.Status, out.Undo = StatusDone, map[string]string{}
		return *out
	case op != "start" && s.State != "RUNNING":
		out.Status = StatusRefused
		out.logf("%s 当前状态是 %s，不是运行中，不能%s", serverText(s), s.State, p.doing)
		return *out
	}
	report("正在%s %s", p.doing, serverText(s))
	if err := c.Power(ctx, s.Region, s.ID, p.call); err != nil {
		out.Status = StatusRefused
		out.logf("%s失败：%v", p.doing, err)
		return *out
	}
	out.Undo["region"], out.Undo["instance"] = s.Region, s.ID
	var state string
	// Reboots first leave RUNNING; wait a moment before checking.
	if op == "reboot" {
		time.Sleep(min(3*time.Second, 10*pollEvery(env)))
	}
	ok, _ := waitFor(ctx, env, 36, func() (bool, error) {
		now, found, err := c.Instance(ctx, s.Region, s.ID)
		state = now.State
		return found && now.State == p.want, err
	})
	if !ok {
		out.Status = StatusFailed
		out.logf("等了几分钟，%s 的状态还是 %s，请到腾讯云控制台查看", serverText(s), state)
		return *out
	}
	report("完成：%s %s", serverText(s), p.done)
	out.Status = StatusDone
	return *out
}

// ---- EdgeOne ----

// splitTargets splits a list written with newlines, commas or spaces.
func splitTargets(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '，' || r == '\n' || r == ' ' || r == ';' }) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// inZone reports whether host belongs to an EdgeOne site.
func inZone(host string, z tencent.Zone) bool {
	host = strings.ToLower(host)
	return host == z.ZoneName || strings.HasSuffix(host, "."+z.ZoneName)
}

func applyPurge(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	z, err := findZone(ctx, c, v["domain"])
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	kind := v["type"]
	targets := splitTargets(v["targets"])
	switch kind {
	case "url", "prefix":
		if len(targets) == 0 {
			out.Status = StatusRefused
			out.logf("要写清除哪些地址（完整的 https:// 地址）")
			return *out
		}
		for _, t := range targets {
			u, err := url.Parse(t)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !inZone(u.Hostname(), z) {
				out.Status = StatusRefused
				out.logf("%s 不是站点 %s 下的完整网址（要以 https:// 开头）", t, z.ZoneName)
				return *out
			}
		}
	case "host":
		if len(targets) == 0 {
			targets = []string{v["domain"]}
		}
		for _, t := range targets {
			if !inZone(t, z) {
				out.Status = StatusRefused
				out.logf("%s 不属于站点 %s", t, z.ZoneName)
				return *out
			}
		}
	case "all":
		targets = nil
	}
	what := map[string]string{"url": "这些网址", "prefix": "这些目录", "host": "这些域名", "all": "整个站点"}[kind]
	report("正在清除 EdgeOne 站点 %s 上%s的缓存：%s", z.ZoneName, what, strings.Join(targets, "、"))
	job, failed, err := c.Purge(ctx, z.ZoneID, "purge_"+kind, v["method"], targets)
	if err != nil {
		out.Status = StatusRefused
		out.logf("提交失败：%v", err)
		return *out
	}
	if len(failed) > 0 {
		report("有 %d 个没有提交成功：%s", len(failed), strings.Join(failed, "；"))
	}
	report("完成：已提交清除任务（%s），一般几分钟内在全部节点生效，之后的访问会重新从源站拉取", job)
	out.Status, out.Undo = StatusDone, map[string]string{}
	return *out
}

func applyPrefetch(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	z, err := findZone(ctx, c, v["domain"])
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	targets := splitTargets(v["targets"])
	for _, t := range targets {
		u, err := url.Parse(t)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !inZone(u.Hostname(), z) {
			out.Status = StatusRefused
			out.logf("%s 不是站点 %s 下的完整网址（要以 https:// 开头）", t, z.ZoneName)
			return *out
		}
	}
	report("正在把 %d 个网址预热到 EdgeOne 节点", len(targets))
	job, err := c.Prefetch(ctx, z.ZoneID, targets)
	if err != nil {
		out.Status = StatusRefused
		out.logf("提交失败：%v", err)
		return *out
	}
	report("完成：已提交预热任务（%s）", job)
	out.Status, out.Undo = StatusDone, map[string]string{}
	return *out
}

func applyDomainStatus(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	name, status := v["domain"], v["status"]
	z, err := findZone(ctx, c, name)
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	d, ok, err := c.AccelerationDomain(ctx, z.ZoneID, name)
	if err != nil || !ok {
		out.Status = StatusRefused
		out.logf("EdgeOne 里没有加速域名 %s（%v）", name, err)
		return *out
	}
	if d.DomainStatus == status {
		report("%s 已经是 %s 状态，不需要修改", name, status)
		out.Status, out.Undo = StatusDone, map[string]string{}
		return *out
	}
	if status == "offline" {
		report("停用后 EdgeOne 不再为 %s 提供服务，解析到 EdgeOne 的访问会失败", name)
	}
	if err := c.SetAccelerationDomainStatus(ctx, z.ZoneID, []string{name}, status); err != nil {
		out.Status = StatusRefused
		out.logf("修改失败：%v", err)
		return *out
	}
	prev := d.DomainStatus
	if prev != "offline" {
		prev = "online"
	}
	out.Undo["zone_id"], out.Undo["domain"], out.Undo["status"] = z.ZoneID, name, prev
	report("完成：%s 已%s", name, map[string]string{"online": "启用", "offline": "停用"}[status])
	out.Status = StatusDone
	return *out
}

func applyOrigin(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	name := v["domain"]
	z, err := findZone(ctx, c, name)
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	d, ok, err := c.AccelerationDomain(ctx, z.ZoneID, name)
	if err != nil || !ok {
		out.Status = StatusRefused
		out.logf("EdgeOne 里没有加速域名 %s（%v）", name, err)
		return *out
	}
	if d.OriginDetail.OriginType != "" && d.OriginDetail.OriginType != "IP_DOMAIN" {
		out.Status = StatusRefused
		out.logf("%s 的源站类型是 %s，只能在控制台修改", name, d.OriginDetail.OriginType)
		return *out
	}
	httpPort, _ := strconv.Atoi(v["http_port"])
	httpsPort, _ := strconv.Atoi(v["https_port"])
	if d.OriginDetail.Origin == v["origin"] && (v["origin_protocol"] == "" || v["origin_protocol"] == d.OriginProtocol) &&
		(httpPort == 0 || uint64(httpPort) == d.HTTPOriginPort) && (httpsPort == 0 || uint64(httpsPort) == d.HTTPSOriginPort) {
		report("%s 已经回源到 %s，不需要修改", name, v["origin"])
		out.Status, out.Undo = StatusDone, map[string]string{}
		return *out
	}
	report("正在把 %s 的源站从 %s（%s）改为 %s", name, d.OriginDetail.Origin, d.OriginProtocol, v["origin"])
	if err := c.SetOrigin(ctx, z.ZoneID, name, v["origin"], v["origin_protocol"], httpPort, httpsPort, d.OriginDetail.HostHeader); err != nil {
		out.Status = StatusRefused
		out.logf("修改失败：%v", err)
		return *out
	}
	out.Undo["zone_id"], out.Undo["domain"], out.Undo["origin"], out.Undo["protocol"] = z.ZoneID, name, d.OriginDetail.Origin, d.OriginProtocol
	out.Undo["http_port"], out.Undo["https_port"] = strconv.FormatUint(d.HTTPOriginPort, 10), strconv.FormatUint(d.HTTPSOriginPort, 10)
	out.Undo["host_header"] = d.OriginDetail.HostHeader
	report("完成：%s 现在回源到 %s", name, v["origin"])
	out.Status = StatusDone
	return *out
}

func undoOrigin(ctx context.Context, c *tencent.Client, undo map[string]string) error {
	hp, _ := strconv.Atoi(undo["http_port"])
	hsp, _ := strconv.Atoi(undo["https_port"])
	return c.SetOrigin(ctx, undo["zone_id"], undo["domain"], undo["origin"], undo["protocol"], hp, hsp, undo["host_header"])
}

// checkCIDR validates an IPv4 address or network.
func checkCIDR(s string) error {
	if ip, _, err := net.ParseCIDR(s); err == nil && ip.To4() != nil {
		return nil
	}
	if ip := net.ParseIP(s); ip != nil && ip.To4() != nil {
		return nil
	}
	return fmt.Errorf("来源 %q 要是 IPv4 地址或网段，例如 0.0.0.0/0", s)
}
