package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/onepanel"
)

// ---- Certificates through 1Panel ----
//
// 1Panel gets Let's Encrypt certificates with its own ACME client and
// renews them in the background around the clock, so certificates on a
// 1Panel server are issued there rather than by Miao Panel, which only
// runs while the desktop app is open.

// waitSSL polls a certificate until 1Panel has finished with it.
func waitSSL(ctx context.Context, env *Env, id uint, report func(string, ...any)) (onepanel.SSL, error) {
	deadline := time.Now().Add(6 * time.Minute)
	for {
		s, err := env.OnePanel.SSL(ctx, id)
		if err == nil {
			switch s.Status {
			case "ready":
				return s, nil
			case "applyError", "error":
				return s, fmt.Errorf("%s", strings.TrimSpace(s.Message))
			}
		}
		if time.Now().After(deadline) {
			return s, fmt.Errorf("等了 6 分钟还没有签发下来（状态 %s）", s.Status)
		}
		select {
		case <-ctx.Done():
			return s, ctx.Err()
		case <-time.After(pollEvery(env)):
		}
	}
}

// waitRenewed polls until 1Panel holds a certificate newer than before.
func waitRenewed(ctx context.Context, env *Env, id uint, before time.Time) (onepanel.SSL, error) {
	deadline := time.Now().Add(6 * time.Minute)
	for {
		s, err := env.OnePanel.SSL(ctx, id)
		if err == nil {
			switch {
			case s.Status == "ready" && s.ExpireDate.After(before):
				return s, nil
			case s.Status == "applyError" || s.Status == "error":
				return s, fmt.Errorf("%s", strings.TrimSpace(s.Message))
			}
		}
		if time.Now().After(deadline) {
			return s, fmt.Errorf("等了 6 分钟还没有续签下来（状态 %s）", s.Status)
		}
		select {
		case <-ctx.Done():
			return s, ctx.Err()
		case <-time.After(pollEvery(env)):
		}
	}
}

func findSSL(list []onepanel.SSL, domain string) (onepanel.SSL, bool) {
	var best onepanel.SSL
	for _, s := range list {
		if !strings.EqualFold(s.PrimaryDomain, domain) {
			continue
		}
		if best.ID == 0 || (s.Status == "ready" && (best.Status != "ready" || s.ExpireDate.After(best.ExpireDate))) {
			best = s
		}
	}
	return best, best.ID != 0
}

