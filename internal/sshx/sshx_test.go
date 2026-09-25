package sshx_test

import (
	"context"
	"errors"
	"strings"
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
