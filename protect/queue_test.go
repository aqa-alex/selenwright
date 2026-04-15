package protect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQueue_CountersAfterHappyPath(t *testing.T) {
	q := New(4, false)
	require.Equal(t, 0, q.Used())
	require.Equal(t, 0, q.Pending())
	require.Equal(t, 0, q.Queued())

	runProtect(t, q, func() {
		require.Equal(t, 1, q.Pending())
		q.Create()
		require.Equal(t, 0, q.Pending())
		require.Equal(t, 1, q.Used())
		q.Release()
	})

	require.Equal(t, 0, q.Used())
	require.Equal(t, 0, q.Pending())
	require.Equal(t, 0, q.Queued())
}

func TestQueue_Drop_Releases_Slot(t *testing.T) {
	q := New(1, false)
	runProtect(t, q, func() {
		require.Equal(t, 1, q.Pending())
		q.Drop()
		require.Equal(t, 0, q.Pending())
	})
	require.True(t, q.hasFreeSlot(), "after Drop the slot must be free again")
}

func TestQueue_TryRejectsOnFull_WithNoWaitHeader(t *testing.T) {
	q := New(1, false)

	runProtect(t, q, func() {
		q.Create()
		t.Cleanup(q.Release)

		srv := httptest.NewServer(q.Try(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)

		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		req.Header.Set("X-Selenwright-No-Wait", "1")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusInternalServerError, resp.StatusCode,
			"No-Wait + full queue must return an error immediately")
	})
}

func TestQueue_TryFallsThroughWithoutHeader(t *testing.T) {
	q := New(1, false)
	runProtect(t, q, func() {
		q.Create()
		t.Cleanup(q.Release)

		hit := int32(0)
		srv := httptest.NewServer(q.Try(func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt32(&hit, 1)
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.EqualValues(t, 1, atomic.LoadInt32(&hit))
	})
}

func TestQueue_CheckRejectsWhenDisabledAndFull(t *testing.T) {
	q := New(1, true)
	runProtect(t, q, func() {
		q.Create()
		t.Cleanup(q.Release)

		srv := httptest.NewServer(q.Check(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	})
}

func TestQueue_ClientDisconnectDuringWait(t *testing.T) {
	q := New(1, false)
	runProtect(t, q, func() {
		q.Create()
		t.Cleanup(q.Release)

		ctx, cancel := context.WithCancel(context.Background())

		waited := make(chan error, 1)
		go func() {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
			w := httptest.NewRecorder()
			q.Protect(func(http.ResponseWriter, *http.Request) {})(w, req)
			waited <- nil
		}()

		// The waiter is now inside Acquire; Queued must increment.
		require.Eventually(t, func() bool { return q.Queued() == 1 }, time.Second, time.Millisecond)

		cancel()
		select {
		case <-waited:
		case <-time.After(time.Second):
			t.Fatal("Protect did not return after client context cancel")
		}

		require.Equal(t, 0, q.Queued(), "cancelled client must decrement Queued")
		require.Equal(t, 1, q.Used(), "first request still holds its slot via used")
	})
}

func TestQueue_ConcurrentStressHasConsistentFinalCounters(t *testing.T) {
	q := New(8, false)

	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
			w := httptest.NewRecorder()
			q.Protect(func(http.ResponseWriter, *http.Request) {
				q.Create()
				time.Sleep(time.Microsecond)
				q.Release()
			})(w, req)
		}()
	}
	wg.Wait()

	require.Equal(t, 0, q.Used())
	require.Equal(t, 0, q.Pending())
	require.Equal(t, 0, q.Queued())
	require.True(t, q.hasFreeSlot(), "after the storm the queue must have free slots")
}

func runProtect(t *testing.T, q *Queue, inside func()) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	ran := false
	q.Protect(func(http.ResponseWriter, *http.Request) {
		ran = true
		inside()
	})(w, req)
	require.True(t, ran, "Protect did not reach inner handler")
}
