// Lesson: phases/02-algorithms/09-shortest-paths/docs/en.md
// Shortest Paths: Dijkstra, Bellman-Ford, and Negative Cycles
// Run:  go run .
// Test: go test ./...
// Sources: Dijkstra (1959), "A note on two problems in connexion with graphs"; Bellman (1958), "On a routing problem";
// CLRS ch. 24. IndexedMinHeap copied from phases/01-complexity-and-core-structures/09-heaps-and-priority-queues;
// Money copied from phases/10-money-and-ledgers/01-representing-money. Go 1.22+, standard library only.
package main

import (
	"errors"
	"fmt"
	"math"
	"math/big"
)

// -----------------------------------------------------------------------------------------------------------
// Money (trimmed copy of Phase 10, Lesson 01): integer minor units, exact rational multiplication.
// -----------------------------------------------------------------------------------------------------------

// RateScale: rates carry 8 decimals, 0.92 is 92_000_000. WeightScale: weights are -ln(rate) in micro-nats.
const (
	RateScale   int64 = 100_000_000
	WeightScale       = 1_000_000.0
	Source            = "__source__"
)

type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	EUR = Currency{"EUR", 2}
	GBP = Currency{"GBP", 2}
	JPY = Currency{"JPY", 0}
)

func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

type Rounding int

const (
	HalfEven Rounding = iota
	HalfUp
	Down
	Up
)

type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// DivideRounded is integer division with an explicit rounding rule (Go's / truncates toward zero like Java's).
func DivideRounded(dividend, divisor int64, rounding Rounding) int64 {
	quotient := dividend / divisor
	remainder := dividend % divisor
	if remainder == 0 {
		return quotient
	}
	sign := int64(1)
	if (dividend < 0) != (divisor < 0) {
		sign = -1
	}
	twice := abs64(remainder) * 2
	absDivisor := abs64(divisor)
	switch rounding {
	case Down:
		return quotient
	case Up:
		return quotient + sign
	case HalfUp:
		if twice >= absDivisor {
			return quotient + sign
		}
		return quotient
	default:
		if twice > absDivisor {
			return quotient + sign
		}
		if twice < absDivisor || quotient%2 == 0 {
			return quotient
		}
		return quotient + sign
	}
}

// Convert at an exact scaled rate (rate = scaledRate / RateScale) with one rounding at the end. Minor-unit
// exponents may differ (USD cents to JPY yen), so both scales are applied. Uses big.Int for the product so a
// large amount times a large rate cannot wrap.
func (m Money) Convert(scaledRate int64, to Currency, rounding Rounding) (Money, error) {
	num := new(big.Int).Mul(big.NewInt(m.MinorUnits), big.NewInt(scaledRate))
	num.Mul(num, big.NewInt(to.MinorUnitsPerMajor()))
	den := new(big.Int).Mul(big.NewInt(RateScale), big.NewInt(m.Currency.MinorUnitsPerMajor()))
	if !num.IsInt64() {
		return Money{}, errors.New("int64 overflow in conversion")
	}
	return Money{DivideRounded(num.Int64(), den.Int64(), rounding), to}, nil
}

func (m Money) String() string {
	magnitude := abs64(m.MinorUnits)
	scale := m.Currency.MinorUnitsPerMajor()
	text := fmt.Sprintf("%d", magnitude/scale)
	if m.Currency.Exponent > 0 {
		text += fmt.Sprintf(".%0*d", m.Currency.Exponent, magnitude%scale)
	}
	if m.MinorUnits < 0 {
		text = "-" + text
	}
	return text + " " + m.Currency.Code
}

// -----------------------------------------------------------------------------------------------------------
// IndexedMinHeap (copied from Phase 1, Lesson 09): the priority queue Dijkstra needs, with DecreaseKey.
// -----------------------------------------------------------------------------------------------------------

var ErrEmpty = errors.New("heap is empty")

type Entry[K comparable] struct {
	Key      K
	Priority int64
	Seq      int64
}

func CompareEntries[K comparable](a, b Entry[K]) int {
	switch {
	case a.Priority < b.Priority:
		return -1
	case a.Priority > b.Priority:
		return 1
	case a.Seq < b.Seq:
		return -1
	case a.Seq > b.Seq:
		return 1
	}
	return 0
}

type IndexedMinHeap[K comparable] struct {
	entries []Entry[K]
	index   map[K]int
	nextSeq int64
}

func NewIndexedMinHeap[K comparable]() *IndexedMinHeap[K] {
	return &IndexedMinHeap[K]{index: make(map[K]int)}
}

