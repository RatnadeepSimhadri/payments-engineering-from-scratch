// Lesson: phases/06-replication-and-consensus/05-raft-leader-election/docs/en.md
// Raft I: Terms, Leader Election, and a Simulated Network
// Run:  go run .
// Test: go test ./...
// Sources: Ongaro & Ousterhout, "In Search of an Understandable Consensus Algorithm" (USENIX ATC 2014), Figure 2;
//
//	Ongaro, "Consensus: Bridging Theory and Practice" (PhD thesis, Stanford 2014) for Pre-Vote.
//
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"container/heap"
	"fmt"
	"sort"
)

// Rng is SplitMix64 (Steele, Lea, Flood, OOPSLA 2014), implemented by hand so that the same seed produces the same
// election timeouts, delays, and drops in Java, Python, and Go: a bug found in one language replays in all three.
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

// Message is one message. Every message carries the sender's current term (Figure 2: every RPC and reply includes the
// term). LastLogIndex/LastLogTerm are the candidate's, used by the receiver's "at least as up-to-date" check (5.4.1).
// Granted doubles as "success" on an AppendEntries reply.
type Message struct {
	Kind         MessageKind
	From, To     int
	Term         int64
	LastLogIndex int
	LastLogTerm  int
	Granted      bool
}

func requestVote(from, to int, term int64, lastLogIndex, lastLogTerm int) Message {
	return Message{Kind: RequestVote, From: from, To: to, Term: term, LastLogIndex: lastLogIndex, LastLogTerm: lastLogTerm}
}
func voteReply(from, to int, term int64, granted bool) Message {
	return Message{Kind: RequestVoteReply, From: from, To: to, Term: term, Granted: granted}
}

// heartbeat is an AppendEntries with no entries. Lesson 06 fills it with log entries.
func heartbeat(from, to int, term int64) Message {
	return Message{Kind: AppendEntries, From: from, To: to, Term: term}
}
func heartbeatReply(from, to int, term int64, success bool) Message {
	return Message{Kind: AppendEntriesReply, From: from, To: to, Term: term, Granted: success}
}

// Storage is Figure 2's "Persistent state on all servers (updated on stable storage before responding to RPCs)".
// In this simulation a Storage survives a crash; everything else on the node is lost. VotedFor == -1 means null.
type Storage struct {
	CurrentTerm int64
	VotedFor    int
	Writes      int
}

func NewStorage() *Storage { return &Storage{VotedFor: -1} }

func (s *Storage) Save(term int64, votedFor int) {
	s.CurrentTerm = term
	s.VotedFor = votedFor
	s.Writes++
}

// Config holds timing in ticks. The paper's recommendation: broadcastTime << electionTimeout << MTBF; the defaults
// mirror its 150-300 ms.
type Config struct {
	ElectionTimeoutMin, ElectionTimeoutMax, HeartbeatInterval int
}

var DefaultConfig = Config{150, 300, 50}

func NewConfig(min, max, heartbeat int) (Config, error) {
	if min <= 0 || max < min || heartbeat <= 0 {
		return Config{}, fmt.Errorf("bad timing config")
	}
	if heartbeat >= min {
		return Config{}, fmt.Errorf("heartbeat interval must be well below the election timeout")
	}
	return Config{min, max, heartbeat}, nil
}

// Tracer receives one line per interesting event, with the tick at which it happened.
type Tracer func(now int, text string)

func noTrace(int, string) {}

func name(id int) string { return fmt.Sprintf("n%d", id) }

// RaftNode is one Raft server, election rules only (Figure 2 minus the log). The log is represented by
// LastLogIndex/LastLogTerm so the "at least as up-to-date" vote check can be built and tested now; Lesson 06 replaces
// the two fields with a real log.
type RaftNode struct {
	ID               int
	Peers            []int
	Storage          *Storage
	rng              *Rng
	config           Config
	tracer           Tracer
	Role             Role
	LeaderID         int
	LastLogIndex     int
	LastLogTerm      int
	votesGranted     map[int]bool
	ElectionDeadline int
	NextHeartbeat    int
	ElectionsStarted int
}

func NewRaftNode(id int, peers []int, storage *Storage, rng *Rng, config Config, now int, tracer Tracer) *RaftNode {
	if tracer == nil {
		tracer = noTrace
	}
	n := &RaftNode{ID: id, Peers: append([]int(nil), peers...), Storage: storage, rng: rng, config: config, tracer: tracer,
		Role: Follower, LeaderID: -1, votesGranted: map[int]bool{}}
	n.ResetElectionTimer(now)
	return n
}

