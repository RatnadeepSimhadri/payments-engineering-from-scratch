// Lesson: phases/08-messaging-and-streams/02-consumer-groups-and-rebalancing/docs/en.md
// Consumer Groups, Offsets, and Rebalancing
// Run:  go run .
// Test: go test ./...
// Sources: Apache Kafka documentation, section 4.5 "Consumer group protocol"; KIP-54 (sticky assignor); KIP-429
// (incremental cooperative rebalancing); Kafka Consumer configs session.timeout.ms / heartbeat.interval.ms.
// Go 1.22+, standard library only. Same design as the Java and Python versions. Time is injected through
// ManualClock; nothing sleeps.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ManualClock is the injected time source. Tests and the demo advance it by hand.
type ManualClock struct {
	now int64
}

func NewManualClock(startMillis int64) *ManualClock { return &ManualClock{now: startMillis} }

func (c *ManualClock) Now() int64 { return c.now }

// Advance moves the clock forward; a negative step is a programming error.
func (c *ManualClock) Advance(millis int64) {
	if millis < 0 {
		panic("clock only moves forward")
	}
	c.now += millis
}

// Assignment is what a member receives after a successful join: the generation it belongs to and the partitions it owns.
type Assignment struct {
	Generation int
	Partitions []int
}

// HeartbeatResult is the answer to a heartbeat. A member that sees anything but OK must stop processing and act.
type HeartbeatResult int

const (
	OK HeartbeatResult = iota
	RebalanceInProgress
	UnknownMember
)

func (r HeartbeatResult) String() string {
	switch r {
	case OK:
		return "OK"
	case RebalanceInProgress:
		return "REBALANCE_IN_PROGRESS"
	default:
		return "UNKNOWN_MEMBER"
	}
}

var (
	// ErrUnknownMember: the member is not (or no longer) in the group.
	ErrUnknownMember = errors.New("unknown member")
	// ErrIllegalGeneration: the member's generation is older than the group's.
	ErrIllegalGeneration = errors.New("illegal generation")
	// ErrNotOwner: the member does not own the partition it is committing.
	ErrNotOwner = errors.New("not owner")
)

func unknownMember(memberID string) error {
	return fmt.Errorf("%w %s", ErrUnknownMember, memberID)
}

func illegalGeneration(memberID string, sent, current int) error {
	return fmt.Errorf("%w: member %s sent generation %d but the group is at %d", ErrIllegalGeneration, memberID, sent, current)
}

func notOwner(memberID string, partition int, owner string) error {
	return fmt.Errorf("%w: member %s does not own partition %d (owner: %s)", ErrNotOwner, memberID, partition, owner)
}

// Assignor is a partition assignment strategy. Input: the sorted member ids, the partition count, and the previous
// assignment (empty on the first generation). Output: member id -> sorted partitions. Deterministic by construction:
// the same inputs produce the same map on every coordinator, every language, every run.
type Assignor interface {
	Name() string
	Assign(members []string, partitionCount int, previous map[string][]int) map[string][]int
}

// Moved is the number of partitions whose owner changed between two assignments (newly created partitions do not count).
func Moved(before, after map[string][]int) int {
	ownerBefore := Owners(before)
	ownerAfter := Owners(after)
	moved := 0
	for p, owner := range ownerBefore {
		if ownerAfter[p] != owner {
			moved++
		}
	}
	return moved
}

// Owners inverts an assignment: partition -> member. A partition assigned twice is a programming error.
func Owners(assignment map[string][]int) map[int]string {
	owners := map[int]string{}
	for member, ps := range assignment {
		for _, p := range ps {
			if _, dup := owners[p]; dup {
				panic(fmt.Sprintf("partition %d assigned twice", p))
			}
			owners[p] = member
		}
	}
	return owners
}

// RangeAssignor hands out contiguous ranges: the first (partitions mod members) members receive one extra partition.
// Kafka's default before 2.4.
type RangeAssignor struct{}

func (RangeAssignor) Name() string { return "range" }

func (RangeAssignor) Assign(members []string, partitionCount int, previous map[string][]int) map[string][]int {
	out := map[string][]int{}
	if len(members) == 0 {
		return out
	}
	perMember := partitionCount / len(members)
	extra := partitionCount % len(members)
	next := 0
	for i, m := range members {
		count := perMember
		if i < extra {
			count++
		}
		mine := []int{}
		for k := 0; k < count; k++ {
			mine = append(mine, next)
			next++
		}
		out[m] = mine
	}
	return out
}

