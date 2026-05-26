package coro_test

import (
	"context"
	"testing"
	"time"

	chrono "github.com/nnikolash/go-chrono"
	coro "github.com/nnikolash/go-coro"
	"github.com/stretchr/testify/require"
)

func TestScheduler_ContextIsScheduler(t *testing.T) {
	t.Parallel()

	// coro.Context satisfies coro.Scheduler directly — no adapter needed.
	// This is the central API claim of the Scheduler interface.
	start := time.Unix(1000, 0)
	clock := chrono.NewSimulator(start)
	loop := coro.NewEventLoop(clock)

	var order []string

	loop.AddTask(func(ctx coro.Context) {
		// ctx is a Context, but here we use it through the Scheduler API only.
		var s coro.Scheduler = ctx

		require.Equal(t, start, s.Now())
		order = append(order, "before-sleep")
		s.Sleep(time.Second)
		require.Equal(t, start.Add(time.Second), s.Now())
		order = append(order, "after-sleep")

		s.Spawn(func(child coro.Scheduler) {
			order = append(order, "child")
			child.Sleep(500 * time.Millisecond)
			order = append(order, "child-end")
		})

		s.Sleep(time.Second)
		order = append(order, "parent-end")
	})

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"before-sleep", "after-sleep", "child", "child-end", "parent-end"}, order)
}

func TestScheduler_ContextPassedDirectlyToSchedulerFunction(t *testing.T) {
	t.Parallel()

	// Downstream pattern: a library function takes Scheduler. Calling code
	// passes ctx directly — no AsScheduler/wrap/cast.
	publishWindow := func(s coro.Scheduler, window time.Duration) time.Time {
		s.Sleep(window)
		return s.Now()
	}

	start := time.Unix(0, 0)
	clock := chrono.NewSimulator(start)
	loop := coro.NewEventLoop(clock)

	var firedAt time.Time
	loop.AddTask(func(ctx coro.Context) {
		firedAt = publishWindow(ctx, 5*time.Second)
	})

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, start.Add(5*time.Second), firedAt)
}

func TestInlineScheduler_NowAndSleep(t *testing.T) {
	t.Parallel()

	start := time.Unix(1000, 0)
	s := coro.NewInlineScheduler(start)

	require.Equal(t, start, s.Now())

	s.Sleep(2 * time.Second)
	require.Equal(t, start.Add(2*time.Second), s.Now())

	s.Sleep(-time.Second) // no-op
	require.Equal(t, start.Add(2*time.Second), s.Now())
}

func TestInlineScheduler_SleepUntil(t *testing.T) {
	t.Parallel()

	start := time.Unix(1000, 0)
	s := coro.NewInlineScheduler(start)

	target := start.Add(time.Minute)
	s.SleepUntil(target)
	require.Equal(t, target, s.Now())

	// SleepUntil into the past must not rewind.
	s.SleepUntil(start)
	require.Equal(t, target, s.Now())
}

func TestInlineScheduler_SinceUntil(t *testing.T) {
	t.Parallel()

	start := time.Unix(1000, 0)
	s := coro.NewInlineScheduler(start)
	s.Sleep(10 * time.Second)

	require.Equal(t, 10*time.Second, s.Since(start))
	require.Equal(t, 5*time.Second, s.Until(start.Add(15*time.Second)))
}

func TestInlineScheduler_Spawn_RunsSynchronouslyAndSharesClock(t *testing.T) {
	t.Parallel()

	start := time.Unix(1000, 0)
	s := coro.NewInlineScheduler(start)

	var order []string

	s.Sleep(time.Second)
	s.Spawn(func(child coro.Scheduler) {
		order = append(order, "child-start")
		require.Equal(t, start.Add(time.Second), child.Now(), "child sees parent's clock")
		child.Sleep(2 * time.Second)
		order = append(order, "child-end")
	})
	order = append(order, "after-spawn")

	// Child must have advanced the shared clock, since Spawn ran synchronously.
	require.Equal(t, start.Add(3*time.Second), s.Now())
	require.Equal(t, []string{"child-start", "child-end", "after-spawn"}, order)
}

func TestContextScheduler_SinceUntilSleepUntil(t *testing.T) {
	t.Parallel()

	start := time.Unix(1000, 0)
	clock := chrono.NewSimulator(start)
	loop := coro.NewEventLoop(clock)

	loop.AddTask(func(ctx coro.Context) {
		var s coro.Scheduler = ctx
		s.SleepUntil(start.Add(5 * time.Second))
		require.Equal(t, 5*time.Second, s.Since(start))
		require.Equal(t, 5*time.Second, s.Until(start.Add(10*time.Second)))
	})

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
}

func TestLoopScheduler_TimeQueries(t *testing.T) {
	t.Parallel()

	start := time.Unix(1000, 0)
	clock := chrono.NewSimulator(start)
	loop := coro.NewEventLoop(clock)
	ls := coro.NewLoopScheduler(loop)

	require.Equal(t, start, ls.Now())
	require.Equal(t, time.Duration(0), ls.Since(start))
	require.Equal(t, 5*time.Second, ls.Until(start.Add(5*time.Second)))
}

func TestLoopScheduler_SpawnSubmitsToLoop(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)
	ls := coro.NewLoopScheduler(loop)

	var ran bool
	ls.Spawn(func(s coro.Scheduler) {
		ran = true
	})

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.True(t, ran)
}

