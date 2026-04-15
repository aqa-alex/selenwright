// PR #22 regression test for the test-hooks switch (audit H-1).
// testing.Testing() was the gate that previously let init() swallow
// fatal auth/source-trust errors under tests — but it also returns
// true in coverage-instrumented production binaries, which silently
// disabled authentication. The replacement is a package-level flag
// flipped only from a _test.go var initializer.

package main

import (
	"testing"

	assert "github.com/stretchr/testify/require"
)

// TestTestHooksEnabledInTestBuild is a sanity check that the _test.go
// initializer ran before main.init() and left the flag set. If this
// ever fails it indicates the hook file was renamed, removed, or its
// var initializer was reordered below main.init's own — which would
// silently reintroduce the old behavior for production tests.
func TestTestHooksEnabledInTestBuild(t *testing.T) {
	assert.True(t, testHooksEnabled,
		"test_hooks_test.go must flip testHooksEnabled before main.init()")
}
