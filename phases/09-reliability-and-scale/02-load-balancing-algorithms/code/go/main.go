// Lesson: phases/09-reliability-and-scale/02-load-balancing-algorithms/docs/en.md
// Load Balancing: Round Robin, Least Connections, Power of Two Choices
// Run:  go run .
// Test: go test ./...
// Sources: Mitzenmacher, "The Power of Two Choices in Randomized Load Balancing" (2001);
//
//	Karger et al., "Consistent Hashing and Random Trees" (1997); nginx smooth weighted round robin.
//
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Rng is SplitMix64: a tiny seeded generator that produces identical streams in Java, Python, and Go.
type Rng struct{ state uint64 }

func NewRng(seed int64) *Rng { return &Rng{state: uint64(seed)} }

// NextLong returns the next 64-bit value as a signed integer, matching Java's long.
func (r *Rng) NextLong() int64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return int64(z ^ (z >> 31))
}

// NextDouble is uniform in [0, 1) with 53 bits of precision.
func (r *Rng) NextDouble() float64 { return float64(uint64(r.NextLong())>>11) * (1.0 / (1 << 53)) }

// NextInt is uniform in [0, bound).
func (r *Rng) NextInt(bound int) int { return int(r.NextDouble() * float64(bound)) }

// NextExponential has the given mean: the classic model of request work and arrival gaps.
func (r *Rng) NextExponential(mean float64) float64 { return -mean * math.Log(1.0-r.NextDouble()) }

// Backend is a server behind the balancer. Mutable because its load changes with every routed request.
type Backend struct {
	ID                string
	Weight            int     // relative capacity for weighted strategies
	Speed             float64 // service-time divisor: 2.0 finishes work twice as fast as 1.0
	ActiveConnections int
	TotalAssigned     int64
	Healthy           bool // flipped by the HealthChecker, never by a strategy
}

func NewBackend(id string, weight int, speed float64) *Backend {
	if weight <= 0 {
		panic("weight must be positive: " + id)
	}
	if speed <= 0 {
		panic("speed must be positive: " + id)
	}
	return &Backend{ID: id, Weight: weight, Speed: speed, Healthy: true}
}

func (b *Backend) String() string { return b.ID }

// Request is one unit of work: who it is for (the affinity key) and how much work it carries, in ms on a speed-1.0 backend.
type Request struct {
	ID     int64
	Key    string
	WorkMs float64
}

var ErrNoHealthyBackends = errors.New("no healthy backends")

// Balancer chooses among the currently healthy backends. It never mutates them.
type Balancer interface {
	Name() string
	Pick(healthy []*Backend, request Request) (*Backend, error)
}

// RoundRobin: the i-th request goes to backend i mod n. Ignores weight, speed, and load.
type RoundRobin struct{ counter int64 }

func (*RoundRobin) Name() string { return "round-robin" }

func (rr *RoundRobin) Pick(healthy []*Backend, _ Request) (*Backend, error) {
	if len(healthy) == 0 {
		return nil, ErrNoHealthyBackends
	}
	b := healthy[int(rr.counter%int64(len(healthy)))]
	rr.counter++
	return b, nil
}

// WeightedRoundRobin is nginx's smooth algorithm: weights 5:1:1 yield a a b a c a a.
type WeightedRoundRobin struct{ current map[string]int64 }

func NewWeightedRoundRobin() *WeightedRoundRobin {
	return &WeightedRoundRobin{current: map[string]int64{}}
}

func (*WeightedRoundRobin) Name() string { return "weighted-round-robin" }

func (w *WeightedRoundRobin) Pick(healthy []*Backend, _ Request) (*Backend, error) {
	if len(healthy) == 0 {
		return nil, ErrNoHealthyBackends
	}
	var total int64
	var best *Backend
	bestScore := int64(math.MinInt64)
	for _, b := range healthy {
		score := w.current[b.ID] + int64(b.Weight)
		w.current[b.ID] = score
		total += int64(b.Weight)
		if score > bestScore {
			bestScore, best = score, b
		}
	}
	w.current[best.ID] = bestScore - total
	return best, nil
}

