// Lesson: phases/07-storage-and-transactions/12-saga-pattern/docs/en.md
// The Saga Pattern: Compensations Instead of Locks
// Run:  go run .
// Test: go test ./...
// Sources: Garcia-Molina & Salem, "Sagas" (SIGMOD 1987); Richardson, "Microservices Patterns" ch. 4 (semantic lock).
// Go 1.22+, standard library only. Money is integer minor units (Phase 10, Lesson 01). Same design as Java and Python.
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
	// ErrStepFailed: a step refused to do its work. The saga reacts by compensating everything before it.
	ErrStepFailed = errors.New("step failed")
	// ErrCrash is returned at an injected crash point. The simulation stops where the real process would die.
	ErrCrash = errors.New("crash")
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

func subChecked(a, b int64) (int64, error) {
	if (b < 0 && a > math.MaxInt64+b) || (b > 0 && a < math.MinInt64+b) {
		return 0, ErrOverflow
	}
	return a - b, nil
}

func (m Money) requireSameCurrency(other Money) error {
	if m.Currency != other.Currency {
		return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.Currency.Code, other.Currency.Code)
	}
	return nil
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
	diff, err := subChecked(m.MinorUnits, other.MinorUnits)
	return Money{diff, m.Currency}, err
}

func (m Money) IsNegative() bool { return m.MinorUnits < 0 }

// Compare panics on a currency mismatch: a comparison cannot report an error, and the mismatch is a programming error.
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

// SimClock only moves when the simulation says so. Timestamps in the saga log come from here.
type SimClock struct{ nowMs int64 }

func NewSimClock(startMs int64) *SimClock { return &SimClock{nowMs: startMs} }
func (c *SimClock) Now() int64            { return c.nowMs }
func (c *SimClock) Advance(ms int64)      { c.nowMs += ms }

// StepFailedError carries the step's refusal verbatim; errors.Is(err, ErrStepFailed) matches it.
type StepFailedError struct{ Msg string }

func (e *StepFailedError) Error() string        { return e.Msg }
func (e *StepFailedError) Is(target error) bool { return target == ErrStepFailed }

func stepFailed(format string, args ...any) error {
	return &StepFailedError{Msg: fmt.Sprintf(format, args...)}
}

// CrashError is returned at an injected crash point; errors.Is(err, ErrCrash) matches it.
type CrashError struct{ Where string }

func (e *CrashError) Error() string        { return "crash at " + e.Where }
func (e *CrashError) Is(target error) bool { return target == ErrCrash }

// CrashPoint says where the orchestrator dies: after a step's side effect happened but before the log recorded it.
type CrashPoint struct {
	Step               string
	DuringCompensation bool
}

var NoCrash = CrashPoint{}

func AfterExecute(step string) CrashPoint    { return CrashPoint{step, false} }
func AfterCompensate(step string) CrashPoint { return CrashPoint{step, true} }

// Order is the business request the saga carries from step to step.
type Order struct {
	SagaID   string
	Customer string
	Sku      string
	Quantity int
	Amount   Money
}

type SagaStatus int

const (
	Running SagaStatus = iota
	Compensating
	Completed
	Compensated
)

func (s SagaStatus) String() string {
	return [...]string{"RUNNING", "COMPENSATING", "COMPLETED", "COMPENSATED"}[s]
}

// SagaRecord is one durable saga-log record. The orchestrator appends one before and one after every step and compensation.
// Order is non-nil only on SAGA_STARTED, so recovery can rebuild the request.
type SagaRecord struct {
	Seq    int64
	AtMs   int64
	SagaID string
	Kind   string
	Step   string
	Detail string
	Order  *Order
}

func (r SagaRecord) String() string {
	s := fmt.Sprintf("%d t=%d %s %s", r.Seq, r.AtMs, r.SagaID, r.Kind)
	if r.Step != "" {
		s += " " + r.Step
	}
	if r.Detail != "" {
		s += " (" + r.Detail + ")"
	}
	return s
}

// Record kinds.
const (
	SagaStarted     = "SAGA_STARTED"
	StepStarted     = "STEP_STARTED"
	StepCompleted   = "STEP_COMPLETED"
	StepFailed      = "STEP_FAILED"
	CompStarted     = "COMP_STARTED"
	CompCompleted   = "COMP_COMPLETED"
	SagaCompleted   = "SAGA_COMPLETED"
	SagaCompensated = "SAGA_COMPENSATED"
)

// SagaLog is append-only. Append is the durability point. The SAGA_STARTED record carries the Order so recovery can rebuild it.
type SagaLog struct {
	records []SagaRecord
	clock   *SimClock
}

