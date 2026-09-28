// Package dbconf remembers where the web edition keeps its data: the
// SQLite file in the data directory, or the MySQL database the install
// wizard was given (database.json, its password in the secret store).
package dbconf

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bocmiao/CloudConsoleWithAI/internal/secrets"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

const (
	file      = "database.json"
	secretKey = "database/mysql_password"
)

// ErrNotInstalled means the data directory has no database yet: the
// install wizard chooses one.
var ErrNotInstalled = errors.New("not installed")

// Choice is where the data is kept.
type Choice struct {
	Kind  string      `json:"kind"` // sqlite or mysql
	MySQL store.MySQL `json:"mysql,omitempty"`
}

// Installed says whether a database has been chosen for dir: a
// database.json, or the SQLite file of an earlier version.
func Installed(dir string) bool {
	for _, f := range []string{file, store.DBFile} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			return true
		}
	}
	return false
}

// Open opens the database chosen for dir, or returns ErrNotInstalled.
func Open(dir string, sec secrets.Store) (*store.Store, error) {
	raw, err := os.ReadFile(filepath.Join(dir, file))
	if errors.Is(err, os.ErrNotExist) {
		if _, err := os.Stat(filepath.Join(dir, store.DBFile)); err != nil {
			return nil, ErrNotInstalled
		}
		return store.Open(dir)
	}
	if err != nil {
		return nil, err
	}
	var c Choice
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("%s 读不懂：%w", file, err)
	}
	switch c.Kind {
	case "sqlite":
		return store.Open(dir)
	case "mysql":
		pw, err := sec.Get(secretKey)
		if err != nil && !errors.Is(err, secrets.ErrNotFound) {
			return nil, err
		}
		c.MySQL.Password = pw
		st, err := store.OpenMySQL(c.MySQL)
		if err != nil {
			return nil, fmt.Errorf("连接 %s 失败：%w", c.MySQL, err)
		}
		return st, nil
	}
	return nil, fmt.Errorf("%s 里的数据库类型 %q 不认识", file, c.Kind)
}

// Save remembers the choice; the MySQL password goes to the secret store.
func Save(dir string, sec secrets.Store, c Choice) error {
	if c.Kind == "mysql" {
		if err := sec.Set(secretKey, c.MySQL.Password); err != nil {
			return err
		}
	} else {
		c.MySQL = store.MySQL{}
		_ = sec.Delete(secretKey)
	}
	c.MySQL.Password = ""
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, file), append(raw, '\n'), 0o600)
}
