package app

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Page data kept ready: what the pages show that has to be asked of
// servers and cloud services (websites, DNS, EdgeOne, storage, cloud
// server details, databases) is gathered when Miao Panel starts and again
// in the background, so a page opens with it at once, says how old it is,
// and has a refresh button for the live answer. A change made through
// Miao Panel (a checklist step, a setting, a file or storage operation)
// makes every page gather anew before answering, so nobody sees the state
// from before their own change. Preloading can be turned off in 设置;
// the pages then ask live, as they used to.

const (
	preloadKey     = "preload" // "off" turns preloading off
	pageTTL        = 5 * time.Minute
	preloadEvery   = 10 * time.Minute
	preloadSites   = 60 // website details kept, at most
	preloadWorkers = 4
)

// PageRead is how a page asks for its data.
type PageRead int

const (
	PageLatest  PageRead = iota // what is kept, at once
	PageWait                    // a current copy: waits for a refresh already running
	PageRefresh                 // gathered now
)

// PageMeta says how old a page's data is.
type PageMeta struct {
	At         time.Time // when it was gathered
	Refreshing bool      // a newer copy is being gathered
}

type pageCopy struct {
	At  time.Time       `json:"at"`
	V   json.RawMessage `json:"v"`
	gen uint64          // the change count when it was gathered
}

type pageCache struct {
	snaps   snapshots[pageCopy]
	mu      sync.Mutex
	gen     uint64    // counts changes made through Miao Panel
	ran     time.Time // the last preload round
	running bool      // a round is under way
}

func (p *pageCache) generation() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.gen
}

// PagesChanged says something was changed through Miao Panel: every page
// gathers anew before it next answers.
func (a *App) PagesChanged() {
	a.pages.mu.Lock()
	a.pages.gen++
	a.pages.mu.Unlock()
}

// PreloadOn says whether page data is gathered ahead of time.
func (a *App) PreloadOn() bool {
	v, _ := a.Store.Setting(preloadKey)
	return v != "off"
}

// PreloadView is the 设置 page's 数据加载 row.
type PreloadView struct {
	Enabled bool   `json:"enabled"`
	RanAt   string `json:"ranAt,omitempty"`
	Running bool   `json:"running"`
}

// PreloadStatus says whether preloading is on and when it last ran.
func (a *App) PreloadStatus() PreloadView {
	v := PreloadView{Enabled: a.PreloadOn()}
	a.pages.mu.Lock()
	v.Running = a.pages.running
	if !a.pages.ran.IsZero() {
		v.RanAt = a.pages.ran.UTC().Format(time.RFC3339)
	}
	a.pages.mu.Unlock()
	return v
}

// SetPreload turns preloading on or off; turned on, it starts at once.
func (a *App) SetPreload(ctx context.Context, on bool) (PreloadView, error) {
	v := "on"
	if !on {
		v = "off"
	}
	if err := a.Store.SetSetting(preloadKey, v); err != nil {
		return PreloadView{}, err
	}
	if on {
		return a.RunPreload(ctx), nil
	}
	return a.PreloadStatus(), nil
}

// RunPreload starts a preload round now, unless one is under way.
func (a *App) RunPreload(ctx context.Context) PreloadView {
	if a.startPreload() {
		go func() {
			defer a.endPreload()
			a.preloadRound(withOrigin(context.WithoutCancel(ctx), OriginAuto))
		}()
	}
	return a.PreloadStatus()
}

// startPreload marks a round as under way; false when one already is.
func (a *App) startPreload() bool {
	a.pages.mu.Lock()
	defer a.pages.mu.Unlock()
	if a.pages.running {
		return false
	}
	a.pages.running = true
	return true
}

func (a *App) endPreload() {
	a.pages.mu.Lock()
	a.pages.running, a.pages.ran = false, time.Now()
	a.pages.mu.Unlock()
}

