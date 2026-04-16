package discovery

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const adoptedFileName = "adopted.json"

// AdoptedEntry records when a specific image digest was adopted.
type AdoptedEntry struct {
	AdoptedAt time.Time `json:"adoptedAt"`
	Browser   string    `json:"browser"`
	Version   string    `json:"version"`
	RepoTag   string    `json:"repoTag"`
}

// DismissedEntry records when a specific image digest was dismissed.
type DismissedEntry struct {
	DismissedAt time.Time `json:"dismissedAt"`
	Browser     string    `json:"browser"`
	Version     string    `json:"version"`
	RepoTag     string    `json:"repoTag"`
}

type adoptedState struct {
	Adopted   map[string]AdoptedEntry   `json:"adopted"`
	Dismissed map[string]DismissedEntry `json:"dismissed"`
}

// AdoptedStore manages the set of adopted and dismissed image digests,
// persisted as a JSON file under the configured state directory.
type AdoptedStore struct {
	mu   sync.RWMutex
	path string
	data adoptedState
}

// NewAdoptedStore creates a store backed by <dir>/adopted.json.
// If the file exists it is loaded; otherwise the store starts empty.
func NewAdoptedStore(dir string) (*AdoptedStore, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create state dir %s: %w", dir, err)
	}
	s := &AdoptedStore{
		path: filepath.Join(dir, adoptedFileName),
		data: adoptedState{
			Adopted:   make(map[string]AdoptedEntry),
			Dismissed: make(map[string]DismissedEntry),
		},
	}
	if buf, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(buf, &s.data); err != nil {
			return nil, fmt.Errorf("parse %s: %w", s.path, err)
		}
		if s.data.Adopted == nil {
			s.data.Adopted = make(map[string]AdoptedEntry)
		}
		if s.data.Dismissed == nil {
			s.data.Dismissed = make(map[string]DismissedEntry)
		}
	}
	return s, nil
}

// IsAdopted returns true if the digest is in the adopted set.
func (s *AdoptedStore) IsAdopted(digest string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.data.Adopted[digest]
	return ok
}

// IsDismissed returns true if the digest is in the dismissed set.
func (s *AdoptedStore) IsDismissed(digest string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.data.Dismissed[digest]
	return ok
}

// Adopt moves an image from any prior state into the adopted set and persists.
func (s *AdoptedStore) Adopt(digest, browser, version, repoTag string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.Dismissed, digest)
	s.data.Adopted[digest] = AdoptedEntry{
		AdoptedAt: time.Now().UTC(),
		Browser:   browser,
		Version:   version,
		RepoTag:   repoTag,
	}
	return s.saveLocked()
}

// Dismiss moves an image from any prior state into the dismissed set and persists.
func (s *AdoptedStore) Dismiss(digest, browser, version, repoTag string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.Adopted, digest)
	s.data.Dismissed[digest] = DismissedEntry{
		DismissedAt: time.Now().UTC(),
		Browser:     browser,
		Version:     version,
		RepoTag:     repoTag,
	}
	return s.saveLocked()
}

// AdoptedDigests returns all currently adopted digests.
func (s *AdoptedStore) AdoptedDigests() map[string]AdoptedEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]AdoptedEntry, len(s.data.Adopted))
	for k, v := range s.data.Adopted {
		out[k] = v
	}
	return out
}

// saveLocked writes the state atomically (tmp + rename). Caller holds s.mu.
func (s *AdoptedStore) saveLocked() error {
	buf, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal adopted state: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o640); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, s.path, err)
	}
	return nil
}
