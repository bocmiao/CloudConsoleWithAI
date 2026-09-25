// Package onepanel calls the 1Panel v2 API. Requests travel through the
// SSH connection to the panel's port on the server's own loopback, so the
// panel never has to be reachable from the internet, and 1Panel sees them
// as coming from 127.0.0.1 (the address users put in its IP whitelist).
package onepanel

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Dialer opens a TCP connection on the server side (an SSH direct-tcpip channel).
type Dialer func(network, addr string) (net.Conn, error)

// Client talks to one 1Panel instance.
type Client struct {
	hc     *http.Client
	Scheme string // "http" or "https"; Probe detects it
	host   string // Host header: the panel's bound domain, or 127.0.0.1:port
	key    string
	now    func() time.Time
}

// New creates a client for the panel listening on port. host overrides the
// Host header for panels that are bound to a domain.
func New(dial Dialer, port int, key, host, scheme string) *Client {
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if host == "" {
		host = target
	}
	if scheme == "" {
		scheme = "http"
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial("tcp", target) },
		// The connection already runs inside the authenticated SSH tunnel to
		// this server's loopback, and panels usually use self-signed certs.
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		ResponseHeaderTimeout: 2 * time.Minute,
	}
	return &Client{hc: &http.Client{Transport: tr, Timeout: 3 * time.Minute}, Scheme: scheme, host: host, key: key, now: time.Now}
}

// Error is an error reported by 1Panel.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return friendly(e.Message) }

// friendly explains 1Panel's API authentication errors in plain words.
func friendly(msg string) string {
	switch {
	case strings.Contains(msg, "ErrApiConfigStatusInvalid"):
		return "1Panel 的 API 接口还没有开启（面板设置 → API 接口）"
	case strings.Contains(msg, "ErrApiConfigKeyInvalid"):
		return "1Panel 的 API 密钥不对"
	case strings.Contains(msg, "ErrApiConfigIPInvalid"):
		return "1Panel API 接口的 IP 白名单里需要加上 127.0.0.1"
	case strings.Contains(msg, "ErrApiConfigKeyTimeInvalid"):
		return "请求时间不对：请检查服务器和电脑的时间是否准确"
	case strings.Contains(msg, "err_domain"):
		return "1Panel 设置了绑定域名，请在 Miao Panel 里填写这个域名"
	}
	return "1Panel 返回错误：" + msg
}

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (c *Client) sign(req *http.Request) {
	ts := strconv.FormatInt(c.now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(c.key))
	mac.Write([]byte("1panel:" + ts))
	req.Header.Set("1Panel-Token", hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("1Panel-Timestamp", ts)
	req.Header.Set("1Panel-Signature-Version", "hmac-sha256")
}

// do calls /api/v2+path and decodes the envelope's data into out.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Scheme+"://"+c.host+"/api/v2"+path, body)
	if err != nil {
		return err
	}
	req.Host = c.host
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.sign(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return &Error{Status: resp.StatusCode, Message: fmt.Sprintf("HTTP %d %s", resp.StatusCode, msg)}
	}
	if resp.StatusCode != http.StatusOK || env.Code != http.StatusOK {
		return &Error{Status: resp.StatusCode, Message: env.Message}
	}
	if out != nil && len(env.Data) > 0 {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

// Probe checks connectivity and credentials, switching to HTTPS when the
// panel has SSL enabled. It returns a short description of the host.
func (c *Client) Probe(ctx context.Context) (string, error) {
	var info struct {
		Hostname        string `json:"hostname"`
		OS              string `json:"os"`
		PlatformVersion string `json:"platformVersion"`
	}
	err := c.do(ctx, http.MethodGet, "/dashboard/base/os", nil, &info)
	var pe *Error
	if err != nil && c.Scheme == "http" && (!errors.As(err, &pe) || strings.Contains(pe.Message, "HTTPS")) {
		c.Scheme = "https"
		err = c.do(ctx, http.MethodGet, "/dashboard/base/os", nil, &info)
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(info.Hostname + " " + info.OS + " " + info.PlatformVersion), nil
}

// Runtime is a PHP (or other) runtime environment managed by 1Panel.
type Runtime struct {
	ID     uint   `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Type   string `json:"type"`
}

// PHPRuntimes lists the PHP runtime environments.
func (c *Client) PHPRuntimes(ctx context.Context) ([]Runtime, error) {
	var page struct {
		Items []Runtime `json:"items"`
	}
	err := c.do(ctx, http.MethodPost, "/runtimes/search",
		map[string]any{"page": 1, "pageSize": 100, "type": "php"}, &page)
	return page.Items, err
}

// FPMConfig returns the process-manager settings (pm, pm.max_children, ...).
func (c *Client) FPMConfig(ctx context.Context, runtimeID uint) (map[string]any, error) {
	var cfg struct {
		Params map[string]any `json:"params"`
	}
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/runtimes/php/fpm/config/%d", runtimeID), nil, &cfg)
	return cfg.Params, err
}

// UpdateFPMConfig writes settings into the runtime's [www] pool; 1Panel
// then restarts the runtime.
func (c *Client) UpdateFPMConfig(ctx context.Context, runtimeID uint, params map[string]string) error {
	return c.do(ctx, http.MethodPost, "/runtimes/php/fpm/config",
		map[string]any{"id": runtimeID, "params": params}, nil)
}

// MySQLVariables returns the current server variables of a MySQL/MariaDB app.
func (c *Client) MySQLVariables(ctx context.Context, dbType, name string) (map[string]string, error) {
	vars := map[string]string{}
	err := c.do(ctx, http.MethodPost, "/databases/variables", map[string]any{"type": dbType, "name": name}, &vars)
	return vars, err
}

// MySQLVariable is one variable to write. Numbers are sizes in bytes (1Panel
// writes them with K/M units); strings are written as-is.
type MySQLVariable struct {
	Param string `json:"param"`
	Value any    `json:"value"`
}

// UpdateMySQLVariables writes my.cnf values; 1Panel then restarts the app.
func (c *Client) UpdateMySQLVariables(ctx context.Context, dbType, name string, vars []MySQLVariable) error {
	return c.do(ctx, http.MethodPost, "/databases/variables/update",
		map[string]any{"type": dbType, "database": name, "variables": vars}, nil)
}
