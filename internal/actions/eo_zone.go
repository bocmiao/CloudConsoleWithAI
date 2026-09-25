package actions

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// ---- EdgeOne sites ----

var planNames = map[string]string{
	"plan-trial": "试用版", "plan-personal": "个人版", "plan-basic": "基础版", "plan-standard": "标准版", "plan-enterprise": "企业版",
}

var areaNames = map[string]string{"mainland": "中国大陆", "overseas": "全球（不含中国大陆）", "global": "全球（含中国大陆）"}

// pickPlan finds a plan that can take a new site in area: the one asked
// for, or any usable plan covering the area.
func pickPlan(plans []tencent.Plan, area, want string) (tencent.Plan, error) {
	usable := func(p tencent.Plan) bool {
		return p.Bindable == "true" && (p.Status == "normal" || p.Status == "expiring-soon")
	}
	covers := func(p tencent.Plan) bool { return p.Area == area || p.Area == "global" }
	for _, p := range plans {
		if want != "" && p.PlanID == want {
			switch {
			case !usable(p):
				return p, fmt.Errorf("套餐 %s 现在不能再绑定站点（状态 %s，可绑定 %s）", want, p.Status, p.Bindable)
			case !covers(p):
				return p, fmt.Errorf("套餐 %s 的服务区域是%s，不包括%s", want, areaNames[p.Area], areaNames[area])
			}
			return p, nil
		}
	}
	if want != "" {
		return tencent.Plan{}, fmt.Errorf("账号里没有套餐 %s", want)
	}
	for _, p := range plans {
		if usable(p) && covers(p) {
			return p, nil
		}
	}
	return tencent.Plan{}, fmt.Errorf("账号里没有能绑定新站点、服务区域包括%s的 EdgeOne 套餐。请先在 EdgeOne 控制台购买或领取套餐（涉及计费，需要你自己操作），然后再执行这一步", areaNames[area])
}

func applyZoneCreate(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	name, area := strings.TrimSuffix(v["domain"], "."), v["area"]
	zones, err := c.Zones(ctx)
	if err != nil {
		out.Status = StatusRefused
		out.logf("查询 EdgeOne 站点失败：%v", err)
		return *out
	}
	for _, z := range zones {
		if z.ZoneName != name {
			continue
		}
		dv := z.Verification()
		if dv == nil {
			report("EdgeOne 里已经有站点 %s 了（%s，状态 %s），不需要新建", name, areaNames[z.Area], z.Status)
			out.Status, out.Undo = StatusDone, map[string]string{}
			return *out
		}
		report("EdgeOne 里已经有站点 %s，但还没有验证域名归属", name)
		verifyZone(ctx, env, name, dv, out, report)
		out.Status = StatusDone
		return *out
	}
	plans, err := c.Plans(ctx)
	if err != nil {
		out.Status = StatusRefused
		out.logf("查询 EdgeOne 套餐失败：%v", err)
		return *out
	}
	plan, err := pickPlan(plans, area, v["plan_id"])
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	if area != "overseas" {
		report("加速区域包括中国大陆，域名需要已经完成 ICP 备案")
	}
	report("正在新建 EdgeOne 站点 %s：CNAME 接入，加速区域%s，绑定%s套餐 %s", name, areaNames[area], planNames[plan.PlanType], plan.PlanID)
	zoneID, dv, err := c.CreateZone(ctx, name, area, plan.PlanID)
	if err != nil {
		out.Status = StatusRefused
		out.logf("新建失败：%v", err)
		return *out
	}
	out.Undo["zone_id"], out.Undo["zone_name"] = zoneID, name
	report("已新建站点（%s）", zoneID)
	if dv != nil {
		verifyZone(ctx, env, name, dv, out, report)
	}
	report("完成：接下来可以添加加速域名（eo.domain.add）")
	out.Status = StatusDone
	return *out
}

