package visits

import (
	"net"
	"strings"
	"sync"
)

// The address ranges Google and Bing publish for their crawlers. An IP in
// them is the real crawler without asking DNS, which matters in mainland
// China, where answers for googlebot.com are often forged and a real
// Googlebot would otherwise look like an impostor.
//
// From developers.google.com/search/apis/ipranges/googlebot.json and
// www.bing.com/toolbox/bingbot.json (merged by github.com/lord-alfred/ipranges),
// taken 2026-09-26. Ranges added later fall back to the DNS check.
var crawlerRanges = map[string]string{
	"Googlebot": `
34.22.85.0/27 34.64.82.64/28 34.65.242.112/28 34.80.50.80/28 34.88.194.0/28 34.89.10.80/28
34.89.198.80/28 34.96.162.48/28 34.100.182.96/28 34.101.50.144/28 34.118.66.0/28
34.118.254.0/28 34.126.178.96/28 34.146.150.144/28 34.147.110.144/28 34.151.74.144/28
34.152.50.64/28 34.154.114.144/28 34.155.98.32/28 34.165.18.176/28 34.175.160.64/28
34.176.130.16/28 35.247.243.240/28 66.249.64.0/23 66.249.66.0/24 66.249.67.0/26 66.249.67.64/27
66.249.68.0/25 66.249.68.128/26 66.249.68.192/27 66.249.69.0/24 66.249.70.0/23 66.249.72.0/22
66.249.76.0/23 66.249.78.0/24 66.249.79.0/26 66.249.79.64/27 66.249.79.128/25 192.178.4.0/24
192.178.5.0/27 192.178.6.0/23 2001:4860:4801:2::/64 2001:4860:4801:4c::/63
2001:4860:4801:4e::/64 2001:4860:4801:10::/60 2001:4860:4801:20::/59 2001:4860:4801:40::/63
2001:4860:4801:42::/64 2001:4860:4801:44::/62 2001:4860:4801:48::/62 2001:4860:4801:50::/61
2001:4860:4801:58::/63 2001:4860:4801:60::/59 2001:4860:4801:80::/61 2001:4860:4801:88::/64
2001:4860:4801:90::/61 2001:4860:4801:a0::/61 2001:4860:4801:a8::/62 2001:4860:4801:ac::/63
2001:4860:4801:ae::/64 2001:4860:4801:b0::/62 2001:4860:4801:b4::/63 2001:4860:4801:b6::/64
2001:4860:4801:c::/64 2001:4860:4801:f::/64
`,
	"Bingbot": `
13.66.139.0/24 13.66.144.0/24 13.67.10.16/28 13.69.66.240/28 13.71.172.224/28 20.15.133.160/27
20.36.108.32/28 20.43.120.16/28 20.74.197.0/28 20.79.107.240/28 20.125.163.80/28 40.77.139.0/25
40.77.167.0/24 40.77.177.0/24 40.77.178.0/23 40.77.188.0/22 40.77.202.0/24 40.79.131.208/28
40.79.186.176/28 51.105.67.0/28 52.167.144.0/24 52.231.148.0/28 65.55.210.0/24 139.217.52.0/28
157.55.39.0/24 191.233.204.224/28 199.30.24.0/23 207.46.13.0/24
`,
}

var (
	crawlerNetsOnce sync.Once
	crawlerNets     map[string][]*net.IPNet
)

// PublishedCrawler says whether ip is in the published ranges of the
// named crawler; known is false when that crawler publishes none.
func PublishedCrawler(name, ip string) (in, known bool) {
	crawlerNetsOnce.Do(func() {
		crawlerNets = map[string][]*net.IPNet{}
		for n, list := range crawlerRanges {
			for _, c := range strings.Fields(list) {
				if _, nw, err := net.ParseCIDR(c); err == nil {
					crawlerNets[n] = append(crawlerNets[n], nw)
				}
			}
		}
	})
	nets, known := crawlerNets[name]
	addr := net.ParseIP(ip)
	if !known || addr == nil {
		return false, known
	}
	for _, nw := range nets {
		if nw.Contains(addr) {
			return true, true
		}
	}
	return false, true
}
