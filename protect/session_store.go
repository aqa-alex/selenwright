package protect

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const sessionTokenBytes = 32

// AuthSession holds an authenticated identity and its expiry.
type AuthSession struct {
	Identity  Identity
	ExpiresAt time.Time
}

// SessionStore is an in-memory store mapping opaque tokens to authenticated sessions.
// Sessions are lost on process restart — users simply re-login.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]AuthSession
	ttl      time.Duration
	stopOnce sync.Once
	stopCh   chan struct{}
}

// NewSessionStore creates a store with the given session lifetime and starts
// a background goroutine that periodically removes expired entries.
func NewSessionStore(ttl time.Duration) *SessionStore {
	s := &SessionStore{
		sessions: make(map[string]AuthSession),
		ttl:      ttl,
		stopCh:   make(chan struct{}),
	}
	go s.cleanup()
	return s
}

// Create generates a cryptographically random token, stores the identity
// with the configured TTL, and returns the token.
func (s *SessionStore) Create(id Identity) (string, error) {
	b := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)

	s.mu.Lock()
	s.sessions[token] = AuthSession{
		Identity:  id,
		ExpiresAt: time.Now().Add(s.ttl),
	}
	s.mu.Unlock()

	return token, nil
}

// Validate looks up a token and returns the associated identity.
// Returns false if the token is unknown or expired.
func (s *SessionStore) Validate(token string) (Identity, bool) {
	s.mu.RLock()
	sess, ok := s.sessions[token]
	s.mu.RUnlock()

	if !ok || time.Now().After(sess.ExpiresAt) {
		if ok {
			s.Delete(token)
		}
		return Identity{}, false
	}
	return sess.Identity, true
}

// Delete removes a session by token (explicit logout).
func (s *SessionStore) Delete(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

// Stop terminates the background cleanup goroutine.
func (s *SessionStore) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}

// TTL returns the configured session lifetime.
func (s *SessionStore) TTL() time.Duration {
	return s.ttl
}

func (s *SessionStore) cleanup() {
	interval := s.ttl / 2
	if interval < time.Minute {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.removeExpired()
		case <-s.stopCh:
			return
		}
	}
}

func (s *SessionStore) removeExpired() {
	now := time.Now()
	s.mu.Lock()
	for token, sess := range s.sessions {
		if now.After(sess.ExpiresAt) {
			delete(s.sessions, token)
		}
	}
	s.mu.Unlock()
}
