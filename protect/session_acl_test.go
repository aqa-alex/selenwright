package protect

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionAccess(t *testing.T) {
	cases := []struct {
		name        string
		id          Identity
		owner       string
		ownerGroups []string
		expect      bool
	}{
		{"owner matches", Identity{User: "alice"}, "alice", nil, true},
		{"owner mismatch no groups", Identity{User: "alice"}, "bob", nil, false},
		{"admin overrides mismatch", Identity{User: "root", IsAdmin: true}, "bob", nil, true},
		{"admin with no groups still wins", Identity{User: "root", IsAdmin: true}, "", nil, true},
		{"non-admin and empty owner", Identity{User: "alice"}, "", nil, false},
		{"anonymous identity rejected", AnonymousIdentity, "alice", nil, false},

		{"group match", Identity{User: "alice", Groups: []string{"qa"}}, "jenkins-bot", []string{"qa"}, true},
		{"group match among many", Identity{User: "alice", Groups: []string{"ops", "qa"}}, "jenkins-bot", []string{"qa", "growth"}, true},
		{"disjoint groups", Identity{User: "alice", Groups: []string{"ops"}}, "jenkins-bot", []string{"qa"}, false},
		{"identity groups empty, owner has groups", Identity{User: "alice"}, "jenkins-bot", []string{"qa"}, false},
		{"identity has groups, owner has none", Identity{User: "alice", Groups: []string{"qa"}}, "jenkins-bot", nil, false},
		{"empty string in group list does not match empty", Identity{User: "alice", Groups: []string{""}}, "jenkins-bot", []string{""}, false},
		{"owner match beats group miss", Identity{User: "alice", Groups: []string{"ops"}}, "alice", []string{"qa"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expect, SessionAccess(tc.id, tc.owner, tc.ownerGroups))
		})
	}
}

func TestExtractSessionID(t *testing.T) {
	cases := []struct {
		path   string
		index  int
		expect string
	}{
		{"/vnc/abc123", 2, "abc123"},
		{"/session/sid", 2, "sid"},
		{"/devtools/abc/page/x", 2, "abc"},
		{"/wd/hub/session/sid", 4, "sid"},
		{"/playwright/session/pwsid", 3, "pwsid"},
		{"/short", 2, ""},
		{"", 2, ""},
		{"/vnc/", 2, ""},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			require.Equal(t, tc.expect, ExtractSessionID(tc.path, tc.index))
		})
	}
}
