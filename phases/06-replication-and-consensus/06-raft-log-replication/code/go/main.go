// Lesson: phases/06-replication-and-consensus/06-raft-log-replication/docs/en.md
// Raft II: Log Replication and Commit
// Run:  go run .
// Test: go test ./...
// Sources: Ongaro & Ousterhout, "In Search of an Understandable Consensus Algorithm" (USENIX ATC 2014), Figure 2,
// sections 5.3 (log replication), 5.4.2 (committing entries from previous terms), 8 (no-op on election).
// Go 1.22+, standard library only. Same design as the Java and Python versions; the network is Lesson 05's.
package main

import (
	"container/heap"
	"fmt"
	"sort"
	"strings"
)

// Rng is SplitMix64 (Steele, Lea, Flood, OOPSLA 2014): identical streams in Java, Python, and Go, so a seed replays everywhere.
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

func (r *Rng) NextDouble() float64 { return float64(r.NextLong()>>11) * (1.0 / (1 << 53)) }

type Role int

const (
	Follower Role = iota
	Candidate
	Leader
)

func (r Role) String() string { return [...]string{"FOLLOWER", "CANDIDATE", "LEADER"}[r] }

type MessageKind int

const (
	RequestVote MessageKind = iota
	RequestVoteReply
	AppendEntries
	AppendEntriesReply
)

// LogEntry is one log entry: the term in which the leader created it and the client command. Index is its position in the log.
type LogEntry struct {
	Term    int64
	Command string
}

func (e LogEntry) String() string { return fmt.Sprintf("t%d:%s", e.Term, e.Command) }

// Message is one message. For RequestVote, PrevLogIndex/PrevLogTerm carry the candidate's last log index and term. For
// AppendEntries they are the entry immediately before Entries, which is the consistency check. Success doubles as
// voteGranted; MatchIndex on a successful AppendEntriesReply tells the leader how far the follower's log now matches.
type Message struct {
	Kind         MessageKind
	From, To     int
	Term         int64
	PrevLogIndex int
	PrevLogTerm  int64
	Entries      []LogEntry
	LeaderCommit int
	Success      bool
	MatchIndex   int
}

func requestVote(from, to int, term int64, lastLogIndex int, lastLogTerm int64) Message {
	return Message{Kind: RequestVote, From: from, To: to, Term: term, PrevLogIndex: lastLogIndex, PrevLogTerm: lastLogTerm}
}

func voteReply(from, to int, term int64, granted bool) Message {
	return Message{Kind: RequestVoteReply, From: from, To: to, Term: term, Success: granted}
}

func appendEntries(from, to int, term int64, prevLogIndex int, prevLogTerm int64, entries []LogEntry, leaderCommit int) Message {
	copied := append([]LogEntry(nil), entries...)
	return Message{Kind: AppendEntries, From: from, To: to, Term: term, PrevLogIndex: prevLogIndex, PrevLogTerm: prevLogTerm,
		Entries: copied, LeaderCommit: leaderCommit}
}

func appendReply(from, to int, term int64, success bool, matchIndex int) Message {
	return Message{Kind: AppendEntriesReply, From: from, To: to, Term: term, Success: success, MatchIndex: matchIndex}
}

// Storage is Figure 2's persistent state: currentTerm, votedFor, and log[]. The log is 1-indexed as in the paper; slot 0
// holds a sentinel entry with term 0 so that prevLogIndex = 0 always matches. Survives Crash(); everything else is lost.
type Storage struct {
	CurrentTerm int64
	VotedFor    int
	Log         []LogEntry
	Writes      int
}

func NewStorage() *Storage { return &Storage{VotedFor: -1, Log: []LogEntry{{0, "<sentinel>"}}} }

func (s *Storage) Save(term int64, votedFor int) {
	s.CurrentTerm = term
	s.VotedFor = votedFor
	s.Writes++
}

func (s *Storage) Append(e LogEntry) {
	s.Log = append(s.Log, e)
	s.Writes++
}

