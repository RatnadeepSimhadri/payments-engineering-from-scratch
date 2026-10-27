// Lesson: phases/04-concurrency/11-structured-concurrency-and-cancellation/docs/en.md
// Structured Concurrency and Cancellation
// Run:  go run .
// Test: go test ./...
// Sources: JEP 453 (Structured Concurrency, preview) for the scope/fork/join shape; Go `context` package docs
// for deadline and cancellation propagation. Go 1.22+, standard library only. Same design as the Java version.
package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Currency is an ISO 4217 currency, copied from Phase 10 Lesson 01: minor units + currency, never a float.
type Currency struct {
	Code     string
	Exponent int
}

var USD = Currency{"USD", 2}

var ErrCurrencyMismatch = errors.New("currency mismatch")

// Money is an exact amount in minor units of one currency.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

// Covers reports whether m is at least other. A currency mismatch is reported as an error, not a wrong answer.
func (m Money) Covers(other Money) (bool, error) {
	if m.Currency != other.Currency {
		return false, ErrCurrencyMismatch
	}
	return m.MinorUnits >= other.MinorUnits, nil
}

func (m Money) String() string {
	scale := int64(1)
	for i := 0; i < m.Currency.Exponent; i++ {
		scale *= 10
	}
	abs := m.MinorUnits
	if abs < 0 {
		abs = -abs
	}
	whole, frac := abs/scale, abs%scale
	f := ""
	if m.Currency.Exponent > 0 {
		f = "." + fmt.Sprintf("%0*d", m.Currency.Exponent, frac)
	}
	sign := ""
	if m.MinorUnits < 0 {
		sign = "-"
	}
	return sign + fmt.Sprint(whole) + f + " " + m.Currency.Code
}

// Clock is a monotonic clock in nanoseconds. Injected so deadlines can be tested without sleeping.
type Clock interface {
	Nanos() int64
}

// SystemClock reads the monotonic clock. Deadlines on it wake waiters with a time.Timer.
type SystemClock struct{ start time.Time }

func NewSystemClock() *SystemClock { return &SystemClock{start: time.Now()} }

func (c *SystemClock) Nanos() int64 { return int64(time.Since(c.start)) }

// ManualClock only moves when the test or the demo moves it. Tokens with a deadline register a watcher so that
// Advance wakes anyone blocked on Done(): the clock moving is the only event that can expire a deadline here.
type ManualClock struct {
	now      atomic.Int64
	mu       sync.Mutex
	watchers []func()
}

func NewManualClock() *ManualClock { return &ManualClock{} }

func (c *ManualClock) Nanos() int64 { return c.now.Load() }

func (c *ManualClock) Advance(d time.Duration) {
	c.now.Add(int64(d))
	c.mu.Lock()
	watchers := append([]func(){}, c.watchers...)
	c.mu.Unlock()
	for _, w := range watchers {
		w()
	}
}

func (c *ManualClock) onAdvance(f func()) {
	c.mu.Lock()
	c.watchers = append(c.watchers, f)
	c.mu.Unlock()
}

// advanceWatcher is implemented by clocks that can tell a token when time has moved.
type advanceWatcher interface{ onAdvance(func()) }

const noDeadline = int64(^uint64(0) >> 1) // math.MaxInt64

// Deadline is an absolute instant on the injected clock by which work must be done. DeadlineNone never expires.
// Deadlines compose by taking the earliest, which is how a child inherits its parent's budget.
type Deadline struct{ AtNanos int64 }

var DeadlineNone = Deadline{noDeadline}

func addChecked(a, b int64) (int64, error) {
	sum := a + b
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum >= 0) {
		return 0, errors.New("deadline overflows int64 nanoseconds")
	}
	return sum, nil
}

// DeadlineAfter is `now + d` on the clock. Like Math.addExact, an overflow is a programming error and panics.
func DeadlineAfter(clock Clock, d time.Duration) Deadline {
	at, err := addChecked(clock.Nanos(), int64(d))
	if err != nil {
		panic(err)
	}
	return Deadline{at}
}

func (d Deadline) Earliest(other Deadline) Deadline {
	if d.AtNanos <= other.AtNanos {
		return d
	}
	return other
}

func (d Deadline) IsExpired(clock Clock) bool {
	return d.AtNanos != noDeadline && clock.Nanos() >= d.AtNanos
}

func (d Deadline) Remaining(clock Clock) time.Duration {
	if d.AtNanos == noDeadline {
		return time.Duration(noDeadline)
	}
	if left := d.AtNanos - clock.Nanos(); left > 0 {
		return time.Duration(left)
	}
	return 0
}

