// Modified by [Aleksander R], 2026: added Playwright protocol support

package main

import (
	"fmt"
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
	assert.Equal(t, 0, queue.Used())
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
		_, ok := sessions.Get(sessionID)
		assert.True(t, ok)
	}

	time.Sleep(40 * time.Millisecond)
	_, ok := sessions.Get(sessionID)
	assert.True(t, ok)

	waitForPlaywrightSessionCleanup(t, sessionID)
	assert.Equal(t, 0, queue.Used())
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
	assert.Equal(t, 0, queue.Used())
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
	assert.Equal(t, 0, queue.Used())
}

func stubPlaywrightManager(t *testing.T, upstream *mockPlaywrightServer) <-chan struct{} {
	t.Helper()

	previousManager := manager
	previousTimeout := timeout
	cancelCh := make(chan struct{}, 1)

	timeout = 100 * time.Millisecond
	manager = &StaticService{
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
		manager = previousManager
		timeout = previousTimeout
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
		_, ok := sessions.Get(sessionID)
		return !ok
	}, 2*time.Second, 10*time.Millisecond)
}

func waitForChannelReceive(t *testing.T, ch <-chan struct{}, timeout time.Duration) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for channel receive")
	}
}

func currentPlaywrightSession() (string, *session.Session, bool) {
	var sessionID string
	var sess *session.Session
	sessions.Each(func(id string, current *session.Session) {
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
	sessions.Each(func(_ string, current *session.Session) {
		if current == nil || current.Protocol != session.ProtocolPlaywright || current.Cancel == nil {
			return
		}
		active = append(active, current)
	})
	for _, sess := range active {
		sess.Cancel()
	}
}