func (h *IndexedMinHeap[K]) Push(key K, priority int64) error {
	if _, dup := h.index[key]; dup {
		return fmt.Errorf("key already present: %v", key)
	}
	h.entries = append(h.entries, Entry[K]{key, priority, h.nextSeq})
	h.nextSeq++
	h.index[key] = len(h.entries) - 1
	h.siftUp(len(h.entries) - 1)
	return nil
}

func (h *IndexedMinHeap[K]) DecreaseKey(key K, newPriority int64) error {
	i, ok := h.index[key]
	if !ok {
		return fmt.Errorf("key not present: %v", key)
	}
	current := h.entries[i]
	if newPriority > current.Priority {
		return fmt.Errorf("DecreaseKey cannot raise %v from %d to %d", key, current.Priority, newPriority)
	}
	h.entries[i] = Entry[K]{key, newPriority, current.Seq}
	h.siftUp(i)
	return nil
}

func (h *IndexedMinHeap[K]) Pop() (Entry[K], error) {
	if len(h.entries) == 0 {
		return Entry[K]{}, ErrEmpty
	}
	top := h.entries[0]
	last := h.entries[len(h.entries)-1]
	h.entries = h.entries[:len(h.entries)-1]
	delete(h.index, top.Key)
	if len(h.entries) > 0 {
		h.entries[0] = last
		h.index[last.Key] = 0
		h.siftDown(0)
	}
	return top, nil
}

func (h *IndexedMinHeap[K]) Contains(key K) bool { _, ok := h.index[key]; return ok }
func (h *IndexedMinHeap[K]) Size() int           { return len(h.entries) }
func (h *IndexedMinHeap[K]) IsEmpty() bool       { return len(h.entries) == 0 }

func (h *IndexedMinHeap[K]) siftUp(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if CompareEntries(h.entries[parent], h.entries[i]) <= 0 {
			return
		}
		h.swap(parent, i)
		i = parent
	}
}

func (h *IndexedMinHeap[K]) siftDown(i int) {
	n := len(h.entries)
	for {
		left, right, smallest := 2*i+1, 2*i+2, i
		if left < n && CompareEntries(h.entries[left], h.entries[smallest]) < 0 {
			smallest = left
		}
		if right < n && CompareEntries(h.entries[right], h.entries[smallest]) < 0 {
			smallest = right
		}
		if smallest == i {
			return
		}
		h.swap(i, smallest)
		i = smallest
	}
}

func (h *IndexedMinHeap[K]) swap(a, b int) {
	h.entries[a], h.entries[b] = h.entries[b], h.entries[a]
	h.index[h.entries[a].Key] = a
	h.index[h.entries[b].Key] = b
}

// -----------------------------------------------------------------------------------------------------------
// Weighted graph and the two shortest-path algorithms.
// -----------------------------------------------------------------------------------------------------------

var (
	ErrUnknownNode    = errors.New("unknown node")
	ErrNegativeWeight = errors.New("Dijkstra requires non-negative weights")
	ErrArbitrage      = errors.New("rates contain an arbitrage cycle")
)

// Edge is a directed edge with an integer weight. Integers on purpose: sums are exact and associative.
type Edge struct {
	From   string
	To     string
	Weight int64
}

// WeightedGraph keeps nodes and out-edges in insertion order (Go maps iterate randomly).
type WeightedGraph struct {
	order     []string
	index     map[string]int
	adjacency [][]Edge
	edges     []Edge
}

func NewWeightedGraph() *WeightedGraph { return &WeightedGraph{index: map[string]int{}} }

func (g *WeightedGraph) AddNode(id string) {
	if _, ok := g.index[id]; ok {
		return
	}
	g.index[id] = len(g.order)
	g.order = append(g.order, id)
	g.adjacency = append(g.adjacency, nil)
}

func (g *WeightedGraph) AddEdge(from, to string, weight int64) {
	g.AddNode(from)
	g.AddNode(to)
	e := Edge{from, to, weight}
	g.adjacency[g.index[from]] = append(g.adjacency[g.index[from]], e)
	g.edges = append(g.edges, e)
}

func (g *WeightedGraph) HasNode(id string) bool { _, ok := g.index[id]; return ok }
func (g *WeightedGraph) Nodes() []string        { return append([]string(nil), g.order...) }
func (g *WeightedGraph) Edges() []Edge          { return g.edges }
func (g *WeightedGraph) NodeCount() int         { return len(g.order) }

func (g *WeightedGraph) OutEdges(id string) ([]Edge, error) {
	i, ok := g.index[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownNode, id)
	}
	return g.adjacency[i], nil
}

