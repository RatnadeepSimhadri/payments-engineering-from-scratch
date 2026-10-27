// Lesson: phases/05-distributed-foundations/13-gossip-and-failure-detection/docs/en.md
// Gossip Protocols and Failure Detection
// Run:  go run .
// Test: go test ./...
// Sources: Das, Gupta, Motivala, "SWIM: Scalable Weakly-consistent Infection-style Process Group Membership Protocol" (DSN 2002),
//
//	sections 3 (failure detector), 4.1 (infection-style dissemination), 4.2 (suspicion mechanism), 4.3 (round-robin probing).
//
// Go 1.22+, standard library only. Deterministic simulated network: seeded delays, drops, partitions, crashes. Nothing sleeps.
package main

import (
	"container/heap"
	"errors"
	"fmt"
	"math"
	"sort"
)

// Rng is SplitMix64: the same stream in every language, so a seed replays everywhere.
type Rng struct{ state uint64 }

func NewRng(seed int64) *Rng { return &Rng{uint64(seed)} }

func (r *Rng) NextLong() int64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return int64(z ^ (z >> 31))
}

func (r *Rng) NextInt(bound int) int { return int((uint64(r.NextLong()) >> 33) % uint64(bound)) }

func (r *Rng) NextDouble() float64 { return float64(uint64(r.NextLong())>>11) * 0x1.0p-53 }

// Shuffle is Fisher-Yates with this generator, so permutations replay with the seed.
func Shuffle[T any](r *Rng, list []T) {
	for i := len(list) - 1; i > 0; i-- {
		j := r.NextInt(i + 1)
		list[i], list[j] = list[j], list[i]
	}
}

// MemberState: DEAD is terminal for an incarnation; a node that comes back must rejoin with a fresh identity in production.
type MemberState int

const (
	Unknown MemberState = iota // no entry in the view (Java's null)
	Alive
	Suspect
	Dead
)

func (s MemberState) String() string { return [...]string{"UNKNOWN", "ALIVE", "SUSPECT", "DEAD"}[s] }

// Update is one piece of membership information: node, state, incarnation number. The incarnation is owned by the
// node it describes; only that node may increase it, and it does so to refute a suspicion. Precedence (SWIM 4.2):
//
//	alive(i)   overrides alive(j), suspect(j)  when i > j
//	suspect(i) overrides alive(j)              when i >= j
//	suspect(i) overrides suspect(j)            when i > j
//	dead(any)  overrides everything
type Update struct {
	Member      int
	State       MemberState
	Incarnation int64
}

// Overrides reports whether u should replace current; ok is false when there is no current entry.
func (u Update) Overrides(current Update, ok bool) bool {
	if !ok {
		return true
	}
	if current.State == Dead {
		return false
	}
	if u.State == Dead {
		return true
	}
	if u.State == Alive {
		return u.Incarnation > current.Incarnation
	}
	// SUSPECT
	if current.State == Alive {
		return u.Incarnation >= current.Incarnation
	}
	return u.Incarnation > current.Incarnation
}

func (u Update) String() string { return fmt.Sprintf("n%d:%s@%d", u.Member, u.State, u.Incarnation) }

type MessageKind int

const (
	Ping MessageKind = iota
	Ack
	PingReq
	IndirectAck
)

// Message is one protocol message. PING/ACK carry a probe sequence number so an ack can be matched to its probe.
// PING_REQ asks the receiver to ping Target on the sender's behalf; the resulting INDIRECT_ACK is relayed back. Every
// message piggybacks a bounded list of membership updates, which is the entire dissemination mechanism.
type Message struct {
	Kind      MessageKind
	From      int
	To        int
	Target    int
	Seq       int64
	Piggyback []Update
}

func copyUpdates(u []Update) []Update { return append([]Update(nil), u...) }

func PingMessage(from, to int, seq int64, gossip []Update) Message {
	return Message{Ping, from, to, to, seq, copyUpdates(gossip)}
}
func AckMessage(from, to int, seq int64, gossip []Update) Message {
	return Message{Ack, from, to, from, seq, copyUpdates(gossip)}
}
func PingReqMessage(from, to, target int, seq int64, gossip []Update) Message {
	return Message{PingReq, from, to, target, seq, copyUpdates(gossip)}
}
func IndirectAckMessage(from, to, target int, seq int64, gossip []Update) Message {
	return Message{IndirectAck, from, to, target, seq, copyUpdates(gossip)}
}

