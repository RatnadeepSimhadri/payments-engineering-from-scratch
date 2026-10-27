// Lesson: phases/04-concurrency/08-the-actor-model/docs/en.md
// The Actor Model
// Run:  go run .
// Test: go test ./...
// Sources: Hewitt, Bishop & Steiger, "A Universal Modular ACTOR Formalism for Artificial Intelligence" (IJCAI 1973);
// Armstrong, "Making reliable distributed systems in the presence of software errors" (PhD thesis, 2003);
// Akka documentation: Dispatchers, Mailboxes, Supervision (doc.akka.io). Go 1.22+, standard library only.
package main

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Currency is an ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01.
type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	EUR = Currency{"EUR", 2}
)

// Sentinel errors. Java's exceptions become values the supervisor can inspect with errors.Is.
var (
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrOverflow         = errors.New("amount overflows int64")
	ErrUnknownMessage   = errors.New("unknown message")
	ErrActorExists      = errors.New("actor already exists")
	ErrNoSuchActor      = errors.New("no such actor")
)

// CurrencyMismatchError carries both currencies and matches ErrCurrencyMismatch under errors.Is.
type CurrencyMismatchError struct{ A, B Currency }

func (e *CurrencyMismatchError) Error() string {
	return "cannot combine " + e.A.Code + " with " + e.B.Code
}

func (e *CurrencyMismatchError) Is(target error) bool { return target == ErrCurrencyMismatch }

// Money is an exact amount in minor units of one currency. Immutable, which is what makes it safe to put in a message.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func (m Money) requireSameCurrency(other Money) error {
	if m.Currency != other.Currency {
		return &CurrencyMismatchError{m.Currency, other.Currency}
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
	if other.MinorUnits == -1<<63 {
		return Money{}, ErrOverflow
	}
	diff, err := addChecked(m.MinorUnits, -other.MinorUnits)
	return Money{diff, m.Currency}, err
}

func (m Money) IsNegative() bool { return m.MinorUnits < 0 }

// Compare panics on a currency mismatch: a comparison cannot return an error, and the mismatch is a programming error.
func (m Money) Compare(other Money) int {
	if err := m.requireSameCurrency(other); err != nil {
		panic(err)
	}
	switch {
	case m.MinorUnits < other.MinorUnits:
		return -1
	case m.MinorUnits > other.MinorUnits:
		return 1
	}
	return 0
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
	fracText := ""
	if m.Currency.Exponent > 0 {
		fracText = "." + fmt.Sprintf("%0*d", m.Currency.Exponent, frac)
	}
	sign := ""
	if m.MinorUnits < 0 {
		sign = "-"
	}
	return sign + fmt.Sprint(whole) + fracText + " " + m.Currency.Code
}

// ---------------------------------------------------------------------------------------------------------------------
// The actor runtime: messages, references, mailboxes, cells, supervision, schedulers.
// ---------------------------------------------------------------------------------------------------------------------

// Message is the closed set of immutable values actors exchange. Go has no sealed interface, so the marker method
// plays that role and Describe's type switch is the exhaustive case list.
type Message interface{ isMessage() }

type Deposit struct {
	CorrelationID string
	Amount        Money
	ReplyTo       *ActorRef
}
type Withdraw struct {
	CorrelationID string
	Amount        Money
	ReplyTo       *ActorRef
}
type GetBalance struct{ ReplyTo *ActorRef }
type Deposited struct {
	CorrelationID string
	Account       string
	Balance       Money
}
type Withdrawn struct {
	CorrelationID string
	Account       string
	Amount        Money
	Balance       Money
}
type InsufficientFunds struct {
	CorrelationID string
	Account       string
	Requested     Money
	Balance       Money
}
type BalanceIs struct {
	Account string
	Balance Money
}
type Transfer struct {
	TransferID string
	From       string
	To         string
	Amount     Money
	ReplyTo    *ActorRef
}
type TransferResult struct {
	TransferID string
	OK         bool
	Reason     string
}
type Increment struct{}
type CountIs struct{ Count int64 }

