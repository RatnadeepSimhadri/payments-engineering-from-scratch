// Lesson: phases/06-replication-and-consensus/03-quorums/docs/en.md
// Quorums: R + W > N and Its Limits
// Run:  go run .
// Test: go test ./...
// Sources: DeCandia et al., "Dynamo: Amazon's Highly Available Key-value Store" (SOSP 2007), sections 4.5-4.7;
// Gifford, "Weighted Voting for Replicated Data" (SOSP 1979); Apache Cassandra docs on consistency levels and read repair.
// Go 1.22+, standard library only. Same design as the Java and Python versions: seeded SplitMix64 for drops, a tick
// counter for time, no goroutines.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf16"
)

// Rng is SplitMix64 (Steele, Lea, Flood, OOPSLA 2014). Same stream for the same seed in every language.
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

// JavaStringHash is Java's String.hashCode(): h = 31*h + c over UTF-16 code units, wrapping at 32 bits, returned signed.
// Placement must hash keys exactly like the Java version or the home replicas (and the demo) differ.
func JavaStringHash(s string) int32 {
	var h int32
	for _, u := range utf16.Encode([]rune(s)) {
		h = 31*h + int32(u)
	}
	return h
}

// floorMod is Java's Math.floorMod: the remainder takes the sign of the divisor.
func floorMod(a int32, n int) int {
	m := int(a) % n
	if m < 0 {
		m += n
	}
	return m
}

// Versioned is a value with its version. Last-writer-wins without version vectors: the version is (timestamp, writerId),
// compared lexicographically so two writes at the same tick still have one deterministic winner. This is the Cassandra
// model; the price is that concurrent writes are not detected, one of them silently loses, and that is a feature of the demo.
type Versioned struct {
	Value     string
	Timestamp int64
	WriterID  int
}

// None is the absent version: what the Java calls Versioned.NONE (a null value). IsNone() is the Go spelling of `value == null`.
var None = Versioned{"", -1, -1}

func (v Versioned) IsNone() bool { return v.Timestamp < 0 }

func (v Versioned) NewerThan(other Versioned) bool {
	return v.Timestamp > other.Timestamp || (v.Timestamp == other.Timestamp && v.WriterID > other.WriterID)
}

func (v Versioned) String() string {
	if v.IsNone() {
		return "<none>"
	}
	return fmt.Sprintf("%s@%d/w%d", v.Value, v.Timestamp, v.WriterID)
}

// ReadResult is the result of a quorum read: the winning version and which replicas answered, so the caller sees what the quorum saw.
type ReadResult struct {
	Winner      Versioned
	Responses   int
	Respondents []int
	Repaired    int
}

// Value returns the winning value and whether there was one (Java returns null for none).
func (r ReadResult) Value() (string, bool) { return r.Winner.Value, !r.Winner.IsNone() }

// ErrQuorumNotReached is returned when fewer than W acks or fewer than R responses arrive. The client must retry or
// report failure; never pretend.
var ErrQuorumNotReached = errors.New("quorum not reached")

// ErrBadConfig is returned for an R/W outside 1..N, or a cluster smaller than N.
var ErrBadConfig = errors.New("bad quorum configuration")

// StorageNode is one storage node. Holds its own data plus hints: writes it accepted on behalf of a node that was
// unreachable, to be handed off when that node comes back (Dynamo 4.6). Hints are keyed by the intended node.
type StorageNode struct {
	ID             int
	Data           map[string]Versioned
	Hints          map[int]map[string]Versioned
	WritesAccepted int
	ReadsServed    int
	HintsStored    int
	HintsDelivered int
	RepairsApplied int
}

func NewStorageNode(id int) *StorageNode {
	return &StorageNode{ID: id, Data: map[string]Versioned{}, Hints: map[int]map[string]Versioned{}}
}

func (n *StorageNode) current(key string) Versioned {
	if v, ok := n.Data[key]; ok {
		return v
	}
	return None
}

