package visits

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/bocmiao/CloudConsoleWithAI/internal/geoip"
)

// Locate says where IPs are: each described IP's place and network, the
// place next to each IP in the rankings, and visitors by region and by
// network (distinct visitor IPs), from the offline IP database.
func (r *Report) Locate() {
	where := func(ip string) (geoip.Location, bool) { return geoip.Lookup(ip) }
	for key, rg := range r.Ranges {
		for i := range rg.IPs {
			p := &rg.IPs[i]
			if l, ok := where(p.IP); ok {
				p.Place, p.ISP = l.Place(), l.ISP
			} else if a, err := netip.ParseAddr(p.IP); err == nil && a.Is6() && !a.Is4In6() {
				p.Place = "IPv6"
			}
		}
		for _, s := range rg.Sites {
			for i := range s.Top["ip"] {
				if l, ok := where(s.Top["ip"][i].Value); ok {
					s.Top["ip"][i].Note = strings.TrimSpace(l.Place() + " " + l.ISP)
				}
			}
		}
		n := rg.Days
		visitors := r.visits[n]
		if visitors == nil {
			continue
		}
		// All sites: each visitor once.
		all := map[string]int64{}
		for _, m := range visitors {
			for ip, pv := range m {
				all[ip] += pv
			}
		}
		regions := func(m map[string]int64) (region, isp []Item) {
			byRegion, byISP := map[string]int64{}, map[string]int64{}
			for ip := range m {
				l, ok := where(ip)
				switch {
				case ok:
					byRegion[l.Region()]++
					if l.ISP != "" {
						byISP[l.ISP]++
					}
				case strings.Contains(ip, ":"):
					byRegion["IPv6（未知）"]++
				default:
					byRegion["未知"]++
				}
			}
			for k, v := range byRegion {
				region = append(region, Item{Value: k, Count: v})
			}
			for k, v := range byISP {
				isp = append(isp, Item{Value: k, Count: v})
			}
			return sortItems(region, topN), sortItems(isp, topN)
		}
		for site, s := range rg.Sites {
			m := visitors[site]
			if site == All {
				m = all
			}
			if len(m) > 0 {
				s.Top["region"], s.Top["isp"] = regions(m)
			}
		}
		_ = key
	}
}

// Facts are what Miao Panel found out about IPs beyond the logs.
type Facts struct {
	EdgeOne     bool   // one of EdgeOne's nodes
	Crawler     string // a search engine crawler whose reverse DNS checked out
	FakeCrawler bool   // claims to be a search engine crawler but is not
}

// Risk levels.
const (
	RiskHigh   = "high"
	RiskMedium = "medium"
	RiskLow    = "low"
	RiskNone   = "none"
)

// Assess rates each described IP from what it did, with the reasons in
// words, and puts the riskiest first.
func (r *Report) Assess(facts map[string]Facts) {
	for _, rg := range r.Ranges {
		for i := range rg.IPs {
			p := &rg.IPs[i]
			f := facts[p.IP]
			p.EdgeOne, p.Crawler, p.FakeCrawler = f.EdgeOne, f.Crawler, f.FakeCrawler
			p.Score, p.Reasons = score(p)
			switch {
			case p.EdgeOne:
				p.Score, p.Reasons = 0, []string{"EdgeOne 的节点：服务器日志没有记下真实访客 IP，不要封禁"}
			case Private(p.IP):
				p.Score, p.Reasons = 0, []string{"内网或本机地址"}
			case p.Crawler != "" && p.Inject == 0:
				p.Score = min(p.Score, 5)
				p.Reasons = append([]string{"已验证的搜索引擎爬虫（" + p.Crawler + "）"}, p.Reasons...)
			}
			switch {
			case p.Score >= 60:
				p.Risk = RiskHigh
			case p.Score >= 30:
				p.Risk = RiskMedium
			case p.Score >= 10:
				p.Risk = RiskLow
			default:
				p.Risk = RiskNone
			}
		}
		sort.SliceStable(rg.IPs, func(i, j int) bool {
			if rg.IPs[i].Score != rg.IPs[j].Score {
				return rg.IPs[i].Score > rg.IPs[j].Score
			}
			return rg.IPs[i].Requests > rg.IPs[j].Requests
		})
	}
}