func (n *RaftNode) CurrentTerm() int64 { return n.Storage.CurrentTerm }
func (n *RaftNode) VotedFor() int      { return n.Storage.VotedFor }
func (n *RaftNode) ClusterSize() int   { return len(n.Peers) + 1 }
func (n *RaftNode) Majority() int      { return n.ClusterSize()/2 + 1 }
func (n *RaftNode) Votes() int         { return len(n.votesGranted) }

// ResetElectionTimer draws a randomized timeout in [min, max]: the paper's whole answer to split votes (section 5.2).
func (n *RaftNode) ResetElectionTimer(now int) {
	span := n.config.ElectionTimeoutMax - n.config.ElectionTimeoutMin + 1
	n.ElectionDeadline = now + n.config.ElectionTimeoutMin + n.rng.NextInt(span)
}

func (n *RaftNode) Tick(now int) []Message {
	if n.Role == Leader {
		if now >= n.NextHeartbeat {
			return n.sendHeartbeats(now)
		}
		return nil
	}
	// Followers: election timeout elapses without AppendEntries from current leader or granting a vote -> candidate.
	// Candidates: election timeout elapses -> start a new election.
	if now >= n.ElectionDeadline {
		return n.StartElection(now)
	}
	return nil
}

// StartElection is Figure 2, Candidates, "On conversion to candidate".
func (n *RaftNode) StartElection(now int) []Message {
	n.Role = Candidate
	n.Storage.Save(n.Storage.CurrentTerm+1, n.ID) // increment currentTerm, vote for self, persist before sending
	n.LeaderID = -1
	n.votesGranted = map[int]bool{n.ID: true}
	n.ElectionsStarted++
	n.ResetElectionTimer(now)
	n.tracer(now, fmt.Sprintf("%s election timeout -> candidate for term %d, votes for itself", name(n.ID), n.CurrentTerm()))
	out := make([]Message, 0, len(n.Peers))
	for _, p := range n.Peers {
		out = append(out, requestVote(n.ID, p, n.CurrentTerm(), n.LastLogIndex, n.LastLogTerm))
	}
	return out
}

func (n *RaftNode) Handle(m Message, now int) []Message {
	// Figure 2, All Servers: if RPC request or response contains term T > currentTerm, set currentTerm = T, convert to follower.
	if m.Term > n.CurrentTerm() {
		n.becomeFollower(m.Term, now)
	}
	switch m.Kind {
	case RequestVote:
		return n.onRequestVote(m, now)
	case RequestVoteReply:
		return n.onVoteReply(m, now)
	case AppendEntries:
		return n.onAppendEntries(m, now)
	}
	return nil // AppendEntriesReply: Lesson 06 uses replies to drive nextIndex/matchIndex
}

func (n *RaftNode) becomeFollower(term int64, now int) {
	before := n.Role
	n.Role = Follower
	n.LeaderID = -1
	n.votesGranted = map[int]bool{}
	if term > n.CurrentTerm() {
		n.Storage.Save(term, -1) // a new term means votedFor is null again
		suffix := ""
		if before == Leader {
			suffix = " (was LEADER)"
		} else if before == Candidate {
			suffix = " (was candidate)"
		}
		n.tracer(now, fmt.Sprintf("%s sees term %d > its own -> follower%s", name(n.ID), term, suffix))
	}
	// A leader keeps no election timer running, so give it a fresh one when it steps down.
	if before == Leader {
		n.ResetElectionTimer(now)
	}
}

// onRequestVote is Figure 2, RequestVote RPC, "Receiver implementation".
func (n *RaftNode) onRequestVote(m Message, now int) []Message {
	if m.Term < n.CurrentTerm() { // 1. Reply false if term < currentTerm
		return []Message{voteReply(n.ID, m.From, n.CurrentTerm(), false)}
	}
	// 2. If votedFor is null or candidateId, and candidate's log is at least as up-to-date as receiver's log, grant vote
	canVote := n.VotedFor() == -1 || n.VotedFor() == m.From
	logUpToDate := m.LastLogTerm > n.LastLogTerm || (m.LastLogTerm == n.LastLogTerm && m.LastLogIndex >= n.LastLogIndex)
	if canVote && logUpToDate {
		n.Storage.Save(n.CurrentTerm(), m.From) // persist the vote before the reply leaves
		n.ResetElectionTimer(now)               // granting a vote resets the election timer
		n.tracer(now, fmt.Sprintf("%s grants vote to %s for term %d", name(n.ID), name(m.From), m.Term))
		return []Message{voteReply(n.ID, m.From, n.CurrentTerm(), true)}
	}
	return []Message{voteReply(n.ID, m.From, n.CurrentTerm(), false)}
}