// Put stores only if newer. Returns true when the stored version changed. Idempotent, so replays and repairs are harmless.
func (n *StorageNode) Put(key string, v Versioned) bool {
	n.WritesAccepted++
	if v.NewerThan(n.current(key)) {
		n.Data[key] = v
		return true
	}
	return false
}

func (n *StorageNode) Get(key string) Versioned {
	n.ReadsServed++
	return n.current(key)
}

func (n *StorageNode) StoreHint(forNode int, key string, v Versioned) {
	n.HintsStored++
	forTarget, ok := n.Hints[forNode]
	if !ok {
		forTarget = map[string]Versioned{}
		n.Hints[forNode] = forTarget
	}
	existing, ok := forTarget[key]
	if !ok {
		existing = None
	}
	if v.NewerThan(existing) {
		forTarget[key] = v
	}
}

func (n *StorageNode) HintCount() int {
	count := 0
	for _, m := range n.Hints {
		count += len(m)
	}
	return count
}

// Network is the simulated network: a set of unreachable nodes, a partition, and a seeded drop rate. Requests are synchronous RPCs.
type Network struct {
	rng         *Rng
	dropRate    float64
	down        map[int]bool
	sideA       map[int]bool
	partitioned bool
	Attempts    int
	Dropped     int
}

func NewNetwork(seed int64, dropRate float64) *Network {
	return &Network{rng: NewRng(seed), dropRate: dropRate, down: map[int]bool{}, sideA: map[int]bool{}}
}

func (n *Network) Crash(id int)       { n.down[id] = true }
func (n *Network) Recover(id int)     { delete(n.down, id) }
func (n *Network) IsDown(id int) bool { return n.down[id] }
func (n *Network) Partition(a ...int) {
	n.sideA = map[int]bool{}
	for _, id := range a {
		n.sideA[id] = true
	}
	n.partitioned = true
}
func (n *Network) Heal() { n.partitioned = false; n.sideA = map[int]bool{} }

// Reachable: can `from` (the coordinator) reach `to` right now? Deterministic given the seed and the call order.
func (n *Network) Reachable(from, to int) bool {
	n.Attempts++
	if n.down[to] || (n.partitioned && n.sideA[from] != n.sideA[to]) {
		n.Dropped++
		return false
	}
	if n.dropRate > 0 && n.rng.NextDouble() < n.dropRate {
		n.Dropped++
		return false
	}
	return true
}

// QuorumConfig is the N/R/W knobs plus the two Dynamo options this lesson is about.
type QuorumConfig struct {
	N, R, W    int
	Sloppy     bool
	ReadRepair bool
}

func NewQuorumConfig(n, r, w int, sloppy, readRepair bool) (QuorumConfig, error) {
	if n < 1 || r < 1 || w < 1 || r > n || w > n {
		return QuorumConfig{}, fmt.Errorf("%w: need 1 <= R, W <= N", ErrBadConfig)
	}
	return QuorumConfig{n, r, w, sloppy, readRepair}, nil
}

// MustConfig is NewQuorumConfig for literal arguments in the demo and tests.
func MustConfig(n, r, w int, sloppy, readRepair bool) QuorumConfig {
	c, err := NewQuorumConfig(n, r, w, sloppy, readRepair)
	if err != nil {
		panic(err)
	}
	return c
}

func (c QuorumConfig) Overlaps() bool { return c.R+c.W > c.N }

func (c QuorumConfig) String() string {
	s := fmt.Sprintf("N=%d R=%d W=%d", c.N, c.R, c.W)
	if c.Sloppy {
		s += " sloppy"
	} else {
		s += " strict"
	}
	if c.ReadRepair {
		s += " read-repair"
	}
	return s
}