func applyCertIssue(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.OnePanel
	domain := v["domain"]
	others := lines(v["other_domains"])
	refuse := func(format string, args ...any) Outcome {
		out.Status = StatusRefused
		out.logf(format, args...)
		return *out
	}

	apps, err := c.InstalledApps(ctx)
	if err != nil {
		return refuse("读取 1Panel 应用失败：%v", err)
	}
	httpsPort := 443
	for _, a := range apps {
		if a.AppKey == "openresty" && a.HTTPSPort > 0 {
			httpsPort = a.HTTPSPort
		}
	}

	var ssl onepanel.SSL
	list, err := c.SSLs(ctx)
	if err != nil {
		return refuse("读取 1Panel 证书失败：%v", err)
	}
	if existing, ok := findSSL(list, domain); ok && existing.Status == "ready" && sameNames(existing.Names(), append([]string{domain}, others...)) {
		ssl = existing
		report("1Panel 里已经有 %s 的证书（有效期到 %s，自动续签：%s），直接使用", domain, existing.ExpireDate.Format("2006-01-02"), yesNo(existing.AutoRenew))
	} else {
		account, err := acmeAccount(ctx, c, v["email"], report)
		if err != nil {
			return refuse("%v", err)
		}
		n := onepanel.NewSSL{Domain: domain, OtherDomains: others, Provider: "http", AcmeAccount: account, AutoRenew: true, Description: "Miao Panel"}
		if v["method"] == "dns" {
			id, err := dnsAccount(ctx, c, v["dns_account"])
			if err != nil {
				return refuse("%v", err)
			}
			n.Provider, n.DNSAccount = "dnsAccount", id
			report("正在让 1Panel 向 Let's Encrypt 申请 %s 的证书（DNS 验证），并开启自动续签", strings.Join(append([]string{domain}, others...), "、"))
		} else {
			report("正在让 1Panel 向 Let's Encrypt 申请 %s 的证书（HTTP 验证：Let's Encrypt 会访问 http://%s/.well-known/acme-challenge/，域名要能访问到这台服务器，经过 EdgeOne 也可以），并开启自动续签",
				strings.Join(append([]string{domain}, others...), "、"), domain)
		}
		id, err := c.CreateSSL(ctx, n)
		if err != nil {
			return refuse("申请失败：%v", err)
		}
		ssl, err = waitSSL(ctx, env, id, report)
		if err != nil {
			_ = c.DeleteSSL(ctx, id)
			return refuse("没有申请下来：%v。常见原因：域名还没有解析到这台服务器或 EdgeOne、80 端口没有放行，或者 Let's Encrypt 一周内给这个域名签发得太多", err)
		}
		out.Undo["ssl_id"] = strconv.FormatUint(uint64(id), 10)
		report("已签发：%s，有效期到 %s。1Panel 会在到期前自动续签", ssl.Organization, ssl.ExpireDate.Format("2006-01-02"))
	}

	if v["apply"] == "no" {
		out.Status = StatusDone
		return *out
	}
	site := v["website"]
	if site == "" {
		site = domain
	}
	sites, err := c.Websites(ctx)
	if err != nil {
		return rollbackIssue(ctx, env, out, "读取网站列表失败：%v", err)
	}
	var w *onepanel.Website
	for i := range sites {
		if strings.EqualFold(sites[i].PrimaryDomain, site) {
			w = &sites[i]
		}
	}
	if w == nil {
		if v["website"] != "" {
			return rollbackIssue(ctx, env, out, "1Panel 里没有网站 %s", site)
		}
		report("1Panel 里没有 %s 这个网站，证书先保存在 1Panel 里，没有启用", site)
		out.Status = StatusDone
		return *out
	}
	prev, err := c.WebsiteHTTPS(ctx, w.ID)
	if err != nil {
		return rollbackIssue(ctx, env, out, "读取网站 %s 的 HTTPS 设置失败：%v", site, err)
	}
	if prev.Enable && prev.SSL.ID == ssl.ID && (v["http_mode"] == "" || prev.HTTPConfig == v["http_mode"]) {
		report("网站 %s 已经在用这张证书了", site)
		out.Status = StatusDone
		return *out
	}
	next := prev
	next.Enable, next.HTTPConfig = true, v["http_mode"]
	next.SSL.ID = ssl.ID
	report("正在给网站 %s 开启 HTTPS（%s）", site, httpModeText[v["http_mode"]])
	if err := c.SetWebsiteHTTPS(ctx, w.ID, next); err != nil {
		return rollbackIssue(ctx, env, out, "开启 HTTPS 失败：%v", err)
	}
	data, _ := json.Marshal(prev)
	out.Undo["website_id"], out.Undo["https"] = strconv.FormatUint(uint64(w.ID), 10), string(data)
	checkHTTPS(ctx, env, site, httpsPort, report)
	report("完成：https://%s 使用 1Panel 的证书", site)
	out.Status = StatusDone
	return *out
}

var httpModeText = map[string]string{
	"HTTPAlso":    "HTTP 和 HTTPS 都能访问",
	"HTTPToHTTPS": "HTTP 自动跳转到 HTTPS",
	"HTTPSOnly":   "只能用 HTTPS 访问",
}

func yesNo(b bool) string {
	if b {
		return "开"
	}
	return "关"
}

func sameNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, x := range a {
		seen[strings.ToLower(x)] = true
	}
	for _, x := range b {
		if !seen[strings.ToLower(x)] {
			return false
		}
	}
	return true
}