// Truncate deletes the entry at fromIndex and everything after it (Figure 2, AppendEntries receiver rule 3).
func (s *Storage) Truncate(fromIndex int) {
	s.Log = s.Log[:fromIndex]
	s.Writes++
}

func (s *Storage) LastIndex() int         { return len(s.Log) - 1 }
func (s *Storage) TermAt(index int) int64 { return s.Log[index].Term }

// KVStore is the replicated state machine: a key-value store. Commands are "SET k v", "DEL k", or "NOOP".
type KVStore struct {
	Data            map[string]string
	AppliedCommands []string
}

func NewKVStore() *KVStore { return &KVStore{Data: map[string]string{}} }

func (kv *KVStore) Apply(command string) error {
	parts := strings.Split(command, " ")
	switch parts[0] {
	case "SET":
		if len(parts) != 3 {
			return fmt.Errorf("SET needs key and value: %s", command)
		}
		kv.Data[parts[1]] = parts[2]
	case "DEL":
		if len(parts) != 2 {
			return fmt.Errorf("DEL needs key: %s", command)
		}
		delete(kv.Data, parts[1])
	case "NOOP":
	default:
		return fmt.Errorf("unknown command: %s", command)
	}
	kv.AppliedCommands = append(kv.AppliedCommands, command)
	return nil
}

// Get returns the value and whether the key exists (the Java/Python versions return null/None).
func (kv *KVStore) Get(key string) (string, bool) {
	v, ok := kv.Data[key]
	return v, ok
}

func (kv *KVStore) String() string {
	keys := make([]string, 0, len(kv.Data))
	for k := range kv.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+kv.Data[k])
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

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

type Tracer func(now int, text string)

func noTrace(int, string) {}

func name(id int) string { return fmt.Sprintf("n%d", id) }

// RaftNode is one Raft server: Lesson 05's election plus Figure 2's log replication, commit, and apply rules.
type RaftNode struct {
	ID      int
	Peers   []int
	Storage *Storage
	rng     *Rng
	config  Config
	tracer  Tracer

	Role     Role
	LeaderID int
	// Volatile state on all servers (Figure 2). Rebuilt after a restart by re-applying the persisted log.
	CommitIndex  int
	LastApplied  int
	StateMachine *KVStore
	// Volatile state on leaders, reinitialised after election.
	NextIndex  map[int]int
	MatchIndex map[int]int

	votesGranted          map[int]bool
	ElectionDeadline      int
	NextHeartbeat         int
	ConsistencyRejections int
}

func NewRaftNode(id int, peers []int, storage *Storage, rng *Rng, config Config, now int, tracer Tracer) *RaftNode {
	if tracer == nil {
		tracer = noTrace
	}
	n := &RaftNode{ID: id, Peers: append([]int(nil), peers...), Storage: storage, rng: rng, config: config, tracer: tracer,
		Role: Follower, LeaderID: -1, StateMachine: NewKVStore(), NextIndex: map[int]int{}, MatchIndex: map[int]int{},
		votesGranted: map[int]bool{}}
	n.ResetElectionTimer(now)
	return n
}

func (n *RaftNode) CurrentTerm() int64 { return n.Storage.CurrentTerm }
func (n *RaftNode) VotedFor() int      { return n.Storage.VotedFor }
func (n *RaftNode) ClusterSize() int   { return len(n.Peers) + 1 }
func (n *RaftNode) Majority() int      { return n.ClusterSize()/2 + 1 }
func (n *RaftNode) Log() []LogEntry    { return n.Storage.Log }
func (n *RaftNode) LastLogIndex() int  { return n.Storage.LastIndex() }
func (n *RaftNode) LastLogTerm() int64 { return n.Storage.TermAt(n.Storage.LastIndex()) }

func (n *RaftNode) ResetElectionTimer(now int) {
	span := n.config.ElectionTimeoutMax - n.config.ElectionTimeoutMin + 1
	n.ElectionDeadline = now + n.config.ElectionTimeoutMin + n.rng.NextInt(span)
}

