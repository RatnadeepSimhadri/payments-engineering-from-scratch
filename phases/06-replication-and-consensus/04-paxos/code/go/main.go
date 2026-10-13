// Lesson: phases/06-replication-and-consensus/04-paxos/docs/en.md
// Single-Decree Paxos from Scratch
// Run:  go run .
// Test: go test ./...
// Sources: Lamport, "Paxos Made Simple" (2001); Lamport, "The Part-Time Parliament" (ACM TOCS 1998).
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"container/heap"
	"fmt"
	"sort"
	"strings"
)

// Rng is SplitMix64 (Steele, Lea, Flood, OOPSLA 2014), implemented by hand so that the same seed produces
// the same message delays and drops in Java, Python, and Go: a bug found in one language replays in all three.
type Rng struct{ state uint64 }

func NewRng(seed int64) *Rng { return &Rng{state: uint64(seed)} }

func (r *Rng) NextLong() uint64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// NextInt returns a uniform integer in [0, bound). Uses the top 31 bits so the result is identical across languages.
func (r *Rng) NextInt(bound int) int { return int((r.NextLong() >> 33) % uint64(bound)) }

// NextDouble returns a uniform float64 in [0, 1) built from the top 53 bits.
func (r *Rng) NextDouble() float64 { return float64(r.NextLong()>>11) * (1.0 / (1 << 53)) }

// Ballot is a ballot number: totally ordered, unique per proposer because the proposer id breaks ties between rounds.
type Ballot struct {
	Round      int
	ProposerID int
}

var None = Ballot{0, 0}

func (b Ballot) IsNone() bool { return b.Round == 0 }

// Compare returns -1, 0, or 1 like Java's compareTo.
func (b Ballot) Compare(o Ballot) int {
	switch {
	case b.Round != o.Round:
		if b.Round < o.Round {
			return -1
		}
		return 1
	case b.ProposerID != o.ProposerID:
		if b.ProposerID < o.ProposerID {
			return -1
		}
		return 1
	}
	return 0
}

func (b Ballot) String() string {
	if b.IsNone() {
		return "none"
	}
	return fmt.Sprintf("%d.%d", b.Round, b.ProposerID)
}

type MessageKind int

const (
	Prepare MessageKind = iota
	Promise
	Accept
	Accepted
	Nack
)

func (k MessageKind) String() string {
	return [...]string{"PREPARE", "PROMISE", "ACCEPT", "ACCEPTED", "NACK"}[k]
}

// Message is one message on the wire. Which fields are meaningful depends on the kind:
// PREPARE(Ballot); PROMISE(Ballot, PriorBallot + Value previously accepted); ACCEPT(Ballot, Value);
// ACCEPTED(Ballot, Value); NACK(Ballot rejected, PriorBallot = the ballot the acceptor has promised).
// Value "" means no value; proposers never propose the empty string.
type Message struct {
	Kind        MessageKind
	From, To    int
	Ballot      Ballot
	PriorBallot Ballot
	Value       string
}

func PrepareMsg(from, to int, b Ballot) Message { return Message{Prepare, from, to, b, None, ""} }
func PromiseMsg(from, to int, b, prior Ballot, v string) Message {
	return Message{Promise, from, to, b, prior, v}
}
func AcceptMsg(from, to int, b Ballot, v string) Message {
	return Message{Accept, from, to, b, None, v}
}
func AcceptedMsg(from, to int, b Ballot, v string) Message {
	return Message{Accepted, from, to, b, None, v}
}
func NackMsg(from, to int, rejected, promised Ballot) Message {
	return Message{Nack, from, to, rejected, promised, ""}
}

// Node is a pure state machine: it consumes one message or one clock tick and returns the messages it wants sent.
type Node interface {
	ID() int
	Handle(m Message, now int) []Message
	Tick(now int) []Message
}

// Acceptor is the only role whose state matters for safety. Two fields, two promises.
// P1: never accept a ballot lower than one already promised. P2: report what was accepted in every promise.
type Acceptor struct {
	id             int
	learnerIDs     []int
	Promised       Ballot
	AcceptedBallot Ballot
	AcceptedValue  string
}

