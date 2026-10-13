// Lesson: phases/05-distributed-foundations/03-lamport-and-vector-clocks/docs/en.md
// Time, Order, and Clocks: Lamport and Vector Clocks
// Run:  go run .
// Test: go test ./...
// Sources: Lamport, "Time, Clocks, and the Ordering of Events in a Distributed System" (CACM 21(7), 1978);
//
//	Fidge (1988) and Mattern (1989) on vector clocks. Go 1.22+, standard library only. Same design as Java and Python.
package main

import (
	"container/heap"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// --- Money, copied from Phase 10 Lesson 01 (only what this lesson needs) ----------------------------------

// Currency is an ISO 4217 currency: a three-letter code and the number of decimals in its minor unit.
type Currency struct {
	Code     string
	Exponent int
}

var USD = Currency{"USD", 2}

func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

// Money is integer minor units plus a currency. Never a float.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, c Currency) Money { return Money{minorUnits, c} }

func (m Money) Plus(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, errors.New("currency mismatch")
	}
	return Money{m.MinorUnits + other.MinorUnits, m.Currency}, nil
}

func (m Money) String() string {
	scale := m.Currency.MinorUnitsPerMajor()
	magnitude := m.MinorUnits
	sign := ""
	if magnitude < 0 {
		magnitude, sign = -magnitude, "-"
	}
	whole := fmt.Sprintf("%d", magnitude/scale)
	if m.Currency.Exponent == 0 {
		return sign + whole + " " + m.Currency.Code
	}
	frac := fmt.Sprintf("%0*d", m.Currency.Exponent, magnitude%scale)
	return sign + whole + "." + frac + " " + m.Currency.Code
}

// --- Clocks -----------------------------------------------------------------------------------------------

// Relation says how two vector clocks relate. Lamport timestamps never produce Concurrent: they are a total order.
type Relation int

const (
	Before Relation = iota
	After
	Equal
	Concurrent
)

func (r Relation) String() string {
	return [...]string{"BEFORE", "AFTER", "EQUAL", "CONCURRENT"}[r]
}

// LamportTimestamp is a Lamport time plus the tie-break that makes it a total order: compare time, then node id.
type LamportTimestamp struct {
	Time   int64
	NodeID string
}

// Compare returns -1, 0, or 1. Two distinct events never compare equal.
func (t LamportTimestamp) Compare(other LamportTimestamp) int {
	switch {
	case t.Time < other.Time:
		return -1
	case t.Time > other.Time:
		return 1
	}
	return strings.Compare(t.NodeID, other.NodeID)
}

func (t LamportTimestamp) Less(other LamportTimestamp) bool { return t.Compare(other) < 0 }

func (t LamportTimestamp) String() string { return fmt.Sprintf("%d@%s", t.Time, t.NodeID) }

// LamportClock is Lamport's scalar clock. Three rules: tick on a local event, tick before send,
// max(local, remote) + 1 on receive.
type LamportClock struct {
	time int64
}

func (c *LamportClock) Time() int64 { return c.time }

// Tick is rule 1: a local event advances the clock by exactly one.
func (c *LamportClock) Tick() int64 {
	c.time++
	return c.time
}

// Send is rule 2: sending is a local event; the value returned is what travels in the message.
func (c *LamportClock) Send() int64 { return c.Tick() }

// Receive is rule 3: on receive, jump past the sender's clock so the receive event is later than the send.
func (c *LamportClock) Receive(remoteTime int64) int64 {
	if remoteTime > c.time {
		c.time = remoteTime
	}
	c.time++
	return c.time
}

// VectorClock is a vector clock over a fixed cluster of n nodes. Immutable: every operation returns a new clock.
// Component i counts the events node i has performed that this clock has observed.
type VectorClock struct {
	counters []int64
}

func NewVectorClock(size int) VectorClock { return VectorClock{make([]int64, size)} }

