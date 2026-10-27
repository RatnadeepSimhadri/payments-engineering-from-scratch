// Lesson: phases/02-algorithms/07-greedy-algorithms/docs/en.md
// Greedy Algorithms and Exchange Arguments
// Run:  go run .
// Test: go test ./...
// Sources: Huffman, "A Method for the Construction of Minimum-Redundancy Codes" (Proc. IRE, 1952); Kleinberg & Tardos,
// Algorithm Design, ch. 4 (interval scheduling, exchange arguments); Pearson, "A polynomial-time algorithm for the
// change-making problem" (2005, canonical coin systems). Go 1.22+, standard library only. Same design as Java and Python.
package main

import (
	"cmp"
	"container/heap"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Sentinel errors: Go reports what Java throws as IllegalArgumentException.
var (
	ErrBadInterval  = errors.New("interval must have end > start")
	ErrNoSymbols    = errors.New("no symbols")
	ErrBadFrequency = errors.New("frequency must be positive")
	ErrInvalidBits  = errors.New("invalid bit string")
	ErrTruncated    = errors.New("truncated bit string")
	ErrNegative     = errors.New("negative amount")
)

// Impossible marks an amount the denominations cannot make (Java's Long.MAX_VALUE).
const Impossible = int64(math.MaxInt64)

// Interval is a half-open time interval [start, end): a settlement window, a maintenance slot, a batch job's run.
type Interval struct {
	Name       string
	Start, End int64
}

// NewInterval validates end > start.
func NewInterval(name string, start, end int64) (Interval, error) {
	if end <= start {
		return Interval{}, fmt.Errorf("%w: %s", ErrBadInterval, name)
	}
	return Interval{name, start, end}, nil
}

// --- Greedy interval problems. Both are provably optimal by exchange arguments (see docs). ---

// MaxNonOverlapping is the maximum number of pairwise non-overlapping intervals: always take the one that ends earliest. O(n log n).
func MaxNonOverlapping(intervals []Interval) []Interval {
	sorted := slices.Clone(intervals)
	slices.SortStableFunc(sorted, func(a, b Interval) int {
		return cmp.Or(cmp.Compare(a.End, b.End), cmp.Compare(a.Start, b.Start))
	})
	chosen := []Interval{}
	lastEnd := int64(math.MinInt64)
	for _, iv := range sorted {
		if iv.Start >= lastEnd { // compatible: take it
			chosen = append(chosen, iv)
			lastEnd = iv.End
		}
	}
	return chosen
}

// ShortestFirst is the tempting wrong greedy: take the shortest interval first. Kept to show it fail.
func ShortestFirst(intervals []Interval) []Interval {
	sorted := slices.Clone(intervals)
	slices.SortStableFunc(sorted, func(a, b Interval) int {
		return cmp.Or(cmp.Compare(a.End-a.Start, b.End-b.Start), cmp.Compare(a.Start, b.Start))
	})
	chosen := []Interval{}
	for _, iv := range sorted {
		compatible := true
		for _, c := range chosen {
			if iv.Start < c.End && c.Start < iv.End {
				compatible = false
				break
			}
		}
		if compatible {
			chosen = append(chosen, iv)
		}
	}
	return chosen
}

// int64Heap is a min-heap of end times for container/heap.
type int64Heap []int64

func (h int64Heap) Len() int           { return len(h) }
func (h int64Heap) Less(i, j int) bool { return h[i] < h[j] }
func (h int64Heap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *int64Heap) Push(x any)        { *h = append(*h, x.(int64)) }
func (h *int64Heap) Pop() any          { old := *h; v := old[len(old)-1]; *h = old[:len(old)-1]; return v }
func (h int64Heap) Peek() int64        { return h[0] }

// MinResources is the minimum number of resources (rooms, settlement lanes, worker threads) so that no two overlapping
// intervals share one. Sort by start; a min-heap of end times tells whether the earliest-finishing lane is free. O(n log n).
// The answer equals the maximum overlap depth, which is the lower bound, so this is optimal.
func MinResources(intervals []Interval) int {
	sorted := slices.Clone(intervals)
	slices.SortStableFunc(sorted, func(a, b Interval) int {
		return cmp.Or(cmp.Compare(a.Start, b.Start), cmp.Compare(a.End, b.End))
	})
	ends := &int64Heap{}
	for _, iv := range sorted {
		if ends.Len() > 0 && ends.Peek() <= iv.Start { // reuse the lane that frees up first
			heap.Pop(ends)
		}
		heap.Push(ends, iv.End)
	}
	return ends.Len()
}

// --- Huffman coding: a greedy that is optimal because merging the two rarest symbols is always safe. ---

// Node is a node of the code tree. Leaves carry a symbol; internal nodes carry the sum of their children's weights.
type Node struct {
	Weight      int64
	Symbol      int
	Left, Right *Node
	Order       int64
}

// IsLeaf is true for a symbol node.
func (n *Node) IsLeaf() bool { return n.Left == nil }

// nodeHeap orders by weight, then by creation order: a total order, so the tree is deterministic.
type nodeHeap []*Node

func (h nodeHeap) Len() int { return len(h) }
func (h nodeHeap) Less(i, j int) bool {
	return cmp.Or(cmp.Compare(h[i].Weight, h[j].Weight), cmp.Compare(h[i].Order, h[j].Order)) < 0
}
func (h nodeHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *nodeHeap) Push(x any)   { *h = append(*h, x.(*Node)) }
func (h *nodeHeap) Pop() any     { old := *h; v := old[len(old)-1]; *h = old[:len(old)-1]; return v }

// Build constructs the tree. `order` breaks weight ties by creation sequence so the tree, and the code, are deterministic.
func Build(frequencies map[int]int64) (*Node, error) {
	if len(frequencies) == 0 {
		return nil, ErrNoSymbols
	}
	symbols := make([]int, 0, len(frequencies)) // Java iterates a TreeMap: ascending symbol order
	for s := range frequencies {
		symbols = append(symbols, s)
	}
	sort.Ints(symbols)
	pq := &nodeHeap{}
	var order int64
	for _, s := range symbols {
		if frequencies[s] <= 0 {
			return nil, fmt.Errorf("%w: %d", ErrBadFrequency, s)
		}
		heap.Push(pq, &Node{frequencies[s], s, nil, nil, order})
		order++
	}
	if pq.Len() == 1 { // a one-symbol alphabet still needs a one-bit code
		only := heap.Pop(pq).(*Node)
		return &Node{only.Weight, -1, only, nil, order}, nil
	}
	for pq.Len() > 1 {
		a, b := heap.Pop(pq).(*Node), heap.Pop(pq).(*Node) // the two lightest: the greedy choice
		heap.Push(pq, &Node{a.Weight + b.Weight, -1, a, b, order})
		order++
	}
	return heap.Pop(pq).(*Node), nil
}

// Codes maps symbol -> bit string. Left edges are 0, right edges are 1.
func Codes(root *Node) map[int]string {
	out := map[int]string{}
	assign(root, "", out)
	return out
}

func assign(n *Node, prefix string, out map[int]string) {
	if n.IsLeaf() {
		if prefix == "" {
			prefix = "0"
		}
		out[n.Symbol] = prefix
		return
	}
	assign(n.Left, prefix+"0", out)
	if n.Right != nil {
		assign(n.Right, prefix+"1", out)
	}
}

// EncodedBits is the total encoded length in bits = sum over symbols of frequency * code length.
func EncodedBits(frequencies map[int]int64, codes map[int]string) int64 {
	var bits int64
	for s, f := range frequencies {
		bits += f * int64(len(codes[s]))
	}
	return bits
}

// Encode concatenates the codes of the symbols.
func Encode(symbols []int, codes map[int]string) string {
	var sb strings.Builder
	for _, s := range symbols {
		sb.WriteString(codes[s])
	}
	return sb.String()
}

// Decode walks the tree; a prefix code needs no separators.
func Decode(bits string, root *Node) ([]int, error) {
	out := []int{}
	cur := root
	for i := 0; i < len(bits); i++ {
		if bits[i] == '0' {
			cur = cur.Left
		} else {
			cur = cur.Right
		}
		if cur == nil {
			return nil, ErrInvalidBits
		}
		if cur.IsLeaf() {
			out = append(out, cur.Symbol)
			cur = root
		}
	}
	if cur != root {
		return nil, ErrTruncated
	}
	return out, nil
}

// IsPrefixFree is true when no code is a prefix of another: the property that makes decoding unambiguous.
func IsPrefixFree(codes map[int]string) bool {
	cs := make([]string, 0, len(codes))
	for _, c := range codes {
		cs = append(cs, c)
	}
	for i := range cs {
		for j := range cs {
			if i != j && strings.HasPrefix(cs[j], cs[i]) {
				return false
			}
		}
	}
	return true
}

// --- Change-making: pay an amount with the fewest coins or notes from a denomination set. ---
// Greedy (largest denomination first) is optimal for canonical systems (USD, EUR, INR) and wrong for others.

// Greedy: largest first. Returns counts per denomination (same order as input); ok is false if the amount cannot be made.
func Greedy(denominations []int64, amount int64) (counts []int64, ok bool) {
	sorted := slices.Clone(denominations)
	slices.Sort(sorted)
	count := make([]int64, len(denominations))
	remaining := amount
	for i := len(sorted) - 1; i >= 0; i-- {
		d := sorted[i]
		idx := slices.Index(denominations, d)
		count[idx] = remaining / d
		remaining %= d
	}
	if remaining != 0 {
		return nil, false
	}
	return count, true
}

// OptimalTable is the exact minimum coin count for every amount 0..maxAmount by dynamic programming; Impossible marks
// an unreachable amount. O(maxAmount * k).
func OptimalTable(denominations []int64, maxAmount int64) ([]int64, error) {
	if maxAmount < 0 {
		return nil, ErrNegative
	}
	best := make([]int64, maxAmount+1)
	for i := range best {
		best[i] = Impossible
	}
	best[0] = 0
	for a := int64(1); a <= maxAmount; a++ {
		for _, d := range denominations {
			if d <= a && best[a-d] != Impossible {
				best[a] = min(best[a], best[a-d]+1)
			}
		}
	}
	return best, nil
}

// OptimalCount is the exact minimum coin count for one amount: the oracle greedy is tested against.
func OptimalCount(denominations []int64, amount int64) int64 {
	table, err := OptimalTable(denominations, amount)
	if err != nil {
		panic(err) // a negative amount is a programming error in the caller
	}
	return table[amount]
}

// Total sums the coin counts.
func Total(counts []int64) int64 {
	var t int64
	for _, c := range counts {
		t += c
	}
	return t
}

// IsCanonical is the Kozen-Zaks test: a denomination system is canonical (greedy is optimal for every amount) iff greedy is
// optimal for every amount below d[k-1] + d[k], the sum of the two largest denominations. Needs a 1-coin to make every amount.
func IsCanonical(denominations []int64) bool {
	sorted := slices.Clone(denominations)
	slices.Sort(sorted)
	if sorted[0] != 1 {
		return false
	}
	limit := sorted[0]
	if len(sorted) >= 2 {
		limit = sorted[len(sorted)-1] + sorted[len(sorted)-2]
	}
	optimal, _ := OptimalTable(denominations, limit) // limit >= 1: cannot fail
	for amount := int64(1); amount < limit; amount++ {
		g, ok := Greedy(denominations, amount)
		if !ok || Total(g) != optimal[amount] {
			return false
		}
	}
	return true
}

func names(xs []Interval) []string {
	out := make([]string, len(xs))
	for i, iv := range xs {
		out[i] = iv.Name
	}
	return out
}

// jstr formats a slice as Java's List.toString does: [a, b, c].
func jstr[T any](xs []T) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// commas renders 1234567 as "1,234,567" (Java's %,d).
func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, ch := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	return b.String()
}

