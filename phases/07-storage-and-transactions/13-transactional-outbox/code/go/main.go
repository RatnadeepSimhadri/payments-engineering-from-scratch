// Lesson: phases/07-storage-and-transactions/13-transactional-outbox/docs/en.md
// The Transactional Outbox and Dual-Write Problem
// Run:  go run .
// Test: go test ./...
// Sources: Richardson, "Microservices Patterns" ch. 3 (transactional outbox); Debezium outbox event router docs;
// Helland, "Life beyond Distributed Transactions" (CIDR 2007). Money is integer minor units (Phase 10, Lesson 01). Same design as Java and Python.
package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Currency is an ISO 4217 currency, copied from Phase 10 Lesson 01.
type Currency struct {
	Code     string
	Exponent int
}

var USD = Currency{"USD", 2}

func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

var (
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrOverflow         = errors.New("int64 overflow")
	// ErrCrash is returned at an injected crash point. The simulation stops where the real process would die.
	ErrCrash = errors.New("crash")
	// ErrBrokerUnavailable: the broker refused or lost a publish. Transient by assumption: the caller retries.
	ErrBrokerUnavailable = errors.New("broker unavailable")
	ErrTxClosed          = errors.New("transaction already closed")
	ErrDuplicatePayment  = errors.New("duplicate payment")
)

// Money is an exact amount of one currency in signed minor units. Copied from Phase 10 Lesson 01.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }
func Zero(currency Currency) Money                      { return Money{0, currency} }

func addChecked(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, ErrOverflow
	}
	return a + b, nil
}

func (m Money) Plus(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.Currency.Code, other.Currency.Code)
	}
	sum, err := addChecked(m.MinorUnits, other.MinorUnits)
	return Money{sum, m.Currency}, err
}

func (m Money) String() string {
	magnitude := m.MinorUnits
	if magnitude < 0 {
		magnitude = -magnitude
	}
	scale := m.Currency.MinorUnitsPerMajor()
	text := strconv.FormatInt(magnitude/scale, 10)
	if m.Currency.Exponent > 0 {
		text += fmt.Sprintf(".%0*d", m.Currency.Exponent, magnitude%scale)
	}
	if m.MinorUnits < 0 {
		text = "-" + text
	}
	return text + " " + m.Currency.Code
}

// SimClock only moves when the simulation says so.
type SimClock struct{ nowMs int64 }

func NewSimClock(startMs int64) *SimClock { return &SimClock{nowMs: startMs} }
func (c *SimClock) Now() int64            { return c.nowMs }
func (c *SimClock) Advance(ms int64)      { c.nowMs += ms }

// CrashError is returned at an injected crash point; errors.Is(err, ErrCrash) matches it.
type CrashError struct{ Where string }

func (e *CrashError) Error() string        { return "crash " + e.Where }
func (e *CrashError) Is(target error) bool { return target == ErrCrash }

// Payment is the business row: a payment that was accepted and must be reflected in the ledger.
type Payment struct {
	PaymentID   string
	Merchant    string
	Amount      Money
	CreatedAtMs int64
}

// OutboxEvent is one outbox row. Seq is the store's insertion order (the primary key); EventID is the identity the
// consumer deduplicates on; AggregateID is the partition key downstream. Published flips only when the relay confirms.
type OutboxEvent struct {
	Seq         int64
	EventID     string
	AggregateID string
	Type        string
	Payload     string
	CreatedAtMs int64
	Published   bool
}

func (e OutboxEvent) MarkPublished() OutboxEvent {
	e.Published = true
	return e
}

func (e OutboxEvent) String() string {
	state := " [pending]"
	if e.Published {
		state = " [published]"
	}
	return fmt.Sprintf("#%d %s %s %s%s", e.Seq, e.EventID, e.Type, e.Payload, state)
}

// Store is an in-memory store with real transactions: writes are buffered in the Tx and applied to the tables only on
// commit, all of them or none. This is the one property the outbox pattern relies on: the payment row and the
// outbox row live in the same store, so one commit covers both.
type Store struct {
	payments     map[string]Payment
	paymentOrder []string
	outbox       map[int64]OutboxEvent // keyed by seq; seq order == insertion order
	nextSeq      int64
	Commits      int
	Rollbacks    int
}

func NewStore() *Store {
	return &Store{payments: map[string]Payment{}, outbox: map[int64]OutboxEvent{}, nextSeq: 1}
}

