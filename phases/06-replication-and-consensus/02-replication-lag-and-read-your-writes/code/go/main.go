// Lesson: phases/06-replication-and-consensus/02-replication-lag-and-read-your-writes/docs/en.md
// Replication Lag and Read-Your-Writes
// Run:  go run .
// Test: go test ./...
// Sources: Terry et al., "Session Guarantees for Weakly Consistent Replicated Data" (PDIS 1994); PostgreSQL docs,
// "Log-Shipping Standby Servers" and pg_current_wal_lsn(); Vogels, "Eventually Consistent" (CACM 2009).
// Go 1.22+, standard library only. Same design as the Java and Python versions: the only randomness is a seeded
// SplitMix64, time is a tick counter.
package main

import (
	"container/heap"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Rng is SplitMix64 (Steele, Lea, Flood, OOPSLA 2014). Same stream for the same seed in every language, so a bug replays.
type Rng struct{ state uint64 }

func NewRng(seed int64) *Rng { return &Rng{state: uint64(seed)} }

func (r *Rng) NextLong() uint64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// NextInt returns a uniform integer in [0, bound) from the top 31 bits, as the Java `(nextLong() >>> 33) % bound`.
func (r *Rng) NextInt(bound int) int { return int((r.NextLong() >> 33) % uint64(bound)) }

// Currency is an ISO 4217 currency, as in Phase 10 Lesson 01. Only what this lesson needs: a code and an exponent.
type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	EUR = Currency{"EUR", 2}
)

// NewCurrency validates the code and exponent; the package-level constants are known-good.
func NewCurrency(code string, exponent int) (Currency, error) {
	if len(code) != 3 {
		return Currency{}, errors.New("currency code must be three letters")
	}
	if exponent < 0 || exponent > 4 {
		return Currency{}, errors.New("exponent must be 0..4")
	}
	return Currency{code, exponent}, nil
}

// ErrCurrencyMismatch is returned when two Money values in different currencies are combined.
var ErrCurrencyMismatch = errors.New("currency mismatch")

// ErrOverflow is returned when a minor-unit sum does not fit in int64 (Java's Math.addExact throws here).
var ErrOverflow = errors.New("overflow")

// Money is integer minor units plus a currency; never a float. Copied from Phase 10 Lesson 01, trimmed to what the demo uses.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func addChecked(a, b int64) (int64, error) {
	sum := a + b
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum >= 0) {
		return 0, ErrOverflow
	}
	return sum, nil
}

func (m Money) Plus(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.Currency.Code, other.Currency.Code)
	}
	sum, err := addChecked(m.MinorUnits, other.MinorUnits)
	if err != nil {
		return Money{}, err
	}
	return Money{sum, m.Currency}, nil
}

func (m Money) String() string {
	scale := int64(1)
	for i := 0; i < m.Currency.Exponent; i++ {
		scale *= 10
	}
	abs := m.MinorUnits
	if abs < 0 {
		abs = -abs
	}
	whole, frac := abs/scale, abs%scale
	sign := ""
	if m.MinorUnits < 0 {
		sign = "-"
	}
	if m.Currency.Exponent == 0 {
		return fmt.Sprintf("%s%d %s", sign, whole, m.Currency.Code)
	}
	return fmt.Sprintf("%s%d.%0*d %s", sign, whole, m.Currency.Exponent, frac, m.Currency.Code)
}

// LogRecord is one record in the leader's replication log. The LSN (log sequence number) is its 1-based position; it only grows.
type LogRecord struct {
	Lsn   int64
	Key   string
	Value string
}

func (r LogRecord) String() string { return fmt.Sprintf("#%d %s=%s", r.Lsn, r.Key, r.Value) }

// ErrLsnOutOfRange is returned by Leader.Since for a watermark the log has never reached.
var ErrLsnOutOfRange = errors.New("lsn out of range")

// Leader is the single writer. Every write appends one record and returns its LSN, which is the token a client carries afterwards.
type Leader struct {
	Log  []LogRecord
	Data map[string]string
}

func NewLeader() *Leader { return &Leader{Data: map[string]string{}} }

func (l *Leader) Lsn() int64 { return int64(len(l.Log)) }

func (l *Leader) Write(key, value string) int64 {
	r := LogRecord{int64(len(l.Log)) + 1, key, value}
	l.Log = append(l.Log, r)
	l.Data[key] = value
	return r.Lsn
}

func (l *Leader) Read(key string) (string, bool) {
	v, ok := l.Data[key]
	return v, ok
}

