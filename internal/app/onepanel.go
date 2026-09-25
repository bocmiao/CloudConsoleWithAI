package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/onepanel"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx"
)

// OnePanelSettings says how to reach a server's 1Panel API. The API key is
// kept in the secret store.
type OnePanelSettings struct {
	Port   int    `json:"port"`
	Host   string `json:"host"`   // bound domain, if the panel has one
	Scheme string `json:"scheme"` // detected: http or https
	HasKey bool   `json:"hasKey"`
}

func onePanelKey(id int64) string { return "onepanel/" + strconv.FormatInt(id, 10) }

// OnePanel returns a server's 1Panel API settings, suggesting the port
// found during discovery when none is saved.
func (a *App) OnePanel(id int64) (OnePanelSettings, error) {
	var s OnePanelSettings
	raw, err := a.Store.Setting(onePanelKey(id))
	if err != nil {
		return s, err
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return s, err
		}
	}
	if s.Port == 0 {
		if v, err := a.Profile(id); err == nil && v.Profile != nil {
			s.Port, _ = strconv.Atoi(v.Profile.Panel.Port)
		}
	}
	_, err = a.Secrets.Get(secretKey(id, "1panel_key"))
	s.HasKey = err == nil
	return s, nil
}

// SaveOnePanel stores the settings, and the API key if one is given.
func (a *App) SaveOnePanel(id int64, s OnePanelSettings, apiKey string) (OnePanelSettings, error) {
	if _, err := a.Store.GetServer(id); err != nil {
		return s, userErr("找不到这台服务器（编号 %d）", id)
	}
	if s.Port < 1 || s.Port > 65535 {
		return s, userErr("请填写 1Panel 的端口（1 到 65535）")
	}
	s.Host = strings.TrimSpace(s.Host)
	if s.Host != "" && !hostRe.MatchString(s.Host) {
		return s, userErr("绑定域名的格式不对")
	}
	s.HasKey = false
	data, _ := json.Marshal(s)
	if err := a.Store.SetSetting(onePanelKey(id), string(data)); err != nil {
		return s, err
	}
	if k := strings.TrimSpace(apiKey); k != "" {
		if err := a.Secrets.Set(secretKey(id, "1panel_key"), k); err != nil {
			return s, err
		}
	}
	_ = a.Store.Audit("user", "onepanel.settings", strconv.FormatInt(id, 10), fmt.Sprintf("端口 %d", s.Port))
	return a.OnePanel(id)
}

// onePanelClient builds an API client over an open connection, or returns
// nil when the server has no 1Panel API configured. Over SSH requests go
// through a tunnel; otherwise curl runs them on the server.
func (a *App) onePanelClient(id int64, c sshx.Conn) (*onepanel.Client, error) {
	s, err := a.OnePanel(id)
	if err != nil || !s.HasKey || s.Port == 0 {
		return nil, err
	}
	key, err := a.Secrets.Get(secretKey(id, "1panel_key"))
	if err != nil {
		return nil, err
	}
	if sc, ok := c.(*sshx.Client); ok {
		return onepanel.New(sc.Dial, s.Port, key, s.Host, s.Scheme), nil
	}
	shell := func(ctx context.Context, cmd, stdin string, maxOut int) (string, string, int, error) {
		res, err := c.Run(ctx, cmd, stdin, maxOut)
		return res.Stdout, res.Stderr, res.ExitCode, err
	}
	return onepanel.NewOverShell(shell, s.Port, key, s.Host, s.Scheme), nil
}

// TestOnePanel checks the 1Panel API from the server itself and remembers
// whether the panel speaks HTTP or HTTPS.
func (a *App) TestOnePanel(ctx context.Context, id int64) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	_, c, err := a.connect(ctx, id)
	if err != nil {
		return "", err
	}
	defer c.Close()
	client, err := a.onePanelClient(id, c)
	if err != nil {
		return "", err
	}
	if client == nil {
		return "", userErr("请先填写 1Panel 的端口和 API 密钥")
	}
	info, err := client.Probe(ctx)
	if err != nil {
		return "", userErr("连接 1Panel 失败：%v", err)
	}
	s, _ := a.OnePanel(id)
	if s.Scheme != client.Scheme {
		s.Scheme = client.Scheme
		data, _ := json.Marshal(s)
		_ = a.Store.SetSetting(onePanelKey(id), string(data))
	}
	return info, nil
}
