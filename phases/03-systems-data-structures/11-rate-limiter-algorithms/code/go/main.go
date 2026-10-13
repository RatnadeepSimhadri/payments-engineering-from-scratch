// Lesson: phases/03-systems-data-structures/11-rate-limiter-algorithms/docs/en.md
// Rate Limiter Algorithms: Token Bucket, Leaky Bucket, Sliding Window
// Run:  go run .
// Test: go test ./...
// Sources: RFC 6585 (429 Too Many Requests); RFC 9110 section 10.2.3 (Retry-After); ATM Forum TM 4.0 / ITU-T I.371
//          (the Generic Cell Rate Algorithm, which the leaky bucket here is); Cloudflare (2017) on the sliding window counter.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"sort"
)

// Scale: tokens and water are tracked in thousandths so refill per millisecond is exact.
const Scale int64 = 1000

// ErrBadParameter is returned by every constructor for a non-positive or otherwise invalid setting.
var ErrBadParameter = errors.New("bad parameter")

// ----- clock ---------------------------------------------------------------------------------------

// Clock is something that tells the time in milliseconds. Injected so tests never sleep.
type Clock interface {
	NowMillis() int64
}

// ManualClock advances only when told to.
type ManualClock struct{ now int64 }

// NewManualClock starts at startMillis.
func NewManualClock(startMillis int64) *ManualClock { return &ManualClock{startMillis} }

// NowMillis implements Clock.
func (c *ManualClock) NowMillis() int64 { return c.now }

// Advance moves the clock forward; it never goes backwards.
func (c *ManualClock) Advance(millis int64) {
	if millis < 0 {
		panic("clock cannot go backwards")
	}
	c.now += millis
}

// Set jumps to an absolute time, which must not be earlier than now.
func (c *ManualClock) Set(millis int64) {
	if millis < c.now {
		panic("clock cannot go backwards")
	}
	c.now = millis
}

// ----- the decision every limiter returns ----------------------------------------------------------

// Decision: Allowed means serve the request. RetryAfterMillis, when denied, is the earliest time a
// retry can succeed (becomes the Retry-After header, rounded up to whole seconds). DelayMillis, when
// allowed by a shaper, is how long the request waits before it is processed.
type Decision struct {
	Allowed          bool
	RetryAfterMillis int64
	DelayMillis      int64
}

// Allow is an immediate admission.
func Allow() Decision { return Decision{Allowed: true} }

// AllowAfter is an admission that is processed delayMillis from now.
func AllowAfter(delayMillis int64) Decision { return Decision{Allowed: true, DelayMillis: delayMillis} }

// Deny is a rejection; the wait is never reported as zero.
func Deny(retryAfterMillis int64) Decision {
	if retryAfterMillis < 1 {
		retryAfterMillis = 1
	}
	return Decision{RetryAfterMillis: retryAfterMillis}
}

// RetryAfterHeader is delta-seconds; always round up so the client never retries early.
func (d Decision) RetryAfterHeader() int64 { return (d.RetryAfterMillis + 999) / 1000 }

// RateLimiter is what all five algorithms implement.
type RateLimiter interface {
	Name() string
	TryAcquire() Decision
}

func requirePositive(name string, value int64) error {
	if value <= 0 {
		return fmt.Errorf("%w: %s must be positive: %d", ErrBadParameter, name, value)
	}
	return nil
}

func ceilDiv(a, b int64) int64 { return (a + b - 1) / b }

// ----- 1. token bucket -----------------------------------------------------------------------------

// TokenBucket: capacity tokens, refilled continuously at RefillPerSecond. A request takes one token.
// Bursts up to capacity are admitted instantly; the long-run rate is RefillPerSecond.
type TokenBucket struct {
	Capacity        int64
	RefillPerSecond int64
	clock           Clock
	tokens          int64 // in thousandths
	lastRefill      int64
}

