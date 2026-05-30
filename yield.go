package coro

import (
	"sync"
)

// TODO: using existing coroutine scheduler, something like https://github.com/nvlled/carrot/tree/main

type Yield = func()

// errCoroutineCanceled is the sentinel panic value used to tear down a suspended
// coroutine. Cancel resumes the parked goroutine with canceled=true; the next
// Yield then panics this value, unwinding the coroutine's stack (running its
// defers) up to the recover installed in RunCoroutine. A distinct pointer is
// used so recover can match it by identity and re-panic anything else.
var errCoroutineCanceled = &struct{ name string }{"coro: coroutine canceled"}

func GoYielding(action func(yield Yield), onYielded func()) *YieldController {
	ctrl := NewYieldController(onYielded)
	go func() {
		defer ctrl.Done()
		action(ctrl.Yield)
	}()

	return ctrl
}

func NewYieldController(onYielded func()) *YieldController {
	t := &YieldController{
		stateToggledEvt: sync.Cond{L: &sync.Mutex{}},
		paused:          false,
		finished:        false,
		onYielded:       onYielded,
	}

	return t
}

type YieldController struct {
	stateToggledEvt sync.Cond
	paused          bool
	finished        bool
	canceled        bool
	onYielded       func()
}

func (t *YieldController) Yield() {
	t.stateToggledEvt.L.Lock()
	defer t.stateToggledEvt.L.Unlock()

	// Already being torn down (e.g. a deferred Sleep/Pause during cancel
	// unwinding): do not suspend again, just keep the panic propagating.
	if t.canceled {
		panic(errCoroutineCanceled)
	}
	if t.paused {
		panic("yielding thread expected to be not be paused")
	}
	if t.finished {
		panic("yielding thread expected to be not be finished")
	}

	t.paused = true
	t.stateToggledEvt.Broadcast()

	if t.onYielded != nil {
		t.onYielded()
	}

	t.stateToggledEvt.Wait()

	// Resumed only so that the coroutine can be torn down: panic out of it.
	if t.canceled {
		panic(errCoroutineCanceled)
	}
	if t.paused || t.finished {
		panic("continued thread expected to not be paused or finished")
	}
}

// Cancel tears down a SUSPENDED coroutine. It must be called while the coroutine
// is paused (i.e. from the event-loop thread while nothing is running) — Close
// guarantees this. It resumes the parked goroutine in cancel mode, which makes
// the coroutine's current Yield panic errCoroutineCanceled; the panic unwinds
// the coroutine stack (running its defers) and exits the goroutine. Cancel
// blocks until the coroutine has finished unwinding. Returns false if there was
// nothing to cancel (already finished).
func (t *YieldController) Cancel() bool {
	t.stateToggledEvt.L.Lock()
	defer t.stateToggledEvt.L.Unlock()

	if t.finished {
		return false
	}
	if !t.paused {
		panic("coro: Cancel called on a coroutine that is not suspended")
	}

	t.canceled = true
	t.paused = false
	t.stateToggledEvt.Broadcast()
	t.stateToggledEvt.Wait()

	if !t.finished {
		panic("coro: coroutine did not finish after Cancel")
	}

	return true
}

func (t *YieldController) Continue() bool {
	t.stateToggledEvt.L.Lock()
	defer t.stateToggledEvt.L.Unlock()

	if t.finished {
		return false
	}

	if t.paused {
		t.paused = false
		t.stateToggledEvt.Broadcast()
	}

	return true
}

func (t *YieldController) WaitUntilYielded() {
	t.stateToggledEvt.L.Lock()
	defer t.stateToggledEvt.L.Unlock()

	if t.paused || t.finished {
		return
	}

	t.stateToggledEvt.Wait()

	if !t.finished && !t.paused {
		panic("yielding thread expected to be paused or finished")
	}
}

func (t *YieldController) RunUntilYielded() {
	t.stateToggledEvt.L.Lock()
	defer t.stateToggledEvt.L.Unlock()

	if t.finished {
		return
	}

	if t.paused {
		t.paused = false
		t.stateToggledEvt.Broadcast()
	}

	t.stateToggledEvt.Wait()

	if !t.finished && !t.paused {
		panic("yielding thread expected to be paused or finished")
	}
}

func (t *YieldController) Done() {
	t.stateToggledEvt.L.Lock()
	defer t.stateToggledEvt.L.Unlock()

	t.finished = true
	t.stateToggledEvt.Broadcast()
}
