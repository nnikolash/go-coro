package coro

import (
	"time"

	chrono "github.com/nnikolash/go-chrono"
)

type Context interface {
	Clock
	Go(f func(ctx Context))
	// Spawn is the Scheduler-flavored equivalent of Go. It exists so that
	// coro.Context satisfies coro.Scheduler without needing an adapter — pass
	// `ctx` directly to any function expecting a Scheduler. Internally Spawn
	// just delegates to Go.
	Spawn(f func(s Scheduler))
	Pause()
	Resume()
}

func newContext(eventLoop EventLoop, ctrl *YieldController) *contextT {
	clk := newClock(eventLoop, ctrl)
	ctx := &contextT{
		Clock: clk,
		ctrl:  ctrl,
		clock: eventLoop.Clock(),
	}
	// Establish the back-pointer so clockT resume closures can call
	// ctx.runUntilYielded() (enter-sites 2–4).
	clk.ctx = ctx
	if t, ok := eventLoop.(currentTracker); ok {
		ctx.tracker = t
	}
	return ctx
}

type contextT struct {
	Clock
	ctrl    *YieldController
	clock   chrono.Clock
	tracker currentTracker // nil when evtLoop doesn't implement currentTracker
}

var _ Context = &contextT{}

func (c *contextT) Go(f func(ctx Context)) {
	c.After(0, f)
}

func (c *contextT) Spawn(f func(s Scheduler)) {
	c.Go(func(child Context) { f(child) })
}

func (c *contextT) Pause() {
	c.ctrl.Yield()
}

// runUntilYielded resumes this coroutine and, if a currentTracker is available,
// wraps the call with save/restore of the active-coroutine pointer. This is the
// shared implementation for all enter-sites (clock resume closures + Resume).
func (c *contextT) runUntilYielded() {
	if c.tracker != nil {
		prev := c.tracker.setCurrentCoroutine(c)
		c.ctrl.RunUntilYielded()
		c.tracker.setCurrentCoroutine(prev)
	} else {
		c.ctrl.RunUntilYielded()
	}
}

func (c *contextT) Resume() {
	// Enter-site 5/5: explicit Resume. Scheduled as an AfterFunc(0) so it
	// runs on the pump goroutine rather than the caller's goroutine.
	c.clock.AfterFunc(0, func(now time.Time) {
		c.runUntilYielded()
	})
}

func (c *contextT) Ctrl() *YieldController {
	return c.ctrl
}

func Callback(evtLoop EventLoop, cb func(ctx Context)) func() {
	return func() {
		evtLoop.AddTask(func(ctx Context) {
			cb(ctx)
		})
	}
}

func Callback1[Arg any](evtLoop EventLoop, cb func(ctx Context, arg Arg)) func(arg Arg) {
	return func(arg Arg) {
		evtLoop.AddTask(func(ctx Context) {
			cb(ctx, arg)
		})
	}
}

func Callback2[Arg1, Arg2 any](evtLoop EventLoop, cb func(ctx Context, arg1 Arg1, arg2 Arg2)) func(arg1 Arg1, arg2 Arg2) {
	return func(arg1 Arg1, arg2 Arg2) {
		evtLoop.AddTask(func(ctx Context) {
			cb(ctx, arg1, arg2)
		})
	}
}
