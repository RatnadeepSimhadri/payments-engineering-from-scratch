// Project: projects/raft-kv — Stages 2-4: one Raft server (Go).
// Stage 2 adds election (Figure 2: Followers, Candidates, RequestVote RPC), Stage 3 log replication (AppendEntries RPC,
// nextIndex/matchIndex), Stage 4 commit and apply (commitIndex, lastApplied, the KV state machine).
// Sources: Ongaro & Ousterhout, "In Search of an Understandable Consensus Algorithm" (USENIX ATC 2014), Figure 2,
//
//	sections 5.2, 5.3, 5.4.1, 5.4.2, 8. Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"fmt"
	"slices"
)

type RaftNode struct {
	id      int
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
	// Volatile state on leaders, reinitialised after every election.
	NextIndex  map[int]int
	MatchIndex map[int]int

	VotesGranted          map[int]bool
	ElectionDeadline      int
	NextHeartbeat         int
	ElectionsStarted      int
	ConsistencyRejections int
}

func NewRaftNode(id int, peers []int, storage *Storage, rng *Rng, config Config, now int, tracer Tracer) *RaftNode {
	if tracer == nil {
		tracer = TracerNone
	}
	n := &RaftNode{id: id, Peers: slices.Clone(peers), Storage: storage, rng: rng, config: config, tracer: tracer,
		Role: Follower, LeaderID: -1, StateMachine: NewKVStore(), NextIndex: map[int]int{}, MatchIndex: map[int]int{}, VotesGranted: map[int]bool{}}
	n.ResetElectionTimer(now)
	return n
}

func (n *RaftNode) ID() int            { return n.id }
func (n *RaftNode) CurrentTerm() int64 { return n.Storage.CurrentTerm }
func (n *RaftNode) VotedFor() int      { return n.Storage.VotedFor }
func (n *RaftNode) ClusterSize() int   { return len(n.Peers) + 1 }
func (n *RaftNode) Majority() int      { return n.ClusterSize()/2 + 1 }
func (n *RaftNode) Votes() int         { return len(n.VotesGranted) }
func (n *RaftNode) Log() []LogEntry    { return n.Storage.Log }
func (n *RaftNode) LastLogIndex() int  { return n.Storage.LastIndex() }
func (n *RaftNode) LastLogTerm() int64 { return n.Storage.LastTerm() }
func NodeName(id int) string           { return fmt.Sprintf("n%d", id) }

// ------------------------------------------------------------------ Stage 2: election

// ResetElectionTimer draws a randomized timeout in [min, max] from the node's own seeded Rng: the paper's whole answer to split votes (section 5.2).
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
	// Followers and candidates: election timeout elapses -> start (another) election.
	if now >= n.ElectionDeadline {
		return n.StartElection(now)
	}
	return nil
}

// StartElection, Figure 2, Candidates, "On conversion to candidate": increment term, vote for self, persist, reset timer, ask everyone.
func (n *RaftNode) StartElection(now int) []Message {
	n.Role = Candidate
	n.Storage.Save(n.Storage.CurrentTerm+1, n.id)
	n.LeaderID = -1
	n.VotesGranted = map[int]bool{n.id: true}
	n.ElectionsStarted++
	n.ResetElectionTimer(now)
	n.tracer(now, fmt.Sprintf("%s election timeout -> candidate for term %d", NodeName(n.id), n.Storage.CurrentTerm))
	out := make([]Message, 0, len(n.Peers))
	for _, p := range n.Peers {
		out = append(out, NewRequestVote(n.id, p, n.Storage.CurrentTerm, n.LastLogIndex(), n.LastLogTerm()))
	}
	return out
}

func (n *RaftNode) Handle(m Message, now int) []Message {
	// Figure 2, All Servers: if RPC request or response contains term T > currentTerm, set currentTerm = T, convert to follower.
	if m.Term > n.Storage.CurrentTerm {
		n.BecomeFollower(m.Term, now)
	}
	switch m.Kind {
	case RequestVote:
		return n.OnRequestVote(m, now)
	case RequestVoteReply:
		return n.OnVoteReply(m, now)
	case AppendEntries:
		return n.OnAppendEntries(m, now)
	default:
		return n.OnAppendReply(m, now)
	}
}

func (n *RaftNode) BecomeFollower(term int64, now int) {
	before := n.Role
	n.Role = Follower
	n.LeaderID = -1
	n.VotesGranted = map[int]bool{}
	if term > n.Storage.CurrentTerm {
		n.Storage.Save(term, -1) // a new term means votedFor is null again
		was := ""
		switch before {
		case Leader:
			was = " (was LEADER)"
		case Candidate:
			was = " (was candidate)"
		}
		n.tracer(now, fmt.Sprintf("%s sees term %d -> follower%s", NodeName(n.id), term, was))
	}
	if before == Leader {
		n.ResetElectionTimer(now) // a leader keeps no election timer; give it one when it steps down
	}
}