// Crash makes any actor fail: the supervision demo's fault injector.
type Crash struct{ Reason string }

func (Deposit) isMessage()           {}
func (Withdraw) isMessage()          {}
func (GetBalance) isMessage()        {}
func (Deposited) isMessage()         {}
func (Withdrawn) isMessage()         {}
func (InsufficientFunds) isMessage() {}
func (BalanceIs) isMessage()         {}
func (Transfer) isMessage()          {}
func (TransferResult) isMessage()    {}
func (Increment) isMessage()         {}
func (CountIs) isMessage()           {}
func (Crash) isMessage()             {}

// Actor is what an actor does. One instance, one mailbox, one message at a time: no field of an Actor is ever read
// by two goroutines at once. Receive returns an error where the Java version throws; the supervisor decides what next.
type Actor interface {
	Bind(context *ActorContext)
	Receive(message Message) error
	// PreStart is called after a RESTART before the first message of the new incarnation.
	PreStart()
}

// BaseActor supplies Bind, Context, Self, and a no-op PreStart; concrete actors embed it.
type BaseActor struct{ context *ActorContext }

func (b *BaseActor) Bind(context *ActorContext) { b.context = context }
func (b *BaseActor) Context() *ActorContext     { return b.context }
func (b *BaseActor) Self() *ActorRef            { return b.context.Self() }
func (b *BaseActor) PreStart()                  {}

// ActorContext is what the runtime gives an actor: who it is, and how to reach others. Never another actor's object.
type ActorContext struct {
	self   *ActorRef
	system *ActorSystem
}

func (c *ActorContext) Self() *ActorRef                       { return c.self }
func (c *ActorContext) Lookup(name string) (*ActorRef, error) { return c.system.Ref(name) }

// SupervisorStrategy decides what the runtime does when Receive fails.
type SupervisorStrategy int

const (
	Resume  SupervisorStrategy = iota // drop the failing message, keep the actor's state
	Restart                           // drop the failing message, replace the actor with a fresh instance; mailbox survives
	Stop                              // drop the actor; later messages are dead letters
)

func (s SupervisorStrategy) String() string {
	return [...]string{"RESUME", "RESTART", "STOP"}[s]
}

// ActorRef is the only handle other code ever holds. Tell is the only operation.
type ActorRef struct {
	name string
	cell *ActorCell
}

func (r *ActorRef) Name() string { return r.name }

// Tell enqueues the message. A nil message is a programming error and panics, like Java's NullPointerException.
func (r *ActorRef) Tell(message Message) {
	if message == nil {
		panic("message must not be nil")
	}
	r.cell.enqueue(message)
}

func (r *ActorRef) String() string { return r.name }

// Mailbox is an unbounded FIFO queue safe for concurrent producers: Go's stand-in for ConcurrentLinkedQueue.
type Mailbox struct {
	mu    sync.Mutex
	items []Message
}

func (q *Mailbox) Offer(m Message) {
	q.mu.Lock()
	q.items = append(q.items, m)
	q.mu.Unlock()
}

// Poll removes the head; ok is false when the mailbox is empty.
func (q *Mailbox) Poll() (Message, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return nil, false
	}
	m := q.items[0]
	q.items[0] = nil
	q.items = q.items[1:]
	return m, true
}

func (q *Mailbox) IsEmpty() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items) == 0
}

// ActorCell is the runtime bookkeeping for one actor: mailbox, current instance, factory for restarts, counters.
type ActorCell struct {
	Name                  string
	Factory               func() Actor
	Strategy              SupervisorStrategy
	Mailbox               Mailbox
	Scheduled             atomic.Bool  // ThreadedScheduler: at most one run queued or running
	ConcurrentReceives    atomic.Int32 // invariant check: never exceeds 1
	MaxConcurrentReceives atomic.Int32
	Processed             atomic.Int64
	Restarts              atomic.Int32
	Failures              atomic.Int32
	DeadLetters           atomic.Int32
	Stopped               atomic.Bool
	Self                  *ActorRef

	mu     sync.Mutex // guards actor, which a restart replaces
	actor  Actor
	system *ActorSystem
}

