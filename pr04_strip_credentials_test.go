// Modified by [Aleksander R], 2026: PR #4 — strip Authorization/Cookie + HandshakeTimeout

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/aqa-alex/selenwright/service"
	gwebsocket "github.com/gorilla/websocket"
	assert "github.com/stretchr/testify/require"
)

func TestPlaywrightDoesNotForwardAuthorizationOrCookie(t *testing.T) {
	upstream := newCapturingUpstream(t)
	prevManager := app.manager
	prevTimeout := app.timeout
	app.timeout = 200 * time.Millisecond
	app.manager = &StaticService{
		Available: true,
		StartedService: service.StartedService{
			PlaywrightURL: upstream.url,
			Cancel:        func() {},
		},
	}
	t.Cleanup(func() {
		cancelPlaywrightSessions()
		app.manager = prevManager
		app.timeout = prevTimeout
	})

	hdr := http.Header{}
	hdr.Set("Origin", "http://localhost")
	hdr.Set("Authorization", "Bearer secret-token")
	hdr.Set("Cookie", "session=stolen")
	wsURL := fmt.Sprintf("ws://%s/playwright/firefox/1.49.1", srv.Listener.Addr().String())

	conn, _, err := gwebsocket.DefaultDialer.Dial(wsURL, hdr)
	assert.NoError(t, err)
	defer conn.Close()

	select {
	case <-upstream.connected:
	case <-time.After(time.Second):
		t.Fatalf("upstream did not receive the proxied connection")
	}

	upstream.mu.Lock()
	got := upstream.lastHeader.Clone()
	upstream.mu.Unlock()

	assert.Empty(t, got.Get("Authorization"),
		"selenwright must not forward client Authorization to the browser container")
	assert.Empty(t, got.Get("Cookie"),
		"selenwright must not forward client Cookie to the browser container")
	assert.Equal(t, "http://localhost", got.Get("Origin"),
		"Origin is still forwarded so upstream can route correctly")
}

func TestUpstreamHandshakeTimeoutsAreSet(t *testing.T) {
	assert.Greater(t, int64(playwrightUpstreamHandshakeTimeout), int64(0),
		"playwright dialer must bound handshake duration")
	assert.Greater(t, int64(upstreamHandshakeTimeout), int64(0),
		"wsproxy dialer must bound handshake duration")
}

type capturingUpstream struct {
	server     *httptest.Server
	url        *url.URL
	connected  chan struct{}
	mu         sync.Mutex
	lastHeader http.Header
}

func newCapturingUpstream(t *testing.T) *capturingUpstream {
	t.Helper()

	up := &capturingUpstream{connected: make(chan struct{}, 1)}
	upgrader := gwebsocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	up.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up.mu.Lock()
		up.lastHeader = r.Header.Clone()
		up.mu.Unlock()

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		select {
		case up.connected <- struct{}{}:
		default:
		}

		_, _, _ = conn.ReadMessage()
	}))
	t.Cleanup(up.server.Close)

	parsed, err := url.Parse(up.server.URL)
	assert.NoError(t, err)
	parsed.Scheme = "ws"
	up.url = parsed
	return up
}
