package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aqa-alex/selenwright/service"
	"github.com/aqa-alex/selenwright/session"
	gwebsocket "github.com/gorilla/websocket"
	assert "github.com/stretchr/testify/require"
)

func TestPlaywrightSessionTimeout(t *testing.T) {
	upstream := newMockPlaywrightServer(t, mockPlaywrightOptions{})
	cancelCh := stubPlaywrightManager(t, upstream)

	conn := connectPlaywrightClient(t, "/playwright/firefox/1.49.1")
	defer conn.Close()

	waitForChannelReceive(t, upstream.connected, time.Second)
	sessionID, sess := waitForPlaywrightSession(t)
	assert.Equal(t, session.ProtocolPlaywright, sess.Protocol)
	assert.Equal(t, "firefox", sess.Caps.Name)
	assert.Equal(t, "1.49.1", sess.Caps.Version)

	waitForPlaywrightSessionCleanup(t, sessionID)
	waitForChannelReceive(t, cancelCh, time.Second)
	assert.Equal(t, 0, app.queue.Used())
}

func TestPlaywrightSessionTrafficKeepsItAlive(t *testing.T) {
	upstream := newMockPlaywrightServer(t, mockPlaywrightOptions{Echo: true})
	stubPlaywrightManager(t, upstream)

	conn := connectPlaywrightClient(t, "/playwright/firefox/1.49.1")
	defer conn.Close()

	waitForChannelReceive(t, upstream.connected, time.Second)
	sessionID, _ := waitForPlaywrightSession(t)

	for i := 0; i < 3; i++ {
		payload := []byte(fmt.Sprintf("ping-%d", i))
		assert.NoError(t, conn.WriteMessage(gwebsocket.TextMessage, payload))
		assert.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))

		messageType, echoedPayload, err := conn.ReadMessage()
		assert.NoError(t, err)
		assert.Equal(t, gwebsocket.TextMessage, messageType)
		assert.Equal(t, payload, echoedPayload)

		time.Sleep(40 * time.Millisecond)
		_, ok := app.sessions.Get(sessionID)
		assert.True(t, ok)
	}

	time.Sleep(40 * time.Millisecond)
	_, ok := app.sessions.Get(sessionID)
	assert.True(t, ok)

	waitForPlaywrightSessionCleanup(t, sessionID)
	assert.Equal(t, 0, app.queue.Used())
}

func TestPlaywrightClientDisconnectCleansUpSession(t *testing.T) {
	upstream := newMockPlaywrightServer(t, mockPlaywrightOptions{})
	cancelCh := stubPlaywrightManager(t, upstream)

	conn := connectPlaywrightClient(t, "/playwright/chromium/1.49.1")

	waitForChannelReceive(t, upstream.connected, time.Second)
	sessionID, _ := waitForPlaywrightSession(t)

	assert.NoError(t, conn.Close())

	waitForPlaywrightSessionCleanup(t, sessionID)
	waitForChannelReceive(t, cancelCh, time.Second)
	waitForChannelReceive(t, upstream.disconnected, time.Second)
	assert.Equal(t, 0, app.queue.Used())
}

func TestPlaywrightUpstreamDisconnectCleansUpSession(t *testing.T) {
	upstream := newMockPlaywrightServer(t, mockPlaywrightOptions{})
	cancelCh := stubPlaywrightManager(t, upstream)

	conn := connectPlaywrightClient(t, "/playwright/webkit/1.49.1")
	defer conn.Close()

	waitForChannelReceive(t, upstream.connected, time.Second)
	sessionID, _ := waitForPlaywrightSession(t)

	upstream.Disconnect()

	assert.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
	_, _, err := conn.ReadMessage()
	assert.Error(t, err)

	waitForPlaywrightSessionCleanup(t, sessionID)
	waitForChannelReceive(t, cancelCh, time.Second)
	assert.Equal(t, 0, app.queue.Used())
}

