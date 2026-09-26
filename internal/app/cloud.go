package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/geoip"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// cloudCache keeps the instance list for a few minutes: listing every
// region of two products takes a few dozen calls.
type cloudCache struct {
	mu   sync.Mutex
	at   time.Time
	list []tencent.Server
	errs []string
}

const cloudCacheTTL = 5 * time.Minute

// CloudServer is a Tencent Cloud instance, with the Miao Panel server that
// has the same public IP.
type CloudServer struct {
	tencent.Server
	ServerID int64 `json:"serverId,omitempty"`
}

// CloudServers is the answer to "which servers do I have on Tencent Cloud".
type CloudServers struct {
	Servers   []CloudServer `json:"servers"`
	Errors    []string      `json:"errors"`
	FetchedAt string        `json:"fetchedAt"`
}

// TencentServers lists Lighthouse and CVM instances, from the cache unless
// refresh is set.
func (a *App) TencentServers(ctx context.Context, refresh bool) (CloudServers, error) {
	c := a.tencentClient()
	if c == nil {
		return CloudServers{}, userErr("还没有配置腾讯云密钥（设置 → 腾讯云）")
	}
	a.cloud.mu.Lock()
	defer a.cloud.mu.Unlock()
	if refresh || a.cloud.at.IsZero() || time.Since(a.cloud.at) > cloudCacheTTL {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		list, errs := c.Servers(ctx)
		if len(list) == 0 && len(errs) > 0 {
			return CloudServers{}, userErr("%v", errs[0])
		}
		a.cloud.list, a.cloud.at, a.cloud.errs = list, time.Now(), nil
		for _, e := range errs {
			a.cloud.errs = append(a.cloud.errs, e.Error())
		}
	}
	servers, _ := a.Store.ListServers()
	out := CloudServers{Errors: a.cloud.errs, FetchedAt: a.cloud.at.UTC().Format(time.RFC3339), Servers: []CloudServer{}}
	for _, s := range a.cloud.list {
		cs := CloudServer{Server: s}
		for _, sv := range servers {
			for _, ip := range s.PublicIPs {
				if sv.Host == ip {
					cs.ServerID = sv.ID
				}
			}
		}
		out.Servers = append(out.Servers, cs)
	}
	return out, nil
}

// ServerCloud returns the Tencent Cloud instance behind a server, if any.
func (a *App) ServerCloud(ctx context.Context, id int64) (*CloudServer, error) {
	if a.tencentClient() == nil {
		return nil, nil
	}
	list, err := a.TencentServers(ctx, false)
	if err != nil {
		return nil, err
	}
	for _, s := range list.Servers {
		if s.ServerID == id {
			return &s, nil
		}
	}
	return nil, nil
}

func gb(bytes int64) string { return fmt.Sprintf("%.1fGB", float64(bytes)/(1<<30)) }

func daysLeft(expire string) string {
	t, err := time.Parse(time.RFC3339, expire)
	if err != nil {
		return ""
	}
	d := int(time.Until(t).Hours() / 24)
	if d < 0 {
		return "（已过期）"
	}
	return fmt.Sprintf("（还剩 %d 天）", d)
}

var kindName = map[string]string{tencent.Lighthouse: "轻量应用服务器", tencent.CVM: "云服务器 CVM"}

