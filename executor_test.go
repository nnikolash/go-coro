package coro_test

// Tests for coro.Executor / coro.ExecuteBlocking.
//
// Test matrix:
//  1. SyncExecutor causes NO yield — proven via the event loop's own task
//     counter (mirrors TestAwaitableCallback_AlreadyResolved_NoYield),
//     not via timing.
//  2. SyncExecutor preserves call order across independent runs — the
//     concrete form of "deterministic": run the same sequence twice, same
//     output both times.
//  3. GoroutineExecutor does NOT block the event loop — a second task
//     queued while the first is still parked in blocking work must be able
//     to run before the first completes. Real-clock, timing-based; see the
//     test's own doc comment for why a simulated clock cannot prove this.
//  4. GoroutineExecutor recovers a panic in fn and delivers it as an error
//     instead of crashing the process.
//  5. Both executors propagate a plain (non-panic) result/error correctly.

import (
	"context"
	"errors"
	"testing"
	"time"

	chrono "github.com/nnikolash/go-chrono"
	"github.com/nnikolash/go-coro"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// (1) SyncExecutor: no yield, proven by task count
// ---------------------------------------------------------------------------

// TestSyncExecutor_NoYield is the determinism proof for the simulation
// executor. If ExecuteBlocking's wait() had to park the coroutine (as it
// would if SyncExecutor deferred fn instead of running it inline before
// resolve), the simulator would need an extra scheduled task to resume it —
// clock.ProcessAll's own task count would be 2, not 1. Counting tasks is
// timing-independent: it fails the same way whether the extra hop would have
// taken a nanosecond or a second of simulated time.
func TestSyncExecutor_NoYield(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	var got int
	var gotErr error
	loop.AddTask(func(ctx coro.Context) {
		got, gotErr = coro.ExecuteBlocking(ctx, coro.SyncExecutor{}, func() (int, error) {
			return 7, nil
		})
	})

	n, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n,
		"ExecuteBlocking with SyncExecutor must not schedule any additional loop task (no yield)")
	require.Equal(t, 7, got)
	require.NoError(t, gotErr)
}

// TestSyncExecutor_OrderReproducible is the concrete form of "the
// simulation executor is deterministic": the same sequence of
// ExecuteBlocking(SyncExecutor{}, ...) calls, run twice from scratch,
// produces the identical output order both times.
func TestSyncExecutor_OrderReproducible(t *testing.T) {
	t.Parallel()

	run := func() []int {
		clock := chrono.NewSimulator(time.Unix(0, 0))
		loop := coro.NewEventLoop(clock)
		order := make([]int, 0, 5)
		for i := 0; i < 5; i++ {
			i := i
			loop.AddTask(func(ctx coro.Context) {
				v, err := coro.ExecuteBlocking(ctx, coro.SyncExecutor{}, func() (int, error) {
					return i, nil
				})
				require.NoError(t, err)
				order = append(order, v)
			})
		}
		_, err := clock.ProcessAll(context.Background())
		require.NoError(t, err)
		return order
	}

	first := run()
	second := run()
	require.Equal(t, []int{0, 1, 2, 3, 4}, first)
	require.Equal(t, first, second, "SyncExecutor must produce the same order on every run")
}

// ---------------------------------------------------------------------------
// (3) GoroutineExecutor: does not block the event loop
// ---------------------------------------------------------------------------

// TestGoroutineExecutor_LoopStaysFreeWhileBlocked proves the whole reason
// GoroutineExecutor exists: a coroutine parked in ExecuteBlocking(..., fn)
// must not stop the event loop from running OTHER tasks while fn is still
// running.
//
// This test deliberately uses chrono.NewRealClock(), not a simulator.
// GoroutineExecutor's fn runs on a real OS goroutine, outside the event
// loop's own scheduling — a simulated clock only controls tasks scheduled
// through the loop/clock API, it has no relationship to when a detached
// goroutine's own code runs. Replacing this with a simulated clock would not
// exercise GoroutineExecutor's actual behavior at all: it would just prove
// that two tasks queued on a simulator run in queue order, which is true
// regardless of whether ExecuteBlocking blocks anything. The property under
// test — "the loop is free while real, external work is in flight" — only
// exists relative to real wall-clock concurrency, so it can only be proven
// with a real clock and real synchronization (the channels below), not with
// simulated time. Do not "fix" this by switching to chrono.Simulator.
func TestGoroutineExecutor_LoopStaysFreeWhileBlocked(t *testing.T) {
	// Not t.Parallel() — real-clock timing test; avoid CPU contention from
	// sibling tests skewing the window below.

	loop := coro.NewEventLoop(chrono.NewRealClock())

	blockingStarted := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan struct{})

	loop.AddTask(func(ctx coro.Context) {
		_, _ = coro.ExecuteBlocking(ctx, coro.GoroutineExecutor{}, func() (int, error) {
			close(blockingStarted)
			<-release
			return 1, nil
		})
		close(firstDone)
	})

	select {
	case <-blockingStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("GoroutineExecutor's fn never started")
	}

	secondRan := make(chan struct{})
	loop.AddTask(func(ctx coro.Context) {
		close(secondRan)
	})

	select {
	case <-secondRan:
		// Loop processed the second task while the first was still parked —
		// proof the loop was not blocked.
	case <-time.After(2 * time.Second):
		t.Fatal("event loop did not run a second task while GoroutineExecutor work was in flight — loop was blocked")
	}

	close(release)
	select {
	case <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("first task never completed after release")
	}
}