type Node interface {
	ID() int
	Tick(now int) []Message
	Handle(m Message, now int) []Message
}

var ErrBadConfig = errors.New("bad config")

// Config is the timing of one protocol period, in ticks. Defaults: probe every 12 ticks, wait 4 for a direct ack,
// suspect for 4 periods.
type Config struct {
	ProtocolPeriod   int
	AckTimeout       int
	IndirectProbes   int
	SuspicionPeriods int
	MaxPiggyback     int
	RetransmitFactor int
}

var DefaultConfig = Config{12, 4, 3, 4, 6, 3}

// NewConfig validates what the Java compact constructor checks.
func NewConfig(protocolPeriod, ackTimeout, indirectProbes, suspicionPeriods, maxPiggyback, retransmitFactor int) (Config, error) {
	if ackTimeout <= 0 || ackTimeout*2 >= protocolPeriod {
		return Config{}, fmt.Errorf("%w: need ackTimeout*2 < protocolPeriod so the indirect probe fits in the period", ErrBadConfig)
	}
	if indirectProbes < 0 || suspicionPeriods <= 0 || maxPiggyback <= 0 || retransmitFactor <= 0 {
		return Config{}, ErrBadConfig
	}
	return Config{protocolPeriod, ackTimeout, indirectProbes, suspicionPeriods, maxPiggyback, retransmitFactor}, nil
}

// Probe is a pending direct probe: who we pinged, when the direct ack is due, whether we already escalated.
type Probe struct {
	Target       int
	Seq          int64
	StartedAt    int
	IndirectSent bool
	Acked        bool
}

// Tracer receives the human-readable events a node emits.
type Tracer func(now int, text string)

type gossipEntry struct {
	update Update
	sent   int
}

// SwimNode is one SWIM member. Holds its view of the group, runs the probe cycle, and gossips updates by piggybacking.
type SwimNode struct {
	id     int
	config Config
	rng    *Rng
	tracer Tracer

	// Incarnation is my incarnation number. Bumped only to refute a suspicion about myself.
	Incarnation int64
	// View is what I believe about every member I know of, including myself. Go maps are unordered, so every
	// iteration that can affect behaviour goes through sortedMembers (Java's TreeMap order).
	View map[int]Update
	// SuspectedAt is when each SUSPECT entry was first suspected locally, for the suspicion timeout.
	SuspectedAt map[int]int
	// gossipQueue holds updates still being disseminated, in insertion order, with how many times each was piggybacked.
	gossipQueue []gossipEntry

	probeOrder   []int
	probeCursor  int
	nextPeriodAt int
	nextSeq      int64
	current      *Probe
	relaying     map[int64]int // indirect probes we are relaying for others: seq -> origin

	MessagesSent, DirectAcks, IndirectAcks, SuspicionsRaised, Refutations, DeclaredDead int
}

func NewSwimNode(id int, initialMembers []int, config Config, rng *Rng, now int, tracer Tracer) *SwimNode {
	n := &SwimNode{id: id, config: config, rng: rng, tracer: tracer, View: map[int]Update{}, SuspectedAt: map[int]int{}, relaying: map[int64]int{}}
	for _, m := range initialMembers {
		n.View[m] = Update{m, Alive, 0}
	}
	n.View[id] = Update{id, Alive, 0}
	n.nextPeriodAt = now + 1 + rng.NextInt(config.ProtocolPeriod) // desynchronise the periods across nodes
	n.nextSeq = int64(id)<<32 + 1                                 // probe seqs are unique across the whole group
	return n
}

func (n *SwimNode) ID() int { return n.id }

func (n *SwimNode) sortedMembers() []int {
	members := make([]int, 0, len(n.View))
	for m := range n.View {
		members = append(members, m)
	}
	sort.Ints(members)
	return members
}

// StateOf returns Unknown for a member not in the view.
func (n *SwimNode) StateOf(member int) MemberState {
	if u, ok := n.View[member]; ok {
		return u.State
	}
	return Unknown
}

