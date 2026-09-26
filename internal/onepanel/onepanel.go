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
	// Trace, when set, is told about every request (never the credentials).
	Trace func(method, path string, body []byte)
}

// New creates a client for the panel listening on port. host overrides the
// Host header for panels that are bound to a domain.
func New(dial Dialer, port int, key, host, scheme string) *Client {
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial("tcp", target) },
		// The connection already runs inside the authenticated SSH tunnel to
		// this server's loopback, and panels usually use self-signed certs.
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		ResponseHeaderTimeout: 2 * time.Minute,
	}
	return newClient(tr, target, key, host, scheme)
}

// Shell runs a command on the server with stdin.
type Shell func(ctx context.Context, cmd, stdin string, maxOut int) (stdout, stderr string, exitCode int, err error)

// NewOverShell creates a client for servers without an SSH tunnel (such as
// ones reached through Tencent Cloud's automation agent): each request
// runs curl on the server against the panel's loopback port.
func NewOverShell(sh Shell, port int, key, host, scheme string) *Client {
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	return newClient(&curlTransport{sh: sh, target: target}, target, key, host, scheme)
}

func newClient(rt http.RoundTripper, target, key, host, scheme string) *Client {
	if host == "" {
		host = target
	}
	if scheme == "" {
		scheme = "http"
	}
	return &Client{hc: &http.Client{Transport: rt, Timeout: 3 * time.Minute}, Scheme: scheme, host: host, key: key, now: time.Now}
}

