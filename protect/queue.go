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

type Queue struct {
	disabled bool
	limit    *semaphore.Weighted
	size     int64

	used    atomic.Int64
	pending atomic.Int64
	queued  atomic.Int64
}

func New(size int, disabled bool) *Queue {
	return &Queue{
		disabled: disabled,
		limit:    semaphore.NewWeighted(int64(size)),
		size:     int64(size),
	}
}

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

func (q *Queue) Drop() {
	q.pending.Add(-1)
	q.limit.Release(1)
}

func (q *Queue) Create() {
	q.pending.Add(-1)
	q.used.Add(1)
}

func (q *Queue) Release() {
	q.used.Add(-1)
	q.limit.Release(1)
}

func (q *Queue) Used() int { return int(q.used.Load()) }

func (q *Queue) Pending() int { return int(q.pending.Load()) }

func (q *Queue) Queued() int { return int(q.queued.Load()) }

func (q *Queue) hasFreeSlot() bool {
	if !q.limit.TryAcquire(1) {
		return false
	}
	q.limit.Release(1)
	return true
}
