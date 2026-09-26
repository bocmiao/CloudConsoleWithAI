// Package store keeps Miao Panel's local state in a SQLite file. Secrets
// never go here; see package secrets.
package store

import (
	"database/sql"
	"encoding/json"
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
	auth_kind  TEXT NOT NULL,              -- password | key | tat
	key_path   TEXT NOT NULL DEFAULT '',
	host_key   TEXT NOT NULL DEFAULT '',   -- SHA256 fingerprint recorded on first connect
	adapter    TEXT NOT NULL DEFAULT '',   -- 1panel | bt | linux, from discovery
	created_at TEXT NOT NULL,
	instance_id TEXT NOT NULL DEFAULT '',  -- Tencent Cloud instance, for auth_kind tat
	region      TEXT NOT NULL DEFAULT ''
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
CREATE TABLE IF NOT EXISTS exec_logs (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	started_at    TEXT NOT NULL,
	finished_at   TEXT NOT NULL DEFAULT '',
	server_id     INTEGER NOT NULL,
	server_name   TEXT NOT NULL,           -- kept so the entry survives deleting the server
	adapter       TEXT NOT NULL DEFAULT '',-- environment the action was resolved for
	origin        TEXT NOT NULL,           -- ai | user | plan
	kind          TEXT NOT NULL,           -- read | change | rollback
	title         TEXT NOT NULL,
	note          TEXT NOT NULL DEFAULT '',-- the AI's explanation of a change
	capability    TEXT NOT NULL DEFAULT '',
	params        TEXT NOT NULL DEFAULT '',-- JSON
	via           TEXT NOT NULL DEFAULT '',
	commands      TEXT NOT NULL DEFAULT '',-- exactly what ran on the server
	script_name   TEXT NOT NULL DEFAULT '',
	script        TEXT NOT NULL DEFAULT '',-- full text of an action script
	status        TEXT NOT NULL,
	output        TEXT NOT NULL DEFAULT '',
	undo          TEXT NOT NULL DEFAULT '',-- JSON: what a rollback needs
	reversible    INTEGER NOT NULL DEFAULT 0,
	backup_dir    TEXT NOT NULL DEFAULT '',
	rollback_file TEXT NOT NULL DEFAULT '',-- standalone rollback script on the server
	plan_id       INTEGER NOT NULL DEFAULT 0,
	step_idx      INTEGER NOT NULL DEFAULT -1,
	undo_of       INTEGER NOT NULL DEFAULT 0,-- for a rollback: the entry it reverted
	undone_by     INTEGER NOT NULL DEFAULT 0 -- for a change: the rollback that reverted it
);
CREATE TABLE IF NOT EXISTS conversations (
	id         TEXT PRIMARY KEY,
	title      TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS chat_messages (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	conv_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
	at      TEXT NOT NULL,
	role    TEXT NOT NULL,                 -- user | assistant | error
	text    TEXT NOT NULL,
	extra   TEXT NOT NULL DEFAULT ''       -- JSON: what the AI looked at, cost, checklists
);
CREATE INDEX IF NOT EXISTS chat_messages_conv ON chat_messages(conv_id, id);
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
-- Web edition: who may log in, and who is logged in.
CREATE TABLE IF NOT EXISTS users (
	id         INTEGER PRIMARY KEY,
	name       TEXT NOT NULL UNIQUE,
	password   TEXT NOT NULL,              -- bcrypt hash
	totp       INTEGER NOT NULL DEFAULT 0, -- two-step login on; the key is in the secret store
	created_at TEXT NOT NULL,
	changed_at TEXT NOT NULL               -- when the password was last set
);
CREATE TABLE IF NOT EXISTS sessions (
	id         TEXT PRIMARY KEY,           -- SHA-256 of the cookie, never the cookie itself
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at TEXT NOT NULL,
	seen_at    TEXT NOT NULL,
	ip         TEXT NOT NULL DEFAULT '',
	ua         TEXT NOT NULL DEFAULT ''
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
	for _, c := range [][3]string{
		{"plans", "result", "TEXT NOT NULL DEFAULT ''"},
		{"servers", "instance_id", "TEXT NOT NULL DEFAULT ''"},
		{"servers", "region", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := addColumn(db, c[0], c[1], c[2]); err != nil {
			db.Close()
			return nil, fmt.Errorf("migrate: %w", err)
		}
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
	// InstanceID and Region locate a Tencent Cloud instance reached
	// through its automation agent (AuthKind "tat").
	InstanceID string `json:"instanceId,omitempty"`
	Region     string `json:"region,omitempty"`
}

// AddServer inserts a server and returns it with its new ID.
func (s *Store) AddServer(sv Server) (Server, error) {
	sv.CreatedAt = now()
	res, err := s.db.Exec(`INSERT INTO servers (name, host, port, username, auth_kind, key_path, created_at, instance_id, region)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, sv.Name, sv.Host, sv.Port, sv.Username, sv.AuthKind, sv.KeyPath, sv.CreatedAt, sv.InstanceID, sv.Region)
	if err != nil {
		return Server{}, err
	}
	sv.ID, err = res.LastInsertId()
	return sv, err
}

const serverCols = `s.id, s.name, s.host, s.port, s.username, s.auth_kind, s.key_path, s.host_key, s.adapter, s.created_at,
	COALESCE(p.collected_at, ''), s.instance_id, s.region`

func scanServer(row interface{ Scan(...any) error }) (Server, error) {
	var sv Server
	err := row.Scan(&sv.ID, &sv.Name, &sv.Host, &sv.Port, &sv.Username, &sv.AuthKind, &sv.KeyPath,
		&sv.HostKey, &sv.Adapter, &sv.CreatedAt, &sv.ProfiledAt, &sv.InstanceID, &sv.Region)
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

// PlansWithCapability returns a server's plans that have a step of this
// capability, newest first, however many other plans came after them.
func (s *Store) PlansWithCapability(serverID int64, capability string) ([]Plan, error) {
	rows, err := s.db.Query(`SELECT `+planCols+` FROM plans WHERE server_id = ? AND steps LIKE ? ORDER BY id DESC LIMIT 50`,
		serverID, `%"`+capability+`"%`)
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

// LoggedInFrom says whether the account logged in from this address
// since then, by the log.
func (s *Store) LoggedInFrom(name, ip string, since time.Time) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = 'auth.login' AND target = ? AND detail = ? AND at >= ?`,
		name, ip, since.UTC().Format(time.RFC3339)).Scan(&n)
	return n > 0, err
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

// ExecLog is one thing Miao Panel ran on a server: a read-only check, a
// change, or a rollback.
type ExecLog struct {
	ID           int64             `json:"id"`
	StartedAt    string            `json:"startedAt"`
	FinishedAt   string            `json:"finishedAt"`
	ServerID     int64             `json:"serverId"`
	ServerName   string            `json:"serverName"`
	Adapter      string            `json:"adapter"`
	Origin       string            `json:"origin"`
	Kind         string            `json:"kind"`
	Title        string            `json:"title"`
	Note         string            `json:"note"`
	Capability   string            `json:"capability"`
	Params       map[string]any    `json:"params"`
	Via          string            `json:"via"`
	Commands     string            `json:"commands"`
	ScriptName   string            `json:"scriptName"`
	Script       string            `json:"script,omitempty"`
	Status       string            `json:"status"`
	Output       string            `json:"output,omitempty"`
	Undo         map[string]string `json:"undo"`
	Reversible   bool              `json:"reversible"`
	BackupDir    string            `json:"backupDir"`
	RollbackFile string            `json:"rollbackFile"`
	PlanID       int64             `json:"planId"`
	StepIdx      int               `json:"stepIdx"`
	UndoOf       int64             `json:"undoOf"`
	UndoneBy     int64             `json:"undoneBy"`
}

// Execution kinds.
const (
	ExecRead     = "read"
	ExecChange   = "change"
	ExecRollback = "rollback"
)

// ExecRunning marks an entry whose command has not finished;
// ExecInterrupted one that never will, because Miao Panel was closed.
const (
	ExecRunning     = "running"
	ExecInterrupted = "interrupted"
)

func jsonText(v any) string {
	data, err := json.Marshal(v)
	if err != nil || string(data) == "null" {
		return ""
	}
	return string(data)
}

// AddExec records the start of an execution and returns it with its ID.
func (s *Store) AddExec(e ExecLog) (ExecLog, error) {
	if e.StartedAt == "" {
		e.StartedAt = now()
	}
	res, err := s.db.Exec(`INSERT INTO exec_logs (started_at, finished_at, server_id, server_name, adapter, origin, kind,
		title, note, capability, params, via, commands, script_name, script, status, output, undo, reversible,
		backup_dir, rollback_file, plan_id, step_idx, undo_of, undone_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.StartedAt, e.FinishedAt, e.ServerID, e.ServerName, e.Adapter, e.Origin, e.Kind,
		e.Title, e.Note, e.Capability, jsonText(e.Params), e.Via, e.Commands, e.ScriptName, e.Script, e.Status, e.Output,
		jsonText(e.Undo), e.Reversible, e.BackupDir, e.RollbackFile, e.PlanID, e.StepIdx, e.UndoOf, e.UndoneBy)
	if err != nil {
		return e, err
	}
	e.ID, err = res.LastInsertId()
	return e, err
}

// UpdateExec saves an entry's outcome.
func (s *Store) UpdateExec(e ExecLog) error {
	_, err := s.db.Exec(`UPDATE exec_logs SET finished_at = ?, commands = ?, script_name = ?, script = ?, status = ?,
		output = ?, undo = ?, backup_dir = ?, rollback_file = ?, undone_by = ? WHERE id = ?`,
		e.FinishedAt, e.Commands, e.ScriptName, e.Script, e.Status, e.Output, jsonText(e.Undo),
		e.BackupDir, e.RollbackFile, e.UndoneBy, e.ID)
	return err
}

const execCols = `id, started_at, finished_at, server_id, server_name, adapter, origin, kind, title, note, capability,
	params, via, commands, script_name, %s, status, %s, undo, reversible, backup_dir, rollback_file, plan_id, step_idx,
	undo_of, undone_by`

func scanExec(row interface{ Scan(...any) error }) (ExecLog, error) {
	var e ExecLog
	var params, undo string
	err := row.Scan(&e.ID, &e.StartedAt, &e.FinishedAt, &e.ServerID, &e.ServerName, &e.Adapter, &e.Origin, &e.Kind,
		&e.Title, &e.Note, &e.Capability, &params, &e.Via, &e.Commands, &e.ScriptName, &e.Script, &e.Status, &e.Output,
		&undo, &e.Reversible, &e.BackupDir, &e.RollbackFile, &e.PlanID, &e.StepIdx, &e.UndoOf, &e.UndoneBy)
	if err != nil {
		return e, err
	}
	if params != "" {
		_ = json.Unmarshal([]byte(params), &e.Params)
	}
	if undo != "" {
		_ = json.Unmarshal([]byte(undo), &e.Undo)
	}
	return e, nil
}

// GetExec returns one entry with its script and output.
func (s *Store) GetExec(id int64) (ExecLog, error) {
	e, err := scanExec(s.db.QueryRow(`SELECT `+fmt.Sprintf(execCols, "script", "output")+` FROM exec_logs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ExecLog{}, ErrNotFound
	}
	return e, err
}

// ListExec returns entries newest first, without scripts and output. With
// changesOnly, read-only checks are left out.
func (s *Store) ListExec(changesOnly bool, limit int) ([]ExecLog, error) {
	where := ""
	if changesOnly {
		where = `WHERE kind != 'read'`
	}
	rows, err := s.db.Query(`SELECT `+fmt.Sprintf(execCols, "''", "''")+` FROM exec_logs `+where+` ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ExecLog{}
	for rows.Next() {
		e, err := scanExec(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkInterrupted flags entries left running by a previous run of Miao
// Panel, which was closed before they finished.
func (s *Store) MarkInterrupted() error {
	_, err := s.db.Exec(`UPDATE exec_logs SET status = ?, finished_at = ? WHERE status = ?`, ExecInterrupted, now(), ExecRunning)
	return err
}

// Conversation is a saved chat with the AI.
type Conversation struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// ChatMessage is one message in a conversation.
type ChatMessage struct {
	ID    int64  `json:"id"`
	At    string `json:"at"`
	Role  string `json:"role"`
	Text  string `json:"text"`
	Extra string `json:"-"`
}

// AddConversation starts a saved conversation.
func (s *Store) AddConversation(id, title string) (Conversation, error) {
	c := Conversation{ID: id, Title: title, CreatedAt: now()}
	c.UpdatedAt = c.CreatedAt
	_, err := s.db.Exec(`INSERT INTO conversations (id, title, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		c.ID, c.Title, c.CreatedAt, c.UpdatedAt)
	return c, err
}

// GetConversation returns one conversation.
func (s *Store) GetConversation(id string) (Conversation, error) {
	var c Conversation
	err := s.db.QueryRow(`SELECT id, title, created_at, updated_at FROM conversations WHERE id = ?`, id).
		Scan(&c.ID, &c.Title, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// ListConversations returns conversations, most recently used first.
func (s *Store) ListConversations(limit int) ([]Conversation, error) {
	rows, err := s.db.Query(`SELECT id, title, created_at, updated_at FROM conversations ORDER BY updated_at DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		var c Conversation
		if err := rows.Scan(&c.ID, &c.Title, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteConversation removes a conversation and its messages.
func (s *Store) DeleteConversation(id string) error {
	_, err := s.db.Exec(`DELETE FROM conversations WHERE id = ?`, id)
	return err
}

// AddChatMessage appends a message and marks the conversation as used.
func (s *Store) AddChatMessage(convID, role, text, extra string) (ChatMessage, error) {
	m := ChatMessage{At: now(), Role: role, Text: text, Extra: extra}
	tx, err := s.db.Begin()
	if err != nil {
		return m, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO chat_messages (conv_id, at, role, text, extra) VALUES (?, ?, ?, ?, ?)`, convID, m.At, role, text, extra)
	if err != nil {
		return m, err
	}
	if m.ID, err = res.LastInsertId(); err != nil {
		return m, err
	}
	if _, err := tx.Exec(`UPDATE conversations SET updated_at = ? WHERE id = ?`, m.At, convID); err != nil {
		return m, err
	}
	return m, tx.Commit()
}

// ChatMessages returns a conversation's messages in order.
func (s *Store) ChatMessages(convID string) ([]ChatMessage, error) {
	rows, err := s.db.Query(`SELECT id, at, role, text, extra FROM chat_messages WHERE conv_id = ? ORDER BY id`, convID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChatMessage{}
	for rows.Next() {
		var m ChatMessage
		if err := rows.Scan(&m.ID, &m.At, &m.Role, &m.Text, &m.Extra); err != nil {
			return nil, err
		}
		out = append(out, m)
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
	// The month as the user sees it (local time), in the UTC the table uses.
	t := time.Now()
	start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location()).UTC().Format(time.RFC3339)
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

// DeleteSetting removes a setting.
func (s *Store) DeleteSetting(key string) error {
	_, err := s.db.Exec(`DELETE FROM settings WHERE key = ?`, key)
	return err
}
