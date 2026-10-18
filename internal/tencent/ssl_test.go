package tencent

import "testing"

func TestSSLCertNamesAndCovers(t *testing.T) {
	c := SSLCert{ID: "cert-1", Domain: "*.miao.club", SANs: []string{"*.miao.club", "miao.club"}}
	if names := c.Names(); len(names) != 2 || names[0] != "*.miao.club" || names[1] != "miao.club" {
		t.Fatalf("names = %v", names)
	}
	for _, host := range []string{"blog.miao.club", "miao.club", "BLOG.Miao.Club"} {
		if !c.Covers(host) {
			t.Errorf("%s not covered", host)
		}
	}
	for _, host := range []string{"a.b.miao.club", "miao.club.example.com", "other.com", ""} {
		if c.Covers(host) {
			t.Errorf("%s covered", host)
		}
	}
}