func (a *App) toolTencentServers(ctx context.Context, raw json.RawMessage) (string, error) {
	var arg struct {
		Refresh bool `json:"refresh"`
	}
	if err := parseArgs(raw, &arg); err != nil {
		return "", err
	}
	return a.cloudRead(ctx, "查询腾讯云服务器列表", func(c *tencent.Client) (string, error) {
		list, err := a.TencentServers(ctx, arg.Refresh)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		if len(list.Servers) == 0 {
			b.WriteString("腾讯云账号下没有轻量应用服务器或云服务器。\n")
		}
		for _, s := range list.Servers {
			fmt.Fprintf(&b, "%s %s id=%s region=%s（%s）状态=%s 配置=%d核%dGB 系统盘%dGB 带宽%dMbps 公网IP=%s 系统=%s",
				kindName[s.Kind], s.Name, s.ID, s.Region, s.RegionName, s.State, s.CPU, s.MemoryGB, s.DiskGB, s.BandwidthMbps,
				orDash(strings.Join(s.PublicIPs, ",")), s.OS)
			if s.ExpiredTime != "" {
				fmt.Fprintf(&b, " 到期=%s%s 续费=%s", s.ExpiredTime, daysLeft(s.ExpiredTime), s.RenewFlag)
			} else {
				fmt.Fprintf(&b, " 计费=%s", s.ChargeType)
			}
			if s.TrafficTotal > 0 {
				fmt.Fprintf(&b, " 本月流量包=已用%s/%s（%.0f%%）", gb(s.TrafficUsed), gb(s.TrafficTotal), float64(s.TrafficUsed)*100/float64(s.TrafficTotal))
			}
			if len(s.Groups) > 0 {
				fmt.Fprintf(&b, " 安全组=%s", strings.Join(s.Groups, ","))
			}
			if s.ServerID > 0 {
				fmt.Fprintf(&b, " 对应 Miao Panel 服务器 id=%d", s.ServerID)
			}
			b.WriteString("\n")
		}
		for _, e := range list.Errors {
			fmt.Fprintf(&b, "注意：%s\n", e)
		}
		return b.String(), nil
	})
}

// monitorWanted are the metrics shown for a server, as candidates for the
// Lighthouse and CVM names, matched without case.
var monitorWanted = []struct {
	label string
	names []string
}{
	{"CPU 使用率", []string{"cpuusage"}},
	{"内存使用率", []string{"memusage"}},
	{"公网出带宽", []string{"wanouttraffic", "lighthouseouttraffic"}},
	{"公网入带宽", []string{"wanintraffic", "lighthouseintraffic"}},
}

// summarize describes a series: average, peak and when, latest value.
func summarize(pts []tencent.Point, format func(float64) string) string {
	if len(pts) == 0 {
		return "没有数据"
	}
	var sum float64
	peak := pts[0]
	for _, p := range pts {
		sum += p.V
		if p.V > peak.V {
			peak = p
		}
	}
	return fmt.Sprintf("平均 %s，最高 %s（%s），最新 %s", format(sum/float64(len(pts))), format(peak.V),
		time.Unix(peak.T, 0).In(localZone).Format("01-02 15:04"), format(pts[len(pts)-1].V))
}

// localZone is the time zone numbers are shown in (China Standard Time).
var localZone = time.FixedZone("CST", 8*3600)

// downsample keeps at most n points, averaging neighbours.
func downsample(pts []tencent.Point, n int) []tencent.Point {
	if len(pts) <= n {
		return pts
	}
	var out []tencent.Point
	step := float64(len(pts)) / float64(n)
	for i := 0; i < n; i++ {
		lo, hi := int(float64(i)*step), int(float64(i+1)*step)
		var sum float64
		for _, p := range pts[lo:hi] {
			sum += p.V
		}
		out = append(out, tencent.Point{T: pts[lo].T, V: sum / float64(hi-lo)})
	}
	return out
}

