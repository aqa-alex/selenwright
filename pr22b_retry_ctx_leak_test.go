// PR #22 regression tests for session-create retry context leak
// (audit H-3). Prior code used `defer done()` at function scope inside
// the retry loop, so every iteration's context survived until create()
// returned — each holds a goroutine in the runtime timer heap. Under
// -retry-count=N this leaks up to N contexts per concurrent create.

package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/aqa-alex/selenwright/service"
	"github.com/aqa-alex/selenwright/session"
	assert "github.com/stretchr/testify/require"
)

// slowManager backs each session create with a httptest upstream that
// never completes a response within the per-attempt timeout, forcing
// the retry loop to walk every iteration.
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
		// Drain the body so the client can finish writing, then block
		// indefinitely; the per-attempt context deadline fires first.
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

// TestCreateRetryReleasesContextPerIteration verifies that after the
// retry loop exhausts its budget, no stray goroutines are left from
// context-timer bookkeeping. With the previous `defer done()` at
// function scope the runtime timer heap retained N entries until
// create() returned; we can't observe the heap directly, so we take a
// baseline goroutine count, drive a few concurrent creates, wait for
// the per-attempt deadlines to expire, GC, and check that the delta is
// bounded.
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

	// Let the test-wide runtime settle, then snapshot goroutine count.
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()

	// Drive several creates in parallel — each will burn all retry
	// attempts and return 500. The previous implementation leaked up
	// to retryCount contexts per invocation.
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

	// Give the runtime a chance to reap expired timers / goroutines.
	runtime.GC()
	time.Sleep(200 * time.Millisecond)
	runtime.GC()

	after := runtime.NumGoroutine()
	// Allow a generous slack for HTTP client connection reuse. The
	// interesting signal is "not proportional to concurrency * retries"
	// (32 before the fix, single digits after).
	assert.LessOrEqual(t, after-before, 10,
		"goroutine growth beyond baseline suggests context/timer leak (before=%d after=%d)", before, after)

	assert.Equal(t, 0, app.queue.Used(),
		"every failed create must drop its queue slot")
}
