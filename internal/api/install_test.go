package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"

	"github.com/bocmiao/CloudConsoleWithAI/internal/app"
	"github.com/bocmiao/CloudConsoleWithAI/internal/auth"
	"github.com/bocmiao/CloudConsoleWithAI/internal/dbconf"
	"github.com/bocmiao/CloudConsoleWithAI/internal/secrets"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

// installWizard runs the wizard over a fresh data directory: when it is
// done, the server answers as Miao Panel, as serve swaps it in.
func installWizard(t *testing.T) (dir string, code string, serve func(call) *http.Response, current func() *Server) {
	t.Helper()
	dir = t.TempDir()
	sec := secrets.OpenFile(dir)
	var s *Server
	s = NewInstaller(dir, sec, "test", func(st *store.Store) error {
		t.Cleanup(func() { st.Close() })
		au := auth.New(st, sec, dir)
		au.Cost = bcrypt.MinCost
		s = NewServer(app.New(st, sec), au, "test")
		return nil
	})
	code, err := auth.InstallCode(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir, code, func(c call) *http.Response { return c.do(s).Result() }, func() *Server { return s }
}

func decodeBody(t *testing.T, r *http.Response) map[string]any {
	t.Helper()
	var m map[string]any
	_ = json.NewDecoder(r.Body).Decode(&m)
	return m
}

func TestInstallWizardBuiltIn(t *testing.T) {
	dir, code, serve, _ := installWizard(t)
	if dbconf.Installed(dir) {
		t.Fatal("installed before the wizard")
	}
	st := decodeBody(t, serve(call{method: "GET", path: "/api/auth/state"}))
	if st["install"] != true || st["sqlite"] != filepath.Join(dir, store.DBFile) {
		t.Fatalf("state = %v", st)
	}
	if r := serve(call{method: "GET", path: "/api/servers"}); r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("api before install = %d", r.StatusCode)
	}
	if r := serve(call{method: "GET", path: "/"}); r.StatusCode != http.StatusOK {
		t.Fatalf("page = %d", r.StatusCode)
	}
	// The code guards every step.
	if r := serve(call{method: "POST", path: "/api/install/code", body: `{"code":"AAAA-BBBB-CCCC-DDDD"}`}); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong code = %d", r.StatusCode)
	}
	if r := serve(call{method: "POST", path: "/api/install/database", body: `{"code":"nope","kind":"sqlite"}`}); r.StatusCode != http.StatusBadRequest || dbconf.Installed(dir) {
		t.Fatalf("database with a wrong code = %d", r.StatusCode)
	}
	if r := serve(call{method: "POST", path: "/api/install/code", body: `{"code":"` + strings.ToLower(code) + `"}`}); r.StatusCode != http.StatusOK {
		t.Fatalf("right code = %d %v", r.StatusCode, decodeBody(t, r))
	}
	r := serve(call{method: "POST", path: "/api/install/database", body: `{"code":"` + code + `","kind":"sqlite"}`})
	if m := decodeBody(t, r); r.StatusCode != http.StatusOK || m["users"] != float64(0) {
		t.Fatalf("database = %d %v", r.StatusCode, m)
	}
	if !dbconf.Installed(dir) {
		t.Fatal("not installed")
	}
	// Now it is Miao Panel: the first-account page takes the same code.
	st = decodeBody(t, serve(call{method: "GET", path: "/api/auth/state"}))
	if st["install"] != nil || st["setup"] != true {
		t.Fatalf("state after = %v", st)
	}
	r = serve(call{method: "POST", path: "/api/auth/setup", body: `{"code":"` + code + `","name":"admin","password":"correct horse battery"}`})
	if r.StatusCode != http.StatusOK {
		t.Fatalf("setup = %d %v", r.StatusCode, decodeBody(t, r))
	}
	// Restarted, the data directory opens the same database.
	st2, err := dbconf.Open(dir, secrets.OpenFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if n, _ := st2.CountUsers(); n != 1 || st2.IsMySQL() {
		t.Fatalf("reopened: %d users, mysql %v", n, st2.IsMySQL())
	}
}

func TestInstallWizardMySQL(t *testing.T) {
	dsn := os.Getenv("MIAO_TEST_MYSQL")
	if dsn == "" {
		t.Skip("MIAO_TEST_MYSQL not set")
	}
	// A database made the way a user would, empty.
	holder, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	cfg, _ := mysql.ParseDSN(dsn)
	host, port, _ := strings.Cut(cfg.Addr, ":")
	db := strings.TrimPrefix(holder.Where(), "MySQL："+cfg.User+"@"+cfg.Addr+"/")

	dir, code, serve, _ := installWizard(t)
	body := func(pw string) string {
		return fmt.Sprintf(`{"code":%q,"kind":"mysql","mysql":{"host":%q,"port":%s,"database":%q,"user":%q,"password":%q}}`, code, host, port, db, cfg.User, pw)
	}
	if r := serve(call{method: "POST", path: "/api/install/check", body: body(cfg.Passwd + "x")}); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong password = %d", r.StatusCode)
	}
	r := serve(call{method: "POST", path: "/api/install/check", body: body(cfg.Passwd)})
	if m := decodeBody(t, r); r.StatusCode != http.StatusOK || m["version"] == "" {
		t.Fatalf("check = %d %v", r.StatusCode, m)
	}
	// The existing tables of the holder mean an earlier install: its
	// account is kept.
	if _, err := holder.AddUser("owner", "h"); err != nil {
		t.Fatal(err)
	}
	r = serve(call{method: "POST", path: "/api/install/database", body: body(cfg.Passwd)})
	if m := decodeBody(t, r); r.StatusCode != http.StatusOK || m["users"] != float64(1) || !strings.Contains(m["where"].(string), db) {
		t.Fatalf("database = %d %v", r.StatusCode, m)
	}
	// The password is in the secret store, not database.json.
	raw, _ := os.ReadFile(filepath.Join(dir, "database.json"))
	if strings.Contains(string(raw), cfg.Passwd) || !strings.Contains(string(raw), `"mysql"`) {
		t.Fatalf("database.json = %s", raw)
	}
	st, err := dbconf.Open(dir, secrets.OpenFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if u, err := st.UserByName("owner"); err != nil || !st.IsMySQL() {
		t.Fatalf("reopened: %+v %v", u, err)
	}
}
