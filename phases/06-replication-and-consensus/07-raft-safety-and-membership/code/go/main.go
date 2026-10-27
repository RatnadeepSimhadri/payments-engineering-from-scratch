// Lesson: phases/06-replication-and-consensus/07-raft-safety-and-membership/docs/en.md
// Raft III: Safety Proofs, Membership Changes, Snapshots
// Run:  go run .
// Test: go test ./...
// Sources: Ongaro & Ousterhout, "In Search of an Understandable Consensus Algorithm" (USENIX ATC 2014): Figure 2, section 5.4
// (safety argument), section 6 (joint consensus, Figures 10-11), section 7 (log compaction, Figure 13: InstallSnapshot).
// Ongaro, "Consensus: Bridging Theory and Practice" (PhD thesis, 2014), chapter 4 (membership changes).
// Go 1.22+, standard library only. Builds on Lesson 05's network and Lesson 06's log replication; same type and method names.
package main

import (
	"container/heap"
	"fmt"
	"sort"
	"strconv"
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
	InstallSnapshot
	InstallSnapshotReply
)

// ---------------------------------------------------------------------------------------------- configurations (section 6)

// Configuration is a cluster configuration. A simple configuration has only OldMembers (NewMembers is nil). A joint
// configuration C_old,new has both sets, and every decision (votes, commits) needs a majority of EACH set. That is the whole
// trick: no quorum of C_old,new can be formed without a quorum of C_old and a quorum of C_new, so two leaders can never be
// elected by disjoint groups. Member sets are kept as sorted slices, which is what Java's TreeSet iterates as.
type Configuration struct {
	OldMembers []int
	NewMembers []int
}

func sortedSet(members []int) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(members))
	for _, m := range members {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	sort.Ints(out)
	return out
}

func Simple(members []int) Configuration { return Configuration{OldMembers: sortedSet(members)} }

func Joint(old, next []int) Configuration {
	return Configuration{OldMembers: sortedSet(old), NewMembers: sortedSet(next)}
}

func (c Configuration) IsJoint() bool { return c.NewMembers != nil }

// All is everyone who must receive log entries: the union, sorted.
func (c Configuration) All() []int {
	return sortedSet(append(append([]int(nil), c.OldMembers...), c.NewMembers...))
}

func (c Configuration) Contains(id int) bool {
	for _, m := range c.All() {
		if m == id {
			return true
		}
	}
	return false
}

// Equal is what Java's record equals() does: same old set, same new set (or both absent).
func (c Configuration) Equal(o Configuration) bool {
	return c.IsJoint() == o.IsJoint() && equalInts(c.OldMembers, o.OldMembers) && equalInts(c.NewMembers, o.NewMembers)
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func MajorityOf(members []int, acks map[int]bool) bool {
	count := 0
	for _, m := range members {
		if acks[m] {
			count++
		}
	}
	return count >= len(members)/2+1
}

// HasQuorum is the quorum rule. Members outside the configuration (a departing leader, a joining server) are simply not counted.
func (c Configuration) HasQuorum(acks map[int]bool) bool {
	if !MajorityOf(c.OldMembers, acks) {
		return false
	}
	return c.NewMembers == nil || MajorityOf(c.NewMembers, acks)
}

// DisjointQuorumsPossible is Figure 10's hazard, checked by brute force: can a majority of `a` and a majority of `b` be
// disjoint? If yes, switching directly from a to b lets two leaders be elected in the same term. Joint consensus never has
// this property because every quorum of C_old,new contains a quorum of C_old.
func DisjointQuorumsPossible(a, b []int) bool {
	union := sortedSet(append(append([]int(nil), a...), b...))
	n := len(union)
	for maskA := 0; maskA < (1 << n); maskA++ {
		qa := subset(union, maskA)
		if !MajorityOf(a, qa) {
			continue
		}
		for maskB := 0; maskB < (1 << n); maskB++ {
			if maskA&maskB != 0 {
				continue
			}
			if MajorityOf(b, subset(union, maskB)) {
				return true
			}
		}
	}
	return false
}

func subset(items []int, mask int) map[int]bool {
	s := map[int]bool{}
	for i, it := range items {
		if mask&(1<<i) != 0 {
			s[it] = true
		}
	}
	return s
}

// Encode: configurations live in the log as commands so they replicate and persist like everything else.
func (c Configuration) Encode() string {
	if c.NewMembers == nil {
		return "CFG " + joinInts(c.OldMembers)
	}
	return "CFG " + joinInts(c.OldMembers) + " -> " + joinInts(c.NewMembers)
}

func Decode(command string) Configuration {
	body := strings.TrimSpace(command[4:])
	arrow := strings.Index(body, "->")
	if arrow < 0 {
		return Simple(parseMembers(body))
	}
	return Joint(parseMembers(body[:arrow]), parseMembers(body[arrow+2:]))
}

func IsConfigCommand(command string) bool { return strings.HasPrefix(command, "CFG ") }

func joinInts(s []int) string {
	parts := make([]string, len(s))
	for i, m := range s {
		parts[i] = strconv.Itoa(m)
	}
	return strings.Join(parts, ",")
}

func parseMembers(s string) []int {
	out := []int{}
	for _, p := range strings.Split(strings.TrimSpace(s), ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			panic(err)
		}
		out = append(out, v)
	}
	return out
}

// formatIDs prints a sorted set the way Java prints a TreeSet: "[0, 1, 2]".
func formatIDs(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func (c Configuration) String() string {
	if c.NewMembers == nil {
		return formatIDs(c.OldMembers)
	}
	return formatIDs(c.OldMembers) + "->" + formatIDs(c.NewMembers)
}

// cfgString prints a nullable configuration as Java prints a null reference.
func cfgString(c *Configuration) string {
	if c == nil {
		return "null"
	}
	return c.String()
}

// ---------------------------------------------------------------------------------------------- log, snapshot, storage (section 7)

type LogEntry struct {
	Term    int64
	Command string
}

func (e LogEntry) String() string { return fmt.Sprintf("t%d:%s", e.Term, e.Command) }

// Snapshot is Figure 13: the state machine's state through LastIncludedIndex, plus the latest configuration in that prefix.
type Snapshot struct {
	LastIncludedIndex int
	LastIncludedTerm  int64
	Data              map[string]string
	Config            *Configuration
}

// NewSnapshot copies data, as the Java record does with new TreeMap<>(data), so the live state machine can keep changing.
func NewSnapshot(lastIncludedIndex int, lastIncludedTerm int64, data map[string]string, config *Configuration) *Snapshot {
	copied := make(map[string]string, len(data))
	for k, v := range data {
		copied[k] = v
	}
	return &Snapshot{lastIncludedIndex, lastIncludedTerm, copied, config}
}

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
	Snapshot     *Snapshot // only on InstallSnapshot
}

