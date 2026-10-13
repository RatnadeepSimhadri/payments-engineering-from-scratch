// Lesson: phases/02-algorithms/08-graph-traversal-bfs-dfs/docs/en.md
// Graph Traversal: BFS, DFS, and Connected Components
// Run:  go run .
// Test: go test ./...
// Sources: CLRS ch. 22 (BFS, DFS, white/gray/black colouring); Tarjan, "Depth-First Search and Linear Graph
// Algorithms" (1972). Money design copied from phases/10-money-and-ledgers/01-representing-money.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
)

// Currency is an ISO 4217 currency: code and minor-unit exponent. Trimmed copy of Phase 10 Lesson 01.
type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	EUR = Currency{"EUR", 2}
)

func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

// ErrCurrencyMismatch is returned when two amounts of different currencies are combined.
var ErrCurrencyMismatch = errors.New("currency mismatch")

// ErrUnknownNode is returned for a node id the graph has never seen.
var ErrUnknownNode = errors.New("unknown node")

// Money is an exact amount of one currency as a signed count of minor units. Never a float.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func (m Money) Plus(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.Currency.Code, other.Currency.Code)
	}
	return Money{m.MinorUnits + other.MinorUnits, m.Currency}, nil
}

func (m Money) String() string {
	magnitude := m.MinorUnits
	if magnitude < 0 {
		magnitude = -magnitude
	}
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

// Graph is an adjacency-list graph over string node ids. Go maps iterate in random order, so the node
// list and each neighbour list are kept as slices in insertion order; that is what makes every
// traversal below deterministic.
type Graph struct {
	directed  bool
	order     []string
	index     map[string]int
	adjacency [][]string
	edgeCount int
}

func NewGraph(directed bool) *Graph {
	return &Graph{directed: directed, index: map[string]int{}}
}

func (g *Graph) IsDirected() bool { return g.directed }

func (g *Graph) AddNode(id string) {
	if _, ok := g.index[id]; ok {
		return
	}
	g.index[id] = len(g.order)
	g.order = append(g.order, id)
	g.adjacency = append(g.adjacency, nil)
}

// AddEdge adds the endpoints if absent. For an undirected graph the edge is stored in both lists.
func (g *Graph) AddEdge(from, to string) {
	g.AddNode(from)
	g.AddNode(to)
	g.adjacency[g.index[from]] = append(g.adjacency[g.index[from]], to)
	if !g.directed && from != to {
		g.adjacency[g.index[to]] = append(g.adjacency[g.index[to]], from)
	}
	g.edgeCount++
}

func (g *Graph) HasNode(id string) bool { _, ok := g.index[id]; return ok }

// Neighbors returns the out-neighbours in insertion order; the slice must not be modified.
func (g *Graph) Neighbors(id string) ([]string, error) {
	i, ok := g.index[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownNode, id)
	}
	return g.adjacency[i], nil
}

func (g *Graph) Nodes() []string { return append([]string(nil), g.order...) }
func (g *Graph) NodeCount() int  { return len(g.order) }
func (g *Graph) EdgeCount() int  { return g.edgeCount }

// UndirectedView is the same nodes with every edge made two-way. Components are defined on this view.
func (g *Graph) UndirectedView() *Graph {
	if !g.directed {
		return g
	}
	u := NewGraph(false)
	for _, n := range g.order {
		u.AddNode(n)
	}
	for i, from := range g.order {
		for _, to := range g.adjacency[i] {
			u.AddEdge(from, to)
		}
	}
	return u
}

// BfsResult is what BFS leaves behind: hop distance and parent for every reached node, and the visit order.
type BfsResult struct {
	Distance map[string]int
	Parent   map[string]string
	Order    []string
}

func (r BfsResult) Reached(id string) bool { _, ok := r.Distance[id]; return ok }

// Color is the DFS colouring for directed-cycle detection: unvisited, on the current path, finished.
type Color int

const (
	White Color = iota
	Gray
	Black
)