// rollbackIssue deletes a certificate this step created when a later part
// of the step fails.
func rollbackIssue(ctx context.Context, env *Env, out *Outcome, format string, args ...any) Outcome {
	out.logf(format, args...)
	out.Status = StatusRefused
	if id, _ := strconv.ParseUint(out.Undo["ssl_id"], 10, 64); id > 0 {
		if err := env.OnePanel.DeleteSSL(ctx, uint(id)); err != nil {
			out.Status = StatusFailed
			out.logf("刚申请的证书没能删除（%v），可以在 1Panel「网站 → 证书」里删除", err)
		} else {
			out.Status = StatusRolledBack
			out.logf("已删除刚申请的证书")
		}
	}
	out.Undo = map[string]string{}
	return *out
}

func acmeAccount(ctx context.Context, c *onepanel.Client, email string, report func(string, ...any)) (uint, error) {
	pick := func() (uint, error) {
		list, err := c.AcmeAccounts(ctx)
		if err != nil {
			return 0, fmt.Errorf("读取 1Panel 的证书账号失败：%w", err)
		}
		for _, a := range list {
			if a.Type == "letsencrypt" && (email == "" || strings.EqualFold(a.Email, email)) {
				return a.ID, nil
			}
		}
		return 0, nil
	}
	id, err := pick()
	if err != nil || id != 0 {
		return id, err
	}
	if email == "" {
		return 0, fmt.Errorf("1Panel 里还没有 Let's Encrypt 账号，请提供一个邮箱（参数 email，Let's Encrypt 用它发到期提醒）")
	}
	report("在 1Panel 里注册 Let's Encrypt 账号（%s）", email)
	if err := c.CreateAcmeAccount(ctx, email); err != nil {
		return 0, fmt.Errorf("注册 Let's Encrypt 账号失败：%w", err)
	}
	if id, err = pick(); err == nil && id == 0 {
		err = fmt.Errorf("注册了 Let's Encrypt 账号，但在 1Panel 里没有找到")
	}
	return id, err
}

func dnsAccount(ctx context.Context, c *onepanel.Client, name string) (uint, error) {
	list, err := c.DNSAccounts(ctx)
	if err != nil {
		return 0, fmt.Errorf("读取 1Panel 的 DNS 账号失败：%w", err)
	}
	for _, a := range list {
		if name != "" && a.Name == name {
			return a.ID, nil
		}
	}
	if name == "" && len(list) == 1 {
		return list[0].ID, nil
	}
	var names []string
	for _, a := range list {
		names = append(names, a.Name+"（"+a.Type+"）")
	}
	if len(list) == 0 {
		return 0, fmt.Errorf("1Panel 里还没有 DNS 账号。DNS 验证（泛域名证书必须用它）需要先在 1Panel「网站 → 证书 → DNS 账户」添加一个，域名在 DNSPod 的选 TencentCloud，建议用只有 DNSPod 权限的子账号密钥；不是泛域名的话也可以改用 HTTP 验证")
	}
	return 0, fmt.Errorf("请用 dns_account 指定 1Panel 的 DNS 账号：%s", strings.Join(names, "、"))
}

// checkHTTPS requests the site over HTTPS on the server itself.
func checkHTTPS(ctx context.Context, env *Env, domain string, port int, report func(string, ...any)) {
	if env.SSH == nil {
		return
	}
	cmd := fmt.Sprintf("curl -sk -o /dev/null -m 15 --noproxy '*' -w '%%{http_code}' --resolve %s https://%s:%d/",
		shq(fmt.Sprintf("%s:%d:127.0.0.1", domain, port)), domain, port)
	res, err := env.SSH.Run(ctx, cmd, "", 256)
	code := strings.TrimSpace(res.Stdout)
	if err != nil || res.ExitCode != 0 || code == "" || code == "000" {
		report("在服务器上访问 https://%s 没有得到回应，请检查 OpenResty 的 %d 端口", domain, port)
		return
	}
	report("在服务器上访问 https://%s 返回 %s", domain, code)
}