func (n *RaftNode) Tick(now int) []Message {
	if n.Role == Leader {
		if now >= n.NextHeartbeat {
			return n.ReplicateAll(now)
		}
		return nil
	}
	if now >= n.ElectionDeadline {
		return n.StartElection(now)
	}
	return nil
}

// ---------------------------------------------------------------- election (Lesson 05, unchanged except the log check)

func (n *RaftNode) StartElection(now int) []Message {
	n.Role = Candidate
	n.Storage.Save(n.Storage.CurrentTerm+1, n.ID)
	n.LeaderID = -1
	n.votesGranted = map[int]bool{n.ID: true}
	n.ResetElectionTimer(now)
	n.tracer(now, fmt.Sprintf("%s election timeout -> candidate for term %d", name(n.ID), n.CurrentTerm()))
	out := make([]Message, 0, len(n.Peers))
	for _, p := range n.Peers {
		out = append(out, requestVote(n.ID, p, n.CurrentTerm(), n.LastLogIndex(), n.LastLogTerm()))
	}
	return out
}

func (n *RaftNode) Handle(m Message, now int) []Message {
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
	default:
		return n.onAppendReply(m, now)
	}
}

func (n *RaftNode) becomeFollower(term int64, now int) {
	before := n.Role
	n.Role = Follower
	n.LeaderID = -1
	n.votesGranted = map[int]bool{}
	if term > n.CurrentTerm() {
		n.Storage.Save(term, -1)
		suffix := ""
		if before == Leader {
			suffix = " (was LEADER)"
		}
		n.tracer(now, fmt.Sprintf("%s sees term %d -> follower%s", name(n.ID), term, suffix))
	}
	if before == Leader {
		n.ResetElectionTimer(now)
	}
}

func (n *RaftNode) onRequestVote(m Message, now int) []Message {
	if m.Term < n.CurrentTerm() {
		return []Message{voteReply(n.ID, m.From, n.CurrentTerm(), false)}
	}
	canVote := n.VotedFor() == -1 || n.VotedFor() == m.From
	// Section 5.4.1: the candidate's log must be at least as up-to-date as ours, now measured on the real log.
	logUpToDate := m.PrevLogTerm > n.LastLogTerm() || (m.PrevLogTerm == n.LastLogTerm() && m.PrevLogIndex >= n.LastLogIndex())
	if canVote && logUpToDate {
		n.Storage.Save(n.CurrentTerm(), m.From)
		n.ResetElectionTimer(now)
		return []Message{voteReply(n.ID, m.From, n.CurrentTerm(), true)}
	}
	return []Message{voteReply(n.ID, m.From, n.CurrentTerm(), false)}
}

func (n *RaftNode) onVoteReply(m Message, now int) []Message {
	if n.Role != Candidate || m.Term != n.CurrentTerm() {
		return nil
	}
	if m.Success {
		n.votesGranted[m.From] = true
		if len(n.votesGranted) >= n.Majority() {
			return n.becomeLeader(now)
		}
	}
	return nil
}

// becomeLeader: Figure 2, Leaders: reinitialise nextIndex/matchIndex; section 8: commit a no-op to learn the commit index quickly.
func (n *RaftNode) becomeLeader(now int) []Message {
	n.Role = Leader
	n.LeaderID = n.ID
	for _, p := range n.Peers {
		n.NextIndex[p] = n.LastLogIndex() + 1
		n.MatchIndex[p] = 0
	}
	n.tracer(now, fmt.Sprintf("%s wins -> LEADER of term %d, log ends at index %d", name(n.ID), n.CurrentTerm(), n.LastLogIndex()))
	n.Storage.Append(LogEntry{n.CurrentTerm(), "NOOP"})
	return n.ReplicateAll(now)
}

// ---------------------------------------------------------------- log replication (section 5.3)

// Propose is the client entry point. Appends to the leader's log. Returns the new index, or -1 if not leader.
func (n *RaftNode) Propose(command string) int {
	if n.Role != Leader {
		return -1
	}
	n.Storage.Append(LogEntry{n.CurrentTerm(), command})
	return n.LastLogIndex()
}