func NewAcceptor(id int, learnerIDs []int) *Acceptor {
	return &Acceptor{id: id, learnerIDs: learnerIDs, Promised: None, AcceptedBallot: None}
}

func (a *Acceptor) ID() int                { return a.id }
func (a *Acceptor) Tick(now int) []Message { return nil }

func (a *Acceptor) Handle(m Message, now int) []Message {
	switch m.Kind {
	case Prepare:
		return a.onPrepare(m)
	case Accept:
		return a.onAccept(m)
	}
	return nil
}

func (a *Acceptor) onPrepare(m Message) []Message {
	if m.Ballot.Compare(a.Promised) >= 0 {
		a.Promised = m.Ballot
		return []Message{PromiseMsg(a.id, m.From, m.Ballot, a.AcceptedBallot, a.AcceptedValue)}
	}
	return []Message{NackMsg(a.id, m.From, m.Ballot, a.Promised)}
}

func (a *Acceptor) onAccept(m Message) []Message {
	if m.Ballot.Compare(a.Promised) >= 0 {
		a.Promised = m.Ballot
		a.AcceptedBallot = m.Ballot
		a.AcceptedValue = m.Value
		out := []Message{AcceptedMsg(a.id, m.From, m.Ballot, m.Value)}
		for _, l := range a.learnerIDs {
			out = append(out, AcceptedMsg(a.id, l, m.Ballot, m.Value))
		}
		return out
	}
	return []Message{NackMsg(a.id, m.From, m.Ballot, a.Promised)}
}

type Phase int

const (
	Idle Phase = iota
	Preparing
	Accepting
	Done
)

// Proposer drives the two phases, retries on NACK or timeout, and must adopt any value it learns was already accepted.
type Proposer struct {
	id                int
	acceptorIDs       []int
	quorum            int
	timeout           int
	Round             int
	Ballot            Ballot
	Phase             Phase
	Wanted            string // the value this proposer would like chosen
	Proposing         string // the value it is actually proposing in phase 2 (may differ from Wanted)
	Decided           string
	promises          map[int]bool
	accepts           map[int]bool
	highestPrior      Ballot
	highestPriorValue string
	deadline          int
	Restarts          int
}

func NewProposer(id int, acceptorIDs []int, timeout int) *Proposer {
	return &Proposer{id: id, acceptorIDs: acceptorIDs, quorum: len(acceptorIDs)/2 + 1, timeout: timeout,
		Ballot: None, promises: map[int]bool{}, accepts: map[int]bool{}, highestPrior: None, deadline: 1 << 62}
}

func (p *Proposer) ID() int { return p.id }

// Propose is phase 1a: pick a ballot higher than any this proposer has used and ask every acceptor to promise.
func (p *Proposer) Propose(value string, now int) []Message {
	p.Wanted = value
	return p.startRound(now)
}

func (p *Proposer) startRound(now int) []Message {
	p.Round++
	p.Ballot = Ballot{p.Round, p.id}
	p.Phase = Preparing
	p.promises = map[int]bool{}
	p.accepts = map[int]bool{}
	p.highestPrior = None
	p.highestPriorValue = ""
	p.deadline = now + p.timeout
	out := make([]Message, 0, len(p.acceptorIDs))
	for _, a := range p.acceptorIDs {
		out = append(out, PrepareMsg(p.id, a, p.Ballot))
	}
	return out
}

func (p *Proposer) Handle(m Message, now int) []Message {
	if p.Phase == Done || m.Ballot != p.Ballot {
		return nil // stale reply from an earlier round
	}
	switch m.Kind {
	case Promise:
		return p.onPromise(m, now)
	case Accepted:
		return p.onAccepted(m)
	case Nack:
		return p.onNack(m, now)
	}
	return nil
}