// Tx buffers writes. InsertPayment, InsertOutbox and MarkPublished chain; a use after close or a duplicate id is
// remembered and reported by Commit, so the transaction fails as a whole.
type Tx struct {
	store           *Store
	pendingPayments []Payment
	pendingEvents   []OutboxEvent
	pendingMarks    []int64
	open            bool
	err             error
}

func (s *Store) Begin() *Tx { return &Tx{store: s, open: true} }

func (t *Tx) requireOpen() error {
	if !t.open {
		return ErrTxClosed
	}
	return nil
}

func (t *Tx) fail(err error) *Tx {
	if t.err == nil {
		t.err = err
	}
	return t
}

func (t *Tx) InsertPayment(p Payment) *Tx {
	if err := t.requireOpen(); err != nil {
		return t.fail(err)
	}
	if _, dup := t.store.payments[p.PaymentID]; dup {
		return t.fail(fmt.Errorf("%w: %s", ErrDuplicatePayment, p.PaymentID))
	}
	t.pendingPayments = append(t.pendingPayments, p)
	return t
}

// InsertOutbox buffers an outbox row. The sequence number is assigned at commit so commit order equals seq order.
func (t *Tx) InsertOutbox(eventID, aggregateID, typ, payload string, atMs int64) *Tx {
	if err := t.requireOpen(); err != nil {
		return t.fail(err)
	}
	t.pendingEvents = append(t.pendingEvents, OutboxEvent{0, eventID, aggregateID, typ, payload, atMs, false})
	return t
}

func (t *Tx) MarkPublished(seq int64) *Tx {
	if err := t.requireOpen(); err != nil {
		return t.fail(err)
	}
	t.pendingMarks = append(t.pendingMarks, seq)
	return t
}

// Err reports a buffered failure (duplicate id, closed transaction) before Commit is attempted.
func (t *Tx) Err() error { return t.err }

func (t *Tx) Commit() error {
	if err := t.requireOpen(); err != nil {
		return err
	}
	if t.err != nil {
		return t.err
	}
	t.open = false
	s := t.store
	for _, p := range t.pendingPayments {
		if _, present := s.payments[p.PaymentID]; !present {
			s.paymentOrder = append(s.paymentOrder, p.PaymentID)
		}
		s.payments[p.PaymentID] = p
	}
	for _, e := range t.pendingEvents {
		seq := s.nextSeq
		s.nextSeq++
		s.outbox[seq] = OutboxEvent{seq, e.EventID, e.AggregateID, e.Type, e.Payload, e.CreatedAtMs, false}
	}
	for _, seq := range t.pendingMarks {
		if e, ok := s.outbox[seq]; ok {
			s.outbox[seq] = e.MarkPublished()
		}
	}
	s.Commits++
	return nil
}

func (t *Tx) Rollback() error {
	if err := t.requireOpen(); err != nil {
		return err
	}
	t.open = false
	t.store.Rollbacks++
	return nil
}

func (s *Store) Payment(id string) (Payment, bool) {
	p, ok := s.payments[id]
	return p, ok
}

func (s *Store) PaymentCount() int { return len(s.payments) }

// OutboxRows returns every outbox row in seq (commit) order.
func (s *Store) OutboxRows() []OutboxEvent {
	out := make([]OutboxEvent, 0, len(s.outbox))
	for seq := int64(1); seq < s.nextSeq; seq++ {
		if e, ok := s.outbox[seq]; ok {
			out = append(out, e)
		}
	}
	return out
}

// Unpublished is the relay's query: unpublished rows in commit order, at most limit.
func (s *Store) Unpublished(limit int) []OutboxEvent {
	out := []OutboxEvent{}
	for _, e := range s.OutboxRows() {
		if !e.Published {
			out = append(out, e)
		}
		if len(out) == limit {
			break
		}
	}
	return out
}

func (s *Store) UnpublishedCount() int {
	n := 0
	for _, e := range s.outbox {
		if !e.Published {
			n++
		}
	}
	return n
}

// Message is a message as the broker holds it. The broker assigns its own offset; the eventId travels inside.
type Message struct {
	Offset      int64
	EventID     string
	AggregateID string
	Type        string
	Payload     string
}

// Broker is an in-memory topic. Append-only, with a switch to simulate an outage.
type Broker struct {
	log             []Message
	Down            bool
	PublishAttempts int
}

func NewBroker() *Broker { return &Broker{} }

func (b *Broker) Publish(eventID, aggregateID, typ, payload string) (int64, error) {
	b.PublishAttempts++
	if b.Down {
		return 0, ErrBrokerUnavailable
	}
	offset := int64(len(b.log))
	b.log = append(b.log, Message{offset, eventID, aggregateID, typ, payload})
	return offset, nil
}

