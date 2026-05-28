package examples_test

import (
	"context"
	"testing"
	"time"

	chrono "github.com/nnikolash/go-chrono"
	"github.com/nnikolash/go-coro"
	"github.com/stretchr/testify/require"
)

func TestExample1(t *testing.T) {
	t.Parallel()

	// Create clock
	clock := chrono.NewSimulator(time.Now())

	// Create event loop
	loop := coro.NewEventLoop(clock)

	res := []int{}
	stop := false

	// Define event processor
	handleEvent := func(ctx coro.Context, evt int) {
		// Process event

		// Storing evt before long sleep
		res = append(res, evt)

		// While we sleeping, other events also generated and processed
		ctx.Sleep(2 * time.Minute)

		// After we awake we store same evt again
		res = append(res, evt)

		if len(res) >= 9 {
			stop = true
		}
	}

	// Generate events
	loop.AddTask(func(ctx coro.Context) {
		for i := 0; !stop; i++ {
			// Events age generated every minute
			ctx.Sleep(time.Minute)

			ctx.Go(func(ctx coro.Context) {
				handleEvent(ctx, i)
			})
		}
	})

	require.Equal(t, []int{}, res)

	clock.ProcessAll(context.Background())

	// go-chrono v1.1.0 made equal-deadline tasks resolve FIFO by insertion
	// order (previously heap-layout-dependent). That shifts the interleaving of
	// same-tick wakeups: the stop flag (len(res) >= 9) now trips one spawn
	// earlier, so event 6 is never generated. Every generated event is still
	// processed twice (before and after its sleep) — the result is just a
	// deterministic, shorter schedule.
	require.Equal(t, []int{0, 1, 0, 2, 1, 3, 2, 4, 3, 5, 4, 5}, res)
}
