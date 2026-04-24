package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aqa-alex/selenwright/service"
	"github.com/aqa-alex/selenwright/session"
	gwebsocket "github.com/gorilla/websocket"
	assert "github.com/stretchr/testify/require"
)

func TestProxyCancelOnDelete_RunsExactlyOnce(t *testing.T) {
	handler := HTTPTest{Handler: Selenium()}
	app.manager = &handler

	resp, err := http.Post(With(srv.URL).Path("/wd/hub/session"), "", strings.NewReader("{}"))
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var sid string
	if body := decodeBodyString(t, resp, "sessionId"); body != "" {
		sid = body
	}
	assert.NotEmpty(t, sid)

	req, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/wd/hub/session/%s", srv.URL, sid), nil)
	rsp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer rsp.Body.Close()

	_, stillThere := app.sessions.Get(sid)
	assert.False(t, stillThere, "session must be removed from the map after DELETE")
}

func TestProxyConcurrentGETAndDELETE(t *testing.T) {
	handler := HTTPTest{Handler: Selenium()}
	app.manager = &handler

	resp, err := http.Post(With(srv.URL).Path("/wd/hub/session"), "", strings.NewReader("{}"))
	assert.NoError(t, err)
	defer resp.Body.Close()
	sid := decodeBodyString(t, resp, "sessionId")
	assert.NotEmpty(t, sid)

	sessURL := fmt.Sprintf("%s/wd/hub/session/%s", srv.URL, sid)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if r, err := http.Get(sessURL + "/title"); err == nil {
					_ = r.Body.Close()
				}
			}
		}()
	}

	time.Sleep(20 * time.Millisecond)
	req, _ := http.NewRequest(http.MethodDelete, sessURL, nil)
	rsp, _ := http.DefaultClient.Do(req)
	if rsp != nil {
		_ = rsp.Body.Close()
	}
	close(stop)
	wg.Wait()

	_, stillThere := app.sessions.Get(sid)
	assert.False(t, stillThere)
}

func decodeBodyString(t *testing.T, resp *http.Response, key string) string {
	t.Helper()
	if resp == nil || resp.Body == nil {
		return ""
	}
	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ""
	}
	if v, ok := out[key].(string); ok {
		return v
	}
	if v, ok := out["value"].(map[string]interface{}); ok {
		if s, ok := v[key].(string); ok {
			return s
		}
	}
	return ""
}

func TestProcessBody_DoesNotPanicOnMalformedJSON(t *testing.T) {
	cases := map[string]string{
		"session-id-not-string":       `{"sessionId": 42}`,
		"value-not-object":            `{"value": 42}`,
		"capabilities-not-object":     `{"value": {"capabilities": 42}}`,
		"session-id-in-value-not-str": `{"value": {"sessionId": 42, "capabilities": {}}}`,
		"session-id-in-value-null":    `{"value": {"sessionId": null, "capabilities": {}}}`,
		"browserVersion-not-string":   `{"value": {"sessionId": "abc", "capabilities": {"browserVersion": 42}}}`,
		"capabilities-nested-garbage": `{"value": {"sessionId": "abc", "capabilities": [1,2,3]}}`,
		"entirely-empty-object":       `{}`,
		"only-null-value":             `{"value": null}`,
		"array-at-root":               `[1,2,3]`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			require := func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("processBody panicked on %s: %v", name, r)
					}
				}()
				_, _, _ = processBody([]byte(raw), "host")
			}
			require()
		})
	}
}

func TestProcessBody_HappyPathW3C(t *testing.T) {
	in := `{"value":{"sessionId":"abc123","capabilities":{"browserVersion":"120"}}}`
	out, sid, err := processBody([]byte(in), "selenwright.local")
	assert.NoError(t, err)
	assert.Equal(t, "abc123", sid)
	assert.Contains(t, string(out), `"se:cdp":"ws://selenwright.local/devtools/abc123/"`)
	assert.Contains(t, string(out), `"se:cdpVersion":"120"`)
}

func TestProcessBody_HappyPathLegacy(t *testing.T) {
	in := `{"sessionId":"abc123","status":0}`
	out, sid, err := processBody([]byte(in), "selenwright.local")
	assert.NoError(t, err)
	assert.Equal(t, "abc123", sid)
	assert.Contains(t, string(out), `"sessionId":"abc123"`)
}

func TestProcessBody_InvalidJSON(t *testing.T) {
	_, _, err := processBody([]byte(`not json`), "host")
	assert.Error(t, err)
}

func FuzzProcessBody(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"sessionId":"abc"}`))
	f.Add([]byte(`{"value":{"sessionId":"abc","capabilities":{}}}`))
	f.Add([]byte(`{"value":{"sessionId":42,"capabilities":{}}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic: %v\ninput: %q", r, data)
			}
		}()
		_, _, _ = processBody(data, "host")
	})
}

