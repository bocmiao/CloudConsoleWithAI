package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBaizeRoutesKeepTokenPrivate(t *testing.T) {
	const token = "baize-route-secret"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("missing upstream authorization")
		}
		_, _ = w.Write([]byte(`{"totals":{"pv":3,"uv":null,"sessions":2},"coverage":1,"rangeStatus":"complete","pages":[],"sources":[]}`))
	}))
	defer upstream.Close()
	s := newServer(t)
	host := "127.0.0.1:18765"
	body := fmt.Sprintf(`{"baseUrl":%q,"siteId":"1234567890abcdef12345678","name":"博客","token":%q}`, upstream.URL, token)
	if w := do(s, "PUT", "/api/baize/settings", host, body, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated save: %d", w.Code)
	}
	w := do(s, "PUT", "/api/baize/settings", host, body, true)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), token) {
		t.Fatalf("save leaked token or failed: %d %s", w.Code, w.Body.String())
	}
	w = do(s, "GET", "/api/baize/settings", host, "", true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"hasToken":true`) || strings.Contains(w.Body.String(), token) {
		t.Fatalf("settings leaked token or failed: %d %s", w.Code, w.Body.String())
	}
	w = do(s, "GET", "/api/baize/stats?days=7", host, "", true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"pv":3`) || !strings.Contains(w.Body.String(), `"uv":null`) {
		t.Fatalf("stats failed: %d %s", w.Code, w.Body.String())
	}
}
