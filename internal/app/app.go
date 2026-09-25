// Package app is Miao Panel's application layer: it ties the store,
// secrets, SSH and AI packages together behind operations the HTTP API
// exposes.
package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/profile"
	"github.com/bocmiao/CloudConsoleWithAI/internal/secrets"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/scripts"
)

// App holds the long-lived state of one running Miao Panel.
type App struct {
	Store   *store.Store
	Secrets secrets.Store
	// Dial opens SSH connections; tests replace it.
	Dial func(ctx context.Context, t sshx.Target) (*sshx.Client, error)

	mu    sync.Mutex
	convs map[string]*conversation
}

// New creates an App.
func New(st *store.Store, sec secrets.Store) *App {
	return &App{Store: st, Secrets: sec, Dial: sshx.Dial, convs: map[string]*conversation{}}
}

// UserError is an error whose message is meant for the user as-is.
type UserError struct{ Msg string }

func (e *UserError) Error() string { return e.Msg }

func userErr(format string, args ...any) error { return &UserError{Msg: fmt.Sprintf(format, args...)} }

// friendlySSHError explains common connection failures in plain words.
func friendlySSHError(err error) error {
	var ue *UserError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ue):
		return err
	case errors.Is(err, sshx.ErrHostKeyChanged):
		return userErr("服务器的身份指纹和第一次连接时不一样。可能是服务器重装过系统，也可能有人在冒充这台服务器。为了安全已停止连接。如果你确认是重装导致的，请删除这台服务器后重新添加。")
	case strings.Contains(err.Error(), "unable to authenticate"):
		return userErr("登录失败：用户名、密码或密钥不正确。")
	case strings.Contains(err.Error(), "parse key file"), strings.Contains(err.Error(), "read key file"):
		return userErr("密钥文件读取失败：%v", err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) || strings.Contains(err.Error(), "connection refused") ||
		strings.Contains(err.Error(), "no such host") {
		return userErr("连不上服务器：请检查 IP 和端口是否正确，以及云服务器的防火墙（安全组）是否放行了 SSH 端口。（%v）", err)
	}
	return err
}

