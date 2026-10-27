// Project: projects/raft-kv — test harness (PROVIDED; do not edit).
// Wires N RaftNodes onto one simulated Network, drives the clock, checks the Figure 3 safety properties after every
// delivery, and records client operations as a History for the Stage 4 linearizability check.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// ErrInvariant is wrapped by every Figure 3 safety violation the harness detects (Election Safety, Leader Append-Only,
// Log Matching, State Machine Safety). The harness panics with it after a delivery, because a violated invariant is a
// bug in the node, not a condition a caller can handle.
var ErrInvariant = errors.New("raft invariant violated")

// ErrNoLeader is returned by SubmitUntilAccepted when no leader accepted the command within the attempt budget.
var ErrNoLeader = errors.New("no leader accepted the command")

// ErrPending is returned by History when client ops are still pending; run until settled first.
var ErrPending = errors.New("client ops still pending")

// ClientOp is a client operation the harness is waiting on: proposed to LeaderID at log position (Index, Term).
type ClientOp struct {
	OpID      int
	Client    string
	Command   string
	LeaderID  int
	Index     int
	Term      int64
	InvokedAt int
}

type Cluster struct {
	Size             int
	Seed             int64
	Config           Config
	Clock            *Clock
	Network          *Network
	Storages         []*Storage
	Nodes            []*RaftNode
	Events           []string
	LeadersByTerm    map[int64]int
	CommittedByIndex map[int]LogEntry
	Pending          []ClientOp
	Completed        []Op
	Failed           []ClientOp
	Proposals        int

	leaderLogSnapshot map[int][]LogEntry
	restarts          []int
	nextOpID          int
}

// NewClusterWithConfig wires size nodes with fresh Storage onto one Network seeded from seed.
func NewClusterWithConfig(size int, seed int64, dropRate float64, maxDelay int, config Config) *Cluster {
	c := &Cluster{Size: size, Seed: seed, Config: config, Clock: NewClock(), LeadersByTerm: map[int64]int{},
		CommittedByIndex: map[int]LogEntry{}, leaderLogSnapshot: map[int][]LogEntry{}, restarts: make([]int, size)}
	c.Network = NewNetwork(c.Clock, seed, dropRate, maxDelay)
	for i := 0; i < size; i++ {
		c.Storages = append(c.Storages, NewStorage())
		n := NewRaftNode(i, c.PeersOf(i), c.Storages[i], NodeRng(seed, i, 0), config, 0, c.Trace)
		c.Nodes = append(c.Nodes, n)
		c.Network.Add(n)
	}
	c.Network.SetObserver(c.CheckInvariants)
	return c
}

// NewCluster is NewClusterWithConfig with DefaultConfig.
func NewCluster(size int, seed int64, dropRate float64, maxDelay int) *Cluster {
	return NewClusterWithConfig(size, seed, dropRate, maxDelay, DefaultConfig)
}

func (c *Cluster) PeersOf(id int) []int {
	ps := make([]int, 0, c.Size-1)
	for j := 0; j < c.Size; j++ {
		if j != id {
			ps = append(ps, j)
		}
	}
	return ps
}

// NodeRng: each node gets its own rng stream derived from the seed, so the network's and the nodes' draws never interleave.
func NodeRng(seed int64, id int, restart int) *Rng {
	return NewRng(seed*7919 + 1000003*int64(id+1) + 17*int64(restart))
}

func (c *Cluster) Trace(now int, text string) {
	c.Events = append(c.Events, fmt.Sprintf("t=%-4d %s", now, text))
}

func (c *Cluster) Now() int              { return c.Clock.Now() }
func (c *Cluster) Node(id int) *RaftNode { return c.Nodes[id] }

func (c *Cluster) Tick() {
	c.Network.Tick()
	c.CheckInvariants()
	c.settlePending()
}

func (c *Cluster) Run(ticks int) {
	for i := 0; i < ticks; i++ {
		c.Tick()
	}
}

// RunUntilStableLeader runs until exactly one live node leads and every other live node follows it in the same term. Returns the tick, or -1.
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
	for _, n := range c.Nodes {
		if c.Network.IsDown(n.id) || n == leader {
			continue
		}
		if n.CurrentTerm() != leader.CurrentTerm() || n.LeaderID != leader.id {
			return nil
		}
	}
	return leader
}

func (c *Cluster) Leaders() []*RaftNode {
	var ls []*RaftNode
	for _, n := range c.Nodes {
		if !c.Network.IsDown(n.id) && n.Role == Leader {
			ls = append(ls, n)
		}
	}
	return ls
}

func (c *Cluster) MaxTerm() int64 {
	var t int64
	for _, n := range c.Nodes {
		t = max(t, n.CurrentTerm())
	}
	return t
}

// ------------------------------------------------------------------ proposals (Stage 3) and client ops (Stage 4)