// PathResult holds distances and parents from one source; a path is read back through the parents.
type PathResult struct {
	Distance map[string]int64
	Parent   map[string]string
}

func (r PathResult) Reached(id string) bool { _, ok := r.Distance[id]; return ok }

func (r PathResult) PathTo(target string) []string {
	if !r.Reached(target) {
		return []string{}
	}
	var path []string
	for at, ok := target, true; ok; at, ok = r.Parent[at] {
		path = append([]string{at}, path...)
	}
	return path
}

// Dijkstra: single-source shortest paths for NON-NEGATIVE weights. Invariant: when a node is popped from the heap
// its distance is final, because every other reachable node has distance >= the popped one and edges only add.
// O((V + E) log V) with the indexed heap: each node pushed once, each edge relaxes at most once.
func Dijkstra(g *WeightedGraph, source string) (PathResult, error) {
	if !g.HasNode(source) {
		return PathResult{}, fmt.Errorf("%w: %s", ErrUnknownNode, source)
	}
	for _, e := range g.edges {
		if e.Weight < 0 {
			return PathResult{}, fmt.Errorf("%w; %s->%s is %d", ErrNegativeWeight, e.From, e.To, e.Weight)
		}
	}
	distance := map[string]int64{}
	parent := map[string]string{}
	tentative := map[string]int64{source: 0}
	heap := NewIndexedMinHeap[string]()
	_ = heap.Push(source, 0)
	for !heap.IsEmpty() {
		top, _ := heap.Pop()
		u := top.Key
		distance[u] = top.Priority // settled: never changes again
		out, _ := g.OutEdges(u)
		for _, e := range out {
			if _, settled := distance[e.To]; settled { // a shorter route cannot exist
				continue
			}
			candidate := top.Priority + e.Weight
			known, seen := tentative[e.To]
			if !seen {
				tentative[e.To] = candidate
				parent[e.To] = u
				_ = heap.Push(e.To, candidate)
			} else if candidate < known {
				tentative[e.To] = candidate
				parent[e.To] = u
				_ = heap.DecreaseKey(e.To, candidate)
			}
		}
	}
	return PathResult{distance, parent}, nil
}

// BellmanFordResult: shortest paths if no negative cycle is reachable from the source, else the cycle (nil otherwise).
type BellmanFordResult struct {
	Paths         PathResult
	NegativeCycle []string
}

// BellmanFord: single-source shortest paths for ANY weights, O(V * E). Relax every edge V-1 times; a shortest path
// has at most V-1 edges, so after round k every node whose shortest path has k edges is correct. If a V-th round
// still relaxes an edge, a negative cycle is reachable, and the parent pointers lead into it.
func BellmanFord(g *WeightedGraph, source string) (BellmanFordResult, error) {
	if !g.HasNode(source) {
		return BellmanFordResult{}, fmt.Errorf("%w: %s", ErrUnknownNode, source)
	}
	distance := map[string]int64{source: 0}
	parent := map[string]string{}
	n := g.NodeCount()
	relaxed := ""
	for round := 1; round <= n; round++ {
		relaxed = ""
		for _, e := range g.edges {
			du, ok := distance[e.From]
			if !ok { // unreachable so far
				continue
			}
			candidate := du + e.Weight
			if dv, seen := distance[e.To]; !seen || candidate < dv {
				distance[e.To] = candidate
				parent[e.To] = e.From
				relaxed = e.To
			}
		}
		if relaxed == "" { // early exit: a full round changed nothing
			break
		}
	}
	if relaxed == "" {
		return BellmanFordResult{PathResult{distance, parent}, nil}, nil
	}
	// Round n still relaxed: walk parents n times to be sure we are inside the cycle, then collect it.
	x := relaxed
	for i := 0; i < n; i++ {
		x = parent[x]
	}
	backwards := []string{x}
	for at := parent[x]; at != x; at = parent[at] {
		backwards = append(backwards, at)
	}
	backwards = append(backwards, x)
	cycle := make([]string, len(backwards)) // parents point backwards; reverse for forward order
	for i, node := range backwards {
		cycle[len(backwards)-1-i] = node
	}
	return BellmanFordResult{PathResult{distance, parent}, cycle}, nil
}

// -----------------------------------------------------------------------------------------------------------
// FX: rates as scaled integers, weights as integer-scaled negative logs, arbitrage as a negative cycle.
// -----------------------------------------------------------------------------------------------------------

// Rate is one quote: 1 unit of From buys ScaledRate / RateScale units of To.
type Rate struct {
	From       string
	To         string
	ScaledRate int64
}

