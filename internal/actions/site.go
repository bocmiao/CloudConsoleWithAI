package actions

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/bocmiao/CloudConsoleWithAI/internal/onepanel"
)

// ---- 1Panel websites ----

var proxyRe = regexp.MustCompile(`^https?://([A-Za-z0-9.-]+|\[[0-9A-Fa-f:]+\]):[0-9]{1,5}(/[A-Za-z0-9._~/-]*)?$`)

func checkSiteParams(v map[string]string) error {
	switch v["type"] {
	case "proxy":
		if (v["app"] == "") == (v["proxy"] == "") {
			return fmt.Errorf("反向代理网站要填 app（代理到哪个 1Panel 应用）或 proxy（后端地址）其中一个")
		}
		if v["proxy"] != "" && !proxyRe.MatchString(v["proxy"]) {
			return fmt.Errorf("proxy 要是后端地址，例如 http://127.0.0.1:8090，%q 不是", v["proxy"])
		}
	case "static":
		if v["app"] != "" || v["proxy"] != "" {
			return fmt.Errorf("静态网站不用填 app 和 proxy")
		}
	}
	return nil
}

func applySiteCreate(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.OnePanel
	domain := v["domain"]
	apps, err := c.InstalledApps(ctx)
	if err != nil {
		out.Status = StatusRefused
		out.logf("读取 1Panel 已安装应用失败：%v", err)
		return *out
	}
	var openresty *onepanel.InstalledApp
	for i := range apps {
		if apps[i].AppKey == "openresty" {
			openresty = &apps[i]
		}
	}
	switch {
	case openresty == nil:
		out.Status = StatusRefused
		out.logf("1Panel 里还没有安装 OpenResty，网站功能需要它。请先在 1Panel「应用商店」安装 OpenResty（会占用服务器的 80 和 443 端口）")
		return *out
	case !strings.EqualFold(openresty.Status, "running"):
		out.Status = StatusRefused
		out.logf("1Panel 的 OpenResty 现在是 %s 状态，没有在运行", openresty.Status)
		return *out
	}
	port := openresty.HTTPPort
	if port == 0 {
		port = 80
	}
	sites, err := c.Websites(ctx)
	if err != nil {
		out.Status = StatusRefused
		out.logf("读取 1Panel 网站列表失败：%v", err)
		return *out
	}
	for _, s := range sites {
		if strings.EqualFold(s.PrimaryDomain, domain) {
			report("1Panel 里已经有网站 %s 了（%s，%s），不需要新建", domain, siteType(s.Type), s.Proxy)
			out.Status, out.Undo = StatusDone, map[string]string{}
			return *out
		}
	}
	proxy := v["proxy"]
	if app := v["app"]; app != "" {
		a, err := findApp(ctx, c, app)
		if err != nil {
			out.Status = StatusRefused
			out.logf("%v", err)
			return *out
		}
		if a.HTTPPort == 0 {
			out.Status = StatusRefused
			out.logf("应用 %s 没有对外的 HTTP 端口，请改用 proxy 填后端地址", a.Name)
			return *out
		}
		proxy = "http://127.0.0.1:" + strconv.Itoa(a.HTTPPort)
	}
	group, err := c.WebsiteGroup(ctx)
	if err != nil {
		out.Status = StatusRefused
		out.logf("读取网站分组失败：%v", err)
		return *out
	}
	if v["type"] == "proxy" {
		report("正在 1Panel 新建网站 %s：反向代理到 %s（OpenResty 端口 %d）", domain, proxy, port)
	} else {
		report("正在 1Panel 新建静态网站 %s（OpenResty 端口 %d）", domain, port)
	}
	err = c.CreateWebsite(ctx, onepanel.NewWebsite{Type: v["type"], Domain: domain, Alias: domain, Proxy: proxy, Port: port, GroupID: group})
	if err != nil {
		out.Status = StatusRefused
		out.logf("新建失败：%v", err)
		return *out
	}
	sites, _ = c.Websites(ctx)
	for _, s := range sites {
		if strings.EqualFold(s.PrimaryDomain, domain) {
			out.Undo["website_id"], out.Undo["domain"] = strconv.FormatUint(uint64(s.ID), 10), domain
			if s.SitePath != "" {
				report("网站目录：%s", s.SitePath)
			}
		}
	}
	if out.Undo["website_id"] == "" {
		out.Status = StatusFailed
		out.logf("1Panel 说创建成功了，但网站列表里找不到 %s，请在 1Panel 里检查", domain)
		return *out
	}
	checkSite(ctx, env, domain, port, report)
	report("完成：%s 已经由 1Panel 的 OpenResty 提供服务", domain)
	out.Status = StatusDone
	return *out
}

// checkSite requests the new site on the server itself and says how it
// answered; a failing backend does not undo the site.
func checkSite(ctx context.Context, env *Env, domain string, port int, report func(string, ...any)) {
	if env.SSH == nil {
		return
	}
	cmd := fmt.Sprintf("curl -s -o /dev/null -m 15 --noproxy '*' -w '%%{http_code}' -H %s http://127.0.0.1:%d/", shq("Host: "+domain), port)
	res, err := env.SSH.Run(ctx, cmd, "", 256)
	code := strings.TrimSpace(res.Stdout)
	switch {
	case err != nil || res.ExitCode != 0 || code == "" || code == "000":
		report("在服务器上访问 http://%s 没有得到回应，请检查 OpenResty", domain)
	case code == "502" || code == "504":
		report("在服务器上访问 http://%s 返回 %s：OpenResty 连不上后端，请确认应用在运行", domain, code)
	default:
		report("在服务器上访问 http://%s 返回 %s", domain, code)
	}
}

func siteType(t string) string {
	switch t {
	case "proxy":
		return "反向代理"
	case "static":
		return "静态网站"
	case "deployment":
		return "一键部署"
	case "runtime":
		return "运行环境"
	}
	return t
}

func undoSiteCreate(ctx context.Context, c *onepanel.Client, undo map[string]string) error {
	id, _ := strconv.ParseUint(undo["website_id"], 10, 64)
	sites, err := c.Websites(ctx)
	if err != nil {
		return err
	}
	for _, s := range sites {
		if uint64(s.ID) == id && strings.EqualFold(s.PrimaryDomain, undo["domain"]) {
			return c.DeleteWebsite(ctx, s.ID)
		}
	}
	return nil // already gone
}
