package coro_test

import (
	"context"
	"testing"
	"time"

	chrono "github.com/nnikolash/go-chrono"
	"github.com/nnikolash/go-coro"
	"github.com/stretchr/testify/require"
)

func TestMutex_Basic1(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(1, 0))
	loop := coro.NewEventLoop(clock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := coro.NewMutex()

	res := []int{}

	loop.AddTask(func(ctx coro.Context) {
		m.Lock(ctx)
		defer m.Unlock()

		ctx.Sleep(100 * time.Millisecond)
		res = append(res, 1)
		ctx.Sleep(100 * time.Millisecond)
	})

	loop.AddTask(func(ctx coro.Context) {
		ctx.Sleep(250 * time.Millisecond)

		m.Lock(ctx)
		defer m.Unlock()

		res = append(res, 2)
	})

	loop.AddTask(func(ctx coro.Context) {
		ctx.Sleep(275 * time.Millisecond)

		m.Lock(ctx)
		defer m.Unlock()

		res = append(res, 3)
	})

	loop.AddTask(func(ctx coro.Context) {
		ctx.Sleep(300 * time.Millisecond)

		m.Lock(ctx)
		defer m.Unlock()

		res = append(res, 4)
	})

	loop.AddTask(func(ctx coro.Context) {
		ctx.Sleep(450 * time.Millisecond)

		m.Lock(ctx)
		defer m.Unlock()

		res = append(res, 5)
	})

	clock.ProcessAll(ctx)

	require.Equal(t, []int{1, 2, 3, 4, 5}, res)
}

func TestMutex_Basic2(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(1, 0))
	loop := coro.NewEventLoop(clock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := coro.NewMutex()

	res := []int{}

	loop.AddTask(func(ctx coro.Context) {
		m.Lock(ctx)
		defer m.Unlock()

		ctx.Sleep(100 * time.Millisecond)
		res = append(res, 1)
		ctx.Sleep(100 * time.Millisecond)
	})

	loop.AddTask(func(ctx coro.Context) {
		ctx.Sleep(10 * time.Millisecond)

		m.Lock(ctx)
		defer m.Unlock()

		res = append(res, 2)
	})

	clock.ProcessAll(ctx)

	require.Equal(t, []int{1, 2}, res)
}

func TestMutex_UnlockWithoutWaiters(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	m := coro.NewMutex()
	var sequence []string

	loop.AddTask(func(ctx coro.Context) {
		m.Lock(ctx)
		sequence = append(sequence, "lock-1")
		m.Unlock()
		sequence = append(sequence, "unlock-1")

		// Re-lock with no contention must succeed without yielding.
		m.Lock(ctx)
		sequence = append(sequence, "lock-2")
		m.Unlock()
		sequence = append(sequence, "unlock-2")
	})

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"lock-1", "unlock-1", "lock-2", "unlock-2"}, sequence)
}

func TestMutex_FIFOOrdering(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Unix(0, 0))
	loop := coro.NewEventLoop(clock)

	m := coro.NewMutex()
	var order []int

	// Holder coroutine: acquires the mutex first, sleeps, then releases.
	loop.AddTask(func(ctx coro.Context) {
		m.Lock(ctx)
		order = append(order, 0)
		ctx.Sleep(100 * time.Millisecond)
		m.Unlock()
	})

	// Three waiters queued in order 1, 2, 3 — must be served in the same order.
	for i := 1; i <= 3; i++ {
		i := i
		loop.AddDelayedTask(time.Duration(i)*time.Millisecond, func(ctx coro.Context) {
			m.Lock(ctx)
			defer m.Unlock()
			order = append(order, i)
		})
	}

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, []int{0, 1, 2, 3}, order)
}