func NewRate(from, to string, scaledRate int64) (Rate, error) {
	if scaledRate <= 0 {
		return Rate{}, fmt.Errorf("rate must be positive: %s->%s", from, to)
	}
	return Rate{from, to, scaledRate}, nil
}

// WeightOf is the only floating-point operation in the lesson. ln turns a product of rates into a sum of
// weights; the minus sign turns "best rate" into "shortest path"; the scale turns the float64 into an int64 so
// every sum is exact, associative, and identical across Java, Python, and Go. Rounding is +-0.5 micro-nat per
// edge, so every detected cycle is re-checked exactly with CycleProduct before anyone calls it arbitrage.
// math.Round rounds ties away from zero; Java's Math.round rounds ties up; Python's round() rounds ties to even.
// No rate in this lesson lands on an exact tie, so the three agree.
func WeightOf(scaledRate int64) int64 {
	rate := float64(scaledRate) / float64(RateScale)
	return int64(math.Round(-math.Log(rate) * WeightScale))
}

func FromRates(rates []Rate) *WeightedGraph {
	g := NewWeightedGraph()
	for _, r := range rates {
		g.AddEdge(r.From, r.To, WeightOf(r.ScaledRate))
	}
	return g
}

// BestConversionPath: max product of rates = min sum of -log weights. Bellman-Ford, because weights can be negative.
func BestConversionPath(rates []Rate, from, to string) ([]string, error) {
	r, err := BellmanFord(FromRates(rates), from)
	if err != nil {
		return nil, err
	}
	if r.NegativeCycle != nil {
		return nil, fmt.Errorf("%w: %v", ErrArbitrage, r.NegativeCycle)
	}
	return r.Paths.PathTo(to), nil
}

// CycleProduct is the exact rational product of the rates along a closed walk: Numerator / RateScale^k.
type CycleProduct struct {
	Numerator   *big.Int
	Denominator *big.Int
}

func (p CycleProduct) ExceedsOne() bool { return p.Numerator.Cmp(p.Denominator) > 0 }

// GainBasisPoints is (num/den - 1) * 10000, truncated toward zero (big.Int.Quo truncates).
func (p CycleProduct) GainBasisPoints() int64 {
	diff := new(big.Int).Sub(p.Numerator, p.Denominator)
	diff.Mul(diff, big.NewInt(10_000))
	return diff.Quo(diff, p.Denominator).Int64()
}

func RateBetween(rates []Rate, from, to string) (int64, error) {
	for _, r := range rates {
		if r.From == from && r.To == to {
			return r.ScaledRate, nil
		}
	}
	return 0, fmt.Errorf("no rate %s->%s", from, to)
}

func ComputeCycleProduct(rates []Rate, closedWalk []string) (CycleProduct, error) {
	num, den := big.NewInt(1), big.NewInt(1)
	for i := 0; i+1 < len(closedWalk); i++ {
		r, err := RateBetween(rates, closedWalk[i], closedWalk[i+1])
		if err != nil {
			return CycleProduct{}, err
		}
		num.Mul(num, big.NewInt(r))
		den.Mul(den, big.NewInt(RateScale))
	}
	return CycleProduct{num, den}, nil
}

// Arbitrage finds any negative cycle in the whole graph by adding a virtual source with zero-weight edges to
// every currency. A cycle whose exact product is <= 1 is a rounding artefact and is not reported.
func Arbitrage(rates []Rate) ([]string, error) {
	g := FromRates(rates)
	for _, node := range g.Nodes() {
		g.AddEdge(Source, node, 0)
	}
	r, err := BellmanFord(g, Source)
	if err != nil {
		return nil, err
	}
	if r.NegativeCycle == nil {
		return nil, nil
	}
	p, err := ComputeCycleProduct(rates, r.NegativeCycle)
	if err != nil {
		return nil, err
	}
	if !p.ExceedsOne() {
		return nil, nil
	}
	return r.NegativeCycle, nil
}

// RotateTo rotates a closed walk so it starts (and ends) at start: [EUR GBP USD EUR] -> [USD EUR GBP USD].
func RotateTo(closedWalk []string, start string) ([]string, error) {
	k := -1
	for i, n := range closedWalk {
		if n == start {
			k = i
			break
		}
	}
	if k < 0 {
		return nil, fmt.Errorf("%s is not on the cycle %v", start, closedWalk)
	}
	length := len(closedWalk) - 1
	out := make([]string, length+1)
	for i := 0; i <= length; i++ {
		out[i] = closedWalk[(k+i)%length]
	}
	return out, nil
}