func requestVote(from, to int, term int64, lastLogIndex int, lastLogTerm int64) Message {
	return Message{Kind: RequestVote, From: from, To: to, Term: term, PrevLogIndex: lastLogIndex, PrevLogTerm: lastLogTerm}
}

func voteReply(from, to int, term int64, granted bool) Message {
	return Message{Kind: RequestVoteReply, From: from, To: to, Term: term, Success: granted}
}

func appendEntries(from, to int, term int64, prevLogIndex int, prevLogTerm int64, entries []LogEntry, leaderCommit int) Message {
	return Message{Kind: AppendEntries, From: from, To: to, Term: term, PrevLogIndex: prevLogIndex, PrevLogTerm: prevLogTerm,
		Entries: append([]LogEntry(nil), entries...), LeaderCommit: leaderCommit}
}

func appendReply(from, to int, term int64, success bool, matchIndex int) Message {
	return Message{Kind: AppendEntriesReply, From: from, To: to, Term: term, Success: success, MatchIndex: matchIndex}
}

func installSnapshot(from, to int, term int64, snapshot *Snapshot) Message {
	return Message{Kind: InstallSnapshot, From: from, To: to, Term: term, PrevLogIndex: snapshot.LastIncludedIndex,
		PrevLogTerm: snapshot.LastIncludedTerm, Snapshot: snapshot}
}

func snapshotReply(from, to int, term int64, matchIndex int) Message {
	return Message{Kind: InstallSnapshotReply, From: from, To: to, Term: term, Success: true, MatchIndex: matchIndex}
}

// Storage is persistent state with compaction. The log is 1-indexed; everything at or before SnapshotIndex has been
// discarded and is summarised by Snapshot. Entries[0] is a sentinel standing for index SnapshotIndex with term SnapshotTerm,
// so the consistency check for prevLogIndex == SnapshotIndex still works.
type Storage struct {
	CurrentTerm   int64
	VotedFor      int
	SnapshotIndex int
	SnapshotTerm  int64
	Snapshot      *Snapshot
	Entries       []LogEntry
	Writes        int
}

func NewStorage() *Storage { return &Storage{VotedFor: -1, Entries: []LogEntry{{0, "<sentinel>"}}} }

func (s *Storage) Save(term int64, votedFor int) {
	s.CurrentTerm = term
	s.VotedFor = votedFor
	s.Writes++
}

func (s *Storage) Append(e LogEntry) {
	s.Entries = append(s.Entries, e)
	s.Writes++
}

func (s *Storage) LastIndex() int     { return s.SnapshotIndex + len(s.Entries) - 1 }
func (s *Storage) FirstIndex() int    { return s.SnapshotIndex + 1 }
func (s *Storage) Has(index int) bool { return index > s.SnapshotIndex && index <= s.LastIndex() }

// TermAt panics below the snapshot: asking about a compacted index is a programming error, not a runtime condition.
func (s *Storage) TermAt(index int) int64 {
	if index == s.SnapshotIndex {
		return s.SnapshotTerm
	}
	if index < s.SnapshotIndex {
		panic(fmt.Sprintf("index %d is compacted (snapshot at %d)", index, s.SnapshotIndex))
	}
	return s.Entries[index-s.SnapshotIndex].Term
}

func (s *Storage) Get(index int) LogEntry {
	if !s.Has(index) {
		panic(fmt.Sprintf("no entry at %d", index))
	}
	return s.Entries[index-s.SnapshotIndex]
}

func (s *Storage) Truncate(fromIndex int) {
	for s.LastIndex() >= fromIndex {
		s.Entries = s.Entries[:len(s.Entries)-1]
	}
	s.Writes++
}

// Compact discards entries through the snapshot's index, keeps the rest. Used by the node's own compaction and by rule 6 of Figure 13.
func (s *Storage) Compact(snap *Snapshot) {
	keep := []LogEntry{{snap.LastIncludedTerm, "<sentinel>"}}
	for i := snap.LastIncludedIndex + 1; i <= s.LastIndex(); i++ {
		keep = append(keep, s.Get(i))
	}
	s.Entries = keep
	s.SnapshotIndex = snap.LastIncludedIndex
	s.SnapshotTerm = snap.LastIncludedTerm
	s.Snapshot = snap
	s.Writes++
}

// ReplaceWithSnapshot is rule 7 of Figure 13: the snapshot replaces the whole log.
func (s *Storage) ReplaceWithSnapshot(snap *Snapshot) {
	s.Entries = []LogEntry{{snap.LastIncludedTerm, "<sentinel>"}}
	s.SnapshotIndex = snap.LastIncludedIndex
	s.SnapshotTerm = snap.LastIncludedTerm
	s.Snapshot = snap
	s.Writes++
}

// ConfigAt is the latest configuration at or before `index`, searching the log then the snapshot. Nil if none is known.
func (s *Storage) ConfigAt(index int, bootstrap *Configuration) *Configuration {
	start := index
	if s.LastIndex() < start {
		start = s.LastIndex()
	}
	for i := start; i > s.SnapshotIndex; i-- {
		if c := s.Get(i).Command; IsConfigCommand(c) {
			d := Decode(c)
			return &d
		}
	}
	if s.Snapshot != nil {
		return s.Snapshot.Config
	}
	return bootstrap
}

// LatestConfigIndex is the index of the latest configuration entry still in the log, or -1 if the latest known configuration is in the snapshot.
func (s *Storage) LatestConfigIndex() int {
	for i := s.LastIndex(); i > s.SnapshotIndex; i-- {
		if IsConfigCommand(s.Get(i).Command) {
			return i
		}
	}
	return -1
}

// KVStore is the replicated state machine. Configuration entries and NOOPs are applied as no-ops but still counted.
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
	case "NOOP", "CFG":
	default:
		return fmt.Errorf("unknown command: %s", command)
	}
	kv.AppliedCommands = append(kv.AppliedCommands, command)
	return nil
}

func (kv *KVStore) Reset(snapshotData map[string]string) {
	kv.Data = make(map[string]string, len(snapshotData))
	for k, v := range snapshotData {
		kv.Data[k] = v
	}
	kv.AppliedCommands = append(kv.AppliedCommands, "<snapshot>")
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
	ElectionTimeoutMin, ElectionTimeoutMax, HeartbeatInterval, SnapshotThreshold int
}

var DefaultConfig = Config{150, 300, 50, 8}

