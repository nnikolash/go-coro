package coro_test

// Tests for coro.AwaitableCallback.
//
// Test matrix (mirrors the brief):
//  1. resolve called BEFORE wait -> wait returns immediately; proven via the
//     event loop's own task counter, not just via timing.
//  2. resolve called AFTER wait starts, from a foreign (real, concurrent)
//     goroutine -> waiter resumes, value arrives, loop keeps running.
//  3. resolve called AFTER wait starts, from the loop itself (another
//     already-running coroutine, mirroring Mutex) -> waiter resumes
//     correctly, and — the "main subtlety" — with no extra clock hop.
//  4. Coroutine cancelled while waiting -> clean unwind; resolve called
//     afterwards must be harmless (must not resurrect a dead coroutine).
//  5. resolve called twice -> panics; the second call must not corrupt the
//     first result. wait called twice concurrently -> panics (single-waiter
//     guard).
//  6. Positive control: run the rest of the package's test suite (done by
//     the "go test ./..." gate, not duplicated here).
//
// Race-detector coverage: TestAwaitableCallback_ResolvedFromForeignGoroutine
// and TestAwaitableCallback_ResolveTwice_ConcurrentCallers below are written
// to be meaningful under `go test -race`.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	chrono "github.com/nnikolash/go-chrono"
	"github.com/nnikolash/go-coro"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// (1) resolve before wait -> immediate return, proven no-yield
// ---------------------------------------------------------------------------

// TestAwaitableCallback_AlreadyResolved_NoYield is the key test: when resolve
// has already run by the time wait is called, wait must return synchronously
// without the calling coroutine ever yielding.
//
// Proof of "no yield": the coroutine is the only task queued on the loop. If
// wait() internally called ctx.Pause() (yielding) and relied on any kind of
// scheduled resume (as a JS-`then`-style "always defer" implementation
// would), that would show up as an *additional* task processed by the
// simulator. clock.ProcessAll returns exactly how many tasks it processed —
// asserting it is exactly 1 is a black-box, timing-independent proof that no
// extra scheduling round-trip happened, not just that the answer came back
// "fast".
//
// RED verification: if wait() is changed to unconditionally register a
// waiter and call ctx.Pause() before checking a.done (removing the fast
// path), this test fails with tasksProcessed == 2 (the extra Resume() hop).
func TestAwaitableCallback_AlreadyResolved_NoYield(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	resolve, wait := coro.AwaitableCallback[int]()

	// Resolve BEFORE anyone is waiting.
	resolve(42, nil)

	var got int
	var gotErr error
	loop.AddTask(func(ctx coro.Context) {
		got, gotErr = wait(ctx)
	})

	n, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n,
		"wait() on an already-resolved value must not schedule any additional loop task (no yield)")
	require.Equal(t, 42, got)
	require.NoError(t, gotErr)
}

// TestAwaitableCallback_AlreadyResolved_WithError verifies the error half of
// the pair travels through the same fast path.
func TestAwaitableCallback_AlreadyResolved_WithError(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	resolve, wait := coro.AwaitableCallback[string]()

	sentinelErr := context.DeadlineExceeded
	resolve("", sentinelErr)

	var got string
	var gotErr error
	loop.AddTask(func(ctx coro.Context) {
		got, gotErr = wait(ctx)
	})

	n, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, "", got)
	require.Equal(t, sentinelErr, gotErr)
}

// ---------------------------------------------------------------------------
// (2) resolve from a foreign, concurrent goroutine
// ---------------------------------------------------------------------------

// TestAwaitableCallback_ResolvedFromForeignGoroutine verifies that a callback
// fired from a goroutine wholly unrelated to the event loop correctly wakes
// the waiting coroutine, with the loop running throughout on chrono.RealClock
// (so the "foreign goroutine" is genuinely concurrent, not just a different
// call stack on the same thread).
func TestAwaitableCallback_ResolvedFromForeignGoroutine(t *testing.T) {
	// Not t.Parallel() — gives the race detector isolated CPU time.

	loop := coro.NewEventLoop(chrono.NewRealClock())

	resolve, wait := coro.AwaitableCallback[string]()

	waiterStarted := make(chan struct{})
	done := make(chan struct{})

	var got string
	var gotErr error
	loop.AddTask(func(ctx coro.Context) {
		close(waiterStarted)
		got, gotErr = wait(ctx)
		close(done)
	})

	// Foreign goroutine: waits for the coroutine to have started waiting,
	// then resolves from a goroutine that has no relationship to the loop.
	go func() {
		<-waiterStarted
		time.Sleep(10 * time.Millisecond) // let wait() actually reach Pause()
		resolve("from-foreign-goroutine", nil)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("waiter never resumed after foreign-goroutine resolve")
	}

	require.Equal(t, "from-foreign-goroutine", got)
	require.NoError(t, gotErr)
}

