package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aqa-alex/selenwright/protect"
	"github.com/aqa-alex/selenwright/service"
	gwebsocket "github.com/gorilla/websocket"
	assert "github.com/stretchr/testify/require"
)

func withStrictOrigin(t *testing.T, allowed ...string) {
	t.Helper()
	prev := app.originChecker
	c, err := protect.NewOriginChecker(allowed)
	assert.NoError(t, err)
	app.originChecker = c
	t.Cleanup(func() { app.originChecker = prev })
}

func upgradeRequest(t *testing.T, target, origin string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	assert.NoError(t, err)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	assert.NoError(t, err)
	return resp
}

func TestVNCRejectsForeignOrigin(t *testing.T) {
	withStrictOrigin(t, "https://ci.example.com")
	resp := upgradeRequest(t, srv.URL+paths.VNC+"some-session-id", "https://evil.example.com")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"foreign Origin must be rejected before VNC handshake")
}

func TestVNCAllowsConfiguredOrigin(t *testing.T) {
	withStrictOrigin(t, "https://ci.example.com")
	resp := upgradeRequest(t, srv.URL+paths.VNC+"unknown-session", "https://ci.example.com")
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusForbidden, resp.StatusCode,
		"allowed Origin must pass the gate")
}

func TestLogsRejectsForeignOrigin(t *testing.T) {
	withStrictOrigin(t, "https://ci.example.com")
	resp := upgradeRequest(t, srv.URL+paths.Logs+"some-session-id", "https://evil.example.com")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestVNCAllowsAbsentOrigin(t *testing.T) {
	withStrictOrigin(t, "https://ci.example.com")
	resp := upgradeRequest(t, srv.URL+paths.VNC+"unknown-session", "")
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusForbidden, resp.StatusCode,
		"absent Origin must pass the gate so native clients keep working")
}

func TestPermissiveModeStillAccepts(t *testing.T) {
	withStrictOrigin(t /* no entries -> permissive */)
	assert.True(t, app.originChecker.AllowsAll())
	resp := upgradeRequest(t, srv.URL+paths.VNC+"x", "https://anywhere.example.com")
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusForbidden, resp.StatusCode)
}

func TestSplitCSV(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , ,b ", []string{"a", "b"}},
		{",,,", nil},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.in, ",", "_"), func(t *testing.T) {
			assert.Equal(t, tc.want, splitCSV(tc.in))
		})
	}
}

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

func TestApplyWSReadLimitRespectsDisabled(t *testing.T) {
	originalLimit := app.maxWSMessageBytes
	defer func() { app.maxWSMessageBytes = originalLimit }()

	applyWSReadLimit(nil)

	app.maxWSMessageBytes = 0
	applyWSReadLimit(nil) // still a no-op
}

func TestMaxWSMessageBytesFlagRegistered(t *testing.T) {
	assert.Greater(t, app.maxWSMessageBytes, int64(0),
		"default -max-ws-message-bytes must be > 0 to cap the OOM blast radius")
}
