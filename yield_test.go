package coro_test

import (
	"testing"
	"time"

	"github.com/nnikolash/go-coro"
	"github.com/stretchr/testify/require"
)

func TestYieldingThread_Basic(t *testing.T) {
	t.Parallel()

	res := []int{}

	thread := coro.GoYielding(func(yield coro.Yield) {
		time.Sleep(30 * time.Millisecond)
		res = append(res, 1)

		yield()

		res = append(res, 3)

		yield()

		res = append(res, 6)
	}, nil)

	thread.WaitUntilYielded()
	res = append(res, 2)

	thread.RunUntilYielded()
	time.Sleep(30 * time.Millisecond)
	res = append(res, 4)
	time.Sleep(30 * time.Millisecond)
	res = append(res, 5)
	time.Sleep(30 * time.Millisecond)

	thread.RunUntilYielded()
	res = append(res, 7)

	require.Equal(t, []int{1, 2, 3, 4, 5, 6, 7}, res)
}

func TestYieldController_ContinueAfterFinished(t *testing.T) {
	t.Parallel()

	// Continue must safely report false once the coroutine has finished.
	thread := coro.GoYielding(func(yield coro.Yield) {
		// Runs to completion without yielding.
	}, nil)

	thread.WaitUntilYielded()

	require.False(t, thread.Continue(), "Continue should return false after Done")
}

func TestYieldController_ContinueResumesPaused(t *testing.T) {
	t.Parallel()

	var stage int

	thread := coro.GoYielding(func(yield coro.Yield) {
		stage = 1
		yield()
		stage = 2
	}, nil)

	thread.WaitUntilYielded()
	require.Equal(t, 1, stage)

	require.True(t, thread.Continue(), "Continue should return true while paused")

	thread.WaitUntilYielded()
	require.Equal(t, 2, stage)
}
