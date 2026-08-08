package coro_test

// Tests for coro.AwaitableCallback.
//
// Test matrix (mirrors the brief):
//  1. resolve called BEFORE wait -> wait returns immediately; proven via the
//     event loop's own task counter, not just via timing.
//  2. resolve called AFTER wait starts, from a foreign (real, concurrent)
//     goroutine -> waiter resumes, value arrives, loop keeps running. Also
//     covers resolve racing wait's own registration, and resolve called from
//     another already-running coroutine on the same loop — both go through
//     the same Resume() path as the foreign-goroutine case; there is no
//     separate "in place" path (see the doc comment on AwaitableCallback).
//  3. Coroutine cancelled while waiting -> clean unwind; resolve called
//     afterwards must be harmless (must not resurrect a dead coroutine), and
//     a fresh wait() call afterwards must not spuriously panic (the waiter
//     registration must be cleared on teardown).
//  4. resolve called twice -> panics; the second call must not corrupt the
//     first result. wait called twice concurrently -> panics (single-waiter
//     guard).
//  5. Known, documented limitation: resolve racing loop.Close() can panic on
//     Close's caller — not fixable from inside this primitive. See
//     TestAwaitableCallback_ResolveRacesClose.
//  6. Positive control: run the rest of the package's test suite (done by
//     the "go test ./..." gate, not duplicated here).
//
// Race-detector coverage: TestAwaitableCallback_ResolvedFromForeignGoroutine,
// TestAwaitableCallback_ResolveTwice_ConcurrentCallers, and
// TestAwaitableCallback_ResolveRacesClose below are written to be meaningful
// under `go test -race`.

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
// resolving — it races the callback against wait()'s own registration.
// Because resolve always resumes through Resume() (a clock hop, never a
// direct call into the parked coroutine), this is safe regardless of exactly
// when, relative to Pause(), the race lands. Run with -race and with -count
// to shake out timing-dependent bugs.
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
// (2b) resolve from another already-running coroutine on the same loop
// ---------------------------------------------------------------------------

// TestAwaitableCallback_ResolvedFromAnotherCoroutine covers the third way
// resolve can be invoked: from another coroutine that is cooperatively
// running on the very same loop as the waiter (as opposed to a genuinely
// foreign goroutine). It goes through exactly the same Resume() path as the
// foreign-goroutine case — there is no separate "resolve in place" shortcut
// (see the doc comment on AwaitableCallback for why an earlier revision had
// one, and why it was removed). Consequently B's resolve() call schedules a
// task and returns immediately; A only resumes once the loop gets to that
// scheduled task, i.e. *after* B's own coroutine body has finished — proven
// here by both the execution order and the task count (3: A's task, B's
// task, and the scheduled resume).
func TestAwaitableCallback_ResolvedFromAnotherCoroutine(t *testing.T) {
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
		resolve("from-another-coroutine", nil)
		order = append(order, "B-done")
	})

	n, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)

	require.Equal(t, "from-another-coroutine", got)
	require.NoError(t, gotErr)
	// resolve() always defers through the clock, so B finishes its own body
	// ("B-done") before A gets resumed ("A-resumed").
	require.Equal(t, []string{"A-start", "B-run", "B-done", "A-resumed"}, order)
	require.Equal(t, 3, n, "resolve() schedules exactly one extra task to resume the waiter")
}

// ---------------------------------------------------------------------------
// (3) cancellation while waiting
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

	// Harmless-after-cancel: resolving now must not panic. wait()'s own
	// cleanup defer already cleared the waiter registration during the
	// Close() above, so resolve() here sees nobody is waiting and does not
	// even schedule anything (see TestAwaitableCallback_WaitAgainAfterCancel
	// for the direct proof that the registration was cleared).
	require.NotPanics(t, func() {
		resolve(99, nil)
	})

	_, err = clock.ProcessAll(context.Background())
	require.NoError(t, err, "there must be nothing left to drain after cancellation")
}

// TestAwaitableCallback_WaitAgainAfterCancel is the direct regression test
// for the bug independent review found: wait()'s registration used to leak
// when the waiting coroutine was torn down — a *fresh* wait() call on the
// same AwaitableCallback instance afterwards would spuriously panic with
// "supports exactly one waiter", even though nobody was actually waiting
// anymore. wait()'s cleanup defer now clears the registration unconditionally
// on the way out (including during cancellation unwind), so a later wait()
// call must succeed normally instead.
//
// The second wait() call is run to completion (a full clock.ProcessAll pass,
// not just enqueued) *before* resolve() fires again, so it genuinely reaches
// wait's "a.waiter != nil" check while a.done is still false — enqueuing
// alone would not exercise that check, since AddTask does not run the
// coroutine synchronously.
//
// RED verification: remove the `defer` block in wait() that clears a.waiter
// and the second wait() call panics with "supports exactly one waiter" —
// inside the freshly spawned coroutine's own goroutine, which RunCoroutine's
// recover re-panics (it is not the cancellation sentinel), crashing the
// whole test binary rather than failing cleanly. That is itself informative:
// it is why this primitive treats "waiter registration leaked" as a bug
// worth a dedicated regression test rather than trusting it to surface as an
// ordinary assertion failure.
func TestAwaitableCallback_WaitAgainAfterCancel(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	resolve, wait := coro.AwaitableCallback[int]()

	firstCleaned := make(chan struct{})
	loop.AddTask(func(ctx coro.Context) {
		defer close(firstCleaned)
		_, _ = wait(ctx) // never resolved; torn down by loop.Close() below
	})

	n, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)

	require.Equal(t, 1, loop.Close(), "the first (parked) waiter must be torn down")
	select {
	case <-firstCleaned:
	case <-time.After(time.Second):
		t.Fatal("first waiter's deferred cleanup did not run")
	}

	// A second, fresh coroutine calls wait() on the SAME AwaitableCallback
	// instance, before resolve() fires again — a.done is still false, so this
	// genuinely exercises the "a.waiter != nil" check.
	secondDone := make(chan struct{})
	var got int
	loop.AddTask(func(ctx coro.Context) {
		got, _ = wait(ctx)
		close(secondDone)
	})

	n, err = clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n, "second wait() must register and park normally, not panic")

	resolve(7, nil)

	_, err = clock.ProcessAll(context.Background())
	require.NoError(t, err)

	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second wait() call never resumed")
	}
	require.Equal(t, 7, got)
}

