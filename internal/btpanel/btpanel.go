// Package btpanel calls the API of 宝塔面板 (BT Panel) and of aaPanel, its
// international edition. Like onepanel, requests go to the panel's port on
// the server's own loopback through a transport the caller has routed
// there (an SSH tunnel, or curl run on the server), so the panel sees them
// come from 127.0.0.1, the address users put in its API whitelist.
//
// Every action and parameter follows the panel's own source: 宝塔 11.8
// (BTPanel/__init__.py for the routes, class/common.py for the API check,
// class/panelSite.py, files.py, data.py, database.py, system.py and
// acme_v2.py for the actions), checked against aaPanel 8.22 where the two
// differ.
//
// Some answers carry private keys (GetSSL's "key", the Let's Encrypt
// order's "private_key"). The types here drop them, but a transport that
// records what it carries, such as curl run through a cloud agent, should
// blank those fields.
package btpanel

import (
	"context"
	"crypto/md5" //nolint:gosec // the panel's token scheme, not our choice
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Client talks to one BT panel.
type Client struct {
	hc     *http.Client
	Scheme string // "http" or "https"; Probe detects it
	host   string // 127.0.0.1:port
	key    string
	now    func() time.Time
	// Trace, when set, is told about every request (never the credentials).
	Trace func(method, path string, body []byte)
}

// New creates a client for the panel listening on port. rt must carry
// requests for 127.0.0.1:port to the panel and, for HTTPS, accept the
// self-signed certificate panels use.
func New(rt http.RoundTripper, port int, key, scheme string) *Client {
	if scheme == "" {
		scheme = "http"
	}
	// The panel keeps sessions as files (SESSION_TYPE filesystem): without
	// its cookie every call would leave a new one behind.
	jar, _ := cookiejar.New(nil)
	return &Client{
		hc: &http.Client{
			Transport: rt,
			Jar:       jar,
			// Backups and certificate orders run inside the call.
			Timeout: 10 * time.Minute,
			// A redirect means the panel took us for a browser that is not
			// logged in; report it instead of fetching the login page.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		Scheme: scheme,
		host:   net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		key:    strings.TrimSpace(key),
		now:    time.Now,
	}
}

// Reason sorts the panel's refusals, so the setup problems users can fix
// get explained.
type Reason int

const (
	// ReasonRefused means the panel turned the operation down; Message
	// says why.
	ReasonRefused Reason = iota
	// ReasonNotFound means the panel answered with its 404 page. It does
	// that while the API is off, and also when an action fails inside
	// the panel (usually a parameter it reads without checking).
	ReasonNotFound
	// ReasonIPNotAllowed means the address the panel saw is not in the
	// API whitelist.
	ReasonIPNotAllowed
	// ReasonBadKey means the API key is wrong.
	ReasonBadKey
	// ReasonLockedOut means 20 failed checks in a row: the panel refuses
	// the address for an hour.
	ReasonLockedOut
	// ReasonUnknownAction means the panel has no such action (another
	// edition or version); it gives the same answer for some bad
	// parameters.
	ReasonUnknownAction
	// ReasonConfigRejected means a written config failed the web server's
	// test and the panel put the old file back.
	ReasonConfigRejected
	// ReasonConfigInvalid means TestNginx found the configuration broken.
	ReasonConfigInvalid
	// ReasonBadAnswer means the answer was not the panel's JSON: another
	// service on the port, or the wrong scheme.
	ReasonBadAnswer
)

// Error is an error reported by the panel.
type Error struct {
	Status  int    // HTTP status of the answer
	Action  string // module/action, such as site/AddSite
	Message string // what the panel said, without its HTML
	Reason  Reason
}

var bracketRe = regexp.MustCompile(`\[([^\]]+)\]`)

func (e *Error) Error() string {
	switch e.Reason {
	case ReasonNotFound:
		return "宝塔面板返回了 404 页面：如果还没有开启 API，请到 面板设置 → API 接口 开启，并把 127.0.0.1 加入 IP 白名单；" +
			"已经开启的话，是面板处理 " + e.Action + " 时出错了"
	case ReasonIPNotAllowed:
		ip := "127.0.0.1"
		if m := bracketRe.FindStringSubmatch(e.Message); m != nil {
			ip = m[1]
		}
		return "宝塔面板 API 接口的 IP 白名单里没有 " + ip + "：请到 面板设置 → API 接口，把 " + ip + " 加入 IP 白名单"
	case ReasonBadKey:
		return "宝塔面板的 API 密钥不对：请到 面板设置 → API 接口 复制接口密钥"
	case ReasonLockedOut:
		return "宝塔面板因为连续 20 次 API 验证失败，暂停了这个地址的 API 访问 1 小时：请先确认接口密钥和 IP 白名单"
	case ReasonUnknownAction:
		return "宝塔面板说“" + e.Message + "”：这个版本的面板可能没有 " + e.Action + " 接口"
	case ReasonConfigRejected:
		return "配置没有通过 Web 服务器的检查，宝塔面板已经恢复了原来的文件：" + e.Message
	case ReasonConfigInvalid:
		return "Nginx 配置检查没有通过：" + e.Message
	case ReasonBadAnswer:
		return fmt.Sprintf("宝塔面板返回了无法识别的内容（HTTP %d）：%s", e.Status, e.Message)
	}
	return "宝塔面板拒绝了请求：" + e.Message
}

// classify recognises the answers of the panel's API check (common.py
// get_sk) and of its action dispatcher, in both editions' words.
func classify(msg string) Reason {
	switch {
	case strings.Contains(msg, "密钥校验失败"), strings.Contains(msg, "Secret key verification failed"):
		return ReasonBadKey
	case strings.Contains(msg, "IP校验失败"), strings.Contains(msg, "IP validation failed"):
		return ReasonIPNotAllowed
	case strings.Contains(msg, "连续20次验证失败"), strings.Contains(msg, "20 consecutive verification failures"):
		return ReasonLockedOut
	case strings.HasPrefix(msg, "指定参数无效"), strings.HasPrefix(msg, "Specific parameters are invalid"):
		return ReasonUnknownAction
	}
	return ReasonRefused
}

// ErrNotFound is matched (errors.Is) by the errors for a site, domain or
// reverse proxy the panel does not have.
var ErrNotFound = errors.New("not found")

type notFound string

func (e notFound) Error() string      { return string(e) }
func (notFound) Is(target error) bool { return target == ErrNotFound }

var (
	breakRe = regexp.MustCompile(`(?i)<br\s*/?>|</p>|</pre>|</div>`)
	tagRe   = regexp.MustCompile(`<[^>]*>`)
)

// plain strips the HTML the panel puts in its messages for its own UI.
func plain(s string) string {
	s = html.UnescapeString(tagRe.ReplaceAllString(breakRe.ReplaceAllString(s, "\n"), ""))
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

// msgText turns the panel's msg into text: usually a string, but a failed
// certificate order sends [its own summary, the whole ACME object]
// (acme_v2.apply_cert), and the summary is what to show.
func msgText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		for _, x := range list {
			if json.Unmarshal(x, &s) == nil {
				return s
			}
		}
	}
	return string(raw)
}

func snippet(raw []byte) string {
	s := plain(string(raw))
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200])
	}
	return s
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s)) //nolint:gosec // the panel's token scheme
	return hex.EncodeToString(sum[:])
}

