// Lesson: phases/06-replication-and-consensus/12-linearizability-checking/docs/en.md
// Linearizability Checking with Recorded Histories
// Run:  go run .
// Test: go test ./...
// Sources: Herlihy & Wing, "Linearizability: A Correctness Condition for Concurrent Objects" (TOPLAS 1990);
//
//	Wing & Gong, "Testing and Verifying Concurrent Objects" (JPDC 1993); Lowe, "Testing for Linearizability" (CCPE 2017).
//
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"math/bits"
	"sort"
	"strings"
)

type OpKind int

const (
	Read OpKind = iota
	Write
)

// Consistency is the model the checker searches for. Same search; only the "may go next" rule differs.
type Consistency int

const (
	Linearizable Consistency = iota
	Sequential
)

func (c Consistency) String() string { return [...]string{"LINEARIZABLE", "SEQUENTIAL"}[c] }

// Op is one completed client operation on a key-value register. For a Write, Value is what was written; for a Read,
// Value is what the client got back. InvokedAt and ReturnedAt are the client's own timestamps.
type Op struct {
	ID         int
	Client     string
	Kind       OpKind
	Key        string
	Value      int
	InvokedAt  int
	ReturnedAt int
}

func (o Op) Precedes(other Op) bool { return o.ReturnedAt < other.InvokedAt }

func (o Op) ConcurrentWith(other Op) bool { return !o.Precedes(other) && !other.Precedes(o) }

func (o Op) String() string {
	body := fmt.Sprintf("read(%s)->%d", o.Key, o.Value)
	if o.Kind == Write {
		body = fmt.Sprintf("write(%s=%d)", o.Key, o.Value)
	}
	return fmt.Sprintf("#%d %s %s [%d,%d]", o.ID, o.Client, body, o.InvokedAt, o.ReturnedAt)
}

// MaxOps bounds a history so the set of linearized ops fits in a uint64 bitmask.
const MaxOps = 64

var ErrBadOp = errors.New("invalid op")

// History is an ordered collection of completed ops with dense ids 0..n-1.
type History struct{ ops []Op }

func NewHistory() *History { return &History{} }

// Add appends an op or returns an error (return before invoke, or too many ops). Write/Read panic on error because
// hand-built histories in demos and tests are programmer input.
func (h *History) Add(client string, kind OpKind, key string, value, invokedAt, returnedAt int) error {
	if returnedAt < invokedAt {
		return fmt.Errorf("%w: op %d returned before it was invoked", ErrBadOp, len(h.ops))
	}
	if len(h.ops) >= MaxOps {
		return fmt.Errorf("%w: this checker handles at most %d ops per history", ErrBadOp, MaxOps)
	}
	h.ops = append(h.ops, Op{len(h.ops), client, kind, key, value, invokedAt, returnedAt})
	return nil
}

func (h *History) Write(client, key string, value, invokedAt, returnedAt int) *History {
	if err := h.Add(client, Write, key, value, invokedAt, returnedAt); err != nil {
		panic(err)
	}
	return h
}

func (h *History) Read(client, key string, value, invokedAt, returnedAt int) *History {
	if err := h.Add(client, Read, key, value, invokedAt, returnedAt); err != nil {
		panic(err)
	}
	return h
}

func (h *History) Ops() []Op    { return append([]Op(nil), h.ops...) }
func (h *History) Size() int    { return len(h.ops) }
func (h *History) Op(id int) Op { return h.ops[id] }

// FakeClock is the injected clock: the recorder never reads time.Now, so recorded histories are reproducible.
type FakeClock struct{ now int }

func (c *FakeClock) Now() int          { return c.now }
func (c *FakeClock) Tick() int         { c.now++; return c.now }
func (c *FakeClock) Advance(ticks int) { c.now += ticks }

type pending struct {
	client    string
	kind      OpKind
	key       string
	value     int
	invokedAt int
}

// HistoryRecorder records a history the way a test harness would: stamp before the request leaves, stamp after the
// response arrives.
type HistoryRecorder struct {
	clock     *FakeClock
	pending   map[int]pending
	completed []Op
	nextID    int
}

func NewHistoryRecorder(clock *FakeClock) *HistoryRecorder {
	return &HistoryRecorder{clock: clock, pending: map[int]pending{}}
}

