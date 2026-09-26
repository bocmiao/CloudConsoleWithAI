// Package visits counts website visits from access logs: PV, UV, IPs,
// pages, directories, referrers, crawlers, errors and what each notable
// IP did, for today, 7 and 30 days at once. Server logs are counted on the
// server by scripts/sitelogs.sh (Parse reads its output); EdgeOne logs are
// counted here by Counter, with the same rules.
package visits

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// All is the name of the entry that adds up every site; its UV and IP
// counts are distinct across sites.
const All = "*"

// Ranges are the periods counted: today, 7 days, 30 days.
var Ranges = []int{1, 7, 30}

// Days of history kept per site.
const historyDays = 30

// Limits on what is kept, the same as sitelogs.sh's.
const (
	topN      = 20   // rows per ranking
	profileN  = 80   // IPs described per range
	ipTopN    = 5    // pages and sites per IP
	visitorsN = 3000 // visitor IPs per site and range, for regions
)

// Counts is what happened on one day, or over a range.
type Counts struct {
	Requests int64 `json:"requests"`
	PV       int64 `json:"pv"`
	UV       int64 `json:"uv"`
	IP       int64 `json:"ip"`
	Bots     int64 `json:"bots"`
	Bytes    int64 `json:"bytes"`
	E4xx     int64 `json:"e4xx"`
	E5xx     int64 `json:"e5xx"`
}

// Day is one day's counts; Date is local to the logs.
type Day struct {
	Date string `json:"date"`
	Counts
}

// Hour is one hour of today.
type Hour struct {
	Hour     int   `json:"hour"`
	Requests int64 `json:"requests"`
	PV       int64 `json:"pv"`
}

// Item is one row of a ranking. Note adds a word about it, e.g. where an
// IP is.
type Item struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
	Note  string `json:"note,omitempty"`
}

// File is a log file that was read.
type File struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// SiteInfo is a site and how its logs read.
type SiteInfo struct {
	Name      string `json:"name"`
	Lines     int64  `json:"lines"`     // log lines read
	Forwarded int64  `json:"forwarded"` // lines in range with the visitor's real IP forwarded
	Unparsed  int64  `json:"unparsed"`
	Files     []File `json:"files,omitempty"`
	// Warning says why the numbers may be off, e.g. visitor IPs that are
	// really EdgeOne's nodes; Fix names what Miao Panel can do about it
	// ("realip": have the server record the visitors' real IPs).
	Warning string `json:"warning,omitempty"`
	Fix     string `json:"fix,omitempty"`
}

// SiteRange is a site over one range.
type SiteRange struct {
	Total Counts            `json:"total"`
	Top   map[string][]Item `json:"top"`
}

// Rankings in each SiteRange; region and isp are added by Locate.
var Kinds = []string{"page", "dir", "referer", "ip", "status", "bot", "device", "errpage", "dead", "leak", "region", "isp"}

// IPProfile is what one IP did over a range, across all sites.
type IPProfile struct {
	IP        string `json:"ip"`
	Requests  int64  `json:"requests"`
	PV        int64  `json:"pv"`
	E4xx      int64  `json:"e4xx"`
	E5xx      int64  `json:"e5xx"`
	Posts     int64  `json:"posts"`
	Paths     int64  `json:"paths"`     // different addresses requested
	Sensitive int64  `json:"sensitive"` // probes of admin, backup and secret files that were not there
	Inject    int64  `json:"inject"`    // requests carrying attack payloads
	Login     int64  `json:"login"`     // refused login attempts
	PeakMin   int64  `json:"peakMin"`   // most requests in one minute
	// Direct counts requests that reached the server itself, not through
	// EdgeOne or another proxy (server logs only): blocking the IP in
	// EdgeOne does not stop these.
	Direct   int64  `json:"direct,omitempty"`
	First    string `json:"first"`
	Last     string `json:"last"`
	UA       string `json:"ua"`
	Bot      string `json:"bot,omitempty"`
	Sites    []Item `json:"sites"`
	TopPaths []Item `json:"topPaths"`

	// Set by Locate and Assess.
	Place       string   `json:"place,omitempty"`
	ISP         string   `json:"isp,omitempty"`
	EdgeOne     bool     `json:"edgeOne,omitempty"`
	Crawler     string   `json:"crawler,omitempty"` // a search engine crawler that checked out
	FakeCrawler bool     `json:"fakeCrawler,omitempty"`
	Risk        string   `json:"risk"` // high, medium, low, none
	Score       int      `json:"score"`
	Reasons     []string `json:"reasons,omitempty"`
}

// Range is every site over one period, and the IPs worth a look.
type Range struct {
	Days  int                   `json:"days"`
	Sites map[string]*SiteRange `json:"sites"`
	IPs   []IPProfile           `json:"ips"`
}

