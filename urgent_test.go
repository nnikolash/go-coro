package coro_test

// Tests for eventLoopT.AddUrgentTask.
//
// AddUrgentTask guarantees (under chrono.Simulator) that the enqueued
// coroutine executes before all tasks already in the clock queue at call
// time. The mechanism: every regular-task clock callback drains urgentTasks
// first; AddUrgentTask also posts a fallback drain-trigger in case no regular
// task is pending.
//
// Test matrix:
//  1. Urgent task runs before previously-queued regular tasks (sim clock).
//  2. Urgent task added from within an escape handler runs before the next
//     queued regular task (sim clock — canonical cmd/bot use-case).
//  3. Normal task ordering (FIFO) is unaffected when no urgent tasks exist.
//  4. Race-detector pass — concurrent AddUrgentTask calls (real clock).

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	chrono "github.com/nnikolash/go-chrono"
	"github.com/nnikolash/go-coro"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// (1) Urgent task runs before previously-queued regular tasks
// ---------------------------------------------------------------------------

// TestAddUrgentTask_RunsBeforeQueued verifies that a task added via
// AddUrgentTask executes before regular tasks that were already in the clock
// queue at the time AddUrgentTask was called.
func TestAddUrgentTask_RunsBeforeQueued(t *testing.T) {
	t.Parallel()

	t0 := time.Unix(0, 0)
	sim := chrono.NewSimulator(t0)
	loop := coro.NewEventLoop(sim)

	var order []int

	// Queue two regular tasks first.
	loop.AddTask(func(_ coro.Context) { order = append(order, 2) })
	loop.AddTask(func(_ coro.Context) { order = append(order, 3) })

	// Now enqueue an urgent task — it must run before the two above.
	loop.AddUrgentTask(func(_ coro.Context) { order = append(order, 1) })

	_, err := sim.ProcessAll(context.Background())
	require.NoError(t, err)

	require.Equal(t, []int{1, 2, 3}, order,
		"urgent task must run before already-queued regular tasks")
}

// ---------------------------------------------------------------------------
// (2) Urgent task from escape handler runs before next queued task
// ---------------------------------------------------------------------------

// TestAddUrgentTask_FromEscapeHandler verifies the canonical cmd/bot restart
// scenario: a coroutine calls coro.Escape; the escape handler calls
// AddUrgentTask to enqueue a stop/restart coroutine; that urgent coroutine
// must execute before any command that was already queued (e.g. a second
// user command received while the first was running).
func TestAddUrgentTask_FromEscapeHandler(t *testing.T) {
	t.Parallel()

	t0 := time.Unix(0, 0)
	sim := chrono.NewSimulator(t0)
	loop := coro.NewEventLoop(sim)

	var order []int

	// Task A: runs first, triggers escape (simulates strategy requesting restart).
	loop.AddTask(func(_ coro.Context) {
		order = append(order, 10) // proof A ran
		coro.Escape("restart-request")
	})

	// Task B: a second command already queued — must run AFTER the urgent stop.
	loop.AddTask(func(_ coro.Context) {
		order = append(order, 3)
	})

	// Escape handler: enqueues the urgent stop/restart coroutine.
	loop.SetEscapeHandler(func(_ any) {
		loop.AddUrgentTask(func(_ coro.Context) {
			order = append(order, 2) // urgent stop — must precede task B
		})
	})

	_, err := sim.ProcessAll(context.Background())
	require.NoError(t, err)

	require.Equal(t, []int{10, 2, 3}, order,
		"escape-handler urgent task must run before next queued command")
}

// ---------------------------------------------------------------------------
// (3) Normal FIFO ordering is unaffected when no urgent tasks are present
// ---------------------------------------------------------------------------

// TestAddUrgentTask_NormalOrderUnaffected verifies that when AddUrgentTask is
// never called, regular tasks still execute in FIFO insertion order. This
// guards against the drainUrgent call in AddDelayedTask accidentally
// reordering tasks.
func TestAddUrgentTask_NormalOrderUnaffected(t *testing.T) {
	t.Parallel()

	t0 := time.Unix(0, 0)
	sim := chrono.NewSimulator(t0)
	loop := coro.NewEventLoop(sim)

	const n = 10
	var order []int

	for i := 1; i <= n; i++ {
		i := i
		loop.AddTask(func(_ coro.Context) { order = append(order, i) })
	}

	_, err := sim.ProcessAll(context.Background())
	require.NoError(t, err)

	expected := make([]int, n)
	for i := range expected {
		expected[i] = i + 1
	}
	require.Equal(t, expected, order, "regular task FIFO order must be preserved")
}

// ---------------------------------------------------------------------------
// (4) Race-detector pass — concurrent AddUrgentTask calls
// ---------------------------------------------------------------------------

// TestAddUrgentTask_Race verifies that concurrent calls to AddUrgentTask and
// AddTask produce no data races under -race. Uses a real clock so goroutines
// run concurrently; exact execution order is intentionally not asserted.
func TestAddUrgentTask_Race(t *testing.T) {
	// Not t.Parallel() — gives the race detector isolated CPU time.

	loop := coro.NewEventLoop(chrono.NewRealClock())

	const n = 8
	var done atomic.Int32

	allDone := make(chan struct{})

	finish := func(_ coro.Context) {
		if done.Add(1) == 2*n {
			close(allDone)
		}
	}

	for range n {
		loop.AddTask(finish)
		loop.AddUrgentTask(finish)
	}

	select {
	case <-allDone:
	case <-time.After(10 * time.Second):
		t.Fatalf("only %d of %d tasks finished within 10s", done.Load(), 2*n)
	}
}
