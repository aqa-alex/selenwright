package protect

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	// TokenPlaintextPrefix is the visible prefix that marks a value as an API token.
	TokenPlaintextPrefix = "swr_live_"
	// tokenEntropyBytes controls the random body length (hex-encoded).
	tokenEntropyBytes = 16
	// tokenHashPrefixLen is the length of the sha256-based lookup index.
	tokenHashPrefixLen = 4
)

var (
	// ErrTokenNotFound is returned when a token id does not exist.
	ErrTokenNotFound = errors.New("token not found")
	// ErrTokenInvalid is returned for malformed token plaintext.
	ErrTokenInvalid = errors.New("token invalid")
)

// TokenRecord is the persisted representation of an API token.
type TokenRecord struct {
	ID          string     `json:"id"`
	HashPrefix  string     `json:"hashPrefix"`
	Hash        string     `json:"hash"`
	Owner       string     `json:"owner"`
	OwnerGroups []string   `json:"ownerGroups,omitempty"`
	Name        string     `json:"name"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastUsedAt  *time.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
}

// TokenMeta is the metadata view returned by list endpoints (no hash).
type TokenMeta struct {
	ID         string     `json:"id"`
	Owner      string     `json:"owner"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
}

// TokenStore persists API tokens to a JSON file on disk. It is safe for
// concurrent use. Last-used-at updates are throttled: in-memory changes are
// flushed to disk at most once per flushInterval per token.
type TokenStore struct {
	mu       sync.RWMutex
	path     string
	tokens   []TokenRecord
	byPrefix map[string][]int

	touchMu       sync.Mutex
	pendingTouch  map[string]time.Time
	flushInterval time.Duration
	stopOnce      sync.Once
	stopCh        chan struct{}
}

// NewTokenStore loads (or creates) a token store at the given file path.
// Missing file is treated as empty store. The parent directory is created if
// necessary.
func NewTokenStore(path string) (*TokenStore, error) {
	if path == "" {
		return nil, errors.New("token store path required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create token store dir: %w", err)
	}
	s := &TokenStore{
		path:          path,
		byPrefix:      map[string][]int{},
		pendingTouch:  map[string]time.Time{},
		flushInterval: time.Minute,
		stopCh:        make(chan struct{}),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	go s.touchFlushLoop()
	return s, nil
}

// Stop terminates the background flush goroutine and persists any pending
// last-used-at updates.
func (s *TokenStore) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
		_ = s.flushTouches()
	})
}

// Len returns the number of tokens in the store.
func (s *TokenStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.tokens)
}

// Create generates a new token for the given owner and persists it. Returns
// the token id and the one-time plaintext. The plaintext is never stored.
func (s *TokenStore) Create(owner, name string, groups []string) (string, string, error) {
	owner = strings.TrimSpace(owner)
	name = strings.TrimSpace(name)
	if owner == "" {
		return "", "", errors.New("owner required")
	}
	if name == "" {
		return "", "", errors.New("name required")
	}

	plaintext, err := generatePlaintext()
	if err != nil {
		return "", "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		return "", "", fmt.Errorf("hash token: %w", err)
	}
	id, err := generateID()
	if err != nil {
		return "", "", err
	}

	rec := TokenRecord{
		ID:          id,
		HashPrefix:  hashPrefix(plaintext),
		Hash:        string(hash),
		Owner:       owner,
		OwnerGroups: append([]string(nil), groups...),
		Name:        name,
		CreatedAt:   time.Now().UTC(),
	}

	s.mu.Lock()
	s.tokens = append(s.tokens, rec)
	s.rebuildIndexLocked()
	err = s.persistLocked()
	s.mu.Unlock()
	if err != nil {
		return "", "", err
	}
	return id, plaintext, nil
}

// SeedFromPlaintext inserts a token with a caller-supplied plaintext (hashed
// before persistence). Used to honor a SELENWRIGHT_AUTH_TOKEN env var on
// first-boot. If a record with the same plaintext already exists, the call is
// a no-op and returns the existing id with matched=true. Owner and name are
// trimmed; both must be non-empty.
func (s *TokenStore) SeedFromPlaintext(owner, name, plaintext string, groups []string) (id string, matched bool, err error) {
	owner = strings.TrimSpace(owner)
	name = strings.TrimSpace(name)
	if owner == "" {
		return "", false, errors.New("owner required")
	}
	if name == "" {
		return "", false, errors.New("name required")
	}
	if !strings.HasPrefix(plaintext, TokenPlaintextPrefix) {
		return "", false, ErrTokenInvalid
	}

	if existingOwner, _, existingID, ok := s.Lookup(plaintext); ok {
		_ = existingOwner
		return existingID, true, nil
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		return "", false, fmt.Errorf("hash token: %w", err)
	}
	newID, err := generateID()
	if err != nil {
		return "", false, err
	}
	rec := TokenRecord{
		ID:          newID,
		HashPrefix:  hashPrefix(plaintext),
		Hash:        string(hash),
		Owner:       owner,
		OwnerGroups: append([]string(nil), groups...),
		Name:        name,
		CreatedAt:   time.Now().UTC(),
	}
	s.mu.Lock()
	s.tokens = append(s.tokens, rec)
	s.rebuildIndexLocked()
	err = s.persistLocked()
	s.mu.Unlock()
	if err != nil {
		return "", false, err
	}
	return newID, false, nil
}