// LeastConnections picks the backend with the fewest in-flight requests; ties go to the earliest in the list.
type LeastConnections struct{}

func (LeastConnections) Name() string { return "least-connections" }

func (LeastConnections) Pick(healthy []*Backend, _ Request) (*Backend, error) {
	if len(healthy) == 0 {
		return nil, ErrNoHealthyBackends
	}
	best := healthy[0]
	for _, b := range healthy {
		if b.ActiveConnections < best.ActiveConnections {
			best = b
		}
	}
	return best, nil
}

// PowerOfTwoChoices samples two distinct backends uniformly at random and takes the less loaded one.
type PowerOfTwoChoices struct{ rng *Rng }

func NewPowerOfTwoChoices(rng *Rng) *PowerOfTwoChoices { return &PowerOfTwoChoices{rng: rng} }

func (*PowerOfTwoChoices) Name() string { return "power-of-two-choices" }

func (p *PowerOfTwoChoices) Pick(healthy []*Backend, _ Request) (*Backend, error) {
	if len(healthy) == 0 {
		return nil, ErrNoHealthyBackends
	}
	if len(healthy) == 1 {
		return healthy[0], nil
	}
	i := p.rng.NextInt(len(healthy))
	j := p.rng.NextInt(len(healthy) - 1)
	if j >= i {
		j++ // distinct second sample
	}
	a, b := healthy[i], healthy[j]
	if b.ActiveConnections < a.ActiveConnections {
		return b, nil
	}
	return a, nil
}

// fnv1a64 is five lines and identical in every language. Its high bits barely depend on the last byte.
func fnv1a64(s string) uint64 {
	h := uint64(0xcbf29ce484222325)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 0x100000001b3
	}
	return h
}

// fmix64 is MurmurHash3's 64-bit finaliser: spreads every input bit across the whole word.
func fmix64(z uint64) uint64 {
	z ^= z >> 33
	z *= 0xff51afd7ed558ccd
	z ^= z >> 33
	z *= 0xc4ceb9fe1a85ec53
	z ^= z >> 33
	return z
}

// Hash64 is the ring hash: FNV-1a for bytes-to-word, fmix64 so "merchant-1" and "merchant-2" land far apart.
func Hash64(s string) uint64 { return fmix64(fnv1a64(s)) }

// ConsistentHash with virtual nodes: same key, same backend; removing a backend moves only its keys.
type ConsistentHash struct {
	VirtualNodes int
	ringHashes   []uint64
	ringOwners   []*Backend
	builtFor     string
}

func NewConsistentHash(virtualNodes int) *ConsistentHash {
	if virtualNodes <= 0 {
		panic("virtualNodes must be positive")
	}
	return &ConsistentHash{VirtualNodes: virtualNodes}
}

func (*ConsistentHash) Name() string { return "consistent-hash" }

func idsOf(healthy []*Backend) string {
	ids := make([]string, len(healthy))
	for i, b := range healthy {
		ids[i] = b.ID
	}
	return strings.Join(ids, ",")
}

func (c *ConsistentHash) rebuild(healthy []*Backend) {
	type point struct {
		hash  uint64
		owner *Backend
	}
	points := make([]point, 0, len(healthy)*c.VirtualNodes)
	for _, b := range healthy {
		for v := 0; v < c.VirtualNodes; v++ {
			points = append(points, point{Hash64(fmt.Sprintf("%s#%d", b.ID, v)), b})
		}
	}
	sort.SliceStable(points, func(i, j int) bool { return points[i].hash < points[j].hash })
	c.ringHashes = make([]uint64, len(points))
	c.ringOwners = make([]*Backend, len(points))
	for i, p := range points {
		c.ringHashes[i], c.ringOwners[i] = p.hash, p.owner
	}
	c.builtFor = idsOf(healthy)
}

