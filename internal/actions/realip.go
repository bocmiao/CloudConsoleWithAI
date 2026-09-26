package actions

import (
	"context"
	"strings"
)

// applyClientIP makes EdgeOne pass each visitor's IP to the origin in a
// request header with Miao Panel's secret name; nginx.realip then trusts
// that header on the server. A site that already sends the visitor's IP
// under another name is left alone: something else may be reading it.
func applyClientIP(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	header := v["header"]
	if !strings.HasPrefix(header, "X-Miao-IP-") {
		out.Status = StatusRefused
		out.logf("请求头名字不对，请重新生成这份清单")
		return *out
	}
	z, err := findZone(ctx, c, strings.ToLower(v["domain"]))
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	on, name, err := c.ClientIPHeader(ctx, z.ZoneID)
	if err != nil {
		out.Status = StatusFailed
		out.logf("查询 EdgeOne 站点 %s 的设置失败：%v", z.ZoneName, err)
		return *out
	}
	switch {
	case on && strings.EqualFold(name, header):
		report("EdgeOne 站点 %s 已经在回源时带上访客 IP 了，不需要修改", z.ZoneName)
		out.Status = StatusDone
		out.Undo = map[string]string{}
		return *out
	case on:
		out.Status = StatusRefused
		out.logf("EdgeOne 站点 %s 已经用请求头 %s 带上访客 IP，可能有别的程序在用它，为了不影响它没有修改。"+
			"如果确定没人用，可以在 EdgeOne 控制台「站点加速 → 回源携带客户端 IP 头部」关掉它再执行", z.ZoneName, name)
		return *out
	}
	report("正在设置 EdgeOne 站点 %s：回源时用一个只有 EdgeOne 和你的服务器知道的请求头带上访客 IP", z.ZoneName)
	if err := c.SetClientIPHeader(ctx, z.ZoneID, true, header); err != nil {
		out.Status = StatusFailed
		out.logf("设置失败：%v", err)
		return *out
	}
	out.Undo = map[string]string{"zone_id": z.ZoneID, "switch": "off", "header": name}
	report("完成：%s 下所有加速域名回源时都会带上访客 IP，几分钟内生效", z.ZoneName)
	out.Status = StatusDone
	return *out
}
