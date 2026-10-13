// Lesson: phases/05-distributed-foundations/10-retries-backoff-and-jitter/docs/en.md
// Retries, Exponential Backoff, and Jitter
// Run:  go run .
// Test: go test ./...
// Sources: AWS Architecture Blog "Exponential Backoff and Jitter" (Brooker, 2015); Finagle RetryBudget docs;
// Google SRE Book ch. 22 "Addressing Cascading Failures". Go 1.22+, standard library only.
// Same design as the Java and Python versions: same xorshift64* generator, same seeds, same schedules.
package main

import (
	"container/heap"
	"errors"
	"fmt"
	"strings"
)

// Rng is a deterministic xorshift64* generator, bit-identical to the Java and Python versions.
// Never use it for anything security-sensitive; it exists so demos and tests are replayable.
type Rng struct{ state uint64 }

func NewRng(seed int64) *Rng {
	s := uint64(seed)
	if s == 0 {
		s = 0x9E3779B97F4A7C15
	}
	return &Rng{state: s}
}

// NextLong returns the next value as a signed 64-bit integer, matching Java's long.
func (r *Rng) NextLong() int64 {
	x := r.state
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	r.state = x
	return int64(x * 0x2545F4914F6CDD1D)
}

// NextBelow returns a uniform integer in [0, bound), using the unsigned value like Long.remainderUnsigned.
func (r *Rng) NextBelow(bound int64) int64 {
	if bound <= 0 {
		panic(fmt.Sprintf("bound must be positive: %d", bound))
	}
	return int64(uint64(r.NextLong()) % uint64(bound))
}

// Clock is what the retrier sleeps against. Tests and demos use FakeClock so no wall time passes.
type Clock interface {
	NowMs() int64
	Sleep(ms int64)
}

type FakeClock struct{ now int64 }

func NewFakeClock(startMs int64) *FakeClock { return &FakeClock{now: startMs} }
func (c *FakeClock) NowMs() int64          { return c.now }
func (c *FakeClock) Sleep(ms int64) {
	if ms < 0 {
		panic("cannot sleep a negative duration")
	}
	c.now += ms
}

// Jitter says how the random component of a delay is drawn. Names follow Brooker's 2015 post.
type Jitter int

const (
	None         Jitter = iota // delay = exp(attempt): everyone who failed together retries together
	Full                       // delay = uniform[0, exp]: widest spread, lowest peak
	Equal                      // delay = exp/2 + uniform[0, exp/2]: never sooner than half the backoff
	Decorrelated               // delay = min(cap, uniform[base, 3 * previous]): spread grows with history
)

var jitterNames = []string{"NONE", "FULL", "EQUAL", "DECORRELATED"}

func (j Jitter) String() string { return jitterNames[j] }

// Outcome is what a call to the payment processor came back with. Retryability is decided once, here.
type Outcome int

const (
	OK              Outcome = iota
	Timeout                 // no response: the processor may or may not have done the work
	ConnectionReset         // transport failed before or after the request was read
	RateLimited             // HTTP 429: slow down, so retry only with backoff
	ServerError             // HTTP 503/502: transient on the processor side
	BadRequest              // HTTP 400: our payload is wrong; the same bytes fail the same way
	Unauthorized            // HTTP 401: our credentials are wrong; retrying is brute force
	CardDeclined            // a valid business answer, not an error
)

func (o Outcome) Retryable() bool {
	switch o {
	case Timeout, ConnectionReset, RateLimited, ServerError:
		return true
	}
	return false
}

// RetryPolicy: exp(attempt) = min(cap, base * 2^attempt); jitter decides how much of that interval is random.
type RetryPolicy struct {
	BaseMs      int64
	CapMs       int64
	MaxAttempts int
	Jitter      Jitter
}

var ErrInvalidPolicy = errors.New("invalid retry policy")