func (a *App) toolTencentServerDetail(ctx context.Context, raw json.RawMessage) (string, error) {
	var arg struct {
		Instance string `json:"instance"`
		Region   string `json:"region"`
		Hours    int    `json:"hours"`
	}
	if err := parseArgs(raw, &arg); err != nil {
		return "", err
	}
	if arg.Hours <= 0 || arg.Hours > 24*30 {
		arg.Hours = 24
	}
	return a.cloudRead(ctx, "查询腾讯云服务器："+arg.Instance, func(c *tencent.Client) (string, error) {
		s, ok, err := c.Instance(ctx, arg.Region, arg.Instance)
		if err != nil {
			return "", err
		}
		if !ok {
			return fmt.Sprintf("在地域 %s 找不到实例 %s，先用 tencent_servers 查实例 id 和地域。", arg.Region, arg.Instance), nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s %s（%s）状态=%s 公网IP=%s\n", kindName[s.Kind], s.Name, s.ID, s.State, strings.Join(s.PublicIPs, ","))

		group := ""
		var rules []tencent.FirewallRule
		if s.Kind == tencent.Lighthouse {
			rules, err = c.LighthouseFirewall(ctx, s.Region, s.ID)
			b.WriteString("防火墙规则（入站）：\n")
		} else if len(s.Groups) > 0 {
			group = s.Groups[0]
			rules, err = c.SecurityGroupIngress(ctx, s.Region, group)
			fmt.Fprintf(&b, "安全组 %s 入站规则（同一安全组可能绑定了多台服务器）：\n", group)
		}
		if err != nil {
			fmt.Fprintf(&b, "  读取失败：%v\n", err)
		}
		for _, r := range rules {
			fmt.Fprintf(&b, "  %s %s 来源=%s %s %s\n", r.Protocol, r.Port, orDash(r.CidrBlock), r.Action, r.Description)
		}

		snaps, err := c.Snapshots(ctx, s.Region, s)
		b.WriteString("系统盘快照：")
		switch {
		case err != nil:
			fmt.Fprintf(&b, "读取失败：%v\n", err)
		case len(snaps) == 0:
			b.WriteString("没有\n")
		default:
			b.WriteString("\n")
			for _, sn := range snaps {
				fmt.Fprintf(&b, "  %s %s 状态=%s 创建于 %s\n", sn.ID, sn.Name, sn.State, sn.Created)
			}
		}

		ns := tencent.Namespace(s.Kind)
		metrics, err := c.Metrics(ctx, s.Region, ns)
		if err != nil {
			fmt.Fprintf(&b, "监控：读取指标列表失败：%v\n", err)
			return b.String(), nil
		}
		end := time.Now()
		start := end.Add(-time.Duration(arg.Hours) * time.Hour)
		fmt.Fprintf(&b, "最近 %d 小时的监控：\n", arg.Hours)
		for _, w := range monitorWanted {
			var m *tencent.Metric
			for i := range metrics {
				for _, n := range w.names {
					if strings.EqualFold(metrics[i].Name, n) {
						m = &metrics[i]
					}
				}
			}
			if m == nil {
				continue
			}
			period := pickPeriod(m.Periods, end.Sub(start))
			pts, err := c.MonitorData(ctx, s.Region, ns, m.Name, s.ID, period, tencent.TimeArg(start), tencent.TimeArg(end))
			if err != nil {
				fmt.Fprintf(&b, "  %s：读取失败：%v\n", w.label, err)
				continue
			}
			unit := m.Unit
			format := func(v float64) string { return fmt.Sprintf("%.1f%s", v, unit) }
			fmt.Fprintf(&b, "  %s：%s\n", w.label, summarize(pts, format))
			var row []string
			for _, p := range downsample(pts, 24) {
				row = append(row, fmt.Sprintf("%s=%.1f", time.Unix(p.T, 0).In(localZone).Format("01-02 15:04"), p.V))
			}
			if len(row) > 0 {
				fmt.Fprintf(&b, "    走势：%s\n", strings.Join(row, " "))
			}
		}
		return b.String(), nil
	})
}

// pickPeriod chooses a sampling period that gives at most ~300 points.
func pickPeriod(supported []int, span time.Duration) int {
	if len(supported) == 0 {
		supported = []int{60, 300, 3600, 86400}
	}
	sort.Ints(supported)
	for _, p := range supported {
		if span.Seconds()/float64(p) <= 300 {
			return p
		}
	}
	return supported[len(supported)-1]
}

// ---- EdgeOne analytics ----

// eoDimensions maps friendly names to the ranking metric suffixes.
var eoDimensions = map[string]string{
	"url": "url", "ip": "sip", "country": "country", "province": "province", "status": "statusCode",
	"referer": "referers", "ua": "ua", "browser": "ua_browser", "os": "ua_os", "device": "ua_device",
	"type": "resourceType", "domain": "domain",
}

var eoDimensionNames = map[string]string{
	"url": "URL 路径", "ip": "客户端 IP", "country": "国家/地区", "province": "省份", "status": "状态码",
	"referer": "来源页", "ua": "User-Agent", "browser": "浏览器", "os": "操作系统", "device": "设备类型",
	"type": "资源类型", "domain": "域名",
}

var (
	filterKeyRe = regexp.MustCompile(`^[A-Za-z]{2,32}$`)
	filterOps   = map[string]bool{"equals": true, "notEquals": true, "include": true, "notInclude": true, "startWith": true, "notStartWith": true, "endWith": true, "notEndWith": true}
)

func humanBytes(v float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.1f%s", v, units[i])
}

func humanBits(v float64) string {
	units := []string{"bps", "Kbps", "Mbps", "Gbps"}
	i := 0
	for v >= 1000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	return fmt.Sprintf("%.1f%s", v, units[i])
}

// eoWindow works out the time range asked for: start/end ("2006-01-02 15:04",
// China time) or the last N hours.
func eoWindow(hours int, start, end string) (time.Time, time.Time, error) {
	if start != "" || end != "" {
		s, err1 := time.ParseInLocation("2006-01-02 15:04", start, localZone)
		e, err2 := time.ParseInLocation("2006-01-02 15:04", end, localZone)
		if err1 != nil || err2 != nil || !e.After(s) {
			return s, e, fmt.Errorf("start 和 end 要写成 2026-09-25 14:00 这样，并且 end 要晚于 start")
		}
		if e.Sub(s) > 31*24*time.Hour {
			return s, e, fmt.Errorf("最多查询 31 天")
		}
		return s, e, nil
	}
	if hours <= 0 {
		hours = 24
	}
	if hours > 31*24 {
		hours = 31 * 24
	}
	e := time.Now().Truncate(time.Minute)
	return e.Add(-time.Duration(hours) * time.Hour), e, nil
}

// eoInterval follows EdgeOne's own granularity rules for a time span.
func eoInterval(span time.Duration) string {
	switch {
	case span <= 2*time.Hour:
		return "min"
	case span <= 2*24*time.Hour:
		return "5min"
	case span <= 7*24*time.Hour:
		return "hour"
	}
	return "day"
}

// EOQuery is one EdgeOne analytics question.
type EOQuery struct {
	Domain    string           `json:"domain"`
	Hours     int              `json:"hours"`
	Start     string           `json:"start"`
	End       string           `json:"end"`
	View      string           `json:"view"`      // overview | top
	Dimension string           `json:"dimension"` // for top
	Metric    string           `json:"metric"`    // request | flux
	Limit     int              `json:"limit"`
	Filters   []tencent.Filter `json:"filters"`
}

// eoScope finds the site of a domain and, for a subdomain, the filter
// that narrows the data to it.
func eoScope(ctx context.Context, c *tencent.Client, domain string) (tencent.Zone, []tencent.Filter, error) {
	zones, err := c.Zones(ctx)
	if err != nil {
		return tencent.Zone{}, nil, err
	}
	domain = strings.ToLower(strings.TrimSpace(domain))
	z, ok := tencent.ZoneFor(zones, domain)
	if !ok {
		return z, nil, userErr("EdgeOne 里没有 %s 所在的站点", domain)
	}
	var filters []tencent.Filter
	if domain != z.ZoneName {
		filters = append(filters, tencent.Filter{Key: "domain", Operator: "equals", Value: []string{domain}})
	}
	return z, filters, nil
}

func (q *EOQuery) validate() error {
	for _, f := range q.Filters {
		if !filterKeyRe.MatchString(f.Key) || !filterOps[f.Operator] || len(f.Value) == 0 || len(f.Value) > 20 {
			return fmt.Errorf("筛选条件不对：%+v（key 如 country、statusCode、url，operator 如 equals、include）", f)
		}
	}
	if q.View == "top" {
		if _, ok := eoDimensions[q.Dimension]; !ok {
			return fmt.Errorf("dimension 只能是：url、ip、country、province、status、referer、ua、browser、os、device、type、domain")
		}
	}
	return nil
}

// EOReport is EdgeOne analytics for the UI.
type EOReport struct {
	Zone      string             `json:"zone"`
	Domain    string             `json:"domain"`
	Start     string             `json:"start"`
	End       string             `json:"end"`
	Interval  string             `json:"interval"`
	Requests  int64              `json:"requests"`
	Bytes     int64              `json:"bytes"`
	PeakBps   int64              `json:"peakBps"`
	AvgRespMs int64              `json:"avgRespMs"`
	HitRatio  float64            `json:"hitRatio"`  // -1 when unknown
	Series    []tencent.Point    `json:"series"`    // requests over time
	Flux      []tencent.Point    `json:"flux"`      // bytes over time
	Bandwidth []tencent.Point    `json:"bandwidth"` // bits per second over time
	Resp      []tencent.Point    `json:"resp"`      // average response time (ms) over time
	Tops      map[string][]EOTop `json:"tops,omitempty"`
	// The same length of time just before, for comparison; -1 when unknown.
	PrevRequests int64  `json:"prevRequests"`
	PrevBytes    int64  `json:"prevBytes"`
	CheckedAt    string `json:"checkedAt,omitempty"`
}

// EOTop is one row of a ranking with its share of the total.
type EOTop struct {
	Key   string  `json:"key"`
	Label string  `json:"label,omitempty"` // what the key means, e.g. 美国 for US
	Value int64   `json:"value"`
	Share float64 `json:"share"`
}

// eoRange is a query's site, domain filter and time window, looked up once
// and shared by the calls that make up a report.
type eoRange struct {
	zone       tencent.Zone
	scope      []tencent.Filter
	start, end time.Time
	interval   string
}

func eoRangeFor(ctx context.Context, c *tencent.Client, q EOQuery) (eoRange, error) {
	start, end, err := eoWindow(q.Hours, q.Start, q.End)
	if err != nil {
		return eoRange{}, userErr("%v", err)
	}
	z, scope, err := eoScope(ctx, c, q.Domain)
	if err != nil {
		return eoRange{}, err
	}
	return eoRange{zone: z, scope: scope, start: start, end: end, interval: eoInterval(end.Sub(start))}, nil
}

// overviewIn reads the totals and the time series; the traffic and the two
// cache numbers are fetched at the same time.
func overviewIn(ctx context.Context, c *tencent.Client, rg eoRange, domain string, extra []tencent.Filter) (EOReport, error) {
	zones := []string{rg.zone.ZoneID}
	r := EOReport{Zone: rg.zone.ZoneName, Domain: domain, Start: tencent.TimeArg(rg.start), End: tencent.TimeArg(rg.end), Interval: rg.interval, HitRatio: -1}
	var series, all, hit []tencent.Series
	var err error
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		series, err = c.L7Timing(ctx, zones, []string{"l7Flow_request", "l7Flow_outFlux", "l7Flow_outBandwidth", "l7Flow_avgResponseTime"},
			rg.start, rg.end, rg.interval, append(append([]tencent.Filter{}, rg.scope...), extra...))
	}()
	go func() {
		defer wg.Done()
		all, _ = c.L7CacheTiming(ctx, zones, []string{"l7Cache_outFlux"}, rg.start, rg.end, rg.interval, rg.scope)
	}()
	go func() {
		defer wg.Done()
		hitFilter := append(append([]tencent.Filter{}, rg.scope...), tencent.Filter{Key: "cacheType", Operator: "equals", Value: []string{"hit"}})
		hit, _ = c.L7CacheTiming(ctx, zones, []string{"l7Cache_outFlux"}, rg.start, rg.end, rg.interval, hitFilter)
	}()
	wg.Wait()
	if err != nil {
		return r, err
	}
	for _, s := range series {
		switch s.Metric {
		case "l7Flow_request":
			r.Requests, r.Series = s.Sum, s.Points
		case "l7Flow_outFlux":
			r.Bytes, r.Flux = s.Sum, s.Points
		case "l7Flow_outBandwidth":
			r.PeakBps, r.Bandwidth = s.Max, s.Points
		case "l7Flow_avgResponseTime":
			r.AvgRespMs, r.Resp = s.Avg, s.Points
		}
	}
	// Cache hit ratio: bytes served from cache over all bytes.
	if len(all) > 0 && all[0].Sum > 0 && len(hit) > 0 {
		r.HitRatio = float64(hit[0].Sum) / float64(all[0].Sum)
	}
	return r, nil
}

