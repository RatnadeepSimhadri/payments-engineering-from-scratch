// Lesson: phases/05-distributed-foundations/08-timeouts-and-deadlines/docs/en.md
// Timeouts and Deadline Propagation
// Run:  go run .
// Test: go test ./...
// Sources: gRPC "grpc-timeout" header (PROTOCOL-HTTP2.md); Dean & Barroso, "The Tail at Scale" (CACM 2013); Go context package docs.
// Go 1.22+, standard library only. Time is injected through Clock; nothing sleeps. Same design as the Java version.
package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Clock is the source of time. Production uses time.Now(); demos and tests use FakeClock.
type Clock interface {
	NowMs() int64
}

// FakeClock only moves when the simulation says so.
type FakeClock struct{ now int64 }

func NewFakeClock(startMs int64) *FakeClock { return &FakeClock{startMs} }

func (c *FakeClock) NowMs() int64 { return c.now }

// Advance moves time forward; moving it backwards is a programming error and panics.
func (c *FakeClock) Advance(ms int64) {
	if ms < 0 {
		panic(fmt.Sprintf("cannot move time backwards: %d", ms))
	}
	c.now += ms
}

// Sentinel errors. Typed errors below carry the details and match these under errors.Is.
var (
	ErrDeadlineExceeded = errors.New("deadline exceeded")
	ErrUnavailable      = errors.New("unavailable")
	ErrBadHeader        = errors.New("bad timeout header")
)

// DeadlineExceededError: a call was refused or abandoned because its deadline has passed or cannot be met.
type DeadlineExceededError struct {
	Message     string
	RemainingMs int64
}

func (e *DeadlineExceededError) Error() string        { return e.Message }
func (e *DeadlineExceededError) Is(target error) bool { return target == ErrDeadlineExceeded }

// UnavailableError: a simulated dependency that is up but failing. Retryable.
type UnavailableError struct{ Message string }

func (e *UnavailableError) Error() string        { return e.Message }
func (e *UnavailableError) Is(target error) bool { return target == ErrUnavailable }

// Deadline is an absolute point in time by which work must be finished. A timeout is relative ("500 ms from now");
// a deadline is absolute ("by t = 1500"). Deadlines compose across a call chain, timeouts do not.
type Deadline struct{ AtMs int64 }

// DeadlineNone: no deadline. RemainingMs reports math.MaxInt64 and it never expires.
var DeadlineNone = Deadline{math.MaxInt64}

// DeadlineAfter is now + timeout. A negative timeout is a programming error and panics.
func DeadlineAfter(clock Clock, timeoutMs int64) Deadline {
	if timeoutMs < 0 {
		panic(fmt.Sprintf("timeout must be >= 0: %d", timeoutMs))
	}
	return Deadline{SaturatingAdd(clock.NowMs(), timeoutMs)}
}

// SaturatingAdd clamps at math.MaxInt64 instead of wrapping: overflow iff both operands disagree in sign with the result.
func SaturatingAdd(a, b int64) int64 {
	r := a + b
	if ((a ^ r) & (b ^ r)) < 0 {
		return math.MaxInt64
	}
	return r
}

func (d Deadline) IsNone() bool { return d.AtMs == math.MaxInt64 }

// RemainingMs is the milliseconds left, clamped at zero. Never negative, so callers can subtract margins safely.
func (d Deadline) RemainingMs(clock Clock) int64 {
	if d.IsNone() {
		return math.MaxInt64
	}
	return max(0, d.AtMs-clock.NowMs())
}

func (d Deadline) IsExpired(clock Clock) bool { return !d.IsNone() && clock.NowMs() >= d.AtMs }

// Earliest is the tighter of two deadlines. A child may shorten its parent's deadline, never extend it.
func (d Deadline) Earliest(other Deadline) Deadline {
	if d.AtMs <= other.AtMs {
		return d
	}
	return other
}

// Context is what travels with a request down the call chain: an identifier for logs and the deadline.
// Modelled on Go's context.Context and gRPC's per-call deadline, but on the injected clock so tests never sleep.
type Context struct {
	RequestID string
	Deadline  Deadline
}

func Root(requestID string) Context { return Context{requestID, DeadlineNone} }

// WithTimeout derives a child context whose deadline is min(parent, now + timeout). Tightening only.
func (c Context) WithTimeout(clock Clock, timeoutMs int64) Context {
	return Context{c.RequestID, c.Deadline.Earliest(DeadlineAfter(clock, timeoutMs))}
}