func NewSagaLog(clock *SimClock) *SagaLog { return &SagaLog{clock: clock} }

func (l *SagaLog) Append(sagaID, kind, step, detail string, order *Order) {
	l.records = append(l.records, SagaRecord{int64(len(l.records) + 1), l.clock.Now(), sagaID, kind, step, detail, order})
}

// Records returns a copy so callers cannot mutate the log.
func (l *SagaLog) Records() []SagaRecord {
	out := make([]SagaRecord, len(l.records))
	copy(out, l.records)
	return out
}

func (l *SagaLog) ForSaga(sagaID string) []SagaRecord {
	out := []SagaRecord{}
	for _, r := range l.records {
		if r.SagaID == sagaID {
			out = append(out, r)
		}
	}
	return out
}

// SagaIDs lists every saga in first-appearance order (Java's LinkedHashSet).
func (l *SagaLog) SagaIDs() []string {
	seen := map[string]bool{}
	ids := []string{}
	for _, r := range l.records {
		if !seen[r.SagaID] {
			seen[r.SagaID] = true
			ids = append(ids, r.SagaID)
		}
	}
	return ids
}

// mustMoney unwraps an internal Money operation whose failure (overflow, mixed currencies inside one account) is a
// programming error that the Java version would surface as an unchecked exception.
func mustMoney(m Money, err error) Money {
	if err != nil {
		panic(err)
	}
	return m
}

// WalletService: a reservation is a hold: it reduces what is available without moving the balance.
// Every mutating call takes an idempotency key (the saga id) and replays its earlier answer on a repeat.
type WalletService struct {
	balances    map[string]Money
	holdAccount map[string]string // key -> account
	holdAmount  map[string]Money  // key -> amount still held
	captured    map[string]Money  // key -> amount captured
	released    map[string]bool
	refunded    map[string]bool
	Calls       int
}

func NewWalletService(opening map[string]Money) *WalletService {
	w := &WalletService{balances: map[string]Money{}, holdAccount: map[string]string{}, holdAmount: map[string]Money{},
		captured: map[string]Money{}, released: map[string]bool{}, refunded: map[string]bool{}}
	for k, v := range opening {
		w.balances[k] = v
	}
	return w
}

func (w *WalletService) Balance(account string) Money { return w.balances[account] }

// Available is the balance minus every live hold on the account: the number a second saga must consult.
func (w *WalletService) Available(account string) Money {
	a := w.balances[account]
	for key, acct := range w.holdAccount {
		if acct == account {
			a = mustMoney(a.Minus(w.holdAmount[key]))
		}
	}
	return a
}

func (w *WalletService) HasHold(key string) bool {
	amt, ok := w.holdAmount[key]
	return ok && amt != Zero(amt.Currency)
}

func (w *WalletService) IsCaptured(key string) bool {
	_, ok := w.captured[key]
	return ok && !w.refunded[key]
}

// Reserve is step 1. Fails if the available balance cannot cover the amount. Idempotent on key.
func (w *WalletService) Reserve(key, account string, amount Money) error {
	w.Calls++
	if _, replay := w.holdAccount[key]; replay {
		return nil
	}
	if _, ok := w.balances[account]; !ok {
		return stepFailed("unknown account %s", account)
	}
	if w.Available(account).Compare(amount) < 0 {
		return stepFailed("insufficient available funds: %s < %s", w.Available(account), amount)
	}
	w.holdAccount[key] = account
	w.holdAmount[key] = amount
	return nil
}

// Release is the compensation of Reserve: drop the hold. Legal even if the hold never existed (the step may not have run).
func (w *WalletService) Release(key string) {
	w.Calls++
	if w.released[key] {
		return
	}
	if _, held := w.holdAccount[key]; held {
		w.holdAmount[key] = Zero(w.holdAmount[key].Currency)
		delete(w.holdAccount, key)
	}
	w.released[key] = true
}

// Capture is step 2. Turns the hold into a debit. Idempotent on key.
func (w *WalletService) Capture(key string) error {
	w.Calls++
	if _, replay := w.captured[key]; replay {
		return nil
	}
	account, held := w.holdAccount[key]
	if !held {
		return stepFailed("no hold to capture for %s", key)
	}
	amount := w.holdAmount[key]
	w.balances[account] = mustMoney(w.balances[account].Minus(amount))
	w.holdAmount[key] = Zero(amount.Currency)
	delete(w.holdAccount, key)
	w.captured[key] = amount
	return nil
}

