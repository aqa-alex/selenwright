// Modified by [Aleksander R], 2026: added Playwright protocol support

package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aqa-alex/selenwright/info"
	"github.com/aqa-alex/selenwright/internal/metrics"
	"github.com/aqa-alex/selenwright/internal/safepath"
	"github.com/aqa-alex/selenwright/protect"

	"dario.cat/mergo"
	"github.com/aqa-alex/selenwright/event"
	"github.com/aqa-alex/selenwright/jsonerror"
	"github.com/aqa-alex/selenwright/service"
	"github.com/aqa-alex/selenwright/session"
)

const slash = "/"

const fileUploadDirHeader = "X-Selenwright-File"

var (
	httpClient = &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 30 * time.Second,
	}
	num     uint64
	numLock sync.RWMutex
)

type request struct {
	*http.Request
}

type sess struct {
	addr string
	id   string
}

// TODO There is simpler way to do this
func (r request) localaddr() string {
	addr := r.Context().Value(http.LocalAddrContextKey).(net.Addr).String()
	_, port, _ := net.SplitHostPort(addr)
	return net.JoinHostPort("127.0.0.1", port)
}

func (r request) session(id string) *sess {
	return &sess{r.localaddr(), id}
}

func (s *sess) url() string {
	return fmt.Sprintf("http://%s/wd/hub/session/%s", s.addr, s.id)
}

func (s *sess) Delete(requestId uint64) {
	log.Printf("[%d] [SESSION_TIMED_OUT] [%s]", requestId, s.id)
	r, err := http.NewRequest(http.MethodDelete, s.url(), nil)
	if err != nil {
		log.Printf("[%d] [DELETE_FAILED] [%s] [%v]", requestId, s.id, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), app.sessionDeleteTimeout)
	defer cancel()
	resp, err := httpClient.Do(r.WithContext(ctx))
	if resp != nil {
		defer resp.Body.Close()
	}
	if err == nil && resp.StatusCode == http.StatusOK {
		return
	}
	if err != nil {
		log.Printf("[%d] [DELETE_FAILED] [%s] [%v]", requestId, s.id, err)
	} else {
		log.Printf("[%d] [DELETE_FAILED] [%s] [%s]", requestId, s.id, resp.Status)
	}
}

func serial() uint64 {
	numLock.Lock()
	defer numLock.Unlock()
	id := num
	num++
	return id
}

func getSerial() uint64 {
	numLock.RLock()
	defer numLock.RUnlock()
	return num
}