func NewRetryPolicy(baseMs, capMs int64, maxAttempts int, jitter Jitter) (RetryPolicy, error) {
	if baseMs <= 0 {
		return RetryPolicy{}, fmt.Errorf("%w: baseMs must be positive", ErrInvalidPolicy)
	}
	if capMs < baseMs {
		return RetryPolicy{}, fmt.Errorf("%w: capMs must be >= baseMs", ErrInvalidPolicy)
	}
	if maxAttempts < 1 {
		return RetryPolicy{}, fmt.Errorf("%w: maxAttempts must be >= 1", ErrInvalidPolicy)
	}
	return RetryPolicy{baseMs, capMs, maxAttempts, jitter}, nil
}

// mustPolicy is for demos and tests where the arguments are literals.
func mustPolicy(baseMs, capMs int64, maxAttempts int, jitter Jitter) RetryPolicy {
	p, err := NewRetryPolicy(baseMs, capMs, maxAttempts, jitter)
	if err != nil {
		panic(err)
	}
	return p
}

// Exponential is pure exponential backoff with the cap applied; doubling stops once the cap is reached.
func (p RetryPolicy) Exponential(attempt int) int64 {
	delay := p.BaseMs
	for i := 0; i < attempt && delay < p.CapMs; i++ {
		delay = min(p.CapMs, delay*2)
	}
	return min(p.CapMs, delay)
}

// Delay before retry number attempt (0 = first retry). Only Decorrelated reads previousMs.
func (p RetryPolicy) Delay(attempt int, previousMs int64, rng *Rng) int64 {
	exp := p.Exponential(attempt)
	switch p.Jitter {
	case None:
		return exp
	case Full:
		return rng.NextBelow(exp + 1)
	case Equal:
		return exp/2 + rng.NextBelow(exp/2+1)
	default:
		upper := min(p.CapMs, max(p.BaseMs, previousMs)*3)
		return p.BaseMs + rng.NextBelow(upper-p.BaseMs+1)
	}
}

// Schedule is every delay this policy would produce if every attempt failed: MaxAttempts - 1 entries.
func (p RetryPolicy) Schedule(rng *Rng) []int64 {
	delays := make([]int64, 0, p.MaxAttempts-1)
	previous := p.BaseMs
	for attempt := 0; attempt < p.MaxAttempts-1; attempt++ {
		d := p.Delay(attempt, previous, rng)
		delays = append(delays, d)
		previous = d
	}
	return delays
}

// RetryBudget is a Finagle-style budget: every first attempt deposits percentCanRetry% of a token,
// every retry withdraws a whole token. Integer millitokens, so no float drift.
type RetryBudget struct {
	depositMillis int64
	maxBalance    int64
	balance       int64
}

const token = 1000

func NewRetryBudget(percentCanRetry, maxTokens int) (*RetryBudget, error) {
	if percentCanRetry < 0 || percentCanRetry > 100 {
		return nil, errors.New("percent must be 0..100")
	}
	if maxTokens < 1 {
		return nil, errors.New("maxTokens must be >= 1")
	}
	return &RetryBudget{depositMillis: int64(token * percentCanRetry / 100), maxBalance: int64(token * maxTokens)}, nil
}

func (b *RetryBudget) Deposit() { b.balance = min(b.maxBalance, b.balance+b.depositMillis) }

func (b *RetryBudget) TryWithdraw() bool {
	if b.balance < token {
		return false
	}
	b.balance -= token
	return true
}

func (b *RetryBudget) Tokens() int64 { return b.balance / token }

type StopReason int

const (
	Succeeded StopReason = iota
	NonRetryable
	AttemptsExhausted
	BudgetExhausted
)

var stopNames = []string{"SUCCEEDED", "NON_RETRYABLE", "ATTEMPTS_EXHAUSTED", "BUDGET_EXHAUSTED"}

func (s StopReason) String() string { return stopNames[s] }

type RetryResult struct {
	FinalOutcome Outcome
	Attempts     int
	TotalDelayMs int64
	StopReason   StopReason
	Delays       []int64
}

func (r RetryResult) Succeeded() bool { return r.StopReason == Succeeded }

// Operation is the thing being retried: given the attempt number (0 = first try) it returns an Outcome.
type Operation func(attempt int) Outcome

// Retrier runs an Operation under a policy, an optional budget (nil = none), an injected clock, and a seeded Rng.
type Retrier struct {
	Policy RetryPolicy
	Budget *RetryBudget
	Clock  Clock
	Rng    *Rng
}

