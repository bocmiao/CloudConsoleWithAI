package actions

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// Editing single records: add, change, pause and delete, each undone.
func TestDNSRecordEdits(t *testing.T) {
	env, f := cloudEnv(t)
	ctx := context.Background()
	f.Records["example.com"] = append(f.Records["example.com"],
		tencent.Record{RecordID: 2, Name: "@", Type: "NS", Value: "f1g1ns1.dnspod.net.", Line: tencent.DefaultLine, TTL: 86400, Status: "ENABLE", DefaultNS: true})

	// A mail record, with its priority.
	mx := mustResolve(t, "dns.record.add", map[string]any{"domain": "example.com", "subdomain": "@", "type": "MX", "value": "mx.example.net", "mx": 5})
	out := Apply(ctx, env, mx, nil)
	recs := f.Lookup("example.com", "@")
	var got *tencent.Record
	for i := range recs {
		if recs[i].Type == "MX" {
			got = &recs[i]
		}
	}
	if out.Status != StatusDone || got == nil || got.MX != 5 || got.TTL != 600 || got.Line != tencent.DefaultLine {
		t.Fatalf("add MX: %+v %+v", out, recs)
	}
	if again := Apply(ctx, env, mx, nil); again.Status != StatusDone || len(again.Undo) != 0 {
		t.Fatalf("adding the same record again: %+v", again)
	}
	id := strconv.FormatUint(got.RecordID, 10)

	// A CNAME cannot join the A record blog already has.
	clash := mustResolve(t, "dns.record.add", map[string]any{"domain": "example.com", "subdomain": "blog", "type": "CNAME", "value": "x.example.net"})
	if o := Apply(ctx, env, clash, nil); o.Status != StatusRefused || !strings.Contains(strings.Join(o.Log, ""), "修改那条记录") {
		t.Fatalf("CNAME next to A: %+v", o)
	}

	// Pause blog's A record, then change it: it stays paused.
	pause := mustResolve(t, "dns.record.status", map[string]any{"domain": "example.com", "record_id": 1, "status": "disable"})
	po := Apply(ctx, env, pause, nil)
	if po.Status != StatusDone || f.Lookup("example.com", "blog")[0].Status != "DISABLE" {
		t.Fatalf("pause: %+v", po)
	}
	mod := mustResolve(t, "dns.record.modify", map[string]any{"domain": "example.com", "record_id": 1, "subdomain": "blog", "type": "A", "value": "5.6.7.8", "ttl": 300})
	mo := Apply(ctx, env, mod, nil)
	b := f.Lookup("example.com", "blog")[0]
	if mo.Status != StatusDone || b.Value != "5.6.7.8" || b.TTL != 300 || b.Status != "DISABLE" {
		t.Fatalf("modify: %+v %+v", mo, b)
	}
	if u := Undo(ctx, env, mod, mo.Undo); u.Status != StatusUndone {
		t.Fatalf("undo modify: %+v", u)
	}
	if b := f.Lookup("example.com", "blog")[0]; b.Value != "1.2.3.4" || b.TTL != 600 || b.Status != "DISABLE" {
		t.Fatalf("after undoing the change: %+v", b)
	}
	if u := Undo(ctx, env, pause, po.Undo); u.Status != StatusUndone || f.Lookup("example.com", "blog")[0].Status != "ENABLE" {
		t.Fatalf("undo pause: %+v", u)
	}

	// Delete the MX record, and bring it back.
	del := mustResolve(t, "dns.record.delete", map[string]any{"domain": "example.com", "record_id": id})
	do := Apply(ctx, env, del, nil)
	if do.Status != StatusDone || len(f.Lookup("example.com", "@")) != 1 {
		t.Fatalf("delete: %+v", do)
	}
	if u := Undo(ctx, env, del, do.Undo); u.Status != StatusUndone {
		t.Fatalf("undo delete: %+v", u)
	}
	back := false
	for _, r := range f.Lookup("example.com", "@") {
		back = back || (r.Type == "MX" && r.MX == 5 && r.Value == "mx.example.net")
	}
	if !back {
		t.Fatalf("MX not restored: %+v", f.Lookup("example.com", "@"))
	}
	if u := Undo(ctx, env, mx, out.Undo); u.Status != StatusUndone {
		t.Fatalf("undoing an add whose record is already gone again: %+v", u)
	}

	// DNSPod's own NS records stay as they are.
	for _, r := range []Resolved{
		mustResolve(t, "dns.record.delete", map[string]any{"domain": "example.com", "record_id": 2}),
		mustResolve(t, "dns.record.status", map[string]any{"domain": "example.com", "record_id": 2, "status": "disable"}),
		mustResolve(t, "dns.record.modify", map[string]any{"domain": "example.com", "record_id": 2, "subdomain": "@", "type": "NS", "value": "ns.evil.example"}),
	} {
		if o := Apply(ctx, env, r, nil); o.Status != StatusRefused {
			t.Fatalf("%s on the default NS: %+v", r.Cap.Name, o)
		}
	}
	// A record that is gone cannot be changed.
	if o := Apply(ctx, env, mustResolve(t, "dns.record.modify", map[string]any{"domain": "example.com", "record_id": 999, "subdomain": "x", "type": "A", "value": "1.1.1.1"}), nil); o.Status != StatusRefused {
		t.Fatalf("modify a missing record: %+v", o)
	}
}

func TestDNSRecordValues(t *testing.T) {
	good := map[string]string{
		"A": "1.2.3.4", "AAAA": "2001:db8::1", "CNAME": "a.example.net", "MX": "mx.example.net", "NS": "ns1.example.net.",
		"TXT": "v=spf1 include:spf.example.net -all", "CAA": `0 issue "letsencrypt.org"`, "SRV": "5 0 5060 sip.example.net",
	}
	for typ, v := range good {
		if _, err := Resolve("dns.record.add", map[string]any{"domain": "example.com", "subdomain": "x", "type": typ, "value": v}, "-"); err != nil {
			t.Errorf("%s %q refused: %v", typ, v, err)
		}
	}
	bad := [][2]string{
		{"A", "1.2.3"}, {"AAAA", "1.2.3.4"}, {"CNAME", "not a host"}, {"MX", "1.2.3.4"}, {"CAA", "letsencrypt.org"},
		{"SRV", "5 0 sip.example.net"}, {"SRV", "5 0 99999 sip.example.net"}, {"TXT", ""}, {"SPF", "x"},
	}
	for _, b := range bad {
		if _, err := Resolve("dns.record.add", map[string]any{"domain": "example.com", "subdomain": "x", "type": b[0], "value": b[1]}, "-"); err == nil {
			t.Errorf("%s %q accepted", b[0], b[1])
		}
	}
	if _, err := Resolve("dns.record.add", map[string]any{"domain": "example.com", "subdomain": "x", "type": "A", "value": "1.1.1.1", "line": "电信; drop"}, "-"); err == nil {
		t.Error("odd line accepted")
	}
	if _, err := Resolve("dns.record.add", map[string]any{"domain": "example.com", "subdomain": "x", "type": "A", "value": "1.1.1.1", "line": "境外"}, "-"); err != nil {
		t.Errorf("line 境外 refused: %v", err)
	}
}
