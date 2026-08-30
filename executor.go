package coro

import "fmt"

// Executor runs blocking work OFF the calling coroutine and reports the
// outcome through resolve. It decides HOW the work runs — on a separate
// goroutine, or synchronously before returning — never WHETHER; that choice
// is made once, by whoever constructs the Executor, not by the caller of
// ExecuteBlocking and not by Executor itself asking "am I live or
// simulated". See ExecuteBlocking's doc comment for the owner rule this
// follows: a component gets a parameter ON BEHAVIOR, never a way to query
// its own mode.
//
// AwaitableCallback already provides the "park a coroutine / wake it with a
// result" half of this (resolve/wait). Executor is the other half — WHO
// starts the work that eventually calls resolve — which AwaitableCallback
// deliberately leaves to its caller. Executor names that missing half as its
// own replaceable thing instead of leaving it implicit in each call site.
type Executor interface {
	// Execute runs fn and calls resolve exactly once with its outcome.
	// Implementations decide when/where fn runs; they must still honor
	// AwaitableCallback's one-shot resolve contract (calling resolve twice
	// panics — see AwaitableCallback's doc comment).
	Execute(fn func() (any, error), resolve func(any, error))
}

// GoroutineExecutor runs fn on a new goroutine and calls resolve with its
// result. This is the LIVE executor: the calling coroutine parks (via
// AwaitableCallback.wait) and the event loop is free to run other tasks
// while fn is in flight — see ExecuteBlocking's doc comment.
//
// A panic inside fn is recovered here and delivered to resolve as an error
// instead of propagating. Executor has no idea what fn does; an unrecovered
// panic on a detached goroutine is fatal to the whole process (Go does not
// let a caller recover a panic on another goroutine), which is a strictly
// worse failure mode than a returned error. Mirrors
// cacheutil.SWRCache.kickBackground's own background-refresh goroutine,
// which recovers for the identical reason.
type GoroutineExecutor struct{}

var _ Executor = GoroutineExecutor{}

func (GoroutineExecutor) Execute(fn func() (any, error), resolve func(any, error)) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				resolve(nil, fmt.Errorf("coro: GoroutineExecutor: fn panicked: %v", r))
			}
		}()
		v, err := fn()
		resolve(v, err)
	}()
}

// SyncExecutor runs fn synchronously, on the calling goroutine, before
// Execute returns. This is the SIMULATION executor: resolve fires before
// ExecuteBlocking ever calls wait, so wait's fast path (AwaitableCallback's
// own a.done check) returns without pausing the coroutine — no scheduling
// hop is inserted, so a deterministic simulation stays deterministic: two
// runs of the same simulation cannot interleave a hop that never happens.
//
// A panic inside fn is NOT recovered — it propagates normally up the calling
// stack, exactly like calling fn directly, because Execute runs on the same
// goroutine as its caller; there is nothing to protect against that isn't
// already protected by the caller's own recover (if any).
type SyncExecutor struct{}

var _ Executor = SyncExecutor{}

func (SyncExecutor) Execute(fn func() (any, error), resolve func(any, error)) {
	v, err := fn()
	resolve(v, err)
}

// ExecuteBlocking runs fn through ex and returns its result to the coroutine
// currently running on ctx's event loop. ctx must be the Context of that
// coroutine — same requirement as AwaitableCallback.wait, which this is
// built on: ExecuteBlocking does not add any new coroutine-parking
// machinery, it wires an Executor's Execute into an AwaitableCallback pair.
//
// The component calling ExecuteBlocking never asks "am I live or
// simulated" — it only holds an Executor, injected by whoever constructed
// it. This is the owner's rule for mode-dependent behavior: a component
// receives a parameter ON BEHAVIOR (the Executor), never a way to detect or
// query its own mode.
func ExecuteBlocking[T any](ctx Context, ex Executor, fn func() (T, error)) (T, error) {
	resolve, wait := AwaitableCallback[any]()
	ex.Execute(func() (any, error) { return fn() }, resolve)
	v, err := wait(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	return v.(T), nil
}
