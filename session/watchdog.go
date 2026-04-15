// Modified by [Aleksander R], 2026: added Playwright protocol support

package session

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

const (
	watchdogActive  int32 = 0
	watchdogStopped int32 = 1
	watchdogExpired int32 = 2
)

// Watchdog is a protocol-agnostic idle timeout controller. Callers
// call Touch whenever traffic arrives; if enough time passes without
// a Touch (or Expire is invoked manually) the onExpire callback fires
// exactly once. Stop cancels the watchdog without firing.
//
// The implementation is a small FSM over atomic.Int32 + context.Done,
// replacing the prior mutex/FSM/channel tuple. Each transition
// (Active → Stopped, Active → Expired) uses a compare-and-swap, so
// the three exported methods can race against each other and against
// the internal timer goroutine without locks.
type Watchdog struct {
	timeout  time.Duration
	onExpire func()

	state atomic.Int32
	// deadline is the time at which the callback should fire in the
	// absence of a Touch. Stored as Unix nanos through atomic.Int64
	// so Touch can shift the deadline forward without a lock. The
	// timer goroutine re-reads this value on every wake, so Touches
	// that land between reads are honored on the next cycle.
	deadline atomic.Int64

	ctx      context.Context
	cancel   context.CancelFunc
	fireOnce sync.Once
}

// NewWatchdog creates an active watchdog that expires after timeout
// of idle. onExpire may be nil.
func NewWatchdog(timeout time.Duration, onExpire func()) *Watchdog {
	if onExpire == nil {
		onExpire = func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &Watchdog{
		timeout:  timeout,
		onExpire: onExpire,
		ctx:      ctx,
		cancel:   cancel,
	}
	w.deadline.Store(time.Now().Add(timeout).UnixNano())
	go w.run()
	return w
}

// Touch delays expiration for another timeout interval. Returns true
// if the watchdog was still active, false if it had already been
// stopped or expired.
func (w *Watchdog) Touch() bool {
	if w.state.Load() != watchdogActive {
		return false
	}
	w.deadline.Store(time.Now().Add(w.timeout).UnixNano())
	return true
}

// Stop cancels the watchdog and prevents future expiration. Returns
// true on the transition Active → Stopped; false on every later call.
func (w *Watchdog) Stop() bool {
	if !w.state.CompareAndSwap(watchdogActive, watchdogStopped) {
		return false
	}
	w.cancel()
	return true
}

// Expire fires the expiration callback immediately. Returns true on
// the transition Active → Expired; false on every later call. Safe
// to race with a natural timer fire: whichever transition wins runs
// onExpire exactly once (via fireOnce).
func (w *Watchdog) Expire() bool {
	if !w.state.CompareAndSwap(watchdogActive, watchdogExpired) {
		return false
	}
	w.cancel()
	w.fire()
	return true
}

// fire runs onExpire at most once regardless of how many transitions
// reach the Expired state.
func (w *Watchdog) fire() {
	w.fireOnce.Do(w.onExpire)
}

func (w *Watchdog) run() {
	for {
		wait := time.Duration(w.deadline.Load() - time.Now().UnixNano())
		if wait <= 0 {
			if w.state.CompareAndSwap(watchdogActive, watchdogExpired) {
				w.cancel()
				w.fire()
			}
			return
		}
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
			// Deadline may have been pushed forward by a Touch that
			// landed after we computed wait; loop and re-read.
		case <-w.ctx.Done():
			timer.Stop()
			return
		}
	}
}
