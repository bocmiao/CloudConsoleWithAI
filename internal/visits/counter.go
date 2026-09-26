package visits

import (
	"sort"
	"time"
)

// Counter counts log records here, the way sitelogs.sh counts them on a
// server. Records may come in any order.
type Counter struct {
	today  time.Time
	source string
	b      *builder
	day    map[string]map[string]*acc // site -> date
	hour   map[string]*[24]Hour
	rng    map[int]*rangeAcc
}

// acc counts requests with distinct visitors.
type acc struct {
	c      Counts
	uv, ip map[string]bool
	top    map[string]map[string]int64
}

func newAcc() *acc {
	return &acc{uv: map[string]bool{}, ip: map[string]bool{}, top: map[string]map[string]int64{}}
}

func (a *acc) bump(kind, value string) {
	m := a.top[kind]
	if m == nil {
		m = map[string]int64{}
		a.top[kind] = m
	}
	m[value]++
}

type ipAcc struct {
	p       *IPProfile
	paths   map[string]int64
	sites   map[string]int64
	minutes map[string]int64
}

type rangeAcc struct {
	sites map[string]*acc
	ips   map[string]*ipAcc
}

// NewCounter counts days up to today, which is in the logs' local time.
func NewCounter(source string, today time.Time) *Counter {
	y, m, d := today.Date()
	c := &Counter{
		today:  time.Date(y, m, d, 0, 0, 0, 0, today.Location()),
		source: source,
		b:      newBuilder(source),
		day:    map[string]map[string]*acc{},
		hour:   map[string]*[24]Hour{},
		rng:    map[int]*rangeAcc{},
	}
	c.b.r.Today = c.today.Format("2006-01-02")
	c.b.r.Zone = c.today.Format("-0700")
	for _, n := range Ranges {
		c.rng[n] = &rangeAcc{sites: map[string]*acc{}, ips: map[string]*ipAcc{}}
	}
	return c
}

// Unparsed notes a line of a site's log that could not be read.
func (c *Counter) Unparsed(site string) {
	s := c.b.site(site)
	s.Lines++
	s.Unparsed++
}

// Add counts one request. t is in the logs' local time; target is what
// was requested, query included; forwarded says the IP is the visitor's
// own (not a proxy's).
func (c *Counter) Add(site string, t time.Time, ip, method, target string, status int, bytes int64, ref, ua string, forwarded bool) {
	s := c.b.site(site)
	s.Lines++
	y, m, d := t.In(c.today.Location()).Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, c.today.Location())
	age := int(c.today.Sub(day).Hours()+0.5) / 24
	if day.After(c.today) || age >= historyDays {
		return
	}
	if forwarded {
		s.Forwarded++
	}
	t = t.In(c.today.Location())
	r := classify(site, method, target, status, ref, ua)
	r.ip, r.bytes = ip, bytes
	date, stamp, minute := t.Format("2006-01-02"), t.Format("2006-01-02 15:04:05"), t.Format("2006-01-02 15:04")

	for _, k := range []string{site, All} {
		dm := c.day[k]
		if dm == nil {
			dm = map[string]*acc{}
			c.day[k] = dm
		}
		a := dm[date]
		if a == nil {
			a = newAcc()
			dm[date] = a
		}
		count(a, r, k)
		if age == 0 {
			h := c.hour[k]
			if h == nil {
				h = &[24]Hour{}
				c.hour[k] = h
			}
			h[t.Hour()].Hour = t.Hour()
			h[t.Hour()].Requests++
			if r.page {
				h[t.Hour()].PV++
			}
		}
	}
	for _, n := range Ranges {
		if age >= n {
			continue
		}
		rg := c.rng[n]
		for _, k := range []string{site, All} {
			a := rg.sites[k]
			if a == nil {
				a = newAcc()
				rg.sites[k] = a
			}
			count(a, r, k)
		}
		x := rg.ips[ip]
		if x == nil {
			x = &ipAcc{p: &IPProfile{IP: ip, Risk: "none"}, paths: map[string]int64{}, sites: map[string]int64{}, minutes: map[string]int64{}}
			rg.ips[ip] = x
		}
		profile(x, r, stamp, minute)
		if r.page {
			c.b.visitor(n, site, ip, 1)
		}
	}
}