func (r *HistoryRecorder) InvokeWrite(client, key string, value int) int {
	return r.invoke(client, Write, key, value)
}
func (r *HistoryRecorder) InvokeRead(client, key string) int { return r.invoke(client, Read, key, 0) }

func (r *HistoryRecorder) invoke(client string, kind OpKind, key string, value int) int {
	id := r.nextID
	r.nextID++
	r.pending[id] = pending{client, kind, key, value, r.clock.Now()}
	return id
}

// Complete finishes a write (value ignored) or a read (value is what came back). The return timestamp is taken now.
func (r *HistoryRecorder) Complete(id, value int) error {
	p, ok := r.pending[id]
	if !ok {
		return fmt.Errorf("no pending op %d", id)
	}
	delete(r.pending, id)
	v := value
	if p.kind == Write {
		v = p.value
	}
	r.completed = append(r.completed, Op{id, p.client, p.kind, p.key, v, p.invokedAt, r.clock.Now()})
	return nil
}

func (r *HistoryRecorder) PendingCount() int { return len(r.pending) }

// History returns the ops so far, renumbered densely in invocation order. Pending ops are an error (Exercise 2).
func (r *HistoryRecorder) History() (*History, error) {
	if len(r.pending) > 0 {
		return nil, fmt.Errorf("%d ops never completed; this checker needs a complete history", len(r.pending))
	}
	sorted := append([]Op(nil), r.completed...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	h := NewHistory()
	for _, o := range sorted {
		if err := h.Add(o.Client, o.Kind, o.Key, o.Value, o.InvokedAt, o.ReturnedAt); err != nil {
			return nil, err
		}
	}
	return h, nil
}

// State is the register model's state: a map of registers, each initially 0.
type State map[string]int

// RegisterModel is the sequential specification. Step returns the next state and true, or false if the op's observed
// result is impossible from this state.
type RegisterModel struct{}

func (RegisterModel) Initial() State { return State{} }

func (RegisterModel) Step(state State, op Op) (State, bool) {
	current := state[op.Key]
	if op.Kind == Read {
		return state, current == op.Value
	}
	next := make(State, len(state)+1)
	for k, v := range state {
		next[k] = v
	}
	next[op.Key] = op.Value
	return next, true
}

// Key is the canonical, hashable form for the memo table: "a=1,b=2".
func (RegisterModel) Key(state State) string {
	keys := make([]string, 0, len(state))
	for k := range state {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, state[k]))
	}
	return strings.Join(parts, ",")
}

// CheckResult is the verdict plus what the search did: the witness order if OK, the longest prefix linearized if not.
type CheckResult struct {
	OK            bool
	Model         Consistency
	Order         []int
	LongestPrefix []int
	Explored      int
	MemoHits      int
}

func (r CheckResult) Describe(h *History) string {
	var sb strings.Builder
	if r.OK {
		fmt.Fprintf(&sb, "%s: yes. Witness order:", r.Model)
		for _, id := range r.Order {
			fmt.Fprintf(&sb, "\n  %s", h.Op(id))
		}
	} else {
		fmt.Fprintf(&sb, "%s: NO. Longest %s prefix (%d of %d ops):", r.Model, strings.ToLower(r.Model.String()), len(r.LongestPrefix), h.Size())
		for _, id := range r.LongestPrefix {
			fmt.Fprintf(&sb, "\n  %s", h.Op(id))
		}
		sb.WriteString("\n  no remaining op can go next from here")
	}
	fmt.Fprintf(&sb, "\n  (%d states explored, %d memo hits)", r.Explored, r.MemoHits)
	return sb.String()
}

// ErrBudget is returned when the search exceeds its state budget.
var ErrBudget = errors.New("state budget exceeded; history too concurrent for this checker")

// Checker is the Wing-Gong backtracking search with Lowe's (linearized-set, state) memo.
type Checker struct {
	model       RegisterModel
	memoize     bool
	stateBudget int

	ops           []Op
	consistency   Consistency
	failedConfigs map[string]bool
	order         []int
	longestPrefix []int
	explored      int
	memoHits      int
}

func NewChecker(model RegisterModel) *Checker { return NewCheckerWith(model, true, 5_000_000) }

func NewCheckerWith(model RegisterModel, memoize bool, stateBudget int) *Checker {
	return &Checker{model: model, memoize: memoize, stateBudget: stateBudget}
}

