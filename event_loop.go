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
		live:  map[*YieldController]struct{}{},
	}
}

type eventLoopT struct {
	clock chrono.Clock

	liveMu sync.Mutex
	live   map[*YieldController]struct{}
}

var _ EventLoop = &eventLoopT{}
var _ coroutineRegistry = &eventLoopT{}

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
