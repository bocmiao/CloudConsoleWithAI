package app

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx/sshtest"
)

// output collects what a terminal prints.
type output struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (o *output) write(p []byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.buf.Write(p)
	return nil
}

func (o *output) waitFor(t *testing.T, s string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		o.mu.Lock()
		got := o.buf.String()
		o.mu.Unlock()
		if strings.Contains(got, s) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	t.Fatalf("terminal never printed %q; got %q", s, o.buf.String())
}

func TestTerminal(t *testing.T) {
	old := termIdle
	termIdle = 200 * time.Millisecond
	defer func() { termIdle = old }()
	a := newApp(t)
	srv := sshtest.Start(t, "root", "pw")
	sv := addTestServer(t, a, srv, "pw")
	ctx := context.Background()

	v, err := a.OpenTerminal(ctx, sv.ID, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if list := a.Terminals(); len(list) != 1 || list[0].ServerName != "blog" {
		t.Fatalf("terminals = %+v", list)
	}
	out := &output{}
	done := make(chan error, 1)
	go func() { done <- a.TerminalOutput(ctx, v.ID, out.write) }()
	if err := a.TerminalInput(v.ID, "echo hello-$((40+2))\n"); err != nil {
		t.Fatal(err)
	}
	out.waitFor(t, "hello-42")
	if err := a.ResizeTerminal(v.ID, 120, 40); err != nil {
		t.Fatal(err)
	}
	if err := a.ResizeTerminal(v.ID, 0, 0); err == nil {
		t.Fatal("zero size accepted")
	}

	// A second page attaching later gets the backlog.
	again := &output{}
	actx, stop := context.WithCancel(ctx)
	go func() { _ = a.TerminalOutput(actx, v.ID, again.write) }()
	again.waitFor(t, "hello-42")
	stop()

	_ = a.TerminalInput(v.ID, "exit\n")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("output did not end with the shell")
	}
	out.waitFor(t, "[会话已结束]")
	if sizes := strings.Join(srv.Sizes(), ","); !strings.Contains(sizes, "xterm-256color 80x24") || !strings.Contains(sizes, "120x40") {
		t.Fatalf("sizes = %s", sizes)
	}
	waitGone := func(id string) {
		t.Helper()
		for i := 0; i < 200; i++ {
			if _, err := a.terminal(id); err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("terminal %s still open", id)
	}
	waitGone(v.ID)
	if err := a.TerminalInput(v.ID, "ls\n"); err == nil || !strings.Contains(err.Error(), "关闭") {
		t.Fatalf("input after exit: %v", err)
	}

	// A terminal no page attaches to is closed after a while.
	idle, err := a.OpenTerminal(ctx, sv.ID, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	waitGone(idle.ID)

	// Closing by hand.
	c, _ := a.OpenTerminal(ctx, sv.ID, 80, 24)
	if err := a.CloseTerminal(c.ID); err != nil || len(a.Terminals()) != 0 {
		t.Fatalf("close: %v %+v", err, a.Terminals())
	}
	var actions []string
	entries, _ := a.Store.ListAudit(20)
	for _, e := range entries {
		actions = append(actions, e.Action)
	}
	if got := strings.Join(actions, ","); strings.Count(got, "terminal.open") != 3 || !strings.Contains(got, "terminal.close") {
		t.Fatalf("audit = %s", got)
	}
}