func (d Deadline) IsNone() bool { return d.AtNanos == noDeadline }

// ErrCancelled is what cooperative code returns when it notices its token has been cancelled.
var ErrCancelled = errors.New("cancelled")

// CancelledError carries the reason and matches ErrCancelled under errors.Is.
type CancelledError struct{ Reason string }

func (e *CancelledError) Error() string        { return e.Reason }
func (e *CancelledError) Is(target error) bool { return target == ErrCancelled }

const deadlineExceeded = "deadline exceeded"

// CancellationToken is a cooperative cancellation signal arranged in a tree: cancelling a parent cancels every
// descendant; cancelling a child never touches the parent. The token carries the effective deadline (the earliest
// along the path to the root) and, because it knows the clock, expiry turns into a cancellation lazily the next
// time anyone asks, and eagerly when the clock reports that it moved. It implements context.Context, so anything
// that takes a ctx can take it, and context.WithCancel(token) derives a standard child that follows it.
type CancellationToken struct {
	parent   *CancellationToken
	clock    Clock
	deadline Deadline

	mu        sync.Mutex
	cancelled bool
	reason    string
	done      chan struct{}
	callbacks []func()
	children  []*CancellationToken
	timer     *time.Timer
}

func newToken(parent *CancellationToken, clock Clock, deadline Deadline) *CancellationToken {
	t := &CancellationToken{parent: parent, clock: clock, deadline: deadline, done: make(chan struct{})}
	if !deadline.IsNone() {
		if w, ok := clock.(advanceWatcher); ok {
			w.onAdvance(func() { t.IsCancelled() })
		} else {
			t.timer = time.AfterFunc(deadline.Remaining(clock), func() { t.IsCancelled() })
		}
	}
	return t
}

// Root is a token that never expires on its own; cancel it to stop everything beneath it.
func Root(clock Clock) *CancellationToken { return newToken(nil, clock, DeadlineNone) }

// Child is a token whose deadline is the earlier of its own and its parent's.
func (t *CancellationToken) Child(own Deadline) *CancellationToken {
	c := newToken(t, t.clock, t.deadline.Earliest(own))
	t.mu.Lock()
	t.children = append(t.children, c)
	t.mu.Unlock()
	if t.IsCancelled() {
		c.Cancel(t.Reason())
	}
	return c
}

func (t *CancellationToken) ChildNone() *CancellationToken { return t.Child(DeadlineNone) }

func (t *CancellationToken) DeadlineOf() Deadline { return t.deadline }

// Cancel is idempotent. The first reason wins; callbacks run exactly once, then children are cancelled.
func (t *CancellationToken) Cancel(why string) bool {
	t.mu.Lock()
	if t.cancelled {
		t.mu.Unlock()
		return false
	}
	t.cancelled = true
	t.reason = why
	close(t.done)
	callbacks, children, timer := t.callbacks, t.children, t.timer
	t.callbacks = nil
	t.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	for _, cb := range callbacks {
		cb()
	}
	for _, c := range children {
		c.Cancel(why)
	}
	return true
}

// IsCancelled is true once cancelled explicitly or once the deadline has passed on the clock.
func (t *CancellationToken) IsCancelled() bool {
	t.mu.Lock()
	cancelled := t.cancelled
	t.mu.Unlock()
	if !cancelled && t.deadline.IsExpired(t.clock) {
		t.Cancel(deadlineExceeded)
		return true
	}
	return cancelled
}

func (t *CancellationToken) Reason() string {
	t.IsCancelled()
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.reason
}

