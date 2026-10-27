// Lesson: phases/08-messaging-and-streams/10-dead-letter-queues-and-poison-messages/docs/en.md
// Dead-Letter Queues and Poison Messages
// Run:  go run .
// Test: go test ./...
// Go 1.22+, standard library only. Same design as the Java and Python versions. Retry classification follows the
// transient/permanent split used by AWS SQS redrive policies (maxReceiveCount) and Kafka Connect's
// errors.tolerance / errors.deadletterqueue.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Event is a posting event as it sits on the partition: offset, the account it belongs to (the ordering key),
// and the raw payload fields exactly as the producer serialized them. Parsing happens in the handler, because a
// poison message is by definition one that cannot be parsed.
type Event struct {
	Offset       int64
	AccountKey   string
	AmountText   string
	CurrencyCode string
}

// FailureKind says whether a failure might succeed on retry (Transient) or never will without a code or data
// change (Permanent).
type FailureKind int

const (
	Transient FailureKind = iota
	Permanent
)

func (k FailureKind) String() string {
	if k == Permanent {
		return "PERMANENT"
	}
	return "TRANSIENT"
}

// HandlerError is the handler's one way to say no. It carries the classification the consumer's retry policy
// depends on; the consumer recovers it with errors.As.
type HandlerError struct {
	Kind FailureKind
	Msg  string
}

func (e *HandlerError) Error() string { return e.Msg }

func newHandlerError(kind FailureKind, msg string) error { return &HandlerError{Kind: kind, Msg: msg} }

var (
	ErrNegativeAdvance = errors.New("cannot advance backwards")
	ErrMaxAttempts     = errors.New("maxAttempts must be >= 1")
)

// SimClock is a deterministic simulated clock; the consumer advances it by the backoff instead of sleeping.
type SimClock struct {
	now int64
}

func (c *SimClock) Now() int64 { return c.now }

func (c *SimClock) Advance(millis int64) error {
	if millis < 0 {
		return ErrNegativeAdvance
	}
	c.now += millis
	return nil
}

// FaultInjector is a seeded transient fault injector: the ledger database times out on a fixed fraction of calls.
type FaultInjector struct {
	random      *JavaRandom
	failureRate float64
	enabled     bool
}

func NewFaultInjector(seed int64, failureRate float64) *FaultInjector {
	return &FaultInjector{random: NewJavaRandom(seed), failureRate: failureRate, enabled: true}
}

func (f *FaultInjector) ShouldFail() bool { return f.enabled && f.random.NextDouble() < f.failureRate }

func (f *FaultInjector) Disable() { f.enabled = false }

// Handler processes one event or returns a *HandlerError. It must be idempotent per offset, because retries
// re-invoke it.
type Handler interface {
	Handle(event Event) error
}

// LedgerPostingHandler posts an amount to an in-memory per-account ledger. Parse failures are Permanent (the
// bytes will never parse differently). Injected database timeouts are Transient. The handler is idempotent: an
// offset that has already been applied is a no-op, so a retry after a late success cannot double-post.
type LedgerPostingHandler struct {
	balances map[string]Money
	applied  map[int64]bool
	faults   *FaultInjector
}

func NewLedgerPostingHandler(faults *FaultInjector) *LedgerPostingHandler {
	return &LedgerPostingHandler{balances: map[string]Money{}, applied: map[int64]bool{}, faults: faults}
}

func (h *LedgerPostingHandler) Handle(e Event) error {
	currency, err := CurrencyOf(e.CurrencyCode)
	if err != nil {
		return newHandlerError(Permanent, err.Error())
	}
	amount, err := ParseMoney(e.AmountText, currency)
	if err != nil {
		return newHandlerError(Permanent, err.Error())
	}
	if h.faults.ShouldFail() {
		return newHandlerError(Transient, "ledger db timeout")
	}
	if h.applied[e.Offset] { // idempotent replay
		return nil
	}
	if current, ok := h.balances[e.AccountKey]; ok {
		sum, err := current.Plus(amount)
		if err != nil {
			return newHandlerError(Permanent, err.Error())
		}
		h.balances[e.AccountKey] = sum
	} else {
		h.balances[e.AccountKey] = amount
	}
	h.applied[e.Offset] = true
	return nil
}

// Balance returns the account's balance and whether the account has ever been posted to (Java returns null).
func (h *LedgerPostingHandler) Balance(accountKey string) (Money, bool) {
	m, ok := h.balances[accountKey]
	return m, ok
}

