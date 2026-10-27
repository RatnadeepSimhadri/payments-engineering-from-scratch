// Lesson: phases/02-algorithms/11-minimum-spanning-trees/docs/en.md
// Minimum Spanning Trees: Kruskal and Prim
// Run:  go run .
// Test: go test ./...
// Sources: Kruskal, "On the shortest spanning subtree of a graph" (Proc. AMS 1956); Prim, "Shortest connection
// networks and some generalizations" (Bell System Tech. J. 1957); Tarjan, "Efficiency of a good but not linear
// set union algorithm" (JACM 1975) for union-find complexity. Go 1.22+, standard library only. Same design as Java/Python.
package main

import (
	"cmp"
	"container/heap"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Sentinel errors: Go reports what Java throws.
var (
	ErrSelfLoop       = errors.New("self-loop can never be in a spanning tree")
	ErrNegativeWeight = errors.New("weight must be non-negative")
	ErrUnknownNode    = errors.New("unknown node")
	ErrBadK           = errors.New("k must be >= 1")
	ErrOverflow       = errors.New("long overflow")
)

// WeightedEdge is an undirected edge with an integer weight (cost in whole currency minor units, metres, milliseconds).
type WeightedEdge struct {
	U, V   string
	Weight int64
}

// NewWeightedEdge validates the endpoints and weight.
func NewWeightedEdge(u, v string, weight int64) (WeightedEdge, error) {
	if u == v {
		return WeightedEdge{}, fmt.Errorf("%w: %s", ErrSelfLoop, u)
	}
	if weight < 0 {
		return WeightedEdge{}, fmt.Errorf("%w: %d", ErrNegativeWeight, weight)
	}
	return WeightedEdge{u, v, weight}, nil
}

// Other is the endpoint that is not `node`. Asking about a node that is not an endpoint is a programming error.
func (e WeightedEdge) Other(node string) string {
	switch node {
	case e.U:
		return e.V
	case e.V:
		return e.U
	}
	panic(node + " is not an endpoint of " + e.String())
}

// SameEndpoints: two edges are the same connection regardless of which endpoint was listed first.
func (e WeightedEdge) SameEndpoints(o WeightedEdge) bool {
	return (e.U == o.U && e.V == o.V) || (e.U == o.V && e.V == o.U)
}

func (e WeightedEdge) String() string {
	return fmt.Sprintf("WeightedEdge[u=%s, v=%s, weight=%d]", e.U, e.V, e.Weight)
}

// WeightedGraph is an undirected weighted graph. Insertion order of nodes and edges is preserved so tie-breaks are
// reproducible; Go maps iterate in randomized order, so the node order lives in a slice.
type WeightedGraph struct {
	order     []string
	adjacency map[string][]WeightedEdge
	edges     []WeightedEdge
}

// NewWeightedGraph returns an empty graph.
func NewWeightedGraph() *WeightedGraph { return &WeightedGraph{adjacency: map[string][]WeightedEdge{}} }

// AddNode registers a node (idempotent) and returns the graph for chaining.
func (g *WeightedGraph) AddNode(node string) *WeightedGraph {
	if _, ok := g.adjacency[node]; !ok {
		g.adjacency[node] = []WeightedEdge{}
		g.order = append(g.order, node)
	}
	return g
}

// AddEdge adds an undirected edge and returns the graph for chaining. An invalid edge (self-loop, negative weight)
// is a programming error in the graph literal and panics; validate untrusted input with NewWeightedEdge first.
func (g *WeightedGraph) AddEdge(u, v string, weight int64) *WeightedGraph {
	e, err := NewWeightedEdge(u, v, weight)
	if err != nil {
		panic(err)
	}
	g.AddNode(u)
	g.AddNode(v)
	g.edges = append(g.edges, e)
	g.adjacency[u] = append(g.adjacency[u], e)
	g.adjacency[v] = append(g.adjacency[v], e)
	return g
}

// Nodes returns the nodes in insertion order (a copy).
func (g *WeightedGraph) Nodes() []string { return slices.Clone(g.order) }

// Edges returns the edges in insertion order (a copy).
func (g *WeightedGraph) Edges() []WeightedEdge { return slices.Clone(g.edges) }

// Incident returns the edges touching node, or ErrUnknownNode.
func (g *WeightedGraph) Incident(node string) ([]WeightedEdge, error) {
	out, ok := g.adjacency[node]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownNode, node)
	}
	return out, nil
}