func newActorCell(name string, factory func() Actor, strategy SupervisorStrategy, system *ActorSystem) *ActorCell {
	return &ActorCell{Name: name, Factory: factory, Strategy: strategy, system: system}
}

func (c *ActorCell) start() {
	c.Self = &ActorRef{c.Name, c}
	c.install(c.Factory())
}

func (c *ActorCell) install(a Actor) {
	a.Bind(&ActorContext{c.Self, c.system})
	a.PreStart()
	c.mu.Lock()
	c.actor = a
	c.mu.Unlock()
}

// Actor returns the current instance: a test-only peek that production code never takes, which is the point.
func (c *ActorCell) Actor() Actor {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.actor
}

func (c *ActorCell) enqueue(message Message) {
	if c.Stopped.Load() {
		c.DeadLetters.Add(1)
		c.system.deadLetter(c.Name, message)
		return
	}
	c.Mailbox.Offer(message)
	c.system.Scheduler().Schedule(c)
}

// ProcessOne processes one message. Returns false if the mailbox was empty. The scheduler guarantees no two calls overlap.
func (c *ActorCell) ProcessOne() bool {
	message, ok := c.Mailbox.Poll()
	if !ok {
		return false
	}
	if c.Stopped.Load() {
		c.DeadLetters.Add(1)
		c.system.deadLetter(c.Name, message)
		return true
	}
	now := c.ConcurrentReceives.Add(1)
	for {
		max := c.MaxConcurrentReceives.Load()
		if now <= max || c.MaxConcurrentReceives.CompareAndSwap(max, now) {
			break
		}
	}
	defer c.ConcurrentReceives.Add(-1)
	c.system.trace(c.Name, message)
	if err := c.Actor().Receive(message); err != nil {
		c.Failures.Add(1)
		c.supervise(message, err)
		return true
	}
	c.Processed.Add(1)
	return true
}

func (c *ActorCell) supervise(message Message, err error) {
	c.system.failure(c.Name, message, err, c.Strategy)
	switch c.Strategy {
	case Resume:
		// state kept, message dropped
	case Restart:
		c.Restarts.Add(1)
		c.install(c.Factory())
	case Stop:
		c.Stopped.Store(true)
		for {
			m, ok := c.Mailbox.Poll()
			if !ok {
				break
			}
			c.DeadLetters.Add(1)
			c.system.deadLetter(c.Name, m)
		}
	}
}

// Scheduler decides how mailboxes get processor time. Two implementations: deterministic for tests, threaded for production.
type Scheduler interface {
	Schedule(cell *ActorCell)
}

// DeterministicScheduler is single-threaded and replayable. Every actor with a non-empty mailbox is runnable; each
// step picks one runnable actor (by seeded JavaRandom, or round-robin with seed 0) and delivers exactly one message.
// The interleaving is a function of the seed, so a failing test can be re-run with the same seed and the same trace.
type DeterministicScheduler struct {
	runnable   []*ActorCell
	random     *JavaRandom
	roundRobin bool
	steps      int64
}

func NewDeterministicScheduler(seed int64) *DeterministicScheduler {
	return &DeterministicScheduler{random: NewJavaRandom(seed), roundRobin: seed == 0}
}

func (s *DeterministicScheduler) contains(cell *ActorCell) bool {
	for _, c := range s.runnable {
		if c == cell {
			return true
		}
	}
	return false
}

func (s *DeterministicScheduler) Schedule(cell *ActorCell) {
	if !s.contains(cell) {
		s.runnable = append(s.runnable, cell)
	}
}

