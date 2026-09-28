package actions

import (
	"context"

	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// Automatic renewal of a prepaid (包年包月) cloud server: on, it renews
// itself from the account's balance before it expires; off, it has to be
// renewed by hand. Nothing is paid by the change itself.

func init() {
	auto := Param{Name: "auto", Kind: "enum", Enum: []string{"on", "off"}, Required: true, Desc: "on 开启自动续费，off 关闭（改为手动续费）"}
	register(&Capability{
		Name: "cloud.renew.set", Title: "设置腾讯云服务器自动续费", Risk: core.R1, Reversible: true,
		Params: []Param{{Name: "instance", Kind: "instance", Required: true, Desc: "腾讯云实例 ID（tencent_servers 返回的 id）"},
			{Name: "region", Kind: "region", Required: true, Desc: "实例所在地域（tencent_servers 返回的 region）"}, auto},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "renew", Downtime: "不影响运行；只有包年包月的服务器能设置，开启后到期前自动从账户余额扣费续费一个月",
			Undo: "改回原来的续费方式"}},
	})
	register(&Capability{
		Name: "aliyun.renew.set", Title: "设置阿里云服务器自动续费", Risk: core.R1, Reversible: true,
		Params: []Param{{Name: "instance", Kind: "aliinstance", Required: true, Desc: "阿里云 ECS 实例 ID（aliyun_servers 返回的 id，i- 开头；轻量应用服务器请在控制台设置）"},
			{Name: "region", Kind: "region", Required: true, Desc: "实例所在地域（aliyun_servers 返回的 region）"}, auto},
		Impls: map[string]Impl{"*": {Via: "阿里云接口", Cloud: "ali_renew", Downtime: "不影响运行；只有包年包月的 ECS 能设置，开启后到期前自动从账户余额扣费续费一个月",
			Undo: "改回原来的续费方式"}},
	})
}

func applyRenew(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	s, err := findInstance(ctx, c, v["region"], v["instance"])
	if err != nil {
		return refused(out, "%v", err)
	}
	if s.ChargeType != "" && s.ChargeType != "PREPAID" {
		return refused(out, "%s 是按量计费的，没有续费这回事", serverText(s))
	}
	want := tencent.RenewManual
	if v["auto"] == "on" {
		want = tencent.RenewAuto
	}
	if s.RenewFlag == want {
		report("%s 已经是%s，不需要改", serverText(s), renewText(want == tencent.RenewAuto))
		out.Status = StatusDone
		return *out
	}
	report("正在把 %s 改为%s", serverText(s), renewText(want == tencent.RenewAuto))
	if err := c.SetRenewFlag(ctx, s.Region, s.ID, want); err != nil {
		return refused(out, "修改失败：%v", err)
	}
	out.Undo["region"], out.Undo["instance"], out.Undo["flag"] = s.Region, s.ID, s.RenewFlag
	report("已改为%s", renewText(want == tencent.RenewAuto))
	out.Status = StatusDone
	return *out
}

func undoRenew(ctx context.Context, c *tencent.Client, undo map[string]string) error {
	flag := undo["flag"]
	if flag == "" {
		flag = tencent.RenewManual
	}
	return c.SetRenewFlag(ctx, undo["region"], undo["instance"], flag)
}

func aliRenew(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Aliyun
	s, err := aliFind(ctx, c, v["region"], v["instance"])
	if err != nil {
		return refused(out, "%v", err)
	}
	if s.ChargeType != "PREPAID" {
		return refused(out, "%s 是按量付费的，没有续费这回事", aliServerText(s))
	}
	on := v["auto"] == "on"
	if s.AutoRenew != nil && *s.AutoRenew == on {
		report("%s 已经是%s，不需要改", aliServerText(s), renewText(on))
		out.Status = StatusDone
		return *out
	}
	report("正在把 %s 改为%s", aliServerText(s), renewText(on))
	if err := c.SetAutoRenew(ctx, s.Region, s.ID, on); err != nil {
		return refused(out, "修改失败：%v", err)
	}
	out.Undo["region"], out.Undo["instance"] = s.Region, s.ID
	out.Undo["auto"] = map[bool]string{true: "on", false: "off"}[!on]
	report("已改为%s", renewText(on))
	out.Status = StatusDone
	return *out
}

func renewText(auto bool) string {
	if auto {
		return "自动续费"
	}
	return "手动续费"
}
