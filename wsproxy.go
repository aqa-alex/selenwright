// Modified by [Aleksander R], 2026: added Playwright protocol support

package main

import (
	"net/http"
	"net/url"
	"strings"
	"sync"

	gwebsocket "github.com/gorilla/websocket"
)

var websocketUpgrader = gwebsocket.Upgrader{
	CheckOrigin: func(_ *http.Request) bool {
		return true
	},
}

func isDevtoolsWebSocketRequest(r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, paths.Devtools) {
		return false
	}
	return headerContainsToken(r.Header, "Connection", "upgrade") &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func headerContainsToken(header http.Header, key string, token string) bool {
	for _, value := range header.Values(key) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
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

	upstreamConn, _, err := gwebsocket.DefaultDialer.DialContext(r.Context(), upstreamURL.String(), nil)
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
