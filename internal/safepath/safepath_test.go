package safepath

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJoin_ValidNames(t *testing.T) {
	cases := []struct {
		name string
		root string
		in   string
		want string
	}{
		{"plain basename", "/var/lib/sw", "video.mp4", filepath.Clean("/var/lib/sw/video.mp4")},
		{"nested subdir", "/var/lib/sw", "sub/video.mp4", filepath.Clean("/var/lib/sw/sub/video.mp4")},
		{"current dir prefix", "/var/lib/sw", "./video.mp4", filepath.Clean("/var/lib/sw/video.mp4")},
		{"redundant separators", "/var/lib/sw", "sub//video.mp4", filepath.Clean("/var/lib/sw/sub/video.mp4")},
		{"trailing slash on root", "/var/lib/sw/", "video.mp4", filepath.Clean("/var/lib/sw/video.mp4")},
		{"name resolves to root", "/var/lib/sw", "sub/..", filepath.Clean("/var/lib/sw")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Join(tc.root, tc.in)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestJoin_RejectsTraversal(t *testing.T) {
	cases := []struct {
		name string
		root string
		in   string
	}{
		{"parent escape", "/var/lib/sw", "../etc/passwd"},
		{"nested parent escape", "/var/lib/sw", "sub/../../etc/passwd"},
		{"deep parent escape", "/var/lib/sw", "../../../tmp/evil"},
		{"absolute unix", "/var/lib/sw", "/etc/passwd"},
		{"empty name", "/var/lib/sw", ""},
		{"empty root", "", "video.mp4"},
		{"name is single dotdot", "/var/lib/sw", ".."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Join(tc.root, tc.in)
			require.Error(t, err)
			require.True(t, errors.Is(err, ErrEscapesRoot),
				"expected ErrEscapesRoot, got %v", err)
		})
	}
}

// TestJoin_PrefixIsNotEnough guards against a naive HasPrefix-only check
// that would let "/var/lib/sw-evil" pass for root "/var/lib/sw".
func TestJoin_PrefixIsNotEnough(t *testing.T) {
	// Construct: root=/var/lib/sw, attempt name that, after Join+Clean,
	// resolves to /var/lib/sw-evil/x. Because filepath.Join can't escape
	// to a sibling directory via subdirectory traversal alone (it goes
	// "up" then "across"), this is more about documenting the boundary.
	_, err := Join("/var/lib/sw", "../sw-evil/x")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrEscapesRoot))
}
