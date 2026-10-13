// Lesson: phases/14-payments-at-scale/03-exactly-once-money-movement/docs/en.md
// Exactly-Once Money Movement End to End
// Run:  go run .
// Test: go test ./...
// Sources: Helland, "Idempotence Is Not a Medical Condition" (ACM Queue, 2012); Kleppmann, DDIA ch. 11;
//          Richardson, microservices.io "Transactional outbox"; Stripe docs "Idempotent requests".
// Go 1.22+, standard library only. Same design as the Java and Python versions. Money is integer minor units.
package main

import (
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
)

// ---------------------------------------------------------------- Money (Phase 10, Lesson 01) ----

// Currency is an ISO 4217 currency: a three-letter code and the number of decimals in its minor unit.
type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	EUR = Currency{"EUR", 2}
)

func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

// ErrCurrencyMismatch is returned when two amounts of different currencies are combined.
var ErrCurrencyMismatch = errors.New("currency mismatch")

// ErrOverflow is returned when an int64 would wrap. Money never silently wraps.
var ErrOverflow = errors.New("int64 overflow")

// Money is an exact amount of one currency stored as a signed count of minor units. Never a float.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func Zero(currency Currency) Money { return Money{0, currency} }

func (m Money) requireSameCurrency(other Money) error {
	if m.Currency != other.Currency {
		return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.Currency.Code, other.Currency.Code)
	}
	return nil
}

func addChecked(a, b int64) (int64, error) {
	sum := a + b
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum >= 0) {
		return 0, ErrOverflow
	}
	return sum, nil
}

func (m Money) Plus(other Money) (Money, error) {
	if err := m.requireSameCurrency(other); err != nil {
		return Money{}, err
	}
	sum, err := addChecked(m.MinorUnits, other.MinorUnits)
	return Money{sum, m.Currency}, err
}

func (m Money) Minus(other Money) (Money, error) {
	if err := m.requireSameCurrency(other); err != nil {
		return Money{}, err
	}
	diff, err := addChecked(m.MinorUnits, -other.MinorUnits)
	return Money{diff, m.Currency}, err
}

// MustPlus is Plus for the ledger hot path, where a mismatch or overflow is a programming error.
func (m Money) MustPlus(other Money) Money {
	sum, err := m.Plus(other)
	if err != nil {
		panic(err)
	}
	return sum
}

func (m Money) Negate() Money    { return Money{-m.MinorUnits, m.Currency} }
func (m Money) IsZero() bool     { return m.MinorUnits == 0 }
func (m Money) IsPositive() bool { return m.MinorUnits > 0 }

// String renders "19.99 USD", "-0.50 EUR".
func (m Money) String() string {
	magnitude := m.MinorUnits
	if magnitude < 0 {
		magnitude = -magnitude
	}
	scale := m.Currency.MinorUnitsPerMajor()
	text := fmt.Sprintf("%d", magnitude/scale)
	if m.Currency.Exponent > 0 {
		text += fmt.Sprintf(".%0*d", m.Currency.Exponent, magnitude%scale)
	}
	if m.MinorUnits < 0 {
		text = "-" + text
	}
	return text + " " + m.Currency.Code
}

// ---------------------------------------------------------------- Faults ----

// FaultPoint is where the harness may crash a component. Each point is a real place a process dies in production.
type FaultPoint int

const (
	ApiBeforeCommit      FaultPoint = iota // handler dies before the transaction commits: nothing is written
	ApiAfterCommit                         // transaction committed, response lost: the client will retry
	RelayAfterPublish                      // relay published to the bus but died before marking the row: a duplicate will follow
	BusDuplicate                           // the bus delivers the same message twice
	BusReorder                             // the bus delivers a batch out of order
	ConsumerBeforeCommit                   // consumer died mid-handle: nothing written, the message is redelivered
	ConsumerAfterCommit                    // consumer committed but died before acknowledging: redelivered anyway
	numFaultPoints
)

var faultPointNames = [...]string{"API_BEFORE_COMMIT", "API_AFTER_COMMIT", "RELAY_AFTER_PUBLISH", "BUS_DUPLICATE",
	"BUS_REORDER", "CONSUMER_BEFORE_COMMIT", "CONSUMER_AFTER_COMMIT"}