// sign adds the API credentials: request_time, and request_token =
// md5(request_time + md5(key)), the key being what 面板设置 → API 接口
// shows (the panel keeps its md5). The panel does not check how old
// request_time is.
func (c *Client) sign(form url.Values) {
	ts := strconv.FormatInt(c.now().Unix(), 10)
	form.Set("request_time", ts)
	form.Set("request_token", md5hex(ts+md5hex(c.key)))
}

// secretFields are blanked in traces.
var secretFields = []string{"key", "password", "datapassword", "private_key"}

func traceBody(form url.Values) []byte {
	t := url.Values{}
	for k, v := range form {
		t[k] = v
	}
	for _, k := range secretFields {
		if t.Has(k) {
			t.Set(k, "***")
		}
	}
	return []byte(t.Encode())
}

// maxAnswer bounds an answer: files come back whole up to 3 MB, escaped.
const maxAnswer = 32 << 20

// call posts form to /module?action=action and returns the panel's JSON
// answer, or an *Error when the panel refused.
func (c *Client) call(ctx context.Context, module, action string, form url.Values) (json.RawMessage, error) {
	path := "/" + module + "?action=" + url.QueryEscape(action)
	if form == nil {
		form = url.Values{}
	}
	if c.Trace != nil {
		c.Trace(http.MethodPost, path, traceBody(form))
	}
	signed := url.Values{}
	for k, v := range form {
		signed[k] = v
	}
	c.sign(signed)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Scheme+"://"+c.host+path, strings.NewReader(signed.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// The panel turns away user agents that contain "bot" or "spider".
	req.Header.Set("User-Agent", "Miao-Panel")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接宝塔面板（%s://%s）失败：%w%s", c.Scheme, c.host, err, schemeHint(err, c.Scheme))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return nil, err
	}
	where := module + "/" + action
	switch {
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode >= 300 && resp.StatusCode < 400:
		// The API check sends callers it does not accept to the login
		// page or a fake nginx 404 (public.redirect_to_login), and so does
		// an action that raised (error_500 for a caller without a session).
		return nil, &Error{Status: resp.StatusCode, Action: where, Reason: ReasonNotFound, Message: snippet(raw)}
	case resp.StatusCode == http.StatusUnauthorized:
		// With the panel's BasicAuth on, a request that fails the API
		// check is asked for the password instead.
		return nil, &Error{Status: resp.StatusCode, Action: where, Reason: ReasonBadKey, Message: "HTTP 401"}
	case resp.StatusCode != http.StatusOK:
		return nil, &Error{Status: resp.StatusCode, Action: where, Reason: ReasonBadAnswer, Message: snippet(raw)}
	}
	raw = []byte(strings.TrimSpace(string(raw)))
	if !json.Valid(raw) {
		return nil, &Error{Status: resp.StatusCode, Action: where, Reason: ReasonBadAnswer, Message: snippet(raw)}
	}
	if len(raw) > 0 && raw[0] == '{' {
		var a struct {
			Status    json.RawMessage `json:"status"`
			Msg       json.RawMessage `json:"msg"`
			ConfCheck json.RawMessage `json:"conf_check"`
		}
		// Answers are {"status": false, "msg": ...} on failure. Some
		// successful ones have status false too, without msg (GetSSL of
		// a site without HTTPS).
		if json.Unmarshal(raw, &a) == nil && string(a.Status) == "false" && a.Msg != nil {
			msg := plain(msgText(a.Msg))
			e := &Error{Status: resp.StatusCode, Action: where, Message: msg, Reason: classify(msg)}
			if len(a.ConfCheck) > 0 && string(a.ConfCheck) != "0" {
				// files.SaveFileBody after nginx -t failed and the old file
				// was copied back.
				e.Reason = ReasonConfigRejected
				if _, rest, ok := strings.Cut(msg, "存在错误:"); ok {
					e.Message = strings.TrimSpace(rest)
				}
			}
			return nil, e
		}
	}
	return raw, nil
}

