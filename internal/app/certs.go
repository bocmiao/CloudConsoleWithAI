package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/certs"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// CertEntry is one certificate Miao Panel knows about.
type CertEntry struct {
	Domain     string   `json:"domain"`
	Names      []string `json:"names"`
	Source     string   `json:"source"` // eo, tencent_ssl, 1panel
	Where      string   `json:"where"`
	ServerID   int64    `json:"serverId,omitempty"`
	ID         string   `json:"id,omitempty"`
	Issuer     string   `json:"issuer,omitempty"`
	NotAfter   string   `json:"notAfter,omitempty"`
	DaysLeft   *int     `json:"daysLeft,omitempty"`
	AutoRenew  bool     `json:"autoRenew"`
	Renew      string   `json:"renew"`  // who renews it
	Level      string   `json:"level"`  // ok, warn, crit, info
	Status     string   `json:"status"` // in words
	UsedBy     []string `json:"usedBy,omitempty"`
	CanRenew   bool     `json:"canRenew,omitempty"` // a 1Panel certificate Miao Panel can renew
	RenewError string   `json:"renewError,omitempty"`
}

// LiveCert is what a domain actually serves on port 443.
type LiveCert struct {
	Domain   string   `json:"domain"`
	Names    []string `json:"names,omitempty"`
	Issuer   string   `json:"issuer,omitempty"`
	NotAfter string   `json:"notAfter,omitempty"`
	DaysLeft *int     `json:"daysLeft,omitempty"`
	Level    string   `json:"level"`
	Status   string   `json:"status"`
}

// CertOverview gathers certificates from EdgeOne, Tencent Cloud's SSL
// service and every 1Panel, and what each site serves.
type CertOverview struct {
	Entries   []CertEntry `json:"entries"`
	Live      []LiveCert  `json:"live"`
	Notes     []string    `json:"notes,omitempty"` // sources that could not be read
	CheckedAt string      `json:"checkedAt"`
}

const certCacheTTL = 5 * time.Minute

type certCache struct {
	mu sync.Mutex
	at time.Time
	ov CertOverview
}

// probeCert fetches the certificate a site serves; tests replace it.
var probeCert = func(ctx context.Context, host string) (certs.Info, error) { return certs.Probe(ctx, host, "", nil) }

var cst = time.FixedZone("CST", 8*3600)

func days(t time.Time) *int {
	d := int(math.Floor(time.Until(t).Hours() / 24))
	return &d
}

// judge rates a certificate by the days it has left and whether something
// renews it.
func judge(left *int, auto bool) (string, string) {
	switch {
	case left == nil:
		return "info", "有效期未知"
	case *left < 0:
		return "crit", "已过期"
	case *left < 7:
		return "crit", fmt.Sprintf("%d 天后过期", *left)
	case *left < 15 && auto:
		return "warn", fmt.Sprintf("%d 天后过期，自动续签可能没有成功", *left)
	case *left < 30 && !auto:
		return "warn", fmt.Sprintf("%d 天后过期，而且不会自动续签", *left)
	}
	return "ok", fmt.Sprintf("还有 %d 天", *left)
}

func parseAnyTime(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, cst); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// Certificates returns the overview, from a 5-minute cache unless refresh.
func (a *App) Certificates(ctx context.Context, refresh bool) (CertOverview, error) {
	a.certs.mu.Lock()
	defer a.certs.mu.Unlock()
	if !refresh && !a.certs.at.IsZero() && time.Since(a.certs.at) < certCacheTTL {
		return a.certs.ov, nil
	}
	ov := CertOverview{Entries: []CertEntry{}, Live: []LiveCert{}, CheckedAt: now()}
	var mu sync.Mutex
	var wg sync.WaitGroup
	add := func(entries []CertEntry, note string) {
		mu.Lock()
		defer mu.Unlock()
		ov.Entries = append(ov.Entries, entries...)
		if note != "" {
			ov.Notes = append(ov.Notes, note)
		}
	}
	if c := a.tencentClient(); c != nil {
		wg.Add(2)
		go func() { defer wg.Done(); add(a.eoCerts(ctx, c)) }()
		go func() { defer wg.Done(); add(sslCerts(ctx, a.tencentClient())) }()
	}
	servers, err := a.Store.ListServers()
	if err != nil {
		return ov, err
	}
	for _, sv := range servers {
		if s, err := a.OnePanel(sv.ID); err != nil || !s.HasKey || s.Port == 0 {
			continue
		}
		wg.Add(1)
		go func(sv store.Server) { defer wg.Done(); add(a.panelCerts(ctx, sv)) }(sv)
	}
	wg.Wait()

	ov.Live = probeAll(ctx, liveDomains(ov.Entries))
	sort.SliceStable(ov.Entries, func(i, j int) bool {
		rank := map[string]int{"crit": 0, "warn": 1, "info": 2, "ok": 3}
		if rank[ov.Entries[i].Level] != rank[ov.Entries[j].Level] {
			return rank[ov.Entries[i].Level] < rank[ov.Entries[j].Level]
		}
		return ov.Entries[i].Domain < ov.Entries[j].Domain
	})
	a.certs.at, a.certs.ov = time.Now(), ov
	return ov, nil
}

