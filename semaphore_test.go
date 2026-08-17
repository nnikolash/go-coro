package coro_test

import (
	"context"
	"testing"
	"time"

	chrono "github.com/nnikolash/go-chrono"
	"github.com/nnikolash/go-coro"
	"github.com/stretchr/testify/require"
)

// TestSemaphore_LimitOne_BehavesLikeMutex mirrors TestMutex_Basic1, but
// through Semaphore(1) directly — guards the "Mutex is exactly Semaphore
// with N=1" equivalence the mutex.go rewrite depends on.
func TestSemaphore_LimitOne_BehavesLikeMutex(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(1, 0))
	loop := coro.NewEventLoop(clock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sem := coro.NewSemaphore(1)

	res := []int{}

	loop.AddTask(func(ctx coro.Context) {
		sem.Acquire(ctx)
		defer sem.Release()

		ctx.Sleep(100 * time.Millisecond)
		res = append(res, 1)
		ctx.Sleep(100 * time.Millisecond)
	})

	loop.AddTask(func(ctx coro.Context) {
		ctx.Sleep(250 * time.Millisecond)

		sem.Acquire(ctx)
		defer sem.Release()

		res = append(res, 2)
	})

	loop.AddTask(func(ctx coro.Context) {
		ctx.Sleep(275 * time.Millisecond)

		sem.Acquire(ctx)
		defer sem.Release()

		res = append(res, 3)
	})

	clock.ProcessAll(ctx)

	require.Equal(t, []int{1, 2, 3}, res)
}

// TestSemaphore_LimitTwo_AllowsConcurrentHolders verifies that a
// Semaphore(2) lets two coroutines hold a permit at the same virtual time —
// the exact behavior a Mutex (limit-1) cannot provide, and the reason
// obsub.Manager needs a counting semaphore rather than a mutex per exchange.
func TestSemaphore_LimitTwo_AllowsConcurrentHolders(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	sem := coro.NewSemaphore(2)
	var order []string

	// Two holders acquire immediately and hold until released; a third must
	// park until one of the first two releases.
	loop.AddTask(func(ctx coro.Context) {
		sem.Acquire(ctx)
		order = append(order, "A-in")
		ctx.Sleep(100 * time.Millisecond)
		order = append(order, "A-out")
		sem.Release()
	})
	loop.AddTask(func(ctx coro.Context) {
		sem.Acquire(ctx)
		order = append(order, "B-in")
		ctx.Sleep(100 * time.Millisecond)
		order = append(order, "B-out")
		sem.Release()
	})
	loop.AddTask(func(ctx coro.Context) {
		sem.Acquire(ctx) // must park: both permits already held by A and B
		order = append(order, "C-in")
		sem.Release()
	})

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)

	// A and B both acquire before either releases (limit=2, both fast-path).
	require.Equal(t, []string{"A-in", "B-in", "A-out", "B-out", "C-in"}, order)
}

// TestSemaphore_FIFOOrdering mirrors TestMutex_FIFOOrdering: waiters queued
// behind an exhausted Semaphore must be served in arrival order.
func TestSemaphore_FIFOOrdering(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	sem := coro.NewSemaphore(1)
	var order []int

	loop.AddTask(func(ctx coro.Context) {
		sem.Acquire(ctx)
		order = append(order, 0)
		ctx.Sleep(100 * time.Millisecond)
		sem.Release()
	})

	for i := 1; i <= 3; i++ {
		i := i
		loop.AddDelayedTask(time.Duration(i)*time.Millisecond, func(ctx coro.Context) {
			sem.Acquire(ctx)
			defer sem.Release()
			order = append(order, i)
		})
	}

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, []int{0, 1, 2, 3}, order)
}

// TestSemaphore_AcquireWithoutContention_DoesNotYield mirrors
// TestMutex_UnlockWithoutWaiters: the fast path (permit available) must not
// pause the calling coroutine.
func TestSemaphore_AcquireWithoutContention_DoesNotYield(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	sem := coro.NewSemaphore(2)
	var sequence []string

	loop.AddTask(func(ctx coro.Context) {
		sem.Acquire(ctx)
		sequence = append(sequence, "acquire-1")
		sem.Release()
		sequence = append(sequence, "release-1")

		// Re-acquire with no contention must succeed without yielding.
		sem.Acquire(ctx)
		sequence = append(sequence, "acquire-2")
		sem.Release()
		sequence = append(sequence, "release-2")
	})

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"acquire-1", "release-1", "acquire-2", "release-2"}, sequence)
}

// TestSemaphore_NonPositiveLimit_NormalizesToOne guards NewSemaphore's
// limit<=0 normalization (mirrors newCoroSem's prior "never fewer than a
// mutex" behavior).
func TestSemaphore_NonPositiveLimit_NormalizesToOne(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	sem := coro.NewSemaphore(0)
	var order []string

	loop.AddTask(func(ctx coro.Context) {
		sem.Acquire(ctx)
		order = append(order, "A-in")
		ctx.Sleep(10 * time.Millisecond)
		order = append(order, "A-out")
		sem.Release()
	})
	loop.AddTask(func(ctx coro.Context) {
		sem.Acquire(ctx) // must park behind A: limit normalized to 1
		order = append(order, "B-in")
		sem.Release()
	})

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"A-in", "A-out", "B-in"}, order)
}