func (p FaultPoint) String() string { return faultPointNames[p] }

// AllFaultPoints lists every point in declaration order.
func AllFaultPoints() []FaultPoint {
	out := make([]FaultPoint, numFaultPoints)
	for i := range out {
		out[i] = FaultPoint(i)
	}
	return out
}

// CrashError is how a component "dies": it unwinds to the caller, which models the client's timeout or the
// supervisor's restart. It is a value so that `errors.As` can name the fault point.
type CrashError struct{ Point FaultPoint }

func (c *CrashError) Error() string { return "crash at " + c.Point.String() }

// IsCrash reports whether err is a CrashError (at any wrapping depth).
func IsCrash(err error) bool {
	var c *CrashError
	return errors.As(err, &c)
}

// FaultInjector is the only source of randomness in the pipeline, so every run is replayable by seed.
type FaultInjector struct {
	Random *rand.Rand
	rates  map[FaultPoint]float64
	fired  map[FaultPoint]int
	armed  bool
}

func NewFaultInjector(seed int64, rates map[FaultPoint]float64) *FaultInjector {
	copied := make(map[FaultPoint]float64, len(rates))
	for k, v := range rates {
		copied[k] = v
	}
	return &FaultInjector{Random: rand.New(rand.NewSource(seed)), rates: copied, fired: map[FaultPoint]int{}, armed: true}
}

func NoFaults() *FaultInjector { return NewFaultInjector(0, nil) }

// Uniform gives every fault point the same probability.
func Uniform(rate float64) map[FaultPoint]float64 {
	m := map[FaultPoint]float64{}
	for _, p := range AllFaultPoints() {
		m[p] = rate
	}
	return m
}

// RandomSchedule draws a fault schedule from a seed: each point gets its own rate in [0, 0.25).
func RandomSchedule(seed int64) map[FaultPoint]float64 {
	r := rand.New(rand.NewSource(seed ^ 0x5DEECE66D))
	m := map[FaultPoint]float64{}
	for _, p := range AllFaultPoints() {
		m[p] = float64(r.Intn(25)) / 100.0
	}
	return m
}

func (f *FaultInjector) Fire(point FaultPoint) bool {
	if !f.armed {
		return false
	}
	rate := f.rates[point]
	if rate <= 0 {
		return false
	}
	hit := f.Random.Float64() < rate
	if hit {
		f.fired[point]++
	}
	return hit
}

// Disarm stops injecting faults so a run can drain to quiescence; counts are kept.
func (f *FaultInjector) Disarm() { f.armed = false }

func (f *FaultInjector) Count(point FaultPoint) int { return f.fired[point] }

func (f *FaultInjector) TotalFired() int {
	total := 0
	for _, n := range f.fired {
		total += n
	}
	return total
}

// ---------------------------------------------------------------- Defences ----

// Defences are the three mechanisms that together give exactly-once effects. Each is a flag so the harness can
// remove it and show which invariant breaks. No two of them cover the same seam.
type Defences struct {
	ApiDedupe      bool
	AtomicOutbox   bool
	ConsumerDedupe bool
}

func AllDefences() Defences { return Defences{true, true, true} }

func (d Defences) Without(name string) (Defences, error) {
	switch name {
	case "apiDedupe":
		d.ApiDedupe = false
	case "atomicOutbox":
		d.AtomicOutbox = false
	case "consumerDedupe":
		d.ConsumerDedupe = false
	default:
		return d, fmt.Errorf("unknown defence %s", name)
	}
	return d, nil
}

// ---------------------------------------------------------------- Ledger (both sides) ----

// Posting is signed: positive debits the account, negative credits it.
type Posting struct {
	Account string
	Amount  Money
}

type JournalEntry struct {
	EntryID  string
	Postings []Posting
}

// Transfer builds a balanced two-posting entry. The amount must be positive.
func Transfer(entryID, debitAccount, creditAccount string, amount Money) (JournalEntry, error) {
	if !amount.IsPositive() {
		return JournalEntry{}, fmt.Errorf("transfer amount must be positive: %v", amount)
	}
	return JournalEntry{entryID, []Posting{{debitAccount, amount}, {creditAccount, amount.Negate()}}}, nil
}

