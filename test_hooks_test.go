package main

import (
	"testing"

	assert "github.com/stretchr/testify/require"
)

var _ = func() bool {
	testHooksEnabled = true
	return true
}()

func TestTestHooksEnabledInTestBuild(t *testing.T) {
	assert.True(t, testHooksEnabled,
		"test_hooks_test.go must flip testHooksEnabled before main.init()")
}
