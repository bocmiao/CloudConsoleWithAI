package store

import (
	"database/sql"
	"errors"
	"time"
)

// User is someone who can log in to the web edition.
type User struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Password  string `json:"-"` // bcrypt hash
	TOTP      bool   `json:"totp"`
	CreatedAt string `json:"createdAt"`
	ChangedAt string `json:"changedAt"`
}

// Session is one logged-in browser. ID is the hash of its cookie.
type Session struct {
	ID        string `json:"-"`
	UserID    int64  `json:"userId"`
	CreatedAt string `json:"createdAt"`
	SeenAt    string `json:"seenAt"`
	IP        string `json:"ip"`
	UA        string `json:"ua"`
}

// CountUsers says how many accounts exist.
func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// AddUser creates an account with a password hash.
func (s *Store) AddUser(name, hash string) (User, error) {
	at := now()
	res, err := s.db.Exec(`INSERT INTO users (name, password, created_at, changed_at) VALUES (?, ?, ?, ?)`, name, hash, at, at)
	if err != nil {
		return User{}, err
	}
	id, _ := res.LastInsertId()
	return User{ID: id, Name: name, Password: hash, CreatedAt: at, ChangedAt: at}, nil
}

const userCols = `id, name, password, totp, created_at, changed_at`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var totp int
	err := row.Scan(&u.ID, &u.Name, &u.Password, &totp, &u.CreatedAt, &u.ChangedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	u.TOTP = totp != 0
	return u, err
}

// UserByName finds an account by its login name.
func (s *Store) UserByName(name string) (User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE name = ?`, name))
}

// GetUser finds an account by id.
func (s *Store) GetUser(id int64) (User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// SetPassword replaces an account's password hash.
func (s *Store) SetPassword(id int64, hash string) error {
	_, err := s.db.Exec(`UPDATE users SET password = ?, changed_at = ? WHERE id = ?`, hash, now(), id)
	return err
}

// SetTOTP turns two-step login on or off for an account.
func (s *Store) SetTOTP(id int64, on bool) error {
	v := 0
	if on {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE users SET totp = ? WHERE id = ?`, v, id)
	return err
}

// AddSession records a login.
func (s *Store) AddSession(se Session) error {
	_, err := s.db.Exec(`INSERT INTO sessions (id, user_id, created_at, seen_at, ip, ua) VALUES (?, ?, ?, ?, ?, ?)`,
		se.ID, se.UserID, se.CreatedAt, se.SeenAt, se.IP, se.UA)
	return err
}

// GetSession finds a session by the hash of its cookie.
func (s *Store) GetSession(id string) (Session, error) {
	var se Session
	err := s.db.QueryRow(`SELECT id, user_id, created_at, seen_at, ip, ua FROM sessions WHERE id = ?`, id).
		Scan(&se.ID, &se.UserID, &se.CreatedAt, &se.SeenAt, &se.IP, &se.UA)
	if errors.Is(err, sql.ErrNoRows) {
		return se, ErrNotFound
	}
	return se, err
}

// TouchSession notes that a session was used.
func (s *Store) TouchSession(id, at, ip string) error {
	_, err := s.db.Exec(`UPDATE sessions SET seen_at = ?, ip = ? WHERE id = ?`, at, ip, id)
	return err
}

// DeleteSession logs one session out.
func (s *Store) DeleteSession(id string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// DeleteSessions logs out every session of a user except keep.
func (s *Store) DeleteSessions(userID int64, keep string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ? AND id != ?`, userID, keep)
	return err
}

// ListSessions returns a user's sessions, most recently used first.
func (s *Store) ListSessions(userID int64) ([]Session, error) {
	rows, err := s.db.Query(`SELECT id, user_id, created_at, seen_at, ip, ua FROM sessions WHERE user_id = ? ORDER BY seen_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var se Session
		if err := rows.Scan(&se.ID, &se.UserID, &se.CreatedAt, &se.SeenAt, &se.IP, &se.UA); err != nil {
			return nil, err
		}
		out = append(out, se)
	}
	return out, rows.Err()
}

// DeleteStaleSessions removes sessions unused since idle or begun before
// oldest.
func (s *Store) DeleteStaleSessions(idle, oldest time.Time) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE seen_at < ? OR created_at < ?`,
		idle.UTC().Format(time.RFC3339), oldest.UTC().Format(time.RFC3339))
	return err
}
