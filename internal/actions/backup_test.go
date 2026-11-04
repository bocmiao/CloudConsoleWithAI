package actions

import (
	"context"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/onepanel/onepaneltest"
)

func TestSiteBackupAndRestore(t *testing.T) {
	f := onepaneltest.New()
	s := f.AddSite("blog.example.com", "static", "")
	env, _ := siteEnv(t, f)
	ctx := context.Background()

	_, out := run(t, env, "site.backup", map[string]any{"website": "blog.example.com", "note": "改版前"}, StatusDone)
	if s.Backups != 1 || out.Result["backup"] == "" {
		t.Fatalf("backup: %d backups, result %v", s.Backups, out.Result)
	}
	list, err := env.OnePanel.BackupList(ctx, "website", s.Alias, s.Alias)
	if err != nil || len(list) != 1 || list[0].Description != "改版前" || list[0].ID == 0 {
		t.Fatalf("list = %+v, %v", list, err)
	}

	// Restoring backs the site up first, then puts the chosen backup back.
	_, out = run(t, env, "site.restore", map[string]any{"website": "blog.example.com", "backup": list[0].FileName}, StatusDone)
	if s.Restores != 1 || s.Backups != 2 || out.Result["before"] == "" {
		t.Fatalf("restore: %d restores, %d backups, result %v", s.Restores, s.Backups, out.Result)
	}
	if !strings.Contains(strings.Join(out.Log, "\n"), out.Result["before"]) {
		t.Errorf("log does not name the backup made before restoring:\n%s", strings.Join(out.Log, "\n"))
	}
	run(t, env, "site.restore", map[string]any{"website": "blog.example.com", "backup": "nope.tar.gz"}, StatusRefused)

	f.FailRestore = true
	_, out = run(t, env, "site.restore", map[string]any{"website": "blog.example.com", "backup": list[0].FileName}, StatusFailed)
	if s.Restores != 1 || !strings.Contains(strings.Join(out.Log, "\n"), "decompress file failed") {
		t.Fatalf("failed restore: %d restores\n%s", s.Restores, strings.Join(out.Log, "\n"))
	}
}

func TestBackupSchedule(t *testing.T) {
	f := onepaneltest.New()
	s := f.AddSite("blog.example.com", "static", "")
	env, _ := siteEnv(t, f)
	ctx := context.Background()

	r, out := run(t, env, "site.backup.schedule", map[string]any{"website": "blog.example.com", "time": "04:30", "keep": 5}, StatusDone)
	j, found, err := BackupJob(ctx, env.OnePanel, "blog.example.com")
	if err != nil || !found {
		t.Fatalf("job not found: %v", err)
	}
	if j.Spec != "30 4 * * *" || j.RetainCopies != 5 || j.Type != "website" || j.SourceAccountIDs != "1" || j.DownloadAccountID != 1 || j.Website == "" {
		t.Fatalf("job = %+v", j)
	}
	if BackupClock(j.Spec) != "04:30" {
		t.Errorf("clock = %q", BackupClock(j.Spec))
	}
	if _, again := run(t, env, "site.backup.schedule", map[string]any{"website": "blog.example.com", "time": "4:30", "keep": 5}, StatusDone); len(again.Undo) != 0 {
		t.Fatalf("the same schedule again should change nothing: %+v", again)
	}

	// Switched off in 1Panel, the same schedule turns it back on; undo
	// switches it off again.
	f.Jobs[0]["status"] = "Disable"
	rOn, outOn := run(t, env, "site.backup.schedule", map[string]any{"website": "blog.example.com", "time": "04:30", "keep": 5}, StatusDone)
	if j, _, _ := BackupJob(ctx, env.OnePanel, "blog.example.com"); j.Status != "Enable" {
		t.Fatalf("still off: %+v", j)
	}
	undo(t, env, rOn, outOn)
	if j, _, _ := BackupJob(ctx, env.OnePanel, "blog.example.com"); j.Status != "Disable" {
		t.Fatalf("undo left it on: %+v", j)
	}
	f.Jobs[0]["status"] = "Enable"

	// Changing it to COS, then undoing, puts the old one back.
	r2, out2 := run(t, env, "site.backup.schedule", map[string]any{"website": "blog.example.com", "time": "02:00", "keep": 14, "account": "COS"}, StatusDone)
	j, _, _ = BackupJob(ctx, env.OnePanel, "blog.example.com")
	if j.Spec != "0 2 * * *" || j.RetainCopies != 14 || j.SourceAccountIDs != "2" {
		t.Fatalf("changed job = %+v", j)
	}
	undo(t, env, r2, out2)
	j, _, _ = BackupJob(ctx, env.OnePanel, "blog.example.com")
	if j.Spec != "30 4 * * *" || j.RetainCopies != 5 || j.SourceAccountIDs != "1" {
		t.Fatalf("undone job = %+v", j)
	}

	// Cancelling and undoing it.
	r3, out3 := run(t, env, "site.backup.unschedule", map[string]any{"website": "blog.example.com"}, StatusDone)
	if _, found, _ := BackupJob(ctx, env.OnePanel, "blog.example.com"); found {
		t.Fatal("job still there")
	}
	undo(t, env, r3, out3)
	if j, found, _ := BackupJob(ctx, env.OnePanel, "blog.example.com"); !found || j.Spec != "30 4 * * *" {
		t.Fatalf("job not back: %+v", j)
	}

	// Undoing the first step removes the job it created.
	undo(t, env, r, out)
	if len(f.Jobs) != 0 {
		t.Fatalf("jobs left: %+v", f.Jobs)
	}

	run(t, env, "site.backup.schedule", map[string]any{"website": "blog.example.com", "account": "OSS"}, StatusRefused)
	if _, err := Resolve("site.backup.schedule", map[string]any{"website": "blog.example.com", "time": "25:00"}, "1panel"); err == nil {
		t.Error("25:00 accepted")
	}
	_ = s
}