// TestAwaitableCallback_ResolvedFromForeignGoroutine_RacesIntoRegistration
// deliberately does NOT wait for the coroutine to reach Pause() before
// resolving — it races the callback against wait()'s own registration, which
// is exactly the narrow window the isPaused() check exists to protect
// (see the doc comment on AwaitableCallback / YieldController.isPaused).
// Run with -race and with -count to shake out timing-dependent bugs.
func TestAwaitableCallback_ResolvedFromForeignGoroutine_RacesIntoRegistration(t *testing.T) {
	// Not t.Parallel() — gives the race detector isolated CPU time.

	for i := 0; i < 50; i++ {
		loop := coro.NewEventLoop(chrono.NewRealClock())
		resolve, wait := coro.AwaitableCallback[int]()

		done := make(chan struct{})
		var got int
		loop.AddTask(func(ctx coro.Context) {
			got, _ = wait(ctx)
			close(done)
		})

		// No synchronization with wait() at all: resolve races the
		// coroutine's own goroutine from the very start.
		go resolve(i, nil)

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: waiter never resumed (lost wakeup)", i)
		}
		require.Equal(t, i, got)
	}
}

// ---------------------------------------------------------------------------
// (3) resolve from the loop itself (another already-running coroutine)
// ---------------------------------------------------------------------------

// TestAwaitableCallback_ResolvedFromLoopItself mirrors coro.Mutex's own
// waiter-resumption pattern: coroutine B calls resolve directly (not via a
// separate goroutine) while it is the one actively running on the loop.
//
// This proves both correctness AND the "main subtlety" from the brief: since
// B is cooperatively running, A (the waiter) must already be paused, so
// resolve must take the in-place path (YieldController.isPaused == true) —
// no clock hop. Proven the same way as test (1): via the simulator's own
// task counter. Two tasks are queued (A and B); if resolve added a third
// (scheduled) task to resume A, the total would be 3 instead of 2.
//
// RED verification: replace the isPaused()-guarded direct call in resolve
// with an unconditional waiter.Resume() and this test's task-count assertion
// fails (3 instead of 2) even though the value still arrives correctly —
// demonstrating why "it works" is not the same as "resolves in place".
func TestAwaitableCallback_ResolvedFromLoopItself(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	resolve, wait := coro.AwaitableCallback[string]()

	var got string
	var gotErr error
	var order []string

	// Coroutine A: starts waiting immediately.
	loop.AddTask(func(ctx coro.Context) {
		order = append(order, "A-start")
		got, gotErr = wait(ctx)
		order = append(order, "A-resumed")
	})

	// Coroutine B: runs after A has yielded (later in the same virtual
	// instant), and resolves directly from its own coroutine body.
	loop.AddTask(func(ctx coro.Context) {
		order = append(order, "B-run")
		resolve("from-the-loop-itself", nil)
		order = append(order, "B-done")
	})

	n, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)

	require.Equal(t, "from-the-loop-itself", got)
	require.NoError(t, gotErr)
	// B calling resolve() resumes A synchronously, in place, before B
	// continues — "B-done" comes after "A-resumed".
	require.Equal(t, []string{"A-start", "B-run", "A-resumed", "B-done"}, order)
	require.Equal(t, 2, n,
		"resolve() called from the loop itself must resume the waiter in place, with no extra scheduled task")
}

// ---------------------------------------------------------------------------
// (4) cancellation while waiting
// ---------------------------------------------------------------------------