// onPromise is phase 1b -> 2a: once a majority has promised, propose the highest-ballot value any of them had accepted, else our own.
func (p *Proposer) onPromise(m Message, now int) []Message {
	if p.Phase != Preparing {
		return nil
	}
	p.promises[m.From] = true
	if !m.PriorBallot.IsNone() && m.PriorBallot.Compare(p.highestPrior) > 0 {
		p.highestPrior = m.PriorBallot
		p.highestPriorValue = m.Value
	}
	if len(p.promises) < p.quorum {
		return nil
	}
	if p.highestPriorValue != "" {
		p.Proposing = p.highestPriorValue
	} else {
		p.Proposing = p.Wanted
	}
	p.Phase = Accepting
	p.deadline = now + p.timeout
	out := make([]Message, 0, len(p.acceptorIDs))
	for _, a := range p.acceptorIDs {
		out = append(out, AcceptMsg(p.id, a, p.Ballot, p.Proposing))
	}
	return out
}

func (p *Proposer) onAccepted(m Message) []Message {
	if p.Phase != Accepting {
		return nil
	}
	p.accepts[m.From] = true
	if len(p.accepts) >= p.quorum {
		p.Decided = p.Proposing
		p.Phase = Done
	}
	return nil
}

// onNack: a NACK carries the ballot the acceptor has promised; jump past it instead of creeping up one round at a time.
func (p *Proposer) onNack(m Message, now int) []Message {
	if m.PriorBallot.Round > p.Round {
		p.Round = m.PriorBallot.Round
	}
	p.Restarts++
	return p.startRound(now)
}

func (p *Proposer) Tick(now int) []Message {
	if (p.Phase == Preparing || p.Phase == Accepting) && now >= p.deadline {
		p.Restarts++
		return p.startRound(now)
	}
	return nil
}

// Learner: a value is chosen once a majority of acceptors have accepted the same ballot. It must never learn two values.
type Learner struct {
	id            int
	quorum        int
	votes         map[Ballot]map[int]bool
	LearnedBallot Ballot
	Learned       string
}

func NewLearner(id, acceptorCount int) *Learner {
	return &Learner{id: id, quorum: acceptorCount/2 + 1, votes: map[Ballot]map[int]bool{}, LearnedBallot: None}
}

func (l *Learner) ID() int                { return l.id }
func (l *Learner) Tick(now int) []Message { return nil }

func (l *Learner) Handle(m Message, now int) []Message {
	if m.Kind != Accepted {
		return nil
	}
	voters := l.votes[m.Ballot]
	if voters == nil {
		voters = map[int]bool{}
		l.votes[m.Ballot] = voters
	}
	voters[m.From] = true
	if len(voters) >= l.quorum {
		if l.Learned != "" && l.Learned != m.Value {
			panic(fmt.Sprintf("SAFETY VIOLATION: learner %d learned %s then %s", l.id, l.Learned, m.Value))
		}
		l.Learned = m.Value
		l.LearnedBallot = m.Ballot
	}
	return nil
}

// Envelope is a message with its delivery time; Seq keeps delivery order deterministic for equal times.
type Envelope struct {
	DeliverAt int
	Seq       int64
	Message   Message
}

type envelopeHeap []Envelope

func (h envelopeHeap) Len() int { return len(h) }
func (h envelopeHeap) Less(i, j int) bool {
	if h[i].DeliverAt != h[j].DeliverAt {
		return h[i].DeliverAt < h[j].DeliverAt
	}
	return h[i].Seq < h[j].Seq
}
func (h envelopeHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *envelopeHeap) Push(x any)   { *h = append(*h, x.(Envelope)) }
func (h *envelopeHeap) Pop() any     { old := *h; e := old[len(old)-1]; *h = old[:len(old)-1]; return e }

// Network is a deterministic simulated network. Delays and drops come from the seeded Rng; delivery order is by
// (DeliverAt, Seq), so a run is a pure function of (seed, program). No goroutines, no sleeping.
type Network struct {
	rng       *Rng
	dropRate  float64
	maxDelay  int
	nodes     map[int]Node
	order     []int // node ids sorted, so Tick visits nodes in the same order as Java's TreeMap
	inFlight  envelopeHeap
	isolated  map[int]bool
	observer  func()
	now       int
	seq       int64
	Sent      int
	Delivered int
	Dropped   int
	Trace     bool
}