// schemeHint suggests the other scheme when a failure looks like the
// panel speaks it: its HTTPS server drops plain requests without an
// answer, and Go says so when an HTTPS request gets a plain answer.
func schemeHint(err error, scheme string) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ""
	}
	s := err.Error()
	if scheme == "https" {
		if strings.Contains(s, "server gave HTTP response") || strings.Contains(s, "does not look like a TLS handshake") {
			return "；面板没有开启面板 SSL，请改用 http"
		}
		return ""
	}
	for _, sign := range []string{"EOF", "malformed HTTP", "connection reset"} {
		if strings.Contains(s, sign) {
			return "；如果面板开启了面板 SSL（面板设置 → 面板SSL），请改用 https"
		}
	}
	return ""
}

// do calls an action and decodes its answer into out.
func (c *Client) do(ctx context.Context, module, action string, form url.Values, out any) error {
	raw, err := c.call(ctx, module, action, form)
	if err != nil || out == nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &Error{Status: http.StatusOK, Action: module + "/" + action, Reason: ReasonBadAnswer, Message: snippet(raw)}
	}
	return nil
}

// num reads a number the panel sends as a JSON number or as a string (the
// same column comes both ways, depending on how the row was written).
type num int64

func (n *num) UnmarshalJSON(b []byte) error {
	f, err := strconv.ParseFloat(strings.Trim(string(b), `"`), 64)
	if err != nil {
		f = 0 // null, "" and the odd placeholder
	}
	*n = num(f)
	return nil
}

// text reads a value the panel sends as a string or as a number.
type text string

func (t *text) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		err := json.Unmarshal(b, &s)
		*t = text(s)
		return err
	}
	if string(b) == "null" {
		*t = ""
		return nil
	}
	*t = text(b)
	return nil
}

// Info describes the panel and its server.
type Info struct {
	Version    string // the panel's version: 11.x for 宝塔, 7.x or 8.x for aaPanel
	System     string // the operating system, as the panel names it
	CPUs       int
	MemTotalMB int
}

func (c *Client) systemTotal(ctx context.Context) (Info, error) {
	var r struct {
		Version  text `json:"version"`
		System   text `json:"system"`
		CPUs     num  `json:"cpuNum"`
		MemTotal num  `json:"memTotal"`
	}
	err := c.do(ctx, "system", "GetSystemTotal", nil, &r)
	return Info{Version: string(r.Version), System: string(r.System), CPUs: int(r.CPUs), MemTotalMB: int(r.MemTotal)}, err
}

// Probe checks that the panel answers and takes the key, switching to
// HTTPS (or back to HTTP) when the panel's SSL setting says so. It uses
// the home page's system summary, which needs no parameters.
func (c *Client) Probe(ctx context.Context) (Info, error) {
	info, err := c.systemTotal(ctx)
	if err == nil || !wrongScheme(err) {
		return info, err
	}
	first := c.Scheme
	c.Scheme = "https"
	if first == "https" {
		c.Scheme = "http"
	}
	if info, err2 := c.systemTotal(ctx); err2 == nil {
		return info, nil
	}
	c.Scheme = first
	return Info{}, err
}

// wrongScheme tells whether err may come from speaking the other
// protocol: no answer at all, or one that is not the panel's.
func wrongScheme(err error) bool {
	var pe *Error
	return !errors.As(err, &pe) || pe.Reason == ReasonBadAnswer
}
