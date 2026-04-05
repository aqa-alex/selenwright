package session

import (
	"sync"
	"time"
)

type watchdogState uint8

const (
	watchdogStateActive watchdogState = iota
	watchdogStateStopped
	watchdogStateExpired
)

// Watchdog - protocol-agnostic idle timeout controller.
type Watchdog struct {
	lock     sync.Mutex
	timeout  time.Duration
	timer    *time.Timer
	done     chan struct{}
	onExpire func()
	state    watchdogState
}

// NewWatchdog - create a watchdog that expires after timeout.
func NewWatchdog(timeout time.Duration, onExpire func()) *Watchdog {
	if onExpire == nil {
		onExpire = func() {}
	}

	w := &Watchdog{
		timeout:  timeout,
		timer:    time.NewTimer(timeout),
		done:     make(chan struct{}),
		onExpire: onExpire,
	}
	go w.run()

	return w
}

// Touch - delay expiration for another timeout interval.
func (w *Watchdog) Touch() bool {
	w.lock.Lock()
	defer w.lock.Unlock()

	if w.state != watchdogStateActive {
		return false
	}
	if !w.timer.Stop() {
		return false
	}

	w.timer.Reset(w.timeout)
	return true
}

// Stop - cancel the watchdog and prevent future expiration.
func (w *Watchdog) Stop() bool {
	w.lock.Lock()
	defer w.lock.Unlock()

	if w.state != watchdogStateActive {
		return false
	}

	w.state = watchdogStateStopped
	if w.timer != nil {
		w.timer.Stop()
	}
	close(w.done)

	return true
}

// Expire - fire the expiration callback immediately.
func (w *Watchdog) Expire() bool {
	callback := w.transitionToExpired()
	if callback == nil {
		return false
	}

	callback()
	return true
}

func (w *Watchdog) run() {
	select {
	case <-w.timer.C:
	case <-w.done:
		return
	}

	callback := w.transitionToExpired()
	if callback == nil {
		return
	}

	callback()
}

func (w *Watchdog) transitionToExpired() func() {
	w.lock.Lock()
	defer w.lock.Unlock()

	if w.state != watchdogStateActive {
		return nil
	}

	w.state = watchdogStateExpired
	if w.timer != nil {
		w.timer.Stop()
	}
	close(w.done)

	return w.onExpire
}