func create(w http.ResponseWriter, r *http.Request) {
	sessionStartTime := time.Now()
	requestId := serial()
	user, remote := info.RequestInfo(r)
	// Cap the request body so a malicious client cannot exhaust memory by
	// streaming gigabytes of capabilities. MaxBytesReader also closes the
	// connection after the limit, surfacing a 413 to the client via the
	// response writer when ReadAll subsequently errors.
	r.Body = http.MaxBytesReader(w, r.Body, app.maxCreateBodyBytes)
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		log.Printf("[%d] [ERROR_READING_REQUEST] [%v]", requestId, err)
		jsonerror.InvalidArgument(err).Encode(w)
		app.queue.Drop()
		return
	}
	var browser struct {
		Caps    session.Caps `json:"desiredCapabilities"`
		W3CCaps struct {
			Caps       session.Caps    `json:"alwaysMatch"`
			FirstMatch []*session.Caps `json:"firstMatch"`
		} `json:"capabilities"`
	}
	err = json.Unmarshal(body, &browser)
	if err != nil {
		log.Printf("[%d] [BAD_JSON_FORMAT] [%v]", requestId, err)
		jsonerror.InvalidArgument(err).Encode(w)
		app.queue.Drop()
		return
	}
	if browser.W3CCaps.Caps.BrowserName() != "" && browser.Caps.BrowserName() == "" {
		browser.Caps = browser.W3CCaps.Caps
	}
	firstMatchCaps := browser.W3CCaps.FirstMatch
	if len(firstMatchCaps) == 0 {
		firstMatchCaps = append(firstMatchCaps, &session.Caps{})
	}
	var caps session.Caps
	var starter service.Starter
	var ok bool
	var sessionTimeout time.Duration
	var finalVideoName, finalLogName string
	identity, _ := protect.IdentityFromContext(r.Context())
	for _, fmc := range firstMatchCaps {
		caps = browser.Caps
		_ = mergo.Merge(&caps, *fmc)
		caps.ProcessExtensionCapabilities()
		if err = session.Sanitize(&caps, session.CapsPolicy(app.capsPolicyFlag), identity.IsAdmin); err != nil {
			metrics.CapsRejected()
			log.Printf("[%d] [REJECTED_CAPS] [%v]", requestId, err)
			jsonerror.InvalidArgument(err).Encode(w)
			app.queue.Drop()
			return
		}
		sessionTimeout, err = getSessionTimeout(caps.SessionTimeout, app.maxTimeout, app.timeout)
		if err != nil {
			log.Printf("[%d] [BAD_SESSION_TIMEOUT] [%s]", requestId, caps.SessionTimeout)
			jsonerror.InvalidArgument(err).Encode(w)
			app.queue.Drop()
			return
		}
		resolution, err := getScreenResolution(caps.ScreenResolution)
		if err != nil {
			log.Printf("[%d] [BAD_SCREEN_RESOLUTION] [%s]", requestId, caps.ScreenResolution)
			jsonerror.InvalidArgument(err).Encode(w)
			app.queue.Drop()
			return
		}
		caps.ScreenResolution = resolution
		videoScreenSize, err := getVideoScreenSize(caps.VideoScreenSize, resolution)
		if err != nil {
			log.Printf("[%d] [BAD_VIDEO_SCREEN_SIZE] [%s]", requestId, caps.VideoScreenSize)
			jsonerror.InvalidArgument(err).Encode(w)
			app.queue.Drop()
			return
		}
		caps.VideoScreenSize = videoScreenSize
		finalVideoName = caps.VideoName
		if caps.Video && !app.disableDocker {
			caps.VideoName = getTemporaryFileName(app.videoOutputDir, videoFileExtension)
		}
		finalLogName = caps.LogName
		if app.logOutputDir != "" && (app.saveAllLogs || caps.Log) {
			caps.LogName = getTemporaryFileName(app.logOutputDir, logFileExtension)
		}
		starter, ok = app.manager.Find(caps, requestId)
		if ok {
			break
		}
	}
	if !ok {
		log.Printf("[%d] [ENVIRONMENT_NOT_AVAILABLE] [%s] [%s]", requestId, caps.BrowserName(), caps.Version)
		jsonerror.InvalidArgument(errors.New("Requested environment is not available")).Encode(w)
		app.queue.Drop()
		return
	}
	startedService, err := starter.StartWithCancel()
	if err != nil {
		log.Printf("[%d] [SERVICE_STARTUP_FAILED] [%v]", requestId, err)
		jsonerror.SessionNotCreated(err).Encode(w)
		app.queue.Drop()
		return
	}
	u := startedService.Url
	cancel := startedService.Cancel
	host := "localhost"
	if startedService.Origin != "" {
		host = startedService.Origin
	}

	var resp *http.Response
	i := 1
	for ; ; i++ {
		r.URL.Host, r.URL.Path = u.Host, path.Join(u.Path, r.URL.Path)
		newBody := removeLegacyOptions(body)
		req, _ := http.NewRequest(http.MethodPost, r.URL.String(), bytes.NewReader(newBody))
		contentType := r.Header.Get("Content-Type")
		if len(contentType) > 0 {
			req.Header.Set("Content-Type", contentType)
		}
		req.Host = host
		// Use defer-in-a-closure so the per-attempt context and its
		// internal timer are released on every loop iteration. Prior
		// code deferred done() at function scope, so a -retry-count=N
		// run against a slow upstream accumulated up to N live contexts
		// until create() finally returned (audit H-3).
		ctx, done := context.WithTimeout(r.Context(), app.newSessionAttemptTimeout)
		log.Printf("[%d] [SESSION_ATTEMPTED] [%s] [%d]", requestId, u.String(), i)
		rsp, err := httpClient.Do(req.WithContext(ctx))
		select {
		case <-ctx.Done():
			if rsp != nil {
				_ = rsp.Body.Close()
			}
			switch ctx.Err() {
			case context.DeadlineExceeded:
				log.Printf("[%d] [SESSION_ATTEMPT_TIMED_OUT] [%s]", requestId, app.newSessionAttemptTimeout)
				if i < app.retryCount {
					done()
					continue
				}
				err := fmt.Errorf("New session attempts retry count exceeded")
				log.Printf("[%d] [SESSION_FAILED] [%s] [%s]", requestId, u.String(), err)
				jsonerror.UnknownError(err).Encode(w)
			case context.Canceled:
				log.Printf("[%d] [CLIENT_DISCONNECTED] [%s] [%s] [%.2fs]", requestId, user, remote, info.SecondsSince(sessionStartTime))
			}
			done()
			app.queue.Drop()
			cancel()
			return
		default:
		}
		if err != nil {
			if rsp != nil {
				_ = rsp.Body.Close()
			}
			log.Printf("[%d] [SESSION_FAILED] [%s] [%s]", requestId, u.String(), err)
			jsonerror.SessionNotCreated(err).Encode(w)
			done()
			app.queue.Drop()
			cancel()
			return
		}
		if rsp.StatusCode == http.StatusNotFound && u.Path == "" {
			u.Path = "/wd/hub"
			done()
			continue
		}
		resp = rsp
		done()
		break
	}
	defer resp.Body.Close()
	var s struct {
		Value struct {
			ID string `json:"sessionId"`
		}
		ID string `json:"sessionId"`
	}
	location := resp.Header.Get("Location")
	if location != "" {
		l, err := url.Parse(location)
		if err == nil {
			fragments := strings.Split(l.Path, slash)
			s.ID = fragments[len(fragments)-1]
			u := &url.URL{
				Scheme: "http",
				Host:   app.hostname,
				Path:   path.Join("/wd/hub/session", s.ID),
			}
			w.Header().Add("Location", u.String())
			w.WriteHeader(resp.StatusCode)
		}
	} else {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Printf("[%d] [ERROR_READING_RESPONSE] [%v]", requestId, err)
			app.queue.Drop()
			cancel()
			w.WriteHeader(resp.StatusCode)
			return
		}
		newBody, sessionId, err := processBody(body, r.Host)
		if err != nil {
			log.Printf("[%d] [ERROR_PROCESSING_RESPONSE] [%v]", requestId, err)
			app.queue.Drop()
			cancel()
			w.WriteHeader(resp.StatusCode)
			return
		}
		resp.Body = io.NopCloser(bytes.NewReader(newBody))
		resp.ContentLength = int64(len(newBody))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(newBody)
		s.ID = sessionId
	}
	if s.ID == "" {
		log.Printf("[%d] [SESSION_FAILED] [%s] [%s]", requestId, u.String(), resp.Status)
		app.queue.Drop()
		cancel()
		return
	}
	owner := identity.User
	if owner == "" {
		owner = user
	}
	sess := &session.Session{
		Quota:     owner,
		Caps:      caps,
		URL:       u,
		Container: startedService.Container,
		HostPort:  startedService.HostPort,
		Origin:    startedService.Origin,
		Timeout:   sessionTimeout,
		Watchdog: session.NewWatchdog(sessionTimeout, func() {
			request{r}.session(s.ID).Delete(requestId)
		}),
		Started: time.Now(),
	}
	cancelAndRenameFiles := func() {
		cancel()
		sessionId := preprocessSessionId(s.ID)
		e := event.Event{
			RequestId: requestId,
			SessionId: sessionId,
			Session:   sess,
		}
		if caps.Video && !app.disableDocker {
			oldVideoName := filepath.Join(app.videoOutputDir, caps.VideoName)
			if finalVideoName == "" {
				finalVideoName = sessionId + videoFileExtension
				e.Session.Caps.VideoName = finalVideoName
			}
			// finalVideoName originates from caps.VideoName supplied by the
			// client (selenwright.go:177). Without this guard a payload like
			// "../../etc/cron.d/evil.mp4" would let the user place the
			// recorded file at an attacker-chosen path on the host.
			newVideoName, joinErr := safepath.Join(app.videoOutputDir, finalVideoName)
			if joinErr != nil {
				log.Printf("[%d] [VIDEO_ERROR] [Rejected video name %q: %v]", requestId, finalVideoName, joinErr)
			} else {
				err := os.Rename(oldVideoName, newVideoName)
				if err != nil {
					log.Printf("[%d] [VIDEO_ERROR] [%s]", requestId, fmt.Sprintf("Failed to rename %s to %s: %v", oldVideoName, newVideoName, err))
				} else {
					createdFile := event.CreatedFile{
						Event: e,
						Name:  newVideoName,
						Type:  "video",
					}
					event.FileCreated(createdFile)
				}
			}
		}
		if app.logOutputDir != "" && (app.saveAllLogs || caps.Log) {
			//The following logic will fail if -capture-driver-logs is enabled and a session is requested in driver mode.
			//Specifying both -log-output-dir and -capture-driver-logs in that case is considered a misconfiguration.
			oldLogName := filepath.Join(app.logOutputDir, caps.LogName)
			if finalLogName == "" {
				finalLogName = sessionId + logFileExtension
				e.Session.Caps.LogName = finalLogName
			}
			// Same defense as above: finalLogName came from caps.LogName.
			newLogName, joinErr := safepath.Join(app.logOutputDir, finalLogName)
			if joinErr != nil {
				log.Printf("[%d] [LOG_ERROR] [Rejected log name %q: %v]", requestId, finalLogName, joinErr)
			} else {
				err := os.Rename(oldLogName, newLogName)
				if err != nil {
					log.Printf("[%d] [LOG_ERROR] [%s]", requestId, fmt.Sprintf("Failed to rename %s to %s: %v", oldLogName, newLogName, err))
				} else {
					createdFile := event.CreatedFile{
						Event: e,
						Name:  newLogName,
						Type:  "log",
					}
					event.FileCreated(createdFile)
				}
			}
		}
		event.SessionStopped(event.StoppedSession{Event: e})
		metrics.SessionEnded("selenium", "cancel", info.SecondsSince(sess.Started))
	}
	sess.Cancel = cancelAndRenameFiles
	app.sessions.Put(s.ID, sess)
	app.queue.Create()
	metrics.SessionCreated("selenium")
	log.Printf("[%d] [SESSION_CREATED] [%s] [%d] [%.2fs]", requestId, s.ID, i, info.SecondsSince(sessionStartTime))
}

