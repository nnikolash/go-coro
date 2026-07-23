package coro

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	chrono "github.com/nnikolash/go-chrono"
)

// panicSleepOutsideCoroutine is the shared panic message for Sleep called
// outside a coroutine. Reused by loopScheduler.Sleep and eventLoopT.Sleep /
// SleepUntil so that all "no current coroutine" paths surface the same text.
const panicSleepOutsideCoroutine = "coro.NewLoopScheduler: Sleep is only valid inside a coroutine; pass the coroutine's Context there"

// panicSleepUntilOutsideCoroutine is the panic message for loopScheduler.SleepUntil.
// Kept distinct from panicSleepOutsideCoroutine so that existing test assertions
// on the exact message continue to pass.
const panicSleepUntilOutsideCoroutine = "coro.NewLoopScheduler: SleepUntil is only valid inside a coroutine; pass the coroutine's Context there"

type EventLoop interface {
	Clock() chrono.Clock
	AddTask(task func(ctx Context)) chrono.Timer
	AddDelayedTask(d time.Duration, task func(ctx Context)) chrono.Timer
	AddPlannedTask(t time.Time, task func(ctx Context)) chrono.Timer
	AddPlannedTaskCtx(ctx context.Context, t time.Time, task func(ctx Context))
}

var DefaultEventLoop = NewEventLoop(chrono.DefaultClock)

func AddTask(task func(ctx Context)) chrono.Timer {
	return DefaultEventLoop.AddTask(task)
}

func AddDelayedTask(d time.Duration, task func(ctx Context)) chrono.Timer {
	return DefaultEventLoop.AddDelayedTask(d, task)
}

func AddPlannedTask(t time.Time, task func(ctx Context)) chrono.Timer {
	return DefaultEventLoop.AddPlannedTask(t, task)
}

func NewEventLoop(clock chrono.Clock) *eventLoopT {
	return &eventLoopT{
		clock: clock,
		live:  map[*YieldController]struct{}{},
	}
}

type eventLoopT struct {
	clock chrono.Clock

	// current is the *contextT of the coroutine that is actively running on
	// this loop. It is nil when the pump is between coroutines. Under
	// chrono.Simulator (cooperative, single-threaded) this pointer is
	// well-defined at all times. Under chrono.RealClock concurrent resumes
	// can race; the atomic ensures no data-race but the value is semantically
	// undefined — do not rely on it there.
	current atomic.Pointer[contextT]

	liveMu sync.Mutex
	live   map[*YieldController]struct{}

	escapeHandlerMu sync.RWMutex
	escapeHandler   func(any)
}

var _ EventLoop = &eventLoopT{}
var _ coroutineRegistry = &eventLoopT{}
var _ currentTracker = &eventLoopT{}

func (e *eventLoopT) Clock() chrono.Clock {
	return e.clock
}

func (e *eventLoopT) registerCoroutine(ctrl *YieldController) {
	e.liveMu.Lock()
	e.live[ctrl] = struct{}{}
	e.liveMu.Unlock()
}

func (e *eventLoopT) unregisterCoroutine(ctrl *YieldController) {
	e.liveMu.Lock()
	delete(e.live, ctrl)
	e.liveMu.Unlock()
}

// setCurrentCoroutine stores ctx as the active coroutine and returns the
// previous value. Part of the currentTracker interface; used at each enter-site
// with save/restore semantics.
func (e *eventLoopT) setCurrentCoroutine(ctx *contextT) *contextT {
	return e.current.Swap(ctx)
}

// Sleep suspends the currently-running coroutine for duration d, delegating to
// the coroutine's own Clock.Sleep. Panics if called outside a coroutine.
// Under chrono.Simulator this is deterministic; under RealClock the behaviour
// is undefined (see field current).
func (e *eventLoopT) Sleep(d time.Duration) {
	cur := e.current.Load()
	if cur == nil {
		panic(panicSleepOutsideCoroutine)
	}
	cur.Sleep(d)
}