// Balances returns the account keys in sorted order with their balances: the Java TreeMap view, so output is
// deterministic.
func (h *LedgerPostingHandler) Balances() ([]string, map[string]Money) {
	keys := make([]string, 0, len(h.balances))
	copied := make(map[string]Money, len(h.balances))
	for k, v := range h.balances {
		keys = append(keys, k)
		copied[k] = v
	}
	sort.Strings(keys)
	return keys, copied
}

func formatBalances(h *LedgerPostingHandler) string {
	keys, balances := h.Balances()
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+balances[k].String())
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// ParkReason says why an event was parked.
type ParkReason int

const (
	PermanentFailure ParkReason = iota
	MaxAttemptsExceeded
	KeyQuarantined
)

func (r ParkReason) String() string {
	switch r {
	case PermanentFailure:
		return "PERMANENT_FAILURE"
	case MaxAttemptsExceeded:
		return "MAX_ATTEMPTS_EXCEEDED"
	default:
		return "KEY_QUARANTINED"
	}
}

// DeadLetter is a parked event with everything an operator needs to decide what to do with it.
type DeadLetter struct {
	Event     Event
	Reason    ParkReason
	Attempts  int
	LastError string
	ParkedAt  int64
}

// DeadLetterQueue is an append-only parking lot with a per-key view, so replay can be done one account at a
// time, in offset order.
type DeadLetterQueue struct {
	entries []DeadLetter
}

func NewDeadLetterQueue() *DeadLetterQueue { return &DeadLetterQueue{} }

func (q *DeadLetterQueue) Park(letter DeadLetter) { q.entries = append(q.entries, letter) }

func (q *DeadLetterQueue) All() []DeadLetter {
	out := make([]DeadLetter, len(q.entries))
	copy(out, q.entries)
	return out
}

func (q *DeadLetterQueue) ForKey(key string) []DeadLetter {
	var out []DeadLetter
	for _, d := range q.entries {
		if d.Event.AccountKey == key {
			out = append(out, d)
		}
	}
	return out
}

// Clear removes every entry for the key; called when a replay for that key begins.
func (q *DeadLetterQueue) Clear(key string) {
	kept := q.entries[:0]
	for _, d := range q.entries {
		if d.Event.AccountKey != key {
			kept = append(kept, d)
		}
	}
	q.entries = kept
}

func (q *DeadLetterQueue) Size() int { return len(q.entries) }

// ConsumerStats are the counters that make the behaviour measurable.
type ConsumerStats struct {
	Processed       int64
	Parked          int64
	Quarantined     int64
	Retries         int64
	CommittedOffset int64
}

func (s ConsumerStats) String() string {
	return fmt.Sprintf("ConsumerStats[processed=%d, parked=%d, quarantined=%d, retries=%d, committedOffset=%d]",
		s.Processed, s.Parked, s.Quarantined, s.Retries, s.CommittedOffset)
}

// ResilientConsumer never stalls the partition. For each event in offset order:
//  1. If the event's key is quarantined, park it as KeyQuarantined without calling the handler, so per-key
//     ordering is preserved (a later posting must not land before an earlier one that is still parked).
//  2. Otherwise call the handler. Permanent failure: park at once. Transient failure: back off (simulated clock)
//     and retry up to maxAttempts, then park as MaxAttemptsExceeded.
//  3. Parking quarantines the key. Either way the offset is committed and the next event is processed.
//
// Invariant: processed + parked == events consumed, and committedOffset == last offset + 1.
type ResilientConsumer struct {
	handler           Handler
	dlq               *DeadLetterQueue
	clock             *SimClock
	maxAttempts       int
	baseBackoffMillis int64
	quarantinedKeys   map[string]int64 // key -> offset that caused it
	processed         int64
	parked            int64
	quarantined       int64
	retries           int64
	committedOffset   int64
}

func NewResilientConsumer(handler Handler, dlq *DeadLetterQueue, clock *SimClock, maxAttempts int, baseBackoffMillis int64) (*ResilientConsumer, error) {
	if maxAttempts < 1 {
		return nil, ErrMaxAttempts
	}
	return &ResilientConsumer{
		handler: handler, dlq: dlq, clock: clock, maxAttempts: maxAttempts, baseBackoffMillis: baseBackoffMillis,
		quarantinedKeys: map[string]int64{},
	}, nil
}