func removeLegacyOptions(input []byte) []byte {
	body := make(map[string]interface{})
	_ = json.Unmarshal(input, &body)
	const legacyOptionsKey = "selenoid:options"
	if raw, ok := body["desiredCapabilities"]; ok {
		if dc, ok := raw.(map[string]interface{}); ok {
			delete(dc, legacyOptionsKey)
		}
	}
	if raw, ok := body["capabilities"]; ok {
		if c, ok := raw.(map[string]interface{}); ok {
			if raw, ok := c["alwaysMatch"]; ok {
				if am, ok := raw.(map[string]interface{}); ok {
					delete(am, legacyOptionsKey)
				}
			}
			if raw, ok := c["firstMatch"]; ok {
				if fm, ok := raw.([]interface{}); ok {
					for _, raw := range fm {
						if c, ok := raw.(map[string]interface{}); ok {
							delete(c, legacyOptionsKey)
						}
					}
				}
			}
		}
	}
	ret, _ := json.Marshal(body)
	return ret
}

func processBody(input []byte, host string) ([]byte, string, error) {
	body := make(map[string]interface{})
	sessionId := ""
	err := json.Unmarshal(input, &body)
	if err != nil {
		return nil, sessionId, fmt.Errorf("parse body response: %v", err)
	}
	if rawId, ok := body["sessionId"]; ok {
		if si, ok := rawId.(string); ok {
			sessionId = si
		}
	} else if raw, ok := body["value"]; ok {
		if v, ok := raw.(map[string]interface{}); ok {
			if raw, ok := v["capabilities"]; ok {
				if c, ok := raw.(map[string]interface{}); ok {
					// Every cast is guarded. A malicious or merely
					// broken browser container returning
					// {"value":{"sessionId": 42}} must not panic the
					// selenwright process — we just skip the CDP
					// injection and let the raw body through.
					if rawSid, ok := v["sessionId"]; ok {
						if sid, ok := rawSid.(string); ok {
							sessionId = sid
							c["se:cdp"] = fmt.Sprintf("ws://%s/devtools/%s/", host, sessionId)
						}
					}
					if rbv, ok := c["browserVersion"]; ok {
						if bv, ok := rbv.(string); ok {
							c["se:cdpVersion"] = bv
						}
					}
				}
			}
		}
	}
	ret, err := json.Marshal(body)
	if err != nil {
		return nil, sessionId, fmt.Errorf("marshal response: %v", err)
	}
	return ret, sessionId, nil
}

