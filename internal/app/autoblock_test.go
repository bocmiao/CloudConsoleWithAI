package app

import (
	"testing"
	"time"
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