// copier gathers a page's data as a copy to keep, noting the change
// count it started after.
func copier[T any](a *App, gather func(context.Context) (T, error)) func(context.Context) (pageCopy, error) {
	return func(ctx context.Context) (pageCopy, error) {
		gen, started := a.pages.generation(), time.Now()
		v, err := gather(ctx)
		if err != nil {
			return pageCopy{}, err
		}
		raw, err := json.Marshal(v)
		return pageCopy{At: started, V: raw, gen: gen}, err
	}
}

// page answers a page with its data, kept or gathered as read asks.
func page[T any](ctx context.Context, a *App, key string, read PageRead, gather func(context.Context) (T, error)) (T, PageMeta, error) {
	var zero T
	g := copier(a, gather)
	if read == PageLatest && !a.PreloadOn() {
		read = PageRefresh // live, as before preloading
	}
	var c pageCopy
	var refreshing bool
	var err error
	switch read {
	case PageRefresh:
		c, err = a.pages.snaps.get(ctx, a, key, 0, true, g)
	case PageWait:
		c, err = a.pages.snaps.get(ctx, a, key, pageTTL, false, g)
	default:
		c, refreshing, err = a.pages.snaps.latest(ctx, a, key, pageTTL, g)
	}
	// Gathered before a change made here: gather again (a refresh already
	// running may have started before the change too).
	for i := 0; i < 2 && err == nil && c.gen < a.pages.generation(); i++ {
		c, err = a.pages.snaps.get(ctx, a, key, 0, true, g)
		refreshing = false
	}
	if err != nil {
		return zero, PageMeta{}, err
	}
	var v T
	if err := json.Unmarshal(c.V, &v); err != nil {
		return zero, PageMeta{}, err
	}
	return v, PageMeta{At: c.At, Refreshing: refreshing}, nil
}

// warmPage gathers a page's data in the background when it is older than
// pageTTL; the channel closes when that is done.
func warmPage[T any](ctx context.Context, a *App, key string, gather func(context.Context) (T, error)) chan struct{} {
	return a.pages.snaps.warm(ctx, a, key, pageTTL, copier(a, gather))
}

// ---- the pages ----

// WebsitesPage is 网站管理's list: every panel server's sites, or one
// server's.
func (a *App) WebsitesPage(ctx context.Context, serverID int64, read PageRead) (WebsitesView, PageMeta, error) {
	v, m, err := page(ctx, a, "page_websites", read, func(ctx context.Context) (WebsitesView, error) { return a.Websites(ctx, 0) })
	if err != nil || serverID == 0 {
		return v, m, err
	}
	one := WebsitesView{Servers: []SiteServerView{}}
	for _, s := range v.Servers {
		if s.ID == serverID {
			one.Servers = append(one.Servers, s)
		}
	}
	return one, m, nil
}

// WebsitePage is one site's detail.
func (a *App) WebsitePage(ctx context.Context, serverID int64, siteID uint, read PageRead) (SiteDetailView, PageMeta, error) {
	return page(ctx, a, "page_site_"+strconv.FormatInt(serverID, 10)+"_"+strconv.FormatUint(uint64(siteID), 10), read,
		func(ctx context.Context) (SiteDetailView, error) { return a.Website(ctx, serverID, siteID) })
}

// DNSDomainsPage is 解析's domains.
func (a *App) DNSDomainsPage(ctx context.Context, read PageRead) (DNSDomainsView, PageMeta, error) {
	return page(ctx, a, "page_dns_domains", read, a.DNSDomains)
}

// DNSRecordsPage is one domain's records.
func (a *App) DNSRecordsPage(ctx context.Context, domain, provider string, read PageRead) (DNSRecordsView, PageMeta, error) {
	return page(ctx, a, "page_dns_"+provider+"_"+domain, read, func(ctx context.Context) (DNSRecordsView, error) {
		return a.DNSRecords(ctx, domain, provider)
	})
}

// EOSitesPage is EdgeOne's sites and domains.
func (a *App) EOSitesPage(ctx context.Context, read PageRead) ([]string, PageMeta, error) {
	return page(ctx, a, "page_eo_sites", read, a.EOSites)
}

