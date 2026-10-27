// Lesson: phases/08-messaging-and-streams/04-message-ordering-and-keys/docs/en.md
// Message Ordering, Keys, and Head-of-Line Blocking
// Run:  go run .
// Test: go test ./...
// Sources: Apache Kafka documentation, section 4.4 "Message Delivery Semantics" and producer configs
// max.in.flight.requests.per.connection / enable.idempotence; KIP-98 (idempotent producer sequence numbers).
// Go 1.22+, standard library only. Same design as the Java and Python versions. Time is simulated with a
// discrete-event clock; nothing sleeps or touches the network.
package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Currency is an ISO 4217 code and minor-unit exponent. Copied from Phase 10, Lesson 01; lessons are self-contained.
type Currency struct {
	Code     string
	Exponent int
}

var USD = Currency{"USD", 2}

// Money is integer minor units plus a currency. Never a float.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func (m Money) String() string {
	scale := int64(1)
	for i := 0; i < m.Currency.Exponent; i++ {
		scale *= 10
	}
	abs := m.MinorUnits
	sign := ""
	if abs < 0 {
		abs = -abs
		sign = "-"
	}
	return fmt.Sprintf("%s%d.%0*d %s", sign, abs/scale, m.Currency.Exponent, abs%scale, m.Currency.Code)
}

// ---------------------------------------------------------------------------------------------------------------------
// Records and the partitioner. The partition is a pure function of the key, so one key has one home and one order.
// ---------------------------------------------------------------------------------------------------------------------

// Message is one payment event as the producer hands it to the log. Seq is the producer's per-key sequence number:
// the n-th event the producer emitted for this key. The log does not assign it; the producer does, before any retry.
type Message struct {
	Key        string
	Seq        int64
	Type       string
	Amount     Money
	ProducedAt int64
	CostMillis int64
}

// NewMessage validates what Java's compact constructor validates.
func NewMessage(key string, seq int64, typ string, amount Money, producedAt, costMillis int64) (Message, error) {
	if seq < 0 {
		return Message{}, errors.New("seq must be non-negative")
	}
	if costMillis < 0 {
		return Message{}, errors.New("costMillis must be non-negative")
	}
	return Message{key, seq, typ, amount, producedAt, costMillis}, nil
}

// StoredMessage is a message as it sits in a partition: the log's offset plus the producer's message.
type StoredMessage struct {
	Offset  int64
	Message Message
}

// Fnv1a32 is FNV-1a 32-bit over UTF-8 bytes; the same function Lesson 01 uses, so a key lands where Lesson 01 would put it.
func Fnv1a32(bytes []byte) uint32 {
	hash := uint32(0x811C9DC5)
	for _, b := range bytes {
		hash ^= uint32(b)
		hash *= 0x01000193
	}
	return hash
}

// PartitionFor is the partition for a key: hash masked to non-negative, then mod N. Same key, same partition, while N
// is fixed. A non-positive partition count is a programming error and panics.
func PartitionFor(key string, partitionCount int) int {
	if partitionCount <= 0 {
		panic("partitionCount must be positive")
	}
	return int(Fnv1a32([]byte(key))&0x7FFFFFFF) % partitionCount
}

// Partition is a minimal single-partition log: dense offsets, append-only, read by offset. Lesson 01 has the
// segmented version.
type Partition struct {
	ID      int
	records []StoredMessage
}

func NewPartition(id int) *Partition { return &Partition{ID: id} }

func (p *Partition) Append(message Message) int64 {
	offset := int64(len(p.records))
	p.records = append(p.records, StoredMessage{offset, message})
	return offset
}

func (p *Partition) LogEndOffset() int64 { return int64(len(p.records)) }

// Get panics on an offset outside the log, like Java's IndexOutOfBoundsException: it is a programming error.
func (p *Partition) Get(offset int64) StoredMessage {
	if offset < 0 || offset >= int64(len(p.records)) {
		panic(fmt.Sprintf("offset %d not in [0, %d)", offset, len(p.records)))
	}
	return p.records[offset]
}

func (p *Partition) ReadAll() []StoredMessage { return p.records }