// NewTokenBucket starts full.
func NewTokenBucket(capacity, refillPerSecond int64, clock Clock) (*TokenBucket, error) {
	if err := requirePositive("capacity", capacity); err != nil {
		return nil, err
	}
	if err := requirePositive("refillPerSecond", refillPerSecond); err != nil {
		return nil, err
	}
	return &TokenBucket{Capacity: capacity, RefillPerSecond: refillPerSecond, clock: clock, tokens: capacity * Scale, lastRefill: clock.NowMillis()}, nil
}

func (b *TokenBucket) refill() {
	now := b.clock.NowMillis()
	if elapsed := now - b.lastRefill; elapsed > 0 {
		// refillPerSecond tokens/s == refillPerSecond thousandths per ms: exact integer math
		b.tokens = min(b.Capacity*Scale, b.tokens+elapsed*b.RefillPerSecond)
		b.lastRefill = now
	}
}

// Tokens is the current fill, for display.
func (b *TokenBucket) Tokens() float64 {
	b.refill()
	return float64(b.tokens) / float64(Scale)
}

// Name implements RateLimiter.
func (b *TokenBucket) Name() string { return "token bucket" }

// TryAcquire implements RateLimiter.
func (b *TokenBucket) TryAcquire() Decision {
	b.refill()
	if b.tokens >= Scale {
		b.tokens -= Scale
		return Allow()
	}
	return Deny(ceilDiv(Scale-b.tokens, b.RefillPerSecond))
}

// ----- 2. leaky bucket (a shaper: constant outflow, bounded queue) --------------------------------

// LeakyBucket: requests leave at exactly leakPerSecond; up to QueueCapacity may wait. Implemented
// with virtual scheduling (the GCRA): nextSlot is the time the next request will be released.
type LeakyBucket struct {
	QueueCapacity  int64
	IntervalMillis int64
	clock          Clock
	nextSlot       int64
}

// NewLeakyBucket requires a leak rate that divides a second so the interval is a whole millisecond.
func NewLeakyBucket(queueCapacity, leakPerSecond int64, clock Clock) (*LeakyBucket, error) {
	if err := requirePositive("queueCapacity", queueCapacity); err != nil {
		return nil, err
	}
	if err := requirePositive("leakPerSecond", leakPerSecond); err != nil {
		return nil, err
	}
	if 1000%leakPerSecond != 0 {
		return nil, fmt.Errorf("%w: leakPerSecond must divide 1000", ErrBadParameter)
	}
	return &LeakyBucket{QueueCapacity: queueCapacity, IntervalMillis: 1000 / leakPerSecond, clock: clock, nextSlot: clock.NowMillis()}, nil
}

// Queued is how many admitted requests are still waiting.
func (b *LeakyBucket) Queued() int64 {
	return max(0, ceilDiv(b.nextSlot-b.clock.NowMillis(), b.IntervalMillis))
}

// Name implements RateLimiter.
func (b *LeakyBucket) Name() string { return "leaky bucket" }

// TryAcquire implements RateLimiter.
func (b *LeakyBucket) TryAcquire() Decision {
	now := b.clock.NowMillis()
	slot := max(now, b.nextSlot)
	delay := slot - now
	if delay >= b.QueueCapacity*b.IntervalMillis {
		// the queue is full: the request would wait longer than queueCapacity slots
		return Deny(delay - b.QueueCapacity*b.IntervalMillis + b.IntervalMillis)
	}
	b.nextSlot = slot + b.IntervalMillis
	return AllowAfter(delay)
}

// ----- 3. fixed window -----------------------------------------------------------------------------

// FixedWindow: Limit requests per window aligned to multiples of WindowMillis. Cheap; admits up to
// 2x limit across a window boundary.
type FixedWindow struct {
	Limit        int64
	WindowMillis int64
	clock        Clock
	windowStart  int64
	count        int64
}

