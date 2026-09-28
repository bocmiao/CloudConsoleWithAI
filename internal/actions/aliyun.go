package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
)

// ---- 阿里云 servers: ECS and Simple Application Server ----
//
// The same changes as for Tencent Cloud servers: power, a snapshot of the
// system disk, and opening or closing a port in the firewall (an ECS
// instance's first security group). Their Impl.Cloud names start with
// "ali_" and run with Env.Aliyun.

var aliInstanceRe = regexp.MustCompile(`^(i-[a-z0-9]{8,30}|[0-9a-f]{32})$`)

func aliServerText(s aliyun.Server) string {
	kind := map[string]string{aliyun.KindECS: "云服务器 ECS", aliyun.KindSWAS: "轻量应用服务器"}[s.Kind]
	name := s.Name
	if name == "" {
		name = s.ID
	}
	return fmt.Sprintf("阿里云%s %s", kind, name)
}

func aliFind(ctx context.Context, c *aliyun.Client, region, id string) (aliyun.Server, error) {
	s, ok, err := c.Instance(ctx, region, id)
	if err != nil {
		return s, fmt.Errorf("读取实例失败：%w", err)
	}
	if !ok {
		return s, fmt.Errorf("在 %s 找不到实例 %s，可能已经释放", region, id)
	}
	return s, nil
}

// traceAliyun records every 阿里云 call an operation makes.
func traceAliyun(env *Env, run func() Outcome) Outcome {
	var cmds []string
	env.Aliyun.Trace = func(product, action string, params map[string]string) {
		b, _ := json.Marshal(params)
		cmds = append(cmds, product+" "+action+" "+string(b))
	}
	defer func() { env.Aliyun.Trace = nil }()
	out := run()
	out.Commands = append([]string{"# 调用阿里云 OpenAPI（请求带签名，密钥不记录）"}, cmds...)
	return out
}

func applyAliyun(ctx context.Context, env *Env, r Resolved, progress Progress) Outcome {
	out := &Outcome{Undo: map[string]string{}}
	if env.Aliyun == nil {
		out.Status = StatusRefused
		out.logf("还没有配置阿里云 AccessKey（设置 → 阿里云）")
		return *out
	}
	report := func(format string, args ...any) {
		out.logf(format, args...)
		if progress != nil {
			progress(out.Log)
		}
	}
	return traceAliyun(env, func() Outcome {
		v := r.Values
		switch r.Impl.Cloud {
		case "ali_power_start", "ali_power_stop", "ali_power_reboot":
			return aliPower(ctx, env, strings.TrimPrefix(r.Impl.Cloud, "ali_power_"), v, out, report)
		case "ali_snapshot":
			return aliSnapshot(ctx, env, v, out, report)
		case "ali_firewall_open":
			return aliFirewallOpen(ctx, env, v, out, report)
		case "ali_firewall_close":
			return aliFirewallClose(ctx, env, v, out, report)
		}
		if strings.HasPrefix(r.Impl.Cloud, "ali_dns_") {
			return aliDNSApply(ctx, env, r.Impl.Cloud, v, out, report)
		}
		if strings.HasPrefix(r.Impl.Cloud, "ali_cdn_") {
			return aliCDNApply(ctx, env, r.Impl.Cloud, v, out, report)
		}
		out.Status = StatusFailed
		out.logf("未知的阿里云操作 %s", r.Impl.Cloud)
		return *out
	})
}

func undoAliyun(ctx context.Context, env *Env, r Resolved, undo map[string]string) Outcome {
	out := Outcome{}
	if len(undo) == 0 {
		out.Status = StatusUndone
		out.logf("这一步当时没有做任何修改，不需要撤销")
		return out
	}
	if env.Aliyun == nil {
		out.Status = StatusRefused
		out.logf("还没有配置阿里云 AccessKey（设置 → 阿里云）")
		return out
	}
	return traceAliyun(env, func() Outcome {
		c := env.Aliyun
		var err error
		switch r.Impl.Cloud {
		case "ali_power_start":
			err = c.Power(ctx, undo["region"], undo["instance"], "stop")
		case "ali_power_stop":
			err = c.Power(ctx, undo["region"], undo["instance"], "start")
		case "ali_firewall_open", "ali_firewall_close":
			err = aliUndoFirewall(ctx, c, undo)
		case "ali_dns_add", "ali_dns_modify", "ali_dns_delete", "ali_dns_status", "ali_dns_set":
			err = aliDNSUndo(ctx, c, r.Impl.Cloud, undo)
		default:
			err = fmt.Errorf("未知的阿里云操作 %s", r.Impl.Cloud)
		}
		if err != nil {
			out.Status = StatusFailed
			out.logf("撤销失败：%v", err)
			return out
		}
		out.Status = StatusUndone
		out.logf("已撤销")
		return out
	})
}