func (n *SwimNode) membersIn(state MemberState) []int {
	var out []int
	for _, m := range n.sortedMembers() {
		if n.View[m].State == state {
			out = append(out, m)
		}
	}
	return out
}

// AliveMembers and DeadMembers return sorted ids, in place of Java's TreeSet.
func (n *SwimNode) AliveMembers() []int { return n.membersIn(Alive) }
func (n *SwimNode) DeadMembers() []int  { return n.membersIn(Dead) }

// ---------------------------------------------------------------- the protocol period (section 3, 4.3)

func (n *SwimNode) Tick(now int) []Message {
	var out []Message
	if n.current != nil && !n.current.Acked && !n.current.IndirectSent && now >= n.current.StartedAt+n.config.AckTimeout {
		out = append(out, n.sendIndirectProbes()...)
	}
	if now >= n.nextPeriodAt {
		n.nextPeriodAt = now + n.config.ProtocolPeriod
		n.finishProbe(now)
		n.expireSuspicions(now)
		if target, ok := n.nextProbeTarget(); ok {
			n.current = &Probe{Target: target, Seq: n.nextSeq, StartedAt: now}
			n.nextSeq++
			out = append(out, PingMessage(n.id, target, n.current.Seq, n.takeGossip()))
		}
	}
	n.MessagesSent += len(out)
	return out
}

// nextProbeTarget: round-robin over a random permutation of members, reshuffled each pass (section 4.3).
func (n *SwimNode) nextProbeTarget() (int, bool) {
	var candidates []int
	for _, m := range n.sortedMembers() {
		if m != n.id && n.View[m].State != Dead {
			candidates = append(candidates, m)
		}
	}
	if len(candidates) == 0 {
		return 0, false
	}
	if n.probeCursor >= len(n.probeOrder) || !containsAll(candidates, n.probeOrder) {
		n.probeOrder = candidates
		Shuffle(n.rng, n.probeOrder)
		n.probeCursor = 0
	}
	target := n.probeOrder[n.probeCursor]
	n.probeCursor++
	return target, true
}

func containsAll(set, items []int) bool {
	lookup := map[int]bool{}
	for _, s := range set {
		lookup[s] = true
	}
	for _, i := range items {
		if !lookup[i] {
			return false
		}
	}
	return true
}

// sendIndirectProbes: no direct ack in time, so ask k other members to ping the target for us (section 3, step 2).
func (n *SwimNode) sendIndirectProbes() []Message {
	n.current.IndirectSent = true
	var helpers []int
	for _, m := range n.sortedMembers() {
		if m != n.id && m != n.current.Target && n.View[m].State == Alive {
			helpers = append(helpers, m)
		}
	}
	Shuffle(n.rng, helpers)
	var out []Message
	for i := 0; i < min(n.config.IndirectProbes, len(helpers)); i++ {
		out = append(out, PingReqMessage(n.id, helpers[i], n.current.Target, n.current.Seq, n.takeGossip()))
	}
	return out
}

// finishProbe: a probe with no direct and no indirect ack makes the target SUSPECT, not DEAD (section 4.2).
func (n *SwimNode) finishProbe(now int) {
	if n.current == nil {
		return
	}
	if !n.current.Acked {
		if known, ok := n.View[n.current.Target]; ok && known.State == Alive {
			n.SuspicionsRaised++
			n.tracer(now, fmt.Sprintf("n%d got no ack from n%d (direct + %d indirect) -> SUSPECT", n.id, n.current.Target, n.config.IndirectProbes))
			n.ApplyUpdate(Update{n.current.Target, Suspect, known.Incarnation}, now)
		}
	}
	n.current = nil
}

// expireSuspicions: a suspicion that nobody refuted within suspicionPeriods periods becomes a confirmed death.
func (n *SwimNode) expireSuspicions(now int) {
	suspects := make([]int, 0, len(n.SuspectedAt))
	for m := range n.SuspectedAt {
		suspects = append(suspects, m)
	}
	sort.Ints(suspects)
	for _, m := range suspects {
		since := n.SuspectedAt[m]
		if now-since >= n.config.SuspicionPeriods*n.config.ProtocolPeriod {
			if known, ok := n.View[m]; ok && known.State == Suspect {
				n.DeclaredDead++
				n.tracer(now, fmt.Sprintf("n%d suspicion of n%d expired -> DEAD", n.id, m))
				n.ApplyUpdate(Update{m, Dead, known.Incarnation}, now)
			}
		}
	}
}

