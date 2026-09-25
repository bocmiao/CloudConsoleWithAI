package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

const (
	tencentIDKey     = "tencent/secret_id"
	tencentSecretKey = "tencent/secret_key"
)

// TencentSettings says whether Tencent Cloud access is configured. The
// credentials themselves stay in the secret store.
type TencentSettings struct {
	Configured bool   `json:"configured"`
	SecretID   string `json:"secretId"` // masked
}

func mask(id string) string {
	if len(id) <= 10 {
		return strings.Repeat("*", len(id))
	}
	return id[:6] + strings.Repeat("*", 6) + id[len(id)-4:]
}

// Tencent returns the Tencent Cloud settings.
func (a *App) Tencent() TencentSettings {
	id, err := a.Secrets.Get(tencentIDKey)
	if err != nil || id == "" {
		return TencentSettings{}
	}
	if _, err := a.Secrets.Get(tencentSecretKey); err != nil {
		return TencentSettings{}
	}
	return TencentSettings{Configured: true, SecretID: mask(id)}
}

// SaveTencent stores Tencent Cloud API credentials.
func (a *App) SaveTencent(secretID, secretKey string) (TencentSettings, error) {
	secretID, secretKey = strings.TrimSpace(secretID), strings.TrimSpace(secretKey)
	if !strings.HasPrefix(secretID, "AKID") || len(secretID) < 16 {
		return a.Tencent(), userErr("SecretId 的格式不对，应该以 AKID 开头")
	}
	if len(secretKey) < 16 {
		return a.Tencent(), userErr("请填写 SecretKey")
	}
	if err := a.Secrets.Set(tencentIDKey, secretID); err != nil {
		return a.Tencent(), err
	}
	if err := a.Secrets.Set(tencentSecretKey, secretKey); err != nil {
		return a.Tencent(), err
	}
	_ = a.Store.Audit("user", "settings.tencent", mask(secretID), "")
	return a.Tencent(), nil
}

// ClearTencent removes the stored credentials.
func (a *App) ClearTencent() TencentSettings {
	_ = a.Secrets.Delete(tencentIDKey)
	_ = a.Secrets.Delete(tencentSecretKey)
	_ = a.Store.Audit("user", "settings.tencent", "清除", "")
	return a.Tencent()
}

// tencentClient returns an API client, or nil when not configured.
func (a *App) tencentClient() *tencent.Client {
	id, err1 := a.Secrets.Get(tencentIDKey)
	key, err2 := a.Secrets.Get(tencentSecretKey)
	if err1 != nil || err2 != nil || id == "" || key == "" {
		return nil
	}
	c := tencent.New(id, key)
	c.Endpoint = a.TencentEndpoint
	return c
}

