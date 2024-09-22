package coro

import (
	"context"
	"sync"
	"time"

	chrono "github.com/nnikolash/go-chrono"
)

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
	}
}

type eventLoopT struct {
	clock chrono.Clock
}

var _ EventLoop = &eventLoopT{}

func (e *eventLoopT) Clock() chrono.Clock {
	return e.clock
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