// ---------------------------------------------------------------- message handling

func (n *SwimNode) Handle(m Message, now int) []Message {
	for _, u := range m.Piggyback {
		n.ApplyUpdate(u, now)
	}
	var out []Message
	switch m.Kind {
	case Ping:
		out = append(out, AckMessage(n.id, m.From, m.Seq, n.takeGossip()))
	case Ack:
		if n.current != nil && m.Seq == n.current.Seq && m.From == n.current.Target {
			n.current.Acked = true
			n.DirectAcks++
		}
	case PingReq:
		n.relaying[m.Seq] = m.From
		out = append(out, PingMessage(n.id, m.Target, m.Seq, n.takeGossip()))
	case IndirectAck:
		if n.current != nil && m.Seq == n.current.Seq && m.Target == n.current.Target {
			n.current.Acked = true
			n.IndirectAcks++
		}
	}
	// An ACK for a seq we are relaying belongs to someone else's probe: forward it as INDIRECT_ACK.
	if origin, relayed := n.relaying[m.Seq]; m.Kind == Ack && relayed && (n.current == nil || m.Seq != n.current.Seq) {
		delete(n.relaying, m.Seq)
		out = append(out, IndirectAckMessage(n.id, origin, m.From, m.Seq, n.takeGossip()))
	}
	n.MessagesSent += len(out)
	return out
}

// ApplyUpdate merges one update into the view using the precedence rules; refutes suspicions about myself; queues what changed.
func (n *SwimNode) ApplyUpdate(u Update, now int) {
	if u.Member == n.id {
		if u.State != Alive && u.Incarnation >= n.Incarnation {
			n.Incarnation = u.Incarnation + 1 // refutation (section 4.2): I am alive, and newer
			n.Refutations++
			n.tracer(now, fmt.Sprintf("n%d hears it is %s@%d -> refutes with incarnation %d", n.id, u.State, u.Incarnation, n.Incarnation))
			alive := Update{n.id, Alive, n.Incarnation}
			n.View[n.id] = alive
			n.enqueue(alive)
		}
		return
	}
	known, ok := n.View[u.Member]
	if !u.Overrides(known, ok) {
		return
	}
	n.View[u.Member] = u
	if u.State == Suspect {
		if _, already := n.SuspectedAt[u.Member]; !already {
			n.SuspectedAt[u.Member] = now
		}
	} else {
		delete(n.SuspectedAt, u.Member)
	}
	n.enqueue(u)
}

func (n *SwimNode) enqueue(u Update) {
	kept := n.gossipQueue[:0]
	for _, e := range n.gossipQueue { // one entry per member: the newest wins
		if e.update.Member != u.Member {
			kept = append(kept, e)
		}
	}
	n.gossipQueue = append(kept, gossipEntry{u, 0})
}

// takeGossip: up to maxPiggyback updates, fewest-transmissions first (stable, so ties keep queue order); retire an
// update after lambda*log2(n) sends (section 4.1).
func (n *SwimNode) takeGossip() []Update {
	size := max(2, len(n.View))
	limit := int(math.Ceil(float64(n.config.RetransmitFactor) * (math.Log(float64(size)) / math.Log(2))))
	entries := append([]gossipEntry(nil), n.gossipQueue...)
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].sent < entries[j].sent })
	var out []Update
	retired := map[Update]bool{}
	bumped := map[Update]int{}
	for _, e := range entries {
		if len(out) >= n.config.MaxPiggyback {
			break
		}
		out = append(out, e.update)
		if e.sent+1 >= limit {
			retired[e.update] = true
		} else {
			bumped[e.update] = e.sent + 1
		}
	}
	kept := n.gossipQueue[:0]
	for _, e := range n.gossipQueue {
		if retired[e.update] {
			continue
		}
		if s, ok := bumped[e.update]; ok {
			e.sent = s
		}
		kept = append(kept, e)
	}
	n.gossipQueue = kept
	return out
}