func VectorClockOf(counters ...int64) VectorClock {
	return VectorClock{append([]int64(nil), counters...)}
}

func (v VectorClock) Size() int { return len(v.counters) }

func (v VectorClock) Get(node int) int64 { return v.counters[node] }

// Increment is a local event on `node`: that component advances by one, nothing else changes.
func (v VectorClock) Increment(node int) VectorClock {
	next := append([]int64(nil), v.counters...)
	next[node]++
	return VectorClock{next}
}

// Merge is the componentwise maximum: everything either side has observed, the result has observed.
func (v VectorClock) Merge(other VectorClock) VectorClock {
	v.requireSameSize(other)
	next := make([]int64, len(v.counters))
	for i := range next {
		next[i] = v.counters[i]
		if other.counters[i] > next[i] {
			next[i] = other.counters[i]
		}
	}
	return VectorClock{next}
}

// Compare is the partial order. Before means v happened-before other; Concurrent means neither caused the other.
func (v VectorClock) Compare(other VectorClock) Relation {
	v.requireSameSize(other)
	anyLess, anyGreater := false, false
	for i := range v.counters {
		if v.counters[i] < other.counters[i] {
			anyLess = true
		}
		if v.counters[i] > other.counters[i] {
			anyGreater = true
		}
	}
	switch {
	case anyLess && anyGreater:
		return Concurrent
	case anyLess:
		return Before
	case anyGreater:
		return After
	}
	return Equal
}

func (v VectorClock) HappensBefore(other VectorClock) bool { return v.Compare(other) == Before }

func (v VectorClock) ConcurrentWith(other VectorClock) bool { return v.Compare(other) == Concurrent }

func (v VectorClock) Equals(other VectorClock) bool {
	if len(v.counters) != len(other.counters) {
		return false
	}
	for i := range v.counters {
		if v.counters[i] != other.counters[i] {
			return false
		}
	}
	return true
}

// requireSameSize panics because a size mismatch is a programming error, not a runtime condition.
func (v VectorClock) requireSameSize(other VectorClock) {
	if len(other.counters) != len(v.counters) {
		panic(fmt.Sprintf("vector clocks must cover the same cluster: %d vs %d", len(v.counters), len(other.counters)))
	}
}

