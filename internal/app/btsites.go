package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/btpanel"
	"github.com/bocmiao/CloudConsoleWithAI/internal/onepanel"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

// 宝塔 sites on the 网站 page, in the same shapes as 1Panel's so the page
// shows them the same way; Panel says which panel a site is on, and the
// page leaves out what 宝塔 cannot do.

// btTime reads the panel's "2006-01-02 15:04:05" (or a bare date) in the
// server's local time, which Miao Panel takes to be China's.
func btTime(s string) time.Time {
	for _, f := range []string{"2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(f, strings.TrimSpace(s), localZone); err == nil {
			return t
		}
	}
	return time.Time{}
}

func btType(s btpanel.Site) string {
	switch strings.ToLower(s.ProjectType) {
	case "", "php":
		if s.PHPVersion == "" || s.PHPVersion == "静态" || strings.EqualFold(s.PHPVersion, "static") || s.PHPVersion == "00" {
			return "static"
		}
		return "php"
	case "proxy":
		return "proxy"
	}
	return strings.ToLower(s.ProjectType)
}

func btSiteView(s btpanel.Site) SiteView {
	v := SiteView{ID: uint(s.ID), Domain: s.Name, Alias: s.Name, Type: btType(s), Running: s.Running, HTTPS: s.Cert != nil,
		Remark: s.Remark, SitePath: s.Path}
	if v.Type == "php" {
		v.Runtime = "PHP " + s.PHPVersion
	}
	if s.Cert != nil {
		v.CertExpires, v.CertDays = certDays(btTime(s.Cert.NotAfter))
	}
	if t := btTime(s.Created); !t.IsZero() {
		v.CreatedAt = t.Format(time.RFC3339)
	}
	return v
}

func (a *App) btServerSites(ctx context.Context, sv store.Server) SiteServerView {
	out := SiteServerView{ID: sv.ID, Name: sv.Name, Host: sv.Host, Sites: []SiteView{}, Panel: "bt"}
	if s, err := a.BT(sv.ID); err == nil && (!s.HasKey || s.Port == 0) {
		out.NoPanel = true
		return out
	}
	err := a.withBT(ctx, sv.ID, func(_ store.Server, _ sshx.Conn, b *btpanel.Client) error {
		sites, err := b.Sites(ctx)
		for _, s := range sites {
			out.Sites = append(out.Sites, btSiteView(s))
		}
		return err
	})
	switch {
	case errors.Is(err, errNoBT):
		out.NoPanel = true
	case err != nil:
		out.Error = err.Error()
	}
	return out
}

// btSite finds a site by its ID.
func btSite(ctx context.Context, b *btpanel.Client, id uint) (btpanel.Site, error) {
	sites, err := b.Sites(ctx)
	if err != nil {
		return btpanel.Site{}, err
	}
	for _, s := range sites {
		if uint(s.ID) == id {
			return s, nil
		}
	}
	return btpanel.Site{}, userErr("宝塔里找不到这个网站，可能已经删除了")
}

// btSiteDetail reads one 宝塔 site, asking for its parts at once.
func btSiteDetail(ctx context.Context, b *btpanel.Client, id uint) (SiteDetailView, error) {
	v := SiteDetailView{Panel: "bt", Domains: []onepanel.SiteDomain{}, Certs: []CertOption{}, Proxies: []ProxyView{}, AccessLog: true, ErrorLog: true}
	s, err := btSite(ctx, b, id)
	if err != nil {
		return v, err
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		domains []btpanel.Domain
		ssl     btpanel.SSL
		proxies []btpanel.Proxy
		conf    btpanel.File
		rewrite btpanel.File
	)
	fail := func(what string, err error) {
		if err != nil {
			mu.Lock()
			v.Problems = append(v.Problems, what+"："+err.Error())
			mu.Unlock()
		}
	}
	run := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f()
		}()
	}
	run(func() { var e error; domains, e = b.Domains(ctx, s); fail("读取域名失败", e) })
	run(func() { var e error; ssl, e = b.SSL(ctx, s); fail("读取 HTTPS 设置失败", e) })
	run(func() { var e error; proxies, e = b.Proxies(ctx, s); fail("读取反向代理失败", e) })
	run(func() { var e error; conf, e = b.NginxConf(ctx, s); fail("读取配置文件失败", e) })
	run(func() { var e error; rewrite, e = b.Rewrite(ctx, s); fail("读取伪静态失败", e) })
	wg.Wait()
	sort.Strings(v.Problems)

	v.Site, v.SiteDir = btSiteView(s), s.Path
	names := []string{s.Name}
	for _, d := range domains {
		v.Domains = append(v.Domains, onepanel.SiteDomain{ID: uint(d.ID), WebsiteID: uint(s.ID), Domain: d.Name, Port: d.Port})
		if !containsFold(names, d.Name) {
			names = append(names, d.Name)
		}
	}
	v.HTTPS = SiteHTTPSView{Enable: ssl.Enabled, Mode: "HTTPAlso"}
	if ssl.ForceHTTPS {
		v.HTTPS.Mode = "HTTPToHTTPS"
	}
	if c := ssl.Cert; ssl.Enabled && c != nil {
		v.HTTPS.CertNames = c.Domains
		if len(c.Domains) > 0 {
			v.HTTPS.Cert = c.Domains[0]
		}
		v.HTTPS.Expires, v.HTTPS.Days = certDays(btTime(c.NotAfter))
		v.HTTPS.AutoRenew, v.HTTPS.Provider = ssl.Type == 1, c.IssuerOrg
		for _, n := range names {
			if !certCovers(c.Domains, n) {
				v.HTTPS.Uncovered = append(v.HTTPS.Uncovered, n)
			}
		}
	}
	for _, p := range proxies {
		v.Proxies = append(v.Proxies, ProxyView{Name: p.Name, Path: p.Dir, Target: p.Target, Host: p.Host, Enabled: p.Enabled, Cache: p.Cache})
	}
	sort.SliceStable(v.Proxies, func(i, j int) bool { return v.Proxies[i].Path < v.Proxies[j].Path })
	v.Rewrite, v.RewriteHash = rewrite.Content, actions.ConfHash(rewrite.Content)
	v.Conf, v.ConfPath, v.ConfHash = conf.Content, conf.Path, actions.ConfHash(conf.Content)
	if v.ConfPath == "" {
		v.ConfPath = btpanel.NginxConfPath(s)
	}
	return v, nil
}

