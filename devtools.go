// Modified by [Aleksander R], 2026: added Playwright protocol support

package main

import (
	"log"
	"net/http"
	"net/url"

	"github.com/aqa-alex/selenwright/session"
)

func handleDevtoolsWebSocket(w http.ResponseWriter, r *http.Request, requestId uint64, sid string, remainingPath string, sess *session.Session) {
	devtoolsHostPort := sess.HostPort.Devtools
	if devtoolsHostPort == "" {
		log.Printf("[%d] [DEVTOOLS_DISABLED] [%s]", requestId, sid)
		http.Error(w, "DevTools are not available for this session", http.StatusBadGateway)
		return
	}

	upstreamURL := &url.URL{
		Scheme:   "ws",
		Host:     devtoolsHostPort,
		Path:     remainingPath,
		RawQuery: r.URL.RawQuery,
	}

	log.Printf("[%d] [DEVTOOLS] [%s] [%s]", requestId, sid, remainingPath)

	err := proxyWebSocket(w, r, upstreamURL, func() {
		touchWatchdog(sess)
	}, func() {
		touchWatchdog(sess)
	})
	if err == nil {
		log.Printf("[%d] [DEVTOOLS_SESSION_CLOSED] [%s]", requestId, sid)
		return
	}

	log.Printf("[%d] [DEVTOOLS_CLIENT_DISCONNECTED] [%s] [%v]", requestId, sid, err)
}
