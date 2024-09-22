package coro

import (
	"sync"
)

// TODO: using existing coroutine scheduler, something like https://github.com/nvlled/carrot/tree/main

type Yield = func()

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
	onYielded       func()
}

func (t *YieldController) Yield() {
	t.stateToggledEvt.L.Lock()
	defer t.stateToggledEvt.L.Unlock()

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

	if t.paused || t.finished {
		panic("continued thread expected to not be paused or finished")
	}
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
