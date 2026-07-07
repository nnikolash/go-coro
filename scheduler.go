package coro

import (
	"time"
)

// Scheduler is a narrow, library-agnostic facade over coro.Context. Libraries
// that only need a few common time/scheduling operations should depend on
// Scheduler instead of coro.Context to stay decoupled from the rest of
// go-coro's API (Pause/Resume, raw YieldController, Callback factories…).
//
// coro.Context implements Scheduler directly — any function that takes a
// coro.Scheduler can be called with a coro.Context. The opposite is not true:
// a Scheduler does not expose Pause/Resume/Go(Context).
//
// Two non-Context implementations ship in this package:
//
//   - NewInlineScheduler(now): synchronous implementation for unit tests that
//     do not care about virtual time. Sleep/SleepUntil advance an internal
//     clock without blocking, Spawn invokes f immediately on the same
//     goroutine.
//   - NewLoopScheduler(loop): Scheduler bound to an EventLoop for code that
//     wants to schedule work from outside any coroutine (signal handlers,
//     gRPC servers, etc.). Sleep panics — there's no coroutine to yield from.
//
// Migration guidance: a library that takes `ctx coro.Context` today can switch
// its public signature to `s coro.Scheduler` without changing the call site —
// callers just keep passing the same ctx.
type Scheduler interface {
	Now() time.Time
	Since(t time.Time) time.Duration
	Until(t time.Time) time.Duration
	Sleep(d time.Duration)
	SleepUntil(t time.Time)
	// Spawn starts f as a new child coroutine. f receives its own Scheduler.
	//
	// Spawn is the Scheduler-flavored counterpart of Context.Go. They share the
	// same scheduling semantics; the only difference is the callback type — so
	// libraries that don't want to depend on coro.Context can stay on Spawn.
	Spawn(f func(s Scheduler))
}

// NewLoopScheduler returns a Scheduler that submits work to the given event
// loop. Sleep/SleepUntil panic — there is no coroutine context to yield from.
// Use this when external code (signal handlers, gRPC servers, etc.) needs to
// schedule coroutines without having a coro.Context in hand.
func NewLoopScheduler(loop EventLoop) Scheduler {
	return &loopScheduler{loop: loop}
}

type loopScheduler struct {
	loop EventLoop
}

func (s *loopScheduler) Now() time.Time                  { return s.loop.Clock().Now() }
func (s *loopScheduler) Since(t time.Time) time.Duration { return s.loop.Clock().Since(t) }
func (s *loopScheduler) Until(t time.Time) time.Duration { return s.loop.Clock().Until(t) }
func (s *loopScheduler) Sleep(time.Duration)             { panic(panicSleepOutsideCoroutine) }
func (s *loopScheduler) SleepUntil(time.Time)            { panic(panicSleepUntilOutsideCoroutine) }
func (s *loopScheduler) Spawn(f func(Scheduler)) {
	s.loop.AddTask(func(c Context) { f(c) })
}

// NewInlineScheduler returns a Scheduler suitable for unit tests of code that
// only needs the time/sleep/spawn API. It runs entirely on the caller's
// goroutine — no event loop, no goroutines, no sync.Cond.
//
// Semantics:
//   - Now() / Since() / Until() are driven by an internal clock that starts at
//     initialNow and is only advanced by Sleep/SleepUntil.
//   - Sleep(d) instantly advances the clock by d (no real blocking).
//   - SleepUntil(t) instantly advances the clock to t if t is in the future.
//   - Spawn(f) invokes f immediately on the current goroutine with a child
//     InlineScheduler sharing the same clock. f runs to completion before
//     Spawn returns, mirroring the synchronous semantics tests usually want.
//
// Use this in tests where you want to assert on logic without spinning up an
// EventLoop / chrono.Simulator and without writing `if ctx == nil` branches in
// production code.
func NewInlineScheduler(initialNow time.Time) Scheduler {
	shared := &inlineClock{now: initialNow}
	return &inlineScheduler{clock: shared}
}

type inlineClock struct {
	now time.Time
}

type inlineScheduler struct {
	clock *inlineClock
}

func (s *inlineScheduler) Now() time.Time                  { return s.clock.now }
func (s *inlineScheduler) Since(t time.Time) time.Duration { return s.clock.now.Sub(t) }
func (s *inlineScheduler) Until(t time.Time) time.Duration { return t.Sub(s.clock.now) }

func (s *inlineScheduler) Sleep(d time.Duration) {
	if d > 0 {
		s.clock.now = s.clock.now.Add(d)
	}
}

func (s *inlineScheduler) SleepUntil(t time.Time) {
	if t.After(s.clock.now) {
		s.clock.now = t
	}
}

func (s *inlineScheduler) Spawn(f func(Scheduler)) {
	f(&inlineScheduler{clock: s.clock})
}