var aliPowerText = map[string]struct{ want, doing, done string }{
	"start":  {"RUNNING", "开机", "已开机"},
	"stop":   {"STOPPED", "关机", "已关机"},
	"reboot": {"RUNNING", "重启", "已重启并正常运行"},
}

func aliPower(ctx context.Context, env *Env, op string, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Aliyun
	p := aliPowerText[op]
	s, err := aliFind(ctx, c, v["region"], v["instance"])
	if err != nil {
		return refused(out, "%v", err)
	}
	// Already on its way there (say, just undone): wait for it.
	if op != "reboot" && s.State == map[string]string{"RUNNING": "STARTING", "STOPPED": "STOPPING"}[p.want] {
		report("%s 正在%s，等它完成", aliServerText(s), p.doing)
		if ok, _ := waitFor(ctx, env, 36, func() (bool, error) {
			now, found, err := c.Instance(ctx, s.Region, s.ID)
			s.State = now.State
			return found && now.State == p.want, err
		}); !ok {
			out.Status = StatusFailed
			out.logf("等了几分钟，%s 的状态还是 %s，请到阿里云控制台查看", aliServerText(s), s.State)
			return *out
		}
	}
	switch {
	case op != "reboot" && s.State == p.want:
		report("%s 现在已经是%s状态，不需要操作", aliServerText(s), map[string]string{"RUNNING": "运行", "STOPPED": "关机"}[s.State])
		out.Status, out.Undo = StatusDone, map[string]string{}
		return *out
	case op != "start" && s.State != "RUNNING":
		return refused(out, "%s 当前状态是 %s，不是运行中，不能%s", aliServerText(s), s.State, p.doing)
	case op == "start" && s.State != "STOPPED":
		return refused(out, "%s 当前状态是 %s，不是已关机，不能开机", aliServerText(s), s.State)
	}
	report("正在%s %s", p.doing, aliServerText(s))
	if err := c.Power(ctx, s.Region, s.ID, op); err != nil {
		return refused(out, "%s失败：%v", p.doing, err)
	}
	out.Undo["region"], out.Undo["instance"] = s.Region, s.ID
	if op == "reboot" {
		time.Sleep(min(3*time.Second, 10*pollEvery(env)))
	}
	var state string
	ok, _ := waitFor(ctx, env, 36, func() (bool, error) {
		now, found, err := c.Instance(ctx, s.Region, s.ID)
		state = now.State
		return found && now.State == p.want, err
	})
	if !ok {
		out.Status = StatusFailed
		out.logf("等了几分钟，%s 的状态还是 %s，请到阿里云控制台查看", aliServerText(s), state)
		return *out
	}
	report("完成：%s %s", aliServerText(s), p.done)
	out.Status = StatusDone
	return *out
}

func aliSnapshot(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Aliyun
	s, err := aliFind(ctx, c, v["region"], v["instance"])
	if err != nil {
		return refused(out, "%v", err)
	}
	name := v["name"]
	if name == "" {
		name = "miaopanel-" + time.Now().Format("20060102-1504")
	}
	report("正在给 %s 的系统盘创建快照 %s", aliServerText(s), name)
	id, err := c.CreateSnapshot(ctx, s, name)
	if err != nil {
		return refused(out, "创建快照失败：%v", err)
	}
	var last aliyun.Snapshot
	ok, waitErr := waitFor(ctx, env, 60, func() (bool, error) {
		list, err := c.Snapshots(ctx, s)
		for _, sn := range list {
			if sn.ID == id {
				last = sn
			}
		}
		return err == nil && last.State == "ACCOMPLISHED", err
	})
	if last.State == "FAILED" {
		out.Status = StatusFailed
		out.logf("快照 %s（%s）创建失败，请在阿里云控制台查看", name, id)
		return *out
	}
	if !ok {
		out.Status = StatusFailed
		if waitErr != nil {
			out.logf("快照 %s（%s）已提交，但查询进度失败：%v；请在阿里云控制台确认", name, id, waitErr)
		} else {
			out.logf("快照 %s（%s）已提交，还没完成（进度 %d%%）；请稍后在阿里云控制台确认", name, id, last.Progress)
		}
		return *out
	}
	report("完成：快照 %s（%s）已创建好，出问题时可以在阿里云控制台用它回滚系统盘", name, id)
	out.Status, out.Undo = StatusDone, map[string]string{}
	return *out
}

