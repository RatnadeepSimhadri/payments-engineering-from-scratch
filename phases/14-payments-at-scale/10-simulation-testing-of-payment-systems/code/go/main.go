// Lesson: phases/14-payments-at-scale/10-simulation-testing-of-payment-systems/docs/en.md
// Deterministic Simulation Testing of Payment Systems
// Run:  go run .
// Test: go test ./...
// Sources: Steele, Lea, Flood, "Fast Splittable Pseudorandom Number Generators" (OOPSLA 2014) for SplitMix64;
//
//	FoundationDB "Simulation and Testing" docs; TigerBeetle VOPR design notes; FIPS 180-4 (SHA-256) for the trace fingerprint.
//
// Go 1.22+, standard library only. Money is integer minor units (Phase 10, Lesson 01). No wall clock, no goroutines.
// SplitMix64 and integer-only probabilities mean the same seed replays the Java lesson's trace to the same fingerprint.
package main

import (
	"container/heap"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"sort"
	"strings"
)

// ---------------------------------------------------------------- Deterministic primitives ----

// Rng is SplitMix64. Chosen over java.util.Random because the algorithm is six lines and trivial to port, so a seed
// produces the identical event sequence in every implementation of this lesson. All arithmetic is on 64-bit words
// with wrap-around; Go's uint64 does that natively and >> on an unsigned word is the logical shift Java spells >>>.
type Rng struct {
	state uint64
	Draws int64
}

func NewRng(seed uint64) *Rng { return &Rng{state: seed} }