// topIn reads one ranking; shares are filled in against total.
func topIn(ctx context.Context, c *tencent.Client, rg eoRange, q EOQuery, total int64) ([]EOTop, error) {
	metric := "request"
	if q.Metric == "flux" {
		metric = "outFlux"
	}
	limit := q.Limit
	if limit <= 0 || limit > 100 {
		limit = 15
	}
	items, err := c.L7Top(ctx, []string{rg.zone.ZoneID}, "l7Flow_"+metric+"_"+eoDimensions[q.Dimension], rg.start, rg.end, limit,
		append(append([]tencent.Filter{}, rg.scope...), q.Filters...))
	if err != nil {
		return nil, err
	}
	var out []EOTop
	for _, it := range items {
		t := EOTop{Key: it.Key, Value: it.Value}
		if total > 0 {
			t.Share = float64(it.Value) / float64(total)
		}
		out = append(out, t)
	}
	return out, nil
}

func (a *App) eoOverview(ctx context.Context, c *tencent.Client, q EOQuery) (EOReport, error) {
	rg, err := eoRangeFor(ctx, c, q)
	if err != nil {
		return EOReport{}, err
	}
	return overviewIn(ctx, c, rg, q.Domain, q.Filters)
}

func (a *App) eoTop(ctx context.Context, c *tencent.Client, q EOQuery, total int64) ([]EOTop, error) {
	rg, err := eoRangeFor(ctx, c, q)
	if err != nil {
		return nil, err
	}
	return topIn(ctx, c, rg, q, total)
}