// Report is one count of a server's or EdgeOne's logs.
type Report struct {
	Source  string                              `json:"source"` // server, edgeone
	Today   string                              `json:"today"`
	Zone    string                              `json:"zone"`
	Problem string                              `json:"problem,omitempty"`
	Sites   []SiteInfo                          `json:"sites"` // All first, then by 30-day PV
	Days    map[string][]Day                    `json:"days"`
	Hours   map[string][]Hour                   `json:"hours"`
	Ranges  map[string]*Range                   `json:"ranges"`
	Notes   []string                            `json:"notes,omitempty"`
	visits  map[int]map[string]map[string]int64 // range -> site -> visitor IP -> page views
}

// Range returns the report for a period of n days.
func (r *Report) Range(n int) *Range { return r.Ranges[strconv.Itoa(n)] }

// SiteNames lists the sites, without All.
func (r *Report) SiteNames() []string {
	var out []string
	for _, s := range r.Sites {
		if s.Name != All {
			out = append(out, s.Name)
		}
	}
	return out
}

func num(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func counts(f []string) Counts {
	for len(f) < 8 {
		f = append(f, "0")
	}
	return Counts{Requests: num(f[0]), PV: num(f[1]), UV: num(f[2]), IP: num(f[3]), Bots: num(f[4]), Bytes: num(f[5]), E4xx: num(f[6]), E5xx: num(f[7])}
}

// builder assembles a Report from counts, whichever way they were made.
type builder struct {
	r       Report
	sites   map[string]*SiteInfo
	days    map[string]map[string]Counts
	hours   map[string]map[int]Hour
	profile map[int]map[string]*IPProfile
}

func newBuilder(source string) *builder {
	return &builder{
		r:       Report{Source: source, Ranges: map[string]*Range{}, visits: map[int]map[string]map[string]int64{}},
		sites:   map[string]*SiteInfo{},
		days:    map[string]map[string]Counts{},
		hours:   map[string]map[int]Hour{},
		profile: map[int]map[string]*IPProfile{},
	}
}

func (b *builder) site(name string) *SiteInfo {
	s := b.sites[name]
	if s == nil {
		s = &SiteInfo{Name: name}
		b.sites[name] = s
	}
	return s
}

func (b *builder) rng(n int) *Range {
	k := strconv.Itoa(n)
	r := b.r.Ranges[k]
	if r == nil {
		r = &Range{Days: n, Sites: map[string]*SiteRange{}, IPs: []IPProfile{}}
		b.r.Ranges[k] = r
	}
	return r
}

func (b *builder) siteRange(n int, site string) *SiteRange {
	r := b.rng(n)
	s := r.Sites[site]
	if s == nil {
		s = &SiteRange{Top: map[string][]Item{}}
		r.Sites[site] = s
	}
	b.site(site)
	return s
}

func (b *builder) ip(n int, ip string) *IPProfile {
	m := b.profile[n]
	if m == nil {
		m = map[string]*IPProfile{}
		b.profile[n] = m
	}
	p := m[ip]
	if p == nil {
		p = &IPProfile{IP: ip, Sites: []Item{}, TopPaths: []Item{}, Risk: "none"}
		m[ip] = p
	}
	return p
}

func (b *builder) visitor(n int, site, ip string, pv int64) {
	m := b.r.visits[n]
	if m == nil {
		m = map[string]map[string]int64{}
		b.r.visits[n] = m
	}
	if m[site] == nil {
		m[site] = map[string]int64{}
	}
	m[site][ip] += pv
}

// sortItems orders a ranking by count, then value, and keeps the first n.
func sortItems(items []Item, n int) []Item {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Count != items[j].Count {
			return items[i].Count > items[j].Count
		}
		return items[i].Value < items[j].Value
	})
	if len(items) > n {
		items = items[:n]
	}
	return items
}