// ReplicateAll sends one AppendEntries per peer, carrying everything from nextIndex onward (an empty list is a heartbeat).
func (n *RaftNode) ReplicateAll(now int) []Message {
	n.NextHeartbeat = now + n.config.HeartbeatInterval
	out := make([]Message, 0, len(n.Peers))
	for _, p := range n.Peers {
		out = append(out, n.AppendEntriesFor(p))
	}
	return out
}

func (n *RaftNode) AppendEntriesFor(peer int) Message {
	next := n.NextIndex[peer]
	prev := next - 1
	return appendEntries(n.ID, peer, n.CurrentTerm(), prev, n.Storage.TermAt(prev), n.Storage.Log[next:], n.CommitIndex)
}

// onAppendEntries is Figure 2, AppendEntries RPC, receiver implementation, rules 1-5 in order.
func (n *RaftNode) onAppendEntries(m Message, now int) []Message {
	if m.Term < n.CurrentTerm() { // 1
		return []Message{appendReply(n.ID, m.From, n.CurrentTerm(), false, 0)}
	}
	if n.Role == Leader {
		panic(fmt.Sprintf("two leaders in term %d", n.CurrentTerm()))
	}
	if n.Role == Candidate {
		n.Role = Follower
		n.votesGranted = map[int]bool{}
	}
	n.LeaderID = m.From
	n.ResetElectionTimer(now)
	if m.PrevLogIndex > n.LastLogIndex() || n.Storage.TermAt(m.PrevLogIndex) != m.PrevLogTerm { // 2
		n.ConsistencyRejections++
		n.tracer(now, fmt.Sprintf("%s rejects AppendEntries: no entry (%d, t%d); my log ends at %d",
			name(n.ID), m.PrevLogIndex, m.PrevLogTerm, n.LastLogIndex()))
		return []Message{appendReply(n.ID, m.From, n.CurrentTerm(), false, 0)}
	}
	index := m.PrevLogIndex
	for _, e := range m.Entries {
		index++
		if index <= n.LastLogIndex() {
			if n.Storage.TermAt(index) == e.Term {
				continue // 4: already have it
			}
			n.tracer(now, fmt.Sprintf("%s conflict at index %d: has %s, leader has %s -> truncating %d entries",
				name(n.ID), index, n.Storage.Log[index], e, n.LastLogIndex()-index+1))
			n.Storage.Truncate(index) // 3
		}
		n.Storage.Append(e) // 4
	}
	lastNew := m.PrevLogIndex + len(m.Entries)
	if m.LeaderCommit > n.CommitIndex { // 5
		n.CommitIndex = lastNew
		if m.LeaderCommit < lastNew {
			n.CommitIndex = m.LeaderCommit
		}
		n.applyCommitted(now)
	}
	return []Message{appendReply(n.ID, m.From, n.CurrentTerm(), true, lastNew)}
}

// onAppendReply: Figure 2, Leaders: on success update nextIndex/matchIndex and try to commit; on failure decrement nextIndex and retry.
func (n *RaftNode) onAppendReply(m Message, now int) []Message {
	if n.Role != Leader || m.Term != n.CurrentTerm() {
		return nil
	}
	if m.Success {
		if m.MatchIndex > n.MatchIndex[m.From] {
			n.MatchIndex[m.From] = m.MatchIndex
			n.NextIndex[m.From] = m.MatchIndex + 1
			n.advanceCommitIndex(now)
		}
		return nil
	}
	if n.NextIndex[m.From]-1 < 1 {
		n.NextIndex[m.From] = 1
	} else {
		n.NextIndex[m.From]--
	}
	return []Message{n.AppendEntriesFor(m.From)}
}