// Step delivers one message to one actor. Returns false when nothing is runnable.
func (s *DeterministicScheduler) Step() bool {
	if len(s.runnable) == 0 {
		return false
	}
	var cell *ActorCell
	if s.roundRobin {
		cell = s.runnable[0]
		s.runnable = s.runnable[1:]
	} else {
		index := int(s.random.NextInt(int32(len(s.runnable))))
		cell = s.runnable[index]
		s.runnable = append(s.runnable[:index:index], s.runnable[index+1:]...)
	}
	cell.ProcessOne()
	s.steps++
	if !cell.Mailbox.IsEmpty() && !s.contains(cell) {
		s.runnable = append(s.runnable, cell)
	}
	return true
}

func (s *DeterministicScheduler) RunUntilIdle() int {
	delivered := 0
	for s.Step() {
		delivered++
	}
	return delivered
}

func (s *DeterministicScheduler) Steps() int64 { return s.steps }

// ThreadedScheduler is multi-goroutine. When a message arrives and the actor is not already scheduled, one run is
// submitted to a bounded worker pool. The run drains up to `throughput` messages, clears the flag, and re-schedules
// itself if more arrived meanwhile. The CAS on Scheduled is the whole guarantee that an actor's Receive never runs
// on two goroutines at once.
type ThreadedScheduler struct {
	sem        chan struct{} // bounded pool: at most cap(sem) runs execute at once
	wg         sync.WaitGroup
	throughput int
}

func NewThreadedScheduler(workers, throughput int) *ThreadedScheduler {
	return &ThreadedScheduler{sem: make(chan struct{}, workers), throughput: throughput}
}

func (s *ThreadedScheduler) Schedule(cell *ActorCell) {
	if cell.Scheduled.CompareAndSwap(false, true) {
		s.execute(func() { s.run(cell) })
	}
}

func (s *ThreadedScheduler) execute(task func()) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.sem <- struct{}{}
		defer func() { <-s.sem }()
		task()
	}()
}

func (s *ThreadedScheduler) run(cell *ActorCell) {
	defer func() {
		cell.Scheduled.Store(false)  // release first ...
		if !cell.Mailbox.IsEmpty() { // ... then re-check: a message may have landed in between
			s.Schedule(cell)
		}
	}()
	for i := 0; i < s.throughput && cell.ProcessOne(); i++ {
		// drain a batch
	}
}

// Close waits for every submitted run to finish, like closing Java's ExecutorService.
func (s *ThreadedScheduler) Close() { s.wg.Wait() }

// ActorSystem owns the cells, the scheduler, and the trace.
type ActorSystem struct {
	scheduler   Scheduler
	mu          sync.Mutex
	cells       map[string]*ActorCell
	order       []string
	traceLines  []string
	failLines   []string
	deadLetters atomic.Int32
	tracing     bool
}

func NewActorSystem(scheduler Scheduler, tracing bool) *ActorSystem {
	return &ActorSystem{scheduler: scheduler, cells: map[string]*ActorCell{}, tracing: tracing}
}

func (s *ActorSystem) Scheduler() Scheduler { return s.scheduler }

func (s *ActorSystem) Spawn(name string, factory func() Actor, strategy SupervisorStrategy) (*ActorRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.cells[name]; exists {
		return nil, fmt.Errorf("%w: %s", ErrActorExists, name)
	}
	cell := newActorCell(name, factory, strategy, s)
	cell.start()
	s.cells[name] = cell
	s.order = append(s.order, name)
	return cell.Self, nil
}

func (s *ActorSystem) Ref(name string) (*ActorRef, error) {
	s.mu.Lock()
	cell := s.cells[name]
	s.mu.Unlock()
	if cell == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoSuchActor, name)
	}
	return cell.Self, nil
}

func (s *ActorSystem) Cell(name string) *ActorCell {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cells[name]
}

func (s *ActorSystem) trace(actor string, message Message) {
	if !s.tracing {
		return
	}
	s.mu.Lock()
	s.traceLines = append(s.traceLines, actor+" <- "+Describe(message))
	s.mu.Unlock()
}