// Refund is the compensation of Capture: credit the money back. Not an undo: the debit and the refund both stay in history.
func (w *WalletService) Refund(key, account string) {
	w.Calls++
	if w.refunded[key] {
		return
	}
	if amount, ok := w.captured[key]; ok {
		w.balances[account] = mustMoney(w.balances[account].Plus(amount))
	}
	w.refunded[key] = true
}

// ShippingService: stock per SKU and shipments keyed by saga id.
type ShippingService struct {
	stock     map[string]int
	shipments map[string]int // key -> quantity shipped
	cancelled map[string]bool
	Calls     int
}

func NewShippingService(opening map[string]int) *ShippingService {
	s := &ShippingService{stock: map[string]int{}, shipments: map[string]int{}, cancelled: map[string]bool{}}
	for k, v := range opening {
		s.stock[k] = v
	}
	return s
}

func (s *ShippingService) StockOf(sku string) int { return s.stock[sku] }

func (s *ShippingService) IsShipped(key string) bool {
	_, ok := s.shipments[key]
	return ok && !s.cancelled[key]
}

// Ship is step 3. Fails when stock is short. Idempotent on key.
func (s *ShippingService) Ship(key, sku string, quantity int) error {
	s.Calls++
	if _, replay := s.shipments[key]; replay {
		return nil
	}
	if s.StockOf(sku) < quantity {
		return stepFailed("out of stock: %s has %d, need %d", sku, s.StockOf(sku), quantity)
	}
	s.stock[sku] = s.StockOf(sku) - quantity
	s.shipments[key] = quantity
	return nil
}

// CancelShipment is the compensation of Ship: cancel and restock.
func (s *ShippingService) CancelShipment(key, sku string) {
	s.Calls++
	if s.cancelled[key] {
		return
	}
	if qty, ok := s.shipments[key]; ok {
		s.stock[sku] = s.StockOf(sku) + qty
	}
	s.cancelled[key] = true
}

// SagaStep is a forward action and the compensation that semantically undoes it. Both must be idempotent.
type SagaStep interface {
	Name() string
	Execute(order Order) error
	Compensate(order Order) error
}

type ReserveFunds struct{ wallet *WalletService }

func (s ReserveFunds) Name() string             { return "reserve-funds" }
func (s ReserveFunds) Execute(o Order) error    { return s.wallet.Reserve(o.SagaID, o.Customer, o.Amount) }
func (s ReserveFunds) Compensate(o Order) error { s.wallet.Release(o.SagaID); return nil }

type CaptureFunds struct{ wallet *WalletService }

func (s CaptureFunds) Name() string             { return "capture-funds" }
func (s CaptureFunds) Execute(o Order) error    { return s.wallet.Capture(o.SagaID) }
func (s CaptureFunds) Compensate(o Order) error { s.wallet.Refund(o.SagaID, o.Customer); return nil }

type ShipOrder struct{ shipping *ShippingService }

func (s ShipOrder) Name() string             { return "ship-order" }
func (s ShipOrder) Execute(o Order) error    { return s.shipping.Ship(o.SagaID, o.Sku, o.Quantity) }
func (s ShipOrder) Compensate(o Order) error { s.shipping.CancelShipment(o.SagaID, o.Sku); return nil }

// SagaOutcome is what the orchestrator reports.
type SagaOutcome struct {
	SagaID     string
	Status     SagaStatus
	FailedStep string
	Reason     string
}

func (o SagaOutcome) String() string {
	s := o.SagaID + " -> " + o.Status.String()
	if o.FailedStep != "" {
		s += " (failed at " + o.FailedStep + ": " + o.Reason + ")"
	}
	return s
}

