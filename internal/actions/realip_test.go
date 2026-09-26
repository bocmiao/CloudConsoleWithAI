package actions

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx/sshtest"
	"github.com/bocmiao/CloudConsoleWithAI/scripts"
)

// TestNginxRealIPForReal runs nginx_realip.sh against a separate Nginx.
func TestNginxRealIPForReal(t *testing.T) {
	if _, err := exec.LookPath("nginx"); err != nil || os.Geteuid() != 0 {
		t.Skip("needs root and nginx")
	}
	dir := t.TempDir()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	conf := filepath.Join(dir, "nginx.conf")
	_ = os.MkdirAll(filepath.Join(dir, "conf.d"), 0o755)
	_ = os.WriteFile(conf, []byte(`pid `+dir+`/nginx.pid;
error_log `+dir+`/error.log;
events { worker_connections 64; }
http {
  access_log off;
  client_body_temp_path `+dir+`; proxy_temp_path `+dir+`; fastcgi_temp_path `+dir+`; uwsgi_temp_path `+dir+`; scgi_temp_path `+dir+`;
  include `+dir+`/conf.d/*.conf;
}
`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "conf.d", "site.conf"), []byte("server { listen 127.0.0.1:"+strconv.Itoa(port)+"; location / { return 200 $remote_addr; } }\n"), 0o644)
	if out, err := exec.Command("nginx", "-c", conf).CombinedOutput(); err != nil {
		t.Skipf("cannot start nginx: %s", out)
	}
	t.Cleanup(func() { _ = exec.Command("nginx", "-c", conf, "-s", "stop").Run() })

	srv := sshtest.Start(t, "root", "pw")
	c, err := sshx.Dial(context.Background(), sshx.Target{Host: srv.Host, Port: srv.Port, User: "root", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	t.Cleanup(UseTestScripts(t.TempDir(), func(name string) (string, error) {
		s, err := scripts.Action(name)
		return "MIAO_NGINX_CONF=" + conf + "; export MIAO_NGINX_CONF\n" + s, err
	}))
	env := &Env{SSH: c, User: "root", PollInterval: 50 * time.Millisecond}
	ctx := context.Background()
	const header = "X-Miao-IP-0123456789abcdef01234567"
	// visitor asks the site with one header; it answers with $remote_addr.
	visitor := func(name, value, want string) {
		t.Helper()
		var got string
		for i := 0; i < 40; i++ { // a reload takes a moment
			req, _ := http.NewRequest("GET", "http://127.0.0.1:"+strconv.Itoa(port)+"/", nil)
			req.Header.Set(name, value)
			if resp, err := http.DefaultClient.Do(req); err == nil {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if got = string(b); got == want {
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("%s: %s → %q, want %q", name, value, got, want)
	}

	r, err := Resolve("nginx.realip", map[string]any{"header": header}, "1panel")
	if err != nil {
		t.Fatal(err)
	}
	out := Apply(ctx, env, r, nil)
	file := filepath.Join(dir, "conf.d", "00-miaopanel-realip.conf")
	if out.Status != StatusDone || out.Undo["file"] != file {
		t.Fatalf("apply: %+v", out)
	}
	visitor(header, "203.0.113.9", "203.0.113.9")
	// Anyone else's claims are ignored.
	visitor("X-Forwarded-For", "6.6.6.6", "127.0.0.1")
	visitor("EO-Connecting-IP", "6.6.6.6", "127.0.0.1")
	if again := Apply(ctx, env, r, nil); again.Status != StatusDone || len(again.Undo) != 0 {
		t.Fatalf("second apply: %+v", again)
	}

	if u := Undo(ctx, env, r, out.Undo); u.Status != StatusUndone {
		t.Fatalf("undo: %+v", u)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("file still there")
	}
	visitor(header, "203.0.113.9", "127.0.0.1")

	// Another real_ip_header is not overridden.
	_ = os.WriteFile(filepath.Join(dir, "conf.d", "other.conf"), []byte("real_ip_header X-Real-IP;\n"), 0o644)
	if out := Apply(ctx, env, r, nil); out.Status != StatusRefused || !strings.Contains(strings.Join(out.Log, ""), "other.conf") {
		t.Fatalf("conflict: %+v", out)
	}
	_ = os.Remove(filepath.Join(dir, "conf.d", "other.conf"))
	bad, _ := Resolve("nginx.realip", map[string]any{"header": "X-Real-IP"}, "linux")
	if out := Apply(ctx, env, bad, nil); out.Status != StatusRefused {
		t.Fatalf("plain header name: %+v", out)
	}
}
