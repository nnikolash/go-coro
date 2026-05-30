package coro

// coroutineRegistry is implemented by event loops that track their live
// coroutines so they can be torn down on Close. It is an internal detail of
// eventLoopT; RunCoroutine registers through it when available so leaked
// goroutines from suspended coroutines can be reclaimed.
type coroutineRegistry interface {
	registerCoroutine(ctrl *YieldController)
	unregisterCoroutine(ctrl *YieldController)
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
}