// TestTencent checks the credentials against DNSPod and EdgeOne.
func (a *App) TestTencent(ctx context.Context) (string, error) {
	c := a.tencentClient()
	if c == nil {
		return "", userErr("请先填写 SecretId 和 SecretKey")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var parts []string
	var errs []error
	if domains, err := c.Domains(ctx); err != nil {
		errs = append(errs, err)
	} else {
		parts = append(parts, fmt.Sprintf("DNSPod：%d 个域名", len(domains)))
	}
	if zones, err := c.Zones(ctx); err != nil {
		errs = append(errs, err)
	} else {
		parts = append(parts, fmt.Sprintf("EdgeOne：%d 个站点", len(zones)))
	}
	if len(parts) == 0 {
		return "", userErr("%v", errors.Join(errs...))
	}
	msg := strings.Join(parts, "，")
	if len(errs) > 0 {
		msg += "；" + errors.Join(errs...).Error()
	}
	return msg, nil
}

// cloudRead runs read-only Tencent Cloud calls for the AI and logs them.
func (a *App) cloudRead(ctx context.Context, title string, run func(c *tencent.Client) (string, error)) (string, error) {
	c := a.tencentClient()
	if c == nil {
		return "", userErr("还没有配置腾讯云密钥，请让用户到「设置 → 腾讯云」填写")
	}
	e := a.startExec(store.ExecLog{ServerName: cloudServer.Name, Adapter: cloudServer.Adapter, Origin: originOf(ctx),
		Kind: store.ExecRead, Title: title, Via: "腾讯云接口"})
	var cmds []string
	c.Trace = func(service, action string, body []byte) { cmds = append(cmds, service+" "+action+" "+string(body)) }
	out, err := run(c)
	e.Commands = strings.Join(append([]string{"# 调用腾讯云 API 3.0（只读查询，请求带 TC3 签名，密钥不记录）"}, cmds...), "\n")
	if err != nil {
		a.finishExec(&e, actions.StatusFailed, err.Error())
		return "", err
	}
	a.finishExec(&e, actions.StatusDone, out)
	return out, nil
}

func (a *App) toolTencentDNS(ctx context.Context, raw json.RawMessage) (string, error) {
	var arg struct {
		Domain    string `json:"domain"`
		Subdomain string `json:"subdomain"`
	}
	if err := parseArgs(raw, &arg); err != nil {
		return "", err
	}
	domain := strings.ToLower(strings.TrimSpace(arg.Domain))
	if domain == "" {
		return a.cloudRead(ctx, "查询 DNSPod 域名列表", func(c *tencent.Client) (string, error) {
			list, err := c.Domains(ctx)
			if err != nil {
				return "", err
			}
			if len(list) == 0 {
				return "DNSPod 里没有域名。", nil
			}
			var b strings.Builder
			for _, d := range list {
				fmt.Fprintf(&b, "%s 状态=%s DNS状态=%s 套餐=%s 记录数=%d\n", d.Name, d.Status, orDash(d.DNSStatus), d.Grade, d.RecordCount)
			}
			return b.String(), nil
		})
	}
	title := "查询 DNSPod 解析：" + domain
	if arg.Subdomain != "" {
		title += "（" + arg.Subdomain + "）"
	}
	return a.cloudRead(ctx, title, func(c *tencent.Client) (string, error) {
		list, err := c.Records(ctx, domain, arg.Subdomain)
		if err != nil {
			return "", err
		}
		if len(list) == 0 {
			return "没有找到解析记录。", nil
		}
		var b strings.Builder
		for _, r := range list {
			fmt.Fprintf(&b, "%s %s %s 线路=%s TTL=%d 状态=%s\n", r.Name, r.Type, r.Value, r.Line, r.TTL, r.Status)
		}
		return b.String(), nil
	})
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

var zoneTypeName = map[string]string{"full": "NS 接入", "partial": "CNAME 接入", "dnsPodAccess": "DNSPod 托管接入", "noDomainAccess": "无域名接入"}

func (a *App) toolTencentEO(ctx context.Context, raw json.RawMessage) (string, error) {
	var arg struct {
		Domain string `json:"domain"`
	}
	if err := parseArgs(raw, &arg); err != nil {
		return "", err
	}
	domain := strings.ToLower(strings.TrimSpace(arg.Domain))
	title := "查询 EdgeOne 站点"
	if domain != "" {
		title = "查询 EdgeOne：" + domain
	}
	return a.cloudRead(ctx, title, func(c *tencent.Client) (string, error) {
		zones, err := c.Zones(ctx)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		if domain == "" {
			if len(zones) == 0 {
				return "EdgeOne 里还没有站点。添加站点需要在 EdgeOne 控制台选择套餐（涉及计费），要由用户自己操作。", nil
			}
			for _, z := range zones {
				fmt.Fprintf(&b, "站点 %s（%s）接入方式=%s 状态=%s 加速区域=%s 已停用=%v\n", z.ZoneName, z.ZoneID, orDash(zoneTypeName[z.Type]+" "+z.Type), z.Status, z.Area, z.Paused)
			}
			return b.String(), nil
		}
		z, ok := tencent.ZoneFor(zones, domain)
		if !ok {
			return fmt.Sprintf("EdgeOne 里没有 %s 所在的站点（现有 %d 个站点）。添加站点需要在 EdgeOne 控制台选择套餐（涉及计费），要由用户自己操作。", domain, len(zones)), nil
		}
		fmt.Fprintf(&b, "站点 %s（%s）接入方式=%s 状态=%s 加速区域=%s\n", z.ZoneName, z.ZoneID, zoneTypeName[z.Type]+" "+z.Type, z.Status, z.Area)
		name := ""
		if domain != z.ZoneName {
			name = domain
		}
		list, err := c.AccelerationDomains(ctx, z.ZoneID, name)
		if err != nil {
			return "", err
		}
		if len(list) == 0 {
			fmt.Fprintf(&b, "加速域名：没有%s\n", map[bool]string{true: " " + domain, false: ""}[name != ""])
		}
		for _, d := range list {
			cert := d.Certificate.Mode
			for _, ci := range d.Certificate.List {
				cert += " " + ci.Status + " 到期=" + ci.ExpireTime
			}
			fmt.Fprintf(&b, "加速域名 %s 状态=%s CNAME=%s 回源=%s %s（HTTP %d / HTTPS %d）证书=%s\n", d.DomainName, d.DomainStatus,
				orDash(d.Cname), d.OriginDetail.Origin, d.OriginProtocol, d.HTTPOriginPort, d.HTTPSOriginPort, orDash(cert))
			if z.Type == "partial" {
				if s, err := c.CnameStatus(ctx, z.ZoneID, d.DomainName); err == nil {
					fmt.Fprintf(&b, "  DNS 是否已解析到 EdgeOne：%s（active=已生效，moved=还没解析过来）\n", orDash(s))
				}
			}
		}
		return b.String(), nil
	})
}