// COSBucketsPage is 存储's buckets.
func (a *App) COSBucketsPage(ctx context.Context, read PageRead) (COSBucketsView, PageMeta, error) {
	return page(ctx, a, "page_cos_buckets", read, a.COSBuckets)
}

// COSBucketPage is one bucket's settings.
func (a *App) COSBucketPage(ctx context.Context, bucket, region string, read PageRead) (COSDetail, PageMeta, error) {
	return page(ctx, a, "page_cos_"+region+"_"+bucket, read, func(ctx context.Context) (COSDetail, error) {
		return a.COSBucketDetail(ctx, bucket, region)
	})
}

// DatabasesPage is a 1Panel server's MySQL databases.
func (a *App) DatabasesPage(ctx context.Context, serverID int64, read PageRead) ([]DBAppView, PageMeta, error) {
	return page(ctx, a, "page_dbs_"+strconv.FormatInt(serverID, 10), read, func(ctx context.Context) ([]DBAppView, error) {
		return a.ServerDatabases(ctx, serverID)
	})
}

// BlockedPage is what EdgeOne blocks.
func (a *App) BlockedPage(ctx context.Context, read PageRead) ([]BlockedZone, PageMeta, error) {
	return page(ctx, a, "page_blocked", read, a.Blocked)
}

// CloudDetailPage is a 腾讯云 server's detail.
func (a *App) CloudDetailPage(ctx context.Context, region, id string, read PageRead) (CloudDetail, PageMeta, error) {
	return page(ctx, a, "page_tc_"+region+"_"+id, read, func(ctx context.Context) (CloudDetail, error) {
		return a.CloudDetail(ctx, region, id)
	})
}

// AliyunDetailPage is an 阿里云 server's detail.
func (a *App) AliyunDetailPage(ctx context.Context, region, id string, read PageRead) (AliDetail, PageMeta, error) {
	return page(ctx, a, "page_ali_"+region+"_"+id, read, func(ctx context.Context) (AliDetail, error) {
		return a.AliyunDetail(ctx, region, id)
	})
}

// ---- preloading ----

