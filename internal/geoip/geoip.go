// Package geoip says where an IPv4 address is, offline, from the
// ip2region database (github.com/lionsoul2014/ip2region, Apache-2.0 or
// MIT, see LICENSE-ip2region.md), embedded compressed and read into
// memory on first use.
package geoip

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/binary"
	"io"
	"net/netip"
	"strings"
	"sync"
)

//go:embed ip2region_v4.xdb.gz
var packed []byte

// Location is where an address is registered.
type Location struct {
	Country  string `json:"country"`            // 中国, 美国, ...
	Province string `json:"province,omitempty"` // 广东, 香港, California
	City     string `json:"city,omitempty"`
	ISP      string `json:"isp,omitempty"` // 电信, 联通, 腾讯, Google LLC, ...
	Code     string `json:"code,omitempty"`
}

// Place is the location in a few words: 广东 深圳, 香港, 美国.
func (l Location) Place() string {
	if l.Code == "CN" {
		parts := []string{}
		for _, p := range []string{l.Province, l.City} {
			if p != "" && (len(parts) == 0 || parts[len(parts)-1] != p) {
				parts = append(parts, p)
			}
		}
		if len(parts) == 0 {
			return "中国"
		}
		return strings.Join(parts, " ")
	}
	return l.Country
}

// Region is what visitors are counted by: provinces in China (with Hong
// Kong, Macau and Taiwan on their own), countries elsewhere.
func (l Location) Region() string {
	if l.Code == "CN" && l.Province != "" {
		return l.Province
	}
	return l.Country
}

var (
	once sync.Once
	db   []byte
)

func load() {
	r, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		return
	}
	data, err := io.ReadAll(r)
	if err != nil || len(data) < 256+256*256*8 {
		return
	}
	db = data
}

const (
	headerLen   = 256
	vectorCols  = 256
	vectorSize  = 8
	segmentSize = 14 // start ip, end ip (little endian), data length, data pointer
)

// Lookup finds an IPv4 address. Private, reserved and IPv6 addresses
// are not found.
func Lookup(ip string) (Location, bool) {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return Location{}, false
	}
	addr = addr.Unmap()
	if !addr.Is4() || addr.IsPrivate() || addr.IsLoopback() || addr.IsUnspecified() || addr.IsLinkLocalUnicast() {
		return Location{}, false
	}
	once.Do(load)
	if db == nil {
		return Location{}, false
	}
	b := addr.As4()
	n := binary.BigEndian.Uint32(b[:])
	idx := headerLen + (int(b[0])*vectorCols+int(b[1]))*vectorSize
	start := binary.LittleEndian.Uint32(db[idx:])
	end := binary.LittleEndian.Uint32(db[idx+4:])
	if start == 0 || end == 0 || int(end)+segmentSize > len(db) {
		return Location{}, false
	}
	lo, hi := 0, int((end-start)/segmentSize)
	for lo <= hi {
		m := (lo + hi) / 2
		p := int(start) + m*segmentSize
		sip := binary.LittleEndian.Uint32(db[p:])
		eip := binary.LittleEndian.Uint32(db[p+4:])
		switch {
		case n < sip:
			hi = m - 1
		case n > eip:
			lo = m + 1
		default:
			l := int(binary.LittleEndian.Uint16(db[p+8:]))
			ptr := int(binary.LittleEndian.Uint32(db[p+10:]))
			if ptr+l > len(db) {
				return Location{}, false
			}
			return parse(string(db[ptr : ptr+l]))
		}
	}
	return Location{}, false
}

// parse reads "中国|广东省|深圳市|电信|CN" or "United States|California|0|Google LLC|US".
func parse(s string) (Location, bool) {
	f := strings.Split(s, "|")
	for len(f) < 5 {
		f = append(f, "0")
	}
	clean := func(v string) string {
		if v == "0" || v == "Reserved" {
			return ""
		}
		return strings.TrimSpace(v)
	}
	l := Location{Country: clean(f[0]), Province: clean(f[1]), City: clean(f[2]), ISP: clean(f[3]), Code: clean(f[4])}
	if l.Country == "" && l.Code == "" {
		return Location{}, false
	}
	if name, ok := countries[l.Code]; ok {
		l.Country = name
	}
	if l.Code == "CN" {
		l.Country = "中国"
		l.Province = shortRegion(l.Province)
		l.City = strings.TrimSuffix(l.City, "市")
	}
	return l, true
}

