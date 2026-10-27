// Lesson: phases/02-algorithms/10-topological-sort-and-dags/docs/en.md
// Topological Sort and DAG Scheduling
// Run:  go run .
// Test: go test ./...
// Sources: Kahn, "Topological sorting of large networks" (CACM 1962); Tarjan, "Depth-first search and
// linear graph algorithms" (SIAM J. Comput. 1972); Kelley & Walker, "Critical-path planning and scheduling" (1959).
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Sentinel errors for argument problems; a cycle is reported through *CycleError instead.
var (
	ErrUnknownNode      = errors.New("unknown node")
	ErrNoDuration       = errors.New("no duration for task")
	ErrNegativeDuration = errors.New("duration must be non-negative")
	ErrOverflow         = errors.New("long overflow")
)

// Digraph is a directed graph over string-named nodes. Insertion order of nodes and edges is preserved so that
// every algorithm below produces the same answer on every run: a scheduler whose output changes
// between runs cannot be audited. Go maps iterate in randomized order, so the order lives in a slice.
type Digraph struct {
	order     []string
	adjacency map[string][]string
}

// NewDigraph returns an empty graph.
func NewDigraph() *Digraph { return &Digraph{adjacency: map[string][]string{}} }

// AddNode registers a node (idempotent) and returns the graph for chaining.
func (g *Digraph) AddNode(node string) *Digraph {
	if _, ok := g.adjacency[node]; !ok {
		g.adjacency[node] = []string{}
		g.order = append(g.order, node)
	}
	return g
}

// AddEdge adds the edge from -> to, meaning "from must finish before to can start".
func (g *Digraph) AddEdge(from, to string) *Digraph {
	g.AddNode(from)
	g.AddNode(to)
	if !slices.Contains(g.adjacency[from], to) {
		g.adjacency[from] = append(g.adjacency[from], to)
	}
	return g
}

// Nodes returns the nodes in insertion order. The slice is a copy; mutating it does not affect the graph.
func (g *Digraph) Nodes() []string { return slices.Clone(g.order) }

// Successors returns the out-neighbours of node in insertion order, or ErrUnknownNode.
func (g *Digraph) Successors(node string) ([]string, error) {
	out, ok := g.adjacency[node]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownNode, node)
	}
	return out, nil
}

// succ is Successors for a node the algorithms already know exists.
func (g *Digraph) succ(node string) []string { return g.adjacency[node] }

// NodeCount is the number of nodes.
func (g *Digraph) NodeCount() int { return len(g.order) }

// EdgeCount is the number of edges.
func (g *Digraph) EdgeCount() int {
	n := 0
	for _, out := range g.adjacency {
		n += len(out)
	}
	return n
}

// InDegrees maps every node to its in-degree.
func (g *Digraph) InDegrees() map[string]int {
	in := make(map[string]int, len(g.order))
	for _, node := range g.order {
		in[node] = 0
	}
	for _, out := range g.adjacency {
		for _, to := range out {
			in[to]++
		}
	}
	return in
}

// CycleError is returned when a graph that must be a DAG contains a cycle. It carries the cycle so the caller can report it.
type CycleError struct {
	// Cycle is the nodes of one cycle, in edge order, without repeating the first node at the end.
	Cycle []string
}

func (e *CycleError) Error() string {
	return "cycle detected: " + strings.Join(e.Cycle, " -> ") + " -> " + e.Cycle[0]
}

// --- Topological orderings of a DAG, with cycle detection that reports the offending cycle. ---