func (s *ActorSystem) failure(actor string, message Message, err error, strategy SupervisorStrategy) {
	s.mu.Lock()
	s.failLines = append(s.failLines, actor+" failed on "+Describe(message)+": "+err.Error()+" -> "+strategy.String())
	s.mu.Unlock()
}

func (s *ActorSystem) deadLetter(string, Message) { s.deadLetters.Add(1) }

// Trace returns a copy of the delivery trace in order.
func (s *ActorSystem) Trace() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.traceLines...)
}

func (s *ActorSystem) Failures() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.failLines...)
}

func (s *ActorSystem) DeadLetters() int { return int(s.deadLetters.Load()) }

// Describe renders a message for traces; the type switch is exhaustive over the Message set.
func Describe(m Message) string {
	switch v := m.(type) {
	case Deposit:
		return "Deposit(" + v.CorrelationID + ", " + v.Amount.String() + ")"
	case Withdraw:
		return "Withdraw(" + v.CorrelationID + ", " + v.Amount.String() + ")"
	case Deposited:
		return "Deposited(" + v.CorrelationID + ", " + v.Account + ", " + v.Balance.String() + ")"
	case Withdrawn:
		return "Withdrawn(" + v.CorrelationID + ", " + v.Account + ", " + v.Amount.String() + ")"
	case InsufficientFunds:
		return "InsufficientFunds(" + v.CorrelationID + ", " + v.Account + ", wanted " + v.Requested.String() + ", has " + v.Balance.String() + ")"
	case Transfer:
		return "Transfer(" + v.TransferID + ", " + v.From + " -> " + v.To + ", " + v.Amount.String() + ")"
	case TransferResult:
		if v.OK {
			return "TransferResult(" + v.TransferID + ", ok)"
		}
		return "TransferResult(" + v.TransferID + ", failed: " + v.Reason + ")"
	case GetBalance:
		return "GetBalance"
	case BalanceIs:
		return "BalanceIs(" + v.Account + ", " + v.Balance.String() + ")"
	case Increment:
		return "Increment"
	case CountIs:
		return fmt.Sprintf("CountIs(%d)", v.Count)
	case Crash:
		return "Crash(" + v.Reason + ")"
	}
	return fmt.Sprintf("%T", m)
}

// ---------------------------------------------------------------------------------------------------------------------
// The payments actors: one account = one actor, a coordinator for transfers, a probe for tests.
// ---------------------------------------------------------------------------------------------------------------------

// AccountActor owns one balance. No lock anywhere: the mailbox serialises every access.
type AccountActor struct {
	BaseActor
	accountID string
	balance   Money
}

// NewAccountActor panics on a negative opening balance: a factory cannot report an error, and this is a programming error.
func NewAccountActor(accountID string, opening Money) *AccountActor {
	if opening.IsNegative() {
		panic("opening balance must not be negative")
	}
	return &AccountActor{accountID: accountID, balance: opening}
}

func (a *AccountActor) Receive(message Message) error {
	switch m := message.(type) {
	case Deposit:
		next, err := a.balance.Plus(m.Amount) // fails with ErrCurrencyMismatch for a wrong-currency deposit
		if err != nil {
			return err
		}
		a.balance = next
		if m.ReplyTo != nil {
			m.ReplyTo.Tell(Deposited{m.CorrelationID, a.accountID, a.balance})
		}
	case Withdraw:
		if a.balance.Compare(m.Amount) < 0 { // check ...
			if m.ReplyTo != nil {
				m.ReplyTo.Tell(InsufficientFunds{m.CorrelationID, a.accountID, m.Amount, a.balance})
			}
		} else {
			next, err := a.balance.Minus(m.Amount) // ... and act: nothing can interleave between them
			if err != nil {
				return err
			}
			a.balance = next
			if m.ReplyTo != nil {
				m.ReplyTo.Tell(Withdrawn{m.CorrelationID, a.accountID, m.Amount, a.balance})
			}
		}
	case GetBalance:
		m.ReplyTo.Tell(BalanceIs{a.accountID, a.balance})
	case Crash:
		return errors.New(m.Reason)
	default:
		return fmt.Errorf("%w: account does not understand %s", ErrUnknownMessage, Describe(message))
	}
	return nil
}

