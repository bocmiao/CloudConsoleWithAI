package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
)

func TestSiteBackups(t *testing.T) {
	a := newApp(t)
	sv, f := panelServer(t, a)
	blog := f.AddSite("blog.example.com", "static", "")
	ctx := context.Background()

	v, err := a.SiteBackups(ctx, sv.ID, uint(blog.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Backups) != 0 || v.Schedule != nil || len(v.Accounts) != 2 {
		t.Fatalf("empty = %+v", v)
	}

	run := func(req SiteRequest) {
		t.Helper()
		req.ServerID, req.Site = sv.ID, "blog.example.com"
		p, err := a.ProposeWebsite(ctx, req)
		if err != nil {
			t.Fatalf("%s: %v", req.Op, err)
		}
		if _, err := a.ExecutePlan(p.ID, []int{0}); err != nil {
			t.Fatal(err)
		}
		if done := waitPlan(t, a, p.ID); done.StepList[0].Status != actions.StatusDone {
			t.Fatalf("%s: %+v", req.Op, done.StepList[0])
		}
		// Running a checklist looks at the server again, and this one has no real 1Panel.
		_ = a.Store.SaveProfile(sv.ID, "", "1panel")
	}
	run(SiteRequest{Op: "backup", Note: "改版前"})
	run(SiteRequest{Op: "backup_schedule", Time: "04:00", Keep: 3, Account: "cos-backup"})
	// A 1Panel task of its own that backs up every site.
	f.Jobs = append(f.Jobs, map[string]any{"id": 900, "name": "全部网站", "type": "website", "website": "all", "spec": "0 1 * * 0", "retainCopies": 2, "sourceAccountIDs": "1"})

	v, err = a.SiteBackups(ctx, sv.ID, uint(blog.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Backups) != 1 || v.Backups[0].Note != "改版前" || !v.Backups[0].Local || v.Backups[0].Account != "服务器本机" || v.Backups[0].Automatic {
		t.Fatalf("backups = %+v", v.Backups)
	}
	if s := v.Schedule; s == nil || s.Time != "04:00" || s.Keep != 3 || s.Account != "cos-backup（COS）" || !s.Enabled {
		t.Fatalf("schedule = %+v", v.Schedule)
	}
	if len(v.Others) != 1 || v.Others[0].Name != "全部网站" || v.Others[0].Time != "" {
		t.Fatalf("others = %+v", v.Others)
	}

	path, err := a.BackupPath(ctx, sv.ID, uint(blog.ID), v.Backups[0].ID)
	if err != nil || !strings.HasPrefix(path, "/opt/1panel/backup/website/") {
		t.Fatalf("path = %q, %v", path, err)
	}
	if _, err := a.BackupPath(ctx, sv.ID, uint(blog.ID), 12345); err == nil {
		t.Error("a missing backup was found")
	}

	run(SiteRequest{Op: "restore", Backup: v.Backups[0].File, When: "今天的备份"})
	if blog.Restores != 1 {
		t.Fatalf("restores = %d", blog.Restores)
	}

	raw, _ := json.Marshal(map[string]any{"server_id": sv.ID, "website": "blog.example.com"})
	text, err := a.toolPanelBackups(ctx, raw)
	if err != nil || !strings.Contains(text, "每天 04:00") || !strings.Contains(text, "file="+v.Backups[0].File) || !strings.Contains(text, "全部网站") {
		t.Fatalf("tool = %s, %v", text, err)
	}
}