// NewFixedWindow validates its parameters.
func NewFixedWindow(limit, windowMillis int64, clock Clock) (*FixedWindow, error) {
	if err := requirePositive("limit", limit); err != nil {
		return nil, err
	}
	if err := requirePositive("windowMillis", windowMillis); err != nil {
		return nil, err
	}
	return &FixedWindow{Limit: limit, WindowMillis: windowMillis, clock: clock, windowStart: -1}, nil
}

// Name implements RateLimiter.
func (w *FixedWindow) Name() string { return "fixed window" }

// TryAcquire implements RateLimiter.
func (w *FixedWindow) TryAcquire() Decision {
	now := w.clock.NowMillis()
	start := now - now%w.WindowMillis
	if start != w.windowStart {
		w.windowStart = start
		w.count = 0
	}
	if w.count < w.Limit {
		w.count++
		return Allow()
	}
	return Deny(start + w.WindowMillis - now)
}

// ----- 4. sliding window log -----------------------------------------------------------------------

// SlidingWindowLog is exact: it keeps the timestamp of every admitted request in the last
// WindowMillis. O(limit) memory per key.
type SlidingWindowLog struct {
	Limit        int64
	WindowMillis int64
	clock        Clock
	log          []int64
}

// NewSlidingWindowLog validates its parameters.
func NewSlidingWindowLog(limit, windowMillis int64, clock Clock) (*SlidingWindowLog, error) {
	if err := requirePositive("limit", limit); err != nil {
		return nil, err
	}
	if err := requirePositive("windowMillis", windowMillis); err != nil {
		return nil, err
	}
	return &SlidingWindowLog{Limit: limit, WindowMillis: windowMillis, clock: clock}, nil
}

// Len is the number of timestamps retained.
func (l *SlidingWindowLog) Len() int { return len(l.log) }

// Name implements RateLimiter.
func (l *SlidingWindowLog) Name() string { return "sliding log" }

// TryAcquire implements RateLimiter.
func (l *SlidingWindowLog) TryAcquire() Decision {
	now := l.clock.NowMillis()
	cutoff := now - l.WindowMillis
	drop := 0
	for drop < len(l.log) && l.log[drop] <= cutoff {
		drop++
	}
	l.log = l.log[drop:]
	if int64(len(l.log)) < l.Limit {
		l.log = append(l.log, now)
		return Allow()
	}
	return Deny(l.log[0] + l.WindowMillis - now)
}

// ----- 5. sliding window counter -------------------------------------------------------------------

// SlidingWindowCounter is approximate: two counters (previous and current fixed window), with the
// previous one weighted by how much of it still overlaps the sliding window. O(1) memory.
type SlidingWindowCounter struct {
	Limit        int64
	WindowMillis int64
	clock        Clock
	windowStart  int64
	current      int64
	previous     int64
}

// NewSlidingWindowCounter validates its parameters.
func NewSlidingWindowCounter(limit, windowMillis int64, clock Clock) (*SlidingWindowCounter, error) {
	if err := requirePositive("limit", limit); err != nil {
		return nil, err
	}
	if err := requirePositive("windowMillis", windowMillis); err != nil {
		return nil, err
	}
	return &SlidingWindowCounter{Limit: limit, WindowMillis: windowMillis, clock: clock, windowStart: -1}, nil
}

// roll moves the windows forward to now and returns the elapsed time inside the current window.
func (c *SlidingWindowCounter) roll(now int64) int64 {
	start := now - now%c.WindowMillis
	if start != c.windowStart {
		if start == c.windowStart+c.WindowMillis {
			c.previous = c.current // the window just before this one
		} else {
			c.previous = 0 // a gap: nothing overlaps
		}
		c.current = 0
		c.windowStart = start
	}
	return now - start
}

// WeightedCountScaled is (previous * remainingOverlap + current * window): the weighted count times
// WindowMillis, kept scaled so the comparison is exact integer arithmetic.
func (c *SlidingWindowCounter) WeightedCountScaled() int64 {
	elapsed := c.roll(c.clock.NowMillis())
	return c.previous*(c.WindowMillis-elapsed) + c.current*c.WindowMillis
}

