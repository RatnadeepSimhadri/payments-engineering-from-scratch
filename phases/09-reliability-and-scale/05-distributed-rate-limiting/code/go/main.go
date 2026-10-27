// Lesson: phases/09-reliability-and-scale/05-distributed-rate-limiting/docs/en.md
// Distributed Rate Limiting: token buckets across N nodes, three coordination strategies measured under seeded load
// Run:  go run .
// Test: go test ./...
// Sources: RFC 6585 (429 Too Many Requests); IETF draft-ietf-httpapi-ratelimit-headers; Redis INCR/EXPIRE docs;
//
//	Stripe "Scaling your API with rate limiters" (2017). Go 1.22+, standard library only.
package main

import (
	"errors"
	"fmt"
	"math"
)

// Clock is the injected time source so every simulation is deterministic and no test ever sleeps.
type Clock interface {
	NowMillis() int64
}

// FakeClock is a clock the demo and tests move by hand.
type FakeClock struct{ now int64 }

func NewFakeClock(startMillis int64) *FakeClock { return &FakeClock{now: startMillis} }

func (c *FakeClock) NowMillis() int64 { return c.now }

// Advance moves time forward; a negative step is a programming error and panics.
func (c *FakeClock) Advance(millis int64) {
	if millis < 0 {
		panic("time does not go backwards")
	}
	c.now += millis
}

// ErrOverflow is returned (or panicked, inside the hot path) when an int64 would wrap. Java's Math.multiplyExact.
var ErrOverflow = errors.New("int64 overflow")

func mulChecked(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	product := a * b
	if product/b != a || (a == -1 && b == math.MinInt64) || (b == -1 && a == math.MinInt64) {
		return 0, ErrOverflow
	}
	return product, nil
}

func addChecked(a, b int64) (int64, error) {
	sum := a + b
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum >= 0) {
		return 0, ErrOverflow
	}
	return sum, nil
}

// mustInt unwraps a checked arithmetic result where overflow would be a bug, not a condition to report.
func mustInt(v int64, err error) int64 {
	if err != nil {
		panic(err)
	}
	return v
}

// TokenBucket is a token bucket with integer arithmetic. Tokens are stored in milli-tokens so a refill rate of
// 7 tokens/second accrues exactly 7 milli-tokens per millisecond with no floating point drift.
type TokenBucket struct {
	capacityMilli    int64
	ratePerSecond    int64 // tokens per second == milli-tokens per millisecond
	tokensMilli      int64
	lastRefillMillis int64
}

// NewTokenBucket starts full: the burst allowance is immediate.
func NewTokenBucket(capacity, ratePerSecond, nowMillis int64) (*TokenBucket, error) {
	if capacity <= 0 {
		return nil, errors.New("capacity must be positive")
	}
	if ratePerSecond < 0 {
		return nil, errors.New("rate must be non-negative")
	}
	capacityMilli, err := mulChecked(capacity, 1000)
	if err != nil {
		return nil, err
	}
	return &TokenBucket{capacityMilli: capacityMilli, ratePerSecond: ratePerSecond, tokensMilli: capacityMilli, lastRefillMillis: nowMillis}, nil
}

// Refill is lazy: add rate * elapsed, clamp to capacity. Called before every decision.
func (b *TokenBucket) Refill(nowMillis int64) {
	if nowMillis <= b.lastRefillMillis {
		return
	}
	elapsed := nowMillis - b.lastRefillMillis
	added := mustInt(mulChecked(elapsed, b.ratePerSecond))
	b.tokensMilli = min(b.capacityMilli, mustInt(addChecked(b.tokensMilli, added)))
	b.lastRefillMillis = nowMillis
}

func (b *TokenBucket) TryAcquire(tokens, nowMillis int64) bool {
	b.Refill(nowMillis)
	needed := mustInt(mulChecked(tokens, 1000))
	if b.tokensMilli >= needed {
		b.tokensMilli -= needed
		return true
	}
	return false
}