// Since returns the records with LSN strictly greater than afterLsn: what a replica that has applied through afterLsn still needs.
func (l *Leader) Since(afterLsn int64) ([]LogRecord, error) {
	if afterLsn < 0 || afterLsn > int64(len(l.Log)) {
		return nil, fmt.Errorf("%w: %d", ErrLsnOutOfRange, afterLsn)
	}
	out := make([]LogRecord, len(l.Log)-int(afterLsn))
	copy(out, l.Log[afterLsn:])
	return out, nil
}

// pending is a shipped record waiting in a replica's inbox for its arrival tick.
type pending struct {
	arriveAt int
	lsn      int64
	record   LogRecord
}

// pendingHeap orders by (arriveAt, lsn), the Java PriorityQueue comparator.
type pendingHeap []pending

func (h pendingHeap) Len() int { return len(h) }
func (h pendingHeap) Less(i, j int) bool {
	if h[i].arriveAt != h[j].arriveAt {
		return h[i].arriveAt < h[j].arriveAt
	}
	return h[i].lsn < h[j].lsn
}
func (h pendingHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *pendingHeap) Push(x interface{}) { *h = append(*h, x.(pending)) }
func (h *pendingHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// ErrBadDelay is returned when a replica's delay range is not 1 <= min <= max.
var ErrBadDelay = errors.New("delays must satisfy 1 <= min <= max")

// Replica is an asynchronous replica. Records arrive after a seeded delay in [minDelay, maxDelay] ticks. With ApplyInOrder
// the replica applies only the contiguous prefix of the log (so its state is always some prefix of the leader's history);
// without it the replica applies each record the moment it arrives, which is faster and violates consistent prefix.
type Replica struct {
	ID           int
	Name         string
	rng          *Rng
	MinDelay     int
	MaxDelay     int
	ApplyInOrder bool
	Data         map[string]string
	inbox        pendingHeap
	arrived      map[int64]LogRecord
	appliedSet   map[int64]bool
	// AppliedLsn is the highest LSN such that every record 1..AppliedLsn has been applied. A session token is compared with it.
	AppliedLsn        int64
	OutOfOrderApplies int
	Applied           int
}

func NewReplica(id int, rng *Rng, minDelay, maxDelay int, applyInOrder bool) (*Replica, error) {
	if minDelay < 1 || maxDelay < minDelay {
		return nil, ErrBadDelay
	}
	return &Replica{
		ID: id, Name: fmt.Sprintf("r%d", id), rng: rng, MinDelay: minDelay, MaxDelay: maxDelay, ApplyInOrder: applyInOrder,
		Data: map[string]string{}, arrived: map[int64]LogRecord{}, appliedSet: map[int64]bool{},
	}, nil
}

// Receive is the leader shipping a record; it lands in the inbox with a seeded delay. Nothing is applied until Tick().
func (r *Replica) Receive(rec LogRecord, now int) {
	delay := r.MinDelay + r.rng.NextInt(r.MaxDelay-r.MinDelay+1)
	heap.Push(&r.inbox, pending{now + delay, rec.Lsn, rec})
}

// Tick moves everything that has arrived into the reorder buffer, then applies. In-order mode applies only while the next
// expected LSN is present (a gap means a record is still in flight); on-arrival mode applies the whole buffer in LSN order.
func (r *Replica) Tick(now int) {
	for len(r.inbox) > 0 && r.inbox[0].arriveAt <= now {
		p := heap.Pop(&r.inbox).(pending)
		r.arrived[p.lsn] = p.record
	}
	if r.ApplyInOrder {
		for {
			rec, ok := r.arrived[r.AppliedLsn+1]
			if !ok {
				break
			}
			delete(r.arrived, rec.Lsn)
			r.apply(rec)
		}
	} else {
		keys := make([]int64, 0, len(r.arrived))
		for k := range r.arrived {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		for _, k := range keys {
			rec := r.arrived[k]
			delete(r.arrived, k)
			r.apply(rec)
		}
	}
}

func (r *Replica) apply(rec LogRecord) {
	if rec.Lsn != r.AppliedLsn+1 {
		r.OutOfOrderApplies++
	}
	r.Data[rec.Key] = rec.Value
	r.appliedSet[rec.Lsn] = true
	r.Applied++
	for r.appliedSet[r.AppliedLsn+1] {
		r.AppliedLsn++
	}
}

func (r *Replica) Read(key string) (string, bool) {
	v, ok := r.Data[key]
	return v, ok
}

// Pending counts records shipped but not yet applied: still in flight, or arrived and waiting for a gap to fill.
func (r *Replica) Pending() int { return len(r.inbox) + len(r.arrived) }

// Buffered is the size of the reorder buffer: records that arrived early and wait for an earlier LSN. Always 0 in on-arrival mode.
func (r *Replica) Buffered() int { return len(r.arrived) }

func (r *Replica) LagBehind(leader *Leader) int64 { return leader.Lsn() - r.AppliedLsn }

// ReadMode says which freshness rule a read follows. All but LeaderMode are implemented by one method: a floor on the replica's LSN.
type ReadMode int

const (
	AnyReplica ReadMode = iota
	ReadYourWrites
	Monotonic
	SessionMode
	LeaderMode
)

func (m ReadMode) String() string {
	return [...]string{"ANY_REPLICA", "READ_YOUR_WRITES", "MONOTONIC", "SESSION", "LEADER"}[m]
}

// Session is the per-client state that makes session guarantees possible. LastWriteLsn is the token the client got from its
// last write; LastReadLsn is the freshest point it has observed. Both are plain int64s: they cost nothing to carry in a cookie.
type Session struct {
	ID           int64
	LastWriteLsn int64
	LastReadLsn  int64
}

func NewSession(id int64) *Session { return &Session{ID: id} }

func (s *Session) Floor(mode ReadMode) int64 {
	switch mode {
	case ReadYourWrites:
		return s.LastWriteLsn
	case Monotonic:
		return s.LastReadLsn
	case SessionMode:
		return max64(s.LastWriteLsn, s.LastReadLsn)
	default: // AnyReplica, LeaderMode
		return 0
	}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// ReadResult is what a read returns, plus where it came from. Found is false where the Java value is null.
type ReadResult struct {
	Value      string
	Found      bool
	ServedBy   string
	AtLsn      int64
	Redirected bool
}

func (r ReadResult) String() string {
	v := "<nothing>"
	if r.Found {
		v = r.Value
	}
	red := ""
	if r.Redirected {
		red = ", redirected"
	}
	return fmt.Sprintf("%s  (served by %s at lsn %d%s)", v, r.ServedBy, r.AtLsn, red)
}

// ErrDelayRanges is returned when the per-replica delay arrays do not line up.
var ErrDelayRanges = errors.New("one delay range per replica")

// ReplicatedStore is leader plus replicas plus a tick clock. The load balancer is a seeded random pick, like a round-robin DNS answer.
type ReplicatedStore struct {
	Leader      *Leader
	Replicas    []*Replica
	balancer    *Rng
	now         int
	Reads       int
	StaleReads  int
	Redirects   int
	LeaderReads int
}

func NewReplicatedStore(seed int64, applyInOrder bool, minDelays, maxDelays []int) (*ReplicatedStore, error) {
	if len(minDelays) != len(maxDelays) || len(minDelays) == 0 {
		return nil, ErrDelayRanges
	}
	s := &ReplicatedStore{Leader: NewLeader(), balancer: NewRng(seed)}
	for i := range maxDelays {
		r, err := NewReplica(i, NewRng(seed*7919+1000003*int64(i+1)), minDelays[i], maxDelays[i], applyInOrder)
		if err != nil {
			return nil, err
		}
		s.Replicas = append(s.Replicas, r)
	}
	return s, nil
}

// Of builds a store whose replicas all have min delay 1. The demo and tests use it; a bad delay is a programming error.
func Of(seed int64, applyInOrder bool, maxDelays ...int) *ReplicatedStore {
	mins := make([]int, len(maxDelays))
	for i := range mins {
		mins[i] = 1
	}
	s, err := NewReplicatedStore(seed, applyInOrder, mins, maxDelays)
	if err != nil {
		panic(err)
	}
	return s
}

func (s *ReplicatedStore) Now() int { return s.now }

func (s *ReplicatedStore) Tick() {
	s.now++
	for _, r := range s.Replicas {
		r.Tick(s.now)
	}
}

func (s *ReplicatedStore) Run(ticks int) {
	for i := 0; i < ticks; i++ {
		s.Tick()
	}
}

// Write goes through the leader and fans the record out. Returns the LSN; a client that keeps it can read its own write.
func (s *ReplicatedStore) Write(key, value string) int64 {
	lsn := s.Leader.Write(key, value)
	r := s.Leader.Log[lsn-1]
	for _, rep := range s.Replicas {
		rep.Receive(r, s.now)
	}
	return lsn
}

// WriteSession writes on behalf of a session: the session remembers the LSN.
func (s *ReplicatedStore) WriteSession(sess *Session, key, value string) int64 {
	lsn := s.Write(key, value)
	sess.LastWriteLsn = lsn
	return lsn
}

func (s *ReplicatedStore) PickReplica(mode ReadMode, sess *Session) *Replica {
	if mode == Monotonic || mode == SessionMode {
		return s.Replicas[int(sess.ID%int64(len(s.Replicas)))] // sticky
	}
	return s.Replicas[s.balancer.NextInt(len(s.Replicas))]
}

// Read is the whole mechanism: pick a replica, and if it has not applied through the floor the session demands, find one
// that has, falling back to the leader. The floor is 0 for AnyReplica, which is the bug, and a token otherwise.
func (s *ReplicatedStore) Read(sess *Session, key string, mode ReadMode) ReadResult {
	s.Reads++
	if mode == LeaderMode {
		s.LeaderReads++
		v, ok := s.Leader.Read(key)
		return s.finish(sess, ReadResult{v, ok, "leader", s.Leader.Lsn(), false})
	}
	floor := sess.Floor(mode)
	chosen := s.PickReplica(mode, sess)
	if chosen.AppliedLsn >= floor {
		v, ok := chosen.Read(key)
		return s.finish(sess, ReadResult{v, ok, chosen.Name, chosen.AppliedLsn, false})
	}
	for _, r := range s.Replicas {
		if r.AppliedLsn >= floor {
			s.Redirects++
			v, ok := r.Read(key)
			return s.finish(sess, ReadResult{v, ok, r.Name, r.AppliedLsn, true})
		}
	}
	s.Redirects++
	s.LeaderReads++
	v, ok := s.Leader.Read(key)
	return s.finish(sess, ReadResult{v, ok, "leader", s.Leader.Lsn(), true})
}

func (s *ReplicatedStore) finish(sess *Session, result ReadResult) ReadResult {
	if result.AtLsn < s.Leader.Lsn() {
		s.StaleReads++
	}
	sess.LastReadLsn = max64(sess.LastReadLsn, result.AtLsn)
	return result
}

// CatchUp resynchronises a replica from its own watermark: the LSN is also the catch-up cursor. Records arrive with the usual delay.
func (s *ReplicatedStore) CatchUp(r *Replica) int {
	missing, err := s.Leader.Since(r.AppliedLsn)
	if err != nil {
		panic(err) // a replica's watermark is always within the leader's log
	}
	shipped := 0
	for _, rec := range missing {
		if r.Pending() == 0 || !r.ApplyInOrder {
			r.Receive(rec, s.now)
			shipped++
		}
	}
	return shipped
}

func (s *ReplicatedStore) LagReport() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "leader lsn=%d", s.Leader.Lsn())
	for _, r := range s.Replicas {
		fmt.Fprintf(&sb, ", %s applied=%d (lag %d)", r.Name, r.AppliedLsn, r.LagBehind(s.Leader))
	}
	return sb.String()
}

// AnomalyDetector watches a sequence of reads by one session and reports the two anomalies this lesson is about:
//   - a monotonic violation: a later read was served at a lower LSN than an earlier one (time went backwards)
//   - a consistent-prefix violation: the reader saw a later record's key but not an earlier one it depends on
type AnomalyDetector struct {
	MonotonicViolations int
	PrefixViolations    int
	highWater           int64
}

func (d *AnomalyDetector) Observe(r ReadResult) {
	if r.AtLsn < d.highWater {
		d.MonotonicViolations++
	}
	d.highWater = max64(d.highWater, r.AtLsn)
}

// ObservePair: `later` was written after `earlier`. Seeing `later` without `earlier` cannot happen on any prefix of the log.
func (d *AnomalyDetector) ObservePair(earlierFound, laterFound bool) {
	if laterFound && !earlierFound {
		d.PrefixViolations++
	}
}

func main() {
	fmt.Println("== The bug: a customer pays, refreshes, and sees no payment (seed 3, replicas with max delay 4, 12, 40) ==")
	store := Of(3, true, 4, 12, 40)
	alice := NewSession(1)
	amount := OfMinor(4999, USD)
	lsn := store.WriteSession(alice, "payment:p-1001", "CAPTURED "+amount.String())
	fmt.Printf("t=%d  POST /payments -> leader appends #%d, returns 201 with lsn token %d\n", store.Now(), lsn, lsn)
	store.Run(2)
	fmt.Printf("t=%d  %s\n", store.Now(), store.LagReport())
	for i := 0; i < 3; i++ {
		r := store.Read(alice, "payment:p-1001", AnyReplica)
		fmt.Printf("t=%d  GET /payments/p-1001 (any replica)       -> %s\n", store.Now(), r)
		store.Tick()
	}

	fmt.Println()
	fmt.Println("== Read-your-writes: the session carries the LSN of its last write and reads only from replicas at or past it ==")
	for i := 0; i < 3; i++ {
		r := store.Read(alice, "payment:p-1001", ReadYourWrites)
		fmt.Printf("t=%d  GET /payments/p-1001 (floor lsn %d) -> %s\n", store.Now(), alice.Floor(ReadYourWrites), r)
		store.Tick()
	}
	bob := NewSession(2)
	bobSees := store.Read(bob, "payment:p-1001", ReadYourWrites)
	fmt.Printf("t=%d  bob (never wrote, floor 0) reads p-1001         -> %s   <- allowed: he has no write to see\n", store.Now(), bobSees)

	fmt.Println()
	fmt.Println("== Monotonic reads: without a sticky replica, a refresh can travel back in time ==")
	tt := Of(5, true, 2, 30)
	carol := NewSession(0)
	tt.WriteSession(carol, "payment:p-2002", "CAPTURED "+OfMinor(1250, USD).String())
	naive := &AnomalyDetector{}
	for i := 0; i < 6; i++ {
		tt.Run(2)
		r := tt.Read(carol, "payment:p-2002", AnyReplica)
		naive.Observe(r)
		fmt.Printf("t=%d  refresh (any replica) -> %s\n", tt.Now(), r)
	}
	fmt.Printf("time-travel anomalies seen by the naive reader: %d\n", naive.MonotonicViolations)
	mt := Of(5, true, 2, 30)
	dave := NewSession(1) // sticky to r1, the slow one, which makes the floor do the work
	mt.WriteSession(dave, "payment:p-2002", "CAPTURED "+OfMinor(1250, USD).String())
	guarded := &AnomalyDetector{}
	for i := 0; i < 6; i++ {
		mt.Run(2)
		r := mt.Read(dave, "payment:p-2002", SessionMode)
		guarded.Observe(r)
		fmt.Printf("t=%d  refresh (session floor %d) -> %s\n", mt.Now(), dave.Floor(SessionMode), r)
	}
	fmt.Printf("time-travel anomalies seen by the session reader: %d\n", guarded.MonotonicViolations)

	fmt.Println()
	fmt.Println("== Consistent prefix: 'captured' must never be visible without 'authorized' (200 payments, seed 11) ==")
	for _, inOrder := range []bool{false, true} {
		cp := Of(11, inOrder, 20)
		det := &AnomalyDetector{}
		for p := 1; p <= 200; p++ {
			cp.Write(fmt.Sprintf("payment:p-%d:auth", p), "AUTHORIZED")
			cp.Write(fmt.Sprintf("payment:p-%d:capture", p), "CAPTURED")
			for t := 0; t < 10; t++ {
				cp.Tick()
				_, auth := cp.Replicas[0].Read(fmt.Sprintf("payment:p-%d:auth", p))
				_, capture := cp.Replicas[0].Read(fmt.Sprintf("payment:p-%d:capture", p))
				det.ObservePair(auth, capture)
			}
		}
		cp.Run(50)
		mode := "on arrival"
		if inOrder {
			mode = "in LSN order"
		}
		fmt.Printf("replica applies %-13s -> out-of-order applies=%d, 'captured before authorized' observed %d times, final appliedLsn=%d of %d\n",
			mode, cp.Replicas[0].OutOfOrderApplies, det.PrefixViolations, cp.Replicas[0].AppliedLsn, cp.Leader.Lsn())
	}

	fmt.Println()
	fmt.Println("== 20 seeds x 300 write-then-read pairs, 3 replicas (max delay 5, 15, 45): stale-read rate by read mode ==")
	for _, mode := range []ReadMode{AnyReplica, ReadYourWrites, SessionMode, LeaderMode} {
		stale, total, redirects, leaderReads, monotonic := 0, 0, 0, 0, 0
		for seed := int64(1); seed <= 20; seed++ {
			s := Of(seed, true, 5, 15, 45)
			user := NewSession(seed)
			det := &AnomalyDetector{}
			for i := 1; i <= 300; i++ {
				s.WriteSession(user, "k", fmt.Sprintf("v%d", i))
				s.Run(1 + (i % 7))
				r := s.Read(user, "k", mode)
				det.Observe(r)
				total++
				if !r.Found || r.Value != fmt.Sprintf("v%d", i) {
					stale++
				}
			}
			redirects += s.Redirects
			leaderReads += s.LeaderReads
			monotonic += det.MonotonicViolations
		}
		fmt.Printf("%-17s stale own-write reads %4d/%d  redirects %4d  leader reads %4d  time-travel %d\n", mode, stale, total, redirects, leaderReads, monotonic)
	}
	fmt.Println("the token costs one long per session; the leader took the hit only when no replica had caught up.")
}
