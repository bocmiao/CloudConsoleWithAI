package geoip

import "testing"

func TestLookup(t *testing.T) {
	cases := []struct{ ip, place, region, isp string }{
		{"61.135.211.75", "北京", "北京", "联通"},
		{"223.5.5.5", "浙江 杭州", "浙江", "阿里"},
		{"43.140.247.223", "香港", "香港", ""},
		{"8.8.8.8", "美国", "美国", "Google LLC"},
		{"1.1.1.1", "澳大利亚", "澳大利亚", ""},
		{"2400:3200::1", "浙江 杭州", "浙江", "阿里"},
		{"240e:3a1:4b3c::1", "北京", "北京", "电信"},
		{"2408:8000::1", "北京", "北京", "联通"},
		{"2001:4860:4860::8888", "美国", "美国", "Google LLC"},
		{"::ffff:61.135.211.75", "北京", "北京", "联通"},
	}
	for _, c := range cases {
		l, ok := Lookup(c.ip)
		if !ok || l.Place() != c.place || l.Region() != c.region || l.ISP != c.isp {
			t.Errorf("%s = %+v (place %q region %q)", c.ip, l, l.Place(), l.Region())
		}
	}
	for _, ip := range []string{"10.0.0.1", "127.0.0.1", "192.168.1.1", "::1", "fd00::1", "fe80::1", "ff02::1", "nonsense", ""} {
		if l, ok := Lookup(ip); ok {
			t.Errorf("%s found: %+v", ip, l)
		}
	}
}
