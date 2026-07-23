package coro_test

import (
	"context"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	chrono "github.com/nnikolash/go-chrono"
	"github.com/nnikolash/go-coro"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// subprocessMode is the env-var key used to run a subprocess that intentionally
// crashes. Tests 3 and 4 use this pattern because a goroutine panic cannot be
// caught from a different goroutine (require.Panics doesn't work across goroutine
// boundaries); the only correct way to verify the process crashes is via subprocess.
const subprocessMode = "CORO_ESCAPE_SUBPROCESS"

// runCrashSubprocess executes the named test function as a subprocess and
// verifies the subprocess exits with a non-zero status (i.e., it crashed).
func runCrashSubprocess(t *testing.T, testName string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+testName+"$", "-test.v")
	cmd.Env = append(os.Environ(), subprocessMode+"=1")
	out, err := cmd.CombinedOutput()
	t.Logf("subprocess output:\n%s", out)
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr,
		"subprocess must crash (exit non-zero); if it exits cleanly the library is swallowing panics")
}

// ---------------------------------------------------------------------------
// (1) Escape delivers value to the handler
// ---------------------------------------------------------------------------

// TestEscape_DeliversValueToHandler verifies that the value passed to
// coro.Escape is delivered intact to the escape handler registered on the loop.
func TestEscape_DeliversValueToHandler(t *testing.T) {
	t.Parallel()
	loop := coro.NewEventLoop(chrono.NewRealClock())

	type sentinelT struct{ n int }
	want := sentinelT{n: 42}

	received := make(chan any, 1)
	loop.SetEscapeHandler(func(v any) { received <- v })

	loop.AddTask(func(_ coro.Context) {
		coro.Escape(want) // never returns — defers run, then handler fires
	})

	select {
	case got := <-received:
		require.Equal(t, want, got, "escape handler must receive the exact value passed to Escape")
	case <-time.After(5 * time.Second):
		t.Fatal("escape handler was not called within 5s")
	}
}

// ---------------------------------------------------------------------------
// (2) Coroutine defers run before the handler is called
// ---------------------------------------------------------------------------

// TestEscape_DefersRunBeforeHandler verifies that all of the escaping
// coroutine's deferred functions execute before the escape handler is called.
// This guarantees that cleanup (e.g. sending a TG confirmation) completes
// before cmd/bot triggers the restart.
func TestEscape_DefersRunBeforeHandler(t *testing.T) {
	t.Parallel()
	loop := coro.NewEventLoop(chrono.NewRealClock())

	// order is written only from the coroutine goroutine (defers + handler),
	// then read from the test goroutine after the channel sync — no race.
	var order []string
	record := func(s string) { order = append(order, s) }

	handlerCh := make(chan struct{}, 1)
	loop.SetEscapeHandler(func(_ any) {
		record("handler")
		handlerCh <- struct{}{}
	})

	loop.AddTask(func(_ coro.Context) {
		defer record("defer-outer")
		defer record("defer-inner")
		coro.Escape("go")
	})

	select {
	case <-handlerCh:
	case <-time.After(5 * time.Second):
		t.Fatal("escape handler was not called within 5s")
	}
	// At this point the handler has finished appending "handler" and sent on
	// handlerCh — happens-before ensures the test goroutine sees all writes.
	require.Equal(t, []string{"defer-inner", "defer-outer", "handler"}, order,
		"deferred cleanups must run BEFORE the escape handler")
}

// ---------------------------------------------------------------------------
// (3) Normal panics still propagate (subprocess)
// ---------------------------------------------------------------------------

// TestEscape_NormalPanicPropagates verifies that a non-escape panic is NOT
// swallowed by the coro escape recover wrapper and instead propagates, crashing
// the process. We use a subprocess because a goroutine panic cannot be caught
// from outside that goroutine; the subprocess's non-zero exit is the proof.
//
// Logic: if RunCoroutine incorrectly routes the non-escape panic to the escape
// handler (a bug), the handler runs, ctrl.Done fires, the goroutine exits
// cleanly → subprocess exits 0 → test FAILS. If RunCoroutine correctly
// re-panics, the goroutine crashes the process → subprocess exits non-zero →
// test PASSES.
func TestEscape_NormalPanicPropagates(t *testing.T) {
	if os.Getenv(subprocessMode) != "1" {
		t.Parallel()
		runCrashSubprocess(t, "TestEscape_NormalPanicPropagates")
		return
	}
	// Subprocess path: trigger the real-bug panic.
	loop := coro.NewEventLoop(chrono.NewRealClock())
	loop.SetEscapeHandler(func(_ any) {}) // must NOT be called for non-escape panic
	loop.AddTask(func(_ coro.Context) {
		panic("real bug — must propagate, not be routed to escape handler")
	})
	time.Sleep(5 * time.Second) // give the goroutine time to crash the process
	// If we reach here the panic was swallowed (wrong behaviour).
	os.Exit(0)
}

// ---------------------------------------------------------------------------
// (4) Escape without a handler re-panics (subprocess)
// ---------------------------------------------------------------------------

// TestEscape_WithoutHandler_Repanics verifies that calling coro.Escape when no
// handler is registered panics (fail-loud). As with test 3, we use a subprocess
// because the resulting crash cannot be caught across goroutine boundaries.
func TestEscape_WithoutHandler_Repanics(t *testing.T) {
	if os.Getenv(subprocessMode) != "1" {
		t.Parallel()
		runCrashSubprocess(t, "TestEscape_WithoutHandler_Repanics")
		return
	}
	// Subprocess path: escape without registered handler.
	t0 := time.Unix(0, 0)
	clock := chrono.NewSimulator(t0)
	loop := coro.NewEventLoop(clock)
	// No SetEscapeHandler call — handler is nil.
	loop.AddTask(func(_ coro.Context) {
		coro.Escape("oops")
	})
	_, _ = clock.ProcessAll(context.Background())
	time.Sleep(5 * time.Second)
	os.Exit(0)
}

// ---------------------------------------------------------------------------
// (5) Race-detector pass — concurrent escapes
// ---------------------------------------------------------------------------

// TestEscape_Race verifies no data races under the -race flag: multiple
// concurrent coroutines each call Escape, all triggering the same handler.
func TestEscape_Race(t *testing.T) {
	// Intentionally not t.Parallel() so the race-detector coverage is isolated.
	const n = 8
	loop := coro.NewEventLoop(chrono.NewRealClock())

	var callCount atomic.Int32
	allDone := make(chan struct{})
	var closeOnce atomic.Bool
	loop.SetEscapeHandler(func(_ any) {
		if callCount.Add(1) == n && closeOnce.CompareAndSwap(false, true) {
			close(allDone)
		}
	})

	for i := range n {
		loop.AddTask(func(_ coro.Context) {
			coro.Escape(i)
		})
	}

	select {
	case <-allDone:
	case <-time.After(10 * time.Second):
		t.Fatalf("only %d of %d escape handlers fired within 10s", callCount.Load(), n)
	}
	assert.Equal(t, int32(n), callCount.Load())
}