func (r *Rng) NextLong() uint64 {
	r.Draws++
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// NextInt is uniform in [0, bound). Uses the top 32 bits so the result does not depend on signed-modulo semantics.
// A non-positive bound is a programming error and panics, as the Java throws IllegalArgumentException.
func (r *Rng) NextInt(bound int) int {
	if bound <= 0 {
		panic("bound must be positive")
	}
	return int((r.NextLong() >> 32) % uint64(bound))
}

// NextLongBounded is uniform in [0, bound) for bounds beyond 32 bits (fault start times over long horizons).
func (r *Rng) NextLongBounded(bound int64) int64 {
	if bound <= 0 {
		panic("bound must be positive")
	}
	return int64(r.NextLong() % uint64(bound))
}

// Between is uniform in [lo, hi].
func (r *Rng) Between(lo, hi int) int { return lo + r.NextInt(hi-lo+1) }

// Chance is true with probability basisPoints / 10000. Integer probabilities: no float compare, no rounding.
func (r *Rng) Chance(basisPoints int) bool { return r.NextInt(10_000) < basisPoints }

// Split is a statistically independent child stream; used so adding a component never shifts another's draws.
func (r *Rng) Split() *Rng { return NewRng(r.NextLong()) }

// Fingerprint is SHA-256 over the processed-event trace, one line per event ("time label\n"). Two runs with the
// same fingerprint executed the same events in the same order; every port must produce the same fingerprint for
// the same seed.
type Fingerprint struct {
	digest hash.Hash
	Count  int
}

func NewFingerprint() *Fingerprint { return &Fingerprint{digest: sha256.New()} }

func (f *Fingerprint) Add(line string) {
	f.digest.Write([]byte(line))
	f.digest.Write([]byte{'\n'})
	f.Count++
}

// Hex is the first 16 hex digits of the digest: enough to compare runs, short enough to paste into a bug report.
// hash.Hash.Sum does not disturb the running state, so no clone is needed.
func (f *Fingerprint) Hex() string { return hex.EncodeToString(f.digest.Sum(nil)[:8]) }

type Event struct {
	Time   int64
	Seq    int64
	Label  string
	Action func()
}

type eventHeap []*Event

func (h eventHeap) Len() int { return len(h) }
func (h eventHeap) Less(i, j int) bool {
	if h[i].Time != h[j].Time {
		return h[i].Time < h[j].Time
	}
	return h[i].Seq < h[j].Seq
}
func (h eventHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *eventHeap) Push(x any)   { *h = append(*h, x.(*Event)) }
func (h *eventHeap) Pop() any     { old := *h; e := old[len(old)-1]; *h = old[:len(old)-1]; return e }
func (h eventHeap) peek() *Event  { return h[0] }

// ErrNoQuiescence is returned when the drain budget runs out: an accidental infinite loop became a test failure.
var ErrNoQuiescence = errors.New("simulation did not reach quiescence")

// Simulator is the virtual clock and the event loop in one object: time is whatever the next event says it is.
// Events are ordered by (time, seq) so two events at the same instant run in the order they were scheduled.
// Nothing in the lesson reads time.Now or sleeps.
type Simulator struct {
	queue     eventHeap
	seq       int64
	Now       int64
	Processed int64
	Trace     *Fingerprint
}

func NewSimulator() *Simulator { return &Simulator{Trace: NewFingerprint()} }

// Schedule runs action after delayMs of virtual time. A negative delay is a programming error and panics.
func (s *Simulator) Schedule(delayMs int64, label string, action func()) {
	if delayMs < 0 {
		panic("delay must be non-negative")
	}
	heap.Push(&s.queue, &Event{s.Now + delayMs, s.seq, label, action})
	s.seq++
}

func (s *Simulator) Step() bool {
	if len(s.queue) == 0 {
		return false
	}
	e := heap.Pop(&s.queue).(*Event)
	s.Now = e.Time
	s.Processed++
	s.Trace.Add(fmt.Sprintf("%d %s", s.Now, e.Label))
	e.Action()
	return true
}

func (s *Simulator) RunUntil(timeMs int64) {
	for len(s.queue) > 0 && s.queue.peek().Time <= timeMs {
		s.Step()
	}
	if s.Now < timeMs {
		s.Now = timeMs
	}
}

// RunUntilIdle drains every remaining event; the guard turns an accidental infinite loop into an error.
func (s *Simulator) RunUntilIdle(maxEvents int64) error {
	budget := maxEvents
	for s.Step() {
		budget--
		if budget < 0 {
			return ErrNoQuiescence
		}
	}
	return nil
}

func (s *Simulator) Pending() int { return len(s.queue) }

type NetworkConfig struct {
	MinLatencyMs    int
	MaxLatencyMs    int
	DropBasisPoints int
}

var LAN = NetworkConfig{20, 200, 100} // 1% loss

// Network is the simulated network: every message takes a seeded latency, may be dropped, and is blocked while
// its link is partitioned.
type Network struct {
	sim         *Simulator
	rng         *Rng
	cfg         NetworkConfig
	partitioned map[string]bool
	Sent        int
	Delivered   int
	Dropped     int
	Blocked     int
}

func NewNetwork(sim *Simulator, rng *Rng, cfg NetworkConfig) *Network {
	return &Network{sim: sim, rng: rng, cfg: cfg, partitioned: map[string]bool{}}
}

func (n *Network) Send(from, to, label string, deliver func()) {
	n.Sent++
	if n.partitioned[from+">"+to] {
		n.Blocked++
		return
	}
	if n.rng.Chance(n.cfg.DropBasisPoints) {
		n.Dropped++
		return
	}
	latency := n.rng.Between(n.cfg.MinLatencyMs, n.cfg.MaxLatencyMs)
	n.sim.Schedule(int64(latency), "deliver "+to+"<-"+from+" "+label, func() { n.Delivered++; deliver() })
}

func (n *Network) Partition(a, b string) {
	n.partitioned[a+">"+b] = true
	n.partitioned[b+">"+a] = true
}

func (n *Network) Heal(a, b string) { delete(n.partitioned, a+">"+b); delete(n.partitioned, b+">"+a) }

func (n *Network) IsPartitioned(a, b string) bool { return n.partitioned[a+">"+b] }

// ---------------------------------------------------------------- Faults ----

type FaultKind int

const (
	PartitionClientGateway FaultKind = iota
	PartitionGatewayProcessor
	ProcessorOutage
	ProcessorSlow
	GatewayCrash
)

var faultKindNames = [...]string{"PARTITION_CLIENT_GATEWAY", "PARTITION_GATEWAY_PROCESSOR", "PROCESSOR_OUTAGE", "PROCESSOR_SLOW", "GATEWAY_CRASH"}

func (k FaultKind) String() string { return faultKindNames[k] }

type Fault struct {
	StartMs int64
	EndMs   int64
	Kind    FaultKind
}

const Minute = int64(60_000)

// DrawFault: durations are kind-specific and drawn from the fault stream, so one fault more or less never shifts
// the workload.
func DrawFault(rng *Rng, horizonMs int64) Fault {
	kind := FaultKind(rng.NextInt(len(faultKindNames)))
	start := rng.NextLongBounded(horizonMs)
	var duration int64
	switch kind {
	case PartitionClientGateway, PartitionGatewayProcessor:
		duration = int64(rng.Between(5, 120)) * 1000
	case ProcessorOutage:
		duration = int64(rng.Between(5, 90)) * 1000
	case ProcessorSlow:
		duration = int64(rng.Between(1, 10)) * Minute
	case GatewayCrash:
		duration = 0
	}
	return Fault{start, min(horizonMs, start+duration), kind}
}

// FaultSchedule is a list of faults with start and end times. Armed onto the simulator before the run begins.
type FaultSchedule struct {
	Faults []Fault
}

func GenerateFaultSchedule(rng *Rng, horizonMs int64, count int) *FaultSchedule {
	s := &FaultSchedule{}
	for i := 0; i < count; i++ {
		s.Faults = append(s.Faults, DrawFault(rng, horizonMs))
	}
	sort.SliceStable(s.Faults, func(i, j int) bool {
		a, b := s.Faults[i], s.Faults[j]
		if a.StartMs != b.StartMs {
			return a.StartMs < b.StartMs
		}
		return a.Kind < b.Kind
	})
	return s
}

func (s *FaultSchedule) Arm(sim *Simulator, net *Network, processor *Processor, gateway *Gateway) {
	for _, f := range s.Faults {
		sim.Schedule(f.StartMs, "fault-begin "+f.Kind.String(), func() { beginFault(f, net, processor, gateway) })
		if f.EndMs > f.StartMs {
			sim.Schedule(f.EndMs, "fault-end "+f.Kind.String(), func() { endFault(f, net, processor) })
		}
	}
}

func beginFault(f Fault, net *Network, processor *Processor, gateway *Gateway) {
	switch f.Kind {
	case PartitionClientGateway:
		for i := 0; i < 8; i++ {
			net.Partition(fmt.Sprintf("client-%d", i), "gateway")
		}
	case PartitionGatewayProcessor:
		net.Partition("gateway", "processor")
	case ProcessorOutage:
		processor.Down = true
	case ProcessorSlow:
		processor.ExtraLatencyMs = 5_000
	case GatewayCrash:
		gateway.CrashWhenBusy()
	}
}

func endFault(f Fault, net *Network, processor *Processor) {
	switch f.Kind {
	case PartitionClientGateway:
		for i := 0; i < 8; i++ {
			net.Heal(fmt.Sprintf("client-%d", i), "gateway")
		}
	case PartitionGatewayProcessor:
		net.Heal("gateway", "processor")
	case ProcessorOutage:
		processor.Down = false
	case ProcessorSlow:
		processor.ExtraLatencyMs = 0
	case GatewayCrash:
	}
}

// Defect is a bug the harness can plant in the gateway so the learner can watch the invariant checkers catch it.
type Defect int

const (
	NoDefect             Defect = iota
	FreshAuthKeyOnRetry         // the gateway mints a new processor idempotency key on every retry
	RecoverySkipsPending        // after a crash the gateway does not re-drive payments that were in flight
)

func (d Defect) String() string {
	return [...]string{"NONE", "FRESH_AUTH_KEY_ON_RETRY", "RECOVERY_SKIPS_PENDING"}[d]
}

// ---------------------------------------------------------------- The mini payment system ----

type Status int

const (
	Pending Status = iota
	Approved
	Declined
)

func (s Status) String() string { return [...]string{"PENDING", "APPROVED", "DECLINED"}[s] }

type PaymentRequest struct {
	Key        string
	CustomerId string
	Amount     Money
}

type AuthRequest struct {
	AuthKey    string
	PaymentId  string
	CustomerId string
	Amount     Money
	Attempt    int
}

type AuthResponse struct {
	AuthKey   string
	PaymentId string
	Approved  bool
}

type Posting struct {
	Account string
	Amount  Money
}

type JournalEntry struct {
	EntryId  string
	Postings []Posting
}

var (
	ErrNonPositiveTransfer = errors.New("transfer amount must be positive")
	ErrUnbalancedEntry     = errors.New("unbalanced entry")
)

func Transfer(entryId, debitAccount, creditAccount string, amount Money) (JournalEntry, error) {
	if !amount.IsPositive() {
		return JournalEntry{}, fmt.Errorf("%w: %v", ErrNonPositiveTransfer, amount)
	}
	return JournalEntry{entryId, []Posting{{debitAccount, amount}, {creditAccount, amount.Negate()}}}, nil
}

func (e JournalEntry) Balanced() bool {
	sum := Zero(e.Postings[0].Amount.Currency)
	for _, p := range e.Postings {
		var err error
		if sum, err = sum.Plus(p.Amount); err != nil {
			return false
		}
	}
	return sum.IsZero()
}

// Ledger is an append-only double-entry ledger with a unique entry id (Phase 10, Lesson 03).
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
		return fmt.Errorf("%w %s", ErrUnbalancedEntry, entry.EntryId)
	}
	l.Journal = append(l.Journal, entry)
	l.entryCounts[entry.EntryId]++
	for _, p := range entry.Postings {
		l.balances[p.Account] = need(l.Balance(p.Account).Plus(p.Amount))
	}
	return nil
}

