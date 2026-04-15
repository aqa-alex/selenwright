package main

import (
	"bytes"
	"encoding/json"
	"log"
	"testing"

	"github.com/aqa-alex/selenwright/internal/slogx"
	assert "github.com/stretchr/testify/require"
)

// TestLogJSONRoutesBracketedOutput feeds a representative log.Printf
// line through the slogx JSON handler and confirms it parses into the
// expected event/request_id/fields shape. The test doesn't flip the
// package-level routing — doing so would fight with every other test
// that reads human-readable log output — it installs a local
// redirect on a captured io.Writer instead.
func TestLogJSONRoutesBracketedOutput(t *testing.T) {
	buf := &bytes.Buffer{}

	prev := log.Default().Writer()
	prevFlags := log.Flags()
	log.SetOutput(slogx.CaptureWriter(buf, 0))
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prev)
		log.SetFlags(prevFlags)
	})

	log.Printf("[42] [SESSION_CREATED] [quota-a] [attempt-1]")
	var obj map[string]any
	assert.NoError(t, json.Unmarshal(buf.Bytes(), &obj))
	assert.Equal(t, "SESSION_CREATED", obj["event"])
	assert.Equal(t, "42", obj["request_id"])
	fields, _ := obj["fields"].([]any)
	assert.Equal(t, []any{"quota-a", "attempt-1"}, fields)
}

// TestLogJSONSentinelRequestId covers the "[-]" form used by init and
// lifecycle messages so the request_id key is cleanly absent rather
// than serialized as a dash.
func TestLogJSONSentinelRequestId(t *testing.T) {
	buf := &bytes.Buffer{}
	prev := log.Default().Writer()
	prevFlags := log.Flags()
	log.SetOutput(slogx.CaptureWriter(buf, 0))
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prev)
		log.SetFlags(prevFlags)
	})

	log.Printf("[-] [INIT] [Metrics enabled at /metrics]")
	var obj map[string]any
	assert.NoError(t, json.Unmarshal(buf.Bytes(), &obj))
	assert.Equal(t, "INIT", obj["event"])
	_, hasReqID := obj["request_id"]
	assert.False(t, hasReqID, "request_id must be absent for the '-' sentinel")
}