func preprocessSessionId(sid string) string {
	if app.ggrHost != nil {
		return app.ggrHost.Sum() + sid
	}
	return sid
}

const (
	videoFileExtension = ".mp4"
	logFileExtension   = ".log"
)

var (
	fullFormat  = regexp.MustCompile(`^([0-9]+x[0-9]+)x(8|16|24)$`)
	shortFormat = regexp.MustCompile(`^[0-9]+x[0-9]+$`)
)

func getScreenResolution(input string) (string, error) {
	if input == "" {
		return "1920x1080x24", nil
	}
	if fullFormat.MatchString(input) {
		return input, nil
	}
	if shortFormat.MatchString(input) {
		return fmt.Sprintf("%sx24", input), nil
	}
	return "", fmt.Errorf(
		"malformed screenResolution capability: %s, correct format is WxH (1920x1080) or WxHxD (1920x1080x24)",
		input,
	)
}

func shortenScreenResolution(screenResolution string) string {
	return fullFormat.FindStringSubmatch(screenResolution)[1]
}

func getVideoScreenSize(videoScreenSize string, screenResolution string) (string, error) {
	if videoScreenSize != "" {
		if shortFormat.MatchString(videoScreenSize) {
			return videoScreenSize, nil
		}
		return "", fmt.Errorf(
			"malformed videoScreenSize capability: %s, correct format is WxH (1920x1080)",
			videoScreenSize,
		)
	}
	return shortenScreenResolution(screenResolution), nil
}