func (a *App) forgetCertificates() {
	a.certs.mu.Lock()
	a.certs.at = time.Time{}
	a.certs.mu.Unlock()
}

// eoCerts reads the certificate of every EdgeOne acceleration domain.
func (a *App) eoCerts(ctx context.Context, c *tencent.Client) ([]CertEntry, string) {
	zones, err := c.Zones(ctx)
	if err != nil {
		return nil, "EdgeOne：" + err.Error()
	}
	var out []CertEntry
	for _, z := range zones {
		domains, err := c.AccelerationDomains(ctx, z.ZoneID, "")
		if err != nil {
			return out, "EdgeOne：" + err.Error()
		}
		for _, d := range domains {
			e := CertEntry{Domain: d.DomainName, Names: []string{d.DomainName}, Source: "eo", Where: "EdgeOne 边缘（站点 " + z.ZoneName + "）"}
			var cert *tencent.Certificate
			if len(d.Certificate.List) > 0 {
				cert = &d.Certificate.List[0]
				e.ID = cert.CertID
				if t, ok := parseAnyTime(cert.ExpireTime); ok {
					e.NotAfter, e.DaysLeft = t.Format(time.RFC3339), days(t)
				}
			}
			switch d.Certificate.Mode {
			case "eofreecert", "eofreecert_manual":
				e.AutoRenew, e.Renew, e.Issuer = true, "EdgeOne 自动续签", "EdgeOne 免费证书"
			case "sslcert":
				e.Renew, e.Issuer = "跟随腾讯云 SSL 证书（见下面同名证书）", "腾讯云 SSL 证书"
			default:
				e.Level, e.Status, e.Renew = "info", "没有开启 HTTPS，访问只能用 http://", "—"
				out = append(out, e)
				continue
			}
			e.Level, e.Status = judge(e.DaysLeft, e.AutoRenew)
			if cert != nil {
				switch cert.Status {
				case "applying", "processing":
					e.Level, e.Status = "info", "证书申请或部署中"
				case "failed":
					e.Level, e.Status = "crit", "证书申请失败，通常是 DNS 还没有解析到 EdgeOne"
				}
			}
			out = append(out, e)
		}
	}
	return out, ""
}

// sslCerts reads Tencent Cloud's SSL certificate service.
func sslCerts(ctx context.Context, c *tencent.Client) ([]CertEntry, string) {
	list, err := c.SSLCertificates(ctx)
	if err != nil {
		return nil, "腾讯云 SSL 证书：" + err.Error()
	}
	var out []CertEntry
	for _, s := range list {
		e := CertEntry{Domain: s.Domain, Names: s.SANs, Source: "tencent_ssl", Where: "腾讯云 SSL 证书（" + s.ID + "）", ID: s.ID, Issuer: orDash(s.Product)}
		if len(e.Names) == 0 {
			e.Names = []string{s.Domain}
		}
		if t, ok := parseAnyTime(s.EndTime); ok {
			e.NotAfter, e.DaysLeft = t.Format(time.RFC3339), days(t)
		}
		e.AutoRenew = s.HostingStatus != nil && *s.HostingStatus >= 0
		e.Renew = "不会自动续签（到期前要重新申请或购买）"
		if e.AutoRenew {
			e.Renew = "腾讯云托管自动续期"
		}
		e.Level, e.Status = judge(e.DaysLeft, e.AutoRenew)
		switch s.Status {
		case 1:
		case 3:
			e.Level, e.Status = "crit", "已过期"
		default:
			e.Level, e.Status = "info", orDash(s.StatusName)
		}
		out = append(out, e)
	}
	return out, ""
}