func (n *SwimNode) GossipQueueSize() int { return len(n.gossipQueue) }

// TakeGossip is exported for the dissemination test; production code never calls it directly.
func (n *SwimNode) TakeGossip() []Update { return n.takeGossip() }

// ---------------------------------------------------------------- the network

type envelope struct {
	deliverAt int
	seq       int64
	message   Message
}

// envelopeHeap orders by (deliverAt, seq): container/heap in place of Java's PriorityQueue.
type envelopeHeap []envelope

func (h envelopeHeap) Len() int { return len(h) }
func (h envelopeHeap) Less(i, j int) bool {
	if h[i].deliverAt != h[j].deliverAt {
		return h[i].deliverAt < h[j].deliverAt
	}
	return h[i].seq < h[j].seq
}
func (h envelopeHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *envelopeHeap) Push(x any)   { *h = append(*h, x.(envelope)) }
func (h *envelopeHeap) Pop() any     { old := *h; e := old[len(old)-1]; *h = old[:len(old)-1]; return e }

// Network is deterministic: (deliverAt, seq) ordering, seeded delays and drops, partitions, crashes, one-way link cuts.
type Network struct {
	rng         *Rng
	dropRate    float64
	maxDelay    int
	nodes       map[int]Node
	inFlight    envelopeHeap
	down        map[int]bool
	cutLinks    map[[2]int]bool // directed a->b pairs that silently drop
	sideA       map[int]bool
	partitioned bool
	now         int
	seq         int64

	Sent, Delivered, Dropped int
	ByKind                   map[MessageKind]int
}

func NewNetwork(seed int64, dropRate float64, maxDelay int) *Network {
	return &Network{rng: NewRng(seed), dropRate: dropRate, maxDelay: max(1, maxDelay), nodes: map[int]Node{},
		down: map[int]bool{}, cutLinks: map[[2]int]bool{}, sideA: map[int]bool{}, ByKind: map[MessageKind]int{}}
}

func (n *Network) Add(node Node)      { n.nodes[node.ID()] = node }
func (n *Network) Now() int           { return n.now }
func (n *Network) IsDown(id int) bool { return n.down[id] }
func (n *Network) Crash(id int)       { n.down[id] = true }

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
	n.cutLinks = map[[2]int]bool{}
}

// CutLink cuts traffic in both directions between exactly two nodes; everyone else still reaches both.
func (n *Network) CutLink(a, b int) {
	n.cutLinks[[2]int{a, b}] = true
	n.cutLinks[[2]int{b, a}] = true
}

func (n *Network) blocked(a, b int) bool {
	return n.down[a] || n.down[b] || (n.partitioned && n.sideA[a] != n.sideA[b]) || n.cutLinks[[2]int{a, b}]
}

func (n *Network) Send(m Message) {
	n.Sent++
	n.ByKind[m.Kind]++
	if n.blocked(m.From, m.To) {
		n.Dropped++
		return
	}
	roll := n.rng.NextDouble()
	delay := 1 + n.rng.NextInt(n.maxDelay)
	if roll < n.dropRate {
		n.Dropped++
		return
	}
	heap.Push(&n.inFlight, envelope{n.now + delay, n.seq, m})
	n.seq++
}

func (n *Network) SendAll(ms []Message) {
	for _, m := range ms {
		n.Send(m)
	}
}

func (n *Network) Tick() {
	n.now++
	for len(n.inFlight) > 0 && n.inFlight[0].deliverAt <= n.now {
		m := heap.Pop(&n.inFlight).(envelope).message
		if n.blocked(m.From, m.To) {
			n.Dropped++
			continue
		}
		n.Delivered++
		n.SendAll(n.nodes[m.To].Handle(m, n.now))
	}
	ids := make([]int, 0, len(n.nodes))
	for id := range n.nodes {
		ids = append(ids, id)
	}
	sort.Ints(ids) // Java's TreeMap visits nodes in id order
	for _, id := range ids {
		if !n.down[id] {
			n.SendAll(n.nodes[id].Tick(n.now))
		}
	}
}

// Cluster: N SWIM nodes on one network, with the measurements the lesson is about: detection time, dissemination
// time, false positives.
type Cluster struct {
	Size    int
	Seed    int64
	Config  Config
	Network *Network
	Nodes   []*SwimNode
	Events  []string
}