func (e JournalEntry) Balanced() bool {
	sum := Zero(e.Postings[0].Amount.Currency)
	for _, p := range e.Postings {
		sum = sum.MustPlus(p.Amount)
	}
	return sum.IsZero()
}

// Ledger is an append-only double-entry ledger. It checks that every entry balances; it does NOT check
// entry-id uniqueness, because dedupe is the job of the layer in front of the ledger (see Exercise 1).
type Ledger struct {
	Journal     []JournalEntry
	entryCounts map[string]int
	balances    map[string]Money
	currency    Currency
}

func NewLedger(currency Currency) *Ledger {
	return &Ledger{entryCounts: map[string]int{}, balances: map[string]Money{}, currency: currency}
}

func (l *Ledger) Post(entry JournalEntry) error {
	if !entry.Balanced() {
		return fmt.Errorf("unbalanced entry %s", entry.EntryID)
	}
	l.Journal = append(l.Journal, entry)
	l.entryCounts[entry.EntryID]++
	for _, p := range entry.Postings {
		l.balances[p.Account] = l.Balance(p.Account).MustPlus(p.Amount)
	}
	return nil
}

func (l *Ledger) Balance(account string) Money {
	if m, ok := l.balances[account]; ok {
		return m
	}
	return Zero(l.currency)
}

// TrialBalance sums every balance; debits positive, credits negative, so a correct ledger is always zero.
func (l *Ledger) TrialBalance() Money {
	sum := Zero(l.currency)
	for _, m := range l.balances {
		sum = sum.MustPlus(m)
	}
	return sum
}

// CreditTotal is the sum of the credit balances of every account whose name starts with prefix, as a positive amount.
func (l *Ledger) CreditTotal(prefix string) Money {
	sum := Zero(l.currency)
	for account, m := range l.balances {
		if strings.HasPrefix(account, prefix) {
			sum = sum.MustPlus(m.Negate())
		}
	}
	return sum
}

func (l *Ledger) CountEntries(entryID string) int { return l.entryCounts[entryID] }

// ---------------------------------------------------------------- Gateway side ----

type PaymentRequest struct {
	IdempotencyKey string
	CustomerID     string
	MerchantID     string
	Amount         Money
}

// Fingerprint is what must be identical for two requests with the same key to be "the same request".
func (r PaymentRequest) Fingerprint() string {
	return r.CustomerID + "|" + r.MerchantID + "|" + r.Amount.String()
}

type Payment struct {
	PaymentID      string
	IdempotencyKey string
	CustomerID     string
	MerchantID     string
	Amount         Money
}

func (p Payment) Fingerprint() string { return p.CustomerID + "|" + p.MerchantID + "|" + p.Amount.String() }

// PaymentEvent is written to the outbox in the same transaction as the payment and later carried by the bus.
type PaymentEvent struct {
	EventID    string
	PaymentID  string
	CustomerID string
	MerchantID string
	Amount     Money
}

func Accepted(p Payment) PaymentEvent {
	return PaymentEvent{"evt-" + p.PaymentID, p.PaymentID, p.CustomerID, p.MerchantID, p.Amount}
}

type OutboxRow struct {
	Event     PaymentEvent
	Published bool
}

// ErrIdempotencyConflict: the same key was reused with a different request body.
var ErrIdempotencyConflict = errors.New("idempotency key reused with a different request")

const (
	Receivable         = "asset:receivable:"          // + customerID (gateway books)
	GatewayClearing    = "liability:clearing:acquirer" // owed onward to the downstream ledger
	DownstreamClearing = "asset:clearing:acquirer"     // what the gateway owes us (downstream books)
	Payable            = "liability:payable:"          // + merchantID (downstream books)
)

// GatewayStore is an in-memory stand-in for the gateway's database. The only thing it models faithfully is
// atomicity: Transaction applies every write or none, and a crash is only possible strictly before or
// strictly after the commit, never half-way through.
type GatewayStore struct {
	Payments        map[string]Payment   // paymentID -> payment
	IdempotencyKeys map[string]string    // key -> paymentID (unique index)
	Outbox          map[string]OutboxRow // eventID -> row
	outboxOrder     []string             // insertion order of eventIDs (Go maps are unordered)
	Ledger          *Ledger
	Commits         int
}