func (c *ConsistentHash) Pick(healthy []*Backend, request Request) (*Backend, error) {
	if len(healthy) == 0 {
		return nil, ErrNoHealthyBackends
	}
	if ids := idsOf(healthy); ids != c.builtFor {
		c.rebuild(healthy)
	}
	h := Hash64(request.Key)
	i := sort.Search(len(c.ringHashes), func(k int) bool { return c.ringHashes[k] >= h }) // first position >= h
	if i == len(c.ringHashes) {
		i = 0 // wrap around the ring
	}
	return c.ringOwners[i], nil
}

// HealthChecker: N consecutive failures evict a backend, M consecutive successes readmit it. Consecutive, not cumulative.
type HealthChecker struct {
	FailThreshold    int
	SuccessThreshold int
	failures         map[string]int
	successes        map[string]int
}

func NewHealthChecker(failThreshold, successThreshold int) *HealthChecker {
	if failThreshold <= 0 || successThreshold <= 0 {
		panic("thresholds must be positive")
	}
	return &HealthChecker{failThreshold, successThreshold, map[string]int{}, map[string]int{}}
}

// Record returns true if the backend's health flag changed as a result of this observation.
func (h *HealthChecker) Record(b *Backend, success bool) bool {
	if success {
		h.failures[b.ID] = 0
		h.successes[b.ID]++
		if !b.Healthy && h.successes[b.ID] >= h.SuccessThreshold {
			b.Healthy = true
			return true
		}
	} else {
		h.successes[b.ID] = 0
		h.failures[b.ID]++
		if b.Healthy && h.failures[b.ID] >= h.FailThreshold {
			b.Healthy = false
			return true
		}
	}
	return false
}

// Pool is the balancer plus its backends plus the health checker: the thing a request actually hits.
type Pool struct {
	Backends []*Backend
	Balancer Balancer
	Health   *HealthChecker
}

func NewPool(backends []*Backend, balancer Balancer, health *HealthChecker) *Pool {
	if len(backends) == 0 {
		panic("pool needs at least one backend")
	}
	return &Pool{append([]*Backend(nil), backends...), balancer, health}
}

func (p *Pool) HealthyBackends() []*Backend {
	out := make([]*Backend, 0, len(p.Backends))
	for _, b := range p.Backends {
		if b.Healthy {
			out = append(out, b)
		}
	}
	return out
}

// Route routes the request, or returns nil when every backend is evicted (the caller sheds load).
func (p *Pool) Route(request Request) *Backend {
	h := p.HealthyBackends()
	if len(h) == 0 {
		return nil
	}
	b, err := p.Balancer.Pick(h, request)
	if err != nil {
		return nil
	}
	b.ActiveConnections++
	b.TotalAssigned++
	return b
}

func (p *Pool) Complete(b *Backend, success bool) {
	b.ActiveConnections--
	p.Health.Record(b, success)
}

// SimResult is what one simulation run measured. Latencies are in milliseconds.
type SimResult struct {
	Strategy    string
	Requests    int
	Failed      int
	Shed        int
	MeanMs      float64
	P50Ms       float64
	P99Ms       float64
	MaxQueue    int
	MaxAssigned int64
	MinAssigned int64
}

func (r SimResult) Line() string {
	return fmt.Sprintf("%-22s mean %8.2f  p50 %7.2f  p99 %9.2f  max-queue %4d  assigned %4d..%-5d failed %d  shed %d",
		r.Strategy, r.MeanMs, r.P50Ms, r.P99Ms, r.MaxQueue, r.MinAssigned, r.MaxAssigned, r.Failed, r.Shed)
}

// Percentile is nearest-rank on a sorted slice.
func Percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p / 100.0 * float64(len(sorted))))
	i := rank - 1
	if i < 0 {
		i = 0
	}
	if i > len(sorted)-1 {
		i = len(sorted) - 1
	}
	return sorted[i]
}

type completion struct {
	at float64
	ok bool
}

type window struct{ from, to float64 }