func (a *AccountActor) Balance() Money { return a.balance }

// TransferCoordinator moves money between two account actors with two messages: Withdraw from the source, then
// Deposit to the destination. It never holds both balances; it holds the transfer's state and reacts to replies.
// A failed withdrawal fails the transfer with nothing to undo; a deposit cannot fail for funds reasons.
type TransferCoordinator struct {
	BaseActor
	inFlight  map[string]Transfer
	completed int
	rejected  int
}

func NewTransferCoordinator() *TransferCoordinator {
	return &TransferCoordinator{inFlight: map[string]Transfer{}}
}

func (c *TransferCoordinator) Receive(message Message) error {
	switch m := message.(type) {
	case Transfer:
		c.inFlight[m.TransferID] = m
		from, err := c.Context().Lookup(m.From)
		if err != nil {
			return err
		}
		from.Tell(Withdraw{m.TransferID, m.Amount, c.Self()})
	case Withdrawn:
		t := c.inFlight[m.CorrelationID]
		to, err := c.Context().Lookup(t.To)
		if err != nil {
			return err
		}
		to.Tell(Deposit{t.TransferID, t.Amount, c.Self()})
	case Deposited:
		t := c.inFlight[m.CorrelationID]
		delete(c.inFlight, m.CorrelationID)
		c.completed++
		if t.ReplyTo != nil {
			t.ReplyTo.Tell(TransferResult{t.TransferID, true, ""})
		}
	case InsufficientFunds:
		t := c.inFlight[m.CorrelationID]
		delete(c.inFlight, m.CorrelationID)
		c.rejected++
		if t.ReplyTo != nil {
			t.ReplyTo.Tell(TransferResult{t.TransferID, false,
				m.Account + " has " + m.Balance.String() + ", needs " + m.Requested.String()})
		}
	default:
		return fmt.Errorf("%w: coordinator does not understand %s", ErrUnknownMessage, Describe(message))
	}
	return nil
}

func (c *TransferCoordinator) Completed() int { return c.completed }
func (c *TransferCoordinator) Rejected() int  { return c.rejected }
func (c *TransferCoordinator) InFlight() int  { return len(c.inFlight) }

// CounterActor counts increments; used to show what RESTART does to state and RESUME does not.
type CounterActor struct {
	BaseActor
	count int64
}

func NewCounterActor() *CounterActor { return &CounterActor{} }

func (c *CounterActor) Receive(message Message) error {
	switch m := message.(type) {
	case Increment:
		c.count++
	case GetBalance:
		m.ReplyTo.Tell(CountIs{c.count})
	case Crash:
		return errors.New(m.Reason)
	default:
		return fmt.Errorf("%w: counter does not understand %s", ErrUnknownMessage, Describe(message))
	}
	return nil
}

// CountDownLatch lets a goroutine wait until `expected` events have happened, like java.util.concurrent.CountDownLatch.
type CountDownLatch struct {
	mu    sync.Mutex
	count int
	done  chan struct{}
}

func NewCountDownLatch(expected int) *CountDownLatch {
	l := &CountDownLatch{count: expected, done: make(chan struct{})}
	if expected <= 0 {
		close(l.done)
	}
	return l
}

func (l *CountDownLatch) CountDown() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.count <= 0 {
		return
	}
	l.count--
	if l.count == 0 {
		close(l.done)
	}
}