// SequenceOf is the sequence of seq values for one key, in offset order. Per-key order holds iff this is strictly
// increasing by one.
func (p *Partition) SequenceOf(key string) []int64 {
	out := []int64{}
	for _, s := range p.records {
		if s.Message.Key == key {
			out = append(out, s.Message.Seq)
		}
	}
	return out
}

// Topic is a topic with N partitions. Appending routes by key.
type Topic struct {
	Name       string
	partitions []*Partition
}

func NewTopic(name string, partitionCount int) *Topic {
	t := &Topic{Name: name}
	for i := 0; i < partitionCount; i++ {
		t.partitions = append(t.partitions, NewPartition(i))
	}
	return t
}

func (t *Topic) PartitionCount() int { return len(t.partitions) }

func (t *Topic) Partition(id int) *Partition { return t.partitions[id] }

func (t *Topic) Append(message Message) int {
	p := PartitionFor(message.Key, len(t.partitions))
	t.partitions[p].Append(message)
	return p
}

// ---------------------------------------------------------------------------------------------------------------------
// Retry-induced reordering. A producer with several sends in flight that retries a failed send lands it after sends
// that were queued behind it. Sequence numbers detect this at the consumer and prevent it at the broker.
// ---------------------------------------------------------------------------------------------------------------------

// FaultInjector decides which sends fail on their first attempt. Deterministic: either an explicit set or a seeded
// JavaRandom.
type FaultInjector struct {
	failFirstAttempt map[int64]bool
}

func NewFaultInjector(failFirstAttempt ...int64) *FaultInjector {
	f := &FaultInjector{failFirstAttempt: map[int64]bool{}}
	for _, i := range failFirstAttempt {
		f.failFirstAttempt[i] = true
	}
	return f
}

// RandomFaults fails roughly probability of the first count sends, chosen by a seeded java.util.Random.
func RandomFaults(seed int64, count int, probability float64) *FaultInjector {
	random := NewJavaRandom(seed)
	f := NewFaultInjector()
	for i := int64(0); i < int64(count); i++ {
		if random.NextDouble() < probability {
			f.failFirstAttempt[i] = true
		}
	}
	return f
}

func (f *FaultInjector) FailsFirstAttempt(sendIndex int64) bool { return f.failFirstAttempt[sendIndex] }

// SendOutcome is what one simulated producer run produced: the final partition contents and how many retries and
// rejections happened.
type SendOutcome struct {
	Appended []Message
	Retries  int
	Rejected int
}

type send struct {
	index     int64
	message   Message
	attempted bool
}

// Deliver simulates a producer sending one key's messages to one partition with up to maxInFlight sends outstanding.
// A send that fails its first attempt is retried after the rest of the in-flight window has been acknowledged, which
// is exactly how a retry overtakes its successors. With sequenceGuard the broker rejects any send whose seq is not
// lastSeq + 1 (KIP-98's idempotent producer), so the producer must re-send in order and the log stays ordered.
func Deliver(messages []Message, maxInFlight int, faults *FaultInjector, sequenceGuard bool) (SendOutcome, error) {
	if maxInFlight <= 0 {
		return SendOutcome{}, errors.New("maxInFlight must be positive")
	}
	pending := make([]*send, 0, len(messages))
	for i, m := range messages {
		pending = append(pending, &send{index: int64(i), message: m})
	}
	appended := []Message{}
	lastSeq := map[string]int64{}
	retries, rejected := 0, 0
	for len(pending) > 0 {
		n := min(maxInFlight, len(pending))
		window := pending[:n]
		pending = pending[n:]
		resend := []*send{}
		for _, s := range window {
			if !s.attempted && faults.FailsFirstAttempt(s.index) {
				s.attempted = true
				retries++
				resend = append(resend, s)
				continue
			}
			s.attempted = true
			key := s.message.Key
			expected := int64(0)
			if last, ok := lastSeq[key]; ok {
				expected = last + 1
			}
			if sequenceGuard && s.message.Seq != expected {
				rejected++
				resend = append(resend, s)
				continue
			}
			lastSeq[key] = s.message.Seq
			appended = append(appended, s.message)
		}
		sort.Slice(resend, func(i, j int) bool { return resend[i].index < resend[j].index })
		pending = append(resend, pending...)
	}
	return SendOutcome{appended, retries, rejected}, nil
}

