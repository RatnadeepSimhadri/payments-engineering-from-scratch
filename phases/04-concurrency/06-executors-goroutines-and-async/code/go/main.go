// Lesson: phases/04-concurrency/06-executors-goroutines-and-async/docs/en.md
// Executors, Goroutines, and asyncio
// Run:  go run .
// Test: go test -race ./...
// Sources: Go Memory Model and `go` statement spec (go.dev/ref/mem, go.dev/ref/spec); JEP 444 (Virtual Threads, Java 21);
// java.util.concurrent.ExecutorService and CompletableFuture Javadoc; Python asyncio documentation (gather, wait_for).
// Go 1.22+, standard library only. Time is injected through Timer; nothing sleeps for real.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Currency is an ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01.
type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	EUR = Currency{"EUR", 2}
)

var (
	// ErrCurrencyMismatch is returned when two currencies are combined.
	ErrCurrencyMismatch = errors.New("currency mismatch")
	// ErrOverflow is returned when minor units leave the int64 range.
	ErrOverflow = errors.New("amount overflows int64 minor units")
)

// Money is an exact amount in minor units of one currency. Immutable, so a quote can be shared across goroutines freely.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

// OfMinor builds a Money from minor units.
func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func addChecked(a, b int64) (int64, error) {
	sum := a + b
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum >= 0) {
		return 0, ErrOverflow
	}
	return sum, nil
}

// Plus adds two amounts of the same currency.
func (m Money) Plus(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.Currency.Code, other.Currency.Code)
	}
	sum, err := addChecked(m.MinorUnits, other.MinorUnits)
	if err != nil {
		return Money{}, err
	}
	return Money{sum, m.Currency}, nil
}

// Compare orders two amounts; it panics on a currency mismatch because a comparison cannot report an error
// and mixing currencies is a programming error.
func (m Money) Compare(other Money) int {
	if m.Currency != other.Currency {
		panic(fmt.Sprintf("cannot compare %s with %s", m.Currency.Code, other.Currency.Code))
	}
	switch {
	case m.MinorUnits < other.MinorUnits:
		return -1
	case m.MinorUnits > other.MinorUnits:
		return 1
	}
	return 0
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
	fracText := ""
	if m.Currency.Exponent > 0 {
		fracText = fmt.Sprintf(".%0*d", m.Currency.Exponent, frac)
	}
	sign := ""
	if m.MinorUnits < 0 {
		sign = "-"
	}
	return fmt.Sprintf("%s%d%s %s", sign, whole, fracText, m.Currency.Code)
}

// PaymentRequest is the payment being quoted.
type PaymentRequest struct {
	PaymentID string
	Amount    Money
}

// Outcome is how a processor call ended.
type Outcome int

const (
	OK Outcome = iota
	TimedOut
	Failed
)

func (o Outcome) String() string { return [...]string{"OK", "TIMED_OUT", "FAILED"}[o] }

// Quote is one processor's answer: a fee quote, or why there is none.
type Quote struct {
	Processor string
	Fee       Money
	Outcome   Outcome
	Detail    string
}

func quoteOK(processor string, fee Money) Quote { return Quote{processor, fee, OK, ""} }
func quoteTimedOut(processor string, afterMillis int64) Quote {
	return Quote{Processor: processor, Outcome: TimedOut, Detail: fmt.Sprintf("no answer within %d ms", afterMillis)}
}
func quoteFailed(processor string, err error) Quote {
	return Quote{Processor: processor, Outcome: Failed, Detail: err.Error()}
}

// ProcessorAdapter is a card processor's quote endpoint. The real one is an HTTP call; here it is whatever the adapter decides.
// A synchronous error is the Go spelling of a client library that throws before sending anything.
type ProcessorAdapter interface {
	Name() string
	Quote(request PaymentRequest) (*Future[Quote], error)
}

// ScriptedAdapter is a deterministic stand-in for a remote processor. Immediate adapters answer at once; scripted ones
// answer when the test (or the demo) calls Respond or Fail; a hung adapter is a scripted one nobody ever answers.
type ScriptedAdapter struct {
	name         string
	immediateFee *Money // nil -> scripted
	mu           sync.Mutex
	pending      []*Future[Quote]
	calls        atomic.Int32
}

// Immediate answers every call with fee at once.
func Immediate(name string, fee Money) *ScriptedAdapter {
	return &ScriptedAdapter{name: name, immediateFee: &fee}
}

// Scripted answers only when told to.
func Scripted(name string) *ScriptedAdapter { return &ScriptedAdapter{name: name} }

func (a *ScriptedAdapter) Name() string { return a.name }

