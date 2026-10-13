// Lesson: phases/07-storage-and-transactions/08-two-phase-commit/docs/en.md
// Two-Phase Commit and Its Blocking Problem
// Run:  go run .
// Test: go test ./...
// Sources: Gray, "Notes on Data Base Operating Systems" (1978); Mohan, Lindsay, Obermarck,
// "Transaction Management in the R* Distributed Database Management System" (1986); X/Open XA spec.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------- Money (Phase 10, Lesson 01)

// Currency is an ISO 4217 currency: a three-letter code and the number of decimals in its minor unit.
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

var ErrCurrencyMismatch = errors.New("currency mismatch")

// Money is an exact amount of one currency stored as a signed count of minor units.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func (m Money) Plus(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.Currency.Code, other.Currency.Code)
	}
	return Money{m.MinorUnits + other.MinorUnits, m.Currency}, nil
}

func (m Money) Negate() Money    { return Money{-m.MinorUnits, m.Currency} }
func (m Money) IsNegative() bool { return m.MinorUnits < 0 }

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

// mustPlus is for same-currency sums the ledger controls; a mismatch there is a programming error.
func mustPlus(a, b Money) Money {
	sum, err := a.Plus(b)
	if err != nil {
		panic(err)
	}
	return sum
}

// USDOf parses "150.00" or "-1.00" into USD minor units for demos and tests.
func USDOf(text string) Money {
	negative := strings.HasPrefix(text, "-")
	digits := strings.ReplaceAll(strings.TrimPrefix(text, "-"), ".", "")
	minor, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		panic(err)
	}
	if negative {
		minor = -minor
	}
	return Money{minor, USD}
}

// ---------------------------------------------------------------- simulation primitives

// SimClock only moves when the simulation says so.
type SimClock struct{ nowMs int64 }

func (c *SimClock) Now() int64       { return c.nowMs }
func (c *SimClock) Advance(ms int64) { c.nowMs += ms }

type Vote int

const (
	VoteYes Vote = iota
	VoteNo
)

func (v Vote) String() string {
	if v == VoteYes {
		return "YES"
	}
	return "NO"
}

type Decision int

const (
	Commit Decision = iota
	Abort
)

func (d Decision) String() string {
	if d == Commit {
		return "COMMIT"
	}
	return "ABORT"
}

func parseDecision(s string) Decision {
	if s == "COMMIT" {
		return Commit
	}
	return Abort
}

// TxState is what a participant knows about a transaction. Prepared is the in-doubt state.
type TxState int

const (
	StateNone TxState = iota
	StatePrepared
	StateCommitted
	StateAborted
)

func (s TxState) String() string {
	return [...]string{"NONE", "PREPARED", "COMMITTED", "ABORTED"}[s]
}

// CrashPoint says where the coordinator dies, if anywhere.
type CrashPoint int

const (
	NoCrash CrashPoint = iota
	AfterVotesBeforeDecisionLog
	AfterDecisionLogBeforeSend
)

// Posting is one account change on one participant. A negative delta is a debit.
type Posting struct {
	Account string
	Delta   Money
}

// LogRecord is one durable log record. Participants and the coordinator both write and replay these.
type LogRecord struct {
	TxID, Kind, Detail string
}

func (r LogRecord) String() string {
	if r.Detail == "" {
		return r.TxID + " " + r.Kind
	}
	return r.TxID + " " + r.Kind + " " + r.Detail
}

// DurableLog is append-only. Append is the durability point: a record in the slice survives a crash.
type DurableLog struct{ records []LogRecord }

func (l *DurableLog) Append(txID, kind, detail string) {
	l.records = append(l.records, LogRecord{txID, kind, detail})
}
func (l *DurableLog) Records() []LogRecord { return append([]LogRecord(nil), l.records...) }
func (l *DurableLog) Size() int            { return len(l.records) }

// ErrCrash is returned by an injected crash point, where the real process would die.
var ErrCrash = errors.New("crash")

// ErrIllegalState is returned when a participant is asked for a transition its state forbids.
var ErrIllegalState = errors.New("illegal state")

// ---------------------------------------------------------------- participant

// Ledger is a participant: one ledger with its own balances, locks, and log. Prepare is a promise:
// after voting YES it holds the lock and the posting until the coordinator tells it the decision.
type Ledger struct {
	Name     string
	initial  map[string]Money
	balances map[string]Money
	prepared map[string]Posting
	states   map[string]TxState
	locked   map[string]bool
	log      *DurableLog
}

func NewLedger(name string, opening map[string]Money) *Ledger {
	return newLedger(name, opening, &DurableLog{})
}

