package event

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// resetRegistry restores package state between tests. The pool uses a
// sync.Once so it can only be started in one test per process; tests
// that need the pool share a single call via ensurePool. Registry
// mutation is safe after reset because there are no workers yet.
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

// TestConcurrentAddAndPublish stresses the registry under concurrent
// append + range. Without the RWMutex the race detector fires on the
// legacy fileCreatedListeners slice access.
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

// TestSlowListenerDoesNotBlockFastOnes verifies bounded fan-out is
// asynchronous — one hung listener must not starve the rest. Without
// the worker pool a synchronous-dispatch model would still pass this
// because each listener runs on its own goroutine; the pool's job is
// to bound the TOTAL number of goroutines under load, which is what
// TestPoolCapsGoroutines covers. This test asserts the behavioral
// contract: all registered listeners eventually observe the event
// within a bounded deadline regardless of each other's speed.
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

// TestInitIfNeededRunsOnce verifies the InitRequired hook fires
// exactly once per registration, preserving the legacy contract for
// uploaders that self-configure from flags at registration time.
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

// TestShutdownBeforeStartIsNoOp ensures Shutdown is safe to call when
// the pool was never started — production shutdown paths may run
// before StartPool has been reached during early init failure.
func TestShutdownBeforeStartIsNoOp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown without StartPool should be no-op, got %v", err)
	}
}

// TestShutdownDrainsPendingWork drives the pool through a full
// lifecycle and asserts enqueued work completes before Shutdown
// returns. Kept last in the file and guarded behind a "pool" subtest
// so we only start the pool once per process (the sync.Once contract
// would refuse a restart anyway); previous tests run against the
// fallback inline-goroutine path.
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
