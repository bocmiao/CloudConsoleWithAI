package app

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/internal/update"
)

// fakeRelease serves v0.3.0 with this system's program, GitHub-like.
func fakeRelease(t *testing.T, program []byte) *update.Checker {
	sum := sha256.Sum256(program)
	name := update.AssetName(runtime.GOOS, runtime.GOARCH)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			fmt.Fprintf(w, `{"tag_name":"v0.3.0","html_url":"https://example.com/v0.3.0","body":"## 新功能\n\n- 监控和备份\n- 更多","assets":[
				{"name":%q,"browser_download_url":"%s/f","size":%d},{"name":"SHA256SUMS.txt","browser_download_url":"%s/sums"}]}`, name, srv.URL, len(program), srv.URL)
		case "/f":
			_, _ = w.Write(program)
		case "/sums":
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), name)
		}
	}))
	t.Cleanup(srv.Close)
	return &update.Checker{URL: srv.URL + "/latest"}
}

// waitRestart waits for the background update to restart the program and
// says as which file.
func waitRestart(t *testing.T, restarted *atomic.Value) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for restarted.Load() == nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	exe, _ := restarted.Load().(string)
	return exe
}

func TestUpdates(t *testing.T) {
	if update.InDocker() {
		t.Skip("updates go to the data directory in Docker")
	}
	program := []byte("new program")
	a := newApp(t)
	a.Updater = fakeRelease(t, program)
	ctx := context.Background()

	a.Version = "dev"
	if v := a.UpdateStatus(); v.CanApply || v.Why == "" {
		t.Fatalf("dev = %+v", v)
	}
	a.Version = "v0.2.0"
	v, err := a.CheckUpdate(ctx)
	if err != nil || !v.Newer || v.Latest.Version != "v0.3.0" || v.CanApply {
		t.Fatalf("check = %+v, %v", v, err) // no way to restart: not appliable
	}
	ov, _ := a.Overview(ctx)
	found := false
	for _, it := range ov.Todo {
		if it.Kind == "update" && it.Title == "有新版本 v0.3.0" && it.Meta == "监控和备份" {
			found = true
		}
	}
	if !found {
		t.Errorf("overview todo = %+v", ov.Todo)
	}

	dir := t.TempDir()
	a.UpdateExe = filepath.Join(dir, "miaopanel")
	_ = os.WriteFile(a.UpdateExe, []byte("old program"), 0o755)
	var restarted atomic.Int32
	var restartedAs atomic.Value
	a.Restart = func(exe string) error { restartedAs.Store(exe); restarted.Add(1); return nil }
	a.locks.try(7) // a checklist running on some server
	if _, err := a.ApplyUpdate(ctx); err == nil || !strings.Contains(err.Error(), "清单") {
		t.Fatalf("updated while a checklist runs: %v", err)
	}
	a.locks.release(7)
	if v, err := a.ApplyUpdate(ctx); err != nil || !v.Applying {
		t.Fatalf("apply = %+v, %v", v, err)
	}
	// No checklist starts until the program restarts.
	if a.locks.try(8) {
		t.Fatal("a checklist could start during the update")
	}
	deadline := time.Now().Add(5 * time.Second)
	for restarted.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if restarted.Load() != 1 || restartedAs.Load() != a.UpdateExe {
		t.Fatalf("restarted %d times as %v", restarted.Load(), restartedAs.Load())
	}
	if b, _ := os.ReadFile(a.UpdateExe); !bytes.Equal(b, program) {
		t.Fatalf("installed %q", b)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("left behind: %v", ents)
	}
	a.locks.reopen() // as the restarted program would be

	if v, _ := a.SetUpdateCheck(false); v.Enabled {
		t.Error("still on")
	}
	a.Version = "v0.3.0"
	if _, err := a.ApplyUpdate(ctx); err == nil {
		t.Error("updated to the same version")
	}
}

