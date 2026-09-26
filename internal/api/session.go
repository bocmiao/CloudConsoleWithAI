package api

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"

	"github.com/bocmiao/CloudConsoleWithAI/internal/app"
	"github.com/bocmiao/CloudConsoleWithAI/internal/auth"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

// Logging in to the web edition. The desktop has no accounts: its state
// answers mode "desktop" and the rest refuses.

func (s *Server) mode() string {
	if s.auth != nil {
		return "server"
	}
	return "desktop"
}

func (s *Server) authRoutes() {
	// These work before logging in; the X-Miao header is still required,
	// so another site cannot log someone in or out.
	open := func(pattern string, h func(w http.ResponseWriter, r *http.Request) (any, error)) {
		s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Miao") != "1" {
				writeJSON(w, http.StatusForbidden, apiError{Error: "请求不对"})
				return
			}
			s.respond(w, r, 16<<10, h)
		})
	}
	open("GET /api/auth/state", s.authState)
	open("POST /api/auth/setup", s.setup)
	open("POST /api/auth/login", s.login)
	open("POST /api/auth/logout", s.logout)
	api := func(pattern string, h func(w http.ResponseWriter, r *http.Request) (any, error)) {
		s.mux.HandleFunc(pattern, s.guard(h))
	}
	api("GET /api/account", s.account)
	api("PUT /api/account/password", s.changePassword)
	api("POST /api/account/totp", s.beginTOTP)
	api("PUT /api/account/totp", s.enableTOTP)
	api("POST /api/account/totp/off", s.disableTOTP)
	api("DELETE /api/account/sessions/{key}", s.endSession)
}

// loggedIn says whether the request carries a good session cookie.
func (s *Server) loggedIn(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	if s.auth == nil {
		return subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.token)) == 1
	}
	_, ok := s.auth.Check(c.Value, s.clientIP(r))
	return ok
}

// proxied says whether the connection comes from this machine or a
// private network, where a reverse proxy passes on who the visitor is.
func proxied(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// clientIP is the visitor's address: the connection's, or what a reverse
// proxy in front says (X-Real-IP, else the last X-Forwarded-For hop).
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if proxied(r) {
		if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); net.ParseIP(v) != nil {
			return v
		}
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if v := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(v) != nil {
				return v
			}
		}
	}
	return host
}

// https says whether the browser reached us over HTTPS, directly or
// through a reverse proxy.
func (s *Server) https(r *http.Request) bool {
	return r.TLS != nil || (proxied(r) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"))
}

func (s *Server) setSession(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: s.https(r), MaxAge: int(auth.MaxAge.Seconds())})
}

func errDesktop() error { return &app.UserError{Msg: "桌面版不需要登录"} }

type userView struct {
	Name string `json:"name"`
	TOTP bool   `json:"totp"`
}

func (s *Server) authState(_ http.ResponseWriter, r *http.Request) (any, error) {
	out := map[string]any{"mode": s.mode(), "version": s.version, "https": s.https(r)}
	if s.auth == nil {
		return out, nil
	}
	need, err := s.auth.NeedsSetup()
	if err != nil {
		return nil, err
	}
	out["setup"] = need
	if c, err := r.Cookie(cookieName); err == nil {
		if u, ok := s.auth.Check(c.Value, s.clientIP(r)); ok {
			out["user"] = userView{u.Name, u.TOTP}
		}
	}
	return out, nil
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) (any, error) {
	if s.auth == nil {
		return nil, errDesktop()
	}
	var req struct{ Code, Name, Password string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	tok, u, err := s.auth.Setup(s.clientIP(r), r.UserAgent(), req.Code, req.Name, req.Password)
	if err != nil {
		return nil, err
	}
	s.setSession(w, r, tok)
	return userView{u.Name, u.TOTP}, nil
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) (any, error) {
	if s.auth == nil {
		return nil, errDesktop()
	}
	var req struct{ Name, Password, Code string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	tok, u, err := s.auth.Login(s.clientIP(r), r.UserAgent(), req.Name, req.Password, req.Code)
	if err != nil {
		return nil, err
	}
	s.setSession(w, r, tok)
	return userView{u.Name, u.TOTP}, nil
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) (any, error) {
	if s.auth == nil {
		return nil, errDesktop()
	}
	if c, err := r.Cookie(cookieName); err == nil {
		s.auth.Logout(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.https(r), MaxAge: -1})
	return map[string]bool{"ok": true}, nil
}

// me is the logged-in account and its cookie.
func (s *Server) me(r *http.Request) (store.User, string, error) {
	if s.auth == nil {
		return store.User{}, "", errDesktop()
	}
	c, err := r.Cookie(cookieName)
	if err != nil {
		return store.User{}, "", &app.UserError{Msg: "请先登录"}
	}
	u, ok := s.auth.Check(c.Value, s.clientIP(r))
	if !ok {
		return store.User{}, "", &app.UserError{Msg: "请先登录"}
	}
	return u, c.Value, nil
}

func (s *Server) account(_ http.ResponseWriter, r *http.Request) (any, error) {
	u, tok, err := s.me(r)
	if err != nil {
		return nil, err
	}
	sessions, err := s.auth.Sessions(u.ID, tok)
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": u.Name, "totp": u.TOTP, "changedAt": u.ChangedAt, "sessions": sessions}, nil
}

func (s *Server) changePassword(_ http.ResponseWriter, r *http.Request) (any, error) {
	u, tok, err := s.me(r)
	if err != nil {
		return nil, err
	}
	var req struct{ Old, New string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, s.auth.ChangePassword(u.ID, tok, req.Old, req.New)
}

func (s *Server) beginTOTP(_ http.ResponseWriter, r *http.Request) (any, error) {
	u, _, err := s.me(r)
	if err != nil {
		return nil, err
	}
	return s.auth.BeginTOTP(u.ID)
}

func (s *Server) enableTOTP(_ http.ResponseWriter, r *http.Request) (any, error) {
	u, _, err := s.me(r)
	if err != nil {
		return nil, err
	}
	var req struct{ Code string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, s.auth.EnableTOTP(u.ID, req.Code)
}

func (s *Server) disableTOTP(_ http.ResponseWriter, r *http.Request) (any, error) {
	u, _, err := s.me(r)
	if err != nil {
		return nil, err
	}
	var req struct{ Password string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, s.auth.DisableTOTP(u.ID, req.Password)
}

func (s *Server) endSession(_ http.ResponseWriter, r *http.Request) (any, error) {
	u, _, err := s.me(r)
	if err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, s.auth.EndSession(u.ID, r.PathValue("key"))
}