func formatOutcomes(outs []SagaOutcome) string {
	parts := make([]string, len(outs))
	for i, o := range outs {
		parts[i] = o.String()
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// SagaOrchestrator runs the steps in order; on failure runs the compensations of every completed step in reverse order.
// The log is appended before and after every action so a restart can resume from the log alone.
type SagaOrchestrator struct {
	log           *SagaLog
	steps         []SagaStep
	clock         *SimClock
	stepLatencyMs int64
	CrashPoint    CrashPoint
}

func NewSagaOrchestrator(log *SagaLog, steps []SagaStep, clock *SimClock, stepLatencyMs int64) *SagaOrchestrator {
	copied := make([]SagaStep, len(steps))
	copy(copied, steps)
	return &SagaOrchestrator{log: log, steps: copied, clock: clock, stepLatencyMs: stepLatencyMs, CrashPoint: NoCrash}
}

func (s *SagaOrchestrator) Log() *SagaLog { return s.log }

// Run executes the saga. The error is a *CrashError at an injected crash point (or an unexpected failure), never a step refusal:
// step refusals are the saga's business and end in a COMPENSATED outcome.
func (s *SagaOrchestrator) Run(order Order) (SagaOutcome, error) {
	o := order
	s.log.Append(order.SagaID, SagaStarted, "", "", &o)
	return s.forward(order, 0)
}

// forward executes steps from index from. A failure at step i compensates i-1 .. 0.
func (s *SagaOrchestrator) forward(order Order, from int) (SagaOutcome, error) {
	for i := from; i < len(s.steps); i++ {
		step := s.steps[i]
		s.log.Append(order.SagaID, StepStarted, step.Name(), "", nil)
		s.clock.Advance(s.stepLatencyMs)
		if err := step.Execute(order); err != nil {
			if !errors.Is(err, ErrStepFailed) {
				return SagaOutcome{}, err
			}
			s.log.Append(order.SagaID, StepFailed, step.Name(), err.Error(), nil)
			return s.backward(order, i-1, step.Name(), err.Error())
		}
		if s.CrashPoint == AfterExecute(step.Name()) {
			return SagaOutcome{}, &CrashError{"after " + step.Name() + " executed, before STEP_COMPLETED logged"}
		}
		s.log.Append(order.SagaID, StepCompleted, step.Name(), "", nil)
	}
	s.log.Append(order.SagaID, SagaCompleted, "", "", nil)
	return SagaOutcome{order.SagaID, Completed, "", ""}, nil
}

// backward compensates steps from index from down to 0. Compensations are not allowed to fail; they are retried.
func (s *SagaOrchestrator) backward(order Order, from int, failedStep, reason string) (SagaOutcome, error) {
	for i := from; i >= 0; i-- {
		step := s.steps[i]
		s.log.Append(order.SagaID, CompStarted, step.Name(), "", nil)
		s.clock.Advance(s.stepLatencyMs)
		if err := step.Compensate(order); err != nil {
			return SagaOutcome{}, err
		}
		if s.CrashPoint == AfterCompensate(step.Name()) {
			return SagaOutcome{}, &CrashError{"after " + step.Name() + " compensated, before COMP_COMPLETED logged"}
		}
		s.log.Append(order.SagaID, CompCompleted, step.Name(), "", nil)
	}
	s.log.Append(order.SagaID, SagaCompensated, "", "", nil)
	return SagaOutcome{order.SagaID, Compensated, failedStep, reason}, nil
}

// StatusOf says where a saga stands according to the log alone.
func StatusOf(records []SagaRecord) SagaStatus {
	status := Running
	for _, r := range records {
		switch r.Kind {
		case StepFailed, CompStarted:
			status = Compensating
		case SagaCompleted:
			status = Completed
		case SagaCompensated:
			status = Compensated
		}
	}
	return status
}

// Recover resumes every unfinished saga from the log. A STEP_STARTED without STEP_COMPLETED is re-executed: the step
// may or may not have happened, and idempotency makes re-execution safe either way. Likewise for compensations.
func (s *SagaOrchestrator) Recover() ([]SagaOutcome, error) {
	s.CrashPoint = NoCrash
	resumed := []SagaOutcome{}
	index := map[string]int{}
	for i, step := range s.steps {
		index[step.Name()] = i
	}
	for _, sagaID := range s.log.SagaIDs() {
		records := s.log.ForSaga(sagaID)
		status := StatusOf(records)
		if status == Completed || status == Compensated {
			continue
		}
		order := *records[0].Order
		lastCompleted, lastCompensated := -1, len(s.steps)
		failedStep, reason := "", ""
		for _, r := range records {
			switch r.Kind {
			case StepCompleted:
				lastCompleted = max(lastCompleted, index[r.Step])
			case StepFailed:
				failedStep, reason = r.Step, r.Detail
			case CompCompleted:
				lastCompensated = min(lastCompensated, index[r.Step])
			}
		}
		var out SagaOutcome
		var err error
		if status == Running {
			out, err = s.forward(order, lastCompleted+1)
		} else {
			failedIndex := len(s.steps) - 1
			if failedStep != "" {
				failedIndex = index[failedStep] - 1
			}
			if reason == "" {
				reason = "resumed after crash"
			}
			out, err = s.backward(order, min(lastCompensated-1, failedIndex), failedStep, reason)
		}
		if err != nil {
			return resumed, err
		}
		resumed = append(resumed, out)
	}
	return resumed, nil
}

// ---- worked example ----

func usd(text string) Money {
	neg := strings.HasPrefix(text, "-")
	minor, err := strconv.ParseInt(strings.ReplaceAll(strings.TrimPrefix(text, "-"), ".", ""), 10, 64)
	if err != nil {
		panic(err)
	}
	if neg {
		minor = -minor
	}
	return OfMinor(minor, USD)
}

func show(wallet *WalletService, shipping *ShippingService) {
	fmt.Printf("  alice balance=%s available=%s | widget stock=%d\n", wallet.Balance("alice"), wallet.Available("alice"), shipping.StockOf("widget"))
}

func printLog(log *SagaLog, sagaID string) {
	for _, r := range log.ForSaga(sagaID) {
		fmt.Println("    " + r.String())
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// crashMessage runs the saga expecting an injected crash and returns the crash message.
func crashMessage(saga *SagaOrchestrator, order Order) string {
	_, err := saga.Run(order)
	if !errors.Is(err, ErrCrash) {
		panic(fmt.Sprintf("expected a crash, got %v", err))
	}
	return err.Error()
}

func main() {
	clock := NewSimClock(0)
	wallet := NewWalletService(map[string]Money{"alice": usd("100.00")})
	shipping := NewShippingService(map[string]int{"widget": 5})
	log := NewSagaLog(clock)
	steps := []SagaStep{ReserveFunds{wallet}, CaptureFunds{wallet}, ShipOrder{shipping}}
	saga := NewSagaOrchestrator(log, steps, clock, 10)

	fmt.Println("== 1. Happy path: alice buys 2 widgets for 30.00 USD ==")
	fmt.Println("  " + must(saga.Run(Order{"saga-1", "alice", "widget", 2, usd("30.00")})).String())
	show(wallet, shipping)
	printLog(log, "saga-1")

	fmt.Println("\n== 2. Step three fails: 4 widgets wanted, 3 in stock. Compensate in reverse ==")
	fmt.Println("  " + must(saga.Run(Order{"saga-2", "alice", "widget", 4, usd("40.00")})).String())
	show(wallet, shipping)
	printLog(log, "saga-2")

	fmt.Println("\n== 3. Crash after capture executed, before it was logged. Recovery re-runs capture; the key makes it a no-op ==")
	saga.CrashPoint = AfterExecute("capture-funds")
	fmt.Println("  orchestrator: " + crashMessage(saga, Order{"saga-3", "alice", "widget", 1, usd("25.00")}))
	fmt.Printf("  before recovery: balance=%s (already captured), log status=%s\n", wallet.Balance("alice"), StatusOf(log.ForSaga("saga-3")))
	fmt.Println("  recover -> " + formatOutcomes(must(saga.Recover())))
	show(wallet, shipping)
	fmt.Println("  wallet calls for saga-3 capture replayed once; balance debited once")

	fmt.Println("\n== 4. Crash mid-compensation: refund done, not logged. Recovery re-runs refund; no double credit ==")
	saga.CrashPoint = AfterCompensate("capture-funds")
	fmt.Println("  orchestrator: " + crashMessage(saga, Order{"saga-4", "alice", "widget", 9, usd("10.00")}))
	fmt.Printf("  before recovery: balance=%s hold=%t\n", wallet.Balance("alice"), wallet.HasHold("saga-4"))
	fmt.Println("  recover -> " + formatOutcomes(must(saga.Recover())))
	show(wallet, shipping)

	fmt.Println("\n== 5. The semantic lock: a hold is visible to a concurrent saga, and the trade-off it forces ==")
	w2 := NewWalletService(map[string]Money{"alice": usd("100.00")})
	s2 := NewShippingService(map[string]int{"widget": 0})
	saga2 := NewSagaOrchestrator(NewSagaLog(clock), []SagaStep{ReserveFunds{w2}, CaptureFunds{w2}, ShipOrder{s2}}, clock, 10)
	saga2.CrashPoint = AfterExecute("reserve-funds")
	fmt.Println("  saga-A reserved 80.00 and its orchestrator died: " + crashMessage(saga2, Order{"saga-A", "alice", "widget", 1, usd("80.00")}))
	fmt.Printf("  alice balance=%s available=%s  <- the hold is the semantic lock\n", w2.Balance("alice"), w2.Available("alice"))
	fmt.Println("  saga-B wants 50.00 while A is pending: " + must(saga2.Run(Order{"saga-B", "alice", "widget", 1, usd("50.00")})).String())
	fmt.Println("  saga-A resumes, fails at shipping, compensates: " + formatOutcomes(must(saga2.Recover())))
	fmt.Printf("  alice balance=%s available=%s\n", w2.Balance("alice"), w2.Available("alice"))
	fmt.Println("  B was refused on a hold that A later released: that is the price of the lock. Without it B would have read 100.00 and alice would be overdrawn.")
	fmt.Printf("\nclock=%d ms, log records=%d\n", clock.Now(), len(log.Records()))
}