// Bfs is breadth-first from source. Each node is enqueued once, when first discovered; distance = hops.
func Bfs(g *Graph, source string) (BfsResult, error) {
	if !g.HasNode(source) {
		return BfsResult{}, fmt.Errorf("%w: %s", ErrUnknownNode, source)
	}
	distance := map[string]int{source: 0}
	parent := map[string]string{}
	var order []string
	queue := []string{source}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		order = append(order, u)
		ns, _ := g.Neighbors(u)
		for _, v := range ns {
			if _, seen := distance[v]; !seen { // discovered: mark BEFORE enqueueing, never after dequeueing
				distance[v] = distance[u] + 1
				parent[v] = u
				queue = append(queue, v)
			}
		}
	}
	return BfsResult{distance, parent, order}, nil
}

// ShortestHopPath is the fewest-hops path from source to target by following BFS parents back; empty if unreachable.
func ShortestHopPath(g *Graph, source, target string) ([]string, error) {
	r, err := Bfs(g, source)
	if err != nil {
		return nil, err
	}
	if !r.Reached(target) {
		return []string{}, nil
	}
	var path []string
	for at, ok := target, true; ok; at, ok = r.Parent[at] {
		path = append([]string{at}, path...)
	}
	return path, nil
}

// DfsIterative is depth-first preorder with an explicit stack. Neighbours are pushed in reverse so the
// visit order matches the recursive version, without the recursive version's stack-depth limit.
func DfsIterative(g *Graph, source string) ([]string, error) {
	if !g.HasNode(source) {
		return nil, fmt.Errorf("%w: %s", ErrUnknownNode, source)
	}
	var order []string
	visited := map[string]bool{}
	stack := []string{source}
	for len(stack) > 0 {
		u := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if visited[u] { // a node may be pushed many times; it is visited once
			continue
		}
		visited[u] = true
		order = append(order, u)
		ns, _ := g.Neighbors(u)
		for i := len(ns) - 1; i >= 0; i-- {
			if !visited[ns[i]] {
				stack = append(stack, ns[i])
			}
		}
	}
	return order, nil
}

// ConnectedComponents returns the components of the undirected view, each in discovery order, components in node order.
func ConnectedComponents(g *Graph) [][]string {
	u := g.UndirectedView()
	seen := map[string]bool{}
	var components [][]string
	for _, start := range u.order {
		if seen[start] {
			continue
		}
		r, _ := Bfs(u, start)
		for _, n := range r.Order {
			seen[n] = true
		}
		components = append(components, r.Order)
	}
	return components
}

// FindCycle detects a directed cycle by three-colour DFS, iteratively. A Gray neighbour is an ancestor on
// the current path, so the edge closes a cycle; a Black neighbour is a finished node reached by a second
// route. Returns the cycle as a closed walk (first node repeated at the end), or nil if the graph is a DAG.
func FindCycle(g *Graph) ([]string, error) {
	if !g.directed {
		return nil, errors.New("FindCycle is defined for directed graphs")
	}
	color := map[string]Color{}
	for _, root := range g.order {
		if color[root] != White {
			continue
		}
		path := []string{root} // the Gray nodes, root first
		next := []int{0}       // per frame: index of the next neighbour to examine
		color[root] = Gray
		for len(path) > 0 {
			u := path[len(path)-1]
			i := next[len(next)-1]
			ns, _ := g.Neighbors(u)
			if i < len(ns) {
				next[len(next)-1] = i + 1
				v := ns[i]
				switch color[v] {
				case Gray:
					start := 0
					for path[start] != v {
						start++
					}
					cycle := append([]string(nil), path[start:]...)
					return append(cycle, v), nil
				case White:
					color[v] = Gray
					path = append(path, v)
					next = append(next, 0)
				}
			} else {
				color[u] = Black
				path = path[:len(path)-1]
				next = next[:len(next)-1]
			}
		}
	}
	return nil, nil
}

// Transfer is one transfer between two accounts: the edge of a money-flow graph.
type Transfer struct {
	From   string
	To     string
	Amount Money
}

// FromTransfers builds the directed graph with one edge per ordered (from, to) pair.
func FromTransfers(transfers []Transfer) *Graph {
	g := NewGraph(true)
	edges := map[[2]string]bool{}
	for _, t := range transfers {
		key := [2]string{t.From, t.To}
		if !edges[key] {
			edges[key] = true
			g.AddEdge(t.From, t.To)
		}
	}
	return g
}