// Kahn repeatedly emits a node with in-degree zero and decrements its successors.
// Ties are broken by node insertion order (the ready queue is FIFO), so the output is deterministic.
// O(V + E) time, O(V) extra space. Returns a *CycleError if some node is never emitted.
func Kahn(g *Digraph) ([]string, error) {
	inDegree := g.InDegrees()
	var ready []string
	for _, node := range g.order {
		if inDegree[node] == 0 {
			ready = append(ready, node)
		}
	}
	order := make([]string, 0, g.NodeCount())
	for head := 0; head < len(ready); head++ { // ready[head:] is the FIFO queue
		node := ready[head]
		order = append(order, node)
		for _, next := range g.succ(node) {
			inDegree[next]--
			if inDegree[next] == 0 {
				ready = append(ready, next)
			}
		}
	}
	if len(order) != g.NodeCount() {
		// Every node still holding in-degree > 0 lies on or downstream of a cycle; find one to report.
		return nil, &CycleError{FindCycle(g)}
	}
	return order, nil
}

type color int

const (
	white color = iota
	gray
	black
)

type frame struct {
	node string
	next int
}

// DFS is the depth-first topological sort: a node is finished only after all of its successors are finished,
// so the reverse of finishing order is a topological order. Iterative, so a 100k-node chain does not
// depend on the goroutine stack. Returns a *CycleError with the back-edge cycle if one is found.
func DFS(g *Digraph) ([]string, error) {
	colors := make(map[string]color, g.NodeCount())
	parent := map[string]string{}
	finished := make([]string, 0, g.NodeCount())

	for _, root := range g.order {
		if colors[root] != white {
			continue
		}
		stack := []frame{{root, 0}}
		colors[root] = gray
		for len(stack) > 0 {
			top := &stack[len(stack)-1]
			succ := g.succ(top.node)
			if top.next < len(succ) {
				child := succ[top.next]
				top.next++
				switch colors[child] {
				case white:
					colors[child] = gray
					parent[child] = top.node
					stack = append(stack, frame{child, 0})
				case gray:
					return nil, &CycleError{walkBack(top.node, child, parent)}
				}
				// black: already finished, a cross or forward edge, safe to ignore
			} else {
				colors[top.node] = black
				finished = append(finished, top.node)
				stack = stack[:len(stack)-1]
			}
		}
	}
	slices.Reverse(finished)
	return finished, nil
}

// FindCycle returns one cycle of g in edge order, or an empty slice if g is acyclic.
func FindCycle(g *Digraph) []string {
	_, err := DFS(g)
	var ce *CycleError
	if errors.As(err, &ce) {
		return ce.Cycle
	}
	return []string{}
}

// walkBack goes from the gray node `from` back up the DFS tree to `to`, then reverses into edge order.
func walkBack(from, to string, parent map[string]string) []string {
	var path []string
	for cur := from; cur != to; cur = parent[cur] {
		path = append(path, cur)
	}
	path = append(path, to)
	slices.Reverse(path)
	return path
}

// IsTopologicalOrder is true if `order` lists every node of g exactly once and every edge points forward.
func IsTopologicalOrder(g *Digraph, order []string) bool {
	if len(order) != g.NodeCount() {
		return false
	}
	pos := make(map[string]int, len(order))
	for i, n := range order {
		if _, dup := pos[n]; dup {
			return false
		}
		pos[n] = i
	}
	for _, from := range g.order {
		pf, ok := pos[from]
		if !ok {
			return false
		}
		for _, to := range g.succ(from) {
			if pos[to] <= pf {
				return false
			}
		}
	}
	return true
}

// Task is a task with a duration in whole time units (minutes, here). Durations are integers, like money.
type Task struct {
	Name    string
	Minutes int64
}

// NewTask validates the duration.
func NewTask(name string, minutes int64) (Task, error) {
	if minutes < 0 {
		return Task{}, fmt.Errorf("%w: %d", ErrNegativeDuration, minutes)
	}
	return Task{name, minutes}, nil
}

// Slot is one task's schedule: earliest start, earliest finish, latest start without delaying the end, and slack.
type Slot struct {
	Task           string
	EarliestStart  int64
	EarliestFinish int64
	LatestStart    int64
	Slack          int64
}

// IsCritical is true when the task has no slack.
func (s Slot) IsCritical() bool { return s.Slack == 0 }

