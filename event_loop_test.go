package coro_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	chrono "github.com/nnikolash/go-chrono"
	coro "github.com/nnikolash/go-coro"
	"github.com/stretchr/testify/require"
)

func TestEventLoop_RealClock(t *testing.T) {
	t.Parallel()

	clock := chrono.NewRealClock()
	loop := coro.NewEventLoop(clock)

	res := []int{}

	d := concurrencyDetector{}

	loop.AddTask(func(ctx coro.Context) {
		d.Check()

		ctx.Sleep(100 * time.Millisecond)
		res = append(res, 1)
		ctx.Sleep(100 * time.Millisecond)
	})

	loop.AddTask(func(ctx coro.Context) {
		d.Check()

		ctx.Sleep(250 * time.Millisecond)
		res = append(res, 2)
	})

	loop.AddTask(func(ctx coro.Context) {
		d.Check()

		ctx.Sleep(275 * time.Millisecond)
		res = append(res, 3)
	})

	loop.AddTask(func(ctx coro.Context) {
		d.Check()

		ctx.Sleep(300 * time.Millisecond)
		res = append(res, 4)
	})

	time.Sleep(350 * time.Millisecond)

	require.Equal(t, []int{1, 2, 3, 4}, res)
}

func TestEventLoop_Stable(t *testing.T) {
	t.Parallel()

	clock := chrono.NewSimulator(time.Now())
	loop := coro.NewEventLoop(clock)

	res := []int{}

	const loops = 1000

	loop.AddTask(func(ctx coro.Context) {
		for i := 0; i < loops; i++ {
			res = append(res, i)
			ctx.Sleep(1 * time.Millisecond)
		}
	})

	loop.AddTask(func(ctx coro.Context) {
		for i := 0; i < loops; i++ {
			res = append(res, i)
			ctx.Sleep(1 * time.Millisecond)
		}
	})

	loop.AddTask(func(ctx coro.Context) {
		for i := 0; i < loops; i++ {
			res = append(res, i)
			ctx.Sleep(1 * time.Millisecond)
		}
	})

	_, err := clock.ProcessAll(context.Background())
	require.NoError(t, err)

	require.Len(t, res, loops*3)

	for i := 0; i < loops; i++ {
		require.Equal(t, i, res[i*3], res)
		require.Equal(t, i, res[i*3+1], res)
		require.Equal(t, i, res[i*3+2], res)
	}
}

func TestEventLoop_OrderOfExpires(t *testing.T) {
	t.Parallel()

	origin, err := time.Parse(time.RFC3339, "1990-01-01T00:00:00Z")
	require.NoError(t, err)

	clock := chrono.NewSimulator(time.Now())
	loop := coro.NewEventLoop(clock)

	res := []int{}

	loop.AddPlannedTask(origin.Add(3*time.Minute), func(ctx coro.Context) {
		res = append(res, 3)
	})

	loop.AddPlannedTask(origin.Add(1*time.Minute), func(ctx coro.Context) {
		res = append(res, 1)
	})

	loop.AddPlannedTask(origin.Add(2*time.Minute), func(ctx coro.Context) {
		res = append(res, 2)
	})

	_, err = clock.ProcessAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, []int{1, 2, 3}, res)
}

type concurrencyDetector struct {
	depth            atomic.Int64
	improveDetection bool
}

func (d *concurrencyDetector) Check() {
	d.Enter()
	d.Exit()
}

func (d *concurrencyDetector) Enter() {
	newVal := d.depth.Add(1)

	if newVal != 1 {
		panic("Concurrent execution detected")
	}

	time.Sleep(5 * time.Millisecond)
}

func (d *concurrencyDetector) Exit() {
	newVal := d.depth.Add(-1)

	if newVal != 0 {
		panic("Concurrent execution detected")
	}
}