func (l *Ledger) Balance(account string) Money {
	if m, ok := l.balances[account]; ok {
		return m
	}
	return Zero(l.currency)
}

func (l *Ledger) TrialBalance() Money {
	sum := Zero(l.currency)
	for _, m := range l.balances {
		sum = need(sum.Plus(m))
	}
	return sum
}

// DebitTotal is the sum of the debit balances of every account with the prefix.
func (l *Ledger) DebitTotal(prefix string) Money {
	sum := Zero(l.currency)
	for account, m := range l.balances {
		if strings.HasPrefix(account, prefix) {
			sum = need(sum.Plus(m))
		}
	}
	return sum
}

func (l *Ledger) CountEntries(entryId string) int { return l.entryCounts[entryId] }

// Balances is a copy of the balances by account, in sorted account order (the Java TreeMap view).
func (l *Ledger) Balances() ([]string, map[string]Money) {
	accounts := make([]string, 0, len(l.balances))
	out := make(map[string]Money, len(l.balances))
	for a, m := range l.balances {
		accounts = append(accounts, a)
		out[a] = m
	}
	sort.Strings(accounts)
	return accounts, out
}

// Processor is the card processor: idempotent on authKey, decides approve/decline from its own seeded stream, can
// be down or slow.
type Processor struct {
	sim                *Simulator
	net                *Network
	rng                *Rng
	declineBasisPoints int
	Decisions          map[string]AuthResponse // authKey -> decision (durable)
	Approvals          []AuthResponse          // every approval ever granted, in order
	Down               bool
	ExtraLatencyMs     int
	Received           int
	Replayed           int
	Swallowed          int
}

