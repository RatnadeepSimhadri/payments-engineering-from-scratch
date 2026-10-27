// Lesson: phases/12-payment-systems-design/14-testing-payment-systems/docs/en.md
// Testing Payment Systems: Simulators, Property Tests, and Invariants
// Run:  go run .
// Test: go test ./...
// Sources: Claessen & Hughes, "QuickCheck: A Lightweight Tool for Random Testing of Haskell Programs" (ICFP 2000);
// Hughes, "Experiences with QuickCheck: Testing the Hard Stuff and Staying Sane" (2016). Go 1.22+, stdlib only.
package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

var (
	// ErrInvalidArgument is Java's IllegalArgumentException: the service rejecting an input on purpose.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrIllegalState is Java's IllegalStateException: an operation the payment's state does not allow.
	ErrIllegalState = errors.New("illegal state")
	// ErrProcessorTimeout is ambiguous: the operation may or may not have been applied.
	ErrProcessorTimeout = errors.New("processor timeout")
	// ErrProcessorError is a 5xx: not applied. Java threw IllegalStateException, so it wraps ErrIllegalState.
	ErrProcessorError = fmt.Errorf("%w: processor error", ErrIllegalState)
)

// BasisPoints is a percentage in basis points (1 bp = 0.01%), rounded HALF_EVEN, via the Phase 10 Money.
func (m Money) BasisPoints(bps int64) (Money, error) { return m.Times(bps, 10_000, HalfEven) }

// ---------------------------------------------------------------------------------------------
// Step 1: the system under test. A payment with a ledger, talking to a processor that can fail.
// ---------------------------------------------------------------------------------------------

type Account int

const (
	CUSTOMER Account = iota
	MERCHANT
	PLATFORM_FEES
)

var accounts = [...]Account{CUSTOMER, MERCHANT, PLATFORM_FEES}

func (a Account) String() string { return [...]string{"CUSTOMER", "MERCHANT", "PLATFORM_FEES"}[a] }

// Ledger: three accounts, double-entry: every posting debits one and credits another by the same amount.
type Ledger struct {
	balances map[Account]Money
	Postings []string
}

func NewLedger(currency Currency) *Ledger {
	l := &Ledger{balances: map[Account]Money{}}
	for _, a := range accounts {
		l.balances[a] = Zero(currency)
	}
	return l
}

func (l *Ledger) Post(from, to Account, amount Money, memo string) error {
	if amount.IsNegative() {
		return fmt.Errorf("%w: posting amount must not be negative: %v", ErrInvalidArgument, amount)
	}
	l.balances[from] = mustMinus(l.balances[from], amount)
	l.balances[to] = mustPlus(l.balances[to], amount)
	l.Postings = append(l.Postings, fmt.Sprintf("%s: %v -> %v %v", memo, from, to, amount))
	return nil
}

// CreditOnly is a deliberately unbalanced write, reachable only through Bugs.UnbalancedFee.
func (l *Ledger) CreditOnly(to Account, amount Money, memo string) {
	l.balances[to] = mustPlus(l.balances[to], amount)
	l.Postings = append(l.Postings, fmt.Sprintf("%s: (nothing) -> %v %v", memo, to, amount))
}

func (l *Ledger) Balance(a Account) Money { return l.balances[a] }

func (l *Ledger) Total() Money {
	t := Zero(l.balances[CUSTOMER].Currency)
	for _, a := range accounts {
		t = mustPlus(t, l.balances[a])
	}
	return t
}

// ProcessorBehaviour is how the processor behaves on one call. A timeout is ambiguous: the operation may or may not have been applied.
type ProcessorBehaviour int

const (
	APPROVE              ProcessorBehaviour = iota // applied, success returned
	DECLINE                                        // not applied, decline returned
	TIMEOUT_AFTER_APPLY                            // applied, but the response was lost on the way back
	TIMEOUT_BEFORE_APPLY                           // the request never arrived; nothing applied
	ERROR                                          // not applied, 5xx returned
)

func (b ProcessorBehaviour) String() string {
	return [...]string{"APPROVE", "DECLINE", "TIMEOUT_AFTER_APPLY", "TIMEOUT_BEFORE_APPLY", "ERROR"}[b]
}

