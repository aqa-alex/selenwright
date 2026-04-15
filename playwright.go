// Modified by [Aleksander R], 2026: added Playwright protocol support

package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aqa-alex/selenwright/event"
	"github.com/aqa-alex/selenwright/info"
	"github.com/aqa-alex/selenwright/jsonerror"
	"github.com/aqa-alex/selenwright/session"
	gwebsocket "github.com/gorilla/websocket"
)

type playwrightTunnelResult struct {
	source string
	err    error
}

func playwright(w http.ResponseWriter, r *http.Request) {
	sessionStartTime := time.Now()
	requestId := serial()
	user, remote := info.RequestInfo(r)

	browserName, playwrightVersion, err := parsePlaywrightPath(r)
	if err != nil {
		log.Printf("[%d] [BAD_PLAYWRIGHT_PATH] [%v]", requestId, err)
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		queue.Drop()
		return
	}
	if !gwebsocket.IsWebSocketUpgrade(r) {
		log.Printf("[%d] [BAD_PLAYWRIGHT_REQUEST] [expected websocket upgrade]", requestId)
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		queue.Drop()
		return
	}

	caps := session.Caps{
		Name:    browserName,
		Version: playwrightVersion,
	}
	if q := r.URL.Query(); q != nil {
		if strings.EqualFold(q.Get("enableVNC"), "true") || q.Get("enableVNC") == "1" {
			caps.VNC = true
		}
		if name := q.Get("name"); name != "" {
			caps.TestName = name
		}
		if screenResolution := q.Get("screenResolution"); screenResolution != "" {
			caps.ScreenResolution = screenResolution
		}
	}
	if logOutputDir != "" && saveAllLogs {
		caps.LogName = getTemporaryFileName(logOutputDir, logFileExtension)
	}

	starter, ok := manager.Find(caps, requestId)
	if !ok {
		log.Printf("[%d] [ENVIRONMENT_NOT_AVAILABLE] [%s] [%s]", requestId, caps.BrowserName(), caps.Version)
		jsonerror.InvalidArgument(errors.New("Requested environment is not available")).Encode(w)
		queue.Drop()
		return
	}

	startedService, err := starter.StartWithCancel()
	if err != nil {
		log.Printf("[%d] [SERVICE_STARTUP_FAILED] [%v]", requestId, err)
		jsonerror.SessionNotCreated(err).Encode(w)
		queue.Drop()
		return
	}

	serviceCancel := startedService.Cancel
	if serviceCancel == nil {
		serviceCancel = func() {}
	}

	if startedService.PlaywrightURL == nil {
		err := errors.New("playwright upstream url is not configured")
		log.Printf("[%d] [SERVICE_STARTUP_FAILED] [%v]", requestId, err)
		jsonerror.SessionNotCreated(err).Encode(w)
		queue.Drop()
		serviceCancel()
		return
	}

	upstreamURL := clonePlaywrightURL(startedService.PlaywrightURL)
	upstreamURL.RawQuery = r.URL.RawQuery

	upstreamConn, err := dialPlaywrightUpstream(r, upstreamURL)
	if err != nil {
		log.Printf("[%d] [PLAYWRIGHT_DIAL_FAILED] [%v]", requestId, err)
		jsonerror.SessionNotCreated(err).Encode(w)
		queue.Drop()
		serviceCancel()
		return
	}

	sessionID, err := newPlaywrightSessionID()
	if err != nil {
		log.Printf("[%d] [PLAYWRIGHT_SESSION_ID_FAILED] [%v]", requestId, err)
		_ = upstreamConn.Close()
		jsonerror.SessionNotCreated(err).Encode(w)
		queue.Drop()
		serviceCancel()
		return
	}

	// Reuse the package-level originChecker built in main.init from
	// -allowed-origins. Defends /playwright/ from Cross-Site WebSocket
	// Hijacking exactly like wsproxy/devtools.
	upgrader := gwebsocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return originChecker.Check(r)
		},
	}
	upgradeHeader := http.Header{}
	if subprotocol := upstreamConn.Subprotocol(); subprotocol != "" {
		upgradeHeader.Set("Sec-WebSocket-Protocol", subprotocol)
	}
	clientConn, err := upgrader.Upgrade(w, r, upgradeHeader)
	if err != nil {
		log.Printf("[%d] [PLAYWRIGHT_UPGRADE_FAILED] [%v]", requestId, err)
		_ = upstreamConn.Close()
		queue.Drop()
		serviceCancel()
		return
	}

	sess := &session.Session{
		Quota:     user,
		Caps:      caps,
		URL:       clonePlaywrightURL(upstreamURL),
		Container: startedService.Container,
		HostPort:  startedService.HostPort,
		Origin:    startedService.Origin,
		Timeout:   timeout,
		Protocol:  session.ProtocolPlaywright,
		Started:   time.Now(),
	}

	var cleanupOnce sync.Once
	cleanup := func(reason string) {
		cleanupOnce.Do(func() {
			if sess.Watchdog != nil {
				_ = sess.Watchdog.Stop()
			}
			_ = clientConn.Close()
			_ = upstreamConn.Close()
			serviceCancel()

			preprocessedID := preprocessSessionId(sessionID)
			playwrightEvent := event.Event{
				RequestId: requestId,
				SessionId: preprocessedID,
				Session:   sess,
			}

			finalizePlaywrightLog(sess, playwrightEvent, preprocessedID)
			sessions.Remove(sessionID)
			queue.Release()
			event.SessionStopped(event.StoppedSession{Event: playwrightEvent})

			switch reason {
			case "timeout":
				log.Printf("[%d] [PLAYWRIGHT_SESSION_TIMED_OUT] [%s]", requestId, sessionID)
			case "shutdown":
				log.Printf("[%d] [PLAYWRIGHT_SESSION_STOPPED] [%s] [server shutdown]", requestId, sessionID)
			}
		})
	}

	sess.Watchdog = session.NewWatchdog(timeout, func() {
		cleanup("timeout")
	})
	sess.Cancel = func() {
		cleanup("shutdown")
	}

	sessions.Put(sessionID, sess)
	queue.Create()
	log.Printf("[%d] [PLAYWRIGHT_SESSION_CREATED] [%s] [%s] [%s] [%.2fs]", requestId, sessionID, browserName, playwrightVersion, info.SecondsSince(sessionStartTime))

	resultCh := make(chan playwrightTunnelResult, 2)
	go tunnelPlaywrightWebSocket(resultCh, "client", clientConn, upstreamConn, sess)
	go tunnelPlaywrightWebSocket(resultCh, "upstream", upstreamConn, clientConn, sess)

	firstResult := <-resultCh
	switch firstResult.source {
	case "client":
		log.Printf("[%d] [PLAYWRIGHT_CLIENT_DISCONNECTED] [%s] [%s] [%s] [Error: %v]", requestId, user, remote, sessionID, firstResult.err)
	case "upstream":
		log.Printf("[%d] [PLAYWRIGHT_UPSTREAM_DISCONNECTED] [%s] [%v]", requestId, sessionID, firstResult.err)
	default:
		log.Printf("[%d] [PLAYWRIGHT_TUNNEL_CLOSED] [%s] [%v]", requestId, sessionID, firstResult.err)
	}

	cleanup(firstResult.source)
	<-resultCh
}