// In Docker, or where the program's directory cannot be written, the new
// program goes into the data directory; the one in place stays.
func TestUpdateIntoDataDir(t *testing.T) {
	t.Setenv("MIAO_DOCKER", "1")
	program := []byte("new program for the data directory")
	a := newApp(t)
	a.Updater = fakeRelease(t, program)
	a.Version = "v0.2.0"
	a.UpdateExe = filepath.Join(t.TempDir(), "miaopanel")
	_ = os.WriteFile(a.UpdateExe, []byte("old program"), 0o755)
	var restarted atomic.Value
	a.Restart = func(exe string) error { restarted.Store(exe); return nil }

	if v := a.UpdateStatus(); v.CanApply || !strings.Contains(v.Why, "不能写入") {
		t.Fatalf("no data directory = %+v", v) // the desktop edition has none
	}
	a.DataDir = t.TempDir()
	if v := a.UpdateStatus(); !v.CanApply {
		t.Fatalf("status = %+v", v)
	}
	// One click: it looks for the newest release itself.
	if v, err := a.ApplyUpdate(context.Background()); err != nil || !v.Applying || v.Latest.Version != "v0.3.0" {
		t.Fatalf("apply = %+v, %v", v, err)
	}
	local := update.LocalPath(a.DataDir)
	if exe := waitRestart(t, &restarted); exe != local {
		t.Fatalf("restarted as %q", exe)
	}
	if b, _ := os.ReadFile(local); !bytes.Equal(b, program) {
		t.Fatalf("installed %q", b)
	}
	if b, _ := os.ReadFile(a.UpdateExe); string(b) != "old program" {
		t.Error("the program in place was touched")
	}
	if v := a.UpdateStatus(); v.Got != int64(len(program)) || v.Size != int64(len(program)) {
		t.Errorf("progress %d of %d", v.Got, v.Size)
	}
}

func TestDiagnostics(t *testing.T) {
	a := newApp(t)
	a.Version = "v0.2.0"
	sv, _ := a.Store.AddServer(store.Server{Name: "web", Host: "203.0.113.45", Port: 22, Username: "root", AuthKind: "password"})
	_ = a.Secrets.Set(secretKey(sv.ID, "password"), "hunter2-secret")
	if _, err := a.SaveTencent("AKIDabcdefghijklmnop1234", "secretkeysecretkey1234"); err != nil {
		t.Fatal(err)
	}
	// Named after its address, and down with the address in the reason.
	unnamed, _ := a.Store.AddServer(store.Server{Name: "198.51.100.7", Host: "198.51.100.7", Port: 22, Username: "root", AuthKind: "password"})
	_, _, _ = a.Store.OpenIncident("server", strconv.FormatInt(unnamed.ID, 10), unnamed.Name,
		"连不上（dial tcp 198.51.100.7:22: connect: connection refused）; v6 2408:8756:c52:1a0::21 at 10:18:43")
	var buf bytes.Buffer
	if err := a.Diagnostics(&buf); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	names := []string{}
	for _, f := range zr.File {
		names = append(names, f.Name)
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		all.Write(b)
	}
	text := all.String()
	if strings.Join(names, ",") != "miaopanel.json,recent-activity.json,incidents.json,README.txt" {
		t.Errorf("files = %v", names)
	}
	for _, secret := range []string{"hunter2-secret", "secretkeysecretkey1234", "AKIDabcdefghijklmnop1234", "203.0.113.45", "198.51.100.7", "2408:8756"} {
		if strings.Contains(text, secret) {
			t.Errorf("the bundle has %q", secret)
		}
	}
	if !strings.Contains(text, `"host": "203.0.*.*"`) || !strings.Contains(text, `"tencent": true`) || !strings.Contains(text, `"version": "v0.2.0"`) ||
		!strings.Contains(text, "10:18:43") || !strings.Contains(text, "2408:*") {
		t.Errorf("bundle = %s", text)
	}
}