// AvailableTokens is the whole tokens available at nowMillis after lazy refill (floor). Exposed for tests and the demo.
func (b *TokenBucket) AvailableTokens(nowMillis int64) int64 {
	b.Refill(nowMillis)
	return b.tokensMilli / 1000
}

func (b *TokenBucket) RatePerSecond() int64 { return b.ratePerSecond }

// SetRatePerSecond is used by the periodic-sync strategy to hand a node a new share of the global rate.
func (b *TokenBucket) SetRatePerSecond(ratePerSecond int64) error {
	if ratePerSecond < 0 {
		return errors.New("rate must be non-negative")
	}
	b.ratePerSecond = ratePerSecond
	return nil
}

// Decision is the per-request outcome plus the simulated cost of reaching it.
type Decision struct {
	Allowed       bool
	LatencyMillis int64
	StoreCalls    int
}

// SharedStore is a simulated shared store (Redis-like). Every call costs one round trip and is counted so the
// strategies can be compared on latency and coordination traffic, not just accuracy.
type SharedStore struct {
	rttMillis    int64
	buckets      map[string]*TokenBucket
	logs         map[string]*[]int64
	usageReports map[string][]int64 // key -> per-node counts
	calls        int64
}

func NewSharedStore(rttMillis int64) *SharedStore {
	return &SharedStore{rttMillis: rttMillis, buckets: map[string]*TokenBucket{}, logs: map[string]*[]int64{}, usageReports: map[string][]int64{}}
}

func (s *SharedStore) RttMillis() int64 { return s.rttMillis }
func (s *SharedStore) Calls() int64     { return s.calls }

func (s *SharedStore) Bucket(key string, capacity, rate, nowMillis int64) *TokenBucket {
	s.calls++
	b, ok := s.buckets[key]
	if !ok {
		var err error
		if b, err = NewTokenBucket(capacity, rate, nowMillis); err != nil {
			panic(err)
		}
		s.buckets[key] = b
	}
	return b
}

// Log returns the admitted-timestamp log for a key: the Java ArrayDeque<Long> is a slice the caller mutates in place.
func (s *SharedStore) Log(key string) *[]int64 {
	s.calls++
	l, ok := s.logs[key]
	if !ok {
		l = &[]int64{}
		s.logs[key] = l
	}
	return l
}

func (s *SharedStore) Usage(key string, nodes int) []int64 {
	s.calls++
	u, ok := s.usageReports[key]
	if !ok {
		u = make([]int64, nodes)
		s.usageReports[key] = u
	}
	return u
}

// RateLimiter is the contract every strategy implements: one node asks whether one request for one key may proceed.
type RateLimiter interface {
	TryAcquire(node int, key string, nowMillis int64) Decision
	Name() string
}

// CentralizedLimiter (strategy 1): one bucket per key in the shared store. Every decision is a round trip; the answer is exact.
type CentralizedLimiter struct {
	store         *SharedStore
	capacity      int64
	ratePerSecond int64
}

func NewCentralizedLimiter(store *SharedStore, capacity, ratePerSecond int64) *CentralizedLimiter {
	return &CentralizedLimiter{store, capacity, ratePerSecond}
}

func (l *CentralizedLimiter) TryAcquire(node int, key string, nowMillis int64) Decision {
	bucket := l.store.Bucket(key, l.capacity, l.ratePerSecond, nowMillis)
	ok := bucket.TryAcquire(1, nowMillis)
	return Decision{ok, l.store.RttMillis(), 1}
}

func (l *CentralizedLimiter) Name() string { return "centralized" }