func (n *RaftNode) onVoteReply(m Message, now int) []Message {
	// A reply from an earlier election (older term) or after we stopped being a candidate is stale: ignore it.
	if n.Role != Candidate || m.Term != n.CurrentTerm() {
		return nil
	}
	if m.Granted {
		n.votesGranted[m.From] = true
		if len(n.votesGranted) >= n.Majority() {
			return n.becomeLeader(now)
		}
	}
	return nil
}

func (n *RaftNode) becomeLeader(now int) []Message {
	n.Role = Leader
	n.LeaderID = n.ID
	n.tracer(now, fmt.Sprintf("%s wins %d/%d votes -> LEADER of term %d", name(n.ID), len(n.votesGranted), n.ClusterSize(), n.CurrentTerm()))
	return n.sendHeartbeats(now) // Leaders: upon election send initial empty AppendEntries to each server
}

func (n *RaftNode) sendHeartbeats(now int) []Message {
	n.NextHeartbeat = now + n.config.HeartbeatInterval
	out := make([]Message, 0, len(n.Peers))
	for _, p := range n.Peers {
		out = append(out, heartbeat(n.ID, p, n.CurrentTerm()))
	}
	return out
}

// onAppendEntries is Figure 2, AppendEntries receiver rule 1, plus Candidates: "if AppendEntries received from new
// leader: convert to follower". It panics on two leaders in one term because that is a safety violation, not an input.
func (n *RaftNode) onAppendEntries(m Message, now int) []Message {
	if m.Term < n.CurrentTerm() {
		return []Message{heartbeatReply(n.ID, m.From, n.CurrentTerm(), false)}
	}
	if n.Role == Leader {
		panic(fmt.Sprintf("two leaders in term %d: %d and %d", n.CurrentTerm(), n.ID, m.From))
	}
	if n.Role == Candidate {
		n.Role = Follower
		n.votesGranted = map[int]bool{}
		n.tracer(now, fmt.Sprintf("%s hears leader %s for term %d -> candidate steps down", name(n.ID), name(m.From), m.Term))
	}
	n.LeaderID = m.From
	n.ResetElectionTimer(now)
	return []Message{heartbeatReply(n.ID, m.From, n.CurrentTerm(), true)}
}

// envelope is a message in flight; ordering is (deliverAt, seq) so delivery is deterministic.
type envelope struct {
	deliverAt int
	seq       int64
	message   Message
}

type envelopeHeap []envelope

func (h envelopeHeap) Len() int { return len(h) }
func (h envelopeHeap) Less(i, j int) bool {
	if h[i].deliverAt != h[j].deliverAt {
		return h[i].deliverAt < h[j].deliverAt
	}
	return h[i].seq < h[j].seq
}
func (h envelopeHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *envelopeHeap) Push(x interface{}) { *h = append(*h, x.(envelope)) }
func (h *envelopeHeap) Pop() interface{} {
	old := *h
	e := old[len(old)-1]
	*h = old[:len(old)-1]
	return e
}

// Network is a deterministic simulated network. Delays and drops come from the seeded Rng; delivery order is by
// (deliverAt, sequence number), so a run is a pure function of (seed, program). Partitions, crashes, and restarts are
// explicit operations, which is what makes every bug replayable. No goroutines, no sleeping.
type Network struct {
	rng         *Rng
	dropRate    float64
	maxDelay    int
	nodes       map[int]*RaftNode
	inFlight    envelopeHeap
	down        map[int]bool
	sideA       map[int]bool
	partitioned bool
	observer    func()
	now         int
	seq         int64
	Sent        int
	Delivered   int
	Dropped     int
}

func NewNetwork(seed int64, dropRate float64, maxDelay int) *Network {
	if maxDelay < 1 {
		maxDelay = 1
	}
	return &Network{rng: NewRng(seed), dropRate: dropRate, maxDelay: maxDelay, nodes: map[int]*RaftNode{},
		down: map[int]bool{}, sideA: map[int]bool{}, observer: func() {}}
}

