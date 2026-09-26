package app

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// The same certificate is often kept in several places: uploaded to
// Tencent Cloud's SSL service for EdgeOne and also installed in 1Panel.
// The overview shows each certificate once, with every place it is kept,
// and puts a domain's certificates together (*.miao.club with miao.club).

// CertCopy is one place a certificate is kept.
type CertCopy struct {
	Source    string `json:"source"` // eo, tencent_ssl, 1panel
	Where     string `json:"where"`
	ID        string `json:"id,omitempty"`
	ServerID  int64  `json:"serverId,omitempty"`
	AutoRenew bool   `json:"autoRenew"`
	CanRenew  bool   `json:"canRenew,omitempty"`
}

// Cert is one certificate, wherever copies of it are kept.
type Cert struct {
	Names     []string   `json:"names"`
	Issuer    string     `json:"issuer,omitempty"`
	NotAfter  string     `json:"notAfter,omitempty"`
	DaysLeft  *int       `json:"daysLeft,omitempty"`
	AutoRenew bool       `json:"autoRenew"`
	Renew     string     `json:"renew"`
	Copies    []CertCopy `json:"copies"`
	// Where it is in use: 1Panel websites, EdgeOne domains, and the
	// domains that actually served it when visited.
	UsedBy     []string `json:"usedBy,omitempty"`
	EdgeOne    []string `json:"edgeOne,omitempty"`
	ServedOn   []string `json:"servedOn,omitempty"`
	InUse      bool     `json:"inUse"`
	Level      string   `json:"level"`
	Status     string   `json:"status"`
	RenewError string   `json:"renewError,omitempty"`
}

// CertGroup is a domain's certificates, in use first.
type CertGroup struct {
	Domain string   `json:"domain"`
	Names  []string `json:"names"`
	Level  string   `json:"level"`
	Status string   `json:"status"`
	Certs  []Cert   `json:"certs"`
}

var levelRank = map[string]int{"crit": 0, "warn": 1, "info": 2, "ok": 3}

// groupKey puts a domain's certificates together: *.miao.club, miao.club
// and www.miao.club all belong to miao.club.
func groupKey(domain string) string {
	d := strings.ToLower(strings.TrimSpace(domain))
	d = strings.TrimPrefix(d, "*.")
	return strings.TrimPrefix(d, "www.")
}

// certKey says which entries are copies of one certificate: the same
// names, expiring on the same day.
func certKey(e CertEntry) string {
	if e.NotAfter == "" {
		return "" // not issued (yet): its own row
	}
	names := map[string]bool{strings.ToLower(e.Domain): true}
	for _, n := range e.Names {
		names[strings.ToLower(n)] = true
	}
	list := make([]string, 0, len(names))
	for n := range names {
		if n != "" {
			list = append(list, n)
		}
	}
	sort.Strings(list)
	day := e.NotAfter
	if t, err := time.Parse(time.RFC3339, e.NotAfter); err == nil {
		day = t.In(cst).Format("2006-01-02")
	}
	return strings.Join(list, ",") + "|" + day
}

// covers reports whether a certificate for names is valid for host.
func covers(names []string, host string) bool {
	host = strings.ToLower(host)
	for _, n := range names {
		n = strings.ToLower(n)
		if n == host {
			return true
		}
		if strings.HasPrefix(n, "*.") {
			rest, ok := strings.CutSuffix(host, n[1:])
			if ok && rest != "" && !strings.Contains(rest, ".") {
				return true
			}
		}
	}
	return false
}

func sameDay(a, b string) bool {
	ta, err1 := time.Parse(time.RFC3339, a)
	tb, err2 := time.Parse(time.RFC3339, b)
	return err1 == nil && err2 == nil && ta.In(cst).Format("2006-01-02") == tb.In(cst).Format("2006-01-02")
}

func addUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// groupCertificates merges copies of the same certificate, links them to
// where they are used, and groups them by domain. EdgeOne domains without
// HTTPS are returned apart.
func groupCertificates(entries []CertEntry, live []LiveCert) ([]CertGroup, []CertEntry) {
	type pending struct {
		cert    Cert
		domain  string
		entries []CertEntry
	}
	var certs []*pending
	byKey := map[string]*pending{}
	byID := map[string]*pending{} // Tencent SSL certificate id -> its certificate
	var noHTTPS, eoUsing []CertEntry
	add := func(e CertEntry) *pending {
		k := certKey(e)
		if p := byKey[k]; k != "" && p != nil {
			p.entries = append(p.entries, e)
			return p
		}
		p := &pending{domain: e.Domain, entries: []CertEntry{e}}
		certs = append(certs, p)
		if k != "" {
			byKey[k] = p
		}
		return p
	}
	for _, e := range entries {
		switch {
		case e.Source == "eo" && e.Renew == "—":
			noHTTPS = append(noHTTPS, e)
		case e.Source == "eo" && !e.AutoRenew && e.ID != "":
			eoUsing = append(eoUsing, e) // an uploaded certificate: linked below
		default:
			p := add(e)
			if e.Source == "tencent_ssl" && e.ID != "" {
				byID[e.ID] = p
			}
		}
	}
	for _, e := range eoUsing {
		if p := byID[e.ID]; p != nil {
			p.cert.EdgeOne = addUnique(p.cert.EdgeOne, e.Domain)
			continue
		}
		add(e) // the certificate is not in this account's list
	}

	for _, p := range certs {
		c := &p.cert
		var issuers [3]string // 1Panel's organisation reads best, then SSL's product, then others
		for _, e := range p.entries {
			c.Copies = append(c.Copies, CertCopy{Source: e.Source, Where: e.Where, ID: e.ID, ServerID: e.ServerID, AutoRenew: e.AutoRenew, CanRenew: e.CanRenew})
			for _, n := range append([]string{e.Domain}, e.Names...) {
				if n != "" {
					c.Names = addUnique(c.Names, n)
				}
			}
			for _, u := range e.UsedBy {
				c.UsedBy = addUnique(c.UsedBy, u)
			}
			if e.Source == "eo" {
				c.EdgeOne = addUnique(c.EdgeOne, e.Domain) // EdgeOne's own certificate for this domain
			}
			if e.NotAfter != "" && c.NotAfter == "" {
				c.NotAfter, c.DaysLeft = e.NotAfter, e.DaysLeft
			}
			if e.AutoRenew {
				c.AutoRenew = true
			}
			if e.RenewError != "" {
				c.RenewError = e.RenewError
			}
			switch e.Source {
			case "1panel":
				if issuers[0] == "" && e.Issuer != "—" {
					issuers[0] = e.Issuer
				}
			case "tencent_ssl":
				if issuers[1] == "" && e.Issuer != "—" {
					issuers[1] = e.Issuer
				}
			default:
				if issuers[2] == "" {
					issuers[2] = e.Issuer
				}
			}
		}
		for _, s := range issuers {
			if s != "" {
				c.Issuer = s
				break
			}
		}
		var renew []string
		for _, e := range p.entries {
			if e.AutoRenew {
				renew = addUnique(renew, e.Renew)
			}
		}
		c.Renew = strings.Join(renew, "；")
		if c.Renew == "" {
			c.Renew = "不会自动续签"
		}
		for _, l := range live {
			if l.NotAfter != "" && sameDay(l.NotAfter, c.NotAfter) && covers(c.Names, l.Domain) {
				c.ServedOn = addUnique(c.ServedOn, l.Domain)
			}
		}
		c.InUse = len(c.UsedBy) > 0 || len(c.EdgeOne) > 0 || len(c.ServedOn) > 0

		// A failed renewal anywhere matters most; otherwise the days left
		// and whether any copy renews itself.
		c.Level, c.Status = p.entries[0].Level, p.entries[0].Status
		for _, e := range p.entries {
			if e.RenewError != "" {
				c.Level, c.Status = e.Level, e.Status
			}
		}
		if c.DaysLeft != nil && c.RenewError == "" {
			c.Level, c.Status = judge(c.DaysLeft, c.AutoRenew)
		}
		switch {
		case !c.InUse && c.DaysLeft != nil && *c.DaysLeft < 0:
			c.Level, c.Status = "info", "已过期，没发现在用（可以删除）"
		case !c.InUse && c.DaysLeft != nil:
			c.Level, c.Status = "info", fmt.Sprintf("没发现在用，还有 %d 天", *c.DaysLeft)
		}
		// EdgeOne keeps serving an uploaded copy: when 1Panel renews the
		// certificate, the copy in Tencent Cloud is not updated.
		if len(c.EdgeOne) > 0 && c.DaysLeft != nil && *c.DaysLeft < 30 && !hostedSSL(p.entries) && renewsOnlyIn1Panel(p.entries) {
			c.Level, c.Status = "warn", fmt.Sprintf("%d 天后过期：EdgeOne 用的是上传到腾讯云的副本，1Panel 续签后要重新上传", *c.DaysLeft)
		}
	}

	groups := map[string]*CertGroup{}
	var order []string
	for _, p := range certs {
		k := groupKey(p.domain)
		g := groups[k]
		if g == nil {
			g = &CertGroup{}
			groups[k] = g
			order = append(order, k)
		}
		g.Certs = append(g.Certs, p.cert)
	}
	out := make([]CertGroup, 0, len(order))
	for _, k := range order {
		g := groups[k]
		sort.SliceStable(g.Certs, func(i, j int) bool {
			a, b := g.Certs[i], g.Certs[j]
			if a.InUse != b.InUse {
				return a.InUse
			}
			return a.NotAfter > b.NotAfter
		})
		// The domain as most certificates name it, e.g. *.miao.club.
		count := map[string]int{}
		for _, c := range g.Certs {
			for _, n := range c.Names {
				g.Names = addUnique(g.Names, n)
			}
		}
		// Certificates in use name the group; the others only when none is.
		anyInUse := false
		for _, p := range certs {
			anyInUse = anyInUse || (groupKey(p.domain) == k && p.cert.InUse)
		}
		best := ""
		for _, p := range certs {
			if groupKey(p.domain) == k && (p.cert.InUse || !anyInUse) {
				count[p.domain]++
				if best == "" || count[p.domain] > count[best] || (count[p.domain] == count[best] && strings.HasPrefix(p.domain, "*.")) {
					best = p.domain
				}
			}
		}
		g.Domain = best
		// The group is as good as the certificates in use; unused ones only
		// matter when nothing is in use.
		g.Level = ""
		for _, c := range g.Certs {
			if !c.InUse && g.Certs[0].InUse {
				continue
			}
			if g.Level == "" || levelRank[c.Level] < levelRank[g.Level] {
				g.Level, g.Status = c.Level, c.Status
			}
		}
		out = append(out, *g)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if levelRank[out[i].Level] != levelRank[out[j].Level] {
			return levelRank[out[i].Level] < levelRank[out[j].Level]
		}
		return groupKey(out[i].Domain) < groupKey(out[j].Domain)
	})
	return out, noHTTPS
}

func hostedSSL(entries []CertEntry) bool {
	for _, e := range entries {
		if e.Source == "tencent_ssl" && e.AutoRenew {
			return true
		}
	}
	return false
}

func renewsOnlyIn1Panel(entries []CertEntry) bool {
	for _, e := range entries {
		if e.Source == "1panel" && e.AutoRenew {
			return true
		}
	}
	return false
}
