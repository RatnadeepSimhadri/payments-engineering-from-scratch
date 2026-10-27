// Lesson: phases/07-storage-and-transactions/01-partitioning-and-rebalancing/docs/en.md
// Partitioning Strategies and Rebalancing
// Run:  go run .
// Test: go test ./...
// Sources: Karger et al., "Consistent Hashing and Random Trees" (STOC 1997); DeCandia et al., "Dynamo" (SOSP 2007);
// FNV-1a hash (Fowler, Noll, Vo). Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf16"
)

// Partitioner maps a key to a logical partition number in [0, PartitionCount()).
type Partitioner interface {
	PartitionOf(key string) int
	PartitionCount() int
}

// Fnv1a64 is FNV-1a, 64-bit, over the key's UTF-16 code units (what Java's charAt sees; identical to the bytes for ASCII).
// Deterministic across processes and languages, unlike Go's randomised map hash.
func Fnv1a64(key string) int64 {
	hash := uint64(0xcbf29ce484222325)
	for _, unit := range utf16.Encode([]rune(key)) {
		hash ^= uint64(unit)
		hash *= 0x100000001b3
	}
	return int64(hash)
}

// Hash64 applies the SplitMix64 finaliser to FNV-1a: FNV mixes low bits well but leaves the high bits of short keys correlated.
func Hash64(key string) int64 {
	z := uint64(Fnv1a64(key))
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return int64(z ^ (z >> 31))
}

// remainderUnsigned is Long.remainderUnsigned: the hash is treated as a 64-bit unsigned value.
func remainderUnsigned(hash int64, n int) int {
	return int(uint64(hash) % uint64(n))
}

// Sentinel errors for the argument and state checks the Java version throws as exceptions.
var (
	ErrSplitPointsUnsorted = errors.New("split points must be strictly increasing")
	ErrPartitionCount      = errors.New("partition count must be positive")
	ErrVirtualNodes        = errors.New("virtual nodes must be positive")
	ErrNoNodes             = errors.New("need at least one node")
	ErrNodePresent         = errors.New("node already present")
	ErrNodeAbsent          = errors.New("node not present")
	ErrLastNode            = errors.New("cannot remove the last node")
	ErrNotOwner            = errors.New("source does not own partition")
	ErrKeyNotInPartition   = errors.New("key not in partition")
	ErrWrongState          = errors.New("migration is in the wrong state")
)

// RangePartitioner: partition i holds keys in [splitPoints[i-1], splitPoints[i]). Preserves sort order, invites hot ranges.
type RangePartitioner struct {
	splitPoints []string
}

func NewRangePartitioner(splitPoints []string) (*RangePartitioner, error) {
	for i := 1; i < len(splitPoints); i++ {
		if splitPoints[i-1] >= splitPoints[i] {
			return nil, ErrSplitPointsUnsorted
		}
	}
	copied := make([]string, len(splitPoints))
	copy(copied, splitPoints)
	return &RangePartitioner{splitPoints: copied}, nil
}

