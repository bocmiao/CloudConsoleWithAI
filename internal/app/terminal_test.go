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

// output collects what a terminal prints, and where the stream is.
type output struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	next  int64
	beats int // calls with no bytes
}

func (o *output) write(p []byte, next int64) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.buf.Write(p)
	o.next = next
	if len(p) == 0 {
		o.beats++
	}
	return nil
}

func (o *output) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
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
	oldIdle, oldBeat := termIdle, termBeat
	termIdle, termBeat = 200*time.Millisecond, 20*time.Millisecond
	defer func() { termIdle, termBeat = oldIdle, oldBeat }()
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
	type result struct {
		ended bool
		err   error
	}
	done := make(chan result, 1)
	go func() {
		ended, err := a.TerminalOutput(ctx, v.ID, -1, out.write)
		done <- result{ended, err}
	}()
	if err := a.TerminalInput(v.ID, "echo hello-$((40+2))\n"); err != nil {
		t.Fatal(err)
	}
	out.waitFor(t, "hello-42")
	// While nothing is printed the stream still says it is there.
	time.Sleep(120 * time.Millisecond)
	out.mu.Lock()
	beats, mark := out.beats, out.next
	out.mu.Unlock()
	if beats < 3 {
		t.Fatalf("only %d beats in 120ms", beats)
	}
	if err := a.ResizeTerminal(v.ID, 120, 40); err != nil {
		t.Fatal(err)
	}
	if err := a.ResizeTerminal(v.ID, 0, 0); err == nil {
		t.Fatal("zero size accepted")
	}

	// A second page attaching later gets the backlog.
	again := &output{}
	actx, stop := context.WithCancel(ctx)
	go func() { _, _ = a.TerminalOutput(actx, v.ID, -1, again.write) }()
	again.waitFor(t, "hello-42")
	stop()

	// A page that lost its connection picks up where it was: only what
	// came after, nothing twice.
	resumed := &output{}
	rctx, rstop := context.WithCancel(ctx)
	go func() { _, _ = a.TerminalOutput(rctx, v.ID, mark, resumed.write) }()
	_ = a.TerminalInput(v.ID, "echo after-$((1+1))\n")
	resumed.waitFor(t, "after-2")
	if strings.Contains(resumed.String(), "hello-42") {
		t.Fatalf("resumed stream repeated old output: %q", resumed.String())
	}
	rstop()

	_ = a.TerminalInput(v.ID, "exit\n")
	select {
	case r := <-done:
		if r.err != nil || !r.ended {
			t.Fatalf("stream ended with %v, ended %v", r.err, r.ended)
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
