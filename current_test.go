package coro_test

import (
	"context"
	"testing"
	"time"

	chrono "github.com/nnikolash/go-chrono"
	coro "github.com/nnikolash/go-coro"
	"github.com/stretchr/testify/require"
)

// TestCurrentCoroutine_RestoreOnPark verifies the restore half of save/restore:
// once a coroutine parks itself (calls loop.Sleep), loop.CurrentScheduler()
// must return nil and loop.Sleep called from a bare AfterFunc callback (which
// runs on the pump goroutine outside any coroutine) must panic.
//
// RED verification: neutralise the restore by removing setCurrentCoroutine(prev)
// calls — current stays pointing at the parked coroutine, the nil-guard never
// fires, and this test fails.
func TestCurrentCoroutine_RestoreOnPark(t *testing.T) {
	t.Parallel()

	start := time.Unix(0, 0)
	clock := chrono.NewSimulator(start)
	loop := coro.NewEventLoop(clock)

	var (
		currentWhileParked      coro.Scheduler
		sleepPanicWhileParked   interface{}
		currentAfterComplete    coro.Scheduler
		sleepPanicAfterComplete interface{}
	)

	// Coroutine parks for 2s.
	loop.AddTask(func(ctx coro.Context) {
		loop.Sleep(2 * time.Second)
	})

	// Bare AfterFunc at T+1s: coroutine is still parked. Observe current and
	// attempt loop.Sleep — both must signal "no active coroutine".
	clock.AfterFunc(1*time.Second, func(now time.Time) {
		currentWhileParked = loop.CurrentScheduler()
		func() {
			defer func() { sleepPanicWhileParked = recover() }()
			loop.Sleep(time.Second)
		}()
	})

	// Bare AfterFunc at T+3s: coroutine has finished. Same assertions.
	clock.AfterFunc(3*time.Second, func(now time.Time) {
		currentAfterComplete = loop.CurrentScheduler()
		func() {
			defer func() { sleepPanicAfterComplete = recover() }()
			loop.Sleep(time.Second)
		}()
	})

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)

	require.Nil(t, currentWhileParked,
		"current must be nil while coroutine is parked")
	require.NotNil(t, sleepPanicWhileParked,
		"loop.Sleep from bare callback (coroutine parked) must panic")

	require.Nil(t, currentAfterComplete,
		"current must be nil after coroutine completes")
	require.NotNil(t, sleepPanicAfterComplete,
		"loop.Sleep from bare callback (coroutine done) must panic")
}

// TestCurrentCoroutine_ABA_Resume verifies save/restore semantics: A sleeps, B
// runs (current=B), B sleeps, A wakes — each coroutine observes itself as
// loop.CurrentScheduler() before and after its own sleep.
func TestCurrentCoroutine_ABA_Resume(t *testing.T) {
	t.Parallel()

	start := time.Unix(0, 0)
	clock := chrono.NewSimulator(start)
	loop := coro.NewEventLoop(clock)

	var aCtx, bCtx coro.Scheduler
	var aCurrent1, bCurrent1, aCurrent2 coro.Scheduler

	// A sleeps 2s; B sleeps 1s — B wakes before A (tests A→B→A restore).
	loop.AddTask(func(ctx coro.Context) {
		aCtx = ctx
		aCurrent1 = loop.CurrentScheduler()
		loop.Sleep(2 * time.Second)
		aCurrent2 = loop.CurrentScheduler()
	})

	loop.AddTask(func(ctx coro.Context) {
		bCtx = ctx
		bCurrent1 = loop.CurrentScheduler()
		loop.Sleep(1 * time.Second)
	})

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)

	require.NotNil(t, aCurrent1, "A's current before sleep must not be nil")
	require.Same(t, aCtx, aCurrent1, "A's current before sleep must be A")
	require.Same(t, aCtx, aCurrent2, "A's current after A→B→A resume must still be A")
	require.Same(t, bCtx, bCurrent1, "B's current before sleep must be B")
	// A and B are distinct coroutines.
	require.NotSame(t, aCtx.(interface{}), bCtx.(interface{}))
}

// TestCurrentCoroutine_DeepStack verifies that loop.Sleep can be called from an
// arbitrarily deep call stack inside a coroutine and that the full stack is
// preserved across the suspension.
func TestCurrentCoroutine_DeepStack(t *testing.T) {
	t.Parallel()

	start := time.Unix(0, 0)
	clock := chrono.NewSimulator(start)
	loop := coro.NewEventLoop(clock)

	depth := 0
	var timeAfterSleep time.Time

	h := func() {
		depth++
		loop.Sleep(5 * time.Second)
		timeAfterSleep = loop.CurrentScheduler().Now()
		depth--
	}
	g := func() { depth++; h(); depth-- }
	f := func() { depth++; g(); depth-- }

	loop.AddTask(func(ctx coro.Context) { f() })

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)

	require.Equal(t, start.Add(5*time.Second), timeAfterSleep,
		"should wake at T+5s")
	require.Equal(t, 0, depth,
		"call stack must be fully unwound after sleep")
}

// TestCurrentCoroutine_NilPanic verifies that calling loop.Sleep outside a
// coroutine panics with the documented message.
func TestCurrentCoroutine_NilPanic(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	require.Panics(t, func() {
		loop.Sleep(time.Second)
	}, "loop.Sleep outside a coroutine must panic")

	require.Panics(t, func() {
		loop.SleepUntil(time.Unix(999, 0))
	}, "loop.SleepUntil outside a coroutine must panic")
}

// TestCurrentCoroutine_SimulatorOrdering verifies that multiple coroutines on
// the same loop with different Sleep durations wake in strict chronological
// order under chrono.Simulator.
func TestCurrentCoroutine_SimulatorOrdering(t *testing.T) {
	t.Parallel()

	start := time.Unix(0, 0)
	clock := chrono.NewSimulator(start)
	loop := coro.NewEventLoop(clock)

	var wakeOrder []string

	loop.AddTask(func(ctx coro.Context) {
		loop.Sleep(3 * time.Second)
		wakeOrder = append(wakeOrder, "A")
	})

	loop.AddTask(func(ctx coro.Context) {
		loop.Sleep(1 * time.Second)
		wakeOrder = append(wakeOrder, "B")
	})

	loop.AddTask(func(ctx coro.Context) {
		loop.Sleep(2 * time.Second)
		wakeOrder = append(wakeOrder, "C")
	})

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)

	require.Equal(t, []string{"B", "C", "A"}, wakeOrder,
		"coroutines must wake in chronological order")
}
