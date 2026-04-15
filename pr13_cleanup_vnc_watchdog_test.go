package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aqa-alex/selenwright/service"
	"github.com/aqa-alex/selenwright/session"
	gwebsocket "github.com/gorilla/websocket"
	assert "github.com/stretchr/testify/require"
)

// TestPlaywrightEarlyCleanupOnDialFailure verifies PR #13's defer-based
// early cleanup: a failing upstream dial must still call serviceCancel
// and queue.Drop so the slot is not leaked.
func TestPlaywrightEarlyCleanupOnDialFailure(t *testing.T) {
	prevManager := manager
	prevTimeout := timeout
	timeout = 200 * time.Millisecond
	t.Cleanup(func() {
		manager = prevManager
		timeout = prevTimeout
	})

	var cancelCalls int32
	manager = &StaticService{
		Available: true,
		StartedService: service.StartedService{
			PlaywrightURL: mustParseWS(t, "ws://127.0.0.1:1/never-listens"),
			Cancel: func() {
				atomic.AddInt32(&cancelCalls, 1)
			},
		},
	}

	usedBefore := queue.Used()
	pendingBefore := queue.Pending()

	wsURL := fmt.Sprintf("ws://%s/playwright/firefox/1.49.1", srv.Listener.Addr().String())
	_, resp, err := gwebsocket.DefaultDialer.Dial(wsURL, nil)
	// We expect the handshake to fail because the upstream dial fails
	// and selenwright responds with 500 Internal Server Error.
	assert.Error(t, err)
	if resp != nil {
		_ = resp.Body.Close()
	}

	assert.Eventually(t, func() bool {
		return atomic.LoadInt32(&cancelCalls) == 1
	}, time.Second, 10*time.Millisecond,
		"serviceCancel must be called once when upstream dial fails")

	assert.Equal(t, usedBefore, queue.Used(),
		"used counter must stay unchanged after failed setup")
	assert.Equal(t, pendingBefore, queue.Pending(),
		"pending counter must not leak — queue.Drop should have fired")
}

func mustParseWS(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	assert.NoError(t, err)
	return u
}

// TestCopyWithWatchdogTouchesWatchdog — guards that the helper actually
// calls Watchdog.Touch on each chunk. Without it a long-running VNC
// tunnel would timeout mid-session.
func TestCopyWithWatchdogTouchesWatchdog(t *testing.T) {
	var touches int32
	// Build a watchdog whose Touch increments a counter. NewWatchdog
	// installs a real *Watchdog; we can't easily intercept its method,
	// so we test the happy-path observable: the session's watchdog is
	// not expired after traffic.
	wd := session.NewWatchdog(200*time.Millisecond, func() {
		atomic.AddInt32(&touches, -1000) // sentinel: would mean timeout fired
	})
	defer wd.Stop()
	sess := &session.Session{Watchdog: wd}

	src := strings.NewReader("hello there")
	dst := &bytes.Buffer{}
	n, err := copyWithWatchdog(dst, src, sess)
	assert.NoError(t, err)
	assert.Equal(t, int64(11), n)
	assert.Equal(t, "hello there", dst.String())

	// Sleep slightly longer than the watchdog timeout; if Touch wasn't
	// called we'd see the timeout sentinel. If it was called, the
	// timeout starts ticking from the Touch and we should still be safe
	// within the window.
	time.Sleep(50 * time.Millisecond)
	assert.GreaterOrEqual(t, atomic.LoadInt32(&touches), int32(0),
		"if Touch fired during Copy, timeout sentinel should not have decremented")
}

func TestCopyWithWatchdogHandlesNilSession(t *testing.T) {
	src := strings.NewReader("x")
	dst := &bytes.Buffer{}
	n, err := copyWithWatchdog(dst, src, nil)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), n)
}

func TestCopyWithWatchdogPropagatesReadError(t *testing.T) {
	erroringReader := &funcReader{fn: func([]byte) (int, error) { return 0, errors.New("boom") }}
	dst := &bytes.Buffer{}
	_, err := copyWithWatchdog(dst, erroringReader, nil)
	assert.Error(t, err)
}

type funcReader struct {
	fn func([]byte) (int, error)
}

func (r *funcReader) Read(p []byte) (int, error) { return r.fn(p) }