const ProcessorName = "processor"

func NewProcessor(sim *Simulator, net *Network, rng *Rng, declineBasisPoints int) *Processor {
	return &Processor{sim: sim, net: net, rng: rng, declineBasisPoints: declineBasisPoints, Decisions: map[string]AuthResponse{}}
}

func (p *Processor) Receive(req AuthRequest, gateway *Gateway) {
	p.Received++
	if p.Down { // an outage looks like a timeout to the caller
		p.Swallowed++
		return
	}
	decision, ok := p.Decisions[req.AuthKey]
	if ok {
		p.Replayed++
	} else {
		approved := !p.rng.Chance(p.declineBasisPoints)
		decision = AuthResponse{req.AuthKey, req.PaymentId, approved}
		p.Decisions[req.AuthKey] = decision
		if approved {
			p.Approvals = append(p.Approvals, decision)
		}
	}
	reply := decision
	p.sim.Schedule(int64(p.ExtraLatencyMs), "processor-respond "+req.PaymentId, func() {
		p.net.Send(ProcessorName, GatewayName, "auth-response "+reply.PaymentId, func() { gateway.OnAuthResponse(reply) })
	})
}

// ApprovalsPerPayment is the distinct payment ids that were approved, with how many times each was approved,
// in sorted id order.
func (p *Processor) ApprovalsPerPayment() ([]string, map[string]int) {
	m := map[string]int{}
	for _, a := range p.Approvals {
		m[a.PaymentId]++
	}
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, m
}

type Payment struct {
	PaymentId  string
	Key        string
	CustomerId string
	Amount     Money
	Status     Status
	AuthKey    string
	Attempts   int
}

func NewPayment(paymentId, key, customerId string, amount Money) *Payment {
	return &Payment{PaymentId: paymentId, Key: key, CustomerId: customerId, Amount: amount, AuthKey: "auth-" + paymentId}
}

// Gateway: durable state (payments, idempotency keys, ledger) survives a crash; the in-memory table of clients
// waiting for a reply does not, and neither do timers armed before the crash (checked via epoch).
type Gateway struct {
	sim                *Simulator
	net                *Network
	processor          *Processor
	defect             Defect
	ProcessorTimeoutMs int64
	RecoveryDelayMs    int64
	// durable
	IdempotencyKeys map[string]string   // key -> paymentId
	Payments        map[string]*Payment // paymentId -> payment
	PaymentIds      []string            // insertion order of Payments (the Java LinkedHashMap)
	Ledger          *Ledger
	// volatile
	waiting      map[string][]func()
	epoch        int
	crashArmed   bool
	sequence     int // durable: never reused after a crash
	Crashes      int
	AuthSends    int
	AuthTimeouts int
	Replays      int
	Recovered    int
}

const (
	GatewayName = "gateway"
	Receivable  = "asset:receivable:"
	Clearing    = "liability:clearing:processor"
)

func NewGateway(sim *Simulator, net *Network, processor *Processor, defect Defect, processorTimeoutMs, recoveryDelayMs int64) *Gateway {
	return &Gateway{sim: sim, net: net, processor: processor, defect: defect, ProcessorTimeoutMs: processorTimeoutMs,
		RecoveryDelayMs: recoveryDelayMs, IdempotencyKeys: map[string]string{}, Payments: map[string]*Payment{},
		Ledger: NewLedger(USD), waiting: map[string][]func(){}}
}

