package coro

import "sync"

// AwaitableCallback constructs a one-shot, single-waiter bridge between an
// ordinary callback and a coroutine: a producer calls the returned resolve
// function exactly once with its result, and a coroutine calls the returned
// wait function (at most once) to obtain that result.
//
//	resolve, wait := coro.AwaitableCallback[Subscription]()
//	sdk.Subscribe(topic, func(sub Subscription, err error) { resolve(sub, err) })
//	sub, err := wait(ctx) // called from inside a coroutine
//
// resolve may be called before wait, from a coroutine running on the same
// event loop, or from a goroutine that has nothing to do with the loop at
// all (an exchange SDK's own read loop, say) — that is the whole reason this
// primitive exists instead of a plain closure-captured variable.
//
// # The one semantic that matters
//
// If resolve has already run by the time wait is called, wait returns
// immediately: the calling coroutine does not yield, and nothing is
// scheduled on the event loop (see wait's a.done fast path below). This is
// the case the whole primitive exists for. The canonical caller looks like:
//
//	resolve, wait := coro.AwaitableCallback[Sub]()
//	client.SubscribeOnOrderBook(sym, depth, handler, resolve) // simulator calls resolve HERE
//	sub, err := wait(ctx)                                     // done already true -> instant return
//
// A simulated exchange client typically calls its "subscribed" callback
// synchronously, inside the call that registers the subscription — i.e.
// strictly *before* wait is ever called. In a deterministic simulation that
// answer is already known; inserting a scheduling hop here would make two
// runs of the same simulation free to interleave that hop differently
// against other zero-delay events, and replays would diverge. Proven by
// TestAwaitableCallback_AlreadyResolved_NoYield via the event loop's own
// task counter (not timing): an "always defer, like a JS `then`" design
// would show up as an extra scheduled task, and does when neutralized.
//
// If resolve instead runs *after* wait has started waiting (the callback
// arrives later — a real subscription confirmation, a real order fill), the
// waiting coroutine is resumed via the waiter's own Resume(), i.e. through
// the event loop's clock — never a direct call into the parked coroutine's
// code. That is a deliberate simplification: an earlier revision of this
// primitive tried to resume the waiter in place (no clock hop) whenever it
// could "prove" doing so was safe, gated on the currentTracker. Independent
// review found a real counterexample: a foreign goroutine racing in while
// the loop happens to be running some unrelated coroutine reads "some other
// coroutine is current" and (wrongly) concludes "safe", then runs the
// waiter's body on the SDK's own goroutine concurrently with the loop — and
// the fix is not a better predicate, it is removing the shortcut: the
// determinism this primitive exists for comes entirely from wait's fast
// path (above), not from resolving a *parked* waiter without a clock hop.
// Always going through Resume() is simpler and closes that whole class of
// bug at once.
//
// # One-shot, single waiter
//
// resolve panics if called a second time — the same reasoning that makes
// closing a channel twice panic: a second call is essentially always a
// producer bug (e.g. both a success and an error path firing), and silently
// ignoring it would hide that bug behind a "first answer wins" behavior
// nobody asked for. wait panics if called while a previous call has not yet
// returned — this primitive supports exactly one waiter at a time; if a
// second consumer is ever needed, it should be added deliberately (fan-out,
// multiple resolutions) rather than discovered as a silently-dropped waiter.
//
// wait clears its own registration on the way out — including when Pause
// panics with the coroutine-cancellation sentinel during teardown — so a
// waiter that was cancelled while parked does not leave the primitive
// permanently unusable: a resolve() that arrives afterwards sees nobody is
// waiting (a harmless no-op) instead of trying to resume a dead coroutine,
// and a fresh wait() call does not spuriously panic with "supports exactly
// one waiter" when in fact nobody is waiting anymore. See
// TestAwaitableCallback_WaitAgainAfterCancel.
//
// # Known limitation: resolve racing loop.Close()
//
// There is a real race between resolve() and loop.Close() that this
// primitive does NOT close, and cannot close without changing Cancel/Close
// themselves (out of scope for this primitive — see
// TestAwaitableCallback_ResolveRacesClose for the full account and a
// reproduction). In short: resolve's call to the waiter's Resume() un-pauses
// it asynchronously, through the clock; if loop.Close() concurrently calls
// Cancel() on that same waiter while it is in the brief window between
// "un-paused" and "paused or finished again", Cancel()'s own precondition
// check ("must be called while paused") can fail and panic — on Close's
// caller's goroutine, not on resolve's. This is not unique to
// AwaitableCallback: the same panic reproduces with plain ctx.Pause() and no
// AwaitableCallback involved at all, racing loop.Close() against a
// coroutine's own natural startup under chrono.RealClock — Close simply
// assumes nothing else can transition a registered coroutine's pause state
// concurrently, which chrono.RealClock does not guarantee. Until that is
// addressed at the Close/Cancel level, callers must not call loop.Close()
// while a producer might still call resolve() for a waiter registered on
// that loop.
func AwaitableCallback[T any]() (resolve func(v T, err error), wait func(ctx Context) (T, error)) {
	a := &awaitableCallback[T]{}
	return a.resolve, a.wait
}

type awaitableCallback[T any] struct {
	mu     sync.Mutex
	done   bool
	value  T
	err    error
	waiter *contextT
}

func (a *awaitableCallback[T]) resolve(v T, err error) {
	a.mu.Lock()
	if a.done {
		a.mu.Unlock()
		panic("coro: AwaitableCallback resolved more than once")
	}
	a.done = true
	a.value = v
	a.err = err
	waiter := a.waiter
	a.mu.Unlock()

	if waiter == nil {
		// Nobody is waiting yet (or the waiter was already torn down and
		// cleared its registration — see wait's defer). wait() will see
		// a.done == true and return immediately, without ever calling
		// Pause — satisfies the one semantic that matters, above.
		return
	}

	// The waiter is parked. Resume it through the clock, exactly like any
	// other cross-goroutine resume in this library — see the doc comment's
	// "one semantic that matters" section for why there is no in-place
	// shortcut here.
	waiter.Resume()
}

func (a *awaitableCallback[T]) wait(ctx Context) (T, error) {
	c, ok := ctx.(*contextT)
	if !ok {
		panic("coro: AwaitableCallback.wait requires the Context produced by this package's event loop")
	}

	a.mu.Lock()
	if a.done {
		v, err := a.value, a.err
		a.mu.Unlock()
		return v, err
	}
	if a.waiter != nil {
		a.mu.Unlock()
		panic("coro: AwaitableCallback supports exactly one waiter")
	}
	a.waiter = c
	a.mu.Unlock()

	defer func() {
		// Clear the registration unconditionally on the way out — including
		// when Pause panics with the cancellation sentinel below, during
		// teardown — so a resolve() that arrives afterwards (or a fresh
		// wait() call) does not find a stale, dead waiter. See the "One-shot,
		// single waiter" section of the doc comment above.
		a.mu.Lock()
		a.waiter = nil
		a.mu.Unlock()
	}()

	c.Pause()

	a.mu.Lock()
	v, err := a.value, a.err
	a.mu.Unlock()

	return v, err
}