func NewConfig(min, max, heartbeat, snapshotThreshold int) (Config, error) {
	if min <= 0 || max < min || heartbeat <= 0 {
		return Config{}, fmt.Errorf("bad timing config")
	}
	if heartbeat >= min {
		return Config{}, fmt.Errorf("heartbeat interval must be well below the election timeout")
	}
	if snapshotThreshold < 1 {
		return Config{}, fmt.Errorf("snapshotThreshold must be positive")
	}
	return Config{min, max, heartbeat, snapshotThreshold}, nil
}

// Node is what the network drives: Lesson 05's interface, unchanged.
type Node interface {
	ID() int
	Tick(now int) []Message
	Handle(m Message, now int) []Message
}

type Tracer func(now int, text string)

func noTrace(int, string) {}

func name(id int) string { return fmt.Sprintf("n%d", id) }

// ---------------------------------------------------------------------------------------------- the server

// RaftNode is one Raft server: Lesson 06 plus configuration-aware quorums, joint consensus, and snapshots.
type RaftNode struct {
	id      int
	Storage *Storage
	rng     *Rng
	config  Config
	tracer  Tracer
	// bootstrap is the configuration a server is created with. Nil for a server that joins later and learns its membership from the log.
	bootstrap *Configuration

	Role                                              Role
	LeaderID                                          int
	CommitIndex                                       int
	LastApplied                                       int
	StateMachine                                      *KVStore
	NextIndex                                         map[int]int
	MatchIndex                                        map[int]int
	votesGranted                                      map[int]bool
	ElectionDeadline                                  int
	NextHeartbeat                                     int
	SnapshotsTaken, SnapshotsInstalled, SnapshotsSent int
	SteppedDownAfterRemoval                           bool
}

func NewRaftNode(id int, bootstrap *Configuration, storage *Storage, rng *Rng, config Config, now int, tracer Tracer) *RaftNode {
	if tracer == nil {
		tracer = noTrace
	}
	n := &RaftNode{id: id, bootstrap: bootstrap, Storage: storage, rng: rng, config: config, tracer: tracer,
		Role: Follower, LeaderID: -1, StateMachine: NewKVStore(), NextIndex: map[int]int{}, MatchIndex: map[int]int{},
		votesGranted: map[int]bool{}}
	n.ResetElectionTimer(now)
	if storage.Snapshot != nil {
		n.StateMachine.Reset(storage.Snapshot.Data)
		n.LastApplied = storage.SnapshotIndex
		n.CommitIndex = storage.SnapshotIndex
	}
	return n
}

func (n *RaftNode) ID() int            { return n.id }
func (n *RaftNode) CurrentTerm() int64 { return n.Storage.CurrentTerm }
func (n *RaftNode) LastLogIndex() int  { return n.Storage.LastIndex() }
func (n *RaftNode) LastLogTerm() int64 { return n.Storage.TermAt(n.Storage.LastIndex()) }

// Configuration is the latest configuration in my log (Figure 11 rule: a server uses the latest configuration it has, committed or not).
func (n *RaftNode) Configuration() *Configuration {
	return n.Storage.ConfigAt(n.Storage.LastIndex(), n.bootstrap)
}

func (n *RaftNode) IsMember() bool {
	c := n.Configuration()
	return c != nil && c.Contains(n.id)
}

// ReplicationTargets is everyone I replicate to: the union of the current configuration, plus servers leaving C_old who still need C_new.
func (n *RaftNode) ReplicationTargets() []int {
	c := n.Configuration()
	if c == nil {
		return nil
	}
	targets := map[int]bool{}
	for _, m := range c.All() {
		targets[m] = true
	}
	cfgIdx := n.Storage.LatestConfigIndex()
	if !c.IsJoint() && cfgIdx > 0 {
		if before := n.Storage.ConfigAt(cfgIdx-1, n.bootstrap); before != nil {
			for _, m := range before.All() {
				if n.MatchIndex[m] < cfgIdx {
					targets[m] = true
				}
			}
		}
	}
	delete(targets, n.id)
	out := make([]int, 0, len(targets))
	for m := range targets {
		out = append(out, m)
	}
	sort.Ints(out)
	return out
}

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
	if now < n.ElectionDeadline {
		return nil
	}
	if !n.IsMember() { // joining or removed servers never campaign
		n.ResetElectionTimer(now)
		return nil
	}
	return n.StartElection(now)
}

// ---------------------------------------------------------------- election (Lesson 05/06, quorum now configuration-aware)

func (n *RaftNode) StartElection(now int) []Message {
	n.Role = Candidate
	n.Storage.Save(n.Storage.CurrentTerm+1, n.id)
	n.LeaderID = -1
	n.votesGranted = map[int]bool{n.id: true}
	n.ResetElectionTimer(now)
	cfg := n.Configuration()
	n.tracer(now, fmt.Sprintf("%s election timeout -> candidate for term %d in config %s", name(n.id), n.Storage.CurrentTerm, cfgString(cfg)))
	out := []Message{}
	for _, p := range cfg.All() {
		if p != n.id {
			out = append(out, requestVote(n.id, p, n.Storage.CurrentTerm, n.LastLogIndex(), n.LastLogTerm()))
		}
	}
	return out
}

func (n *RaftNode) Handle(m Message, now int) []Message {
	if m.Term > n.Storage.CurrentTerm {
		n.becomeFollower(m.Term, now)
	}
	switch m.Kind {
	case RequestVote:
		return n.onRequestVote(m, now)
	case RequestVoteReply:
		return n.onVoteReply(m, now)
	case AppendEntries:
		return n.onAppendEntries(m, now)
	case AppendEntriesReply:
		return n.onAppendReply(m, now)
	case InstallSnapshot:
		return n.onInstallSnapshot(m, now)
	default:
		return n.onSnapshotReply(m, now)
	}
}

func (n *RaftNode) becomeFollower(term int64, now int) {
	before := n.Role
	n.Role = Follower
	n.LeaderID = -1
	n.votesGranted = map[int]bool{}
	if term > n.Storage.CurrentTerm {
		n.Storage.Save(term, -1)
		suffix := ""
		if before == Leader {
			suffix = " (was LEADER)"
		}
		n.tracer(now, fmt.Sprintf("%s sees term %d -> follower%s", name(n.id), term, suffix))
	}
	if before == Leader {
		n.ResetElectionTimer(now)
	}
}

