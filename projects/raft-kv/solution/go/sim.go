// Project: projects/raft-kv — deterministic simulation substrate (PROVIDED; do not edit).
// Clock, seeded Rng, message types, Config, Node interface, and the Network with drop/delay/partition/crash.
// Sources: Ongaro & Ousterhout, "In Search of an Understandable Consensus Algorithm" (USENIX ATC 2014), Figure 2.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"container/heap"
	"errors"
	"fmt"
	"slices"
	"sort"
)

// Clock is the injected clock. Time is an integer tick count; nothing in the project reads a wall clock.
type Clock struct {
	now int
}

func NewClock() *Clock { return &Clock{} }

func (c *Clock) Now() int { return c.now }

func (c *Clock) Advance() { c.now++ }

// Rng is SplitMix64 (Steele, Lea, Flood, OOPSLA 2014), implemented by hand so that the same seed produces the same
// election timeouts, delays, and drops in Java, Python, and Go: a bug found in one language replays in all three.
// The state is uint64 so that the shifts are logical, like Java's >>>; NextLong converts back to Java's signed long.
type Rng struct {
	state uint64
}

func NewRng(seed int64) *Rng { return &Rng{state: uint64(seed)} }

func (r *Rng) NextLong() int64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return int64(z ^ (z >> 31))
}

// NextInt is a uniform integer in [0, bound). Uses the top 31 bits so the result is identical across languages.
func (r *Rng) NextInt(bound int) int { return int((uint64(r.NextLong()) >> 33) % uint64(bound)) }

// NextDouble is a uniform float64 in [0, 1) built from the top 53 bits.
func (r *Rng) NextDouble() float64 { return float64(uint64(r.NextLong())>>11) * 0x1.0p-53 }

type Role int

const (
	Follower Role = iota
	Candidate
	Leader
)

func (r Role) String() string { return [...]string{"FOLLOWER", "CANDIDATE", "LEADER"}[r] }

// MessageKind: Raft has two RPCs. On a simulated network each RPC is two one-way messages: the request and its reply.
type MessageKind int

const (
	RequestVote MessageKind = iota
	RequestVoteReply
	AppendEntries
	AppendEntriesReply
)

func (k MessageKind) String() string {
	return [...]string{"REQUEST_VOTE", "REQUEST_VOTE_REPLY", "APPEND_ENTRIES", "APPEND_ENTRIES_REPLY"}[k]
}

// LogEntry is one log entry: the term in which the leader created it and the client command. Its index is its position in the log.
type LogEntry struct {
	Term    int64
	Command string
}

func (e LogEntry) String() string { return fmt.Sprintf("t%d:%s", e.Term, e.Command) }

// Message is one message. Every message carries the sender's current term (Figure 2: every RPC and reply includes the term).
// RequestVote: PrevLogIndex/PrevLogTerm carry the candidate's last log index and term (section 5.4.1).
// AppendEntries: PrevLogIndex/PrevLogTerm name the entry immediately before Entries (the consistency check);
// LeaderCommit is the leader's commitIndex. Success doubles as voteGranted; MatchIndex on a successful
// AppendEntriesReply tells the leader how far the follower's log now matches.
type Message struct {
	Kind         MessageKind
	From         int
	To           int
	Term         int64
	PrevLogIndex int
	PrevLogTerm  int64
	Entries      []LogEntry
	LeaderCommit int
	Success      bool
	MatchIndex   int
}

func NewRequestVote(from, to int, term int64, lastLogIndex int, lastLogTerm int64) Message {
	return Message{Kind: RequestVote, From: from, To: to, Term: term, PrevLogIndex: lastLogIndex, PrevLogTerm: lastLogTerm, Entries: []LogEntry{}}
}

func NewVoteReply(from, to int, term int64, granted bool) Message {
	return Message{Kind: RequestVoteReply, From: from, To: to, Term: term, Entries: []LogEntry{}, Success: granted}
}

func NewAppendEntries(from, to int, term int64, prevLogIndex int, prevLogTerm int64, entries []LogEntry, leaderCommit int) Message {
	return Message{Kind: AppendEntries, From: from, To: to, Term: term, PrevLogIndex: prevLogIndex, PrevLogTerm: prevLogTerm,
		Entries: slices.Clone(entries), LeaderCommit: leaderCommit}
}

func NewAppendReply(from, to int, term int64, success bool, matchIndex int) Message {
	return Message{Kind: AppendEntriesReply, From: from, To: to, Term: term, Entries: []LogEntry{}, Success: success, MatchIndex: matchIndex}
}

// Config is timing in ticks. The paper's recommendation: broadcastTime << electionTimeout << MTBF; defaults mirror its 150-300 ms.
type Config struct {
	ElectionTimeoutMin int
	ElectionTimeoutMax int
	HeartbeatInterval  int
}

var DefaultConfig = Config{150, 300, 50}

