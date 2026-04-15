// Modified by [Aleksander R], 2026: added Playwright protocol support

package main

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	gwebsocket "github.com/gorilla/websocket"
	"golang.org/x/net/http/httpguts"
)

const upstreamHandshakeTimeout = 15 * time.Second

// websocketUpgrader is the shared upgrader used by the generic WS reverse
// proxy (devtools, etc.). CheckOrigin delegates to the package-level
// originChecker built in main.init from -allowed-origins; without an
// allow-list this preserves the legacy permissive behavior but logs a
// startup warning so operators notice.
var websocketUpgrader = gwebsocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return app.originChecker.Check(r)
	},
}

func isDevtoolsWebSocketRequest(r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, paths.Devtools) {
		return false
	}
	return httpguts.HeaderValuesContainsToken(r.Header.Values("Connection"), "upgrade") &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func proxyWebSocket(
	w http.ResponseWriter,
	r *http.Request,
	upstreamURL *url.URL,
	onConnect func(),
	onTraffic func(),
) error {
	clientConn, err := websocketUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return err
	}

	dialer := *gwebsocket.DefaultDialer
	dialer.HandshakeTimeout = upstreamHandshakeTimeout
	upstreamConn, _, err := dialer.DialContext(r.Context(), upstreamURL.String(), nil)
	if err != nil {
		_ = clientConn.Close()
		return err
	}
	if onConnect != nil {
		onConnect()
	}

	var closeOnce sync.Once
	closeConnections := func() {
		closeOnce.Do(func() {
			_ = upstreamConn.Close()
			_ = clientConn.Close()
		})
	}

	errCh := make(chan error, 2)
	go proxyWebSocketPump(errCh, clientConn, upstreamConn, onTraffic)
	go proxyWebSocketPump(errCh, upstreamConn, clientConn, onTraffic)

	err = <-errCh
	closeConnections()
	<-errCh
	return err
}

func proxyWebSocketPump(errCh chan<- error, src *gwebsocket.Conn, dst *gwebsocket.Conn, onTraffic func()) {
	for {
		messageType, payload, err := src.ReadMessage()
		if err != nil {
			errCh <- err
			return
		}
		if err := dst.WriteMessage(messageType, payload); err != nil {
			errCh <- err
			return
		}
		if onTraffic != nil {
			onTraffic()
		}
	}
}
