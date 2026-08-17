package coro

// NewMutex constructs a coroutine-native mutual-exclusion lock.
func NewMutex() *Mutex {
	return &Mutex{sem: NewSemaphore(1)}
}

// Mutex is a coroutine-native mutual-exclusion lock — a Semaphore with a
// single permit; see Semaphore's doc for the concurrency model both share
// (pausing the CALLING COROUTINE via ctx.Pause/ctx.Resume rather than
// blocking a goroutine or a channel, and single-event-loop-only safety).
//
// Kept as its own named type rather than a plain `type Mutex = Semaphore`
// alias: Lock/Unlock read better than Acquire/Release at a call site
// guarding a single critical section, and the distinct name documents that
// intent regardless of the shared implementation underneath.
//
// Zero value is not usable — construct with NewMutex (same reason as
// Semaphore's zero value: no permit exists yet for Lock to hand out or
// Unlock to free).
type Mutex struct {
	sem *Semaphore
}

// Lock acquires the mutex, pausing the calling coroutine (ctx.Pause) if it is
// already held. See Semaphore.Acquire for the fast-path/yield behavior.
func (m *Mutex) Lock(ctx Context) {
	m.sem.Acquire(ctx)
}

// Unlock releases the mutex, handing it directly to the oldest waiting
// coroutine (FIFO) if any. See Semaphore.Release.
func (m *Mutex) Unlock() {
	m.sem.Release()
}