// AddServerRequest is what the user fills in to add a server.
type AddServerRequest struct {
	Name          string `json:"name"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Username      string `json:"username"`
	AuthKind      string `json:"authKind"` // password | key
	Password      string `json:"password"`
	KeyPath       string `json:"keyPath"`
	KeyPassphrase string `json:"keyPassphrase"`
}

var hostRe = regexp.MustCompile(`^[A-Za-z0-9.:\-\[\]]+$`)

func secretKey(id int64, what string) string {
	return "server/" + strconv.FormatInt(id, 10) + "/" + what
}

// AddServer validates and saves a server. Credentials go to the secret store.
func (a *App) AddServer(req AddServerRequest) (store.Server, error) {
	req.Host = strings.TrimSpace(req.Host)
	req.Name = strings.TrimSpace(req.Name)
	req.Username = strings.TrimSpace(req.Username)
	if req.Host == "" || !hostRe.MatchString(req.Host) {
		return store.Server{}, userErr("请填写正确的服务器 IP 或域名")
	}
	if req.Port == 0 {
		req.Port = 22
	}
	if req.Port < 1 || req.Port > 65535 {
		return store.Server{}, userErr("端口必须在 1 到 65535 之间")
	}
	if req.Username == "" {
		req.Username = "root"
	}
	if req.Name == "" {
		req.Name = req.Host
	}
	switch req.AuthKind {
	case "password":
		if req.Password == "" {
			return store.Server{}, userErr("请填写登录密码")
		}
	case "key":
		if strings.TrimSpace(req.KeyPath) == "" {
			return store.Server{}, userErr("请填写密钥文件的位置")
		}
	default:
		return store.Server{}, userErr("请选择登录方式")
	}
	sv, err := a.Store.AddServer(store.Server{
		Name: req.Name, Host: req.Host, Port: req.Port, Username: req.Username,
		AuthKind: req.AuthKind, KeyPath: strings.TrimSpace(req.KeyPath),
	})
	if err != nil {
		return sv, err
	}
	var secErr error
	if req.AuthKind == "password" {
		secErr = a.Secrets.Set(secretKey(sv.ID, "password"), req.Password)
	} else if req.KeyPassphrase != "" {
		secErr = a.Secrets.Set(secretKey(sv.ID, "passphrase"), req.KeyPassphrase)
	}
	if secErr != nil {
		_ = a.Store.DeleteServer(sv.ID)
		return store.Server{}, fmt.Errorf("save credentials: %w", secErr)
	}
	_ = a.Store.Audit("user", "server.add", sv.Name, fmt.Sprintf("%s@%s:%d", sv.Username, sv.Host, sv.Port))
	return sv, nil
}

// DeleteServer removes a server and its stored credentials.
func (a *App) DeleteServer(id int64) error {
	sv, err := a.Store.GetServer(id)
	if err != nil {
		return err
	}
	_ = a.Secrets.Delete(secretKey(id, "password"))
	_ = a.Secrets.Delete(secretKey(id, "passphrase"))
	if err := a.Store.DeleteServer(id); err != nil {
		return err
	}
	_ = a.Store.Audit("user", "server.delete", sv.Name, sv.Host)
	return nil
}

func (a *App) target(sv store.Server) (sshx.Target, error) {
	t := sshx.Target{Host: sv.Host, Port: sv.Port, User: sv.Username, KnownHostKey: sv.HostKey}
	if sv.AuthKind == "key" {
		t.KeyPath = sv.KeyPath
		if p, err := a.Secrets.Get(secretKey(sv.ID, "passphrase")); err == nil {
			t.KeyPassphrase = p
		}
		return t, nil
	}
	p, err := a.Secrets.Get(secretKey(sv.ID, "password"))
	if err != nil {
		return t, userErr("找不到这台服务器保存的密码，请删除后重新添加")
	}
	t.Password = p
	return t, nil
}

// connect opens SSH to a server, pinning its host key on first use.
func (a *App) connect(ctx context.Context, id int64) (store.Server, *sshx.Client, error) {
	sv, err := a.Store.GetServer(id)
	if err != nil {
		return sv, nil, userErr("找不到这台服务器（编号 %d）", id)
	}
	t, err := a.target(sv)
	if err != nil {
		return sv, nil, err
	}
	c, err := a.Dial(ctx, t)
	if err != nil {
		return sv, nil, friendlySSHError(err)
	}
	if sv.HostKey == "" && c.HostKey != "" {
		if err := a.Store.SetHostKey(sv.ID, c.HostKey); err != nil {
			c.Close()
			return sv, nil, err
		}
		sv.HostKey = c.HostKey
		_ = a.Store.Audit("system", "server.hostkey.recorded", sv.Name, c.HostKey)
	}
	return sv, c, nil
}

// TestResult is shown after "测试连接".
type TestResult struct {
	HostKey string `json:"hostKey"`
	Output  string `json:"output"`
}

// TestConnection logs in and runs a harmless command.
func (a *App) TestConnection(ctx context.Context, id int64) (TestResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	sv, c, err := a.connect(ctx, id)
	if err != nil {
		return TestResult{}, err
	}
	defer c.Close()
	res, err := c.Run(ctx, "uname -srm; id -un", "", 4096)
	if err != nil {
		return TestResult{}, friendlySSHError(err)
	}
	_ = a.Store.Audit("user", "server.test", sv.Name, "ok")
	return TestResult{HostKey: sv.HostKey, Output: strings.TrimSpace(res.Stdout)}, nil
}

const maxDiscoverOutput = 256 << 10

// Discover runs discover.sh. With no sections it runs everything and saves
// the result as the server's profile; with sections it only returns them.
func (a *App) Discover(ctx context.Context, id int64, sections []string) (string, *profile.Profile, error) {
	for _, s := range sections {
		if !scripts.ValidSection(s) {
			return "", nil, userErr("未知的检查项：%s", s)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	sv, c, err := a.connect(ctx, id)
	if err != nil {
		return "", nil, err
	}
	defer c.Close()
	res, err := c.RunScript(ctx, sv.Username, scripts.Discover, sections, maxDiscoverOutput)
	if err != nil {
		return "", nil, friendlySSHError(err)
	}
	if strings.TrimSpace(res.Stdout) == "" {
		return "", nil, userErr("识别脚本没有输出（退出码 %d）：%s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	raw := res.Stdout
	if res.Truncated {
		raw += "\n（输出过长，已截断）\n"
	}
	prof := profile.Parse(raw)
	if len(sections) == 0 {
		if err := a.Store.SaveProfile(sv.ID, raw, prof.Adapter); err != nil {
			return raw, prof, err
		}
		_ = a.Store.Audit("user", "server.discover", sv.Name,
			fmt.Sprintf("适配器 %s，发现 %d 项", prof.Adapter, len(prof.Findings)))
	}
	return raw, prof, nil
}

// ProfileView is a server with its latest parsed profile.
type ProfileView struct {
	Server      store.Server     `json:"server"`
	Profile     *profile.Profile `json:"profile"`
	CollectedAt string           `json:"collectedAt"`
	Raw         string           `json:"raw"`
}

// Profile returns the stored profile; Profile is nil if discovery never ran.
func (a *App) Profile(id int64) (ProfileView, error) {
	sv, err := a.Store.GetServer(id)
	if err != nil {
		return ProfileView{}, userErr("找不到这台服务器（编号 %d）", id)
	}
	v := ProfileView{Server: sv}
	raw, at, err := a.Store.GetProfile(id)
	if errors.Is(err, store.ErrNotFound) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.Profile, v.CollectedAt, v.Raw = profile.Parse(raw), at, raw
	return v, nil
}