func (r *Retrier) Execute(op Operation) RetryResult {
	if r.Budget != nil {
		r.Budget.Deposit()
	}
	delays := []int64{}
	var total int64
	previous := r.Policy.BaseMs
	outcome := Timeout
	for attempt := 0; attempt < r.Policy.MaxAttempts; attempt++ {
		outcome = op(attempt)
		if outcome == OK {
			return RetryResult{outcome, attempt + 1, total, Succeeded, delays}
		}
		if !outcome.Retryable() {
			return RetryResult{outcome, attempt + 1, total, NonRetryable, delays}
		}
		if attempt == r.Policy.MaxAttempts-1 {
			break
		}
		if r.Budget != nil && !r.Budget.TryWithdraw() {
			return RetryResult{outcome, attempt + 1, total, BudgetExhausted, delays}
		}
		d := r.Policy.Delay(attempt, previous, r.Rng)
		r.Clock.Sleep(d)
		delays = append(delays, d)
		total += d
		previous = d
	}
	return RetryResult{outcome, r.Policy.MaxAttempts, total, AttemptsExhausted, delays}
}

// StormReport is what the processor saw during one storm: attempts per bucket, retries per bucket, outcomes.
type StormReport struct {
	LoadPerBucket    []int64
	RetriesPerBucket []int64
	Succeeded        int
	GaveUp           int
	StillWaiting     int
	TotalAttempts    int64
	BucketMs         int64
}

func (s StormReport) PeakLoad() int64 {
	var peak int64
	for _, l := range s.LoadPerBucket {
		peak = max(peak, l)
	}
	return peak
}

// PeakLoadFrom is the highest load in any bucket starting at or after fromMs: what the recovered processor sees.
func (s StormReport) PeakLoadFrom(fromMs int64) int64 {
	var peak int64
	for i, l := range s.LoadPerBucket {
		if int64(i)*s.BucketMs >= fromMs {
			peak = max(peak, l)
		}
	}
	return peak
}

func (s StormReport) BusyBuckets() int {
	n := 0
	for _, l := range s.LoadPerBucket {
		if l > 0 {
			n++
		}
	}
	return n
}

func (s StormReport) Histogram(scale int64) string {
	var sb strings.Builder
	for i, l := range s.LoadPerBucket {
		if l == 0 {
			continue
		}
		bars := (l + scale - 1) / scale
		fmt.Fprintf(&sb, "%5d ms | %4d | %s\n", int64(i)*s.BucketMs, l, strings.Repeat("#", int(bars)))
	}
	return sb.String()
}

// event is one scheduled attempt; the heap orders by (at, client) so the RNG draw order is fixed.
type event struct {
	at     int64
	client int
}

type eventHeap []event

func (h eventHeap) Len() int { return len(h) }
func (h eventHeap) Less(i, j int) bool {
	if h[i].at != h[j].at {
		return h[i].at < h[j].at
	}
	return h[i].client < h[j].client
}
func (h eventHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *eventHeap) Push(x any)         { *h = append(*h, x.(event)) }
func (h *eventHeap) Pop() any           { old := *h; e := old[len(old)-1]; *h = old[:len(old)-1]; return e }

