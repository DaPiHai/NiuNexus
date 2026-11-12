// Package secrets stores proxy credentials separately from the readable
// configuration document.
package secrets

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// Data is the secret payload.
type Data struct {
	ProxyPassword    string `json:"proxyPassword,omitempty"`
	UpstreamPassword string `json:"upstreamPassword,omitempty"`
}

// Store owns secrets.json with root-only permissions.
type Store struct {
	path string
	mu   sync.Mutex
}

// New creates a store at path.
func New(path string) *Store {
	return &Store{path: path}
}

// Path returns the backing file path.
func (s *Store) Path() string { return s.path }

// Load reads the secret payload. A missing file is not an error.
func (s *Store) Load() (Data, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var data Data
	payload, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return data, nil
		}
		return data, err
	}
	if err := json.Unmarshal(payload, &data); err != nil {
		return Data{}, err
	}
	return data, nil
}

// Save writes the payload atomically. An empty payload removes the file.
func (s *Store) Save(data Data) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if data.ProxyPassword == "" && data.UpstreamPassword == "" {
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