// HasNode reports whether node is in the graph.
func (g *WeightedGraph) HasNode(node string) bool { _, ok := g.adjacency[node]; return ok }

// NodeCount is the number of nodes.
func (g *WeightedGraph) NodeCount() int { return len(g.order) }

// EdgeCount is the number of edges.
func (g *WeightedGraph) EdgeCount() int { return len(g.edges) }

// UnionFind is a disjoint-set forest with union by rank and path compression (Phase 1, Lesson 10). Amortized
// O(alpha(n)) per operation, effectively constant. Element names are strings; the parent map is the forest.
type UnionFind struct {
	parent     map[string]string
	rank       map[string]int
	components int
}

// NewUnionFind puts every element in its own set.
func NewUnionFind(elements []string) *UnionFind {
	uf := &UnionFind{parent: map[string]string{}, rank: map[string]int{}}
	for _, e := range elements {
		uf.parent[e] = e
		uf.rank[e] = 0
		uf.components++
	}
	return uf
}

// Find is the representative of e's set. Path compression: every node on the way up is re-pointed at the root.
func (uf *UnionFind) Find(e string) string {
	root := e
	for uf.parent[root] != root {
		root = uf.parent[root]
	}
	for uf.parent[e] != root {
		next := uf.parent[e]
		uf.parent[e] = root
		e = next
	}
	return root
}

// Union merges the sets of a and b. Returns false if they were already the same set (an edge between them would close a cycle).
func (uf *UnionFind) Union(a, b string) bool {
	ra, rb := uf.Find(a), uf.Find(b)
	if ra == rb {
		return false
	}
	rka, rkb := uf.rank[ra], uf.rank[rb]
	switch {
	case rka < rkb:
		uf.parent[ra] = rb
	case rka > rkb:
		uf.parent[rb] = ra
	default:
		uf.parent[rb] = ra
		uf.rank[ra] = rka + 1
	}
	uf.components--
	return true
}

// Connected is true when a and b share a set.
func (uf *UnionFind) Connected(a, b string) bool { return uf.Find(a) == uf.Find(b) }

// Components is the number of disjoint sets.
func (uf *UnionFind) Components() int { return uf.components }

// SpanningForest is the output of either algorithm: the chosen edges, their total weight, and how many trees the forest has.
type SpanningForest struct {
	Edges          []WeightedEdge
	TotalWeight    int64
	ComponentCount int
}

// IsTree is true when the forest is a single tree, i.e. the input graph was connected.
func (f SpanningForest) IsTree() bool { return f.ComponentCount == 1 }

// Kruskal: sort edges by weight (stable, so equal weights keep insertion order), take each edge whose
// endpoints are in different components, skip the rest. O(E log E) for the sort; the union-find work
// is O(E alpha(V)). Correct by the cut property: the lightest edge crossing any cut is in some MST.
func Kruskal(g *WeightedGraph) (SpanningForest, error) {
	sorted := g.Edges()
	slices.SortStableFunc(sorted, func(a, b WeightedEdge) int { return cmp.Compare(a.Weight, b.Weight) })
	uf := NewUnionFind(g.order)
	chosen := []WeightedEdge{}
	var total int64
	for _, e := range sorted {
		if len(chosen) == g.NodeCount()-1 { // a tree on V nodes has V-1 edges
			break
		}
		if uf.Union(e.U, e.V) {
			chosen = append(chosen, e)
			var err error
			if total, err = addChecked(total, e.Weight); err != nil {
				return SpanningForest{}, err
			}
		}
	}
	return SpanningForest{chosen, total, uf.Components()}, nil
}

// frontierEntry is (weight, insertion index) so equal weights pop in a reproducible order.
type frontierEntry struct {
	weight int64
	index  int
}

type frontierHeap []frontierEntry

func (h frontierHeap) Len() int { return len(h) }
func (h frontierHeap) Less(i, j int) bool {
	return cmp.Or(cmp.Compare(h[i].weight, h[j].weight), cmp.Compare(h[i].index, h[j].index)) < 0
}
func (h frontierHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *frontierHeap) Push(x any)   { *h = append(*h, x.(frontierEntry)) }
func (h *frontierHeap) Pop() any     { old := *h; v := old[len(old)-1]; *h = old[:len(old)-1]; return v }