func NewClusterWith(size int, seed int64, dropRate float64, maxDelay int, config Config) *Cluster {
	c := &Cluster{Size: size, Seed: seed, Config: config, Network: NewNetwork(seed, dropRate, maxDelay)}
	all := make([]int, size)
	for i := range all {
		all[i] = i
	}
	for i := 0; i < size; i++ {
		n := NewSwimNode(i, all, config, NewRng(seed*7919+1000003*int64(i+1)), 0, c.trace)
		c.Nodes = append(c.Nodes, n)
		c.Network.Add(n)
	}
	return c
}

func NewCluster(size int, seed int64, dropRate float64, maxDelay int) *Cluster {
	return NewClusterWith(size, seed, dropRate, maxDelay, DefaultConfig)
}

func (c *Cluster) trace(now int, text string) {
	c.Events = append(c.Events, fmt.Sprintf("t=%-4d %s", now, text))
}

func (c *Cluster) Now() int              { return c.Network.Now() }
func (c *Cluster) Node(id int) *SwimNode { return c.Nodes[id] }
func (c *Cluster) Tick()                 { c.Network.Tick() }

func (c *Cluster) Run(ticks int) {
	for i := 0; i < ticks; i++ {
		c.Tick()
	}
}

func (c *Cluster) Crash(id int) {
	c.Network.Crash(id)
	c.trace(c.Now(), fmt.Sprintf("n%d crashes", id))
}

// RunUntilAllBelieve returns the tick at which every live node holds state for member, or -1.
func (c *Cluster) RunUntilAllBelieve(member int, state MemberState, maxTicks int) int {
	for i := 0; i < maxTicks; i++ {
		c.Tick()
		if c.AllBelieve(member, state) {
			return c.Now()
		}
	}
	return -1
}

func (c *Cluster) AllBelieve(member int, state MemberState) bool {
	for _, n := range c.Nodes {
		if !c.Network.IsDown(n.id) && n.id != member && n.StateOf(member) != state {
			return false
		}
	}
	return true
}

func (c *Cluster) CountBelieving(member int, state MemberState) int {
	count := 0
	for _, n := range c.Nodes {
		if !c.Network.IsDown(n.id) && n.id != member && n.StateOf(member) == state {
			count++
		}
	}
	return count
}

// FalsePositives counts live nodes marked DEAD by any other live node.
func (c *Cluster) FalsePositives() int {
	count := 0
	for _, n := range c.Nodes {
		if c.Network.IsDown(n.id) {
			continue
		}
		for _, dead := range n.DeadMembers() {
			if !c.Network.IsDown(dead) {
				count++
			}
		}
	}
	return count
}

func (c *Cluster) ViewsConverged() bool {
	first, have := "", false
	for _, n := range c.Nodes {
		if c.Network.IsDown(n.id) {
			continue
		}
		v := fmt.Sprint(n.DeadMembers())
		if !have {
			first, have = v, true
		} else if first != v {
			return false
		}
	}
	return true
}

func (c *Cluster) TotalSent() int {
	s := 0
	for _, n := range c.Nodes {
		s += n.MessagesSent
	}
	return s
}