// Lookup validates a plaintext token and returns (owner, groups, true) on
// match. Returns false for unknown or expired tokens.
func (s *TokenStore) Lookup(plaintext string) (string, []string, string, bool) {
	if !strings.HasPrefix(plaintext, TokenPlaintextPrefix) {
		return "", nil, "", false
	}
	prefix := hashPrefix(plaintext)

	s.mu.RLock()
	candidates := append([]int(nil), s.byPrefix[prefix]...)
	hashes := make([][]byte, len(candidates))
	owners := make([]string, len(candidates))
	groups := make([][]string, len(candidates))
	ids := make([]string, len(candidates))
	expiries := make([]*time.Time, len(candidates))
	for i, idx := range candidates {
		hashes[i] = []byte(s.tokens[idx].Hash)
		owners[i] = s.tokens[idx].Owner
		groups[i] = s.tokens[idx].OwnerGroups
		ids[i] = s.tokens[idx].ID
		expiries[i] = s.tokens[idx].ExpiresAt
	}
	s.mu.RUnlock()

	now := time.Now().UTC()
	for i, h := range hashes {
		if bcrypt.CompareHashAndPassword(h, []byte(plaintext)) == nil {
			if exp := expiries[i]; exp != nil && now.After(*exp) {
				return "", nil, "", false
			}
			return owners[i], append([]string(nil), groups[i]...), ids[i], true
		}
	}
	return "", nil, "", false
}

// TouchLastUsed schedules a throttled last-used-at update for the given id.
// Returns immediately; the actual disk write happens in the background
// flush goroutine.
func (s *TokenStore) TouchLastUsed(id string) {
	if id == "" {
		return
	}
	now := time.Now().UTC()
	s.touchMu.Lock()
	s.pendingTouch[id] = now
	s.touchMu.Unlock()
}

// List returns token metadata (no hashes). If owner is non-empty, only tokens
// owned by that user are returned.
func (s *TokenStore) List(owner string) []TokenMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]TokenMeta, 0, len(s.tokens))
	for _, t := range s.tokens {
		if owner != "" && t.Owner != owner {
			continue
		}
		out = append(out, TokenMeta{
			ID:         t.ID,
			Owner:      t.Owner,
			Name:       t.Name,
			CreatedAt:  t.CreatedAt,
			LastUsedAt: t.LastUsedAt,
			ExpiresAt:  t.ExpiresAt,
		})
	}
	return out
}

// Revoke removes a token by id. Returns ErrTokenNotFound if the id is unknown.
func (s *TokenStore) Revoke(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.tokens {
		if t.ID == id {
			s.tokens = append(s.tokens[:i], s.tokens[i+1:]...)
			s.rebuildIndexLocked()
			return s.persistLocked()
		}
	}
	return ErrTokenNotFound
}

// RevokeByOwner removes all tokens owned by the given user. Returns the
// number of tokens removed.
func (s *TokenStore) RevokeByOwner(owner string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.tokens[:0]
	removed := 0
	for _, t := range s.tokens {
		if t.Owner == owner {
			removed++
			continue
		}
		kept = append(kept, t)
	}
	if removed == 0 {
		return 0, nil
	}
	s.tokens = kept
	s.rebuildIndexLocked()
	if err := s.persistLocked(); err != nil {
		return removed, err
	}
	return removed, nil
}

// Owners returns the distinct owner names across all stored tokens.
func (s *TokenStore) Owners() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]struct{}{}
	for _, t := range s.tokens {
		seen[t.Owner] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for o := range seen {
		out = append(out, o)
	}
	return out
}

func (s *TokenStore) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.tokens = nil
		s.rebuildIndexLocked()
		return nil
	}
	if err != nil {
		return fmt.Errorf("read token store: %w", err)
	}
	if len(data) == 0 {
		s.tokens = nil
		s.rebuildIndexLocked()
		return nil
	}
	var tokens []TokenRecord
	if err := json.Unmarshal(data, &tokens); err != nil {
		return fmt.Errorf("parse token store: %w", err)
	}
	s.tokens = tokens
	s.rebuildIndexLocked()
	return nil
}

func (s *TokenStore) rebuildIndexLocked() {
	idx := make(map[string][]int, len(s.tokens))
	for i, t := range s.tokens {
		idx[t.HashPrefix] = append(idx[t.HashPrefix], i)
	}
	s.byPrefix = idx
}

func (s *TokenStore) persistLocked() error {
	data, err := json.MarshalIndent(s.tokens, "", "  ")
	if err != nil {
		return fmt.Errorf("encode token store: %w", err)
	}
	return atomicWrite(s.path, data, 0o600)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tokens-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

func (s *TokenStore) touchFlushLoop() {
	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = s.flushTouches()
		case <-s.stopCh:
			return
		}
	}
}

func (s *TokenStore) flushTouches() error {
	s.touchMu.Lock()
	pending := s.pendingTouch
	s.pendingTouch = map[string]time.Time{}
	s.touchMu.Unlock()
	if len(pending) == 0 {
		return nil
	}

	s.mu.Lock()
	changed := false
	for i := range s.tokens {
		if t, ok := pending[s.tokens[i].ID]; ok {
			tt := t
			s.tokens[i].LastUsedAt = &tt
			changed = true
		}
	}
	if !changed {
		s.mu.Unlock()
		return nil
	}
	err := s.persistLocked()
	s.mu.Unlock()
	return err
}

func generatePlaintext() (string, error) {
	b := make([]byte, tokenEntropyBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return TokenPlaintextPrefix + hex.EncodeToString(b), nil
}

func generateID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return "tok_" + hex.EncodeToString(b), nil
}

func hashPrefix(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])[:tokenHashPrefixLen]
}
