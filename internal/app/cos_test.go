package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent/tencenttest"
)

func cosApp(t *testing.T) (*App, *tencenttest.Fake) {
	t.Helper()
	a := newApp(t)
	f := tencenttest.Start(t)
	a.TencentEndpoint = f.Endpoint
	a.PollInterval = 10 * time.Millisecond
	if _, err := a.SaveTencent(tencenttest.SecretID, tencenttest.SecretKey); err != nil {
		t.Fatal(err)
	}
	return a, f
}

func TestCOSBucketsAndFindings(t *testing.T) {
	a, f := cosApp(t)
	ctx := context.Background()
	img := "img-" + tencenttest.COSAppID
	bak := "bak-" + tencenttest.COSAppID

	// No buckets yet: the APPID comes from the account.
	v, err := a.COSBuckets(ctx)
	if err != nil || len(v.Buckets) != 0 || v.AppID != tencenttest.COSAppID {
		t.Fatalf("empty = %+v %v", v, err)
	}
	f.AddBucket(img, "ap-guangzhou", "public-read")
	b := f.AddBucket(bak, "ap-shanghai", "public-read-write")
	b.PutFile("db/2026-09-01.sql.gz", []byte("x"))
	b.PutFile("site/.env", []byte("x"))
	b.PutFile("site/index.html", []byte("x"))
	f.COS[bak].Policy = `{"Statement":[{"Principal":{"qcs":["qcs::cam::anyone:anyone"]},"Effect":"Allow","Action":["name/cos:PutObject"],"Resource":["*"]},{"Principal":{"qcs":["qcs::cam::uin/2:uin/2"]},"Effect":"Allow","Action":["name/cos:GetObject"],"Resource":["*"]}],"version":"2.0"}`

	v, _ = a.COSBuckets(ctx)
	if len(v.Buckets) != 2 || v.Buckets[0].Name != bak || v.Buckets[0].Level != "crit" || v.Buckets[1].Level != "warn" {
		t.Fatalf("buckets = %+v", v.Buckets)
	}

	d, err := a.COSBucketDetail(ctx, bak, "ap-shanghai")
	if err != nil || len(d.Errors) != 0 || d.PolicyPublic != "write" {
		t.Fatalf("detail = %+v %v", d, err)
	}
	text := ""
	for _, x := range d.Findings {
		text += x.Level + ":" + x.Fix + ":" + x.Text + "\n"
	}
	for _, want := range []string{"crit:private:访问权限是「公有读写」", "crit:policy_public_off:", "crit:private:公开的桶里有 2 个", "info:versioning:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("findings miss %q:\n%s", want, text)
		}
	}
	d, _ = a.COSBucketDetail(ctx, img, "ap-guangzhou")
	if len(d.Findings) < 1 || d.Findings[0].Fix != "referer" {
		t.Fatalf("public-read without referer: %+v", d.Findings)
	}

	// Fixing from the page runs at once and can be undone.
	p, err := a.ProposeCOS(ctx, COSRequest{Op: "acl", Bucket: bak, Region: "ap-shanghai", ACL: "private", Run: true})
	if err != nil {
		t.Fatal(err)
	}
	done := waitPlan(t, a, p.ID)
	if done.StepList[0].Status != actions.StatusDone || f.Bucket(bak).ACL.Canned != "private" {
		t.Fatalf("acl fix = %+v", done.StepList[0])
	}
	p, err = a.ProposeCOS(ctx, COSRequest{Op: "policy_public_off", Bucket: bak, Region: "ap-shanghai", Run: true})
	if err != nil {
		t.Fatal(err)
	}
	waitPlan(t, a, p.ID)
	if pol := f.Bucket(bak).Policy; strings.Contains(pol, "anyone") || !strings.Contains(pol, "uin/2") {
		t.Fatalf("policy after = %s", pol)
	}
	if _, err := a.UndoPlan(ctx, p.ID); err != nil || !strings.Contains(f.Bucket(bak).Policy, "anyone") {
		t.Fatalf("undo policy: %v %s", err, f.Bucket(bak).Policy)
	}

	// Lifecycle from the page, keeping a rule made elsewhere.
	f.COS[img].Lifecycle = `<LifecycleConfiguration><Rule><ID>tagged</ID><Filter><Tag><Key>k</Key><Value>v</Value></Tag></Filter><Status>Enabled</Status><Expiration><Days>9</Days></Expiration></Rule></LifecycleConfiguration>`
	d, _ = a.COSBucketDetail(ctx, img, "ap-guangzhou")
	if len(d.Lifecycle) != 1 || d.Lifecycle[0].Editable {
		t.Fatalf("tagged rule = %+v", d.Lifecycle)
	}
	p, err = a.ProposeCOS(ctx, COSRequest{Op: "lifecycle", Bucket: img, Region: "ap-guangzhou", Keep: []string{"tagged"},
		Lifecycle: []actions.LifeRule{{ID: "logs", Prefix: "logs/", Enabled: true, ArchiveDays: 30, ExpireDays: 180}}, Run: true})
	if err != nil || !strings.Contains(p.Reason, "找不回") || !strings.Contains(p.StepList[0].Summary, "logs/：30 天后转归档，180 天后删除") {
		t.Fatalf("lifecycle plan = %+v %v", p, err)
	}
	waitPlan(t, a, p.ID)
	d, _ = a.COSBucketDetail(ctx, img, "ap-guangzhou")
	if len(d.Lifecycle) != 2 || !d.Lifecycle[1].Editable || d.Lifecycle[1].ExpireDays != 180 {
		t.Fatalf("lifecycle after = %+v", d.Lifecycle)
	}

	// New bucket, then referer on it; bad requests are refused up front.
	nb := "new-" + tencenttest.COSAppID
	p, _ = a.ProposeCOS(ctx, COSRequest{Op: "create", Bucket: nb, Region: "ap-beijing", Run: true})
	waitPlan(t, a, p.ID)
	if f.Bucket(nb) == nil {
		t.Fatal("bucket not created")
	}
	for _, req := range []COSRequest{
		{Op: "acl", Bucket: nb, Region: "ap-beijing", ACL: "everyone"},
		{Op: "referer", Bucket: nb, Region: "ap-beijing", Status: "on"},
		{Op: "cors", Bucket: nb, Region: "ap-beijing", CORS: nil, Policy: ""},
		{Op: "policy_public_off", Bucket: nb, Region: "ap-beijing"},
		{Op: "acl", Bucket: "Bad Name", Region: "ap-beijing", ACL: "private"},
		{Op: "nothing", Bucket: nb, Region: "ap-beijing"},
	} {
		_, err := a.ProposeCOS(ctx, req)
		if req.Op == "cors" { // no rules removes them: allowed
			if err != nil {
				t.Errorf("empty cors refused: %v", err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%+v accepted", req)
		}
	}

	// What the AI sees.
	out, err := a.toolTencentCOS(ctx, json.RawMessage(`{"bucket":"`+bak+`"}`))
	if err != nil || !strings.Contains(out, "ap-shanghai") || !strings.Contains(out, "db/") || !strings.Contains(out, "发现") {
		t.Fatalf("tool = %q %v", out, err)
	}
}

func TestCOSFiles(t *testing.T) {
	a, f := cosApp(t)
	ctx := context.Background()
	name := "files-" + tencenttest.COSAppID
	f.AddBucket(name, "ap-guangzhou", "private")

	if err := a.COSMkdir(ctx, name, "ap-guangzhou", "docs/"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.COSUpload(ctx, name, "ap-guangzhou", "docs/报告 1.txt", strings.NewReader("hello"), 5, "text/plain", false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.COSUpload(ctx, name, "ap-guangzhou", "docs/报告 1.txt", strings.NewReader("again"), 5, "text/plain", false); err == nil ||
		err.(*UserError).Code != "conflict" {
		t.Fatalf("clash: %v", err)
	}
	if _, err := a.COSUpload(ctx, name, "ap-guangzhou", "docs/报告 1.txt", strings.NewReader("again"), 5, "text/plain", true); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"/abs.txt", "a/../b.txt", "a//b.txt", "dir/", "x\n.txt"} {
		if _, err := a.COSUpload(ctx, name, "ap-guangzhou", bad, strings.NewReader("x"), 1, "", false); err == nil {
			t.Errorf("key %q accepted", bad)
		}
	}
	l, err := a.COSObjects(ctx, name, "ap-guangzhou", "", "")
	if err != nil || len(l.Entries) != 1 || !l.Entries[0].Folder || l.Entries[0].Name != "docs" {
		t.Fatalf("root = %+v %v", l, err)
	}
	l, _ = a.COSObjects(ctx, name, "ap-guangzhou", "docs/", "")
	if len(l.Entries) != 1 || l.Entries[0].Name != "报告 1.txt" || l.Entries[0].Size != 5 {
		t.Fatalf("docs = %+v", l)
	}
	if string(f.Bucket(name).Objects["docs/报告 1.txt"].Data) != "again" {
		t.Fatal("not overwritten")
	}

	// Rename a file, then the folder.
	if err := a.COSRename(ctx, name, "ap-guangzhou", "docs/报告 1.txt", "docs/报告.txt"); err != nil {
		t.Fatal(err)
	}
	if err := a.COSRename(ctx, name, "ap-guangzhou", "docs/", "docs/sub/"); err == nil {
		t.Fatal("folder moved into itself")
	}
	if err := a.COSRename(ctx, name, "ap-guangzhou", "docs/", "papers/"); err != nil {
		t.Fatal(err)
	}
	objs := f.Bucket(name).Objects
	if _, ok := objs["papers/报告.txt"]; !ok || len(objs) != 2 {
		t.Fatalf("after rename: %v", keysOf(objs))
	}

	link, err := a.COSLink(name, "ap-guangzhou", "papers/报告.txt", time.Hour, false)
	if err != nil || !strings.Contains(link["url"], "q-signature=") || !strings.Contains(link["public"], "myqcloud.com/papers/") {
		t.Fatalf("link = %v %v", link, err)
	}

	n, err := a.COSDelete(ctx, name, "ap-guangzhou", []string{"papers/"})
	if err != nil || n != 2 || len(f.Bucket(name).Objects) != 0 {
		t.Fatalf("delete folder: %d %v %v", n, err, keysOf(f.Bucket(name).Objects))
	}
	logs, _ := a.Store.ListAudit(50)
	var seen []string
	for _, l := range logs {
		if strings.HasPrefix(l.Action, "cos.") {
			seen = append(seen, l.Action)
		}
	}
	if len(seen) < 6 {
		t.Fatalf("audit = %v", seen)
	}
}

func keysOf[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestCOSUsage(t *testing.T) {
	a, f := cosApp(t)
	ctx := context.Background()
	name := "img-" + tencenttest.COSAppID
	f.AddBucket(name, "ap-guangzhou", "public-read")
	u, err := a.COSUsage(ctx, name, "ap-guangzhou")
	if err != nil || u.Available || u.Note == "" {
		t.Fatalf("no data = %+v %v", u, err)
	}
	hours := make([]float64, 48)
	for i := range hours {
		hours[i] = 10 << 20
		if i >= 40 {
			hours[i] = 2 << 30 // somebody is pulling files hard
		}
	}
	f.COSUsage = map[string]tencenttest.COSUsage{name: {StorageMB: 2048, TrafficPerHour: hours}}
	u, err = a.COSUsage(ctx, name, "ap-guangzhou")
	if err != nil || !u.Available || u.StorageBytes != 2048<<20 || !u.Spike || len(u.Hourly) != 48 || u.Metrics["traffic"] != "InternetTraffic" {
		t.Fatalf("usage = %+v %v", u, err)
	}
}

func TestCOSSensitiveKeys(t *testing.T) {
	for key, want := range map[string]bool{
		"db/2026-09-01.sql.gz": true, "site/.env": true, "wwwroot-2026-09-01.tar.gz": true, "backup/app.zip": true,
		"mysql_dump.tgz": true, "certs/example.com.key": true, "site-2026.zip": true,
		"downloads/app-v1.2.zip": false, "images/logo.png": false, "release/tool.tar.gz": false, "fonts.zip": false, "website.zip": false,
	} {
		if got := sensitiveKey(key); got != want {
			t.Errorf("sensitiveKey(%q) = %v", key, got)
		}
	}
}

// Public through the policy alone: the fix offered is the policy, not the
// access setting, which is private already.
func TestCOSPublicByPolicy(t *testing.T) {
	a, f := cosApp(t)
	name := "pol-" + tencenttest.COSAppID
	b := f.AddBucket(name, "ap-guangzhou", "private")
	b.PutFile("db/all.sql", []byte("x"))
	f.COS[name].Policy = `{"Statement":[{"Principal":{"qcs":["qcs::cam::anyone:anyone"]},"Effect":"Allow","Action":["name/cos:GetObject"],"Resource":["*"]}],"version":"2.0"}`
	d, err := a.COSBucketDetail(context.Background(), name, "ap-guangzhou")
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range d.Findings {
		if len(x.Files) > 0 && x.Fix != "policy_public_off" {
			t.Fatalf("sensitive files fix = %q", x.Fix)
		}
	}
}
