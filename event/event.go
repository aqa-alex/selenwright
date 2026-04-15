package event

import (
	"context"
	"sync"

	"github.com/aqa-alex/selenwright/session"
)

const (
	defaultWorkers = 16
	defaultBufSize = 64
)

type registry struct {
	mu      sync.RWMutex
	file    []FileCreatedListener
	session []SessionStoppedListener
}

var (
	reg      registry
	poolOnce sync.Once
	poolWork chan func()
	poolWG   sync.WaitGroup
	poolStop chan struct{}
	stopOnce sync.Once
)

type InitRequired interface {
	Init()
}

type Event struct {
	RequestId uint64
	SessionId string
	Session   *session.Session
}

type CreatedFile struct {
	Event
	Name string
	Type string
}

type FileCreatedListener interface {
	OnFileCreated(createdFile CreatedFile)
}

type StoppedSession struct {
	Event
}

type SessionStoppedListener interface {
	OnSessionStopped(stoppedSession StoppedSession)
}

// StartPool starts workers goroutines that consume listener
// invocations from a buffered channel. Workers < 1 or bufSize < 1
// fall back to the package defaults (16 workers, 64-slot buffer).
// Safe to call once per process; additional calls are no-ops so
// tests that re-import the package don't fight over the pool. Without
// a call, FileCreated/SessionStopped fall back to spawning one
// goroutine per listener — the legacy unbounded fan-out behavior.
func StartPool(workers, bufSize int) {
	poolOnce.Do(func() {
		if workers < 1 {
			workers = defaultWorkers
		}
		if bufSize < 1 {
			bufSize = defaultBufSize
		}
		poolWork = make(chan func(), bufSize)
		poolStop = make(chan struct{})
		poolWG.Add(workers)
		for i := 0; i < workers; i++ {
			go func() {
				defer poolWG.Done()
				for job := range poolWork {
					job()
				}
			}()
		}
	})
}

// Shutdown closes the work channel, waits for workers to drain any
// enqueued jobs, and returns once either all work finishes or ctx
// expires. After Shutdown begins, publish falls back to the legacy
// inline-goroutine path so late events from orphaned handler
// goroutines still reach their listeners. Idempotent.
func Shutdown(ctx context.Context) error {
	if poolWork == nil {
		return nil
	}
	stopOnce.Do(func() {
		close(poolStop)
		close(poolWork)
	})
	done := make(chan struct{})
	go func() {
		poolWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// publish dispatches a listener invocation through the pool. If the
// pool has not been started (e.g. unit tests) or shutdown has been
// initiated, fall back to a one-off goroutine so callers never
// observe a silent drop. This matches the upstream behavior.
func publish(job func()) {
	if poolWork == nil {
		go job()
		return
	}
	select {
	case <-poolStop:
		go job()
		return
	default:
	}
	select {
	case poolWork <- job:
	case <-poolStop:
		go job()
	}
}

func FileCreated(createdFile CreatedFile) {
	reg.mu.RLock()
	listeners := append([]FileCreatedListener(nil), reg.file...)
	reg.mu.RUnlock()
	for _, l := range listeners {
		l := l
		publish(func() { l.OnFileCreated(createdFile) })
	}
}

func InitIfNeeded(listener interface{}) {
	if l, ok := listener.(InitRequired); ok {
		l.Init()
	}
}

func AddFileCreatedListener(listener FileCreatedListener) {
	InitIfNeeded(listener)
	reg.mu.Lock()
	reg.file = append(reg.file, listener)
	reg.mu.Unlock()
}

func SessionStopped(stoppedSession StoppedSession) {
	reg.mu.RLock()
	listeners := append([]SessionStoppedListener(nil), reg.session...)
	reg.mu.RUnlock()
	for _, l := range listeners {
		l := l
		publish(func() { l.OnSessionStopped(stoppedSession) })
	}
}

func AddSessionStoppedListener(listener SessionStoppedListener) {
	InitIfNeeded(listener)
	reg.mu.Lock()
	reg.session = append(reg.session, listener)
	reg.mu.Unlock()
}