func (n *RaftNode) onRequestVote(m Message, now int) []Message {
	if m.Term < n.Storage.CurrentTerm {
		return []Message{voteReply(n.id, m.From, n.Storage.CurrentTerm, false)}
	}
	canVote := n.Storage.VotedFor == -1 || n.Storage.VotedFor == m.From
	logUpToDate := m.PrevLogTerm > n.LastLogTerm() || (m.PrevLogTerm == n.LastLogTerm() && m.PrevLogIndex >= n.LastLogIndex())
	if canVote && logUpToDate {
		n.Storage.Save(n.Storage.CurrentTerm, m.From)
		n.ResetElectionTimer(now)
		return []Message{voteReply(n.id, m.From, n.Storage.CurrentTerm, true)}
	}
	return []Message{voteReply(n.id, m.From, n.Storage.CurrentTerm, false)}
}

func (n *RaftNode) onVoteReply(m Message, now int) []Message {
	if n.Role != Candidate || m.Term != n.Storage.CurrentTerm {
		return nil
	}
	if m.Success {
		n.votesGranted[m.From] = true
		if n.Configuration().HasQuorum(n.votesGranted) {
			return n.becomeLeader(now)
		}
	}
	return nil
}

func (n *RaftNode) becomeLeader(now int) []Message {
	n.Role = Leader
	n.LeaderID = n.id
	n.NextIndex = map[int]int{}
	n.MatchIndex = map[int]int{}
	n.tracer(now, fmt.Sprintf("%s wins -> LEADER of term %d with %d votes in %s, log ends at %d",
		name(n.id), n.Storage.CurrentTerm, len(n.votesGranted), cfgString(n.Configuration()), n.LastLogIndex()))
	n.Storage.Append(LogEntry{n.Storage.CurrentTerm, "NOOP"})
	return n.ReplicateAll(now)
}

// ---------------------------------------------------------------- replication (Lesson 06) with snapshots (section 7)

func (n *RaftNode) Propose(command string) int {
	if n.Role != Leader {
		return -1
	}
	n.Storage.Append(LogEntry{n.Storage.CurrentTerm, command})
	return n.LastLogIndex()
}

// ChangeMembership is section 6: start a membership change. Refused while another change is in flight (latest
// configuration is joint or not yet committed). Appends C_old,new; the leader uses it for quorums as soon as it is in its own log.
func (n *RaftNode) ChangeMembership(newMembers []int) int {
	if n.Role != Leader {
		return -1
	}
	current := n.Configuration()
	if current.IsJoint() || n.Storage.LatestConfigIndex() > n.CommitIndex {
		return -1
	}
	return n.Propose(Joint(current.OldMembers, newMembers).Encode())
}

func (n *RaftNode) ReplicateAll(now int) []Message {
	n.NextHeartbeat = now + n.config.HeartbeatInterval
	out := []Message{}
	for _, p := range n.ReplicationTargets() {
		out = append(out, n.AppendEntriesFor(p))
	}
	return out
}

// AppendEntriesFor sends AppendEntries from nextIndex, or InstallSnapshot when the follower needs entries we have already compacted.
func (n *RaftNode) AppendEntriesFor(peer int) Message {
	next, ok := n.NextIndex[peer]
	if !ok {
		next = n.LastLogIndex() + 1
		n.NextIndex[peer] = next
	}
	if _, ok := n.MatchIndex[peer]; !ok {
		n.MatchIndex[peer] = 0
	}
	if next <= n.Storage.SnapshotIndex {
		n.SnapshotsSent++
		return installSnapshot(n.id, peer, n.Storage.CurrentTerm, n.Storage.Snapshot)
	}
	prev := next - 1
	entries := []LogEntry{}
	for i := next; i <= n.LastLogIndex(); i++ {
		entries = append(entries, n.Storage.Get(i))
	}
	return appendEntries(n.id, peer, n.Storage.CurrentTerm, prev, n.Storage.TermAt(prev), entries, n.CommitIndex)
}

func (n *RaftNode) onAppendEntries(m Message, now int) []Message {
	if m.Term < n.Storage.CurrentTerm {
		return []Message{appendReply(n.id, m.From, n.Storage.CurrentTerm, false, 0)}
	}
	if n.Role == Leader {
		panic(fmt.Sprintf("two leaders in term %d", n.Storage.CurrentTerm))
	}
	if n.Role == Candidate {
		n.Role = Follower
		n.votesGranted = map[int]bool{}
	}
	n.LeaderID = m.From
	n.ResetElectionTimer(now)
	if m.PrevLogIndex < n.Storage.SnapshotIndex {
		// Everything the leader is sending about is already inside my snapshot; tell it where I really am.
		return []Message{appendReply(n.id, m.From, n.Storage.CurrentTerm, true, n.Storage.SnapshotIndex)}
	}
	if m.PrevLogIndex > n.LastLogIndex() || n.Storage.TermAt(m.PrevLogIndex) != m.PrevLogTerm {
		return []Message{appendReply(n.id, m.From, n.Storage.CurrentTerm, false, 0)}
	}
	index := m.PrevLogIndex
	for _, e := range m.Entries {
		index++
		if index <= n.LastLogIndex() {
			if n.Storage.TermAt(index) == e.Term {
				continue
			}
			n.tracer(now, fmt.Sprintf("%s conflict at index %d: has %s, leader has %s -> truncating", name(n.id), index, n.Storage.Get(index), e))
			n.Storage.Truncate(index)
		}
		n.Storage.Append(e)
	}
	lastNew := m.PrevLogIndex + len(m.Entries)
	if m.LeaderCommit > n.CommitIndex {
		n.CommitIndex = minInt(m.LeaderCommit, lastNew)
		n.applyCommitted(now)
	}
	return []Message{appendReply(n.id, m.From, n.Storage.CurrentTerm, true, lastNew)}
}

func (n *RaftNode) onAppendReply(m Message, now int) []Message {
	if n.Role != Leader || m.Term != n.Storage.CurrentTerm {
		return nil
	}
	if m.Success {
		if m.MatchIndex > n.MatchIndex[m.From] {
			n.MatchIndex[m.From] = m.MatchIndex
			n.NextIndex[m.From] = m.MatchIndex + 1
			return n.advanceCommitIndex(now)
		}
		return nil
	}
	next, ok := n.NextIndex[m.From]
	if !ok {
		next = n.LastLogIndex() + 1
	}
	n.NextIndex[m.From] = maxInt(1, next-1)
	return []Message{n.AppendEntriesFor(m.From)}
}

