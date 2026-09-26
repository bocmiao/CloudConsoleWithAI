package app

import (
	"context"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx/sshtest"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent/tencenttest"
)

func TestRealIP(t *testing.T) {
	a := newApp(t)
	srv := sshtest.Start(t, "root", "pw")
	sv := addTestServer(t, a, srv, "pw")
	ctx := context.Background()
	if _, err := a.ProposeRealIP(ctx, sv.ID); err == nil {
		t.Fatal("proposed without Tencent Cloud")
	}
	f := tencenttest.Start(t)
	a.TencentEndpoint = f.Endpoint
	if _, err := a.SaveTencent(tencenttest.SecretID, tencenttest.SecretKey); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ProposeRealIP(ctx, sv.ID); err == nil || !strings.Contains(err.Error(), "没有回源到") {
		t.Fatalf("no domains fetch from the server: %v", err)
	}
	f.Domains = append(f.Domains,
		&tencenttest.Domain{Zone: "zone-abc", Name: "blog.example.com", Status: "online", Origin: srv.Host, Protocol: "HTTP"},
		&tencenttest.Domain{Zone: "zone-abc", Name: "shop.example.com", Status: "online", Origin: "9.9.9.9", Protocol: "HTTP"})

	v, err := a.ProposeRealIP(ctx, sv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.StepList) != 2 || v.StepList[0].Capability != "nginx.realip" || v.StepList[1].Capability != "eo.clientip.header" ||
		v.StepList[1].Params["domain"] != "example.com" || !strings.Contains(v.Reason, "blog.example.com") || strings.Contains(v.Reason, "shop") {
		t.Fatalf("plan = %+v", v)
	}
	header, _ := v.StepList[0].Params["header"].(string)
	if !strings.HasPrefix(header, "X-Miao-IP-") || len(header) != len("X-Miao-IP-")+24 || v.StepList[1].Params["header"] != header ||
		!v.StepList[0].Executable || !v.StepList[1].Executable {
		t.Fatalf("header = %q, steps = %+v", header, v.StepList)
	}
	// The name is made once; the AI cannot choose it.
	steps := v.StepList
	steps[0].Params = map[string]any{"header": "X-Miao-IP-chosenbytheai0000"}
	p, got, err := a.proposePlan(ctx, "ai", sv.ID, "t", "r", steps)
	if err != nil || got[0].Params["header"] != header || p.ID == v.ID {
		t.Fatalf("AI step header = %v %v", got[0].Params["header"], err)
	}

	// EdgeOne: on with the secret name, and back off on undo.
	if _, err := a.ExecutePlan(v.ID, []int{1}); err != nil {
		t.Fatal(err)
	}
	done := waitPlan(t, a, v.ID)
	if st := done.StepList[1]; st.Status != actions.StatusDone || f.ClientIPHeaders["zone-abc"]["HeaderName"] != header || f.ClientIPHeaders["zone-abc"]["Switch"] != "on" {
		t.Fatalf("eo step = %+v, setting = %v", st, f.ClientIPHeaders)
	}
	if _, err := a.UndoStep(ctx, v.ID, 1); err != nil {
		t.Fatal(err)
	}
	if u := waitPlan(t, a, v.ID); u.StepList[1].Status != actions.StatusUndone || f.ClientIPHeaders["zone-abc"]["Switch"] != "off" {
		t.Fatalf("undo = %+v, setting = %v", u.StepList[1], f.ClientIPHeaders)
	}

	// A site already sending the IP under another name is left alone.
	f.ClientIPHeaders["zone-abc"] = map[string]any{"Switch": "on", "HeaderName": "EO-Client-IP"}
	v2, _ := a.ProposeRealIP(ctx, sv.ID)
	if _, err := a.ExecutePlan(v2.ID, []int{1}); err != nil {
		t.Fatal(err)
	}
	if st := waitPlan(t, a, v2.ID).StepList[1]; st.Status != actions.StatusRefused || f.ClientIPHeaders["zone-abc"]["HeaderName"] != "EO-Client-IP" ||
		!strings.Contains(strings.Join(st.Log, ""), "EO-Client-IP") {
		t.Fatalf("other header = %+v", st)
	}

	// The stats page learns the server records real IPs once Nginx does.
	if a.realIPSince(sv.ID) != "" {
		t.Fatal("real IPs before the Nginx step ran")
	}
	pl, _ := a.Store.GetPlan(v.ID)
	pl.Steps = strings.Replace(pl.Steps, `"capability":"nginx.realip"`, `"capability":"nginx.realip","status":"done","finishedAt":"2026-09-26T01:00:00Z"`, 1)
	if err := a.Store.UpdatePlan(pl); err != nil {
		t.Fatal(err)
	}
	if a.realIPSince(sv.ID) == "" {
		t.Fatalf("done step not seen: %s", pl.Steps)
	}
}