// ---------------------------------------------------------------------------
// (4) GoroutineExecutor: panic recovery
// ---------------------------------------------------------------------------

// TestGoroutineExecutor_PanicBecomesError verifies a panic inside fn is
// recovered by GoroutineExecutor and delivered as an error, instead of
// crashing the process (an unrecovered panic on a detached goroutine is
// fatal to the whole program in Go — this test passing at all, without the
// test binary dying, is part of the proof).
func TestGoroutineExecutor_PanicBecomesError(t *testing.T) {
	// Not t.Parallel() — real-clock timing test.

	loop := coro.NewEventLoop(chrono.NewRealClock())

	done := make(chan struct{})
	var gotErr error
	loop.AddTask(func(ctx coro.Context) {
		_, gotErr = coro.ExecuteBlocking(ctx, coro.GoroutineExecutor{}, func() (int, error) {
			panic("boom")
		})
		close(done)
	})

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ExecuteBlocking never returned after fn panicked")
	}

	require.Error(t, gotErr)
	require.Contains(t, gotErr.Error(), "boom")
}

// ---------------------------------------------------------------------------
// (5) Plain result/error propagation, both executors
// ---------------------------------------------------------------------------

func TestExecuteBlocking_SyncExecutor_PropagatesResult(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	var got string
	var gotErr error
	loop.AddTask(func(ctx coro.Context) {
		got, gotErr = coro.ExecuteBlocking(ctx, coro.SyncExecutor{}, func() (string, error) {
			return "value", nil
		})
	})
	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, "value", got)
	require.NoError(t, gotErr)
}

func TestExecuteBlocking_SyncExecutor_PropagatesError(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	sentinelErr := errors.New("source unavailable")
	var got string
	var gotErr error
	loop.AddTask(func(ctx coro.Context) {
		got, gotErr = coro.ExecuteBlocking(ctx, coro.SyncExecutor{}, func() (string, error) {
			return "", sentinelErr
		})
	})
	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, "", got)
	require.ErrorIs(t, gotErr, sentinelErr)
}

func TestExecuteBlocking_GoroutineExecutor_PropagatesResult(t *testing.T) {
	// Not t.Parallel() — real-clock timing test.

	loop := coro.NewEventLoop(chrono.NewRealClock())

	done := make(chan struct{})
	var got string
	var gotErr error
	loop.AddTask(func(ctx coro.Context) {
		got, gotErr = coro.ExecuteBlocking(ctx, coro.GoroutineExecutor{}, func() (string, error) {
			return "value", nil
		})
		close(done)
	})

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ExecuteBlocking never returned")
	}
	require.Equal(t, "value", got)
	require.NoError(t, gotErr)
}

func TestExecuteBlocking_GoroutineExecutor_PropagatesError(t *testing.T) {
	// Not t.Parallel() — real-clock timing test.

	loop := coro.NewEventLoop(chrono.NewRealClock())

	sentinelErr := errors.New("source unavailable")
	done := make(chan struct{})
	var got string
	var gotErr error
	loop.AddTask(func(ctx coro.Context) {
		got, gotErr = coro.ExecuteBlocking(ctx, coro.GoroutineExecutor{}, func() (string, error) {
			return "", sentinelErr
		})
		close(done)
	})

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ExecuteBlocking never returned")
	}
	require.Equal(t, "", got)
	require.ErrorIs(t, gotErr, sentinelErr)
}
