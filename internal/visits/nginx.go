package visits

import (
	"strconv"
	"strings"
	"time"
)

// AddNginxLine counts one line of an Nginx access log in the main or
// combined format (with X-Forwarded-For last), reading it exactly the way
// sitelogs.sh does.
func (c *Counter) AddNginxLine(site, line string) {
	b := strings.IndexByte(line, '[')
	if b < 0 {
		c.Unparsed(site)
		return
	}
	ts := line[b+1:]
	if len(ts) > 20 {
		ts = ts[:20]
	}
	t, err := time.ParseInLocation("02/Jan/2006:15:04:05", ts, c.today.Location())
	y, m, d := t.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, c.today.Location())
	if err != nil || day.After(c.today) || int(c.today.Sub(day).Hours()+0.5)/24 >= historyDays {
		c.b.site(site).Lines++ // outside the 30 days
		return
	}
	q := strings.Split(line, `"`)
	if len(q) < 7 {
		c.Unparsed(site)
		return
	}
	field := func(s string, i int) string {
		f := strings.Fields(s)
		if i < len(f) {
			return f[i]
		}
		return ""
	}
	ip := field(q[0], 0)
	method, target := field(q[1], 0), field(q[1], 1)
	status, bytes := awkNum(field(q[2], 0)), awkNum(field(q[2], 1))
	forwarded := false
	if len(q) >= 9 && q[7] != "-" && q[7] != "" {
		first, _, _ := strings.Cut(q[7], ",")
		if first = strings.ReplaceAll(first, " ", ""); first != "" {
			ip, forwarded = first, true
		}
	}
	c.Add(site, t, ip, method, target, int(status), bytes, q[3], q[5], forwarded)
}

// awkNum reads a number the way awk does: its leading digits, or 0.
func awkNum(s string) int64 {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	n, _ := strconv.ParseInt(s[:i], 10, 64)
	return n
}
