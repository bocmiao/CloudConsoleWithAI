package app

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/profile"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/internal/visits"
)

// The 总览 page: servers, sites, certificates and security at a glance,
// what needs the user, and what changed lately. It is built only from what
// Miao Panel already knows (the last server profiles, the kept statistics
// and certificate overview), so it opens at once; each part that cannot
// be read is simply left out.

// OverviewServer is one server's last known state.
type OverviewServer struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Adapter    string `json:"adapter"`
	Host       string `json:"host"`
	Level      string `json:"level"`          // ok, warn, crit, unknown
	Note       string `json:"note,omitempty"` // the most pressing thing about it
	CPUPct     *int   `json:"cpuPct,omitempty"`
	MemPct     *int   `json:"memPct,omitempty"`
	DiskPct    *int   `json:"diskPct,omitempty"`
	ProfiledAt string `json:"profiledAt,omitempty"`
}

// OverviewVisits is today's traffic.
type OverviewVisits struct {
	Source    string  `json:"source"`
	Title     string  `json:"title"`
	Sites     int     `json:"sites"`
	PV        int64   `json:"pv"`
	PVBefore  int64   `json:"pvBefore"` // yesterday up to the same hour
	Hours     []int64 `json:"hours"`    // today's PV by hour so far
	CheckedAt string  `json:"checkedAt,omitempty"`
}

// OverviewCerts is the certificates' state.
type OverviewCerts struct {
	Total     int    `json:"total"`
	Attention int    `json:"attention"`
	Soonest   string `json:"soonest,omitempty"` // the domain that needs a look first
	Days      *int   `json:"days,omitempty"`
	Level     string `json:"level"`
}

// OverviewSecurity is the risky IPs of the last day.
type OverviewSecurity struct {
	High      int    `json:"high"`
	Unblocked int    `json:"unblocked"`
	Known     bool   `json:"known"` // whether blocking could be checked
	Source    string `json:"source"`
}

// OverviewItem is one thing that needs the user.
type OverviewItem struct {
	Level  string `json:"level"` // crit, warn, info, plan
	Title  string `json:"title"`
	Meta   string `json:"meta,omitempty"`
	Kind   string `json:"kind"` // plan, notice, cert, server, security, monitor, update, account
	ID     int64  `json:"id,omitempty"`
	Action string `json:"action"`
}

// OverviewView is the 总览 page.
type OverviewView struct {
	Servers  []OverviewServer  `json:"servers"`
	Visits   *OverviewVisits   `json:"visits,omitempty"`
	Certs    *OverviewCerts    `json:"certs,omitempty"`
	Security *OverviewSecurity `json:"security,omitempty"`
	Todo     []OverviewItem    `json:"todo"`
	Pending  int               `json:"pending"` // checklists waiting to be run
	Unread   int               `json:"unread"`
	Changes  []store.ExecLog   `json:"changes"`
}

func pct(n, total int) *int {
	if total <= 0 {
		return nil
	}
	v := n * 100 / total
	return &v
}

func serverState(sv store.Server, raw, at string) OverviewServer {
	o := OverviewServer{ID: sv.ID, Name: sv.Name, Adapter: sv.Adapter, Host: sv.Host, Level: "unknown", ProfiledAt: at}
	if strings.TrimSpace(raw) == "" {
		o.Note = "还没有识别环境"
		return o
	}
	p := profile.Parse(raw)
	o.Level = "ok"
	if m := p.Memory; m.TotalMB > 0 {
		o.MemPct = pct(m.TotalMB-m.AvailableMB, m.TotalMB)
	}
	for _, d := range p.Disks {
		if d.Mount == "/" {
			v := d.UsePct
			o.DiskPct = &v
		}
	}
	if f := strings.Fields(p.Load); len(f) > 0 && p.CPUCores > 0 {
		if l, err := strconv.ParseFloat(f[0], 64); err == nil {
			v := min(int(l*100/float64(p.CPUCores)), 100)
			o.CPUPct = &v
		}
	}
	for _, f := range p.Findings {
		switch {
		case f.Level == "danger":
			o.Level, o.Note = "crit", f.Title
		case f.Level == "warn" && o.Level == "ok":
			o.Level, o.Note = "warn", f.Title
		}
	}
	if o.DiskPct != nil && *o.DiskPct >= 85 && o.Level == "ok" {
		o.Level, o.Note = "warn", fmt.Sprintf("磁盘已用 %d%%", *o.DiskPct)
	}
	if o.MemPct != nil && *o.MemPct >= 90 && o.Level == "ok" {
		o.Level, o.Note = "warn", fmt.Sprintf("内存已用 %d%%", *o.MemPct)
	}
	return o
}

