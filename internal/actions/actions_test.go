package actions

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx/sshtest"
)

func TestResolveValidatesParams(t *testing.T) {
	r, err := Resolve("swap.set", map[string]any{"size_gb": float64(2)}, "1panel")
	if err != nil || r.Values["size_gb"] != "2" || r.Impl.Script != "swap.sh" {
		t.Fatalf("swap.set: %+v %v", r, err)
	}
	r, err = Resolve("php_fpm.set", map[string]any{"max_children": "12"}, "linux")
	if err != nil || r.Values["pm"] != "dynamic" || r.Impl.Via != "系统脚本" {
		t.Fatalf("php_fpm.set default pm: %+v %v", r, err)
	}
	r, err = Resolve("php_fpm.set", map[string]any{"max_children": 12, "pm": "ondemand"}, "1panel")
	if err != nil || r.Impl.Panel != "php_fpm" {
		t.Fatalf("php_fpm.set on 1panel: %+v %v", r, err)
	}

	for _, c := range []struct {
		capability string
		params     map[string]any
		adapter    string
		want       string
	}{
		{"swap.set", map[string]any{"size_gb": float64(64)}, "linux", "1 到 16"},
		{"swap.set", map[string]any{"size_gb": 1.5}, "linux", "整数"},
		{"swap.set", map[string]any{}, "linux", "缺少参数"},
		{"swap.set", map[string]any{"size_gb": 2, "path": "/x"}, "linux", "没有参数 path"},
		{"service.restart", map[string]any{"name": "nginx; rm -rf /"}, "linux", "只能包含"},
		{"php_fpm.set", map[string]any{"max_children": 10, "pm": "fast"}, "linux", "只能是"},
		{"php_fpm.set", map[string]any{"max_children": 10}, "bt", "宝塔"},
		{"mysql.vars.set", map[string]any{"max_connections": 200}, "linux", "纯 Linux"},
		{"mysql.vars.set", map[string]any{}, "1panel", "至少要改"},
		{"free_command", nil, "linux", "自由命令"},
		{"server.reboot", nil, "linux", "还不能自动执行"},
	} {
		_, err := Resolve(c.capability, c.params, c.adapter)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %v on %s: err = %v, want %q", c.capability, c.params, c.adapter, err, c.want)
		}
	}
	var ne *ErrNotExecutable
	if _, err := Resolve("free_command", nil, "linux"); !errors.As(err, &ne) {
		t.Errorf("free_command should be ErrNotExecutable, got %T", err)
	}
}

func TestFPMParamsAreConsistent(t *testing.T) {
	for _, n := range []int{2, 3, 5, 8, 12, 50, 500} {
		p := fpmParams(n, "dynamic")
		atoi := func(k string) int {
			var v int
			for _, ch := range p[k] {
				v = v*10 + int(ch-'0')
			}
			return v
		}
		minS, start, maxS := atoi("pm.min_spare_servers"), atoi("pm.start_servers"), atoi("pm.max_spare_servers")
		if !(minS >= 1 && minS <= start && start <= maxS && maxS <= n) {
			t.Errorf("max_children=%d: min=%d start=%d max=%d", n, minS, start, maxS)
		}
	}
	if p := fpmParams(10, "ondemand"); p["pm.process_idle_timeout"] != "10s" || p["pm.start_servers"] != "" {
		t.Errorf("ondemand params = %v", p)
	}
}

func TestParseProtocol(t *testing.T) {
	o := parseProtocol("MIAO_INFO 开始\nMIAO_UNDO swapfile=/swapfile\nMIAO_UNDO backup:/etc/fstab=/b/_etc_fstab\nswapon: failed\nMIAO_RESULT swap_total_mb=1023\n\n")
	if strings.Join(o.Log, "|") != "开始|swapon: failed" || o.Undo["swapfile"] != "/swapfile" ||
		o.Undo["backup:/etc/fstab"] != "/b/_etc_fstab" || o.Result["swap_total_mb"] != "1023" {
		t.Fatalf("parsed = %+v", o)
	}
}