// onInstallSnapshot is Figure 13, receiver implementation. Rule 6 keeps a log that already agrees at lastIncludedIndex; rule 7 discards it.
func (n *RaftNode) onInstallSnapshot(m Message, now int) []Message {
	if m.Term < n.Storage.CurrentTerm {
		return []Message{snapshotReply(n.id, m.From, n.Storage.CurrentTerm, 0)}
	}
	if n.Role == Candidate {
		n.Role = Follower
		n.votesGranted = map[int]bool{}
	}
	n.LeaderID = m.From
	n.ResetElectionTimer(now)
	s := m.Snapshot
	if s.LastIncludedIndex <= n.Storage.SnapshotIndex {
		return []Message{snapshotReply(n.id, m.From, n.Storage.CurrentTerm, n.Storage.SnapshotIndex)} // already have it
	}
	n.SnapshotsInstalled++
	if n.Storage.Has(s.LastIncludedIndex) && n.Storage.TermAt(s.LastIncludedIndex) == s.LastIncludedTerm {
		n.Storage.Compact(s) // 6
		n.tracer(now, fmt.Sprintf("%s installs snapshot through %d, keeps %d later entries", name(n.id), s.LastIncludedIndex, n.LastLogIndex()-s.LastIncludedIndex))
	} else {
		n.Storage.ReplaceWithSnapshot(s) // 7
		n.tracer(now, fmt.Sprintf("%s installs snapshot through %d (term %d), log discarded, config %s", name(n.id), s.LastIncludedIndex, s.LastIncludedTerm, cfgString(s.Config)))
	}
	if n.LastApplied < s.LastIncludedIndex { // 8
		n.StateMachine.Reset(s.Data)
		n.LastApplied = s.LastIncludedIndex
	}
	n.CommitIndex = maxInt(n.CommitIndex, s.LastIncludedIndex)
	return []Message{snapshotReply(n.id, m.From, n.Storage.CurrentTerm, s.LastIncludedIndex)}
}

func (n *RaftNode) onSnapshotReply(m Message, now int) []Message {
	if n.Role != Leader || m.Term != n.Storage.CurrentTerm {
		return nil
	}
	if m.MatchIndex > n.MatchIndex[m.From] {
		n.MatchIndex[m.From] = m.MatchIndex
		n.NextIndex[m.From] = m.MatchIndex + 1
		return n.advanceCommitIndex(now)
	}
	return nil
}

// advanceCommitIndex is Figure 2's commit rule with a configuration-aware quorum. Returns messages because committing
// C_old,new makes the leader append C_new immediately (section 6), and committing C_new may make it step down.
func (n *RaftNode) advanceCommitIndex(now int) []Message {
	for idx := n.LastLogIndex(); idx > n.CommitIndex; idx-- {
		if n.Storage.TermAt(idx) != n.Storage.CurrentTerm {
			continue
		}
		acks := map[int]bool{n.id: true}
		for peer, match := range n.MatchIndex {
			if match >= idx {
				acks[peer] = true
			}
		}
		cfg := n.Configuration()
		if cfg.HasQuorum(acks) {
			n.tracer(now, fmt.Sprintf("%s commits through index %d (acks %s satisfy %s)", name(n.id), idx, formatIDs(sortedKeys(acks)), cfgString(cfg)))
			n.CommitIndex = idx
			n.applyCommitted(now)
			return n.afterCommit(now)
		}
	}
	return nil
}

// afterCommit is section 6: once C_old,new is committed, append C_new; once C_new is committed and I am not in it, step
// down. A joint configuration whose entry has been compacted into the snapshot is committed by definition, so a new leader
// that inherits a half-finished change (the old leader crashed between the two entries) finishes it here.
func (n *RaftNode) afterCommit(now int) []Message {
	c := n.Configuration()
	cfgIdx := n.Storage.LatestConfigIndex()
	latestConfigCommitted := cfgIdx == -1 || cfgIdx <= n.CommitIndex
	if c.IsJoint() && latestConfigCommitted {
		next := Simple(c.NewMembers)
		n.tracer(now, fmt.Sprintf("%s C_old,new committed at %d -> appends C_new %s", name(n.id), cfgIdx, next))
		n.Propose(next.Encode())
		return n.ReplicateAll(now)
	}
	if !c.IsJoint() && latestConfigCommitted && !c.Contains(n.id) {
		n.tracer(now, fmt.Sprintf("%s C_new %s committed and I am not in it -> step down", name(n.id), c))
		n.SteppedDownAfterRemoval = true
		finalBeat := n.ReplicateAll(now) // one last round so departing followers learn the commit index
		n.Role = Follower
		n.LeaderID = -1
		return finalBeat
	}
	return nil
}

func (n *RaftNode) applyCommitted(now int) {
	for n.LastApplied < n.CommitIndex {
		n.LastApplied++
		if err := n.StateMachine.Apply(n.Storage.Get(n.LastApplied).Command); err != nil {
			panic(err)
		}
	}
	if n.LastApplied-n.Storage.SnapshotIndex >= n.config.SnapshotThreshold {
		n.takeSnapshot(now)
	}
}

// takeSnapshot is section 7: each server snapshots independently once enough applied entries have accumulated.
func (n *RaftNode) takeSnapshot(now int) {
	s := NewSnapshot(n.LastApplied, n.Storage.TermAt(n.LastApplied), n.StateMachine.Data, n.Storage.ConfigAt(n.LastApplied, n.bootstrap))
	n.Storage.Compact(s)
	n.SnapshotsTaken++
	n.tracer(now, fmt.Sprintf("%s snapshots through index %d (log now %d..%d, config %s)", name(n.id), s.LastIncludedIndex, n.Storage.FirstIndex(), n.LastLogIndex(), cfgString(s.Config)))
}