// visitsOverview reads today's traffic from the kept statistics.
func visitsOverview(v VisitsView) *OverviewVisits {
	days := v.Days[visits.All]
	if len(days) == 0 {
		return nil
	}
	o := &OverviewVisits{Source: v.Source, Title: v.Title, CheckedAt: v.CheckedAt}
	for _, s := range v.Sites {
		if s.Name != visits.All {
			o.Sites++
		}
	}
	today := days[len(days)-1]
	if today.Date == v.Today {
		o.PV = today.PV
	}
	// Hours still to come today are not drawn.
	last := 23
	if t := time.Now().In(cst); v.Today == t.Format("2006-01-02") {
		last = t.Hour()
	}
	now := -1
	for _, h := range v.Hours[visits.All] {
		if h.Hour > last {
			break
		}
		for len(o.Hours) < h.Hour {
			o.Hours = append(o.Hours, 0)
		}
		if len(o.Hours) == h.Hour {
			o.Hours = append(o.Hours, h.PV)
			now = h.Hour
		}
	}
	// Yesterday by hour is not kept; its day total spread by today's
	// shape would be made up, so only the total up to now is compared.
	if len(days) >= 2 && now >= 0 {
		y := days[len(days)-2]
		o.PVBefore = y.PV * int64(now+1) / 24
	}
	return o
}

// Overview gathers the 总览 page. Parts that need the network wait a few
// seconds at most.
func (a *App) Overview(ctx context.Context) (OverviewView, error) {
	v := OverviewView{Servers: []OverviewServer{}, Todo: []OverviewItem{}, Changes: []store.ExecLog{}}
	servers, err := a.Store.ListServers()
	if err != nil {
		return v, err
	}
	for _, sv := range servers {
		raw, at, _ := a.Store.GetProfile(sv.ID)
		o := serverState(sv, raw, at)
		// A sample from the last ten minutes is fresher than the profile.
		if list, _ := a.Store.ServerSamples(sv.ID, time.Now().Add(-10*time.Minute).UTC().Format(time.RFC3339)); len(list) > 0 {
			if m := list[len(list)-1]; m.OK {
				cpu, mem, disk := int(m.CPU+.5), int(m.Mem+.5), int(m.Disk+.5)
				o.CPUPct, o.MemPct, o.DiskPct = &cpu, &mem, &disk
			}
		}
		v.Servers = append(v.Servers, o)
	}

	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var (
		wg      sync.WaitGroup
		vv      VisitsView
		vvErr   error = fmt.Errorf("no source")
		certs   CertOverview
		certErr error = fmt.Errorf("not read")
		blocked []BlockedZone
		blkErr  error = fmt.Errorf("not read")
		acct    AccountView
		acctErr error = fmt.Errorf("not read")
	)
	source := ""
	if a.tencentClient() != nil {
		source = "edgeone"
	} else if len(servers) > 0 {
		source = fmt.Sprintf("server:%d", servers[0].ID)
	}
	if source != "" {
		wg.Add(1)
		go func() { defer wg.Done(); vv, vvErr = a.LatestVisits(ctx, source) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); certs, certErr = a.LatestCertificates(ctx) }()
	if source == "edgeone" {
		wg.Add(1)
		go func() { defer wg.Done(); blocked, _, blkErr = a.BlockedPage(ctx, PageLatest) }()
	}
	if a.hasCloud() {
		wg.Add(1)
		go func() { defer wg.Done(); acct, _, acctErr = a.CloudAccountPage(ctx, PageLatest) }()
	}
	wg.Wait()

	if vvErr == nil {
		v.Visits = visitsOverview(vv)
		if r := vv.Range(1); r != nil {
			sec := &OverviewSecurity{Source: source, Known: blkErr == nil}
			isBlocked := map[string]bool{}
			for _, z := range blocked {
				for _, ip := range z.IPs {
					isBlocked[ip] = true
				}
			}
			for _, ip := range r.IPs {
				if ip.Risk != "high" || ip.EdgeOne || ip.Crawler != "" {
					continue
				}
				sec.High++
				if !isBlocked[ip.IP] {
					sec.Unblocked++
				}
			}
			v.Security = sec
			if sec.Known && sec.Unblocked > 0 {
				v.Todo = append(v.Todo, OverviewItem{Level: "crit", Kind: "security", Action: "去看看",
					Title: fmt.Sprintf("%d 个高风险 IP 还没有封禁", sec.Unblocked), Meta: "扫描后台、猜密码或攻击 · 最近 24 小时"})
			}
		}
	}
	if certErr == nil {
		if c := certsOverview(certs); c.Total > 0 || c.Attention > 0 {
			v.Certs = c
			if c.Attention > 0 {
				meta := c.Soonest
				if c.Days != nil {
					meta += " " + leftText(*c.Days)
				}
				v.Todo = append(v.Todo, OverviewItem{Level: c.Level, Kind: "cert", Action: "查看",
					Title: fmt.Sprintf("%d 张证书需要处理", c.Attention), Meta: meta})
			}
		}
	}

	// What the monitoring finds wrong now.
	if list, err := a.Store.Incidents(time.Now().UTC().Format(time.RFC3339), 50); err == nil {
		for _, in := range list {
			if in.EndedAt != "" {
				continue
			}
			item := OverviewItem{Level: "warn", Kind: "monitor", Action: "让 AI 排查", Meta: in.Reason + " · " + whenShort(in.StartedAt) + "开始"}
			switch in.Kind {
			case "site":
				item.Level, item.Title = "crit", "网站打不开："+in.Name
			case "server":
				item.Level, item.Title = "crit", "服务器连不上："+in.Name
			case "disk":
				item.Title, item.Meta = "磁盘快满了："+in.Name, whenShort(in.StartedAt)+"开始"
			case "mem":
				item.Title, item.Meta = "内存快用完了："+in.Name, whenShort(in.StartedAt)+"开始"
			default:
				item.Title, item.Meta = "CPU 一直很忙："+in.Name, whenShort(in.StartedAt)+"开始"
			}
			v.Todo = append(v.Todo, item)
			for i := range v.Servers {
				if in.Kind == "server" && strconv.FormatInt(v.Servers[i].ID, 10) == in.Target {
					v.Servers[i].Level, v.Servers[i].Note = "crit", "连不上"
				}
			}
		}
	}

	// Money owed and what expires soon, a few at most.
	if acctErr == nil {
		items := accountTodo(acct)
		if len(items) > 4 {
			items = append(items[:3], OverviewItem{Level: "warn", Kind: "account", Action: "去看看",
				Title: fmt.Sprintf("还有 %d 项续费提醒", len(items)-3), Meta: "云服务器 → 账户和续费"})
		}
		v.Todo = append(v.Todo, items...)
	}

	if t := a.updateTodo(); t != nil {
		v.Todo = append(v.Todo, *t)
	}

	// Checklists proposed but not run, newest first.
	if plans, err := a.Store.ListPlans(50); err == nil {
		for _, p := range plans {
			if p.Status != core.PlanProposed {
				continue
			}
			v.Pending++
			if v.Pending <= 3 {
				v.Todo = append(v.Todo, OverviewItem{Level: "plan", Kind: "plan", ID: p.ID, Action: "查看清单",
					Title: "清单等你确认：" + p.Title, Meta: whenShort(p.CreatedAt)})
			}
		}
	}
	for _, s := range v.Servers {
		if s.Level == "crit" || s.Level == "warn" {
			v.Todo = append(v.Todo, OverviewItem{Level: s.Level, Kind: "server", ID: s.ID, Action: "让 AI 看看",
				Title: s.Name + "：" + s.Note, Meta: adapterText(s.Adapter)})
		}
	}
	nv := a.Notices()
	v.Unread = nv.Unread
	listed := map[string]bool{}
	for _, t := range v.Todo {
		listed[t.Title] = true
	}
	for _, n := range nv.Notices {
		// An outage still going on is listed already, from the monitoring.
		if n.Read || n.Kind != "alert" || len(v.Todo) >= 8 || listed[n.Title] {
			continue
		}
		v.Todo = append(v.Todo, OverviewItem{Level: "warn", Kind: "notice", ID: n.ID, Action: "查看", Title: n.Title, Meta: whenShort(n.At)})
	}
	rank := map[string]int{"crit": 0, "plan": 1, "warn": 2, "info": 3}
	sort.SliceStable(v.Todo, func(i, j int) bool { return rank[v.Todo[i].Level] < rank[v.Todo[j].Level] })

	if logs, err := a.Store.ListExec(true, 5); err == nil {
		v.Changes = logs
	}
	return v, nil
}