// count adds a request to a day's or a range's counts for site k.
func count(a *acc, r request, k string) {
	a.c.Requests++
	a.c.Bytes += r.bytes
	if r.status >= 400 && r.status < 500 {
		a.c.E4xx++
	}
	if r.status >= 500 {
		a.c.E5xx++
	}
	prefix := ""
	if k == All {
		prefix = r.site
	}
	a.bump("status", itoa(r.status))
	a.bump("dir", prefix+r.dir)
	if r.status >= 400 {
		a.bump("errpage", itoa(r.status)+" "+prefix+r.path)
	}
	if r.leak {
		a.bump("leak", itoa(r.status)+" "+prefix+r.path)
	}
	if r.dead {
		a.bump("dead", prefix+r.path+" ← "+r.deadFrom)
	}
	if r.bot {
		a.c.Bots++
		a.bump("bot", r.botName)
		return
	}
	a.bump("ip", r.ip)
	if !r.page {
		return
	}
	a.c.PV++
	a.bump("page", prefix+r.path)
	a.bump("referer", r.refHost)
	a.bump("device", r.device)
	ua := r.ua
	if len(ua) > 80 {
		ua = ua[:80]
	}
	if key := r.ip + "|" + ua; !a.uv[key] {
		a.uv[key] = true
		a.c.UV++
	}
	if !a.ip[r.ip] {
		a.ip[r.ip] = true
		a.c.IP++
	}
}

func profile(x *ipAcc, r request, stamp, minute string) {
	p := x.p
	p.Requests++
	if r.page {
		p.PV++
	}
	if r.status >= 400 && r.status < 500 {
		p.E4xx++
	}
	if r.status >= 500 {
		p.E5xx++
	}
	if r.method == "POST" {
		p.Posts++
	}
	if r.sensitive {
		p.Sensitive++
	}
	if r.inject {
		p.Inject++
	}
	if r.login {
		p.Login++
	}
	x.paths[r.path]++
	x.sites[r.site]++
	x.minutes[minute]++
	if x.minutes[minute] > p.PeakMin {
		p.PeakMin = x.minutes[minute]
	}
	if p.First == "" || stamp < p.First {
		p.First = stamp
	}
	if stamp >= p.Last {
		p.Last, p.UA, p.Bot = stamp, r.ua, r.botName
	}
}

// flagged IPs are described before busy ones.
func flagged(p *IPProfile) bool {
	return p.Sensitive > 0 || p.Inject > 0 || p.Login >= 5 || (p.Requests >= 30 && p.E4xx*2 >= p.Requests) || p.PeakMin >= 60
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// Report is the counts so far.
func (c *Counter) Report() Report {
	b := c.b
	b.site(All)
	for k, dm := range c.day {
		b.site(k)
		for date, a := range dm {
			if b.days[k] == nil {
				b.days[k] = map[string]Counts{}
			}
			b.days[k][date] = a.c
		}
	}
	for k, h := range c.hour {
		m := map[int]Hour{}
		for i, v := range h {
			v.Hour = i
			m[i] = v
		}
		b.hours[k] = m
	}
	for _, n := range Ranges {
		rg := c.rng[n]
		for k, a := range rg.sites {
			s := b.siteRange(n, k)
			s.Total = a.c
			for kind, m := range a.top {
				items := make([]Item, 0, len(m))
				for v, cnt := range m {
					items = append(items, Item{Value: v, Count: cnt})
				}
				s.Top[kind] = items
			}
		}
		// The IPs worth describing: flagged ones, then the busiest.
		list := make([]*ipAcc, 0, len(rg.ips))
		for _, x := range rg.ips {
			list = append(list, x)
		}
		sort.Slice(list, func(i, j int) bool {
			fi, fj := flagged(list[i].p), flagged(list[j].p)
			if fi != fj {
				return fi
			}
			if list[i].p.Requests != list[j].p.Requests {
				return list[i].p.Requests > list[j].p.Requests
			}
			return list[i].p.IP < list[j].p.IP
		})
		if len(list) > profileN {
			list = list[:profileN]
		}
		for _, x := range list {
			p := b.ip(n, x.p.IP)
			*p = *x.p
			for v, cnt := range x.paths {
				p.TopPaths = append(p.TopPaths, Item{Value: v, Count: cnt})
			}
			for v, cnt := range x.sites {
				p.Sites = append(p.Sites, Item{Value: v, Count: cnt})
			}
			p.Paths = int64(len(x.paths))
		}
	}
	return b.finish()
}