// RoundRobinAssignor deals partitions like cards: partition p goes to member p mod n. Balanced, but a membership
// change moves almost everything.
type RoundRobinAssignor struct{}

func (RoundRobinAssignor) Name() string { return "round-robin" }

func (RoundRobinAssignor) Assign(members []string, partitionCount int, previous map[string][]int) map[string][]int {
	out := map[string][]int{}
	if len(members) == 0 {
		return out
	}
	for _, m := range members {
		out[m] = []int{}
	}
	for p := 0; p < partitionCount; p++ {
		m := members[p%len(members)]
		out[m] = append(out[m], p)
	}
	return out
}

// StickyAssignor keeps what survivors already own, hands orphaned partitions to the least-loaded member, then moves
// single partitions from the most-loaded to the least-loaded member until the spread is at most one. Minimises
// movement (KIP-54).
type StickyAssignor struct{}

func (StickyAssignor) Name() string { return "sticky" }

func (StickyAssignor) Assign(members []string, partitionCount int, previous map[string][]int) map[string][]int {
	out := map[string][]int{}
	if len(members) == 0 {
		return out
	}
	for _, m := range members {
		out[m] = []int{}
	}
	unassigned := map[int]bool{}
	for p := 0; p < partitionCount; p++ {
		unassigned[p] = true
	}
	// 1. survivors keep their partitions
	for _, m := range members {
		for _, p := range previous[m] {
			if p < partitionCount && unassigned[p] {
				delete(unassigned, p)
				out[m] = append(out[m], p)
			}
		}
	}
	// 2. orphans go to whoever has the fewest (ties: lowest member id, because members are sorted); ascending order
	orphans := make([]int, 0, len(unassigned))
	for p := range unassigned {
		orphans = append(orphans, p)
	}
	sort.Ints(orphans)
	for _, p := range orphans {
		least := leastLoaded(members, out)
		out[least] = append(out[least], p)
	}
	// 3. level: move one partition at a time from the heaviest to the lightest while the spread exceeds one
	for {
		most, least := mostLoaded(members, out), leastLoaded(members, out)
		if len(out[most])-len(out[least]) <= 1 {
			break
		}
		from := out[most]
		p := from[len(from)-1]
		out[most] = from[:len(from)-1]
		out[least] = append(out[least], p)
	}
	for _, l := range out {
		sort.Ints(l)
	}
	return out
}

func leastLoaded(members []string, out map[string][]int) string {
	best := members[0]
	for _, m := range members {
		if len(out[m]) < len(out[best]) {
			best = m
		}
	}
	return best
}

func mostLoaded(members []string, out map[string][]int) string {
	best := members[0]
	for _, m := range members {
		if len(out[m]) > len(out[best]) {
			best = m
		}
	}
	return best
}

// GroupCoordinator is the group coordinator for one topic. It owns membership, liveness (heartbeats against an
// injected clock), the generation counter, the current assignment, and the committed offsets. Every mutation that
// changes membership bumps the generation and recomputes the assignment; commits carrying an older generation are fenced.
type GroupCoordinator struct {
	groupID          string
	partitionCount   int
	assignor         Assignor
	sessionTimeoutMs int64
	clock            *ManualClock

	generation    int
	lastHeartbeat map[string]int64 // memberId -> millis of last contact
	assignment    map[string][]int
	committed     map[int]int64 // partition -> next offset to read
	history       []string      // human-readable rebalance log
}

func NewGroupCoordinator(groupID string, partitionCount int, assignor Assignor, sessionTimeoutMs int64, clock *ManualClock) (*GroupCoordinator, error) {
	if partitionCount <= 0 {
		return nil, errors.New("partitionCount must be positive")
	}
	if sessionTimeoutMs <= 0 {
		return nil, errors.New("sessionTimeoutMs must be positive")
	}
	return &GroupCoordinator{
		groupID:          groupID,
		partitionCount:   partitionCount,
		assignor:         assignor,
		sessionTimeoutMs: sessionTimeoutMs,
		clock:            clock,
		lastHeartbeat:    map[string]int64{},
		assignment:       map[string][]int{},
		committed:        map[int]int64{},
	}, nil
}