// curlTransport sends a request by running curl on the server.
type curlTransport struct {
	sh     Shell
	target string
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

const statusMark = "\nMIAOHTTP "

// redactKeys blanks private keys in the panel's answers on the server, so
// they never appear in command output that is stored elsewhere (such as
// the automation agent's execution records).
const redactKeys = ` | sed -E 's/"privateKey":"[^"]*"/"privateKey":""/g'`

func (t *curlTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		if body, err = io.ReadAll(req.Body); err != nil {
			return nil, err
		}
		req.Body.Close()
	}
	u := req.URL.Scheme + "://" + t.target + req.URL.EscapedPath()
	if req.URL.RawQuery != "" {
		u += "?" + req.URL.RawQuery
	}
	cmd := []string{"curl", "-sS", "-k", "--noproxy", "'*'", "-m", "170", "-X", req.Method, "-o", "-", "-w", shq(statusMark + "%{http_code}"), "-H", shq("Host: " + req.Host)}
	for k, vs := range req.Header {
		for _, v := range vs {
			cmd = append(cmd, "-H", shq(k+": "+v))
		}
	}
	if body != nil {
		cmd = append(cmd, "--data-binary", "@-")
	}
	cmd = append(cmd, shq(u))
	line := "{ " + strings.Join(cmd, " ") + "; rc=$?; echo; echo MIAOCURL $rc; }" + redactKeys
	stdout, stderr, _, err := t.sh(req.Context(), line, string(body), 8<<20)
	if err != nil {
		return nil, err
	}
	end := strings.LastIndex(stdout, "\nMIAOCURL ")
	if end < 0 {
		return nil, fmt.Errorf("curl 返回了无法识别的内容：%.200s", stdout)
	}
	code, _ := strconv.Atoi(strings.TrimSpace(stdout[end+len("\nMIAOCURL "):]))
	switch {
	case code == 127:
		return nil, errors.New("服务器上没有 curl，无法调用 1Panel 接口")
	case code != 0:
		return nil, fmt.Errorf("curl 连接 1Panel 失败（退出码 %d）：%s", code, strings.TrimSpace(stderr))
	}
	stdout = stdout[:end]
	i := strings.LastIndex(stdout, statusMark)
	if i < 0 {
		return nil, fmt.Errorf("curl 返回了无法识别的内容：%.200s", stdout)
	}
	status, _ := strconv.Atoi(strings.TrimSpace(stdout[i+len(statusMark):]))
	return &http.Response{
		StatusCode: status, Status: http.StatusText(status), Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stdout[:i])), Request: req,
	}, nil
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
	var data []byte
	if in != nil {
		var err error
		if data, err = json.Marshal(in); err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	if c.Trace != nil {
		c.Trace(method, "/api/v2"+path, data)
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

// InstalledApp is an app installed from the 1Panel app store.
type InstalledApp struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	AppKey      string `json:"appKey"`
	Container   string `json:"container"`
	ServiceName string `json:"serviceName"` // the app's service in its docker-compose file
	Status      string `json:"status"`
	Version     string `json:"version"`
	HTTPPort    int    `json:"httpPort"` // the port the app is published on, if any
	HTTPSPort   int    `json:"httpsPort"`
}

// InstalledApps lists the installed apps.
func (c *Client) InstalledApps(ctx context.Context) ([]InstalledApp, error) {
	var page struct {
		Items []InstalledApp `json:"items"`
	}
	err := c.do(ctx, http.MethodPost, "/apps/installed/search", map[string]any{"page": 1, "pageSize": 200}, &page)
	return page.Items, err
}

// ContainerConfig is the "advanced settings" of an installed app: resource
// limits and how its ports are published.
type ContainerConfig struct {
	CPUQuota      float64 `json:"cpuQuota"`
	MemoryLimit   float64 `json:"memoryLimit"` // 0 means no limit
	MemoryUnit    string  `json:"memoryUnit"`
	ContainerName string  `json:"containerName"`
	AllowPort     bool    `json:"allowPort"`
	SpecifyIP     string  `json:"specifyIP"`
	HostMode      bool    `json:"hostMode"`
	RestartPolicy string  `json:"restartPolicy"`
	DockerCompose string  `json:"dockerCompose"` // the app's docker-compose file
}

// AppConfig returns an installed app's container settings.
func (c *Client) AppConfig(ctx context.Context, installID uint) (ContainerConfig, error) {
	var cfg ContainerConfig
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/apps/installed/params/%d", installID), nil, &cfg)
	return cfg, err
}

// UpdateAppConfig writes an installed app's container settings; 1Panel
// then rebuilds its containers. Every other setting is sent back exactly
// as AppConfig returned it, so that only the limits change. A non-empty
// compose replaces the app's docker-compose file as well.
func (c *Client) UpdateAppConfig(ctx context.Context, installID uint, cfg ContainerConfig, compose string) error {
	body := map[string]any{
		"installId": installID, "params": map[string]any{}, "advanced": true, "editCompose": false,
		"cpuQuota": cfg.CPUQuota, "memoryLimit": cfg.MemoryLimit, "memoryUnit": cfg.MemoryUnit,
		"containerName": cfg.ContainerName, "allowPort": cfg.AllowPort, "specifyIP": cfg.SpecifyIP,
		"hostMode": cfg.HostMode, "restartPolicy": cfg.RestartPolicy,
	}
	if compose != "" {
		body["editCompose"], body["dockerCompose"] = true, compose
	}
	return c.do(ctx, http.MethodPost, "/apps/installed/params/update", body, nil)
}

// Databases lists the databases 1Panel manages inside a MySQL/MariaDB app.
func (c *Client) Databases(ctx context.Context, app string) ([]string, error) {
	var page struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	err := c.do(ctx, http.MethodPost, "/databases/search",
		map[string]any{"page": 1, "pageSize": 200, "database": app, "orderBy": "createdAt", "order": "null"}, &page)
	var out []string
	for _, it := range page.Items {
		out = append(out, it.Name)
	}
	return out, err
}

// Backup asks 1Panel to back something up into its local backup account.
// kind is "app" (name = app key, detail = install name) or a database type
// such as "mysql" (name = database app, detail = database name).
func (c *Client) Backup(ctx context.Context, kind, name, detail, taskID string) error {
	return c.do(ctx, http.MethodPost, "/backups/backup", map[string]any{
		"type": kind, "name": name, "detailName": detail, "taskID": taskID,
		"description": "Miao Panel 修改前备份",
	}, nil)
}

// BackupRecord is one entry in 1Panel's backup list.
type BackupRecord struct {
	TaskID   string `json:"taskID"`
	Status   string `json:"status"` // Waiting, Success, Failed
	Message  string `json:"message"`
	FileDir  string `json:"fileDir"`
	FileName string `json:"fileName"`
}

// FindBackup looks up the record of the backup started with taskID.
func (c *Client) FindBackup(ctx context.Context, kind, name, detail, taskID string) (BackupRecord, bool, error) {
	var page struct {
		Items []BackupRecord `json:"items"`
	}
	err := c.do(ctx, http.MethodPost, "/backups/record/search",
		map[string]any{"page": 1, "pageSize": 20, "type": kind, "name": name, "detailName": detail}, &page)
	for _, r := range page.Items {
		if r.TaskID == taskID {
			return r, true, err
		}
	}
	return BackupRecord{}, false, err
}

// Website is a site on 1Panel's 网站 page (served by its OpenResty).
type Website struct {
	ID            uint   `json:"id"`
	PrimaryDomain string `json:"primaryDomain"`
	Alias         string `json:"alias"`
	Type          string `json:"type"` // static, proxy, deployment, runtime, subsite, stream
	Status        string `json:"status"`
	Proxy         string `json:"proxy"`
	SitePath      string `json:"sitePath"`
}

// Websites lists all websites.
func (c *Client) Websites(ctx context.Context) ([]Website, error) {
	var list []Website
	err := c.do(ctx, http.MethodGet, "/websites/list", nil, &list)
	return list, err
}

// WebsiteGroup returns the default website group, which new sites go in.
func (c *Client) WebsiteGroup(ctx context.Context) (uint, error) {
	var groups []struct {
		ID        uint `json:"id"`
		IsDefault bool `json:"isDefault"`
	}
	if err := c.do(ctx, http.MethodPost, "/groups/search", map[string]any{"type": "website"}, &groups); err != nil {
		return 0, err
	}
	for _, g := range groups {
		if g.IsDefault {
			return g.ID, nil
		}
	}
	if len(groups) > 0 {
		return groups[0].ID, nil
	}
	return 0, errors.New("1Panel 里没有网站分组")
}

// NewWebsite is what creating a static or reverse-proxy site needs.
type NewWebsite struct {
	Type    string // static or proxy
	Domain  string
	Alias   string // the site's directory name under /www/sites
	Proxy   string // for proxy sites, e.g. http://127.0.0.1:8090
	Port    int    // OpenResty's HTTP port
	GroupID uint
}

// CreateWebsite adds a site; 1Panel writes its OpenResty config and
// reloads it before answering.
func (c *Client) CreateWebsite(ctx context.Context, w NewWebsite) error {
	in := map[string]any{
		"type": w.Type, "alias": w.Alias, "webSiteGroupID": w.GroupID, "remark": "Miao Panel",
		"domains": []map[string]any{{"domain": w.Domain, "port": w.Port, "ssl": false}},
		// Only used for sites that install an app, but always validated.
		"appType": "installed",
	}
	if w.Type == "proxy" {
		in["proxy"] = w.Proxy
	}
	return c.do(ctx, http.MethodPost, "/websites", in, nil)
}

// DeleteWebsite removes a site with its OpenResty config and directory,
// keeping any app and database it uses.
func (c *Client) DeleteWebsite(ctx context.Context, id uint) error {
	return c.do(ctx, http.MethodPost, "/websites/del",
		map[string]any{"id": id, "deleteApp": false, "deleteBackup": false, "forceDelete": false, "deleteDB": false}, nil)
}
