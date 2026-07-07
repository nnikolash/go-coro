package coro

// coroutineRegistry is implemented by event loops that track their live
// coroutines so they can be torn down on Close. It is an internal detail of
// eventLoopT; RunCoroutine registers through it when available so leaked
// goroutines from suspended coroutines can be reclaimed.
type coroutineRegistry interface {
	registerCoroutine(ctrl *YieldController)
	unregisterCoroutine(ctrl *YieldController)
}

// currentTracker is implemented by event loops that track the currently-running
// coroutine. It is an internal detail of eventLoopT; enter-sites use it to
// maintain the current *contextT pointer with save/restore semantics so that
// loop.Sleep / loop.CurrentScheduler work from any depth of the call stack
// without threading coro.Context through every signature.
type currentTracker interface {
	// setCurrentCoroutine atomically stores ctx as the active coroutine and
	// returns the previous value. Callers must restore the returned value when
	// the coroutine yields again (save/restore pattern).
	setCurrentCoroutine(ctx *contextT) *contextT
}

func MakeCoroutine(evtLoop EventLoop, f func(ctx Context)) func() {
	return func() { RunCoroutine(evtLoop, f) }
}

func RunCoroutine(evtLoop EventLoop, f func(ctx Context)) {
	ctrl := NewYieldController(nil)
	ctx := newContext(evtLoop, ctrl)

	reg, _ := evtLoop.(coroutineRegistry)
	if reg != nil {
		reg.registerCoroutine(ctrl)
	}

	// Enter-site 1/5: first-run. Set current BEFORE starting the goroutine so
	// that code running at the very beginning of f(ctx) already sees itself as
	// the active coroutine. Restore after the first Yield.
	tracker, _ := evtLoop.(currentTracker)
	var prev *contextT
	if tracker != nil {
		prev = tracker.setCurrentCoroutine(ctx)
	}

	go func() {
		if reg != nil {
			defer reg.unregisterCoroutine(ctrl)
		}
		defer ctrl.Done()
		// Swallow the cancel sentinel (raised by ctrl.Cancel via Yield) so a
		// torn-down coroutine exits cleanly; re-panic anything else.
		defer func() {
			if r := recover(); r != nil && r != errCoroutineCanceled {
				panic(r)
			}
		}()

		f(ctx)
	}()

	ctrl.WaitUntilYielded()

	if tracker != nil {
		tracker.setCurrentCoroutine(prev)
	}
}