// Schedule is the result of scheduling a DAG of tasks: per-task slots, the makespan, and the critical path.
type Schedule struct {
	Slots        map[string]Slot // iterate with the graph's Nodes() for insertion order
	Makespan     int64
	CriticalPath []string
}

// DagScheduler computes parallel levels (which jobs can run together) and the critical path (the longest
// chain of durations, which is the shortest possible total time no matter how many workers you add).
type DagScheduler struct {
	graph *Digraph
	tasks map[string]Task
}

// NewDagScheduler checks that every node has a duration.
func NewDagScheduler(graph *Digraph, taskList []Task) (*DagScheduler, error) {
	tasks := make(map[string]Task, len(taskList))
	for _, t := range taskList {
		tasks[t.Name] = t
	}
	for _, n := range graph.order {
		if _, ok := tasks[n]; !ok {
			return nil, fmt.Errorf("%w %s", ErrNoDuration, n)
		}
	}
	return &DagScheduler{graph, tasks}, nil
}

// Levels groups tasks: level 0 has no prerequisites; level k's prerequisites are all at levels < k.
// Every task in one level may run concurrently. The number of levels is the length of the longest
// chain of edges, which is the minimum number of sequential rounds.
func (s *DagScheduler) Levels() ([][]string, error) {
	order, err := Kahn(s.graph)
	if err != nil {
		return nil, err
	}
	depth := make(map[string]int, len(order))
	maxDepth := -1
	for _, n := range order {
		d := 0
		for _, from := range s.graph.order {
			if slices.Contains(s.graph.succ(from), n) {
				d = max(d, depth[from]+1)
			}
		}
		depth[n] = d
		maxDepth = max(maxDepth, d)
	}
	out := make([][]string, maxDepth+1)
	for i := range out {
		out[i] = []string{}
	}
	for _, n := range s.graph.order {
		out[depth[n]] = append(out[depth[n]], n)
	}
	return out, nil
}

// Schedule runs the forward pass in topological order for earliest start/finish and the backward pass in reverse
// order for latest start; slack = latestStart - earliestStart. Tasks with zero slack form the critical path. O(V + E).
func (s *DagScheduler) Schedule() (Schedule, error) {
	order, err := Kahn(s.graph)
	if err != nil {
		return Schedule{}, err
	}
	earliestStart := map[string]int64{}
	earliestFinish := map[string]int64{}
	criticalPredecessor := map[string]string{}
	var makespan int64
	for _, n := range order {
		start := int64(-1)
		for _, from := range s.predecessors(n) {
			if f := earliestFinish[from]; f > start { // strict: first predecessor wins ties, deterministically
				start = f
				criticalPredecessor[n] = from
			}
		}
		if start < 0 { // no predecessors: can start at time zero
			start = 0
		}
		earliestStart[n] = start
		finish, err := addChecked(start, s.tasks[n].Minutes)
		if err != nil {
			return Schedule{}, err
		}
		earliestFinish[n] = finish
		makespan = max(makespan, finish)
	}

	latestFinish := map[string]int64{}
	for i := len(order) - 1; i >= 0; i-- {
		n := order[i]
		lf := makespan
		for _, to := range s.graph.succ(n) {
			lf = min(lf, latestFinish[to]-s.tasks[to].Minutes)
		}
		latestFinish[n] = lf
	}

	slots := make(map[string]Slot, len(order))
	for _, n := range s.graph.order {
		es := earliestStart[n]
		ls := latestFinish[n] - s.tasks[n].Minutes
		slots[n] = Slot{n, es, earliestFinish[n], ls, ls - es}
	}

	// Walk back from the latest-finishing task along critical predecessors.
	end, found := "", false
	for _, n := range order {
		if earliestFinish[n] == makespan && !found {
			end, found = n, true
		}
	}
	var critical []string
	seen := map[string]bool{}
	for cur, ok := end, found; ok && !seen[cur]; cur, ok = criticalPredecessor[cur] {
		seen[cur] = true
		critical = append(critical, cur)
	}
	slices.Reverse(critical)
	if critical == nil {
		critical = []string{}
	}
	return Schedule{slots, makespan, critical}, nil
}