func getSessionTimeout(sessionTimeout string, maxTimeout time.Duration, defaultTimeout time.Duration) (time.Duration, error) {
	if sessionTimeout != "" {
		st, err := time.ParseDuration(sessionTimeout)
		if err != nil {
			return 0, fmt.Errorf("invalid sessionTimeout capability: %v", err)
		}
		if st <= app.maxTimeout {
			return st, nil
		}
		return app.maxTimeout, nil
	}
	return defaultTimeout, nil
}

// getTemporaryFileName returns a fresh filename (not path) inside dir
// with the given extension. Uses os.CreateTemp so the name is selected
// atomically — no Stat-then-use TOCTOU window, no ignored rand.Read
// error. The placeholder file is removed before returning because the
// real writer (video recorder container, log sink) needs to create
// the file itself with its own permission and open flags; the tiny
// gap between remove and the downstream create is acceptable given
// the 128 bits of entropy in the generated suffix.
func getTemporaryFileName(dir string, extension string) string {
	f, err := os.CreateTemp(dir, "selenwright*"+extension)
	if err != nil {
		return "selenwright" + extension
	}
	name := filepath.Base(f.Name())
	_ = f.Close()
	_ = os.Remove(f.Name())
	return name
}

const vendorPrefix = "aerokube"

func proxy(w http.ResponseWriter, r *http.Request) {
	requestId := serial()
	fragments := strings.Split(r.URL.Path, slash)
	if len(fragments) < 3 {
		r.URL.Path = paths.Error
		(&httputil.ReverseProxy{Director: func(*http.Request) {}, ErrorHandler: defaultErrorHandler(requestId)}).ServeHTTP(w, r)
		return
	}
	id := fragments[2]
	sess, ok := app.sessions.Get(id)
	if !ok {
		r.URL.Path = paths.Error
		(&httputil.ReverseProxy{Director: func(*http.Request) {}, ErrorHandler: defaultErrorHandler(requestId)}).ServeHTTP(w, r)
		return
	}

	if len(fragments) >= 4 && fragments[3] == vendorPrefix {
		newFragments := []string{"", fragments[4], id}
		if len(fragments) >= 5 {
			newFragments = append(newFragments, fragments[5:]...)
		}
		rewritten := path.Clean(strings.Join(newFragments, slash))
		(&httputil.ReverseProxy{
			Director: func(r *http.Request) {
				stripTrustHeaders(r)
				r.URL.Host = (&request{r}).localaddr()
				r.URL.Path = rewritten
			},
			ErrorHandler: defaultErrorHandler(requestId),
		}).ServeHTTP(w, r)
		return
	}

	isSessionDelete := r.Method == http.MethodDelete && len(fragments) == 3
	var postCleanup func()

	if isSessionDelete {
		sess.Lock.Lock()
		if app.enableFileUpload {
			_ = os.RemoveAll(filepath.Join(os.TempDir(), id))
		}
		stopWatchdog(sess)
		app.sessions.Remove(id)
		app.queue.Release()
		log.Printf("[%d] [SESSION_DELETED] [%s]", requestId, id)
		postCleanup = sess.Cancel
		sess.Lock.Unlock()
	} else {
		touchWatchdog(sess)
	}

	director := func(r *http.Request) {
		stripTrustHeaders(r)
		if len(fragments) == 4 && fragments[len(fragments)-1] == "file" && app.enableFileUpload {
			r.Header.Set(fileUploadDirHeader, filepath.Join(os.TempDir(), id))
			r.URL.Path = "/file"
			return
		}
		seUploadPath, uploadPath := "/se/file", "/file"
		if strings.HasSuffix(r.URL.Path, seUploadPath) {
			r.URL.Path = strings.TrimSuffix(r.URL.Path, seUploadPath) + uploadPath
		}
		r.URL.Host, r.URL.Path = sess.URL.Host, path.Clean(sess.URL.Path+r.URL.Path)
		r.Host = "localhost"
		if sess.Origin != "" {
			r.Host = sess.Origin
		}
	}

	(&httputil.ReverseProxy{
		Director:     director,
		ErrorHandler: defaultErrorHandler(requestId),
	}).ServeHTTP(w, r)

	if postCleanup != nil {
		postCleanup()
	}
}