func (b *Broker) Messages() []Message {
	out := make([]Message, len(b.log))
	copy(out, b.log)
	return out
}

func (b *Broker) From(offset int64) []Message {
	if offset >= int64(len(b.log)) {
		return []Message{}
	}
	out := make([]Message, len(b.log)-int(offset))
	copy(out, b.log[offset:])
	return out
}

func (b *Broker) Size() int { return len(b.log) }

// CrashPoint says where a process dies.
type CrashPoint int

const (
	NoCrash CrashPoint = iota
	AfterCommitBeforePublish
	AfterPublishBeforeCommit
	AfterPublishBeforeMark
)

// Events builds the payload once so the dual-write and outbox services emit byte-identical events.
const PaymentAccepted = "PaymentAccepted"

func EventIDFor(p Payment) string { return "evt-" + p.PaymentID }
func PayloadFor(p Payment) string { return p.PaymentID + "|" + p.Merchant + "|" + p.Amount.String() }

// DualWritePaymentService is the naive service: write the row, then publish the event (or the other way round). Two
// systems, two commits, no atomicity between them. Crash points show both ways it loses data.
type DualWritePaymentService struct {
	store        *Store
	broker       *Broker
	CrashPoint   CrashPoint
	PublishFirst bool
}

func NewDualWritePaymentService(store *Store, broker *Broker) *DualWritePaymentService {
	return &DualWritePaymentService{store: store, broker: broker}
}

func (s *DualWritePaymentService) Accept(p Payment) error {
	if s.PublishFirst {
		if _, err := s.broker.Publish(EventIDFor(p), p.Merchant, PaymentAccepted, PayloadFor(p)); err != nil {
			return err
		}
		if s.CrashPoint == AfterPublishBeforeCommit {
			return &CrashError{"after publish, before commit"}
		}
		return s.store.Begin().InsertPayment(p).Commit()
	}
	if err := s.store.Begin().InsertPayment(p).Commit(); err != nil {
		return err
	}
	if s.CrashPoint == AfterCommitBeforePublish {
		return &CrashError{"after commit, before publish"}
	}
	_, err := s.broker.Publish(EventIDFor(p), p.Merchant, PaymentAccepted, PayloadFor(p))
	return err
}

// OutboxPaymentService is the fixed service: the payment row and the outbox row go into one transaction. The broker is never touched here.
type OutboxPaymentService struct {
	store *Store
	clock *SimClock
}

func NewOutboxPaymentService(store *Store, clock *SimClock) *OutboxPaymentService {
	return &OutboxPaymentService{store: store, clock: clock}
}

func (s *OutboxPaymentService) Accept(p Payment) error {
	return s.store.Begin().
		InsertPayment(p).
		InsertOutbox(EventIDFor(p), p.Merchant, PaymentAccepted, PayloadFor(p), s.clock.Now()).
		Commit()
}

// AcceptThenFail is a request that fails validation after the row was staged: rollback must drop the outbox row with it.
func (s *OutboxPaymentService) AcceptThenFail(p Payment) error {
	tx := s.store.Begin().
		InsertPayment(p).
		InsertOutbox(EventIDFor(p), p.Merchant, PaymentAccepted, PayloadFor(p), s.clock.Now())
	return tx.Rollback()
}

// OutboxRelay is the relay (a polling publisher). It reads unpublished rows in seq order, publishes each, then marks it
// published in a second transaction. A crash between publish and mark re-publishes on the next poll: at-least-once.
type OutboxRelay struct {
	store          *Store
	broker         *Broker
	BatchSize      int
	CrashPoint     CrashPoint
	Published      int
	FailedAttempts int
	Polls          int
}

func NewOutboxRelay(store *Store, broker *Broker, batchSize int) *OutboxRelay {
	return &OutboxRelay{store: store, broker: broker, BatchSize: batchSize}
}

// Poll runs one poll. Returns how many rows were marked published. Stops at the first broker failure to keep order.
// The error is a *CrashError at an injected crash point, or a failed mark transaction; a broker outage is not an error here.
func (r *OutboxRelay) Poll() (int, error) {
	r.Polls++
	marked := 0
	for _, e := range r.store.Unpublished(r.BatchSize) {
		if _, err := r.broker.Publish(e.EventID, e.AggregateID, e.Type, e.Payload); err != nil {
			if errors.Is(err, ErrBrokerUnavailable) {
				r.FailedAttempts++
				return marked, nil // leave the row pending; retry next poll
			}
			return marked, err
		}
		r.Published++
		if r.CrashPoint == AfterPublishBeforeMark {
			r.CrashPoint = NoCrash
			return marked, &CrashError{"after publish of " + e.EventID + ", before mark"}
		}
		if err := r.store.Begin().MarkPublished(e.Seq).Commit(); err != nil {
			return marked, err
		}
		marked++
	}
	return marked, nil
}

