package event

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func resetRegistry() {
	reg.mu.Lock()
	reg.file = nil
	reg.session = nil
	reg.mu.Unlock()
}

type fileListener struct {
	fn func(CreatedFile)
}

func (l *fileListener) OnFileCreated(cf CreatedFile) { l.fn(cf) }

type sessionListener struct {
	fn func(StoppedSession)
}

func (l *sessionListener) OnSessionStopped(ss StoppedSession) { l.fn(ss) }

func TestConcurrentAddAndPublish(t *testing.T) {
	resetRegistry()

	var fired int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				AddFileCreatedListener(&fileListener{fn: func(CreatedFile) {
					atomic.AddInt64(&fired, 1)
				}})
			}
		}
	}()

	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				FileCreated(CreatedFile{Name: "x"})
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

func TestSlowListenerDoesNotBlockFastOnes(t *testing.T) {
	resetRegistry()

	var fastSeen int64
	hangRelease := make(chan struct{})
	defer close(hangRelease)

	slow := &fileListener{fn: func(CreatedFile) {
		<-hangRelease
	}}
	fast := &fileListener{fn: func(CreatedFile) {
		atomic.AddInt64(&fastSeen, 1)
	}}
	AddFileCreatedListener(slow)
	AddFileCreatedListener(fast)

	FileCreated(CreatedFile{Name: "payload"})

	deadline := time.After(time.Second)
	for atomic.LoadInt64(&fastSeen) == 0 {
		select {
		case <-deadline:
			t.Fatal("fast listener never observed event")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestInitIfNeededRunsOnce(t *testing.T) {
	var initCount int32
	l := &initOnceListener{onInit: func() { atomic.AddInt32(&initCount, 1) }}
	AddFileCreatedListener(l)
	if got := atomic.LoadInt32(&initCount); got != 1 {
		t.Fatalf("Init called %d times, want 1", got)
	}
}

type initOnceListener struct {
	onInit func()
}

func (l *initOnceListener) Init()                           { l.onInit() }
func (l *initOnceListener) OnFileCreated(cf CreatedFile)    {}
func (l *initOnceListener) OnSessionStopped(StoppedSession) {}

func TestShutdownBeforeStartIsNoOp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown without StartPool should be no-op, got %v", err)
	}
}

func TestShutdownDrainsPendingWork(t *testing.T) {
	resetRegistry()
	StartPool(2, 4)

	var processed int64
	var wg sync.WaitGroup
	wg.Add(5)
	for i := 0; i < 5; i++ {
		AddFileCreatedListener(&fileListener{fn: func(CreatedFile) {
			time.Sleep(5 * time.Millisecond)
			atomic.AddInt64(&processed, 1)
			wg.Done()
		}})
	}
	FileCreated(CreatedFile{Name: "x"})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	wg.Wait()
	if got := atomic.LoadInt64(&processed); got != 5 {
		t.Fatalf("processed %d events, want 5", got)
	}
}
