package api

import (
	"context"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/app"
	"github.com/bocmiao/CloudConsoleWithAI/internal/auth"
	"github.com/bocmiao/CloudConsoleWithAI/internal/dbconf"
	"github.com/bocmiao/CloudConsoleWithAI/internal/secrets"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/internal/webui"
)

// The install wizard of a web edition that has no database yet. Every
// step takes the setup code printed at start, so only whoever runs the
// server can install it. Once a database is chosen, ready gets it and
// switches the server over to Miao Panel itself, whose first-account
// page finishes the wizard.

type installer struct {
	dir   string
	sec   secrets.Store
	codes *auth.Service // checks the setup code, counting wrong ones
	ready func(*store.Store) error

	mu   sync.Mutex
	done bool
}

// dbName is a MySQL database name the wizard takes, or makes.
var dbName = regexp.MustCompile(`^[0-9A-Za-z_$-]{1,64}$`)

// installRequest is what the wizard sends.
type installRequest struct {
	Code  string      `json:"code"`
	Kind  string      `json:"kind"` // sqlite or mysql
	MySQL store.MySQL `json:"mysql"`
}

// NewInstaller serves the install wizard for the data directory dir.
func NewInstaller(dir string, sec secrets.Store, version string, ready func(*store.Store) error) *Server {
	in := &installer{dir: dir, sec: sec, codes: auth.New(nil, sec, dir), ready: ready}
	s := &Server{auth: in.codes, version: version, mux: http.NewServeMux()}
	s.mux.Handle("GET /", webui.Handler())
	open := func(pattern string, h func(w http.ResponseWriter, r *http.Request) (any, error)) {
		s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Miao") != "1" {
				writeJSON(w, http.StatusForbidden, apiError{Error: "请求不对"})
				return
			}
			s.respond(w, r, 16<<10, h)
		})
	}
	open("GET /api/auth/state", func(_ http.ResponseWriter, r *http.Request) (any, error) {
		return map[string]any{"mode": "server", "version": version, "https": s.https(r), "install": true,
			"sqlite": filepath.Join(dir, store.DBFile)}, nil
	})
	open("POST /api/install/code", func(_ http.ResponseWriter, r *http.Request) (any, error) {
		var req installRequest
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, in.codes.CheckSetupCode(s.clientIP(r), req.Code)
	})
	open("POST /api/install/check", func(_ http.ResponseWriter, r *http.Request) (any, error) {
		req, err := in.request(s, r)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		v, missing, err := store.CheckMySQL(ctx, req.MySQL)
		if err != nil {
			return nil, &app.UserError{Msg: err.Error()}
		}
		return map[string]any{"version": v, "missing": missing}, nil
	})
	open("POST /api/install/database", func(_ http.ResponseWriter, r *http.Request) (any, error) {
		req, err := in.request(s, r)
		if err != nil {
			return nil, err
		}
		return in.install(req)
	})
	// Everything else waits for the installation.
	for _, m := range []string{"GET", "POST", "PUT", "DELETE"} {
		s.mux.HandleFunc(m+" /api/", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusServiceUnavailable, apiError{Error: "Miao Panel 还没有安装完成，请刷新页面继续安装", Code: "install"})
		})
	}
	return s
}

// request reads a step's body and checks its setup code.
func (in *installer) request(s *Server, r *http.Request) (installRequest, error) {
	var req installRequest
	if err := decode(r, &req); err != nil {
		return req, err
	}
	if err := in.codes.CheckSetupCode(s.clientIP(r), req.Code); err != nil {
		return req, err
	}
	req.MySQL.Host = strings.TrimSpace(req.MySQL.Host)
	req.MySQL.Database = strings.TrimSpace(req.MySQL.Database)
	req.MySQL.User = strings.TrimSpace(req.MySQL.User)
	if req.Kind == "mysql" || strings.HasSuffix(r.URL.Path, "/check") {
		if req.MySQL.Host == "" || req.MySQL.Database == "" || req.MySQL.User == "" {
			return req, &app.UserError{Msg: "请填写 MySQL 的地址、数据库名和用户名"}
		}
		if !dbName.MatchString(req.MySQL.Database) {
			return req, &app.UserError{Msg: "库名只能用字母、数字、下划线和减号，最长 64 个字符"}
		}
		if req.MySQL.Port == 0 {
			req.MySQL.Port = 3306
		}
		if req.MySQL.Port < 1 || req.MySQL.Port > 65535 {
			return req, &app.UserError{Msg: "端口要在 1 到 65535 之间"}
		}
	}
	return req, nil
}

// install opens the chosen database, remembers the choice and hands the
// database to the server. It says how many accounts the database has:
// one used by Miao Panel before already has its administrator.
func (in *installer) install(req installRequest) (any, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.done {
		return nil, &app.UserError{Msg: "已经安装好了，请刷新页面"}
	}
	var st *store.Store
	var err error
	switch req.Kind {
	case "sqlite":
		st, err = store.Open(in.dir)
	case "mysql":
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err = store.CreateMySQLDatabase(ctx, req.MySQL)
		cancel()
		if err != nil {
			return nil, &app.UserError{Msg: err.Error()}
		}
		st, err = store.OpenMySQL(req.MySQL)
	default:
		return nil, &app.UserError{Msg: "请选择数据库"}
	}
	if err != nil {
		return nil, &app.UserError{Msg: "打开数据库失败：" + err.Error()}
	}
	users, err := st.CountUsers()
	if err == nil {
		err = dbconf.Save(in.dir, in.sec, dbconf.Choice{Kind: req.Kind, MySQL: &req.MySQL})
	}
	if err == nil {
		err = in.ready(st)
	}
	if err != nil {
		st.Close()
		return nil, err
	}
	in.done = true
	return map[string]any{"users": users, "where": st.Where()}, nil
}