// ConvertAlong converts an amount along a path, rounding HalfEven at each hop as a real multi-leg conversion would.
func ConvertAlong(amount Money, path []string, rates []Rate, currencies map[string]Currency) (Money, error) {
	if len(path) == 0 || amount.Currency.Code != path[0] {
		return Money{}, fmt.Errorf("amount is %s but path starts at %v", amount.Currency.Code, path)
	}
	current := amount
	for i := 0; i+1 < len(path); i++ {
		rate, err := RateBetween(rates, path[i], path[i+1])
		if err != nil {
			return Money{}, err
		}
		if current, err = current.Convert(rate, currencies[path[i+1]], HalfEven); err != nil {
			return Money{}, err
		}
	}
	return current, nil
}

// Market: mid rates with a 0.2% spread applied, except USD->JPY which is quoted wide (1%).
func Market() []Rate {
	return []Rate{
		{"USD", "EUR", 91_816_000},     // 0.92 * 0.998
		{"EUR", "USD", 108_478_260},    // 1/0.92 * 0.998
		{"EUR", "GBP", 85_828_000},     // 0.86 * 0.998
		{"GBP", "EUR", 116_046_510},    // 1/0.86 * 0.998
		{"USD", "JPY", 14_850_000_000}, // 150 * 0.99 (wide)
		{"JPY", "USD", 665_333},        // 1/150 * 0.998
		{"EUR", "JPY", 16_271_400_000}, // 163.04 * 0.998
		{"JPY", "EUR", 612_060},        // 1/163.04 * 0.998
		{"GBP", "USD", 126_137_000},    // 1.2639 * 0.998
	}
}

var Currencies = map[string]Currency{"USD": USD, "EUR": EUR, "GBP": GBP, "JPY": JPY}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func main() {
	fmt.Println("== Rates to weights: -ln(rate) in micro-nats ==")
	for _, r := range Market()[:5] {
		fmt.Printf("%s->%s rate=%d/1e8 weight=%d\n", r.From, r.To, r.ScaledRate, WeightOf(r.ScaledRate))
	}

	fmt.Println("\n== Dijkstra on non-negative spread costs (basis points) ==")
	spreads := NewWeightedGraph()
	spreads.AddEdge("USD", "EUR", 20)
	spreads.AddEdge("EUR", "JPY", 20)
	spreads.AddEdge("USD", "JPY", 100)
	spreads.AddEdge("EUR", "GBP", 20)
	spreads.AddEdge("GBP", "JPY", 15)
	d := must(Dijkstra(spreads, "USD"))
	fmt.Printf("USD->JPY cheapest spread: %d bps via %v\n", d.Distance["JPY"], d.PathTo("JPY"))
	for _, n := range spreads.Nodes() {
		fmt.Printf("  %s = %d bps\n", n, d.Distance[n])
	}

	fmt.Println("\n== Bellman-Ford on -log weights: best conversion path ==")
	rates := Market()
	best := must(BestConversionPath(rates, "USD", "JPY"))
	tenK := OfMinor(1_000_000, USD)
	fmt.Printf("best USD->JPY path: %v\n", best)
	fmt.Printf("  10,000.00 USD along best path  = %v\n", must(ConvertAlong(tenK, best, rates, Currencies)))
	fmt.Printf("  10,000.00 USD on the direct quote = %v\n", must(ConvertAlong(tenK, []string{"USD", "JPY"}, rates, Currencies)))
	if arb := must(Arbitrage(rates)); arb == nil {
		fmt.Println("arbitrage in consistent market: none")
	} else {
		fmt.Printf("arbitrage in consistent market: %v\n", arb)
	}

	fmt.Println("\n== A stale GBP->USD quote creates a negative cycle ==")
	stale := append(append([]Rate(nil), rates[:8]...), Rate{"GBP", "USD", 128_000_000}) // 1.28 instead of 1.2614
	cycle := must(Arbitrage(stale))
	fmt.Printf("arbitrage cycle: %v\n", cycle)
	if cycle != nil {
		p := must(ComputeCycleProduct(stale, cycle))
		fmt.Printf("exact product of rates along the cycle = %v / %v\n", p.Numerator, p.Denominator)
		fmt.Printf("gain = %d basis points per loop\n", p.GainBasisPoints())
		fromUsd := must(RotateTo(cycle, "USD"))
		fmt.Printf("10,000.00 USD around %v = %v\n", fromUsd, must(ConvertAlong(tenK, fromUsd, stale, Currencies)))
	}

	fmt.Println("\n== Why Dijkstra cannot be used on these weights ==")
	if _, err := Dijkstra(FromRates(rates), "USD"); err != nil {
		fmt.Println("rejected:", err)
	}
}