// Verdict classifies one observed message against the expected next seq for its key.
type Verdict int

const (
	InOrder Verdict = iota
	Duplicate
	Gap
)

func (v Verdict) String() string {
	switch v {
	case InOrder:
		return "IN_ORDER"
	case Duplicate:
		return "DUPLICATE"
	default:
		return "GAP"
	}
}

// SequenceTracker is the consumer-side check that a key's messages arrive with seq = last + 1.
type SequenceTracker struct {
	lastSeq    map[string]int64
	duplicates int
	gaps       int
}

func NewSequenceTracker() *SequenceTracker { return &SequenceTracker{lastSeq: map[string]int64{}} }

func (t *SequenceTracker) Observe(m Message) Verdict {
	expected := int64(0)
	if last, ok := t.lastSeq[m.Key]; ok {
		expected = last + 1
	}
	if m.Seq == expected {
		t.lastSeq[m.Key] = m.Seq
		return InOrder
	}
	if m.Seq < expected {
		t.duplicates++
		return Duplicate
	}
	t.gaps++
	t.lastSeq[m.Key] = m.Seq
	return Gap
}

func (t *SequenceTracker) Duplicates() int { return t.duplicates }

func (t *SequenceTracker) Gaps() int { return t.gaps }

// IsOrdered is true iff every message in the list for every key was InOrder.
func IsOrdered(messages []Message) bool {
	t := NewSequenceTracker()
	for _, m := range messages {
		if t.Observe(m) != InOrder {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------------------------------------------------
// Head-of-line blocking and per-key lanes. A discrete-event simulation: each message has an arrival time and a
// processing cost; the scheduler decides when it starts, and latency is completion minus arrival.
// ---------------------------------------------------------------------------------------------------------------------

// Completion says when a message was processed and how long it waited.
type Completion struct {
	Offset        int64
	Key           string
	StartedAt     int64
	CompletedAt   int64
	LatencyMillis int64
}

// LatencyStats summarises completion latencies. All values are simulated milliseconds.
type LatencyStats struct {
	Count int
	Max   int64
	Mean  int64
	P50   int64
	P99   int64
}

func StatsOf(completions []Completion) LatencyStats {
	if len(completions) == 0 {
		return LatencyStats{}
	}
	lat := make([]int64, len(completions))
	var sum int64
	for i, c := range completions {
		lat[i] = c.LatencyMillis
		sum += lat[i]
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	return LatencyStats{len(lat), lat[len(lat)-1], sum / int64(len(lat)), percentile(lat, 50), percentile(lat, 99)}
}

func percentile(sorted []int64, p int) int64 {
	idx := int(math.Ceil(float64(p)/100.0*float64(len(sorted)))) - 1
	return sorted[max(0, min(len(sorted)-1, idx))]
}

// StatsExcluding restricts the stats to completions whose key is not excluded: "how did everyone else fare?"
func StatsExcluding(completions []Completion, excluded string) LatencyStats {
	rest := []Completion{}
	for _, c := range completions {
		if c.Key != excluded {
			rest = append(rest, c)
		}
	}
	return StatsOf(rest)
}

func onlyKey(completions []Completion, key string) []Completion {
	out := []Completion{}
	for _, c := range completions {
		if c.Key == key {
			out = append(out, c)
		}
	}
	return out
}

var (
	// ErrAlreadyCommitted: an offset below the committable mark was completed again.
	ErrAlreadyCommitted = errors.New("offset already committed")
	// ErrCompletedTwice: an offset ahead of the mark was completed a second time.
	ErrCompletedTwice = errors.New("offset completed twice")
)

// CommitTracker tracks which offsets of a partition have finished processing and reports the offset that may be
// committed: the smallest offset not yet completed. With out-of-order completion this is the "committed offset is the
// next offset to read" contract from Lesson 01 applied to parallel processing: commit the low-water mark, never the
// highest offset.
type CommitTracker struct {
	committable int64
	doneAhead   map[int64]bool
}

func NewCommitTracker(startOffset int64) *CommitTracker {
	return &CommitTracker{committable: startOffset, doneAhead: map[int64]bool{}}
}

// Complete marks an offset finished and returns the new committable mark.
func (c *CommitTracker) Complete(offset int64) (int64, error) {
	if offset < c.committable {
		return c.committable, fmt.Errorf("%w: %d", ErrAlreadyCommitted, offset)
	}
	if c.doneAhead[offset] {
		return c.committable, fmt.Errorf("%w: %d", ErrCompletedTwice, offset)
	}
	c.doneAhead[offset] = true
	for c.doneAhead[c.committable] {
		delete(c.doneAhead, c.committable)
		c.committable++
	}
	return c.committable, nil
}

func (c *CommitTracker) Committable() int64 { return c.committable }

func (c *CommitTracker) CompletedAhead() int { return len(c.doneAhead) }

// Scheduler is a processing strategy for one partition's records.
type Scheduler interface {
	Run(partition *Partition) []Completion
}

// SequentialScheduler: one consumer, one thread, offsets in order. The slow record at offset k delays every record after k.
type SequentialScheduler struct{}

func (SequentialScheduler) Run(partition *Partition) []Completion {
	out := []Completion{}
	var busyUntil int64
	for _, s := range partition.ReadAll() {
		m := s.Message
		start := max(busyUntil, m.ProducedAt)
		end := start + m.CostMillis
		busyUntil = end
		out = append(out, Completion{s.Offset, m.Key, start, end, end - m.ProducedAt})
	}
	return out
}

// KeyedLaneScheduler runs per-key lanes inside one partition: lane = hash(key) mod lanes. Each lane processes its
// records in offset order, so per-key order holds; different lanes run independently, so a slow key blocks only the
// keys sharing its lane. Also drives a CommitTracker so the demo can show how far behind the committable offset
// trails completion.
type KeyedLaneScheduler struct {
	lanes             int
	finalCommittable  int64
	maxCompletedAhead int
}

func NewKeyedLaneScheduler(lanes int) (*KeyedLaneScheduler, error) {
	if lanes <= 0 {
		return nil, errors.New("lanes must be positive")
	}
	return &KeyedLaneScheduler{lanes: lanes}, nil
}

func (k *KeyedLaneScheduler) LaneFor(key string) int { return PartitionFor(key, k.lanes) }

func (k *KeyedLaneScheduler) Run(partition *Partition) []Completion {
	laneBusyUntil := make([]int64, k.lanes)
	out := []Completion{}
	for _, s := range partition.ReadAll() {
		m := s.Message
		lane := k.LaneFor(m.Key)
		start := max(laneBusyUntil[lane], m.ProducedAt)
		end := start + m.CostMillis
		laneBusyUntil[lane] = end
		out = append(out, Completion{s.Offset, m.Key, start, end, end - m.ProducedAt})
	}
	// Replay completions in time order through a CommitTracker to find how far the commit lags.
	byTime := append([]Completion(nil), out...)
	sort.Slice(byTime, func(i, j int) bool {
		if byTime[i].CompletedAt != byTime[j].CompletedAt {
			return byTime[i].CompletedAt < byTime[j].CompletedAt
		}
		return byTime[i].Offset < byTime[j].Offset
	})
	tracker := NewCommitTracker(0)
	k.maxCompletedAhead = 0
	for _, c := range byTime {
		if _, err := tracker.Complete(c.Offset); err != nil {
			panic(err) // every offset completes exactly once in a replay
		}
		k.maxCompletedAhead = max(k.maxCompletedAhead, tracker.CompletedAhead())
	}
	k.finalCommittable = tracker.Committable()
	return out
}

func (k *KeyedLaneScheduler) FinalCommittable() int64 { return k.finalCommittable }

// MaxCompletedAhead is the most offsets that were ever finished but uncommittable because an earlier offset was
// still running.
func (k *KeyedLaneScheduler) MaxCompletedAhead() int { return k.maxCompletedAhead }

// BuildWorkload makes count messages arriving one every gapMillis, spread round-robin over keys. Every message costs
// fastCost, except messages for slowKey, which cost slowCost.
func BuildWorkload(keys []string, count int, gapMillis, fastCost int64, slowKey string, slowCost int64) []Message {
	seqs := map[string]int64{}
	out := make([]Message, 0, count)
	for i := 0; i < count; i++ {
		key := keys[i%len(keys)]
		seq := seqs[key]
		seqs[key] = seq + 1
		cost := fastCost
		if key == slowKey {
			cost = slowCost
		}
		typ := "REVERSAL"
		switch seq % 3 {
		case 0:
			typ = "AUTH"
		case 1:
			typ = "CAPTURE"
		}
		out = append(out, Message{key, seq, typ, OfMinor(int64(1999+i), USD), int64(i) * gapMillis, cost})
	}
	return out
}

// PerKeyOrderHolds is true iff, for every key, completions happen in offset order (StartedAt non-decreasing along
// the key's offsets).
func PerKeyOrderHolds(partition *Partition, completions []Completion) bool {
	byOffset := map[int64]Completion{}
	for _, c := range completions {
		byOffset[c.Offset] = c
	}
	lastEnd := map[string]int64{}
	for _, s := range partition.ReadAll() {
		c := byOffset[s.Offset]
		key := s.Message.Key
		previousEnd, seen := lastEnd[key]
		if seen && c.StartedAt < previousEnd {
			return false
		}
		lastEnd[key] = c.CompletedAt
	}
	return true
}

func seqsOf(messages []Message) []int64 {
	out := make([]int64, len(messages))
	for i, m := range messages {
		out[i] = m.Seq
	}
	return out
}

func formatInt64s(xs []int64) string {
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

func main() {
	fmt.Println("== 1. Keys choose partitions; a partition is the unit of order ==")
	topic := NewTopic("payments.events", 3)
	accounts := []string{"acct-1001", "acct-1002", "acct-1003", "acct-1004", "acct-1005", "acct-1006"}
	for _, a := range accounts {
		fmt.Printf("%-10s -> partition %d\n", a, PartitionFor(a, 3))
	}
	for _, m := range BuildWorkload(accounts, 18, 10, 5, "none", 5) {
		topic.Append(m)
	}
	for p := 0; p < topic.PartitionCount(); p++ {
		part := topic.Partition(p)
		var sb strings.Builder
		for _, s := range part.ReadAll() {
			fmt.Fprintf(&sb, "%s#%d ", s.Message.Key[5:], s.Message.Seq)
		}
		fmt.Printf("partition %d (%d records): %s\n", p, part.LogEndOffset(), strings.TrimSpace(sb.String()))
	}
	fmt.Println("acct-1001 seqs in its partition: " + formatInt64s(topic.Partition(PartitionFor("acct-1001", 3)).SequenceOf("acct-1001")))
	moved := make([]string, len(accounts))
	for i, a := range accounts {
		moved[i] = fmt.Sprintf("%s->%d", a[5:], PartitionFor(a, 4))
	}
	fmt.Printf("same keys with 4 partitions: %s\n", formatStrings(moved))

	fmt.Println("\n== 2. Head-of-line blocking: one slow account in a partition of fast ones ==")
	hol := NewPartition(0)
	keys := []string{"acct-fast-1", "acct-fast-2", "acct-fast-3", "acct-slow", "acct-fast-4", "acct-fast-5"}
	for _, m := range BuildWorkload(keys, 60, 10, 2, "acct-slow", 40) {
		hol.Append(m)
	}
	fmt.Println("60 messages, one every 10 ms; fast accounts cost 2 ms, acct-slow costs 40 ms")
	sequential := SequentialScheduler{}.Run(hol)
	seqAll := StatsOf(sequential)
	seqFast := StatsExcluding(sequential, "acct-slow")
	fmt.Printf("sequential   : all p50=%d p99=%d max=%d | fast accounts only p50=%d max=%d\n",
		seqAll.P50, seqAll.P99, seqAll.Max, seqFast.P50, seqFast.Max)
	fastVictim := sequential[4]
	fmt.Printf("  offset 4 (%s, cost 2 ms) arrived at %d ms, started at %d ms: it waited behind acct-slow at offset 3\n",
		fastVictim.Key, hol.Get(4).Message.ProducedAt, fastVictim.StartedAt)

	fmt.Println("\n== 3. Per-key lanes inside the partition: order per key, parallelism across keys ==")
	for _, lanes := range []int{2, 4, 8} {
		scheduler := must(NewKeyedLaneScheduler(lanes))
		laned := scheduler.Run(hol)
		fast := StatsExcluding(laned, "acct-slow")
		slow := StatsOf(onlyKey(laned, "acct-slow"))
		fmt.Printf("%d lanes: fast accounts p50=%d max=%d | acct-slow max=%d | per-key order holds=%t | committable at end=%d, max finished-but-uncommittable=%d\n",
			lanes, fast.P50, fast.Max, slow.Max, PerKeyOrderHolds(hol, laned), scheduler.FinalCommittable(), scheduler.MaxCompletedAhead())
		if lanes == 2 {
			sharing := []string{}
			for _, k := range keys {
				if scheduler.LaneFor(k) == scheduler.LaneFor("acct-slow") && k != "acct-slow" {
					sharing = append(sharing, k)
				}
			}
			sort.Strings(sharing)
			fmt.Println("  keys sharing acct-slow's lane (still blocked): " + formatStrings(sharing))
		}
	}

	fmt.Println("\n== 4. Commit the low-water mark, not the highest finished offset ==")
	tracker := NewCommitTracker(0)
	for _, off := range []int64{1, 2, 0, 4, 3} {
		mark := must(tracker.Complete(off))
		fmt.Printf("offset %d done -> committable %d (finished ahead: %d)\n", off, mark, tracker.CompletedAhead())
	}

	fmt.Println("\n== 5. Retries reorder when more than one send is in flight ==")
	sends := BuildWorkload([]string{"acct-7"}, 8, 1, 1, "none", 1)
	faults := NewFaultInjector(2, 5)
	for _, inFlight := range []int{1, 5} {
		outcome := must(Deliver(sends, inFlight, faults, false))
		fmt.Printf("maxInFlight=%d, sends 2 and 5 fail once: log seqs=%s retries=%d ordered=%t\n",
			inFlight, formatInt64s(seqsOf(outcome.Appended)), outcome.Retries, IsOrdered(outcome.Appended))
	}
	reordered := must(Deliver(sends, 5, faults, false))
	tracker2 := NewSequenceTracker()
	var verdicts strings.Builder
	for _, m := range reordered.Appended {
		fmt.Fprintf(&verdicts, "%d:%s ", m.Seq, tracker2.Observe(m))
	}
	fmt.Println("consumer-side SequenceTracker on the reordered log: " + strings.TrimSpace(verdicts.String()))
	fmt.Printf("  gaps=%d duplicates=%d (a GAP followed by a lower seq is the signature of a reordered retry)\n", tracker2.Gaps(), tracker2.Duplicates())
	guarded := must(Deliver(sends, 5, faults, true))
	fmt.Printf("maxInFlight=5 with broker sequence guard: log seqs=%s retries=%d rejected=%d ordered=%t\n",
		formatInt64s(seqsOf(guarded.Appended)), guarded.Retries, guarded.Rejected, IsOrdered(guarded.Appended))

	fmt.Println("\n== 6. The same thing at scale, seeded ==")
	many := BuildWorkload([]string{"acct-9"}, 2000, 1, 1, "none", 1)
	seeded := RandomFaults(42, 2000, 0.05)
	unguarded := must(Deliver(many, 5, seeded, false))
	guardedMany := must(Deliver(many, 5, seeded, true))
	t3 := NewSequenceTracker()
	for _, m := range unguarded.Appended {
		t3.Observe(m)
	}
	fmt.Printf("2000 sends, 5%% fail once, maxInFlight=5: unguarded retries=%d gaps seen by consumer=%d; guarded rejected=%d ordered=%t\n",
		unguarded.Retries, t3.Gaps(), guardedMany.Rejected, IsOrdered(guardedMany.Appended))
}