func (c *ResilientConsumer) Consume(partition []Event) {
	for _, e := range partition {
		if cause, quarantined := c.quarantinedKeys[e.AccountKey]; quarantined {
			c.dlq.Park(DeadLetter{Event: e, Reason: KeyQuarantined, Attempts: 0,
				LastError: fmt.Sprintf("earlier offset %d for this key is parked", cause), ParkedAt: c.clock.Now()})
			c.parked++
			c.quarantined++
		} else {
			c.attempt(e)
		}
		c.committedOffset = e.Offset + 1
	}
}

// attempt tries the handler with retries; returns true on success, false if the event was parked.
func (c *ResilientConsumer) attempt(e Event) bool {
	attempts := 0
	for {
		attempts++
		err := c.handler.Handle(e)
		if err == nil {
			c.processed++
			return true
		}
		var he *HandlerError
		if !errors.As(err, &he) {
			panic(fmt.Sprintf("handler returned a non-HandlerError for offset %d: %v", e.Offset, err)) // Java: uncaught
		}
		if he.Kind == Permanent {
			c.park(e, PermanentFailure, attempts, he.Msg)
			return false
		}
		if attempts >= c.maxAttempts {
			c.park(e, MaxAttemptsExceeded, attempts, he.Msg)
			return false
		}
		c.retries++
		if err := c.clock.Advance(c.baseBackoffMillis << (attempts - 1)); err != nil { // exponential backoff: base, 2x, 4x, ...
			panic(err)
		}
	}
}

func (c *ResilientConsumer) park(e Event, reason ParkReason, attempts int, lastError string) {
	c.dlq.Park(DeadLetter{Event: e, Reason: reason, Attempts: attempts, LastError: lastError, ParkedAt: c.clock.Now()})
	c.parked++
	c.quarantinedKeys[e.AccountKey] = e.Offset
}

// Replay replays every parked event for one key, in offset order, after the operator has fixed the cause.
// corrections maps an offset to a repaired event (for poison payloads); other events replay as-is. Stops at the
// first event that fails again, leaving it and its followers parked. On full success the key leaves quarantine.
func (c *ResilientConsumer) Replay(key string, corrections map[int64]Event) int {
	letters := c.dlq.ForKey(key)
	sort.SliceStable(letters, func(i, j int) bool { return letters[i].Event.Offset < letters[j].Event.Offset })
	c.dlq.Clear(key)
	delete(c.quarantinedKeys, key)
	replayed := 0
	for i := 0; i < len(letters); i++ {
		e := letters[i].Event
		if fixed, ok := corrections[e.Offset]; ok {
			e = fixed
		}
		c.parked-- // leaving the DLQ, one way or the other
		if c.attempt(e) {
			replayed++
			continue
		}
		// Re-park the rest in order, preserving per-key ordering behind the failure.
		for j := i + 1; j < len(letters); j++ {
			rest := letters[j].Event
			c.dlq.Park(DeadLetter{Event: rest, Reason: KeyQuarantined, Attempts: 0,
				LastError: fmt.Sprintf("replay of offset %d failed again", e.Offset), ParkedAt: c.clock.Now()})
			c.quarantined++
		}
		break
	}
	return replayed
}

func (c *ResilientConsumer) IsQuarantined(key string) bool {
	_, ok := c.quarantinedKeys[key]
	return ok
}

func (c *ResilientConsumer) Stats() ConsumerStats {
	return ConsumerStats{c.processed, c.parked, c.quarantined, c.retries, c.committedOffset}
}

// StopTheWorldConsumer is the alternative most systems ship by accident: on any failure, stop and retry the same
// offset forever. It records how far the partition got before it stalled.
type StopTheWorldConsumer struct {
	handler         Handler
	maxAttempts     int
	processed       int64
	committedOffset int64
	stuckOn         *Event
}

func NewStopTheWorldConsumer(handler Handler, maxAttempts int) *StopTheWorldConsumer {
	return &StopTheWorldConsumer{handler: handler, maxAttempts: maxAttempts}
}

func (c *StopTheWorldConsumer) Consume(partition []Event) {
	for i := range partition {
		e := partition[i]
		attempts := 0
		for {
			attempts++
			err := c.handler.Handle(e)
			if err == nil {
				c.processed++
				c.committedOffset = e.Offset + 1
				break
			}
			var he *HandlerError
			if !errors.As(err, &he) {
				panic(fmt.Sprintf("handler returned a non-HandlerError for offset %d: %v", e.Offset, err))
			}
			if he.Kind == Permanent || attempts >= c.maxAttempts {
				stuck := e
				c.stuckOn = &stuck
				return // the partition is now stalled behind this offset
			}
		}
	}
}

