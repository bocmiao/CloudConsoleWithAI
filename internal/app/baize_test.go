package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBaizeStatsAndSecretBoundary(t *testing.T) {
	const token = "baize-read-secret"
	const siteID = "1234567890abcdef12345678"
	var requested bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
		if r.URL.Path != "/api/v1/sites/"+siteID+"/stats" || r.Header.Get("Authorization") != "Bearer "+token ||
			r.URL.Query().Get("from") == "" || r.URL.Query().Get("to") == "" {
			t.Errorf("unexpected Baize request: %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"totals":{"pv":42,"uv":null,"sessions":9,"bounceRate":0.25,"averageDuration":12},"coverage":0.75,"rangeStatus":"partial_unavailable","uvStatus":"unsupported_in_this_mode","watermark":null,"pages":[{"path":"/a","pv":30,"uv":null}],"sources":[{"channel":"direct","pv":20}]}`))
	}))
	defer srv.Close()
	a := newApp(t)
	s, err := a.SaveBaizeSettings(BaizeSettings{BaseURL: srv.URL, SiteID: siteID, Name: "博客"}, token)
	if err != nil || !s.HasToken {
		t.Fatalf("save settings: %+v, %v", s, err)
	}
	encoded, _ := json.Marshal(s)
	if strings.Contains(string(encoded), token) {
		t.Fatal("settings response leaked token")
	}
	stored, err := a.Store.Setting(baizeSettingKey)
	if err != nil || strings.Contains(stored, token) {
		t.Fatalf("database setting leaked token: %v", err)
	}
	if _, err := a.SaveBaizeSettings(BaizeSettings{BaseURL: "https://another.example.com", SiteID: siteID}, ""); err == nil {
		t.Fatal("reused token after changing Baize host")
	}
	report, err := a.BaizeStats(context.Background(), 7)
	if err != nil || !requested {
		t.Fatalf("stats request: %v", err)
	}
	if report.PV == nil || *report.PV != 42 || report.UV != nil || report.Coverage == nil || *report.Coverage != .75 || len(report.Pages) != 1 {
		t.Fatalf("bad report: %+v", report)
	}
	if err := a.DeleteBaizeSettings(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.BaizeStats(context.Background(), 7); err == nil {
		t.Fatal("deleted connection remained usable")
	}
}

func TestBaizeRejectsUnsafeURLAndRedirect(t *testing.T) {
	a := newApp(t)
	for _, base := range []string{"http://localhost.evil.com", "http://example.com", "https://user:pass@example.com", "https://example.com/?token=x"} {
		if _, err := a.SaveBaizeSettings(BaizeSettings{BaseURL: base, SiteID: "1234567890abcdef12345678"}, "read-token"); err == nil {
			t.Errorf("accepted unsafe URL %q", base)
		}
	}
	secretSeen := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secretSeen = r.Header.Get("Authorization") != ""
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	if _, err := a.SaveBaizeSettings(BaizeSettings{BaseURL: redirect.URL, SiteID: "1234567890abcdef12345678"}, "read-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.BaizeStats(context.Background(), 1); err == nil {
		t.Fatal("redirect accepted as statistics")
	}
	if secretSeen {
		t.Fatal("token forwarded to redirect target")
	}
}