func (w *Network) Add(n *RaftNode)       { w.nodes[n.ID] = n }
func (w *Network) SetObserver(f func())  { w.observer = f }
func (w *Network) Now() int              { return w.now }
func (w *Network) InFlight() int         { return len(w.inFlight) }
func (w *Network) IsDown(id int) bool    { return w.down[id] }
func (w *Network) Crash(id int)          { w.down[id] = true }
func (w *Network) Restart(n *RaftNode)   { delete(w.down, n.ID); w.nodes[n.ID] = n }
func (w *Network) Heal()                 { w.partitioned = false; w.sideA = map[int]bool{} }
func (w *Network) crosses(a, b int) bool { return w.partitioned && w.sideA[a] != w.sideA[b] }

// Partition splits the cluster into sideA and everyone else; messages across the cut are dropped.
func (w *Network) Partition(sideA []int) {
	w.sideA = map[int]bool{}
	for _, id := range sideA {
		w.sideA[id] = true
	}
	w.partitioned = true
}

func (w *Network) Send(m Message) {
	w.Sent++
	if w.down[m.From] || w.down[m.To] || w.crosses(m.From, m.To) {
		w.Dropped++
		return
	}
	roll := w.rng.NextDouble() // always draw both numbers so the rng stream is identical across languages
	delay := 1 + w.rng.NextInt(w.maxDelay)
	if roll < w.dropRate {
		w.Dropped++
		return
	}
	heap.Push(&w.inFlight, envelope{w.now + delay, w.seq, m})
	w.seq++
}

func (w *Network) SendAll(ms []Message) {
	for _, m := range ms {
		w.Send(m)
	}
}

// Tick advances the clock one tick: every live node acts on the time (in id order), then everything due is delivered.
func (w *Network) Tick() {
	w.now++
	ids := make([]int, 0, len(w.nodes))
	for id := range w.nodes {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		if !w.down[id] {
			w.SendAll(w.nodes[id].Tick(w.now))
		}
	}
	for len(w.inFlight) > 0 && w.inFlight[0].deliverAt <= w.now {
		w.deliver(heap.Pop(&w.inFlight).(envelope))
	}
}

func (w *Network) deliver(e envelope) {
	m := e.message
	if w.down[m.To] || w.crosses(m.From, m.To) {
		w.Dropped++
		return
	}
	w.Delivered++
	w.SendAll(w.nodes[m.To].Handle(m, w.now))
	w.observer()
}

// Cluster wires N RaftNodes onto one network, drives the clock, and checks Election Safety after every delivery.
type Cluster struct {
	Size          int
	Seed          int64
	Config        Config
	Network       *Network
	Storages      []*Storage
	Nodes         []*RaftNode
	Events        []string
	LeadersByTerm map[int64]int
	restarts      []int
}

func NewCluster(size int, seed int64, dropRate float64, maxDelay int) *Cluster {
	return NewClusterWithConfig(size, seed, dropRate, maxDelay, DefaultConfig)
}

func NewClusterWithConfig(size int, seed int64, dropRate float64, maxDelay int, config Config) *Cluster {
	c := &Cluster{Size: size, Seed: seed, Config: config, Network: NewNetwork(seed, dropRate, maxDelay),
		LeadersByTerm: map[int64]int{}, restarts: make([]int, size)}
	for i := 0; i < size; i++ {
		c.Storages = append(c.Storages, NewStorage())
		n := NewRaftNode(i, c.peersOf(i), c.Storages[i], nodeRng(seed, i, 0), config, 0, c.trace)
		c.Nodes = append(c.Nodes, n)
		c.Network.Add(n)
	}
	c.Network.SetObserver(c.CheckElectionSafety)
	return c
}

func (c *Cluster) peersOf(id int) []int {
	ps := make([]int, 0, c.Size-1)
	for j := 0; j < c.Size; j++ {
		if j != id {
			ps = append(ps, j)
		}
	}
	return ps
}

// nodeRng gives each node its own rng stream derived from the seed, so the network's and the nodes' draws never interleave.
func nodeRng(seed int64, id, restart int) *Rng {
	return NewRng(seed*7919 + 1000003*int64(id+1) + 17*int64(restart))
}

func (c *Cluster) trace(now int, text string) {
	c.Events = append(c.Events, fmt.Sprintf("t=%-4d %s", now, text))
}

func (c *Cluster) Now() int              { return c.Network.Now() }
func (c *Cluster) Node(id int) *RaftNode { return c.Nodes[id] }