// certsOverview counts certificates the way the 证书 page does: the ones
// in use, and the ones in its 需要处理 (a problem certificate in use, or of
// a domain with none in use, and a site that served a bad certificate
// none of those explains).
func certsOverview(c CertOverview) *OverviewCerts {
	o := &OverviewCerts{Level: "ok"}
	note := func(level, domain string, days *int) {
		o.Attention++
		if level == "crit" || o.Level == "ok" {
			o.Level = level
		}
		if o.Soonest == "" || days != nil && (o.Days == nil || *days < *o.Days) {
			o.Soonest, o.Days = domain, days
		}
	}
	bad := func(level string) bool { return level == "crit" || level == "warn" }
	served := map[string]bool{}
	for _, g := range c.Groups {
		used := false
		for _, x := range g.Certs {
			used = used || x.InUse
		}
		for _, x := range g.Certs {
			if x.InUse {
				o.Total++
			}
			if bad(x.Level) && (x.InUse || !used) {
				note(x.Level, g.Domain, x.DaysLeft)
				for _, d := range append(append(append([]string{}, x.UsedBy...), x.EdgeOne...), x.ServedOn...) {
					served[d] = true
				}
			}
		}
	}
	for _, l := range c.Live {
		if bad(l.Level) && !served[l.Domain] {
			note(l.Level, l.Domain, l.DaysLeft)
		}
	}
	return o
}

func leftText(d int) string {
	switch {
	case d < 0:
		return "已过期"
	case d == 0:
		return "今天到期"
	}
	return fmt.Sprintf("%d 天后到期", d)
}

func adapterText(a string) string {
	switch a {
	case "1panel":
		return "1Panel"
	case "bt":
		return "宝塔"
	case "linux":
		return "Linux"
	}
	return "还没有识别"
}

// whenShort says when something happened, briefly.
func whenShort(at string) string {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	}
	return t.In(cst).Format("1-2 15:04")
}