// TestAwaitableCallback_CancelWhileWaiting verifies clean unwind when the
// waiting coroutine is torn down (loop.Close()) while still parked in wait(),
// and that a subsequent resolve() call is harmless — it must not attempt to
// resume the now-dead coroutine.
func TestAwaitableCallback_CancelWhileWaiting(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	resolve, wait := coro.AwaitableCallback[int]()

	cleaned := make(chan struct{})
	resumed := false
	loop.AddTask(func(ctx coro.Context) {
		defer close(cleaned)
		_, _ = wait(ctx)
		resumed = true // must NOT run: torn down while parked in wait()
	})

	// Run once so the coroutine starts and parks inside wait(). Nothing else
	// is queued, so ProcessAll returns as soon as the coroutine yields.
	n, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)

	require.Equal(t, 1, loop.Close(), "the parked coroutine must be torn down")

	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("deferred cleanup did not run — coroutine leaked")
	}
	require.False(t, resumed, "code after wait() must not run on a cancelled coroutine")

	// Harmless-after-cancel: resolving now must not panic, and any task it
	// schedules (the Resume() fallback path, since the cancelled waiter's
	// YieldController is no longer paused) must be a safe no-op once run.
	require.NotPanics(t, func() {
		resolve(99, nil)
	})

	_, err = clock.ProcessAll(context.Background())
	require.NoError(t, err, "draining the fallback resume task after cancellation must not error or hang")
}

// ---------------------------------------------------------------------------
// (5) one-shot / single-waiter misuse panics
// ---------------------------------------------------------------------------

// TestAwaitableCallback_ResolveTwice_Panics documents and verifies the chosen
// behavior for a repeated resolve call: panic, on the same reasoning as a
// double close(chan) — a second call is a producer bug, and silently
// accepting a "first answer wins" would hide it.
func TestAwaitableCallback_ResolveTwice_Panics(t *testing.T) {
	t.Parallel()

	resolve, wait := coro.AwaitableCallback[int]()

	resolve(1, nil)
	require.Panics(t, func() { resolve(2, nil) })

	// The first result must be unaffected by the rejected second call.
	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	var got int
	loop.AddTask(func(ctx coro.Context) { got, _ = wait(ctx) })
	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, got)
}

// TestAwaitableCallback_ResolveTwice_ConcurrentCallers is the race-detector
// variant: two goroutines call resolve concurrently. Exactly one must
// "win" (no data race on the stored value/error), and the other must panic.
func TestAwaitableCallback_ResolveTwice_ConcurrentCallers(t *testing.T) {
	// Not t.Parallel() — gives the race detector isolated CPU time.

	resolve, wait := coro.AwaitableCallback[int]()

	var panics atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if recover() != nil {
					panics.Add(1)
				}
			}()
			resolve(i, nil)
		}()
	}
	wg.Wait()

	require.Equal(t, int32(1), panics.Load(), "exactly one of two concurrent resolve calls must panic")

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)
	var got int
	loop.AddTask(func(ctx coro.Context) { got, _ = wait(ctx) })
	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Contains(t, []int{0, 1}, got)
}

// TestAwaitableCallback_WaitTwice_Panics verifies the single-waiter guard:
// calling wait a second time while the first call is still pending panics
// rather than silently discarding the first waiter (which would otherwise
// leave the first coroutine parked forever with no one left to resume it).
func TestAwaitableCallback_WaitTwice_Panics(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	_, wait := coro.AwaitableCallback[int]()

	firstWaiting := make(chan struct{})
	secondPanicked := make(chan struct{})

	loop.AddTask(func(ctx coro.Context) {
		close(firstWaiting)
		_, _ = wait(ctx) // never resolved; torn down by loop.Close() below
	})

	loop.AddDelayedTask(time.Millisecond, func(ctx coro.Context) {
		<-firstWaiting
		require.Panics(t, func() { _, _ = wait(ctx) })
		close(secondPanicked)
	})

	_, err := clock.ProcessAllUntil(context.Background(), time.Unix(0, 0).Add(time.Second))
	require.NoError(t, err)

	select {
	case <-secondPanicked:
	default:
		t.Fatal("second wait() call did not panic")
	}

	require.Equal(t, 1, loop.Close(), "the first (still-parked) waiter must be torn down")
}

// ---------------------------------------------------------------------------
// (6) positive control
// ---------------------------------------------------------------------------
//
// Neighboring primitives (Mutex, plain loop tasks) are exercised by the rest
// of this package's test suite (mutex_test.go, current_test.go,
// urgent_test.go, teardown_test.go, ...) — running `go test ./...` alongside
// this file is the positive control that AwaitableCallback's additions
// (YieldController.isPaused in yield.go) did not disturb them.
