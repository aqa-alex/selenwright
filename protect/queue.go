package protect

import (
	"errors"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/aqa-alex/selenwright/info"
	"github.com/aqa-alex/selenwright/jsonerror"
	"golang.org/x/sync/semaphore"
)

// Queue bounds the number of concurrent browser sessions and exposes a
// handful of counters for /status. It is the rewrite described in PR #12
// (finding H9 in the security review): the previous channel-of-struct{}
// implementation used Try/Check in a TOCTOU style that could lose queued
// tokens on client disconnect and mis-report Queued/Pending under load.
type Queue struct {
	disabled bool
	limit    *semaphore.Weighted
	size     int64

	used    atomic.Int64
	pending atomic.Int64
	queued  atomic.Int64
}

// New returns a Queue that allows up to size concurrent in-flight
// sessions. When disabled is true, Check short-circuits requests with a
// 500 once the queue is full instead of letting them wait.
func New(size int, disabled bool) *Queue {
	return &Queue{
		disabled: disabled,
		limit:    semaphore.NewWeighted(int64(size)),
		size:     int64(size),
	}
}

// Try responds with 429 immediately when the client set
// X-Selenwright-No-Wait and the queue is already at capacity. Otherwise
// it hands the request off to the next handler, which will usually be
// wrapped in Protect and block until a slot frees up.
func (q *Queue) Try(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, noWait := r.Header["X-Selenwright-No-Wait"]
		if noWait && !q.hasFreeSlot() {
			jsonerror.UnknownError(errors.New(http.StatusText(http.StatusTooManyRequests))).Encode(w)
			return
		}
		next.ServeHTTP(w, r)
	}
}

// Check rejects the request when the queue is full AND the operator
// disabled waiting via -disable-queue. Without -disable-queue a full
// queue falls through to Protect's blocking acquire.
func (q *Queue) Check(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if q.disabled && !q.hasFreeSlot() {
			user, remote := info.RequestInfo(r)
			log.Printf("[-] [QUEUE_IS_FULL] [%s] [%s]", user, remote)
			jsonerror.UnknownError(errors.New("queue is full")).Encode(w)
			return
		}
		next.ServeHTTP(w, r)
	}
}

// Protect blocks until a slot is available (respecting request context
// cancellation) and transitions the request from queued to pending. On
// successful acquire the request owns a slot until Drop or Release frees
// it.
func (q *Queue) Protect(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, remote := info.RequestInfo(r)
		log.Printf("[-] [NEW_REQUEST] [%s] [%s]", user, remote)
		start := time.Now()

		q.queued.Add(1)
		err := q.limit.Acquire(r.Context(), 1)
		q.queued.Add(-1)
		if err != nil {
			log.Printf("[-] [CLIENT_DISCONNECTED] [%s] [%s] [%s]", user, remote, time.Since(start))
			return
		}
		q.pending.Add(1)

		log.Printf("[-] [NEW_REQUEST_ACCEPTED] [%s] [%s]", user, remote)
		next.ServeHTTP(w, r)
	}
}

// Drop is called when session creation fails after the slot was acquired
// in Protect: the slot must be released and the pending counter
// decremented. Safe to call at most once per successful Protect entry.
func (q *Queue) Drop() {
	q.pending.Add(-1)
	q.limit.Release(1)
}

// Create is called when a session has been successfully created: the
// slot stays acquired until Release, and the pending counter is moved
// over to used.
func (q *Queue) Create() {
	q.pending.Add(-1)
	q.used.Add(1)
}

// Release is called when a running session ends: the slot is returned
// to the pool and the used counter is decremented.
func (q *Queue) Release() {
	q.used.Add(-1)
	q.limit.Release(1)
}

// Used returns the number of slots currently held by running sessions.
func (q *Queue) Used() int { return int(q.used.Load()) }

// Pending returns the number of acquired slots not yet transitioned to
// used — i.e. sessions whose containers are still starting.
func (q *Queue) Pending() int { return int(q.pending.Load()) }

// Queued returns the number of waiters currently blocked inside Protect
// waiting for a slot.
func (q *Queue) Queued() int { return int(q.queued.Load()) }

// hasFreeSlot is a non-blocking peek. It briefly acquires and releases a
// slot — if that succeeds the queue had capacity at that instant. It is
// NOT a guarantee that the next Acquire will not block; callers use this
// only for fast-path rejections (Try / Check).
func (q *Queue) hasFreeSlot() bool {
	if !q.limit.TryAcquire(1) {
		return false
	}
	q.limit.Release(1)
	return true
}