// fmtList prints a slice the way Java prints a List: "[1, 3]".
func fmtList(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// QuorumStore is a leaderless replicated store. Any node may coordinate. Each key has N home replicas (the first N nodes
// on the ring starting at hash(key)); a sloppy quorum may spill onto the next nodes with a hint when a home node is unreachable.
type QuorumStore struct {
	Nodes         []*StorageNode
	Network       *Network
	Config        QuorumConfig
	clock         int64
	Writes        int
	WriteFailures int
	Reads         int
	ReadFailures  int
	HintedWrites  int
	Repairs       int
	Events        []string
}

func NewQuorumStore(totalNodes int, config QuorumConfig, seed int64, dropRate float64) (*QuorumStore, error) {
	if totalNodes < config.N {
		return nil, fmt.Errorf("%w: need at least N nodes", ErrBadConfig)
	}
	s := &QuorumStore{Config: config, Network: NewNetwork(seed, dropRate)}
	for i := 0; i < totalNodes; i++ {
		s.Nodes = append(s.Nodes, NewStorageNode(i))
	}
	return s, nil
}

// MustStore is NewQuorumStore for literal arguments in the demo and tests.
func MustStore(totalNodes int, config QuorumConfig, seed int64, dropRate float64) *QuorumStore {
	s, err := NewQuorumStore(totalNodes, config, seed, dropRate)
	if err != nil {
		panic(err)
	}
	return s
}

func (s *QuorumStore) Now() int64 { return s.clock }
func (s *QuorumStore) Tick()      { s.clock++ }

// HomeNodes is deterministic placement: the key's home replicas are N consecutive ring positions.
func (s *QuorumStore) HomeNodes(key string) []int {
	start := floorMod(JavaStringHash(key), len(s.Nodes))
	home := make([]int, 0, s.Config.N)
	for i := 0; i < s.Config.N; i++ {
		home = append(home, (start+i)%len(s.Nodes))
	}
	return home
}

// ContactOrder is the home replicas with the coordinator first when it is one of them: a coordinator always consults its own copy first.
func (s *QuorumStore) ContactOrder(coordinator int, key string) []int {
	order := s.HomeNodes(key)
	at := -1
	for i, n := range order {
		if n == coordinator {
			at = i
			break
		}
	}
	if at > 0 {
		order = append(order[at:], order[:at]...)
	}
	if s.Config.Sloppy {
		order = append(order, s.FallbackNodes(key)...)
	}
	return order
}

// FallbackNodes is the ring beyond the home set, in order: where a sloppy quorum spills.
func (s *QuorumStore) FallbackNodes(key string) []int {
	start := floorMod(JavaStringHash(key), len(s.Nodes))
	rest := []int{}
	for i := s.Config.N; i < len(s.Nodes); i++ {
		rest = append(rest, (start+i)%len(s.Nodes))
	}
	return rest
}

// Put writes through a coordinator. Each home replica that answers stores the version; with sloppy quorums, each home
// replica that does not answer is replaced by the next ring node, which stores the value plus a hint for the absentee.
// Success means at least W acks. Nothing is rolled back on failure: some replicas may hold the value anyway.
func (s *QuorumStore) Put(coordinator int, key, value string) (int, error) {
	s.Writes++
	s.clock++
	v := Versioned{value, s.clock, coordinator}
	acks := 0
	acked, missed := []int{}, []int{}
	for _, node := range s.HomeNodes(key) {
		if node == coordinator || s.Network.Reachable(coordinator, node) {
			s.Nodes[node].Put(key, v)
			acks++
			acked = append(acked, node)
		} else {
			missed = append(missed, node)
		}
	}
	if s.Config.Sloppy && len(missed) > 0 {
		spare := s.FallbackNodes(key)
		si := 0
		for _, absent := range missed {
			for si < len(spare) {
				candidate := spare[si]
				si++
				if candidate == coordinator || s.Network.Reachable(coordinator, candidate) {
					s.Nodes[candidate].Put(key, v)
					s.Nodes[candidate].StoreHint(absent, key, v)
					s.HintedWrites++
					acks++
					acked = append(acked, candidate)
					break
				}
			}
		}
	}
	if acks < s.Config.W {
		s.WriteFailures++
		s.Events = append(s.Events, fmt.Sprintf("t=%d PUT %s=%s via n%d FAILED: %d acks < W=%d", s.clock, key, value, coordinator, acks, s.Config.W))
		return acks, fmt.Errorf("%w: write %s got %d acks, needs W=%d", ErrQuorumNotReached, key, acks, s.Config.W)
	}
	missedNote := ""
	if len(missed) > 0 {
		missedNote = " (missed " + fmtList(missed) + ")"
	}
	s.Events = append(s.Events, fmt.Sprintf("t=%d PUT %s=%s via n%d acked by %s%s", s.clock, key, value, coordinator, fmtList(acked), missedNote))
	return acks, nil
}

// Get reads through a coordinator: ask the home replicas (and, for a sloppy quorum, the fallback ring after them), take the
// first R answers, return the newest version. With read repair, push the winner back to every responder that returned
// something older.
func (s *QuorumStore) Get(coordinator int, key string) (ReadResult, error) {
	s.Reads++
	winner := None
	respondents := []int{}
	seen := map[int]Versioned{}
	for _, node := range s.ContactOrder(coordinator, key) {
		if len(respondents) >= s.Config.R {
			break
		}
		if node == coordinator || s.Network.Reachable(coordinator, node) {
			v := s.Nodes[node].Get(key)
			respondents = append(respondents, node)
			seen[node] = v
			if v.NewerThan(winner) {
				winner = v
			}
		}
	}
	if len(respondents) < s.Config.R {
		s.ReadFailures++
		return ReadResult{}, fmt.Errorf("%w: read %s got %d responses, needs R=%d", ErrQuorumNotReached, key, len(respondents), s.Config.R)
	}
	repaired := 0
	if s.Config.ReadRepair && !winner.IsNone() {
		for _, node := range respondents {
			if winner.NewerThan(seen[node]) && s.Nodes[node].Put(key, winner) {
				s.Nodes[node].RepairsApplied++
				repaired++
			}
		}
		s.Repairs += repaired
	}
	return ReadResult{winner, len(respondents), respondents, repaired}, nil
}

// Handoff is hinted handoff: every node that holds hints for a now-reachable target delivers them. Returns how many were delivered.
func (s *QuorumStore) Handoff() int {
	delivered := 0
	for _, holder := range s.Nodes {
		targets := make([]int, 0, len(holder.Hints))
		for target := range holder.Hints {
			targets = append(targets, target)
		}
		sort.Ints(targets) // Java iterates the TreeMap in key order; the RNG is consumed in that order
		for _, target := range targets {
			if !s.Network.Reachable(holder.ID, target) {
				continue
			}
			pending := holder.Hints[target]
			keys := make([]string, 0, len(pending))
			for k := range pending {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				s.Nodes[target].Put(k, pending[k])
				delivered++
			}
			holder.HintsDelivered += len(pending)
			delete(holder.Hints, target)
		}
	}
	if delivered > 0 {
		s.Events = append(s.Events, fmt.Sprintf("t=%d hinted handoff delivered %d value(s)", s.clock, delivered))
	}
	return delivered
}

// AntiEntropy: for every key, converge all reachable home replicas to the newest version. Production systems compare
// Merkle trees per key range first so that agreeing ranges cost one hash exchange; here every key is visited.
// Returns the number of replica updates.
func (s *QuorumStore) AntiEntropy() int {
	updates := 0
	keySet := map[string]bool{}
	for _, n := range s.Nodes {
		for k := range n.Data {
			keySet[k] = true
		}
	}
	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		home := s.HomeNodes(key)
		newest := None
		for _, node := range home {
			if !s.Network.IsDown(node) {
				if v := s.Nodes[node].current(key); v.NewerThan(newest) {
					newest = v
				}
			}
		}
		if newest.IsNone() {
			continue
		}
		for _, node := range home {
			if !s.Network.IsDown(node) && s.Nodes[node].Put(key, newest) {
				updates++
			}
		}
	}
	if updates > 0 {
		s.Events = append(s.Events, fmt.Sprintf("t=%d anti-entropy updated %d replica value(s)", s.clock, updates))
	}
	return updates
}