func NewNetwork(seed int64, dropRate float64, maxDelay int) *Network {
	if maxDelay < 1 {
		maxDelay = 1
	}
	return &Network{rng: NewRng(seed), dropRate: dropRate, maxDelay: maxDelay, nodes: map[int]Node{},
		isolated: map[int]bool{}, observer: func() {}}
}

func (n *Network) Add(node Node) {
	n.nodes[node.ID()] = node
	n.order = append(n.order, node.ID())
	sort.Ints(n.order)
}
func (n *Network) SetObserver(f func()) { n.observer = f }
func (n *Network) Now() int             { return n.now }
func (n *Network) InFlight() int        { return len(n.inFlight) }
func (n *Network) Isolate(id int)       { n.isolated[id] = true }
func (n *Network) Heal()                { n.isolated = map[int]bool{} }

func (n *Network) Send(m Message) {
	n.Sent++
	if n.isolated[m.From] || n.isolated[m.To] {
		n.Dropped++
		return
	}
	roll := n.rng.NextDouble()
	delay := 1 + n.rng.NextInt(n.maxDelay)
	if roll < n.dropRate {
		n.Dropped++
		return
	}
	heap.Push(&n.inFlight, Envelope{n.now + delay, n.seq, m})
	n.seq++
}

func (n *Network) SendAll(ms []Message) {
	for _, m := range ms {
		n.Send(m)
	}
}

// Tick advances the clock one tick: let every node act on the time, then deliver everything that is due.
func (n *Network) Tick() {
	n.now++
	for _, id := range n.order {
		n.SendAll(n.nodes[id].Tick(n.now))
	}
	for len(n.inFlight) > 0 && n.inFlight[0].DeliverAt <= n.now {
		n.deliver(heap.Pop(&n.inFlight).(Envelope))
	}
}

func (n *Network) deliver(e Envelope) {
	n.Delivered++
	target := n.nodes[e.Message.To]
	if n.Trace {
		fmt.Printf("t=%-3d %s -> %s %s\n", n.now, name(e.Message.From), name(e.Message.To), describe(e.Message))
	}
	n.SendAll(target.Handle(e.Message, n.now))
	n.observer()
}

// Drain delivers, ignoring delays, every in-flight message matching the filter, repeating until none match.
// Lets a test script an adversarial schedule.
func (n *Network) Drain(filter func(Message) bool) int {
	count := 0
	for {
		var matching, rest envelopeHeap
		for _, e := range n.inFlight {
			if filter(e.Message) {
				matching = append(matching, e)
			} else {
				rest = append(rest, e)
			}
		}
		if len(matching) == 0 {
			return count
		}
		sort.Slice(matching, func(i, j int) bool { return matching[i].Seq < matching[j].Seq })
		heap.Init(&rest)
		n.inFlight = rest
		for _, e := range matching {
			n.deliver(e)
			count++
		}
	}
}

func describe(m Message) string {
	switch m.Kind {
	case Prepare:
		return fmt.Sprintf("PREPARE %s", m.Ballot)
	case Promise:
		if m.PriorBallot.IsNone() {
			return fmt.Sprintf("PROMISE %s (nothing accepted)", m.Ballot)
		}
		return fmt.Sprintf("PROMISE %s (accepted %s at %s)", m.Ballot, m.Value, m.PriorBallot)
	case Accept:
		return fmt.Sprintf("ACCEPT %s %s", m.Ballot, m.Value)
	case Accepted:
		return fmt.Sprintf("ACCEPTED %s %s", m.Ballot, m.Value)
	}
	return fmt.Sprintf("NACK %s (promised %s)", m.Ballot, m.PriorBallot)
}

