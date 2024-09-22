package coro

func MakeCoroutine(evtLoop EventLoop, f func(ctx Context)) func() {
	return func() { RunCoroutine(evtLoop, f) }
}

func RunCoroutine(evtLoop EventLoop, f func(ctx Context)) {
	ctrl := NewYieldController(nil)
	ctx := newContext(evtLoop, ctrl)

	go func() {
		defer ctrl.Done()

		f(ctx)
	}()

	ctrl.WaitUntilYielded()
}