func stubPlaywrightManager(t *testing.T, upstream *mockPlaywrightServer) <-chan struct{} {
	t.Helper()

	previousManager := app.manager
	previousTimeout := app.timeout
	cancelCh := make(chan struct{}, 1)

	app.timeout = 100 * time.Millisecond
	app.manager = &StaticService{
		Available: true,
		StartedService: service.StartedService{
			PlaywrightURL: upstream.URL(),
			Cancel: func() {
				select {
				case cancelCh <- struct{}{}:
				default:
				}
			},
		},
	}

	t.Cleanup(func() {
		cancelPlaywrightSessions()
		assert.Eventually(t, func() bool {
			_, _, ok := currentPlaywrightSession()
			return !ok
		}, time.Second, 10*time.Millisecond)
		app.manager = previousManager
		app.timeout = previousTimeout
	})

	return cancelCh
}

func connectPlaywrightClient(t *testing.T, requestPath string) *gwebsocket.Conn {
	t.Helper()

	u := fmt.Sprintf("ws://%s%s", srv.Listener.Addr().String(), requestPath)
	conn, _, err := gwebsocket.DefaultDialer.Dial(u, nil)
	assert.NoError(t, err)
	return conn
}

func waitForPlaywrightSession(t *testing.T) (string, *session.Session) {
	t.Helper()

	var sessionID string
	var sess *session.Session
	assert.Eventually(t, func() bool {
		var ok bool
		sessionID, sess, ok = currentPlaywrightSession()
		return ok
	}, time.Second, 10*time.Millisecond)
	return sessionID, sess
}

func waitForPlaywrightSessionCleanup(t *testing.T, sessionID string) {
	t.Helper()

	assert.Eventually(t, func() bool {
		_, ok := app.sessions.Get(sessionID)
		return !ok
	}, 2*time.Second, 10*time.Millisecond)
}

func waitForChannelReceive(t *testing.T, ch <-chan struct{}, timeout time.Duration) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(app.timeout):
		t.Fatalf("timed out waiting for channel receive")
	}
}

func currentPlaywrightSession() (string, *session.Session, bool) {
	var sessionID string
	var sess *session.Session
	app.sessions.Each(func(id string, current *session.Session) {
		if sess != nil || current == nil || current.Protocol != session.ProtocolPlaywright {
			return
		}
		sessionID = id
		sess = current
	})
	return sessionID, sess, sess != nil
}

func cancelPlaywrightSessions() {
	var active []*session.Session
	app.sessions.Each(func(_ string, current *session.Session) {
		if current == nil || current.Protocol != session.ProtocolPlaywright || current.Cancel == nil {
			return
		}
		active = append(active, current)
	})
	for _, sess := range active {
		sess.Cancel()
	}
}

func TestPlaywrightProtocolLogging(t *testing.T) {
	upstream := newMockPlaywrightServer(t, mockPlaywrightOptions{Echo: true})
	cancelCh := stubPlaywrightManager(t, upstream)

	artifactHistoryMu.Lock()
	previousLogOutputDir := app.logOutputDir
	app.logOutputDir = t.TempDir()
	app.saveAllLogs = true
	artifactHistoryMu.Unlock()
	t.Cleanup(func() {
		artifactHistoryMu.Lock()
		app.logOutputDir = previousLogOutputDir
		artifactHistoryMu.Unlock()
	})

	conn := connectPlaywrightClient(t, "/playwright/chromium/1.49.1")
	waitForChannelReceive(t, upstream.connected, time.Second)
	sessionID, sess := waitForPlaywrightSession(t)
	assert.NotNil(t, sess.LogSink)

	payload := []byte(`{"id":5,"guid":"page-1","method":"Page.goto","params":{"url":"https://example.com"}}`)
	assert.NoError(t, conn.WriteMessage(gwebsocket.TextMessage, payload))
	assert.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
	_, _, err := conn.ReadMessage()
	assert.NoError(t, err)

	assert.NoError(t, conn.Close())
	waitForPlaywrightSessionCleanup(t, sessionID)
	waitForChannelReceive(t, cancelCh, time.Second)

	logFiles, _ := filepath.Glob(filepath.Join(app.logOutputDir, "*.log"))
	assert.NotEmpty(t, logFiles)

	logContent, err := os.ReadFile(logFiles[0])
	assert.NoError(t, err)

	content := string(logContent)
	assert.Contains(t, content, "--- playwright protocol activity ---")
	assert.Contains(t, content, "Page.goto")
	assert.Contains(t, content, "[id=5]")
	assert.Contains(t, content, "[page-1]")
	assert.Contains(t, content, "→")
}