// WithoutDeadline is what a service that drops the deadline effectively passes downstream. The bug this lesson is about.
func (c Context) WithoutDeadline() Context { return Context{c.RequestID, DeadlineNone} }

func (c Context) RemainingMs(clock Clock) int64 { return c.Deadline.RemainingMs(clock) }

// Checkpoint: call before any expensive or irreversible step.
func (c Context) Checkpoint(clock Clock, where string) error {
	if c.Deadline.IsExpired(clock) {
		return &DeadlineExceededError{where + ": deadline already passed", 0}
	}
	return nil
}

// RequireBudget refuses work that cannot finish in the budget. Cheaper than starting it and being abandoned,
// and it is the step that prevents a side effect nobody will ever read the result of.
func (c Context) RequireBudget(clock Clock, neededMs int64, where string) error {
	remaining := c.RemainingMs(clock)
	if remaining < neededMs {
		return &DeadlineExceededError{fmt.Sprintf("%s needs %d ms but only %d ms remain", where, neededMs, remaining), remaining}
	}
	return nil
}

// ToWireTimeout encodes the remaining budget as a relative duration for the wire, in the shape of gRPC's grpc-timeout
// header: 1-8 ASCII digits followed by a unit (H, M, S, m, u, n). Relative, not absolute, because the receiver's clock
// is not ours (Phase 5, Lesson 04). Returns "" when there is no deadline (an absent header).
func (c Context) ToWireTimeout(clock Clock) string {
	if c.Deadline.IsNone() {
		return ""
	}
	return strconv.FormatInt(c.RemainingMs(clock), 10) + "m"
}

// FromWire rebuilds a context on the receiving side from the wire header against the receiver's own clock.
func FromWire(requestID, header string, clock Clock) (Context, error) {
	if header == "" {
		return Root(requestID), nil
	}
	if len(header) < 2 || len(header) > 9 {
		return Context{}, fmt.Errorf("%w: %q", ErrBadHeader, header)
	}
	unit := header[len(header)-1]
	digits := header[:len(header)-1]
	if strings.Trim(digits, "0123456789") != "" { // Long.parseLong's grammar: ASCII digits only
		return Context{}, fmt.Errorf("%w: %q", ErrBadHeader, header)
	}
	value, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return Context{}, fmt.Errorf("%w: %q", ErrBadHeader, header)
	}
	var ms int64
	switch unit {
	case 'H':
		ms = value * 3_600_000
	case 'M':
		ms = value * 60_000
	case 'S':
		ms = value * 1_000
	case 'm':
		ms = value
	case 'u':
		ms = value / 1_000
	case 'n':
		ms = value / 1_000_000
	default:
		return Context{}, fmt.Errorf("%w: bad timeout unit: %c", ErrBadHeader, unit)
	}
	return Root(requestID).WithTimeout(clock, ms), nil
}

// Effect is one irreversible thing a simulated service did, with the time it did it.
type Effect struct {
	AtMs int64
	What string
}

func (e Effect) String() string { return fmt.Sprintf("t=%dms %s", e.AtMs, e.What) }

// EffectLog is an append-only record of effects.
type EffectLog struct{ effects []Effect }

func (l *EffectLog) Record(atMs int64, what string) {
	l.effects = append(l.effects, Effect{atMs, what})
}

func (l *EffectLog) All() []Effect { return append([]Effect(nil), l.effects...) }

func (l *EffectLog) Contains(fragment string) bool {
	for _, e := range l.effects {
		if strings.Contains(e.What, fragment) {
			return true
		}
	}
	return false
}

// Service is something that can be called with a context.
type Service interface {
	Call(ctx Context) (string, error)
}

const HopMs = 20

// RemoteService is a simulated remote service. Receiving the request costs one network hop; doing the work costs
// WorkMs; if there is a downstream it is called next; a side effect (if any) is recorded once the work is done.
// The response costs one more hop, even when the response is an error.
type RemoteService struct {
	Name       string
	WorkMs     int64
	SideEffect string  // "" when the service is pure
	Downstream Service // nil at the end of the chain
	Propagate  bool    // false models a service that forgets to forward the deadline
	clock      *FakeClock
	log        *EffectLog
}

func NewRemoteService(name string, workMs int64, sideEffect string, downstream Service, propagate bool, clock *FakeClock, log *EffectLog) *RemoteService {
	return &RemoteService{name, workMs, sideEffect, downstream, propagate, clock, log}
}