// ProcessorPayment is the processor's own record of a payment. This is the external truth the ledger must reconcile to.
type ProcessorPayment struct {
	Authorized, Captured, Refunded Money
	Voided                         bool
}

// FakeProcessor's behaviour per call is scripted. Exhausting the script means APPROVE.
type FakeProcessor struct {
	script []ProcessorBehaviour
	cursor int
	Record *ProcessorPayment
	Calls  int
}

func NewFakeProcessor(currency Currency) *FakeProcessor {
	z := Zero(currency)
	return &FakeProcessor{Record: &ProcessorPayment{z, z, z, false}}
}

func (p *FakeProcessor) Script(behaviours ...ProcessorBehaviour) *FakeProcessor {
	p.script = append(p.script, behaviours...)
	return p
}

func (p *FakeProcessor) next() ProcessorBehaviour {
	p.Calls++
	if p.cursor < len(p.script) {
		b := p.script[p.cursor]
		p.cursor++
		return b
	}
	return APPROVE
}

// Call applies the operation if the processor would, then returns or fails according to the behaviour.
func (p *FakeProcessor) Call(op string, apply func()) (bool, error) {
	switch p.next() {
	case APPROVE:
		apply()
		return true, nil
	case DECLINE:
		return false, nil
	case TIMEOUT_AFTER_APPLY:
		apply()
		return false, fmt.Errorf("%w: timed out waiting for processor response to %s", ErrProcessorTimeout, op)
	case TIMEOUT_BEFORE_APPLY:
		return false, fmt.Errorf("%w: timed out waiting for processor response to %s", ErrProcessorTimeout, op)
	default: // ERROR
		return false, fmt.Errorf("%w: processor 5xx on %s", ErrProcessorError, op)
	}
}

func (p *FakeProcessor) Authorize(amount Money) (bool, error) {
	return p.Call("authorize", func() { p.Record.Authorized = amount })
}
func (p *FakeProcessor) Capture(amount Money) (bool, error) {
	return p.Call("capture", func() { p.Record.Captured = mustPlus(p.Record.Captured, amount) })
}
func (p *FakeProcessor) Refund(amount Money) (bool, error) {
	return p.Call("refund", func() { p.Record.Refunded = mustPlus(p.Record.Refunded, amount) })
}
func (p *FakeProcessor) VoidAuth() (bool, error) {
	return p.Call("void", func() { p.Record.Voided = true })
}

// Bugs are switches that re-introduce real bugs so the property tests have something to find. All false in production.
type Bugs struct {
	AllowOverCapture bool
	AllowOverRefund  bool
	UnbalancedFee    bool
	PostOnTimeout    bool
}

var NoBugs = Bugs{}

func (b Bugs) String() string {
	return fmt.Sprintf("Bugs[allowOverCapture=%t, allowOverRefund=%t, unbalancedFee=%t, postOnTimeout=%t]",
		b.AllowOverCapture, b.AllowOverRefund, b.UnbalancedFee, b.PostOnTimeout)
}

type PaymentState int

const (
	NEW PaymentState = iota
	AUTHORIZED
	PARTIALLY_CAPTURED
	CAPTURED
	VOIDED
	UNKNOWN
)

func (s PaymentState) String() string {
	return [...]string{"NEW", "AUTHORIZED", "PARTIALLY_CAPTURED", "CAPTURED", "VOIDED", "UNKNOWN"}[s]
}

const FEE_BPS int64 = 290 // 2.9%

type PaymentService struct {
	Ledger        *Ledger
	Processor     *FakeProcessor
	Bugs          Bugs
	Currency      Currency
	State         PaymentState
	Authorized    Money
	Captured      Money
	Refunded      Money
	LastUnknownOp string // "" where Java held null
}

func NewPaymentService(currency Currency, processor *FakeProcessor, bugs Bugs) *PaymentService {
	z := Zero(currency)
	return &PaymentService{Ledger: NewLedger(currency), Processor: processor, Bugs: bugs, Currency: currency,
		State: NEW, Authorized: z, Captured: z, Refunded: z}
}