// panelCerts reads one server's 1Panel certificates.
func (a *App) panelCerts(ctx context.Context, sv store.Server) ([]CertEntry, string) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	sv, c, err := a.connect(ctx, sv.ID)
	if err != nil {
		return nil, sv.Name + " 的 1Panel：" + err.Error()
	}
	defer c.Close()
	op, err := a.onePanelClient(sv.ID, c)
	if err != nil || op == nil {
		return nil, sv.Name + " 的 1Panel：接口没有配置好"
	}
	e := a.startExec(store.ExecLog{ServerID: sv.ID, ServerName: sv.Name, Adapter: sv.Adapter, Origin: originOf(ctx),
		Kind: store.ExecRead, Title: "查询 1Panel 证书", Via: "1Panel 接口", Commands: "POST /api/v2/websites/ssl/search"})
	list, err := op.SSLs(ctx)
	if err != nil {
		a.finishExec(&e, actions.StatusFailed, err.Error())
		return nil, sv.Name + " 的 1Panel：" + err.Error()
	}
	a.finishExec(&e, actions.StatusDone, fmt.Sprintf("%d 张证书", len(list)))
	var out []CertEntry
	for _, s := range list {
		ce := CertEntry{Domain: s.PrimaryDomain, Names: s.Names(), Source: "1panel", Where: "1Panel（服务器 " + sv.Name + "）",
			ServerID: sv.ID, ID: fmt.Sprint(s.ID), Issuer: orDash(s.Organization), AutoRenew: s.AutoRenew,
			CanRenew: s.Provider == "http" || s.Provider == "dnsAccount"}
		for _, w := range s.Websites {
			ce.UsedBy = append(ce.UsedBy, w.PrimaryDomain)
		}
		if !s.ExpireDate.IsZero() && s.Status == "ready" {
			ce.NotAfter, ce.DaysLeft = s.ExpireDate.Format(time.RFC3339), days(s.ExpireDate)
		}
		ce.Renew = "不会自动续签"
		if s.AutoRenew {
			ce.Renew = "1Panel 自动续签"
		}
		ce.Level, ce.Status = judge(ce.DaysLeft, s.AutoRenew)
		switch s.Status {
		case "ready":
		case "applying", "init":
			ce.Level, ce.Status = "info", "申请中"
		default:
			ce.Level, ce.Status = "crit", "申请或续签失败"
			ce.RenewError = clipText(s.Message, 300)
		}
		out = append(out, ce)
	}
	return out, ""
}

// liveDomains picks the names worth checking over the internet: the
// EdgeOne domains with HTTPS and the names of servers' certificates in use.
func liveDomains(entries []CertEntry) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range entries {
		if e.Source == "tencent_ssl" || (e.Source == "eo" && e.Renew == "—") {
			continue
		}
		names := e.Names
		if e.Source == "1panel" {
			names = e.UsedBy
		}
		for _, n := range names {
			if n != "" && !strings.Contains(n, "*") && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

func probeAll(ctx context.Context, domains []string) []LiveCert {
	out := make([]LiveCert, len(domains))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, d := range domains {
		wg.Add(1)
		go func(i int, d string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			l := LiveCert{Domain: d}
			info, err := probeCert(ctx, d)
			switch {
			case err != nil:
				l.Level, l.Status = "info", "连不上 https://"+d+"（"+clipText(err.Error(), 80)+"）"
			default:
				l.Names, l.Issuer, l.NotAfter, l.DaysLeft = info.Names, info.Issuer, info.NotAfter.Format(time.RFC3339), days(info.NotAfter)
				l.Level, l.Status = judge(l.DaysLeft, true)
				if !info.Valid {
					l.Level, l.Status = "crit", info.Problem
				}
			}
			out[i] = l
		}(i, d)
	}
	wg.Wait()
	return out
}

func (a *App) toolCertificates(ctx context.Context, raw json.RawMessage) (string, error) {
	var arg struct {
		Refresh bool   `json:"refresh"`
		Domain  string `json:"domain"`
	}
	if err := parseArgs(raw, &arg); err != nil {
		return "", err
	}
	ov, err := a.Certificates(ctx, arg.Refresh)
	if err != nil {
		return "", err
	}
	match := func(names ...string) bool {
		if arg.Domain == "" {
			return true
		}
		for _, n := range names {
			if strings.Contains(n, arg.Domain) {
				return true
			}
		}
		return false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "检查时间 %s\n证书：\n", ov.CheckedAt)
	for _, e := range ov.Entries {
		if !match(append(e.Names, e.Domain)...) {
			continue
		}
		fmt.Fprintf(&b, "- [%s] %s（%s）在 %s；签发 %s；到期 %s；续签：%s", e.Level, e.Domain, strings.Join(e.Names, ","), e.Where, orDash(e.Issuer), orDash(e.NotAfter), e.Renew)
		if e.Source == "1panel" {
			fmt.Fprintf(&b, "；1Panel 证书编号 %s，服务器 %d，用在 %s", e.ID, e.ServerID, orDash(strings.Join(e.UsedBy, ",")))
		}
		fmt.Fprintf(&b, "；%s", e.Status)
		if e.RenewError != "" {
			fmt.Fprintf(&b, "（%s）", e.RenewError)
		}
		b.WriteString("\n")
	}
	b.WriteString("实际访问到的证书：\n")
	for _, l := range ov.Live {
		if match(l.Domain) {
			fmt.Fprintf(&b, "- [%s] https://%s：%s，签发 %s，到期 %s\n", l.Level, l.Domain, l.Status, orDash(l.Issuer), orDash(l.NotAfter))
		}
	}
	for _, n := range ov.Notes {
		fmt.Fprintf(&b, "没能读取：%s\n", n)
	}
	return b.String(), nil
}