// Check searches for a valid sequential order. It returns ErrBudget if the search exceeds its state budget.
func (c *Checker) Check(h *History, consistency Consistency) (CheckResult, error) {
	c.ops = h.Ops()
	c.consistency = consistency
	c.failedConfigs = map[string]bool{}
	c.order = nil
	c.longestPrefix = nil
	c.explored = 0
	c.memoHits = 0
	ok, err := c.search(0, c.model.Initial())
	if err != nil {
		return CheckResult{}, err
	}
	return CheckResult{ok, consistency, append([]int{}, c.order...), append([]int{}, c.longestPrefix...), c.explored, c.memoHits}, nil
}

// CheckLinearizable is Check with the Linearizable model; it panics on a budget error, which tests treat as a failure.
func (c *Checker) CheckLinearizable(h *History) CheckResult {
	r, err := c.Check(h, Linearizable)
	if err != nil {
		panic(err)
	}
	return r
}

func (c *Checker) search(done uint64, state State) (bool, error) {
	c.explored++
	if c.explored > c.stateBudget {
		return false, fmt.Errorf("%w (budget %d)", ErrBudget, c.stateBudget)
	}
	if len(c.order) > len(c.longestPrefix) {
		c.longestPrefix = append([]int{}, c.order...)
	}
	if bits.OnesCount64(done) == len(c.ops) {
		return true, nil
	}
	memoKey := ""
	if c.memoize {
		memoKey = fmt.Sprintf("%d|%s", done, c.model.Key(state))
		if c.failedConfigs[memoKey] {
			c.memoHits++
			return false, nil
		}
	}
	for i, op := range c.ops {
		if done&(1<<uint(i)) != 0 || !c.mayGoNext(i, done) {
			continue
		}
		next, ok := c.model.Step(state, op)
		if !ok {
			continue
		}
		c.order = append(c.order, i)
		found, err := c.search(done|(1<<uint(i)), next)
		if err != nil || found {
			return found, err
		}
		c.order = c.order[:len(c.order)-1]
	}
	if c.memoize {
		c.failedConfigs[memoKey] = true
	}
	return false, nil
}

// mayGoNext: Linearizable: no not-yet-linearized op returned before i was invoked. Sequential: no earlier op of the
// same client remains.
func (c *Checker) mayGoNext(i int, done uint64) bool {
	candidate := c.ops[i]
	for j, other := range c.ops {
		if j == i || done&(1<<uint(j)) != 0 {
			continue
		}
		if c.consistency == Linearizable {
			if other.Precedes(candidate) {
				return false
			}
		} else if other.Client == candidate.Client && other.InvokedAt < candidate.InvokedAt {
			return false
		}
	}
	return true
}

// Rng is SplitMix64 (Steele, Lea, Flood 2014): identical streams in all three languages.
type Rng struct{ state uint64 }

func NewRng(seed int64) *Rng { return &Rng{state: uint64(seed)} }

func (r *Rng) NextLong() uint64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

func (r *Rng) NextInt(bound int) int { return int((r.NextLong() >> 33) % uint64(bound)) }

// GenerateLinearizable builds a history that is linearizable by construction: pick a sequential run, then widen each
// op into an interval around its linearization point.
func GenerateLinearizable(seed int64, n, clients, window int) *History {
	rng := NewRng(seed)
	h := NewHistory()
	value := 0
	for k := 0; k < n; k++ {
		point := 10 * (k + 1)
		invoked := point - rng.NextInt(window)
		returned := point + rng.NextInt(window)
		client := fmt.Sprintf("c%d", rng.NextInt(clients))
		if rng.NextInt(2) == 0 {
			value = 1 + rng.NextInt(9)
			h.Write(client, "x", value, invoked, returned)
		} else {
			h.Read(client, "x", value, invoked, returned)
		}
	}
	return h
}

func show(title string, h *History, r CheckResult) {
	fmt.Printf("== %s ==\n", title)
	for _, o := range h.Ops() {
		fmt.Printf("  %s\n", o)
	}
	fmt.Println(r.Describe(h))
	fmt.Println()
}

