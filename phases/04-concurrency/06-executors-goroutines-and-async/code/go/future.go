package main

import (
	"errors"
	"sync"
)

// ErrCancelled is the error a cancelled Future reports.
var ErrCancelled = errors.New("cancelled")

// Future is the small slice of CompletableFuture this lesson needs: a value or error set exactly once, a channel to
// wait on, and callbacks that run synchronously in the completing goroutine (as CompletableFuture runs its dependents).
// Running callbacks synchronously is what keeps the simulated clock deterministic: Advance returns only after every
// consequence of the alarms it fired has happened.
type Future[T any] struct {
	mu        sync.Mutex
	done      chan struct{}
	value     T
	err       error
	completed bool
	cancelled bool
	callbacks []func(T, error)
}

// NewFuture is an incomplete promise.
func NewFuture[T any]() *Future[T] { return &Future[T]{done: make(chan struct{})} }

// Completed is a future that already holds value.
func Completed[T any](value T) *Future[T] {
	f := NewFuture[T]()
	f.Complete(value)
	return f
}

func (f *Future[T]) settle(value T, err error, cancelled bool) bool {
	f.mu.Lock()
	if f.completed {
		f.mu.Unlock()
		return false
	}
	f.completed, f.cancelled = true, cancelled
	f.value, f.err = value, err
	callbacks := f.callbacks
	f.callbacks = nil
	close(f.done)
	f.mu.Unlock()
	for _, cb := range callbacks { // outside the lock: a callback may complete other futures
		cb(value, err)
	}
	return true
}

// Complete sets the value; false if the future was already settled (first completion wins).
func (f *Future[T]) Complete(value T) bool { return f.settle(value, nil, false) }

// Fail sets the error; false if already settled.
func (f *Future[T]) Fail(err error) bool {
	var zero T
	return f.settle(zero, err, false)
}

// Cancel settles the future with ErrCancelled; false if it had already completed.
func (f *Future[T]) Cancel() bool {
	var zero T
	return f.settle(zero, ErrCancelled, true)
}

// Done is closed when the future settles; select on it.
func (f *Future[T]) Done() <-chan struct{} { return f.done }

// IsDone reports whether the future has settled (completed, failed, or cancelled).
func (f *Future[T]) IsDone() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.completed
}

// IsCancelled reports whether the future was settled by Cancel.
func (f *Future[T]) IsCancelled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancelled
}

// Get blocks until the future settles. It is join(): the blocking style the executor models compare.
func (f *Future[T]) Get() (T, error) {
	<-f.done
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.value, f.err
}

// MustGet is Get for futures the caller knows are complete and successful.
func (f *Future[T]) MustGet() T {
	v, err := f.Get()
	if err != nil {
		panic(err)
	}
	return v
}

// OnComplete registers a callback; it runs at once if the future has already settled (whenComplete).
func (f *Future[T]) OnComplete(cb func(T, error)) {
	f.mu.Lock()
	if !f.completed {
		f.callbacks = append(f.callbacks, cb)
		f.mu.Unlock()
		return
	}
	value, err := f.value, f.err
	f.mu.Unlock()
	cb(value, err)
}

// Then maps a successful result into a new future (thenApply); an error passes through.
func Then[T, U any](f *Future[T], fn func(T) U) *Future[U] {
	out := NewFuture[U]()
	f.OnComplete(func(v T, err error) {
		if err != nil {
			out.Fail(err)
			return
		}
		out.Complete(fn(v))
	})
	return out
}

// AllOf completes with every value, in order, once every input has settled (allOf then join).
func AllOf[T any](futures []*Future[T]) *Future[[]T] {
	out := NewFuture[[]T]()
	if len(futures) == 0 {
		out.Complete([]T{})
		return out
	}
	var mu sync.Mutex
	remaining := len(futures)
	for _, f := range futures {
		f.OnComplete(func(T, error) {
			mu.Lock()
			remaining--
			last := remaining == 0
			mu.Unlock()
			if last {
				values := make([]T, len(futures))
				var firstErr error
				for i, g := range futures {
					v, err := g.Get() // cannot block: every input has settled
					values[i] = v
					if err != nil && firstErr == nil {
						firstErr = err
					}
				}
				if firstErr != nil {
					out.Fail(firstErr)
				} else {
					out.Complete(values)
				}
			}
		})
	}
	return out
}

// CountDownLatch is java.util.concurrent.CountDownLatch: Await returns once CountDown has been called count times.
type CountDownLatch struct {
	mu    sync.Mutex
	count int
	zero  chan struct{}
}

// NewCountDownLatch starts at count; a zero count is already open.
func NewCountDownLatch(count int) *CountDownLatch {
	l := &CountDownLatch{count: count, zero: make(chan struct{})}
	if count <= 0 {
		close(l.zero)
	}
	return l
}

// CountDown decrements; the last decrement releases every waiter.
func (l *CountDownLatch) CountDown() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.count <= 0 {
		return
	}
	l.count--
	if l.count == 0 {
		close(l.zero)
	}
}

// Await blocks until the count reaches zero.
func (l *CountDownLatch) Await() { <-l.zero }
