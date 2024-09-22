package coro

import (
	"time"

	chrono "github.com/nnikolash/go-chrono"
)

type Clock interface {
	Now() time.Time
	Since(t time.Time) time.Duration
	Until(t time.Time) time.Duration
	After(d time.Duration, f func(ctx Context)) chrono.Timer
	Every(d time.Duration, f func(ctx Context)) chrono.Ticker
	Sleep(d time.Duration)
	SleepUntil(t time.Time)
	Wait(condition func())
}

func newClock(loop EventLoop, ctrl *YieldController) *clockT {
	return &clockT{
		loop: loop,
		ctrl: ctrl,
	}
}

type clockT struct {
	loop EventLoop
	ctrl *YieldController
}

func (t *clockT) Now() time.Time {
	return t.loop.Clock().Now()
}

func (t *clockT) Since(t0 time.Time) time.Duration {
	return t.loop.Clock().Since(t0)
}

func (t *clockT) Until(t0 time.Time) time.Duration {
	return t.loop.Clock().Until(t0)
}

func (t *clockT) After(d time.Duration, f func(ctx Context)) chrono.Timer {
	c := MakeCoroutine(t.loop, f)

	return t.loop.Clock().AfterFunc(d, func(now time.Time) { c() })
}

func (t *clockT) Every(d time.Duration, f func(ctx Context)) chrono.Ticker {
	c := MakeCoroutine(t.loop, f)

	return t.loop.Clock().EveryFunc(d, func(now time.Time) bool {
		c()
		return true
	})
}

func (t *clockT) Sleep(d time.Duration) {
	t.loop.Clock().AfterFunc(d, func(now time.Time) {
		t.ctrl.RunUntilYielded()
	})
	t.ctrl.Yield()
}

func (t *clockT) SleepUntil(t0 time.Time) {
	t.loop.Clock().AfterFunc(t.loop.Clock().Until(t0), func(now time.Time) {
		t.ctrl.RunUntilYielded()
	})
	t.ctrl.Yield()
}

func (t *clockT) Wait(condition func()) {
	go func() {
		condition()
		t.ctrl.RunUntilYielded()
	}()

	t.ctrl.Yield()
}
