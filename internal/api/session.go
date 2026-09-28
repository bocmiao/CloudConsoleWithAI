package api

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/bocmiao/CloudConsoleWithAI/internal/app"
	"github.com/bocmiao/CloudConsoleWithAI/internal/auth"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sender"
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
	open("POST /api/auth/code", s.loginCode)
	api := func(pattern string, h func(w http.ResponseWriter, r *http.Request) (any, error)) {
		s.mux.HandleFunc(pattern, s.guard(h))
	}
	api("GET /api/account", s.account)
	api("PUT /api/account/password", s.changePassword)
	api("POST /api/account/totp", s.beginTOTP)
	api("PUT /api/account/totp", s.enableTOTP)
	api("POST /api/account/totp/off", s.disableTOTP)
	api("DELETE /api/account/sessions/{key}", s.endSession)
	api("PUT /api/account/methods", s.setMethods)
	api("PUT /api/account/send-scope", s.setSendScope)
	api("POST /api/account/bind", s.beginBind)
	api("PUT /api/account/bind", s.confirmBind)
	api("POST /api/account/unbind", s.unbind)
	api("PUT /api/account/mail", s.saveMail)
	api("POST /api/account/mail/clear", s.clearMail)
	api("POST /api/account/mail/test", s.testMail)
	api("PUT /api/account/sms", s.saveSMS)
	api("POST /api/account/sms/clear", s.clearSMS)
	api("POST /api/account/sms/test", s.testSMS)
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