func describe(denoms, counts []int64) string {
	var sb strings.Builder
	for i := len(denoms) - 1; i >= 0; i-- {
		if counts[i] > 0 {
			fmt.Fprintf(&sb, "%dx%d ", counts[i], denoms[i])
		}
	}
	return "{" + strings.TrimSpace(sb.String()) + "}"
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func iv(name string, start, end int64) Interval { return must(NewInterval(name, start, end)) }

// Windows is the demo's settlement schedule, shared with the tests.
func Windows() []Interval {
	return []Interval{
		iv("ACH-batch", 0, 6), iv("card-settle", 1, 4), iv("wire-cutoff", 3, 5),
		iv("fx-fixing", 3, 8), iv("payout-run", 4, 7), iv("recon", 5, 9),
		iv("eod-close", 6, 10), iv("reporting", 8, 11)}
}

// ResponseCodeFrequencies is the demo's ISO 8583 response-code histogram, shared with the tests.
func ResponseCodeFrequencies() map[int]int64 {
	return map[int]int64{0: 850_000, 5: 60_000, 51: 45_000, 14: 20_000, 54: 15_000, 91: 7_000, 96: 3_000}
}

func main() {
	usd := USD

	fmt.Println("== Interval scheduling: settlement windows that need an exclusive lane ==")
	windows := Windows()
	best := MaxNonOverlapping(windows)
	fmt.Printf("earliest-end-first picks %d: %s\n", len(best), jstr(names(best)))
	worse := ShortestFirst(windows)
	fmt.Printf("shortest-first picks     %d: %s\n", len(worse), jstr(names(worse)))
	fmt.Printf("lanes needed to run all %d windows: %d\n", len(windows), MinResources(windows))

	fmt.Println()
	fmt.Println("== Huffman: a prefix code for ISO 8583 response codes by frequency ==")
	freq := ResponseCodeFrequencies()
	tree := must(Build(freq))
	codes := Codes(tree)
	symbols := make([]int, 0, len(freq))
	for s := range freq {
		symbols = append(symbols, s)
	}
	sort.Ints(symbols)
	var total int64
	for _, s := range symbols {
		fmt.Printf("  code %2d  freq %8s  bits %s\n", s, commas(freq[s]), codes[s])
		total += freq[s]
	}
	huff := EncodedBits(freq, codes)
	fixed := total * 3 // 7 symbols need 3 bits each in a fixed-width code
	fmt.Printf("fixed 3-bit code: %s bits; Huffman: %s bits (%.1f%% of fixed); prefix-free: %t\n",
		commas(fixed), commas(huff), 100.0*float64(huff)/float64(fixed), IsPrefixFree(codes))
	msg := []int{0, 0, 0, 5, 0, 51, 0, 0, 14, 0}
	bits := Encode(msg, codes)
	fmt.Printf("encode %s -> %s -> decode %s\n", jstr(msg), bits, jstr(must(Decode(bits, tree))))

	fmt.Println()
	fmt.Println("== Change-making: greedy is optimal for some denomination sets and wrong for others ==")
	usdCoins := []int64{1, 5, 10, 25, 100, 500, 1000, 2000, 5000, 10000}
	odd := []int64{1, 3, 4}
	oldUk := []int64{1, 3, 6, 12, 24, 30, 60, 240} // pre-decimal pence: penny, threepence, sixpence, shilling, florin, half-crown, crown, pound
	fmt.Printf("USD-like %s canonical? %t\n", jstr(usdCoins), IsCanonical(usdCoins))
	odd6, _ := Greedy(odd, 6)
	fmt.Printf("{1,3,4}  canonical? %t: amount 6 -> greedy %d coins, optimal %d\n", IsCanonical(odd), Total(odd6), OptimalCount(odd, 6))
	uk48, _ := Greedy(oldUk, 48)
	fmt.Printf("pre-1971 UK pence canonical? %t: amount 48 -> greedy %d coins (30+12+6), optimal %d (24+24)\n",
		IsCanonical(oldUk), Total(uk48), OptimalCount(oldUk, 48))
	payout, _ := Greedy(usdCoins, 18_741)
	fmt.Printf("pay %s greedily: %d pieces %s\n", OfMinor(18_741, usd), Total(payout), describe(usdCoins, payout))
}