// btWebsiteLog reads the end of a 宝塔 site's access or error log.
func (a *App) btWebsiteLog(ctx context.Context, serverID int64, siteID uint, kind string, lines int) (SiteLog, error) {
	var out SiteLog
	err := a.withBT(ctx, serverID, func(_ store.Server, _ sshx.Conn, b *btpanel.Client) error {
		s, err := btSite(ctx, b, siteID)
		if err != nil {
			return err
		}
		out.Enabled = true
		out.Path = "/www/wwwlogs/" + s.Name + ".log"
		var text string
		if kind == "error" {
			out.Path = "/www/wwwlogs/" + s.Name + ".error.log"
			text, err = b.SiteErrorLog(ctx, s, lines)
		} else {
			text, err = b.SiteLog(ctx, s, lines)
		}
		if err != nil {
			return userErr("读取日志失败：%v", err)
		}
		if strings.TrimSpace(text) != "日志为空" {
			out.Lines = text
		}
		return nil
	})
	return out, err
}

// btSiteBackups lists a 宝塔 site's backups; 宝塔 keeps no schedule the
// page can change, and cannot put a site's backup back from its API.
func (a *App) btSiteBackups(ctx context.Context, serverID int64, siteID uint) (SiteBackupsView, error) {
	v := SiteBackupsView{Backups: []BackupView{}, Others: []BackupScheduleView{}, Accounts: []onepanel.BackupAccount{}}
	err := a.withBT(ctx, serverID, func(_ store.Server, _ sshx.Conn, b *btpanel.Client) error {
		s, err := btSite(ctx, b, siteID)
		if err != nil {
			return err
		}
		list, err := b.SiteBackups(ctx, s)
		if err != nil {
			return userErr("读取网站的备份失败：%v", err)
		}
		for _, x := range list {
			st := "Success"
			if x.Running {
				st = "Waiting"
			}
			where := "服务器本机"
			if x.Missing {
				where = "云存储（本机没有）"
			}
			bv := BackupView{ID: uint(x.ID), File: x.Name, Account: where, Local: !x.Missing, Status: st, Note: x.Remark}
			if t := btTime(x.Created); !t.IsZero() {
				bv.CreatedAt = t.Format(time.RFC3339)
			}
			v.Backups = append(v.Backups, bv)
		}
		return nil
	})
	return v, err
}

// btBackupPath is where a 宝塔 site backup's file is on the server.
func (a *App) btBackupPath(ctx context.Context, serverID int64, siteID, backupID uint) (string, error) {
	var path string
	err := a.withBT(ctx, serverID, func(_ store.Server, _ sshx.Conn, b *btpanel.Client) error {
		s, err := btSite(ctx, b, siteID)
		if err != nil {
			return err
		}
		list, err := b.SiteBackups(ctx, s)
		if err != nil {
			return userErr("读取网站的备份失败：%v", err)
		}
		for _, x := range list {
			if uint(x.ID) == backupID {
				if x.Missing || !strings.HasPrefix(x.File, "/") {
					return userErr("这份备份不在服务器上（可能只在云存储里），请到宝塔面板下载")
				}
				path = x.File
				return nil
			}
		}
		return userErr("找不到这份备份，可能已经被删除了")
	})
	return path, err
}

