package store

import (
	"database/sql"
	"time"
)

// Monitoring: a check of each website every minute, a sample of each
// server every few minutes, and the incidents they found. Seven days are
// kept.

const monitorSchema = `
CREATE TABLE IF NOT EXISTS uptime_checks (
	id     INTEGER PRIMARY KEY AUTOINCREMENT,
	target TEXT NOT NULL,              -- the address checked
	at     TEXT NOT NULL,              -- RFC 3339, UTC
	ok     INTEGER NOT NULL,
	status INTEGER NOT NULL DEFAULT 0, -- HTTP status, 0 when there was no answer
	ms     INTEGER NOT NULL DEFAULT 0, -- time to the answer
	error  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS uptime_checks_target ON uptime_checks(target, at);
CREATE TABLE IF NOT EXISTS server_metrics (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	server_id INTEGER NOT NULL,
	at        TEXT NOT NULL,
	ok        INTEGER NOT NULL,
	cpu       REAL NOT NULL DEFAULT 0,   -- percent of all cores
	mem       REAL NOT NULL DEFAULT 0,   -- percent in use (total - available)
	disk      REAL NOT NULL DEFAULT 0,   -- percent of / in use
	load1     REAL NOT NULL DEFAULT 0,
	rx        REAL NOT NULL DEFAULT 0,   -- bytes per second in, over all interfaces but lo
	tx        REAL NOT NULL DEFAULT 0,
	error     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS server_metrics_server ON server_metrics(server_id, at);
CREATE TABLE IF NOT EXISTS incidents (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	kind       TEXT NOT NULL,              -- site, server, disk, mem, cpu
	target     TEXT NOT NULL,              -- address, or the server's id
	name       TEXT NOT NULL DEFAULT '',   -- in words: the site or the server's name
	started_at TEXT NOT NULL,
	ended_at   TEXT NOT NULL DEFAULT '',   -- empty while it lasts
	reason     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS incidents_started ON incidents(started_at);
`

// monitorKept is how long checks and samples are kept.
const monitorKept = 7 * 24 * time.Hour

// UptimeCheck is one look at a website.
type UptimeCheck struct {
	Target string `json:"target"`
	At     string `json:"at"`
	OK     bool   `json:"ok"`
	Status int    `json:"status"`
	MS     int    `json:"ms"`
	Error  string `json:"error,omitempty"`
}

// ServerSample is one look at a server.
type ServerSample struct {
	ServerID int64   `json:"serverId"`
	At       string  `json:"at"`
	OK       bool    `json:"ok"`
	CPU      float64 `json:"cpu"`
	Mem      float64 `json:"mem"`
	Disk     float64 `json:"disk"`
	Load1    float64 `json:"load1"`
	RX       float64 `json:"rx"`
	TX       float64 `json:"tx"`
	Error    string  `json:"error,omitempty"`
}

// Incident is a stretch of time something was wrong.
type Incident struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Target    string `json:"target"`
	Name      string `json:"name"`
	StartedAt string `json:"startedAt"`
	EndedAt   string `json:"endedAt,omitempty"`
	Reason    string `json:"reason"`
}

// AddUptimeCheck records a website check.
func (s *Store) AddUptimeCheck(c UptimeCheck) error {
	if c.At == "" {
		c.At = now()
	}
	_, err := s.db.Exec(`INSERT INTO uptime_checks (target, at, ok, status, ms, error) VALUES (?, ?, ?, ?, ?, ?)`,
		c.Target, c.At, c.OK, c.Status, c.MS, c.Error)
	return err
}