// ReplicasHolding counts how many of the home replicas of `key` hold `value` as their current version.
func (s *QuorumStore) ReplicasHolding(key, value string) int {
	count := 0
	for _, node := range s.HomeNodes(key) {
		if v := s.Nodes[node].current(key); !v.IsNone() && v.Value == value {
			count++
		}
	}
	return count
}

// ReplicasAgree: do all live home replicas of `key` hold the same version? The convergence check anti-entropy is trying to make true.
func (s *QuorumStore) ReplicasAgree(key string) bool {
	versions := map[Versioned]bool{}
	for _, node := range s.HomeNodes(key) {
		if !s.Network.IsDown(node) {
			versions[s.Nodes[node].current(key)] = true
		}
	}
	return len(versions) <= 1
}

func (s *QuorumStore) ReplicaView(key string) string {
	var sb strings.Builder
	for _, node := range s.HomeNodes(key) {
		fmt.Fprintf(&sb, " n%d=%s", node, s.Nodes[node].current(key))
		if s.Network.IsDown(node) {
			sb.WriteString("(down)")
		}
	}
	return strings.TrimSpace(sb.String())
}

// Outcome is one experiment's tally.
type Outcome struct {
	Writes, WriteFailures, ReadFailures, StaleReads, HintedWrites, Repairs int
}