// advanceCommitIndex: Figure 2, Leaders, last rule: if there exists an N > commitIndex such that a majority of
// matchIndex[i] >= N and log[N].term == currentTerm, set commitIndex = N. The term condition is section 5.4.2 (Figure 8).
func (n *RaftNode) advanceCommitIndex(now int) {
	for idx := n.LastLogIndex(); idx > n.CommitIndex; idx-- {
		if n.Storage.TermAt(idx) != n.CurrentTerm() {
			continue
		}
		count := 1 // the leader itself
		for _, p := range n.Peers {
			if n.MatchIndex[p] >= idx {
				count++
			}
		}
		if count >= n.Majority() {
			n.tracer(now, fmt.Sprintf("%s commits through index %d (%d/%d replicas)", name(n.ID), idx, count, n.ClusterSize()))
			n.CommitIndex = idx
			n.applyCommitted(now)
			return
		}
	}
}

// applyCommitted: Figure 2, All Servers: if commitIndex > lastApplied, increment lastApplied and apply log[lastApplied].
func (n *RaftNode) applyCommitted(now int) {
	for n.LastApplied < n.CommitIndex {
		n.LastApplied++
		if err := n.StateMachine.Apply(n.Storage.Log[n.LastApplied].Command); err != nil {
			panic(err)
		}
	}
}

func (n *RaftNode) ReplicatedCount(index int) int {
	count := 1
	for _, p := range n.Peers {
		if n.MatchIndex[p] >= index {
			count++
		}
	}
	return count
}

// ---------------------------------------------------------------- Lesson 05's deterministic network, unchanged

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

// Network: (deliverAt, seq) ordering, seeded delays and drops, partitions, crashes. No goroutines, no sleeping.
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
	roll := w.rng.NextDouble()
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

// ---------------------------------------------------------------- cluster harness with the Figure 3 checkers

// Cluster wires N nodes onto one network and checks the Figure 3 properties after every delivery.
type Cluster struct {
	Size              int
	Seed              int64
	Config            Config
	Network           *Network
	Storages          []*Storage
	Nodes             []*RaftNode
	Events            []string
	LeadersByTerm     map[int64]int
	CommittedByIndex  map[int]LogEntry
	leaderLogSnapshot map[int][]LogEntry
	restarts          []int
	Proposals         int
}

func NewCluster(size int, seed int64, dropRate float64, maxDelay int) *Cluster {
	return NewClusterWithConfig(size, seed, dropRate, maxDelay, DefaultConfig)
}

func NewClusterWithConfig(size int, seed int64, dropRate float64, maxDelay int, config Config) *Cluster {
	c := &Cluster{Size: size, Seed: seed, Config: config, Network: NewNetwork(seed, dropRate, maxDelay),
		LeadersByTerm: map[int64]int{}, CommittedByIndex: map[int]LogEntry{}, leaderLogSnapshot: map[int][]LogEntry{},
		restarts: make([]int, size)}
	for i := 0; i < size; i++ {
		c.Storages = append(c.Storages, NewStorage())
		n := NewRaftNode(i, c.peersOf(i), c.Storages[i], nodeRng(seed, i, 0), config, 0, c.trace)
		c.Nodes = append(c.Nodes, n)
		c.Network.Add(n)
	}
	c.Network.SetObserver(c.CheckInvariants)
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
	c.CheckInvariants()
}

func (c *Cluster) Run(ticks int) {
	for i := 0; i < ticks; i++ {
		c.Tick()
	}
}

func (c *Cluster) RunUntilStableLeader(maxTicks int) int {
	for i := 0; i < maxTicks; i++ {
		c.Tick()
		if c.StableLeader() != nil {
			return c.Now()
		}
	}
	return -1
}

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

// Propose submits a client command to the current stable leader; returns its log index or -1.
func (c *Cluster) Propose(command string) int {
	l := c.StableLeader()
	if l == nil {
		return -1
	}
	return c.ProposeTo(l.ID, command)
}

// ProposeTo submits to a specific node (which may be a stale leader inside a partition).
func (c *Cluster) ProposeTo(nodeID int, command string) int {
	n := c.Nodes[nodeID]
	index := n.Propose(command)
	if index > 0 {
		c.Proposals++
		c.trace(c.Now(), fmt.Sprintf("%s appends index %d %s and replicates", name(nodeID), index, n.Log()[index]))
		c.Network.SendAll(n.ReplicateAll(c.Now()))
	}
	return index
}