// Volume totals the amount moved along each ordered pair; keys are returned in first-seen order.
// Mixed currencies are an error, not a sum.
func Volume(transfers []Transfer) ([]string, map[string]Money, error) {
	var keys []string
	out := map[string]Money{}
	for _, t := range transfers {
		key := t.From + "->" + t.To
		if cur, ok := out[key]; ok {
			sum, err := cur.Plus(t.Amount)
			if err != nil {
				return nil, nil, err
			}
			out[key] = sum
		} else {
			keys = append(keys, key)
			out[key] = t.Amount
		}
	}
	return keys, out, nil
}

// Rings returns components with at least minSize accounts: candidate rings or mule networks.
func Rings(g *Graph, minSize int) [][]string {
	var out [][]string
	for _, c := range ConnectedComponents(g) {
		if len(c) >= minSize {
			out = append(out, c)
		}
	}
	return out
}

// CircularFlow is a closed loop of transfers, if any: money that returns to where it started.
func CircularFlow(g *Graph) ([]string, error) { return FindCycle(g) }

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func main() {
	transfers := []Transfer{
		{"acct-A", "acct-B", OfMinor(50000, USD)},
		{"acct-B", "acct-C", OfMinor(48000, USD)},
		{"acct-C", "acct-A", OfMinor(47000, USD)}, // closes the ring A -> B -> C -> A
		{"acct-A", "acct-B", OfMinor(12000, USD)}, // second transfer on an existing edge
		{"acct-C", "acct-D", OfMinor(1000, USD)},
		{"acct-D", "acct-E", OfMinor(900, USD)},
		{"acct-A", "acct-D", OfMinor(500, USD)}, // shortcut: A reaches D in one hop
		{"payroll", "acct-X", OfMinor(250000, USD)},
		{"acct-X", "landlord", OfMinor(180000, USD)},
		{"acct-X", "grocer", OfMinor(9000, USD)},
	}
	g := FromTransfers(transfers)
	g.AddNode("dormant")
	fmt.Println("== Money-flow graph ==")
	fmt.Printf("%d accounts, %d distinct edges\n", g.NodeCount(), g.EdgeCount())
	keys, vol, _ := Volume(transfers)
	for _, k := range keys {
		fmt.Printf("  %s : %v\n", k, vol[k])
	}

	fmt.Println("\n== BFS from acct-A: hop distance to every reachable account ==")
	r := must(Bfs(g, "acct-A"))
	fmt.Printf("visit order : %v\n", r.Order)
	for _, n := range r.Order {
		fmt.Printf("  %s at %d hops\n", n, r.Distance[n])
	}
	fmt.Printf("path A -> E : %v\n", must(ShortestHopPath(g, "acct-A", "acct-E")))
	fmt.Printf("path A -> X : %v  (unreachable: empty)\n", must(ShortestHopPath(g, "acct-A", "acct-X")))

	fmt.Println("\n== Iterative DFS preorder from acct-A ==")
	fmt.Println(must(DfsIterative(g, "acct-A")))

	fmt.Println("\n== Connected components (direction ignored) ==")
	for _, c := range ConnectedComponents(g) {
		fmt.Printf("  size %d: %v\n", len(c), c)
	}
	fmt.Printf("rings of 3+ accounts: %v\n", Rings(g, 3))

	fmt.Println("\n== Directed cycle (three-colour DFS) ==")
	fmt.Printf("circular flow: %v\n", must(CircularFlow(g)))
	dag := NewGraph(true)
	dag.AddEdge("payroll", "acct-X")
	dag.AddEdge("acct-X", "landlord")
	dag.AddEdge("payroll", "landlord") // diamond: two routes to landlord, no cycle
	fmt.Printf("payroll graph has cycle: %v\n", must(FindCycle(dag)) != nil)

	fmt.Println("\n== 100,000-hop chain: iterative DFS does not overflow the stack ==")
	chain := NewGraph(true)
	for i := 0; i < 100_000; i++ {
		chain.AddEdge(fmt.Sprintf("n%d", i), fmt.Sprintf("n%d", i+1))
	}
	fmt.Printf("visited %d nodes\n", len(must(DfsIterative(chain, "n0"))))
}