func (c *StopTheWorldConsumer) Processed() int64 { return c.processed }

func (c *StopTheWorldConsumer) CommittedOffset() int64 { return c.committedOffset }

// StuckOn is the event the consumer stalled on, or nil if it finished the partition.
func (c *StopTheWorldConsumer) StuckOn() *Event { return c.stuckOn }

func partition() []Event {
	return []Event{
		{0, "acct-A", "100.00", "USD"},
		{1, "acct-B", "25.50", "USD"},
		{2, "acct-A", "12.345", "USD"}, // poison: three decimals in a two-decimal currency
		{3, "acct-C", "1200", "JPY"},
		{4, "acct-A", "-40.00", "USD"}, // fine on its own, but acct-A is quarantined behind offset 2
		{5, "acct-B", "10.00", "USD"},
		{6, "acct-D", "7.00", "US$"}, // poison: unknown currency code
		{7, "acct-C", "300", "JPY"},
		{8, "acct-B", "4.25", "USD"},
		{9, "acct-A", "5.00", "USD"}, // also quarantined behind offset 2
	}
}

func mustConsumer(handler Handler, dlq *DeadLetterQueue, clock *SimClock, maxAttempts int, baseBackoffMillis int64) *ResilientConsumer {
	c, err := NewResilientConsumer(handler, dlq, clock, maxAttempts, baseBackoffMillis)
	if err != nil {
		panic(err)
	}
	return c
}

func main() {
	events := partition()

	fmt.Println("== Stop-the-world consumer: retry the same offset until it works ==")
	naive := NewStopTheWorldConsumer(NewLedgerPostingHandler(NewFaultInjector(7, 0.0)), 3)
	naive.Consume(events)
	stuck := naive.StuckOn()
	fmt.Printf("processed %d of %d, committed offset %d, stuck on offset %d (%s %s)\n", naive.Processed(), len(events),
		naive.CommittedOffset(), stuck.Offset, stuck.AccountKey, stuck.AmountText)
	fmt.Println("accounts B, C, and D never see another posting until someone wakes up.")
	fmt.Println()

	fmt.Println("== Resilient consumer: park the poison, quarantine its key, keep every other account moving ==")
	clock := &SimClock{}
	faults := NewFaultInjector(11, 0.25) // one ledger call in four times out
	handler := NewLedgerPostingHandler(faults)
	dlq := NewDeadLetterQueue()
	consumer := mustConsumer(handler, dlq, clock, 3, 100)
	consumer.Consume(events)
	s := consumer.Stats()
	fmt.Printf("processed=%d parked=%d (of which quarantined followers=%d) transient retries=%d committed offset=%d simulated clock=%dms\n",
		s.Processed, s.Parked, s.Quarantined, s.Retries, s.CommittedOffset, clock.Now())
	fmt.Println("balances: " + formatBalances(handler))
	fmt.Println("dead letters:")
	for _, d := range dlq.All() {
		fmt.Printf("  offset %d %-6s %-8s %-4s reason=%-22s attempts=%d error=%s\n", d.Event.Offset, d.Event.AccountKey,
			d.Event.AmountText, d.Event.CurrencyCode, d.Reason, d.Attempts, d.LastError)
	}
	fmt.Println()

	fmt.Println("== Replay acct-A after correcting offset 2 (12.345 -> 12.34, approved by finance) ==")
	faults.Disable()
	replayedA := consumer.Replay("acct-A", map[int64]Event{2: {2, "acct-A", "12.34", "USD"}})
	fmt.Printf("replayed %d events in offset order; acct-A quarantined=%v\n", replayedA, consumer.IsQuarantined("acct-A"))
	balanceA, _ := handler.Balance("acct-A")
	fmt.Printf("acct-A balance = %s  (100.00 + 12.34 - 40.00 + 5.00)\n", balanceA)
	fmt.Println()

	fmt.Println("== Replay acct-B after the ledger database recovered: no correction needed ==")
	replayedB := consumer.Replay("acct-B", map[int64]Event{})
	balanceB, _ := handler.Balance("acct-B")
	fmt.Printf("replayed %d events; acct-B balance = %s  (25.50 + 10.00 + 4.25)\n", replayedB, balanceB)
	fmt.Printf("dead letters remaining: %d (offset 6, acct-D, awaiting a currency-code fix)\n", dlq.Size())
	fmt.Println("stats: " + consumer.Stats().String())
}