// Await returns true when the count reached zero before the timeout.
func (l *CountDownLatch) Await(timeout time.Duration) bool {
	select {
	case <-l.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// ProbeActor is a test inbox: it records everything it receives.
type ProbeActor struct {
	BaseActor
	mu       sync.Mutex
	received []Message
	Latch    *CountDownLatch
}

func NewProbeActor(expected int) *ProbeActor { return &ProbeActor{Latch: NewCountDownLatch(expected)} }

func (p *ProbeActor) Receive(message Message) error {
	p.mu.Lock()
	p.received = append(p.received, message)
	p.mu.Unlock()
	p.Latch.CountDown()
	return nil
}

// Received returns a snapshot of every message so far, in arrival order.
func (p *ProbeActor) Received() []Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Message(nil), p.received...)
}

func (p *ProbeActor) TransferResults() []TransferResult {
	var out []TransferResult
	for _, m := range p.Received() {
		if r, ok := m.(TransferResult); ok {
			out = append(out, r)
		}
	}
	return out
}

// Balances returns the latest BalanceIs per account, keyed by account.
func (p *ProbeActor) Balances() map[string]Money {
	out := map[string]Money{}
	for _, m := range p.Received() {
		if b, ok := m.(BalanceIs); ok {
			out[b.Account] = b.Balance
		}
	}
	return out
}

// ---------------------------------------------------------------------------------------------------------------------
// Demo
// ---------------------------------------------------------------------------------------------------------------------

func usd(minor int64) Money { return OfMinor(minor, USD) }

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// BankWith builds three accounts and a coordinator on the given scheduler. Total money in the system: 300.00 USD.
func BankWith(scheduler Scheduler, tracing bool) *ActorSystem {
	system := NewActorSystem(scheduler, tracing)
	for _, name := range []string{"acct-A", "acct-B", "acct-C"} {
		name := name
		must(system.Spawn(name, func() Actor { return NewAccountActor(name, usd(10_000)) }, Resume))
	}
	must(system.Spawn("transfers", func() Actor { return NewTransferCoordinator() }, Resume))
	return system
}

// Total sums the named accounts' balances by peeking at the actor instances (test-only).
func Total(system *ActorSystem, accounts ...string) Money {
	sum := usd(0)
	for _, a := range accounts {
		sum = must(sum.Plus(ActorOf(system, a).(*AccountActor).Balance()))
	}
	return sum
}

// ActorOf is a test-only peek at the actor instance; production code never does this, which is the point of the model.
func ActorOf(system *ActorSystem, name string) Actor { return system.Cell(name).Actor() }

func ScriptedTransfers(replyTo *ActorRef) []Transfer {
	return []Transfer{
		{"t1", "acct-A", "acct-B", usd(7_500), replyTo},
		{"t2", "acct-B", "acct-C", usd(12_000), replyTo},
		{"t3", "acct-C", "acct-A", usd(5_000), replyTo},
		{"t4", "acct-A", "acct-C", usd(9_000), replyTo},
		{"t5", "acct-B", "acct-A", usd(2_500), replyTo},
		{"t6", "acct-C", "acct-B", usd(1_000), replyTo},
	}
}

func demoDeterministic(seed int64) {
	scheduler := NewDeterministicScheduler(seed)
	system := BankWith(scheduler, true)
	probe := must(system.Spawn("probe", func() Actor { return NewProbeActor(0) }, Resume))
	transfers := must(system.Ref("transfers"))
	for _, t := range ScriptedTransfers(probe) {
		transfers.Tell(t)
	}
	delivered := scheduler.RunUntilIdle()
	p := ActorOf(system, "probe").(*ProbeActor)
	fmt.Printf("seed %d: %d messages delivered, %d traced\n", seed, delivered, len(system.Trace()))
	for _, r := range p.TransferResults() {
		fmt.Println("  " + Describe(r))
	}
	fmt.Printf("  balances: A=%s B=%s C=%s  total=%s\n", ActorOf(system, "acct-A").(*AccountActor).Balance(),
		ActorOf(system, "acct-B").(*AccountActor).Balance(), ActorOf(system, "acct-C").(*AccountActor).Balance(),
		Total(system, "acct-A", "acct-B", "acct-C"))
}

