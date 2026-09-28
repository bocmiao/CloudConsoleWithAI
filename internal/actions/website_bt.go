package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bocmiao/CloudConsoleWithAI/internal/btpanel"
)

// ---- The same website changes on 宝塔面板 ----
//
// The site.* capabilities (and cert.issue, site.backup) get a "bt"
// implementation next to 1Panel's, so the page and the AI ask for them
// the same way and the server's panel decides how they run. What 宝塔's
// API cannot do (HSTS, HTTP/3, choosing a stored certificate, creating or
// deleting sites, restoring a site's backup) is not offered for it.
//
// This file's init runs after website.go's (files initialise in name
// order), so the capabilities exist when the implementations are added.

func btSiteFor(ctx context.Context, b *btpanel.Client, domain string) (btpanel.Site, error) {
	if s, err := b.Site(ctx, domain); err == nil {
		return s, nil
	}
	sites, err := b.Sites(ctx)
	if err != nil {
		return btpanel.Site{}, fmt.Errorf("读取宝塔网站列表失败：%w", err)
	}
	for _, s := range sites {
		if strings.EqualFold(s.Name, domain) || strings.EqualFold(s.Title, domain) {
			return s, nil
		}
	}
	return btpanel.Site{}, fmt.Errorf("宝塔里没有网站 %s", domain)
}

func traceBT(env *Env, run func() Outcome) Outcome {
	var cmds []string
	env.BT.Trace = func(method, path string, body []byte) {
		line := method + " " + path
		if len(body) > 0 {
			line += " " + string(body)
		}
		cmds = append(cmds, line)
	}
	defer func() { env.BT.Trace = nil }()
	out := run()
	out.Commands = append([]string{"# 在服务器本机调用宝塔面板接口（经 SSH 隧道，或用自动化助手运行 curl；请求带签名，密钥不记录）"}, cmds...)
	return out
}

