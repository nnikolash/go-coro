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
