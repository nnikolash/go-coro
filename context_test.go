package coro_test

import (
	"context"
	"sync"
	"testing"
	"time"

	chrono "github.com/nnikolash/go-chrono"
	coro "github.com/nnikolash/go-coro"
	"github.com/stretchr/testify/require"
)

func TestContext_Go_SpawnsChild(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	var order []string

	loop.AddTask(func(ctx coro.Context) {
		order = append(order, "parent-start")

		ctx.Go(func(ctx coro.Context) {
			order = append(order, "child-1")
			ctx.Sleep(time.Second)
			order = append(order, "child-2")
		})

		ctx.Sleep(2 * time.Second)
		order = append(order, "parent-end")
	})

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)

	require.Equal(t, []string{"parent-start", "child-1", "child-2", "parent-end"}, order)
}

func TestContext_PauseResume_RoundTrip(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	var stored coro.Context
	var phases []string

	loop.AddTask(func(ctx coro.Context) {
		phases = append(phases, "before-pause")
		stored = ctx
		ctx.Pause()
		phases = append(phases, "after-resume")
	})

	loop.AddDelayedTask(time.Second, func(ctx coro.Context) {
		phases = append(phases, "resumer")
		stored.Resume()
	})

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)

	require.Equal(t, []string{"before-pause", "resumer", "after-resume"}, phases)
}

func TestContext_SleepUntil(t *testing.T) {
	t.Parallel()

	start := time.Unix(1000, 0)
	clock := chrono.NewSimulator(start)
	loop := coro.NewEventLoop(clock)

	var stopAt time.Time

	loop.AddTask(func(ctx coro.Context) {
		ctx.SleepUntil(start.Add(2 * time.Minute))
		stopAt = ctx.Now()
	})

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, start.Add(2*time.Minute), stopAt)
}

func TestContext_NowSinceUntil(t *testing.T) {
	t.Parallel()

	start := time.Unix(1000, 0)
	clock := chrono.NewSimulator(start)
	loop := coro.NewEventLoop(clock)

	loop.AddTask(func(ctx coro.Context) {
		require.Equal(t, start, ctx.Now())

		ctx.Sleep(10 * time.Second)

		require.Equal(t, start.Add(10*time.Second), ctx.Now())
		require.Equal(t, 10*time.Second, ctx.Since(start))
		require.Equal(t, 5*time.Second, ctx.Until(start.Add(15*time.Second)))
	})

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
}

func TestClock_After(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	var fired []string

	loop.AddTask(func(ctx coro.Context) {
		ctx.After(2*time.Second, func(ctx coro.Context) {
			fired = append(fired, "fired-at-2s")
		})
		ctx.Sleep(5 * time.Second)
		fired = append(fired, "parent-done")
	})

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"fired-at-2s", "parent-done"}, fired)
}

func TestClock_Every(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	var ticks []time.Time

	loop.AddTask(func(ctx coro.Context) {
		ticker := ctx.Every(time.Second, func(ctx coro.Context) {
			ticks = append(ticks, ctx.Now())
		})
		ctx.Sleep(3500 * time.Millisecond)
		ticker.Stop()
	})

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Len(t, ticks, 3)
}

func TestClock_Wait_RealClock(t *testing.T) {
	t.Parallel()

	// Wait spawns a raw goroutine and is only safe with the real clock.
	// Documented as such; this test pins the existing RealClock behavior.
	clock := chrono.NewRealClock()
	loop := coro.NewEventLoop(clock)

	done := make(chan struct{})
	resumed := false

	go func() {
		loop.AddTask(func(ctx coro.Context) {
			ctx.Wait(func() {
				time.Sleep(20 * time.Millisecond)
			})
			resumed = true
			close(done)
		})
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Wait did not resume in time")
	}
	require.True(t, resumed)
}

func TestCallback_NoArg(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	var fired bool
	cb := coro.Callback(loop, func(ctx coro.Context) {
		fired = true
	})

	cb()

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.True(t, fired)
}

