package actions

import (
	"context"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent/tencenttest"
)

func TestCOSSettings(t *testing.T) {
	env, f := cloudEnv(t)
	ctx := context.Background()
	name := "blog-" + tencenttest.COSAppID
	where := map[string]any{"bucket": name, "region": "ap-guangzhou"}
	with := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range where {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	run := func(capability string, params map[string]any) (Resolved, Outcome) {
		t.Helper()
		r := mustResolve(t, capability, params)
		out := Apply(ctx, env, r, nil)
		if out.Status != StatusDone {
			t.Fatalf("%s: %+v", capability, out)
		}
		return r, out
	}
	undo := func(r Resolved, out Outcome) {
		t.Helper()
		if u := Undo(ctx, env, r, out.Undo); u.Status != StatusUndone {
			t.Fatalf("undo %s: %+v", r.Cap.Name, u)
		}
	}

	// A new bucket, undone while empty; not once it has a file.
	cr, co := run("cos.bucket.create", where)
	if b := f.Bucket(name); b == nil || b.ACL.Canned != "private" {
		t.Fatalf("created %+v", b)
	}
	undo(cr, co)
	if f.Bucket(name) != nil {
		t.Fatal("undo left the bucket")
	}
	cr, co = run("cos.bucket.create", with(map[string]any{"acl": "public-read"}))
	f.COS[name].PutFile("a.txt", []byte("x"))
	if u := Undo(ctx, env, cr, co.Undo); u.Status != StatusFailed || !strings.Contains(strings.Join(u.Log, ""), "已经有文件") {
		t.Fatalf("undo a used bucket: %+v", u)
	}

	// Access: grants to other accounts survive, and come back on undo.
	f.COS[name].ACL.Grants["x-cos-grant-read"] = `id="qcs::cam::uin/100000000002:uin/100000000002"`
	ar, ao := run("cos.acl.set", with(map[string]any{"acl": "private"}))
	if b := f.Bucket(name); b.ACL.Canned != "private" || b.ACL.Grants["x-cos-grant-read"] == "" {
		t.Fatalf("acl %+v", b.ACL)
	}
	if again := Apply(ctx, env, ar, nil); again.Status != StatusDone || len(again.Undo) != 0 {
		t.Fatalf("same acl again: %+v", again)
	}
	undo(ar, ao)
	if b := f.Bucket(name); b.ACL.Canned != "public-read" || b.ACL.Grants["x-cos-grant-read"] == "" {
		t.Fatalf("acl after undo %+v", b.ACL)
	}

	rr, ro := run("cos.referer.set", with(map[string]any{"status": "on", "domains": "example.com\n*.example.com", "allow_empty": "no"}))
	if b := f.Bucket(name); b.Referer == nil || b.Referer.Status != "Enabled" || len(b.Referer.Domains) != 2 || b.Referer.EmptyRefer != "Deny" {
		t.Fatalf("referer %+v", b.Referer)
	}
	undo(rr, ro)
	if b := f.Bucket(name); b.Referer.Status != "Disabled" {
		t.Fatalf("referer after undo %+v", b.Referer)
	}

	cors := `[{"origins":["https://example.com"],"methods":["get","put"],"headers":["*"],"maxAge":600}]`
	xr, xo := run("cos.cors.set", with(map[string]any{"rules": cors}))
	if b := f.Bucket(name); len(b.CORS) != 1 || b.CORS[0].Methods[0] != "GET" {
		t.Fatalf("cors %+v", b.CORS)
	}
	undo(xr, xo)
	if b := f.Bucket(name); b.CORS != nil {
		t.Fatalf("cors after undo %+v", b.CORS)
	}

	// A rule made elsewhere (with a tag filter) is kept as it is.
	orig := `<LifecycleConfiguration><Rule><ID>tagged</ID><Filter><Tag><Key>k</Key><Value>v</Value></Tag></Filter><Status>Enabled</Status><Expiration><Days>9</Days></Expiration></Rule></LifecycleConfiguration>`
	f.COS[name].Lifecycle = orig
	rules := `[{"id":"backup-30d","prefix":"backup/","enabled":true,"expireDays":30},{"id":"cold","enabled":true,"iaDays":30,"archiveDays":90,"abortDays":7}]`
	lr, lo := run("cos.lifecycle.set", with(map[string]any{"rules": rules, "keep": "tagged"}))
	got := f.Bucket(name).Lifecycle
	for _, want := range []string{"<ID>tagged</ID><Filter><Tag><Key>k</Key>", "<ID>backup-30d</ID><Filter><Prefix>backup/</Prefix></Filter><Status>Enabled</Status><Expiration><Days>30</Days>",
		"<Transition><Days>30</Days><StorageClass>STANDARD_IA</StorageClass></Transition><Transition><Days>90</Days><StorageClass>ARCHIVE</StorageClass>",
		"<AbortIncompleteMultipartUpload><DaysAfterInitiation>7</DaysAfterInitiation>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("lifecycle missing %s in %s", want, got)
		}
	}
	undo(lr, lo)
	if got := f.Bucket(name).Lifecycle; got != orig {
		t.Fatalf("lifecycle after undo %s", got)
	}
	cl, clo := run("cos.lifecycle.set", with(map[string]any{"rules": "[]"}))
	if f.Bucket(name).Lifecycle != "" {
		t.Fatal("lifecycle not removed")
	}
	undo(cl, clo)
	if f.Bucket(name).Lifecycle != orig {
		t.Fatal("removed lifecycle not restored")
	}

	vr, vo := run("cos.versioning.set", with(map[string]any{"status": "on"}))
	if f.Bucket(name).Versioning != "Enabled" {
		t.Fatal("versioning not on")
	}
	undo(vr, vo)
	if f.Bucket(name).Versioning != "Suspended" {
		t.Fatalf("versioning after undo %q", f.Bucket(name).Versioning)
	}

	er, eo := run("cos.encryption.set", with(map[string]any{"status": "on"}))
	if f.Bucket(name).Encryption != "AES256" {
		t.Fatal("encryption not on")
	}
	undo(er, eo)
	if f.Bucket(name).Encryption != "" {
		t.Fatal("encryption still on")
	}

	wr, wo := run("cos.website.set", with(map[string]any{"status": "on", "error": "404.html", "https": "yes"}))
	if w := f.Bucket(name).Website; w == nil || w.Index != "index.html" || w.Error != "404.html" || !w.HTTPS {
		t.Fatalf("website %+v", w)
	}
	undo(wr, wo)
	if f.Bucket(name).Website != nil {
		t.Fatal("website still on")
	}

	pol := `{"Statement":[{"Principal":{"qcs":["qcs::cam::anyone:anyone"]},"Effect":"Allow","Action":["name/cos:GetObject"],"Resource":["qcs::cos:ap-guangzhou:uid/1250000000:blog-1250000000/*"]}],"version":"2.0"}`
	pr, po := run("cos.policy.set", with(map[string]any{"policy": pol}))
	if f.Bucket(name).Policy != pol {
		t.Fatal("policy not set")
	}
	undo(pr, po)
	if f.Bucket(name).Policy != "" {
		t.Fatal("policy not removed by undo")
	}

	// Deleting: only an empty bucket.
	del := mustResolve(t, "cos.bucket.delete", where)
	if o := Apply(ctx, env, del, nil); o.Status != StatusRefused || f.Bucket(name) == nil {
		t.Fatalf("delete a bucket with files: %+v", o)
	}
	delete(f.COS[name].Objects, "a.txt")
	if o := Apply(ctx, env, del, nil); o.Status != StatusDone || f.Bucket(name) != nil {
		t.Fatalf("delete an empty bucket: %+v", o)
	}
}