// testScript stands in for a real action: it echoes its arguments and the
// undo data it was given, and exits with the code passed in args.
const testScript = `
case "$1" in
apply) printf 'MIAO_INFO arg=[%s]\n' "$2"; printf 'MIAO_UNDO token=%s\n' "$2"; exit "$3" ;;
undo) printf 'MIAO_INFO undo token=[%s]\n' "$UNDO_token"; exit 0 ;;
esac
`

func testEnv(t *testing.T) *Env {
	t.Helper()
	srv := sshtest.Start(t, "root", "pw")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := sshx.Dial(ctx, sshx.Target{Host: srv.Host, Port: srv.Port, User: "root", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	old := loadScript
	loadScript = func(string) (string, error) { return testScript, nil }
	t.Cleanup(func() { loadScript = old })
	return &Env{SSH: c, User: "root", PollInterval: 50 * time.Millisecond}
}

func TestRunScriptDetachedApplyAndUndo(t *testing.T) {
	env := testEnv(t)
	ctx := context.Background()
	cap := &Capability{Name: "t", Reversible: true}
	tricky := `it's "quoted" $(not run) & ok`
	r := Resolved{Cap: cap, Impl: Impl{Script: "t.sh", Args: []string{"x", "code"}}, Values: map[string]string{"x": tricky, "code": "0"}}

	var progressed bool
	out := Apply(ctx, env, r, func([]string) { progressed = true })
	if out.Status != StatusDone || out.Undo["token"] != tricky || !progressed {
		t.Fatalf("apply: %+v progressed=%v", out, progressed)
	}
	if !strings.Contains(strings.Join(out.Log, "\n"), "arg=["+tricky+"]") {
		t.Fatalf("argument was not passed intact: %v", out.Log)
	}

	undone := Undo(ctx, env, r, out.Undo)
	if undone.Status != StatusUndone || !strings.Contains(strings.Join(undone.Log, "\n"), "undo token=["+tricky+"]") {
		t.Fatalf("undo: %+v", undone)
	}

	r.Values["code"] = "20"
	if out := Apply(ctx, env, r, nil); out.Status != StatusRolledBack {
		t.Fatalf("exit 20 should be rolled back, got %+v", out)
	}
	r.Values["code"] = "10"
	if out := Apply(ctx, env, r, nil); out.Status != StatusRefused {
		t.Fatalf("exit 10 should be refused, got %+v", out)
	}
}

func TestUndoIrreversibleIsRefused(t *testing.T) {
	r, _ := Resolve("logs.clean", nil, "linux")
	if out := Undo(context.Background(), &Env{}, r, nil); out.Status != StatusRefused {
		t.Fatalf("got %+v", out)
	}
}

func TestPanelStepWithoutAPIIsRefused(t *testing.T) {
	r, err := Resolve("mysql.vars.set", map[string]any{"max_connections": 300}, "1panel")
	if err != nil {
		t.Fatal(err)
	}
	out := Apply(context.Background(), &Env{}, r, nil)
	if out.Status != StatusRefused || !strings.Contains(strings.Join(out.Log, ""), "API 密钥") {
		t.Fatalf("got %+v", out)
	}
}

func TestMySQLAppSelection(t *testing.T) {
	if typ, name, err := mysqlApp([]string{"openresty/openresty", "mysql/mysql"}, ""); err != nil || typ != "mysql" || name != "mysql" {
		t.Fatalf("single app: %s %s %v", typ, name, err)
	}
	if _, _, err := mysqlApp([]string{"mysql/a", "mariadb/b"}, ""); err == nil {
		t.Fatal("two apps without a name should fail")
	}
	if typ, name, err := mysqlApp([]string{"mysql/a", "mariadb/b"}, "b"); err != nil || typ != "mariadb" || name != "b" {
		t.Fatalf("named app: %s %s %v", typ, name, err)
	}
}