// Handle a client request; reply is how the response gets back to the client (over the network).
func (g *Gateway) Handle(req PaymentRequest, reply func(Status)) {
	if existingId, ok := g.IdempotencyKeys[req.Key]; ok {
		g.Replays++
		p := g.Payments[existingId]
		if p.Status != Pending {
			reply(p.Status)
			return
		}
		g.waiting[p.PaymentId] = append(g.waiting[p.PaymentId], func() { reply(p.Status) })
		return
	}
	g.sequence++
	paymentId := fmt.Sprintf("pay-%06d", g.sequence)
	p := NewPayment(paymentId, req.Key, req.CustomerId, req.Amount)
	g.Payments[paymentId] = p // one transaction: payment + key
	g.PaymentIds = append(g.PaymentIds, paymentId)
	g.IdempotencyKeys[req.Key] = paymentId
	g.waiting[paymentId] = append(g.waiting[paymentId], func() { reply(p.Status) })
	g.sendAuth(p)
	if g.crashArmed { // the armed crash fires in the worst window: durable, in flight, unanswered
		g.Crash()
	}
}

func (g *Gateway) sendAuth(p *Payment) {
	p.Attempts++
	if g.defect == FreshAuthKeyOnRetry && p.Attempts > 1 {
		p.AuthKey = fmt.Sprintf("auth-%s-%d", p.PaymentId, p.Attempts)
	}
	g.AuthSends++
	req := AuthRequest{p.AuthKey, p.PaymentId, p.CustomerId, p.Amount, p.Attempts}
	g.net.Send(GatewayName, ProcessorName, fmt.Sprintf("auth-request %s a%d", p.PaymentId, p.Attempts), func() { g.processor.Receive(req, g) })
	armedEpoch, armedAttempt := g.epoch, p.Attempts
	g.sim.Schedule(g.ProcessorTimeoutMs, fmt.Sprintf("auth-timeout %s a%d", p.PaymentId, armedAttempt), func() {
		if armedEpoch != g.epoch || p.Status != Pending || p.Attempts != armedAttempt {
			return
		}
		g.AuthTimeouts++
		g.sendAuth(p) // same authKey: the processor will dedupe
	})
}

func (g *Gateway) OnAuthResponse(r AuthResponse) {
	p, ok := g.Payments[r.PaymentId]
	if !ok || p.Status != Pending { // late duplicate: already resolved
		return
	}
	if r.Approved {
		p.Status = Approved // one transaction: status + ledger entry
		need(0, g.Ledger.Post(need(Transfer(p.PaymentId, Receivable+p.CustomerId, Clearing, p.Amount))))
	} else {
		p.Status = Declined
	}
	replies := g.waiting[p.PaymentId]
	delete(g.waiting, p.PaymentId)
	for _, reply := range replies {
		reply()
	}
}

// CrashWhenBusy: a crash at 3 a.m. with nothing in flight tests nothing. The fault schedule arms the crash; it
// fires immediately if a payment is pending, otherwise the next time one is accepted. VOPR-style harnesses bias
// faults toward interesting states.
func (g *Gateway) CrashWhenBusy() {
	if g.PendingCount() > 0 {
		g.Crash()
	} else {
		g.crashArmed = true
	}
}

// Crash loses everything in memory; durable state stays. Schedules the recovery sweep that re-drives in-flight
// payments.
func (g *Gateway) Crash() {
	g.Crashes++
	g.epoch++
	g.crashArmed = false
	g.waiting = map[string][]func(){}
	if g.defect == RecoverySkipsPending {
		return
	}
	g.sim.Schedule(g.RecoveryDelayMs, "gateway-recover", func() {
		for _, id := range g.PaymentIds {
			if p := g.Payments[id]; p.Status == Pending {
				g.Recovered++
				g.sendAuth(p)
			}
		}
	})
}

func (g *Gateway) PendingCount() int {
	n := 0
	for _, p := range g.Payments {
		if p.Status == Pending {
			n++
		}
	}
	return n
}

type ClientConfig struct {
	TimeoutMs   int64
	MaxAttempts int
	BackoffMs   int64
}

var DefaultClientConfig = ClientConfig{8_000, 6, 2_000}

// Client retries on timeout with the same idempotency key and records the final status it believes.
type Client struct {
	Name          string
	sim           *Simulator
	net           *Network
	gateway       *Gateway
	cfg           ClientConfig
	Outcomes      map[string]Status // key -> status the client believes (absent = unknown)
	OutcomeKeys   []string          // insertion order of Outcomes
	Unknown       map[string]bool
	Disagreements []string
	Requests      int
	Retries       int
}

func NewClient(name string, sim *Simulator, net *Network, gateway *Gateway, cfg ClientConfig) *Client {
	return &Client{Name: name, sim: sim, net: net, gateway: gateway, cfg: cfg, Outcomes: map[string]Status{}, Unknown: map[string]bool{}}
}

func (c *Client) Pay(req PaymentRequest) { c.Requests++; c.attempt(req, 1) }

