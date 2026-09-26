package certs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProbe(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	roots := srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs

	info, err := Probe(context.Background(), "example.com", addr, roots)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Valid || info.DaysLeft(time.Now()) < 30 || len(info.SHA256) != 64 || !strings.Contains(strings.Join(info.Names, ","), "example.com") {
		t.Fatalf("info = %+v", info)
	}
	// Without the test root the certificate is not trusted.
	bad, err := Probe(context.Background(), "example.com", addr, nil)
	if err != nil || bad.Valid || bad.Problem == "" {
		t.Fatalf("untrusted = %+v %v", bad, err)
	}
	if _, err := Parse("not a cert"); err == nil {
		t.Fatal("parsed garbage")
	}
}