// eoReportTTL keeps a report briefly, so going back and forth on the
// 网站统计 page is instant; the page refreshes itself every minute.
const eoReportTTL = 20 * time.Second

type eoReportCache struct {
	mu sync.Mutex
	m  map[string]eoCached
}

type eoCached struct {
	at time.Time
	r  EOReport
}

// eoPageDims are the rankings on the 网站统计 page's EdgeOne view.
var eoPageDims = []string{"url", "country", "status", "ip", "referer", "device", "browser"}

// LatestEOAnalytics is the last report made for a domain and range, however
// old, so the page can show it at once while a fresh one is fetched.
func (a *App) LatestEOAnalytics(domain string, hours int) (EOReport, bool) {
	a.eoReports.mu.Lock()
	defer a.eoReports.mu.Unlock()
	c, ok := a.eoReports.m[fmt.Sprintf("%s|%d", strings.ToLower(domain), hours)]
	return c.r, ok
}

// EOAnalytics is the 网站统计 page's report: totals, the time series,
// the rankings and the period before, all fetched at the same time.
func (a *App) EOAnalytics(ctx context.Context, domain string, hours int, refresh bool) (EOReport, error) {
	c := a.tencentClient()
	if c == nil {
		return EOReport{}, userErr("还没有配置腾讯云密钥（设置 → 腾讯云）")
	}
	key := fmt.Sprintf("%s|%d", strings.ToLower(domain), hours)
	a.eoReports.mu.Lock()
	cached, ok := a.eoReports.m[key]
	a.eoReports.mu.Unlock()
	if ok && !refresh && time.Since(cached.at) < eoReportTTL {
		return cached.r, nil
	}

	q := EOQuery{Domain: domain, Hours: hours}
	rg, err := eoRangeFor(ctx, c, q)
	if err != nil {
		return EOReport{}, err
	}
	dims := append([]string{}, eoPageDims...)
	if len(rg.scope) == 0 { // the whole site: which of its domains
		dims = append(dims, "domain")
	}
	tops := make([][]EOTop, len(dims))
	var r EOReport
	var prev []tencent.Series
	var prevErr error
	var wg sync.WaitGroup
	wg.Add(2 + len(dims))
	go func() { defer wg.Done(); r, err = overviewIn(ctx, c, rg, domain, nil) }()
	go func() {
		defer wg.Done()
		span := rg.end.Sub(rg.start)
		prev, prevErr = c.L7Timing(ctx, []string{rg.zone.ZoneID}, []string{"l7Flow_request", "l7Flow_outFlux"},
			rg.start.Add(-span), rg.start, rg.interval, rg.scope)
	}()
	for i, dim := range dims {
		go func(i int, dim string) {
			defer wg.Done()
			tops[i], _ = topIn(ctx, c, rg, EOQuery{Dimension: dim, Limit: 20}, 0)
		}(i, dim)
	}
	wg.Wait()
	if err != nil {
		return r, err
	}
	r.PrevRequests, r.PrevBytes = -1, -1
	if prevErr == nil {
		for _, s := range prev {
			switch s.Metric {
			case "l7Flow_request":
				r.PrevRequests = s.Sum
			case "l7Flow_outFlux":
				r.PrevBytes = s.Sum
			}
		}
	}
	r.Tops = map[string][]EOTop{}
	for i, dim := range dims {
		for j := range tops[i] {
			if r.Requests > 0 {
				tops[i][j].Share = float64(tops[i][j].Value) / float64(r.Requests)
			}
			tops[i][j].Label = eoLabel(dim, tops[i][j].Key)
		}
		if tops[i] != nil {
			r.Tops[dim] = tops[i]
		}
	}
	r.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	a.eoReports.mu.Lock()
	if a.eoReports.m == nil {
		a.eoReports.m = map[string]eoCached{}
	}
	a.eoReports.m[key] = eoCached{at: time.Now(), r: r}
	a.eoReports.mu.Unlock()
	return r, nil
}

