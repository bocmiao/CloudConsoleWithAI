package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx/sshtest"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

func TestFiles(t *testing.T) {
	a := newApp(t)
	srv := sshtest.Start(t, "root", "pw")
	sv := addTestServer(t, a, srv, "pw")
	ctx := context.Background()
	root := t.TempDir()
	code := func(err error) string {
		var ue *UserError
		if errors.As(err, &ue) {
			return ue.Code
		}
		return ""
	}
	op := func(o FileOp) FileOpResult {
		t.Helper()
		r, err := a.FileChange(ctx, sv.ID, o)
		if err != nil {
			t.Fatalf("%s: %v", o.Op, err)
		}
		return r
	}

	// New folder and file, edit, and a save that would lose someone
	// else's change.
	op(FileOp{Op: "mkdir", Dir: root, Name: "site"})
	op(FileOp{Op: "touch", Dir: root + "/site", Name: "index.html"})
	if _, err := a.FileChange(ctx, sv.ID, FileOp{Op: "mkdir", Dir: root, Name: "site"}); err == nil {
		t.Fatal("made a folder twice")
	}
	if _, err := a.FileChange(ctx, sv.ID, FileOp{Op: "mkdir", Dir: root, Name: "a/b"}); err == nil {
		t.Fatal("accepted a name with /")
	}
	f, err := a.ReadFileText(ctx, sv.ID, root+"/site/index.html")
	if err != nil || f.Content != "" || f.Binary {
		t.Fatalf("read = %+v %v", f, err)
	}
	f, err = a.WriteFileText(ctx, sv.ID, root+"/site/index.html", "<h1>喵</h1>\n", f.ModTime, false)
	if err != nil || f.Content != "<h1>喵</h1>\n" {
		t.Fatalf("write = %+v %v", f, err)
	}
	if _, err := a.WriteFileText(ctx, sv.ID, root+"/site/index.html", "old tab", "2000-01-01T00:00:00Z", false); code(err) != "conflict" {
		t.Fatalf("stale save: %v", err)
	}
	if _, err := a.WriteFileText(ctx, sv.ID, root+"/site/index.html", "forced\n", "2000-01-01T00:00:00Z", true); err != nil {
		t.Fatal(err)
	}
	// Saving through a symbolic link keeps the link.
	_ = os.Symlink("index.html", filepath.Join(root, "site", "home.html"))
	if _, err := a.WriteFileText(ctx, sv.ID, root+"/site/home.html", "forced\n", "", false); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Lstat(filepath.Join(root, "site", "home.html")); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link was replaced by a file")
	}
	if l, _ := a.ListFiles(ctx, sv.ID, root+"/site"); len(l.Entries) != 2 || l.Entries[0].Link != "index.html" {
		t.Fatalf("link entry = %+v", l.Entries)
	}
	_ = os.Remove(filepath.Join(root, "site", "home.html"))
	_ = os.WriteFile(filepath.Join(root, "site", "logo.png"), []byte{0x89, 'P', 'N', 'G', 0, 0, 1}, 0o644)
	if f, _ := a.ReadFileText(ctx, sv.ID, root+"/site/logo.png"); !f.Binary {
		t.Fatal("binary file opened as text")
	}

	// Upload, and not over an existing file unless asked.
	if _, err := a.UploadFile(ctx, sv.ID, root+"/site", "../evil/app.js", strings.NewReader("console.log(1)"), false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "site", "app.js")); string(b) != "console.log(1)" {
		t.Fatalf("uploaded %q", b)
	}
	if _, err := a.UploadFile(ctx, sv.ID, root+"/site", "app.js", strings.NewReader("x"), false); code(err) != "conflict" {
		t.Fatalf("upload over a file: %v", err)
	}
	if _, err := a.UploadFile(ctx, sv.ID, root+"/site", "app.js", strings.NewReader("v2"), true); err != nil {
		t.Fatal(err)
	}

	// List: folders first, with owner and permissions.
	l, err := a.ListFiles(ctx, sv.ID, root)
	if err != nil || l.Path != root || len(l.Entries) != 1 || !l.Entries[0].Dir || l.Entries[0].Mode[0] != 'd' || l.Entries[0].Owner == "" {
		t.Fatalf("list = %+v %v", l, err)
	}
	if home, err := a.ListFiles(ctx, sv.ID, ""); err != nil || home.Path == "" {
		t.Fatalf("home = %+v %v", home, err)
	}

	// Copy, name clashes, move, rename.
	op(FileOp{Op: "mkdir", Dir: root, Name: "backup"})
	op(FileOp{Op: "copy", Paths: []string{root + "/site"}, Dir: root + "/backup"})
	if _, err := a.FileChange(ctx, sv.ID, FileOp{Op: "copy", Paths: []string{root + "/site"}, Dir: root + "/backup"}); code(err) != "conflict" {
		t.Fatalf("copy onto a name: %v", err)
	}
	op(FileOp{Op: "copy", Paths: []string{root + "/site"}, Dir: root + "/backup", Conflict: "rename"})
	if _, err := os.Stat(filepath.Join(root, "backup", "site (2)", "app.js")); err != nil {
		t.Fatal("no renamed copy")
	}
	op(FileOp{Op: "copy", Paths: []string{root + "/site/app.js"}, Dir: root + "/site", Conflict: "rename"})
	if _, err := os.Stat(filepath.Join(root, "site", "app (2).js")); err != nil {
		t.Fatal("pasting in the same folder did not make a copy")
	}
	if _, err := a.FileChange(ctx, sv.ID, FileOp{Op: "move", Paths: []string{root + "/backup"}, Dir: root + "/backup/site"}); err == nil {
		t.Fatal("moved a folder into itself")
	}
	op(FileOp{Op: "move", Paths: []string{root + "/backup/site (2)"}, Dir: root})
	op(FileOp{Op: "rename", Paths: []string{root + "/site (2)"}, Name: "old"})
	if _, err := os.Stat(filepath.Join(root, "old", "index.html")); err != nil {
		t.Fatal("move and rename")
	}

	// Archives.
	op(FileOp{Op: "compress", Paths: []string{root + "/site"}, Name: "site", Format: "tar.gz"})
	op(FileOp{Op: "extract", Paths: []string{root + "/site.tar.gz"}, Dir: root + "/out"})
	if b, _ := os.ReadFile(filepath.Join(root, "out", "site", "index.html")); string(b) != "forced\n" {
		t.Fatalf("extracted %q", b)
	}
	if _, err := exec.LookPath("zip"); err == nil {
		if _, err := exec.LookPath("unzip"); err == nil {
			op(FileOp{Op: "compress", Paths: []string{root + "/site/app.js", root + "/site/index.html"}, Name: "two", Format: "zip"})
			op(FileOp{Op: "extract", Paths: []string{root + "/site/two.zip"}, Dir: root + "/z"})
			if _, err := os.Stat(filepath.Join(root, "z", "app.js")); err != nil {
				t.Fatal("zip round trip")
			}
		}
	}
	if _, err := a.FileChange(ctx, sv.ID, FileOp{Op: "extract", Paths: []string{root + "/site/app.js"}}); err == nil {
		t.Fatal("extracted a .js")
	}

	// Permissions.
	op(FileOp{Op: "chmod", Paths: []string{root + "/site/app.js"}, Mode: "600"})
	if st, _ := os.Stat(filepath.Join(root, "site", "app.js")); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", st.Mode())
	}
	if _, err := a.FileChange(ctx, sv.ID, FileOp{Op: "chmod", Paths: []string{root}, Mode: "7777x"}); err == nil {
		t.Fatal("bad mode accepted")
	}

	// Downloads: a file, and a folder as .tar.gz.
	var buf bytes.Buffer
	var name string
	if err := a.DownloadFile(ctx, sv.ID, root+"/site/index.html", func(n string, _ int64) { name = n }, &buf); err != nil || name != "index.html" || buf.String() != "forced\n" {
		t.Fatalf("download %q %q %v", name, buf.String(), err)
	}
	buf.Reset()
	if err := a.DownloadFile(ctx, sv.ID, root+"/site", func(n string, _ int64) { name = n }, &buf); err != nil || name != "site.tar.gz" {
		t.Fatalf("folder download %q %v", name, err)
	}
	gz, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	found := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		found = found || h.Name == "site/index.html"
	}
	if !found {
		t.Fatal("folder archive without its files")
	}

	// New files take the folder's owner (only root can give them away).
	if os.Geteuid() == 0 {
		site := filepath.Join(root, "www")
		_ = os.Mkdir(site, 0o755)
		_ = os.Chown(site, 1000, 1000)
		op(FileOp{Op: "mkdir", Dir: site, Name: "img"})
		op(FileOp{Op: "touch", Dir: site, Name: "a.txt"})
		if _, err := a.UploadFile(ctx, sv.ID, site, "b.txt", strings.NewReader("b"), false); err != nil {
			t.Fatal(err)
		}
		op(FileOp{Op: "compress", Paths: []string{site + "/a.txt"}, Name: "a", Format: "tar.gz"})
		top, _ := a.ListFiles(ctx, sv.ID, root)
		var owner string
		for _, e := range top.Entries {
			if e.Name == "www" {
				owner = e.Owner
			}
		}
		for _, n := range []string{"img", "a.txt", "b.txt", "a.tar.gz"} {
			if l, _ := a.ListFiles(ctx, sv.ID, site); owner == "root" || !ownedBy(l, n, owner) {
				t.Fatalf("%s not owned like its folder: %+v", n, l.Entries)
			}
		}
	}

	// Deleting: never the system's folders or what keeps the server
	// reachable.
	for _, p := range []string{"/", "/etc", "/usr", "/root/.ssh/authorized_keys", "/home/alice/.ssh", "/boot/vmlinuz", "/opt/1panel"} {
		if _, err := a.FileChange(ctx, sv.ID, FileOp{Op: "delete", Paths: []string{p}}); err == nil || !strings.Contains(err.Error(), "不能删除") {
			t.Fatalf("delete %s: %v", p, err)
		}
	}
	if _, err := a.FileChange(ctx, sv.ID, FileOp{Op: "move", Paths: []string{"/etc/passwd"}, Dir: root}); err == nil {
		t.Fatal("moved /etc/passwd")
	}
	op(FileOp{Op: "delete", Paths: []string{root + "/site", root + "/old"}})
	if _, err := os.Stat(filepath.Join(root, "site")); !os.IsNotExist(err) {
		t.Fatal("not deleted")
	}
	if logs, _ := a.Store.ListAudit(50); !auditHas(logs, "files.delete") || !auditHas(logs, "files.upload") || !auditHas(logs, "files.edit") {
		t.Fatal("changes not in the audit log")
	}
}

func auditHas(logs []store.AuditEntry, action string) bool {
	for _, l := range logs {
		if l.Action == action {
			return true
		}
	}
	return false
}

// ownedBy says whether the entry named n has the given owner.
func ownedBy(l FileList, n, owner string) bool {
	for _, e := range l.Entries {
		if e.Name == n {
			return e.Owner == owner
		}
	}
	return false
}
