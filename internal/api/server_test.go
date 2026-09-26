package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/bocmiao/CloudConsoleWithAI/internal/app"
	"github.com/bocmiao/CloudConsoleWithAI/internal/auth"
	"github.com/bocmiao/CloudConsoleWithAI/internal/secrets"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

func newWebServer(t *testing.T) (*Server, *auth.Service) {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	dir := t.TempDir()
	sec := secrets.OpenFile(dir)
	au := auth.New(st, sec, dir)
	au.Cost = bcrypt.MinCost
	return NewServer(app.New(st, sec), au, "test"), au
}

type call struct {
	method, path, body, cookie, remote string
	header                             map[string]string
	noMiao                             bool
}

func (c call) do(s *Server) *httptest.ResponseRecorder {
	req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
	req.Host = "panel.example.com"
	if c.remote != "" {
		req.RemoteAddr = c.remote
	}
	if !c.noMiao {
		req.Header.Set("X-Miao", "1")
	}
	for k, v := range c.header {
		req.Header.Set(k, v)
	}
	if c.cookie != "" {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: c.cookie})
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}

func sessionCookie(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName {
			return c
		}
	}
	return nil
}

func TestWebEditionLogin(t *testing.T) {
	s, au := newWebServer(t)
	// Nothing works before logging in, from any host name.
	if w := (call{method: "GET", path: "/api/servers"}).do(s); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"login"`) {
		t.Fatalf("servers before login: %d %s", w.Code, w.Body.String())
	}
	w := call{method: "GET", path: "/api/auth/state"}.do(s)
	var st struct {
		Mode  string
		Setup bool
		User  *struct{ Name string }
	}
	_ = json.Unmarshal(w.Body.Bytes(), &st)
	if w.Code != 200 || st.Mode != "server" || !st.Setup || st.User != nil {
		t.Fatalf("state: %d %s", w.Code, w.Body.String())
	}
	if w := (call{method: "GET", path: "/auth?token=x"}).do(s); w.Code == http.StatusFound {
		t.Fatal("the desktop's launch link works on the web edition")
	}
	// Another site cannot post a login form: the X-Miao header is needed.
	if w := (call{method: "POST", path: "/api/auth/login", body: `{"name":"a","password":"b"}`, noMiao: true}).do(s); w.Code != http.StatusForbidden {
		t.Fatalf("login without X-Miao: %d", w.Code)
	}

	code, _ := au.SetupCode()
	if w := (call{method: "POST", path: "/api/auth/setup", body: `{"code":"nope","name":"admin","password":"correct horse"}`}).do(s); w.Code != 400 || !strings.Contains(w.Body.String(), "bad_setup") {
		t.Fatalf("bad setup code: %d %s", w.Code, w.Body.String())
	}
	// Behind an HTTPS reverse proxy on the same machine the cookie is Secure.
	w = call{method: "POST", path: "/api/auth/setup", body: `{"code":"` + code + `","name":"admin","password":"correct horse"}`,
		remote: "127.0.0.1:5555", header: map[string]string{"X-Forwarded-Proto": "https", "X-Real-IP": "203.0.113.7"}}.do(s)
	c := sessionCookie(w)
	if w.Code != 200 || c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || w.Header().Get("Strict-Transport-Security") == "" {
		t.Fatalf("setup: %d %s %+v", w.Code, w.Body.String(), c)
	}
	if w := (call{method: "GET", path: "/api/servers", cookie: c.Value}).do(s); w.Code != 200 {
		t.Fatalf("servers after setup: %d %s", w.Code, w.Body.String())
	}
	if w := (call{method: "GET", path: "/api/servers", cookie: c.Value, noMiao: true}).do(s); w.Code != http.StatusUnauthorized {
		t.Fatalf("cookie without X-Miao: %d", w.Code)
	}
	w = call{method: "GET", path: "/api/account", cookie: c.Value, remote: "127.0.0.1:5555", header: map[string]string{"X-Real-IP": "203.0.113.7"}}.do(s)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"admin"`) || !strings.Contains(w.Body.String(), "203.0.113.7") {
		t.Fatalf("account: %d %s", w.Code, w.Body.String())
	}

	// Plain HTTP from outside: not Secure, no HSTS, and a forged
	// X-Forwarded-For does not change who is guessing.
	for i := 0; i < 5; i++ {
		w = call{method: "POST", path: "/api/auth/login", body: `{"name":"admin","password":"wrong one here"}`,
			remote: "198.51.100.9:4000", header: map[string]string{"X-Forwarded-For": "10.9.9." + string(rune('0'+i))}}.do(s)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "bad_login") {
			t.Fatalf("wrong password %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	if w := (call{method: "POST", path: "/api/auth/login", body: `{"name":"admin","password":"correct horse"}`, remote: "198.51.100.9:4000"}).do(s); w.Code != http.StatusTooManyRequests {
		t.Fatalf("locked address: %d %s", w.Code, w.Body.String())
	}
	w = call{method: "POST", path: "/api/auth/login", body: `{"name":"admin","password":"correct horse"}`, remote: "198.51.100.10:4000"}.do(s)
	c2 := sessionCookie(w)
	if w.Code != 200 || c2 == nil || c2.Secure || w.Header().Get("Strict-Transport-Security") != "" {
		t.Fatalf("login over http: %d %s %+v", w.Code, w.Body.String(), c2)
	}

	// Downloads need the session too.
	if w := (call{method: "GET", path: "/dl/whatever", noMiao: true}).do(s); w.Code != http.StatusForbidden {
		t.Fatalf("download without login: %d", w.Code)
	}

	if w := (call{method: "POST", path: "/api/auth/logout", cookie: c2.Value}).do(s); w.Code != 200 {
		t.Fatalf("logout: %d", w.Code)
	}
	if w := (call{method: "GET", path: "/api/servers", cookie: c2.Value}).do(s); w.Code != http.StatusUnauthorized {
		t.Fatalf("after logout: %d", w.Code)
	}
	if w := (call{method: "GET", path: "/api/servers", cookie: c.Value}).do(s); w.Code != 200 {
		t.Fatalf("the other session ended too: %d", w.Code)
	}
}

func TestDesktopHasNoAccounts(t *testing.T) {
	s := newServer(t)
	w := do(s, "GET", "/api/auth/state", "127.0.0.1:18765", "", true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"mode":"desktop"`) {
		t.Fatalf("state: %d %s", w.Code, w.Body.String())
	}
	if w := do(s, "POST", "/api/auth/login", "127.0.0.1:18765", `{"name":"a","password":"b"}`, true); w.Code != 400 {
		t.Fatalf("desktop login: %d", w.Code)
	}
	if w := do(s, "GET", "/api/auth/state", "evil.example.com:18765", "", true); w.Code != http.StatusForbidden {
		t.Fatalf("desktop host check: %d", w.Code)
	}
}
