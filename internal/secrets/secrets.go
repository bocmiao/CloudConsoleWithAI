// Package secrets stores passwords and API keys outside the database: in
// the OS keychain (Windows Credential Manager, macOS Keychain, Linux Secret
// Service) when available, otherwise in a 0600 file in the data directory.
package secrets

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/zalando/go-keyring"
)

// ErrNotFound is returned when a key has no stored value.
var ErrNotFound = errors.New("secret not found")

const service = "MiaoPanel"

// Store keeps secret strings by key.
type Store interface {
	Get(key string) (string, error)
	Set(key, value string) error
	Delete(key string) error
	// Kind is "keychain" or "file", so the UI can tell the user where
	// secrets live.
	Kind() string
}

// Open returns the OS keychain when it works on this machine, otherwise a
// file store inside dir.
func Open(dir string) Store {
	const probe = "miaopanel-probe"
	if err := keyring.Set(service, probe, "ok"); err == nil {
		_ = keyring.Delete(service, probe)
		return keychain{}
	}
	return OpenFile(dir)
}

// OpenFile returns a store backed by dir/secrets.json (mode 0600).
func OpenFile(dir string) Store {
	return &fileStore{path: filepath.Join(dir, "secrets.json")}
}

type keychain struct{}

func (keychain) Get(key string) (string, error) {
	v, err := keyring.Get(service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return v, err
}

func (keychain) Set(key, value string) error { return keyring.Set(service, key, value) }

func (keychain) Delete(key string) error {
	err := keyring.Delete(service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

func (keychain) Kind() string { return "keychain" }

type fileStore struct {
	mu   sync.Mutex
	path string
}

func (f *fileStore) load() (map[string]string, error) {
	m := map[string]string{}
	data, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (f *fileStore) save(m map[string]string) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

func (f *fileStore) Get(key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return "", err
	}
	v, ok := m[key]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (f *fileStore) Set(key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	m[key] = value
	return f.save(m)
}

func (f *fileStore) Delete(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	delete(m, key)
	return f.save(m)
}

func (f *fileStore) Kind() string { return "file" }