// PaxosCluster wires acceptors, proposers, and learners onto one network and checks the safety invariant after every delivery.
type PaxosCluster struct {
	AcceptorCount int
	Acceptors     []*Acceptor
	Proposers     []*Proposer
	Learners      []*Learner
	Network       *Network
	FirstChosen   string
	ChosenAt      int
}

func NewPaxosCluster(acceptorCount, proposerCount, learnerCount int, seed int64, dropRate float64, maxDelay, timeout int) *PaxosCluster {
	c := &PaxosCluster{AcceptorCount: acceptorCount, Network: NewNetwork(seed, dropRate, maxDelay), ChosenAt: -1}
	acceptorIDs := make([]int, acceptorCount)
	for i := range acceptorIDs {
		acceptorIDs[i] = i
	}
	learnerIDs := make([]int, learnerCount)
	for i := range learnerIDs {
		learnerIDs[i] = acceptorCount + proposerCount + i
	}
	for i := 0; i < acceptorCount; i++ {
		c.Acceptors = append(c.Acceptors, NewAcceptor(i, learnerIDs))
	}
	for i := 0; i < proposerCount; i++ {
		c.Proposers = append(c.Proposers, NewProposer(acceptorCount+i, acceptorIDs, timeout))
	}
	for i := 0; i < learnerCount; i++ {
		c.Learners = append(c.Learners, NewLearner(learnerIDs[i], acceptorCount))
	}
	for _, a := range c.Acceptors {
		c.Network.Add(a)
	}
	for _, p := range c.Proposers {
		c.Network.Add(p)
	}
	for _, l := range c.Learners {
		c.Network.Add(l)
	}
	c.Network.SetObserver(c.checkSafety)
	return c
}

func (c *PaxosCluster) Propose(proposerIndex int, value string) {
	c.Network.SendAll(c.Proposers[proposerIndex].Propose(value, c.Network.Now()))
}

func (c *PaxosCluster) Tick() { c.Network.Tick() }

// Run until every learner has learned a value or maxTicks elapse. Returns true if a value was chosen.
func (c *PaxosCluster) Run(maxTicks int) bool {
	for i := 0; i < maxTicks; i++ {
		c.Tick()
		if c.AllLearned() {
			return true
		}
	}
	return c.ChosenValue() != ""
}

func (c *PaxosCluster) AllLearned() bool {
	for _, l := range c.Learners {
		if l.Learned == "" {
			return false
		}
	}
	return true
}

// ChosenValue is the definition of "chosen": some ballot has been accepted by a majority of acceptors.
// Computed from acceptor state alone; "" means nothing is chosen yet.
func (c *PaxosCluster) ChosenValue() string {
	counts := map[Ballot]int{}
	for _, a := range c.Acceptors {
		if a.AcceptedBallot.IsNone() {
			continue
		}
		counts[a.AcceptedBallot]++
		if counts[a.AcceptedBallot] >= c.AcceptorCount/2+1 {
			return a.AcceptedValue
		}
	}
	return ""
}

func (c *PaxosCluster) checkSafety() {
	current := c.ChosenValue()
	if current == "" {
		return
	}
	if c.FirstChosen == "" {
		c.FirstChosen = current
		c.ChosenAt = c.Network.Now()
		return
	}
	if c.FirstChosen != current {
		panic(fmt.Sprintf("SAFETY VIOLATION: chosen %s at t=%d then %s at t=%d", c.FirstChosen, c.ChosenAt, current, c.Network.Now()))
	}
}

func (c *PaxosCluster) MaxRound() int {
	r := 0
	for _, p := range c.Proposers {
		if p.Round > r {
			r = p.Round
		}
	}
	return r
}

// Naming used by the trace output: ids 0..A-1 are acceptors, then proposers, then learners.
var acceptorsForNames, proposersForNames = 3, 2

func name(id int) string {
	if id < acceptorsForNames {
		return fmt.Sprintf("a%d", id)
	}
	if id < acceptorsForNames+proposersForNames {
		return fmt.Sprintf("p%d", id)
	}
	return fmt.Sprintf("l%d", id)
}

func show(value string) string {
	if value == "" {
		return "(none)"
	}
	return value
}