func newLedger(name string, opening map[string]Money, log *DurableLog) *Ledger {
	l := &Ledger{Name: name, initial: map[string]Money{}, balances: map[string]Money{},
		prepared: map[string]Posting{}, states: map[string]TxState{}, locked: map[string]bool{}, log: log}
	for k, v := range opening {
		l.initial[k] = v
		l.balances[k] = v
	}
	return l
}

func (l *Ledger) Balance(account string) Money { return l.balances[account] }
func (l *Ledger) StateOf(txID string) TxState  { return l.states[txID] }
func (l *Ledger) IsLocked(account string) bool { return l.locked[account] }
func (l *Ledger) LogRecords() []LogRecord      { return l.log.Records() }

// InDoubt lists prepared transactions still waiting. Non-empty means this ledger is blocked.
func (l *Ledger) InDoubt() []string {
	out := make([]string, 0, len(l.prepared))
	for id := range l.prepared {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Prepare is phase 1: validate, lock, log PREPARED, then vote. The log write comes before the vote leaves.
func (l *Ledger) Prepare(txID string, p Posting) Vote {
	if l.states[txID] == StatePrepared {
		return VoteYes // duplicate prepare: same answer
	}
	if _, decided := l.states[txID]; decided {
		return VoteNo // already decided: too late
	}
	current, ok := l.balances[p.Account]
	if !ok || l.locked[p.Account] {
		why := "account locked"
		if !ok {
			why = "unknown account"
		}
		l.log.Append(txID, "ABORTED", "vote NO: "+why)
		l.states[txID] = StateAborted
		return VoteNo
	}
	if mustPlus(current, p.Delta).IsNegative() {
		l.log.Append(txID, "ABORTED", "vote NO: insufficient funds in "+p.Account)
		l.states[txID] = StateAborted
		return VoteNo
	}
	l.locked[p.Account] = true
	l.prepared[txID] = p
	l.states[txID] = StatePrepared
	l.log.Append(txID, "PREPARED", p.Account+" "+p.Delta.String())
	return VoteYes
}

// Commit is phase 2. Idempotent: a second COMMIT for the same tx is acknowledged, not re-applied.
func (l *Ledger) Commit(txID string) error {
	switch l.StateOf(txID) {
	case StateCommitted:
		return nil
	case StatePrepared:
	default:
		return fmt.Errorf("%w: %s: commit of %s in state %v", ErrIllegalState, l.Name, txID, l.StateOf(txID))
	}
	p := l.prepared[txID]
	delete(l.prepared, txID)
	l.balances[p.Account] = mustPlus(l.balances[p.Account], p.Delta)
	delete(l.locked, p.Account)
	l.states[txID] = StateCommitted
	l.log.Append(txID, "COMMITTED", "")
	return nil
}

// Abort is phase 2. Idempotent, and legal from None (the coordinator may abort a tx we never prepared).
func (l *Ledger) Abort(txID string) error {
	switch l.StateOf(txID) {
	case StateAborted:
		return nil
	case StateCommitted:
		return fmt.Errorf("%w: %s: cannot abort committed %s", ErrIllegalState, l.Name, txID)
	}
	if p, ok := l.prepared[txID]; ok {
		delete(l.prepared, txID)
		delete(l.locked, p.Account)
	}
	l.states[txID] = StateAborted
	l.log.Append(txID, "ABORTED", "")
	return nil
}

// CrashAndRecover loses all memory and rebuilds from the log. PREPARED records with no later
// decision come back as in-doubt: locks re-taken, posting held. The participant cannot resolve them.
func (l *Ledger) CrashAndRecover() *Ledger {
	fresh := newLedger(l.Name, l.initial, l.log)
	pending := map[string]Posting{}
	order := []string{}
	for _, r := range l.log.Records() {
		switch r.Kind {
		case "PREPARED":
			parts := strings.Split(r.Detail, " ")
			pending[r.TxID] = Posting{parts[0], parseMoney(parts[1], parts[2])}
			order = append(order, r.TxID)
			fresh.states[r.TxID] = StatePrepared
		case "COMMITTED":
			p := pending[r.TxID]
			delete(pending, r.TxID)
			fresh.balances[p.Account] = mustPlus(fresh.balances[p.Account], p.Delta)
			fresh.states[r.TxID] = StateCommitted
		case "ABORTED":
			delete(pending, r.TxID)
			fresh.states[r.TxID] = StateAborted
		default:
			panic("unknown record " + r.String())
		}
	}
	for _, id := range order {
		if p, ok := pending[id]; ok {
			fresh.prepared[id] = p
			fresh.locked[p.Account] = true
		}
	}
	return fresh
}

func parseMoney(amount, code string) Money {
	negative := strings.HasPrefix(amount, "-")
	s := strings.TrimPrefix(amount, "-")
	exponent := 0
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		exponent = len(s) - dot - 1
	}
	minor, err := strconv.ParseInt(strings.ReplaceAll(s, ".", ""), 10, 64)
	if err != nil {
		panic(err)
	}
	if negative {
		minor = -minor
	}
	return Money{minor, Currency{code, exponent}}
}

func (l *Ledger) Total(currency Currency) Money {
	acc := Money{0, currency}
	for _, m := range l.balances {
		acc = mustPlus(acc, m)
	}
	return acc
}

// ---------------------------------------------------------------- network

// SimNetwork is a deterministic network between the coordinator and its participants. A partitioned
// node never answers; the caller waits out the timeout and the clock advances. Every call is counted.
type SimNetwork struct {
	clock          *SimClock
	LatencyMs      int64
	TimeoutMs      int64
	nodes          map[string]*Ledger
	partitioned    map[string]bool
	crashAfterVote map[string]bool
	MessagesSent   int
	Timeouts       int
}

func NewSimNetwork(clock *SimClock, latencyMs, timeoutMs int64) *SimNetwork {
	return &SimNetwork{clock: clock, LatencyMs: latencyMs, TimeoutMs: timeoutMs,
		nodes: map[string]*Ledger{}, partitioned: map[string]bool{}, crashAfterVote: map[string]bool{}}
}

func (n *SimNetwork) Register(l *Ledger)    { n.nodes[l.Name] = l }
func (n *SimNetwork) Replace(l *Ledger)     { n.nodes[l.Name] = l }
func (n *SimNetwork) Partition(node string) { n.partitioned[node] = true }
func (n *SimNetwork) Heal(node string)      { delete(n.partitioned, node) }

// CrashAfterVote makes the node answer the next prepare and then go dark.
func (n *SimNetwork) CrashAfterVote(node string) { n.crashAfterVote[node] = true }

func (n *SimNetwork) reachable(node string) bool {
	n.MessagesSent++
	if n.partitioned[node] {
		n.clock.Advance(n.TimeoutMs)
		n.Timeouts++
		return false
	}
	n.clock.Advance(n.LatencyMs)
	return true
}

// Prepare returns ok=false when the request timed out; the coordinator must treat that as NO.
func (n *SimNetwork) Prepare(node, txID string, p Posting) (vote Vote, ok bool) {
	if !n.reachable(node) {
		return VoteNo, false
	}
	vote = n.nodes[node].Prepare(txID, p)
	if n.crashAfterVote[node] {
		delete(n.crashAfterVote, node)
		n.partitioned[node] = true
	}
	return vote, true
}

// Decide returns false when no acknowledgement arrived; the coordinator must retry later.
func (n *SimNetwork) Decide(node, txID string, d Decision) bool {
	if !n.reachable(node) {
		return false
	}
	var err error
	if d == Commit {
		err = n.nodes[node].Commit(txID)
	} else {
		err = n.nodes[node].Abort(txID)
	}
	if err != nil {
		panic(err) // a participant refusing a legal decision is a protocol bug, not a network condition
	}
	return true
}

// ---------------------------------------------------------------- coordinator

// Outcome is what the coordinator reports to the caller.
type Outcome struct {
	TxID     string
	Decision Decision
	Complete bool
	Reason   string
}

func (o Outcome) String() string {
	if o.Complete {
		return fmt.Sprintf("%s -> %v", o.TxID, o.Decision)
	}
	return fmt.Sprintf("%s -> %v (incomplete: %s)", o.TxID, o.Decision, o.Reason)
}

// Coordinator owns the only place the atomic decision lives: its log. The commit point of the whole
// protocol is the single append of a DECISION record; everything after that is delivery.
type Coordinator struct {
	log          *DurableLog
	net          *SimNetwork
	participants map[string][]string
	postings     map[string]map[string]Posting
	decisions    map[string]Decision
	unacked      map[string]map[string]bool
	CrashPoint   CrashPoint
}

func NewCoordinator(net *SimNetwork) *Coordinator {
	return &Coordinator{log: &DurableLog{}, net: net, participants: map[string][]string{},
		postings: map[string]map[string]Posting{}, decisions: map[string]Decision{}, unacked: map[string]map[string]bool{}}
}

func (c *Coordinator) LogRecords() []LogRecord { return c.log.Records() }

func (c *Coordinator) PendingTransactions() []string {
	out := make([]string, 0, len(c.unacked))
	for id := range c.unacked {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (c *Coordinator) DecisionOf(txID string) (Decision, bool) {
	d, ok := c.decisions[txID]
	return d, ok
}

// Transfer moves amount from one account on one ledger to another account on another ledger, atomically.
func (c *Coordinator) Transfer(txID string, from *Ledger, fromAccount string, to *Ledger, toAccount string, amount Money) (Outcome, error) {
	return c.Run(txID, []string{from.Name, to.Name},
		map[string]Posting{from.Name: {fromAccount, amount.Negate()}, to.Name: {toAccount, amount}})
}

// Run executes the protocol over the participants in the given order. The error is ErrCrash only.
func (c *Coordinator) Run(txID string, order []string, postings map[string]Posting) (Outcome, error) {
	c.participants[txID] = order
	c.postings[txID] = postings
	c.log.Append(txID, "BEGIN", strings.Join(order, ","))

	// Phase 1: collect votes. Any NO or any timeout means ABORT.
	decision, reason := Commit, "all voted YES"
	for _, node := range order {
		vote, ok := c.net.Prepare(node, txID, postings[node])
		if !ok {
			decision, reason = Abort, node+" timed out in prepare"
			break
		}
		if vote == VoteNo {
			decision, reason = Abort, node+" voted NO"
			break
		}
	}

	if c.CrashPoint == AfterVotesBeforeDecisionLog {
		return Outcome{}, fmt.Errorf("%w at after votes, before decision log", ErrCrash)
	}

	// The commit point. Before this append the outcome is undecided; after it, it is fixed forever.
	c.log.Append(txID, "DECISION", decision.String())
	c.decisions[txID] = decision
	waiting := map[string]bool{}
	for _, node := range order {
		waiting[node] = true
	}
	c.unacked[txID] = waiting

	if c.CrashPoint == AfterDecisionLogBeforeSend {
		return Outcome{}, fmt.Errorf("%w at after decision log, before send", ErrCrash)
	}

	// Phase 2: deliver the decision until every participant acknowledges.
	complete := c.Deliver(txID)
	if !complete {
		reason = fmt.Sprintf("%s; awaiting acks from %v", reason, sortedKeys(c.unacked[txID]))
	}
	return Outcome{txID, decision, complete, reason}, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Deliver runs one delivery round: send the decision to every unacked participant; log END when none remain.
func (c *Coordinator) Deliver(txID string) bool {
	waiting, ok := c.unacked[txID]
	if !ok {
		return true
	}
	decision := c.decisions[txID]
	for _, node := range sortedKeys(waiting) {
		if c.net.Decide(node, txID, decision) {
			delete(waiting, node)
		}
	}
	if len(waiting) == 0 {
		delete(c.unacked, txID)
		c.log.Append(txID, "END", "")
		return true
	}
	return false
}

// RetryPending retries delivery for every transaction still waiting on acknowledgements.
func (c *Coordinator) RetryPending() int {
	finished := 0
	for _, txID := range c.PendingTransactions() {
		if c.Deliver(txID) {
			finished++
		}
	}
	return finished
}

// RecoverCoordinator restarts with nothing but the log. BEGIN without DECISION: presumed abort,
// because no commit record exists, so no participant can have been told COMMIT. DECISION without END: redeliver.
func RecoverCoordinator(crashed *Coordinator, net *SimNetwork) *Coordinator {
	fresh := NewCoordinator(net)
	begun := map[string][]string{}
	begunOrder := []string{}
	ended := map[string]bool{}
	for _, r := range crashed.log.Records() {
		fresh.log.Append(r.TxID, r.Kind, r.Detail)
		switch r.Kind {
		case "BEGIN":
			begun[r.TxID] = strings.Split(r.Detail, ",")
			begunOrder = append(begunOrder, r.TxID)
		case "DECISION":
			fresh.decisions[r.TxID] = parseDecision(r.Detail)
		case "END":
			ended[r.TxID] = true
		}
	}
	for _, txID := range begunOrder {
		if ended[txID] {
			continue
		}
		if _, decided := fresh.decisions[txID]; !decided {
			fresh.log.Append(txID, "DECISION", Abort.String()) // presumed abort
			fresh.decisions[txID] = Abort
		}
		waiting := map[string]bool{}
		for _, node := range begun[txID] {
			waiting[node] = true
		}
		fresh.unacked[txID] = waiting
		fresh.Deliver(txID)
	}
	return fresh
}

// ---------------------------------------------------------------- demo

func show(a, b *Ledger) {
	fmt.Printf("  %s: alice=%v locked=%v | %s: bob=%v locked=%v | total=%v\n",
		a.Name, a.Balance("alice"), a.IsLocked("alice"), b.Name, b.Balance("bob"), b.IsLocked("bob"),
		mustPlus(a.Total(USD), b.Total(USD)))
}

func main() {
	clock := &SimClock{}
	net := NewSimNetwork(clock, 5, 1000)
	bankA := NewLedger("bank-a", map[string]Money{"alice": USDOf("500.00")})
	bankB := NewLedger("bank-b", map[string]Money{"bob": USDOf("100.00")})
	net.Register(bankA)
	net.Register(bankB)
	coord := NewCoordinator(net)

	fmt.Println("== 1. Happy path: 150.00 USD alice@bank-a -> bob@bank-b ==")
	out, _ := coord.Transfer("tx-1", bankA, "alice", bankB, "bob", USDOf("150.00"))
	fmt.Println("  " + out.String())
	show(bankA, bankB)
	fmt.Printf("  coordinator log: %v\n", coord.LogRecords())

	fmt.Println("\n== 2. Participant votes NO: 900.00 exceeds alice's balance ==")
	out, _ = coord.Transfer("tx-2", bankA, "alice", bankB, "bob", USDOf("900.00"))
	fmt.Println("  " + out.String())
	show(bankA, bankB)

	fmt.Println("\n== 3. bank-b unreachable during prepare: timeout is a NO ==")
	net.Partition("bank-b")
	before := clock.Now()
	out, _ = coord.Transfer("tx-3", bankA, "alice", bankB, "bob", USDOf("10.00"))
	fmt.Println("  " + out.String())
	fmt.Printf("  waited %d ms; bank-a state for tx-3 = %v\n", clock.Now()-before, bankA.StateOf("tx-3"))
	net.Heal("bank-b")
	show(bankA, bankB)

	fmt.Println("\n== 4. Coordinator dies after both YES votes, before logging the decision ==")
	coord.CrashPoint = AfterVotesBeforeDecisionLog
	if _, err := coord.Transfer("tx-4", bankA, "alice", bankB, "bob", USDOf("25.00")); err != nil {
		fmt.Println("  coordinator:", err)
	}
	fmt.Printf("  bank-a in doubt: %v, bank-b in doubt: %v\n", bankA.InDoubt(), bankB.InDoubt())
	show(bankA, bankB)
	fmt.Printf("  another coordinator tries alice while she is locked: %v  <- this is the blocking problem\n",
		bankA.Prepare("tx-5", Posting{"alice", USDOf("-1.00")}))
	fmt.Printf("  bank-b restarts from its own log and is still in doubt: %v\n", bankB.CrashAndRecover().InDoubt())
	coord = RecoverCoordinator(coord, net)
	d, _ := coord.DecisionOf("tx-4")
	fmt.Printf("  recovered coordinator: presumed abort -> tx-4 = %v\n", d)
	show(bankA, bankB)

	fmt.Println("\n== 5. Coordinator dies after logging COMMIT, before telling anyone ==")
	coord.CrashPoint = AfterDecisionLogBeforeSend
	if _, err := coord.Transfer("tx-6", bankA, "alice", bankB, "bob", USDOf("40.00")); err != nil {
		fmt.Println("  coordinator:", err)
	}
	fmt.Printf("  participants still PREPARED: %v, %v\n", bankA.StateOf("tx-6"), bankB.StateOf("tx-6"))
	coord = RecoverCoordinator(coord, net)
	d, _ = coord.DecisionOf("tx-6")
	fmt.Printf("  recovered coordinator redelivers %v -> %v, %v\n", d, bankA.StateOf("tx-6"), bankB.StateOf("tx-6"))
	show(bankA, bankB)

	fmt.Println("\n== 6. bank-b votes YES then crashes: the decision is final, delivery retries ==")
	net.CrashAfterVote("bank-b")
	out, _ = coord.Transfer("tx-7", bankA, "alice", bankB, "bob", USDOf("5.00"))
	fmt.Println("  " + out.String())
	fmt.Printf("  bank-a already %v, bank-b crashed while %v\n", bankA.StateOf("tx-7"), bankB.StateOf("tx-7"))
	bankB = bankB.CrashAndRecover()
	net.Replace(bankB)
	net.Heal("bank-b")
	fmt.Printf("  bank-b recovered from its log: in doubt %v, bob locked=%v\n", bankB.InDoubt(), bankB.IsLocked("bob"))
	fmt.Printf("  RetryPending finished %d tx; bank-b now %v\n", coord.RetryPending(), bankB.StateOf("tx-7"))
	show(bankA, bankB)
	fmt.Printf("\nmessages sent=%d timeouts=%d clock=%d ms\n", net.MessagesSent, net.Timeouts, clock.Now())
}