func TestCallback1_OneArg(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	var got int
	cb := coro.Callback1(loop, func(ctx coro.Context, v int) {
		got = v
	})

	cb(42)

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, 42, got)
}

func TestCallback2_TwoArgs(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	var gotA int
	var gotB string
	cb := coro.Callback2(loop, func(ctx coro.Context, a int, b string) {
		gotA, gotB = a, b
	})

	cb(7, "hi")

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, 7, gotA)
	require.Equal(t, "hi", gotB)
}

func TestEventLoop_AddDelayedTask(t *testing.T) {
	t.Parallel()

	start := time.Unix(0, 0)
	clock := chrono.NewSimulator(start)
	loop := coro.NewEventLoop(clock)

	var firedAt time.Time
	loop.AddDelayedTask(5*time.Second, func(ctx coro.Context) {
		firedAt = ctx.Now()
	})

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, start.Add(5*time.Second), firedAt)
}

func TestEventLoop_AddPlannedTaskCtx_RunsAtPlannedTime(t *testing.T) {
	t.Parallel()

	start := time.Unix(0, 0)
	clock := chrono.NewSimulator(start)
	loop := coro.NewEventLoop(clock)

	planned := start.Add(3 * time.Second)
	var firedAt time.Time

	loop.AddPlannedTaskCtx(context.Background(), planned, func(ctx coro.Context) {
		firedAt = ctx.Now()
	})

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, planned, firedAt)
}

func TestEventLoop_AddPlannedTaskCtx_CancelledFiresEarly(t *testing.T) {
	t.Parallel()

	// External cancellation must trigger the task immediately on the loop,
	// not at planned time. We use RealClock here because we want to observe
	// cancellation propagating from a background goroutine.
	clock := chrono.NewRealClock()
	loop := coro.NewEventLoop(clock)

	plannedFar := time.Now().Add(time.Hour)
	cancelCtx, cancel := context.WithCancel(context.Background())

	var firedOnce sync.Once
	done := make(chan struct{})

	loop.AddPlannedTaskCtx(cancelCtx, plannedFar, func(ctx coro.Context) {
		firedOnce.Do(func() { close(done) })
	})

	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cancellation did not trigger task")
	}
}

func TestEventLoop_AddPlannedTaskCtx_RunsOnlyOnce(t *testing.T) {
	t.Parallel()

	// If planned time fires first, the cancellation-driven path must be a no-op.
	clock := chrono.NewRealClock()
	loop := coro.NewEventLoop(clock)

	planned := time.Now().Add(50 * time.Millisecond)
	cancelCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int
	var mu sync.Mutex
	done := make(chan struct{}, 2)

	loop.AddPlannedTaskCtx(cancelCtx, planned, func(ctx coro.Context) {
		mu.Lock()
		calls++
		mu.Unlock()
		done <- struct{}{}
	})

	// Wait for the planned firing to happen.
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("planned task did not fire")
	}

	// Give the cancellation goroutine time to also try (it shouldn't actually run).
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, calls, "task must not run twice")
}

func TestPackageLevel_AddTaskHelpers(t *testing.T) {
	t.Parallel()

	// The package-level AddTask/AddDelayedTask/AddPlannedTask helpers use
	// DefaultEventLoop, which is RealClock-backed. Smoke-test their wiring.
	done := make(chan struct{})
	coro.AddTask(func(ctx coro.Context) {
		close(done)
	})

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("AddTask did not run")
	}

	done2 := make(chan struct{})
	coro.AddDelayedTask(10*time.Millisecond, func(ctx coro.Context) {
		close(done2)
	})
	select {
	case <-done2:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("AddDelayedTask did not run")
	}

	done3 := make(chan struct{})
	coro.AddPlannedTask(time.Now().Add(10*time.Millisecond), func(ctx coro.Context) {
		close(done3)
	})
	select {
	case <-done3:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("AddPlannedTask did not run")
	}
}