// verifyZone proves the domain is yours: when the domain is on DNSPod in
// this account it adds the TXT record EdgeOne asked for and triggers the
// check; otherwise it tells the user which record to add.
func verifyZone(ctx context.Context, env *Env, name string, dv *tencent.DNSVerification, out *Outcome, report func(string, ...any)) {
	c := env.Cloud
	manual := func(why string) {
		report("%s。请在域名的 DNS 服务商那里添加 %s 记录：主机记录 %s，值 %s，然后在 EdgeOne 控制台点「验证」", why, dv.RecordType, dv.Subdomain, dv.RecordValue)
	}
	domains, err := c.Domains(ctx)
	if err != nil {
		manual(fmt.Sprintf("查询 DNSPod 失败（%v）", err))
		return
	}
	onDNSPod := false
	for _, d := range domains {
		if d.Name == name {
			onDNSPod = true
		}
	}
	if !onDNSPod {
		manual("域名不在这个账号的 DNSPod 里")
		return
	}
	sub := strings.TrimSuffix(strings.TrimSuffix(dv.Subdomain, "."+name), ".")
	existing, err := c.Records(ctx, name, sub)
	if err != nil {
		manual(fmt.Sprintf("查询 DNSPod 解析失败（%v）", err))
		return
	}
	found := false
	for _, r := range existing {
		if r.Type == dv.RecordType && strings.Trim(r.Value, `"`) == dv.RecordValue {
			found = true
		}
	}
	if !found {
		report("正在 DNSPod 添加验证记录：%s %s %s", sub, dv.RecordType, dv.RecordValue)
		id, err := c.CreateRecord(ctx, name, tencent.Record{Name: sub, Type: dv.RecordType, Value: dv.RecordValue, Line: tencent.DefaultLine, TTL: 600})
		if err != nil {
			manual(fmt.Sprintf("添加验证记录失败（%v）", err))
			return
		}
		out.Undo["dnspod_domain"], out.Undo["txt_record_id"] = name, strconv.FormatUint(id, 10)
	}
	report("正在请 EdgeOne 验证域名归属")
	var reason string
	ok, _ := waitFor(ctx, env, 12, func() (bool, error) {
		status, result, err := c.VerifyOwnership(ctx, name)
		reason = result
		return status == "success", err
	})
	if ok {
		report("域名归属验证通过")
		return
	}
	report("EdgeOne 暂时没有验证通过（%s）。DNS 生效可能要几分钟，稍后可以在 EdgeOne 控制台点「验证」，或让 AI 再执行一次这一步", reason)
}

func undoZoneCreate(ctx context.Context, env *Env, undo map[string]string) error {
	c := env.Cloud
	if zone := undo["zone_id"]; zone != "" {
		domains, err := c.AccelerationDomains(ctx, zone, "")
		if err != nil {
			return err
		}
		if len(domains) > 0 {
			var names []string
			for _, d := range domains {
				names = append(names, d.DomainName)
			}
			return fmt.Errorf("站点里已经有加速域名 %s，删除站点会让它们一起失效。请先回滚或删除这些域名", strings.Join(names, "、"))
		}
		if err := c.SetZonePaused(ctx, zone, true); err != nil {
			return fmt.Errorf("停用站点失败：%w", err)
		}
		var err2 error
		ok, _ := waitFor(ctx, env, 12, func() (bool, error) {
			err2 = c.DeleteZone(ctx, zone)
			return err2 == nil, err2
		})
		if !ok {
			return fmt.Errorf("删除站点 %s 失败：%w", undo["zone_name"], err2)
		}
	}
	if id, _ := strconv.ParseUint(undo["txt_record_id"], 10, 64); id > 0 {
		if err := c.DeleteRecord(ctx, undo["dnspod_domain"], id); err != nil && !tencent.IsCode(err, "InvalidParameter.RecordIdInvalid") {
			return fmt.Errorf("删除验证记录失败：%w", err)
		}
	}
	return nil
}
