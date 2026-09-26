package tencent_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent/tencenttest"
)

// Every COS call the app makes, against the fake (which checks each
// signature the way COS does).
func TestCOSAgainstFake(t *testing.T) {
	f := tencenttest.Start(t)
	c := f.Client()
	ctx := context.Background()
	name := "blog-" + tencenttest.COSAppID

	if err := c.CreateBucket(ctx, name, "ap-guangzhou", "private"); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateBucket(ctx, name, "ap-guangzhou", "private"); !tencent.IsCOSCode(err, "BucketAlreadyOwnedByYou") || !strings.Contains(err.Error(), "换一个名字") {
		t.Fatalf("second create: %v", err)
	}
	bs, err := c.Buckets(ctx)
	if err != nil || len(bs) != 1 || bs[0].Name != name || bs[0].Region != "ap-guangzhou" {
		t.Fatalf("buckets %+v %v", bs, err)
	}

	// Access: public read, a grant to another account, then back.
	if a, err := c.BucketACL(ctx, name, "ap-guangzhou"); err != nil || a.Canned != "private" {
		t.Fatalf("acl %+v %v", a, err)
	}
	grant := tencent.ACL{Canned: "public-read", Grants: map[string]string{"x-cos-grant-read": `id="qcs::cam::uin/100000000002:uin/100000000002"`}}
	if err := c.SetBucketACL(ctx, name, "ap-guangzhou", grant); err != nil {
		t.Fatal(err)
	}
	if a, _ := c.BucketACL(ctx, name, "ap-guangzhou"); a.Canned != "public-read" || a.Grants["x-cos-grant-read"] != grant.Grants["x-cos-grant-read"] {
		t.Fatalf("acl after = %+v", a)
	}

	// Settings never made read as off.
	if r, err := c.BucketReferer(ctx, name, "ap-guangzhou"); err != nil || r.Status != "Disabled" {
		t.Fatalf("referer %+v %v", r, err)
	}
	if rules, err := c.BucketCORS(ctx, name, "ap-guangzhou"); err != nil || len(rules) != 0 {
		t.Fatalf("cors %+v %v", rules, err)
	}
	if raw, rules, err := c.BucketLifecycle(ctx, name, "ap-guangzhou"); err != nil || raw != "" || len(rules) != 0 {
		t.Fatalf("lifecycle %q %+v %v", raw, rules, err)
	}
	if v, err := c.BucketVersioning(ctx, name, "ap-guangzhou"); err != nil || v != "" {
		t.Fatalf("versioning %q %v", v, err)
	}
	if p, err := c.BucketPolicy(ctx, name, "ap-guangzhou"); err != nil || p != "" {
		t.Fatalf("policy %q %v", p, err)
	}

	ref := tencent.Referer{Status: "Enabled", Type: "White-List", Domains: []string{"example.com", "*.example.com"}, EmptyRefer: "Allow"}
	if err := c.SetBucketReferer(ctx, name, "ap-guangzhou", ref); err != nil {
		t.Fatal(err)
	}
	if r, _ := c.BucketReferer(ctx, name, "ap-guangzhou"); r.Status != "Enabled" || len(r.Domains) != 2 || r.EmptyRefer != "Allow" {
		t.Fatalf("referer after = %+v", r)
	}
	cors := []tencent.CORSRule{{Origins: []string{"https://example.com"}, Methods: []string{"GET", "PUT"}, Headers: []string{"*"}, MaxAge: 600}}
	if err := c.SetBucketCORS(ctx, name, "ap-guangzhou", cors); err != nil {
		t.Fatal(err)
	}
	if rules, _ := c.BucketCORS(ctx, name, "ap-guangzhou"); len(rules) != 1 || rules[0].MaxAge != 600 || rules[0].Methods[1] != "PUT" {
		t.Fatalf("cors after = %+v", rules)
	}
	if err := c.SetBucketCORS(ctx, name, "ap-guangzhou", nil); err != nil {
		t.Fatal(err)
	}
	life := `<LifecycleConfiguration><Rule><ID>backup-30d</ID><Status>Enabled</Status><Filter><Prefix>backup/</Prefix></Filter><Expiration><Days>30</Days></Expiration></Rule></LifecycleConfiguration>`
	if err := c.SetBucketLifecycleXML(ctx, name, "ap-guangzhou", life); err != nil {
		t.Fatal(err)
	}
	if raw, rules, err := c.BucketLifecycle(ctx, name, "ap-guangzhou"); err != nil || raw != life || len(rules) != 1 || rules[0].Expiration.Days != 30 || rules[0].Filter.Prefix != "backup/" {
		t.Fatalf("lifecycle after %q %+v %v", raw, rules, err)
	}
	if err := c.SetBucketVersioning(ctx, name, "ap-guangzhou", "Enabled"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetBucketEncryption(ctx, name, "ap-guangzhou", "AES256"); err != nil {
		t.Fatal(err)
	}
	if e, _ := c.BucketEncryption(ctx, name, "ap-guangzhou"); e != "AES256" {
		t.Fatalf("encryption %q", e)
	}
	if err := c.SetBucketWebsite(ctx, name, "ap-guangzhou", tencent.Website{Enabled: true, Index: "index.html", Error: "404.html", HTTPS: true}); err != nil {
		t.Fatal(err)
	}
	if w, _ := c.BucketWebsite(ctx, name, "ap-guangzhou"); !w.Enabled || w.Index != "index.html" || w.Error != "404.html" || !w.HTTPS {
		t.Fatalf("website %+v", w)
	}
	pol := `{"Statement":[{"Principal":{"qcs":["qcs::cam::anyone:anyone"]},"Effect":"Allow","Action":["name/cos:GetObject"],"Resource":["qcs::cos:ap-guangzhou:uid/1250000000:blog-1250000000/*"]}],"version":"2.0"}`
	if err := c.SetBucketPolicy(ctx, name, "ap-guangzhou", pol); err != nil {
		t.Fatal(err)
	}
	if p, _ := c.BucketPolicy(ctx, name, "ap-guangzhou"); p != pol {
		t.Fatalf("policy %q", p)
	}

	// Files, with names that need encoding.
	key := "图片/头像 (1)+x.png"
	if err := c.PutObject(ctx, name, "ap-guangzhou", key, bytes.NewReader([]byte("pngdata")), 7, "image/png"); err != nil {
		t.Fatal(err)
	}
	_ = c.PutObject(ctx, name, "ap-guangzhou", "图片/", nil, 0, "")
	for i := 0; i < 5; i++ {
		_ = c.PutObject(ctx, name, "ap-guangzhou", "logs/"+string(rune('a'+i))+".log", strings.NewReader("x"), 1, "text/plain")
	}
	l, err := c.ListObjects(ctx, name, "ap-guangzhou", "", "/", "", 100)
	if err != nil || len(l.Folders) != 2 || l.Folders[0] != "logs/" || l.Folders[1] != "图片/" || len(l.Objects) != 0 {
		t.Fatalf("root listing %+v %v", l, err)
	}
	l, _ = c.ListObjects(ctx, name, "ap-guangzhou", "图片/", "/", "", 100)
	if len(l.Objects) != 2 || l.Objects[1].Key != key || l.Objects[1].Size != 7 {
		t.Fatalf("folder listing %+v", l)
	}
	l, _ = c.ListObjects(ctx, name, "ap-guangzhou", "logs/", "/", "", 2)
	if len(l.Objects) != 2 || l.NextMarker != "logs/b.log" {
		t.Fatalf("first page %+v", l)
	}
	l, _ = c.ListObjects(ctx, name, "ap-guangzhou", "logs/", "/", l.NextMarker, 2)
	if len(l.Objects) != 2 || l.Objects[0].Key != "logs/c.log" {
		t.Fatalf("second page %+v", l)
	}
	if err := c.CopyObject(ctx, name, "ap-guangzhou", key, "头像.png"); err != nil {
		t.Fatal(err)
	}
	if size, ct, ok, err := c.HeadObject(ctx, name, "ap-guangzhou", "头像.png"); err != nil || !ok || size != 7 || ct != "image/png" {
		t.Fatalf("head copy %d %q %v %v", size, ct, ok, err)
	}
	if _, _, ok, err := c.HeadObject(ctx, name, "ap-guangzhou", "nope"); ok || err != nil {
		t.Fatalf("head missing %v %v", ok, err)
	}

	// A presigned link works without credentials, for a while.
	link := c.PresignedURL(name, "ap-guangzhou", key, time.Minute, true)
	req, _ := http.NewRequest("GET", link, nil)
	req.Host = tencent.COSHost(name, "ap-guangzhou")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(data) != "pngdata" || !strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("presigned %d %q %v", resp.StatusCode, data, resp.Header)
	}

	failed, err := c.DeleteObjects(ctx, name, "ap-guangzhou", []string{"logs/a.log", "logs/b.log", "gone.txt"})
	if err != nil || len(failed) != 0 {
		t.Fatalf("delete many %v %v", failed, err)
	}
	if err := c.DeleteObject(ctx, name, "ap-guangzhou", "logs/c.log"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteBucket(ctx, name, "ap-guangzhou"); !tencent.IsCOSCode(err, "BucketNotEmpty") {
		t.Fatalf("delete non-empty bucket: %v", err)
	}
	if got := len(f.Bucket(name).Objects); got != 5 {
		t.Fatalf("objects left %d", got)
	}

	// Wrong credentials are refused.
	bad := tencent.New(tencenttest.SecretID, "wrong")
	bad.Endpoint = f.Endpoint
	if _, err := bad.Buckets(ctx); !tencent.IsCOSCode(err, "SignatureDoesNotMatch") {
		t.Fatalf("bad key: %v", err)
	}
}
