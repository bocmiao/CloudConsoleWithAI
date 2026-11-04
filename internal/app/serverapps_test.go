package app

import (
	"context"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
)

func TestServerAppsPage(t *testing.T) {
	a := newApp(t)
	sv, f := panelServer(t, a)
	f.MySQL = true
	f.DBs = append(f.DBs, map[string]any{"id": 900, "name": "halo", "username": "halo", "permission": "%", "database": "mysql", "createdAt": "2026-09-01T10:00:00+08:00"})
	ctx := context.Background()

	v, err := a.ServerDatabases(ctx, sv.ID)
	if err != nil || len(v) != 1 || v[0].App != "mysql" || len(v[0].Databases) != 1 || v[0].Databases[0].User != "halo" || v[0].Databases[0].CreatedAt == "" {
		t.Fatalf("databases = %+v, %v", v, err)
	}
	run := func(req AppRequest) PlanView {
		t.Helper()
		p, err := a.ProposeServerApps(ctx, sv.ID, req)
		if err != nil {
			t.Fatalf("%s: %v", req.Op, err)
		}
		if _, err := a.ExecutePlan(p.ID, []int{0}); err != nil {
			t.Fatal(err)
		}
		done := waitPlan(t, a, p.ID)
		if done.StepList[0].Status != actions.StatusDone {
			t.Fatalf("%s: %+v", req.Op, done.StepList[0])
		}
		_ = a.Store.SaveProfile(sv.ID, "", "1panel")
		return done
	}
	run(AppRequest{Op: "db_create", App: "mysql", Name: "shop", User: "shop_rw"})
	if v, _ = a.ServerDatabases(ctx, sv.ID); len(v[0].Databases) != 2 {
		t.Fatalf("after create = %+v", v)
	}
	run(AppRequest{Op: "db_backup", App: "mysql", Name: "shop"})
	run(AppRequest{Op: "db_delete", App: "mysql", Name: "shop"})
	if v, _ = a.ServerDatabases(ctx, sv.ID); len(v[0].Databases) != 1 {
		t.Fatalf("after delete = %+v", v)
	}
	for _, bad := range []AppRequest{{Op: "db_create", App: "mysql", Name: "bad-name"}, {Op: "reboot", Name: "x"}, {Op: "service_restart"}, {Op: "service_restart", Name: "sshd"}} {
		if _, err := a.ProposeServerApps(ctx, sv.ID, bad); err == nil {
			t.Errorf("%+v was proposed", bad)
		}
	}
	p, err := a.ProposeServerApps(ctx, sv.ID, AppRequest{Op: "container_restart", Name: "1Panel-mysql-abcd"})
	if err != nil || p.StepList[0].Capability != "container.restart" {
		t.Fatalf("container plan = %+v, %v", p, err)
	}
}
