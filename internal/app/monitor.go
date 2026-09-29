package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/profile"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sender"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

// Monitoring: every website Miao Panel knows is opened once a minute, and
// every server reached over SSH is sampled every two minutes (CPU, memory,
// disk, network) over a connection kept open, so its login log is not
// filled with logins. A website that fails twice in a row, a server that
// cannot be reached three times in a row, a disk past its limit, memory
// or CPU kept past theirs, each start an incident and an alert; a website
// or server coming back ends it with a second one.

// MonitorSettings says what is watched and when to alert.
type MonitorSettings struct {
	Enabled bool     `json:"enabled"`
	Auto    bool     `json:"auto"`    // watch every website Miao Panel knows
	Extra   []string `json:"extra"`   // more addresses to watch
	Skip    []string `json:"skip"`    // known addresses not to watch
	Servers bool     `json:"servers"` // sample the servers
	DiskPct int      `json:"diskPct"` // alert when / is this full
	MemPct  int      `json:"memPct"`  // … memory stays this full for 6 minutes
	CPUPct  int      `json:"cpuPct"`  // … CPU stays this busy for 10 minutes
	Email   string   `json:"email"`   // also mail alerts here (when mail is set up)
}

const monitorKey = "monitor"

func defaultMonitorSettings() MonitorSettings {
	return MonitorSettings{Enabled: true, Auto: true, Servers: true, DiskPct: 90, MemPct: 95, CPUPct: 95, Extra: []string{}, Skip: []string{}}
}

// MonitorSettings returns what is watched.
func (a *App) MonitorSettings() MonitorSettings {
	s := defaultMonitorSettings()
	if raw, _ := a.Store.Setting(monitorKey); raw != "" {
		_ = json.Unmarshal([]byte(raw), &s)
	}
	if s.Extra == nil {
		s.Extra = []string{}
	}
	if s.Skip == nil {
		s.Skip = []string{}
	}
	return s
}

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// SaveMonitorSettings checks and saves what is watched.
func (a *App) SaveMonitorSettings(s MonitorSettings) (MonitorSettings, error) {
	clean := func(list []string) ([]string, error) {
		out := []string{}
		seen := map[string]bool{}
		for _, x := range list {
			x = strings.TrimSpace(x)
			if x == "" {
				continue
			}
			u, err := normTarget(x)
			if err != nil {
				return nil, err
			}
			if !seen[u] {
				seen[u] = true
				out = append(out, u)
			}
		}
		if len(out) > 200 {
			return nil, userErr("最多监控 200 个地址")
		}
		return out, nil
	}
	var err error
	if s.Extra, err = clean(s.Extra); err != nil {
		return s, err
	}
	if s.Skip, err = clean(s.Skip); err != nil {
		return s, err
	}
	for _, v := range []*int{&s.DiskPct, &s.MemPct, &s.CPUPct} {
		if *v < 50 || *v > 100 {
			return s, userErr("提醒的比例要在 50 到 100 之间")
		}
	}
	s.Email = strings.TrimSpace(s.Email)
	if s.Email != "" && !emailRe.MatchString(s.Email) {
		return s, userErr("邮箱地址不对")
	}
	data, _ := json.Marshal(s)
	if err := a.Store.SetSetting(monitorKey, string(data)); err != nil {
		return s, err
	}
	a.mon.mu.Lock()
	a.mon.targetsAt = time.Time{} // the list of what to watch is made again
	a.mon.mu.Unlock()
	return s, nil
}

// normTarget turns what was typed into the address checked: a scheme is
// added when missing, and only http and https are watched.
func normTarget(s string) (string, error) {
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || len(s) > 2048 || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsAny(u.Host, " /\\") {
		return "", userErr("地址不对：%s", s)
	}
	u.Host = strings.ToLower(u.Host)
	if u.Path == "" {
		u.Path = "/"
	}
	u.Fragment = ""
	return u.String(), nil
}

// monTarget is one address watched.
type monTarget struct {
	URL    string `json:"url"`
	Name   string `json:"name"`
	Source string `json:"source"` // where it came from, in words
}

type monitorState struct {
	round     sync.Mutex // one round at a time: 立即检查 waits for the minute's
	mu        sync.Mutex
	targets   []monTarget
	targetsAt time.Time
	fails     map[string]int         // consecutive failures, by address
	failAt    map[string]time.Time   // when the run of failures began
	gap       time.Duration          // between two failures that make an outage; 45s when 0
	conns     map[int64]sshx.Conn    // kept open between samples
	prev      map[int64]serverTotals // the last sample's counters
	misses    map[int64]int          // consecutive failed samples
	hot       map[string]int         // consecutive samples past a limit: "mem:3"
	lastPrune time.Time
}

// serverTotals are the counters a rate is worked out from.
type serverTotals struct {
	at          time.Time
	busy, total uint64
	rx, tx      uint64
}

// hostnameRe is a name a website can be reached at.
var hostnameRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)