// LocalSyncLimiter (strategy 2): each node owns a local bucket holding a share of the global rate and decides
// in-process (zero latency). Every syncInterval the nodes report their demand to the store and the shares are
// redistributed in proportion to it. Between syncs the global limit is only approximately enforced.
type LocalSyncLimiter struct {
	store              *SharedStore
	nodes              int
	capacity           int64
	ratePerSecond      int64
	syncIntervalMillis int64
	local              map[string][]*TokenBucket
	demandSinceSync    map[string][]int64
	lastSync           map[string]int64
	syncs              int64
}

func NewLocalSyncLimiter(store *SharedStore, nodes int, capacity, ratePerSecond, syncIntervalMillis int64) (*LocalSyncLimiter, error) {
	if nodes <= 0 {
		return nil, errors.New("need at least one node")
	}
	return &LocalSyncLimiter{
		store: store, nodes: nodes, capacity: capacity, ratePerSecond: ratePerSecond, syncIntervalMillis: syncIntervalMillis,
		local: map[string][]*TokenBucket{}, demandSinceSync: map[string][]int64{}, lastSync: map[string]int64{},
	}, nil
}

func (l *LocalSyncLimiter) bucketsFor(key string, nowMillis int64) []*TokenBucket {
	if arr, ok := l.local[key]; ok {
		return arr
	}
	arr := make([]*TokenBucket, l.nodes)
	for i := range arr {
		b, err := NewTokenBucket(max(1, l.capacity/int64(l.nodes)), l.ratePerSecond/int64(l.nodes), nowMillis)
		if err != nil {
			panic(err)
		}
		arr[i] = b
	}
	l.local[key] = arr
	l.lastSync[key] = nowMillis
	l.demandSinceSync[key] = make([]int64, l.nodes)
	return arr
}

func (l *LocalSyncLimiter) TryAcquire(node int, key string, nowMillis int64) Decision {
	buckets := l.bucketsFor(key, nowMillis)
	demand := l.demandSinceSync[key]
	demand[node]++
	storeCalls := 0
	var latency int64
	if nowMillis-l.lastSync[key] >= l.syncIntervalMillis {
		storeCalls = l.Sync(key, buckets, demand, nowMillis)
		latency = l.store.RttMillis() // the sync is off the request path in production; charged here for honesty
	}
	ok := buckets[node].TryAcquire(1, nowMillis)
	return Decision{ok, latency, storeCalls}
}

// Sync redistributes the global rate in proportion to each node's observed demand; floor at 1/(4N) so a cold node can still start.
func (l *LocalSyncLimiter) Sync(key string, buckets []*TokenBucket, demand []int64, nowMillis int64) int {
	shared := l.store.Usage(key, l.nodes)
	var total int64
	for i := 0; i < l.nodes; i++ {
		shared[i] = demand[i]
		total += demand[i]
	}
	n := int64(l.nodes)
	floor := max(1, l.ratePerSecond/(4*n))
	remaining := l.ratePerSecond - floor*n
	for i := 0; i < l.nodes; i++ {
		var share int64
		if total == 0 {
			share = floor + remaining/n
		} else {
			share = floor + remaining*shared[i]/total
		}
		buckets[i].Refill(nowMillis)
		if err := buckets[i].SetRatePerSecond(share); err != nil {
			panic(err)
		}
		demand[i] = 0
	}
	l.lastSync[key] = nowMillis
	l.syncs++
	return 1
}

func (l *LocalSyncLimiter) Syncs() int64 { return l.syncs }

func (l *LocalSyncLimiter) RateOf(key string, node int) int64 {
	return l.local[key][node].RatePerSecond()
}

func (l *LocalSyncLimiter) Name() string {
	return fmt.Sprintf("local+sync(%dms)", l.syncIntervalMillis)
}

// SlidingWindowLogLimiter (strategy 3): a shared sorted log of admitted timestamps per key. Exact over any
// sliding window, one round trip per decision, and memory proportional to the limit itself.
type SlidingWindowLogLimiter struct {
	store        *SharedStore
	limit        int64
	windowMillis int64
}