func (s *RemoteService) Call(incoming Context) (string, error) {
	s.clock.Advance(HopMs)       // request on the wire
	defer s.clock.Advance(HopMs) // response (or error) on the wire
	// What arrives is a header, not an object: rebuild the context against this service's clock.
	ctx, err := FromWire(incoming.RequestID, incoming.ToWireTimeout(s.clock), s.clock)
	if err != nil {
		return "", err
	}
	if err := ctx.RequireBudget(s.clock, s.WorkMs, s.Name); err != nil { // admission: refuse what cannot finish
		return "", err
	}
	s.clock.Advance(s.WorkMs)
	if s.Downstream != nil {
		forwarded := ctx
		if !s.Propagate {
			forwarded = ctx.WithoutDeadline()
		}
		if _, err := s.Downstream.Call(forwarded); err != nil {
			return "", err
		}
	}
	if s.SideEffect != "" {
		s.log.Record(s.clock.NowMs(), s.Name+": "+s.SideEffect)
	}
	return s.Name + " ok", nil
}

// Status is how a call ended from the caller's point of view.
type Status int

const (
	OK          Status = iota // response arrived before the deadline
	FailedFast                // a deadline error came back before the deadline: no work was wasted
	TimedOut                  // the caller stopped waiting; the response, if any, arrived to nobody
	Unavailable               // the dependency answered with a retryable error
)

func (s Status) String() string {
	return [...]string{"OK", "FAILED_FAST", "TIMED_OUT", "UNAVAILABLE"}[s]
}

type CallOutcome struct {
	Status    Status
	ElapsedMs int64
	Detail    string
}

// Client is the edge: the client that sets the top-level deadline. It is the only place a timeout is chosen;
// everything below derives its budget from this one number.
type Client struct{ clock *FakeClock }

func NewClient(clock *FakeClock) *Client { return &Client{clock} }

func (c *Client) Invoke(service Service, requestID string, timeoutMs int64) CallOutcome {
	start := c.clock.NowMs()
	ctx := Root(requestID).WithTimeout(c.clock, timeoutMs)
	result, err := service.Call(ctx)
	elapsed := c.clock.NowMs() - start
	switch {
	case err == nil:
		// The response may have arrived after we stopped listening. Then it is a timeout, whatever it says.
		if elapsed > timeoutMs {
			return CallOutcome{TimedOut, elapsed, fmt.Sprintf("response discarded: arrived %d ms late", elapsed-timeoutMs)}
		}
		return CallOutcome{OK, elapsed, result}
	case errors.Is(err, ErrDeadlineExceeded):
		if elapsed > timeoutMs {
			return CallOutcome{TimedOut, elapsed, err.Error()}
		}
		return CallOutcome{FailedFast, elapsed, err.Error()}
	case errors.Is(err, ErrUnavailable):
		return CallOutcome{Unavailable, elapsed, err.Error()}
	}
	return CallOutcome{FailedFast, elapsed, err.Error()}
}

// FlakyService fails its first N calls with UnavailableError, then succeeds. Each call costs LatencyMs.
type FlakyService struct {
	clock        *FakeClock
	latencyMs    int64
	failuresLeft int
	Calls        int
}

func NewFlakyService(clock *FakeClock, latencyMs int64, failBefore int) *FlakyService {
	return &FlakyService{clock: clock, latencyMs: latencyMs, failuresLeft: failBefore}
}

func (f *FlakyService) Call(ctx Context) (string, error) {
	f.Calls++
	if err := ctx.RequireBudget(f.clock, f.latencyMs, "flaky"); err != nil {
		return "", err
	}
	f.clock.Advance(f.latencyMs)
	if f.failuresLeft > 0 {
		f.failuresLeft--
		return "", &UnavailableError{fmt.Sprintf("503 from flaky (call %d)", f.Calls)}
	}
	return fmt.Sprintf("flaky ok after %d call(s)", f.Calls), nil
}

type RetryOutcome struct {
	Status            Status
	Attempts          int
	ElapsedMs         int64
	AttemptTimeoutsMs []int64
}

// DeadlineAwareRetrier: retries that live inside a deadline. Each attempt gets min(perAttemptTimeout, remaining), and
// no attempt starts when the remaining budget is below the floor a useful attempt needs. Contrast with a retry loop
// that counts attempts and knows nothing about the caller's deadline.
type DeadlineAwareRetrier struct {
	clock              *FakeClock
	perAttemptTimeout  int64
	backoffMs          int64
	minUsefulAttemptMs int64
}

