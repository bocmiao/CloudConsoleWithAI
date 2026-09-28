package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		latest, current string
		want            bool
	}{
		{"v0.3.0", "v0.2.0", true}, {"v0.2.0", "v0.2.0", false}, {"v0.2.1", "v0.2.0", true}, {"v1.0.0", "v0.9.9", true},
		{"v0.2.0", "v0.3.0", false}, {"v0.3.0", "v0.3.0-rc.1", true}, {"v0.3.0-rc.2", "v0.3.0", false},
		{"v0.3.0", "dev", false}, {"v0.3.0", "dev-abc1234", false}, {"latest", "v0.1.0", false},
	} {
		if got := Newer(c.latest, c.current); got != c.want {
			t.Errorf("Newer(%s, %s) = %v", c.latest, c.current, got)
		}
	}
	if AssetName("windows", "amd64") != "MiaoPanel-windows-amd64.exe" || AssetName("linux", "arm64") != "MiaoPanel-linux-arm64" {
		t.Error("asset names")
	}
}

// fakeGitHub serves a release with a program and its checksums.
func fakeGitHub(t *testing.T, program []byte, badSum bool) *httptest.Server {
	var srv *httptest.Server
	sum := sha256.Sum256(program)
	hexSum := hex.EncodeToString(sum[:])
	if badSum {
		hexSum = strings.Repeat("0", 64)
	}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			fmt.Fprintf(w, `{"tag_name":"v9.9.9","html_url":"https://example.com/r","body":"新功能","published_at":"2026-09-30T00:00:00Z","assets":[
				{"name":"MiaoPanel-linux-amd64","browser_download_url":"%[1]s/f","size":%[2]d},
				{"name":"SHA256SUMS.txt","browser_download_url":"%[1]s/sums","size":100}]}`, srv.URL, len(program))
		case "/f":
			_, _ = w.Write(program)
		case "/sums":
			fmt.Fprintf(w, "%s  MiaoPanel-linux-amd64\n%s  MiaoPanel-windows-amd64.exe\n", hexSum, strings.Repeat("1", 64))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDownloadAndInstall(t *testing.T) {
	program := []byte("#!/bin/sh\necho new\n")
	srv := fakeGitHub(t, program, false)
	c := &Checker{URL: srv.URL + "/latest"}
	ctx := context.Background()
	rel, err := c.Latest(ctx)
	if err != nil || rel.Version != "v9.9.9" || rel.Notes != "新功能" || len(rel.Assets) != 2 {
		t.Fatalf("latest = %+v, %v", rel, err)
	}
	dir := t.TempDir()
	path, err := c.Download(ctx, rel, "linux", "amd64", dir)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != string(program) {
		t.Fatal("wrong content")
	}
	if st, _ := os.Stat(path); st.Mode().Perm()&0o100 == 0 {
		t.Error("not executable")
	}
	exe := filepath.Join(dir, "miaopanel")
	_ = os.WriteFile(exe, []byte("old"), 0o755)
	if err := Install(exe, path); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != string(program) {
		t.Fatal("not installed")
	}
	if _, err := c.Download(ctx, rel, "darwin", "arm64", dir); err == nil {
		t.Error("a missing file was downloaded")
	}

	bad := fakeGitHub(t, program, true)
	c = &Checker{URL: bad.URL + "/latest"}
	rel, _ = c.Latest(ctx)
	if _, err := c.Download(ctx, rel, "linux", "amd64", dir); err == nil || !strings.Contains(err.Error(), "校验值") {
		t.Errorf("bad checksum: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("left files behind: %v", entries)
	}
}

func TestWritable(t *testing.T) {
	dir := t.TempDir()
	if !Writable(dir) {
		t.Error("temp dir not writable")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("left %v behind", ents)
	}
	if Writable(filepath.Join(dir, "missing")) {
		t.Error("missing dir writable")
	}
}
