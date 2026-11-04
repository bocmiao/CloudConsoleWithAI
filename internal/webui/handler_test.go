package webui

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	h := Handler()
	get := func(path string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	raw, _ := Static.ReadFile("static/app.js")

	w := get("/app.js", map[string]string{"Accept-Encoding": "gzip, deflate, br"})
	if w.Code != 200 || w.Header().Get("Content-Encoding") != "gzip" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") || w.Body.Len() >= len(raw)/2 {
		t.Fatalf("gzip app.js: %d %v %d bytes", w.Code, w.Header(), w.Body.Len())
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := io.ReadAll(zr); !bytes.Equal(got, raw) {
		t.Fatal("unzipped app.js differs")
	}
	etag := w.Header().Get("ETag")

	if w := get("/app.js", nil); w.Header().Get("Content-Encoding") != "" || !bytes.Equal(w.Body.Bytes(), raw) {
		t.Fatal("plain app.js wrong")
	}
	if w := get("/app.js", map[string]string{"Accept-Encoding": "gzip;q=0"}); w.Header().Get("Content-Encoding") != "" {
		t.Fatal("gzip sent though refused")
	}
	if w := get("/app.js", map[string]string{"If-None-Match": etag}); w.Code != http.StatusNotModified || w.Body.Len() != 0 {
		t.Fatalf("revalidation = %d", w.Code)
	}
	if w := get("/", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "<html") || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("index = %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	for _, p := range []string{"/nope.js", "/xterm", "/../embed.go"} {
		if w := get(p, nil); w.Code != 404 {
			t.Errorf("%s = %d", p, w.Code)
		}
	}
}