func printEvents(c *Cluster, from int) {
	for i := from; i < len(c.Events); i++ {
		fmt.Println(c.Events[i])
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func main() {
	fmt.Println("== Precedence rules for membership updates (SWIM section 4.2) ==")
	alive0, suspect0 := Update{3, Alive, 0}, Update{3, Suspect, 0}
	alive1, dead0 := Update{3, Alive, 1}, Update{3, Dead, 0}
	fmt.Printf("%s overrides %s = %t   (same incarnation: suspicion wins)\n", suspect0, alive0, suspect0.Overrides(alive0, true))
	fmt.Printf("%s overrides %s = %t  (a stale alive cannot clear a suspicion)\n", alive0, suspect0, alive0.Overrides(suspect0, true))
	fmt.Printf("%s overrides %s = %t   (refutation: higher incarnation wins)\n", alive1, suspect0, alive1.Overrides(suspect0, true))
	fmt.Printf("%s overrides %s = %t    (dead is terminal)\n", dead0, alive1, dead0.Overrides(alive1, true))

	fmt.Println()
	fmt.Println("== 8 nodes, seed 3, delay 1-2, no drops: n5 crashes at t=100 ==")
	c := NewCluster(8, 3, 0.0, 2)
	c.Run(100)
	before := len(c.Events)
	c.Crash(5)
	suspectAt := c.RunUntilAllBelieve(5, Suspect, 2000)
	deadAt := c.RunUntilAllBelieve(5, Dead, 2000)
	printEvents(c, before)
	fmt.Printf("first suspicion reached everyone by t=%d, DEAD everywhere by t=%d (protocol period %d, suspicion timeout %d)\n",
		suspectAt, deadAt, c.Config.ProtocolPeriod, c.Config.SuspicionPeriods*c.Config.ProtocolPeriod)
	fmt.Printf("messages per node per period: %.2f  (constant: does not grow with N)\n",
		float64(c.TotalSent())/float64(8*(c.Now()/c.Config.ProtocolPeriod)))
	fmt.Printf("false positives: %d\n", c.FalsePositives())

	fmt.Println()
	fmt.Println("== Load per node is constant in N (the reason gossip beats all-to-all heartbeats) ==")
	for _, n := range []int{8, 16, 32, 64} {
		l := NewCluster(n, 5, 0.0, 2)
		l.Run(600)
		perNodePerPeriod := float64(l.TotalSent()) / float64(n*(600/l.Config.ProtocolPeriod))
		fmt.Printf("N=%-3d messages per node per period = %.2f   (all-to-all heartbeats would need %d)\n", n, perNodePerPeriod, n-1)
	}

	fmt.Println()
	fmt.Println("== A slow link is not a dead node: n1 cannot reach n2, but n0, n3, n4 can; indirect probes save n2 ==")
	s := NewCluster(5, 11, 0.0, 2)
	s.Run(50)
	before = len(s.Events)
	s.Network.CutLink(1, 2)
	s.Run(600)
	printEvents(s, before)
	fmt.Printf("after 600 ticks with the n1<->n2 link cut: n1 believes n2 is %s; false positives = %d; indirect acks received by n1 = %d\n",
		s.Node(1).StateOf(2), s.FalsePositives(), s.Node(1).IndirectAcks)

	fmt.Println()
	fmt.Println("== Without indirect probes the same cut becomes a false suspicion, and n2 refutes it by bumping its incarnation ==")
	noIndirect := NewClusterWith(5, 11, 0.0, 2, must(NewConfig(12, 4, 0, 4, 6, 3)))
	noIndirect.Run(50)
	before = len(noIndirect.Events)
	noIndirect.Network.CutLink(1, 2)
	noIndirect.Run(120)
	printEvents(noIndirect, before)
	fmt.Printf("n2 incarnation = %d, refutations = %d; n1 believes n2 is %s (the refutation arrived via other members' piggyback)\n",
		noIndirect.Node(2).Incarnation, noIndirect.Node(2).Refutations, noIndirect.Node(1).StateOf(2))

	fmt.Println()
	fmt.Println("== Convergence over 20 seeds: 12 nodes, 10% message loss, delay 1-2, one crash at t=200 ==")
	converged, worstDead, totalFalse := 0, 0, 0
	sumDead := 0
	for seed := int64(1); seed <= 20; seed++ {
		r := NewCluster(12, seed, 0.10, 2)
		r.Run(200)
		r.Crash(7)
		t := r.RunUntilAllBelieve(7, Dead, 3000)
		if t > 0 {
			converged++
			sumDead += t - 200
			worstDead = max(worstDead, t-200)
		}
		totalFalse += r.FalsePositives()
	}
	mean := 0
	if converged > 0 {
		mean = sumDead / converged
	}
	fmt.Printf("seeds where every live node declared n7 DEAD: %d/20; mean detection+dissemination %d ticks, worst %d ticks; false positives across all seeds: %d\n",
		converged, mean, worstDead, totalFalse)
	fmt.Printf("expected: suspicion timeout (48) + one or two periods to notice + a few ticks to gossip; never a false positive because every suspect gets %d periods to refute\n",
		DefaultConfig.SuspicionPeriods)
}
