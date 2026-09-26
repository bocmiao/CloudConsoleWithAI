package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

// Behind EdgeOne a server's access log sees EdgeOne's nodes, not the
// visitors. The fix is a checklist of two steps: EdgeOne passes each
// visitor's IP in a request header whose name is a secret shared by
// EdgeOne and the servers (eo.clientip.header), and Nginx takes the
// visitor's address from that header alone (nginx.realip). A request
// that reaches the server directly cannot know the name, so it cannot
// pretend to come from someone else.

const realIPKey = "realip_header"

// realIPCaps are the capabilities that carry the secret header name;
// Miao Panel fills it in, never the AI.
var realIPCaps = map[string]bool{"nginx.realip": true, "eo.clientip.header": true}

// realIPHeader is the account's secret header name, made once.
func (a *App) realIPHeader() (string, error) {
	if h, err := a.Secrets.Get(realIPKey); err == nil && strings.HasPrefix(h, "X-Miao-IP-") {
		return h, nil
	}
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	h := "X-Miao-IP-" + hex.EncodeToString(b)
	return h, a.Secrets.Set(realIPKey, h)
}

// fillRealIP puts the secret header name into the steps that need it.
func (a *App) fillRealIP(steps []core.Step) {
	for i := range steps {
		if !realIPCaps[steps[i].Capability] {
			continue
		}
		h, err := a.realIPHeader()
		if err != nil {
			continue // the step is then blocked for its missing parameter
		}
		if steps[i].Params == nil {
			steps[i].Params = map[string]any{}
		}
		steps[i].Params["header"] = h
	}
}

// originAddrs are the names and addresses EdgeOne may use to reach sv.
func originAddrs(ctx context.Context, sv store.Server) map[string]bool {
	out := map[string]bool{strings.ToLower(sv.Host): true}
	if net.ParseIP(sv.Host) == nil {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if ips, err := net.DefaultResolver.LookupHost(ctx, sv.Host); err == nil {
			for _, ip := range ips {
				out[ip] = true
			}
		}
	}
	return out
}

// ProposeRealIP makes the checklist for one server: its Nginx, and every
// EdgeOne site with a domain that fetches from it.
func (a *App) ProposeRealIP(ctx context.Context, serverID int64) (PlanView, error) {
	c := a.tencentClient()
	if c == nil {
		return PlanView{}, userErr("要在 EdgeOne 里设置，请先在「设置 → 腾讯云」填写密钥")
	}
	sv, err := a.Store.GetServer(serverID)
	if err != nil {
		return PlanView{}, userErr("找不到这台服务器")
	}
	zones, err := c.Zones(ctx)
	if err != nil {
		return PlanView{}, err
	}
	addrs := originAddrs(ctx, sv)
	var sites, domains []string
	for _, z := range zones {
		if z.Paused || z.Status == "initializing" {
			continue
		}
		ds, err := c.AccelerationDomains(ctx, z.ZoneID, "")
		if err != nil {
			return PlanView{}, err
		}
		found := false
		for _, d := range ds {
			if addrs[strings.ToLower(d.OriginDetail.Origin)] {
				domains = append(domains, d.DomainName)
				found = true
			}
		}
		if found {
			sites = append(sites, z.ZoneName)
		}
	}
	if len(sites) == 0 {
		return PlanView{}, userErr("EdgeOne 里没有回源到 %s 的加速域名，所以不需要这样设置", sv.Host)
	}
	sort.Strings(domains)
	steps := []core.Step{{Capability: "nginx.realip", Summary: "在服务器的 Nginx 里添加一个配置文件：带着这个请求头的请求，用它里面的 IP 作为访客地址"}}
	for _, s := range sites {
		steps = append(steps, core.Step{Capability: "eo.clientip.header", Summary: fmt.Sprintf("EdgeOne 站点 %s 回源时用这个请求头带上访客 IP", s),
			Params: map[string]any{"domain": s}})
	}
	reason := fmt.Sprintf("这些网站经过 EdgeOne 回源到 %s：%s。服务器日志里记的是 EdgeOne 节点的地址，所以访客 IP、UV、地区和风险 IP 都不准，网站程序看到的也是节点的地址。\n"+
		"设置后，EdgeOne 回源时用一个随机名字的请求头带上真实访客 IP，只有 EdgeOne 和这台服务器知道这个名字；Nginx 只认这个请求头，"+
		"所以直接访问服务器的请求没法冒充别人。之后的访问日志、网站程序和 1Panel 看到的都是真实访客 IP，以前的日志不会改变。",
		sv.Host, strings.Join(domains, "、"))
	p, _, err := a.proposePlan(ctx, "user", serverID, "让服务器日志记录真实访客 IP", reason, steps)
	if err != nil {
		return PlanView{}, err
	}
	return a.Plan(p.ID)
}

// realIPSince is when the server's Nginx started taking visitors' IPs
// from EdgeOne, or "" if it does not.
func (a *App) realIPSince(serverID int64) string {
	plans, err := a.Store.ListPlans(500)
	if err != nil {
		return ""
	}
	last, status := "", ""
	for _, p := range plans {
		if p.ServerID != serverID {
			continue
		}
		var steps []core.Step
		if json.Unmarshal([]byte(p.Steps), &steps) != nil {
			continue
		}
		for _, s := range steps {
			if s.Capability == "nginx.realip" && s.FinishedAt > last {
				last, status = s.FinishedAt, s.Status
			}
		}
	}
	if status != "done" {
		return ""
	}
	return last
}

// sinceText shows a stored time as a local date and time.
func sinceText(at string) string {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return at
	}
	return t.Local().Format("1月2日 15:04")
}