// eoLabel says in words what a ranking's key is, where it is a code.
func eoLabel(dim, key string) string {
	switch dim {
	case "country":
		return geoip.CountryName(key)
	case "status":
		return statusMeaning[key]
	case "device":
		return deviceNames[strings.ToLower(key)]
	}
	return ""
}

var statusMeaning = map[string]string{
	"200": "正常", "204": "正常（没有内容）", "206": "部分内容（下载、视频）", "301": "永久跳转", "302": "临时跳转",
	"304": "没有变化（用浏览器缓存）", "307": "临时跳转", "308": "永久跳转", "400": "请求有误", "401": "需要登录",
	"403": "拒绝访问", "404": "找不到", "405": "请求方式不允许", "408": "请求超时", "413": "请求太大",
	"416": "请求范围不对", "418": "被拦截", "429": "请求太频繁（被限速）", "499": "访客提前断开",
	"500": "源站程序出错", "502": "源站出错", "503": "源站暂时不可用", "504": "源站响应超时",
	"520": "源站返回异常", "522": "连不上源站", "523": "源站不可达", "524": "源站响应超时",
}

var deviceNames = map[string]string{"pc": "电脑", "mobile": "手机", "tablet": "平板", "tv": "电视", "other": "其他", "unknown": "未知"}