// Propose submits a command to the current stable leader and replicates at once; returns its log index or -1.
func (c *Cluster) Propose(command string) int {
	l := c.StableLeader()
	if l == nil {
		return -1
	}
	return c.ProposeTo(l.id, command)
}

// ProposeTo submits to a specific node (which may be a stale leader inside a partition).
func (c *Cluster) ProposeTo(nodeID int, command string) int {
	n := c.Nodes[nodeID]
	if c.Network.IsDown(nodeID) {
		return -1
	}
	index := n.Propose(command)
	if index > 0 {
		c.Proposals++
		c.Trace(c.Now(), fmt.Sprintf("%s appends index %d %v and replicates", NodeName(nodeID), index, n.Log()[index]))
		c.Network.SendAll(n.ReplicateAll(c.Now()))
	}
	return index
}

// Submit: a client submits PUT/GET through the stable leader. The op is pending until the node that accepted it applies that
// log position: same term -> completed with the apply-time result; different term -> the entry was overwritten, failed.
// Returns the op id, or -1 when there is no stable leader (the client should retry later).
func (c *Cluster) Submit(client, command string) int {
	l := c.StableLeader()
	if l == nil {
		return -1
	}
	invokedAt := c.Now()
	index := c.ProposeTo(l.id, command)
	if index < 0 {
		return -1
	}
	opID := c.nextOpID
	c.nextOpID++
	c.Pending = append(c.Pending, ClientOp{opID, client, command, l.id, index, l.CurrentTerm(), invokedAt})
	return opID
}

// SubmitUntilAccepted retries Submit every 10 ticks until a leader accepts, like a client that follows redirects.
func (c *Cluster) SubmitUntilAccepted(client, command string, maxAttempts int) (int, error) {
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if opID := c.Submit(client, command); opID >= 0 {
			return opID, nil
		}
		c.Run(10)
	}
	return -1, fmt.Errorf("%w: %s after %d attempts", ErrNoLeader, command, maxAttempts)
}

func (c *Cluster) settlePending() {
	for i := len(c.Pending) - 1; i >= 0; i-- {
		p := c.Pending[i]
		n := c.Nodes[p.LeaderID]
		if n.LastApplied < p.Index {
			continue
		}
		c.Pending = slices.Delete(c.Pending, i, i+1)
		if n.Storage.TermAt(p.Index) == p.Term {
			parts := strings.SplitN(p.Command, " ", 3)
			kind := PUT
			if parts[0] == "GET" {
				kind = GET
			}
			var value string
			if kind == GET {
				value = n.StateMachine.Results[p.Index-1]
			} else {
				value = parts[2]
			}
			c.Completed = append(c.Completed, Op{p.OpID, p.Client, kind, parts[1], value, p.InvokedAt, c.Now()})
		} else {
			c.Failed = append(c.Failed, p)
			c.Trace(c.Now(), fmt.Sprintf("client op #%d %s lost: index %d was overwritten", p.OpID, p.Command, p.Index))
		}
	}
}

func (c *Cluster) PendingCount() int { return len(c.Pending) }

// RunUntilSettled runs until no client op is pending, or maxTicks pass. Returns the tick or -1.
func (c *Cluster) RunUntilSettled(maxTicks int) int {
	for i := 0; i < maxTicks; i++ {
		if len(c.Pending) == 0 {
			return c.Now()
		}
		c.Tick()
	}
	if len(c.Pending) == 0 {
		return c.Now()
	}
	return -1
}

// History is the completed ops as a History (ids renumbered densely in completion order). Pending ops are ErrPending.
func (c *Cluster) History() (*History, error) {
	if len(c.Pending) > 0 {
		return nil, fmt.Errorf("%w: %d client ops; run until settled first", ErrPending, len(c.Pending))
	}
	h := NewHistory()
	for _, o := range c.Completed {
		h.Add(o.Client, o.Kind, o.Key, o.Value, o.InvokedAt, o.ReturnedAt)
	}
	return h, nil
}

// RunUntilApplied runs until every live node has applied through index, or maxTicks pass. Returns the tick or -1.
func (c *Cluster) RunUntilApplied(index int, maxTicks int) int {
	for i := 0; i < maxTicks; i++ {
		c.Tick()
		all := true
		for _, n := range c.Nodes {
			if !c.Network.IsDown(n.id) && n.LastApplied < index {
				all = false
			}
		}
		if all {
			return c.Now()
		}
	}
	return -1
}

// RunUntilLogsConverged runs until every live node has the same log, or maxTicks pass. Returns the tick or -1.
func (c *Cluster) RunUntilLogsConverged(maxTicks int) int {
	for i := 0; i < maxTicks; i++ {
		c.Tick()
		if c.LogsConverged() {
			return c.Now()
		}
	}
	return -1
}

