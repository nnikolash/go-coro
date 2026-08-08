package coro

import "sync"

// AwaitableCallback constructs a one-shot, single-waiter bridge between an
// ordinary callback and a coroutine: a producer calls the returned resolve
// function exactly once with its result, and a coroutine calls the returned
// wait function (at most once) to obtain that result.
//
//	resolve, wait := coro.AwaitableCallback[Subscription]()
//	sdk.Subscribe(topic, func(sub Subscription, err error) { resolve(sub, err) })
//	sub, err := wait(ctx) // called from inside a coroutine
//
// resolve may be called before wait, from a coroutine running on the same
// event loop, or from a goroutine that has nothing to do with the loop at
// all (an exchange SDK's own read loop, say) — that is the whole reason this
// primitive exists instead of a plain closure-captured variable.
//
// Two semantics are load-bearing, and the second is where this differs from
// coro.Mutex (its nearest sibling in this package): Mutex assumes a
// single-threaded loop and is never touched from a foreign goroutine, so it
// can always resume waiters via ctx.Resume() (a clock hop). AwaitableCallback
// cannot make that assumption.
//
//  1. If resolve has already run by the time wait is called, wait returns
//     immediately: the calling coroutine does not yield, and nothing is
//     scheduled on the event loop. This is the case that matters most — see
//     the "AlreadyResolved" test for why. In a deterministic simulation the
//     producer's answer is often already known; inserting a scheduling hop
//     here would make two runs of the same simulation free to interleave
//     that hop differently against other zero-delay events, and replays
//     would diverge. A "resolve always defers, like a JS `then`" design does
//     not satisfy this.
//  2. If resolve runs after wait has started waiting, the waiting coroutine
//     is resumed through the same machinery Context.Resume uses
//     (contextT.runUntilYielded, including its current-tracker save/restore)
//     — never a raw call into the coroutine's own code. What varies is *how*
//     that resumption is triggered — see "Resolving in place" below.
//
// # Resolving in place
//
// The naive direct call — "if the waiter's YieldController is already
// paused, just call runUntilYielded right here" — is NOT safe on its own,
// even though the paused check is read under YieldController's own lock.
// Here is the race a stress test (ResolvedFromForeignGoroutine_
// RacesIntoRegistration) actually reproduced with that version:
//
// RunCoroutine (coro.go) starts a coroutine by launching its goroutine and
// then calling ctrl.WaitUntilYielded() — and on chrono.RealClock that whole
// call happens *inside* RealClock.executeTask's handlersLock (go-chrono
// clock.go), which is what serializes it against every other clock callback
// on that loop, including a later ctx.Resume()'s AfterFunc(0). A *direct*
// call to runUntilYielded from an unsynchronized foreign goroutine is not
// inside that lock. So: the waiter sets paused=true and parks in Yield's
// Wait(); a foreign resolve() sees isPaused() == true and calls
// runUntilYielded directly, which flips paused back to false, broadcasts,
// and starts its *own* Wait(); that broadcast can wake the coroutine's
// still-pending initial WaitUntilYielded() first — at which point paused is
// false again (the foreign call just cleared it) and finished is still
// false, and WaitUntilYielded panics ("yielding thread expected to be
// paused or finished"). isPaused() answered correctly at the instant it was
// read; the problem is that *acting* on it via a direct call raced a
// synchronization point (the initial enter-site) that only clock.AfterFunc
// is serialized against.
//
// The fix is to gate the direct call on the currentTracker instead of (in
// addition to) isPaused: resolve calls runUntilYielded in place only when
// currentCoroutine() reports some coroutine *other than the waiter itself*
// is presently active on the loop. That is sufficient, not just a heuristic:
// under both chrono.Simulator (single active execution) and chrono.RealClock
// (executeTask's handlersLock), a *different* coroutine can only become
// "current" after the waiter's own initial WaitUntilYielded() has already
// returned — the same mutex/single-thread invariant that makes the loop
// cooperative in the first place. So "some other coroutine is current" both
// implies the waiter has already reached a stable paused-or-finished state
// AND implies we are not a foreign, unsynchronized goroutine — which is
// exactly Mutex's own precondition for calling a waiter directly, just
// checked explicitly instead of assumed. Comparing against the waiter
// itself (not just against nil) matters separately: RunCoroutine marks a
// coroutine "current" *before* spawning its goroutine, so during the narrow
// window between wait() registering itself and actually reaching Pause, a
// foreign goroutine racing in at that exact instant would see "current ==
// the waiter" — which must NOT be treated as "some other coroutine is
// cooperatively running right now".
//
//   - currentCoroutine() reports some coroutine other than the waiter is
//     current: resolve calls runUntilYielded directly, in place — no clock
//     hop. Always true when resolve runs cooperatively from the loop itself
//     (e.g. from another already-running coroutine, mirroring Mutex.Unlock
//     calling a waiter's Resume).
//   - Otherwise (no currentTracker, current is nil, or current is the
//     waiter itself — including the foreign-goroutine race above): resolve
//     falls back to the waiter's Resume(), which schedules through the
//     clock. chrono's AfterFunc/UntilFunc/EveryFunc are documented as
//     mutex-guarded and safe to call from any goroutine (see
//     chrono.Simulator's doc comment), which is exactly what a
//     foreign-goroutine callback needs, and — per the paragraph above — is
//     also what correctly serializes it against the waiter's own enter-site.
//
// isPaused (yield.go) is still checked, in addition to the currentTracker
// check, as a second, independent guard before taking the in-place path
// (belt and suspenders: cheap, and it is what makes the "no lost wakeup"
// argument in the doc for isPaused explicit at the call site). It is not,
// on its own, sufficient — that is the bug this comment documents.
//
// # One-shot, single waiter
//
// resolve panics if called a second time — the same reasoning that makes
// closing a channel twice panic: a second call is essentially always a
// producer bug (e.g. both a success and an error path firing), and silently
// ignoring it would hide that bug behind a "first answer wins" behavior
// nobody asked for. wait panics if called while a previous call has not yet
// returned — this primitive supports exactly one waiter; if a second
// consumer is ever needed, it should be added deliberately (fan-out,
// multiple resolutions) rather than discovered as a silently-dropped waiter.
func AwaitableCallback[T any]() (resolve func(v T, err error), wait func(ctx Context) (T, error)) {
	a := &awaitableCallback[T]{}
	return a.resolve, a.wait
}

