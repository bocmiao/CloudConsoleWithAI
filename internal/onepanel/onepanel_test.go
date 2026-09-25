package onepanel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

const key = "test-api-key"

// fakePanel checks 1Panel's HMAC authentication and serves a few routes.
func fakePanel(t *testing.T, bodies *[]map[string]any) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts := r.Header.Get("1Panel-Timestamp")
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte("1panel:" + ts))
		n, _ := strconv.ParseInt(ts, 10, 64)
		if r.Header.Get("1Panel-Signature-Version") != "hmac-sha256" ||
			r.Header.Get("1Panel-Token") != hex.EncodeToString(mac.Sum(nil)) || time.Now().Unix()-n > 60 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"code":401,"message":"ErrApiConfigKeyInvalid"}`)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		*bodies = append(*bodies, body)
		reply := func(data string) { _, _ = io.WriteString(w, `{"code":200,"message":"","data":`+data+`}`) }
		switch r.URL.Path {
		case "/api/v2/dashboard/base/os":
			reply(`{"hostname":"blog","os":"ubuntu","platformVersion":"24.04"}`)
		case "/api/v2/runtimes/search":
			reply(`{"total":1,"items":[{"id":3,"name":"php82","status":"running","type":"php"}]}`)
		case "/api/v2/runtimes/php/fpm/config/3":
			reply(`{"id":3,"params":{"pm":"dynamic","pm.max_children":"30"}}`)
		case "/api/v2/runtimes/php/fpm/config", "/api/v2/databases/variables/update":
			reply(`null`)
		case "/api/v2/databases/variables":
			reply(`{"innodb_buffer_pool_size":"1073741824","max_connections":"500"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":404,"message":"not found"}`)
		}
	})
}

func dialTo(addr string) Dialer {
	return func(network, _ string) (net.Conn, error) { return net.Dial(network, addr) }
}

func TestClientCallsAndSigning(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(fakePanel(t, &bodies))
	defer srv.Close()
	c := New(dialTo(srv.Listener.Addr().String()), 12345, key, "", "")
	ctx := context.Background()

	info, err := c.Probe(ctx)
	if err != nil || info != "blog ubuntu 24.04" || c.Scheme != "http" {
		t.Fatalf("probe: %q %v scheme=%s", info, err, c.Scheme)
	}
	rts, err := c.PHPRuntimes(ctx)
	if err != nil || len(rts) != 1 || rts[0].ID != 3 {
		t.Fatalf("runtimes: %+v %v", rts, err)
	}
	cfg, err := c.FPMConfig(ctx, 3)
	if err != nil || cfg["pm.max_children"] != "30" {
		t.Fatalf("fpm config: %v %v", cfg, err)
	}
	if err := c.UpdateFPMConfig(ctx, 3, map[string]string{"pm.max_children": "10"}); err != nil {
		t.Fatal(err)
	}
	last := bodies[len(bodies)-1]
	if last["id"].(float64) != 3 || last["params"].(map[string]any)["pm.max_children"] != "10" {
		t.Fatalf("update body = %v", last)
	}
	vars, err := c.MySQLVariables(ctx, "mysql", "mysql")
	if err != nil || vars["max_connections"] != "500" {
		t.Fatalf("variables: %v %v", vars, err)
	}
	err = c.UpdateMySQLVariables(ctx, "mysql", "mysql", []MySQLVariable{
		{Param: "innodb_buffer_pool_size", Value: float64(256 << 20)}, {Param: "max_connections", Value: "1024"}})
	if err != nil {
		t.Fatal(err)
	}
	last = bodies[len(bodies)-1]
	v := last["variables"].([]any)
	if last["database"] != "mysql" || v[0].(map[string]any)["value"].(float64) != 256<<20 || v[1].(map[string]any)["value"] != "1024" {
		t.Fatalf("variables body = %v", last)
	}
}

func TestWrongKeyIsExplained(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(fakePanel(t, &bodies))
	defer srv.Close()
	c := New(dialTo(srv.Listener.Addr().String()), 12345, "wrong", "", "")
	_, err := c.Probe(context.Background())
	if err == nil || !strings.Contains(err.Error(), "API 密钥不对") {
		t.Fatalf("err = %v", err)
	}
}

func TestProbeSwitchesToHTTPS(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewTLSServer(fakePanel(t, &bodies))
	defer srv.Close()
	c := New(dialTo(srv.Listener.Addr().String()), 12345, key, "", "")
	info, err := c.Probe(context.Background())
	if err != nil || c.Scheme != "https" || info == "" {
		t.Fatalf("probe over TLS: %q %v scheme=%s", info, err, c.Scheme)
	}
}

func TestBoundDomainHostHeader(t *testing.T) {
	var gotHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		_, _ = io.WriteString(w, `{"code":200,"data":{}}`)
	}))
	defer srv.Close()
	c := New(dialTo(srv.Listener.Addr().String()), 12345, key, "panel.example.com", "")
	if _, err := c.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotHost != "panel.example.com" {
		t.Fatalf("Host = %q", gotHost)
	}
}

// localShell runs commands with the local sh, standing in for a server.
func localShell(ctx context.Context, cmd, stdin string, _ int) (string, string, int, error) {
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Stdin = strings.NewReader(stdin)
	var out, errb strings.Builder
	c.Stdout, c.Stderr = &out, &errb
	err := c.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.String(), errb.String(), ee.ExitCode(), nil
	}
	return out.String(), errb.String(), 0, err
}

func portOf(t *testing.T, srv *httptest.Server) int {
	_, p, _ := net.SplitHostPort(srv.Listener.Addr().String())
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestClientOverShell(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	var bodies []map[string]any
	srv := httptest.NewServer(fakePanel(t, &bodies))
	defer srv.Close()
	c := NewOverShell(localShell, portOf(t, srv), key, "", "")
	ctx := context.Background()
	if info, err := c.Probe(ctx); err != nil || info != "blog ubuntu 24.04" {
		t.Fatalf("probe: %q %v", info, err)
	}
	if err := c.UpdateFPMConfig(ctx, 3, map[string]string{"pm.max_children": "it's 10"}); err != nil {
		t.Fatal(err)
	}
	last := bodies[len(bodies)-1]
	if last["params"].(map[string]any)["pm.max_children"] != "it's 10" {
		t.Fatalf("body = %v", last)
	}
	if _, err := NewOverShell(localShell, portOf(t, srv), "wrong", "", "").Probe(ctx); err == nil || !strings.Contains(err.Error(), "API 密钥不对") {
		t.Fatalf("wrong key: %v", err)
	}

	tls := httptest.NewTLSServer(fakePanel(t, &bodies))
	defer tls.Close()
	c = NewOverShell(localShell, portOf(t, tls), key, "", "")
	if _, err := c.Probe(ctx); err != nil || c.Scheme != "https" {
		t.Fatalf("probe over TLS: %v scheme=%s", err, c.Scheme)
	}
}
