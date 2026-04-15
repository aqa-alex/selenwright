//go:build metadata
// +build metadata

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aqa-alex/selenwright/event"
	"github.com/aqa-alex/selenwright/session"
	assert "github.com/stretchr/testify/require"
)

func TestMetadataRejectsTraversalSessionId(t *testing.T) {
	tmp, err := os.MkdirTemp("", "selenwright-meta-test")
	assert.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })

	previousDir := app.logOutputDir
	app.logOutputDir = tmp
	t.Cleanup(func() { app.logOutputDir = previousDir })

	mp := &MetadataProcessor{}

	mp.OnSessionStopped(event.StoppedSession{
		Event: event.Event{
			RequestId: 42,
			SessionId: "../escape",
			Session: &session.Session{
				Started: time.Now(),
			},
		},
	})

	entries, err := os.ReadDir(tmp)
	assert.NoError(t, err)
	assert.Empty(t, entries, "traversal session id must not produce any file in logOutputDir")

	parent := filepath.Dir(tmp)
	parentEntries, err := os.ReadDir(parent)
	assert.NoError(t, err)
	for _, e := range parentEntries {
		if strings.Contains(e.Name(), "escape") {
			t.Fatalf("attacker-controlled id placed file at %s", filepath.Join(parent, e.Name()))
		}
	}
}

func TestMetadataAcceptsHonestSessionId(t *testing.T) {
	tmp, err := os.MkdirTemp("", "selenwright-meta-test")
	assert.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })

	previousDir := app.logOutputDir
	app.logOutputDir = tmp
	t.Cleanup(func() { app.logOutputDir = previousDir })

	mp := &MetadataProcessor{}
	sid := "abc123"
	mp.OnSessionStopped(event.StoppedSession{
		Event: event.Event{
			RequestId: 1,
			SessionId: sid,
			Session: &session.Session{
				Started: time.Now(),
			},
		},
	})

	expected := filepath.Join(tmp, sid+".json")
	info, err := os.Stat(expected)
	assert.NoError(t, err, "legitimate session id must still produce its metadata file")
	assert.False(t, info.IsDir())
}