func (v VectorClock) String() string {
	parts := make([]string, len(v.counters))
	for i, c := range v.counters {
		parts[i] = fmt.Sprintf("%d", c)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// --- Events, messages, nodes -------------------------------------------------------------------------------

// Event is one thing that happened on one node, stamped by both clocks so the two can be compared side by side.
type Event struct {
	NodeID      string
	Description string
	Lamport     LamportTimestamp
	Vector      VectorClock
}

func (e Event) String() string {
	return fmt.Sprintf("%-11s %-8s %-9s %s", e.Lamport, e.Vector, e.NodeID, e.Description)
}

// Message is a message in flight. Carries the sender's Lamport time and vector clock as of the send event.
type Message struct {
	From        string
	To          string
	Payload     string
	LamportTime int64
	Vector      VectorClock
}

// Node is one service: owns both clocks and an append-only log of its events.
type Node struct {
	ID      string
	Index   int
	Lamport LamportClock
	Vector  VectorClock
	Log     []*Event
}

func NewNode(id string, index, clusterSize int) *Node {
	return &Node{ID: id, Index: index, Vector: NewVectorClock(clusterSize)}
}

// LocalEvent is a local posting: both clocks tick; nothing leaves the node.
func (n *Node) LocalEvent(description string) *Event {
	t := n.Lamport.Tick()
	n.Vector = n.Vector.Increment(n.Index)
	return n.record(description, t)
}

// Send is an event too; the message carries the clock values after the tick.
func (n *Node) Send(to, payload string) Message {
	t := n.Lamport.Send()
	n.Vector = n.Vector.Increment(n.Index)
	n.record("send "+payload+" -> "+to, t)
	return Message{n.ID, to, payload, t, n.Vector}
}

// Receive merges what the sender knew, then counts the receive as a local event.
func (n *Node) Receive(m Message) *Event {
	t := n.Lamport.Receive(m.LamportTime)
	n.Vector = n.Vector.Merge(m.Vector).Increment(n.Index)
	return n.record("recv "+m.Payload+" <- "+m.From, t)
}

func (n *Node) record(description string, lamportTime int64) *Event {
	e := &Event{n.ID, description, LamportTimestamp{lamportTime, n.ID}, n.Vector}
	n.Log = append(n.Log, e)
	return e
}

// --- Deterministic network ---------------------------------------------------------------------------------

type pending struct {
	deliverAt int64
	seq       int64
	message   Message
}

type pendingQueue []pending

func (q pendingQueue) Len() int { return len(q) }
func (q pendingQueue) Less(i, j int) bool {
	if q[i].deliverAt != q[j].deliverAt {
		return q[i].deliverAt < q[j].deliverAt
	}
	return q[i].seq < q[j].seq
}
func (q pendingQueue) Swap(i, j int)       { q[i], q[j] = q[j], q[i] }
func (q *pendingQueue) Push(x interface{}) { *q = append(*q, x.(pending)) }
func (q *pendingQueue) Pop() interface{} {
	old := *q
	item := old[len(old)-1]
	*q = old[:len(old)-1]
	return item
}

// SimulatedNetwork is a deterministic message scheduler. Each send is assigned a delivery tick = now + 1 + delay,
// where delay comes from a 64-bit linear congruential generator seeded by the test. Messages are delivered in
// (deliveryTick, sequence) order, so two messages sent in one order can arrive in the other, and the same seed
// always produces the same schedule.
type SimulatedNetwork struct {
	nodes     map[string]*Node
	order     []string
	queue     pendingQueue
	maxDelay  int
	state     uint64
	tick      int64
	seq       int64
	delivered int
}

func NewSimulatedNetwork(seed uint64, maxDelay int) (*SimulatedNetwork, error) {
	if maxDelay < 0 {
		return nil, errors.New("maxDelay must be >= 0")
	}
	return &SimulatedNetwork{nodes: map[string]*Node{}, maxDelay: maxDelay, state: seed}, nil
}

func (s *SimulatedNetwork) Add(n *Node) {
	s.nodes[n.ID] = n
	s.order = append(s.order, n.ID)
}

func (s *SimulatedNetwork) Node(id string) *Node {
	n, ok := s.nodes[id]
	if !ok {
		panic("unknown node " + id)
	}
	return n
}

// NextDelay uses Knuth's MMIX LCG constants; uint64 wraps exactly like Java's long and Python's masked int.
func (s *SimulatedNetwork) NextDelay() int {
	s.state = s.state*6364136223846793005 + 1442695040888963407
	return int((s.state >> 33) % uint64(s.maxDelay+1))
}

func (s *SimulatedNetwork) Send(m Message) error {
	if _, ok := s.nodes[m.To]; !ok {
		return fmt.Errorf("unknown node %s", m.To)
	}
	heap.Push(&s.queue, pending{s.tick + 1 + int64(s.NextDelay()), s.seq, m})
	s.seq++
	return nil
}

// DeliverOne delivers the earliest pending message. Returns the receive event, or nil if nothing is in flight.
func (s *SimulatedNetwork) DeliverOne() *Event {
	if s.queue.Len() == 0 {
		return nil
	}
	p := heap.Pop(&s.queue).(pending)
	s.tick = p.deliverAt
	s.delivered++
	return s.Node(p.message.To).Receive(p.message)
}

func (s *SimulatedNetwork) DeliverAll() {
	for s.DeliverOne() != nil {
	}
}

func (s *SimulatedNetwork) InFlight() int  { return s.queue.Len() }
func (s *SimulatedNetwork) Delivered() int { return s.delivered }
func (s *SimulatedNetwork) Tick() int64    { return s.tick }

func (s *SimulatedNetwork) AllEvents() []*Event {
	var all []*Event
	for _, id := range s.order {
		all = append(all, s.nodes[id].Log...)
	}
	return all
}

// --- Analysis helpers and the scenario ---------------------------------------------------------------------

const (
	Payments = "payments"
	Refunds  = "refunds"
	Ledger   = "ledger"
)

var (
	Capture = OfMinor(4_999, USD)
	Refund  = OfMinor(-4_999, USD)
)

// TotalOrder is the single global sequence a ledger can use: Lamport time, then node id. Consistent with causality.
func TotalOrder(events []*Event) []*Event {
	sorted := append([]*Event(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Lamport.Less(sorted[j].Lamport) })
	return sorted
}

// ConcurrentPairs lists pairs of events that neither caused the other: the candidates for a write-write conflict.
func ConcurrentPairs(events []*Event) [][2]*Event {
	var pairs [][2]*Event
	for i := 0; i < len(events); i++ {
		for j := i + 1; j < len(events); j++ {
			if events[i].Vector.ConcurrentWith(events[j].Vector) {
				pairs = append(pairs, [2]*Event{events[i], events[j]})
			}
		}
	}
	return pairs
}

// Postings keeps only the ledger postings: send and receive events are bookkeeping, not account updates.
func Postings(events []*Event) []*Event {
	var out []*Event
	for _, e := range events {
		if strings.HasPrefix(e.Description, "post ") {
			out = append(out, e)
		}
	}
	return out
}

func mustPlus(a, b Money) Money {
	m, err := a.Plus(b)
	if err != nil {
		panic(err)
	}
	return m
}

func cluster(seed uint64) *SimulatedNetwork {
	net, _ := NewSimulatedNetwork(seed, 3)
	net.Add(NewNode(Payments, 0, 3))
	net.Add(NewNode(Refunds, 1, 3))
	net.Add(NewNode(Ledger, 2, 3))
	return net
}

// CausalRefundScenario is the healthy flow: refunds only refunds after hearing about the capture.
func CausalRefundScenario(seed uint64) *SimulatedNetwork {
	net := cluster(seed)
	payments, refunds, ledger := net.Node(Payments), net.Node(Refunds), net.Node(Ledger)

	payments.LocalEvent(fmt.Sprintf("post captured %s pay_8f3", Capture)) // A
	_ = net.Send(payments.Send(Ledger, fmt.Sprintf("captured %s pay_8f3", Capture)))
	_ = net.Send(payments.Send(Refunds, fmt.Sprintf("captured %s pay_8f3", Capture)))
	net.DeliverAll()                                                    // refunds now knows about A
	refunds.LocalEvent(fmt.Sprintf("post refunded %s pay_8f3", Refund)) // B: caused by A
	_ = net.Send(refunds.Send(Ledger, fmt.Sprintf("refunded %s pay_8f3", Refund)))
	net.DeliverAll()
	ledger.LocalEvent(fmt.Sprintf("post settle pay_8f3 net %s", mustPlus(Capture, Refund))) // C: after A and B
	return net
}

// ConcurrentRefundScenario is the incident: the refund is issued before the capture notice arrives.
func ConcurrentRefundScenario(seed uint64) *SimulatedNetwork {
	net := cluster(seed)
	payments, refunds, ledger := net.Node(Payments), net.Node(Refunds), net.Node(Ledger)

	payments.LocalEvent(fmt.Sprintf("post captured %s pay_8f3", Capture)) // A
	_ = net.Send(payments.Send(Ledger, fmt.Sprintf("captured %s pay_8f3", Capture)))
	refunds.LocalEvent(fmt.Sprintf("post refunded %s pay_8f3", Refund)) // B: concurrent with A
	_ = net.Send(refunds.Send(Ledger, fmt.Sprintf("refunded %s pay_8f3", Refund)))
	net.DeliverAll()                                                                        // arrival order depends on seed
	ledger.LocalEvent(fmt.Sprintf("post settle pay_8f3 net %s", mustPlus(Capture, Refund))) // C: after A and B
	return net
}

func printLogs(net *SimulatedNetwork) {
	fmt.Printf("%-11s %-8s %-9s %s\n", "lamport", "vector", "node", "event")
	for _, id := range []string{Payments, Refunds, Ledger} {
		for _, e := range net.Node(id).Log {
			fmt.Println(e)
		}
	}
}

func main() {
	fmt.Println("== Lamport clock: three rules ==")
	var a, b LamportClock
	fmt.Printf("a.tick()      -> %d   (local event)\n", a.Tick())
	sent := a.Send()
	fmt.Printf("a.send()      -> %d   (travels in the message)\n", sent)
	for i := 0; i < 5; i++ {
		b.Tick()
	}
	bWas := b.Time()
	fmt.Printf("b is at %d; b.receive(%d) -> %d   (max(5,2)+1: local clock already ahead)\n", bWas, sent, b.Receive(sent))
	var c LamportClock
	fmt.Printf("c is at 0; c.receive(%d) -> %d   (max(0,2)+1: jump past the sender)\n", sent, c.Receive(sent))

	fmt.Println("\n== Vector clocks: the partial order ==")
	v1, v2 := VectorClockOf(2, 0, 0), VectorClockOf(0, 1, 0)
	v3 := v1.Merge(v2).Increment(1)
	fmt.Printf("%s vs %s -> %s\n", v1, v2, v1.Compare(v2))
	fmt.Printf("%s vs %s -> %s\n", v1, v3, v1.Compare(v3))
	fmt.Printf("%s vs %s -> %s\n", v3, v1, v3.Compare(v1))
	fmt.Printf("%s = merge(%s,%s).increment(refunds)\n", v3, v1, v2)

	fmt.Println("\n== Scenario 1: refund issued after the capture notice arrived (seed 7) ==")
	causal := CausalRefundScenario(7)
	printLogs(causal)
	capA, refA := causal.Node(Payments).Log[0], causal.Node(Refunds).Log[1]
	fmt.Printf("captured %s vs refunded %s -> %s\n", capA.Vector, refA.Vector, capA.Vector.Compare(refA.Vector))

	fmt.Println("\n== Scenario 2: refund issued before the capture notice arrived (seed 7) ==")
	conc := ConcurrentRefundScenario(7)
	printLogs(conc)

	fmt.Println("\n== Total order by (lamport, node): the sequence the ledger would assign ==")
	events := conc.AllEvents()
	for _, e := range TotalOrder(events) {
		fmt.Println(e)
	}

	fmt.Println("\n== Concurrent postings by vector clock: the conflicts to resolve ==")
	for _, pair := range ConcurrentPairs(Postings(events)) {
		fmt.Printf("%s %s '%s'  ||  %s %s '%s'\n", pair[0].Lamport, pair[0].Vector, pair[0].Description,
			pair[1].Lamport, pair[1].Vector, pair[1].Description)
	}

	fmt.Println("\n== What Lamport order cannot tell you ==")
	captured, refunded := conc.Node(Payments).Log[0], conc.Node(Refunds).Log[0]
	first, second := captured, refunded
	if !captured.Lamport.Less(refunded.Lamport) {
		first, second = refunded, captured
	}
	fmt.Printf("lamport total order puts %s before %s; vector clocks say %s vs %s is %s\n",
		first.Lamport, second.Lamport, first.Vector, second.Vector, first.Vector.Compare(second.Vector))
	fmt.Println("a smaller Lamport time is necessary for happens-before, never sufficient")
	fmt.Printf("messages delivered: %d, final tick: %d\n", conc.Delivered(), conc.Tick())
}