func requirePositive(m Money) error {
	if m.MinorUnits <= 0 {
		return fmt.Errorf("%w: amount must be positive: %v", ErrInvalidArgument, m)
	}
	return nil
}

// markUnknown records an ambiguous outcome; any non-timeout error is passed through to the caller.
func (s *PaymentService) markUnknown(op string, err error) (bool, error) {
	if !errors.Is(err, ErrProcessorTimeout) {
		return false, err
	}
	s.State = UNKNOWN
	s.LastUnknownOp = op
	return false, nil
}

func (s *PaymentService) Authorize(amount Money) (bool, error) {
	if err := requirePositive(amount); err != nil {
		return false, err
	}
	if s.State != NEW {
		return false, fmt.Errorf("%w: authorize in state %v", ErrIllegalState, s.State)
	}
	ok, err := s.Processor.Authorize(amount)
	if err != nil {
		return s.markUnknown(fmt.Sprintf("authorize:%d", amount.MinorUnits), err)
	}
	if !ok {
		return false, nil
	}
	s.Authorized = amount
	s.State = AUTHORIZED
	return true, nil
}

func (s *PaymentService) Capture(amount Money) (bool, error) {
	if err := requirePositive(amount); err != nil {
		return false, err
	}
	if s.State != AUTHORIZED && s.State != PARTIALLY_CAPTURED {
		return false, fmt.Errorf("%w: capture in state %v", ErrIllegalState, s.State)
	}
	if !s.Bugs.AllowOverCapture && mustPlus(s.Captured, amount).Compare(s.Authorized) > 0 {
		return false, fmt.Errorf("%w: capture %v exceeds remaining authorization %v", ErrInvalidArgument, amount, mustMinus(s.Authorized, s.Captured))
	}
	ok, err := s.Processor.Capture(amount)
	if err != nil {
		if errors.Is(err, ErrProcessorTimeout) && s.Bugs.PostOnTimeout {
			s.postCapture(amount) // the bug: assuming a timeout meant success
		}
		return s.markUnknown(fmt.Sprintf("capture:%d", amount.MinorUnits), err)
	}
	if !ok {
		return false, nil
	}
	s.postCapture(amount)
	return true, nil
}

func (s *PaymentService) postCapture(amount Money) {
	fee := must(amount.BasisPoints(FEE_BPS))
	mustOK(s.Ledger.Post(CUSTOMER, MERCHANT, amount, "capture"))
	if s.Bugs.UnbalancedFee {
		s.Ledger.CreditOnly(PLATFORM_FEES, fee, "fee")
	} else {
		mustOK(s.Ledger.Post(MERCHANT, PLATFORM_FEES, fee, "fee"))
	}
	s.Captured = mustPlus(s.Captured, amount)
	if s.Captured.Compare(s.Authorized) >= 0 {
		s.State = CAPTURED
	} else {
		s.State = PARTIALLY_CAPTURED
	}
}

func (s *PaymentService) Refund(amount Money) (bool, error) {
	if err := requirePositive(amount); err != nil {
		return false, err
	}
	if s.State != PARTIALLY_CAPTURED && s.State != CAPTURED {
		return false, fmt.Errorf("%w: refund in state %v", ErrIllegalState, s.State)
	}
	if !s.Bugs.AllowOverRefund && mustPlus(s.Refunded, amount).Compare(s.Captured) > 0 {
		return false, fmt.Errorf("%w: refund %v exceeds refundable %v", ErrInvalidArgument, amount, mustMinus(s.Captured, s.Refunded))
	}
	ok, err := s.Processor.Refund(amount)
	if err != nil {
		return s.markUnknown(fmt.Sprintf("refund:%d", amount.MinorUnits), err)
	}
	if !ok {
		return false, nil
	}
	s.postRefund(amount)
	return true, nil
}

func (s *PaymentService) postRefund(amount Money) {
	mustOK(s.Ledger.Post(MERCHANT, CUSTOMER, amount, "refund"))
	s.Refunded = mustPlus(s.Refunded, amount)
}