// OnCancel registers cleanup. If already cancelled the callback runs now, so registration can never miss the signal.
// Unlike context.AfterFunc, the callback runs synchronously inside Cancel, so cleanup is done when Cancel returns.
func (t *CancellationToken) OnCancel(callback func()) {
	t.IsCancelled()
	t.mu.Lock()
	if !t.cancelled {
		t.callbacks = append(t.callbacks, callback)
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()
	callback()
}

// CheckCancelled is the value-returning form of throwIfCancelled.
func (t *CancellationToken) CheckCancelled() error {
	if t.IsCancelled() {
		return &CancelledError{t.Reason()}
	}
	return nil
}

func (t *CancellationToken) Parent() *CancellationToken { return t.parent }

// --- context.Context ---

// Done is closed when the token is cancelled, including by deadline expiry.
func (t *CancellationToken) Done() <-chan struct{} {
	t.IsCancelled()
	return t.done
}

// Err is nil until cancelled, then context.DeadlineExceeded or context.Canceled, as the context package specifies.
func (t *CancellationToken) Err() error {
	if !t.IsCancelled() {
		return nil
	}
	if t.Reason() == deadlineExceeded {
		return context.DeadlineExceeded
	}
	return context.Canceled
}

// Deadline reports the instant on the injected clock as a time.Time offset from the Unix epoch.
func (t *CancellationToken) Deadline() (time.Time, bool) {
	if t.deadline.IsNone() {
		return time.Time{}, false
	}
	return time.Unix(0, t.deadline.AtNanos), true
}

func (t *CancellationToken) Value(any) any { return nil }

var _ context.Context = (*CancellationToken)(nil)

// Task is the body of a subtask. It receives the scope's token so it can check for cancellation and inherit the deadline.
type Task[T any] func(token *CancellationToken) (T, error)

// State of a Subtask.
type State int

const (
	Unavailable State = iota
	Success
	Failed
	Cancelled
)

func (s State) String() string {
	return [...]string{"UNAVAILABLE", "SUCCESS", "FAILED", "CANCELLED"}[s]
}

// Subtask is the handle a scope returns from Fork. Values are available only after Join.
type Subtask[T any] struct {
	name    string
	mu      sync.Mutex
	state   State
	value   T
	failure error
}

// subtask is the type-erased view the scope keeps of every Subtask[T].
type subtask interface {
	Name() string
	State() State
	Exception() error
	fail(error)
	cancelled()
}

func (s *Subtask[T]) Name() string { return s.name }

func (s *Subtask[T]) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

var ErrNotSuccess = errors.New("subtask did not succeed")

// Get returns the value; asking a subtask that is not SUCCESS for its value is an error.
func (s *Subtask[T]) Get() (T, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != Success {
		var zero T
		return zero, fmt.Errorf("%w: %s is %s, not SUCCESS", ErrNotSuccess, s.name, s.state)
	}
	return s.value, nil
}

func (s *Subtask[T]) Exception() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failure
}

func (s *Subtask[T]) succeed(v T) {
	s.mu.Lock()
	s.value, s.state = v, Success
	s.mu.Unlock()
}

func (s *Subtask[T]) fail(err error) {
	s.mu.Lock()
	s.failure, s.state = err, Failed
	s.mu.Unlock()
}

func (s *Subtask[T]) cancelled() {
	s.mu.Lock()
	if s.state == Unavailable {
		s.state = Cancelled
	}
	s.mu.Unlock()
}

var (
	ErrScopeClosed = errors.New("cannot fork after join or close")
	ErrNotJoined   = errors.New("Err before Join")
)

// ExecutionError is what a scope reports when a subtask failed; Unwrap exposes the subtask's own error.
type ExecutionError struct {
	TaskName string
	Cause    error
}

func (e *ExecutionError) Error() string { return e.TaskName + " failed" }
func (e *ExecutionError) Unwrap() error { return e.Cause }

// StructuredScope: every task forked inside it is joined before the scope exits, no exceptions. Policy is
// shutdown-on-failure: the first failure cancels the siblings. The deadline is inherited from the parent token and
// tightened by the scope's own budget. Close guarantees that no forked task is still running when it returns, so a
// scope has the same lifetime discipline as a block of code. Each body runs in its own goroutine (Java's virtual
// thread per task) and a sync.WaitGroup is the join.
type StructuredScope struct {
	name  string
	clock Clock
	token *CancellationToken

	mu           sync.Mutex
	subtasks     []subtask
	firstFailure subtask
	wg           sync.WaitGroup
	running      atomic.Int32
	joined       atomic.Bool
	closed       atomic.Bool
}

func NewStructuredScope(name string, parent *CancellationToken, deadline Deadline, clock Clock) *StructuredScope {
	return &StructuredScope{name: name, clock: clock, token: parent.Child(deadline)}
}

func (s *StructuredScope) Token() *CancellationToken { return s.token }
func (s *StructuredScope) Deadline() Deadline        { return s.token.DeadlineOf() }
func (s *StructuredScope) Name() string              { return s.name }

// Fork starts body on its own goroutine under the scope's token. Go methods cannot be generic, so Fork is a function.
func Fork[T any](scope *StructuredScope, taskName string, body Task[T]) (*Subtask[T], error) {
	if scope.joined.Load() || scope.closed.Load() {
		return nil, fmt.Errorf("%s: %w", scope.name, ErrScopeClosed)
	}
	st := &Subtask[T]{name: taskName}
	scope.mu.Lock()
	scope.subtasks = append(scope.subtasks, st)
	scope.mu.Unlock()
	scope.running.Add(1)
	scope.wg.Add(1)
	go func() {
		defer scope.wg.Done()
		defer scope.running.Add(-1)
		defer func() {
			if r := recover(); r != nil { // a panic in a body is a failure, like an uncaught Throwable
				scope.settle(st, fmt.Errorf("panic: %v", r))
			}
		}()
		if scope.token.IsCancelled() {
			st.cancelled()
			return
		}
		value, err := body(scope.token)
		if err == nil {
			st.succeed(value)
			return
		}
		scope.settle(st, err)
	}()
	return st, nil
}