// EOSites lists the EdgeOne sites and acceleration domains, for the
// 网站统计 page's picker.
func (a *App) EOSites(ctx context.Context) ([]string, error) {
	c := a.tencentClient()
	if c == nil {
		return nil, userErr("还没有配置腾讯云密钥（设置 → 腾讯云）")
	}
	zones, err := c.Zones(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, z := range zones {
		out = append(out, z.ZoneName)
		if domains, err := c.AccelerationDomains(ctx, z.ZoneID, ""); err == nil {
			for _, d := range domains {
				if d.DomainName != z.ZoneName {
					out = append(out, d.DomainName)
				}
			}
		}
	}
	return out, nil
}

func (a *App) toolTencentEOAnalytics(ctx context.Context, raw json.RawMessage) (string, error) {
	var q EOQuery
	if err := parseArgs(raw, &q); err != nil {
		return "", err
	}
	if q.View == "" {
		q.View = "overview"
	}
	if err := q.validate(); err != nil {
		return "", err
	}
	title := "EdgeOne 访问分析：" + q.Domain
	if q.View == "top" {
		title += "（按" + eoDimensionNames[q.Dimension] + "排行）"
	}
	return a.cloudRead(ctx, title, func(c *tencent.Client) (string, error) {
		r, err := a.eoOverview(ctx, c, q)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "站点 %s，范围 %s 至 %s（粒度 %s）", r.Zone, r.Start, r.End, r.Interval)
		if len(q.Filters) > 0 {
			data, _ := json.Marshal(q.Filters)
			fmt.Fprintf(&b, "，筛选 %s", data)
		}
		b.WriteString("\n")
		if q.View == "top" {
			total := r.Requests
			if q.Metric == "flux" {
				total = r.Bytes
			}
			tops, err := a.eoTop(ctx, c, q, total)
			if err != nil {
				return "", err
			}
			unit := "请求"
			if q.Metric == "flux" {
				unit = "流量"
			}
			fmt.Fprintf(&b, "按%s的%s排行（总%s %s）：\n", eoDimensionNames[q.Dimension], unit, unit, map[bool]string{true: humanBytes(float64(total)), false: fmt.Sprint(total)}[q.Metric == "flux"])
			for i, t := range tops {
				v := fmt.Sprint(t.Value)
				if q.Metric == "flux" {
					v = humanBytes(float64(t.Value))
				}
				fmt.Fprintf(&b, "%d. %s  %s（%.1f%%）\n", i+1, t.Key, v, t.Share*100)
			}
			if len(tops) == 0 {
				b.WriteString("没有数据\n")
			}
			return b.String(), nil
		}
		fmt.Fprintf(&b, "请求数 %d，流量 %s，峰值带宽 %s，平均响应 %dms", r.Requests, humanBytes(float64(r.Bytes)), humanBits(float64(r.PeakBps)), r.AvgRespMs)
		if r.HitRatio >= 0 {
			fmt.Fprintf(&b, "，缓存命中率 %.1f%%（按流量）", r.HitRatio*100)
		}
		b.WriteString("\n")
		if len(r.Series) > 0 {
			peak := r.Series[0]
			for _, p := range r.Series {
				if p.V > peak.V {
					peak = p
				}
			}
			fmt.Fprintf(&b, "请求最多的时段：%s（%d 次）\n", time.Unix(peak.T, 0).In(localZone).Format("01-02 15:04"), int64(peak.V))
			b.WriteString("走势（时间 请求数 流量）：\n")
			reqs, flux := downsample(r.Series, 48), downsample(r.Flux, 48)
			for i, p := range reqs {
				f := "-"
				if i < len(flux) && len(r.Flux) == len(r.Series) {
					f = humanBytes(flux[i].V)
				}
				fmt.Fprintf(&b, "%s %d %s\n", time.Unix(p.T, 0).In(localZone).Format("01-02 15:04"), int64(math.Round(p.V)), f)
			}
		} else {
			b.WriteString("这段时间没有访问数据。\n")
		}
		return b.String(), nil
	})
}
