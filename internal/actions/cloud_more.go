package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
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
	cidr := r.CidrBlock
	if cidr == "" {
		cidr = "0.0.0.0/0"
	}
	return fmt.Sprintf("%s %s 来源 %s %s", r.Protocol, r.Port, cidr, r.Action)
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
	for _, r := range rules {
		r.Index = 0
		if err := addRule(ctx, c, s, undo["group"], r); err != nil {
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
	ok, _ := waitFor(ctx, env, 60, func() (bool, error) {
		list, err := c.Snapshots(ctx, s.Region, s)
		for _, sn := range list {
			if sn.ID == id {
				last = sn
			}
		}
		return last.State == "NORMAL", err
	})
	if ok {
		report("完成：快照 %s（%s）已创建好，出问题时可以在腾讯云控制台用它回滚整台服务器", name, id)
	} else {
		report("快照 %s（%s）已提交，还在创建中（%d%%），大的磁盘需要十几分钟", name, id, last.Percent)
	}
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
	if err := c.SetOrigin(ctx, z.ZoneID, name, v["origin"], v["origin_protocol"], httpPort, httpsPort); err != nil {
		out.Status = StatusRefused
		out.logf("修改失败：%v", err)
		return *out
	}
	out.Undo["zone_id"], out.Undo["domain"], out.Undo["origin"], out.Undo["protocol"] = z.ZoneID, name, d.OriginDetail.Origin, d.OriginProtocol
	out.Undo["http_port"], out.Undo["https_port"] = strconv.FormatUint(d.HTTPOriginPort, 10), strconv.FormatUint(d.HTTPSOriginPort, 10)
	report("完成：%s 现在回源到 %s", name, v["origin"])
	out.Status = StatusDone
	return *out
}

func undoOrigin(ctx context.Context, c *tencent.Client, undo map[string]string) error {
	hp, _ := strconv.Atoi(undo["http_port"])
	hsp, _ := strconv.Atoi(undo["https_port"])
	return c.SetOrigin(ctx, undo["zone_id"], undo["domain"], undo["origin"], undo["protocol"], hp, hsp)
}

// checkCIDR validates an IPv4 address or network.
func checkCIDR(s string) error {
	if _, _, err := net.ParseCIDR(s); err == nil {
		return nil
	}
	if ip := net.ParseIP(s); ip != nil && ip.To4() != nil {
		return nil
	}
	return fmt.Errorf("来源 %q 要是 IPv4 地址或网段，例如 0.0.0.0/0", s)
}
