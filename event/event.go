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
