// Project: projects/raft-kv — Stages 2-4: one Raft server (Go). YOUR CODE GOES HERE (nothing to do in Stage 1).
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
	n.ResetElectionTimer(now) // Stage 2: a fresh node must draw its first election deadline here
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

// ResetElectionTimer draws a randomized timeout in [min, max] from this node's own seeded Rng (section 5.2).
func (n *RaftNode) ResetElectionTimer(now int) {
	panic("not implemented: TODO stage 02 RaftNode.ResetElectionTimer")
}

func (n *RaftNode) Tick(now int) []Message {
	panic("not implemented: TODO stage 02 RaftNode.Tick")
}

// StartElection, Figure 2, Candidates, "On conversion to candidate". Returns the RequestVote messages to send.
func (n *RaftNode) StartElection(now int) []Message {
	panic("not implemented: TODO stage 02 RaftNode.StartElection")
}

func (n *RaftNode) Handle(m Message, now int) []Message {
	panic("not implemented: TODO stage 02 RaftNode.Handle")
}

// BecomeFollower, Figure 2, All Servers: a higher term converts any role to follower.
func (n *RaftNode) BecomeFollower(term int64, now int) {
	panic("not implemented: TODO stage 02 RaftNode.BecomeFollower")
}

// OnRequestVote, Figure 2, RequestVote RPC, receiver implementation.
func (n *RaftNode) OnRequestVote(m Message, now int) []Message {
	panic("not implemented: TODO stage 02 RaftNode.OnRequestVote")
}

func (n *RaftNode) OnVoteReply(m Message, now int) []Message {
	panic("not implemented: TODO stage 02 RaftNode.OnVoteReply")
}

// BecomeLeader, Figure 2, Leaders, "upon election". Returns the first round of AppendEntries.
func (n *RaftNode) BecomeLeader(now int) []Message {
	panic("not implemented: TODO stage 02 RaftNode.BecomeLeader")
}

// ReplicateAll sends one AppendEntries per peer (a heartbeat when there is nothing new). Resets the heartbeat timer.
func (n *RaftNode) ReplicateAll(now int) []Message {
	panic("not implemented: TODO stage 02 RaftNode.ReplicateAll")
}

// OnAppendEntries, Figure 2, AppendEntries RPC, receiver implementation. Stage 2: rule 1 and "candidate steps down";
// Stage 3: rules 2-4; Stage 4: rule 5.
func (n *RaftNode) OnAppendEntries(m Message, now int) []Message {
	panic("not implemented: TODO stage 02 RaftNode.OnAppendEntries")
}

// ------------------------------------------------------------------ Stage 3: log replication

// Propose is the client entry point. Appends to the leader's log; the caller replicates. Returns the new index, or -1 if not leader.
func (n *RaftNode) Propose(command string) int {
	panic("not implemented: TODO stage 03 RaftNode.Propose")
}

// AppendEntriesFor builds the AppendEntries for one peer: everything from NextIndex[peer] onward, preceded by the (index, term) it must already hold.
func (n *RaftNode) AppendEntriesFor(peer int) Message {
	panic("not implemented: TODO stage 03 RaftNode.AppendEntriesFor")
}

// OnAppendReply, Figure 2, Leaders: on success update nextIndex/matchIndex; on failure decrement nextIndex and retry.
func (n *RaftNode) OnAppendReply(m Message, now int) []Message {
	panic("not implemented: TODO stage 03 RaftNode.OnAppendReply")
}

// ReplicatedCount is how many nodes (counting the leader) are known to hold the entry at index.
func (n *RaftNode) ReplicatedCount(index int) int {
	panic("not implemented: TODO stage 03 RaftNode.ReplicatedCount")
}

// ------------------------------------------------------------------ Stage 4: commit and apply

// AdvanceCommitIndex, Figure 2, Leaders, last rule, with the section 5.4.2 current-term condition.
func (n *RaftNode) AdvanceCommitIndex(now int) {
	panic("not implemented: TODO stage 04 RaftNode.AdvanceCommitIndex")
}

// ApplyCommitted, Figure 2, All Servers: if commitIndex > lastApplied, increment lastApplied and apply log[lastApplied].
func (n *RaftNode) ApplyCommitted(now int) {
	panic("not implemented: TODO stage 04 RaftNode.ApplyCommitted")
}