func applyBT(ctx context.Context, env *Env, r Resolved, progress Progress) Outcome {
	out := &Outcome{Undo: map[string]string{}}
	if env.BT == nil {
		out.Status = StatusRefused
		out.logf("还没有配置这台服务器的宝塔接口（服务器的「连接设置」里填写）")
		return *out
	}
	report := func(format string, args ...any) {
		out.logf(format, args...)
		if progress != nil {
			progress(out.Log)
		}
	}
	return traceBT(env, func() Outcome {
		b, v := env.BT, r.Values
		domain := v["website"]
		if r.Impl.Panel == "bt_cert_issue" && domain == "" {
			domain = v["domain"]
		}
		s, err := btSiteFor(ctx, b, domain)
		if err != nil {
			return refused(out, "%v", err)
		}
		out.Undo["site"] = s.Name
		done := func(format string, args ...any) Outcome {
			report(format, args...)
			out.Status = StatusDone
			return *out
		}
		nothing := func(format string, args ...any) Outcome {
			report(format, args...)
			out.Status, out.Undo = StatusDone, map[string]string{}
			return *out
		}
		switch r.Impl.Panel {
		case "bt_site_status":
			run := v["action"] == "start"
			if run == s.Running {
				return nothing("网站 %s 已经是%s状态，不需要修改", s.Name, map[bool]string{true: "运行", false: "停止"}[run])
			}
			report("正在%s网站 %s", map[bool]string{true: "启动", false: "停止"}[run], s.Name)
			if err := b.SetSiteRunning(ctx, s, run); err != nil {
				return refused(out, "操作失败：%v", err)
			}
			out.Undo["running"] = strconv.FormatBool(s.Running)
			return done("完成")

		case "bt_domain_add":
			port, _ := strconv.Atoi(v["port"])
			ds, err := b.Domains(ctx, s)
			if err != nil {
				return refused(out, "读取网站的域名失败：%v", err)
			}
			for _, d := range ds {
				if strings.EqualFold(d.Name, v["domain"]) && d.Port == port {
					return nothing("网站 %s 已经有域名 %s:%d 了", s.Name, d.Name, port)
				}
			}
			name := v["domain"]
			if port != 80 {
				name += ":" + strconv.Itoa(port)
			}
			report("正在给网站 %s 添加域名 %s", s.Name, name)
			if err := b.AddDomains(ctx, s, name); err != nil {
				return refused(out, "添加失败：%v", err)
			}
			out.Undo["domain"], out.Undo["port"] = v["domain"], strconv.Itoa(port)
			return done("完成：%s 现在也由这个网站提供服务（域名还要解析到这台服务器）", v["domain"])

		case "bt_domain_remove":
			ds, err := b.Domains(ctx, s)
			if err != nil {
				return refused(out, "读取网站的域名失败：%v", err)
			}
			want, _ := strconv.Atoi(v["port"])
			var gone []string
			for _, d := range ds {
				if !strings.EqualFold(d.Name, v["domain"]) || (want != 0 && d.Port != want) {
					continue
				}
				report("正在删除网站 %s 的域名 %s:%d", s.Name, d.Name, d.Port)
				if err := b.DeleteDomain(ctx, s, d); err != nil {
					return refused(out, "删除失败：%v", err)
				}
				gone = append(gone, d.Name+":"+strconv.Itoa(d.Port))
			}
			if len(gone) == 0 {
				return nothing("网站 %s 没有域名 %s，不需要修改", s.Name, v["domain"])
			}
			out.Undo["removed"] = strings.Join(gone, ",")
			return done("完成")

		case "bt_https":
			if v["enabled"] == "off" || v["cert"] != "" || v["hsts"] != "" || v["http3"] != "" || v["http_mode"] == "HTTPSOnly" {
				return refused(out, "宝塔网站在这里只能切换「HTTP 跳转到 HTTPS」；关闭 HTTPS、换证书、HSTS 和 HTTP/3 请在宝塔面板里设置，申请免费证书用 cert.issue")
			}
			ssl, err := b.SSL(ctx, s)
			if err != nil {
				return refused(out, "读取 HTTPS 设置失败：%v", err)
			}
			if !ssl.Enabled {
				return refused(out, "网站 %s 还没有开启 HTTPS，先申请证书（cert.issue）", s.Name)
			}
			force := v["http_mode"] == "HTTPToHTTPS"
			if v["http_mode"] == "" || force == ssl.ForceHTTPS {
				return nothing("网站 %s 的 HTTPS 设置不需要修改", s.Name)
			}
			report("正在%s网站 %s 的 HTTP 跳转 HTTPS", map[bool]string{true: "开启", false: "关闭"}[force], s.Name)
			if err := b.SetForceHTTPS(ctx, s, force); err != nil {
				return refused(out, "设置失败：%v", err)
			}
			out.Undo["force"] = strconv.FormatBool(ssl.ForceHTTPS)
			return done("完成")

		case "bt_cert_issue":
			if v["method"] == "dns" {
				return refused(out, "宝塔网站在这里只能用 HTTP 验证申请证书；泛域名证书请在宝塔面板里用 DNS 验证申请")
			}
			before, err := b.SSL(ctx, s)
			if err != nil {
				return refused(out, "读取 HTTPS 设置失败：%v", err)
			}
			domains := append([]string{v["domain"]}, lines(v["other_domains"])...)
			report("正在让宝塔向 Let's Encrypt 申请 %s 的证书（HTTP 验证，一般一分钟左右）……", strings.Join(domains, "、"))
			got, err := b.ApplyLetsEncrypt(ctx, s, domains)
			if err != nil {
				return refused(out, "申请失败：%v。常见原因：域名还没解析到这台服务器，或 80 端口访问不到", err)
			}
			if v["http_mode"] == "HTTPToHTTPS" && !got.ForceHTTPS {
				if err := b.SetForceHTTPS(ctx, s, true); err != nil {
					report("证书已装好，但开启 HTTP 跳转 HTTPS 失败：%v", err)
				}
			}
			if !before.Enabled {
				out.Undo["was_off"] = "true"
			} else {
				// The certificate it replaced cannot be put back: its key is
				// not readable through the API.
				out.Undo = map[string]string{}
				report("注意：网站原来就开着 HTTPS，原来的证书不能通过接口恢复，这一步不能一键撤销")
			}
			exp := ""
			if got.Cert != nil {
				exp = "，有效期到 " + got.Cert.NotAfter
			}
			return done("完成：%s 已开启 HTTPS%s。宝塔会在到期前自动续签", s.Name, exp)

		case "bt_proxy_set", "bt_proxy_remove", "bt_proxy_status":
			list, err := b.Proxies(ctx, s)
			if err != nil {
				return refused(out, "读取反向代理失败：%v", err)
			}
			var old *btpanel.Proxy
			for i := range list {
				if list[i].Name == v["name"] {
					old = &list[i]
				}
			}
			if old != nil {
				data, _ := json.Marshal(old)
				out.Undo["old"] = string(data)
			}
			switch r.Impl.Panel {
			case "bt_proxy_set":
				p := btpanel.Proxy{Name: v["name"], Site: s.Name, Dir: v["path"], Target: v["target"], Host: v["host"], Enabled: true}
				if p.Dir == "" {
					p.Dir = "/"
				}
				if p.Host == "" {
					p.Host = "$host"
				}
				if old != nil {
					p.Cache, p.CacheMinutes, p.Replace = old.Cache, old.CacheMinutes, old.Replace
					if old.Dir == p.Dir && old.Target == p.Target && old.Host == p.Host && old.Enabled {
						return nothing("反向代理 %s 已经是这样，不需要修改", p.Name)
					}
					report("正在修改网站 %s 的反向代理 %s：%s → %s", s.Name, p.Name, p.Dir, p.Target)
					err = b.ModifyProxy(ctx, s, p)
				} else {
					report("正在给网站 %s 添加反向代理 %s：%s → %s", s.Name, p.Name, p.Dir, p.Target)
					err = b.CreateProxy(ctx, s, p)
					out.Undo["created"] = p.Name
				}
				if err != nil {
					return refused(out, "设置失败：%v", err)
				}
				return done("完成")
			case "bt_proxy_remove":
				if old == nil {
					return nothing("网站 %s 没有反向代理 %s，不需要删除", s.Name, v["name"])
				}
				report("正在删除网站 %s 的反向代理 %s（%s → %s）", s.Name, old.Name, old.Dir, old.Target)
				if err := b.RemoveProxy(ctx, s, old.Name); err != nil {
					return refused(out, "删除失败：%v", err)
				}
				return done("完成")
			default:
				if old == nil {
					return refused(out, "网站 %s 没有反向代理 %s", s.Name, v["name"])
				}
				on := v["enabled"] == "on"
				if old.Enabled == on {
					return nothing("反向代理 %s 已经是%s状态", old.Name, map[bool]string{true: "启用", false: "停用"}[on])
				}
				p := *old
				p.Enabled = on
				report("正在%s反向代理 %s", map[bool]string{true: "启用", false: "停用"}[on], p.Name)
				if err := b.ModifyProxy(ctx, s, p); err != nil {
					return refused(out, "操作失败：%v", err)
				}
				return done("完成")
			}

		case "bt_conf", "bt_rewrite":
			rewrite := r.Impl.Panel == "bt_rewrite"
			var cur btpanel.File
			if rewrite {
				cur, err = b.Rewrite(ctx, s)
			} else {
				cur, err = b.NginxConf(ctx, s)
			}
			if err != nil {
				return refused(out, "读取原文件失败：%v", err)
			}
			if cur.ReadOnly {
				return refused(out, "%s 是只读的（开了防篡改，或者太大），没有修改", cur.Path)
			}
			if h := v["base_hash"]; h != "" && ConfHash(cur.Content) != h {
				return refused(out, "文件在生成清单之后被改过了，没有修改。请刷新后重新编辑")
			}
			next := normConf(v["content"])
			if normConf(cur.Content) == next {
				return nothing("内容没有变化，不需要修改")
			}
			what := map[bool]string{true: "伪静态规则", false: "Nginx 配置文件"}[rewrite]
			report("正在写入网站 %s 的%s（宝塔先用 nginx -t 检查，不通过自动恢复）", s.Name, what)
			if rewrite {
				err = b.SetRewrite(ctx, s, next, cur.ModTime)
			} else {
				err = b.SetNginxConf(ctx, s, next, cur.ModTime)
			}
			if err != nil {
				return refused(out, "没有修改：%v", err)
			}
			if err := b.TestNginx(ctx); err != nil {
				if rewrite {
					_ = b.SetRewrite(ctx, s, cur.Content, "")
				} else {
					_ = b.SetNginxConf(ctx, s, cur.Content, "")
				}
				out.Status = StatusRolledBack
				out.logf("nginx 检查不通过，已恢复原来的内容：%v", err)
				return *out
			}
			out.Undo["content"] = cur.Content
			return done("完成：检查通过，已生效")

		case "bt_backup":
			report("正在备份网站 %s（网站目录）……", s.Name)
			bk, err := b.BackupSite(ctx, s)
			if err != nil {
				out.Status = StatusFailed
				out.logf("备份失败：%v", err)
				return *out
			}
			out.Result = map[string]string{"backup": bk.Name}
			out.Undo = map[string]string{}
			return done("完成：已备份到 %s", bk.File)
		}
		out.Status = StatusFailed
		out.logf("未知的宝塔操作 %s", r.Impl.Panel)
		return *out
	})
}

