package actions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/freecmd"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx/sshtest"
	"github.com/bocmiao/CloudConsoleWithAI/scripts"
)

func TestParseDryRun(t *testing.T) {
	d := parseDryRun("MIAO_HASH 1 2 /etc/a.conf\nMIAO_HASH absent - /etc/b.conf\nMIAO_DRY ok\nMIAO_DRY_RC 3\nMIAO_FILE /etc/a.conf\nMIAO_D -old\nMIAO_D  same\nMIAO_SVC systemctl reload nginx\nMIAO_EXTRA /etc/c\nMIAO_O hello\nMIAO_O \n")
	if !d.Supported || d.ExitCode != 3 || len(d.Files) != 1 || d.Files[0].Diff != "-old\n same\n" || d.Services[0] != "systemctl reload nginx" ||
		d.Extra[0] != "/etc/c" || d.Expect != "1 2 /etc/a.conf\nabsent - /etc/b.conf" || d.Output != "hello\n" {
		t.Fatalf("%+v", d)
	}
	if u := parseDryRun("MIAO_DRY unsupported 没有 overlay\n"); u.Supported || u.Reason != "没有 overlay" {
		t.Fatalf("%+v", u)
	}
}

// canDryRun reports whether this machine can run the real script: it needs
// root, unshare and overlayfs, like the servers it runs on.
func canDryRun() bool {
	fs, _ := os.ReadFile("/proc/filesystems")
	_, err := exec.LookPath("unshare")
	return os.Geteuid() == 0 && err == nil && strings.Contains(string(fs), "overlay")
}

// The real free_command.sh, end to end over SSH: dry run changes nothing,
// apply changes the file and is confirmed, undo restores it.
func TestFreeCommandForReal(t *testing.T) {
	if !canDryRun() {
		t.Skip("needs root, unshare and overlayfs")
	}
	dir := t.TempDir()
	t.Cleanup(freecmd.AllowForTests(dir))
	srv := sshtest.Start(t, "root", "pw")
	c, err := sshx.Dial(context.Background(), sshx.Target{Host: srv.Host, Port: srv.Port, User: "root", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	t.Cleanup(UseTestScripts(t.TempDir(), scripts.Action))
	env := &Env{SSH: c, User: "root", PollInterval: 50 * time.Millisecond}
	ctx := context.Background()

	conf := filepath.Join(dir, "site.conf")
	if err := os.WriteFile(conf, []byte("gzip off;\nworkers 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	added := filepath.Join(dir, "extra", "gzip.conf")
	params := map[string]any{
		"goal":   "开启 gzip",
		"script": "sed -i 's/gzip off;/gzip on;/' " + conf + "\nmkdir -p " + filepath.Dir(added) + "\ncat > " + added + " <<'EOF'\ngzip_types text/css;\nEOF\necho checked",
		"files":  conf + "," + added,
	}
	r, err := Resolve("free_command", params, "linux")
	if err != nil {
		t.Fatal(err)
	}
	d, err := DryRun(ctx, env, r)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Supported || d.ExitCode != 0 || len(d.Files) != 2 || !strings.Contains(d.Files[0].Diff, "+gzip on;") ||
		!strings.Contains(d.Files[1].Diff, "+gzip_types text/css;") || len(d.Extra) != 0 || d.Output != "checked" {
		t.Fatalf("dry run = %+v", d)
	}
	if b, _ := os.ReadFile(conf); string(b) != "gzip off;\nworkers 1;\n" {
		t.Fatalf("dry run changed the file: %q", b)
	}
	if _, err := os.Stat(added); err == nil {
		t.Fatal("dry run created a file")
	}

	params["expect"] = d.Expect
	r, _ = Resolve("free_command", params, "linux")
	out := Apply(ctx, env, r, nil)
	log := strings.Join(out.Log, "\n")
	if out.Status != StatusDone || !strings.Contains(log, "取消了 5 分钟保险") || out.RollbackFile == "" {
		t.Fatalf("apply = %+v", out)
	}
	if b, _ := os.ReadFile(conf); !strings.Contains(string(b), "gzip on;") {
		t.Fatalf("not applied: %q", b)
	}
	u := Undo(ctx, env, r, out.Undo)
	if u.Status != StatusUndone {
		t.Fatalf("undo = %+v", u)
	}
	if b, _ := os.ReadFile(conf); string(b) != "gzip off;\nworkers 1;\n" {
		t.Fatalf("undo did not restore: %q", b)
	}
	if _, err := os.Stat(added); err == nil {
		t.Fatal("undo left the new file")
	}

	// Changed since the dry run: refused.
	_ = os.WriteFile(conf, []byte("gzip off;\nworkers 2;\n"), 0o644)
	if o := Apply(ctx, env, r, nil); o.Status != StatusRefused || !strings.Contains(strings.Join(o.Log, ""), "试运行之后被改动过") {
		t.Fatalf("stale apply = %+v", o)
	}

	// A command that fails is restored at once.
	_ = os.WriteFile(conf, []byte("gzip off;\n"), 0o644)
	params["script"] = "sed -i 's/gzip off;/broken/' " + conf + "\nmkdir -p " + filepath.Dir(added) + "\ntouch " + added + "\nfalse"
	delete(params, "expect")
	r, _ = Resolve("free_command", params, "linux")
	if o := Apply(ctx, env, r, nil); o.Status != StatusRolledBack {
		t.Fatalf("failing command = %+v", o)
	}
	if b, _ := os.ReadFile(conf); string(b) != "gzip off;\n" {
		t.Fatalf("not restored after failure: %q", b)
	}

	// Writing through a symbolic link would reach a file outside the
	// dry-run layer and the backup: refused, before anything runs.
	real := filepath.Join(dir, "real.conf")
	_ = os.WriteFile(real, []byte("keep\n"), 0o644)
	link := filepath.Join(dir, "link.conf")
	_ = os.Symlink(real, link)
	params = map[string]any{"goal": "写入", "script": "echo changed > " + link, "files": link}
	r, err = Resolve("free_command", params, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if d, err := DryRun(ctx, env, r); err == nil && d.Supported {
		t.Fatalf("dry run through a link = %+v", d)
	}
	if o := Apply(ctx, env, r, nil); o.Status != StatusRefused || !strings.Contains(strings.Join(o.Log, ""), "软链接") {
		t.Fatalf("apply through a link = %+v", o)
	}
	if b, _ := os.ReadFile(real); string(b) != "keep\n" {
		t.Fatalf("the link's target changed: %q", b)
	}
}
