package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFilterClientEnv_AllowsWhitelistedPrefixes(t *testing.T) {
	in := []string{"SELENWRIGHT_LOG=debug", "SE_BROWSER_VERSION=120", "PATH=/tmp/evil", "LD_PRELOAD=/tmp/rootkit.so", "HOME=/root"}
	out := filterClientEnv(in)
	require.Equal(t, []string{"SELENWRIGHT_LOG=debug", "SE_BROWSER_VERSION=120"}, out)
}

func TestFilterClientEnv_EmptyInputReturnsNil(t *testing.T) {
	require.Nil(t, filterClientEnv(nil))
	require.Nil(t, filterClientEnv([]string{}))
}

func TestFilterClientEnv_DropsEntriesWithoutEquals(t *testing.T) {
	in := []string{"JUSTNAME", "SELENWRIGHT_OK=1"}
	out := filterClientEnv(in)
	require.Equal(t, []string{"SELENWRIGHT_OK=1"}, out)
}

func TestFilterClientEnv_DropsPrefixSuffixOnlyMatches(t *testing.T) {
	// Ensure "FOO_SELENWRIGHT_X=1" is NOT accepted (prefix must be at start).
	in := []string{"FOO_SELENWRIGHT_X=1", "SELENWRIGHT_OK=1"}
	out := filterClientEnv(in)
	require.Equal(t, []string{"SELENWRIGHT_OK=1"}, out)
}

func TestFilterClientEnv_DropsEmptyName(t *testing.T) {
	in := []string{"=somevalue", "SE_OK=1"}
	out := filterClientEnv(in)
	require.Equal(t, []string{"SE_OK=1"}, out)
}
