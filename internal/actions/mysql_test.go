package actions

import (
	"context"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/onepanel/onepaneltest"
)

func TestMySQLDatabases(t *testing.T) {
	f := onepaneltest.New()
	f.MySQL = true
	env, _ := siteEnv(t, f)
	ctx := context.Background()
	names := func() map[string]string {
		list, err := env.OnePanel.MySQLDatabases(ctx, "mysql")
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, d := range list {
			out[d.Name] = d.Username + "@" + d.Permission
		}
		return out
	}

	r, out := run(t, env, "mysql.db.create", map[string]any{"app": "mysql", "name": "shop"}, StatusDone)
	// The random password is on the panel only, never in the record.
	if cmds := strings.Join(out.Commands, "\n"); !strings.Contains(cmds, `"password":"（不记录）"`) || strings.Count(cmds, `"password":"`) != strings.Count(cmds, `"password":"（不记录）"`) {
		t.Fatalf("commands = %s", cmds)
	}
	if got := names(); got["shop"] != "shop@%" {
		t.Fatalf("databases = %v", got)
	}
	if _, again := run(t, env, "mysql.db.create", map[string]any{"app": "mysql", "name": "shop"}, StatusDone); len(again.Undo) != 0 {
		t.Error("creating it twice changed something")
	}
	run(t, env, "mysql.db.create", map[string]any{"app": "mysql", "name": "blog", "user": "blog_rw", "access": "localhost"}, StatusDone)
	if got := names(); got["blog"] != "blog_rw@localhost" {
		t.Fatalf("databases = %v", got)
	}
	// Undoing the first backs shop up, then drops it.
	undo(t, env, r, out)
	if _, ok := names()["shop"]; ok {
		t.Fatal("shop still there")
	}
	if list, _ := env.OnePanel.BackupList(ctx, "mysql", "mysql", "shop"); len(list) != 1 {
		t.Fatalf("shop backups = %+v", list)
	}

	_, out = run(t, env, "mysql.db.delete", map[string]any{"app": "mysql", "name": "blog"}, StatusDone)
	if _, ok := names()["blog"]; ok || out.Result["backup"] == "" {
		t.Fatalf("blog not deleted with a backup: %v", out.Result)
	}
	run(t, env, "mysql.db.create", map[string]any{"app": "openresty", "name": "x"}, StatusRefused)
	if _, err := Resolve("mysql.db.create", map[string]any{"app": "mysql", "name": "bad-name"}, "1panel"); err == nil {
		t.Error("a name with - was accepted")
	}
}