func (c *Client) attempt(req PaymentRequest, n int) {
	answered := false
	c.net.Send(c.Name, GatewayName, fmt.Sprintf("payment %s a%d", req.Key, n), func() {
		c.gateway.Handle(req, func(status Status) {
			c.net.Send(GatewayName, c.Name, fmt.Sprintf("payment-reply %s %v", req.Key, status), func() {
				answered = true
				if previous, seen := c.Outcomes[req.Key]; !seen {
					c.Outcomes[req.Key] = status
					c.OutcomeKeys = append(c.OutcomeKeys, req.Key)
				} else if previous != status {
					c.Disagreements = append(c.Disagreements, fmt.Sprintf("%s saw %v then %v", req.Key, previous, status))
				}
			})
		})
	})
	c.sim.Schedule(c.cfg.TimeoutMs, fmt.Sprintf("client-timeout %s a%d", req.Key, n), func() {
		if _, known := c.Outcomes[req.Key]; answered || known {
			return
		}
		if n >= c.cfg.MaxAttempts {
			c.Unknown[req.Key] = true
			return
		}
		c.Retries++
		c.sim.Schedule(c.cfg.BackoffMs*int64(n), fmt.Sprintf("client-retry %s a%d", req.Key, n+1), func() { c.attempt(req, n+1) })
	})
}

// ---------------------------------------------------------------- Harness ----

type SimConfig struct {
	Seed               uint64
	VirtualHours       int64
	Clients            int
	MeanInterArrivalMs int64
	Faults             int
	Defect             Defect
}

// StandardConfig: four clients, one payment per client every ten virtual minutes on average, one fault every four
// virtual hours.
func StandardConfig(seed uint64, virtualHours int64) SimConfig {
	return SimConfig{seed, virtualHours, 4, 10 * 60_000, int(max(1, virtualHours/4)), NoDefect}
}

func (c SimConfig) With(d Defect) SimConfig     { c.Defect = d; return c }
func (c SimConfig) WithSeed(s uint64) SimConfig { c.Seed = s; return c }
func (c SimConfig) HorizonMs() int64            { return c.VirtualHours * 3_600_000 }

type SimReport struct {
	Seed             uint64
	VirtualHours     int64
	Events           int64
	Payments         int
	Approved         int
	Declined         int
	Pending          int
	UnknownAtClient  int
	ClientRetries    int
	AuthTimeouts     int
	NetDropped       int
	NetBlocked       int
	Crashes          int
	Faults           int
	TrialBalance     Money
	FirstViolationMs int64
	Violations       []string
	Fingerprint      string
}

func (r SimReport) Passed() bool { return len(r.Violations) == 0 }

func (r SimReport) FirstBrokenInvariant() string {
	if len(r.Violations) == 0 {
		return ""
	}
	v := r.Violations[0]
	return v[1:strings.IndexByte(v, ']')]
}

func (r SimReport) Summary() string {
	return fmt.Sprintf("seed=%d hours=%d events=%d payments=%d approved=%d declined=%d pending=%d unknown=%d retries=%d"+
		" authTimeouts=%d dropped=%d blocked=%d crashes=%d faults=%d trial=%v violations=%d fp=%s",
		r.Seed, r.VirtualHours, r.Events, r.Payments, r.Approved, r.Declined, r.Pending, r.UnknownAtClient, r.ClientRetries, r.AuthTimeouts,
		r.NetDropped, r.NetBlocked, r.Crashes, r.Faults, r.TrialBalance, len(r.Violations), r.Fingerprint)
}

// Reproduction is what you paste into the bug report: enough to replay the exact run.
func (r SimReport) Reproduction() string {
	s := fmt.Sprintf("reproduce with: Harness.run(SimConfig.standard(%d, %d))", r.Seed, r.VirtualHours)
	if len(r.Violations) > 0 {
		s += fmt.Sprintf("; first violation at t=%dms: %s", r.FirstViolationMs, r.Violations[0])
	}
	return s
}

const (
	Hour          = int64(3_600_000)
	CheckInterval = 24 * Hour
)

// Harness wires the system, runs it for the configured virtual horizon, checks the safety invariants once per
// virtual day and the quiescence invariants after the drain. Checks are O(payments), so checking every event would
// be quadratic.
var Harness harness

type harness struct{}

