package app

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/btpanel"
	"github.com/bocmiao/CloudConsoleWithAI/internal/onepanel"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

// 宝塔面板's API, reached like 1Panel's: from the server itself, through
// the SSH connection (or curl run by the automation agent), so the
// panel's port need not be open and its IP whitelist is just 127.0.0.1.

// BTSettings are a server's 宝塔 API settings.
type BTSettings struct {
	Port   int    `json:"port"`
	Scheme string `json:"scheme"` // detected: http or https
	HasKey bool   `json:"hasKey"`
}

func btKey(id int64) string { return "btpanel/" + strconv.FormatInt(id, 10) }

// BT returns a server's 宝塔 API settings, suggesting the port found
// during discovery when none is saved.
func (a *App) BT(id int64) (BTSettings, error) {
	var s BTSettings
	raw, err := a.Store.Setting(btKey(id))
	if err != nil {
		return s, err
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return s, err
		}
	}
	if s.Port == 0 {
		if v, err := a.Profile(id); err == nil && v.Profile != nil && v.Profile.Adapter == "bt" {
			s.Port, _ = strconv.Atoi(v.Profile.Panel.Port)
		}
	}
	_, err = a.Secrets.Get(secretKey(id, "bt_key"))
	s.HasKey = err == nil
	return s, nil
}

// SaveBT stores a server's 宝塔 API port and key (an empty key keeps the
// saved one).
func (a *App) SaveBT(id int64, s BTSettings, apiKey string) (BTSettings, error) {
	defer a.relistTargets()
	if _, err := a.Store.GetServer(id); err != nil {
		return s, userErr("找不到这台服务器（编号 %d）", id)
	}
	if s.Port < 1 || s.Port > 65535 {
		return s, userErr("请填写宝塔面板的端口（1 到 65535）")
	}
	if s.Scheme != "https" {
		s.Scheme = "http"
	}
	s.HasKey = false
	data, _ := json.Marshal(s)
	if err := a.Store.SetSetting(btKey(id), string(data)); err != nil {
		return s, err
	}
	if k := strings.TrimSpace(apiKey); k != "" {
		if err := a.Secrets.Set(secretKey(id, "bt_key"), k); err != nil {
			return s, err
		}
	}
	_ = a.Store.Audit("user", "btpanel.settings", strconv.FormatInt(id, 10), fmt.Sprintf("端口 %d", s.Port))
	return a.BT(id)
}

// btRedact blanks private keys in the panel's answers on the server.
const btRedact = ` | sed -E 's/"(key|private_key)": ?"[^"]*"/"\1":""/g'`

// btClient builds a 宝塔 API client over an open connection, or returns
// nil when the server has no 宝塔 API configured.
func (a *App) btClient(id int64, c sshx.Conn) (*btpanel.Client, error) {
	s, err := a.BT(id)
	if err != nil || !s.HasKey || s.Port == 0 {
		return nil, err
	}
	key, err := a.Secrets.Get(secretKey(id, "bt_key"))
	if err != nil {
		return nil, err
	}
	var rt http.RoundTripper
	if sc, ok := c.(*sshx.Client); ok {
		target := net.JoinHostPort("127.0.0.1", strconv.Itoa(s.Port))
		rt = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return sc.Dial("tcp", target) },
			// Inside the authenticated SSH tunnel to the server's loopback;
			// the panel's certificate is its own self-signed one.
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			ResponseHeaderTimeout: 5 * time.Minute,
		}
	} else {
		shell := func(ctx context.Context, cmd, stdin string, maxOut int) (string, string, int, error) {
			res, err := c.Run(ctx, cmd, stdin, maxOut)
			return res.Stdout, res.Stderr, res.ExitCode, err
		}
		rt = onepanel.ShellTransport(shell, s.Port, "宝塔", btRedact)
	}
	b := btpanel.New(rt, s.Port, key, s.Scheme)
	_, tunnel := rt.(*http.Transport)
	b.KeysHidden = !tunnel // btRedact blanks them in curl's output
	return b, nil
}

// isBT says whether a server's panel is 宝塔.
// noPanel explains why a panel tool has nothing to show on a server,
// or is "" when it may: plain Linux has no panel, and 宝塔 only when bt
// says the tool reads it too.
func (a *App) noPanel(id int64, bt bool) string {
	sv, err := a.Store.GetServer(id)
	switch {
	case err != nil:
		return ""
	case sv.Adapter == "linux":
		return sv.Name + " 是没装面板的纯 Linux 服务器，没有面板里的网站、数据库和备份。可以用 run_check（web、db 检查项）看网站和数据库，" +
			"用 server_files 读 Nginx 配置文件，修改用 free_command。"
	case sv.Adapter == "bt" && !bt:
		return sv.Name + " 是宝塔服务器，这一项只支持 1Panel。宝塔的网站用 panel_websites、panel_website、panel_backups 查看，数据库请在宝塔面板里看，" +
			"或者用 run_check 的 db 检查项。"
	}
	return ""
}

func (a *App) isBT(id int64) bool {
	sv, err := a.Store.GetServer(id)
	return err == nil && sv.Adapter == "bt"
}

// errNoBT says a server's 宝塔 API is not set up.
var errNoBT = userErr("这台服务器还没有配置宝塔接口，请在服务器的「连接设置」里填写宝塔面板的端口和接口密钥")

// withBT runs op with the server's 宝塔 client over the kept connection,
// reconnecting once when it turns out to be gone.
func (a *App) withBT(ctx context.Context, id int64, op func(sv store.Server, c sshx.Conn, b *btpanel.Client) error) error {
	for try := 0; ; try++ {
		pc, sv, err := a.panelConnFor(ctx, id)
		if err != nil {
			return err
		}
		b, err := a.btClient(id, pc.conn)
		if err == nil && b == nil {
			err = errNoBT
		}
		if err == nil {
			err = op(sv, pc.conn, b)
		}
		lost := err != nil && lostConn(err)
		a.releasePanel(id, pc, lost)
		if lost && try == 0 {
			continue
		}
		return err
	}
}

// TestBT checks the 宝塔 API from the server itself and remembers whether
// the panel speaks HTTP or HTTPS.
func (a *App) TestBT(ctx context.Context, id int64) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var info btpanel.Info
	var scheme string
	err := a.withBT(ctx, id, func(_ store.Server, _ sshx.Conn, b *btpanel.Client) error {
		var err error
		info, err = b.Probe(ctx)
		scheme = b.Scheme
		return err
	})
	if err != nil {
		var be *btpanel.Error
		if errors.As(err, &be) {
			return "", userErr("%v", be)
		}
		return "", err
	}
	if s, err := a.BT(id); err == nil && s.Scheme != scheme {
		s.Scheme = scheme
		data, _ := json.Marshal(BTSettings{Port: s.Port, Scheme: scheme})
		_ = a.Store.SetSetting(btKey(id), string(data))
	}
	msg := "宝塔 " + info.Version
	if info.System != "" {
		msg += " · " + info.System
	}
	return msg, nil
}
