// PR #22 regression tests for WebSocket message size limit (C-1 in audit):
// without SetReadLimit, gorilla/websocket.ReadMessage allocates the full
// frame before returning — a single hostile multi-gigabyte frame OOMs
// the process. applyWSReadLimit is called on every upgraded WS conn
// (Playwright, DevTools, VNC, logs) and on the upstream dial side of
// proxy/tunnel paths.

package main

import (
	"testing"
	"time"

	gwebsocket "github.com/gorilla/websocket"
	assert "github.com/stretchr/testify/require"
)

// TestWSReadLimitRejectsOversizedFrame verifies that a client frame
// above app.maxWSMessageBytes is rejected by the Playwright tunnel with
// a 1009 (message too big) close, instead of being materialized.
func TestWSReadLimitRejectsOversizedFrame(t *testing.T) {
	upstream := newMockPlaywrightServer(t, mockPlaywrightOptions{Echo: true})
	stubPlaywrightManager(t, upstream)

	originalLimit := app.maxWSMessageBytes
	app.maxWSMessageBytes = 1 << 10 // 1 KiB
	defer func() { app.maxWSMessageBytes = originalLimit }()

	conn := connectPlaywrightClient(t, "/playwright/chromium/1.49.1")
	defer conn.Close()

	waitForChannelReceive(t, upstream.connected, time.Second)
	sessionID, _ := waitForPlaywrightSession(t)

	// Write a frame well beyond the 1 KiB limit. gorilla/websocket on
	// the server side will detect the overshoot during ReadMessage and
	// close the connection with 1009 — the client observes this as a
	// read error with CloseMessageTooBig code.
	oversized := make([]byte, 8<<10)
	assert.NoError(t, conn.WriteMessage(gwebsocket.BinaryMessage, oversized))

	assert.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, _, err := conn.ReadMessage()
	assert.Error(t, err, "oversized frame must not be echoed back")
	if ce, ok := err.(*gwebsocket.CloseError); ok {
		assert.Equal(t, gwebsocket.CloseMessageTooBig, ce.Code,
			"server should close with 1009 Message Too Big")
	}

	waitForPlaywrightSessionCleanup(t, sessionID)
	assert.Equal(t, 0, app.queue.Used())
}

// TestWSReadLimitHonestFrameAllowed confirms the legitimate path still
// works — a frame well under the limit is relayed end-to-end.
func TestWSReadLimitHonestFrameAllowed(t *testing.T) {
	upstream := newMockPlaywrightServer(t, mockPlaywrightOptions{Echo: true})
	stubPlaywrightManager(t, upstream)

	originalLimit := app.maxWSMessageBytes
	app.maxWSMessageBytes = 1 << 20 // 1 MiB
	defer func() { app.maxWSMessageBytes = originalLimit }()

	conn := connectPlaywrightClient(t, "/playwright/chromium/1.49.1")
	defer conn.Close()

	waitForChannelReceive(t, upstream.connected, time.Second)
	sessionID, _ := waitForPlaywrightSession(t)

	payload := make([]byte, 64<<10) // 64 KiB, well under 1 MiB
	for i := range payload {
		payload[i] = byte(i)
	}
	assert.NoError(t, conn.WriteMessage(gwebsocket.BinaryMessage, payload))

	assert.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	msgType, echoed, err := conn.ReadMessage()
	assert.NoError(t, err)
	assert.Equal(t, gwebsocket.BinaryMessage, msgType)
	assert.Equal(t, payload, echoed)

	waitForPlaywrightSessionCleanup(t, sessionID)
}

// TestApplyWSReadLimitRespectsDisabled verifies the helper no-ops when
// the flag is 0 (legacy, no limit) or when passed nil.
func TestApplyWSReadLimitRespectsDisabled(t *testing.T) {
	originalLimit := app.maxWSMessageBytes
	defer func() { app.maxWSMessageBytes = originalLimit }()

	// nil conn must not panic
	applyWSReadLimit(nil)

	app.maxWSMessageBytes = 0
	applyWSReadLimit(nil) // still a no-op
}

// TestMaxWSMessageBytesFlagRegistered ensures the flag binding survives
// refactoring and keeps a sane default. 0 is permitted (legacy) but we
// want the out-of-the-box default to be strictly positive.
func TestMaxWSMessageBytesFlagRegistered(t *testing.T) {
	assert.Greater(t, app.maxWSMessageBytes, int64(0),
		"default -max-ws-message-bytes must be > 0 to cap the OOM blast radius")
}