func (s *StructuredScope) settle(st subtask, err error) {
	if errors.Is(err, ErrCancelled) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || s.token.IsCancelled() {
		st.cancelled()
		return
	}
	st.fail(err)
	s.onFailure(st)
}

func (s *StructuredScope) onFailure(failed subtask) {
	s.mu.Lock()
	if s.firstFailure == nil {
		s.firstFailure = failed
	}
	s.mu.Unlock()
	s.token.Cancel(s.name + ": " + failed.Name() + " failed: " + failed.Exception().Error())
}

// Join waits until every subtask has finished. There is no interrupt: a body ends because it observed the token,
// and a deadline reaches it through the clock (ManualClock.Advance or the SystemClock timer) closing Done().
func (s *StructuredScope) Join() {
	s.joined.Store(true)
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.subtasks {
		if st.State() == Unavailable {
			st.cancelled()
		}
	}
}

// Err, after Join, returns the first failure as an *ExecutionError. A scope cancelled by its deadline returns an
// error that matches context.DeadlineExceeded.
func (s *StructuredScope) Err() error {
	if !s.joined.Load() {
		return fmt.Errorf("%s: %w", s.name, ErrNotJoined)
	}
	s.mu.Lock()
	f := s.firstFailure
	s.mu.Unlock()
	if f != nil {
		return &ExecutionError{f.Name(), f.Exception()}
	}
	if s.token.IsCancelled() && s.token.Reason() == deadlineExceeded {
		return fmt.Errorf("%s: %s: %w", s.name, s.token.Reason(), context.DeadlineExceeded)
	}
	return nil
}

func (s *StructuredScope) IsCancelled() bool          { return s.token.IsCancelled() }
func (s *StructuredScope) CancellationReason() string { return s.token.Reason() }

// Active is the number of forked tasks that have not yet finished; zero after Join or Close.
func (s *StructuredScope) Active() int { return int(s.running.Load()) }

func (s *StructuredScope) Subtasks() []subtask {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]subtask(nil), s.subtasks...)
}

// Close: leaving the scope without joining is a bug; Close cancels what is left and waits for it anyway.
func (s *StructuredScope) Close() {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	if !s.joined.Load() {
		s.token.Cancel(s.name + ": scope closed without join")
		s.Join()
	}
}

// Log is a goroutine-safe list of strings for cleanup messages.
type Log struct {
	mu    sync.Mutex
	lines []string
}

func (l *Log) Add(line string) {
	l.mu.Lock()
	l.lines = append(l.lines, line)
	l.mu.Unlock()
}

func (l *Log) Sorted() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := append([]string(nil), l.lines...)
	sort.Strings(out)
	return out
}

// Authorize is a fan-out authorization: three checks, one answer. All three share one token and one deadline.
func Authorize(parent *CancellationToken, clock Clock, budget time.Duration, balance, amount Money,
	fraudFails bool, slowBank *ManualClock, log *Log) (string, error) {
	var siblingsStarted, holdPlaced sync.WaitGroup // make the demo's ordering deterministic
	siblingsStarted.Add(2)
	holdPlaced.Add(1)
	scope := NewStructuredScope("authorize", parent, DeadlineAfter(clock, budget), clock)
	defer scope.Close()
	fraud, err := Fork(scope, "fraud-check", func(token *CancellationToken) (bool, error) {
		siblingsStarted.Wait()
		if fraudFails {
			return false, errors.New("velocity rule tripped")
		}
		return true, nil
	})
	if err != nil {
		return "", err
	}
	funds, err := Fork(scope, "balance-check", func(token *CancellationToken) (bool, error) {
		token.OnCancel(func() { log.Add("balance-check: released hold") })
		holdPlaced.Done()
		siblingsStarted.Done()
		if err := WaitForSignalOrCancel(token); err != nil {
			return false, err
		}
		return balance.Covers(amount)
	})
	if err != nil {
		return "", err
	}
	fx, err := Fork(scope, "fx-quote", func(token *CancellationToken) (int64, error) {
		token.OnCancel(func() { log.Add("fx-quote: closed quote stream") })
		siblingsStarted.Done()
		if slowBank != nil {
			holdPlaced.Wait()                           // Advance cancels eagerly; let the sibling register its cleanup first
			slowBank.Advance(budget + time.Millisecond) // the bank is slower than our budget
		}
		if err := WaitForSignalOrCancel(token); err != nil {
			return 0, err
		}
		return 10_000, nil
	})
	if err != nil {
		return "", err
	}
	scope.Join()
	var exec *ExecutionError
	switch err := scope.Err(); {
	case errors.As(err, &exec):
		return "DECLINED (" + exec.Cause.Error() + ")", nil
	case errors.Is(err, context.DeadlineExceeded):
		return "TIMEOUT (" + scope.Name() + ": " + scope.CancellationReason() + ")", nil
	case err != nil:
		return "", err
	}
	ok1, err := fraud.Get()
	if err != nil {
		return "", err
	}
	ok2, err := funds.Get()
	if err != nil {
		return "", err
	}
	rate, err := fx.Get()
	if err != nil {
		return "", err
	}
	if ok1 && ok2 {
		return fmt.Sprintf("APPROVED at rate %d", rate), nil
	}
	return "DECLINED (insufficient funds)", nil
}

