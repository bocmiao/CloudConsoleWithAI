package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/app"
	"github.com/bocmiao/CloudConsoleWithAI/internal/secrets"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

const port = 18765

func newServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(app.New(st, secrets.OpenFile(t.TempDir())), "tok", port, "test")
}

func do(s *Server, method, path, host, body string, withSession bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = host
	if withSession {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: "tok"})
		req.Header.Set("X-Miao", "1")
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}

func TestHostHeaderIsChecked(t *testing.T) {
	s := newServer(t)
	if w := do(s, "GET", "/", "evil.example.com:18765", "", false); w.Code != http.StatusForbidden {
		t.Fatalf("foreign host: %d", w.Code)
	}
	if w := do(s, "GET", "/", "127.0.0.1:9999", "", false); w.Code != http.StatusForbidden {
		t.Fatalf("wrong port: %d", w.Code)
	}
	w := do(s, "GET", "/", "127.0.0.1:18765", "", false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Miao Panel") {
		t.Fatalf("index: %d", w.Code)
	}
}

func TestAPIRequiresSession(t *testing.T) {
	s := newServer(t)
	if w := do(s, "GET", "/api/servers", "127.0.0.1:18765", "", false); w.Code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", w.Code)
	}
	// Cookie without the X-Miao header (e.g. a cross-site form) is rejected.
	req := httptest.NewRequest("GET", "/api/servers", nil)
	req.Host = "127.0.0.1:18765"
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "tok"})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("cookie without header: %d", w.Code)
	}
	if w := do(s, "GET", "/api/servers", "127.0.0.1:18765", "", true); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("with session: %d %s", w.Code, w.Body.String())
	}
}

func TestAuthSetsCookie(t *testing.T) {
	s := newServer(t)
	if w := do(s, "GET", "/auth?token=wrong", "127.0.0.1:18765", "", false); w.Code != http.StatusForbidden {
		t.Fatalf("wrong token: %d", w.Code)
	}
	w := do(s, "GET", "/auth?token=tok", "localhost:18765", "", false)
	if w.Code != http.StatusFound {
		t.Fatalf("auth: %d", w.Code)
	}
	c := w.Result().Cookies()
	if len(c) != 1 || c[0].Value != "tok" || !c[0].HttpOnly || c[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie = %+v", c)
	}
}

func TestUserErrorsAreReadable(t *testing.T) {
	s := newServer(t)
	w := do(s, "POST", "/api/servers", "127.0.0.1:18765", `{"host":"","authKind":"password","password":"x"}`, true)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "IP") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	w = do(s, "GET", "/api/ai/presets", "127.0.0.1:18765", "", true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "deepseek-flash") {
		t.Fatalf("presets: %d", w.Code)
	}
}
