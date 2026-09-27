package app

import (
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
	"github.com/bocmiao/CloudConsoleWithAI/internal/visits"
)

func TestStampIn(t *testing.T) {
	loc := time.Local
	defer func() { time.Local = loc }()
	time.Local = time.FixedZone("CST", 8*3600)
	if got := stampIn("2026-09-26 10:00:00", "+0000"); got != "2026-09-26 02:00:00" {
		t.Fatalf("utc log: %s", got)
	}
	if got := stampIn("2026-09-26 10:00:00", "+0800"); got != "2026-09-26 10:00:00" {
		t.Fatalf("same zone: %s", got)
	}
	if got := stampIn("2026-09-26 10:00:00", ""); got != "2026-09-26 10:00:00" {
		t.Fatalf("no zone: %s", got)
	}
}

func TestAutoBlockRecordsOnlyRulesItCreated(t *testing.T) {
	a := newApp(t)
	plan := PlanView{StepList: []core.Step{{
		Capability: "eo.ip.block", Status: actions.StatusDone,
		Params: map[string]any{"domain": "example.com", "ips": "1.2.3.4,5.6.7.8"},
		Undo:   map[string]string{"changed": "5.6.7.8"},
	}}}
	if n := a.recordAutoBlocked(plan, AutoBlockSettings{Hours: 24}, nil); n != 1 {
		t.Fatalf("recorded %d rules, want 1", n)
	}
	st := a.AutoBlock()
	if len(st.Blocked) != 1 || st.Blocked[0].IP != "5.6.7.8" || st.Blocked[0].Zone != "example.com" {
		t.Fatalf("owned rules: %+v", st.Blocked)
	}
}

func TestAutoBlockChecksEachVisitedZone(t *testing.T) {
	zones := []tencent.Zone{{ZoneID: "a", ZoneName: "example.com"}, {ZoneID: "b", ZoneName: "other.com"}}
	p := visits.IPProfile{IP: "1.2.3.4", Sites: []visits.Item{{Value: "blog.example.com"}, {Value: "api.other.com"}}}
	blocked := map[string]map[string]bool{"example.com": {p.IP: true}}
	if blockedOnSites(blocked, zones, p, nil) {
		t.Fatal("block on example.com hid a missing block on other.com")
	}
	blocked["other.com"] = map[string]bool{p.IP: true}
	if !blockedOnSites(blocked, zones, p, nil) {
		t.Fatal("same IP already blocked on both sites")
	}
}
