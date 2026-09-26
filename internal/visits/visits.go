// Package visits reads the output of scripts/sitelogs.sh: website visits
// (PV, UV, IPs, pages, referrers, ...) counted from access logs on a server.
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

// Counts is what happened on one day, or over the whole range.
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

// Day is one day's counts; Date is the server's local date.
type Day struct {
	Date string `json:"date"`
	Counts
}

// Hour is one hour of the most recent day.
type Hour struct {
	Hour     int   `json:"hour"`
	Requests int64 `json:"requests"`
	PV       int64 `json:"pv"`
}

// Item is one row of a ranking.
type Item struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// File is a log file that was read.
type File struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Rankings kept for each site.
var Kinds = []string{"page", "referer", "ip", "status", "bot", "device"}

// Site is one website's visits, or all of them (Name == All).
type Site struct {
	Name      string            `json:"name"`
	Total     Counts            `json:"total"`
	Days      []Day             `json:"days"`
	Hours     []Hour            `json:"hours"`
	Top       map[string][]Item `json:"top"`
	Lines     int64             `json:"lines"`     // log lines read
	Forwarded int64             `json:"forwarded"` // lines with X-Forwarded-For
	Unparsed  int64             `json:"unparsed"`
	Files     []File            `json:"files,omitempty"`
}

// Report is one run of sitelogs.sh.
type Report struct {
	Today   string `json:"today"` // the server's date
	Days    int    `json:"days"`
	Zone    string `json:"zone"`
	Sites   []Site `json:"sites"` // All first, then by PV
	Problem string `json:"problem,omitempty"`
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

// Parse reads the script's output. Every day of the range gets an entry
// and the latest day every hour, so charts have no gaps.
func Parse(out string) Report {
	var r Report
	sites := map[string]*Site{}
	get := func(name string) *Site {
		s := sites[name]
		if s == nil {
			s = &Site{Name: name, Top: map[string][]Item{}}
			sites[name] = s
		}
		return s
	}
	days := map[string]map[string]Counts{}
	hours := map[string]map[int]Hour{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "N":
			if len(f) >= 4 {
				r.Today, r.Days, r.Zone = f[1], int(num(f[2])), f[3]
			}
		case "E":
			r.Problem = f[1]
		case "F":
			if len(f) >= 4 {
				s := get(f[1])
				s.Files = append(s.Files, File{Path: f[2], Size: num(f[3])})
			}
		case "D":
			if len(f) >= 11 {
				if days[f[1]] == nil {
					days[f[1]] = map[string]Counts{}
				}
				days[f[1]][f[2]] = counts(f[3:])
				get(f[1])
			}
		case "S":
			if len(f) >= 10 {
				get(f[1]).Total = counts(f[2:])
			}
		case "H":
			if len(f) >= 5 {
				h := int(num(f[2]))
				if hours[f[1]] == nil {
					hours[f[1]] = map[int]Hour{}
				}
				hours[f[1]][h] = Hour{Hour: h, Requests: num(f[3]), PV: num(f[4])}
			}
		case "T":
			if len(f) >= 5 {
				s := get(f[1])
				s.Top[f[2]] = append(s.Top[f[2]], Item{Value: f[4], Count: num(f[3])})
			}
		case "V":
			if len(f) >= 5 {
				s := get(f[1])
				s.Lines, s.Forwarded, s.Unparsed = num(f[2]), num(f[3]), num(f[4])
			}
		}
	}
	// The all-sites entry reads no files and lines itself.
	if all := sites[All]; all != nil {
		for name, s := range sites {
			if name != All {
				all.Lines += s.Lines
				all.Forwarded += s.Forwarded
				all.Unparsed += s.Unparsed
			}
		}
	}
	dates := dateRange(r.Today, r.Days)
	for name, s := range sites {
		for _, d := range dates {
			s.Days = append(s.Days, Day{Date: d, Counts: days[name][d]})
		}
		if len(dates) == 0 { // no N line: the days that had visits
			for d, c := range days[name] {
				s.Days = append(s.Days, Day{Date: d, Counts: c})
			}
			sort.Slice(s.Days, func(i, j int) bool { return s.Days[i].Date < s.Days[j].Date })
		}
		for h := 0; h < 24; h++ {
			v, ok := hours[name][h]
			if !ok {
				v = Hour{Hour: h}
			}
			s.Hours = append(s.Hours, v)
		}
		for _, k := range Kinds {
			sort.SliceStable(s.Top[k], func(i, j int) bool { return s.Top[k][i].Count > s.Top[k][j].Count })
		}
		r.Sites = append(r.Sites, *s)
	}
	sort.Slice(r.Sites, func(i, j int) bool {
		a, b := r.Sites[i], r.Sites[j]
		if (a.Name == All) != (b.Name == All) {
			return a.Name == All
		}
		if a.Total.PV != b.Total.PV {
			return a.Total.PV > b.Total.PV
		}
		return a.Name < b.Name
	})
	return r
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

// Site returns the named site's entry.
func (r Report) Site(name string) (Site, bool) {
	for _, s := range r.Sites {
		if s.Name == name {
			return s, true
		}
	}
	return Site{}, false
}
