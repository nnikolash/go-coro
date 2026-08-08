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

	// Obtain the escape-handler provider once, before launching the goroutine,
	// so the type assertion is not repeated on every escape (optimisation) and
	// the goroutine closure captures a stable interface value.
	escapeProv, _ := evtLoop.(escapeHandlerProvider)

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
		// Recover wrapper — handles three cases:
		//   1. nil recover (normal return): nothing to do.
		//   2. errCoroutineCanceled sentinel: swallow so the goroutine exits cleanly.
		//   3. escapeRequest sentinel: call the registered escape handler (after
		//      the coroutine's own defers have fully unwound), then return.
		//      The handler runs in this goroutine; it must be non-blocking because
		//      the event loop is still parked in WaitUntilYielded — ctrl.Done()
		//      only fires after this defer returns, unblocking the loop.
		//   4. Anything else: re-panic so real bugs surface unchanged.
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			if r == errCoroutineCanceled {
				return
			}
			if esc, ok := r.(escapeRequest); ok {
				var handler func(any)
				if escapeProv != nil {
					handler = escapeProv.getEscapeHandler()
				}
				if handler == nil {
					panic(noEscapeHandlerPanic(esc.value))
				}
				handler(esc.value)
				return
			}
			panic(r)
		}()

		f(ctx)
	}()

	ctrl.WaitUntilYielded()

	if tracker != nil {
		tracker.setCurrentCoroutine(prev)
	}
}