func (s *PaymentService) VoidAuthorization() (bool, error) {
	if s.State != AUTHORIZED {
		return false, fmt.Errorf("%w: void in state %v", ErrIllegalState, s.State)
	}
	ok, err := s.Processor.VoidAuth()
	if err != nil {
		return s.markUnknown("void", err)
	}
	if !ok {
		return false, nil
	}
	s.State = VOIDED
	return true, nil
}

// Reconcile: after a timeout, ask the processor what actually happened and bring the ledger into line.
func (s *PaymentService) Reconcile() {
	if s.State != UNKNOWN {
		return
	}
	p := s.Processor.Record
	if p.Authorized != s.Authorized {
		s.Authorized = p.Authorized
	}
	if missingCapture := mustMinus(p.Captured, s.Captured); missingCapture.MinorUnits > 0 {
		s.postCapture(missingCapture)
	}
	if missingRefund := mustMinus(p.Refunded, s.Refunded); missingRefund.MinorUnits > 0 {
		s.postRefund(missingRefund)
	}
	switch {
	case p.Voided:
		s.State = VOIDED
	case s.Captured.IsZero():
		if s.Authorized.IsZero() {
			s.State = NEW
		} else {
			s.State = AUTHORIZED
		}
	case s.Captured.Compare(s.Authorized) >= 0:
		s.State = CAPTURED
	default:
		s.State = PARTIALLY_CAPTURED
	}
	s.LastUnknownOp = ""
}

// ---------------------------------------------------------------------------------------------
// Step 2: the invariants. CheckInvariants returns "" when they hold, or a sentence naming the violation.
// ---------------------------------------------------------------------------------------------

func CheckInvariants(s *PaymentService) string {
	if !s.Ledger.Total().IsZero() {
		return fmt.Sprintf("money not conserved: ledger total %v", s.Ledger.Total())
	}
	if s.Captured.Compare(s.Authorized) > 0 {
		return fmt.Sprintf("captured %v > authorized %v", s.Captured, s.Authorized)
	}
	if s.Refunded.Compare(s.Captured) > 0 {
		return fmt.Sprintf("refunded %v > captured %v", s.Refunded, s.Captured)
	}
	customerExpected := mustMinus(s.Captured, s.Refunded).Negate()
	if s.Ledger.Balance(CUSTOMER) != customerExpected {
		return fmt.Sprintf("customer balance %v != -(captured - refunded) %v", s.Ledger.Balance(CUSTOMER), customerExpected)
	}
	if s.Ledger.Balance(PLATFORM_FEES).IsNegative() {
		return "negative fee balance"
	}
	if s.State != UNKNOWN {
		p := s.Processor.Record
		if p.Captured != s.Captured {
			return fmt.Sprintf("ledger captured %v != processor captured %v", s.Captured, p.Captured)
		}
		if p.Refunded != s.Refunded {
			return fmt.Sprintf("ledger refunded %v != processor refunded %v", s.Refunded, p.Refunded)
		}
	}
	return ""
}

// ---------------------------------------------------------------------------------------------
// Step 3: a property-based testing harness. Generators produce values and know how to shrink them.
// ---------------------------------------------------------------------------------------------

type Gen[T any] interface {
	Generate(rng *JavaRandom) T
	// Shrink lists smaller candidates, simplest first. Empty when the value cannot be made smaller.
	Shrink(value T) []T
}

// Longs is uniform in [lo, hi]; shrinks toward lo by halving the distance, then by single steps.
func Longs(lo, hi int64) Gen[int64] { return longGen{lo, hi} }

type longGen struct{ lo, hi int64 }

func (g longGen) Generate(rng *JavaRandom) int64 {
	return g.lo + int64(rng.NextDouble()*float64(g.hi-g.lo+1))
}

func (g longGen) Shrink(v int64) []int64 {
	out := []int64{}
	if v <= g.lo {
		return out
	}
	out = append(out, g.lo)
	d := v - g.lo
	for step := d / 2; step > 0; step /= 2 {
		out = append(out, v-step)
	}
	if !slices.Contains(out, v-1) {
		out = append(out, v-1)
	}
	return out
}

