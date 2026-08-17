package coro

// NewSemaphore constructs a Semaphore allowing up to limit concurrent
// holders. limit<=0 is normalized to 1 — a Semaphore with capacity 1 behaves
// like Mutex (which is implemented in terms of Semaphore; see Mutex's doc).
func NewSemaphore(limit int) *Semaphore {
	if limit <= 0 {
		limit = 1
	}
	return &Semaphore{limit: limit}
}

// Semaphore is a coroutine-native counting semaphore: Acquire blocks the
// CALLING COROUTINE (via ctx.Pause/ctx.Resume) rather than the underlying OS
// goroutine or a channel — the same shape Mutex uses, generalized from one
// permit to N. A `select { case sem <- struct{}{}: }` channel semaphore
// called FROM a coroutine running on an event loop would block the one
// goroutine the loop's pump needs to yield, stalling every OTHER coroutine on
// that loop too — not just the one waiting for a permit. Semaphore avoids
// that by pausing only the calling coroutine.
//
// Like Mutex, Semaphore has NO internal sync.Mutex: it relies on the event
// loop's single-threaded-cooperative guarantee (only one coroutine's body
// executes at a time on a given loop; Acquire/Release must only ever be
// called from coroutines running on the SAME event loop). It is NOT a
// general-purpose semaphore safe for arbitrary goroutines.
//
// Zero value is not usable — construct with NewSemaphore, which normalizes
// limit<=0 to 1 (a zero-value Semaphore has limit 0, so Acquire would park
// forever: there is no permit and nothing will ever Release one).
type Semaphore struct {
	limit   int
	held    int
	waiters []func()
}

// Acquire reserves one permit, pausing the calling coroutine (ctx.Pause) if
// none is free. Fast path (permit immediately available): returns without
// pausing — the coroutine never yields, matching Mutex.Lock and
// AwaitableCallback.wait's "already resolved, no yield" behavior, so
// deterministic simulations do not gain a spurious scheduling hop just
// because a semaphore happened to be involved.
func (s *Semaphore) Acquire(ctx Context) {
	if s.held < s.limit {
		s.held++
		return
	}
	s.waiters = append(s.waiters, ctx.Resume)
	ctx.Pause()
}

// Release frees one permit. If a coroutine is waiting, the freed permit is
// handed directly to the oldest waiter (FIFO) via its own Resume — held stays
// unchanged in that case, mirroring Mutex.Unlock's handoff.
func (s *Semaphore) Release() {
	if len(s.waiters) == 0 {
		s.held--
		return
	}
	waiter := s.waiters[0]
	s.waiters = s.waiters[1:]
	waiter()
}
