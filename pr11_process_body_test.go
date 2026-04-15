package main

import (
	"testing"

	assert "github.com/stretchr/testify/require"
)

// TestProcessBody_DoesNotPanicOnMalformedJSON exercises every code path
// in processBody that the old implementation reached with an unchecked
// type assertion. A malicious or broken browser container could return
// any of these shapes; none must crash the selenwright process.
func TestProcessBody_DoesNotPanicOnMalformedJSON(t *testing.T) {
	cases := map[string]string{
		"session-id-not-string":       `{"sessionId": 42}`,
		"value-not-object":            `{"value": 42}`,
		"capabilities-not-object":     `{"value": {"capabilities": 42}}`,
		"session-id-in-value-not-str": `{"value": {"sessionId": 42, "capabilities": {}}}`,
		"session-id-in-value-null":    `{"value": {"sessionId": null, "capabilities": {}}}`,
		"browserVersion-not-string":   `{"value": {"sessionId": "abc", "capabilities": {"browserVersion": 42}}}`,
		"capabilities-nested-garbage": `{"value": {"sessionId": "abc", "capabilities": [1,2,3]}}`,
		"entirely-empty-object":       `{}`,
		"only-null-value":             `{"value": null}`,
		"array-at-root":               `[1,2,3]`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			require := func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("processBody panicked on %s: %v", name, r)
					}
				}()
				_, _, _ = processBody([]byte(raw), "host")
			}
			require()
		})
	}
}

// TestProcessBody_HappyPathW3C — regression for the legitimate case, so
// we know the refactor did not break CDP injection.
func TestProcessBody_HappyPathW3C(t *testing.T) {
	in := `{"value":{"sessionId":"abc123","capabilities":{"browserVersion":"120"}}}`
	out, sid, err := processBody([]byte(in), "selenwright.local")
	assert.NoError(t, err)
	assert.Equal(t, "abc123", sid)
	assert.Contains(t, string(out), `"se:cdp":"ws://selenwright.local/devtools/abc123/"`)
	assert.Contains(t, string(out), `"se:cdpVersion":"120"`)
}

func TestProcessBody_HappyPathLegacy(t *testing.T) {
	in := `{"sessionId":"abc123","status":0}`
	out, sid, err := processBody([]byte(in), "selenwright.local")
	assert.NoError(t, err)
	assert.Equal(t, "abc123", sid)
	assert.Contains(t, string(out), `"sessionId":"abc123"`)
}

func TestProcessBody_InvalidJSON(t *testing.T) {
	_, _, err := processBody([]byte(`not json`), "host")
	assert.Error(t, err)
}

func FuzzProcessBody(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"sessionId":"abc"}`))
	f.Add([]byte(`{"value":{"sessionId":"abc","capabilities":{}}}`))
	f.Add([]byte(`{"value":{"sessionId":42,"capabilities":{}}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic: %v\ninput: %q", r, data)
			}
		}()
		_, _, _ = processBody(data, "host")
	})
}