func (harness) Run(cfg SimConfig) SimReport {
	sim := NewSimulator()
	master := NewRng(cfg.Seed)
	netRng, processorRng, faultRng, workloadRng := master.Split(), master.Split(), master.Split(), master.Split()
	net := NewNetwork(sim, netRng, LAN)
	processor := NewProcessor(sim, net, processorRng, 500) // 5% declines
	gateway := NewGateway(sim, net, processor, cfg.Defect, 3_000, 2_000)
	clients := make([]*Client, 0, cfg.Clients)
	for i := 0; i < cfg.Clients; i++ {
		clients = append(clients, NewClient(fmt.Sprintf("client-%d", i), sim, net, gateway, DefaultClientConfig))
	}
	schedule := GenerateFaultSchedule(faultRng, cfg.HorizonMs(), cfg.Faults)
	schedule.Arm(sim, net, processor, gateway)

	// Workload: each client's next payment is drawn from the workload stream; the stream never sees faults.
	issued := 0
	for _, c := range clients {
		scheduleNextPayment(sim, c, workloadRng, cfg, &issued)
	}

	violations := []string{}
	firstViolation := int64(-1)
	for t := CheckInterval; t <= cfg.HorizonMs(); t += CheckInterval {
		sim.Schedule(t, "invariant-check", func() {
			if len(violations) > 0 {
				return
			}
			violations = append(violations, CheckSafety(gateway, processor, clients)...)
			if len(violations) > 0 {
				firstViolation = sim.Now
			}
		})
	}
	sim.RunUntil(cfg.HorizonMs())
	need(0, sim.RunUntilIdle(5_000_000)) // drain: no new payments, faults all ended

	if len(violations) == 0 {
		violations = append(violations, CheckSafety(gateway, processor, clients)...)
		violations = append(violations, CheckQuiescent(gateway, processor)...)
		if len(violations) > 0 {
			firstViolation = sim.Now
		}
	}
	approved, declined, unknown, retries := 0, 0, 0, 0
	for _, p := range gateway.Payments {
		switch p.Status {
		case Approved:
			approved++
		case Declined:
			declined++
		}
	}
	for _, c := range clients {
		unknown += len(c.Unknown)
		retries += c.Retries
	}
	return SimReport{cfg.Seed, cfg.VirtualHours, sim.Processed, len(gateway.Payments), approved, declined, gateway.PendingCount(),
		unknown, retries, gateway.AuthTimeouts, net.Dropped, net.Blocked, gateway.Crashes, len(schedule.Faults),
		gateway.Ledger.TrialBalance(), firstViolation, violations, sim.Trace.Hex()}
}

func scheduleNextPayment(sim *Simulator, c *Client, rng *Rng, cfg SimConfig, issued *int) {
	gap := rng.Between(int(cfg.MeanInterArrivalMs/2), int(cfg.MeanInterArrivalMs*3/2))
	sim.Schedule(int64(gap), "client-pay "+c.Name, func() {
		if sim.Now >= cfg.HorizonMs() { // the horizon has passed: stop generating load
			return
		}
		key := fmt.Sprintf("key-%d", *issued)
		*issued++
		amount := OfMinor(int64(100+rng.NextInt(50_000)), USD)
		c.Pay(PaymentRequest{key, fmt.Sprintf("cust-%d", rng.NextInt(200)), amount})
		scheduleNextPayment(sim, c, rng, cfg, issued)
	})
}

// CheckSafety: true at every instant, so checked during the run. Each violation is "[invariant] detail".
// Note what is NOT here: "every approval is posted". An approval whose response is still on the wire is not a bug,
// so that property is only checkable at quiescence (CheckQuiescent).
func CheckSafety(gateway *Gateway, processor *Processor, clients []*Client) []string {
	v := []string{}
	ledger := gateway.Ledger
	approvedIds, approvals := processor.ApprovalsPerPayment()

	for _, id := range approvedIds {
		if approvals[id] != 1 {
			v = append(v, fmt.Sprintf("[no-double-authorization] %s approved %d times by the processor", id, approvals[id]))
		}
	}
	for _, id := range approvedIds {
		if n := ledger.CountEntries(id); n > 1 {
			v = append(v, fmt.Sprintf("[approval-posted-once] %s posted %d times", id, n))
		}
	}
	for _, e := range ledger.Journal {
		if _, ok := approvals[e.EntryId]; !ok {
			v = append(v, "[approval-posted-once] "+e.EntryId+" posted without a processor approval")
		}
	}
	approvedAtGateway := Zero(USD)
	for _, id := range gateway.PaymentIds {
		p := gateway.Payments[id]
		if p.Status == Approved {
			approvedAtGateway = need(approvedAtGateway.Plus(p.Amount))
			if _, ok := approvals[p.PaymentId]; !ok {
				v = append(v, "[approval-posted-once] "+p.PaymentId+" marked APPROVED without an approval")
			}
			if n := ledger.CountEntries(p.PaymentId); n != 1 {
				v = append(v, fmt.Sprintf("[approval-posted-once] %s APPROVED but posted %d times", p.PaymentId, n))
			}
		}
	}
	if !ledger.TrialBalance().IsZero() {
		v = append(v, fmt.Sprintf("[trial-balance] %v", ledger.TrialBalance()))
	}
	receivables := ledger.DebitTotal(Receivable)
	if receivables != approvedAtGateway {
		v = append(v, fmt.Sprintf("[money-conserved] receivables %v != approved at gateway %v", receivables, approvedAtGateway))
	}
	perKey := map[string]int{}
	keys := []string{}
	for _, p := range gateway.Payments {
		if perKey[p.Key] == 0 {
			keys = append(keys, p.Key)
		}
		perKey[p.Key]++
	}
	sort.Strings(keys)
	for _, k := range keys {
		if perKey[k] != 1 {
			v = append(v, fmt.Sprintf("[one-payment-per-key] %s has %d payments", k, perKey[k]))
		}
	}
	for _, c := range clients {
		for _, d := range c.Disagreements {
			v = append(v, "[client-gateway-agree] "+c.Name+" "+d)
		}
		for _, key := range c.OutcomeKeys {
			believed := c.Outcomes[key]
			p, ok := gateway.Payments[gateway.IdempotencyKeys[key]]
			if !ok || p.Status != believed {
				has := "nothing"
				if ok {
					has = p.Status.String()
				}
				v = append(v, fmt.Sprintf("[client-gateway-agree] %s believes %s is %v but gateway has %s", c.Name, key, believed, has))
			}
		}
	}
	return v
}