// Quote records the call and either answers or parks a future.
func (a *ScriptedAdapter) Quote(PaymentRequest) (*Future[Quote], error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls.Add(1)
	if a.immediateFee != nil {
		return Completed(quoteOK(a.name, *a.immediateFee)), nil
	}
	f := NewFuture[Quote]()
	a.pending = append(a.pending, f)
	return f, nil
}

func (a *ScriptedAdapter) takePending() []*Future[Quote] {
	a.mu.Lock()
	defer a.mu.Unlock()
	pending := a.pending
	a.pending = nil
	return pending
}

// Respond answers every outstanding call with this fee.
func (a *ScriptedAdapter) Respond(fee Money) {
	for _, f := range a.takePending() {
		f.Complete(quoteOK(a.name, fee))
	}
}

// Fail answers every outstanding call with an error.
func (a *ScriptedAdapter) Fail(message string) {
	for _, f := range a.takePending() {
		f.Fail(errors.New(message))
	}
}

// Calls is how many times Quote was invoked.
func (a *ScriptedAdapter) Calls() int { return int(a.calls.Load()) }

// Outstanding is how many calls are still unanswered.
func (a *ScriptedAdapter) Outstanding() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.pending)
}

// Timer is the one place time enters the design. Production uses time.AfterFunc; tests and the demo advance a ManualTimer.
type Timer interface {
	After(millis int64) *Future[struct{}]
	NowMillis() int64
}

// RealTimer wraps the wall clock.
type RealTimer struct {
	start time.Time
}

// NewRealTimer starts the clock now.
func NewRealTimer() *RealTimer { return &RealTimer{start: time.Now()} }

// After fires once the duration has elapsed.
func (r *RealTimer) After(millis int64) *Future[struct{}] {
	f := NewFuture[struct{}]()
	time.AfterFunc(time.Duration(millis)*time.Millisecond, func() { f.Complete(struct{}{}) })
	return f
}

// NowMillis is elapsed wall-clock time.
func (r *RealTimer) NowMillis() int64 { return time.Since(r.start).Milliseconds() }

type pendingAlarm struct {
	deadline int64
	seq      int64
	future   *Future[struct{}]
}

// ManualTimer is a simulated clock: Advance fires every alarm whose deadline has passed, in deadline order.
type ManualTimer struct {
	mu      sync.Mutex
	pending []pendingAlarm
	now     int64
	seq     int64
}

// After arms an alarm; a negative delay is a programming error and panics.
func (t *ManualTimer) After(millis int64) *Future[struct{}] {
	if millis < 0 {
		panic("negative delay")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	f := NewFuture[struct{}]()
	t.pending = append(t.pending, pendingAlarm{t.now + millis, t.seq, f})
	t.seq++
	return f
}

// NowMillis is the simulated time.
func (t *ManualTimer) NowMillis() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.now
}

// Advance moves the clock and fires due alarms outside the lock, because completion runs callbacks.
func (t *ManualTimer) Advance(millis int64) {
	var due []pendingAlarm
	t.mu.Lock()
	t.now += millis
	sort.SliceStable(t.pending, func(i, j int) bool {
		if t.pending[i].deadline != t.pending[j].deadline {
			return t.pending[i].deadline < t.pending[j].deadline
		}
		return t.pending[i].seq < t.pending[j].seq
	})
	keep := t.pending[:0]
	for _, p := range t.pending {
		switch {
		case p.future.IsCancelled(): // the caller no longer needs this alarm
		case p.deadline <= t.now:
			due = append(due, p)
		default:
			keep = append(keep, p)
		}
	}
	t.pending = keep
	t.mu.Unlock()
	for _, p := range due {
		p.future.Complete(struct{}{})
	}
}

// Outstanding counts alarms that are still armed (not fired, not cancelled).
func (t *ManualTimer) Outstanding() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, p := range t.pending {
		if !p.future.IsCancelled() {
			n++
		}
	}
	return n
}

// WithTimeout races one adapter call against its own alarm. Nothing in here blocks a goroutine: the first of the two
// to settle completes the result, and a late answer or a late alarm is a no-op.
func WithTimeout(adapter ProcessorAdapter, request PaymentRequest, timeoutMillis int64, timer Timer) *Future[Quote] {
	call, err := adapter.Quote(request)
	if err != nil { // a synchronous error is still a failed quote, not a crashed fan-out
		return Completed(quoteFailed(adapter.Name(), err))
	}
	result := NewFuture[Quote]()
	alarm := timer.After(timeoutMillis)
	call.OnComplete(func(q Quote, err error) {
		alarm.Cancel() // the answer arrived: the alarm is no longer needed
		if err != nil {
			result.Complete(quoteFailed(adapter.Name(), err))
			return
		}
		result.Complete(q)
	})
	alarm.OnComplete(func(_ struct{}, err error) {
		if err == nil { // fired, not cancelled
			result.Complete(quoteTimedOut(adapter.Name(), timeoutMillis))
		}
	})
	return result
}