func NewSlidingWindowLogLimiter(store *SharedStore, limit, windowMillis int64) *SlidingWindowLogLimiter {
	return &SlidingWindowLogLimiter{store, limit, windowMillis}
}

func (l *SlidingWindowLogLimiter) TryAcquire(node int, key string, nowMillis int64) Decision {
	log := l.store.Log(key)
	cutoff := nowMillis - l.windowMillis
	for len(*log) > 0 && (*log)[0] <= cutoff {
		*log = (*log)[1:]
	}
	if int64(len(*log)) < l.limit {
		*log = append(*log, nowMillis)
		return Decision{true, l.store.RttMillis(), 1}
	}
	return Decision{false, l.store.RttMillis(), 1}
}

func (l *SlidingWindowLogLimiter) LogSize(key string) int64 { return int64(len(*l.store.Log(key))) }

func (l *SlidingWindowLogLimiter) Name() string { return "sliding-log" }

// LoadReport is what the load generator records: enough to judge accuracy, latency, and coordination cost.
type LoadReport struct {
	Strategy                                              string
	Offered, Admitted, Rejected, LatencyTotal, StoreCalls int64
	AdmittedPerSecond                                     []int64
}

func NewLoadReport(strategy string, seconds int) *LoadReport {
	return &LoadReport{Strategy: strategy, AdmittedPerSecond: make([]int64, seconds)}
}

func (r *LoadReport) PeakPerSecond() int64 {
	var peak int64
	for _, v := range r.AdmittedPerSecond {
		peak = max(peak, v)
	}
	return peak
}

func (r *LoadReport) MeanLatencyMillis() float64 {
	if r.Offered == 0 {
		return 0
	}
	return float64(r.LatencyTotal) / float64(r.Offered)
}

// VersusIdeal is admitted minus the most a correct token bucket could admit (rate * seconds + one full burst),
// capped at what was offered. Positive = leaked over the limit; negative = under-admitted (tokens left unused).
func (r *LoadReport) VersusIdeal(ratePerSecond, capacity int64) int64 {
	ideal := min(r.Offered, ratePerSecond*int64(len(r.AdmittedPerSecond))+capacity)
	return r.Admitted - ideal
}

// LoadGenerator is a seeded load generator. Each millisecond each node gets a Bernoulli request draw whose
// probability depends on node skew: node 0 receives hotShare of the traffic, the rest is spread evenly.
type LoadGenerator struct {
	nodes             int
	requestsPerSecond float64
	hotShare          float64
	seed              int64
}

func NewLoadGenerator(nodes int, requestsPerSecond, hotShare float64, seed int64) *LoadGenerator {
	return &LoadGenerator{nodes, requestsPerSecond, hotShare, seed}
}

func (g *LoadGenerator) Run(limiter RateLimiter, clock *FakeClock, key string, seconds int, ratePerSecond, capacity int64) *LoadReport {
	random := NewJavaRandom(g.seed)
	report := NewLoadReport(limiter.Name(), seconds)
	start := clock.NowMillis()
	for ms := 0; ms < seconds*1000; ms++ {
		for node := 0; node < g.nodes; node++ {
			var share float64
			switch {
			case g.nodes == 1:
				share = 1.0
			case node == 0:
				share = g.hotShare
			default:
				share = (1.0 - g.hotShare) / float64(g.nodes-1)
			}
			p := g.requestsPerSecond * share / 1000.0
			if random.NextDouble() < p {
				d := limiter.TryAcquire(node, key, clock.NowMillis())
				report.Offered++
				report.LatencyTotal += d.LatencyMillis
				report.StoreCalls += int64(d.StoreCalls)
				if d.Allowed {
					report.Admitted++
					report.AdmittedPerSecond[(clock.NowMillis()-start)/1000]++
				} else {
					report.Rejected++
				}
			}
		}
		clock.Advance(1)
	}
	return report
}