func (c *Cluster) LogsConverged() bool {
	var first []LogEntry
	seen := false
	for _, n := range c.Nodes {
		if c.Network.IsDown(n.id) {
			continue
		}
		if !seen {
			first, seen = n.Log(), true
		} else if !slices.Equal(first, n.Log()) {
			return false
		}
	}
	return true
}

// ------------------------------------------------------------------ invariants (Figure 3)

// CheckInvariants checks Election Safety, Leader Append-Only, Log Matching, and State Machine Safety after every delivery.
// A violation panics with an error wrapping ErrInvariant.
func (c *Cluster) CheckInvariants() {
	for _, n := range c.Nodes {
		if n.Role == Leader {
			if existing, ok := c.LeadersByTerm[n.CurrentTerm()]; !ok {
				c.LeadersByTerm[n.CurrentTerm()] = n.id
			} else if existing != n.id {
				panic(fmt.Errorf("%w: ELECTION SAFETY: term %d has leaders %d and %d", ErrInvariant, n.CurrentTerm(), existing, n.id))
			}
			if previous, ok := c.leaderLogSnapshot[n.id]; ok {
				limit := min(len(previous), len(n.Log()))
				if !slices.Equal(n.Log()[:limit], previous) {
					panic(fmt.Errorf("%w: LEADER APPEND-ONLY: %d changed an existing entry while leader", ErrInvariant, n.id))
				}
			}
			c.leaderLogSnapshot[n.id] = slices.Clone(n.Log())
		} else {
			delete(c.leaderLogSnapshot, n.id)
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
			if seen, ok := c.CommittedByIndex[k]; !ok {
				c.CommittedByIndex[k] = n.Log()[k]
			} else if seen != n.Log()[k] {
				panic(fmt.Errorf("%w: STATE MACHINE SAFETY: index %d committed as %v and %v", ErrInvariant, k, seen, n.Log()[k]))
			}
		}
	}
}

// CheckLogMatching: if two logs contain an entry with the same index and term, the logs are identical up to that index.
// Returns an error wrapping ErrInvariant naming the pair of nodes and the index when the property fails.
func CheckLogMatching(a, b []LogEntry, ia, ib int) error {
	limit := min(len(a), len(b)) - 1
	for k := limit; k >= 1; k-- {
		if a[k].Term == b[k].Term {
			for i := 1; i <= k; i++ {
				if a[i] != b[i] {
					return fmt.Errorf("%w: LOG MATCHING: nodes %d and %d agree at index %d (term %d) but differ at index %d: %v vs %v",
						ErrInvariant, ia, ib, k, a[k].Term, i, a[i], b[i])
				}
			}
			return nil
		}
	}
	return nil
}

// ------------------------------------------------------------------ faults

func (c *Cluster) Partition(sideA []int) {
	c.Network.Partition(sideA)
	sorted := slices.Clone(sideA)
	sort.Ints(sorted)
	parts := make([]string, 0, len(sorted))
	for _, id := range sorted {
		parts = append(parts, fmt.Sprint(id))
	}
	c.Trace(c.Now(), "network partition: ["+strings.Join(parts, ", ")+"] | rest")
}

func (c *Cluster) Heal() {
	c.Network.Heal()
	c.Trace(c.Now(), "network heals")
}

func (c *Cluster) Crash(id int) {
	c.Network.Crash(id)
	c.Trace(c.Now(), NodeName(id)+" crashes (volatile state lost, Storage kept)")
}

// Restart from stable storage: term, vote, and log survive; commitIndex, lastApplied, and the KV store are rebuilt.
func (c *Cluster) Restart(id int) *RaftNode {
	c.restarts[id]++
	n := NewRaftNode(id, c.PeersOf(id), c.Storages[id], NodeRng(c.Seed, id, c.restarts[id]), c.Config, c.Now(), c.Trace)
	c.Nodes[id] = n
	c.Network.Restart(n)
	c.Trace(c.Now(), fmt.Sprintf("%s restarts: term %d, log length %d, commitIndex 0, empty state machine", NodeName(id), n.CurrentTerm(), n.LastLogIndex()))
	return n
}

// ExpireTimer forces a node's election timer to expire on the next tick (for scripted scenarios).
func (c *Cluster) ExpireTimer(id int) { c.Nodes[id].ElectionDeadline = c.Now() }

func (c *Cluster) StateMachinesConverged() bool {
	first := ""
	seen := false
	for _, n := range c.Nodes {
		if c.Network.IsDown(n.id) {
			continue
		}
		s := fmt.Sprintf("%v@%d", n.StateMachine, n.LastApplied)
		if !seen {
			first, seen = s, true
		} else if first != s {
			return false
		}
	}
	return true
}

func (c *Cluster) LogOf(id int) string { return c.Nodes[id].Storage.Describe() }