// OnRequestVote, Figure 2, RequestVote RPC, receiver implementation.
func (n *RaftNode) OnRequestVote(m Message, now int) []Message {
	if m.Term < n.Storage.CurrentTerm { // 1. reply false if term < currentTerm
		return []Message{NewVoteReply(n.id, m.From, n.Storage.CurrentTerm, false)}
	}
	canVote := n.Storage.VotedFor == -1 || n.Storage.VotedFor == m.From // 2. votedFor is null or candidateId
	logOk := n.Storage.UpToDate(m.PrevLogIndex, m.PrevLogTerm)          //    and candidate's log is at least as up-to-date
	if canVote && logOk {
		n.Storage.Save(n.Storage.CurrentTerm, m.From) // persist the vote before the reply leaves
		n.ResetElectionTimer(now)                     // granting a vote resets the election timer
		n.tracer(now, fmt.Sprintf("%s grants vote to %s for term %d", NodeName(n.id), NodeName(m.From), m.Term))
		return []Message{NewVoteReply(n.id, m.From, n.Storage.CurrentTerm, true)}
	}
	return []Message{NewVoteReply(n.id, m.From, n.Storage.CurrentTerm, false)}
}

func (n *RaftNode) OnVoteReply(m Message, now int) []Message {
	// A reply from an earlier election (older term) or after we stopped being a candidate is stale: ignore it.
	if n.Role != Candidate || m.Term != n.Storage.CurrentTerm {
		return nil
	}
	if m.Success {
		n.VotesGranted[m.From] = true
		if len(n.VotesGranted) >= n.Majority() {
			return n.BecomeLeader(now)
		}
	}
	return nil
}

// BecomeLeader, Figure 2, Leaders: reinitialise nextIndex/matchIndex, append a no-op (section 8), send initial AppendEntries.
func (n *RaftNode) BecomeLeader(now int) []Message {
	n.Role = Leader
	n.LeaderID = n.id
	for _, p := range n.Peers {
		n.NextIndex[p] = n.LastLogIndex() + 1
		n.MatchIndex[p] = 0
	}
	n.tracer(now, fmt.Sprintf("%s wins %d/%d votes -> LEADER of term %d, log ends at index %d", NodeName(n.id), len(n.VotesGranted), n.ClusterSize(), n.Storage.CurrentTerm, n.LastLogIndex()))
	n.Storage.Append(LogEntry{n.Storage.CurrentTerm, "NOOP"}) // Stage 4: lets the leader commit earlier-term entries at once
	return n.ReplicateAll(now)
}

// ReplicateAll sends one AppendEntries per peer; with nothing new to ship it is a heartbeat. Resets the heartbeat timer.
func (n *RaftNode) ReplicateAll(now int) []Message {
	n.NextHeartbeat = now + n.config.HeartbeatInterval
	out := make([]Message, 0, len(n.Peers))
	for _, p := range n.Peers {
		out = append(out, n.AppendEntriesFor(p))
	}
	return out
}

// ------------------------------------------------------------------ Stage 3: log replication

// Propose is the client entry point. Appends to the leader's log; the caller replicates. Returns the new index, or -1 if not leader.
func (n *RaftNode) Propose(command string) int {
	if n.Role != Leader {
		return -1
	}
	n.Storage.Append(LogEntry{n.Storage.CurrentTerm, command})
	return n.LastLogIndex()
}

// AppendEntriesFor builds the AppendEntries for one peer: everything from nextIndex[peer] onward, preceded by the (index, term) it must already hold.
func (n *RaftNode) AppendEntriesFor(peer int) Message {
	next := n.NextIndex[peer]
	prev := next - 1
	return NewAppendEntries(n.id, peer, n.Storage.CurrentTerm, prev, n.Storage.TermAt(prev), n.Storage.EntriesFrom(next), n.CommitIndex)
}