// finish fills gaps (every day of history, every hour of today) and puts
// things in a stable order.
func (b *builder) finish() Report {
	r := b.r
	if all := b.sites[All]; all != nil {
		all.Lines, all.Forwarded, all.Unparsed = 0, 0, 0
		for name, s := range b.sites {
			if name != All {
				all.Lines += s.Lines
				all.Forwarded += s.Forwarded
				all.Unparsed += s.Unparsed
			}
		}
	}
	r.Days, r.Hours = map[string][]Day{}, map[string][]Hour{}
	dates := dateRange(r.Today, historyDays)
	for name := range b.sites {
		var days []Day
		for _, d := range dates {
			days = append(days, Day{Date: d, Counts: b.days[name][d]})
		}
		if len(dates) == 0 {
			for d, c := range b.days[name] {
				days = append(days, Day{Date: d, Counts: c})
			}
			sort.Slice(days, func(i, j int) bool { return days[i].Date < days[j].Date })
		}
		r.Days[name] = days
		hours := make([]Hour, 24)
		for h := range hours {
			hours[h] = Hour{Hour: h}
			if v, ok := b.hours[name][h]; ok {
				hours[h] = v
			}
		}
		r.Hours[name] = hours
	}
	for _, n := range Ranges {
		rg := b.rng(n)
		for name := range b.sites {
			s := rg.Sites[name]
			if s == nil {
				s = &SiteRange{Top: map[string][]Item{}}
				rg.Sites[name] = s
			}
			for k, items := range s.Top {
				s.Top[k] = sortItems(items, topN)
			}
		}
		for _, p := range b.profile[n] {
			p.TopPaths = sortItems(p.TopPaths, ipTopN)
			p.Sites = sortItems(p.Sites, ipTopN)
			rg.IPs = append(rg.IPs, *p)
		}
		sort.Slice(rg.IPs, func(i, j int) bool {
			if rg.IPs[i].Requests != rg.IPs[j].Requests {
				return rg.IPs[i].Requests > rg.IPs[j].Requests
			}
			return rg.IPs[i].IP < rg.IPs[j].IP
		})
		// Visitors per site, capped as the script caps them.
		for site, m := range r.visits[n] {
			items := make([]Item, 0, len(m))
			for ip, pv := range m {
				items = append(items, Item{Value: ip, Count: pv})
			}
			items = sortItems(items, visitorsN)
			capped := make(map[string]int64, len(items))
			for _, it := range items {
				capped[it.Value] = it.Count
			}
			r.visits[n][site] = capped
		}
	}
	for _, s := range b.sites {
		r.Sites = append(r.Sites, *s)
	}
	month := b.rng(30)
	pv := func(name string) int64 {
		if s := month.Sites[name]; s != nil {
			return s.Total.PV
		}
		return 0
	}
	sort.Slice(r.Sites, func(i, j int) bool {
		a, c := r.Sites[i], r.Sites[j]
		if (a.Name == All) != (c.Name == All) {
			return a.Name == All
		}
		if pv(a.Name) != pv(c.Name) {
			return pv(a.Name) > pv(c.Name)
		}
		return a.Name < c.Name
	})
	if r.Sites == nil {
		r.Sites = []SiteInfo{}
	}
	return r
}

// Parse reads the output of scripts/sitelogs.sh.
func Parse(out string) Report {
	b := newBuilder("server")
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "N":
			if len(f) >= 4 {
				b.r.Today, b.r.Zone = f[1], f[3]
			}
		case "E":
			b.r.Problem = f[1]
		case "F":
			if len(f) >= 4 {
				s := b.site(f[1])
				s.Files = append(s.Files, File{Path: f[2], Size: num(f[3])})
			}
		case "V":
			if len(f) >= 5 {
				s := b.site(f[1])
				s.Lines, s.Forwarded, s.Unparsed = num(f[2]), num(f[3]), num(f[4])
			}
		case "D":
			if len(f) >= 11 {
				if b.days[f[1]] == nil {
					b.days[f[1]] = map[string]Counts{}
				}
				b.days[f[1]][f[2]] = counts(f[3:])
				b.site(f[1])
			}
		case "H":
			if len(f) >= 5 {
				h := int(num(f[2]))
				if b.hours[f[1]] == nil {
					b.hours[f[1]] = map[int]Hour{}
				}
				b.hours[f[1]][h] = Hour{Hour: h, Requests: num(f[3]), PV: num(f[4])}
			}
		case "S":
			if len(f) >= 11 {
				b.siteRange(int(num(f[1])), f[2]).Total = counts(f[3:])
			}
		case "T":
			if len(f) >= 6 {
				s := b.siteRange(int(num(f[1])), f[2])
				s.Top[f[3]] = append(s.Top[f[3]], Item{Value: f[5], Count: num(f[4])})
			}
		case "I":
			if len(f) >= 18 {
				p := b.ip(int(num(f[1])), f[2])
				p.Requests, p.PV, p.E4xx, p.E5xx, p.Posts, p.Paths = num(f[3]), num(f[4]), num(f[5]), num(f[6]), num(f[7]), num(f[8])
				p.Sensitive, p.Inject, p.Login, p.PeakMin = num(f[9]), num(f[10]), num(f[11]), num(f[12])
				p.First, p.Last, p.Bot, p.Direct, p.UA = f[13], f[14], f[15], num(f[16]), strings.Join(f[17:], "\t")
			}
		case "P":
			if len(f) >= 6 {
				p := b.ip(int(num(f[1])), f[2])
				it := Item{Value: f[5], Count: num(f[4])}
				if f[3] == "site" {
					p.Sites = append(p.Sites, it)
				} else {
					p.TopPaths = append(p.TopPaths, it)
				}
			}
		case "A":
			if len(f) >= 6 {
				b.visitor(int(num(f[1])), f[2], f[5], num(f[4]))
			}
		case "X":
			b.r.Notes = append(b.r.Notes, f[1])
		}
	}
	return b.finish()
}

// dateRange lists the n days up to today, oldest first.
func dateRange(today string, n int) []string {
	t, err := time.Parse("2006-01-02", today)
	if err != nil || n <= 0 {
		return nil
	}
	out := make([]string, 0, n)
	for i := n - 1; i >= 0; i-- {
		out = append(out, t.AddDate(0, 0, -i).Format("2006-01-02"))
	}
	return out
}
