package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileURI(t *testing.T) {
	for in, want := range map[string]string{
		"/home/u/.config/MiaoPanel/miaopanel.db": "file:///home/u/.config/MiaoPanel/miaopanel.db",
		"/tmp/a#b?c%d/x.db":                      "file:///tmp/a%23b%3fc%25d/x.db",
	} {
		if got := fileURI(in); got != want {
			t.Errorf("fileURI(%q) = %q, want %q", in, got, want)
		}
	}
}

// A data directory with spaces, Chinese characters and URI-special
// characters must still open, reopen and keep its data.
func TestOpenUnusualPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "张三 的#数据?%")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddServer(Server{Name: "a", Host: "1.2.3.4", Port: 22, Username: "root", AuthKind: "password"}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if _, err := os.Stat(filepath.Join(dir, "miaopanel.db")); err != nil {
		t.Fatalf("database not created where expected: %v", err)
	}
	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	servers, err := st.ListServers()
	if err != nil || len(servers) != 1 {
		t.Fatalf("servers = %v, err = %v", servers, err)
	}
}