func NewDeadlineAwareRetrier(clock *FakeClock, perAttemptTimeoutMs, backoffMs, minUsefulAttemptMs int64) *DeadlineAwareRetrier {
	return &DeadlineAwareRetrier{clock, perAttemptTimeoutMs, backoffMs, minUsefulAttemptMs}
}

func (r *DeadlineAwareRetrier) Call(service Service, ctx Context) RetryOutcome {
	start := r.clock.NowMs()
	var budgets []int64
	attempts := 0
	for {
		remaining := ctx.RemainingMs(r.clock)
		if remaining < r.minUsefulAttemptMs {
			return RetryOutcome{FailedFast, attempts, r.clock.NowMs() - start, budgets}
		}
		attemptBudget := min(r.perAttemptTimeout, remaining)
		budgets = append(budgets, attemptBudget)
		attempts++
		attempt := ctx.WithTimeout(r.clock, attemptBudget)
		_, err := service.Call(attempt)
		switch {
		case err == nil:
			return RetryOutcome{OK, attempts, r.clock.NowMs() - start, budgets}
		case errors.Is(err, ErrUnavailable):
			sleep := min(r.backoffMs, ctx.RemainingMs(r.clock)) // never sleep past the deadline
			r.clock.Advance(sleep)
		default: // deadline exceeded
			return RetryOutcome{FailedFast, attempts, r.clock.NowMs() - start, budgets}
		}
	}
}

// HedgeResult is the outcome of one hedged call.
type HedgeResult struct {
	LatencyMs    int64
	RequestsSent int
	Winner       string
}

// Hedge: send to the primary; if no answer within hedgeAfterMs, also send to a backup and take whichever answers first.
// Only legal for idempotent reads (Phase 5, Lesson 09 covers why).
func Hedge(primaryLatencyMs, backupLatencyMs, hedgeAfterMs int64) HedgeResult {
	if primaryLatencyMs <= hedgeAfterMs {
		return HedgeResult{primaryLatencyMs, 1, "primary"}
	}
	hedgedArrival := hedgeAfterMs + backupLatencyMs
	if primaryLatencyMs <= hedgedArrival {
		return HedgeResult{primaryLatencyMs, 2, "primary"}
	}
	return HedgeResult{hedgedArrival, 2, "backup"}
}

// SampleLatency draws a latency with a long tail: most calls are fast, a few percent hit a stall. Seeded, so reproducible.
func SampleLatency(rng *JavaRandom) int64 {
	if rng.NextInt(100) < 5 {
		return 600 + int64(rng.NextInt(400))
	}
	return 20 + int64(rng.NextInt(40))
}

func Percentile(sorted []int64, p float64) int64 {
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[max(0, min(len(sorted)-1, idx))]
}

type Study struct {
	P50, P99, P99Hedged int64
	ExtraLoadPercent    float64
}

func RunStudy(seed int64, samples int, hedgeAfterMs int64) Study {
	rng := NewJavaRandom(seed)
	plain := make([]int64, samples)
	hedged := make([]int64, samples)
	var extraRequests int64
	for i := 0; i < samples; i++ {
		primary := SampleLatency(rng)
		backup := SampleLatency(rng)
		plain[i] = primary
		r := Hedge(primary, backup, hedgeAfterMs)
		hedged[i] = r.LatencyMs
		extraRequests += int64(r.RequestsSent - 1)
	}
	sort.Slice(plain, func(i, j int) bool { return plain[i] < plain[j] })
	sort.Slice(hedged, func(i, j int) bool { return hedged[i] < hedged[j] })
	return Study{Percentile(plain, 0.50), Percentile(plain, 0.99), Percentile(hedged, 0.99), 100.0 * float64(extraRequests) / float64(samples)}
}

// BuildChain: checkout -> payments -> processor, where the processor charges a card.
func BuildChain(clock *FakeClock, log *EffectLog, propagate bool) Service {
	processor := NewRemoteService("processor", 400, "CHARGE 19.99 USD card 4242", nil, propagate, clock, log)
	payments := NewRemoteService("payments", 50, "", processor, propagate, clock, log)
	return NewRemoteService("checkout", 50, "", payments, propagate, clock, log)
}

