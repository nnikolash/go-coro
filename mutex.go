package coro

func NewMutex() *Mutex {
	return &Mutex{}
}

type Mutex struct {
	locked  bool
	waiters []func()
}

func (m *Mutex) Lock(ctx Context) {
	if !m.locked {
		m.locked = true
		return
	}

	m.waiters = append(m.waiters, ctx.Resume)
	ctx.Pause()
}

func (m *Mutex) Unlock() {
	if len(m.waiters) <= 0 {
		m.locked = false
		return
	}

	waiter := m.waiters[0]
	m.waiters = m.waiters[1:]
	waiter()
}
