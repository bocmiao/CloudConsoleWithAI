package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
)

// The CDN page: Tencent Cloud's and 阿里云's accelerated domains together,
// with refreshing and prefetching their cache, turning acceleration on or
// off and (Tencent) HTTPS with a certificate from SSL 证书, each a
// checklist.

// CDNItem is one accelerated domain.
type CDNItem struct {
	Provider    string   `json:"provider"` // tencent, aliyun
	Domain      string   `json:"domain"`
	Cname       string   `json:"cname"`
	Status      string   `json:"status"` // online, offline, or the cloud's word for in between
	Type        string   `json:"type"`   // web, download, media (video)
	Area        string   `json:"area"`   // mainland, overseas, global
	Origins     []string `json:"origins"`
	HTTPS       bool     `json:"https"`
	CertID      string   `json:"certId,omitempty"`
	CertExpires string   `json:"certExpires,omitempty"`
	Disabled    string   `json:"disabled,omitempty"` // why the cloud shut it (Tencent: overdue, malicious, ...)
}

// CDNCert is an issued certificate in Tencent Cloud's SSL 证书, for the
// HTTPS of a Tencent CDN domain.
type CDNCert struct {
	ID      string   `json:"id"`
	Names   []string `json:"names"`
	Expires string   `json:"expires"`
}

// CDNView is the CDN page.
type CDNView struct {
	Domains []CDNItem `json:"domains"`
	Certs   []CDNCert `json:"certs"`
	Errors  []string  `json:"errors"`
}

// CDN lists both clouds' CDN domains; one that cannot be read says why.
func (a *App) CDN(ctx context.Context) (CDNView, error) {
	v := CDNView{Domains: []CDNItem{}, Certs: []CDNCert{}, Errors: []string{}}
	tc, ac := a.tencentClient(), a.aliyunClient()
	if tc == nil && ac == nil {
		return v, userErr("还没有配置腾讯云或阿里云的密钥（设置 → 腾讯云 / 阿里云）")
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	if tc != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			list, err := tc.CDNDomains(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				v.Errors = append(v.Errors, "腾讯云 CDN："+err.Error())
			}
			if len(list) > 0 {
				// The certificates that could go on them; without the
				// permission to read them, only applying for one is offered.
				certs, _ := tc.SSLCertificates(ctx)
				for _, c := range certs {
					if c.Status == 1 {
						names := append([]string{strings.ToLower(c.Domain)}, c.SANs...)
						v.Certs = append(v.Certs, CDNCert{ID: c.ID, Names: uniqLower(names), Expires: c.EndTime})
					}
				}
			}
			for _, d := range list {
				v.Domains = append(v.Domains, CDNItem{Provider: "tencent", Domain: d.Domain, Cname: d.Cname, Status: d.Status, Type: d.ServiceType,
					Area: d.Area, Origins: d.Origins, HTTPS: d.HTTPS, CertID: d.CertID, CertExpires: d.CertExpires, Disabled: d.Disable})
			}
		}()
	}
	if ac != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			list, err := ac.CDNDomains(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				v.Errors = append(v.Errors, "阿里云 CDN："+err.Error())
			}
			area := map[string]string{"domestic": "mainland", "overseas": "overseas", "global": "global"}
			for _, d := range list {
				item := CDNItem{Provider: "aliyun", Domain: d.Name, Cname: d.CNAME, Status: d.Status, Type: d.Type, Area: area[d.Coverage],
					HTTPS: d.HTTPS, Origins: []string{}}
				if item.Type == "video" {
					item.Type = "media"
				}
				for _, s := range d.Sources {
					item.Origins = append(item.Origins, s.Content)
				}
				v.Domains = append(v.Domains, item)
			}
		}()
	}
	wg.Wait()
	sort.Slice(v.Domains, func(i, j int) bool {
		if v.Domains[i].Domain != v.Domains[j].Domain {
			return v.Domains[i].Domain < v.Domains[j].Domain
		}
		return v.Domains[i].Provider < v.Domains[j].Provider
	})
	sort.Strings(v.Errors)
	return v, nil
}

// CDNPage is the CDN page's list, kept ready.
func (a *App) CDNPage(ctx context.Context, read PageRead) (CDNView, PageMeta, error) {
	return page(ctx, a, "page_cdn", read, a.CDN)
}

// CDNRequest is a change asked for on the CDN page.
type CDNRequest struct {
	Provider string   `json:"provider"`
	Domain   string   `json:"domain"`
	Op       string   `json:"op"`      // purge, prefetch, on, off, https
	Targets  []string `json:"targets"` // purge, prefetch: addresses (a path is taken as on the domain)
	Dir      bool     `json:"dir"`     // purge whole directories
	Cert     string   `json:"cert"`    // https: an SSL 证书 certificate ID, empty to turn HTTPS off
}