func main() {
	fmt.Println("== A deadline is absolute; a timeout is relative ==")
	clock := NewFakeClock(1_000)
	ctx := Root("req-1").WithTimeout(clock, 500)
	fmt.Printf("now=%d deadline=%d remaining=%d\n", clock.NowMs(), ctx.Deadline.AtMs, ctx.RemainingMs(clock))
	clock.Advance(180)
	fmt.Printf("now=%d remaining=%d wire header=%s\n", clock.NowMs(), ctx.RemainingMs(clock), ctx.ToWireTimeout(clock))
	child := ctx.WithTimeout(clock, 1_000)
	fmt.Printf("child asked for 1000 ms, got %d ms (a child can only tighten)\n", child.RemainingMs(clock))
	clock.Advance(400)
	fmt.Printf("now=%d remaining=%d expired=%t\n", clock.NowMs(), ctx.RemainingMs(clock), ctx.Deadline.IsExpired(clock))

	fmt.Println()
	fmt.Println("== The downstream call that outlives its caller ==")
	c1 := NewFakeClock(0)
	log1 := &EffectLog{}
	o1 := NewClient(c1).Invoke(BuildChain(c1, log1, false), "pay-1", 500)
	fmt.Printf("no propagation : %s after %d ms; %s\n", o1.Status, o1.ElapsedMs, o1.Detail)
	for _, e := range log1.All() {
		fmt.Println("  side effect: " + e.String())
	}
	fmt.Println("  customer saw a timeout and will retry; the card was charged anyway.")

	c2 := NewFakeClock(0)
	log2 := &EffectLog{}
	o2 := NewClient(c2).Invoke(BuildChain(c2, log2, true), "pay-2", 500)
	fmt.Printf("propagation    : %s after %d ms; %s\n", o2.Status, o2.ElapsedMs, o2.Detail)
	fmt.Printf("  side effects recorded: %d\n", len(log2.All()))

	c3 := NewFakeClock(0)
	log3 := &EffectLog{}
	o3 := NewClient(c3).Invoke(BuildChain(c3, log3, true), "pay-3", 700)
	fmt.Printf("budget 700 ms  : %s after %d ms; %s\n", o3.Status, o3.ElapsedMs, o3.Detail)
	for _, e := range log3.All() {
		fmt.Println("  side effect: " + e.String())
	}

	fmt.Println()
	fmt.Println("== Retries live inside the deadline, not beside it ==")
	c4 := NewFakeClock(0)
	r4 := NewDeadlineAwareRetrier(c4, 300, 50, 50).Call(NewFlakyService(c4, 150, 2), Root("rt-1").WithTimeout(c4, 1_000))
	fmt.Printf("deadline 1000 ms: %s after %d attempts, %d ms, per-attempt budgets %s\n", r4.Status, r4.Attempts, r4.ElapsedMs, budgets(r4.AttemptTimeoutsMs))
	c5 := NewFakeClock(0)
	r5 := NewDeadlineAwareRetrier(c5, 300, 50, 50).Call(NewFlakyService(c5, 150, 2), Root("rt-2").WithTimeout(c5, 400))
	fmt.Printf("deadline  400 ms: %s after %d attempts, %d ms, per-attempt budgets %s (third attempt never started)\n", r5.Status, r5.Attempts, r5.ElapsedMs, budgets(r5.AttemptTimeoutsMs))

	fmt.Println()
	fmt.Println("== Hedged requests cut the tail for a few percent more load ==")
	h1 := Hedge(800, 40, 60)
	fmt.Printf("primary 800 ms, backup 40 ms, hedge after 60 ms -> %d ms from %s using %d requests\n", h1.LatencyMs, h1.Winner, h1.RequestsSent)
	h2 := Hedge(30, 40, 60)
	fmt.Printf("primary  30 ms                                  -> %d ms from %s using %d request\n", h2.LatencyMs, h2.Winner, h2.RequestsSent)
	s := RunStudy(42, 10_000, 60)
	fmt.Printf("10000 calls, seed 42: p50=%d ms p99=%d ms; hedged p99=%d ms; extra load=%.1f%%\n", s.P50, s.P99, s.P99Hedged, s.ExtraLoadPercent)
}

// budgets prints a slice like Java's List.toString: [300, 300, 300].
func budgets(ms []int64) string {
	parts := make([]string, len(ms))
	for i, m := range ms {
		parts[i] = strconv.FormatInt(m, 10)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