func defaultErrorHandler(requestId uint64) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		user, remote := info.RequestInfo(r)
		log.Printf("[%d] [CLIENT_DISCONNECTED] [%s] [%s] [Error: %v]", requestId, user, remote, err)
		w.WriteHeader(http.StatusBadGateway)
	}
}

func reverseProxy(hostFn func(sess *session.Session) string, status string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		requestId := serial()
		sid, remainingPath := splitRequestPath(r.URL.Path)
		sess, ok := app.sessions.Get(sid)
		if ok {
			if isDevtoolsWebSocketRequest(r) {
				handleDevtoolsWebSocket(w, r, requestId, sid, remainingPath, sess)
				return
			}
			touchWatchdog(sess)
			(&httputil.ReverseProxy{
				Director: func(r *http.Request) {
					stripTrustHeaders(r)
					r.URL.Scheme = "http"
					r.URL.Host = hostFn(sess)
					r.URL.Path = remainingPath
					log.Printf("[%d] [%s] [%s] [%s]", requestId, status, sid, remainingPath)
				},
				ErrorHandler: defaultErrorHandler(requestId),
			}).ServeHTTP(w, r)
		} else {
			jsonerror.InvalidSessionID(fmt.Errorf("unknown session %s", sid)).Encode(w)
			log.Printf("[%d] [SESSION_NOT_FOUND] [%s]", requestId, sid)
		}
	}
}

func splitRequestPath(p string) (string, string) {
	fragments := strings.Split(p, slash)
	return fragments[2], slash + strings.Join(fragments[3:], slash)
}

