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
	"github.com/aqa-alex/selenwright/internal/metrics"
	"github.com/aqa-alex/selenwright/jsonerror"
	"github.com/aqa-alex/selenwright/protect"
	"github.com/aqa-alex/selenwright/session"
	gwebsocket "github.com/gorilla/websocket"
)

// metricsReason normalizes the free-form reason tag passed to
// cleanup() (which also feeds the PLAYWRIGHT_* log prefixes) into the
// short stable label the session_ended counter uses. Anything not in
// the explicit set collapses to "close" so the cardinality stays
// bounded.
func metricsReason(reason string) string {
	switch reason {
	case "client", "upstream", "timeout", "shutdown":
		return reason
	default:
		return "close"
	}
}

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
		app.queue.Drop()
		return
	}
	if !gwebsocket.IsWebSocketUpgrade(r) {
		log.Printf("[%d] [BAD_PLAYWRIGHT_REQUEST] [expected websocket upgrade]", requestId)
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		app.queue.Drop()
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
	if app.logOutputDir != "" && app.saveAllLogs {
		caps.LogName = getTemporaryFileName(app.logOutputDir, logFileExtension)
	}

	identity, _ := protect.IdentityFromContext(r.Context())
	if err := session.Sanitize(&caps, session.CapsPolicy(app.capsPolicyFlag), identity.IsAdmin); err != nil {
		metrics.CapsRejected()
		log.Printf("[%d] [REJECTED_CAPS] [%v]", requestId, err)
		jsonerror.InvalidArgument(err).Encode(w)
		app.queue.Drop()
		return
	}

	starter, ok := app.manager.Find(caps, requestId)
	if !ok {
		log.Printf("[%d] [ENVIRONMENT_NOT_AVAILABLE] [%s] [%s]", requestId, caps.BrowserName(), caps.Version)
		jsonerror.InvalidArgument(errors.New("Requested environment is not available")).Encode(w)
		app.queue.Drop()
		return
	}

	// Early-init cleanup: walks the stack in reverse on any early return
	// before the request is successfully upgraded and tracked in the
	// session map. When init reaches the success point we clear the list
	// so the deferred runner becomes a no-op; from that point the
	// cleanup closure defined later takes over resource ownership.
	var earlyCleanup []func()
	defer func() {
		for i := len(earlyCleanup) - 1; i >= 0; i-- {
			earlyCleanup[i]()
		}
	}()
	earlyCleanup = append(earlyCleanup, app.queue.Drop)

	startedService, err := starter.StartWithCancel()
	if err != nil {
		log.Printf("[%d] [SERVICE_STARTUP_FAILED] [%v]", requestId, err)
		jsonerror.SessionNotCreated(err).Encode(w)
		return
	}

	serviceCancel := startedService.Cancel
	if serviceCancel == nil {
		serviceCancel = func() {}
	}
	earlyCleanup = append(earlyCleanup, serviceCancel)

	if startedService.PlaywrightURL == nil {
		err := errors.New("playwright upstream url is not configured")
		log.Printf("[%d] [SERVICE_STARTUP_FAILED] [%v]", requestId, err)
		jsonerror.SessionNotCreated(err).Encode(w)
		return
	}

	upstreamURL := clonePlaywrightURL(startedService.PlaywrightURL)
	upstreamURL.RawQuery = r.URL.RawQuery

	upstreamConn, err := dialPlaywrightUpstream(r, upstreamURL)
	if err != nil {
		log.Printf("[%d] [PLAYWRIGHT_DIAL_FAILED] [%v]", requestId, err)
		jsonerror.SessionNotCreated(err).Encode(w)
		return
	}
	earlyCleanup = append(earlyCleanup, func() { _ = upstreamConn.Close() })

	sessionID, err := newPlaywrightSessionID()
	if err != nil {
		log.Printf("[%d] [PLAYWRIGHT_SESSION_ID_FAILED] [%v]", requestId, err)
		jsonerror.SessionNotCreated(err).Encode(w)
		return
	}

	upgrader := gwebsocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return app.originChecker.Check(r)
		},
	}
	upgradeHeader := http.Header{}
	if subprotocol := upstreamConn.Subprotocol(); subprotocol != "" {
		upgradeHeader.Set("Sec-WebSocket-Protocol", subprotocol)
	}
	clientConn, err := upgrader.Upgrade(w, r, upgradeHeader)
	if err != nil {
		log.Printf("[%d] [PLAYWRIGHT_UPGRADE_FAILED] [%v]", requestId, err)
		return
	}
	earlyCleanup = nil

	owner := identity.User
	if owner == "" {
		owner = user
	}
	sess := &session.Session{
		Quota:     owner,
		Caps:      caps,
		URL:       clonePlaywrightURL(upstreamURL),
		Container: startedService.Container,
		HostPort:  startedService.HostPort,
		Origin:    startedService.Origin,
		Timeout:   app.timeout,
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
			app.sessions.Remove(sessionID)
			app.queue.Release()
			event.SessionStopped(event.StoppedSession{Event: playwrightEvent})
			metrics.SessionEnded("playwright", metricsReason(reason), info.SecondsSince(sess.Started))

			switch reason {
			case "timeout":
				log.Printf("[%d] [PLAYWRIGHT_SESSION_TIMED_OUT] [%s]", requestId, sessionID)
			case "shutdown":
				log.Printf("[%d] [PLAYWRIGHT_SESSION_STOPPED] [%s] [server shutdown]", requestId, sessionID)
			}
		})
	}

	sess.Watchdog = session.NewWatchdog(app.timeout, func() {
		cleanup("timeout")
	})
	sess.Cancel = func() {
		cleanup("shutdown")
	}

	app.sessions.Put(sessionID, sess)
	app.queue.Create()
	metrics.SessionCreated("playwright")
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
	dialer.HandshakeTimeout = playwrightUpstreamHandshakeTimeout

	header := http.Header{}
	copyPlaywrightHeader(header, r.Header, "Origin")

	upstreamConn, resp, err := dialer.DialContext(r.Context(), upstreamURL.String(), header)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, err
	}

	return upstreamConn, nil
}

const playwrightUpstreamHandshakeTimeout = 15 * time.Second

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
		if _, exists := app.sessions.Get(sessionID); exists {
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
	if sess == nil || app.logOutputDir == "" || !app.saveAllLogs || sess.Caps.LogName == "" {
		return
	}

	temporaryLogName := sess.Caps.LogName
	finalLogName := preprocessedID + logFileExtension
	sess.Caps.LogName = finalLogName

	oldLogName := filepath.Join(app.logOutputDir, temporaryLogName)
	newLogName := filepath.Join(app.logOutputDir, finalLogName)
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