// Prim: grow one tree from a start node, always adding the lightest edge that leaves the tree. The
// frontier is a binary heap of candidate edges with lazy deletion: stale entries (both endpoints
// already in the tree) are discarded when popped. O(E log E). For a disconnected graph, restarts
// from the first unvisited node so the result is a forest comparable with Kruskal's.
func Prim(g *WeightedGraph, start string) (SpanningForest, error) {
	if !g.HasNode(start) {
		return SpanningForest{}, fmt.Errorf("%w: unknown start node %s", ErrUnknownNode, start)
	}
	inTree := map[string]bool{}
	chosen := []WeightedEdge{}
	var total int64
	components := 0

	roots := []string{start}
	for _, n := range g.order {
		if n != start {
			roots = append(roots, n)
		}
	}

	for _, root := range roots {
		if inTree[root] {
			continue
		}
		components++
		inTree[root] = true
		h := &frontierHeap{}
		var byIndex []WeightedEdge
		byIndex = pushFrontier(g, root, inTree, h, byIndex)
		for h.Len() > 0 {
			top := heap.Pop(h).(frontierEntry)
			e := byIndex[top.index]
			var next string
			switch {
			case !inTree[e.U]:
				next = e.U
			case !inTree[e.V]:
				next = e.V
			default:
				continue // stale: both ends already in the tree
			}
			inTree[next] = true
			chosen = append(chosen, e)
			var err error
			if total, err = addChecked(total, e.Weight); err != nil {
				return SpanningForest{}, err
			}
			byIndex = pushFrontier(g, next, inTree, h, byIndex)
		}
	}
	return SpanningForest{chosen, total, components}, nil
}

func pushFrontier(g *WeightedGraph, node string, inTree map[string]bool, h *frontierHeap, byIndex []WeightedEdge) []WeightedEdge {
	for _, e := range g.adjacency[node] {
		if inTree[e.Other(node)] {
			continue
		}
		byIndex = append(byIndex, e)
		heap.Push(h, frontierEntry{e.Weight, len(byIndex) - 1})
	}
	return byIndex
}

// IsMinimumSpanningForest is an independent check of a candidate forest: it uses only edges of g, has exactly
// V - components edges, connects everything that g connects, and every edge is a lightest edge across the cut it
// closes. Brute force O(E * V) per edge; for tests and reviews, not for production.
func IsMinimumSpanningForest(g *WeightedGraph, f SpanningForest) bool {
	if len(f.Edges) != g.NodeCount()-f.ComponentCount {
		return false
	}
	all := NewUnionFind(g.order)
	for _, e := range g.edges {
		all.Union(e.U, e.V)
	}
	if all.Components() != f.ComponentCount {
		return false
	}

	forest := NewUnionFind(g.order)
	for _, e := range f.Edges {
		if !slices.Contains(g.edges, e) {
			return false
		}
		if !forest.Union(e.U, e.V) { // a cycle: not a forest
			return false
		}
	}
	// Cut property: removing edge e splits its tree into two sides; no edge of g across that split is lighter.
	// Edges are skipped by position, not by value, so parallel equal edges stay distinct (Java compares references here).
	for ri, removed := range f.Edges {
		without := NewUnionFind(g.order)
		for i, e := range f.Edges {
			if i != ri {
				without.Union(e.U, e.V)
			}
		}
		side := without.Find(removed.U)
		for _, e := range g.edges {
			crosses := (without.Find(e.U) == side) != (without.Find(e.V) == side)
			if crosses && e.Weight < removed.Weight {
				return false
			}
		}
	}
	return true
}

// Clusters is single-linkage clustering: the MST with its k - 1 heaviest edges removed is the k-clustering that
// maximizes the minimum distance between clusters. Returns the clusters as slices of nodes in insertion order.
func Clusters(g *WeightedGraph, k int) ([][]string, error) {
	if k < 1 {
		return nil, ErrBadK
	}
	f, err := Kruskal(g)
	if err != nil {
		return nil, err
	}
	keep := slices.Clone(f.Edges)
	slices.SortStableFunc(keep, func(a, b WeightedEdge) int { return cmp.Compare(a.Weight, b.Weight) })
	toRemove := min(k-f.ComponentCount, len(keep))
	if toRemove > 0 {
		keep = keep[:len(keep)-toRemove]
	}
	uf := NewUnionFind(g.order)
	for _, e := range keep {
		uf.Union(e.U, e.V)
	}
	var groupOrder []string
	groups := map[string][]string{}
	for _, n := range g.order {
		root := uf.Find(n)
		if _, seen := groups[root]; !seen {
			groupOrder = append(groupOrder, root)
		}
		groups[root] = append(groups[root], n)
	}
	out := make([][]string, 0, len(groupOrder))
	for _, root := range groupOrder {
		out = append(out, groups[root])
	}
	return out, nil
}

