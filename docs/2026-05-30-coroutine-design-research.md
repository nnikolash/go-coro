# Coroutines in Go: implementation techniques, prior art, and where go-coro stands

**Date:** 2026-05-30
**Method:** multi-source web research (21 sources fetched, 97 candidate claims, 25 adversarially
verified by 3-vote panels — 25 confirmed, 0 refuted), plus a direct reading of go-coro's own source
to close the one caveat the web research could not (leak-on-exit / cancel API).
**Question:** Is go-coro's "one real goroutine per coroutine + `sync.Cond` handshake" stackful
coroutine design the right approach, or is there a more idiomatic/modern way (Go 1.23 `iter.Pull` /
runtime coroutines, or an established library)?

---

## TL;DR / verdict

**Stay as-is.** go-coro's goroutine + `sync.Cond` baton over an injected `chrono.Clock` is the
established pre-1.23 Go stackful-coroutine technique, and the overall architecture — cooperative
coroutines yielding to a virtual clock, with nondeterministic primitives forbidden — is exactly how
proven deterministic-replay/simulation systems (Temporal, SimPy) work. There is **no clean migration
target**: Go 1.23 runtime coroutines are themselves a faster goroutine-baton but their switch
primitive (`coroswitch`) is unexported; `iter.Pull` is a forward-only iterator API, not a general
coroutine primitive; and the candidate libraries (`nvlled/carrot`, `dispatchrun/coroutine`) are
stale/experimental.

**The one real, code-verified risk is goroutine leaks**: a suspended coroutine is a goroutine blocked
on `sync.Cond.Wait()`, and go-coro has no teardown/cancel API. Any simulation that ends with
coroutines still suspended (e.g. `ProcessAllUntil` cutoff, context cancel, end-of-data mid-`Sleep`)
leaks those goroutines. This matters most for optimization sweeps that run many backtests in one
process.

---

## How go-coro maps onto established practice (all web-verified)

### Implementation technique: goroutine-as-coroutine baton

- The "channel/goroutine baton handoff" is the established pre-1.23 way to build stackful
  coroutines/fibers in Go. go-coro uses `sync.Cond` instead of a channel; functionally equivalent.