type awaitableCallback[T any] struct {
	mu     sync.Mutex
	done   bool
	value  T
	err    error
	waiter *contextT
}

func (a *awaitableCallback[T]) resolve(v T, err error) {
	a.mu.Lock()
	if a.done {
		a.mu.Unlock()
		panic("coro: AwaitableCallback resolved more than once")
	}
	a.done = true
	a.value = v
	a.err = err
	waiter := a.waiter
	a.mu.Unlock()

	if waiter == nil {
		// Nobody is waiting yet. wait() will see a.done == true and return
		// immediately, without ever calling Pause — satisfies semantic 1.
		return
	}

	if a.resolvingInPlaceIsSafe(waiter) {
		// See the "Resolving in place" doc comment above for why both of
		// these conditions together (not either alone) make this safe.
		waiter.runUntilYielded()
		return
	}

	// Not provably safe to call directly: marshal through the clock instead
	// — exactly what Context.Resume does, and (per the doc comment above)
	// what correctly serializes against the waiter's own enter-site.
	waiter.Resume()
}

// resolvingInPlaceIsSafe reports whether resolve may call waiter.
// runUntilYielded directly instead of going through waiter.Resume(). See the
// "Resolving in place" section of AwaitableCallback's doc comment.
func (a *awaitableCallback[T]) resolvingInPlaceIsSafe(waiter *contextT) bool {
	if waiter.tracker == nil {
		return false // no way to tell; always safe to fall back to Resume().
	}

	cur := waiter.tracker.currentCoroutine()
	if cur == nil || cur == waiter {
		return false
	}

	return waiter.ctrl.isPaused()
}

func (a *awaitableCallback[T]) wait(ctx Context) (T, error) {
	c, ok := ctx.(*contextT)
	if !ok {
		panic("coro: AwaitableCallback.wait requires the Context produced by this package's event loop")
	}

	a.mu.Lock()
	if a.done {
		v, err := a.value, a.err
		a.mu.Unlock()
		return v, err
	}
	if a.waiter != nil {
		a.mu.Unlock()
		panic("coro: AwaitableCallback supports exactly one waiter")
	}
	a.waiter = c
	a.mu.Unlock()

	c.Pause()

	a.mu.Lock()
	v, err := a.value, a.err
	a.mu.Unlock()

	return v, err
}