// Drain polls until nothing is pending or the broker refuses. Bounded so a dead broker cannot loop forever.
func (r *OutboxRelay) Drain(maxPolls int) (int, error) {
	total := 0
	for i := 0; i < maxPolls && r.store.UnpublishedCount() > 0; i++ {
		n, err := r.Poll()
		total += n
		if err != nil {
			return total, err
		}
		if n == 0 {
			break // broker refused; nothing changes until it recovers
		}
	}
	return total, nil
}

// LedgerConsumer is a downstream consumer: the ledger projection. It reads the broker from its offset, applies each
// event once, and ignores any eventId it has already applied. The dedupe set is what turns at-least-once into effectively-once.
type LedgerConsumer struct {
	broker            *Broker
	balances          map[string]Money
	applied           map[string]bool
	offset            int64
	DuplicatesIgnored int
}

func NewLedgerConsumer(broker *Broker) *LedgerConsumer {
	return &LedgerConsumer{broker: broker, balances: map[string]Money{}, applied: map[string]bool{}}
}

func (c *LedgerConsumer) Consume() (int, error) {
	appliedNow := 0
	for _, m := range c.broker.From(c.offset) {
		c.offset = m.Offset + 1
		if c.applied[m.EventID] {
			c.DuplicatesIgnored++
			continue
		}
		c.applied[m.EventID] = true
		parts := strings.Split(m.Payload, "|")
		amount, err := ParseMoney(parts[2])
		if err != nil {
			return appliedNow, err
		}
		current, ok := c.balances[m.AggregateID]
		if !ok {
			current = Zero(amount.Currency)
		}
		if c.balances[m.AggregateID], err = current.Plus(amount); err != nil {
			return appliedNow, err
		}
		appliedNow++
	}
	return appliedNow, nil
}

// ParseMoney reads "1234.56 USD" back into minor units without a float.
func ParseMoney(text string) (Money, error) {
	p := strings.Split(text, " ")
	neg := strings.HasPrefix(p[0], "-")
	digits := strings.ReplaceAll(strings.TrimPrefix(p[0], "-"), ".", "")
	minor, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return Money{}, err
	}
	if neg {
		minor = -minor
	}
	return OfMinor(minor, USD), nil
}

func (c *LedgerConsumer) Balance(merchant string) Money {
	if m, ok := c.balances[merchant]; ok {
		return m
	}
	return Zero(USD)
}

func (c *LedgerConsumer) AppliedCount() int { return len(c.applied) }
func (c *LedgerConsumer) Offset() int64     { return c.offset }

// ---- worked example ----

func usd(text string) Money {
	m, err := ParseMoney(text + " USD")
	if err != nil {
		panic(err)
	}
	return m
}

func payment(id, merchant, amount string, clock *SimClock) Payment {
	return Payment{id, merchant, usd(amount), clock.Now()}
}