// SetTrustedProxies adds reverse proxies outside loopback. Only addresses of
// proxies you control belong here; direct clients must not set their own IP.
func (s *Server) SetTrustedProxies(spec string) error {
	var proxies []netip.Prefix
	for _, raw := range strings.Split(spec, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if ip, err := netip.ParseAddr(raw); err == nil {
			proxies = append(proxies, netip.PrefixFrom(ip, ip.BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(raw)
		if err != nil {
			return fmt.Errorf("无效的反向代理地址 %q：%w", raw, err)
		}
		proxies = append(proxies, p.Masked())
	}
	s.trustedProxies = proxies
	return nil
}

// proxied reports whether the immediate peer is a trusted reverse proxy.
func (s *Server) proxied(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, p := range s.trustedProxies {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP is the visitor's address: the connection's, or what a reverse
// proxy in front says. The last X-Forwarded-For hop comes first: nginx
// ($proxy_add_x_forwarded_for) and Caddy both put the address they saw
// there, whatever the visitor sent, while X-Real-IP passes through a
// proxy that does not set it.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if s.proxied(r) {
		if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
			parts := strings.Split(xff[len(xff)-1], ",")
			if v := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(v) != nil {
				return v
			}
		}
		if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); net.ParseIP(v) != nil {
			return v
		}
	}
	return host
}

// https says whether the browser reached us over HTTPS, directly or
// through a reverse proxy.
func (s *Server) https(r *http.Request) bool {
	return r.TLS != nil || (s.proxied(r) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"))
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
	out["methods"] = s.auth.ActiveMethods()
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
	// Code is the authenticator app's; a code by email or text message
	// comes as Channel, Target and LoginCode instead of Name and Password.
	var req struct{ Name, Password, Code, Channel, Target, LoginCode string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	var tok string
	var u store.User
	var err error
	if req.Channel != "" {
		tok, u, err = s.auth.LoginWithCode(s.clientIP(r), r.UserAgent(), req.Channel, req.Target, req.LoginCode, req.Code)
	} else {
		tok, u, err = s.auth.Login(s.clientIP(r), r.UserAgent(), req.Name, req.Password, req.Code)
	}
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
	return map[string]any{"name": u.Name, "totp": u.TOTP, "changedAt": u.ChangedAt, "sessions": sessions,
		"email": u.Email, "phone": u.Phone, "methods": s.auth.SavedMethods(), "active": s.auth.ActiveMethods(),
		"mail": s.app.GetMailSettings(), "sms": s.app.GetSMSSettings(), "presets": app.MailPresets(), "adminOnly": s.auth.AdminOnly()}, nil
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
	return map[string]bool{"ok": true}, s.auth.ChangePassword(u.ID, tok, s.clientIP(r), req.Old, req.New)
}

func (s *Server) beginTOTP(_ http.ResponseWriter, r *http.Request) (any, error) {
	u, _, err := s.me(r)
	if err != nil {
		return nil, err
	}
	return s.auth.BeginTOTP(u.ID)
}

func (s *Server) enableTOTP(_ http.ResponseWriter, r *http.Request) (any, error) {
	u, tok, err := s.me(r)
	if err != nil {
		return nil, err
	}
	var req struct{ Code string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, s.auth.EnableTOTP(u.ID, tok, req.Code)
}

func (s *Server) disableTOTP(_ http.ResponseWriter, r *http.Request) (any, error) {
	u, tok, err := s.me(r)
	if err != nil {
		return nil, err
	}
	var req struct{ Password string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, s.auth.DisableTOTP(u.ID, tok, s.clientIP(r), req.Password)
}

func (s *Server) endSession(_ http.ResponseWriter, r *http.Request) (any, error) {
	u, _, err := s.me(r)
	if err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, s.auth.EndSession(u.ID, r.PathValue("key"))
}

// ---- Other ways of logging in ----

// ConnectSenders has login codes go out by the app's mail and
// text-message settings.
func ConnectSenders(a *app.App, au *auth.Service) {
	au.Send = a.SendCode
	au.Ready = func(channel string) bool {
		switch channel {
		case "email":
			return a.MailReady()
		case "sms":
			return a.SMSReady()
		}
		return false
	}
}

func (s *Server) loginCode(_ http.ResponseWriter, r *http.Request) (any, error) {
	if s.auth == nil {
		return nil, errDesktop()
	}
	var req struct{ Channel, Target string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := s.auth.SendLoginCode(r.Context(), s.clientIP(r), req.Channel, req.Target); err != nil {
		return nil, err
	}
	// The same answer whether or not the address is bound.
	return map[string]string{"done": "如果它绑定了账号，验证码已经发出，10 分钟内有效"}, nil
}

func (s *Server) setMethods(_ http.ResponseWriter, r *http.Request) (any, error) {
	u, _, err := s.me(r)
	if err != nil {
		return nil, err
	}
	var m auth.Methods
	if err := decode(r, &m); err != nil {
		return nil, err
	}
	if err := s.auth.SetMethods(u.ID, m); err != nil {
		return nil, err
	}
	return map[string]any{"methods": s.auth.SavedMethods(), "active": s.auth.ActiveMethods()}, nil
}

func (s *Server) setSendScope(_ http.ResponseWriter, r *http.Request) (any, error) {
	u, _, err := s.me(r)
	if err != nil {
		return nil, err
	}
	var req struct {
		On       bool
		Password string
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := s.auth.SetAdminOnly(u.ID, s.clientIP(r), req.On, req.Password); err != nil {
		return nil, err
	}
	return map[string]bool{"adminOnly": s.auth.AdminOnly()}, nil
}

func (s *Server) beginBind(_ http.ResponseWriter, r *http.Request) (any, error) {
	u, _, err := s.me(r)
	if err != nil {
		return nil, err
	}
	var req struct{ Channel, Target, Password string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, s.auth.BeginBind(r.Context(), u.ID, s.clientIP(r), req.Channel, req.Target, req.Password)
}

func (s *Server) confirmBind(_ http.ResponseWriter, r *http.Request) (any, error) {
	u, _, err := s.me(r)
	if err != nil {
		return nil, err
	}
	var req struct{ Channel, Code string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	target, err := s.auth.ConfirmBind(u.ID, s.clientIP(r), req.Channel, req.Code)
	return map[string]string{"target": target}, err
}

func (s *Server) unbind(_ http.ResponseWriter, r *http.Request) (any, error) {
	u, _, err := s.me(r)
	if err != nil {
		return nil, err
	}
	var req struct{ Channel, Password string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, s.auth.Unbind(u.ID, s.clientIP(r), req.Channel, req.Password)
}

// mailRequest is the SMTP form: the mailbox's own password (授权码) and,
// to save, the account's password, since whoever sets where login codes
// are sent from could read them.
type mailRequest struct {
	sender.SMTP
	SMTPPassword string `json:"smtpPassword"`
	Current      string `json:"current"`
	To           string `json:"to"`
}

func (s *Server) saveMail(_ http.ResponseWriter, r *http.Request) (any, error) {
	u, _, err := s.me(r)
	if err != nil {
		return nil, err
	}
	var req mailRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := s.auth.Confirm(u.ID, s.clientIP(r), req.Current); err != nil {
		return nil, err
	}
	req.SMTP.Password = req.SMTPPassword
	return s.app.SaveMailSettings(req.SMTP)
}

func (s *Server) clearMail(_ http.ResponseWriter, r *http.Request) (any, error) {
	u, _, err := s.me(r)
	if err != nil {
		return nil, err
	}
	var req struct{ Current string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := s.auth.Confirm(u.ID, s.clientIP(r), req.Current); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, s.app.ClearMailSettings()
}

func (s *Server) testMail(_ http.ResponseWriter, r *http.Request) (any, error) {
	u, _, err := s.me(r)
	if err != nil {
		return nil, err
	}
	var req mailRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	to, err := auth.NormEmail(req.To)
	if err != nil {
		return nil, err
	}
	if err := s.auth.CheckTestTarget(u.ID, "email", to); err != nil {
		return nil, err
	}
	req.To = to
	req.SMTP.Password = req.SMTPPassword
	return map[string]bool{"ok": true}, s.app.TestMail(r.Context(), req.SMTP, req.To)
}

type smsRequest struct {
	app.SMSSettings
	Current string `json:"current"`
	Phone   string `json:"phone"`
}

func (s *Server) saveSMS(_ http.ResponseWriter, r *http.Request) (any, error) {
	u, _, err := s.me(r)
	if err != nil {
		return nil, err
	}
	var req smsRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := s.auth.Confirm(u.ID, s.clientIP(r), req.Current); err != nil {
		return nil, err
	}
	return s.app.SaveSMSSettings(req.SMSSettings)
}

func (s *Server) clearSMS(_ http.ResponseWriter, r *http.Request) (any, error) {
	u, _, err := s.me(r)
	if err != nil {
		return nil, err
	}
	var req struct{ Current string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := s.auth.Confirm(u.ID, s.clientIP(r), req.Current); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, s.app.ClearSMSSettings()
}

func (s *Server) testSMS(_ http.ResponseWriter, r *http.Request) (any, error) {
	u, _, err := s.me(r)
	if err != nil {
		return nil, err
	}
	var req smsRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	phone, err := auth.NormPhone(req.Phone)
	if err != nil {
		return nil, err
	}
	if err := s.auth.CheckTestTarget(u.ID, "sms", phone); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, s.app.TestSMS(r.Context(), req.SMSSettings, phone)
}
