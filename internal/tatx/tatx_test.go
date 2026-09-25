package tatx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent/tencenttest"
)

func conn(t *testing.T) (*Conn, *tencenttest.Fake) {
	f := tencenttest.Start(t)
	return &Conn{Cloud: f.Client(), Region: "ap-guangzhou", Instance: "lhins-abc12345", Poll: time.Millisecond}, f
}

func TestRunPassesStdinAndExitCode(t *testing.T) {
	c, _ := conn(t)
	res, err := c.Run(context.Background(), `read x; echo "got $x"; echo oops >&2; exit 3`, "hello\n", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stdout != "got hello\n" || res.Stderr != "oops\n" || res.ExitCode != 3 {
		t.Fatalf("got %+v", res)
	}
	if matches, _ := filepath.Glob("/tmp/miaopanel.*"); len(matches) > 0 {
		for _, m := range matches {
			if st, err := os.Stat(m); err == nil && time.Since(st.ModTime()) < time.Minute {
				t.Fatalf("temporary directory left behind: %s", m)
			}
		}
	}
}

func TestLargeOutputIsFetchedInPieces(t *testing.T) {
	c, f := conn(t)
	// Random-looking output does not compress, so it needs several reads.
	res, err := c.Run(context.Background(), `head -c 60000 /dev/urandom | od -An -tx1 | tr -d ' \n'`, "", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stdout) != 120000 || res.Truncated {
		t.Fatalf("got %d bytes, truncated %v", len(res.Stdout), res.Truncated)
	}
	runs := 0
	for _, call := range f.Calls {
		if call == "tat RunCommand" {
			runs++
		}
	}
	if runs < 4 {
		t.Fatalf("expected the output to be read in pieces, got %d commands", runs)
	}
	res, err = c.Run(context.Background(), `head -c 60000 /dev/zero | tr '\0' a`, "", 1000)
	if err != nil || len(res.Stdout) != 1000 || !res.Truncated {
		t.Fatalf("maxOut not applied: %d %v %v", len(res.Stdout), res.Truncated, err)
	}
}

func TestScriptFromStdin(t *testing.T) {
	c, _ := conn(t)
	res, err := c.RunScript(context.Background(), "root", "echo \"args: $1 $2\"; id -u >/dev/null", []string{"a", "b"}, 4096)
	if err != nil || strings.TrimSpace(res.Stdout) != "args: a b" {
		t.Fatalf("got %+v, %v", res, err)
	}
}

func TestAgentOffline(t *testing.T) {
	c, f := conn(t)
	f.AgentOnline["lhins-abc12345"] = false
	_, err := c.Run(context.Background(), "true", "", 100)
	if err == nil || !strings.Contains(err.Error(), "不在线") {
		t.Fatalf("got %v", err)
	}
}