func main() {
	checker := NewChecker(RegisterModel{})

	good := NewHistory().Write("ledger", "bal", 500, 0, 4).Write("ledger", "bal", 300, 6, 9).
		Read("risk", "bal", 300, 10, 12).Read("app", "bal", 300, 13, 15)
	show("A correct replica: every read sees the latest completed write", good, checker.CheckLinearizable(good))

	stale := NewHistory().Write("ledger", "bal", 500, 0, 4).Write("ledger", "bal", 300, 6, 9).
		Read("risk", "bal", 500, 10, 12).Read("app", "bal", 300, 13, 15)
	show("A lagging replica: risk reads 500 after the write of 300 returned (stale read)", stale, checker.CheckLinearizable(stale))

	seqOnly := NewHistory().Write("ledger", "bal", 300, 0, 4).Read("risk", "bal", 0, 6, 8)
	fmt.Println("== Sequentially consistent but not linearizable ==")
	for _, o := range seqOnly.Ops() {
		fmt.Printf("  %s\n", o)
	}
	lin, _ := checker.Check(seqOnly, Linearizable)
	fmt.Println(lin.Describe(seqOnly))
	seq, _ := checker.Check(seqOnly, Sequential)
	fmt.Println(seq.Describe(seqOnly))
	fmt.Println("  (sequential consistency may put risk's read before ledger's write; linearizability may not, because the write returned at 4 and the read was invoked at 6)")
	fmt.Println()

	window := NewHistory().Write("ledger", "bal", 300, 0, 10).Read("risk", "bal", 0, 5, 15).Read("app", "bal", 300, 6, 8)
	show("Linearizable only because of the concurrency window: risk's read of 0 overlaps the write", window, checker.CheckLinearizable(window))
	shrunk := NewHistory().Write("ledger", "bal", 300, 0, 10).Read("risk", "bal", 0, 11, 15).Read("app", "bal", 300, 6, 8)
	show("Same ops, but risk's read starts after the write returned", shrunk, checker.CheckLinearizable(shrunk))

	fmt.Println("== Recording a history with an injected clock: a read served by a replica 3 ticks behind ==")
	clock := &FakeClock{}
	rec := NewHistoryRecorder(clock)
	w1 := rec.InvokeWrite("ledger", "bal", 500)
	clock.Advance(2)
	_ = rec.Complete(w1, 0)
	clock.Advance(1)
	w2 := rec.InvokeWrite("ledger", "bal", 300)
	clock.Advance(2)
	_ = rec.Complete(w2, 0)
	clock.Advance(1)
	r1 := rec.InvokeRead("risk", "bal")
	clock.Advance(1)
	_ = rec.Complete(r1, 500) // replica had not applied w2 yet
	recorded, err := rec.History()
	if err != nil {
		panic(err)
	}
	for _, o := range recorded.Ops() {
		fmt.Printf("  %s\n", o)
	}
	fmt.Println(checker.CheckLinearizable(recorded).Describe(recorded))
	fmt.Println()

	fmt.Println("== Why the memo matters: 8 fully concurrent writes followed by one read ==")
	burst := NewHistory()
	for i := 0; i < 8; i++ {
		burst.Write(fmt.Sprintf("c%d", i), "x", i+1, 0, 100)
	}
	burst.Read("reader", "x", 1, 101, 102) // the FIRST write must be linearized last
	withMemo := NewCheckerWith(RegisterModel{}, true, 5_000_000).CheckLinearizable(burst)
	withoutMemo := NewCheckerWith(RegisterModel{}, false, 5_000_000).CheckLinearizable(burst)
	fmt.Printf("  with memo:    ok=%t, %d states explored, %d memo hits\n", withMemo.OK, withMemo.Explored, withMemo.MemoHits)
	fmt.Printf("  without memo: ok=%t, %d states explored (depth-first in id order keeps retrying permutations that put write(x=1) first)\n", withoutMemo.OK, withoutMemo.Explored)
	fmt.Println()

	fmt.Println("== 50 generated histories (20 ops, 3 clients, window 15) that are linearizable by construction ==")
	passed, maxExplored := 0, 0
	for seed := int64(1); seed <= 50; seed++ {
		r := checker.CheckLinearizable(GenerateLinearizable(seed, 20, 3, 15))
		if r.OK {
			passed++
		}
		if r.Explored > maxExplored {
			maxExplored = r.Explored
		}
	}
	fmt.Printf("  passed %d/50; most states explored by any one check: %d\n", passed, maxExplored)
}