// Name implements RateLimiter.
func (c *SlidingWindowCounter) Name() string { return "sliding counter" }

// TryAcquire implements RateLimiter.
func (c *SlidingWindowCounter) TryAcquire() Decision {
	now := c.clock.NowMillis()
	elapsed := c.roll(now)
	w := c.WindowMillis
	scaled := c.previous*(w-elapsed) + c.current*w
	if scaled+w <= c.Limit*w {
		c.current++
		return Allow()
	}
	if c.current >= c.Limit || c.previous == 0 {
		return Deny(w - elapsed)
	}
	// smallest e' with previous*(w - e') + current*w + w <= limit*w
	needed := w - ((c.Limit-1-c.current)*w)/c.previous
	return Deny(max(1, needed-elapsed))
}

// ----- a trace: a retry storm against a 10 rps limit --------------------------------------------

// RetryStormTrace: timestamps in ms. 3 s at a steady 10 rps (every 100 ms), then at t=3000 a burst of
// 40 retries within 20 ms, then steady 10 rps again until t=6000.
func RetryStormTrace() []int64 {
	var trace []int64
	for t := int64(0); t < 3000; t += 100 {
		trace = append(trace, t)
	}
	for i := 0; i < 40; i++ {
		trace = append(trace, 3000+int64(i%20))
	}
	for t := int64(3100); t < 6000; t += 100 {
		trace = append(trace, t)
	}
	sort.Slice(trace, func(i, j int) bool { return trace[i] < trace[j] })
	return trace
}

// Limiter pairs a limiter with the clock that drives it.
type Limiter struct {
	Limiter RateLimiter
	Clock   *ManualClock
}

// MakeLimiters returns five limiters configured for the same nominal 10 requests per second, each with its own clock.
func MakeLimiters() []Limiter {
	var out []Limiter
	c := NewManualClock(0)
	tb, _ := NewTokenBucket(10, 10, c)
	out = append(out, Limiter{tb, c})
	c = NewManualClock(0)
	lb, _ := NewLeakyBucket(10, 10, c)
	out = append(out, Limiter{lb, c})
	c = NewManualClock(0)
	fw, _ := NewFixedWindow(10, 1000, c)
	out = append(out, Limiter{fw, c})
	c = NewManualClock(0)
	sl, _ := NewSlidingWindowLog(10, 1000, c)
	out = append(out, Limiter{sl, c})
	c = NewManualClock(0)
	sc, _ := NewSlidingWindowCounter(10, 1000, c)
	out = append(out, Limiter{sc, c})
	return out
}

// ReplaySummary tallies what a limiter did on a trace.
type ReplaySummary struct {
	Name                string
	Allowed, Denied     int
	AllowedInBurst      int
	MaxInAnySecond      int
	MaxRetryAfterMillis int64
	MaxDelayMillis      int64
}

// Replay drives the injected clock through the trace and tallies what the limiter did.
func Replay(limiter RateLimiter, clock *ManualClock, trace []int64, burstStart, burstEnd int64) ReplaySummary {
	var allowedTimes []int64
	s := ReplaySummary{Name: limiter.Name()}
	for _, t := range trace {
		clock.Set(t)
		d := limiter.TryAcquire()
		if d.Allowed {
			allowedTimes = append(allowedTimes, t+d.DelayMillis)
			s.MaxDelayMillis = max(s.MaxDelayMillis, d.DelayMillis)
			if t >= burstStart && t < burstEnd {
				s.AllowedInBurst++
			}
		} else {
			s.Denied++
			s.MaxRetryAfterMillis = max(s.MaxRetryAfterMillis, d.RetryAfterMillis)
		}
	}
	s.Allowed = len(allowedTimes)
	s.MaxInAnySecond = MaxInWindow(allowedTimes, 1000)
	return s
}