// monitorTargets gathers the websites to watch: the running sites on each
// 1Panel, the names in the Nginx configs other servers were found with,
// and the addresses added by hand; minus the ones set aside.
func (a *App) monitorTargets(ctx context.Context, s MonitorSettings) []monTarget {
	byHost := map[string]monTarget{}
	var order []string
	add := func(t monTarget) {
		u, err := url.Parse(t.URL)
		if err != nil {
			return
		}
		if _, ok := byHost[u.Host]; ok {
			return
		}
		byHost[u.Host] = t
		order = append(order, u.Host)
	}
	for _, x := range s.Extra {
		u, _ := url.Parse(x)
		add(monTarget{URL: x, Name: u.Host + strings.TrimSuffix(u.Path, "/"), Source: "手动添加"})
	}
	if s.Auto {
		listed := map[int64]bool{} // servers whose panel listed their sites
		if v, err := a.Websites(ctx, 0); err == nil {
			for _, sv := range v.Servers {
				panel := "1Panel"
				if sv.Panel == "bt" {
					panel = "宝塔"
				}
				listed[sv.ID] = sv.Error == "" && !sv.NoPanel
				for _, x := range sv.Sites {
					d := strings.ToLower(x.Domain)
					if !x.Running || !hostnameRe.MatchString(d) || x.Type == "stream" {
						continue
					}
					scheme := "http"
					if x.HTTPS {
						scheme = "https"
					}
					add(monTarget{URL: scheme + "://" + d + "/", Name: d, Source: panel + " · " + sv.Name})
				}
			}
		}
		servers, _ := a.Store.ListServers()
		for _, sv := range servers {
			if sv.Adapter == "1panel" || listed[sv.ID] {
				continue
			}
			raw, _, _ := a.Store.GetProfile(sv.ID)
			if raw == "" {
				continue
			}
			for _, w := range profileWebsites(raw) {
				w = strings.ToLower(w)
				if hostnameRe.MatchString(w) {
					add(monTarget{URL: "http://" + w + "/", Name: w, Source: "服务器 " + sv.Name})
				}
			}
		}
	}
	skip := map[string]bool{}
	for _, x := range s.Skip {
		if u, err := url.Parse(x); err == nil {
			skip[u.Host] = true
		}
	}
	out := []monTarget{}
	for _, h := range order {
		if !skip[h] {
			out = append(out, byHost[h])
		}
	}
	return out
}

// relistTargets makes the monitoring gather what to watch again on its
// next round, after a server or a panel was set up.
func (a *App) relistTargets() {
	a.mon.mu.Lock()
	a.mon.targetsAt = time.Time{}
	a.mon.mu.Unlock()
}

// profileWebsites is the site names a server was found with.
func profileWebsites(raw string) []string { return profile.Parse(raw).Websites }

// Monitor watches until ctx ends: a round every minute.
func (a *App) Monitor(ctx context.Context) {
	ctx = withOrigin(ctx, OriginAuto)
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for n := 0; ; n++ {
		a.monitorRound(ctx, n%2 == 0)
		select {
		case <-ctx.Done():
			a.dropMonitorConns(0)
			return
		case <-tick.C:
		}
	}
}

// serverKinds are the incidents of a server.
var serverKinds = []string{"server", "disk", "mem", "cpu"}

// dropMonitorConns closes the kept connection to a server, or to every
// server when id is 0.
func (a *App) dropMonitorConns(id int64) {
	a.mon.mu.Lock()
	defer a.mon.mu.Unlock()
	for sid, c := range a.mon.conns {
		if id == 0 || sid == id {
			c.Close()
			delete(a.mon.conns, sid)
		}
	}
}

// forgetServer stops watching a deleted server: its connection closes and
// its open incidents end.
func (a *App) forgetServer(id int64) {
	a.dropMonitorConns(id)
	for _, k := range serverKinds {
		_, _ = a.Store.CloseIncident(k, strconv.FormatInt(id, 10))
	}
}

// monitorRound checks every website, and samples the servers when asked.
func (a *App) monitorRound(ctx context.Context, servers bool) {
	a.mon.round.Lock()
	defer a.mon.round.Unlock()
	s := a.MonitorSettings()
	if !s.Enabled || !s.Servers {
		a.dropMonitorConns(0)
	}
	if !s.Enabled {
		return
	}
	a.mon.mu.Lock()
	if a.mon.fails == nil {
		a.mon.fails, a.mon.failAt, a.mon.conns, a.mon.prev, a.mon.misses, a.mon.hot = map[string]int{}, map[string]time.Time{}, map[int64]sshx.Conn{}, map[int64]serverTotals{}, map[int64]int{}, map[string]int{}
	}
	age := time.Since(a.mon.targetsAt)
	stale := a.mon.targetsAt.IsZero() || age > 30*time.Minute || (len(a.mon.targets) == 0 && age > 5*time.Minute)
	a.mon.mu.Unlock()
	if stale {
		tctx, cancel := context.WithTimeout(ctx, time.Minute)
		t := a.monitorTargets(tctx, s)
		complete := tctx.Err() == nil
		cancel()
		a.mon.mu.Lock()
		a.mon.targets, a.mon.targetsAt = t, time.Now()
		a.mon.mu.Unlock()
		if complete {
			// Addresses no longer watched (skipped, moved to https, their
			// server gone) end their outages here; they cannot recover.
			keep := map[string]bool{}
			for _, x := range t {
				keep[x.URL] = true
			}
			_ = a.Store.EndIncidents("site", keep)
		}
	}
	a.mon.mu.Lock()
	targets := append([]monTarget(nil), a.mon.targets...)
	a.mon.mu.Unlock()

	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, t := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			a.checkSite(ctx, t)
		}()
	}
	if servers && s.Servers {
		list, _ := a.Store.ListServers()
		for _, sv := range list {
			if sv.AuthKind == "tat" {
				continue // the automation agent is too slow to ask every two minutes
			}
			wg.Add(1)
			go func() { defer wg.Done(); a.sampleServer(ctx, sv, s) }()
		}
	}
	wg.Wait()

	a.mon.mu.Lock()
	prune := time.Since(a.mon.lastPrune) > time.Hour
	if prune {
		a.mon.lastPrune = time.Now()
	}
	a.mon.mu.Unlock()
	if prune {
		_ = a.Store.PruneMonitor()
	}
}

