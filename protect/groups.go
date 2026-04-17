package protect

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// GroupsProvider resolves groups for a given username. Groups are used by
// the session ACL to let group-mates manage each other's sessions (including
// sessions owned by service accounts such as CI runners).
type GroupsProvider interface {
	GroupsFor(user string) []string
}

// StaticGroups is a no-op provider returned when group membership is not
// configured. It keeps the rest of the codebase free of nil checks.
type StaticGroups struct{}

func (StaticGroups) GroupsFor(string) []string { return nil }

// FileGroupsProvider loads group membership from a JSON file of the shape
//
//	{ "qa-payments": ["alice", "jenkins-bot"], "qa-growth": ["bob"] }
//
// It maintains a reverse index (user -> sorted deduplicated groups) for O(1)
// lookup. Reload is safe to call concurrently with GroupsFor.
type FileGroupsProvider struct {
	mu     sync.RWMutex
	path   string
	byUser map[string][]string
}

// NewFileGroupsProvider reads and validates the file immediately so that
// misconfiguration fails at startup rather than on the first request.
func NewFileGroupsProvider(path string) (*FileGroupsProvider, error) {
	p := &FileGroupsProvider{path: path}
	if err := p.Reload(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *FileGroupsProvider) Reload() error {
	data, err := os.ReadFile(p.path)
	if err != nil {
		return fmt.Errorf("read groups file: %w", err)
	}
	byUser, err := parseGroupsFile(data)
	if err != nil {
		return fmt.Errorf("parse groups file %s: %w", p.path, err)
	}
	p.mu.Lock()
	p.byUser = byUser
	p.mu.Unlock()
	return nil
}

func (p *FileGroupsProvider) GroupsFor(user string) []string {
	if user == "" {
		return nil
	}
	p.mu.RLock()
	groups := p.byUser[user]
	p.mu.RUnlock()
	if len(groups) == 0 {
		return nil
	}
	out := make([]string, len(groups))
	copy(out, groups)
	return out
}

func parseGroupsFile(data []byte) (map[string][]string, error) {
	// Reject unknown fields so typos like {"team-name": ..., "member": ...}
	// (a plausible mistake) surface instead of silently loading nothing.
	var raw map[string][]string
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, errors.New("groups file is empty or null")
	}
	byUser := make(map[string]map[string]struct{})
	for group, members := range raw {
		group = strings.TrimSpace(group)
		if group == "" {
			return nil, errors.New("group name must not be empty")
		}
		if strings.ContainsAny(group, "\r\n\t") {
			return nil, fmt.Errorf("group %q: name contains control characters", group)
		}
		seen := make(map[string]struct{}, len(members))
		for _, m := range members {
			m = strings.TrimSpace(m)
			if m == "" {
				return nil, fmt.Errorf("group %q: member name must not be empty", group)
			}
			if strings.ContainsAny(m, "\r\n\t") {
				return nil, fmt.Errorf("group %q: member %q contains control characters", group, m)
			}
			if _, dup := seen[m]; dup {
				return nil, fmt.Errorf("group %q: duplicate member %q", group, m)
			}
			seen[m] = struct{}{}
			if _, ok := byUser[m]; !ok {
				byUser[m] = make(map[string]struct{})
			}
			byUser[m][group] = struct{}{}
		}
	}
	out := make(map[string][]string, len(byUser))
	for user, groupSet := range byUser {
		list := make([]string, 0, len(groupSet))
		for g := range groupSet {
			list = append(list, g)
		}
		sort.Strings(list)
		out[user] = list
	}
	return out, nil
}