func (o Outcome) StaleRate() float64 {
	ok := o.Writes - o.WriteFailures - o.ReadFailures
	if ok == 0 {
		return 0
	}
	return float64(o.StaleReads) / float64(ok)
}

// RunExperiment runs the one experiment that matters: write, then read from a different coordinator, and count how often
// the read misses the latest write. Reports the stale rate for one configuration under crashes and drops.
func RunExperiment(config QuorumConfig, totalNodes int, seed int64, dropRate float64, operations, crashEvery int) Outcome {
	store := MustStore(totalNodes, config, seed, dropRate)
	rng := NewRng(seed*31 + 7)
	stale, writeFailures, readFailures := 0, 0, 0
	for i := 1; i <= operations; i++ {
		key := fmt.Sprintf("k%d", rng.NextInt(5))
		value := fmt.Sprintf("v%d", i)
		writer := rng.NextInt(totalNodes)
		reader := rng.NextInt(totalNodes)
		if crashEvery > 0 && i%crashEvery == 0 {
			victim := rng.NextInt(totalNodes)
			downCount := 0
			for n := 0; n < totalNodes; n++ {
				if store.Network.IsDown(n) {
					downCount++
				}
			}
			if store.Network.IsDown(victim) {
				store.Network.Recover(victim)
				store.Handoff()
			} else if downCount < 2 {
				store.Network.Crash(victim)
			}
		}
		if _, err := store.Put(writer, key, value); err != nil {
			writeFailures++
			continue
		}
		r, err := store.Get(reader, key)
		if err != nil {
			readFailures++
			continue
		}
		if got, ok := r.Value(); !ok || got != value {
			stale++
		}
	}
	return Outcome{operations, writeFailures, readFailures, stale, store.HintedWrites, store.Repairs}
}

func section(title string) {
	fmt.Println()
	fmt.Println("== " + title + " ==")
}

// must unwraps a (T, error) whose error is a programming mistake in the demo script.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func val(r ReadResult) string {
	v, ok := r.Value()
	if !ok {
		return "null"
	}
	return v
}