func undoBT(ctx context.Context, env *Env, r Resolved, undo map[string]string) Outcome {
	out := Outcome{}
	if len(undo) == 0 || (len(undo) == 1 && undo["site"] != "") {
		out.Status = StatusUndone
		out.logf("这一步当时没有做任何修改，不需要撤销")
		return out
	}
	if env.BT == nil {
		out.Status = StatusRefused
		out.logf("还没有配置这台服务器的宝塔接口")
		return out
	}
	return traceBT(env, func() Outcome {
		b := env.BT
		s, err := btSiteFor(ctx, b, undo["site"])
		if err == nil {
			err = undoBTStep(ctx, b, s, r.Impl.Panel, undo)
		}
		if err != nil {
			out.Status = StatusFailed
			out.logf("撤销失败：%v", err)
			return out
		}
		out.Status = StatusUndone
		out.logf("已撤销")
		return out
	})
}

func undoBTStep(ctx context.Context, b *btpanel.Client, s btpanel.Site, op string, undo map[string]string) error {
	switch op {
	case "bt_site_status":
		return b.SetSiteRunning(ctx, s, undo["running"] == "true")
	case "bt_domain_add":
		ds, err := b.Domains(ctx, s)
		if err != nil {
			return err
		}
		port, _ := strconv.Atoi(undo["port"])
		for _, d := range ds {
			if strings.EqualFold(d.Name, undo["domain"]) && d.Port == port {
				return b.DeleteDomain(ctx, s, d)
			}
		}
		return nil
	case "bt_domain_remove":
		var back []string
		for _, x := range strings.Split(undo["removed"], ",") {
			back = append(back, strings.TrimSuffix(x, ":80"))
		}
		return b.AddDomains(ctx, s, back...)
	case "bt_https":
		return b.SetForceHTTPS(ctx, s, undo["force"] == "true")
	case "bt_cert_issue":
		if undo["was_off"] == "true" {
			return b.CloseSSL(ctx, s)
		}
		return errors.New("原来的证书不能通过接口恢复")
	case "bt_proxy_set", "bt_proxy_remove", "bt_proxy_status":
		if name := undo["created"]; name != "" {
			return b.RemoveProxy(ctx, s, name)
		}
		var old btpanel.Proxy
		if err := json.Unmarshal([]byte(undo["old"]), &old); err != nil {
			return err
		}
		if op == "bt_proxy_remove" {
			return b.CreateProxy(ctx, s, old)
		}
		return b.ModifyProxy(ctx, s, old)
	case "bt_conf":
		if err := b.SetNginxConf(ctx, s, undo["content"], ""); err != nil {
			return err
		}
		return b.TestNginx(ctx)
	case "bt_rewrite":
		return b.SetRewrite(ctx, s, undo["content"], "")
	}
	return fmt.Errorf("未知的宝塔操作 %s", op)
}