func (r *RangePartitioner) PartitionOf(key string) int {
	lo, hi := 0, len(r.splitPoints) // find the number of split points <= key
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if r.splitPoints[mid] <= key {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

func (r *RangePartitioner) PartitionCount() int { return len(r.splitPoints) + 1 }

func (r *RangePartitioner) SplitPoints() []string { return r.splitPoints }

// HashPartitioner: hash of the key modulo a FIXED partition count. Spreads keys uniformly; destroys range scans.
type HashPartitioner struct {
	partitions int
}

func NewHashPartitioner(partitions int) (*HashPartitioner, error) {
	if partitions <= 0 {
		return nil, ErrPartitionCount
	}
	return &HashPartitioner{partitions: partitions}, nil
}

func (h *HashPartitioner) PartitionOf(key string) int {
	return remainderUnsigned(Hash64(key), h.partitions)
}

func (h *HashPartitioner) PartitionCount() int { return h.partitions }

// ConsistentHashRing: nodes own arcs of a 64-bit ring; adding a node moves only the keys on the arcs it takes.
// The ring is a sorted slice of signed hashes plus a map, standing in for Java's TreeMap<Long, String>.
type ConsistentHashRing struct {
	points       []int64 // sorted ascending (signed, like Java's TreeMap<Long>)
	owner        map[int64]string
	virtualNodes int
}

func NewConsistentHashRing(virtualNodes int) (*ConsistentHashRing, error) {
	if virtualNodes <= 0 {
		return nil, ErrVirtualNodes
	}
	return &ConsistentHashRing{owner: map[int64]string{}, virtualNodes: virtualNodes}, nil
}

func (r *ConsistentHashRing) AddNode(node string) {
	for v := 0; v < r.virtualNodes; v++ {
		h := Hash64(fmt.Sprintf("%s#%d", node, v))
		if _, present := r.owner[h]; !present {
			i := sort.Search(len(r.points), func(i int) bool { return r.points[i] >= h })
			r.points = append(r.points, 0)
			copy(r.points[i+1:], r.points[i:])
			r.points[i] = h
		}
		r.owner[h] = node
	}
}

func (r *ConsistentHashRing) RemoveNode(node string) {
	for v := 0; v < r.virtualNodes; v++ {
		h := Hash64(fmt.Sprintf("%s#%d", node, v))
		if _, present := r.owner[h]; !present {
			continue
		}
		delete(r.owner, h)
		i := sort.Search(len(r.points), func(i int) bool { return r.points[i] >= h })
		r.points = append(r.points[:i], r.points[i+1:]...)
	}
}

// NodeFor returns the first node clockwise from the key's hash. Panics on an empty ring: that is a programming error,
// exactly like Java's IllegalStateException here.
func (r *ConsistentHashRing) NodeFor(key string) string {
	if len(r.points) == 0 {
		panic("ring has no nodes")
	}
	h := Hash64(key)
	i := sort.Search(len(r.points), func(i int) bool { return r.points[i] >= h }) // TreeMap.ceilingEntry
	if i == len(r.points) {
		i = 0 // wrap to firstEntry
	}
	return r.owner[r.points[i]]
}

func (r *ConsistentHashRing) Size() int { return len(r.points) }

// RoutingTable is a versioned partition -> node map. Every change bumps the version so a client holding an old copy
// can be told exactly that: "your table is stale, refresh". Fixed partition count, so rebalancing moves whole partitions.
type RoutingTable struct {
	partitioner Partitioner
	owner       []string
	version     int64
}

func NewRoutingTable(partitioner Partitioner, nodes []string) (*RoutingTable, error) {
	if len(nodes) == 0 {
		return nil, ErrNoNodes
	}
	owner := make([]string, partitioner.PartitionCount())
	for p := range owner {
		owner[p] = nodes[p%len(nodes)] // round-robin initial layout
	}
	return &RoutingTable{partitioner: partitioner, owner: owner, version: 1}, nil
}

func (t *RoutingTable) Version() int64 { return t.version }

func (t *RoutingTable) Partitioner() Partitioner { return t.partitioner }

func (t *RoutingTable) PartitionCount() int { return len(t.owner) }

func (t *RoutingTable) OwnerOf(partition int) string { return t.owner[partition] }

func (t *RoutingTable) NodeFor(key string) string { return t.owner[t.partitioner.PartitionOf(key)] }

func (t *RoutingTable) Assign(partition int, node string) {
	if node != t.owner[partition] {
		t.owner[partition] = node
		t.version++
	}
}

// Nodes returns the distinct owners, sorted.
func (t *RoutingTable) Nodes() []string {
	var out []string
	for _, n := range t.owner {
		if !contains(out, n) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (t *RoutingTable) PartitionsPerNode() map[string]int {
	counts := map[string]int{}
	for _, n := range t.owner {
		counts[n]++
	}
	return counts
}

// PartitionsOf lists the partitions currently owned by node, in ascending order.
func (t *RoutingTable) PartitionsOf(node string) []int {
	out := []int{}
	for p, n := range t.owner {
		if n == node {
			out = append(out, p)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// formatLayout prints a partitions-per-node map the way Java's TreeMap prints: {a=13, b=13, ...} sorted by node.
func formatLayout(counts map[string]int) string {
	nodes := make([]string, 0, len(counts))
	for n := range counts {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	parts := make([]string, len(nodes))
	for i, n := range nodes {
		parts[i] = fmt.Sprintf("%s=%d", n, counts[n])
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// Move is one partition handed from one node to another.
type Move struct {
	Partition int
	From      string
	To        string
}

// ownedLoad is the LinkedHashMap<String, List<Integer>> of the Java rebalancer: insertion-ordered node -> partitions.
type ownedLoad struct {
	order []string
	parts map[string][]int
}

func newOwnedLoad() *ownedLoad { return &ownedLoad{parts: map[string][]int{}} }

func (o *ownedLoad) put(node string, partitions []int) {
	if _, present := o.parts[node]; !present {
		o.order = append(o.order, node)
	}
	o.parts[node] = partitions
}

func (o *ownedLoad) mostLoaded() string {
	best := ""
	for i, n := range o.order {
		if i == 0 || len(o.parts[n]) > len(o.parts[best]) {
			best = n
		}
	}
	return best
}

func (o *ownedLoad) leastLoaded() string {
	best := ""
	for i, n := range o.order {
		if i == 0 || len(o.parts[n]) < len(o.parts[best]) {
			best = n
		}
	}
	return best
}

// Rebalancer plans the minimum set of partition moves that makes a routing table balanced after a node joins or leaves.
type Rebalancer struct{}

// PlanAddNode: add node; repeatedly take one partition from the most loaded node until no node has 2+ more than any other.
func (Rebalancer) PlanAddNode(table *RoutingTable, newNode string) ([]Move, error) {
	if contains(table.Nodes(), newNode) {
		return nil, fmt.Errorf("%w: %s", ErrNodePresent, newNode)
	}
	moves := []Move{}
	owned := newOwnedLoad()
	for _, n := range table.Nodes() {
		owned.put(n, table.PartitionsOf(n))
	}
	owned.put(newNode, []int{})
	nodes := len(owned.order)
	target := table.PartitionCount() / nodes // the new node gets floor(P / N)
	for len(owned.parts[newNode]) < target {
		donor := owned.mostLoaded()
		donorParts := owned.parts[donor]
		partition := donorParts[len(donorParts)-1] // take the highest-numbered partition: deterministic
		owned.parts[donor] = donorParts[:len(donorParts)-1]
		owned.parts[newNode] = append(owned.parts[newNode], partition)
		moves = append(moves, Move{partition, donor, newNode})
	}
	return moves, nil
}

// PlanRemoveNode: remove node; hand each of its partitions to the currently least loaded survivor.
func (Rebalancer) PlanRemoveNode(table *RoutingTable, leaving string) ([]Move, error) {
	if !contains(table.Nodes(), leaving) {
		return nil, fmt.Errorf("%w: %s", ErrNodeAbsent, leaving)
	}
	if len(table.Nodes()) == 1 {
		return nil, ErrLastNode
	}
	moves := []Move{}
	owned := newOwnedLoad()
	for _, n := range table.Nodes() {
		if n != leaving {
			owned.put(n, table.PartitionsOf(n))
		}
	}
	for _, partition := range table.PartitionsOf(leaving) {
		receiver := owned.leastLoaded()
		owned.parts[receiver] = append(owned.parts[receiver], partition)
		moves = append(moves, Move{partition, leaving, receiver})
	}
	return moves, nil
}

func (Rebalancer) Apply(table *RoutingTable, moves []Move) {
	for _, m := range moves {
		table.Assign(m.Partition, m.To)
	}
}

// HotKeyDetector counts requests per partition and reports skew: how much hotter the hottest partition is than the average.
type HotKeyDetector struct {
	hits  []int64
	total int64
}

func NewHotKeyDetector(partitions int) *HotKeyDetector {
	return &HotKeyDetector{hits: make([]int64, partitions)}
}

func (d *HotKeyDetector) Record(partition int) {
	d.hits[partition]++
	d.total++
}

func (d *HotKeyDetector) HottestPartition() int {
	best := 0
	for p := 1; p < len(d.hits); p++ {
		if d.hits[p] > d.hits[best] {
			best = p
		}
	}
	return best
}

func (d *HotKeyDetector) Hits(partition int) int64 { return d.hits[partition] }

// Skew is max / mean, so 1.0 is perfectly balanced and N means one partition takes everything.
func (d *HotKeyDetector) Skew() float64 {
	if d.total == 0 {
		return 1.0
	}
	mean := float64(d.total) / float64(len(d.hits))
	return float64(d.hits[d.HottestPartition()]) / mean
}

// Salt appends a small salt derived from a secondary field so one logical key spreads over `fanout` partitions.
func Salt(key, secondary string, fanout int) string {
	s := remainderUnsigned(Hash64(secondary), fanout)
	return fmt.Sprintf("%s#%d", key, s)
}

// WrongOwnerError is returned by a node that receives a request for a partition it no longer owns;
// it carries the version the client should fetch. Check with errors.As.
type WrongOwnerError struct {
	Partition      int
	CurrentVersion int64
}

func (e *WrongOwnerError) Error() string {
	return fmt.Sprintf("partition %d not owned here; routing table is now at version %d", e.Partition, e.CurrentVersion)
}

// StorageNode is an in-memory storage node that only answers for partitions the routing table says it owns.
type StorageNode struct {
	Name  string
	data  map[string]string
	table *RoutingTable
}

func NewStorageNode(name string, table *RoutingTable) *StorageNode {
	return &StorageNode{Name: name, data: map[string]string{}, table: table}
}

func (n *StorageNode) Put(key, value string) error {
	if err := n.requireOwner(key); err != nil {
		return err
	}
	n.data[key] = value
	return nil
}

// Get returns the stored value ("" when absent, like Java's null) or a *WrongOwnerError.
func (n *StorageNode) Get(key string) (string, error) {
	if err := n.requireOwner(key); err != nil {
		return "", err
	}
	return n.data[key], nil
}

// PutDirect is used by the migration itself, which legitimately writes into a node that is not yet the owner.
func (n *StorageNode) PutDirect(key, value string) { n.data[key] = value }

func (n *StorageNode) SnapshotOfPartition(partition int) map[string]string {
	out := map[string]string{}
	for k, v := range n.data {
		if n.table.Partitioner().PartitionOf(k) == partition {
			out[k] = v
		}
	}
	return out
}

func (n *StorageNode) DropPartition(partition int) int {
	removed := 0
	for k := range n.data {
		if n.table.Partitioner().PartitionOf(k) == partition {
			delete(n.data, k)
			removed++
		}
	}
	return removed
}

func (n *StorageNode) Size() int { return len(n.data) }

func (n *StorageNode) requireOwner(key string) error {
	p := n.table.Partitioner().PartitionOf(key)
	if n.table.OwnerOf(p) != n.Name {
		return &WrongOwnerError{Partition: p, CurrentVersion: n.table.Version()}
	}
	return nil
}

// MigrationState is the phase of a PartitionMigration.
type MigrationState int

const (
	Planned MigrationState = iota
	Copying
	DualWrite
	CutOver
	Done
)

func (s MigrationState) String() string {
	return [...]string{"PLANNED", "COPYING", "DUAL_WRITE", "CUT_OVER", "DONE"}[s]
}

// PartitionMigration moves one partition from source to destination without refusing writes: bulk copy, dual-write for
// the writes that arrive meanwhile, cutover by bumping the routing table, then drop the old copy.
type PartitionMigration struct {
	Partition   int
	Source      *StorageNode
	Destination *StorageNode
	table       *RoutingTable
	state       MigrationState
	Copied      int
	DualWrites  int
}

func NewPartitionMigration(partition int, source, destination *StorageNode, table *RoutingTable) (*PartitionMigration, error) {
	if table.OwnerOf(partition) != source.Name {
		return nil, fmt.Errorf("%w: %s does not own partition %d", ErrNotOwner, source.Name, partition)
	}
	return &PartitionMigration{Partition: partition, Source: source, Destination: destination, table: table, state: Planned}, nil
}

func (m *PartitionMigration) State() MigrationState { return m.state }

// StartCopy is phase 1: snapshot the partition and copy it. Writes keep going to source only.
func (m *PartitionMigration) StartCopy() error {
	if err := m.requireState(Planned); err != nil {
		return err
	}
	m.state = Copying
	snapshot := m.Source.SnapshotOfPartition(m.Partition)
	keys := make([]string, 0, len(snapshot))
	for k := range snapshot {
		keys = append(keys, k)
	}
	sort.Strings(keys) // TreeMap order in Java; deterministic copy order here
	for _, k := range keys {
		m.Destination.PutDirect(k, snapshot[k])
		m.Copied++
	}
	m.state = DualWrite
	return nil
}

// Write is phase 2: every write to this partition goes to both nodes until cutover. Reads still go to source.
func (m *PartitionMigration) Write(key, value string) error {
	if m.table.Partitioner().PartitionOf(key) != m.Partition {
		return ErrKeyNotInPartition
	}
	switch m.state {
	case DualWrite:
		if err := m.Source.Put(key, value); err != nil {
			return err
		}
		m.Destination.PutDirect(key, value)
		m.DualWrites++
		return nil
	case Planned, Copying:
		return m.Source.Put(key, value)
	default:
		return m.Destination.Put(key, value)
	}
}

// CutOver is phase 3: flip the routing table. From now on source rejects the partition with WrongOwnerError.
func (m *PartitionMigration) CutOver() error {
	if err := m.requireState(DualWrite); err != nil {
		return err
	}
	m.table.Assign(m.Partition, m.Destination.Name)
	m.state = CutOver
	return nil
}

// DropSource is phase 4: once no client can still be mid-write to source, free the old copy.
func (m *PartitionMigration) DropSource() (int, error) {
	if err := m.requireState(CutOver); err != nil {
		return 0, err
	}
	dropped := m.Source.DropPartition(m.Partition)
	m.state = Done
	return dropped, nil
}

func (m *PartitionMigration) requireState(expected MigrationState) error {
	if m.state != expected {
		return fmt.Errorf("%w: expected %s but migration is %s", ErrWrongState, expected, m.state)
	}
	return nil
}

// MovedFraction is the fraction of keys whose owner changes when the layout goes from `before` to `after`.
func MovedFraction(keys []string, before, after func(string) string) float64 {
	moved := 0
	for _, k := range keys {
		if before(k) != after(k) {
			moved++
		}
	}
	return float64(moved) / float64(len(keys))
}

func PaymentIds(n int) []string {
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, fmt.Sprintf("pay_%08d", i))
	}
	return ids
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	keys := PaymentIds(100_000)
	nodes := []string{"node-a", "node-b", "node-c", "node-d"}

	fmt.Println("== Range vs hash: where do merchants land? ==")
	byMerchantPrefix := must(NewRangePartitioner([]string{"f", "m", "s"})) // [..f) [f..m) [m..s) [s..)
	byHash := must(NewHashPartitioner(4))
	for _, m := range []string{"acme", "globex", "megamart", "zenith"} {
		fmt.Printf("  merchant %-9s range-partition=%d hash-partition=%d\n", m, byMerchantPrefix.PartitionOf(m), byHash.PartitionOf(m))
	}

	fmt.Println()
	fmt.Println("== Hot key: one merchant is 60% of traffic ==")
	plain := NewHotKeyDetector(16)
	salted := NewHotKeyDetector(16)
	p16 := must(NewHashPartitioner(16))
	for i := 0; i < 10_000; i++ {
		merchant := fmt.Sprintf("merchant-%d", i%97)
		if i%10 < 6 {
			merchant = "megamart"
		}
		plain.Record(p16.PartitionOf(merchant))
		salted.Record(p16.PartitionOf(Salt(merchant, fmt.Sprintf("pay_%d", i), 8)))
	}
	fmt.Printf("  unsalted: hottest partition %d takes %d of 10000 (skew %.1fx)\n",
		plain.HottestPartition(), plain.Hits(plain.HottestPartition()), plain.Skew())
	fmt.Printf("  salted x8: hottest partition %d takes %d of 10000 (skew %.1fx); reads for megamart now fan out to 8 partitions\n",
		salted.HottestPartition(), salted.Hits(salted.HottestPartition()), salted.Skew())

	fmt.Println()
	fmt.Println("== Adding a 5th node: how many keys move? ==")
	mod4 := must(NewHashPartitioner(4))
	mod5 := must(NewHashPartitioner(5))
	naive := MovedFraction(keys,
		func(k string) string { return fmt.Sprintf("n%d", mod4.PartitionOf(k)) },
		func(k string) string { return fmt.Sprintf("n%d", mod5.PartitionOf(k)) })
	fmt.Printf("  hash mod N, N 4 -> 5:               %.1f%% of keys move (almost everything)\n", naive*100)

	fixed := must(NewRoutingTable(must(NewHashPartitioner(64)), nodes))
	ownerBefore := map[string]string{}
	for _, k := range keys {
		ownerBefore[k] = fixed.NodeFor(k)
	}
	var rebalancer Rebalancer
	moves := must(rebalancer.PlanAddNode(fixed, "node-e"))
	rebalancer.Apply(fixed, moves)
	fixedMoved := MovedFraction(keys, func(k string) string { return ownerBefore[k] }, fixed.NodeFor)
	fmt.Printf("  64 fixed partitions, 4 -> 5 nodes:  %.1f%% of keys move (%d partition moves, layout %s)\n",
		fixedMoved*100, len(moves), formatLayout(fixed.PartitionsPerNode()))

	ring := must(NewConsistentHashRing(150))
	for _, n := range nodes {
		ring.AddNode(n)
	}
	ringBefore := map[string]string{}
	for _, k := range keys {
		ringBefore[k] = ring.NodeFor(k)
	}
	ring.AddNode("node-e")
	ringMoved := MovedFraction(keys, func(k string) string { return ringBefore[k] }, ring.NodeFor)
	fmt.Printf("  consistent hashing, 150 vnodes:     %.1f%% of keys move (ideal 1/5 = 20.0%%)\n", ringMoved*100)

	fmt.Println()
	fmt.Println("== Moving partition 6 from node-a to node-e without downtime ==")
	table := must(NewRoutingTable(must(NewHashPartitioner(8)), []string{"node-a", "node-b"}))
	a := NewStorageNode("node-a", table)
	e := NewStorageNode("node-e", table)
	loaded := 0
	for _, k := range keys[:2_000] {
		if table.Partitioner().PartitionOf(k) == 6 {
			check(a.Put(k, "v1"))
			loaded++
		}
	}
	fmt.Printf("  partition 6 has %d keys on node-a; routing table v%d\n", loaded, table.Version())
	mig := must(NewPartitionMigration(6, a, e, table))
	check(mig.StartCopy())
	fmt.Printf("  bulk copy done: %d keys copied, state %s\n", mig.Copied, mig.State())
	live := ""
	for _, k := range keys[2_000:] {
		if table.Partitioner().PartitionOf(k) == 6 {
			live = k
			break
		}
	}
	check(mig.Write(live, "v2-written-during-migration"))
	fmt.Printf("  live write to %s during migration went to both nodes (dual writes: %d)\n", live, mig.DualWrites)
	check(mig.CutOver())
	fmt.Printf("  cut over: routing table v%d, partition 6 owner = %s\n", table.Version(), table.OwnerOf(6))
	if _, err := a.Get(live); err != nil {
		var wrong *WrongOwnerError
		if errors.As(err, &wrong) {
			fmt.Println("  stale client asked node-a: " + wrong.Error())
		}
	}
	fmt.Printf("  node-e serves %s = %s\n", live, must(e.Get(live)))
	fmt.Printf("  dropped %d keys from node-a; migration %s\n", must(mig.DropSource()), mig.State())
}