// ProposeCDN turns a change on the CDN page into a checklist.
func (a *App) ProposeCDN(ctx context.Context, req CDNRequest) (PlanView, error) {
	v, _, err := a.CDNPage(ctx, PageWait)
	if err != nil {
		return PlanView{}, err
	}
	var d *CDNItem
	for i := range v.Domains {
		if v.Domains[i].Domain == strings.ToLower(req.Domain) && v.Domains[i].Provider == req.Provider {
			d = &v.Domains[i]
		}
	}
	if d == nil {
		return PlanView{}, userErr("找不到 CDN 加速域名 %s", req.Domain)
	}
	cloud := map[string]string{"tencent": "腾讯云", "aliyun": "阿里云"}[d.Provider]
	capOf := func(tencentCap, aliCap string) string {
		if d.Provider == "aliyun" {
			return aliCap
		}
		return tencentCap
	}
	params := map[string]any{}
	var capability, title, summary, reason string
	switch req.Op {
	case "purge", "prefetch":
		var targets []string
		for _, t := range req.Targets {
			if t = strings.TrimSpace(t); t == "" {
				continue
			}
			if strings.HasPrefix(t, "/") {
				t = "https://" + d.Domain + t
			}
			u, err := url.Parse(t)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() != d.Domain {
				return PlanView{}, userErr("%s 不是 %s 下的网址", t, d.Domain)
			}
			targets = append(targets, t)
		}
		if len(targets) == 0 {
			return PlanView{}, userErr("请填写要%s的网址或路径", map[bool]string{true: "刷新", false: "预热"}[req.Op == "purge"])
		}
		if len(targets) > 100 {
			return PlanView{}, userErr("一次最多 100 个")
		}
		params["targets"] = strings.Join(targets, "\n")
		if req.Op == "purge" {
			capability = capOf("cdn.cache.purge", "aliyun.cdn.purge")
			kind := "网址"
			if req.Dir {
				params["type"], kind = "dir", "目录"
			}
			title = "刷新 CDN 缓存：" + d.Domain
			summary = fmt.Sprintf("刷新 %s CDN 上 %d 个%s的缓存", cloud, len(targets), kind)
			reason = "源站的文件改了、访客看到的还是旧的时刷新：节点丢掉旧的缓存，下次访问从源站取新的。不中断访问，刷新后的第一次访问会慢一点。"
		} else {
			capability = capOf("cdn.cache.prefetch", "aliyun.cdn.prefetch")
			title = "预热 CDN：" + d.Domain
			summary = fmt.Sprintf("把 %d 个网址提前缓存到 %s CDN 节点", len(targets), cloud)
			reason = "大文件或活动页上线前预热，第一批访客就能直接从节点拿到，不用等回源。会从源站拉一次内容，产生回源流量。"
		}
	case "on", "off":
		capability = capOf("cdn.domain.status", "aliyun.cdn.status")
		params["domain"], params["status"] = d.Domain, req.Op
		if req.Op == "on" {
			title, summary = "启用 CDN 加速："+d.Domain, "启用 "+d.Domain+" 的 "+cloud+" CDN 加速"
			reason = "启用后访问这个域名会经过 CDN 节点（解析要指向 CDN 的 CNAME " + orDash(d.Cname) + "）。"
		} else {
			title, summary = "停用 CDN 加速："+d.Domain, "停用 "+d.Domain+" 的 "+cloud+" CDN 加速"
			reason = "停用后 CDN 不再为这个域名服务：如果解析还指向 CDN 的 CNAME，访客会打不开，要先把解析改回源站（" + strings.Join(d.Origins, "、") + "）。可以撤销。"
		}
	case "https":
		if d.Provider != "tencent" {
			return PlanView{}, userErr("阿里云 CDN 的证书请在阿里云控制台设置")
		}
		capability = "cdn.https.set"
		params["domain"] = d.Domain
		if req.Cert == "new" {
			capability = "ssl.cert.apply"
			params = map[string]any{"domain": d.Domain, "use_cdn": "yes"}
			title, summary = "申请免费证书并开启 HTTPS："+d.Domain, "申请 "+d.Domain+" 的免费证书，签发后给 CDN 开启 HTTPS"
			reason = "向腾讯云 SSL 证书申请免费证书（TrustAsia，有效期 3 个月，到期前要重新申请），通过 DNSPod 自动验证，一般几分钟签发，签发后配置到 CDN 上并开启 HTTP/2。" +
				"域名的解析要在这个账号的 DNSPod 里。腾讯云 CDN 的 HTTPS 请求按量计费。"
			break
		}
		if req.Cert == "" {
			params["cert"] = "off"
			title, summary = "关闭 CDN 的 HTTPS："+d.Domain, "关闭 "+d.Domain+" 的 HTTPS"
			reason = "关闭后访客只能用 http:// 访问这个域名，https:// 会打不开。可以撤销。"
		} else {
			params["cert"] = req.Cert
			title, summary = "CDN 开启 HTTPS："+d.Domain, "用证书 "+req.Cert+" 给 "+d.Domain+" 开启 HTTPS"
			reason = "用腾讯云 SSL 证书里的证书给这个 CDN 域名开启 HTTPS（同时开 HTTP/2）。腾讯云 CDN 的 HTTPS 请求按量计费。可以撤销。"
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

var cdnStatusWords = map[string]string{"online": "已启用", "offline": "已停用", "processing": "部署中", "closing": "关闭中", "rejected": "审核未通过",
	"configuring": "配置中", "configure_failed": "配置失败", "checking": "审核中", "check_failed": "审核失败", "stopping": "停用中", "deleting": "删除中"}

// toolCDN is the AI's look at both clouds' CDN domains.
func (a *App) toolCDN(ctx context.Context, raw json.RawMessage) (string, error) {
	var arg struct {
		Refresh bool `json:"refresh"`
	}
	if err := parseArgs(raw, &arg); err != nil {
		return "", err
	}
	read := PageWait
	if arg.Refresh {
		read = PageRefresh
	}
	v, _, err := a.CDNPage(ctx, read)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if len(v.Domains) == 0 {
		b.WriteString("腾讯云和阿里云账号里都没有 CDN 加速域名（EdgeOne 的站点用 tencent_eo 查）。\n")
	}
	for _, d := range v.Domains {
		fmt.Fprintf(&b, "%s CDN %s 状态=%s CNAME=%s 类型=%s 区域=%s 源站=%s HTTPS=%s", providerName[d.Provider], d.Domain,
			orDash(cdnStatusWords[d.Status]+"（"+d.Status+"）"), orDash(d.Cname), d.Type, d.Area, orDash(strings.Join(d.Origins, ",")), kaiGuan(d.HTTPS))
		if d.CertID != "" {
			fmt.Fprintf(&b, " 证书=%s 到期 %s", d.CertID, d.CertExpires)
		}
		if d.Disabled != "" {
			fmt.Fprintf(&b, " 被腾讯云关闭（%s）", d.Disabled)
		}
		b.WriteString("\n")
	}
	for _, e := range v.Errors {
		fmt.Fprintf(&b, "读取失败：%s\n", e)
	}
	return b.String(), nil
}

func uniqLower(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range list {
		x = strings.ToLower(strings.TrimSpace(x))
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// ProposeFreeCert makes a checklist that applies for a free certificate
// from Tencent Cloud's SSL 证书 (the 证书 page).
func (a *App) ProposeFreeCert(ctx context.Context, domain string) (PlanView, error) {
	if a.tencentClient() == nil {
		return PlanView{}, userErr("还没有配置腾讯云密钥（设置 → 腾讯云）")
	}
	domain = strings.ToLower(strings.TrimSpace(domain))
	if strings.HasPrefix(domain, "*.") {
		return PlanView{}, userErr("免费证书不支持泛域名，请填写要用的名字，例如 www.example.com")
	}
	params := map[string]any{"domain": domain}
	if _, err := actions.Resolve("ssl.cert.apply", params, noServer); err != nil {
		return PlanView{}, userErr("%v", err)
	}
	reason := "向腾讯云 SSL 证书申请免费证书（TrustAsia，有效期 3 个月），通过 DNSPod 自动验证，一般几分钟签发。签发后可以用在腾讯云 CDN、负载均衡、COS 等产品上，" +
		"也可以在腾讯云控制台下载。经过 EdgeOne 的网站用 EdgeOne 的免费证书、直接访问服务器的网站用「证书」页的 Let's Encrypt 更省事：它们会自动续签。"
	p, _, err := a.proposePlan(ctx, "user", 0, "申请免费证书："+domain, reason,
		[]core.Step{{Capability: "ssl.cert.apply", Summary: "申请 " + domain + " 的免费证书", Params: params}})
	if err != nil {
		return PlanView{}, err
	}
	return a.Plan(p.ID)
}