func main() {
	fmt.Println("== One account, one mailbox: six transfers on a deterministic scheduler ==")
	demoDeterministic(42)
	fmt.Println("first 6 trace lines (seed 42):")
	s42 := NewDeterministicScheduler(42)
	sys42 := BankWith(s42, true)
	probe42 := must(sys42.Spawn("probe", func() Actor { return NewProbeActor(0) }, Resume))
	for _, t := range ScriptedTransfers(probe42) {
		must(sys42.Ref("transfers")).Tell(t)
	}
	s42.RunUntilIdle()
	for _, line := range sys42.Trace()[:6] {
		fmt.Println("  " + line)
	}
	fmt.Println()
	fmt.Println("Same script, different seed: a different interleaving, the same conservation of money.")
	demoDeterministic(7)
	fmt.Println()

	fmt.Println("== Supervision: RESUME keeps state, RESTART resets it, STOP turns the mailbox into dead letters ==")
	sched := NewDeterministicScheduler(0)
	system := NewActorSystem(sched, false)
	probe := must(system.Spawn("probe", func() Actor { return NewProbeActor(0) }, Resume))
	account := must(system.Spawn("acct-X", func() Actor { return NewAccountActor("acct-X", usd(5_000)) }, Resume))
	resumed := must(system.Spawn("counter-resume", func() Actor { return NewCounterActor() }, Resume))
	restarted := must(system.Spawn("counter-restart", func() Actor { return NewCounterActor() }, Restart))
	stopped := must(system.Spawn("counter-stop", func() Actor { return NewCounterActor() }, Stop))
	account.Tell(Deposit{"d1", OfMinor(100, EUR), probe}) // wrong currency: Receive fails
	account.Tell(GetBalance{probe})
	for _, c := range []*ActorRef{resumed, restarted, stopped} {
		c.Tell(Increment{})
		c.Tell(Increment{})
		c.Tell(Crash{"simulated bug"})
		c.Tell(Increment{})
		c.Tell(GetBalance{probe})
	}
	sched.RunUntilIdle()
	for _, f := range system.Failures() {
		fmt.Println("  " + f)
	}
	p := ActorOf(system, "probe").(*ProbeActor)
	fmt.Println("  acct-X balance after the bad deposit was dropped: " + p.Balances()["acct-X"].String())
	var counts []int64
	for _, m := range p.Received() {
		if c, ok := m.(CountIs); ok {
			counts = append(counts, c.Count)
		}
	}
	fmt.Printf("  counter-resume reports %d (2 before the crash + 1 after); counter-restart reports %d (state reset); counter-stop reports nothing, dead letters = %d\n",
		counts[0], counts[1], system.DeadLetters())
	fmt.Println()

	fmt.Println("== Threaded scheduler: 4 workers x 25,000 deposits into one account, no locks in AccountActor ==")
	pool := NewThreadedScheduler(4, 64)
	threaded := NewActorSystem(pool, false)
	acct := must(threaded.Spawn("acct-hot", func() Actor { return NewAccountActor("acct-hot", usd(0)) }, Resume))
	workers, perWorker := 4, 25_000
	var done sync.WaitGroup
	for t := 0; t < workers; t++ {
		done.Add(1)
		go func() {
			defer done.Done()
			for i := 0; i < perWorker; i++ {
				acct.Tell(Deposit{"x", usd(1), nil})
			}
		}()
	}
	done.Wait()
	probeRef := must(threaded.Spawn("probe", func() Actor { return NewProbeActor(1) }, Resume))
	acct.Tell(GetBalance{probeRef})
	pr := ActorOf(threaded, "probe").(*ProbeActor)
	pr.Latch.Await(10 * time.Second)
	pool.Close()
	cell := threaded.Cell("acct-hot")
	fmt.Printf("  balance = %s (expected 1000.00 USD); messages processed = %d; max concurrent Receive = %d\n",
		pr.Balances()["acct-hot"], cell.Processed.Load(), cell.MaxConcurrentReceives.Load())
}