// ---------------------------------------------------------------------------
// (4) one-shot / single-waiter misuse panics
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
// (5) known, documented limitation: resolve racing loop.Close()
// ---------------------------------------------------------------------------

// TestAwaitableCallback_ResolveRacesClose demonstrates and characterizes a
// real race between resolve() and loop.Close() that AwaitableCallback cannot
// close on its own — see the "Known limitation" section of the doc comment
// on AwaitableCallback for the full account.
//
// In short: resolve's call to the waiter's Resume() un-pauses it
// asynchronously, through the clock. If loop.Close() concurrently calls
// Cancel() on that same waiter while it is in the brief window between
// "un-paused" and "paused or finished again", Cancel's own precondition
// check can fail and panic — on Close's caller's goroutine, with the message
// "coro: Cancel called on a coroutine that is not suspended". This is not
// unique to AwaitableCallback: the identical panic reproduces with plain
// ctx.Pause() and no AwaitableCallback involved at all, racing loop.Close()
// against a coroutine's own natural startup under chrono.RealClock — Close
// assumes nothing else can transition a registered coroutine's pause state
// concurrently, which chrono.RealClock does not guarantee. A real fix would
// have to live in YieldController.Cancel / eventLoopT.Close, which is out of
// scope for this primitive (see the boundary on not changing existing loop
// behavior); this test exists to document and bound the exposure, not to
// pretend it is fixed.
//
// The interleaving needed to hit the panic (loop.Close() must land inside a
// window that is normally microseconds wide, right after the waiter's
// asynchronous resume fires and before it re-pauses or finishes) is rare
// under pure goroutine-scheduler racing — under 1 hit per 300 tries measured
// during development. To keep this test meaningful rather than a coin flip
// that is 99%+ likely to report "0 hits" and prove nothing, the waiting
// coroutine deliberately widens its own post-resume window with a short
// real-time sleep, and loop.Close() is invoked a short, deliberate delay
// after resolve() (sequenced, not raced blindly) — giving resolve()'s
// AfterFunc(0) time to actually fire. This does not change *what* races —
// resolve's resume still goes through the same asynchronous Resume() call a
// production caller would use — it only makes the reproduction reliable
// instead of leaving it to chance. Measured at 300/300 during development;
// this test tolerates *exactly* the one documented panic message (counting
// it, not failing on it) and fails on anything else — a different panic
// message, a hang, or (run under -race) any data race — so it stays useful
// as a regression guard for "did something get WORSE" without being flaky
// about a limitation that is already known, explained, and out of this
// primitive's reach.
func TestAwaitableCallback_ResolveRacesClose(t *testing.T) {
	// Not t.Parallel() — gives the race detector isolated CPU time, and this
	// iterates internally.

	const knownRacePanic = "coro: Cancel called on a coroutine that is not suspended"
	const iterations = 300
	knownRaceHits := 0

	for i := 0; i < iterations; i++ {
		loop := coro.NewEventLoop(chrono.NewRealClock())
		resolve, wait := coro.AwaitableCallback[int]()

		waiting := make(chan struct{})
		loop.AddTask(func(ctx coro.Context) {
			close(waiting)
			_, _ = wait(ctx)
			// Widen the "resumed but not yet re-paused/finished" window —
			// this is what loop.Close() needs to land inside of to panic.
			time.Sleep(3 * time.Millisecond)
		})
		<-waiting
		time.Sleep(time.Millisecond) // let the coroutine's own startup settle

		resolve(i, nil)                    // schedules the resume via Resume()
		time.Sleep(500 * time.Microsecond) // give that AfterFunc(0) time to fire

		func() {
			defer func() {
				r := recover()
				if r == nil {
					return
				}
				if msg, ok := r.(string); ok && msg == knownRacePanic {
					knownRaceHits++
					return
				}
				t.Errorf("iteration %d: loop.Close() panicked with an unexpected value: %v", i, r)
			}()
			loop.Close()
		}()
	}

	t.Logf("known resolve-vs-Close race reproduced in %d/%d iterations", knownRaceHits, iterations)
	require.Greater(t, knownRaceHits, 0,
		"this test is supposed to reliably reproduce the known race; 0 hits means the reproduction itself broke (check timing), not that the race went away")
}

// ---------------------------------------------------------------------------
// (6) positive control
// ---------------------------------------------------------------------------
//
// Neighboring primitives (Mutex, plain loop tasks) are exercised by the rest
// of this package's test suite (mutex_test.go, current_test.go,
// urgent_test.go, teardown_test.go, ...) — running `go test ./...` alongside
// this file is the positive control that AwaitableCallback did not disturb
// them.