// OneOf picks uniformly from values; earlier entries are "simpler", so a value shrinks to those before it.
func OneOf[T comparable](values []T) Gen[T] { return oneOfGen[T]{values} }

type oneOfGen[T comparable] struct{ values []T }

func (g oneOfGen[T]) Generate(rng *JavaRandom) T { return g.values[rng.NextInt(int32(len(g.values)))] }

func (g oneOfGen[T]) Shrink(v T) []T {
	out := []T{}
	for _, c := range g.values {
		if c == v {
			break
		}
		out = append(out, c)
	}
	return out
}

// Lists are 1..maxLen elements; shrinks by dropping chunks, then single elements, then shrinking elements.
func Lists[T any](element Gen[T], maxLen int) Gen[[]T] { return listGen[T]{element, maxLen} }

type listGen[T any] struct {
	element Gen[T]
	maxLen  int
}

func (g listGen[T]) Generate(rng *JavaRandom) []T {
	n := 1 + int(rng.NextInt(int32(g.maxLen)))
	out := make([]T, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, g.element.Generate(rng))
	}
	return out
}

func (g listGen[T]) Shrink(v []T) [][]T {
	out := [][]T{}
	n := len(v)
	for chunk := n / 2; chunk >= 1; chunk /= 2 {
		for start := 0; start+chunk <= n; start += chunk {
			c := append(append([]T{}, v[:start]...), v[start+chunk:]...)
			if len(c) > 0 {
				out = append(out, c)
			}
		}
	}
	for i := 0; i < n; i++ {
		for _, smaller := range g.element.Shrink(v[i]) {
			c := append([]T{}, v...)
			c[i] = smaller
			out = append(out, c)
		}
	}
	return out
}

type Counterexample[T any] struct {
	Original    T
	Shrunk      T
	ShrinkSteps int
	Reason      string
}

// PropertyResult: Counterexample is nil when the property passed (Java's null).
type PropertyResult[T any] struct {
	Name           string
	Passed         bool
	Seed           int64
	TrialsRun      int
	Counterexample *Counterexample[T]
}

