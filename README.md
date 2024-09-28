# go-coro - Coroutines for Golang

## What this library can do?

This library implements coroutines to have control over how time is perceived by the program. It can run **simulations** of any periods of time in a **easy-to-read** and **easy-to-maintain** manner. The code then can be switched to use real time, so that same code used both for simulation and real application.

This library uses [go-chrono](https://github.com/nnikolash/go-chrono) for time simulation.

## **What is coroutine?**

**Coroutine** - is a piece of **synchornous** code, which can be interrupted and then continuted from last point. Coroutines were invented to overcome terrible readability of asynchronous callback-based code, which for a long time was a standard way of implementing asyncronous logic. Coroutines is just a **syntax-sugar over callbacks**.

Good examples of transition from callacks to coroutines:

* C++: Boost.Asio.IoService -> Boost.Asio.Coroutines (stackless & stackfull) or async/await (stackless)
* JavaScript: setTimeout -> Promise() -> async/await (stackless)

The concent of coroutines is so much easier for perception than callbacks, that even on a system level they still make sense.  That's why Temporal team has inveted their sort-a system-wide coroutines.

In **Golang** we have built-in **goroutines**, which are type of stackfull coroutines (or fibers). Their purpose is to increase performance of and add control over executing code.
Unfortunatelly only Go scheduler has control of when goroutine is interrupted and resumed. So we can't directly use goroutines for time simulation.

## Intention

In my case the need for this library appeared when I was implementing **crypto market trading strategy**.

Most of the actions of the strategy were triggered by market data ticks, so it was easy - just go through all the ticks and call hadler. But there was also need in the strategy to do some **periodic** or **delayed** actions, not on every tick. I couldn't use regular "sleep" for that, because it would block processing of ticks. So for that I used non-blockig approach - I was using simple trick of storing the timestamp of start of waiting and immediatelly returning from the tick handler if desired time difference not yet reached.

Although it worked fine, such **code was hard to read and maintain**. But then I started to write some additional logic - indicators. They also had periodic actions inside them. This increased ugliness of the code even more. Sometimes there were even multiple of such "timers" in one place, and accounting for them all became really complicated.

I started to look for ways to control time in Go. My plan what to use all the regular stuff of Go, but in background to shift time returned from `time.Tim`e() and used by `time.AfterFunc()` .
Unfortunatelly, there no such option on Go, and I did not find alternative solutions. So I decided to create my own library.

## Usage

Central object of almost any coroutine framework is `EventLoop`. By **coroutine** we call a function, which is scheduled on event loop for processing.

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

## Examples

See folder `examples` and test files `*_test.go` for more examples.

## Troubleshooting

### Program hangs on ProcessAll

There could be multiple reasons:

* You have loop without `ctx.Sleep()`.
* You coroutine blocks on waiting for some syncronisation primitive: mutex, channel etc. But it will neven become available because entire event loop is waiting for this coroutine.
* You have periodic job, which you did not stop. The job creates new task everytime previous is processed, so there is always tasks in a loop.

### ProcessAll exists unexpectedly

First time working with simulator might produce confusing issues. That is because usually when we work with real-time programs we are used to make some assumptions, which in simulated time might not be true.

Most of the time these assumptions are related to the time of execution of some code. For example, next code would work perfectly fine in real time, but won't work in simulation:

```
loop.AddTask(fun(ctx coro.Context) {
   for i := 0; i < 100; i++ {
      go loop.AddTask(func(ctx coro.Context) {
         processEvent(ctx, i)
      })

      ctx.Sleep(time.Second)
   }
})

clock.ProcessAll()
```

Here an error is that `loop.AddTask()` called instead of `ctx.Go()`. In real time sleep of 1 second would be more than enough for event processor to be scheduled onto loop. But in simulation this sleep is in instant moment. So most likely goroutines won't even start execution before 100 sleep will be processed. After that `ProcessAll()` will see that no stasks left, and will return. After task goroutines will start adding event processor, but it will be too late.

In simulated world, time between events passes in instant. So infinity may pass faster than goroutine even starts.
That's why **goroutines** and **channels** most of the time **should not be used** with the coroutines or should we used with caution.

### How to synchonize without using mutex/channels?

Coroutines all run on the same thread, so most of the time synchronization is not even need. But if it is still needed, it is possible to implement any synchronization primitive by using funtions `Pause()` and `Resume()` of context.
An example of implementation of such primitive is `coro.Mutext`.