// Private reports whether ip is a private, loopback or link-local address.
func Private(ip string) bool {
	a, err := netip.ParseAddr(ip)
	return err == nil && (a.IsPrivate() || a.IsLoopback() || a.IsUnspecified() || a.IsLinkLocalUnicast())
}

// score adds up the signs of trouble.
func score(p *IPProfile) (int, []string) {
	s := 0
	var why []string
	add := func(points int, format string, args ...any) {
		s += points
		why = append(why, fmt.Sprintf(format, args...))
	}
	if p.Inject > 0 {
		add(60, "请求里带攻击代码 %d 次（目录穿越、SQL 注入、XSS 等）", p.Inject)
	}
	switch {
	case p.Sensitive >= 3:
		add(50, "探测后台、备份和密钥文件 %d 次（如 /.env、/wp-login.php）", p.Sensitive)
	case p.Sensitive > 0:
		add(15, "探测敏感路径 %d 次", p.Sensitive)
	}
	switch {
	case p.Login >= 20:
		add(50, "登录失败 %d 次，像在猜密码", p.Login)
	case p.Login >= 5:
		add(30, "登录失败 %d 次", p.Login)
	}
	switch {
	case p.PeakMin >= 300:
		add(50, "一分钟最多 %d 次请求，像 CC 攻击或刷量", p.PeakMin)
	case p.PeakMin >= 120:
		add(30, "一分钟最多 %d 次请求，频率很高", p.PeakMin)
	case p.PeakMin >= 60:
		add(15, "一分钟最多 %d 次请求", p.PeakMin)
	}
	if p.Requests >= 30 && p.E4xx*2 >= p.Requests {
		add(30, "%d%% 的请求是 404/403，像在扫描", p.E4xx*100/p.Requests)
	}
	if p.Paths >= 200 && p.PV*10 < p.Paths {
		add(20, "访问了 %d 个不同地址，像在爬取或扫描", p.Paths)
	}
	if p.FakeCrawler {
		add(40, "自称%s，但 IP 不属于这个搜索引擎（冒充爬虫）", crawlerClaim(p.UA))
	}
	if p.Bot != "" && p.Crawler == "" && !p.FakeCrawler && p.Requests >= 200 {
		add(10, "程序（%s）大量访问 %d 次", p.Bot, p.Requests)
	}
	return s, why
}

// Crawlers that can be checked by reverse DNS: the UA word, and the host
// names their addresses resolve to.
var Crawlers = []struct {
	Name, UA string
	Hosts    []string
}{
	{"Googlebot", "googlebot", []string{".googlebot.com", ".google.com", ".googleusercontent.com"}},
	{"Bingbot", "bingbot", []string{".search.msn.com"}},
	{"百度蜘蛛", "baiduspider", []string{".baidu.com", ".baidu.jp"}},
	{"Yandex", "yandex", []string{".yandex.ru", ".yandex.net", ".yandex.com"}},
	{"搜狗蜘蛛", "sogou", []string{".sogou.com"}},
	{"Applebot", "applebot", []string{".applebot.apple.com"}},
}

// ClaimedCrawler returns which checkable crawler a UA says it is.
func ClaimedCrawler(ua string) (name string, hosts []string, ok bool) {
	lua := lower(ua)
	for _, c := range Crawlers {
		if strings.Contains(lua, c.UA) {
			return c.Name, c.Hosts, true
		}
	}
	return "", nil, false
}

func crawlerClaim(ua string) string {
	if name, _, ok := ClaimedCrawler(ua); ok {
		return name
	}
	return "搜索引擎"
}
