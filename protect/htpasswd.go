package protect

import (
	"bufio"
	"bytes"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

const HtpasswdRealm = "selenwright"

type HtpasswdAuthenticator struct {
	mu      sync.RWMutex
	path    string
	entries map[string][]byte
	admins  map[string]struct{}
}

func NewHtpasswdAuthenticator(path string, admins []string) (*HtpasswdAuthenticator, error) {
	a := &HtpasswdAuthenticator{path: path, admins: stringSet(admins)}
	if err := a.Reload(); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *HtpasswdAuthenticator) Reload() error {
	data, err := os.ReadFile(a.path)
	if err != nil {
		return fmt.Errorf("read htpasswd: %w", err)
	}
	entries, err := parseHtpasswd(data)
	if err != nil {
		return fmt.Errorf("parse htpasswd: %w", err)
	}
	a.mu.Lock()
	a.entries = entries
	a.mu.Unlock()
	return nil
}

func (a *HtpasswdAuthenticator) SetAdmins(admins []string) {
	a.mu.Lock()
	a.admins = stringSet(admins)
	a.mu.Unlock()
}

func (a *HtpasswdAuthenticator) Authenticate(r *http.Request) (Identity, error) {
	user, pass, ok := r.BasicAuth()
	if !ok {
		return Identity{}, ErrAuthRequired
	}
	a.mu.RLock()
	hash, known := a.entries[user]
	_, isAdmin := a.admins[user]
	a.mu.RUnlock()
	if !known {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(pass))
		return Identity{}, ErrAuthFailed
	}
	if err := bcrypt.CompareHashAndPassword(hash, []byte(pass)); err != nil {
		return Identity{}, ErrAuthFailed
	}
	return Identity{User: user, IsAdmin: isAdmin}, nil
}

func (a *HtpasswdAuthenticator) Realm() string { return HtpasswdRealm }

var dummyHash = []byte(`$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy`)

func parseHtpasswd(data []byte) (map[string][]byte, error) {
	entries := map[string][]byte{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon <= 0 || colon == len(line)-1 {
			return nil, fmt.Errorf("line %d: expected user:hash", lineNo)
		}
		user := line[:colon]
		hash := line[colon+1:]
		if !isBcryptHash(hash) {
			return nil, fmt.Errorf("line %d (user %q): only bcrypt hashes supported (use htpasswd -B)", lineNo, user)
		}
		if _, dup := entries[user]; dup {
			return nil, fmt.Errorf("line %d: duplicate user %q", lineNo, user)
		}
		entries[user] = []byte(hash)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, errors.New("no entries in htpasswd file")
	}
	return entries, nil
}

func isBcryptHash(s string) bool {
	if len(s) < 4 {
		return false
	}
	prefix := s[:4]
	return prefix == "$2a$" || prefix == "$2b$" || prefix == "$2y$"
}

func stringSet(in []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out[s] = struct{}{}
		}
	}
	return out
}

func ConstantTimeStringEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
