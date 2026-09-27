package app

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// Clearing and warming EdgeOne's cache from the pages. Both become a
// checklist like everything else that changes something, so they are
// confirmed first and logged.

// SiteEOView is the EdgeOne site that serves some of a website's domains.
type SiteEOView struct {
	Zone    string         `json:"zone"`
	Domains []SiteEODomain `json:"domains"`
}

// SiteEODomain is one of the website's domains on EdgeOne.
type SiteEODomain struct {
	Name   string `json:"name"`
	Status string `json:"status"` // online, offline, process…
	Origin string `json:"origin,omitempty"`
}

// siteEdgeOne finds which of names EdgeOne accelerates; nil when none (or
// Tencent Cloud is not set up).
func (a *App) siteEdgeOne(ctx context.Context, names []string) *SiteEOView {
	c := a.tencentClient()
	if c == nil || len(names) == 0 {
		return nil
	}
	zones, err := c.Zones(ctx)
	if err != nil {
		return nil
	}
	z, ok := tencent.ZoneFor(zones, names[0])
	if !ok {
		return nil
	}
	list, err := c.AccelerationDomains(ctx, z.ZoneID, "")
	if err != nil {
		return nil
	}
	v := &SiteEOView{Zone: z.ZoneName}
	for _, n := range names {
		for _, d := range list {
			if strings.EqualFold(d.DomainName, n) {
				v.Domains = append(v.Domains, SiteEODomain{Name: d.DomainName, Status: d.DomainStatus, Origin: d.OriginDetail.Origin})
			}
		}
	}
	if len(v.Domains) == 0 {
		return nil
	}
	return v
}

// EOCacheRequest asks to clear or warm EdgeOne's cache for a domain.
type EOCacheRequest struct {
	ServerID int64    `json:"serverId"` // the server the site is on, if any
	Domain   string   `json:"domain"`
	Op       string   `json:"op"`   // purge or prefetch
	Type     string   `json:"type"` // purge: url, prefix, host or all
	Targets  []string `json:"targets"`
	Method   string   `json:"method"` // purge: invalidate or delete
}

// eoTargets turns what was typed into full addresses on domain: a path
// such as /css/app.css becomes https://domain/css/app.css.
func eoTargets(domain string, in []string, dir bool) ([]string, error) {
	var out []string
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "/") {
			t = "https://" + domain + t
		} else if !strings.Contains(t, "://") {
			t = "https://" + t
		}
		u, err := url.Parse(t)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, userErr("%s 不是网址，请写完整的网址（https:// 开头）或者以 / 开头的路径", t)
		}
		if dir && !strings.HasSuffix(t, "/") {
			t += "/"
		}
		out = append(out, t)
	}
	if len(out) > 1000 {
		return nil, userErr("一次最多 1000 个网址")
	}
	return out, nil
}

// ProposeEOCache makes the checklist that clears or warms the cache.
func (a *App) ProposeEOCache(ctx context.Context, req EOCacheRequest) (PlanView, error) {
	if a.tencentClient() == nil {
		return PlanView{}, userErr("请先在「设置 → 腾讯云」填写密钥")
	}
	domain := strings.ToLower(strings.TrimSpace(req.Domain))
	if domain == "" {
		return PlanView{}, userErr("请选择域名")
	}
	var capability, title, summary, reason string
	params := map[string]any{"domain": domain}
	switch req.Op {
	case "purge":
		capability = "eo.cache.purge"
		kind := req.Type
		if kind == "" {
			kind = "url"
		}
		method := req.Method
		if method == "" {
			method = "invalidate"
		}
		params["type"], params["method"] = kind, method
		how := map[string]string{"invalidate": "标记过期（节点下次访问时向源站确认有没有更新）", "delete": "直接删除（下次访问一定回源）"}[method]
		switch kind {
		case "url", "prefix":
			targets, err := eoTargets(domain, req.Targets, kind == "prefix")
			if err != nil {
				return PlanView{}, err
			}
			if len(targets) == 0 {
				return PlanView{}, userErr("请填写要清除的%s", map[string]string{"url": "网址", "prefix": "目录"}[kind])
			}
			params["targets"] = strings.Join(targets, "\n")
			what := fmt.Sprintf("EdgeOne 上 %d 个%s的缓存", len(targets), map[string]string{"url": "网址", "prefix": "目录"}[kind])
			if len(targets) == 1 {
				what = "EdgeOne 上 " + targets[0] + " 的缓存"
			}
			title, summary = "清除 EdgeOne 缓存："+domain, "清除"+what+"（"+how+"）"
		case "host":
			title, summary = "清除 EdgeOne 缓存："+domain, "清除 EdgeOne 上 "+domain+" 整个域名的缓存（"+how+"）"
		case "all":
			title, summary = "清除 EdgeOne 缓存：整个站点", "清除 EdgeOne 站点里所有域名的缓存（"+how+"）"
		default:
			return PlanView{}, userErr("清除范围只能是网址、目录、整个域名或整个站点")
		}
		reason = "清除后，EdgeOne 节点会重新从源站拉取内容，网站改版或更新了文件后用它让访客马上看到新内容。一般几分钟内在全部节点生效，不中断访问，清除后的第一批访问会慢一点。"
		if kind == "host" || kind == "all" {
			reason += "\n注意：范围越大，清除后短时间内回源的请求越多，源站压力会突然变大；只改了几个文件时，清除这几个网址就够了。"
		}
	case "prefetch":
		capability = "eo.cache.prefetch"
		targets, err := eoTargets(domain, req.Targets, false)
		if err != nil {
			return PlanView{}, err
		}
		if len(targets) == 0 {
			return PlanView{}, userErr("请填写要预热的网址")
		}
		params["targets"] = strings.Join(targets, "\n")
		what := fmt.Sprintf("把 %d 个网址", len(targets))
		if len(targets) == 1 {
			what = "把 " + targets[0] + " "
		}
		title, summary = "预热 EdgeOne 缓存："+domain, what+"预热到 EdgeOne 节点"
		reason = "预热会让 EdgeOne 提前从源站拉取这些文件并缓存到节点，访客第一次访问也能直接从节点拿到。适合发布新版本、大文件或活动页面之前使用。不影响访问。"
	default:
		return PlanView{}, userErr("不支持的操作 %q", req.Op)
	}
	serverID := req.ServerID
	if serverID != 0 {
		if _, err := a.Store.GetServer(serverID); err != nil {
			serverID = 0
		}
	}
	if _, err := actions.Resolve(capability, params, noServer); err != nil {
		return PlanView{}, userErr("%v", err)
	}
	p, _, err := a.proposePlan(ctx, "user", serverID, title, reason, []core.Step{{Capability: capability, Summary: summary, Params: params}})
	if err != nil {
		return PlanView{}, err
	}
	return a.Plan(p.ID)
}