func aliRule(v map[string]string) aliyun.FirewallRule {
	desc := v["description"]
	if desc == "" {
		desc = "Miao Panel"
	}
	proto := v["protocol"]
	if proto == "" {
		proto = "TCP"
	}
	src := v["cidr"]
	if src == "" {
		src = "0.0.0.0/0"
	}
	return aliyun.FirewallRule{Protocol: proto, Port: v["port"], Source: src, Policy: "accept", Description: desc}
}

// aliSame says whether a listed rule lets the same traffic in as want.
func aliSame(r, want aliyun.FirewallRule) bool {
	return strings.EqualFold(r.Protocol, want.Protocol) && r.Port == want.Port && r.Source == want.Source && strings.EqualFold(r.Policy, "accept")
}

func aliRuleText(r aliyun.FirewallRule) string {
	port := r.Port
	if port == "" {
		port = "全部端口"
	}
	return fmt.Sprintf("%s %s 来源 %s", strings.ToUpper(r.Protocol), port, r.Source)
}

func aliFirewallOpen(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Aliyun
	s, err := aliFind(ctx, c, v["region"], v["instance"])
	if err != nil {
		return refused(out, "%v", err)
	}
	rules, err := c.Firewall(ctx, s)
	if err != nil {
		return refused(out, "读取防火墙规则失败：%v", err)
	}
	rule := aliRule(v)
	for _, r := range rules {
		if aliSame(r, rule) {
			report("%s 已经放行 %s，不需要修改", aliServerText(s), aliRuleText(rule))
			out.Status, out.Undo = StatusDone, map[string]string{}
			return *out
		}
	}
	if s.Kind == aliyun.KindECS && len(s.SecurityGroups) > 0 {
		report("ECS 的防火墙是安全组 %s；加入同一个安全组的其他服务器也会放行这个端口", s.SecurityGroups[0])
	}
	report("正在给 %s 放行 %s", aliServerText(s), aliRuleText(rule))
	if err := c.AddFirewallRule(ctx, s, rule); err != nil {
		return refused(out, "添加规则失败：%v", err)
	}
	b, _ := json.Marshal(rule)
	out.Undo["region"], out.Undo["instance"], out.Undo["rule"] = s.Region, s.ID, string(b)
	report("完成：已放行 %s", aliRuleText(rule))
	out.Status = StatusDone
	return *out
}

func aliFirewallClose(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Aliyun
	s, err := aliFind(ctx, c, v["region"], v["instance"])
	if err != nil {
		return refused(out, "%v", err)
	}
	want := aliRule(v)
	if p := LoginPortIn(want.Port); p != 0 || want.Port == "" {
		return refused(out, "远程登录端口（22、3389）或全部端口不能在这里关闭，关掉会连不上服务器")
	}
	rules, err := c.Firewall(ctx, s)
	if err != nil {
		return refused(out, "读取防火墙规则失败：%v", err)
	}
	var removed []aliyun.FirewallRule
	for _, r := range rules {
		if !aliSame(r, want) {
			continue
		}
		report("正在删除 %s 的规则 %s", aliServerText(s), aliRuleText(r))
		if err := c.DeleteFirewallRule(ctx, s, r); err != nil {
			for _, back := range removed {
				back.ID = ""
				_ = c.AddFirewallRule(ctx, s, back)
			}
			out.Status = StatusRolledBack
			out.logf("删除失败：%v（已删除的规则已加回）", err)
			return *out
		}
		removed = append(removed, r)
	}
	if len(removed) == 0 {
		report("%s 没有放行 %s 的规则，不需要修改", aliServerText(s), aliRuleText(want))
		out.Status, out.Undo = StatusDone, map[string]string{}
		return *out
	}
	b, _ := json.Marshal(removed)
	out.Undo["region"], out.Undo["instance"], out.Undo["removed"] = s.Region, s.ID, string(b)
	report("完成：已关闭 %s", aliRuleText(want))
	out.Status = StatusDone
	return *out
}

func aliUndoFirewall(ctx context.Context, c *aliyun.Client, undo map[string]string) error {
	s, err := aliFind(ctx, c, undo["region"], undo["instance"])
	if err != nil {
		return err
	}
	if undo["rule"] != "" {
		var want aliyun.FirewallRule
		if err := json.Unmarshal([]byte(undo["rule"]), &want); err != nil {
			return err
		}
		rules, err := c.Firewall(ctx, s)
		if err != nil {
			return err
		}
		for _, r := range rules {
			if aliSame(r, want) {
				return c.DeleteFirewallRule(ctx, s, r)
			}
		}
		return nil // already gone
	}
	var rules []aliyun.FirewallRule
	if err := json.Unmarshal([]byte(undo["removed"]), &rules); err != nil {
		return err
	}
	for _, r := range rules {
		r.ID = ""
		if err := c.AddFirewallRule(ctx, s, r); err != nil {
			return err
		}
	}
	return nil
}

