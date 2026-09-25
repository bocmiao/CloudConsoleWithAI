// Package store keeps Miao Panel's local state in a SQLite file. Secrets
// never go here; see package secrets.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, no cgo needed for Windows builds
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS servers (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT NOT NULL,
	host       TEXT NOT NULL,
	port       INTEGER NOT NULL DEFAULT 22,
	username   TEXT NOT NULL,
	auth_kind  TEXT NOT NULL,              -- password | key
	key_path   TEXT NOT NULL DEFAULT '',
	host_key   TEXT NOT NULL DEFAULT '',   -- SHA256 fingerprint recorded on first connect
	adapter    TEXT NOT NULL DEFAULT '',   -- 1panel | bt | linux, from discovery
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS host_profiles (
	server_id    INTEGER PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
	raw          TEXT NOT NULL,            -- redacted discover.sh output
	collected_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS plans (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	server_id  INTEGER NOT NULL DEFAULT 0,
	title      TEXT NOT NULL,
	reason     TEXT NOT NULL,
	steps      TEXT NOT NULL,              -- JSON
	status     TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS audit_logs (
	id     INTEGER PRIMARY KEY AUTOINCREMENT,
	at     TEXT NOT NULL,
	actor  TEXT NOT NULL,
	action TEXT NOT NULL,
	target TEXT NOT NULL DEFAULT '',
	detail TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS ai_usage (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	at            TEXT NOT NULL,
	model         TEXT NOT NULL,
	input_tokens  INTEGER NOT NULL,
	cached_tokens INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL,
	cost          REAL NOT NULL,
	currency      TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

// Open opens (and migrates) the database in dir.
func Open(dir string) (*Store, error) {
	return open(fileURI(filepath.Join(dir, "miaopanel.db")) +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
}

// fileURI turns an absolute path into an SQLite URI: file:///home/u/x.db
// on Unix, file:///C:/Users/u/x.db on Windows. Characters that have a
// meaning in URIs are escaped; others (spaces, Chinese user names) are
// passed through as UTF-8, which SQLite accepts.
func fileURI(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(p)
	return "file://" + p
}

// OpenMemory opens a throwaway in-memory database, for tests.
func OpenMemory() (*Store, error) {
	return open("file::memory:?_pragma=foreign_keys(1)")
}

func open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite handles one writer at a time; a single connection also keeps
	// in-memory databases alive across calls.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := addColumn(db, "plans", "result", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// addColumn adds a column to databases created by older versions.
func addColumn(db *sql.DB, table, column, decl string) error {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	_, err = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + decl)
	return err
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// Server is a machine the user manages.
type Server struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Username  string `json:"username"`
	AuthKind  string `json:"authKind"`
	KeyPath   string `json:"keyPath"`
	HostKey   string `json:"hostKey"`
	Adapter   string `json:"adapter"`
	CreatedAt string `json:"createdAt"`
	// ProfiledAt is when discovery last ran; empty if never.
	ProfiledAt string `json:"profiledAt"`
}

// AddServer inserts a server and returns it with its new ID.
func (s *Store) AddServer(sv Server) (Server, error) {
	sv.CreatedAt = now()
	res, err := s.db.Exec(`INSERT INTO servers (name, host, port, username, auth_kind, key_path, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, sv.Name, sv.Host, sv.Port, sv.Username, sv.AuthKind, sv.KeyPath, sv.CreatedAt)
	if err != nil {
		return Server{}, err
	}
	sv.ID, err = res.LastInsertId()
	return sv, err
}

const serverCols = `s.id, s.name, s.host, s.port, s.username, s.auth_kind, s.key_path, s.host_key, s.adapter, s.created_at,
	COALESCE(p.collected_at, '')`

func scanServer(row interface{ Scan(...any) error }) (Server, error) {
	var sv Server
	err := row.Scan(&sv.ID, &sv.Name, &sv.Host, &sv.Port, &sv.Username, &sv.AuthKind, &sv.KeyPath,
		&sv.HostKey, &sv.Adapter, &sv.CreatedAt, &sv.ProfiledAt)
	return sv, err
}

// ListServers returns all servers ordered by creation.
func (s *Store) ListServers() ([]Server, error) {
	rows, err := s.db.Query(`SELECT ` + serverCols + ` FROM servers s
		LEFT JOIN host_profiles p ON p.server_id = s.id ORDER BY s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Server{}
	for rows.Next() {
		sv, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sv)
	}
	return out, rows.Err()
}

// GetServer returns one server.
func (s *Store) GetServer(id int64) (Server, error) {
	sv, err := scanServer(s.db.QueryRow(`SELECT `+serverCols+` FROM servers s
		LEFT JOIN host_profiles p ON p.server_id = s.id WHERE s.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Server{}, ErrNotFound
	}
	return sv, err
}

// DeleteServer removes a server and its profile.
func (s *Store) DeleteServer(id int64) error {
	_, err := s.db.Exec(`DELETE FROM servers WHERE id = ?`, id)
	return err
}

// SetHostKey records the server's SSH host key fingerprint.
func (s *Store) SetHostKey(id int64, fingerprint string) error {
	_, err := s.db.Exec(`UPDATE servers SET host_key = ? WHERE id = ?`, fingerprint, id)
	return err
}

// SaveProfile stores the latest discovery output and the detected adapter.
func (s *Store) SaveProfile(id int64, raw, adapter string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO host_profiles (server_id, raw, collected_at) VALUES (?, ?, ?)
		ON CONFLICT(server_id) DO UPDATE SET raw = excluded.raw, collected_at = excluded.collected_at`,
		id, raw, now()); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE servers SET adapter = ? WHERE id = ?`, adapter, id); err != nil {
		return err
	}
	return tx.Commit()
}

// GetProfile returns the latest raw discovery output and when it was taken.
func (s *Store) GetProfile(id int64) (raw, collectedAt string, err error) {
	err = s.db.QueryRow(`SELECT raw, collected_at FROM host_profiles WHERE server_id = ?`, id).Scan(&raw, &collectedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return raw, collectedAt, err
}

// Plan is a change the AI proposed. In M0 plans are recorded but not run.
type Plan struct {
	ID        int64  `json:"id"`
	ServerID  int64  `json:"serverId"`
	Title     string `json:"title"`
	Reason    string `json:"reason"`
	Steps     string `json:"steps"` // JSON array of core.Step
	Status    string `json:"status"`
	Result    string `json:"result"` // JSON: before/after comparison of the last run
	CreatedAt string `json:"createdAt"`
}

// AddPlan records a proposed plan.
func (s *Store) AddPlan(p Plan) (Plan, error) {
	p.CreatedAt = now()
	res, err := s.db.Exec(`INSERT INTO plans (server_id, title, reason, steps, status, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		p.ServerID, p.Title, p.Reason, p.Steps, p.Status, p.CreatedAt)
	if err != nil {
		return Plan{}, err
	}
	p.ID, err = res.LastInsertId()
	return p, err
}

const planCols = `id, server_id, title, reason, steps, status, result, created_at`

func scanPlan(row interface{ Scan(...any) error }) (Plan, error) {
	var p Plan
	err := row.Scan(&p.ID, &p.ServerID, &p.Title, &p.Reason, &p.Steps, &p.Status, &p.Result, &p.CreatedAt)
	return p, err
}

// ListPlans returns plans, newest first.
func (s *Store) ListPlans(limit int) ([]Plan, error) {
	rows, err := s.db.Query(`SELECT `+planCols+` FROM plans ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Plan{}
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPlan returns one plan.
func (s *Store) GetPlan(id int64) (Plan, error) {
	p, err := scanPlan(s.db.QueryRow(`SELECT `+planCols+` FROM plans WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Plan{}, ErrNotFound
	}
	return p, err
}

// UpdatePlan saves a plan's steps, status and result.
func (s *Store) UpdatePlan(p Plan) error {
	_, err := s.db.Exec(`UPDATE plans SET steps = ?, status = ?, result = ? WHERE id = ?`, p.Steps, p.Status, p.Result, p.ID)
	return err
}

// AuditEntry is one line of the operation history.
type AuditEntry struct {
	ID     int64  `json:"id"`
	At     string `json:"at"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Target string `json:"target"`
	Detail string `json:"detail"`
}

// Audit appends to the operation history. Callers must not pass secrets.
func (s *Store) Audit(actor, action, target, detail string) error {
	_, err := s.db.Exec(`INSERT INTO audit_logs (at, actor, action, target, detail) VALUES (?, ?, ?, ?, ?)`,
		now(), actor, action, target, detail)
	return err
}

// ListAudit returns the newest entries first.
func (s *Store) ListAudit(limit int) ([]AuditEntry, error) {
	rows, err := s.db.Query(`SELECT id, at, actor, action, target, detail FROM audit_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.Action, &e.Target, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Usage is the token count and cost of one model call.
type Usage struct {
	Model        string  `json:"model"`
	InputTokens  int     `json:"inputTokens"`
	CachedTokens int     `json:"cachedTokens"`
	OutputTokens int     `json:"outputTokens"`
	Cost         float64 `json:"cost"`
	Currency     string  `json:"currency"`
}

// AddUsage records one model call.
func (s *Store) AddUsage(u Usage) error {
	_, err := s.db.Exec(`INSERT INTO ai_usage (at, model, input_tokens, cached_tokens, output_tokens, cost, currency)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, now(), u.Model, u.InputTokens, u.CachedTokens, u.OutputTokens, u.Cost, u.Currency)
	return err
}

// MonthCost sums AI spending for the current calendar month (UTC), per currency.
func (s *Store) MonthCost() (map[string]float64, error) {
	start := time.Now().UTC().Format("2006-01") + "-01"
	rows, err := s.db.Query(`SELECT currency, SUM(cost) FROM ai_usage WHERE at >= ? GROUP BY currency`, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var cur string
		var sum float64
		if err := rows.Scan(&cur, &sum); err != nil {
			return nil, err
		}
		out[cur] = sum
	}
	return out, rows.Err()
}

// Setting returns a stored non-secret setting, or "" if unset.
func (s *Store) Setting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting stores a non-secret setting.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