// WaitForSignalOrCancel simulates a remote call that completes only when cancelled or when a signal arrives;
// here nobody signals, so it returns when Done() closes. This is the whole reason every Go I/O API takes a ctx.
func WaitForSignalOrCancel(token *CancellationToken) error {
	<-token.Done()
	return token.CheckCancelled()
}

func main() {
	clock := NewSystemClock()
	balance := OfMinor(10_000, USD)
	amount := OfMinor(2_500, USD)

	fmt.Println("== Deadlines compose by taking the earliest ==")
	mc := NewManualClock()
	root := Root(mc)
	request := root.Child(DeadlineAfter(mc, 200*time.Millisecond))
	bankCall := request.Child(DeadlineAfter(mc, 800*time.Millisecond))
	fmt.Printf("request budget  = %d ms\n", request.DeadlineOf().Remaining(mc).Milliseconds())
	fmt.Printf("bank-call asked = 800 ms, got %d ms (inherited)\n", bankCall.DeadlineOf().Remaining(mc).Milliseconds())
	mc.Advance(250 * time.Millisecond)
	fmt.Printf("after 250 ms: request cancelled? %t (%s)\n", request.IsCancelled(), request.Reason())
	fmt.Printf("              bank-call cancelled? %t, root cancelled? %t\n", bankCall.IsCancelled(), root.IsCancelled())

	fmt.Println()
	fmt.Println("== One sibling fails: the others are cancelled and clean up ==")
	log := &Log{}
	r1, err := Authorize(Root(clock), clock, 5*time.Second, balance, amount, true, nil, log)
	if err != nil {
		panic(err)
	}
	fmt.Println(r1)
	for _, line := range log.Sorted() {
		fmt.Println("  cleanup: " + line)
	}

	fmt.Println()
	fmt.Println("== The bank is slower than the budget: deadline cancels everything ==")
	slow := NewManualClock()
	log2 := &Log{}
	r2, err := Authorize(Root(slow), slow, 300*time.Millisecond, balance, amount, false, slow, log2)
	if err != nil {
		panic(err)
	}
	fmt.Println(r2)
	for _, line := range log2.Sorted() {
		fmt.Println("  cleanup: " + line)
	}

	fmt.Println()
	fmt.Println("== Leaving a scope without join still waits for every child ==")
	var finished atomic.Int32
	leaked := func() *StructuredScope {
		scope := NewStructuredScope("leaky", Root(clock), DeadlineNone, clock)
		defer scope.Close()
		for i := 0; i < 3; i++ {
			if _, err := Fork(scope, fmt.Sprintf("worker-%d", i), func(token *CancellationToken) (struct{}, error) {
				if err := WaitForSignalOrCancel(token); err != nil {
					return struct{}{}, err
				}
				finished.Add(1)
				return struct{}{}, nil
			}); err != nil {
				panic(err)
			}
		}
		return scope // no join: the function returns early, say because of an early return
	}()
	fmt.Println("scope cancelled on close: " + leaked.CancellationReason())
	cancelled := 0
	for _, s := range leaked.Subtasks() {
		if s.State() == Cancelled {
			cancelled++
		}
	}
	fmt.Printf("subtasks cancelled = %d of 3; completed normally = %d; still running after close = %d\n",
		cancelled, finished.Load(), leaked.Active())
}