func TestPlaywrightEarlyCleanupOnDialFailure(t *testing.T) {
	prevManager := app.manager
	prevTimeout := app.timeout
	app.timeout = 200 * time.Millisecond
	t.Cleanup(func() {
		app.manager = prevManager
		app.timeout = prevTimeout
	})

	var cancelCalls int32
	app.manager = &StaticService{
		Available: true,
		StartedService: service.StartedService{
			PlaywrightURL: mustParseWS(t, "ws://127.0.0.1:1/never-listens"),
			Cancel: func() {
				atomic.AddInt32(&cancelCalls, 1)
			},
		},
	}

	usedBefore := app.queue.Used()
	pendingBefore := app.queue.Pending()

	wsURL := fmt.Sprintf("ws://%s/playwright/firefox/1.49.1", srv.Listener.Addr().String())
	_, resp, err := gwebsocket.DefaultDialer.Dial(wsURL, nil)
	assert.Error(t, err)
	if resp != nil {
		_ = resp.Body.Close()
	}

	assert.Eventually(t, func() bool {
		return atomic.LoadInt32(&cancelCalls) == 1
	}, time.Second, 10*time.Millisecond,
		"serviceCancel must be called once when upstream dial fails")

	assert.Equal(t, usedBefore, app.queue.Used(),
		"used counter must stay unchanged after failed setup")
	assert.Equal(t, pendingBefore, app.queue.Pending(),
		"pending counter must not leak — queue.Drop should have fired")
}

func mustParseWS(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	assert.NoError(t, err)
	return u
}

func TestCopyWithWatchdogTouchesWatchdog(t *testing.T) {
	var touches int32
	wd := session.NewWatchdog(200*time.Millisecond, func() {
		atomic.AddInt32(&touches, -1000) // sentinel: would mean timeout fired
	})
	wd.Start()
	defer wd.Stop()
	sess := &session.Session{Watchdog: wd}

	src := strings.NewReader("hello there")
	dst := &bytes.Buffer{}
	n, err := copyWithWatchdog(dst, src, sess)
	assert.NoError(t, err)
	assert.Equal(t, int64(11), n)
	assert.Equal(t, "hello there", dst.String())

	time.Sleep(50 * time.Millisecond)
	assert.GreaterOrEqual(t, atomic.LoadInt32(&touches), int32(0),
		"if Touch fired during Copy, timeout sentinel should not have decremented")
}

func TestCopyWithWatchdogHandlesNilSession(t *testing.T) {
	src := strings.NewReader("x")
	dst := &bytes.Buffer{}
	n, err := copyWithWatchdog(dst, src, nil)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), n)
}

func TestCopyWithWatchdogPropagatesReadError(t *testing.T) {
	erroringReader := &funcReader{fn: func([]byte) (int, error) { return 0, errors.New("boom") }}
	dst := &bytes.Buffer{}
	_, err := copyWithWatchdog(dst, erroringReader, nil)
	assert.Error(t, err)
}

type funcReader struct {
	fn func([]byte) (int, error)
}

func (r *funcReader) Read(p []byte) (int, error) { return r.fn(p) }

type slowManager struct {
	mu      sync.Mutex
	servers []*httptest.Server
}

func (m *slowManager) Find(caps session.Caps, requestId uint64) (service.Starter, bool) {
	return m, true
}

func (m *slowManager) StartWithCancel() (*service.StartedService, error) {
	block := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-block
	}))
	m.mu.Lock()
	m.servers = append(m.servers, upstream)
	m.mu.Unlock()
	u, _ := url.Parse(upstream.URL)
	closed := false
	return &service.StartedService{
		Url: u,
		Cancel: func() {
			if closed {
				return
			}
			closed = true
			close(block)
			upstream.Close()
		},
	}, nil
}

func (m *slowManager) shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.servers {
		s.Close()
	}
}

func TestCreateRetryReleasesContextPerIteration(t *testing.T) {
	mgr := &slowManager{}
	t.Cleanup(mgr.shutdown)

	previousMgr := app.manager
	previousRetries := app.retryCount
	previousAttempt := app.newSessionAttemptTimeout
	app.manager = mgr
	app.retryCount = 4
	app.newSessionAttemptTimeout = 60 * time.Millisecond
	t.Cleanup(func() {
		app.manager = previousMgr
		app.retryCount = previousRetries
		app.newSessionAttemptTimeout = previousAttempt
	})

	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()

	const concurrency = 8
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			resp, err := http.Post(
				With(srv.URL).Path("/wd/hub/session"),
				"application/json",
				bytes.NewReader([]byte(`{}`)),
			)
			if err == nil {
				_ = resp.Body.Close()
			}
		}()
	}
	wg.Wait()

	runtime.GC()
	time.Sleep(200 * time.Millisecond)
	runtime.GC()

	after := runtime.NumGoroutine()
	assert.LessOrEqual(t, after-before, 10,
		"goroutine growth beyond baseline suggests context/timer leak (before=%d after=%d)", before, after)

	assert.Equal(t, 0, app.queue.Used(),
		"every failed create must drop its queue slot")
}