func line(s string) { fmt.Println(s) }

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
	clock := NewSimClock(0)

	line("== 1. Dual write, commit then publish: the broker is down at the wrong moment ==")
	{
		store := NewStore()
		broker := NewBroker()
		svc := NewDualWritePaymentService(store, broker)
		check(svc.Accept(payment("pay-1", "acme", "20.00", clock)))
		broker.Down = true
		if err := svc.Accept(payment("pay-2", "acme", "35.00", clock)); errors.Is(err, ErrBrokerUnavailable) {
			line("  pay-2: row committed, then publish failed: " + err.Error())
		}
		broker.Down = false
		ledger := NewLedgerConsumer(broker)
		must(ledger.Consume())
		line(fmt.Sprintf("  store has %d payments; broker has %d messages", store.PaymentCount(), broker.Size()))
		line(fmt.Sprintf("  ledger balance for acme = %s  (store says 55.00: pay-2 is an orphan row, the ledger will never see it)", ledger.Balance("acme")))
	}

	line("\n== 2. Dual write, publish then commit: the process dies between the two ==")
	{
		store := NewStore()
		broker := NewBroker()
		svc := NewDualWritePaymentService(store, broker)
		svc.PublishFirst = true
		svc.CrashPoint = AfterPublishBeforeCommit
		if err := svc.Accept(payment("pay-3", "acme", "12.50", clock)); errors.Is(err, ErrCrash) {
			line("  pay-3: " + err.Error())
		}
		ledger := NewLedgerConsumer(broker)
		must(ledger.Consume())
		line(fmt.Sprintf("  store has %d payments; ledger balance for acme = %s  (a ghost event: the ledger credits a payment that does not exist)",
			store.PaymentCount(), ledger.Balance("acme")))
	}

	line("\n== 3. Outbox: one transaction, two rows; the relay delivers when the broker comes back ==")
	{
		store := NewStore()
		broker := NewBroker()
		svc := NewOutboxPaymentService(store, clock)
		relay := NewOutboxRelay(store, broker, 10)
		ledger := NewLedgerConsumer(broker)
		broker.Down = true
		check(svc.Accept(payment("pay-4", "acme", "20.00", clock)))
		clock.Advance(5)
		check(svc.Accept(payment("pay-5", "acme", "35.00", clock)))
		line(fmt.Sprintf("  broker down; store has %d payments and %d pending outbox rows", store.PaymentCount(), store.UnpublishedCount()))
		for _, e := range store.OutboxRows() {
			line("    " + e.String())
		}
		n := must(relay.Drain(3))
		line(fmt.Sprintf("  relay polled %d times, delivered %d, failed attempts %d", relay.Polls, n, relay.FailedAttempts))
		broker.Down = false
		n = must(relay.Drain(3))
		must(ledger.Consume())
		line(fmt.Sprintf("  broker back; relay delivered %d; pending now %d", n, store.UnpublishedCount()))
		line(fmt.Sprintf("  ledger balance for acme = %s  (matches the store)", ledger.Balance("acme")))
	}

	line("\n== 4. Relay crashes after publish, before mark: at-least-once, and the consumer dedupes ==")
	{
		store := NewStore()
		broker := NewBroker()
		svc := NewOutboxPaymentService(store, clock)
		relay := NewOutboxRelay(store, broker, 10)
		ledger := NewLedgerConsumer(broker)
		check(svc.Accept(payment("pay-6", "globex", "100.00", clock)))
		check(svc.Accept(payment("pay-7", "globex", "1.00", clock)))
		relay.CrashPoint = AfterPublishBeforeMark
		if _, err := relay.Poll(); errors.Is(err, ErrCrash) {
			line("  relay: " + err.Error())
		}
		line(fmt.Sprintf("  broker has %d message(s); store still has %d pending rows", broker.Size(), store.UnpublishedCount()))
		must(relay.Drain(3))
		line(fmt.Sprintf("  relay restarted and drained; broker now has %d messages for 2 payments", broker.Size()))
		for _, m := range broker.Messages() {
			line(fmt.Sprintf("    offset %d %s", m.Offset, m.EventID))
		}
		must(ledger.Consume())
		line(fmt.Sprintf("  ledger applied %d, ignored %d duplicate; balance for globex = %s", ledger.AppliedCount(), ledger.DuplicatesIgnored, ledger.Balance("globex")))
	}

	line("\n== 5. Rollback drops the outbox row with the payment: no event for a payment that never existed ==")
	{
		store := NewStore()
		svc := NewOutboxPaymentService(store, clock)
		check(svc.AcceptThenFail(payment("pay-8", "acme", "999.00", clock)))
		line(fmt.Sprintf("  payments=%d outbox rows=%d rollbacks=%d", store.PaymentCount(), len(store.OutboxRows()), store.Rollbacks))
	}

	line("\n== 6. Ordering: outbox seq is commit order, and the relay publishes in that order per aggregate ==")
	{
		store := NewStore()
		broker := NewBroker()
		svc := NewOutboxPaymentService(store, clock)
		relay := NewOutboxRelay(store, broker, 2)
		for i := 1; i <= 5; i++ {
			check(svc.Accept(payment(fmt.Sprintf("pay-%d", 10+i), "acme", fmt.Sprintf("%d.00", i), clock)))
		}
		polls := 0
		for store.UnpublishedCount() > 0 {
			must(relay.Poll())
			polls++
		}
		order := []string{}
		for _, m := range broker.Messages() {
			order = append(order, strings.TrimPrefix(m.EventID, "evt-pay-"))
		}
		line(fmt.Sprintf("  batch size 2, %d polls, broker order: %s", polls, strings.Join(order, " ")))
	}
}