- **Go 1.23 runtime coroutines** (`runtime/coro.go`, Russ Cox's "Coroutines for Go") are themselves
  "a goroutine blocked on a special channel" — the *same* baton idea, just faster: a runtime
  coroutine switch is ~20ns vs ~190ns for a channel send/receive (~10×). But `coroswitch` is
  **unexported**; only `iter.Pull`/`iter.Pull2` expose it, and only as a forward-only `next`/`stop`
  iterator. It is **deliberately not** a general coroutine primitive.
  (https://research.swtch.com/coro, https://go.dev/src/runtime/coro.go, https://pkg.go.dev/iter)
- `iter.Pull`'s `stop()` is **mandatory** — not calling it leaks the underlying coroutine goroutine;
  `defer stop()` is the idiomatic guard. Russ Cox's reference `coro.New` likewise returns a cancel.
  This is the same teardown obligation go-coro has (see risk below).
- `sync.Cond` in Go has **no spurious wakeups**, so go-coro's `paused`/`finished` baton handshake is
  correct (no lost/spurious-wakeup hazard). (https://victoriametrics.com/blog/go-sync-cond/)

### Prior art: coroutines over a virtual clock = standard for deterministic sim/replay

- **SimPy** (process-based discrete-event simulation): processes are plain Python generators
  (coroutines); when a process yields an event, SimPy suspends it and resumes it later off a
  simulation-time event loop. This is the *same* architecture as go-coro (coroutine yields to a
  virtual clock). (https://simpy.readthedocs.io/en/latest/topical_guides/simpy_basics.html)
- **Temporal** (Go SDK): a deterministic runner executes workflow coroutines **one at a time**;
  native Go goroutines/`time`/channels are **never allowed** inside workflow code; the *same*
  deterministic workflow code runs identically in production and in replay/test. This is precisely
  go-coro's "same code in prod and sim, forbid nondeterministic primitives" contract. Temporal's
  Python SDK even *replaces* the asyncio event loop for the same reason.
  (https://docs.temporal.io/develop/go/go-sdk-multithreading, https://docs.temporal.io/workflow-definition)
- **Polar Signals** built deterministic simulation testing in Go on the same idea
  (https://www.polarsignals.com/blog/posts/2024/05/28/mostly-dst-in-go); the general DST pattern is
  well documented (https://notes.eatonphil.com/2024-08-20-deterministic-simulation-testing.html).

Conclusion: "cooperative coroutines over an injectable virtual clock, forbidding nondeterministic
primitives" is a recognized, sound pattern — not a homemade oddity.

### Library landscape (no viable migration target)

| Option | Design | Status | Verdict |
|---|---|---|---|
| **go-coro** (this) | goroutine + `sync.Cond` baton over `chrono.Clock` | maintained | keep |
| Go 1.23 `iter.Pull` / runtime coro | faster goroutine-baton (`coroswitch`) | stdlib | not reusable — forward-only iterator, `coroswitch` unexported |
| [nvlled/carrot](https://github.com/nvlled/carrot) | one goroutine per coroutine, one-at-a-time (same as go-coro) | **unmaintained, low adoption** | don't adopt |
| [dispatchrun/coroutine](https://github.com/dispatchrun/coroutine) | compiler/codegen-based durable coroutines | **experimental, effectively unmaintained** | don't adopt |

---

## The real risk: goroutine leaks (verified in go-coro source)

The web research flagged this as a caveat ("confirm leak-on-exit and a cancel/stop API"); reading the
source confirms it. Mechanics (`coro.go`, `context.go`, `yield.go`):

- `RunCoroutine` spawns `go func() { defer ctrl.Done(); f(ctx) }()`. When the coroutine calls
  `ctx.Sleep`/`Pause`, it calls `ctrl.Yield()` → sets `paused=true`, `Broadcast()`, then **`Wait()`
  — the goroutine blocks on the `sync.Cond`.**
- It only resumes when a **clock-scheduled task** fires (`AfterFunc(d, …RunUntilYielded)` /
  `Resume`'s `AfterFunc(0, …)`).

Therefore **if the simulation ends while a coroutine is suspended, its goroutine stays blocked on
`Wait()` forever.** Triggers:

1. `ProcessAllUntil(ctx, until)` — coroutines sleeping past `until` never resume → leak.
2. Context cancellation (`ProcessAll` returns on `ctx.Err()`).
3. End-of-backtest-data while a strategy coroutine is mid-`Sleep`.
4. `coro.Mutex`/`Pause` with no matching `Resume`.
5. `AddPlannedTaskCtx` spawns `go func() { <-ctx.Done(); … }` — also leaks on early exit if the
   planned time is never reached and the parent context is never cancelled.

There is **no `Close()` on `EventLoop` and no cancel on `RunCoroutine`/`YieldController`.**

**Impact:** an optimization sweep running many backtests per process leaks N goroutines (+ their
stacks + captured state) for every run that ends with N suspended coroutines → goroutine-count and
memory growth over the sweep.

**Fix shape** (same technique as `iter.Pull`/Cox's `coro`): add a "resume-in-cancel-mode" where, after
`Wait()` returns in cancel mode, `Yield()` panics with a sentinel that unwinds the coroutine's stack
(running its `defer`s) and exits the goroutine; the event loop invokes this for all live coroutines on
shutdown. This is nontrivial (must unwind a stackful coroutine cleanly and not let the sentinel escape
the boundary), so it warrants its own change.

---

## Honest caveats

- The prior-art mappings ("same architecture as go-coro") are analyst inferences on top of
  primary-sourced facts about SimPy/Temporal/runtime-coro; those facts are verified and unanimous, but
  no source audited go-coro's code — *I* did, for the leak finding specifically.
- Three claims passed only 2–1 (SimPy suspend-on-yield detail; "reuse Go's native scheduler";
  dispatchrun codegen detail) — directionally supported, lower confidence on specifics.
- Performance numbers (~20ns coroswitch vs ~190ns channel) are from Russ Cox's writeup; go-coro's
  `sync.Cond` path is in the same order as the channel path. For a backtester this switch cost is
  negligible against strategy-callback work — not a migration motivation.

## Recommended actions

1. **Keep the goroutine + `sync.Cond` design** — idiomatic, correct, matches Temporal/SimPy.
2. **Add a teardown/cancel path** to stop leaking suspended-coroutine goroutines on simulation end —
   the single concrete improvement. **DONE (2026-05-30):** `EventLoop.Close()` now cancels every
   still-suspended coroutine via the panic-unwind technique described above (`YieldController.Cancel`
   resumes the parked goroutine in cancel mode → `Yield` panics a sentinel → stack unwinds running
   defers → recovered at the `RunCoroutine` boundary). Tie `defer loop.Close()` into the backtester's
   run lifecycle.
3. **Do not migrate** to `iter.Pull`/runtime coro (not reusable) or to carrot/dispatchrun (stale).

## Primary sources

- Go coroutines: Russ Cox "Coroutines for Go" (https://research.swtch.com/coro), `runtime/coro.go`
  (https://go.dev/src/runtime/coro.go), `iter` package (https://pkg.go.dev/iter), coroutines-in-Go
  walkthrough (https://unskilled.blog/posts/coroutines-in-go/)
- Deterministic sim/replay: SimPy basics
  (https://simpy.readthedocs.io/en/latest/topical_guides/simpy_basics.html), Temporal Go SDK
  multithreading (https://docs.temporal.io/develop/go/go-sdk-multithreading), Temporal workflow
  definition (https://docs.temporal.io/workflow-definition), Polar Signals DST
  (https://www.polarsignals.com/blog/posts/2024/05/28/mostly-dst-in-go), DST overview
  (https://notes.eatonphil.com/2024-08-20-deterministic-simulation-testing.html)
- `sync.Cond` semantics (https://victoriametrics.com/blog/go-sync-cond/)
- Libraries: nvlled/carrot (https://github.com/nvlled/carrot), dispatchrun/coroutine
  (https://github.com/dispatchrun/coroutine)
