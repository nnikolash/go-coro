# go-coro - Coroutines for Golang

## What this library can do?

This library implements coroutines to have control over how time is perceived by the program. It can run **simulations** of any periods of time in a **easy-to-read** and **easy-to-maintain** manner. The code then can be switched to use real time, so that same code used both for simulation and real application.

This library uses [go-chrono](https://github.com/nnikolash/go-chrono) for time simulation.

## **What is coroutine?**

**Coroutine** - is a piece of **synchornous** code, which can be interrupted and then continuted from last point. Coroutines were invented to overcome terrible readability of asynchronous callback-based code, which for a long time was a standard way of implementing asyncronous logic. Coroutines is just a **syntax-sugar over callbacks**.

Good examples of transition from callbacks to coroutines:

* C++: Boost.Asio.IoService -> Boost.Asio.Coroutines (stackless & stackfull) or async/await (stackless)
* JavaScript: setTimeout -> Promise() -> async/await (stackless)

The concept of coroutines is so much easier for perception than callbacks, that even on a system level they still make sense.  That's why Temporal team has inveted their sort-a system-wide coroutines.

In **Golang** we have built-in **goroutines**, which are type of stackfull coroutines (or fibers). Their purpose is to increase performance of and add control over executing code.
Unfortunatelly only Go scheduler has control of when goroutine is interrupted and resumed. So we can't directly use goroutines for time simulation.

## Intention

In my case the need for this library appeared when I was implementing **crypto market trading strategy**.

Most of the actions of the strategy were triggered by market data ticks, so it was easy - just go through all the ticks and call hadler. But there was also need in the strategy to do some **periodic** or **delayed** actions, not on every tick. I couldn't use regular "sleep" for that, because it would block processing of ticks. So for that I used non-blockig approach - I was using simple trick of storing the timestamp of start of waiting and immediatelly returning from the tick handler if desired time difference not yet reached.

Although it worked fine, such **code was hard to read and maintain**. But then I started to write some additional logic - indicators. They also had periodic actions inside them. This increased ugliness of the code even more. Sometimes there were even multiple of such "timers" in one place, and accounting for them all became really complicated.

I started to look for ways to control time in Go. My plan what to use all the regular stuff of Go, but in background to shift time returned from `time.Time()` and used by `time.AfterFunc()` .
Unfortunatelly, at that time there no such option in Go, and I did not find alternative solutions. So I decided to create my own library.

## Usage

Central object of almost any coroutine framework is `EventLoop`. By **coroutine** we mean a function, which is scheduled on event loop for processing.

Event loop is constructed from a `chrono.Clock`. The clock defines time which will be used by event loop to schedule and process events.
Library [go-chrono](https://github.com/nnikolash/go-chrono) provies two clocks: `RealClock` for real time execution, and `Simulator` for simulation.

Each coroutine function has **context** argument - `coro.Context`. This context can be used to:

* Get current time: `ctx.Now()`
* Spawn other coroutines: `ctx.Go(...)`
* Interrupt execution of coroutine, e.g. by calling `ctx.Sleep(...)`

Context of each coroutine is unique object and **must not be shared** with other coroutines.

###### Create clock and event loop

```
clock := chrono.NewSimulator(time.Now())
loop := coro.NewEventLoop(clock)
```

###### Schedule tasks from ouside of coroutine

```
loop.AddTask(func(ctx coro.Context) {
   for i := 0; !stop; i++ {
      ctx.Sleep(time.Minute)
      fmt.Println(i) // print i every minute
   }
})
```

###### Schedule tasks from coroutine

```
func generateEvents(ctx coro.Context) {
   for i := 0; i < 10; i++ {
      ctx.Go(func(ctx coro.Context) {
         handleEvent(ctx, evt)
      })

      ctx.Sleep(time.Minute)
   }
}
```

###### Run processing of tasks

```
clock.ProcessAll(context.Background())
```

###### Use real clock to run in real time

```
clock := chrono.NewRealClock()
...
```

###### Access shared data safely by scheduling a task

```
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
defer stop()

shouldStop := false

go func() {
   <-ctx
   loop.AddTask(func(ctx coro.Context) {
      // Will be executed on the same thread as other tasks
      shouldStop = true
   })
}()

loop.AddTask(func(ctx coro.Context) {
   // Safe to read shouldStop
   for i := 0; !shouldStop; i++ {
      fmt.Println(i)
      ctx.Sleep(time.Second)
   }
})

clock.ProcessAll()
```

###### Add sleep into loop

Coroutine is executed until it releases control. It can be done by interrupting it using `ctx.Sleep()` or `ctx.SleepUntil()`.
Creating task by `ctx.Go()` does not release control, so sometimes `ctx.Sleep()` in required in addition to `ctx.Go()` to not stall the program.
After coroutine released control, it is paused until ready to be continued.

## The `Scheduler` interface — decoupling library code from `coro.Context`

If you are building a library or an `Indicator`-style object that wants
`Now() / Sleep / Spawn` semantics **without** taking on `coro.Context` as a
public dependency, use the narrow `coro.Scheduler` interface:

```go
type Scheduler interface {
    Now() time.Time
    Since(t time.Time) time.Duration
    Until(t time.Time) time.Duration
    Sleep(d time.Duration)
    SleepUntil(t time.Time)
    Spawn(f func(s Scheduler))
}
```

`coro.Context` implements `Scheduler` directly — any function expecting a
Scheduler can be called with a Context. No adapter, no wrapping:

```go
func (ind *MyIndicator) Publish(s coro.Scheduler, evt Event) {
    s.Sleep(ind.window)
    ind.listener.Notify(s, evt)
}

// inside a coroutine: pass ctx straight through.
loop.AddTask(func(ctx coro.Context) {
    ind.Publish(ctx, evt)
})
```

Two non-Context implementations ship in this package:

- `coro.NewLoopScheduler(loop)` — outside any coroutine, when you only need
  `Now()` / `Spawn(...)`. `Sleep` panics here (no coroutine to yield from).
- `coro.NewInlineScheduler(now)` — purely synchronous, no event loop, no
  goroutines, no `sync.Cond`. Sleep advances an in-memory clock instantly;
  Spawn runs `f` inline on the caller's goroutine. Use it to **unit-test
  Scheduler-based code without spinning up `chrono.Simulator`** and without
  `if ctx == nil` branches in production code.

```go
// unit test — no event loop, deterministic, race-free
s := coro.NewInlineScheduler(time.Unix(0, 0))
ind.Publish(s, evt)
require.Equal(t, 1, listener.Calls)
```

Why `Spawn` and not `Go`? `Context.Go(f func(Context))` and
`Scheduler.Spawn(f func(Scheduler))` differ in their callback type, so they
must have different names — Go doesn't allow one type to expose both. `Spawn`
delegates to `Go` internally; they share the exact same scheduling semantics.

## Examples

See folder `examples` and test files `*_test.go` for more examples.

## Do / Don't inside coroutines

`coro.Context` schedules work on a virtual `chrono.Clock`. Under
`chrono.Simulator` (used for backtesting and deterministic tests), the clock
only advances when a coroutine **yields back to the event loop**. The standard
Go concurrency primitives don't yield, so they will silently misbehave in
simulation. The rule of thumb: **everything time-related must go through `ctx`
or `chrono.Clock`**.

### ❌ Don't

Inside a `coro.Context`-driven coroutine, do **not** use:

| Anti-pattern | Why it breaks in `Simulator` |
|---|---|
| `time.Sleep(d)` | Blocks on the real OS clock; the simulator can't observe or advance through it. |
| `<-time.After(d)` | Same as above — pulls from the real clock. |
| `go someFunc()` (with no coordination) | The simulator may exhaust all scheduled tasks before the goroutine wakes. Whatever the goroutine planned to do gets dropped. |
| `<-ch` / `ch <- v` | Channels don't yield to the event loop; if no other coroutine drives the other side, the loop sees no pending work and returns. |
| `sync.WaitGroup.Wait()` / `sync.Mutex.Lock()` | Blocks the goroutine without yielding. Same hazard as raw goroutines. |
| `ctx.Wait(condition)` with a real-clock `condition` | `Wait` spawns a raw goroutine — only safe with `RealClock` (see `clock.go`). Do not use under `Simulator`. |

### ✅ Do

| Pattern | Effect |
|---|---|
| `ctx.Sleep(d)` / `ctx.SleepUntil(t)` | Yields to the event loop and resumes once virtual time has advanced by `d` (Simulator: instant; RealClock: real wait). |
| `ctx.Go(func(ctx) { ... })` | Spawns a child coroutine on the same event loop. The child yields like any other coroutine. |
| `ctx.After(d, func(ctx) { ... })` / `ctx.Every(d, ...)` | Schedules a deferred coroutine — the wrapper is what makes it Simulator-safe. |
| `coro.Callback(loop, cb)` / `Callback1` / `Callback2` | Adapt a callback-style API into a Scheduler-friendly closure that posts a task to the loop instead of running inline. Use these on any boundary where external code (e.g. an HTTP client, a websocket reader) wants to call you back from a foreign goroutine. |
| `coro.NewMutex()` | A coroutine-aware mutex — yields properly via `Pause/Resume`. Use this instead of `sync.Mutex` if multiple coroutines on the same loop need exclusion. |

### When you really need to talk to a real goroutine

The only correct bridge is to post back through the event loop. Wrap your
goroutine's completion in `loop.AddTask(...)` (or use `coro.Callback*` to wrap
a callback the foreign code will call), so the resulting work runs on the
event-loop thread under a fresh `Context`.

```go
// HTTP-style example — the response handler must NOT call ctx methods
// directly from the http client's goroutine.
client.Get(url, coro.Callback1(loop, func(ctx coro.Context, resp Response) {
    // Safe: this runs on the event loop thread under a fresh coroutine.
    ctx.Sleep(time.Second)
    process(ctx, resp)
}))
```

## Troubleshooting

### Program hangs on ProcessAll

There could be multiple reasons:

* You have loop without `ctx.Sleep()`.
* Your coroutine blocks on waiting for some syncronisation primitive: mutex, channel etc. But it will neven become available because entire event loop is waiting for this coroutine to yield control.
* You have periodic job, which you did not stop. The job creates a new task everytime previous is processed, so there is always tasks in a loop.

### ProcessAll exists unexpectedly

First time working with simulator might produce confusing issues. That is because usually when we work with real-time programs we are used to make some assumptions, which in simulated time might not be true.

Most of the time these assumptions are related to the time of execution of some code. For example, next code would work perfectly fine in real time, but won't work in simulation:

```
loop.AddTask(fun(ctx coro.Context) {
   for i := 0; i < 100; i++ {
      go loop.AddTask(func(ctx coro.Context) {
         ...
      })

      ctx.Sleep(time.Second)
   }
})

clock.ProcessAll()
```

Here an error is that `go ...` called instead of `ctx.Go()`. In real time sleep of 1 second would be more than enough for goroutine to start and do its job. But in simulation this sleep is an instant moment. So in this case most likely goroutines won't even start execution before 100 sleeps will be processed. After that `ProcessAll()` will see that no tasks left, and will return. After that goroutines will eventually start adding new tasks, but it will be too late - the has already stopped.

In simulated world, time between events passes in instant. So infinity may pass faster than goroutine even starts.
That's why **goroutines** and **channels** most of the time **should not be used** with the coroutines or should we used with caution. Avoid assuming, that some code will execute faster than other code without explicit synchronization.

### How to synchonize without using mutex/channels?

Coroutines all run on the same thread, so most of the time synchronization is not even need. But if it is still needed, it is possible to implement any synchronization primitive by using funtions `Pause()` and `Resume()` of the context.
An example of implementation of such primitive is `coro.Mutex`.

## Design, prior art and alternatives

A coroutine here is implemented as **one real goroutine per coroutine**, coordinated by a
per-coroutine `sync.Cond` baton (`paused`/`finished` flags): exactly one coroutine runs at a time,
yielding (`ctx.Sleep`/`Pause`) blocks its goroutine on the cond, and a clock-scheduled task resumes
it. This is a deliberate, standard design. A 2026 multi-source review
([`docs/2026-05-30-coroutine-design-research.md`](docs/2026-05-30-coroutine-design-research.md))
confirmed it against the Go ecosystem and established deterministic-simulation systems:

* The **goroutine-as-coroutine baton** is the established pre-Go-1.23 way to get stackful
  coroutines/fibers. Go 1.23's runtime coroutines (behind `iter.Pull`) are the *same* idea, just
  faster (~20ns vs ~190ns per switch) — but `coroswitch` is unexported and `iter.Pull` is a
  forward-only iterator, **not** a reusable general coroutine primitive. `sync.Cond` has no spurious
  wakeups, so the baton handshake is correct.
* **Cooperative coroutines yielding to a virtual clock, with nondeterministic primitives forbidden**,
  is the recognized pattern for deterministic simulation and replay: it matches **SimPy** (processes
  are generators yielding to a simulation-time loop) and **Temporal** (workflow coroutines run one at
  a time, no native goroutines/wall-clock, *same code in production and replay*).

### Honest comparison

| Option | Design | Reusable for this | Status (2026) |
|---|---|---|---|
| **go-coro** (this) | goroutine + `sync.Cond` baton over `chrono.Clock` | — | maintained |
| Go 1.23 `iter.Pull` / runtime coro | faster goroutine-baton (`coroswitch`) | ❌ forward-only iterator; `coroswitch` unexported | stdlib |
| [nvlled/carrot](https://github.com/nvlled/carrot) | one goroutine per coroutine, one-at-a-time (same as this) | ⚠️ same model, no benefit | ❌ unmaintained, low adoption |
| [dispatchrun/coroutine](https://github.com/dispatchrun/coroutine) | compiler/codegen durable coroutines | ⚠️ different model | ❌ experimental, stale |
| [Temporal](https://docs.temporal.io/) | hosted workflow engine | ⚠️ heavyweight; not a library | ✅ but a different scale of tool |

Verdict: the current design is idiomatic and correct; there is no better library or primitive to
migrate to.

### ⚠️ Known limitation: suspended coroutines are blocked goroutines

Because a suspended coroutine is a goroutine parked on `sync.Cond.Wait()`, it is only cleaned up when
its resume task actually fires. **If the simulation ends while coroutines are still suspended, those
goroutines leak** (stay blocked forever). This happens when:

* `Simulator.ProcessAllUntil(ctx, until)` returns and some coroutine was sleeping past `until`;
* the context is cancelled mid-run;
* backtest data ends while a strategy coroutine is mid-`Sleep`;
* a `coro.Mutex`/`Pause` never gets a matching `Resume`.

There is currently **no `EventLoop.Close()` / cancel API** to tear these down. This is mostly
harmless for a single run that drains to completion, but **matters for long-lived processes that run
many backtests** (e.g. parameter-optimization sweeps): leaked goroutines and their captured state
accumulate across runs. If you sweep, prefer a fresh process per batch, or drain each run to
completion, until a teardown API exists.