func (g *GroupCoordinator) Generation() int { return g.generation }

// Members returns the member ids in sorted order (Java's TreeMap key order).
func (g *GroupCoordinator) Members() []string {
	out := make([]string, 0, len(g.lastHeartbeat))
	for m := range g.lastHeartbeat {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

func (g *GroupCoordinator) History() []string { return append([]string(nil), g.history...) }

// Assignment returns a deep copy of the current assignment.
func (g *GroupCoordinator) Assignment() map[string][]int {
	copied := map[string][]int{}
	for m, ps := range g.assignment {
		copied[m] = append([]int(nil), ps...)
	}
	return copied
}

// Join: a member joins (or re-joins after being told to). Membership changes trigger a rebalance; a re-join does not.
func (g *GroupCoordinator) Join(memberID string) Assignment {
	_, known := g.lastHeartbeat[memberID]
	g.lastHeartbeat[memberID] = g.clock.Now()
	if !known {
		g.rebalance("join of " + memberID)
	}
	return Assignment{g.generation, append([]int{}, g.assignment[memberID]...)}
}

// Leave: a member announces it is leaving. Its partitions are reassigned immediately rather than after the session timeout.
func (g *GroupCoordinator) Leave(memberID string) error {
	if _, known := g.lastHeartbeat[memberID]; !known {
		return unknownMember(memberID)
	}
	delete(g.lastHeartbeat, memberID)
	g.rebalance("leave of " + memberID)
	return nil
}

// Heartbeat from a member that believes it is in memberGeneration. OK refreshes the session; RebalanceInProgress
// tells the member its generation is stale and it must re-join before touching any partition.
func (g *GroupCoordinator) Heartbeat(memberID string, memberGeneration int) HeartbeatResult {
	if _, known := g.lastHeartbeat[memberID]; !known {
		return UnknownMember
	}
	g.lastHeartbeat[memberID] = g.clock.Now()
	if memberGeneration == g.generation {
		return OK
	}
	return RebalanceInProgress
}

// ExpireSessions evicts every member whose last heartbeat is older than the session timeout. Called on the
// coordinator's own tick. The evicted ids come back sorted.
func (g *GroupCoordinator) ExpireSessions() []string {
	dead := []string{}
	deadline := g.clock.Now() - g.sessionTimeoutMs
	for _, m := range g.Members() {
		if g.lastHeartbeat[m] <= deadline {
			dead = append(dead, m)
		}
	}
	if len(dead) > 0 {
		for _, m := range dead {
			delete(g.lastHeartbeat, m)
		}
		g.rebalance("session timeout of " + formatStrings(dead))
	}
	return dead
}

func (g *GroupCoordinator) rebalance(reason string) {
	previous := g.assignment
	g.assignment = g.assignor.Assign(g.Members(), g.partitionCount, previous)
	g.generation++
	g.history = append(g.history, fmt.Sprintf("generation %d (%s): %s moved=%d",
		g.generation, reason, FormatAssignment(g.assignment), Moved(previous, g.assignment)))
}

// OwnerOf is the member that owns partition in the current generation, or "" when the group is empty.
func (g *GroupCoordinator) OwnerOf(partition int) (string, error) {
	if partition < 0 || partition >= g.partitionCount {
		return "", fmt.Errorf("no partition %d", partition)
	}
	return Owners(g.assignment)[partition], nil
}

// CommitOffset commits the next offset to read for a partition. Fenced three ways: the member must be known, must be
// in the current generation, and must own the partition. A zombie that lost its partitions cannot overwrite the new
// owner's progress.
func (g *GroupCoordinator) CommitOffset(memberID string, memberGeneration, partition int, nextOffset int64) error {
	if _, known := g.lastHeartbeat[memberID]; !known {
		return unknownMember(memberID)
	}
	if memberGeneration != g.generation {
		return illegalGeneration(memberID, memberGeneration, g.generation)
	}
	owner, err := g.OwnerOf(partition)
	if err != nil {
		return err
	}
	if memberID != owner {
		return notOwner(memberID, partition, owner)
	}
	if nextOffset < 0 {
		return errors.New("offset must be non-negative")
	}
	g.committed[partition] = nextOffset
	return nil
}

// CommittedOffset is the committed next-offset-to-read for a partition, or 0 when nothing has been committed yet.
func (g *GroupCoordinator) CommittedOffset(partition int) int64 { return g.committed[partition] }

func (g *GroupCoordinator) GroupID() string { return g.groupID }

// FormatAssignment renders an assignment in sorted member order, like Java's LinkedHashMap of sorted members.
func FormatAssignment(a map[string][]int) string {
	members := make([]string, 0, len(a))
	for m := range a {
		members = append(members, m)
	}
	sort.Strings(members)
	parts := make([]string, 0, len(members))
	for _, m := range members {
		parts = append(parts, m+"="+formatInts(a[m]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func formatInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func formatStrings(xs []string) string { return "[" + strings.Join(xs, ", ") + "]" }

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func printAssignment(g *GroupCoordinator) {
	fmt.Printf("  generation %d, members %s\n", g.Generation(), formatStrings(g.Members()))
	a := g.Assignment()
	for _, m := range g.Members() {
		fmt.Printf("    %-4s owns %s\n", m, formatInts(a[m]))
	}
}

func main() {
	clock := NewManualClock(1_700_000_000_000)
	const partitions = 8
	const sessionTimeout = 10_000

	fmt.Println("== Three consumers join group 'ledger-poster' (8 partitions, range assignor) ==")
	rng := must(NewGroupCoordinator("ledger-poster", partitions, RangeAssignor{}, sessionTimeout, clock))
	a1 := rng.Join("c1")
	rng.Join("c2")
	a3 := rng.Join("c3")
	printAssignment(rng)
	fmt.Printf("  c1 joined in generation %d and was handed %s; after c2 and c3 the group is at generation %d\n",
		a1.Generation, formatInts(a1.Partitions), rng.Generation())
	fmt.Printf("  c1's heartbeat with generation %d -> %s\n", a1.Generation, rng.Heartbeat("c1", a1.Generation))
	a1 = rng.Join("c1")
	fmt.Printf("  c1 re-joins and now holds generation %d with %s\n", a1.Generation, formatInts(a1.Partitions))

	fmt.Println("\n== c2 stops heartbeating; who owns partition 3? ==")
	clock.Advance(4_000)
	rng.Heartbeat("c1", rng.Generation())
	rng.Heartbeat("c3", a3.Generation)
	fmt.Printf("  t+4s : expired = %s (c2 still inside its 10s session)\n", formatStrings(rng.ExpireSessions()))
	clock.Advance(6_000)
	rng.Heartbeat("c1", rng.Generation())
	rng.Heartbeat("c3", rng.Generation())
	fmt.Printf("  t+10s: expired = %s\n", formatStrings(rng.ExpireSessions()))
	printAssignment(rng)
	owner3 := must(rng.OwnerOf(3))
	fmt.Println("  partition 3 is owned by", owner3)

	fmt.Println("\n== The zombie: c2 comes back from its GC pause and tries to commit partition 3 ==")
	if err := rng.CommitOffset("c2", 3, 3, 1_042); errors.Is(err, ErrUnknownMember) {
		fmt.Println("  rejected:", err)
	}
	must(0, rng.CommitOffset(owner3, rng.Generation(), 3, 1_040))
	fmt.Printf("  new owner committed next offset %d for partition 3\n", rng.CommittedOffset(3))
	zombie := rng.Join("c2")
	fmt.Printf("  c2 re-joins -> generation %d, owns %s\n", zombie.Generation, formatInts(zombie.Partitions))
	if err := rng.CommitOffset("c2", zombie.Generation-1, zombie.Partitions[0], 7); errors.Is(err, ErrIllegalGeneration) {
		fmt.Println("  stale generation rejected:", err)
	}

	fmt.Println("\n== Adding a fourth consumer: how many partitions move? ==")
	for _, assignor := range []Assignor{RangeAssignor{}, RoundRobinAssignor{}, StickyAssignor{}} {
		three := []string{"c1", "c2", "c3"}
		before := assignor.Assign(three, partitions, map[string][]int{})
		after := assignor.Assign([]string{"c1", "c2", "c3", "c4"}, partitions, before)
		fmt.Printf("  %-11s before=%s\n", assignor.Name(), FormatAssignment(before))
		fmt.Printf("  %-11s after =%s moved=%d\n", "", FormatAssignment(after), Moved(before, after))
	}

	fmt.Println("\n== Rebalance history (range group) ==")
	for _, h := range rng.History() {
		fmt.Println("  " + h)
	}
}
