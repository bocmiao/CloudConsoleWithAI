package app

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/geoip"
	"github.com/bocmiao/CloudConsoleWithAI/internal/visits"
)

// What is known about IPs beyond the logs, kept for a day: whether each
// is one of EdgeOne's nodes, and whether a self-declared search engine
// crawler really is one.
type ipFacts struct {
	mu      sync.Mutex
	edgeOne map[string]factAt[bool]
	crawler map[string]factAt[crawlerCheck]
}

type factAt[T any] struct {
	v  T
	at time.Time
}

type crawlerCheck struct {
	name string // verified crawler
	fake bool
}

const factTTL = 24 * time.Hour

// lookupAddr and lookupHost resolve names; tests replace them.
var (
	lookupAddr = net.DefaultResolver.LookupAddr
	lookupHost = net.DefaultResolver.LookupHost
)

// facts finds out what it can about the described IPs of a report.
func (a *App) facts(ctx context.Context, r *visits.Report) map[string]visits.Facts {
	ips := map[string]string{} // IP -> its UA
	for _, rg := range r.Ranges {
		for _, p := range rg.IPs {
			ips[p.IP] = p.UA
		}
		for _, s := range rg.Sites {
			for _, it := range s.Top["ip"] {
				if _, ok := ips[it.Value]; !ok {
					ips[it.Value] = ""
				}
			}
		}
	}
	out := make(map[string]visits.Facts, len(ips))
	if r.Source == "server" { // EdgeOne's own logs have the visitors' IPs
		nodes, err := a.edgeOneNodes(ctx, ips)
		if err != nil {
			r.Notes = append(r.Notes, "没能向 EdgeOne 核对哪些 IP 是它的节点（"+err.Error()+"）。列表里的风险只看访问记录；封禁前会再核对一次，核对不了就不封")
		}
		for ip, eo := range nodes {
			f := out[ip]
			f.EdgeOne = eo
			out[ip] = f
		}
	}
	for ip, c := range a.checkCrawlers(ctx, ips) {
		f := out[ip]
		f.Crawler, f.FakeCrawler = c.name, c.fake
		out[ip] = f
	}
	return out
}

// edgeOneNodes asks EdgeOne which IPs are its nodes (when Tencent Cloud is
// set up); unknown IPs are left out, and err says why some are unknown.
func (a *App) edgeOneNodes(ctx context.Context, ips map[string]string) (map[string]bool, error) {
	out := map[string]bool{}
	var ask []string
	a.ipf.mu.Lock()
	if a.ipf.edgeOne == nil {
		a.ipf.edgeOne = map[string]factAt[bool]{}
	}
	for ip := range ips {
		if f, ok := a.ipf.edgeOne[ip]; ok && time.Since(f.at) < factTTL {
			out[ip] = f.v
		} else if net.ParseIP(ip) != nil {
			ask = append(ask, ip)
		}
	}
	a.ipf.mu.Unlock()
	c := a.tencentClient()
	if c == nil || len(ask) == 0 {
		return out, nil
	}
	for i := 0; i < len(ask); i += 100 {
		batch := ask[i:min(i+100, len(ask))]
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		res, err := c.EdgeOneIPs(ctx, batch)
		cancel()
		if err != nil {
			return out, err // e.g. no EdgeOne permission: the rest is unknown
		}
		a.ipf.mu.Lock()
		for _, ip := range batch {
			a.ipf.edgeOne[ip] = factAt[bool]{v: res[ip], at: time.Now()}
			out[ip] = res[ip]
		}
		a.ipf.mu.Unlock()
	}
	return out, nil
}

// checkCrawlers verifies IPs whose UA claims a search engine crawler: by
// the address ranges Google and Bing publish, otherwise the way the search
// engines say to, the reverse DNS name must be theirs and resolve back to
// the IP.
func (a *App) checkCrawlers(ctx context.Context, ips map[string]string) map[string]crawlerCheck {
	out := map[string]crawlerCheck{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	a.ipf.mu.Lock()
	if a.ipf.crawler == nil {
		a.ipf.crawler = map[string]factAt[crawlerCheck]{}
	}
	a.ipf.mu.Unlock()
	for ip, ua := range ips {
		name, hosts, ok := visits.ClaimedCrawler(ua)
		if !ok {
			continue
		}
		if in, _ := visits.PublishedCrawler(name, ip); in {
			out[ip] = crawlerCheck{name: name}
			continue
		}
		a.ipf.mu.Lock()
		f, cached := a.ipf.crawler[ip]
		a.ipf.mu.Unlock()
		if cached && time.Since(f.at) < factTTL {
			out[ip] = f.v
			continue
		}
		wg.Add(1)
		go func(ip, name string, hosts []string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			c, known := verifyCrawler(ctx, ip, name, hosts)
			if !known {
				return
			}
			mu.Lock()
			out[ip] = c
			mu.Unlock()
			a.ipf.mu.Lock()
			a.ipf.crawler[ip] = factAt[crawlerCheck]{v: c, at: time.Now()}
			a.ipf.mu.Unlock()
		}(ip, name, hosts)
	}
	wg.Wait()
	return out
}

// verifyCrawler reports known=false when DNS could not tell. In mainland
// China answers for foreign names are often forged, so a name that does
// not resolve back to the IP proves nothing, and an address the IP
// database gives to the search engine's own company is never called fake.
func verifyCrawler(ctx context.Context, ip, name string, hosts []string) (c crawlerCheck, known bool) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	names, err := lookupAddr(ctx, ip)
	var dnsErr *net.DNSError
	if err != nil && !(errors.As(err, &dnsErr) && dnsErr.IsNotFound) {
		return c, false
	}
	for _, n := range names {
		n = strings.TrimSuffix(strings.ToLower(n), ".")
		for _, h := range hosts {
			if !strings.HasSuffix(n, h) {
				continue
			}
			addrs, err := lookupHost(ctx, n)
			if err != nil {
				return c, false
			}
			for _, addr := range addrs {
				if x, y := net.ParseIP(addr), net.ParseIP(ip); x != nil && x.Equal(y) {
					return crawlerCheck{name: name}, true
				}
			}
			return c, false // its name, but not its address: maybe a forged answer
		}
	}
	if loc, ok := geoip.Lookup(ip); ok && visits.OwnedBy(name, loc.ISP) {
		return c, false
	}
	return crawlerCheck{fake: true}, true
}