// Simulation: each backend is a single FIFO server, so latency = queueing + service time.
// A backend's queue length is exactly its ActiveConnections, which is what least-connections and P2C read.
type Simulation struct {
	Backends         []*Backend
	Pool             *Pool
	MeanArrivalGapMs float64
	MeanWorkMs       float64
	Rng              *Rng
	ProbeIntervalMs  float64
	outages          map[string]window
	inFlight         map[string][]completion
	freeAt           map[string]float64
	nextProbe        map[string]float64
}

func NewSimulation(backends []*Backend, balancer Balancer, health *HealthChecker,
	meanArrivalGapMs, meanWorkMs float64, rng *Rng) *Simulation {
	s := &Simulation{Backends: backends, Pool: NewPool(backends, balancer, health),
		MeanArrivalGapMs: meanArrivalGapMs, MeanWorkMs: meanWorkMs, Rng: rng, ProbeIntervalMs: 500,
		outages: map[string]window{}, inFlight: map[string][]completion{}, freeAt: map[string]float64{},
		nextProbe: map[string]float64{}}
	return s
}

// Outage declares that a backend fails every request between from and to (milliseconds).
func (s *Simulation) Outage(backendID string, fromMs, toMs float64) {
	s.outages[backendID] = window{fromMs, toMs}
}

func (s *Simulation) isDown(b *Backend, now float64) bool {
	w, ok := s.outages[b.ID]
	return ok && now >= w.from && now < w.to
}

func (s *Simulation) retireCompleted(now float64) {
	for _, b := range s.Backends {
		q := s.inFlight[b.ID]
		for len(q) > 0 && q[0].at <= now {
			s.Pool.Complete(b, q[0].ok)
			q = q[1:]
		}
		s.inFlight[b.ID] = q
	}
}

// probeEvicted: an evicted backend gets no traffic, so only an active probe can ever readmit it.
func (s *Simulation) probeEvicted(now float64) {
	for _, b := range s.Backends {
		if b.Healthy {
			continue
		}
		if now >= s.nextProbe[b.ID] {
			s.Pool.Health.Record(b, !s.isDown(b, now))
			s.nextProbe[b.ID] = now + s.ProbeIntervalMs
		}
	}
}

func (s *Simulation) Run(requests, keySpace int) SimResult {
	now := 0.0
	latencies := make([]float64, 0, requests)
	failed, shed, maxQueue := 0, 0, 0
	for i := 0; i < requests; i++ {
		now += s.Rng.NextExponential(s.MeanArrivalGapMs)
		work := s.Rng.NextExponential(s.MeanWorkMs)
		key := fmt.Sprintf("merchant-%d", s.Rng.NextInt(keySpace))
		s.retireCompleted(now)
		s.probeEvicted(now)
		b := s.Pool.Route(Request{int64(i), key, work})
		if b == nil {
			shed++
			continue
		}
		if b.ActiveConnections > maxQueue {
			maxQueue = b.ActiveConnections
		}
		down := s.isDown(b, now)
		service := work / b.Speed
		if down {
			service = 1.0 // a dead backend fails fast
		}
		start := math.Max(now, s.freeAt[b.ID])
		done := start + service
		s.freeAt[b.ID] = done
		s.inFlight[b.ID] = append(s.inFlight[b.ID], completion{done, !down})
		if down {
			failed++
		} else {
			latencies = append(latencies, done-now)
		}
	}
	sort.Float64s(latencies)
	sum := 0.0
	for _, l := range latencies {
		sum += l
	}
	mean := 0.0
	if len(latencies) > 0 {
		mean = sum / float64(len(latencies))
	}
	maxA, minA := int64(math.MinInt64), int64(math.MaxInt64)
	for _, b := range s.Backends {
		if b.TotalAssigned > maxA {
			maxA = b.TotalAssigned
		}
		if b.TotalAssigned < minA {
			minA = b.TotalAssigned
		}
	}
	return SimResult{s.Pool.Balancer.Name(), len(latencies), failed, shed, mean,
		Percentile(latencies, 50), Percentile(latencies, 99), maxQueue, maxA, minA}
}

