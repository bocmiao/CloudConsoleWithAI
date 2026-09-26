package tencent

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

// The expected values come from the official Go SDK
// (cos-go-sdk-v5 v0.7.75, AddAuthorizationHeader) for the same requests.
func TestCOSSignatureMatchesSDK(t *testing.T) {
	ak, sk := "AKIDexampleexampleexample00", "exampleSecretKey000000000000000"
	start := time.Unix(1790000000, 0)
	end := start.Add(time.Hour)
	host := "blog-1250000000.cos.ap-guangzhou.myqcloud.com"

	h := http.Header{}
	h.Set("Host", host)
	h.Set("Content-Type", "image/png")
	h.Set("x-cos-acl", "private")
	h.Set("X-Cos-Meta-Note", "a b")
	h.Set("User-Agent", "not signed")
	got := COSAuthorization(ak, sk, "PUT", "/img/头像 (1)+x!.png", nil, h, start, end)
	want := "q-sign-algorithm=sha1&q-ak=AKIDexampleexampleexample00&q-sign-time=1790000000;1790003600&q-key-time=1790000000;1790003600" +
		"&q-header-list=content-type;host;x-cos-acl;x-cos-meta-note&q-url-param-list=&q-signature=4f9c1fe51dcdaed5787cfd68b7c112ae9ab69be9"
	if got != want {
		t.Errorf("object upload:\n got %s\nwant %s", got, want)
	}

	h = http.Header{}
	h.Set("Host", host)
	q := url.Values{"prefix": {"backup/2026 09/"}, "delimiter": {"/"}, "max-keys": {"200"}, "encoding-type": {"url"}, "marker": {"backup/a*b"}}
	got = COSAuthorization(ak, sk, "GET", "/", q, h, start, end)
	want = "q-sign-algorithm=sha1&q-ak=AKIDexampleexampleexample00&q-sign-time=1790000000;1790003600&q-key-time=1790000000;1790003600" +
		"&q-header-list=host&q-url-param-list=delimiter;encoding-type;marker;max-keys;prefix&q-signature=54b62456dd4c2c58b06f1491e00a7d663afb480c"
	if got != want {
		t.Errorf("listing:\n got %s\nwant %s", got, want)
	}
}

func TestCOSURLs(t *testing.T) {
	c := New("AKIDexampleexampleexample00", "sk")
	u, host, path := c.cosURL("blog-1250000000", "ap-guangzhou", "img/头像 (1)+x.png")
	if host != "blog-1250000000.cos.ap-guangzhou.myqcloud.com" || path != "/img/头像 (1)+x.png" ||
		u.String() != "https://blog-1250000000.cos.ap-guangzhou.myqcloud.com/img/%E5%A4%B4%E5%83%8F%20(1)%2Bx.png" {
		t.Fatalf("url %s host %s path %s", u, host, path)
	}
	if q := cosQuery(url.Values{"acl": {""}}); q != "acl" {
		t.Fatalf("subresource %q", q)
	}
	c.now = func() time.Time { return time.Unix(1790000000, 0) }
	link := c.PresignedURL("blog-1250000000", "ap-guangzhou", "a b.zip", time.Hour, true)
	pu, err := url.Parse(link)
	if err != nil || pu.Path != "/a b.zip" || pu.Query().Get("q-sign-time") != "1789999940;1790003600" ||
		pu.Query().Get("response-content-disposition") != "attachment; filename*=UTF-8''a%20b.zip" || pu.Query().Get("q-signature") == "" {
		t.Fatalf("presigned %s", link)
	}
}