// toolBTWebsites is panel_websites for a 宝塔 server.
func (a *App) toolBTWebsites(ctx context.Context, serverID int64) (string, error) {
	sv, err := a.Store.GetServer(serverID)
	if err != nil {
		return "", err
	}
	v := a.btServerSites(ctx, sv)
	switch {
	case v.NoPanel:
		return "这台服务器还没有配置宝塔接口（服务器的「连接设置」→ 宝塔接口），看不到宝塔的网站列表。", nil
	case v.Error != "":
		return "", userErr("%s", v.Error)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "宝塔面板里的网站 %d 个：\n", len(v.Sites))
	for _, s := range v.Sites {
		fmt.Fprintf(&b, "- %s 类型=%s 状态=%s 目录=%s HTTPS=%s", s.Domain, s.Type, map[bool]string{true: "运行中", false: "已停止"}[s.Running], orDash(s.SitePath), yesNoText(s.HTTPS))
		if s.CertExpires != "" {
			fmt.Fprintf(&b, " 证书到期=%s", s.CertExpires[:10])
		}
		if s.Remark != "" {
			fmt.Fprintf(&b, " 备注=%s", s.Remark)
		}
		b.WriteString("\n")
	}
	b.WriteString("宝塔网站能用的操作：site.status、site.domain.add / remove、site.https.set（只能切换 HTTP 跳转 HTTPS）、cert.issue（HTTP 验证）、site.proxy.set / remove / status、site.conf.set、site.rewrite.set、site.backup。新建、删除网站和恢复备份请在宝塔面板里操作。\n")
	return b.String(), nil
}

// toolBTWebsite is panel_website for a 宝塔 site.
func (a *App) toolBTWebsite(ctx context.Context, sv store.Server, domain string, logLines int) (string, error) {
	e := a.startExec(store.ExecLog{ServerID: sv.ID, ServerName: sv.Name, Adapter: sv.Adapter, Origin: originOf(ctx),
		Kind: store.ExecRead, Title: "查看宝塔网站 " + domain, Via: "宝塔接口"})
	var b strings.Builder
	err := a.withBT(ctx, sv.ID, func(_ store.Server, _ sshx.Conn, bt *btpanel.Client) error {
		s, err := bt.Site(ctx, domain)
		if err != nil {
			return err
		}
		v, err := btSiteDetail(ctx, bt, uint(s.ID))
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "网站 %s（宝塔）类型=%s 状态=%s 目录=%s", v.Site.Domain, v.Site.Type, map[bool]string{true: "运行中", false: "已停止"}[v.Site.Running], orDash(v.Site.SitePath))
		if v.Site.Runtime != "" {
			fmt.Fprintf(&b, " %s", v.Site.Runtime)
		}
		b.WriteString("\n")
		var ds []string
		for _, d := range v.Domains {
			ds = append(ds, fmt.Sprintf("%s:%d", d.Domain, d.Port))
		}
		fmt.Fprintf(&b, "域名：%s\n", orDash(strings.Join(ds, "、")))
		if v.HTTPS.Enable {
			fmt.Fprintf(&b, "HTTPS：开（%s）证书包含=%s 到期=%s 签发=%s 自动续签=%s\n", httpModeName[v.HTTPS.Mode], strings.Join(v.HTTPS.CertNames, "、"),
				orDash(v.HTTPS.Expires), orDash(v.HTTPS.Provider), yesNoText(v.HTTPS.AutoRenew))
			if len(v.HTTPS.Uncovered) > 0 {
				fmt.Fprintf(&b, "注意：证书不包含 %s\n", strings.Join(v.HTTPS.Uncovered, "、"))
			}
		} else {
			b.WriteString("HTTPS：关\n")
		}
		if len(v.Proxies) == 0 {
			b.WriteString("反向代理：没有\n")
		}
		for _, pr := range v.Proxies {
			fmt.Fprintf(&b, "反向代理 %s：%s → %s Host=%s %s\n", pr.Name, pr.Path, pr.Target, pr.Host, map[bool]string{true: "启用", false: "停用"}[pr.Enabled])
		}
		if strings.TrimSpace(v.Rewrite) == "" {
			b.WriteString("伪静态规则：没有\n")
		} else {
			fmt.Fprintf(&b, "伪静态规则：\n%s\n", clipText(v.Rewrite, 16000))
		}
		fmt.Fprintf(&b, "Nginx 配置文件 %s：\n%s\n", v.ConfPath, clipText(v.Conf, 40000))
		for _, p := range v.Problems {
			fmt.Fprintf(&b, "（%s）\n", p)
		}
		if text, err := bt.SiteLog(ctx, s, logLines); err == nil {
			fmt.Fprintf(&b, "访问日志最后 %d 行：\n%s\n", logLines, orDash(clipText(text, 12000)))
		}
		if text, err := bt.SiteErrorLog(ctx, s, logLines); err == nil {
			fmt.Fprintf(&b, "错误日志最后 %d 行：\n%s\n", logLines, orDash(clipText(text, 12000)))
		}
		return nil
	})
	e.Commands = "# 在服务器本机调用宝塔面板接口（只读）"
	if err != nil {
		a.finishExec(&e, actions.StatusFailed, err.Error())
		return "", err
	}
	a.finishExec(&e, actions.StatusDone, b.String())
	return b.String(), nil
}