// CheckQuiescent: true only once every message has been delivered or dropped and every fault has ended.
func CheckQuiescent(gateway *Gateway, processor *Processor) []string {
	v := []string{}
	for _, id := range gateway.PaymentIds {
		if p := gateway.Payments[id]; p.Status == Pending {
			v = append(v, fmt.Sprintf("[no-stuck-payments] %s still PENDING after quiescence (%d auth attempts)", p.PaymentId, p.Attempts))
		}
	}
	approvedIds, _ := processor.ApprovalsPerPayment()
	for _, id := range approvedIds {
		if n := gateway.Ledger.CountEntries(id); n != 1 {
			v = append(v, fmt.Sprintf("[approval-posted-once] %s approved by the processor but posted %d times", id, n))
		}
	}
	approvedAtProcessor := Zero(USD)
	for _, a := range processor.Approvals {
		approvedAtProcessor = need(approvedAtProcessor.Plus(gateway.Payments[a.PaymentId].Amount))
	}
	receivables := gateway.Ledger.DebitTotal(Receivable)
	if receivables != approvedAtProcessor {
		v = append(v, fmt.Sprintf("[money-conserved] receivables %v != approved at processor %v", receivables, approvedAtProcessor))
	}
	return v
}

// FindFailingSeed tries seeds in order until one fails; the report carries everything needed to replay it.
// The second result is false when every seed passed.
func (h harness) FindFailingSeed(cfg SimConfig, firstSeed uint64, maxSeeds int) (SimReport, bool) {
	for seed := firstSeed; seed < firstSeed+uint64(maxSeeds); seed++ {
		if r := h.Run(cfg.WithSeed(seed)); !r.Passed() {
			return r, true
		}
	}
	return SimReport{}, false
}

// ---------------------------------------------------------------- Demo ----

func passFail(r SimReport) string {
	if r.Passed() {
		return "PASS "
	}
	return "FAIL "
}

func main() {
	fmt.Println("== SplitMix64: the same seed is the same universe, on every machine and every run ==")
	rng := NewRng(0)
	fmt.Printf("seed 0 first draw = %016x (reference value e220a8397b1dcdaf)\n", rng.NextLong())

	fmt.Println("\n== Same seed twice: identical event trace ==")
	a := Harness.Run(StandardConfig(1, 48))
	b := Harness.Run(StandardConfig(1, 48))
	fmt.Println(a.Summary())
	fmt.Printf("fingerprints equal: %t\n", a.Fingerprint == b.Fingerprint)

	fmt.Println("\n== 480 virtual hours (20 days) of payments under injected faults, seed 2 ==")
	r := Harness.Run(StandardConfig(2, 480))
	fmt.Println(passFail(r) + r.Summary())

	fmt.Println("\n== Four more seeds, 120 virtual hours each ==")
	for seed := uint64(3); seed <= 6; seed++ {
		s := Harness.Run(StandardConfig(seed, 120))
		fmt.Println(passFail(s) + s.Summary())
	}

	fmt.Println("\n== Plant a defect; the harness finds a failing seed and prints how to replay it ==")
	for _, d := range []Defect{FreshAuthKeyOnRetry, RecoverySkipsPending} {
		failing, found := Harness.FindFailingSeed(StandardConfig(1, 24).With(d), 1, 40)
		if !found {
			fmt.Printf("%v: no failing seed in 40 tries\n", d)
			continue
		}
		fmt.Printf("%v -> seed %d breaks [%s]\n", d, failing.Seed, failing.FirstBrokenInvariant())
		fmt.Println("  " + failing.Violations[0])
		fmt.Println("  " + failing.Reproduction())
	}
}