func (c *Cluster) Tick() {
	c.Network.Tick()
	c.CheckElectionSafety()
}

func (c *Cluster) Run(ticks int) {
	for i := 0; i < ticks; i++ {
		c.Tick()
	}
}

// RunUntilStableLeader runs until exactly one live node is a leader and every other live node follows it (same term).
// It returns the tick, or -1.
func (c *Cluster) RunUntilStableLeader(maxTicks int) int {
	for i := 0; i < maxTicks; i++ {
		c.Tick()
		if c.StableLeader() != nil {
			return c.Now()
		}
	}
	return -1
}

// StableLeader is the leader whose term every live, reachable node agrees on, or nil.
func (c *Cluster) StableLeader() *RaftNode {
	var leader *RaftNode
	for _, n := range c.Nodes {
		if c.Network.IsDown(n.ID) {
			continue
		}
		if n.Role == Leader {
			if leader != nil {
				return nil
			}
			leader = n
		}
	}
	if leader == nil {
		return nil
	}
	for _, n := range c.Nodes {
		if c.Network.IsDown(n.ID) || n == leader {
			continue
		}
		if n.CurrentTerm() != leader.CurrentTerm() || n.LeaderID != leader.ID {
			return nil
		}
	}
	return leader
}

func (c *Cluster) Leaders() []*RaftNode {
	var ls []*RaftNode
	for _, n := range c.Nodes {
		if !c.Network.IsDown(n.ID) && n.Role == Leader {
			ls = append(ls, n)
		}
	}
	return ls
}

func (c *Cluster) MaxTerm() int64 {
	var t int64
	for _, n := range c.Nodes {
		if n.CurrentTerm() > t {
			t = n.CurrentTerm()
		}
	}
	return t
}

// CheckElectionSafety enforces Figure 3's Election Safety: at most one leader can be elected in a given term.
func (c *Cluster) CheckElectionSafety() {
	for _, n := range c.Nodes {
		if n.Role != Leader {
			continue
		}
		existing, ok := c.LeadersByTerm[n.CurrentTerm()]
		if !ok {
			c.LeadersByTerm[n.CurrentTerm()] = n.ID
		} else if existing != n.ID {
			panic(fmt.Sprintf("ELECTION SAFETY VIOLATED: term %d has leaders %d and %d", n.CurrentTerm(), existing, n.ID))
		}
	}
}

func (c *Cluster) Partition(sideA ...int) {
	c.Network.Partition(sideA)
	sorted := append([]int(nil), sideA...)
	sort.Ints(sorted)
	c.trace(c.Now(), fmt.Sprintf("network partition: %s | rest", formatIDs(sorted)))
}

func formatIDs(ids []int) string {
	s := "["
	for i, id := range ids {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprint(id)
	}
	return s + "]"
}

func (c *Cluster) Heal() { c.Network.Heal(); c.trace(c.Now(), "network heals") }

func (c *Cluster) Crash(id int) {
	c.Network.Crash(id)
	c.trace(c.Now(), fmt.Sprintf("%s crashes (volatile state lost, Storage kept)", name(id)))
}

// Restart rebuilds a node from stable storage: currentTerm and votedFor survive; role, leaderId, votes do not.
func (c *Cluster) Restart(id int) *RaftNode {
	c.restarts[id]++
	n := NewRaftNode(id, c.peersOf(id), c.Storages[id], nodeRng(c.Seed, id, c.restarts[id]), c.Config, c.Now(), c.trace)
	c.Nodes[id] = n
	c.Network.Restart(n)
	voted := "null"
	if n.VotedFor() >= 0 {
		voted = name(n.VotedFor())
	}
	c.trace(c.Now(), fmt.Sprintf("%s restarts as follower, term %d, votedFor %s", name(id), n.CurrentTerm(), voted))
	return n
}

// ExpireTimer forces a node's election timer to expire on the next tick (for scripted scenarios).
func (c *Cluster) ExpireTimer(id int) { c.Nodes[id].ElectionDeadline = c.Now() }

func printEvents(c *Cluster, from int) {
	for _, e := range c.Events[from:] {
		fmt.Println(e)
	}
}