func touchWatchdog(sess *session.Session) {
	if sess == nil || sess.Watchdog == nil {
		return
	}
	_ = sess.Watchdog.Touch()
}

func stopWatchdog(sess *session.Session) {
	if sess == nil || sess.Watchdog == nil {
		return
	}
	_ = sess.Watchdog.Stop()
}

func fileUpload(w http.ResponseWriter, r *http.Request) {
	// Cap the JSON envelope size before decoding. The upload endpoint accepts
	// a base64-encoded zip in `file`, which expands ~33% on the wire — we
	// allow generous headroom but still bound it.
	r.Body = http.MaxBytesReader(w, r.Body, app.maxUploadBodyBytes)
	var jsonRequest struct {
		File []byte `json:"file"`
	}
	err := json.NewDecoder(r.Body).Decode(&jsonRequest)
	if err != nil {
		jsonerror.InvalidArgument(err).Encode(w)
		return
	}
	z, err := zip.NewReader(bytes.NewReader(jsonRequest.File), int64(len(jsonRequest.File)))
	if err != nil {
		jsonerror.InvalidArgument(err).Encode(w)
		return
	}
	if len(z.File) != 1 {
		err := fmt.Errorf("expected there to be only 1 file. There were: %d", len(z.File))
		jsonerror.InvalidArgument(err).Encode(w)
		return
	}
	file := z.File[0]
	// Reject zip-bomb attempts whose declared uncompressed size already
	// exceeds the limit. The header can lie, so we re-check the actual
	// extracted byte count below.
	if file.UncompressedSize64 > uint64(app.maxUploadExtractedBytes) {
		jsonerror.InvalidArgument(fmt.Errorf("uncompressed file size %d exceeds limit %d", file.UncompressedSize64, app.maxUploadExtractedBytes)).Encode(w)
		return
	}
	src, err := file.Open()
	if err != nil {
		jsonerror.InvalidArgument(err).Encode(w)
		return
	}
	defer src.Close()
	dir := r.Header.Get(fileUploadDirHeader)
	err = os.MkdirAll(dir, 0o750)
	if err != nil {
		jsonerror.UnknownError(err).Encode(w)
		return
	}
	// Resolve the zip entry name against the per-session upload dir while
	// rejecting traversal attempts (entry names like "../../etc/passwd").
	// CWE-22 (Zip Slip) — without this guard, filepath.Join would happily
	// produce a path outside `dir`.
	fileName, err := safepath.Join(dir, file.Name)
	if err != nil {
		jsonerror.InvalidArgument(fmt.Errorf("rejected zip entry name %q: %v", file.Name, err)).Encode(w)
		return
	}
	dst, err := os.OpenFile(fileName, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		jsonerror.UnknownError(err).Encode(w)
		return
	}
	defer dst.Close()
	// Bound the actual decompressed bytes regardless of the header. We read
	// one byte beyond the limit to detect overshoot and reject without
	// committing a partial-but-still-huge file to disk.
	limited := io.LimitReader(src, app.maxUploadExtractedBytes+1)
	written, err := io.Copy(dst, limited)
	if err != nil {
		jsonerror.UnknownError(err).Encode(w)
		return
	}
	if written > app.maxUploadExtractedBytes {
		_ = dst.Close()
		_ = os.Remove(fileName)
		jsonerror.InvalidArgument(fmt.Errorf("uncompressed file size exceeds limit %d", app.maxUploadExtractedBytes)).Encode(w)
		return
	}

	reply := struct {
		V string `json:"value"`
	}{
		V: fileName,
	}
	_ = json.NewEncoder(w).Encode(reply)
}

func status(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	ready := app.limit > app.sessions.Len()
	_ = json.NewEncoder(w).Encode(
		map[string]interface{}{
			"value": map[string]interface{}{
				"message": fmt.Sprintf("Selenwright %s built at %s", gitRevision, buildStamp),
				"ready":   ready,
			},
		})
}

func welcome(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(fmt.Sprintf("You are using Selenwright %s!", gitRevision)))
}