// UptimeChecks returns a website's checks since a time, oldest first.
func (s *Store) UptimeChecks(target, since string) ([]UptimeCheck, error) {
	rows, err := s.db.Query(`SELECT target, at, ok, status, ms, error FROM uptime_checks WHERE target = ? AND at >= ? ORDER BY at`, target, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UptimeCheck{}
	for rows.Next() {
		var c UptimeCheck
		if err := rows.Scan(&c.Target, &c.At, &c.OK, &c.Status, &c.MS, &c.Error); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddServerSample records a server sample.
func (s *Store) AddServerSample(m ServerSample) error {
	if m.At == "" {
		m.At = now()
	}
	_, err := s.db.Exec(`INSERT INTO server_metrics (server_id, at, ok, cpu, mem, disk, load1, rx, tx, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ServerID, m.At, m.OK, m.CPU, m.Mem, m.Disk, m.Load1, m.RX, m.TX, m.Error)
	return err
}

// ServerSamples returns a server's samples since a time, oldest first.
func (s *Store) ServerSamples(serverID int64, since string) ([]ServerSample, error) {
	rows, err := s.db.Query(`SELECT server_id, at, ok, cpu, mem, disk, load1, rx, tx, error FROM server_metrics WHERE server_id = ? AND at >= ? ORDER BY at`, serverID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServerSample{}
	for rows.Next() {
		var m ServerSample
		if err := rows.Scan(&m.ServerID, &m.At, &m.OK, &m.CPU, &m.Mem, &m.Disk, &m.Load1, &m.RX, &m.TX, &m.Error); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// OpenIncident starts an incident, unless one of that kind is still open
// for the target; it returns the open incident's id either way.
func (s *Store) OpenIncident(kind, target, name, reason string) (int64, bool, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM incidents WHERE kind = ? AND target = ? AND ended_at = '' ORDER BY id DESC LIMIT 1`, kind, target).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if err != sql.ErrNoRows {
		return 0, false, err
	}
	res, err := s.db.Exec(`INSERT INTO incidents (kind, target, name, started_at, reason) VALUES (?, ?, ?, ?, ?)`, kind, target, name, now(), reason)
	if err != nil {
		return 0, false, err
	}
	id, err = res.LastInsertId()
	return id, true, err
}

// CloseIncident ends the open incident of that kind for the target, and
// returns it (with ID 0 when none was open).
func (s *Store) CloseIncident(kind, target string) (Incident, error) {
	var in Incident
	err := s.db.QueryRow(`SELECT id, kind, target, name, started_at, reason FROM incidents WHERE kind = ? AND target = ? AND ended_at = '' ORDER BY id DESC LIMIT 1`, kind, target).
		Scan(&in.ID, &in.Kind, &in.Target, &in.Name, &in.StartedAt, &in.Reason)
	if err == sql.ErrNoRows {
		return Incident{}, nil
	}
	if err != nil {
		return Incident{}, err
	}
	in.EndedAt = now()
	_, err = s.db.Exec(`UPDATE incidents SET ended_at = ? WHERE id = ?`, in.EndedAt, in.ID)
	return in, err
}

// EndIncidents ends the open incidents of kind whose target is not in
// keep: what is no longer watched cannot recover.
func (s *Store) EndIncidents(kind string, keep map[string]bool) error {
	rows, err := s.db.Query(`SELECT id, target FROM incidents WHERE kind = ? AND ended_at = ''`, kind)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		var target string
		if err := rows.Scan(&id, &target); err != nil {
			rows.Close()
			return err
		}
		if !keep[target] {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.db.Exec(`UPDATE incidents SET ended_at = ? WHERE id = ?`, now(), id); err != nil {
			return err
		}
	}
	return nil
}

// Incidents returns the incidents that started since a time, or are still
// open, newest first.
func (s *Store) Incidents(since string, limit int) ([]Incident, error) {
	rows, err := s.db.Query(`SELECT id, kind, target, name, started_at, ended_at, reason FROM incidents
		WHERE started_at >= ? OR ended_at = '' ORDER BY started_at DESC, id DESC LIMIT ?`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Incident{}
	for rows.Next() {
		var in Incident
		if err := rows.Scan(&in.ID, &in.Kind, &in.Target, &in.Name, &in.StartedAt, &in.EndedAt, &in.Reason); err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// PruneMonitor drops checks, samples and ended incidents older than a week.
func (s *Store) PruneMonitor() error {
	cut := time.Now().Add(-monitorKept).UTC().Format(time.RFC3339)
	for _, q := range []string{
		`DELETE FROM uptime_checks WHERE at < ?`,
		`DELETE FROM server_metrics WHERE at < ?`,
		`DELETE FROM incidents WHERE ended_at != '' AND ended_at < ?`,
	} {
		if _, err := s.db.Exec(q, cut); err != nil {
			return err
		}
	}
	return nil
}
