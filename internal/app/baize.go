package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/secrets"
)

const baizeSettingKey = "baize/site"
const baizeTokenKey = "baize/site/token"

var baizeSiteID = regexp.MustCompile(`^[a-zA-Z0-9-]{8,64}$`)

// BaizeSettings keeps a single Baize site connection. The token is held in
// the secret store and is never returned through the settings API.
type BaizeSettings struct {
	BaseURL  string `json:"baseUrl"`
	SiteID   string `json:"siteId"`
	Name     string `json:"name"`
	HasToken bool   `json:"hasToken"`
}

type BaizePage struct {
	Path string `json:"path"`
	PV   int64  `json:"pv"`
	UV   *int64 `json:"uv"`
}

type BaizeSource struct {
	Channel string `json:"channel"`
	PV      int64  `json:"pv"`
}

// BaizeReport deliberately remains separate from log-based visits: the
// collection methods and visitor identities have different meanings.
type BaizeReport struct {
	Name            string        `json:"name"`
	SiteID          string        `json:"siteId"`
	BaseURL         string        `json:"baseUrl"`
	From            string        `json:"from"`
	To              string        `json:"to"`
	PV              *int64        `json:"pv"`
	UV              *int64        `json:"uv"`
	Sessions        *int64        `json:"sessions"`
	BounceRate      *float64      `json:"bounceRate"`
	AverageDuration *float64      `json:"averageDuration"`
	Coverage        *float64      `json:"coverage"`
	RangeStatus     string        `json:"rangeStatus"`
	UVStatus        string        `json:"uvStatus"`
	Watermark       *string       `json:"watermark"`
	Pages           []BaizePage   `json:"pages"`
	Sources         []BaizeSource `json:"sources"`
}

func (a *App) BaizeSettings() (BaizeSettings, error) {
	var s BaizeSettings
	raw, err := a.Store.Setting(baizeSettingKey)
	if err != nil || raw == "" {
		return s, err
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return BaizeSettings{}, err
	}
	_, err = a.Secrets.Get(baizeTokenKey)
	if err != nil && !errors.Is(err, secrets.ErrNotFound) {
		return BaizeSettings{}, err
	}
	s.HasToken = err == nil
	return s, nil
}

func validBaizeURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || strings.ContainsAny(raw, "?#") {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

func (a *App) SaveBaizeSettings(s BaizeSettings, token string) (BaizeSettings, error) {
	s.BaseURL = strings.TrimRight(strings.TrimSpace(s.BaseURL), "/")
	s.SiteID = strings.TrimSpace(s.SiteID)
	s.Name = strings.TrimSpace(s.Name)
	if !validBaizeURL(s.BaseURL) || !baizeSiteID.MatchString(s.SiteID) || len(s.Name) > 128 {
		return BaizeSettings{}, userErr("请填写 Baize 的 HTTPS 地址和有效站点 ID；本机地址可用 HTTP")
	}
	if s.Name == "" {
		s.Name = s.SiteID
	}
	token = strings.TrimSpace(token)
	if token == "" {
		previous, err := a.BaizeSettings()
		if err != nil {
			return BaizeSettings{}, err
		}
		if previous.BaseURL != s.BaseURL || previous.SiteID != s.SiteID {
			return BaizeSettings{}, userErr("更换 Baize 地址或站点时，请重新填写该站点的只读 API Token")
		}
		if _, err := a.Secrets.Get(baizeTokenKey); err != nil {
			return BaizeSettings{}, userErr("请填写 Baize 站点的只读 API Token")
		}
	} else {
		if len(token) > 4096 {
			return BaizeSettings{}, userErr("Baize API Token 太长")
		}
		if err := a.Secrets.Set(baizeTokenKey, token); err != nil {
			return BaizeSettings{}, err
		}
	}
	s.HasToken = false
	raw, err := json.Marshal(s)
	if err != nil {
		return BaizeSettings{}, err
	}
	if err := a.Store.SetSetting(baizeSettingKey, string(raw)); err != nil {
		return BaizeSettings{}, err
	}
	_ = a.Store.Audit("user", "settings.baize", s.SiteID, "修改 Baize 站点连接")
	return a.BaizeSettings()
}

func (a *App) DeleteBaizeSettings() error {
	if err := a.Secrets.Delete(baizeTokenKey); err != nil {
		return err
	}
	if err := a.Store.DeleteSetting(baizeSettingKey); err != nil {
		return err
	}
	return a.Store.Audit("user", "settings.baize", "", "移除 Baize 站点连接")
}

func (a *App) BaizeStats(ctx context.Context, days int) (BaizeReport, error) {
	if days != 1 && days != 7 && days != 30 {
		return BaizeReport{}, userErr("时间范围只能是 1、7 或 30 天")
	}
	s, err := a.BaizeSettings()
	if err != nil {
		return BaizeReport{}, err
	}
	if s.SiteID == "" || !s.HasToken {
		return BaizeReport{}, userErr("请先连接 Baize 站点")
	}
	if !validBaizeURL(s.BaseURL) || !baizeSiteID.MatchString(s.SiteID) {
		return BaizeReport{}, userErr("保存的 Baize 连接地址或站点 ID 无效，请重新设置")
	}
	token, err := a.Secrets.Get(baizeTokenKey)
	if err != nil {
		return BaizeReport{}, userErr("Baize API Token 已丢失，请重新填写")
	}
	to := time.Now().UTC()
	from := to.Add(-time.Duration(days) * 24 * time.Hour)
	u, err := url.Parse(s.BaseURL + "/api/v1/sites/" + url.PathEscape(s.SiteID) + "/stats")
	if err != nil {
		return BaizeReport{}, err
	}
	q := u.Query()
	q.Set("from", from.Format(time.RFC3339))
	q.Set("to", to.Format(time.RFC3339))
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return BaizeReport{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return BaizeReport{}, userErr("连接 Baize 失败：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return BaizeReport{}, userErr("Baize 拒绝访问，请检查只读 Token 和站点权限（HTTP %d）", resp.StatusCode)
		}
		return BaizeReport{}, userErr("Baize 统计接口返回 HTTP %d", resp.StatusCode)
	}
	var raw struct {
		Totals struct {
			PV              *int64   `json:"pv"`
			UV              *int64   `json:"uv"`
			Sessions        *int64   `json:"sessions"`
			BounceRate      *float64 `json:"bounceRate"`
			AverageDuration *float64 `json:"averageDuration"`
		} `json:"totals"`
		Coverage    *float64      `json:"coverage"`
		RangeStatus string        `json:"rangeStatus"`
		UVStatus    string        `json:"uvStatus"`
		Watermark   *string       `json:"watermark"`
		Pages       []BaizePage   `json:"pages"`
		Sources     []BaizeSource `json:"sources"`
	}
	limited := io.LimitReader(resp.Body, 16<<20)
	if err := json.NewDecoder(limited).Decode(&raw); err != nil {
		return BaizeReport{}, userErr("Baize 返回的数据无法读取：%v", err)
	}
	if raw.Coverage == nil || *raw.Coverage < 0 || *raw.Coverage > 1 || raw.RangeStatus == "" {
		return BaizeReport{}, userErr("Baize 返回的数据缺少有效的统计覆盖信息")
	}
	sort.Slice(raw.Pages, func(i, j int) bool { return raw.Pages[i].PV > raw.Pages[j].PV })
	sort.Slice(raw.Sources, func(i, j int) bool { return raw.Sources[i].PV > raw.Sources[j].PV })
	if len(raw.Pages) > 10 {
		raw.Pages = raw.Pages[:10]
	}
	if len(raw.Sources) > 10 {
		raw.Sources = raw.Sources[:10]
	}
	if raw.Pages == nil {
		raw.Pages = []BaizePage{}
	}
	if raw.Sources == nil {
		raw.Sources = []BaizeSource{}
	}
	return BaizeReport{Name: s.Name, SiteID: s.SiteID, BaseURL: s.BaseURL,
		From: from.Format(time.RFC3339), To: to.Format(time.RFC3339), PV: raw.Totals.PV, UV: raw.Totals.UV,
		Sessions: raw.Totals.Sessions, BounceRate: raw.Totals.BounceRate, AverageDuration: raw.Totals.AverageDuration,
		Coverage: raw.Coverage, RangeStatus: raw.RangeStatus, UVStatus: raw.UVStatus, Watermark: raw.Watermark,
		Pages: raw.Pages, Sources: raw.Sources}, nil
}
