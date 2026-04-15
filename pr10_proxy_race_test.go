package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	assert "github.com/stretchr/testify/require"
)

// TestProxyCancelOnDelete_RunsExactlyOnce verifies PR #10 removed the
// done-channel/goroutine pattern from proxy() without losing the
// guarantee that sess.Cancel runs exactly once when a session is
// DELETEd. Under the old pattern an early panic in ReverseProxy could
// leave the cancel callback invoked or not depending on timing.
func TestProxyCancelOnDelete_RunsExactlyOnce(t *testing.T) {
	handler := HTTPTest{Handler: Selenium()}
	manager = &handler

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

	_, stillThere := sessions.Get(sid)
	assert.False(t, stillThere, "session must be removed from the map after DELETE")
}

// TestProxyConcurrentGETAndDELETE runs many parallel GET requests against
// a session, then DELETEs it once. Race detector will flag any data race
// left behind by the rewrite. The test is deliberately low-fidelity:
// we're asserting "no panic, no race", not functional correctness of the
// upstream proxy.
func TestProxyConcurrentGETAndDELETE(t *testing.T) {
	handler := HTTPTest{Handler: Selenium()}
	manager = &handler

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

	_, stillThere := sessions.Get(sid)
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