func (s *DagScheduler) predecessors(node string) []string {
	var preds []string
	for _, from := range s.graph.order {
		if slices.Contains(s.graph.succ(from), node) {
			preds = append(preds, from)
		}
	}
	return preds
}

// addChecked is Math.addExact: int64 addition that reports overflow instead of wrapping.
func addChecked(a, b int64) (int64, error) {
	sum := a + b
	if (a >= 0) == (b >= 0) && (sum >= 0) != (a >= 0) {
		return 0, ErrOverflow
	}
	return sum, nil
}

// jstr formats a slice as Java's List.toString does: [a, b, c].
func jstr(xs []string) string { return "[" + strings.Join(xs, ", ") + "]" }

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// SettlementGraph is the end-of-day settlement batch used throughout the lesson text.
func SettlementGraph() *Digraph {
	return NewDigraph().
		AddEdge("ingest-captures", "compute-fees").
		AddEdge("ingest-captures", "fx-fixing").
		AddEdge("fetch-fx-rates", "fx-fixing").
		AddEdge("compute-fees", "net-positions").
		AddEdge("fx-fixing", "net-positions").
		AddEdge("net-positions", "generate-payout-file").
		AddEdge("net-positions", "post-ledger").
		AddEdge("post-ledger", "reconcile").
		AddEdge("generate-payout-file", "submit-to-bank").
		AddEdge("reconcile", "submit-to-bank")
}

// SettlementTasks are the durations for SettlementGraph.
func SettlementTasks() []Task {
	return []Task{
		{"ingest-captures", 20},
		{"fetch-fx-rates", 5},
		{"compute-fees", 15},
		{"fx-fixing", 10},
		{"net-positions", 25},
		{"generate-payout-file", 10},
		{"post-ledger", 30},
		{"reconcile", 20},
		{"submit-to-bank", 5}}
}

func main() {
	g := SettlementGraph()
	fmt.Printf("== Settlement batch: %d jobs, %d dependencies ==\n", g.NodeCount(), g.EdgeCount())
	kahn := must(Kahn(g))
	dfs := must(DFS(g))
	fmt.Println("Kahn order: " + jstr(kahn))
	fmt.Println("DFS order:  " + jstr(dfs))
	fmt.Printf("both valid: %t %t\n", IsTopologicalOrder(g, kahn), IsTopologicalOrder(g, dfs))

	fmt.Println()
	fmt.Println("== Parallel levels (jobs in one level can run together) ==")
	scheduler := must(NewDagScheduler(g, SettlementTasks()))
	for i, level := range must(scheduler.Levels()) {
		fmt.Printf("level %d: %s\n", i, jstr(level))
	}

	fmt.Println()
	fmt.Println("== Critical path ==")
	s := must(scheduler.Schedule())
	fmt.Printf("%-22s %5s %5s %5s %5s\n", "task", "ES", "EF", "LS", "slack")
	for _, n := range g.Nodes() {
		slot := s.Slots[n]
		mark := ""
		if slot.IsCritical() {
			mark = "  <- critical"
		}
		fmt.Printf("%-22s %5d %5d %5d %5d%s\n", slot.Task, slot.EarliestStart, slot.EarliestFinish, slot.LatestStart, slot.Slack, mark)
	}
	fmt.Printf("makespan = %d minutes; critical path = %s\n", s.Makespan, jstr(s.CriticalPath))

	fmt.Println()
	fmt.Println("== A cycle: reconcile now waits for submit-to-bank ==")
	broken := SettlementGraph().AddEdge("submit-to-bank", "reconcile")
	if _, err := Kahn(broken); err != nil {
		fmt.Println("rejected: " + err.Error())
	}
}