func sortedKeys(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------------------------- network and cluster

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

// Network is Lesson 05's deterministic network: (deliverAt, seq) ordering, seeded delays and drops, partitions, crashes.
type Network struct {
	rng         *Rng
	dropRate    float64
	maxDelay    int
	nodes       map[int]Node
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
	return &Network{rng: NewRng(seed), dropRate: dropRate, maxDelay: maxDelay, nodes: map[int]Node{},
		down: map[int]bool{}, sideA: map[int]bool{}, observer: func() {}}
}

func (w *Network) Add(n Node)            { w.nodes[n.ID()] = n }
func (w *Network) SetObserver(f func())  { w.observer = f }
func (w *Network) Now() int              { return w.now }
func (w *Network) IsDown(id int) bool    { return w.down[id] }
func (w *Network) Crash(id int)          { w.down[id] = true }
func (w *Network) Restart(n Node)        { delete(w.down, n.ID()); w.nodes[n.ID()] = n }
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
	if _, known := w.nodes[m.To]; !known || w.down[m.From] || w.down[m.To] || w.crosses(m.From, m.To) {
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
	sort.Ints(ids) // Java's TreeMap ticks nodes in id order
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

// Cluster wires `size` servers onto one network. Only InitialMembers start in the configuration; the others are spare
// servers that join via ChangeMembership. Checks Election Safety, Leader Completeness, Log Matching, and State Machine
// Safety after every delivery.
type Cluster struct {
	Size                int
	Seed                int64
	Config              Config
	Network             *Network
	InitialMembers      []int
	Storages            []*Storage
	Nodes               []*RaftNode
	Events              []string
	LeadersByTerm       map[int64]int
	CommittedByIndex    map[int]LogEntry
	AppliedByIndex      map[int]string
	appliedSeen         map[int]int // node -> highest index whose apply we recorded
	completenessChecked map[int64]bool
	restarts            []int
	Proposals           int
}

func NewClusterWithMembers(size int, initialMembers []int, seed int64, dropRate float64, maxDelay int, config Config) *Cluster {
	c := &Cluster{Size: size, Seed: seed, Config: config, Network: NewNetwork(seed, dropRate, maxDelay), InitialMembers: sortedSet(initialMembers),
		LeadersByTerm: map[int64]int{}, CommittedByIndex: map[int]LogEntry{}, AppliedByIndex: map[int]string{},
		appliedSeen: map[int]int{}, completenessChecked: map[int64]bool{}, restarts: make([]int, size)}
	boot := Simple(initialMembers)
	for i := 0; i < size; i++ {
		c.Storages = append(c.Storages, NewStorage())
		var bootstrap *Configuration
		if boot.Contains(i) {
			b := boot
			bootstrap = &b
		}
		n := NewRaftNode(i, bootstrap, c.Storages[i], nodeRng(seed, i, 0), config, 0, c.trace)
		c.Nodes = append(c.Nodes, n)
		c.Network.Add(n)
	}
	c.Network.SetObserver(c.CheckInvariants)
	return c
}

// NewCluster starts every server in the configuration with the default config (the Java four-argument constructor).
func NewCluster(size int, seed int64, dropRate float64, maxDelay int) *Cluster {
	return NewClusterWithMembers(size, AllOf(size), seed, dropRate, maxDelay, DefaultConfig)
}

func AllOf(n int) []int {
	s := make([]int, n)
	for i := range s {
		s[i] = i
	}
	return s
}

func nodeRng(seed int64, id, restart int) *Rng {
	return NewRng(seed*7919 + 1000003*int64(id+1) + 17*int64(restart))
}

func (c *Cluster) trace(now int, text string) {
	c.Events = append(c.Events, fmt.Sprintf("t=%-5d %s", now, text))
}

func (c *Cluster) Now() int              { return c.Network.Now() }
func (c *Cluster) Node(id int) *RaftNode { return c.Nodes[id] }
func (c *Cluster) Tick()                 { c.Network.Tick(); c.CheckInvariants() }

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

// StableLeader: one live leader, and every live member of its configuration follows it in its term.
func (c *Cluster) StableLeader() *RaftNode {
	var leader *RaftNode
	for _, n := range c.Nodes {
		if c.Network.IsDown(n.id) {
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
	for _, m := range leader.Configuration().All() {
		n := c.Nodes[m]
		if c.Network.IsDown(m) || n == leader {
			continue
		}
		if n.CurrentTerm() != leader.CurrentTerm() || n.LeaderID != leader.id {
			return nil
		}
	}
	return leader
}

// SoleLeader is the only live server claiming leadership, or nil. Clients talk to whoever claims to lead; stability is a test-side notion.
func (c *Cluster) SoleLeader() *RaftNode {
	var leader *RaftNode
	for _, n := range c.Nodes {
		if c.Network.IsDown(n.id) || n.Role != Leader {
			continue
		}
		if leader != nil {
			return nil
		}
		leader = n
	}
	return leader
}

func (c *Cluster) Propose(command string) int {
	l := c.SoleLeader()
	if l == nil {
		return -1
	}
	index := l.Propose(command)
	if index > 0 {
		c.Proposals++
		c.Network.SendAll(l.ReplicateAll(c.Now()))
	}
	return index
}

func (c *Cluster) ChangeMembership(newMembers []int) int {
	l := c.SoleLeader()
	if l == nil {
		return -1
	}
	index := l.ChangeMembership(newMembers)
	if index > 0 {
		c.trace(c.Now(), fmt.Sprintf("%s appends C_old,new %s at index %d", name(l.id), cfgString(l.Configuration()), index))
		c.Network.SendAll(l.ReplicateAll(c.Now()))
	}
	return index
}

// RunUntilApplied runs until every live member of the current configuration has applied through `index`.
func (c *Cluster) RunUntilApplied(index int, maxTicks int) int {
	for i := 0; i < maxTicks; i++ {
		c.Tick()
		l := c.StableLeader()
		if l == nil {
			continue
		}
		all := true
		for _, m := range l.Configuration().All() {
			if !c.Network.IsDown(m) && c.Nodes[m].LastApplied < index {
				all = false
			}
		}
		if all {
			return c.Now()
		}
	}
	return -1
}

// RunUntilConfigured runs until the membership change is complete: latest config is simple, equals `members`, and is committed on the leader.
func (c *Cluster) RunUntilConfigured(members []int, maxTicks int) int {
	want := sortedSet(members)
	for i := 0; i < maxTicks; i++ {
		c.Tick()
		l := c.StableLeader()
		if l == nil {
			continue
		}
		cfg := l.Configuration()
		if !cfg.IsJoint() && equalInts(cfg.All(), want) && l.Storage.LatestConfigIndex() <= l.CommitIndex {
			return c.Now()
		}
	}
	return -1
}

// ---------------------------------------------------------------- the safety checker (Figure 3)

// CheckInvariants panics on the first violation, as the Java throws IllegalStateException: there is no correct value to return.
func (c *Cluster) CheckInvariants() {
	for _, n := range c.Nodes {
		if n.Role != Leader {
			continue
		}
		// Election Safety: at most one leader per term.
		if existing, ok := c.LeadersByTerm[n.CurrentTerm()]; !ok {
			c.LeadersByTerm[n.CurrentTerm()] = n.id
		} else if existing != n.id {
			panic(fmt.Sprintf("ELECTION SAFETY: term %d has leaders %d and %d", n.CurrentTerm(), existing, n.id))
		}
		// Leader Completeness: every entry committed before this leader's term is in its log (or inside its snapshot).
		if !c.completenessChecked[n.CurrentTerm()] {
			c.completenessChecked[n.CurrentTerm()] = true
			keys := make([]int, 0, len(c.CommittedByIndex))
			for k := range c.CommittedByIndex {
				keys = append(keys, k)
			}
			sort.Ints(keys)
			for _, k := range keys {
				if k <= n.Storage.SnapshotIndex {
					continue
				}
				if !n.Storage.Has(k) || n.Storage.Get(k) != c.CommittedByIndex[k] {
					panic(fmt.Sprintf("LEADER COMPLETENESS: leader %d of term %d lacks committed entry %d=%s", n.id, n.CurrentTerm(), k, c.CommittedByIndex[k]))
				}
			}
		}
	}
	// Record committed entries and check they never change (the committed prefix is immutable).
	for _, n := range c.Nodes {
		for k := maxInt(1, n.Storage.FirstIndex()); k <= minInt(n.CommitIndex, n.LastLogIndex()); k++ {
			if seen, ok := c.CommittedByIndex[k]; !ok {
				c.CommittedByIndex[k] = n.Storage.Get(k)
			} else if seen != n.Storage.Get(k) {
				panic(fmt.Sprintf("STATE MACHINE SAFETY: index %d committed as %s and %s on %d", k, seen, n.Storage.Get(k), n.id))
			}
		}
		// State Machine Safety, directly: the command applied at index k is the same on every server.
		from := c.appliedSeen[n.id]
		for k := from + 1; k <= n.LastApplied; k++ {
			if !n.Storage.Has(k) {
				continue // applied via snapshot: the snapshot's source recorded it
			}
			cmd := n.Storage.Get(k).Command
			if prior, ok := c.AppliedByIndex[k]; !ok {
				c.AppliedByIndex[k] = cmd
			} else if prior != cmd {
				panic(fmt.Sprintf("STATE MACHINE SAFETY: index %d applied as '%s' and '%s'", k, prior, cmd))
			}
		}
		c.appliedSeen[n.id] = n.LastApplied
	}
	// Log Matching on the overlapping, uncompacted ranges.
	for i := 0; i < c.Size; i++ {
		for j := i + 1; j < c.Size; j++ {
			CheckLogMatching(c.Nodes[i].Storage, c.Nodes[j].Storage, i, j)
		}
	}
}

func CheckLogMatching(a, b *Storage, ia, ib int) {
	low := maxInt(a.SnapshotIndex, b.SnapshotIndex) + 1
	high := minInt(a.LastIndex(), b.LastIndex())
	for k := high; k >= low; k-- {
		if a.TermAt(k) == b.TermAt(k) {
			for i := low; i <= k; i++ {
				if a.Get(i) != b.Get(i) {
					panic(fmt.Sprintf("LOG MATCHING: nodes %d and %d agree at %d but differ at %d", ia, ib, k, i))
				}
			}
			return
		}
	}
}

func (c *Cluster) Partition(sideA ...int) {
	c.Network.Partition(sideA)
	c.trace(c.Now(), fmt.Sprintf("network partition: %s | rest", formatIDs(sortedSet(sideA))))
}

func (c *Cluster) Heal() { c.Network.Heal(); c.trace(c.Now(), "network heals") }
func (c *Cluster) Crash(id int) {
	c.Network.Crash(id)
	c.trace(c.Now(), fmt.Sprintf("%s crashes", name(id)))
}

func (c *Cluster) Restart(id int) *RaftNode {
	c.restarts[id]++
	var boot *Configuration
	if Simple(c.InitialMembers).Contains(id) {
		b := Simple(c.InitialMembers)
		boot = &b
	}
	n := NewRaftNode(id, boot, c.Storages[id], nodeRng(c.Seed, id, c.restarts[id]), c.Config, c.Now(), c.trace)
	c.Nodes[id] = n
	c.Network.Restart(n)
	c.trace(c.Now(), fmt.Sprintf("%s restarts: term %d, snapshot through %d, log to %d, config %s",
		name(id), n.CurrentTerm(), n.Storage.SnapshotIndex, n.LastLogIndex(), cfgString(n.Configuration())))
	return n
}

func (c *Cluster) StateMachinesConverged(members []int) bool {
	first, seen := "", false
	for _, m := range sortedSet(members) {
		if c.Network.IsDown(m) {
			continue
		}
		s := c.Nodes[m].StateMachine.String()
		if !seen {
			first, seen = s, true
		} else if s != first {
			return false
		}
	}
	return true
}

func (c *Cluster) LogOf(id int) string {
	s := c.Nodes[id].Storage
	tail := make([]string, 0)
	for i := s.FirstIndex(); i <= s.LastIndex(); i++ {
		tail = append(tail, s.Get(i).String())
	}
	prefix := ""
	if s.SnapshotIndex > 0 {
		prefix = fmt.Sprintf("[snap<=%d]", s.SnapshotIndex)
	}
	return prefix + "[" + strings.Join(tail, ", ") + "]"
}

// ---------------------------------------------------------------------------------------------- demo

func printEvents(c *Cluster, from int) {
	for _, e := range c.Events[from:] {
		fmt.Println(e)
	}
}

func printNodes(c *Cluster) {
	for _, n := range c.Nodes {
		role := n.Role.String()
		if c.Network.IsDown(n.id) {
			role = "DOWN"
		}
		fmt.Printf("  %s %-9s term %d cfg=%-16s commit=%-3d applied=%-3d log=%s kv=%s\n", name(n.id), role,
			n.CurrentTerm(), cfgString(n.Configuration()), n.CommitIndex, n.LastApplied, c.LogOf(n.id), n.StateMachine)
	}
}

func proposeUntilAccepted(c *Cluster, command string) int {
	for attempt := 0; attempt < 500; attempt++ {
		if index := c.Propose(command); index > 0 {
			return index
		}
		c.Run(10)
	}
	panic("no leader accepted " + command)
}

func changeUntilAccepted(c *Cluster, members []int) {
	for attempt := 0; attempt < 500; attempt++ {
		if c.ChangeMembership(members) > 0 {
			return
		}
		c.Run(10)
	}
	panic(fmt.Sprintf("no leader accepted membership change to %s", formatIDs(sortedSet(members))))
}

func without(set []int, id int) []int {
	out := []int{}
	for _, m := range set {
		if m != id {
			out = append(out, m)
		}
	}
	return out
}

func set(xs ...int) map[int]bool {
	m := map[int]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func main() {
	fmt.Println("== Why you cannot just swap the configuration (Figure 10) ==")
	three, five := []int{0, 1, 2}, []int{0, 1, 2, 3, 4}
	fmt.Printf("C_old = %s (majority 2), C_new = %s (majority 3)\n", formatIDs(three), formatIDs(five))
	fmt.Printf("disjoint majorities possible with a direct switch? %v   e.g. {0,1} is a majority of C_old and {2,3,4} is a majority of C_new: two leaders in one term\n",
		DisjointQuorumsPossible(three, five))
	fmt.Printf("adding exactly one server, 3 -> 4: disjoint majorities possible? %v   (majorities of 3 and of 4 always overlap: single-server changes need no joint phase, which is etcd's approach)\n",
		DisjointQuorumsPossible(three, []int{0, 1, 2, 3}))
	joint := Joint(three, five)
	fmt.Printf("joint C_old,new = %s: {0,1} has quorum? %v; {2,3,4}? %v; {0,1,3,4}? %v  (needs a majority of BOTH)\n",
		joint, joint.HasQuorum(set(0, 1)), joint.HasQuorum(set(2, 3, 4)), joint.HasQuorum(set(0, 1, 3, 4)))

	fmt.Println()
	fmt.Println("== Grow a 3-server cluster to 5 with joint consensus, under client traffic (seed 7, delay 1-5) ==")
	noSnap, _ := NewConfig(150, 300, 50, 1000)
	c := NewClusterWithMembers(5, []int{0, 1, 2}, 7, 0.0, 5, noSnap)
	c.RunUntilStableLeader(2000)
	c.Propose("SET alice 100")
	idx := c.Propose("SET bob 50")
	c.RunUntilApplied(idx, 1000)
	before := len(c.Events)
	changeUntilAccepted(c, five)
	c.Propose("SET carol 7") // traffic continues during the change
	done := c.RunUntilConfigured(five, 3000)
	c.Propose("SET dave 1")
	c.RunUntilApplied(c.StableLeader().LastLogIndex(), 2000)
	printEvents(c, before)
	fmt.Printf("configuration %s committed at tick %d; all five state machines agree = %v\n", cfgString(c.StableLeader().Configuration()), done, c.StateMachinesConverged(five))
	printNodes(c)

	fmt.Println()
	fmt.Println("== Remove the leader: it commits C_new without counting itself, then steps down ==")
	leader := c.StableLeader()
	withoutLeader := without(five, leader.id)
	before = len(c.Events)
	changeUntilAccepted(c, withoutLeader)
	c.RunUntilConfigured(withoutLeader, 3000)
	c.RunUntilStableLeader(3000)
	proposeUntilAccepted(c, "SET erin 9")
	c.RunUntilApplied(c.StableLeader().LastLogIndex(), 2000)
	printEvents(c, before)
	fmt.Printf("old leader %s is now %s (steppedDownAfterRemoval=%v); new leader %s of term %d in %s\n", name(leader.id), leader.Role, leader.SteppedDownAfterRemoval,
		name(c.StableLeader().id), c.StableLeader().CurrentTerm(), cfgString(c.StableLeader().Configuration()))

	fmt.Println()
	fmt.Println("== Snapshots: compaction every 8 applied entries; a follower that falls behind gets InstallSnapshot ==")
	s := NewCluster(3, 11, 0.0, 5) // snapshotThreshold 8 from DefaultConfig
	s.RunUntilStableLeader(2000)
	for k := 1; k <= 5; k++ {
		s.Propose(fmt.Sprintf("SET k%d v%d", k, k))
	}
	s.RunUntilApplied(s.StableLeader().LastLogIndex(), 1000)
	lead := s.StableLeader()
	follower := (lead.id + 1) % 3
	before = len(s.Events)
	s.Crash(follower)
	for k := 6; k <= 16; k++ {
		s.Propose(fmt.Sprintf("SET k%d v%d", k, k))
		s.Run(20)
	}
	s.Run(200)
	fmt.Printf("while %s is down, the leader applies and compacts:\n", name(follower))
	printEvents(s, before)
	fmt.Printf("  leader log: %s (snapshots taken %d)\n", s.LogOf(lead.id), lead.SnapshotsTaken)
	before = len(s.Events)
	s.Restart(follower)
	s.RunUntilApplied(lead.LastLogIndex(), 2000)
	printEvents(s, before)
	fmt.Printf("after restart: %s log=%s kv size=%d; snapshots sent by leader=%d; state machines converged=%v\n", name(follower), s.LogOf(follower),
		len(s.Node(follower).StateMachine.Data), lead.SnapshotsSent, s.StateMachinesConverged([]int{0, 1, 2}))

	fmt.Println()
	fmt.Println("== Safety checker over 12 seeds: 20% drops, delay 1-10, membership 3->5->4, crash+restart, snapshots every 8 entries ==")
	converged, snapshotsInstalled, changesCompleted := 0, 0, 0
	for seed := int64(1); seed <= 12; seed++ {
		r := NewClusterWithMembers(5, []int{0, 1, 2}, seed, 0.2, 10, DefaultConfig)
		r.RunUntilStableLeader(4000)
		for k := 1; k <= 5; k++ {
			proposeUntilAccepted(r, fmt.Sprintf("SET k%d v%d", k, k))
			r.Run(30)
		}
		changeUntilAccepted(r, five)
		for k := 6; k <= 10; k++ {
			proposeUntilAccepted(r, fmt.Sprintf("SET k%d v%d", k, k))
			r.Run(30)
		}
		if r.RunUntilConfigured(five, 6000) > 0 {
			changesCompleted++
		}
		victim := r.StableLeader()
		if victim == nil {
			victim = r.Node(0)
		}
		r.Crash(victim.id)
		r.RunUntilStableLeader(4000)
		for k := 11; k <= 18; k++ {
			proposeUntilAccepted(r, fmt.Sprintf("SET k%d v%d", k, k))
			r.Run(30)
		}
		r.Restart(victim.id)
		r.RunUntilStableLeader(4000)
		four := without(five, (victim.id+1)%5)
		changeUntilAccepted(r, four)
		if r.RunUntilConfigured(four, 6000) > 0 {
			changesCompleted++
		}
		proposeUntilAccepted(r, "SET final yes")
		fin := r.StableLeader()
		if fin != nil {
			r.RunUntilApplied(fin.LastLogIndex(), 4000)
		}
		for _, n := range r.Nodes {
			snapshotsInstalled += n.SnapshotsInstalled
		}
		if fin != nil && r.StateMachinesConverged(four) {
			_, hasFinal := fin.StateMachine.Get("final")
			_, hasK18 := fin.StateMachine.Get("k18")
			if hasFinal && hasK18 {
				converged++
			}
		}
	}
	fmt.Printf("seeds converged on all writes in the final configuration: %d/12; membership changes completed: %d/24; InstallSnapshot handled: %d\n",
		converged, changesCompleted, snapshotsInstalled)
	fmt.Println("no Election Safety, Leader Completeness, Log Matching, or State Machine Safety check fired (each would have thrown)")
}