// RunUntilApplied runs until every live node has applied through index, or maxTicks pass. Returns the tick or -1.
func (c *Cluster) RunUntilApplied(index int, maxTicks int) int {
	for i := 0; i < maxTicks; i++ {
		c.Tick()
		all := true
		for _, n := range c.Nodes {
			if !c.Network.IsDown(n.ID) && n.LastApplied < index {
				all = false
			}
		}
		if all {
			return c.Now()
		}
	}
	return -1
}

// CheckInvariants enforces Election Safety, Leader Append-Only, Log Matching, and State Machine Safety (Figure 3).
func (c *Cluster) CheckInvariants() {
	for _, n := range c.Nodes {
		if n.Role == Leader {
			existing, ok := c.LeadersByTerm[n.CurrentTerm()]
			if !ok {
				c.LeadersByTerm[n.CurrentTerm()] = n.ID
			} else if existing != n.ID {
				panic(fmt.Sprintf("ELECTION SAFETY: term %d has leaders %d and %d", n.CurrentTerm(), existing, n.ID))
			}
			if previous, ok := c.leaderLogSnapshot[n.ID]; ok {
				limit := len(previous)
				if len(n.Log()) < limit {
					limit = len(n.Log())
				}
				for i := 0; i < limit; i++ {
					if n.Log()[i] != previous[i] {
						panic(fmt.Sprintf("LEADER APPEND-ONLY: %d changed an existing entry while leader", n.ID))
					}
				}
				if len(n.Log()) < len(previous) {
					panic(fmt.Sprintf("LEADER APPEND-ONLY: %d changed an existing entry while leader", n.ID))
				}
			}
			c.leaderLogSnapshot[n.ID] = append([]LogEntry(nil), n.Log()...)
		} else {
			delete(c.leaderLogSnapshot, n.ID)
		}
	}
	for i := 0; i < c.Size; i++ {
		for j := i + 1; j < c.Size; j++ {
			if err := CheckLogMatching(c.Nodes[i].Log(), c.Nodes[j].Log(), i, j); err != nil {
				panic(err)
			}
		}
	}
	for _, n := range c.Nodes {
		for k := 1; k <= n.CommitIndex; k++ {
			seen, ok := c.CommittedByIndex[k]
			if !ok {
				c.CommittedByIndex[k] = n.Log()[k]
			} else if seen != n.Log()[k] {
				panic(fmt.Sprintf("STATE MACHINE SAFETY: index %d committed as %s and %s", k, seen, n.Log()[k]))
			}
		}
	}
}

// CheckLogMatching: if two logs contain an entry with the same index and term, the logs are identical up to that index.
func CheckLogMatching(a, b []LogEntry, ia, ib int) error {
	limit := len(a)
	if len(b) < limit {
		limit = len(b)
	}
	for k := limit - 1; k >= 1; k-- {
		if a[k].Term == b[k].Term {
			for i := 1; i <= k; i++ {
				if a[i] != b[i] {
					return fmt.Errorf("LOG MATCHING: nodes %d and %d agree at index %d (term %d) but differ at index %d: %s vs %s",
						ia, ib, k, a[k].Term, i, a[i], b[i])
				}
			}
			return nil
		}
	}
	return nil
}