// aliPort accepts one port or one range: 阿里云 rules take no lists.
func aliPort(v map[string]string) error {
	p := v["port"]
	if p == "ALL" || strings.Contains(p, ",") {
		return fmt.Errorf("阿里云的一条规则只能是一个端口（80）或一段范围（8000-8100），%q 不行", p)
	}
	return nil
}

func init() {
	inst := Param{Name: "instance", Kind: "aliinstance", Required: true, Desc: "阿里云实例 ID（aliyun_servers 返回的 id：i- 开头是云服务器 ECS，32 位十六进制是轻量应用服务器）"}
	region := Param{Name: "region", Kind: "region", Required: true, Desc: "实例所在地域，例如 cn-hangzhou（aliyun_servers 返回的 region）"}
	impl := func(op, downtime, undo string) map[string]Impl {
		return map[string]Impl{"*": {Via: "阿里云接口", Cloud: op, Downtime: downtime, Undo: undo}}
	}
	register(&Capability{
		Name: "aliyun.server.start", Title: "启动阿里云服务器", Risk: core.R2, Reversible: true,
		Params: []Param{inst, region},
		Impls:  impl("ali_power_start", "开机需要一两分钟", "重新关机"),
	})
	register(&Capability{
		Name: "aliyun.server.stop", Title: "关闭阿里云服务器", Risk: core.R3, Reversible: true,
		Params: []Param{inst, region},
		Impls:  impl("ali_power_stop", "整台服务器上的网站和服务全部停止，直到重新开机；按量付费的 ECS 关机后继续计费（保留公网 IP）", "重新开机"),
	})
	register(&Capability{
		Name: "aliyun.server.reboot", Title: "重启阿里云服务器", Risk: core.R3,
		NoUndo: "重启没有修改任何配置，不需要回滚",
		Params: []Param{inst, region},
		Impls:  impl("ali_power_reboot", "整台服务器上的网站和服务中断一到几分钟", ""),
	})
	register(&Capability{
		Name: "aliyun.snapshot.create", Title: "创建阿里云服务器快照", Risk: core.R1,
		NoUndo: "快照是新增的系统盘备份，不需要回滚；不再需要时可以在阿里云控制台删除（快照按容量收费，轻量服务器有免费额度）",
		Params: []Param{inst, region, {Name: "name", Kind: "text", Desc: "快照名称，不填自动生成"}},
		Impls:  impl("ali_snapshot", "不影响运行", ""),
		RiskFor: func(v map[string]string) core.Risk {
			if strings.HasPrefix(v["instance"], "i-") {
				return core.R2 // ECS snapshots are billed by size
			}
			return core.R1
		},
	})
	register(&Capability{
		Name: "aliyun.firewall.open", Title: "阿里云防火墙放行端口", Risk: core.R1, Reversible: true,
		Params: []Param{inst, region,
			{Name: "port", Kind: "port", Required: true, Desc: "一个端口（80）或一段范围（8000-8100）"},
			{Name: "protocol", Kind: "enum", Enum: []string{"TCP", "UDP"}, Default: "TCP", Desc: "协议"},
			{Name: "cidr", Kind: "cidr", Default: "0.0.0.0/0", Desc: "允许哪些来源访问：0.0.0.0/0 表示所有人，也可以只写一个 IP"},
			{Name: "description", Kind: "text", Desc: "规则备注"}},
		Impls: impl("ali_firewall_open", "不影响现有访问", "删除这条放行规则"),
		Check: aliPort,
		RiskFor: func(v map[string]string) core.Risk {
			if v["cidr"] != "" && v["cidr"] != "0.0.0.0/0" {
				return core.R1
			}
			for _, p := range []int{3306, 5432, 6379, 27017, 9200, 11211, 1433} {
				if portCovers(v["port"], p) {
					return core.R3
				}
			}
			return core.R1
		},
	})
	register(&Capability{
		Name: "aliyun.firewall.close", Title: "阿里云防火墙关闭端口", Risk: core.R2, Reversible: true,
		Params: []Param{inst, region,
			{Name: "port", Kind: "port", Required: true, Desc: "要关闭的端口（22、3389 远程登录端口不允许关闭）"},
			{Name: "protocol", Kind: "enum", Enum: []string{"TCP", "UDP"}, Default: "TCP", Desc: "协议"},
			{Name: "cidr", Kind: "cidr", Default: "0.0.0.0/0", Desc: "要删除的规则的来源，默认 0.0.0.0/0"}},
		Impls: impl("ali_firewall_close", "用这个端口的访问会被拒绝", "把删掉的放行规则加回去"),
		Check: aliPort,
	})
}
