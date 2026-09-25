// Package config resolves where Miao Panel keeps its local data.
package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// AppName is used for the data directory and the keychain service name.
const AppName = "MiaoPanel"

// DataDir returns the directory for the database and other local files,
// creating it if needed. An explicit override wins; otherwise it lives in
// the user's config directory (%AppData%\MiaoPanel on Windows).
func DataDir(override string) (string, error) {
	dir := override
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("locate user config dir: %w", err)
		}
		dir = filepath.Join(base, AppName)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create data dir: %w", err)
	}
	return dir, nil
}
