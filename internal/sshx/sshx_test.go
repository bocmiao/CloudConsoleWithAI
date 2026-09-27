package sshx_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx/sshtest"
	"github.com/bocmiao/CloudConsoleWithAI/scripts"
)

func dial(t *testing.T, srv *sshtest.Server, password, known string) (*sshx.Client, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return sshx.Dial(ctx, sshx.Target{
		Host: srv.Host, Port: srv.Port, User: "root", Password: password, KnownHostKey: known,
	})
}

func TestDialRecordsAndPinsHostKey(t *testing.T) {
	srv := sshtest.Start(t, "root", "pw")

	c, err := dial(t, srv, "pw", "")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if c.HostKey != srv.HostKey {
		t.Fatalf("host key = %q, want %q", c.HostKey, srv.HostKey)
	}

	c, err = dial(t, srv, "pw", srv.HostKey)
	if err != nil {
		t.Fatalf("known key should be accepted: %v", err)
	}
	c.Close()

	if _, err := dial(t, srv, "pw", "SHA256:somethingelse"); !errors.Is(err, sshx.ErrHostKeyChanged) {
		t.Fatalf("changed key: got %v, want ErrHostKeyChanged", err)
	}
}

func TestDialWrongPassword(t *testing.T) {
	srv := sshtest.Start(t, "root", "pw")
	_, err := dial(t, srv, "nope", "")
	if err == nil || !strings.Contains(err.Error(), "unable to authenticate") {
		t.Fatalf("got %v, want authentication failure", err)
	}
}

func TestRunExitCodeStdinAndLimit(t *testing.T) {
	srv := sshtest.Start(t, "root", "pw")
	c, err := dial(t, srv, "pw", "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()

	res, err := c.Run(ctx, "cat; echo oops >&2; exit 3", "hello", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stdout != "hello" || strings.TrimSpace(res.Stderr) != "oops" || res.ExitCode != 3 {
		t.Fatalf("unexpected result %+v", res)
	}

	res, err = c.Run(ctx, "printf 0123456789", "", 4)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stdout != "0123" || !res.Truncated {
		t.Fatalf("limit not applied: %+v", res)
	}
}

func TestRunScriptDiscover(t *testing.T) {
	srv := sshtest.Start(t, "root", "pw")
	c, err := dial(t, srv, "pw", "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	res, err := c.RunScript(context.Background(), "root", scripts.Discover, []string{"system", "panel"}, 256<<10)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "== system ==") || !strings.Contains(res.Stdout, "== panel ==") {
		t.Fatalf("unexpected discover output (exit %d):\n%s\n%s", res.ExitCode, res.Stdout, res.Stderr)
	}
	if strings.Contains(res.Stdout, "== docker ==") {
		t.Fatal("sections were not limited")
	}
}

// freezer forwards TCP connections to target until frozen, then drops
// everything silently, like a NAT that forgot the connection.
type freezer struct {
	ln     net.Listener
	frozen atomic.Bool
}

func startFreezer(t *testing.T, target string) *freezer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &freezer{ln: ln}
	pipe := func(dst, src net.Conn) {
		buf := make([]byte, 32<<10)
		for {
			n, err := src.Read(buf)
			if err != nil {
				dst.Close()
				return
			}
			if !f.frozen.Load() {
				_, _ = dst.Write(buf[:n])
			}
		}
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			d, err := net.Dial("tcp", target)
			if err != nil {
				c.Close()
				continue
			}
			go pipe(c, d)
			go pipe(d, c)
		}
	}()
	return f
}

func TestKeepAliveNoticesADeadConnection(t *testing.T) {
	srv := sshtest.Start(t, "root", "pw")
	f := startFreezer(t, net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port)))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := sshx.Dial(ctx, sshx.Target{Host: "127.0.0.1", Port: f.ln.Addr().(*net.TCPAddr).Port, User: "root", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sh, err := c.Shell(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, sh.Output) }()
	c.KeepAlive(40 * time.Millisecond)
	// Answered checks keep it open.
	select {
	case <-sh.Done():
		t.Fatal("a live connection was closed")
	case <-time.After(400 * time.Millisecond):
	}
	// Unanswered ones close it, and the shell with it.
	f.frozen.Store(true)
	select {
	case <-sh.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a dead connection stayed open")
	}
}
