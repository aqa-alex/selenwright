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

type Watchdog struct {
	timeout  time.Duration
	onExpire func()

	state    atomic.Int32
	deadline atomic.Int64

	ctx       context.Context
	cancel    context.CancelFunc
	fireOnce  sync.Once
	startOnce sync.Once
}

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
	return w
}

func (w *Watchdog) Start() {
	if w == nil {
		return
	}
	w.startOnce.Do(func() {
		w.deadline.Store(time.Now().Add(w.timeout).UnixNano())
		go w.run()
	})
}

func (w *Watchdog) Touch() bool {
	if w.state.Load() != watchdogActive {
		return false
	}
	w.deadline.Store(time.Now().Add(w.timeout).UnixNano())
	return true
}

func (w *Watchdog) Stop() bool {
	if !w.state.CompareAndSwap(watchdogActive, watchdogStopped) {
		return false
	}
	w.cancel()
	return true
}

func (w *Watchdog) Expire() bool {
	if !w.state.CompareAndSwap(watchdogActive, watchdogExpired) {
		return false
	}
	w.cancel()
	w.fire()
	return true
}

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
		case <-w.ctx.Done():
			timer.Stop()
			return
		}
	}
}
