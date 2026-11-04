package dbconf

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/secrets"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

func TestChoice(t *testing.T) {
	dir := t.TempDir()
	sec := secrets.OpenFile(dir)
	if Installed(dir) {
		t.Fatal("fresh directory installed")
	}
	if _, err := Open(dir, sec); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("open fresh = %v", err)
	}

	// An earlier version's SQLite file counts as installed.
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if !Installed(dir) {
		t.Fatal("SQLite file not seen")
	}
	if st, err = Open(dir, sec); err != nil || st.IsMySQL() {
		t.Fatalf("open = %v", err)
	}
	st.Close()

	// A MySQL choice keeps its password in the secret store only, and
	// going back to SQLite forgets it.
	m := store.MySQL{Host: "127.0.0.1", Port: 1, Database: "miao", User: "miao", Password: "s3cret-pw"}
	if err := Save(dir, sec, Choice{Kind: "mysql", MySQL: &m}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, file))
	if pw, _ := sec.Get(secretKey); pw != "s3cret-pw" || len(raw) == 0 || bytes.Contains(raw, []byte("s3cret")) {
		t.Fatalf("saved %s, secret %q", raw, pw)
	}
	if fi, _ := os.Stat(filepath.Join(dir, file)); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode())
	}
	if _, err := Open(dir, sec); err == nil {
		t.Fatal("opened a MySQL nobody listens on")
	}
	if err := Save(dir, sec, Choice{Kind: "sqlite", MySQL: &m}); err != nil {
		t.Fatal(err)
	}
	if _, err := sec.Get(secretKey); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("password kept: %v", err)
	}
	if raw, _ = os.ReadFile(filepath.Join(dir, file)); bytes.Contains(raw, []byte("mysql")) {
		t.Fatalf("sqlite choice = %s", raw)
	}
	if st, err = Open(dir, sec); err != nil || st.IsMySQL() {
		t.Fatalf("open sqlite = %v", err)
	}
	st.Close()
	_ = os.WriteFile(filepath.Join(dir, file), []byte(`{"kind":"oracle"}`), 0o600)
	if _, err := Open(dir, sec); err == nil {
		t.Fatal("unknown kind opened")
	}
}