func demoLossless() {
	fmt.Println("== 1. Three acceptors, two proposers, one learner, lossless network, delay 1 ==")
	c := NewPaxosCluster(3, 2, 1, 7, 0.0, 1, 10)
	c.Network.Trace = true
	c.Propose(0, "debit:acct-42:19.99")
	c.Propose(1, "credit:acct-42:5.00")
	for i := 0; i < 8; i++ { // long enough for the losing proposer to retry and adopt the chosen value
		c.Tick()
	}
	l := c.Learners[0]
	fmt.Printf("chosen = %s at t=%d; learner learned %s at ballot %s; p3.decided=%s p4.decided=%s\n\n",
		c.FirstChosen, c.ChosenAt, l.Learned, l.LearnedBallot, c.Proposers[0].Decided, c.Proposers[1].Decided)
}

func demoSeededRuns() {
	fmt.Println("== 2. 200 seeded runs: 5 acceptors, 3 proposers, 20% message loss, delays 1..3 ==")
	decided, totalTicks, maxRounds := 0, 0, 0
	winners := map[string]int{}
	for seed := int64(1); seed <= 200; seed++ {
		c := NewPaxosCluster(5, 3, 2, seed, 0.20, 3, 12)
		c.Propose(0, "A")
		c.Propose(1, "B")
		c.Propose(2, "C")
		if c.Run(400) {
			decided++
			totalTicks += c.ChosenAt
			winners[c.FirstChosen]++
		}
		if c.MaxRound() > maxRounds {
			maxRounds = c.MaxRound()
		}
	}
	fmt.Printf("decided in %d/200 runs, distinct chosen values per run: never more than 1 (checked after every delivery)\n", decided)
	keys := make([]string, 0, len(winners))
	for k := range winners {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, winners[k]))
	}
	mean := 0.0
	if decided > 0 {
		mean = float64(totalTicks) / float64(decided)
	}
	fmt.Printf("winners: {%s}; mean ticks to choose: %.1f; highest round seen: %d\n\n", strings.Join(parts, ", "), mean, maxRounds)
}

func demoDuel() {
	fmt.Println("== 3. Constructed duel: two proposers whose prepares keep landing between each other's promise and accept ==")
	c := NewPaxosCluster(3, 2, 1, 1, 0.0, 1, 1000)
	notAccept := func(m Message) bool { return m.Kind != Accept }
	acceptFrom := func(id int) func(Message) bool {
		return func(m Message) bool { return m.Kind == Accept && m.From == id }
	}
	c.Propose(0, "A")
	c.Network.Drain(notAccept) // p3: prepare 1.3 -> promises -> ACCEPT 1.3 held in flight
	c.Propose(1, "B")
	c.Network.Drain(notAccept) // p4: prepare 1.4 -> promises -> ACCEPT 1.4 held in flight
	for k := 1; k <= 3; k++ {
		c.Network.Drain(acceptFrom(3)) // p3's accept arrives: NACK (promised 1.4)
		c.Network.Drain(notAccept)     // p3 re-prepares at a higher round and gets promises
		c.Network.Drain(acceptFrom(4)) // p4's accept arrives: NACK
		c.Network.Drain(notAccept)
		fmt.Printf("after exchange %d: p3 at ballot %s, p4 at ballot %s, chosen=%s\n", k, c.Proposers[0].Ballot, c.Proposers[1].Ballot, show(c.ChosenValue()))
	}
	c.Network.Drain(func(Message) bool { return true }) // stop scheduling adversarially: whichever accept lands first wins
	fmt.Printf("schedule released: chosen=%s, rounds used=%d\n", show(c.ChosenValue()), c.MaxRound())
	fmt.Println("fix: one distinguished proposer (a leader), or randomized backoff before retrying -- Lesson 05 builds the leader.")
}

func main() {
	demoLossless()
	acceptorsForNames, proposersForNames = 5, 3
	demoSeededRuns()
	acceptorsForNames, proposersForNames = 3, 2
	demoDuel()
}
