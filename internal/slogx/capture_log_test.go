package slogx

import (
	"bytes"
	"encoding/json"
	"log"
	"testing"

	assert "github.com/stretchr/testify/require"
)

func TestLogJSONRoutesBracketedOutput(t *testing.T) {
	buf := &bytes.Buffer{}

	prev := log.Default().Writer()
	prevFlags := log.Flags()
	log.SetOutput(CaptureWriter(buf, 0))
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

func TestLogJSONSentinelRequestId(t *testing.T) {
	buf := &bytes.Buffer{}
	prev := log.Default().Writer()
	prevFlags := log.Flags()
	log.SetOutput(CaptureWriter(buf, 0))
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