func TestCOSSettingValues(t *testing.T) {
	where := func(extra map[string]any) map[string]any {
		m := map[string]any{"bucket": "blog-1250000000", "region": "ap-guangzhou"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	bad := []struct {
		capability string
		params     map[string]any
	}{
		{"cos.bucket.create", map[string]any{"bucket": "Blog-1250000000", "region": "ap-guangzhou"}},
		{"cos.bucket.create", map[string]any{"bucket": "blog", "region": "ap-guangzhou"}},
		{"cos.bucket.create", where(map[string]any{"acl": "public-read-write"})},
		{"cos.referer.set", where(map[string]any{"status": "on"})},
		{"cos.referer.set", where(map[string]any{"status": "on", "domains": "exa mple.com; rm"})},
		{"cos.cors.set", where(map[string]any{"rules": `[{"origins":["ftp://x"],"methods":["GET"]}]`})},
		{"cos.cors.set", where(map[string]any{"rules": `[{"origins":["*"],"methods":["PATCH"]}]`})},
		{"cos.lifecycle.set", where(map[string]any{"rules": `[{"id":"x","enabled":true,"iaDays":90,"archiveDays":30}]`})},
		{"cos.lifecycle.set", where(map[string]any{"rules": `[{"id":"x","enabled":true}]`})},
		{"cos.lifecycle.set", where(map[string]any{"rules": `[{"id":"x","enabled":true,"iaDays":30,"expireDays":10}]`})},
		{"cos.policy.set", where(map[string]any{"policy": "not json"})},
	}
	for _, b := range bad {
		if _, err := Resolve(b.capability, b.params, "-"); err == nil {
			t.Errorf("%s %v accepted", b.capability, b.params)
		}
	}
	if _, err := Resolve("cos.lifecycle.set", where(map[string]any{"rules": `[{"prefix":"logs/","enabled":true,"archiveDays":90,"expireDays":365}]`}), "-"); err != nil {
		t.Errorf("good lifecycle refused: %v", err)
	}
}