func TestLoopScheduler_SleepPanics(t *testing.T) {
	t.Parallel()

	loop := coro.NewEventLoop(chrono.NewSimulator(time.Unix(0, 0)))
	ls := coro.NewLoopScheduler(loop)

	require.PanicsWithValue(t,
		"coro.NewLoopScheduler: Sleep is only valid inside a coroutine; pass the coroutine's Context there",
		func() { ls.Sleep(time.Second) })

	require.PanicsWithValue(t,
		"coro.NewLoopScheduler: SleepUntil is only valid inside a coroutine; pass the coroutine's Context there",
		func() { ls.SleepUntil(time.Now()) })
}

// ---------------------------------------------------------------------------
// Integration test mimicking the downstream "Indicator" / pub-sub pattern.
//
// This is the "single end-to-end integration test under chrono.Simulator"
// requested in PROBLEMS_AND_RECOMMENDATIONS.md (Recommendation 3). It models a
// simplified indicator that:
//   - subscribes to tick events
//   - publishes its own derived events on a schedule
//   - notifies a downstream listener via the Scheduler interface
//
// Goals of this test:
//   - Prove that Scheduler is sufficient for a non-trivial pub-sub flow.
//   - Catch ordering / timing regressions when chrono's Simulator semantics
//     change under coro.
//   - Serve as a living example for contributors writing coro-based code.
// ---------------------------------------------------------------------------

type tickEvent struct {
	at    time.Time
	price float64
}

type derivedEvent struct {
	at  time.Time
	avg float64
}

type indicator struct {
	scheduler coro.Scheduler
	window    time.Duration
	prices    []float64
	listener  func(coro.Scheduler, derivedEvent)
}

func newIndicator(s coro.Scheduler, window time.Duration, listener func(coro.Scheduler, derivedEvent)) *indicator {
	return &indicator{scheduler: s, window: window, listener: listener}
}

func (i *indicator) onTick(tick tickEvent) {
	i.prices = append(i.prices, tick.price)
}

// publish runs as its own coroutine: every `window` it emits an averaged event.
func (i *indicator) publish(s coro.Scheduler, stop *bool) {
	for !*stop {
		s.Sleep(i.window)
		if len(i.prices) == 0 {
			continue
		}
		var sum float64
		for _, p := range i.prices {
			sum += p
		}
		evt := derivedEvent{at: s.Now(), avg: sum / float64(len(i.prices))}
		i.prices = i.prices[:0]
		i.listener(s, evt)
	}
}

func TestIntegration_IndicatorPubSub_UnderSimulator(t *testing.T) {
	t.Parallel()

	start := time.Unix(0, 0)
	clock := chrono.NewSimulator(start)
	loop := coro.NewEventLoop(clock)

	var derived []derivedEvent
	stop := false

	loop.AddTask(func(ctx coro.Context) {
		// ctx IS a Scheduler — pass it directly.
		ind := newIndicator(ctx, 10*time.Second, func(_ coro.Scheduler, evt derivedEvent) {
			derived = append(derived, evt)
		})

		// Publisher coroutine.
		ctx.Spawn(func(child coro.Scheduler) {
			ind.publish(child, &stop)
		})

		// Tick producer: send a tick every second for 35 seconds, then stop.
		for i := 0; i < 35; i++ {
			ind.onTick(tickEvent{at: ctx.Now(), price: float64(i + 1)})
			ctx.Sleep(time.Second)
		}
		stop = true
		// Let the publisher observe stop.
		ctx.Sleep(11 * time.Second)
	})

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)

	// We expect 3 published windows at t=10s, t=20s, t=30s (each with 10 ticks).
	// The 4th window after stop=true has only 5 ticks queued; it might or might
	// not publish depending on stop timing — but it must not double-publish.
	require.GreaterOrEqual(t, len(derived), 3)
	require.LessOrEqual(t, len(derived), 4)

	require.Equal(t, start.Add(10*time.Second), derived[0].at)
	require.InDelta(t, 5.5, derived[0].avg, 0.001) // mean of 1..10

	require.Equal(t, start.Add(20*time.Second), derived[1].at)
	require.InDelta(t, 15.5, derived[1].avg, 0.001) // mean of 11..20

	require.Equal(t, start.Add(30*time.Second), derived[2].at)
	require.InDelta(t, 25.5, derived[2].avg, 0.001) // mean of 21..30
}

func TestIntegration_IndicatorPubSub_UnitTestableWithInline(t *testing.T) {
	t.Parallel()

	// Same indicator code, tested entirely without an event loop. This is the
	// payoff of Scheduler: downstream libraries can unit-test the logic of a
	// Scheduler-based object without depending on chrono.Simulator, without
	// spinning goroutines, and without `if ctx == nil` test branches in
	// production code.
	start := time.Unix(0, 0)
	s := coro.NewInlineScheduler(start)

	var derived []derivedEvent
	ind := newIndicator(s, 10*time.Second, func(_ coro.Scheduler, evt derivedEvent) {
		derived = append(derived, evt)
	})

	// Drive 10 ticks (one per second).
	for i := 0; i < 10; i++ {
		ind.onTick(tickEvent{at: s.Now(), price: float64(i + 1)})
		s.Sleep(time.Second)
	}

	// Run publish in a controlled way: stop the loop after exactly one cycle.
	// The 1st iteration emits the averaged event and sets stop=true; the 2nd
	// iteration's check on *stop exits cleanly.
	stop := false
	cycles := 0
	ind.listener = func(_ coro.Scheduler, evt derivedEvent) {
		derived = append(derived, evt)
		cycles++
		if cycles == 1 {
			stop = true
		}
	}

	ind.publish(s, &stop)

	require.Len(t, derived, 1)
	require.Equal(t, start.Add(20*time.Second), derived[0].at) // 10s of ticks + 10s window
	require.InDelta(t, 5.5, derived[0].avg, 0.001)             // mean of 1..10
}
