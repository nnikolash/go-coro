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
	return &contextT{
		Clock: newClock(eventLoop, ctrl),
		ctrl:  ctrl,
		clock: eventLoop.Clock(),
	}
}

type contextT struct {
	Clock
	ctrl  *YieldController
	clock chrono.Clock
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

func (c *contextT) Resume() {
	c.clock.AfterFunc(0, func(now time.Time) {
		c.ctrl.RunUntilYielded()
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