// probeClient opens websites for the checks: certificates are checked
// (a bad one is an outage for visitors), redirects followed.
var probeClient = &http.Client{
	Timeout: 15 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("重定向次数太多")
		}
		return nil
	},
	Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 10 * time.Second,
		ResponseHeaderTimeout: 12 * time.Second, MaxIdleConnsPerHost: 1, IdleConnTimeout: 90 * time.Second},
}

// probe opens one address, through ProbeTransport when a test or demo
// sets it.
func (a *App) probe(ctx context.Context, target string) store.UptimeCheck {
	c := probeClient
	if a.ProbeTransport != nil {
		cc := *probeClient
		cc.Transport = a.ProbeTransport
		c = &cc
	}
	return probeWith(ctx, c, target)
}

func probeWith(ctx context.Context, client *http.Client, target string) store.UptimeCheck {
	c := store.UptimeCheck{Target: target}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		c.Error = err.Error()
		return c
	}
	req.Header.Set("User-Agent", "MiaoPanel-Monitor/1.0 (+uptime check)")
	start := time.Now()
	resp, err := client.Do(req)
	c.MS = int(time.Since(start) / time.Millisecond)
	if err != nil {
		c.Error = probeError(err)
		return c
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 256<<10))
	resp.Body.Close()
	c.Status = resp.StatusCode
	c.OK = resp.StatusCode < 500
	if !c.OK {
		c.Error = fmt.Sprintf("服务器返回 %d", resp.StatusCode)
	}
	return c
}

// probeError says in words why a website could not be opened.
func probeError(err error) string {
	var dns *net.DNSError
	var cert *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var opErr *net.OpError
	switch {
	case errors.As(err, &dns):
		return "域名解析失败"
	case errors.As(err, &hostname):
		return "证书和域名不匹配"
	case errors.As(err, &invalid):
		if invalid.Reason == x509.Expired {
			return "证书已过期"
		}
		return "证书无效"
	case errors.As(err, &unknown), errors.As(err, &cert):
		return "证书不受信任"
	case errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err):
		return "15 秒内没有响应"
	case errors.As(err, &opErr) && strings.Contains(opErr.Err.Error(), "refused"):
		return "连接被拒绝（端口没有服务在监听）"
	case strings.Contains(err.Error(), "重定向次数太多"):
		return "重定向次数太多"
	case strings.Contains(err.Error(), "connection reset"):
		return "连接被重置"
	}
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		msg = msg[i+2:]
	}
	return "打不开：" + msg
}

// checkSite checks one website and starts or ends its incident.
func (a *App) checkSite(ctx context.Context, t monTarget) {
	c := a.probe(ctx, t.URL)
	if ctx.Err() != nil {
		return
	}
	_ = a.Store.AddUptimeCheck(c)
	a.mon.mu.Lock()
	if c.OK {
		a.mon.fails[t.URL] = 0
	} else if a.mon.fails[t.URL]++; a.mon.fails[t.URL] == 1 {
		a.mon.failAt[t.URL] = time.Now()
	}
	fails, since, gap := a.mon.fails[t.URL], a.mon.failAt[t.URL], a.mon.gap
	a.mon.mu.Unlock()
	if gap == 0 {
		gap = 45 * time.Second
	}
	switch {
	// Two failures a minute apart: 立即检查 right after one is not a second.
	case !c.OK && fails >= 2 && time.Since(since) >= gap:
		if _, opened, err := a.Store.OpenIncident("site", t.URL, t.Name, c.Error); err == nil && opened {
			a.monitorAlert(ctx, "网站打不开："+t.Name, fmt.Sprintf("**%s** 连续两次打不开：%s。\n\n地址：%s（%s）\n\n可以在「监控」页看详情，或者让 AI 排查。", t.Name, c.Error, t.URL, t.Source))
		}
	case c.OK:
		if in, err := a.Store.CloseIncident("site", t.URL); err == nil && in.ID != 0 {
			a.monitorAlert(ctx, "网站恢复了："+t.Name, fmt.Sprintf("**%s** 又能打开了，中断了 %s（原因：%s）。", t.Name, lasted(in), in.Reason))
		}
	}
}

