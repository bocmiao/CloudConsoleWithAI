package actions

import (
	"context"
	"net/url"
	"strings"

	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// Tencent Cloud CDN: refreshing and prefetching the cache, turning a
// domain's acceleration on or off, and its HTTPS certificate. 阿里云 CDN's
// switch is here too; its refresh and prefetch are in alicdn.go.

func init() {
	onOff := Param{Name: "status", Kind: "enum", Enum: []string{"on", "off"}, Required: true, Desc: "on 启用加速，off 停用（停用后访客会访问失败，除非解析已经改回源站）"}
	register(&Capability{
		Name: "cdn.cache.purge", Title: "刷新腾讯云 CDN 缓存", Risk: core.R2,
		NoUndo: "刷新缓存只是让节点重新从源站拉取内容，不需要也无法回滚",
		Params: []Param{
			{Name: "targets", Kind: "lines", Required: true, Desc: "要刷新的完整网址或目录（https:// 开头），多个用逗号或换行分隔；必须是这个账号的 CDN 加速域名"},
			{Name: "type", Kind: "enum", Enum: []string{"url", "dir"}, Default: "url", Desc: "url：指定网址；dir：整个目录（只刷新源站有变化的）"},
		},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "cdn_purge", Downtime: "不中断访问，刷新后的第一次访问会慢一点"}},
	})
	register(&Capability{
		Name: "cdn.cache.prefetch", Title: "预热腾讯云 CDN", Risk: core.R1,
		NoUndo: "预热只是提前把内容缓存到节点，不需要回滚",
		Params: []Param{{Name: "targets", Kind: "lines", Required: true, Desc: "要预热的完整网址（https:// 开头），多个用逗号或换行分隔"}},
		Impls:  map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "cdn_prefetch", Downtime: "不影响访问；会从源站拉一次内容，产生回源流量"}},
	})
	register(&Capability{
		Name: "cdn.domain.status", Title: "启用或停用腾讯云 CDN 加速域名", Risk: core.R2, Reversible: true,
		Params: []Param{{Name: "domain", Kind: "host", Required: true, Desc: "CDN 加速域名"}, onOff},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "cdn_status", Downtime: "停用后这个域名不再经过 CDN，解析还指向 CDN 时访客会打不开；几分钟内生效",
			Undo: "恢复原来的启用状态"}},
	})
	register(&Capability{
		Name: "cdn.https.set", Title: "腾讯云 CDN 的 HTTPS 证书", Risk: core.R1, Reversible: true,
		Params: []Param{{Name: "domain", Kind: "host", Required: true, Desc: "CDN 加速域名"},
			{Name: "cert", Kind: "name", Required: true, Desc: "腾讯云 SSL 证书里已签发的证书 ID（certificates 里 ssl- 开头的 ID，或 ssl.cert.apply 刚申请的）；off 关闭 HTTPS"}},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "cdn_https", Downtime: "不中断访问；证书几分钟内部署到所有节点。CDN 的 HTTPS 请求按量计费",
			Undo: "恢复原来的证书（或关闭 HTTPS）"}},
	})
	register(&Capability{
		Name: "aliyun.cdn.status", Title: "启用或停用阿里云 CDN 加速域名", Risk: core.R2, Reversible: true,
		Params: []Param{{Name: "domain", Kind: "host", Required: true, Desc: "阿里云 CDN 加速域名"}, onOff},
		Impls: map[string]Impl{"*": {Via: "阿里云接口", Cloud: "ali_cdn_status", Downtime: "停用后这个域名不再经过 CDN，解析还指向 CDN 时访客会打不开；几分钟内生效",
			Undo: "恢复原来的启用状态"}},
	})
}

// cdnDomainOf finds the CDN domain an address is on.
func cdnDomainOf(list []tencent.CDNDomain, host string) *tencent.CDNDomain {
	for i := range list {
		if list[i].Domain == strings.ToLower(host) {
			return &list[i]
		}
	}
	return nil
}

func applyCDNCache(ctx context.Context, env *Env, op string, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	targets := splitTargets(v["targets"])
	if len(targets) == 0 {
		return refused(out, "要写清楚是哪些网址（https:// 开头）")
	}
	domains, err := c.CDNDomains(ctx)
	if err != nil {
		return refused(out, "读取 CDN 域名失败：%v", err)
	}
	dir := op == "cdn_purge" && v["type"] == "dir"
	for i, t := range targets {
		u, err := url.Parse(t)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return refused(out, "%s 不是完整的网址，要以 https:// 开头", t)
		}
		d := cdnDomainOf(domains, u.Hostname())
		switch {
		case d == nil:
			return refused(out, "%s 不是这个腾讯云账号的 CDN 加速域名", u.Hostname())
		case d.Status != "online":
			return refused(out, "%s 的 CDN 加速现在没有启用（%s），不能%s", d.Domain, d.Status, map[bool]string{true: "刷新", false: "预热"}[op == "cdn_purge"])
		}
		if dir && !strings.HasSuffix(t, "/") {
			targets[i] = t + "/"
		}
	}
	var task string
	if op == "cdn_purge" {
		report("正在刷新腾讯云 CDN 缓存（%s）：%s", map[bool]string{true: "目录", false: "网址"}[dir], strings.Join(targets, "、"))
		task, err = c.PurgeCDN(ctx, targets, dir)
	} else {
		report("正在预热腾讯云 CDN：%s", strings.Join(targets, "、"))
		task, err = c.PrefetchCDN(ctx, targets)
	}
	if err != nil {
		return refused(out, "提交失败：%v", err)
	}
	report("已提交（任务 %s），一般 5 分钟内在所有节点生效", task)
	out.Status, out.Undo = StatusDone, map[string]string{}
	return *out
}