func formatIDs(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprint(id)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func (c *Cluster) Partition(sideA ...int) {
	c.Network.Partition(sideA)
	sorted := append([]int(nil), sideA...)
	sort.Ints(sorted)
	c.trace(c.Now(), fmt.Sprintf("network partition: %s | rest", formatIDs(sorted)))
}

func (c *Cluster) Heal() { c.Network.Heal(); c.trace(c.Now(), "network heals") }

func (c *Cluster) Crash(id int) {
	c.Network.Crash(id)
	c.trace(c.Now(), fmt.Sprintf("%s crashes (volatile state lost, Storage kept)", name(id)))
}

// Restart rebuilds a node from stable storage: term, vote, and log survive; commitIndex, lastApplied, and the KV store are rebuilt.
func (c *Cluster) Restart(id int) *RaftNode {
	c.restarts[id]++
	n := NewRaftNode(id, c.peersOf(id), c.Storages[id], nodeRng(c.Seed, id, c.restarts[id]), c.Config, c.Now(), c.trace)
	c.Nodes[id] = n
	c.Network.Restart(n)
	c.trace(c.Now(), fmt.Sprintf("%s restarts: term %d, log length %d, commitIndex 0, empty state machine",
		name(id), n.CurrentTerm(), n.LastLogIndex()))
	return n
}

func (c *Cluster) StateMachinesConverged() bool {
	first := ""
	seen := false
	for _, n := range c.Nodes {
		if c.Network.IsDown(n.ID) {
			continue
		}
		s := fmt.Sprintf("%s@%d", n.StateMachine, n.LastApplied)
		if !seen {
			first, seen = s, true
		} else if s != first {
			return false
		}
	}
	return true
}

func (c *Cluster) LogOf(id int) string {
	log := c.Nodes[id].Log()
	parts := make([]string, 0, len(log)-1)
	for _, e := range log[1:] {
		parts = append(parts, e.String())
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// ---------------------------------------------------------------- demo

func printEvents(c *Cluster, from int) {
	for _, e := range c.Events[from:] {
		fmt.Println(e)
	}
}

// proposeUntilAccepted: a real client retries against the new leader after an election; here that is "run until a stable leader accepts".
func proposeUntilAccepted(c *Cluster, command string) int {
	for attempt := 0; attempt < 500; attempt++ {
		if index := c.Propose(command); index > 0 {
			return index
		}
		c.Run(10)
	}
	panic("no leader accepted " + command)
}

func printLogs(c *Cluster) {
	for _, n := range c.Nodes {
		fmt.Printf("  %s %-9s term %d log=%s commit=%d applied=%d kv=%s\n", name(n.ID), n.Role, n.CurrentTerm(),
			c.LogOf(n.ID), n.CommitIndex, n.LastApplied, n.StateMachine)
	}
}

func valueOrNull(kv *KVStore, key string) string {
	if v, ok := kv.Get(key); ok {
		return v
	}
	return "null"
}

func main() {
	fmt.Println("== Replicate three commands on a 5-node cluster (seed 7, no drops, delay 1-5) ==")
	c := NewCluster(5, 7, 0.0, 5)
	c.RunUntilStableLeader(2000)
	before := len(c.Events)
	c.Propose("SET alice 100")
	c.Propose("SET bob 50")
	last := c.Propose("SET alice 75")
	appliedAt := c.RunUntilApplied(last, 1000)
	printEvents(c, before)
	fmt.Printf("all five state machines applied through index %d at tick %d:\n", last, appliedAt)
	printLogs(c)

	fmt.Println()
	fmt.Println("== A stale leader's uncommitted entries are overwritten ==")
	old := c.StableLeader()
	follower := (old.ID + 1) % 5
	before = len(c.Events)
	c.Partition(old.ID, follower)
	c.ProposeTo(old.ID, "SET alice 999")
	c.ProposeTo(old.ID, "SET carol 1")
	c.Run(600)
	printEvents(c, before)
	fmt.Println("during the partition (minority side cannot commit; majority elected a new leader and committed its own entries):")
	printLogs(c)
	before = len(c.Events)
	c.Heal()
	c.RunUntilStableLeader(2000)
	c.Propose("SET dave 7")
	c.RunUntilApplied(c.StableLeader().LastLogIndex(), 1000)
	printEvents(c, before)
	fmt.Println("after heal: the minority's term-1 entries at the conflicting indexes are truncated and replaced:")
	printLogs(c)
	fmt.Printf("state machines converged = %v; alice=%s (the 999 write was never committed, so it never happened)\n",
		c.StateMachinesConverged(), valueOrNull(c.Node(0).StateMachine, "alice"))

	fmt.Println()
	fmt.Println("== Commit needs a majority of matchIndex, not a reply from anyone ==")
	q := NewCluster(5, 11, 0.0, 5)
	q.RunUntilStableLeader(2000)
	lead := q.StableLeader()
	others := append([]int(nil), lead.Peers...)
	q.Crash(others[0])
	q.Crash(others[1])
	q.Crash(others[2])
	idx := q.Propose("SET balance 1000")
	q.Run(400)
	fmt.Printf("3 of 5 crashed; proposed index %d; after 400 ticks leader commitIndex=%d, entry replicated on %d/5 nodes -> not committed\n",
		idx, lead.CommitIndex, lead.ReplicatedCount(idx))
	q.Restart(others[0])
	q.Run(400)
	fmt.Printf("one node restarted (2 live followers + leader = 3): commitIndex=%d, replicated on %d/5 -> committed and applied: balance=%s\n",
		lead.CommitIndex, lead.ReplicatedCount(idx), valueOrNull(lead.StateMachine, "balance"))

	fmt.Println()
	fmt.Println("== Only entries from the leader's current term are committed by counting replicas (section 5.4.2) ==")
	s := NewStorage()
	s.Append(LogEntry{2, "SET x 1"})
	s.Append(LogEntry{2, "SET x 2"})
	s.Save(4, 0)
	l := NewRaftNode(0, []int{1, 2, 3, 4}, s, NewRng(1), DefaultConfig, 0, nil)
	l.StartElection(1)
	l.Handle(voteReply(1, 0, l.CurrentTerm(), true), 2)
	l.Handle(voteReply(2, 0, l.CurrentTerm(), true), 3)
	entries := make([]string, 0)
	for _, e := range l.Log()[1:] {
		entries = append(entries, e.String())
	}
	fmt.Printf("leader of term %d with log [%s]\n", l.CurrentTerm(), strings.Join(entries, ", "))
	l.Handle(appendReply(1, 0, l.CurrentTerm(), true, 2), 4)
	l.Handle(appendReply(2, 0, l.CurrentTerm(), true, 2), 5)
	fmt.Printf("term-2 entries replicated on 3/5 (indexes 1-2): commitIndex=%d  (counting replicas of an older-term entry would be unsafe)\n", l.CommitIndex)
	l.Handle(appendReply(1, 0, l.CurrentTerm(), true, 3), 6)
	l.Handle(appendReply(2, 0, l.CurrentTerm(), true, 3), 7)
	fmt.Printf("term-%d NOOP at index 3 replicated on 3/5: commitIndex=%d  (the current-term entry commits everything before it)\n", l.CurrentTerm(), l.CommitIndex)

	fmt.Println()
	fmt.Println("== 20 seeds, 5 nodes, 20% drops, delay 1-10, a crash and restart, 12 commands: all four Figure 3 properties hold ==")
	converged := 0
	for seed := int64(1); seed <= 20; seed++ {
		r := NewCluster(5, seed, 0.2, 10)
		r.RunUntilStableLeader(3000)
		for k := 1; k <= 6; k++ {
			proposeUntilAccepted(r, fmt.Sprintf("SET k%d v%d", k, k))
			r.Run(40)
		}
		victim := r.StableLeader()
		if victim == nil {
			victim = r.Node(0)
		}
		r.Crash(victim.ID)
		r.RunUntilStableLeader(3000)
		for k := 7; k <= 12; k++ {
			proposeUntilAccepted(r, fmt.Sprintf("SET k%d v%d", k, k))
			r.Run(40)
		}
		r.Restart(victim.ID)
		r.RunUntilStableLeader(3000)
		fin := r.StableLeader()
		if fin != nil {
			r.RunUntilApplied(fin.LastLogIndex(), 3000)
		}
		if fin != nil && r.StateMachinesConverged() {
			if _, ok := fin.StateMachine.Get("k12"); ok {
				converged++
			}
		}
	}
	fmt.Printf("seeds whose five state machines converged on all 12 writes: %d/20; no invariant check fired\n", converged)
}
