package actions

import (
	"context"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun"
)

func aliRecords(t *testing.T, env *Env, name string) []aliyun.Record {
	t.Helper()
	all, err := env.Aliyun.Records(context.Background(), "example.com")
	if err != nil {
		t.Fatal(err)
	}
	var out []aliyun.Record
	for _, r := range all {
		if r.Name == name {
			out = append(out, r)
		}
	}
	return out
}

func TestAlidnsRecords(t *testing.T) {
	env, _ := aliEnv(t)
	ctx := context.Background()

	aliRun(t, env, "aliyun.dns.record.add", map[string]any{"domain": "example.com", "subdomain": "api", "type": "A", "value": "203.0.113.9", "line": "电信"}, StatusDone)
	got := aliRecords(t, env, "api")
	if len(got) != 1 || got[0].Line != "telecom" || got[0].TTL != 600 {
		t.Fatalf("added = %+v", got)
	}
	if _, again := aliRun(t, env, "aliyun.dns.record.add", map[string]any{"domain": "example.com", "subdomain": "api", "type": "A", "value": "203.0.113.9", "line": "telecom"}, StatusDone); len(again.Undo) != 0 {
		t.Error("adding the same record twice changed something")
	}
	id := got[0].ID

	r2, out2 := aliRun(t, env, "aliyun.dns.record.modify", map[string]any{"domain": "example.com", "record_id": id, "subdomain": "api", "type": "A", "value": "203.0.113.10"}, StatusDone)
	if got = aliRecords(t, env, "api"); got[0].Value != "203.0.113.10" || got[0].Line != "telecom" {
		t.Fatalf("modified = %+v", got)
	}
	if u := Undo(ctx, env, r2, out2.Undo); u.Status != StatusUndone {
		t.Fatalf("undo modify: %+v", u)
	}
	if got = aliRecords(t, env, "api"); got[0].Value != "203.0.113.9" {
		t.Fatalf("after undo = %+v", got)
	}

	r3, out3 := aliRun(t, env, "aliyun.dns.record.status", map[string]any{"domain": "example.com", "record_id": id, "status": "disable"}, StatusDone)
	if got = aliRecords(t, env, "api"); got[0].Status != "disabled" {
		t.Fatalf("paused = %+v", got)
	}
	if u := Undo(ctx, env, r3, out3.Undo); u.Status != StatusUndone {
		t.Fatalf("undo status: %+v", u)
	}

	r4, out4 := aliRun(t, env, "aliyun.dns.record.delete", map[string]any{"domain": "example.com", "record_id": id}, StatusDone)
	if len(aliRecords(t, env, "api")) != 0 {
		t.Fatal("not deleted")
	}
	if u := Undo(ctx, env, r4, out4.Undo); u.Status != StatusUndone || len(aliRecords(t, env, "api")) != 1 {
		t.Fatalf("undo delete: %+v", u)
	}
	aliRun(t, env, "aliyun.dns.record.delete", map[string]any{"domain": "example.com", "record_id": "123"}, StatusRefused)
	// Undoing again finds it done already.
	if u := Undo(ctx, env, r4, out4.Undo); u.Status != StatusUndone || len(aliRecords(t, env, "api")) != 1 {
		t.Fatalf("undo delete again: %+v", u)
	}

	// A line only the domain's edition has, by the name the page shows.
	r5, out5 := aliRun(t, env, "aliyun.dns.record.add", map[string]any{"domain": "example.com", "subdomain": "nw", "type": "A", "value": "203.0.113.11", "line": "中国地区_西北", "remark": "西北机房"}, StatusDone)
	if got = aliRecords(t, env, "nw"); len(got) != 1 || got[0].Line != "cn_region_xibei" || got[0].Remark != "西北机房" {
		t.Fatalf("regional = %+v", got)
	}
	// Clearing the remark.
	aliRun(t, env, "aliyun.dns.record.modify", map[string]any{"domain": "example.com", "record_id": got[0].ID, "subdomain": "nw", "type": "A", "value": "203.0.113.11", "clear_remark": "yes"}, StatusDone)
	if got = aliRecords(t, env, "nw"); got[0].Remark != "" {
		t.Fatalf("remark = %q", got[0].Remark)
	}
	if u := Undo(ctx, env, r5, out5.Undo); u.Status != StatusUndone || len(aliRecords(t, env, "nw")) != 0 {
		t.Fatalf("undo regional: %+v", u)
	}
	if u := Undo(ctx, env, r5, out5.Undo); u.Status != StatusUndone {
		t.Fatalf("undo regional again: %+v", u)
	}

	// Record IDs are too big for JSON numbers.
	if _, err := Resolve("aliyun.dns.record.delete", map[string]any{"domain": "example.com", "record_id": float64(1847365210987654321)}, "*"); err == nil {
		t.Error("a rounded record ID was accepted")
	}
}

func TestAlidnsSet(t *testing.T) {
	env, _ := aliEnv(t)
	ctx := context.Background()
	// www is A 47.96.1.2; point it at a CDN name instead.
	r, out := aliRun(t, env, "aliyun.dns.record.set", map[string]any{"domain": "example.com", "subdomain": "www", "type": "CNAME", "value": "www.example.com.w.kunlunsl.com"}, StatusDone)
	got := aliRecords(t, env, "www")
	if len(got) != 1 || got[0].Type != "CNAME" {
		t.Fatalf("set = %+v", got)
	}
	if _, again := aliRun(t, env, "aliyun.dns.record.set", map[string]any{"domain": "example.com", "subdomain": "www", "type": "CNAME", "value": "www.example.com.w.kunlunsl.com"}, StatusDone); len(again.Undo) != 0 {
		t.Error("setting it again changed something")
	}
	if u := Undo(ctx, env, r, out.Undo); u.Status != StatusUndone {
		t.Fatalf("undo: %+v", u)
	}
	if got = aliRecords(t, env, "www"); len(got) != 1 || got[0].Type != "A" || got[0].Value != "47.96.1.2" {
		t.Fatalf("after undo = %+v", got)
	}
	if AliLineCode("境外") != "oversea" || AliLineName("unicom") != "联通" || AliLineCode("") != "default" {
		t.Error("line names")
	}
}

func TestAliyunCDN(t *testing.T) {
	env, _ := aliEnv(t)
	aliRun(t, env, "aliyun.cdn.purge", map[string]any{"targets": "https://cdn.example.com/css/app.css\nhttps://cdn.example.com/js/app.js"}, StatusDone)
	aliRun(t, env, "aliyun.cdn.purge", map[string]any{"targets": "https://cdn.example.com/img", "type": "dir"}, StatusDone)
	aliRun(t, env, "aliyun.cdn.prefetch", map[string]any{"targets": "https://cdn.example.com/big.zip"}, StatusDone)
	aliRun(t, env, "aliyun.cdn.purge", map[string]any{"targets": "https://www.other.com/a.css"}, StatusRefused)
	aliRun(t, env, "aliyun.cdn.purge", map[string]any{"targets": "cdn.example.com/a.css"}, StatusRefused)
}