// lasted says how long an incident went on.
func lasted(in store.Incident) string {
	s, err1 := time.Parse(time.RFC3339, in.StartedAt)
	e, err2 := time.Parse(time.RFC3339, in.EndedAt)
	if err1 != nil || err2 != nil {
		return "一段时间"
	}
	d := e.Sub(s)
	switch {
	case d < 2*time.Minute:
		return "约 1 分钟"
	case d < time.Hour:
		return fmt.Sprintf("约 %d 分钟", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("约 %.1f 小时", d.Hours())
	}
	return fmt.Sprintf("约 %d 天", int(d.Hours()/24))
}

// monitorAlert makes an alert (pushed to the webhook, when there is one)
// and mails it when an address is set and mail is set up.
func (a *App) monitorAlert(ctx context.Context, title, text string) {
	a.addNotice(ctx, "alert", title, text)
	s := a.MonitorSettings()
	if s.Email == "" {
		return
	}
	if m, ok := a.mailConfig(); ok {
		mctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = sender.Send(mctx, m, s.Email, "[Miao Panel] "+title, strings.ReplaceAll(text, "**", ""))
	}
}

// sampleScript reads what a sample needs in one go, sections separated
// by "---": load, cores, the CPU counters, memory total and available
// (MB), how full / is, and bytes in and out on every interface but lo.
const sampleScript = `cat /proc/loadavg; echo ---
nproc 2>/dev/null || grep -c ^processor /proc/cpuinfo; echo ---
head -n 1 /proc/stat; echo ---
free -m | awk '/^Mem:/ {print $2, $7}'; echo ---
df -P / | awk 'NR==2 {print $5}'; echo ---
awk 'NR > 2 {sub(/:/, " "); if ($1 != "lo") {rx += $2; tx += $10}} END {printf "%.0f %.0f\n", rx, tx}' /proc/net/dev`

// sampleServer takes one sample of a server and checks it against the
// limits.
func (a *App) sampleServer(ctx context.Context, sv store.Server, s MonitorSettings) {
	a.mon.mu.Lock()
	c := a.mon.conns[sv.ID]
	a.mon.mu.Unlock()
	var err error
	if c == nil {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, c, err = a.connect(cctx, sv.ID)
		cancel()
		if err == nil {
			a.mon.mu.Lock()
			a.mon.conns[sv.ID] = c
			a.mon.mu.Unlock()
		}
	}
	var out string
	if err == nil {
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		var r sshx.Result
		r, err = runBounded(rctx, c, sampleScript)
		cancel()
		out = r.Stdout
		if err != nil {
			// The connection is done for; the next sample opens another.
			c.Close()
			a.mon.mu.Lock()
			if a.mon.conns[sv.ID] == c {
				delete(a.mon.conns, sv.ID)
			}
			a.mon.mu.Unlock()
		}
	}
	if ctx.Err() != nil {
		return
	}
	now := time.Now()
	m := store.ServerSample{ServerID: sv.ID}
	var tot serverTotals
	if err == nil {
		m, tot, err = parseSample(sv.ID, out, now)
	}
	a.mon.mu.Lock()
	prev, hasPrev := a.mon.prev[sv.ID]
	if err == nil {
		a.mon.prev[sv.ID] = tot
		a.mon.misses[sv.ID] = 0
	} else {
		a.mon.misses[sv.ID]++
	}
	misses := a.mon.misses[sv.ID]
	a.mon.mu.Unlock()
	id := strconv.FormatInt(sv.ID, 10)
	if err != nil {
		m = store.ServerSample{ServerID: sv.ID, Error: err.Error()}
		_ = a.Store.AddServerSample(m)
		if misses >= 3 {
			if _, opened, e := a.Store.OpenIncident("server", id, sv.Name, err.Error()); e == nil && opened {
				a.monitorAlert(ctx, "服务器连不上："+sv.Name, fmt.Sprintf("连续三次连不上 **%s**（%s）：%s。\n\n可能是服务器宕机、网络断了或者 SSH 服务停了。", sv.Name, sv.Host, err.Error()))
			}
		}
		return
	}
	if hasPrev {
		dt := now.Sub(prev.at).Seconds()
		if tot.total > prev.total && tot.busy >= prev.busy {
			m.CPU = round1(float64(tot.busy-prev.busy) * 100 / float64(tot.total-prev.total))
		}
		if dt > 0 && tot.rx >= prev.rx && tot.tx >= prev.tx {
			m.RX, m.TX = float64(tot.rx-prev.rx)/dt, float64(tot.tx-prev.tx)/dt
		}
	}
	_ = a.Store.AddServerSample(m)
	if in, e := a.Store.CloseIncident("server", id); e == nil && in.ID != 0 {
		a.monitorAlert(ctx, "服务器恢复了："+sv.Name, fmt.Sprintf("**%s** 又能连上了，中断了 %s。", sv.Name, lasted(in)))
	}
	// A limit is alerted once when it is passed, and ends quietly once
	// clear of it.
	over := func(kind string, now, clear bool, need int, title, text string) {
		key := kind + ":" + id
		a.mon.mu.Lock()
		if now {
			a.mon.hot[key]++
		} else {
			a.mon.hot[key] = 0
		}
		n := a.mon.hot[key]
		a.mon.mu.Unlock()
		if now && n >= need {
			if _, opened, e := a.Store.OpenIncident(kind, id, sv.Name, text); e == nil && opened {
				a.monitorAlert(ctx, title, text+"\n\n可以让 AI 看看是什么占用的、怎么处理。")
			}
		} else if clear {
			_, _ = a.Store.CloseIncident(kind, id)
		}
	}
	// The disk alerts on one sample, so it ends three points below the line
	// instead of opening and closing while it hovers there.
	over("disk", m.Disk >= float64(s.DiskPct), m.Disk < float64(s.DiskPct-3), 1, "磁盘快满了："+sv.Name, fmt.Sprintf("**%s** 的系统盘已经用了 %.0f%%（提醒线 %d%%），满了网站和数据库会出错。", sv.Name, m.Disk, s.DiskPct))
	over("mem", m.Mem >= float64(s.MemPct), m.Mem < float64(s.MemPct), 3, "内存快用完了："+sv.Name, fmt.Sprintf("**%s** 的内存已经连续 6 分钟用了 %.0f%% 以上（提醒线 %d%%），再多系统会杀掉进程。", sv.Name, m.Mem, s.MemPct))
	if hasPrev {
		over("cpu", m.CPU >= float64(s.CPUPct), m.CPU < float64(s.CPUPct), 5, "CPU 一直很忙："+sv.Name, fmt.Sprintf("**%s** 的 CPU 已经连续 10 分钟用了 %.0f%% 以上（提醒线 %d%%），网站可能会变慢。", sv.Name, m.CPU, s.CPUPct))
	}
}

// runBounded runs a command on a kept connection, closing the connection
// when ctx ends first: a server that went silent (powered off, no RST)
// otherwise blocks the call until TCP gives up, a quarter of an hour.
func runBounded(ctx context.Context, c sshx.Conn, cmd string) (sshx.Result, error) {
	type result struct {
		r   sshx.Result
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, err := c.Run(ctx, cmd, "", 16<<10)
		done <- result{r, err}
	}()
	select {
	case x := <-done:
		return x.r, x.err
	case <-ctx.Done():
		c.Close()
		return sshx.Result{}, errors.New("30 秒内没有回应")
	}
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }

// parseSample reads the output of sampleScript.
func parseSample(serverID int64, out string, at time.Time) (store.ServerSample, serverTotals, error) {
	m := store.ServerSample{ServerID: serverID, OK: true, At: at.UTC().Format(time.RFC3339)}
	tot := serverTotals{at: at}
	parts := strings.Split(out, "---")
	if len(parts) < 6 {
		return m, tot, errors.New("服务器返回的内容不完整")
	}
	field := func(i int) []string { return strings.Fields(parts[i]) }
	if f := field(0); len(f) > 0 {
		m.Load1, _ = strconv.ParseFloat(f[0], 64)
	}
	cores := 1
	if f := field(1); len(f) > 0 {
		if n, err := strconv.Atoi(f[0]); err == nil && n > 0 {
			cores = n
		}
	}
	if f := field(2); len(f) >= 5 && f[0] == "cpu" {
		var vals []uint64
		for _, x := range f[1:] {
			v, _ := strconv.ParseUint(x, 10, 64)
			vals = append(vals, v)
		}
		for i, v := range vals {
			// guest time is already counted in user time.
			if i >= 8 {
				break
			}
			tot.total += v
		}
		idle := vals[3]
		if len(vals) > 4 {
			idle += vals[4] // iowait
		}
		tot.busy = tot.total - idle
	}
	// Until there are two counters to compare, the load stands in.
	m.CPU = round1(min(100, m.Load1*100/float64(cores)))
	if f := field(3); len(f) >= 2 {
		total, _ := strconv.ParseFloat(f[0], 64)
		avail, _ := strconv.ParseFloat(f[1], 64)
		if total > 0 {
			m.Mem = round1((total - avail) * 100 / total)
		}
	}
	if f := field(4); len(f) > 0 {
		v, _ := strconv.ParseFloat(strings.TrimSuffix(f[0], "%"), 64)
		m.Disk = v
	}
	if f := field(5); len(f) >= 2 {
		tot.rx, _ = strconv.ParseUint(f[0], 10, 64)
		tot.tx, _ = strconv.ParseUint(f[1], 10, 64)
	}
	return m, tot, nil
}

// ---- the 监控 page ----

// SiteMonitor is one website on the page.
type SiteMonitor struct {
	URL       string   `json:"url"`
	Name      string   `json:"name"`
	Source    string   `json:"source"`
	Up        *bool    `json:"up"` // nil before the first check
	Status    int      `json:"status,omitempty"`
	MS        int      `json:"ms"`
	Error     string   `json:"error,omitempty"`
	CheckedAt string   `json:"checkedAt,omitempty"`
	Uptime    *float64 `json:"uptime"` // percent over the last day
	AvgMS     int      `json:"avgMs"`
	Recent    []int    `json:"recent"`          // the last hour, oldest first: ms, or -1 for a failure
	Since     string   `json:"since,omitempty"` // when the ongoing outage started
}

// ServerMonitor is one server on the page.
type ServerMonitor struct {
	ID       int64               `json:"id"`
	Name     string              `json:"name"`
	Sampled  bool                `json:"sampled"` // false for servers reached through the automation agent
	Latest   *store.ServerSample `json:"latest,omitempty"`
	Down     bool                `json:"down"`
	Since    string              `json:"since,omitempty"`
	Problems []string            `json:"problems"` // limits passed now
}

// MonitorView is the 监控 page.
type MonitorView struct {
	Settings  MonitorSettings  `json:"settings"`
	Mail      bool             `json:"mail"` // mail is set up, so alerts can be mailed
	Sites     []SiteMonitor    `json:"sites"`
	Servers   []ServerMonitor  `json:"servers"`
	Incidents []store.Incident `json:"incidents"`
	Listed    bool             `json:"listed"` // the list of websites has been made
}

// MonitorPage returns the 监控 page.
func (a *App) MonitorPage(ctx context.Context) (MonitorView, error) {
	s := a.MonitorSettings()
	_, mail := a.mailConfig()
	v := MonitorView{Settings: s, Mail: mail, Sites: []SiteMonitor{}, Servers: []ServerMonitor{}, Incidents: []store.Incident{}}
	a.mon.mu.Lock()
	targets := append([]monTarget(nil), a.mon.targets...)
	v.Listed = !a.mon.targetsAt.IsZero()
	a.mon.mu.Unlock()
	day := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	open := map[string]store.Incident{}
	if list, err := a.Store.Incidents(time.Now().Add(-7*24*time.Hour).UTC().Format(time.RFC3339), 200); err == nil {
		v.Incidents = list
		for _, in := range list {
			if in.EndedAt == "" {
				open[in.Kind+"|"+in.Target] = in
			}
		}
	}
	for _, t := range targets {
		sm := SiteMonitor{URL: t.URL, Name: t.Name, Source: t.Source, Recent: []int{}}
		checks, _ := a.Store.UptimeChecks(t.URL, day)
		if n := len(checks); n > 0 {
			last := checks[n-1]
			up := last.OK
			sm.Up, sm.Status, sm.MS, sm.Error, sm.CheckedAt = &up, last.Status, last.MS, last.Error, last.At
			good, sum := 0, 0
			for _, c := range checks {
				if c.OK {
					good++
					sum += c.MS
				}
			}
			pct := float64(int(float64(good)*10000/float64(n))) / 100
			sm.Uptime = &pct
			if good > 0 {
				sm.AvgMS = sum / good
			}
			for _, c := range checks[max(0, n-60):] {
				if c.OK {
					sm.Recent = append(sm.Recent, c.MS)
				} else {
					sm.Recent = append(sm.Recent, -1)
				}
			}
		}
		if in, ok := open["site|"+t.URL]; ok {
			sm.Since = in.StartedAt
		}
		v.Sites = append(v.Sites, sm)
	}
	// Down first, then the slowest.
	sort.SliceStable(v.Sites, func(i, j int) bool {
		di, dj := v.Sites[i].Up != nil && !*v.Sites[i].Up, v.Sites[j].Up != nil && !*v.Sites[j].Up
		if di != dj {
			return di
		}
		return false
	})
	servers, _ := a.Store.ListServers()
	for _, sv := range servers {
		m := ServerMonitor{ID: sv.ID, Name: sv.Name, Sampled: sv.AuthKind != "tat", Problems: []string{}}
		if list, _ := a.Store.ServerSamples(sv.ID, time.Now().Add(-30*time.Minute).UTC().Format(time.RFC3339)); len(list) > 0 {
			for i := len(list) - 1; i >= 0; i-- {
				if list[i].OK {
					x := list[i]
					m.Latest = &x
					break
				}
			}
		}
		id := strconv.FormatInt(sv.ID, 10)
		if in, ok := open["server|"+id]; ok {
			m.Down, m.Since = true, in.StartedAt
		}
		for _, k := range []string{"disk", "mem", "cpu"} {
			if in, ok := open[k+"|"+id]; ok {
				m.Problems = append(m.Problems, in.Reason)
			}
		}
		v.Servers = append(v.Servers, m)
	}
	return v, nil
}

// CheckNow checks every website at once, outside the minute, and
// remakes the list of what is watched.
func (a *App) CheckNow(ctx context.Context) (MonitorView, error) {
	a.mon.mu.Lock()
	a.mon.targetsAt = time.Time{}
	a.mon.mu.Unlock()
	a.monitorRound(ctx, false)
	return a.MonitorPage(ctx)
}

// SitePoint is a website's checks over a stretch of time.
type SitePoint struct {
	T     int64 `json:"t"` // unix seconds, start of the stretch
	MS    int   `json:"ms"`
	Fails int   `json:"fails"`
	N     int   `json:"n"`
}

// SiteHistory returns a website's checks over the last hours, in steps.
func (a *App) SiteHistory(target string, hours int) ([]SitePoint, error) {
	if hours <= 0 || hours > 24*7 {
		hours = 24
	}
	step := int64(hours) * 3600 / 96 // about 96 points
	if step < 60 {
		step = 60
	}
	checks, err := a.Store.UptimeChecks(target, time.Now().Add(-time.Duration(hours)*time.Hour).UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	out := []SitePoint{}
	idx := map[int64]int{}
	for _, c := range checks {
		t, err := time.Parse(time.RFC3339, c.At)
		if err != nil {
			continue
		}
		k := t.Unix() / step * step
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			out = append(out, SitePoint{T: k})
		}
		p := &out[i]
		p.N++
		if c.OK {
			p.MS += c.MS
		} else {
			p.Fails++
		}
	}
	for i := range out {
		if ok := out[i].N - out[i].Fails; ok > 0 {
			out[i].MS /= ok
		}
	}
	return out, nil
}

// ServerHistory returns a server's samples over the last hours, thinned
// to about 150 points.
func (a *App) ServerHistory(serverID int64, hours int) ([]store.ServerSample, error) {
	if hours <= 0 || hours > 24*7 {
		hours = 24
	}
	list, err := a.Store.ServerSamples(serverID, time.Now().Add(-time.Duration(hours)*time.Hour).UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	ok := list[:0]
	for _, m := range list {
		if m.OK {
			ok = append(ok, m)
		}
	}
	if len(ok) <= 150 {
		return ok, nil
	}
	step := float64(len(ok)) / 150
	out := make([]store.ServerSample, 0, 150)
	for i := 0; i < 150; i++ {
		lo, hi := int(float64(i)*step), int(float64(i+1)*step)
		p := ok[hi-1]
		var cpu, mem, rx, tx float64
		for _, m := range ok[lo:hi] {
			cpu, mem, rx, tx = cpu+m.CPU, mem+m.Mem, rx+m.RX, tx+m.TX
		}
		n := float64(hi - lo)
		p.CPU, p.Mem, p.RX, p.TX = round1(cpu/n), round1(mem/n), rx/n, tx/n
		out = append(out, p)
	}
	return out, nil
}

// toolMonitorStatus tells the AI what the monitoring knows: everything
// now, or one website's or server's history.
func (a *App) toolMonitorStatus(ctx context.Context, raw json.RawMessage) (string, error) {
	var arg struct {
		Refresh  bool   `json:"refresh"`
		Site     string `json:"site"`
		ServerID int64  `json:"server_id"`
		Hours    int    `json:"hours"`
	}
	if err := parseArgs(raw, &arg); err != nil {
		return "", err
	}
	if arg.Hours <= 0 || arg.Hours > 24*7 {
		arg.Hours = 24
	}
	if arg.ServerID > 0 {
		return a.serverHistoryText(arg.ServerID, arg.Hours)
	}
	var v MonitorView
	var err error
	if arg.Refresh {
		v, err = a.CheckNow(ctx)
	} else {
		v, err = a.MonitorPage(ctx)
	}
	if err != nil {
		return "", err
	}
	if arg.Site != "" {
		return a.siteHistoryText(v, arg.Site, arg.Hours)
	}
	var b strings.Builder
	st := v.Settings
	if !st.Enabled {
		b.WriteString("监控已关闭（可以用 monitor.settings.set 打开）。\n")
	}
	fmt.Fprintf(&b, "监控设置：自动监控所有网站 %s，采集服务器 %s，提醒线 磁盘 %d%% 内存 %d%%（持续 6 分钟）CPU %d%%（持续 10 分钟），告警邮箱 %s",
		kaiGuan(st.Auto), kaiGuan(st.Servers), st.DiskPct, st.MemPct, st.CPUPct, orDash(st.Email))
	if st.Email != "" && !v.Mail {
		b.WriteString("（邮件还没配置，发不出去）")
	}
	if len(st.Extra) > 0 {
		b.WriteString("；另外监控：" + strings.Join(st.Extra, "、"))
	}
	if len(st.Skip) > 0 {
		b.WriteString("；不监控：" + strings.Join(st.Skip, "、"))
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "网站（每分钟检查一次，%d 个）：\n", len(v.Sites))
	if len(v.Sites) == 0 {
		b.WriteString("  还没有要监控的网站\n")
	}
	for _, s := range v.Sites {
		state := "还没检查"
		if s.Up != nil && *s.Up {
			state = fmt.Sprintf("正常 %dms", s.MS)
		} else if s.Up != nil {
			state = "打不开：" + s.Error
			if s.Since != "" {
				state += "（从 " + whenShort(s.Since) + " 开始）"
			}
		}
		up := ""
		if s.Uptime != nil {
			up = fmt.Sprintf(" 24 小时可用率 %.2f%% 平均 %dms", *s.Uptime, s.AvgMS)
		}
		fmt.Fprintf(&b, "  %s（%s，%s）%s%s\n", s.Name, s.URL, s.Source, state, up)
	}
	b.WriteString("服务器（每两分钟采样一次）：\n")
	for _, sv := range v.Servers {
		switch {
		case !sv.Sampled:
			fmt.Fprintf(&b, "  %s：通过自动化助手连接，不采样（用 tencent_server_detail 看云监控）\n", sv.Name)
			continue
		case sv.Down:
			fmt.Fprintf(&b, "  %s：连不上（从 %s 开始）\n", sv.Name, whenShort(sv.Since))
			continue
		case sv.Latest == nil:
			fmt.Fprintf(&b, "  %s：最近 30 分钟没有采样\n", sv.Name)
			continue
		}
		m := sv.Latest
		fmt.Fprintf(&b, "  %s：现在 CPU %.0f%% 内存 %.0f%% 磁盘 %.0f%% 负载 %.2f 入 %s/s 出 %s/s", sv.Name, m.CPU, m.Mem, m.Disk, m.Load1, bytesText(int64(m.RX)), bytesText(int64(m.TX)))
		if h, err := a.ServerHistory(sv.ID, 24); err == nil && len(h) > 1 {
			var cpu, mem, cpuMax, memMax float64
			for _, x := range h {
				cpu, mem = cpu+x.CPU, mem+x.Mem
				cpuMax, memMax = max(cpuMax, x.CPU), max(memMax, x.Mem)
			}
			n := float64(len(h))
			fmt.Fprintf(&b, "；24 小时 CPU 平均 %.0f%% 最高 %.0f%%，内存平均 %.0f%% 最高 %.0f%%", cpu/n, cpuMax, mem/n, memMax)
		}
		if len(sv.Problems) > 0 {
			b.WriteString("；超过提醒线：" + strings.Join(sv.Problems, "；"))
		}
		b.WriteString("\n")
	}
	b.WriteString("最近 7 天的故障：\n")
	if len(v.Incidents) == 0 {
		b.WriteString("  没有\n")
	}
	for _, in := range v.Incidents {
		end := "还没恢复"
		if in.EndedAt != "" {
			end = "持续 " + lasted(in)
		}
		fmt.Fprintf(&b, "  %s %s %s：%s（%s）\n", in.StartedAt, incidentKind[in.Kind], in.Name, in.Reason, end)
	}
	return b.String(), nil
}

// siteHistoryText is one website's checks over the last hours.
func (a *App) siteHistoryText(v MonitorView, site string, hours int) (string, error) {
	want := strings.ToLower(strings.TrimSpace(site))
	var target *SiteMonitor
	for i, s := range v.Sites {
		if strings.EqualFold(s.URL, want) || strings.EqualFold(s.Name, want) {
			target = &v.Sites[i]
			break
		}
	}
	if target == nil {
		for i, s := range v.Sites {
			if strings.Contains(strings.ToLower(s.URL), want) {
				target = &v.Sites[i]
				break
			}
		}
	}
	if target == nil {
		return "", userErr("监控里没有 %s，先不填 site 看有哪些网站", site)
	}
	points, err := a.SiteHistory(target.URL, hours)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s（%s）最近 %d 小时每段的检查次数、失败次数和平均响应时间：\n", target.Name, target.URL, hours)
	if len(points) == 0 {
		b.WriteString("没有检查记录\n")
	}
	var n, fails int
	for _, p := range points {
		n, fails = n+p.N, fails+p.Fails
		line := fmt.Sprintf("%s 检查 %d 次", time.Unix(p.T, 0).Format("01-02 15:04"), p.N)
		if p.Fails > 0 {
			line += fmt.Sprintf(" 失败 %d 次", p.Fails)
		}
		if p.N > p.Fails {
			line += fmt.Sprintf(" 平均 %dms", p.MS)
		}
		b.WriteString(line + "\n")
	}
	if n > 0 {
		fmt.Fprintf(&b, "合计检查 %d 次，失败 %d 次，可用率 %.2f%%\n", n, fails, float64(n-fails)*100/float64(n))
	}
	return b.String(), nil
}

// serverHistoryText is one server's samples over the last hours, an
// hour (or a few) to a line.
func (a *App) serverHistoryText(serverID int64, hours int) (string, error) {
	sv, err := a.Store.GetServer(serverID)
	if err != nil {
		return "", userErr("找不到服务器 %d", serverID)
	}
	list, err := a.Store.ServerSamples(serverID, time.Now().Add(-time.Duration(hours)*time.Hour).UTC().Format(time.RFC3339))
	if err != nil {
		return "", err
	}
	per := max(1, hours/48) // at most about 48 lines
	var b strings.Builder
	fmt.Fprintf(&b, "%s 最近 %d 小时（每行 %d 小时：CPU 平均/最高、内存平均/最高、磁盘、负载最高、网络入/出平均）：\n", sv.Name, hours, per)
	type bucket struct {
		n, fails                 int
		cpu, cpuMax, mem, memMax float64
		disk, load, rx, tx       float64
	}
	var keys []int64
	buckets := map[int64]*bucket{}
	for _, m := range list {
		t, err := time.Parse(time.RFC3339, m.At)
		if err != nil {
			continue
		}
		k := t.Unix() / int64(per*3600) * int64(per*3600)
		x := buckets[k]
		if x == nil {
			x = &bucket{}
			buckets[k] = x
			keys = append(keys, k)
		}
		if !m.OK {
			x.fails++
			continue
		}
		x.n++
		x.cpu, x.mem, x.rx, x.tx = x.cpu+m.CPU, x.mem+m.Mem, x.rx+m.RX, x.tx+m.TX
		x.cpuMax, x.memMax, x.load, x.disk = max(x.cpuMax, m.CPU), max(x.memMax, m.Mem), max(x.load, m.Load1), m.Disk
	}
	if len(keys) == 0 {
		b.WriteString("没有采样记录（监控关着、这台服务器不采样，或者是新加的）\n")
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, k := range keys {
		x := buckets[k]
		at := time.Unix(k, 0).Format("01-02 15:04")
		if x.n == 0 {
			fmt.Fprintf(&b, "%s 连不上（%d 次）\n", at, x.fails)
			continue
		}
		n := float64(x.n)
		fmt.Fprintf(&b, "%s CPU %.0f%%/%.0f%% 内存 %.0f%%/%.0f%% 磁盘 %.0f%% 负载 %.2f 入 %s/s 出 %s/s", at, x.cpu/n, x.cpuMax, x.mem/n, x.memMax,
			x.disk, x.load, bytesText(int64(x.rx/n)), bytesText(int64(x.tx/n)))
		if x.fails > 0 {
			fmt.Fprintf(&b, " 连不上 %d 次", x.fails)
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

var incidentKind = map[string]string{"site": "网站打不开", "server": "服务器连不上", "disk": "磁盘快满", "mem": "内存快用完", "cpu": "CPU 过高"}