func main() {
	fmt.Println("== R + W > N: a strict quorum read always overlaps the last quorum write (N=3, R=2, W=2, node 1 crashed) ==")
	s := MustStore(5, MustConfig(3, 2, 2, false, false), 1, 0.0)
	key := "merchant:m-77:settlement-currency"
	fmt.Printf("home replicas of %s: %s; fallback ring: %s\n", key, fmtList(s.HomeNodes(key)), fmtList(s.FallbackNodes(key)))
	crashed := s.HomeNodes(key)[1]
	s.Network.Crash(crashed)
	must(s.Put(s.HomeNodes(key)[0], key, "EUR"))
	r := must(s.Get(s.HomeNodes(key)[2], key))
	fmt.Println(s.Events[len(s.Events)-1])
	fmt.Printf("read from a different coordinator with R=2 saw %s from %s  (replicas: %s)\n", r.Winner, fmtList(r.Respondents), s.ReplicaView(key))
	fmt.Printf("the write set {%d,%d} and the read set %s share at least one node: 2 + 2 > 3\n", s.HomeNodes(key)[0], s.HomeNodes(key)[2], fmtList(r.Respondents))

	section("R + W <= N: the quorums can miss each other (N=3, R=1, W=1), one node briefly unreachable")
	weak := MustStore(5, MustConfig(3, 1, 1, false, false), 1, 0.0)
	wh := weak.HomeNodes(key)
	must(weak.Put(wh[0], key, "EUR"))
	weak.Network.Crash(wh[2])
	must(weak.Put(wh[1], key, "GBP"))
	weak.Network.Recover(wh[2])
	w1, w2, w3 := must(weak.Get(wh[0], key)), must(weak.Get(wh[1], key)), must(weak.Get(wh[2], key))
	fmt.Printf("PUT EUR; node %d unreachable during PUT GBP (1 ack suffices), then back: %s\n", wh[2], weak.ReplicaView(key))
	fmt.Printf("R=1 reads via n%d, n%d, n%d return: %s, %s, %s  <- every node is healthy and one of them is wrong\n", wh[0], wh[1], wh[2], val(w1), val(w2), val(w3))

	section("Sloppy quorum: the write reaches W nodes, but not the home set, so a later strict-looking read can miss it")
	sloppy := MustStore(5, MustConfig(3, 2, 2, true, false), 1, 0.0)
	home := sloppy.HomeNodes(key)
	sloppy.Network.Crash(home[1])
	sloppy.Network.Crash(home[2])
	must(sloppy.Put(home[0], key, "EUR"))
	fmt.Println(sloppy.Events[len(sloppy.Events)-1] + "  <- 2 acks reached W=2, one of them is a hint on a non-home node")
	fmt.Printf("replicas now: %s; hints held: %d\n", sloppy.ReplicaView(key), sloppy.Nodes[sloppy.FallbackNodes(key)[0]].HintCount())
	sloppy.Network.Recover(home[1])
	sloppy.Network.Recover(home[2])
	sloppy.Network.Crash(home[0])
	stale := must(sloppy.Get(home[1], key))
	fmt.Printf("home nodes %d,%d recover, node %d crashes; R=2 read sees %s from %s  <- R + W > N held and the read is still stale\n", home[1], home[2], home[0], stale.Winner, fmtList(stale.Respondents))
	delivered := sloppy.Handoff()
	fixed := must(sloppy.Get(home[1], key))
	fmt.Printf("hinted handoff delivers %d value(s); the same read now sees %s  (replicas: %s)\n", delivered, fixed.Winner, sloppy.ReplicaView(key))

	section("Read repair: a stale replica is fixed by the next quorum read that notices it")
	rr := MustStore(5, MustConfig(3, 2, 2, false, true), 1, 0.0)
	rr.Network.Crash(home[2])
	must(rr.Put(home[0], key, "EUR"))
	rr.Network.Recover(home[2])
	fmt.Printf("after a write with node %d down and back: %s\n", home[2], rr.ReplicaView(key))
	rr.Network.Crash(home[0])
	repairRead := must(rr.Get(home[1], key))
	fmt.Printf("R=2 read via n%d (node %d now down) saw %s from %s, repaired %d replica -> %s\n", home[1], home[0], repairRead.Winner, fmtList(repairRead.Respondents), repairRead.Repaired, rr.ReplicaView(key))

	section("Last-writer-wins: two concurrent writers, one of them silently loses, and why a balance must never be a register")
	lww := MustStore(5, MustConfig(3, 2, 2, false, true), 1, 0.0)
	bal := "wallet:w-9:balance"
	must(lww.Put(0, bal, "100.00 USD"))
	seenByA, seenByB := must(lww.Get(1, bal)), must(lww.Get(3, bal))
	fmt.Printf("coordinator A reads %s, coordinator B reads %s; A applies a 30.00 USD debit, B applies a 50.00 USD credit\n", val(seenByA), val(seenByB))
	acksA := must(lww.Put(1, bal, "70.00 USD"))
	fmt.Printf("A's write lands with %d acks: %s\n", acksA, lww.ReplicaView(bal))
	acksB := must(lww.Put(3, bal, "150.00 USD"))
	fmt.Printf("B's write lands with %d acks one tick later and both clients receive success: %s\n", acksB, lww.ReplicaView(bal))
	after := must(lww.Get(lww.HomeNodes(bal)[2], bal))
	lww.AntiEntropy()
	fmt.Printf("quorum read returns %s; after anti-entropy every replica holds %s\n", after.Winner, lww.ReplicaView(bal))
	fmt.Println("the 30.00 USD debit vanished with no error anywhere: LWW chose by timestamp, not by meaning. Lesson 08 (CRDTs) and Phase 10 (ledgers) are the answer.")

	section("Anti-entropy converges replicas that no read ever touched")
	ae := MustStore(5, MustConfig(3, 2, 2, true, false), 1, 0.0)
	ae.Network.Crash(2)
	for i := 0; i < 8; i++ {
		coord := i % 5
		if coord == 2 {
			coord = 0
		}
		must(ae.Put(coord, fmt.Sprintf("cfg:%d", i), fmt.Sprintf("value%d", i)))
	}
	ae.Network.Recover(2)
	before := 0
	for i := 0; i < 8; i++ {
		if ae.ReplicasAgree(fmt.Sprintf("cfg:%d", i)) {
			before++
		}
	}
	updates := ae.AntiEntropy()
	afterCount := 0
	for i := 0; i < 8; i++ {
		if ae.ReplicasAgree(fmt.Sprintf("cfg:%d", i)) {
			afterCount++
		}
	}
	fmt.Printf("node 2 missed its share of 8 writes; keys whose home replicas agree before: %d/8, anti-entropy applied %d update(s), after: %d/8\n", before, updates, afterCount)

	section("20 seeds x 400 write-then-read pairs, 5 nodes, 5% drops, a crash or recovery every 25 ops: stale reads by configuration")
	configs := []QuorumConfig{
		MustConfig(3, 1, 1, false, false), MustConfig(3, 1, 3, false, false), MustConfig(3, 2, 2, false, false),
		MustConfig(3, 2, 2, false, true), MustConfig(3, 2, 2, true, false), MustConfig(3, 3, 1, false, false),
	}
	fmt.Printf("%-34s %8s %10s %10s %7s %7s\n", "config", "stale", "writeFail", "readFail", "hinted", "repair")
	for _, cfg := range configs {
		stale2, wf, rf, hinted, repairsTotal := 0, 0, 0, 0, 0
		for seed := int64(1); seed <= 20; seed++ {
			o := RunExperiment(cfg, 5, seed, 0.05, 400, 25)
			stale2 += o.StaleReads
			wf += o.WriteFailures
			rf += o.ReadFailures
			hinted += o.HintedWrites
			repairsTotal += o.Repairs
		}
		fmt.Printf("%-34s %8d %10d %10d %7d %7d\n", cfg, stale2, wf, rf, hinted, repairsTotal)
	}
	fmt.Println("R + W > N buys zero stale reads only when every ack came from a home replica; sloppy quorums trade that for availability.")
}