// addChecked is Math.addExact: int64 addition that reports overflow instead of wrapping.
func addChecked(a, b int64) (int64, error) {
	sum := a + b
	if (a >= 0) == (b >= 0) && (sum >= 0) != (a >= 0) {
		return 0, ErrOverflow
	}
	return sum, nil
}

// SameEdgeSet is true when the two forests contain the same connections with the same weights.
func SameEdgeSet(a, b SpanningForest) bool {
	if len(a.Edges) != len(b.Edges) {
		return false
	}
	for _, e := range a.Edges {
		found := false
		for _, f := range b.Edges {
			if e.SameEndpoints(f) && e.Weight == f.Weight {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// SiteNetwork: eight processing sites and the monthly cost, in whole dollars, of a leased line between each pair that is feasible.
func SiteNetwork() *WeightedGraph {
	return NewWeightedGraph().
		AddEdge("ashburn", "newyork", 4).
		AddEdge("ashburn", "chicago", 7).
		AddEdge("newyork", "chicago", 9).
		AddEdge("newyork", "london", 12).
		AddEdge("chicago", "dallas", 6).
		AddEdge("chicago", "london", 15).
		AddEdge("dallas", "phoenix", 5).
		AddEdge("dallas", "london", 20).
		AddEdge("phoenix", "sanjose", 3).
		AddEdge("sanjose", "chicago", 11).
		AddEdge("london", "frankfurt", 2).
		AddEdge("sanjose", "frankfurt", 25)
}

func jstr2(xss [][]string) string {
	parts := make([]string, len(xss))
	for i, xs := range xss {
		parts[i] = "[" + strings.Join(xs, ", ") + "]"
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func main() {
	g := SiteNetwork()
	fmt.Printf("== Site network: %d sites, %d feasible lines ==\n", g.NodeCount(), g.EdgeCount())
	var all int64
	for _, e := range g.Edges() {
		all += e.Weight
	}
	fmt.Println("cost of leasing every line:", all)

	fmt.Println()
	fmt.Println("== Kruskal: lightest edges first, skip any that closes a cycle ==")
	k := must(Kruskal(g))
	for _, e := range k.Edges {
		fmt.Printf("  take %-9s - %-9s %3d\n", e.U, e.V, e.Weight)
	}
	fmt.Printf("total %d, tree: %t, verified minimum: %t\n", k.TotalWeight, k.IsTree(), IsMinimumSpanningForest(g, k))

	fmt.Println()
	fmt.Println("== Prim from ashburn: grow one tree, always the lightest edge leaving it ==")
	p := must(Prim(g, "ashburn"))
	for _, e := range p.Edges {
		fmt.Printf("  take %-9s - %-9s %3d\n", e.U, e.V, e.Weight)
	}
	fmt.Printf("total %d, same edge set as Kruskal: %t\n", p.TotalWeight, SameEdgeSet(k, p))

	fmt.Println()
	fmt.Println("== Disconnected: drop the four transatlantic lines ==")
	split := NewWeightedGraph()
	for _, e := range g.Edges() {
		transatlantic := e.SameEndpoints(WeightedEdge{"newyork", "london", 0}) ||
			e.SameEndpoints(WeightedEdge{"chicago", "london", 0}) ||
			e.SameEndpoints(WeightedEdge{"dallas", "london", 0}) ||
			e.SameEndpoints(WeightedEdge{"sanjose", "frankfurt", 0})
		if !transatlantic {
			split.AddEdge(e.U, e.V, e.Weight)
		}
	}
	forest := must(Kruskal(split))
	fmt.Printf("components: %d, edges: %d, total %d\n", forest.ComponentCount, len(forest.Edges), forest.TotalWeight)

	fmt.Println()
	fmt.Println("== Single-linkage clustering: cut the 2 heaviest MST edges for 3 clusters ==")
	fmt.Println(jstr2(must(Clusters(g, 3))))
}
