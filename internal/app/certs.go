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
	// Refreshing: this is an older overview and a new one is on its way.
	Refreshing bool `json:"refreshing,omitempty"`
}

const certCacheTTL = 5 * time.Minute

// certSettingKey keeps the last overview, so the 证书 page has something
// to show at once after Miao Panel restarts.
const certSettingKey = "cert_overview"

// certCache holds the latest overview and the refresh in progress, if any.
// A refresh runs on its own, so a page that stops waiting does not cancel
// it and the next visit picks up its result.
type certCache struct {
	mu      sync.Mutex
	loaded  bool      // ov holds an overview, possibly an old one
	at      time.Time // when ov was gathered in this run; zero if it is old
	ov      CertOverview
	err     error
	gen     int           // bumped when something may have changed a certificate
	ovGen   int           // gen when the refresh that produced ov began
	running chan struct{} // closed when the refresh in progress ends
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

// Certificates returns a current overview: the one gathered in the last
// 5 minutes unless refresh, or else a new one.
func (a *App) Certificates(ctx context.Context, refresh bool) (CertOverview, error) {
	a.certs.mu.Lock()
	if !refresh && a.certs.running == nil && !a.certs.at.IsZero() && time.Since(a.certs.at) < certCacheTTL {
		defer a.certs.mu.Unlock()
		return a.certs.ov, nil
	}
	want := a.certs.gen
	for {
		done := a.refreshCertsLocked()
		a.certs.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return CertOverview{}, ctx.Err()
		}
		a.certs.mu.Lock()
		// A refresh that began before a change was made is not enough.
		if a.certs.err != nil || a.certs.ovGen >= want {
			defer a.certs.mu.Unlock()
			return a.certs.ov, a.certs.err
		}
	}
}

// LatestCertificates answers at once with the last overview, even one
// saved before Miao Panel restarted, and starts a refresh in the
// background if it is out of date (Refreshing says so). Only the very
// first time, with nothing to show, does it wait.
func (a *App) LatestCertificates(ctx context.Context) (CertOverview, error) {
	a.certs.mu.Lock()
	if !a.certs.loaded {
		if raw, err := a.Store.Setting(certSettingKey); err == nil && raw != "" {
			var ov CertOverview
			if json.Unmarshal([]byte(raw), &ov) == nil && ov.CheckedAt != "" {
				a.certs.ov, a.certs.loaded = ov, true
			}
		}
	}
	if !a.certs.loaded {
		a.certs.mu.Unlock()
		return a.Certificates(ctx, false)
	}
	defer a.certs.mu.Unlock()
	ov := a.certs.ov
	if a.certs.running != nil || a.certs.at.IsZero() || time.Since(a.certs.at) >= certCacheTTL {
		a.refreshCertsLocked()
		ov.Refreshing = true
	}
	return ov, nil
}

// refreshCertsLocked starts gathering a new overview unless one is
// already under way, and returns a channel closed when it is done.
func (a *App) refreshCertsLocked() chan struct{} {
	if a.certs.running != nil {
		return a.certs.running
	}
	done := make(chan struct{})
	a.certs.running = done
	gen := a.certs.gen
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		started := time.Now()
		ov, err := a.gatherCertificates(ctx)
		a.certs.mu.Lock()
		defer a.certs.mu.Unlock()
		a.certs.err = err
		if err == nil {
			a.certs.ov, a.certs.loaded, a.certs.at, a.certs.ovGen = ov, true, started, gen
			if a.certs.gen != gen {
				a.certs.at = time.Time{} // something changed while it ran
			}
			if raw, err := json.Marshal(ov); err == nil {
				_ = a.Store.SetSetting(certSettingKey, string(raw))
			}
		}
		a.certs.running = nil
		close(done)
	}()
	return done
}

func (a *App) forgetCertificates() {
	a.certs.mu.Lock()
	a.certs.at = time.Time{}
	a.certs.gen++
	a.certs.mu.Unlock()
}

// gatherCertificates reads every source and probes the sites.
func (a *App) gatherCertificates(ctx context.Context) (CertOverview, error) {
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
	return ov, nil
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