func TestPlaywrightProtocolLoggingBinaryFrame(t *testing.T) {
	upstream := newMockPlaywrightServer(t, mockPlaywrightOptions{Echo: true})
	cancelCh := stubPlaywrightManager(t, upstream)

	artifactHistoryMu.Lock()
	previousLogOutputDir := app.logOutputDir
	app.logOutputDir = t.TempDir()
	app.saveAllLogs = true
	artifactHistoryMu.Unlock()
	t.Cleanup(func() {
		artifactHistoryMu.Lock()
		app.logOutputDir = previousLogOutputDir
		artifactHistoryMu.Unlock()
	})

	conn := connectPlaywrightClient(t, "/playwright/chromium/1.49.1")
	waitForChannelReceive(t, upstream.connected, time.Second)
	sessionID, _ := waitForPlaywrightSession(t)

	binaryPayload := make([]byte, 256)
	for i := range binaryPayload {
		binaryPayload[i] = byte(i)
	}
	assert.NoError(t, conn.WriteMessage(gwebsocket.BinaryMessage, binaryPayload))
	assert.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
	_, _, err := conn.ReadMessage()
	assert.NoError(t, err)

	assert.NoError(t, conn.Close())
	waitForPlaywrightSessionCleanup(t, sessionID)
	waitForChannelReceive(t, cancelCh, time.Second)

	logFiles, _ := filepath.Glob(filepath.Join(app.logOutputDir, "*.log"))
	assert.NotEmpty(t, logFiles)

	logContent, err := os.ReadFile(logFiles[0])
	assert.NoError(t, err)

	content := string(logContent)
	assert.Contains(t, content, "⊞ binary 256 bytes")
}

func TestPlaywrightProtocolLoggingErrorFrame(t *testing.T) {
	upstream := newMockPlaywrightServer(t, mockPlaywrightOptions{Echo: true})
	cancelCh := stubPlaywrightManager(t, upstream)

	conn := connectPlaywrightClient(t, "/playwright/chromium/1.49.1")
	waitForChannelReceive(t, upstream.connected, time.Second)
	sessionID, sess := waitForPlaywrightSession(t)

	payload := []byte(`{"id":6,"error":{"name":"TimeoutError","message":"page closed unexpectedly"}}`)
	assert.NoError(t, conn.WriteMessage(gwebsocket.TextMessage, payload))
	assert.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
	_, _, err := conn.ReadMessage()
	assert.NoError(t, err)

	assert.NoError(t, conn.Close())
	waitForPlaywrightSessionCleanup(t, sessionID)
	waitForChannelReceive(t, cancelCh, time.Second)

	content := sess.LogSink.Content()
	assert.Contains(t, content, "✕")
	assert.Contains(t, content, "[id=6]")
	assert.Contains(t, content, "TimeoutError")
	assert.Contains(t, content, "page closed unexpectedly")
}