func main() {
	fmt.Println("== Election on a 5-node cluster (seed 7, no drops, delay 1-5 ticks) ==")
	fmt.Println("t=0    all nodes are followers in term 0; election timeouts drawn from [150, 300] ticks, heartbeat every 50")
	c := NewCluster(5, 7, 0.0, 5)
	electedAt := c.RunUntilStableLeader(2000)
	printEvents(c, 0)
	leader := c.StableLeader()
	fmt.Printf("leader %s, term %d, stable at tick %d; messages sent=%d delivered=%d dropped=%d\n",
		name(leader.ID), leader.CurrentTerm(), electedAt, c.Network.Sent, c.Network.Delivered, c.Network.Dropped)
	before := len(c.Events)
	c.Run(1000)
	fmt.Printf("after 1000 more ticks of heartbeats: term still %d, %d new events, leader unchanged = %v\n",
		c.MaxTerm(), len(c.Events)-before, c.StableLeader() == leader)

	fmt.Println()
	fmt.Println("== Partition: isolate the leader with one follower ==")
	follower := (leader.ID + 1) % 5
	before = len(c.Events)
	c.Partition(leader.ID, follower)
	c.Run(700)
	printEvents(c, before)
	newest := c.Leaders()[0]
	for _, l := range c.Leaders() {
		if l.CurrentTerm() > newest.CurrentTerm() {
			newest = l
		}
	}
	fmt.Printf("minority side: %s still believes it leads term %d; majority side elected %s for term %d (%d leaders alive, different terms)\n",
		name(leader.ID), leader.CurrentTerm(), name(newest.ID), newest.CurrentTerm(), len(c.Leaders()))
	before = len(c.Events)
	c.Heal()
	healedAt := c.RunUntilStableLeader(2000)
	printEvents(c, before)
	after := c.StableLeader()
	fmt.Printf("after heal: single leader %s in term %d at tick %d; old leader %s is now %s\n",
		name(after.ID), after.CurrentTerm(), healedAt, name(leader.ID), leader.Role)

	fmt.Println()
	fmt.Println("== Split vote on a 4-node cluster: first seed whose first leader needs more than one term ==")
	for seed := int64(1); seed <= 500; seed++ {
		s := NewCluster(4, seed, 0.0, 3)
		s.RunUntilStableLeader(3000)
		if l := s.StableLeader(); l != nil && l.CurrentTerm() > 1 {
			fmt.Printf("seed %d:\n", seed)
			printEvents(s, 0)
			break
		}
	}

	fmt.Println()
	fmt.Println("== votedFor must be persisted ==")
	durable := NewStorage()
	n1 := NewRaftNode(1, []int{0, 2}, durable, NewRng(1), DefaultConfig, 0, nil)
	r1 := n1.Handle(requestVote(0, 1, 1, 0, 0), 10)[0]
	fmt.Printf("n1 receives RequestVote(term 1) from n0 -> granted=%v, votedFor=%s\n", r1.Granted, name(n1.VotedFor()))
	n1b := NewRaftNode(1, []int{0, 2}, durable, NewRng(2), DefaultConfig, 11, nil)
	r2 := n1b.Handle(requestVote(2, 1, 1, 0, 0), 12)[0]
	fmt.Printf("n1 crashes and restarts from Storage (term %d, votedFor %s); RequestVote(term 1) from n2 -> granted=%v\n",
		n1b.CurrentTerm(), name(n1b.VotedFor()), r2.Granted)
	n1c := NewRaftNode(1, []int{0, 2}, NewStorage(), NewRng(3), DefaultConfig, 11, nil)
	r3 := n1c.Handle(requestVote(2, 1, 1, 0, 0), 12)[0]
	fmt.Printf("counterfactual, votedFor lost on restart: RequestVote(term 1) from n2 -> granted=%v  (two votes in one term: Election Safety breaks)\n", r3.Granted)

	fmt.Println()
	fmt.Println("== 30 seeds, 5 nodes, 20% drops, delay 1-10, 3000 ticks each: Election Safety holds ==")
	elected, worst, terms := 0, 0, 0
	for seed := int64(1); seed <= 30; seed++ {
		s := NewCluster(5, seed, 0.2, 10)
		t := s.RunUntilStableLeader(3000)
		if t > 0 {
			elected++
			if t > worst {
				worst = t
			}
		}
		if t < 0 {
			t = 0
		}
		s.Run(3000 - t)
		terms += len(s.LeadersByTerm)
	}
	fmt.Printf("seeds with a stable leader: %d/30, slowest election at tick %d, %d (term, leader) pairs recorded, never two leaders in one term\n",
		elected, worst, terms)
}
