package protect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newTestTokenStore(t *testing.T) (*TokenStore, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "auth", "tokens.json")
	s, err := NewTokenStore(path)
	require.NoError(t, err)
	t.Cleanup(s.Stop)
	return s, path
}

func TestTokenStore_CreateAndLookup(t *testing.T) {
	s, _ := newTestTokenStore(t)

	id, plaintext, err := s.Create("alice", "my-laptop", []string{"qa-team"})
	require.NoError(t, err)
	require.NotEmpty(t, id)
	require.True(t, strings.HasPrefix(plaintext, TokenPlaintextPrefix))

	owner, groups, gotID, ok := s.Lookup(plaintext)
	require.True(t, ok)
	require.Equal(t, "alice", owner)
	require.Equal(t, []string{"qa-team"}, groups)
	require.Equal(t, id, gotID)
}

func TestTokenStore_LookupUnknown(t *testing.T) {
	s, _ := newTestTokenStore(t)
	_, plaintext, err := s.Create("alice", "t1", nil)
	require.NoError(t, err)

	_, _, _, ok := s.Lookup(plaintext + "x")
	require.False(t, ok)

	_, _, _, ok = s.Lookup("not-a-token-at-all")
	require.False(t, ok)

	_, _, _, ok = s.Lookup(TokenPlaintextPrefix + "deadbeef")
	require.False(t, ok)
}

func TestTokenStore_CreateRejectsEmptyOwnerOrName(t *testing.T) {
	s, _ := newTestTokenStore(t)
	_, _, err := s.Create("", "x", nil)
	require.Error(t, err)
	_, _, err = s.Create("alice", "", nil)
	require.Error(t, err)
}

func TestTokenStore_Revoke(t *testing.T) {
	s, _ := newTestTokenStore(t)
	id, plaintext, err := s.Create("alice", "t1", nil)
	require.NoError(t, err)

	require.NoError(t, s.Revoke(id))

	_, _, _, ok := s.Lookup(plaintext)
	require.False(t, ok)

	require.ErrorIs(t, s.Revoke(id), ErrTokenNotFound)
}

func TestTokenStore_RevokeByOwner(t *testing.T) {
	s, _ := newTestTokenStore(t)
	_, p1, err := s.Create("alice", "laptop", nil)
	require.NoError(t, err)
	_, p2, err := s.Create("alice", "ci", nil)
	require.NoError(t, err)
	_, p3, err := s.Create("bob", "laptop", nil)
	require.NoError(t, err)

	n, err := s.RevokeByOwner("alice")
	require.NoError(t, err)
	require.Equal(t, 2, n)

	_, _, _, ok := s.Lookup(p1)
	require.False(t, ok)
	_, _, _, ok = s.Lookup(p2)
	require.False(t, ok)
	_, _, _, ok = s.Lookup(p3)
	require.True(t, ok)
}

func TestTokenStore_List(t *testing.T) {
	s, _ := newTestTokenStore(t)
	_, _, err := s.Create("alice", "laptop", nil)
	require.NoError(t, err)
	_, _, err = s.Create("bob", "ci", nil)
	require.NoError(t, err)

	all := s.List("")
	require.Len(t, all, 2)
	for _, m := range all {
		require.NotEmpty(t, m.ID)
		require.NotEmpty(t, m.Owner)
		require.NotEmpty(t, m.Name)
	}

	alice := s.List("alice")
	require.Len(t, alice, 1)
	require.Equal(t, "alice", alice[0].Owner)
}

func TestTokenStore_PersistenceAcrossReloads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth", "tokens.json")

	s1, err := NewTokenStore(path)
	require.NoError(t, err)
	_, plaintext, err := s1.Create("alice", "laptop", []string{"qa"})
	require.NoError(t, err)
	s1.Stop()

	s2, err := NewTokenStore(path)
	require.NoError(t, err)
	defer s2.Stop()

	owner, groups, _, ok := s2.Lookup(plaintext)
	require.True(t, ok)
	require.Equal(t, "alice", owner)
	require.Equal(t, []string{"qa"}, groups)
}

func TestTokenStore_PersistedFileHasNoPlaintext(t *testing.T) {
	s, path := newTestTokenStore(t)
	_, plaintext, err := s.Create("alice", "laptop", nil)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(data), plaintext, "plaintext token must never land on disk")
}

func TestTokenStore_MissingFileTreatedAsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth", "tokens.json")
	s, err := NewTokenStore(path)
	require.NoError(t, err)
	defer s.Stop()
	require.Equal(t, 0, s.Len())
}

func TestTokenStore_CorruptedFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "auth"), 0o700))
	path := filepath.Join(dir, "auth", "tokens.json")
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o600))

	_, err := NewTokenStore(path)
	require.Error(t, err)
}

func TestTokenStore_ExpiredTokenRejected(t *testing.T) {
	s, path := newTestTokenStore(t)
	_, plaintext, err := s.Create("alice", "t1", nil)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var recs []TokenRecord
	require.NoError(t, json.Unmarshal(data, &recs))
	past := time.Now().UTC().Add(-time.Hour)
	recs[0].ExpiresAt = &past
	out, err := json.MarshalIndent(recs, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, out, 0o600))

	s.Stop()
	s2, err := NewTokenStore(path)
	require.NoError(t, err)
	defer s2.Stop()

	_, _, _, ok := s2.Lookup(plaintext)
	require.False(t, ok)
}

func TestTokenStore_TouchLastUsedFlushes(t *testing.T) {
	s, path := newTestTokenStore(t)
	id, _, err := s.Create("alice", "t1", nil)
	require.NoError(t, err)

	s.TouchLastUsed(id)
	require.NoError(t, s.flushTouches())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var recs []TokenRecord
	require.NoError(t, json.Unmarshal(data, &recs))
	require.Len(t, recs, 1)
	require.NotNil(t, recs[0].LastUsedAt)
}

func TestTokenStore_ConcurrentCreateAndLookup(t *testing.T) {
	s, _ := newTestTokenStore(t)

	const n = 20
	plaintexts := make([]string, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, p, err := s.Create("user", "name", nil)
			require.NoError(t, err)
			plaintexts[i] = p
		}(i)
	}
	wg.Wait()

	for _, p := range plaintexts {
		_, _, _, ok := s.Lookup(p)
		require.True(t, ok)
	}
	require.Equal(t, n, s.Len())
}

func TestTokenStore_Owners(t *testing.T) {
	s, _ := newTestTokenStore(t)
	_, _, err := s.Create("alice", "t1", nil)
	require.NoError(t, err)
	_, _, err = s.Create("alice", "t2", nil)
	require.NoError(t, err)
	_, _, err = s.Create("bob", "t1", nil)
	require.NoError(t, err)

	owners := s.Owners()
	require.ElementsMatch(t, []string{"alice", "bob"}, owners)
}
