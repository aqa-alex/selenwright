//go:build metadata
// +build metadata

// PR #22 regression tests for metadata path traversal (audit H-5).
// OnSessionStopped writes stoppedSession.SessionId into a filesystem
// path — and SessionId originates from the upstream browser container
// response (processBody), i.e. crosses a trust boundary. A crafted
// value like "../../etc/cron.d/evil" must be refused, not just cleaned.

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

	// A session id with a traversal component must be rejected before
	// os.WriteFile ever runs. The handler logs and returns; no file
	// lands on disk under logOutputDir or its parent.
	mp.OnSessionStopped(event.StoppedSession{
		Event: event.Event{
			RequestId: 42,
			SessionId: "../escape",
			Session: &session.Session{
				Started: time.Now(),
			},
		},
	})

	// Nothing must have been written under the tree.
	entries, err := os.ReadDir(tmp)
	assert.NoError(t, err)
	assert.Empty(t, entries, "traversal session id must not produce any file in logOutputDir")

	// And nothing must have been written one directory up either —
	// the specific attack the safepath guard blocks.
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
