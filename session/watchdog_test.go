package session

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	require "github.com/stretchr/testify/require"
)

func TestWatchdogTimeoutFiresOnce(t *testing.T) {
	var calls atomic.Int32
	done := callbackDone(&calls)

	watchdog := NewWatchdog(20*time.Millisecond, done.callback)
	watchdog.Start()

	waitForCallback(t, done.ch)
	time.Sleep(40 * time.Millisecond)

	require.Equal(t, int32(1), calls.Load())
	require.False(t, watchdog.Touch())
	require.False(t, watchdog.Stop())
	require.False(t, watchdog.Expire())
}

func TestWatchdogTouchDelaysExpiration(t *testing.T) {
	var calls atomic.Int32
	done := callbackDone(&calls)

	watchdog := NewWatchdog(40*time.Millisecond, done.callback)
	watchdog.Start()

	time.Sleep(15 * time.Millisecond)
	require.True(t, watchdog.Touch())

	time.Sleep(15 * time.Millisecond)
	require.True(t, watchdog.Touch())

	time.Sleep(20 * time.Millisecond)
	require.Zero(t, calls.Load())

	waitForCallback(t, done.ch)
	time.Sleep(20 * time.Millisecond)

	require.Equal(t, int32(1), calls.Load())
}

func TestWatchdogStopCancelsExpiration(t *testing.T) {
	var calls atomic.Int32
	done := callbackDone(&calls)

	watchdog := NewWatchdog(20*time.Millisecond, done.callback)
	watchdog.Start()

	require.True(t, watchdog.Stop())
	require.False(t, watchdog.Stop())

	time.Sleep(50 * time.Millisecond)

	require.Zero(t, calls.Load())
	require.False(t, watchdog.Touch())
	require.False(t, watchdog.Expire())
}

func TestWatchdogExpireFiresImmediately(t *testing.T) {
	var calls atomic.Int32
	done := callbackDone(&calls)

	watchdog := NewWatchdog(time.Second, done.callback)
	watchdog.Start()

	require.True(t, watchdog.Expire())
	waitForCallback(t, done.ch)
	time.Sleep(20 * time.Millisecond)

	require.Equal(t, int32(1), calls.Load())
	require.False(t, watchdog.Expire())
	require.False(t, watchdog.Touch())
	require.False(t, watchdog.Stop())
}

func TestWatchdogConcurrentTouchAndStop(t *testing.T) {
	var calls atomic.Int32
	done := callbackDone(&calls)

	watchdog := NewWatchdog(15*time.Millisecond, done.callback)
	watchdog.Start()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if !watchdog.Touch() {
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond)
		watchdog.Stop()
	}()

	wg.Wait()
	time.Sleep(40 * time.Millisecond)

	require.True(t, calls.Load() <= 1)
}

func TestWatchdogDoesNotRunBeforeStart(t *testing.T) {
	var calls atomic.Int32
	done := callbackDone(&calls)

	watchdog := NewWatchdog(20*time.Millisecond, done.callback)

	time.Sleep(40 * time.Millisecond)
	require.Zero(t, calls.Load())

	watchdog.Start()
	waitForCallback(t, done.ch)
	require.Equal(t, int32(1), calls.Load())
}

type callbackSignal struct {
	callback func()
	ch       <-chan struct{}
}

func callbackDone(calls *atomic.Int32) callbackSignal {
	done := make(chan struct{})
	var once sync.Once

	return callbackSignal{
		callback: func() {
			calls.Add(1)
			once.Do(func() {
				close(done)
			})
		},
		ch: done,
	}
}

func waitForCallback(t *testing.T, done <-chan struct{}) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchdog callback did not fire")
	}
}
