package app

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/internal/update"
)

// A diagnostics bundle for reporting a problem: what Miao Panel runs on,
// what is set up (yes or no, never keys, passwords or addresses), the
// servers with their addresses masked, and the recent failures. The user
// saves it and can read it before sending it anywhere.

var (
	ipv4Re = regexp.MustCompile(`\b(\d{1,3})\.(\d{1,3})\.\d{1,3}\.\d{1,3}\b`)
	// Four groups or more, or a "::": clock times like 10:18:43 stay.
	ipv6Re = regexp.MustCompile(`(?i)\b([0-9a-f]{1,4})(?::[0-9a-f]{1,4}){3,7}\b|\b([0-9a-f]{1,4})(?::[0-9a-f]{1,4})*::(?:[0-9a-f]{1,4}(?::[0-9a-f]{1,4})*)?`)
)

// maskHost keeps enough of an address to tell servers apart.
func maskHost(h string) string {
	if ip := net.ParseIP(h); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return fmt.Sprintf("%d.%d.*.*", v4[0], v4[1])
		}
		return "IPv6"
	}
	parts := strings.Split(h, ".")
	if len(parts) <= 2 {
		return "*." + h
	}
	return "*." + strings.Join(parts[len(parts)-2:], ".")
}

// maskText hides IP addresses in free text.
func maskText(s string) string {
	s = ipv4Re.ReplaceAllString(s, "$1.$2.*.*")
	return ipv6Re.ReplaceAllString(s, "$1$2:*")
}

// masker hides addresses in free text, the servers' own (names such as
// blog.example.com included) first.
func masker(servers []store.Server) func(string) string {
	var pairs []string
	for _, sv := range servers {
		if h := strings.TrimSpace(sv.Host); h != "" {
			pairs = append(pairs, h, maskHost(h))
		}
	}
	r := strings.NewReplacer(pairs...)
	return func(s string) string { return maskText(r.Replace(s)) }
}

type diagServer struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Adapter  string `json:"adapter"`
	AuthKind string `json:"authKind"`
	OS       string `json:"os,omitempty"`
	Panel    string `json:"panel,omitempty"`
	Profiled string `json:"profiled,omitempty"`
	OnePanel bool   `json:"onePanelApi,omitempty"`
	BT       bool   `json:"btApi,omitempty"`
}

type diagExec struct {
	At         string `json:"at"`
	Server     string `json:"server"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Capability string `json:"capability,omitempty"`
	Via        string `json:"via,omitempty"`
	Status     string `json:"status"`
	Output     string `json:"output,omitempty"` // failures only, the end
}

// Diagnostics writes the bundle as a zip.
func (a *App) Diagnostics(w io.Writer) error {
	zw := zip.NewWriter(w)
	now := time.Now()
	create := func(name string) (io.Writer, error) {
		return zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: now})
	}
	put := func(name string, v any) error {
		f, err := create(name)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(v)
	}

	info := map[string]any{
		"version": a.Version, "os": runtime.GOOS, "arch": runtime.GOARCH, "go": runtime.Version(),
		"generatedAt": time.Now().UTC().Format(time.RFC3339), "docker": update.InDocker(), "secretStore": a.Secrets.Kind(),
	}
	set := map[string]any{"tencent": a.Tencent().Configured, "aliyun": a.Aliyun().Configured}
	if ai, err := a.AISettings(); err == nil {
		set["ai"] = map[string]any{"preset": ai.PresetID, "model": ai.Model, "hasKey": ai.HasKey}
	}
	ms := a.MonitorSettings()
	set["monitor"] = map[string]any{"enabled": ms.Enabled, "auto": ms.Auto, "servers": ms.Servers, "extra": len(ms.Extra), "mail": ms.Email != ""}
	set["updateCheck"] = a.updateEnabled()
	set["database"] = map[bool]string{true: "mysql", false: "sqlite"}[a.Store.IsMySQL()]
	info["settings"] = set

	servers := []diagServer{}
	list, _ := a.Store.ListServers()
	mask := masker(list)
	for _, sv := range list {
		// A server added without a name is named after its address.
		d := diagServer{ID: sv.ID, Name: mask(sv.Name), Host: maskHost(sv.Host), Adapter: sv.Adapter, AuthKind: sv.AuthKind}
		if v, err := a.Profile(sv.ID); err == nil && v.Profile != nil {
			d.OS, d.Panel, d.Profiled = v.Profile.OS, v.Profile.Panel.Version, v.CollectedAt
		}
		if s, err := a.OnePanel(sv.ID); err == nil {
			d.OnePanel = s.HasKey
		}
		if s, err := a.BT(sv.ID); err == nil {
			d.BT = s.HasKey
		}
		servers = append(servers, d)
	}
	info["servers"] = servers
	if err := put("miaopanel.json", info); err != nil {
		return err
	}

	execs := []diagExec{}
	if list, err := a.Store.ListExec(false, 200); err == nil {
		for _, e := range list {
			d := diagExec{At: e.StartedAt, Server: mask(e.ServerName), Kind: e.Kind, Title: mask(e.Title), Capability: e.Capability, Via: e.Via, Status: e.Status}
			if e.Status != "done" && e.Status != "undone" {
				out := e.Output
				if r := []rune(out); len(r) > 1500 {
					out = string(r[len(r)-1500:])
				}
				d.Output = mask(out)
			}
			execs = append(execs, d)
		}
	}
	if err := put("recent-activity.json", execs); err != nil {
		return err
	}
	incidents, _ := a.Store.Incidents(time.Now().Add(-7*24*time.Hour).UTC().Format(time.RFC3339), 200)
	for i := range incidents {
		in := &incidents[i]
		in.Target, in.Name, in.Reason = mask(in.Target), mask(in.Name), mask(in.Reason)
	}
	if incidents == nil {
		incidents = []store.Incident{}
	}
	if err := put("incidents.json", incidents); err != nil {
		return err
	}
	readme, err := create("README.txt")
	if err != nil {
		return err
	}
	_, _ = io.WriteString(readme, `Miao Panel 诊断包

用来报告问题。里面有：
- miaopanel.json：版本、系统、哪些功能配置了（只有是否配置，没有密钥和密码）、服务器列表（地址只保留前两段）
- recent-activity.json：最近 200 条查看和修改记录的标题和结果，失败的附上最后一段输出（IPv4 地址已部分隐藏）
- incidents.json：最近 7 天的监控故障记录

没有：AI 和云账号的密钥、服务器密码、对话内容、网站访问日志。
标题里可能有你的网站域名，发出去之前可以打开看看。
`)
	return zw.Close()
}
