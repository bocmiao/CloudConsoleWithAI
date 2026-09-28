package actions

import (
	"context"
	"strings"

	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
)

// Free certificates from Tencent Cloud's SSL 证书: applied for one name at
// a time, validated automatically through DNSPod, valid for three months.
// Asked to, the new certificate goes onto the Tencent CDN domain of the
// same name once issued.

func init() {
	register(&Capability{
		Name: "ssl.cert.apply", Title: "申请腾讯云免费 SSL 证书", Risk: core.R1,
		NoUndo: "证书申请提交后不能撤销；不用的证书可以在腾讯云 SSL 证书控制台删除（免费证书有数量限制）。用在 CDN 上的那一步可以在「CDN」页改回",
		Params: []Param{
			{Name: "domain", Kind: "host", Required: true, Desc: "证书的域名，一个名字，例如 www.example.com（免费证书不支持泛域名）；域名的解析要在这个账号的 DNSPod 里"},
			{Name: "use_cdn", Kind: "enum", Enum: []string{"yes", "no"}, Default: "no", Desc: "yes：签发后给同名的腾讯云 CDN 加速域名开启 HTTPS"},
		},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "ssl_apply",
			Downtime: "不影响访问；DNSPod 里会临时多一条验证记录，签发后自动删除，一般几分钟签发，有效期 3 个月"}},
	})
}

func applySSL(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	name := v["domain"]
	if strings.HasPrefix(name, "*.") {
		return refused(out, "免费证书不支持泛域名 %s，请给要用的名字分别申请", name)
	}
	domains, err := c.Domains(ctx)
	if err != nil {
		return refused(out, "读取 DNSPod 域名失败：%v", err)
	}
	zone := ""
	for _, d := range domains {
		if name == d.Name || strings.HasSuffix(name, "."+d.Name) {
			zone = d.Name
		}
	}
	if zone == "" {
		return refused(out, "%s 的解析不在这个腾讯云账号的 DNSPod 里，不能自动验证；可以让网站经过 EdgeOne 用它的免费证书，或者在服务器面板里申请 Let's Encrypt 证书", name)
	}
	useCDN := v["use_cdn"] == "yes"
	if useCDN {
		list, err := c.CDNDomains(ctx)
		if err != nil {
			return refused(out, "读取 CDN 域名失败：%v", err)
		}
		if cdnDomainOf(list, name) == nil {
			return refused(out, "%s 不是这个账号的腾讯云 CDN 加速域名", name)
		}
	}
	report("正在为 %s 申请免费证书（通过 DNSPod 的 %s 自动验证）", name, zone)
	id, err := c.ApplyFreeCert(ctx, name)
	if err != nil {
		return refused(out, "申请失败：%v", err)
	}
	out.Result = map[string]string{"cert": id}
	report("已提交，证书 ID %s，正在等腾讯云验证并签发", id)
	status, statusName := 0, ""
	issued, _ := waitFor(ctx, env, 90, func() (bool, error) {
		cert, err := c.SSLCertificate(ctx, id)
		status, statusName = cert.Status, cert.StatusName
		return err == nil && (cert.Status == 1 || cert.Status == 2), err
	})
	switch {
	case status == 2:
		out.Status = StatusFailed
		out.logf("证书验证没有通过（%s），请在腾讯云 SSL 证书控制台查看原因", statusName)
		return *out
	case !issued && useCDN:
		out.Status = StatusFailed
		out.logf("证书 %s 还在验证中（%s），没有用到 CDN 上；签发后在「CDN」页给 %s 开启 HTTPS", id, orDash(statusName), name)
		return *out
	case !issued:
		report("腾讯云还在验证（%s），一般几分钟到一小时签发，签发后会出现在「证书」页", orDash(statusName))
		out.Status, out.Undo = StatusDone, map[string]string{}
		return *out
	}
	report("证书已签发：%s（%s）", id, name)
	out.Status, out.Undo = StatusDone, map[string]string{}
	if !useCDN {
		return *out
	}
	cdn := &Outcome{Undo: map[string]string{}}
	applyCDNHTTPS(ctx, env, map[string]string{"domain": name, "cert": id}, cdn, report)
	if cdn.Status != StatusDone {
		out.Status = StatusFailed
		out.logf("%s", strings.Join(cdn.Log, "\n"))
		out.logf("证书已经签发，但没有配置到 CDN 上；可以在「CDN」页重试")
		return *out
	}
	return *out
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