const (
	Rate     int64 = 100 // merchant API quota: 100 requests/second
	Capacity int64 = 100 // one second of burst
	Nodes          = 4
	Seconds        = 10
	Rtt      int64 = 2 // milliseconds to the shared store
)

func mustLocal(l *LocalSyncLimiter, err error) *LocalSyncLimiter {
	if err != nil {
		panic(err)
	}
	return l
}

// Compare runs the same seeded load against all four strategies and returns their reports in table order.
func Compare(offeredRps, hotShare float64, seed int64) []*LoadReport {
	load := NewLoadGenerator(Nodes, offeredRps, hotShare, seed)
	reports := []*LoadReport{
		load.Run(NewCentralizedLimiter(NewSharedStore(Rtt), Capacity, Rate), NewFakeClock(0), "merchant:acme", Seconds, Rate, Capacity),
		load.Run(mustLocal(NewLocalSyncLimiter(NewSharedStore(Rtt), Nodes, Capacity, Rate, 1000)), NewFakeClock(0), "merchant:acme", Seconds, Rate, Capacity),
		load.Run(mustLocal(NewLocalSyncLimiter(NewSharedStore(Rtt), Nodes, Capacity, Rate, 100)), NewFakeClock(0), "merchant:acme", Seconds, Rate, Capacity),
		load.Run(NewSlidingWindowLogLimiter(NewSharedStore(Rtt), Rate, 1000), NewFakeClock(0), "merchant:acme", Seconds, Rate, Capacity),
	}
	return reports
}

func printTable(title string, reports []*LoadReport) {
	fmt.Println("== " + title + " ==")
	fmt.Printf("%-20s %8s %9s %9s %10s %10s %10s\n", "strategy", "offered", "admitted", "rejected", "peak/s", "vs ideal", "mean ms")
	for _, r := range reports {
		fmt.Printf("%-20s %8d %9d %9d %10d %+10d %10.2f\n",
			r.Strategy, r.Offered, r.Admitted, r.Rejected, r.PeakPerSecond(), r.VersusIdeal(Rate, Capacity), r.MeanLatencyMillis())
	}
	fmt.Println()
}

func main() {
	fmt.Println("== A single token bucket: 100/s, capacity 100 ==")
	clock := NewFakeClock(0)
	bucket, err := NewTokenBucket(Capacity, Rate, clock.NowMillis())
	if err != nil {
		panic(err)
	}
	burst := 0
	for bucket.TryAcquire(1, clock.NowMillis()) {
		burst++
	}
	fmt.Printf("burst admitted from a full bucket: %d\n", burst)
	clock.Advance(10)
	fmt.Printf("after 10 ms: %d token(s) refilled (100/s * 0.010 s = 1)\n", bucket.AvailableTokens(clock.NowMillis()))
	clock.Advance(5000)
	fmt.Printf("after 5 s idle: %d tokens (clamped at capacity, not 500)\n", bucket.AvailableTokens(clock.NowMillis()))
	fmt.Println()

	printTable("Even load, 4 nodes, 400 offered/s against a 100/s limit (seed 7)", Compare(400, 0.25, 7))
	printTable("Skewed load, node 0 takes 85% of 400 offered/s (seed 7)", Compare(400, 0.85, 7))
	printTable("Light load, 60 offered/s, nobody should be rejected (seed 7)", Compare(60, 0.25, 7))

	fmt.Println("Reading the table:")
	fmt.Println("  centralized and sliding-log never exceed 100 admitted in any second: exact, but every call pays the 2 ms round trip.")
	fmt.Println("  local+sync pays no latency, but with skew the hot node runs dry while cold nodes hold unused tokens, so it under-admits;")
	fmt.Println("  syncing every 100 ms instead of every 1 s closes much of that gap at 10x the coordination traffic.")
	fmt.Println("  sliding-log has no burst allowance, so it admits exactly 100 per window and shows -100 against the bucket ideal.")
}
