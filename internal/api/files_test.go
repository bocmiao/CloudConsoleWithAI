package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/app"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx/sshtest"
)

func TestFilesOverHTTP(t *testing.T) {
	s := newServer(t)
	ssh := sshtest.Start(t, "root", "pw")
	sv, err := s.app.AddServer(app.AddServerRequest{Name: "blog", Host: ssh.Host, Port: ssh.Port, Username: "root", AuthKind: "password", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s)
	defer srv.Close()
	dir := t.TempDir()
	base := fmt.Sprintf("/api/servers/%d/files", sv.ID)

	upload := func(overwrite string) *http.Response {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, _ := mw.CreateFormFile("file", "说明.txt")
		_, _ = io.WriteString(fw, "你好")
		_ = mw.Close()
		req, _ := http.NewRequest("POST", srv.URL+base+"/upload?dir="+dir+overwrite, &body)
		req.Host = "127.0.0.1:18765"
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.AddCookie(&http.Cookie{Name: cookieName, Value: "tok"})
		req.Header.Set("X-Miao", "1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	if resp := upload(""); resp.StatusCode != 200 {
		t.Fatalf("upload: %d", resp.StatusCode)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "说明.txt")); string(b) != "你好" {
		t.Fatalf("uploaded %q", b)
	}
	// Uploading onto a name says so with a code the page acts on.
	resp := upload("")
	var e struct{ Error, Code string }
	_ = json.NewDecoder(resp.Body).Decode(&e)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || e.Code != "conflict" {
		t.Fatalf("second upload: %d %+v", resp.StatusCode, e)
	}
	if resp := upload("&overwrite=1"); resp.StatusCode != 200 {
		t.Fatalf("overwrite: %d", resp.StatusCode)
	}

	if w := do(s, "GET", base+"?path="+dir, "127.0.0.1:18765", "", true); w.Code != 200 || !strings.Contains(w.Body.String(), "说明.txt") {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}

	// Downloads go through a one-time link that still needs the cookie.
	link := func() string {
		w := do(s, "POST", base+"/link", "127.0.0.1:18765", `{"path":"`+dir+`/说明.txt"}`, true)
		var l struct{ URL string }
		_ = json.Unmarshal(w.Body.Bytes(), &l)
		if !strings.HasPrefix(l.URL, "/dl/") {
			t.Fatalf("link: %s", w.Body.String())
		}
		return l.URL
	}
	u := link()
	if w := do(s, "GET", u, "127.0.0.1:18765", "", false); w.Code != http.StatusForbidden {
		t.Fatalf("download without the cookie: %d", w.Code)
	}
	u = link()
	get := func(u string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", u, nil)
		req.Host = "127.0.0.1:18765"
		req.AddCookie(&http.Cookie{Name: cookieName, Value: "tok"})
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		return w
	}
	w := get(u)
	if w.Code != 200 || w.Body.String() != "你好" || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment; filename*=UTF-8''%E8%AF%B4") {
		t.Fatalf("download: %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	if w := get(u); w.Code != http.StatusForbidden {
		t.Fatalf("link used twice: %d", w.Code)
	}
	if w := do(s, "POST", base+"/link", "127.0.0.1:18765", `{"path":"`+dir+`/说明.txt"}`, false); w.Code == 200 {
		t.Fatal("link made without a session")
	}
}