// BallsInBins: the maximum load after dropping balls into bins with d random choices each.
func BallsInBins(balls, bins, choices int, rng *Rng) int {
	load := make([]int, bins)
	maxLoad := 0
	for i := 0; i < balls; i++ {
		best := rng.NextInt(bins)
		for c := 1; c < choices; c++ {
			cand := rng.NextInt(bins)
			if load[cand] < load[best] {
				best = cand
			}
		}
		load[best]++
		if load[best] > maxLoad {
			maxLoad = load[best]
		}
	}
	return maxLoad
}

// Fleet: five fast servers and one at half speed, a fleet mid-rollout or with a noisy neighbour.
func Fleet() []*Backend {
	return []*Backend{NewBackend("auth-1", 2, 1.0), NewBackend("auth-2", 2, 1.0), NewBackend("auth-3", 2, 1.0),
		NewBackend("auth-4", 2, 1.0), NewBackend("auth-5", 2, 1.0), NewBackend("auth-6", 1, 0.5)}
}

func Simulate(balancer Balancer, seed int64, withOutage bool) SimResult {
	sim := NewSimulation(Fleet(), balancer, NewHealthChecker(3, 2), 2.2, 10.0, NewRng(seed))
	if withOutage {
		sim.Outage("auth-3", 5_000, 8_000)
	}
	return sim.Run(20_000, 500)
}

func mustPick(b Balancer, healthy []*Backend, r Request) *Backend {
	out, err := b.Pick(healthy, r)
	if err != nil {
		panic(err)
	}
	return out
}

func main() {
	fmt.Println("== Balls into bins: 10,000 balls, 10,000 bins ==")
	for d := 1; d <= 3; d++ {
		fmt.Printf("%d choice(s): max load %d\n", d, BallsInBins(10_000, 10_000, d, NewRng(7)))
	}

	fmt.Println()
	fmt.Println("== Authorization fleet: 5 fast servers + 1 at half speed, 20,000 requests, ~83% utilisation ==")
	for _, s := range []Balancer{&RoundRobin{}, NewWeightedRoundRobin(), LeastConnections{},
		NewPowerOfTwoChoices(NewRng(11)), NewConsistentHash(100)} {
		fmt.Println(Simulate(s, 42, false).Line())
	}

	fmt.Println()
	fmt.Println("== Same fleet, auth-3 dies from t=5s to t=8s; health checker evicts after 3 failures ==")
	for _, s := range []Balancer{&RoundRobin{}, LeastConnections{}, NewPowerOfTwoChoices(NewRng(11))} {
		fmt.Println(Simulate(s, 42, true).Line())
	}

	fmt.Println()
	fmt.Println("== Smooth weighted round robin, weights a=5 b=1 c=1 ==")
	wrr := NewWeightedRoundRobin()
	abc := []*Backend{NewBackend("a", 5, 1), NewBackend("b", 1, 1), NewBackend("c", 1, 1)}
	seq := make([]string, 0, 7)
	for i := 0; i < 7; i++ {
		seq = append(seq, mustPick(wrr, abc, Request{int64(i), "k", 1}).ID)
	}
	fmt.Println(strings.Join(seq, " "))

	fmt.Println()
	fmt.Println("== Consistent hashing: remove one of 6 backends, how many of 1,000 keys move? ==")
	ch := NewConsistentHash(100)
	six := Fleet()
	five := append(append([]*Backend{}, six[:2]...), six[3:]...)
	moved := 0
	for k := 0; k < 1000; k++ {
		r := Request{int64(k), fmt.Sprintf("merchant-%d", k), 1}
		if mustPick(ch, six, r) != mustPick(ch, five, r) {
			moved++
		}
	}
	fmt.Printf("moved %d of 1000 keys (ideal 1/6 = 167); modulo hashing would move ~833\n", moved)
}