func undoCertIssue(ctx context.Context, c *onepanel.Client, undo map[string]string) error {
	if id, _ := strconv.ParseUint(undo["website_id"], 10, 64); id > 0 {
		var prev onepanel.HTTPS
		if err := json.Unmarshal([]byte(undo["https"]), &prev); err != nil {
			return err
		}
		if err := c.SetWebsiteHTTPS(ctx, uint(id), prev); err != nil {
			return fmt.Errorf("恢复网站原来的 HTTPS 设置失败：%w", err)
		}
	}
	if id, _ := strconv.ParseUint(undo["ssl_id"], 10, 64); id > 0 {
		if err := c.DeleteSSL(ctx, uint(id)); err != nil {
			return fmt.Errorf("删除证书失败：%w", err)
		}
	}
	return nil
}

func panelSSL(ctx context.Context, c *onepanel.Client, domain string) (onepanel.SSL, error) {
	list, err := c.SSLs(ctx)
	if err != nil {
		return onepanel.SSL{}, fmt.Errorf("读取 1Panel 证书失败：%w", err)
	}
	s, ok := findSSL(list, domain)
	if !ok {
		return s, fmt.Errorf("1Panel 里没有 %s 的证书", domain)
	}
	return s, nil
}

func applyCertRenew(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	s, err := panelSSL(ctx, env.OnePanel, v["domain"])
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	if s.Provider != "http" && s.Provider != "dnsAccount" {
		out.Status = StatusRefused
		out.logf("这张证书是%s的，不能自动续签，需要重新上传", map[string]string{"manual": "手动上传", "selfSigned": "自签名", "dnsManual": "手动 DNS 验证"}[s.Provider])
		return *out
	}
	before := s.ExpireDate
	report("正在让 1Panel 续签 %s 的证书（现在有效期到 %s）", s.PrimaryDomain, before.Format("2006-01-02"))
	if err := env.OnePanel.RenewSSL(ctx, s.ID); err != nil {
		out.Status = StatusRefused
		out.logf("续签失败：%v", err)
		return *out
	}
	got, err := waitRenewed(ctx, env, s.ID, before)
	if err != nil {
		out.Status = StatusFailed
		out.logf("续签没有成功：%v（原来的证书还在用）", err)
		return *out
	}
	report("完成：新证书有效期到 %s", got.ExpireDate.Format("2006-01-02"))
	out.Status, out.Undo = StatusDone, map[string]string{}
	return *out
}

func applyCertAutoRenew(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	s, err := panelSSL(ctx, env.OnePanel, v["domain"])
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	on := v["enabled"] == "on"
	if s.AutoRenew == on {
		report("%s 的证书自动续签已经是%s的，不需要修改", s.PrimaryDomain, yesNo(on))
		out.Status, out.Undo = StatusDone, map[string]string{}
		return *out
	}
	if on && s.Provider != "http" && s.Provider != "dnsAccount" {
		out.Status = StatusRefused
		out.logf("这张证书不是通过 HTTP 或 DNS 账号验证申请的，1Panel 不能自动续签它；可以用 cert.issue 重新申请一张")
		return *out
	}
	if err := env.OnePanel.SetSSLAutoRenew(ctx, s, on); err != nil {
		out.Status = StatusRefused
		out.logf("修改失败：%v", err)
		return *out
	}
	out.Undo["domain"], out.Undo["enabled"] = s.PrimaryDomain, map[bool]string{true: "on", false: "off"}[s.AutoRenew]
	report("完成：%s 的证书自动续签已%s", s.PrimaryDomain, map[bool]string{true: "开启", false: "关闭"}[on])
	out.Status = StatusDone
	return *out
}

func undoCertAutoRenew(ctx context.Context, c *onepanel.Client, undo map[string]string) error {
	s, err := panelSSL(ctx, c, undo["domain"])
	if err != nil {
		return err
	}
	return c.SetSSLAutoRenew(ctx, s, undo["enabled"] == "on")
}