func TestPlaywrightLogSinkStreamsInRealTime(t *testing.T) {
	upstream := newMockPlaywrightServer(t, mockPlaywrightOptions{Echo: true})
	stubPlaywrightManager(t, upstream)

	conn := connectPlaywrightClient(t, "/playwright/chromium/1.49.1")
	waitForChannelReceive(t, upstream.connected, time.Second)
	_, sess := waitForPlaywrightSession(t)

	payload := []byte(`{"id":1,"guid":"Playwright","method":"BrowserType.connect"}`)
	assert.NoError(t, conn.WriteMessage(gwebsocket.TextMessage, payload))
	assert.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
	_, _, err := conn.ReadMessage()
	assert.NoError(t, err)

	assert.Eventually(t, func() bool {
		return strings.Contains(sess.LogSink.Content(), "BrowserType.connect")
	}, time.Second, 10*time.Millisecond, "LogSink should contain protocol line before session ends")

	conn.Close()
}

func TestResolvePlaywrightSessionID_UsesExternalHeader(t *testing.T) {
	const externalID = "pw_router-supplied-id-01"
	t.Cleanup(func() { app.sessions.Remove(externalID) })

	r := httptest.NewRequest(http.MethodGet, "/playwright/chrome/stable", nil)
	r.Header.Set(externalPlaywrightSessionIDHeader, externalID)

	got, err := resolvePlaywrightSessionID(r)
	assert.NoError(t, err)
	assert.Equal(t, externalID, got)
}

func TestResolvePlaywrightSessionID_FallsBackWhenHeaderAbsent(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/playwright/chrome/stable", nil)

	got, err := resolvePlaywrightSessionID(r)
	assert.NoError(t, err)
	t.Cleanup(func() { app.sessions.Remove(got) })

	assert.Regexp(t, `^[0-9a-f]{32}$`, got,
		"fallback must produce the auto-generated 32-hex ID")
}

func TestResolvePlaywrightSessionID_FallsBackWhenHeaderBlank(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/playwright/chrome/stable", nil)
	r.Header.Set(externalPlaywrightSessionIDHeader, "   ")

	got, err := resolvePlaywrightSessionID(r)
	assert.NoError(t, err)
	t.Cleanup(func() { app.sessions.Remove(got) })

	assert.Regexp(t, `^[0-9a-f]{32}$`, got,
		"whitespace-only header must trigger the random fallback")
}

func TestResolvePlaywrightSessionID_RejectsInvalidChars(t *testing.T) {
	cases := []string{
		"pw_../etc/passwd",
		"pw id with spaces",
		"pw/slash",
		"pw:colon",
		"",
	}
	for _, value := range cases {
		value := value
		t.Run(value, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/playwright/chrome/stable", nil)
			if value != "" {
				r.Header.Set(externalPlaywrightSessionIDHeader, value)
			}
			_, err := resolvePlaywrightSessionID(r)
			if value == "" {
				assert.NoError(t, err, "empty string header should fall back, not error")
				return
			}
			assert.ErrorIs(t, err, errInvalidExternalSessionID)
		})
	}
}

func TestResolvePlaywrightSessionID_RejectsTooLong(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/playwright/chrome/stable", nil)
	r.Header.Set(externalPlaywrightSessionIDHeader, strings.Repeat("a", 129))

	_, err := resolvePlaywrightSessionID(r)
	assert.ErrorIs(t, err, errInvalidExternalSessionID)
}

func TestResolvePlaywrightSessionID_RejectsCollision(t *testing.T) {
	const externalID = "pw_existing-session"
	app.sessions.Put(externalID, &session.Session{Quota: "alice"})
	t.Cleanup(func() { app.sessions.Remove(externalID) })

	r := httptest.NewRequest(http.MethodGet, "/playwright/chrome/stable", nil)
	r.Header.Set(externalPlaywrightSessionIDHeader, externalID)

	_, err := resolvePlaywrightSessionID(r)
	assert.ErrorIs(t, err, errExternalSessionIDCollision)
}

func TestResolvePlaywrightSessionID_SentinelErrorsAreDistinct(t *testing.T) {
	assert.False(t, errors.Is(errInvalidExternalSessionID, errExternalSessionIDCollision))
	assert.False(t, errors.Is(errExternalSessionIDCollision, errInvalidExternalSessionID))
}