// SleepUntil suspends the currently-running coroutine until the given wall time,
// delegating to the coroutine's own Clock.SleepUntil. Panics outside a coroutine.
func (e *eventLoopT) SleepUntil(t time.Time) {
	cur := e.current.Load()
	if cur == nil {
		panic(panicSleepUntilOutsideCoroutine)
	}
	cur.SleepUntil(t)
}

// CurrentScheduler returns the Scheduler of the currently-running coroutine, or
// nil when the pump is between coroutines. Under chrono.RealClock the value is
// racy and should not be used.
func (e *eventLoopT) CurrentScheduler() Scheduler {
	cur := e.current.Load()
	if cur == nil {
		return nil
	}
	return cur
}

// SetEscapeHandler registers fn as the handler to call when a coroutine on
// this loop calls coro.Escape. The handler is invoked from within the
// coroutine's goroutine after its stack has been fully unwound (all defers
// have run); it must therefore be non-blocking — in particular, it must not
// synchronously wait for the event loop (which is still blocked in
// WaitUntilYielded at that point). Calling a goroutine-launching helper like
// requestReinit is safe.
//
// SetEscapeHandler may be called before any coroutine starts. Calling it a
// second time replaces the previous handler. Passing nil removes the handler;
// Escape without a handler re-panics (fail-loud).
func (e *eventLoopT) SetEscapeHandler(fn func(any)) {
	e.escapeHandlerMu.Lock()
	defer e.escapeHandlerMu.Unlock()
	e.escapeHandler = fn
}

// getEscapeHandler returns the currently registered escape handler, or nil.
// Implements escapeHandlerProvider (used by RunCoroutine via type assertion).
func (e *eventLoopT) getEscapeHandler() func(any) {
	e.escapeHandlerMu.RLock()
	defer e.escapeHandlerMu.RUnlock()
	return e.escapeHandler
}

// Close tears down every coroutine that is still suspended on this loop and
// returns how many were torn down. A suspended coroutine is a goroutine parked
// on sync.Cond.Wait(); without Close it stays blocked forever once the
// simulation stops resuming it (e.g. ProcessAllUntil cutoff, context cancel, or
// end-of-data mid-Sleep). Call Close after the simulation has stopped — i.e.
// when no coroutine is running — typically deferred right after constructing the
// loop, or at the end of each backtest in a long-lived process that runs many.
//
// Each torn-down coroutine's deferred cleanup runs (the cancel is delivered as a
// panic that unwinds its stack). Do NOT blanket-recover() in coroutine code, or
// the teardown sentinel will be swallowed and the goroutine will leak.
func (e *eventLoopT) Close() int {
	e.liveMu.Lock()
	snapshot := make([]*YieldController, 0, len(e.live))
	for ctrl := range e.live {
		snapshot = append(snapshot, ctrl)
	}
	e.liveMu.Unlock()

	torndown := 0
	for _, ctrl := range snapshot {
		if ctrl.Cancel() {
			torndown++
		}
	}

	return torndown
}

func (e *eventLoopT) AddTask(task func(ctx Context)) chrono.Timer {
	return e.AddDelayedTask(0, task)
}

func (e *eventLoopT) AddDelayedTask(d time.Duration, task func(ctx Context)) chrono.Timer {
	c := MakeCoroutine(e, task)
	timer := e.clock.AfterFunc(d, func(now time.Time) { c() })

	return timer
}

func (e *eventLoopT) AddPlannedTask(t time.Time, task func(ctx Context)) chrono.Timer {
	c := MakeCoroutine(e, task)
	timer := e.clock.UntilFunc(t, func(now time.Time) { c() })

	return timer
}

func (e *eventLoopT) AddPlannedTaskCtx(ctx context.Context, t time.Time, task func(ctx Context)) {
	ctx, cancel := context.WithCancel(ctx)

	var once sync.Once
	taskOnce := func(ctx Context) {
		once.Do(func() {
			cancel()
			task(ctx)
		})
	}

	e.AddPlannedTask(t, taskOnce)

	go func() {
		<-ctx.Done()
		e.AddTask(taskOnce)
	}()
}