// QuoteAll fans out to every processor and fans in to one list, in adapter order. The combined future completes
// when every adapter has either answered, failed, or been timed out.
func QuoteAll(adapters []ProcessorAdapter, request PaymentRequest, timeoutMillis int64, timer Timer) *Future[[]Quote] {
	futures := make([]*Future[Quote], 0, len(adapters))
	for _, a := range adapters {
		futures = append(futures, WithTimeout(a, request, timeoutMillis, timer))
	}
	return AllOf(futures)
}

// FirstOK completes with the first adapter to answer OK; timeouts and failures are ignored unless everyone produced one,
// in which case it completes with nil.
func FirstOK(adapters []ProcessorAdapter, request PaymentRequest, timeoutMillis int64, timer Timer) *Future[*Quote] {
	winner := NewFuture[*Quote]()
	var remaining atomic.Int32
	remaining.Store(int32(len(adapters)))
	if len(adapters) == 0 {
		winner.Complete(nil)
	}
	for _, a := range adapters {
		WithTimeout(a, request, timeoutMillis, timer).OnComplete(func(q Quote, _ error) {
			if q.Outcome == OK {
				quote := q
				winner.Complete(&quote) // first OK completes; later ones are no-ops
			}
			if remaining.Add(-1) == 0 {
				winner.Complete(nil) // everyone answered, none OK
			}
		})
	}
	return winner
}

// Cheapest is the lowest-fee OK quote, if any.
func Cheapest(quotes []Quote) (Quote, bool) {
	var best Quote
	found := false
	for _, q := range quotes {
		if q.Outcome != OK {
			continue
		}
		if !found || q.Fee.Compare(best.Fee) < 0 {
			best, found = q, true
		}
	}
	return best, found
}

// Executor runs tasks; who supplies the goroutines is the whole difference between the models.
type Executor interface {
	Execute(task func())
	// Close waits for every submitted task to finish, like ExecutorService.close().
	Close()
}

// FixedPool is Model 1: at most n tasks run at once. A bounded semaphore channel plays the pool's n threads; a task
// that blocks while holding a permit starves the tasks behind it exactly as a blocked pool thread does.
type FixedPool struct {
	permits chan struct{}
	wg      sync.WaitGroup
}

// NewFixedPool allows n concurrent tasks.
func NewFixedPool(n int) *FixedPool { return &FixedPool{permits: make(chan struct{}, n)} }

// Execute waits for a permit, then runs the task.
func (p *FixedPool) Execute(task func()) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.permits <- struct{}{} // no permit free: this task waits for a goroutine that is itself waiting
		defer func() { <-p.permits }()
		task()
	}()
}

// Close waits for every task.
func (p *FixedPool) Close() { p.wg.Wait() }

// GoroutinePerTask is Model 2: one goroutine per task, which is what virtual threads give Java. A blocked goroutine
// costs a few kilobytes of stack and holds no OS thread, so every submitted task starts.
type GoroutinePerTask struct {
	wg sync.WaitGroup
}

// Execute starts the task immediately.
func (g *GoroutinePerTask) Execute(task func()) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		task()
	}()
}

// Close waits for every task.
func (g *GoroutinePerTask) Close() { g.wg.Wait() }

// Submit runs fn on the executor and returns its result as a Future (ExecutorService.submit).
func Submit[T any](ex Executor, fn func() (T, error)) *Future[T] {
	f := NewFuture[T]()
	ex.Execute(func() {
		v, err := fn()
		if err != nil {
			f.Fail(err)
			return
		}
		f.Complete(v)
	})
	return f
}

// BlockingQuoteAll is the same fan-out written in the blocking style: one task per adapter, each task blocks until its
// answer arrives. How well this works depends entirely on who supplies the goroutines.
func BlockingQuoteAll(ex Executor, adapters []ProcessorAdapter, request PaymentRequest) []Quote {
	futures := make([]*Future[Quote], len(adapters))
	for i, a := range adapters {
		adapter := a
		futures[i] = Submit(ex, func() (Quote, error) {
			call, err := adapter.Quote(request)
			if err != nil {
				return Quote{}, err
			}
			return call.Get() // blocks this task's goroutine
		})
	}
	out := make([]Quote, len(adapters))
	for i, f := range futures {
		q, err := f.Get() // returns only when every task is done
		if err != nil {
			q = quoteFailed(adapters[i].Name(), err)
		}
		out[i] = q
	}
	return out
}