// formatValue prints values the way Java's toString did: lists as [a, b, c].
func formatValue(v any) string {
	switch x := v.(type) {
	case []Command:
		parts := make([]string, len(x))
		for i, c := range x {
			parts[i] = c.String()
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case []int64:
		parts := make([]string, len(x))
		for i, n := range x {
			parts[i] = fmt.Sprint(n)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		return fmt.Sprint(v)
	}
}

func (r PropertyResult[T]) Summary() string {
	if r.Passed {
		return fmt.Sprintf("%s: OK, %d trials (seed %d)", r.Name, r.TrialsRun, r.Seed)
	}
	c := r.Counterexample
	return fmt.Sprintf("%s: FAILED at trial %d (seed %d)\n  original: %s\n  shrunk (%d steps): %s\n  reason: %s",
		r.Name, r.TrialsRun, r.Seed, formatValue(c.Original), c.ShrinkSteps, formatValue(c.Shrunk), c.Reason)
}

const MAX_SHRINK_STEPS = 2_000

// Check returns nil when the property holds. An error wrapping ErrInvalidArgument or ErrIllegalState
// means the system rejected the input on purpose (not a failure); any other error is the violation.
type Check[T any] func(value T) error

// ForAll runs check on trials generated values, shrinking the first counterexample it finds.
func ForAll[T any](name string, gen Gen[T], check Check[T], seed int64, trials int) PropertyResult[T] {
	rng := NewJavaRandom(seed)
	for i := 1; i <= trials; i++ {
		value := gen.Generate(rng)
		reason, failed := safeCheck(check, value)
		if !failed {
			continue
		}
		current, currentReason := value, reason
		steps := 0
		progressed := true
		for progressed && steps < MAX_SHRINK_STEPS {
			progressed = false
			for _, candidate := range gen.Shrink(current) {
				steps++
				if r, f := safeCheck(check, candidate); f {
					current, currentReason, progressed = candidate, r, true
					break
				}
				if steps >= MAX_SHRINK_STEPS {
					break
				}
			}
		}
		return PropertyResult[T]{name, false, seed, i, &Counterexample[T]{value, current, steps, currentReason}}
	}
	return PropertyResult[T]{name, true, seed, trials, nil}
}

// safeCheck classifies the outcome: (reason, true) on a violation or a crash, ("", false) when the
// property holds or the input was rejected on purpose.
func safeCheck[T any](check Check[T], value T) (reason string, failed bool) {
	defer func() {
		if crash := recover(); crash != nil {
			reason, failed = fmt.Sprintf("crash: %v", crash), true
		}
	}()
	err := check(value)
	switch {
	case err == nil:
		return "", false
	case errors.Is(err, ErrInvalidArgument), errors.Is(err, ErrIllegalState):
		return "", false // the system rejected the input on purpose: the property is about accepted inputs
	default:
		return err.Error(), true
	}
}

// ---------------------------------------------------------------------------------------------
// Step 4: scenarios as data. A command list plus a processor script replays identically every time.
// ---------------------------------------------------------------------------------------------

type CommandKind int

const (
	AUTHORIZE CommandKind = iota
	CAPTURE
	REFUND
	VOID
	RECONCILE
)

func (k CommandKind) String() string {
	return [...]string{"AUTHORIZE", "CAPTURE", "REFUND", "VOID", "RECONCILE"}[k]
}

type Command struct {
	Kind        CommandKind
	AmountMinor int64
	Behaviour   ProcessorBehaviour
}

func (c Command) String() string {
	suffix := ""
	if c.Behaviour != APPROVE {
		suffix = "/" + c.Behaviour.String()
	}
	if c.Kind == VOID || c.Kind == RECONCILE {
		return c.Kind.String() + suffix
	}
	return fmt.Sprintf("%v %d%s", c.Kind, c.AmountMinor, suffix)
}

type Scenario struct {
	Name     string
	Commands []Command
}

// ScenarioRun: Violation is "" when every invariant held after every command.
type ScenarioRun struct {
	Scenario  Scenario
	Service   *PaymentService
	Log       []string
	Violation string
}

func (r ScenarioRun) Passed() bool { return r.Violation == "" }

var (
	amounts    = Longs(1, 10_000)
	kinds      = OneOf([]CommandKind{AUTHORIZE, CAPTURE, REFUND, RECONCILE, VOID})
	behaviours = OneOf([]ProcessorBehaviour{APPROVE, DECLINE, TIMEOUT_AFTER_APPLY, TIMEOUT_BEFORE_APPLY, ERROR})
	// COMMANDS generates one command, biased toward the happy path so sequences get deep enough to reach refunds.
	COMMANDS Gen[Command] = commandGen{}
)

type commandGen struct{}

func (commandGen) Generate(rng *JavaRandom) Command {
	k := kinds.Generate(rng)
	b := APPROVE
	if rng.NextInt(10) >= 7 {
		b = behaviours.Generate(rng)
	}
	return Command{k, amounts.Generate(rng), b}
}

func (commandGen) Shrink(c Command) []Command {
	out := []Command{}
	for _, b := range behaviours.Shrink(c.Behaviour) {
		out = append(out, Command{c.Kind, c.AmountMinor, b})
	}
	for _, a := range amounts.Shrink(c.AmountMinor) {
		out = append(out, Command{c.Kind, a, c.Behaviour})
	}
	return out
}

func Sequences(maxLen int) Gen[[]Command] { return Lists(COMMANDS, maxLen) }

// RunScenario applies the commands in order. Commands the service rejects as illegal are logged and skipped.
func RunScenario(scenario Scenario, bugs Bugs) ScenarioRun {
	processor := NewFakeProcessor(USD)
	service := NewPaymentService(USD, processor, bugs)
	log := []string{}
	for _, c := range scenario.Commands {
		processor.Script(c.Behaviour)
		amount := OfMinor(c.AmountMinor, USD)
		var ok bool
		var err error
		switch c.Kind {
		case AUTHORIZE:
			ok, err = service.Authorize(amount)
		case CAPTURE:
			ok, err = service.Capture(amount)
		case REFUND:
			ok, err = service.Refund(amount)
		case VOID:
			ok, err = service.VoidAuthorization()
		default:
			service.Reconcile()
			ok = true
		}
		outcome := "declined/unknown"
		switch {
		case err != nil && (errors.Is(err, ErrInvalidArgument) || errors.Is(err, ErrIllegalState)):
			outcome = "rejected: " + err.Error()
		case err != nil:
			panic(err) // nothing else can come back from the service; Java would have let it propagate too
		case ok:
			outcome = "ok"
		}
		log = append(log, fmt.Sprintf("%v -> %s [%v]", c, outcome, service.State))
		if violation := CheckInvariants(service); violation != "" {
			return ScenarioRun{scenario, service, log, fmt.Sprintf("%s after %v", violation, c)}
		}
	}
	return ScenarioRun{scenario, service, log, ""}
}

// InvariantsHold is the property: no command sequence, with any processor behaviour, violates the invariants.
func InvariantsHold(bugs Bugs, seed int64, trials, maxLen int) PropertyResult[[]Command] {
	return ForAll("invariants hold under "+bugs.String(), Sequences(maxLen), func(cmds []Command) error {
		if v := RunScenario(Scenario{"generated", cmds}, bugs).Violation; v != "" {
			return errors.New(v)
		}
		return nil
	}, seed, trials)
}

// ---------------------------------------------------------------------------------------------
// Demo.
// ---------------------------------------------------------------------------------------------

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func mustOK(err error) {
	if err != nil {
		panic(err)
	}
}

// mustMinus is for same-currency values the caller already validated; a mismatch is a bug.
func mustMinus(a, b Money) Money { return must(a.Minus(b)) }

func main() {
	fmt.Println("== One scripted scenario: capture times out, reconcile repairs the ledger ==")
	timeout := Scenario{"timeout then reconcile", []Command{
		{AUTHORIZE, 5000, APPROVE},
		{CAPTURE, 5000, TIMEOUT_AFTER_APPLY},
		{RECONCILE, 0, APPROVE},
		{REFUND, 1250, APPROVE},
	}}
	r := RunScenario(timeout, NoBugs)
	for _, l := range r.Log {
		fmt.Println("  " + l)
	}
	fmt.Printf("  ledger: customer %v, merchant %v, fees %v, total %v\n", r.Service.Ledger.Balance(CUSTOMER),
		r.Service.Ledger.Balance(MERCHANT), r.Service.Ledger.Balance(PLATFORM_FEES), r.Service.Ledger.Total())
	verdict := r.Violation
	if r.Passed() {
		verdict = "hold"
	}
	fmt.Println("  invariants: " + verdict)

	fmt.Println("\n== Property: invariants hold for 500 random sequences (seed 42) ==")
	fmt.Println(InvariantsHold(NoBugs, 42, 500, 12).Summary())

	fmt.Println("\n== Same property with real bugs switched back on (500 trials) ==")
	for _, bug := range []Bugs{{AllowOverCapture: true}, {AllowOverRefund: true}, {UnbalancedFee: true}, {PostOnTimeout: true}} {
		fmt.Println(InvariantsHold(bug, 42, 500, 12).Summary())
	}
	fmt.Println("\n== The timeout bug needs AUTHORIZE, CAPTURE/TIMEOUT_BEFORE_APPLY, RECONCILE in that order: 2000 trials ==")
	fmt.Println(InvariantsHold(Bugs{PostOnTimeout: true}, 42, 2000, 12).Summary())

	fmt.Println("\n== Shrinking a plain integer property: 'truncating the 2.9% fee equals HALF_EVEN' ==")
	ints := ForAll("truncated fee == rounded fee", Longs(1, 1_000_000), func(n int64) error {
		truncated := n * FEE_BPS / 10_000
		rounded := must(OfMinor(n, USD).BasisPoints(FEE_BPS)).MinorUnits
		if truncated == rounded {
			return nil
		}
		return fmt.Errorf("n=%d: truncated %d != rounded %d", n, truncated, rounded)
	}, 7, 10_000)
	fmt.Println(ints.Summary())
	fmt.Printf("\nFee on 100.00 USD at 2.9%% = %v\n", must(OfMinor(10_000, USD).BasisPoints(FEE_BPS)))
}