func applyCDNStatus(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	domains, err := c.CDNDomains(ctx)
	if err != nil {
		return refused(out, "读取 CDN 域名失败：%v", err)
	}
	d := cdnDomainOf(domains, v["domain"])
	if d == nil {
		return refused(out, "%s 不是这个腾讯云账号的 CDN 加速域名", v["domain"])
	}
	on := v["status"] == "on"
	switch {
	case on && d.Status == "online", !on && d.Status == "offline":
		report("%s 现在已经是%s，不需要改", d.Domain, cdnStatusText(on))
		out.Status = StatusDone
		return *out
	case d.Status != "online" && d.Status != "offline":
		return refused(out, "%s 现在是 %s 状态，等它完成再改", d.Domain, d.Status)
	}
	report("正在把 %s 的 CDN 加速改为%s", d.Domain, cdnStatusText(on))
	if err := c.SetCDNDomain(ctx, d.Domain, on); err != nil {
		return refused(out, "修改失败：%v", err)
	}
	out.Undo["domain"], out.Undo["on"] = d.Domain, map[bool]string{true: "off", false: "on"}[on]
	report("已%s，几分钟内在所有节点生效", cdnStatusText(on))
	out.Status = StatusDone
	return *out
}

func aliCDNStatus(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Aliyun
	domains, err := c.CDNDomains(ctx)
	if err != nil {
		return refused(out, "读取 CDN 域名失败：%v", err)
	}
	var d *aliyun.CDNDomain
	for i := range domains {
		if domains[i].Name == v["domain"] {
			d = &domains[i]
		}
	}
	if d == nil {
		return refused(out, "%s 不是这个阿里云账号的 CDN 加速域名", v["domain"])
	}
	on := v["status"] == "on"
	switch {
	case on && d.Status == "online", !on && d.Status == "offline":
		report("%s 现在已经是%s，不需要改", d.Name, cdnStatusText(on))
		out.Status = StatusDone
		return *out
	case d.Status != "online" && d.Status != "offline":
		return refused(out, "%s 现在是 %s 状态，等它完成再改", d.Name, d.Status)
	}
	report("正在把 %s 的 CDN 加速改为%s", d.Name, cdnStatusText(on))
	if err := c.SetCDNDomain(ctx, d.Name, on); err != nil {
		return refused(out, "修改失败：%v", err)
	}
	out.Undo["domain"], out.Undo["on"] = d.Name, map[bool]string{true: "off", false: "on"}[on]
	report("已%s，几分钟内在所有节点生效", cdnStatusText(on))
	out.Status = StatusDone
	return *out
}

func applyCDNHTTPS(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.Cloud
	domains, err := c.CDNDomains(ctx)
	if err != nil {
		return refused(out, "读取 CDN 域名失败：%v", err)
	}
	d := cdnDomainOf(domains, v["domain"])
	if d == nil {
		return refused(out, "%s 不是这个腾讯云账号的 CDN 加速域名", v["domain"])
	}
	cert := v["cert"]
	if cert == "off" {
		if !d.HTTPS {
			report("%s 本来就没有开 HTTPS", d.Domain)
			out.Status = StatusDone
			return *out
		}
		cert = ""
	} else {
		if d.HTTPS && d.CertID == cert {
			report("%s 已经在用证书 %s", d.Domain, cert)
			out.Status = StatusDone
			return *out
		}
		certs, err := c.SSLCertificates(ctx)
		if err != nil {
			return refused(out, "读取 SSL 证书失败：%v", err)
		}
		var found *tencent.SSLCert
		for i := range certs {
			if certs[i].ID == cert {
				found = &certs[i]
			}
		}
		switch {
		case found == nil:
			return refused(out, "腾讯云 SSL 证书里没有证书 %s", cert)
		case found.Status != 1:
			return refused(out, "证书 %s 还不能用（%s）", cert, found.StatusName)
		case !sslCovers(*found, d.Domain):
			return refused(out, "证书 %s 不包含 %s（证书的域名：%s）", cert, d.Domain, strings.Join(append([]string{found.Domain}, found.SANs...), "、"))
		}
	}
	if cert == "" {
		report("正在关闭 %s 的 HTTPS", d.Domain)
	} else {
		report("正在给 %s 配置证书 %s 并开启 HTTPS", d.Domain, cert)
	}
	if err := c.SetCDNCert(ctx, d.Domain, cert); err != nil {
		return refused(out, "修改失败：%v", err)
	}
	out.Undo["domain"] = d.Domain
	out.Undo["cert"] = "off"
	if d.HTTPS && d.CertID != "" {
		out.Undo["cert"] = d.CertID
	}
	if cert == "" {
		report("已关闭 HTTPS")
	} else {
		report("已提交，证书几分钟内部署到所有节点，之后 https://%s 就能访问", d.Domain)
	}
	out.Status = StatusDone
	return *out
}

// sslCovers reports whether a certificate is for host (a wildcard covers
// one level).
func sslCovers(c tencent.SSLCert, host string) bool {
	for _, n := range append([]string{c.Domain}, c.SANs...) {
		n = strings.ToLower(n)
		if n == host {
			return true
		}
		if rest, ok := strings.CutPrefix(n, "*."); ok {
			if sub, ok := strings.CutSuffix(host, "."+rest); ok && sub != "" && !strings.Contains(sub, ".") {
				return true
			}
		}
	}
	return false
}

func cdnStatusText(on bool) string {
	if on {
		return "启用"
	}
	return "停用"
}