func parsePlaywrightPath(r *http.Request) (string, string, error) {
	encodedPath := r.URL.EscapedPath()
	if !strings.HasPrefix(encodedPath, paths.Playwright) {
		return "", "", fmt.Errorf("unexpected path: %s", encodedPath)
	}

	segments := strings.Split(strings.TrimPrefix(encodedPath, paths.Playwright), "/")
	if len(segments) != 2 || segments[0] == "" || segments[1] == "" {
		return "", "", fmt.Errorf("expected /playwright/<browser>/<playwright-version>, got %s", encodedPath)
	}

	browserName, err := url.PathUnescape(segments[0])
	if err != nil {
		return "", "", fmt.Errorf("decode browser name: %w", err)
	}
	playwrightVersion, err := url.PathUnescape(segments[1])
	if err != nil {
		return "", "", fmt.Errorf("decode playwright version: %w", err)
	}
	if browserName == "" || playwrightVersion == "" {
		return "", "", fmt.Errorf("playwright browser and version must be non-empty")
	}

	return browserName, playwrightVersion, nil
}

func dialPlaywrightUpstream(r *http.Request, upstreamURL *url.URL) (*gwebsocket.Conn, error) {
	dialer := *gwebsocket.DefaultDialer
	dialer.Subprotocols = gwebsocket.Subprotocols(r)

	header := http.Header{}
	copyPlaywrightHeader(header, r.Header, "Origin")
	copyPlaywrightHeader(header, r.Header, "Authorization")
	copyPlaywrightHeader(header, r.Header, "Cookie")

	upstreamConn, resp, err := dialer.DialContext(r.Context(), upstreamURL.String(), header)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, err
	}

	return upstreamConn, nil
}

func copyPlaywrightHeader(dst http.Header, src http.Header, key string) {
	for _, value := range src.Values(key) {
		dst.Add(key, value)
	}
}

func newPlaywrightSessionID() (string, error) {
	for {
		randomBytes := make([]byte, 16)
		if _, err := rand.Read(randomBytes); err != nil {
			return "", fmt.Errorf("generate session id: %w", err)
		}

		sessionID := hex.EncodeToString(randomBytes)
		if _, exists := sessions.Get(sessionID); exists {
			continue
		}
		return sessionID, nil
	}
}

func tunnelPlaywrightWebSocket(resultCh chan<- playwrightTunnelResult, source string, src *gwebsocket.Conn, dst *gwebsocket.Conn, sess *session.Session) {
	for {
		messageType, payload, err := src.ReadMessage()
		if err != nil {
			resultCh <- playwrightTunnelResult{source: source, err: err}
			return
		}
		if err := dst.WriteMessage(messageType, payload); err != nil {
			resultCh <- playwrightTunnelResult{source: source, err: err}
			return
		}
		if sess != nil && sess.Watchdog != nil {
			_ = sess.Watchdog.Touch()
		}
	}
}

func finalizePlaywrightLog(sess *session.Session, playwrightEvent event.Event, preprocessedID string) {
	if sess == nil || logOutputDir == "" || !saveAllLogs || sess.Caps.LogName == "" {
		return
	}

	temporaryLogName := sess.Caps.LogName
	finalLogName := preprocessedID + logFileExtension
	sess.Caps.LogName = finalLogName

	oldLogName := filepath.Join(logOutputDir, temporaryLogName)
	newLogName := filepath.Join(logOutputDir, finalLogName)
	if err := os.Rename(oldLogName, newLogName); err != nil {
		log.Printf("[%d] [LOG_ERROR] [%s]", playwrightEvent.RequestId, fmt.Sprintf("Failed to rename %s to %s: %v", oldLogName, newLogName, err))
		return
	}

	event.FileCreated(event.CreatedFile{
		Event: playwrightEvent,
		Name:  newLogName,
		Type:  "log",
	})
}

func clonePlaywrightURL(u *url.URL) *url.URL {
	if u == nil {
		return nil
	}
	cloned := *u
	return &cloned
}