// OnAppendEntries, Figure 2, AppendEntries RPC, receiver implementation, rules 1-5 in order.
func (n *RaftNode) OnAppendEntries(m Message, now int) []Message {
	if m.Term < n.Storage.CurrentTerm { // 1
		return []Message{NewAppendReply(n.id, m.From, n.Storage.CurrentTerm, false, 0)}
	}
	if n.Role == Leader {
		panic(fmt.Sprintf("two leaders in term %d: %d and %d", n.Storage.CurrentTerm, n.id, m.From))
	}
	if n.Role == Candidate {
		n.Role = Follower
		n.VotesGranted = map[int]bool{}
		n.tracer(now, fmt.Sprintf("%s hears leader %s for term %d -> candidate steps down", NodeName(n.id), NodeName(m.From), m.Term))
	}
	n.LeaderID = m.From
	n.ResetElectionTimer(now)
	if m.PrevLogIndex > n.LastLogIndex() || n.Storage.TermAt(m.PrevLogIndex) != m.PrevLogTerm { // 2: consistency check
		n.ConsistencyRejections++
		n.tracer(now, fmt.Sprintf("%s rejects AppendEntries: no entry (%d, t%d); my log ends at %d", NodeName(n.id), m.PrevLogIndex, m.PrevLogTerm, n.LastLogIndex()))
		return []Message{NewAppendReply(n.id, m.From, n.Storage.CurrentTerm, false, 0)}
	}
	index := m.PrevLogIndex
	for _, e := range m.Entries {
		index++
		if index <= n.LastLogIndex() {
			if n.Storage.TermAt(index) == e.Term { // 4: already have it
				continue
			}
			n.tracer(now, fmt.Sprintf("%s conflict at index %d: has %v, leader has %v -> truncating %d", NodeName(n.id), index, n.Storage.Log[index], e, n.LastLogIndex()-index+1))
			n.Storage.Truncate(index) // 3: delete the conflict and all after it
		}
		n.Storage.Append(e) // 4: append the new entry
	}
	lastNew := m.PrevLogIndex + len(m.Entries)
	if m.LeaderCommit > n.CommitIndex { // 5 (Stage 4)
		n.CommitIndex = min(m.LeaderCommit, lastNew)
		n.ApplyCommitted(now)
	}
	return []Message{NewAppendReply(n.id, m.From, n.Storage.CurrentTerm, true, lastNew)}
}

// OnAppendReply, Figure 2, Leaders: on success update nextIndex/matchIndex and try to commit; on failure decrement nextIndex and retry.
func (n *RaftNode) OnAppendReply(m Message, now int) []Message {
	if n.Role != Leader || m.Term != n.Storage.CurrentTerm {
		return nil
	}
	if m.Success {
		if m.MatchIndex > n.MatchIndex[m.From] {
			n.MatchIndex[m.From] = m.MatchIndex
			n.NextIndex[m.From] = m.MatchIndex + 1
			n.AdvanceCommitIndex(now) // Stage 4
		}
		return nil
	}
	n.NextIndex[m.From] = max(1, n.NextIndex[m.From]-1)
	return []Message{n.AppendEntriesFor(m.From)}
}

// ReplicatedCount is how many nodes (counting the leader) are known to hold the entry at index.
func (n *RaftNode) ReplicatedCount(index int) int {
	count := 1
	for _, p := range n.Peers {
		if n.MatchIndex[p] >= index {
			count++
		}
	}
	return count
}

// ------------------------------------------------------------------ Stage 4: commit and apply

// AdvanceCommitIndex, Figure 2, Leaders, last rule: if there exists an N > commitIndex such that a majority of matchIndex[i] >= N and
// log[N].term == currentTerm, set commitIndex = N. The term condition is section 5.4.2 (Figure 8): a leader never
// commits an earlier-term entry by counting replicas; it commits it by committing one of its own entries after it.
func (n *RaftNode) AdvanceCommitIndex(now int) {
	if n.Role != Leader {
		return
	}
	for idx := n.LastLogIndex(); idx > n.CommitIndex; idx-- {
		if n.Storage.TermAt(idx) != n.Storage.CurrentTerm {
			continue
		}
		if n.ReplicatedCount(idx) >= n.Majority() {
			n.tracer(now, fmt.Sprintf("%s commits through index %d (%d/%d replicas)", NodeName(n.id), idx, n.ReplicatedCount(idx), n.ClusterSize()))
			n.CommitIndex = idx
			n.ApplyCommitted(now)
			return
		}
	}
}

// ApplyCommitted, Figure 2, All Servers: if commitIndex > lastApplied, increment lastApplied and apply log[lastApplied] to the state machine.
// A command the state machine rejects means the log is corrupt, which is a bug, so it panics.
func (n *RaftNode) ApplyCommitted(now int) {
	for n.LastApplied < n.CommitIndex {
		n.LastApplied++
		if _, err := n.StateMachine.Apply(n.Storage.Log[n.LastApplied].Command); err != nil {
			panic(err)
		}
	}
}