func init() {
	bt := func(capability, op, downtime, undo string) {
		c := registry[capability]
		if c == nil {
			panic("no capability " + capability)
		}
		c.Impls["bt"] = Impl{Via: "宝塔接口", Panel: op, Downtime: downtime, Undo: undo}
	}
	bt("site.status", "bt_site_status", "停止期间网站打不开（显示宝塔的「网站已停止」页）；不影响其他网站", "恢复成原来的运行或停止状态")
	bt("site.domain.add", "bt_domain_add", "不影响现有访问；宝塔重新加载 Nginx", "删除这个域名")
	bt("site.domain.remove", "bt_domain_remove", "访问这个域名不会再到这个网站", "把删掉的域名加回来")
	bt("site.https.set", "bt_https", "不中断访问", "恢复原来的跳转设置")
	bt("cert.issue", "bt_cert_issue", "不中断访问；宝塔装好证书后重新加载 Nginx", "关闭这次开启的 HTTPS（原来就开着 HTTPS 时不能撤销）")
	bt("site.proxy.set", "bt_proxy_set", "改动的路径会转到新的后端；代理整个网站时宝塔会把 PHP 改成纯静态", "新建的规则删除；修改的规则恢复原来的样子")
	bt("site.proxy.remove", "bt_proxy_remove", "这个路径不再转发到后端", "按原来的设置把规则加回来")
	bt("site.proxy.status", "bt_proxy_status", "停用后这个路径不再转发到后端", "恢复成原来的启用或停用状态")
	bt("site.conf.set", "bt_conf", "宝塔先用 nginx -t 检查，通过后重新加载，不中断访问；不通过自动恢复原文件", "把配置文件恢复成修改前的内容")
	bt("site.rewrite.set", "bt_rewrite", "宝塔检查通过后重新加载，不中断访问；不通过自动恢复", "恢复原来的伪静态规则")
	bt("site.backup", "bt_backup", "不影响访问；网站很大时服务器会忙一会儿", "")
}
