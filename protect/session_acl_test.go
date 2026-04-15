package protect

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionOwnership(t *testing.T) {
	cases := []struct {
		name   string
		id     Identity
		owner  string
		expect bool
	}{
		{"owner matches", Identity{User: "alice"}, "alice", true},
		{"owner mismatch", Identity{User: "alice"}, "bob", false},
		{"admin overrides mismatch", Identity{User: "root", IsAdmin: true}, "bob", true},
		{"admin and empty owner", Identity{User: "root", IsAdmin: true}, "", true},
		{"non-admin and empty owner", Identity{User: "alice"}, "", false},
		{"anonymous identity rejected", AnonymousIdentity, "alice", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expect, SessionOwnership(tc.id, tc.owner))
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
