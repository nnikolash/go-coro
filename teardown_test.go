package coro_test

import (
	"context"
	"testing"
	"time"

	chrono "github.com/nnikolash/go-chrono"
	"github.com/nnikolash/go-coro"
	"github.com/stretchr/testify/require"
)

// A coroutine left suspended after the simulation stops is a parked goroutine.
// Close must tear it down: its deferred cleanup runs and the code after the
// Sleep it was parked on must NOT execute.
func TestEventLoop_Close_TearsDownSuspendedCoroutine(t *testing.T) {
	t.Parallel()

	t0 := time.Unix(0, 0)
	clock := chrono.NewSimulator(t0)
	loop := coro.NewEventLoop(clock)

	cleaned := make(chan struct{})
	resumed := false
	loop.AddTask(func(ctx coro.Context) {
		defer close(cleaned)
		ctx.Sleep(time.Hour)
		resumed = true // must not run: torn down while parked on Sleep
	})

	// Stop the simulation at +1min; the coroutine is parked on a +1h Sleep,
	// so it stays suspended (its resume task never fires).
	n, err := clock.ProcessAllUntil(context.Background(), t0.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 1, n, "only the initial task should have run")

	require.Equal(t, 1, loop.Close(), "one suspended coroutine should be torn down")

	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("coroutine deferred cleanup did not run — goroutine leaked")
	}
	require.False(t, resumed, "code after Sleep must not run on a torn-down coroutine")
}

// Close is a no-op for coroutines that already ran to completion.
func TestEventLoop_Close_AfterFullRun_NothingToTearDown(t *testing.T) {
	t.Parallel()

	t0 := time.Unix(0, 0)
	clock := chrono.NewSimulator(t0)
	loop := coro.NewEventLoop(clock)

	done := false
	loop.AddTask(func(ctx coro.Context) {
		ctx.Sleep(time.Minute)
		done = true
	})

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.True(t, done, "coroutine should have run to completion")

	require.Equal(t, 0, loop.Close(), "no suspended coroutines remain after a full run")
}

// Close tears down every suspended coroutine and runs each one's cleanup.
func TestEventLoop_Close_TearsDownAll(t *testing.T) {
	t.Parallel()

	const n = 5
	t0 := time.Unix(0, 0)
	clock := chrono.NewSimulator(t0)
	loop := coro.NewEventLoop(clock)

	cleaned := make(chan int, n)
	for i := 0; i < n; i++ {
		i := i
		loop.AddTask(func(ctx coro.Context) {
			defer func() { cleaned <- i }()
			ctx.Sleep(time.Duration(i+1) * time.Hour)
		})
	}

	_, err := clock.ProcessAllUntil(context.Background(), t0.Add(time.Minute))
	require.NoError(t, err)

	require.Equal(t, n, loop.Close(), "all suspended coroutines should be torn down")

	for i := 0; i < n; i++ {
		select {
		case <-cleaned:
		case <-time.After(time.Second):
			t.Fatalf("only %d of %d coroutines cleaned up", i, n)
		}
	}
}

// A deferred Sleep/Pause that runs during the cancel unwind must not re-suspend
// the coroutine (it would deadlock the teardown); it must keep unwinding.
func TestEventLoop_Close_ReentrantYieldInDeferDoesNotHang(t *testing.T) {
	t.Parallel()

	t0 := time.Unix(0, 0)
	clock := chrono.NewSimulator(t0)
	loop := coro.NewEventLoop(clock)

	cleaned := make(chan struct{})
	loop.AddTask(func(ctx coro.Context) {
		defer close(cleaned)
		defer ctx.Sleep(time.Second) // yields during cancel unwind
		ctx.Sleep(time.Hour)
	})

	_, err := clock.ProcessAllUntil(context.Background(), t0.Add(time.Minute))
	require.NoError(t, err)

	require.Equal(t, 1, loop.Close())

	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("teardown hung on a re-entrant yield during cancel unwinding")
	}
}

// Tearing down a coroutine that spawned a still-suspended child: both must be
// reclaimed (the child is registered on the same loop).
func TestEventLoop_Close_TearsDownChildCoroutines(t *testing.T) {
	t.Parallel()

	t0 := time.Unix(0, 0)
	clock := chrono.NewSimulator(t0)
	loop := coro.NewEventLoop(clock)

	parentCleaned := make(chan struct{})
	childCleaned := make(chan struct{})
	loop.AddTask(func(ctx coro.Context) {
		defer close(parentCleaned)
		ctx.Go(func(child coro.Context) {
			defer close(childCleaned)
			child.Sleep(time.Hour)
		})
		ctx.Sleep(time.Hour)
	})

	_, err := clock.ProcessAllUntil(context.Background(), t0.Add(time.Minute))
	require.NoError(t, err)

	require.Equal(t, 2, loop.Close(), "parent and child should both be torn down")

	for _, ch := range []chan struct{}{parentCleaned, childCleaned} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatal("a coroutine was not torn down")
		}
	}
}

// Cancel on a YieldController whose coroutine already finished returns false.
func TestYieldController_Cancel_FinishedIsNoop(t *testing.T) {
	t.Parallel()

	ctrl := coro.NewYieldController(nil)
	done := make(chan struct{})
	go func() {
		defer ctrl.Done()
		close(done) // finishes immediately without yielding
	}()
	ctrl.WaitUntilYielded()
	<-done

	require.False(t, ctrl.Cancel(), "cancelling a finished coroutine is a no-op")
}