// NewConfig validates what the Java compact constructor validates.
func NewConfig(electionTimeoutMin, electionTimeoutMax, heartbeatInterval int) (Config, error) {
	if electionTimeoutMin <= 0 || electionTimeoutMax < electionTimeoutMin || heartbeatInterval <= 0 {
		return Config{}, errors.New("bad timing config")
	}
	if heartbeatInterval >= electionTimeoutMin {
		return Config{}, errors.New("heartbeat interval must be well below the election timeout")
	}
	return Config{electionTimeoutMin, electionTimeoutMax, heartbeatInterval}, nil
}

// Tracer receives one line per interesting event, with the tick at which it happened.
type Tracer func(now int, text string)

// TracerNone discards every event.
func TracerNone(int, string) {}

// Node is a participant on the simulated network.
type Node interface {
	ID() int
	// Tick is called once per tick with the current time; returns messages to send.
	Tick(now int) []Message
	// Handle is called once per delivered message; returns replies to send.
	Handle(m Message, now int) []Message
}

// Envelope is a message in flight: delivered at deliverAt, ties broken by send sequence.
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
func (h envelopeHeap) Swap(i, j int)  { h[i], h[j] = h[j], h[i] }
func (h *envelopeHeap) Push(x any)    { *h = append(*h, x.(Envelope)) }
func (h *envelopeHeap) Pop() any      { old := *h; e := old[len(old)-1]; *h = old[:len(old)-1]; return e }
func (h envelopeHeap) peek() Envelope { return h[0] }

// Network is the deterministic simulated network. Delays and drops come from the seeded Rng; delivery order is by
// (deliverAt, sequence number), so a run is a pure function of (seed, program). Partitions, crashes, and restarts are
// explicit operations, which is what makes every bug replayable. No goroutines, no sleeping.
type Network struct {
	clock       *Clock
	rng         *Rng
	dropRate    float64
	maxDelay    int
	nodes       map[int]Node
	inFlight    envelopeHeap
	down        map[int]bool
	sideA       map[int]bool
	partitioned bool
	observer    func()
	seq         int64
	Sent        int
	Delivered   int
	Dropped     int
}

func NewNetwork(clock *Clock, seed int64, dropRate float64, maxDelay int) *Network {
	return &Network{clock: clock, rng: NewRng(seed), dropRate: dropRate, maxDelay: max(1, maxDelay),
		nodes: map[int]Node{}, down: map[int]bool{}, sideA: map[int]bool{}, observer: func() {}}
}

func (n *Network) Add(node Node)               { n.nodes[node.ID()] = node }
func (n *Network) SetObserver(observer func()) { n.observer = observer }
func (n *Network) Now() int                    { return n.clock.Now() }
func (n *Network) InFlight() int               { return len(n.inFlight) }
func (n *Network) IsDown(id int) bool          { return n.down[id] }
func (n *Network) IsPartitioned() bool         { return n.partitioned }

// Partition splits the cluster into sideA and everyone else; messages across the cut are dropped.
func (n *Network) Partition(sideA []int) {
	n.sideA = map[int]bool{}
	for _, id := range sideA {
		n.sideA[id] = true
	}
	n.partitioned = true
}

func (n *Network) Heal() {
	n.partitioned = false
	n.sideA = map[int]bool{}
}

// Crash: a crashed node receives nothing, sends nothing, and does not tick. In-flight messages to it are lost.
func (n *Network) Crash(id int) { n.down[id] = true }

// Restart brings a node back with fresh volatile state (the caller rebuilds it from its Storage).
func (n *Network) Restart(node Node) {
	delete(n.down, node.ID())
	n.nodes[node.ID()] = node
}

func (n *Network) crossesPartition(a, b int) bool { return n.partitioned && n.sideA[a] != n.sideA[b] }

func (n *Network) Send(m Message) {
	n.Sent++
	if n.down[m.From] || n.down[m.To] || n.crossesPartition(m.From, m.To) {
		n.Dropped++
		return
	}
	roll := n.rng.NextDouble() // always draw both numbers so the rng stream is identical across languages
	delay := 1 + n.rng.NextInt(n.maxDelay)
	if roll < n.dropRate {
		n.Dropped++
		return
	}
	heap.Push(&n.inFlight, Envelope{n.clock.Now() + delay, n.seq, m})
	n.seq++
}

func (n *Network) SendAll(ms []Message) {
	for _, m := range ms {
		n.Send(m)
	}
}

// Tick advances the clock one tick: let every live node act on the time (in id order), then deliver everything that is due.
func (n *Network) Tick() {
	n.clock.Advance()
	now := n.clock.Now()
	ids := make([]int, 0, len(n.nodes))
	for id := range n.nodes {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		if !n.down[id] {
			n.SendAll(n.nodes[id].Tick(now))
		}
	}
	for len(n.inFlight) > 0 && n.inFlight.peek().DeliverAt <= now {
		n.deliver(heap.Pop(&n.inFlight).(Envelope))
	}
}

func (n *Network) deliver(e Envelope) {
	m := e.Message
	if n.down[m.To] || n.crossesPartition(m.From, m.To) {
		n.Dropped++
		return
	}
	n.Delivered++
	n.SendAll(n.nodes[m.To].Handle(m, n.clock.Now()))
	n.observer()
}
