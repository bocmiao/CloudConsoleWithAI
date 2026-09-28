package webui

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
)

// Handler serves the UI. Scripts and styles go out gzipped to browsers
// that take it (a quarter of the size), and with an ETag, so opening the
// page again only asks whether they changed.
func Handler() http.Handler {
	sub, _ := fs.Sub(Static, "static")
	return &assets{fs: sub, cache: map[string]*asset{}}
}

type assets struct {
	fs    fs.FS
	mu    sync.Mutex
	cache map[string]*asset
}

type asset struct {
	raw, gz []byte // gz is nil when compressing does not pay
	etag    string
	ctype   string
}

func (h *assets) get(name string) (*asset, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if a, ok := h.cache[name]; ok {
		return a, true
	}
	raw, err := fs.ReadFile(h.fs, name)
	if err != nil {
		return nil, false
	}
	sum := sha256.Sum256(raw)
	a := &asset{raw: raw, etag: `"` + hex.EncodeToString(sum[:8]) + `"`, ctype: mime.TypeByExtension(path.Ext(name))}
	if a.ctype == "" {
		a.ctype = http.DetectContentType(raw)
	}
	if len(raw) > 1024 {
		var b bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
		_, _ = zw.Write(raw)
		_ = zw.Close()
		if b.Len() < len(raw)*9/10 {
			a.gz = b.Bytes()
		}
	}
	h.cache[name] = a
	return a, true
}

func (h *assets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	a, ok := h.get(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", a.ctype)
	hd.Set("ETag", a.etag)
	hd.Set("Cache-Control", "no-cache") // use the copy, after asking
	hd.Set("Vary", "Accept-Encoding")
	if inm := r.Header.Get("If-None-Match"); inm != "" && strings.Contains(inm, a.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := a.raw
	if a.gz != nil && acceptsGzip(r.Header.Get("Accept-Encoding")) {
		hd.Set("Content-Encoding", "gzip")
		body = a.gz
	}
	hd.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func acceptsGzip(ae string) bool {
	for _, part := range strings.Split(ae, ",") {
		name, params, _ := strings.Cut(part, ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			q, err := strconv.ParseFloat(v, 64)
			return err == nil && q > 0
		}
		return true
	}
	return false
}