// PreloadLoop gathers the pages' data at start and every preloadEvery,
// while preloading is on.
func (a *App) PreloadLoop(ctx context.Context) {
	ctx = withOrigin(ctx, OriginAuto)
	for {
		if a.PreloadOn() {
			a.preloadPages(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(preloadEvery):
		}
	}
}

// preloadPages gathers what each page shows: the lists first, then what
// they lead to (each site, each domain's records, each bucket, each cloud
// server), a few at a time.
func (a *App) preloadPages(ctx context.Context) {
	if a.startPreload() {
		defer a.endPreload()
		a.preloadRound(ctx)
	}
}

func (a *App) preloadRound(ctx context.Context) {
	var jobs []func()
	var jobsMu sync.Mutex
	wait := func(ch chan struct{}) {
		if ch != nil {
			select {
			case <-ch:
			case <-ctx.Done():
			}
		}
	}
	later := func(f func()) {
		jobsMu.Lock()
		jobs = append(jobs, f)
		jobsMu.Unlock()
	}

	// The lists, in parallel.
	var wg sync.WaitGroup
	run := func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}
	servers, _ := a.Store.ListServers()
	run(func() {
		wait(warmPage(ctx, a, "page_websites", func(ctx context.Context) (WebsitesView, error) { return a.Websites(ctx, 0) }))
		v, _, err := a.WebsitesPage(ctx, 0, PageLatest)
		if err != nil {
			return
		}
		n := 0
		for _, s := range v.Servers {
			for _, site := range s.Sites {
				if n++; n > preloadSites {
					return
				}
				sid, id := s.ID, site.ID
				later(func() {
					wait(warmPage(ctx, a, "page_site_"+strconv.FormatInt(sid, 10)+"_"+strconv.FormatUint(uint64(id), 10),
						func(ctx context.Context) (SiteDetailView, error) { return a.Website(ctx, sid, id) }))
				})
			}
		}
	})
	for _, sv := range servers {
		if sv.Adapter == "1panel" {
			id := sv.ID
			run(func() {
				wait(warmPage(ctx, a, "page_dbs_"+strconv.FormatInt(id, 10), func(ctx context.Context) ([]DBAppView, error) {
					return a.ServerDatabases(ctx, id)
				}))
			})
		}
	}
	if a.tencentClient() != nil || a.aliyunClient() != nil {
		run(func() {
			wait(warmPage(ctx, a, "page_dns_domains", a.DNSDomains))
			v, _, err := a.DNSDomainsPage(ctx, PageLatest)
			if err != nil {
				return
			}
			for _, d := range v.Domains {
				name, provider := d.Name, d.Provider
				later(func() {
					wait(warmPage(ctx, a, "page_dns_"+provider+"_"+name, func(ctx context.Context) (DNSRecordsView, error) {
						return a.DNSRecords(ctx, name, provider)
					}))
				})
			}
		})
	}
	if a.hasCloud() {
		run(func() { wait(warmPage(ctx, a, "page_account", a.CloudAccount)) })
		run(func() { wait(warmPage(ctx, a, "page_cdn", a.CDN)) })
		run(func() { wait(warmPage(ctx, a, "page_alarms", a.CloudAlarms)) })
	}
	if a.tencentClient() != nil {
		run(func() {
			wait(warmPage(ctx, a, "page_eo_sites", a.EOSites))
			zones, err := a.tencentClient().Zones(ctx)
			if err != nil {
				return
			}
			for _, z := range zones {
				name := z.ZoneName
				later(func() {
					wait(warmPage(ctx, a, "page_eoprot_"+name, func(ctx context.Context) (EOProtectView, error) { return a.EOProtection(ctx, name) }))
				})
			}
		})
		run(func() { wait(warmPage(ctx, a, "page_blocked", a.Blocked)) })
		run(func() {
			wait(warmPage(ctx, a, "page_cos_buckets", a.COSBuckets))
			v, _, err := a.COSBucketsPage(ctx, PageLatest)
			if err != nil {
				return
			}
			for _, b := range v.Buckets {
				name, region := b.Name, b.Region
				later(func() {
					wait(warmPage(ctx, a, "page_cos_"+region+"_"+name, func(ctx context.Context) (COSDetail, error) {
						return a.COSBucketDetail(ctx, name, region)
					}))
				})
			}
		})
		run(func() {
			list, err := a.TencentServers(ctx, false)
			if err != nil {
				return
			}
			for _, s := range list.Servers {
				region, id := s.Region, s.ID
				later(func() {
					wait(warmPage(ctx, a, "page_tc_"+region+"_"+id, func(ctx context.Context) (CloudDetail, error) {
						return a.CloudDetail(ctx, region, id)
					}))
				})
			}
		})
	}
	if a.aliyunClient() != nil {
		run(func() {
			list, err := a.AliyunServers(ctx, false)
			if err != nil {
				return
			}
			for _, s := range list.Servers {
				region, id := s.Region, s.ID
				later(func() {
					wait(warmPage(ctx, a, "page_ali_"+region+"_"+id, func(ctx context.Context) (AliDetail, error) {
						return a.AliyunDetail(ctx, region, id)
					}))
				})
			}
		})
	}
	wg.Wait()

	// What the lists lead to, a few at a time.
	work := make(chan func())
	for range preloadWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range work {
				f()
			}
		}()
	}
	for _, f := range jobs {
		select {
		case work <- f:
		case <-ctx.Done():
		}
	}
	close(work)
	wg.Wait()
}

// pageKeys lists the kept pages, for tests.
func (a *App) pageKeys() []string {
	a.pages.snaps.mu.Lock()
	defer a.pages.snaps.mu.Unlock()
	var keys []string
	for k := range a.pages.snaps.entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