// MaxInWindow is the largest number of admitted requests whose (processing) times fall in any
// half-open window of width windowMillis.
func MaxInWindow(times []int64, windowMillis int64) int {
	sorted := append([]int64(nil), times...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	best, lo := 0, 0
	for hi := range sorted {
		for sorted[hi]-sorted[lo] >= windowMillis {
			lo++
		}
		best = max(best, hi-lo+1)
	}
	return best
}

func main() {
	fmt.Println("== Token bucket: burst now, steady later ==")
	clock := NewManualClock(0)
	tb, _ := NewTokenBucket(5, 2, clock)
	var burst []bool
	for i := 0; i < 7; i++ {
		burst = append(burst, tb.TryAcquire().Allowed)
	}
	fmt.Printf("7 requests at t=0 against capacity 5: %v\n", burst)
	d := tb.TryAcquire()
	fmt.Printf("denied: retry after %d ms -> Retry-After: %d\n", d.RetryAfterMillis, d.RetryAfterHeader())
	clock.Advance(500)
	fmt.Printf("t=500ms: tokens = %.1f, request allowed = %v\n", tb.Tokens(), tb.TryAcquire().Allowed)

	fmt.Println("\n== Leaky bucket: admits, but spaces the output ==")
	clock = NewManualClock(0)
	lb, _ := NewLeakyBucket(3, 10, clock)
	for i := 0; i < 5; i++ {
		r := lb.TryAcquire()
		if r.Allowed {
			fmt.Printf("request %d at t=0: queued, runs at +%d ms\n", i, r.DelayMillis)
		} else {
			fmt.Printf("request %d at t=0: rejected, Retry-After %d ms\n", i, r.RetryAfterMillis)
		}
	}

	fmt.Println("\n== Fixed window: the 2x problem at the boundary ==")
	clock = NewManualClock(0)
	fw, _ := NewFixedWindow(10, 1000, clock)
	log, _ := NewSlidingWindowLog(10, 1000, clock)
	admittedFw, admittedLog := 0, 0
	for t := int64(950); t < 1050; t += 5 {
		clock.Set(t)
		if fw.TryAcquire().Allowed {
			admittedFw++
		}
		if log.TryAcquire().Allowed {
			admittedLog++
		}
	}
	fmt.Printf("20 requests between t=950 and t=1050 ms, limit 10/s: fixed window admitted %d, sliding log admitted %d\n", admittedFw, admittedLog)

	fmt.Println("\n== Sliding counter: weighting the previous window ==")
	clock = NewManualClock(0)
	sc, _ := NewSlidingWindowCounter(10, 1000, clock)
	for t := int64(0); t < 1000; t += 100 {
		clock.Set(t)
		sc.TryAcquire()
	}
	clock.Set(1250)
	fmt.Printf("10 requests in window 0; at t=1250 weighted count = %.2f, next request allowed = %v\n", float64(sc.WeightedCountScaled())/1000, sc.TryAcquire().Allowed)
	clock.Set(1500)
	fmt.Printf("at t=1500 weighted count = %.2f, next request allowed = %v\n", float64(sc.WeightedCountScaled())/1000, sc.TryAcquire().Allowed)

	fmt.Println("\n== The same retry storm through all five (limit 10 rps, burst of 40 at t=3.0 s) ==")
	trace := RetryStormTrace()
	fmt.Printf("%d requests over 6 s: 59 steady plus 40 in the burst; a limiter at exactly 10 rps admits 59 + 10\n", len(trace))
	fmt.Printf("%-16s %7s %6s %8s %6s %15s %9s\n", "limiter", "allowed", "denied", "in burst", "max/1s", "max Retry-After", "max delay")
	for _, l := range MakeLimiters() {
		s := Replay(l.Limiter, l.Clock, trace, 3000, 3020)
		fmt.Printf("%-16s %7d %6d %8d %6d %12d ms %6d ms\n", s.Name, s.Allowed, s.Denied, s.AllowedInBurst, s.MaxInAnySecond, s.MaxRetryAfterMillis, s.MaxDelayMillis)
	}
}