// CountStartedBeforeRelease submits tasks blocking tasks, waits until expectedStarted of them are running, then reports
// how many started. With a fixed pool of N < tasks, exactly N start; the rest wait for a permit held by a task that is
// itself waiting. With a goroutine per task, all of them start.
func CountStartedBeforeRelease(ex Executor, tasks, expectedStarted int) int {
	gate := make(chan struct{})
	startedLatch := NewCountDownLatch(expectedStarted)
	var started atomic.Int32
	futures := make([]*Future[int], 0, tasks)
	for i := 0; i < tasks; i++ {
		id := i
		futures = append(futures, Submit(ex, func() (int, error) {
			started.Add(1)
			startedLatch.CountDown()
			<-gate // a blocked goroutine: a card network that has not answered yet
			return id, nil
		}))
	}
	startedLatch.Await()
	observed := int(started.Load()) // tasks beyond the pool size cannot have started: no permit exists for them
	close(gate)
	for _, f := range futures {
		f.MustGet()
	}
	return observed
}

// Describe renders quotes one per line.
func Describe(quotes []Quote) string {
	var sb strings.Builder
	for _, q := range quotes {
		fmt.Fprintf(&sb, "  %-8s%v", q.Processor, q.Outcome)
		if q.Outcome == OK {
			fmt.Fprintf(&sb, " fee %v", q.Fee)
		} else {
			fmt.Fprintf(&sb, " (%s)", q.Detail)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

func adapters(as ...ProcessorAdapter) []ProcessorAdapter { return as }

func main() {
	request := PaymentRequest{"pay_001", OfMinor(12_999, USD)}

	fmt.Println("== Model 1: a bounded pool and blocking calls ==")
	pool := NewFixedPool(2)
	started := CountStartedBeforeRelease(pool, 3, 2)
	pool.Close()
	fmt.Printf("3 blocking adapter calls on a pool of 2: %d started; the third waits for a permit held by a task that is itself waiting\n", started)

	fmt.Println()
	fmt.Println("== Model 2: a goroutine per task, same blocking code ==")
	perTask := &GoroutinePerTask{}
	started = CountStartedBeforeRelease(perTask, 3, 3)
	fmt.Printf("3 blocking adapter calls on goroutines: %d started; a blocked goroutine parks, it does not hold an OS thread\n", started)
	alpha := Immediate("alpha", OfMinor(329, USD))
	beta := Immediate("beta", OfMinor(301, USD))
	quotes := BlockingQuoteAll(perTask, adapters(alpha, beta), request)
	perTask.Close()
	fmt.Print(Describe(quotes))
	cheapest, _ := Cheapest(quotes)
	fmt.Printf("cheapest: %s\n", cheapest.Processor)

	fmt.Println()
	fmt.Println("== Model 3: Future fan-out with per-adapter timeouts (simulated clock) ==")
	timer := &ManualTimer{}
	alpha = Immediate("alpha", OfMinor(329, USD))
	beta = Scripted("beta")
	gamma := Scripted("gamma")
	delta := Scripted("delta")
	all := QuoteAll(adapters(alpha, beta, gamma, delta), request, 800, timer)
	fmt.Printf("t=%dms  fan-out issued to 4 processors; alpha answered immediately; combined future done: %v\n", timer.NowMillis(), all.IsDone())
	timer.Advance(250)
	beta.Respond(OfMinor(275, USD))
	fmt.Printf("t=%dms  beta answered; combined future done: %v\n", timer.NowMillis(), all.IsDone())
	timer.Advance(300)
	delta.Fail("HTTP 503 from processor")
	fmt.Printf("t=%dms  delta failed; gamma silent; combined future done: %v\n", timer.NowMillis(), all.IsDone())
	timer.Advance(250)
	fmt.Printf("t=%dms  deadline passed; combined future done: %v\n", timer.NowMillis(), all.IsDone())
	quotes = all.MustGet()
	fmt.Print(Describe(quotes))
	if best, ok := Cheapest(quotes); ok {
		fmt.Printf("cheapest OK quote: %s at %v\n", best.Processor, best.Fee)
	} else {
		fmt.Println("cheapest OK quote: none")
	}
	fmt.Printf("gamma still has %d outstanding call: the timeout abandoned the wait, it did not cancel the request\n", gamma.Outstanding())

	fmt.Println()
	fmt.Println("== Fan-in variant: first OK wins ==")
	timer2 := &ManualTimer{}
	slow := Scripted("slow")
	fast := Scripted("fast")
	first := FirstOK(adapters(slow, fast), request, 800, timer2)
	timer2.Advance(100)
	fast.Respond(OfMinor(350, USD))
	winner := "none"
	if q := first.MustGet(); q != nil {
		winner = q.Processor
	}
	fmt.Printf("fast answered at t=100ms -> winner: %s (slow never answered and nobody waited for it)\n", winner)
}
