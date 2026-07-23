package coro

import "fmt"

// escapeRequest is the sentinel panic value used by Escape. It is distinct from
// errCoroutineCanceled so that RunCoroutine's recover can route it to the
// registered escape handler instead of swallowing it silently.
type escapeRequest struct {
	value any
}

// Escape immediately unwinds the current coroutine's call stack (all its defers
// run), then calls the escape handler registered on the event loop via
// SetEscapeHandler. This function never returns; it panics with an internal
// sentinel that only the coro recover wrapper intercepts.
//
// Escape without a registered handler re-panics with a descriptive message
// (fail-loud). Do NOT blanket-recover from coroutine code — the sentinel would
// be swallowed and the handler would never be called.
//
// Typical use: a strategy calls Escape(RestartRequest{...}) from inside a
// coroutine to hand control to cmd/bot for a runner restart without blocking.
func Escape(value any) {
	panic(escapeRequest{value: value})
}

// escapeHandlerProvider is an internal interface that lets RunCoroutine retrieve
// the escape handler from the event loop without adding it to the public
// EventLoop interface. It is implemented by *eventLoopT.
type escapeHandlerProvider interface {
	getEscapeHandler() func(any)
}

// noEscapeHandlerPanic is the panic message when Escape is called but no
// handler has been registered.
func noEscapeHandlerPanic(value any) string {
	return fmt.Sprintf(
		"coro.Escape: no escape handler registered on event loop; "+
			"call evtLoop.SetEscapeHandler before running coroutines that may escape "+
			"(escaped value type=%T)", value)
}