// shortRegion drops administrative suffixes: 广东省 -> 广东, 广西壮族自治区 -> 广西.
func shortRegion(p string) string {
	for _, suffix := range []string{"特别行政区", "壮族自治区", "回族自治区", "维吾尔自治区", "自治区", "省", "市"} {
		if s, ok := strings.CutSuffix(p, suffix); ok && s != "" {
			return s
		}
	}
	return p
}

// CountryName is the Chinese name of a country or region code (CN, US,
// HK, ...), or "" when not known.
func CountryName(code string) string {
	return countries[strings.ToUpper(strings.TrimSpace(code))]
}

var countries = map[string]string{
	"CN": "中国", "US": "美国", "JP": "日本", "DE": "德国", "KR": "韩国", "GB": "英国", "BR": "巴西", "FR": "法国",
	"CA": "加拿大", "NL": "荷兰", "IT": "意大利", "AU": "澳大利亚", "IN": "印度", "RU": "俄罗斯", "ES": "西班牙",
	"SE": "瑞典", "MX": "墨西哥", "ZA": "南非", "CH": "瑞士", "EG": "埃及", "PL": "波兰", "ID": "印度尼西亚",
	"AR": "阿根廷", "CO": "哥伦比亚", "TR": "土耳其", "SG": "新加坡", "VN": "越南", "NO": "挪威", "IE": "爱尔兰",
	"FI": "芬兰", "PK": "巴基斯坦", "DK": "丹麦", "BE": "比利时", "MA": "摩洛哥", "SA": "沙特阿拉伯", "IR": "伊朗",
	"AT": "奥地利", "TH": "泰国", "CL": "智利", "UA": "乌克兰", "IL": "以色列", "CZ": "捷克", "TN": "突尼斯",
	"MY": "马来西亚", "NZ": "新西兰", "RO": "罗马尼亚", "PT": "葡萄牙", "VE": "委内瑞拉", "KE": "肯尼亚",
	"PH": "菲律宾", "HU": "匈牙利", "GR": "希腊", "AE": "阿联酋", "DZ": "阿尔及利亚", "BG": "保加利亚",
	"KZ": "哈萨克斯坦", "PE": "秘鲁", "NG": "尼日利亚", "BD": "孟加拉国", "LT": "立陶宛", "EC": "厄瓜多尔",
	"SK": "斯洛伐克", "SI": "斯洛文尼亚", "UY": "乌拉圭", "HR": "克罗地亚", "RS": "塞尔维亚", "LV": "拉脱维亚",
	"EE": "爱沙尼亚", "BY": "白俄罗斯", "KW": "科威特", "QA": "卡塔尔", "LU": "卢森堡", "IS": "冰岛",
	"MO": "中国澳门", "HK": "中国香港", "TW": "中国台湾", "MN": "蒙古", "KH": "柬埔寨", "LA": "老挝", "MM": "缅甸",
	"NP": "尼泊尔", "LK": "斯里兰卡", "KP": "朝鲜", "UZ": "乌兹别克斯坦", "KG": "吉尔吉斯斯坦", "GE": "格鲁吉亚",
	"AM": "亚美尼亚", "AZ": "阿塞拜疆", "IQ": "伊拉克", "JO": "约旦", "LB": "黎巴嫩", "SY": "叙利亚", "OM": "阿曼",
	"BH": "巴林", "YE": "也门", "CY": "塞浦路斯", "MT": "马耳他", "MD": "摩尔多瓦", "BA": "波黑", "AL": "阿尔巴尼亚",
	"MK": "北马其顿", "ME": "黑山", "BO": "玻利维亚", "PY": "巴拉圭", "PA": "巴拿马", "CR": "哥斯达黎加",
	"DO": "多米尼加", "CU": "古巴", "GT": "危地马拉", "ET": "埃塞俄比亚", "GH": "加纳", "CM": "喀麦隆",
	"SD": "苏丹", "ZM": "赞比亚", "MU": "毛里求斯", "CI": "科特迪瓦", "TZ": "坦桑尼亚", "UG": "乌干达",
	"AO": "安哥拉", "SN": "塞内加尔", "LY": "利比亚",
}