// RunStorm is a retry storm in virtual time as a discrete-event simulation. N clients all send a payment at
// t=0; the processor is unreachable until outageMs, then serves capacityPerBucket calls per bucket. Replayable
// from its seed in all three languages.
func RunStorm(clients int, capacityPerBucket, bucketMs, outageMs int64, horizonBuckets int, policy RetryPolicy, seed int64) StormReport {
	rng := NewRng(seed)
	queue := make(eventHeap, 0, clients)
	for c := 0; c < clients; c++ {
		queue = append(queue, event{0, c})
	}
	heap.Init(&queue)
	load := make([]int64, horizonBuckets)
	retries := make([]int64, horizonBuckets)
	served := make([]int64, horizonBuckets)
	previousDelay := make([]int64, clients)
	attemptsMade := make([]int, clients)
	for c := range previousDelay {
		previousDelay[c] = policy.BaseMs
	}
	horizonMs := int64(horizonBuckets) * bucketMs
	succeeded, gaveUp := 0, 0
	var totalAttempts int64

	for queue.Len() > 0 && queue[0].at < horizonMs {
		e := heap.Pop(&queue).(event)
		c := e.client
		bucket := int(e.at / bucketMs)
		load[bucket]++
		if attemptsMade[c] > 0 {
			retries[bucket]++
		}
		attemptsMade[c]++
		totalAttempts++
		healthy := e.at >= outageMs
		if healthy && served[bucket] < capacityPerBucket {
			served[bucket]++
			succeeded++
			continue
		}
		if attemptsMade[c] >= policy.MaxAttempts {
			gaveUp++
			continue
		}
		d := policy.Delay(attemptsMade[c]-1, previousDelay[c], rng)
		previousDelay[c] = d
		heap.Push(&queue, event{e.at + max(d, 1), c}) // a zero delay still lands strictly later
	}
	return StormReport{load, retries, succeeded, gaveUp, queue.Len(), totalAttempts, bucketMs}
}

func main() {
	fmt.Println("== The schedule: base 100 ms, cap 10 s, 6 attempts, seed 42 ==")
	for _, j := range []Jitter{None, Full, Equal, Decorrelated} {
		fmt.Printf("%-13s %v\n", j, mustPolicy(100, 10_000, 6, j).Schedule(NewRng(42)))
	}
	fmt.Printf("%-13s %v  (cap reached at attempt 4)\n", "NONE, cap 1 s", mustPolicy(100, 1_000, 8, None).Schedule(NewRng(42)))

	fmt.Println("\n== One payment, processor flapping: TIMEOUT, TIMEOUT, OK ==")
	clock := NewFakeClock(0)
	retrier := &Retrier{mustPolicy(100, 10_000, 5, None), nil, clock, NewRng(7)}
	script := []Outcome{Timeout, Timeout, OK}
	r := retrier.Execute(func(attempt int) Outcome { return script[min(attempt, len(script)-1)] })
	fmt.Printf("result=%v attempts=%d waited=%d ms delays=%v clock=%d ms\n", r.StopReason, r.Attempts, r.TotalDelayMs, r.Delays, clock.NowMs())

	fmt.Println("\n== Non-retryable: the issuer declined; a retry would be a second authorization ==")
	declined := retrier.Execute(func(int) Outcome { return CardDeclined })
	fmt.Printf("result=%v attempts=%d waited=%d ms\n", declined.StopReason, declined.Attempts, declined.TotalDelayMs)

	fmt.Println("\n== Retry budget: 20% of organic traffic may be retries, max 10 tokens banked ==")
	budget, _ := NewRetryBudget(20, 10)
	budgeted := &Retrier{mustPolicy(100, 1_000, 3, None), budget, NewFakeClock(0), NewRng(1)}
	for i := 0; i < 50; i++ {
		budgeted.Execute(func(int) Outcome { return OK }) // healthy traffic banks tokens
	}
	fmt.Printf("after 50 healthy requests: %d tokens banked\n", budget.Tokens())
	for i := 1; i <= 8; i++ {
		rr := budgeted.Execute(func(int) Outcome { return ServerError })
		fmt.Printf("outage request %d: attempts=%d stop=%-18v tokens left=%d\n", i, rr.Attempts, rr.StopReason, budget.Tokens())
	}

	fmt.Println("\n== Retry storm: 1000 clients, processor down 1 s, then 200 calls per 100 ms bucket, 8 attempts ==")
	for _, j := range []Jitter{None, Full, Equal, Decorrelated} {
		report := RunStorm(1000, 200, 100, 1_000, 60, mustPolicy(100, 5_000, 8, j), 2024)
		fmt.Printf("\n-- %v: peak after recovery=%d/bucket busy buckets=%d succeeded=%d gave up=%d waiting=%d attempts=%d\n",
			j, report.PeakLoadFrom(1_000), report.BusyBuckets(), report.Succeeded, report.GaveUp, report.StillWaiting, report.TotalAttempts)
		if j == None || j == Full {
			fmt.Print(report.Histogram(25))
		}
	}
}