func NewGatewayStore() *GatewayStore {
	return &GatewayStore{Payments: map[string]Payment{}, IdempotencyKeys: map[string]string{},
		Outbox: map[string]OutboxRow{}, Ledger: NewLedger(USD)}
}

func (s *GatewayStore) Transaction(faults *FaultInjector, before, after FaultPoint, hasBefore, hasAfter bool, writes func()) error {
	if hasBefore && faults.Fire(before) {
		return &CrashError{before}
	}
	writes()
	s.Commits++
	if hasAfter && faults.Fire(after) {
		return &CrashError{after}
	}
	return nil
}

func (s *GatewayStore) putOutbox(row OutboxRow) {
	if _, exists := s.Outbox[row.Event.EventID]; !exists {
		s.outboxOrder = append(s.outboxOrder, row.Event.EventID)
	}
	s.Outbox[row.Event.EventID] = row
}

// Unpublished returns outbox rows not yet marked published, in insertion order.
func (s *GatewayStore) Unpublished() []PaymentEvent {
	var out []PaymentEvent
	for _, id := range s.outboxOrder {
		if row := s.Outbox[id]; !row.Published {
			out = append(out, row.Event)
		}
	}
	return out
}

// PaymentIDs returns payment ids in creation order (the sequence numbers sort lexicographically).
func (s *GatewayStore) PaymentIDs() []string {
	ids := make([]string, 0, len(s.Payments))
	for id := range s.Payments {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

type Response struct {
	Payment  Payment
	Replayed bool
}

// PaymentApi is the idempotent handler: look up the key, else write payment + key + ledger entry + outbox row
// in one transaction.
type PaymentApi struct {
	store    *GatewayStore
	faults   *FaultInjector
	defences Defences
	sequence int
}

func NewPaymentApi(store *GatewayStore, faults *FaultInjector, defences Defences) *PaymentApi {
	return &PaymentApi{store: store, faults: faults, defences: defences}
}

func (a *PaymentApi) CreatePayment(request PaymentRequest) (Response, error) {
	store := a.store
	if a.defences.ApiDedupe {
		if existingID, ok := store.IdempotencyKeys[request.IdempotencyKey]; ok {
			existing := store.Payments[existingID]
			if existing.Fingerprint() != request.Fingerprint() {
				return Response{}, fmt.Errorf("%w: %s", ErrIdempotencyConflict, request.IdempotencyKey)
			}
			return Response{existing, true}, nil
		}
	}
	a.sequence++
	paymentID := fmt.Sprintf("pay-%06d", a.sequence)
	payment := Payment{paymentID, request.IdempotencyKey, request.CustomerID, request.MerchantID, request.Amount}
	event := Accepted(payment)
	entry, err := Transfer(paymentID, Receivable+request.CustomerID, GatewayClearing, request.Amount)
	if err != nil {
		return Response{}, err
	}
	record := func() {
		store.Payments[paymentID] = payment
		store.IdempotencyKeys[request.IdempotencyKey] = paymentID
		if err := store.Ledger.Post(entry); err != nil {
			panic(err) // the entry was built balanced; this cannot happen
		}
	}
	enqueue := func() { store.putOutbox(OutboxRow{event, false}) }
	if a.defences.AtomicOutbox {
		if err := store.Transaction(a.faults, ApiBeforeCommit, ApiAfterCommit, true, true, func() { record(); enqueue() }); err != nil {
			return Response{}, err
		}
	} else {
		// The dual-write anti-pattern: record, then enqueue in a second transaction. A crash between them
		// (ApiAfterCommit) leaves a payment that no downstream system will ever hear about.
		if err := store.Transaction(a.faults, ApiBeforeCommit, ApiAfterCommit, true, true, record); err != nil {
			return Response{}, err
		}
		if err := store.Transaction(a.faults, 0, 0, false, false, enqueue); err != nil {
			return Response{}, err
		}
	}
	return Response{payment, false}, nil
}

// OutboxRelay publishes unpublished outbox rows to the bus. At-least-once: a crash between publish and mark republishes.
type OutboxRelay struct {
	store  *GatewayStore
	bus    *Bus
	faults *FaultInjector
}

func NewOutboxRelay(store *GatewayStore, bus *Bus, faults *FaultInjector) *OutboxRelay {
	return &OutboxRelay{store, bus, faults}
}

func (r *OutboxRelay) RelayOnce() (int, error) {
	published := 0
	for _, event := range r.store.Unpublished() {
		r.bus.Publish(event)
		published++
		if r.faults.Fire(RelayAfterPublish) {
			return published, &CrashError{RelayAfterPublish}
		}
		event := event
		if err := r.store.Transaction(r.faults, 0, 0, false, false, func() { r.store.putOutbox(OutboxRow{event, true}) }); err != nil {
			return published, err
		}
	}
	return published, nil
}

// ---------------------------------------------------------------- Bus ----

type EventHandler func(event PaymentEvent) error

// Bus is an in-memory at-least-once bus: may duplicate on publish, may reorder a batch, redelivers on consumer crash.
type Bus struct {
	queue                                                        []PaymentEvent
	faults                                                       *FaultInjector
	Published, Duplicated, Delivered, Redelivered, ReorderedBatches int
}

func NewBus(faults *FaultInjector) *Bus { return &Bus{faults: faults} }

func (b *Bus) Publish(event PaymentEvent) {
	b.queue = append(b.queue, event)
	b.Published++
	if b.faults.Fire(BusDuplicate) {
		b.queue = append(b.queue, event)
		b.Duplicated++
	}
}

func (b *Bus) Deliver(handler EventHandler, batchSize int) (int, error) {
	n := batchSize
	if n > len(b.queue) {
		n = len(b.queue)
	}
	batch := make([]PaymentEvent, n)
	copy(batch, b.queue[:n])
	b.queue = b.queue[n:]
	if len(batch) > 1 && b.faults.Fire(BusReorder) {
		b.faults.Random.Shuffle(len(batch), func(i, j int) { batch[i], batch[j] = batch[j], batch[i] })
		b.ReorderedBatches++
	}
	ok := 0
	for i, event := range batch {
		if err := handler(event); err != nil {
			if IsCrash(err) {
				// nothing from this message on was acknowledged: put the rest back, in order, for redelivery
				b.queue = append(append([]PaymentEvent{}, batch[i:]...), b.queue...)
				b.Redelivered++
			}
			return ok, err
		}
		b.Delivered++
		ok++
	}
	return ok, nil
}

func (b *Bus) Pending() int { return len(b.queue) }

// ---------------------------------------------------------------- Downstream ledger side ----

// LedgerStore is the consumer's database: processed-message inbox and the downstream ledger, written in one transaction.
type LedgerStore struct {
	Inbox   map[string]bool
	Ledger  *Ledger
	Commits int
}

func NewLedgerStore() *LedgerStore { return &LedgerStore{Inbox: map[string]bool{}, Ledger: NewLedger(USD)} }

func (s *LedgerStore) Transaction(faults *FaultInjector, before, after FaultPoint, writes func()) error {
	if faults.Fire(before) {
		return &CrashError{before}
	}
	writes()
	s.Commits++
	if faults.Fire(after) {
		return &CrashError{after}
	}
	return nil
}

// LedgerConsumer dedupes by event id (inbox pattern), then posts to the downstream ledger with entryID == eventID.
type LedgerConsumer struct {
	store             *LedgerStore
	faults            *FaultInjector
	defences          Defences
	SkippedDuplicates int
}

func NewLedgerConsumer(store *LedgerStore, faults *FaultInjector, defences Defences) *LedgerConsumer {
	return &LedgerConsumer{store: store, faults: faults, defences: defences}
}

func (c *LedgerConsumer) Handle(event PaymentEvent) error {
	if c.defences.ConsumerDedupe && c.store.Inbox[event.EventID] {
		c.SkippedDuplicates++
		return nil
	}
	entry, err := Transfer(event.EventID, DownstreamClearing, Payable+event.MerchantID, event.Amount)
	if err != nil {
		return err
	}
	return c.store.Transaction(c.faults, ConsumerBeforeCommit, ConsumerAfterCommit, func() {
		c.store.Inbox[event.EventID] = true
		if err := c.store.Ledger.Post(entry); err != nil {
			panic(err)
		}
	})
}

// ---------------------------------------------------------------- Harness ----

type HarnessConfig struct {
	Seed       int64
	Operations int
	Rates      map[FaultPoint]float64
	Defences   Defences
}

func StandardConfig(seed int64, operations int) HarnessConfig {
	return HarnessConfig{seed, operations, Uniform(0.08), AllDefences()}
}

func RandomScheduleConfig(seed int64, operations int) HarnessConfig {
	return HarnessConfig{seed, operations, RandomSchedule(seed), AllDefences()}
}

func (c HarnessConfig) Without(defence string) HarnessConfig {
	d, err := c.Defences.Without(defence)
	if err != nil {
		panic(err)
	}
	c.Defences = d
	return c
}

type HarnessReport struct {
	Seed                                                      int64
	ClientRequests, ApiCalls, Crashes, AcceptedPayments        int
	BusPublished, BusDuplicated, BusRedelivered               int
	GatewayEntries, DownstreamEntries, ConsumerSkipped        int
	GatewayTrialBalance, DownstreamTrialBalance               Money
	Violations                                                []string
}

func (r HarnessReport) Passed() bool { return len(r.Violations) == 0 }

// FirstBrokenInvariant is the invariant named in the first violation, e.g. "one-payment-per-key"; empty on a pass.
func (r HarnessReport) FirstBrokenInvariant() string {
	if len(r.Violations) == 0 {
		return ""
	}
	v := r.Violations[0]
	return v[1:strings.IndexByte(v, ']')]
}

func (r HarnessReport) Summary() string {
	return fmt.Sprintf("seed=%d requests=%d apiCalls=%d crashes=%d accepted=%d published=%d dup=%d redelivered=%d"+
		" gatewayEntries=%d downstreamEntries=%d skipped=%d trial=%v/%v violations=%d",
		r.Seed, r.ClientRequests, r.ApiCalls, r.Crashes, r.AcceptedPayments, r.BusPublished, r.BusDuplicated, r.BusRedelivered,
		r.GatewayEntries, r.DownstreamEntries, r.ConsumerSkipped, r.GatewayTrialBalance, r.DownstreamTrialBalance, len(r.Violations))
}

const MaxClientAttempts = 12

func step(err error) int {
	if err == nil {
		return 0
	}
	if IsCrash(err) {
		return 1
	}
	panic(err)
}

// RunHarness drives thousands of operations through the pipeline under injected faults and checks the invariants.
func RunHarness(cfg HarnessConfig) HarnessReport {
	faults := NewFaultInjector(cfg.Seed, cfg.Rates)
	workload := rand.New(rand.NewSource(cfg.Seed*1_000_003 + 17))
	gateway := NewGatewayStore()
	api := NewPaymentApi(gateway, faults, cfg.Defences)
	bus := NewBus(faults)
	relay := NewOutboxRelay(gateway, bus, faults)
	downstream := NewLedgerStore()
	consumer := NewLedgerConsumer(downstream, faults, cfg.Defences)

	var issued []PaymentRequest             // every distinct request a client ever made
	acknowledged := map[string]Payment{}    // key -> payment the client saw
	apiCalls, crashes, clientRequests := 0, 0, 0

	for i := 0; i < cfg.Operations; i++ {
		var request PaymentRequest
		if len(issued) > 0 && workload.Intn(100) < 15 {
			request = issued[workload.Intn(len(issued))] // a client re-sends an old key (retry storm)
		} else {
			amount := OfMinor(int64(100+workload.Intn(20_000)), USD)
			request = PaymentRequest{fmt.Sprintf("key-%d", len(issued)), fmt.Sprintf("cust-%d", workload.Intn(50)),
				fmt.Sprintf("merch-%d", workload.Intn(8)), amount}
			issued = append(issued, request)
		}
		clientRequests++
		for attempt := 0; attempt < MaxClientAttempts; attempt++ {
			apiCalls++
			resp, err := api.CreatePayment(request)
			if err == nil {
				acknowledged[request.IdempotencyKey] = resp.Payment
				break
			}
			if !IsCrash(err) {
				panic(err)
			}
			crashes++ // the client saw a timeout and retries with the same key
		}
		if i%3 == 0 {
			_, err := relay.RelayOnce()
			crashes += step(err)
		}
		if i%5 == 0 {
			_, err := bus.Deliver(consumer.Handle, 8)
			crashes += step(err)
		}
	}

	faults.Disarm()
	for len(gateway.Unpublished()) > 0 || bus.Pending() > 0 {
		if _, err := relay.RelayOnce(); err != nil {
			panic(err)
		}
		if _, err := bus.Deliver(consumer.Handle, 64); err != nil {
			panic(err)
		}
	}

	violations := CheckInvariants(gateway, downstream.Ledger, issued, acknowledged)
	return HarnessReport{cfg.Seed, clientRequests, apiCalls, crashes, len(gateway.Payments), bus.Published,
		bus.Duplicated, bus.Redelivered, len(gateway.Ledger.Journal), len(downstream.Ledger.Journal),
		consumer.SkippedDuplicates, gateway.Ledger.TrialBalance(), downstream.Ledger.TrialBalance(), violations}
}

// CheckInvariants returns one "[invariant-name] detail" string per violation so a failing test can say which layer is missing.
func CheckInvariants(gateway *GatewayStore, downstream *Ledger, issued []PaymentRequest, acknowledged map[string]Payment) []string {
	var violations []string
	books := gateway.Ledger
	paymentIDs := gateway.PaymentIDs()

	// 1. One payment per idempotency key, and the payment the client was told about is the stored one.
	perKey := map[string]int{}
	for _, id := range paymentIDs {
		perKey[gateway.Payments[id].IdempotencyKey]++
	}
	for _, key := range sortedKeys(perKey) {
		if perKey[key] != 1 {
			violations = append(violations, fmt.Sprintf("[one-payment-per-key] %s has %d payments", key, perKey[key]))
		}
	}
	ackKeys := make([]string, 0, len(acknowledged))
	for k := range acknowledged {
		ackKeys = append(ackKeys, k)
	}
	sort.Strings(ackKeys)
	for _, key := range ackKeys {
		if stored := gateway.IdempotencyKeys[key]; stored != acknowledged[key].PaymentID {
			violations = append(violations, fmt.Sprintf("[one-payment-per-key] %s acknowledged %s but store has %s", key, acknowledged[key].PaymentID, stored))
		}
	}

	// 2. Atomicity of record + enqueue: a payment never exists without its outbox row.
	for _, id := range paymentIDs {
		if _, ok := gateway.Outbox["evt-"+id]; !ok {
			violations = append(violations, fmt.Sprintf("[payment-has-outbox-row] %s has no outbox row", id))
		}
	}

	// 3. Every accepted payment is posted exactly once in each ledger; nothing else is posted.
	for _, id := range paymentIDs {
		if g := books.CountEntries(id); g != 1 {
			violations = append(violations, fmt.Sprintf("[posted-once-gateway] %s posted %d times", id, g))
		}
		if d := downstream.CountEntries("evt-" + id); d != 1 {
			violations = append(violations, fmt.Sprintf("[posted-once-downstream] %s posted %d times downstream", id, d))
		}
	}
	if len(books.Journal) != len(gateway.Payments) {
		violations = append(violations, fmt.Sprintf("[posted-once-gateway] gatewayEntries=%d payments=%d", len(books.Journal), len(gateway.Payments)))
	}
	if len(downstream.Journal) != len(gateway.Payments) {
		violations = append(violations, fmt.Sprintf("[posted-once-downstream] downstreamEntries=%d payments=%d", len(downstream.Journal), len(gateway.Payments)))
	}

	// 4. Money is conserved: both trial balances are zero and the clearing amounts agree with the accepted total.
	if !books.TrialBalance().IsZero() {
		violations = append(violations, fmt.Sprintf("[trial-balance] gateway %v", books.TrialBalance()))
	}
	if !downstream.TrialBalance().IsZero() {
		violations = append(violations, fmt.Sprintf("[trial-balance] downstream %v", downstream.TrialBalance()))
	}
	accepted := Zero(USD)
	for _, id := range paymentIDs {
		accepted = accepted.MustPlus(gateway.Payments[id].Amount)
	}
	if gatewayClearing := books.CreditTotal(GatewayClearing); gatewayClearing != accepted {
		violations = append(violations, fmt.Sprintf("[money-conserved] gateway clearing %v != accepted %v", gatewayClearing, accepted))
	}
	if payables := downstream.CreditTotal(Payable); payables != accepted {
		violations = append(violations, fmt.Sprintf("[money-conserved] merchant payables %v != accepted %v", payables, accepted))
	}

	// 5. Per customer, the receivable equals the sum of that customer's distinct acknowledged requests.
	intended := map[string]Money{}
	for _, r := range issued {
		if _, ok := acknowledged[r.IdempotencyKey]; ok {
			cur, ok := intended[r.CustomerID]
			if !ok {
				cur = Zero(USD)
			}
			intended[r.CustomerID] = cur.MustPlus(r.Amount)
		}
	}
	for _, customer := range sortedMoneyKeys(intended) {
		if receivable := books.Balance(Receivable + customer); receivable != intended[customer] {
			violations = append(violations, fmt.Sprintf("[charged-what-was-asked] %s intended %v receivable %v", customer, intended[customer], receivable))
		}
	}
	return violations
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedMoneyKeys(m map[string]Money) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------- Demo ----

func main() {
	fmt.Println("== One payment, a crash at every seam, exactly one posting in each ledger ==")
	faults := NewFaultInjector(7, map[FaultPoint]float64{ApiAfterCommit: 1.0})
	gateway := NewGatewayStore()
	api := NewPaymentApi(gateway, faults, AllDefences())
	request := PaymentRequest{"key-1", "cust-7", "merch-2", OfMinor(1999, USD)}
	if _, err := api.CreatePayment(request); err != nil {
		fmt.Printf("attempt 1: %v  (payment + ledger entry + outbox row committed, client saw a timeout)\n", err)
	}
	faults.Disarm()
	retry, _ := api.CreatePayment(request)
	fmt.Printf("attempt 2: replayed=%v paymentId=%s  payments=%d gatewayEntries=%d outboxRows=%d\n",
		retry.Replayed, retry.Payment.PaymentID, len(gateway.Payments), len(gateway.Ledger.Journal), len(gateway.Outbox))

	faults = NewFaultInjector(7, map[FaultPoint]float64{RelayAfterPublish: 1.0})
	bus := NewBus(faults)
	relay := NewOutboxRelay(gateway, bus, faults)
	if _, err := relay.RelayOnce(); err != nil {
		fmt.Printf("relay 1: %v  (event on the bus, row still unpublished)\n", err)
	}
	faults.Disarm()
	relay.RelayOnce()
	fmt.Printf("relay 2: bus now holds %d copies of evt-%s\n", bus.Pending(), retry.Payment.PaymentID)

	downstream := NewLedgerStore()
	consumer := NewLedgerConsumer(downstream, NoFaults(), AllDefences())
	bus.Deliver(consumer.Handle, 10)
	fmt.Printf("consumer: delivered=%d skippedDuplicates=%d downstreamEntries=%d trialBalance=%v\n",
		bus.Delivered, consumer.SkippedDuplicates, len(downstream.Ledger.Journal), downstream.Ledger.TrialBalance())
	fmt.Printf("  gateway:    %scust-7 = %v, %s = %v\n", Receivable, gateway.Ledger.Balance(Receivable+"cust-7"),
		GatewayClearing, gateway.Ledger.Balance(GatewayClearing))
	fmt.Printf("  downstream: %s = %v, %smerch-2 = %v\n", DownstreamClearing, downstream.Ledger.Balance(DownstreamClearing),
		Payable, downstream.Ledger.Balance(Payable+"merch-2"))

	fmt.Println("\n== Seeded fault-injection harness: 3000 operations, 8% fault rate at every seam ==")
	for seed := int64(1); seed <= 3; seed++ {
		report := RunHarness(StandardConfig(seed, 3000))
		verdict := "FAIL "
		if report.Passed() {
			verdict = "PASS "
		}
		fmt.Println(verdict + report.Summary())
	}

	fmt.Println("\n== Remove one defence at a time: the harness names the invariant that breaks ==")
	for _, defence := range []string{"apiDedupe", "atomicOutbox", "consumerDedupe"} {
		broken := RunHarness(StandardConfig(1, 3000).Without(defence))
		verdict, outcome := "FAIL", "(still holds)"
		if broken.Passed() {
			verdict = "PASS"
		} else {
			outcome = broken.Violations[0]
		}
		fmt.Printf("%s without %-14s -> %s\n", verdict, defence, outcome)
	}
}
