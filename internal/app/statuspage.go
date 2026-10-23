package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// StatusPageSettings is the public status page: a read-only snapshot of what
// the monitor knows, reachable without logging in when a token is set.
type StatusPageSettings struct {
	Enabled bool   `json:"enabled"`
	Token   string `json:"token"`
}

const statusPageKey = "statuspage"

// StatusPageSettings returns the saved public status page settings.
func (a *App) StatusPageSettings() StatusPageSettings {
	s := StatusPageSettings{}
	if raw, _ := a.Store.Setting(statusPageKey); raw != "" {
		_ = json.Unmarshal([]byte(raw), &s)
	}
	return s
}

// SaveStatusPageSettings turns the page on or off, making a new token when
// asked, and returns what was saved.
func (a *App) SaveStatusPageSettings(enabled bool, regen bool) (StatusPageSettings, error) {
	s := a.StatusPageSettings()
	s.Enabled = enabled
	if regen || s.Token == "" {
		b := make([]byte, 12)
		if _, err := rand.Read(b); err != nil {
			return s, err
		}
		s.Token = hex.EncodeToString(b)
	}
	data, _ := json.Marshal(s)
	if err := a.Store.SetSetting(statusPageKey, string(data)); err != nil {
		return s, err
	}
	return s, nil
}

// StatusSite is one website row on the public page.
type StatusSite struct {
	Name      string   `json:"name"`
	Up        bool     `json:"up"`
	Status    int      `json:"status"`
	Uptime    *float64 `json:"uptime,omitempty"`
	AvgMS     int      `json:"avgMs"`
	CheckedAt string   `json:"checkedAt,omitempty"`
}

// StatusServer is one server row on the public page. Its name is masked so
// the page can be shared without telling the world which hosts are yours.
type StatusServer struct {
	Name      string `json:"name"`
	Up        bool   `json:"up"`
	CPU       int    `json:"cpu"`
	Mem       int    `json:"mem"`
	Disk      int    `json:"disk"`
	CheckedAt string `json:"checkedAt,omitempty"`
}

// StatusIncident is one recent incident on the public page.
type StatusIncident struct {
	Name    string `json:"name"`
	Target  string `json:"target"`
	Reason  string `json:"reason"`
	Started string `json:"started"`
	Ended   string `json:"ended,omitempty"`
}

// StatusView is what the public page shows.
type StatusView struct {
	UpdatedAt string           `json:"updatedAt"`
	AllUp     bool             `json:"allUp"`
	Down      int              `json:"down"`
	Sites     []StatusSite     `json:"sites"`
	Servers   []StatusServer   `json:"servers"`
	Incidents []StatusIncident `json:"incidents"`
}

// maskHost hides an IP-ish server name.
func maskStatusHost(name string, i int) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "服务器"
	}
	allDigits, dots := true, 0
	for _, r := range name {
		if r == '.' {
			dots++
			continue
		}
		if r < '0' || r > '9' {
			allDigits = false
			break
		}
	}
	if allDigits && dots == 3 {
		return "服务器 " + string(rune('A'+i%26))
	}
	return name
}

// PublicStatus builds the snapshot for token, or says the page is off.
func (a *App) PublicStatus(token string) (StatusView, bool) {
	s := a.StatusPageSettings()
	if !s.Enabled || s.Token == "" || s.Token != token {
		return StatusView{}, false
	}
	page, err := a.MonitorPage(context.Background())
	if err != nil {
		return StatusView{}, false
	}
	v := StatusView{UpdatedAt: time.Now().Format(time.RFC3339), Sites: []StatusSite{}, Servers: []StatusServer{}, Incidents: []StatusIncident{}}
	down := 0
	for _, site := range page.Sites {
		up := site.Up != nil && *site.Up
		if !up {
			down++
		}
		v.Sites = append(v.Sites, StatusSite{Name: site.Name, Up: up, Status: site.Status, Uptime: site.Uptime, AvgMS: site.AvgMS, CheckedAt: site.CheckedAt})
	}
	for i, sv := range page.Servers {
		up := !sv.Down
		if !up {
			down++
		}
		row := StatusServer{Name: maskStatusHost(sv.Name, i), Up: up, CheckedAt: sv.Since}
		if sv.Latest != nil {
			row.CPU = int(sv.Latest.CPU + 0.5)
			row.Mem = int(sv.Latest.Mem + 0.5)
			row.Disk = int(sv.Latest.Disk + 0.5)
			row.CheckedAt = sv.Latest.At
		}
		v.Servers = append(v.Servers, row)
	}
	for _, in := range page.Incidents {
		v.Incidents = append(v.Incidents, StatusIncident{Name: in.Name, Target: in.Target, Reason: in.Reason, Started: in.StartedAt, Ended: in.EndedAt})
	}
	v.Down = down
	v.AllUp = down == 0
	return v, true
}
