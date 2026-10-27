// Lesson: phases/12-payment-systems-design/03-payment-orchestration/docs/en.md
// Payment Orchestration Across Processors
// Run:  go run .
// Test: go test ./...
// Sources: Garcia-Molina & Salem, "Sagas" (SIGMOD 1987); IETF draft-ietf-httpapi-idempotency-key-header;
// Stripe API reference (PaymentIntent capture, Refund). Go 1.22+, stdlib only. Same design as the Java version.
package main

import (
	"errors"
	"fmt"
	"strings"
)

// Clock is injected so demos and tests are deterministic.
type Clock interface{ NowMillis() int64 }

type FakeClock struct{ now int64 }

func NewFakeClock(startMillis int64) *FakeClock { return &FakeClock{now: startMillis} }
func (c *FakeClock) NowMillis() int64           { return c.now }
func (c *FakeClock) Advance(millis int64)       { c.now += millis }

// ---------------------------------------------------------------------------------------------
// The saga vocabulary. Four forward steps in a fixed order; each has exactly one compensation.
// ---------------------------------------------------------------------------------------------

type SagaStep int

const (
	TOKENIZE SagaStep = iota
	AUTHORIZE
	CAPTURE
	LEDGER_POST
)

// SagaSteps is the forward order; Go's iota has no values() so the slice is explicit.
var SagaSteps = []SagaStep{TOKENIZE, AUTHORIZE, CAPTURE, LEDGER_POST}

var sagaStepNames = [...]string{"TOKENIZE", "AUTHORIZE", "CAPTURE", "LEDGER_POST"}
var sagaStepSystems = [...]string{"vault", "acquirer", "acquirer", "ledger"}
var sagaStepCompensations = [...]string{"deleteToken", "voidAuth", "refund", "reverse"}

func (s SagaStep) String() string       { return sagaStepNames[s] }
func (s SagaStep) System() string       { return sagaStepSystems[s] }
func (s SagaStep) Compensation() string { return sagaStepCompensations[s] }

type SagaState int

const (
	RUNNING SagaState = iota
	COMPLETED
	COMPENSATING
	COMPENSATED
	DEAD_LETTERED
)

func (s SagaState) String() string {
	return [...]string{"RUNNING", "COMPLETED", "COMPENSATING", "COMPENSATED", "DEAD_LETTERED"}[s]
}

func (s SagaState) IsTerminal() bool { return s == COMPLETED || s == COMPENSATED }

type StepStatus int

const (
	PENDING StepStatus = iota
	DONE
	STEP_COMPENSATED
)

func (s StepStatus) String() string { return [...]string{"PENDING", "DONE", "COMPENSATED"}[s] }

// ErrProcessorUnavailable: a downstream system could not be reached. The call may or may not have happened.
var ErrProcessorUnavailable = errors.New("processor unavailable")

// ErrProcessCrash simulates our own process dying after the downstream committed but before we persisted.
var ErrProcessCrash = errors.New("process crash")

// ErrIllegalState: unknown saga, or an operator action on a saga in the wrong state.
var ErrIllegalState = errors.New("illegal state")

// downstreamError carries the downstream's own message (what the journal records) and matches a
// sentinel through errors.Is, so callers branch on the kind without parsing text.
type downstreamError struct {
	kind error
	msg  string
}

func (e *downstreamError) Error() string        { return e.msg }
func (e *downstreamError) Is(target error) bool { return target == e.kind }

func unavailable(msg string) error { return &downstreamError{ErrProcessorUnavailable, msg} }
func crash(msg string) error       { return &downstreamError{ErrProcessCrash, msg} }

// ErrInvalidAmount: a saga amount that is not positive.
var ErrInvalidAmount = errors.New("invalid amount")

type ProcessorResponse struct {
	Approved  bool
	Reference string
	Code      string
}

func ApprovedResponse(reference string) ProcessorResponse {
	return ProcessorResponse{Approved: true, Reference: reference, Code: "00"}
}
func DeclinedResponse(code string) ProcessorResponse {
	return ProcessorResponse{Approved: false, Code: code}
}

// ---------------------------------------------------------------------------------------------
// The persisted saga record. Every field is written to the store after every step; a crash at any
// point leaves a record that Recover can resume from.
// ---------------------------------------------------------------------------------------------

type SagaRecord struct {
	SagaID               string
	PaymentID            string
	Amount               Money
	PAN                  string
	State                SagaState
	Steps                map[SagaStep]StepStatus
	References           map[SagaStep]string
	ForwardAttempts      map[SagaStep]int
	CompensationAttempts map[SagaStep]int
	FailureReason        string
	Version              int
	Journal              []string
}

func NewSagaRecord(sagaID, paymentID string, amount Money, pan string) (*SagaRecord, error) {
	if !amount.IsPositive() {
		return nil, fmt.Errorf("%w: amount must be positive: %v", ErrInvalidAmount, amount)
	}
	r := &SagaRecord{
		SagaID: sagaID, PaymentID: paymentID, Amount: amount, PAN: pan, State: RUNNING,
		Steps:           map[SagaStep]StepStatus{},
		References:      map[SagaStep]string{},
		ForwardAttempts: map[SagaStep]int{}, CompensationAttempts: map[SagaStep]int{},
	}
	for _, s := range SagaSteps {
		r.Steps[s] = PENDING
		r.ForwardAttempts[s] = 0
		r.CompensationAttempts[s] = 0
	}
	return r, nil
}

// NextForward is the first step not yet DONE, in forward order; false when every step is DONE.
func (r *SagaRecord) NextForward() (SagaStep, bool) {
	for _, s := range SagaSteps {
		if r.Steps[s] == PENDING {
			return s, true
		}
	}
	return 0, false
}

// NextCompensation is the last DONE step, in reverse order; false when nothing remains to undo.
func (r *SagaRecord) NextCompensation() (SagaStep, bool) {
	for i := len(SagaSteps) - 1; i >= 0; i-- {
		if r.Steps[SagaSteps[i]] == DONE {
			return SagaSteps[i], true
		}
	}
	return 0, false
}

func (r *SagaRecord) MarkDone(step SagaStep, reference string) {
	r.Steps[step] = DONE
	r.References[step] = reference
	r.Journal = append(r.Journal, fmt.Sprintf("%v done -> %s", step, reference))
}

func (r *SagaRecord) MarkCompensated(step SagaStep, how string) {
	r.Steps[step] = STEP_COMPENSATED
	r.Journal = append(r.Journal, fmt.Sprintf("%v compensated via %s", step, how))
}

func (r *SagaRecord) BeginCompensation(reason string) {
	r.State = COMPENSATING
	r.FailureReason = reason
	r.Journal = append(r.Journal, "compensating: "+reason)
}

func (r *SagaRecord) Copy() *SagaRecord {
	c, _ := NewSagaRecord(r.SagaID, r.PaymentID, r.Amount, r.PAN)
	c.State = r.State
	for k, v := range r.Steps {
		c.Steps[k] = v
	}
	for k, v := range r.References {
		c.References[k] = v
	}
	for k, v := range r.ForwardAttempts {
		c.ForwardAttempts[k] = v
	}
	for k, v := range r.CompensationAttempts {
		c.CompensationAttempts[k] = v
	}
	c.FailureReason = r.FailureReason
	c.Version = r.Version
	c.Journal = append([]string(nil), r.Journal...)
	return c
}

func (r *SagaRecord) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %v v%d [", r.SagaID, r.State, r.Version)
	for i, s := range SagaSteps {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "%c=%c", s.String()[0], r.Steps[s].String()[0])
	}
	sb.WriteByte(']')
	return sb.String()
}

// SagaStore is durable storage for saga records. It stores copies so an in-flight mutation that never
// reaches Save is lost on crash, as it would be in a database. Insertion order is kept (LinkedHashMap).
type SagaStore struct {
	records map[string]*SagaRecord
	order   []string
}

func NewSagaStore() *SagaStore { return &SagaStore{records: map[string]*SagaRecord{}} }

func (s *SagaStore) Save(r *SagaRecord) {
	r.Version++
	if _, exists := s.records[r.SagaID]; !exists {
		s.order = append(s.order, r.SagaID)
	}
	s.records[r.SagaID] = r.Copy()
}

func (s *SagaStore) Load(sagaID string) (*SagaRecord, error) {
	r, ok := s.records[sagaID]
	if !ok {
		return nil, fmt.Errorf("%w: unknown saga %s", ErrIllegalState, sagaID)
	}
	return r.Copy(), nil
}

func (s *SagaStore) Unfinished() []*SagaRecord {
	var out []*SagaRecord
	for _, id := range s.order {
		r := s.records[id]
		if !r.State.IsTerminal() && r.State != DEAD_LETTERED {
			out = append(out, r.Copy())
		}
	}
	return out
}

// ---------------------------------------------------------------------------------------------
// Downstream systems. Each operation takes an idempotency key: the same key returns the stored
// response without repeating the side effect. Faults are scripted per operation.
// ---------------------------------------------------------------------------------------------

type Fault int

const (
	DECLINE Fault = iota
	OUTAGE
	CRASH_AFTER_COMMIT
)

type Processor interface {
	Name() string
	Tokenize(idempotencyKey, pan string) (ProcessorResponse, error)
	DeleteToken(idempotencyKey, token string) (ProcessorResponse, error)
	Authorize(idempotencyKey, token string, amount Money) (ProcessorResponse, error)
	VoidAuth(idempotencyKey, authRef string) (ProcessorResponse, error)
	Capture(idempotencyKey, authRef string, amount Money) (ProcessorResponse, error)
	Refund(idempotencyKey, captureRef string, amount Money) (ProcessorResponse, error)
}

type FakeProcessor struct {
	name        string
	byKey       map[string]ProcessorResponse
	faults      map[string][]Fault
	sideEffects map[string]int
	replays     int
	sequence    int
}

func NewFakeProcessor(name string) *FakeProcessor {
	return &FakeProcessor{name: name, byKey: map[string]ProcessorResponse{}, faults: map[string][]Fault{}, sideEffects: map[string]int{}}
}

func (p *FakeProcessor) Name() string { return p.name }

func (p *FakeProcessor) FailNext(operation string, fault Fault) *FakeProcessor {
	p.faults[operation] = append(p.faults[operation], fault)
	return p
}

func (p *FakeProcessor) SideEffects(operation string) int { return p.sideEffects[operation] }
func (p *FakeProcessor) Replays() int                     { return p.replays }

func (p *FakeProcessor) call(operation, key, declineCode string) (ProcessorResponse, error) {
	if stored, ok := p.byKey[key]; ok {
		p.replays++
		return stored, nil
	}
	fault, hasFault := Fault(0), false
	if q := p.faults[operation]; len(q) > 0 {
		fault, hasFault = q[0], true
		p.faults[operation] = q[1:]
	}
	if hasFault && fault == OUTAGE {
		return ProcessorResponse{}, unavailable(fmt.Sprintf("%s.%s: connection reset", p.name, operation))
	}
	var response ProcessorResponse
	if hasFault && fault == DECLINE {
		response = DeclinedResponse(declineCode)
	} else {
		p.sideEffects[operation]++
		p.sequence++
		response = ApprovedResponse(fmt.Sprintf("%s_%s_%04d", p.name, operation, p.sequence))
	}
	p.byKey[key] = response
	if hasFault && fault == CRASH_AFTER_COMMIT {
		return ProcessorResponse{}, crash(fmt.Sprintf("process died after %s.%s committed", p.name, operation))
	}
	return response, nil
}

func (p *FakeProcessor) Tokenize(key, pan string) (ProcessorResponse, error) {
	return p.call("tokenize", key, "invalid_card")
}
func (p *FakeProcessor) DeleteToken(key, token string) (ProcessorResponse, error) {
	return p.call("deleteToken", key, "token_not_found")
}
func (p *FakeProcessor) Authorize(key, token string, amount Money) (ProcessorResponse, error) {
	return p.call("authorize", key, "05")
}
func (p *FakeProcessor) VoidAuth(key, authRef string) (ProcessorResponse, error) {
	return p.call("voidAuth", key, "already_captured")
}
func (p *FakeProcessor) Capture(key, authRef string, amount Money) (ProcessorResponse, error) {
	return p.call("capture", key, "auth_expired")
}
func (p *FakeProcessor) Refund(key, captureRef string, amount Money) (ProcessorResponse, error) {
	return p.call("refund", key, "insufficient_merchant_balance")
}

// Entry is one ledger posting. The full ledger is Phase 10, Lesson 03.
type Entry struct {
	EntryID       string
	PaymentID     string
	DebitAccount  string
	CreditAccount string
	Amount        Money
}

// Ledger is a two-account ledger with idempotent postings and reversals.
type Ledger struct {
	byKey    map[string]Entry
	entries  []Entry
	faults   []Fault
	sequence int
}

func NewLedger() *Ledger { return &Ledger{byKey: map[string]Entry{}} }

func (l *Ledger) FailNext(fault Fault) *Ledger {
	l.faults = append(l.faults, fault)
	return l
}

func (l *Ledger) Post(key, paymentID string, amount Money) (string, error) {
	return l.append(key, paymentID, "acquirer_receivable", "merchant_payable", amount)
}

func (l *Ledger) Reverse(key, paymentID string, amount Money) (string, error) {
	return l.append(key, paymentID, "merchant_payable", "acquirer_receivable", amount)
}

func (l *Ledger) append(key, paymentID, debit, credit string, amount Money) (string, error) {
	if stored, ok := l.byKey[key]; ok {
		return stored.EntryID, nil
	}
	fault, hasFault := Fault(0), false
	if len(l.faults) > 0 {
		fault, hasFault = l.faults[0], true
		l.faults = l.faults[1:]
	}
	if hasFault && fault == OUTAGE {
		return "", unavailable("ledger: write timeout")
	}
	l.sequence++
	e := Entry{fmt.Sprintf("le_%04d", l.sequence), paymentID, debit, credit, amount}
	l.byKey[key] = e
	l.entries = append(l.entries, e)
	if hasFault && fault == CRASH_AFTER_COMMIT {
		return "", crash("process died after ledger post committed")
	}
	return e.EntryID, nil
}

// NetMerchantPayable is the net movement into merchant_payable for one payment; zero after a completed compensation.
func (l *Ledger) NetMerchantPayable(paymentID string, currency Currency) Money {
	net := Zero(currency)
	for _, e := range l.entries {
		if e.PaymentID != paymentID {
			continue
		}
		if e.CreditAccount == "merchant_payable" {
			net = mustPlus(net, e.Amount)
		}
		if e.DebitAccount == "merchant_payable" {
			net = mustMinus(net, e.Amount)
		}
	}
	return net
}

func (l *Ledger) Entries() []Entry { return append([]Entry(nil), l.entries...) }

// ---------------------------------------------------------------------------------------------
// Dead letters: sagas the orchestrator could not finish compensating. A human, not a loop, owns them.
// ---------------------------------------------------------------------------------------------

type DeadLetter struct {
	SagaID   string
	Step     SagaStep
	Reason   string
	AtMillis int64
}

// ---------------------------------------------------------------------------------------------
// The orchestrator. Load, do one thing, save. Repeat until terminal or until a crash propagates.
// ---------------------------------------------------------------------------------------------

type Orchestrator struct {
	store       *SagaStore
	vault       Processor
	acquirer    Processor
	ledger      *Ledger
	clock       Clock
	maxAttempts int
	deadLetters []DeadLetter
}

func NewOrchestrator(store *SagaStore, vault, acquirer Processor, ledger *Ledger, clock Clock, maxAttempts int) *Orchestrator {
	return &Orchestrator{store: store, vault: vault, acquirer: acquirer, ledger: ledger, clock: clock, maxAttempts: maxAttempts}
}

func (o *Orchestrator) DeadLetters() []DeadLetter { return append([]DeadLetter(nil), o.deadLetters...) }

// Key: the idempotency key for a step is a pure function of the saga and the step: retries and recoveries reuse it.
func Key(r *SagaRecord, step SagaStep, compensation bool) string {
	suffix := ":fwd"
	if compensation {
		suffix = ":comp"
	}
	return r.SagaID + ":" + strings.ToLower(step.String()) + suffix
}

func (o *Orchestrator) Start(sagaID, paymentID string, amount Money, pan string) (*SagaRecord, error) {
	r, err := NewSagaRecord(sagaID, paymentID, amount, pan)
	if err != nil {
		return nil, err
	}
	o.store.Save(r)
	return o.Run(sagaID)
}

// Run drives a saga until it is terminal or dead-lettered. An ErrProcessCrash propagates: the process is gone.
func (o *Orchestrator) Run(sagaID string) (*SagaRecord, error) {
	r, err := o.store.Load(sagaID)
	if err != nil {
		return nil, err
	}
	for {
		switch r.State {
		case RUNNING:
			step, ok := r.NextForward()
			if !ok {
				r.State = COMPLETED
				r.Journal = append(r.Journal, "completed")
				o.store.Save(r)
				return r, nil
			}
			r.ForwardAttempts[step]++
			response, err := o.forward(step, r)
			if errors.Is(err, ErrProcessorUnavailable) {
				r.Journal = append(r.Journal, fmt.Sprintf("%v attempt %d failed: %v", step, r.ForwardAttempts[step], err))
				if r.ForwardAttempts[step] >= o.maxAttempts {
					r.BeginCompensation(fmt.Sprintf("%v exhausted %d attempts: %v", step, o.maxAttempts, err))
				}
				o.store.Save(r)
				continue
			}
			if err != nil {
				return nil, err // the crash: nothing after this line runs
			}
			if !response.Approved {
				r.BeginCompensation(fmt.Sprintf("%v declined with code %s", step, response.Code))
				o.store.Save(r)
				continue
			}
			r.MarkDone(step, response.Reference)
			o.store.Save(r)
		case COMPENSATING:
			step, ok := r.NextCompensation()
			if !ok {
				r.State = COMPENSATED
				r.Journal = append(r.Journal, "compensated")
				o.store.Save(r)
				return r, nil
			}
			r.CompensationAttempts[step]++
			response, err := o.compensate(step, r)
			if errors.Is(err, ErrProcessorUnavailable) {
				r.Journal = append(r.Journal, fmt.Sprintf("%s attempt %d failed: %v", step.Compensation(), r.CompensationAttempts[step], err))
				if r.CompensationAttempts[step] >= o.maxAttempts {
					o.deadLetter(r, step, err.Error())
				}
				o.store.Save(r)
				if r.State == DEAD_LETTERED {
					return r, nil
				}
				continue
			}
			if err != nil {
				return nil, err
			}
			if !response.Approved {
				o.deadLetter(r, step, fmt.Sprintf("%s refused with code %s", step.Compensation(), response.Code))
				o.store.Save(r)
				return r, nil
			}
			how := step.Compensation()
			if strings.HasPrefix(response.Reference, "noop:") {
				how = response.Reference
			}
			r.MarkCompensated(step, how)
			o.store.Save(r)
		default: // COMPLETED, COMPENSATED, DEAD_LETTERED
			return r, nil
		}
	}
}

func (o *Orchestrator) forward(step SagaStep, r *SagaRecord) (ProcessorResponse, error) {
	k := Key(r, step, false)
	switch step {
	case TOKENIZE:
		return o.vault.Tokenize(k, r.PAN)
	case AUTHORIZE:
		return o.acquirer.Authorize(k, r.References[TOKENIZE], r.Amount)
	case CAPTURE:
		return o.acquirer.Capture(k, r.References[AUTHORIZE], r.Amount)
	default: // LEDGER_POST
		ref, err := o.ledger.Post(k, r.PaymentID, r.Amount)
		if err != nil {
			return ProcessorResponse{}, err
		}
		return ApprovedResponse(ref), nil
	}
}

func (o *Orchestrator) compensate(step SagaStep, r *SagaRecord) (ProcessorResponse, error) {
	k := Key(r, step, true)
	switch step {
	case TOKENIZE:
		return o.vault.DeleteToken(k, r.References[TOKENIZE])
	case AUTHORIZE:
		// A captured authorization is consumed; the refund already returned the money. Voiding it would be
		// refused by the acquirer, so the compensation is semantic: nothing to undo.
		if r.Steps[CAPTURE] == STEP_COMPENSATED {
			return ApprovedResponse("noop:auth-consumed-by-capture"), nil
		}
		return o.acquirer.VoidAuth(k, r.References[AUTHORIZE])
	case CAPTURE:
		return o.acquirer.Refund(k, r.References[CAPTURE], r.Amount)
	default: // LEDGER_POST
		ref, err := o.ledger.Reverse(k, r.PaymentID, r.Amount)
		if err != nil {
			return ProcessorResponse{}, err
		}
		return ApprovedResponse(ref), nil
	}
}

func (o *Orchestrator) deadLetter(r *SagaRecord, step SagaStep, reason string) {
	r.State = DEAD_LETTERED
	r.Journal = append(r.Journal, fmt.Sprintf("dead-lettered at %s: %s", step.Compensation(), reason))
	o.deadLetters = append(o.deadLetters, DeadLetter{r.SagaID, step, reason, o.clock.NowMillis()})
}

// Recover, after a restart, resumes every saga the store says is unfinished. Each resumes from its persisted step.
func (o *Orchestrator) Recover() (int, error) {
	resumed := 0
	for _, r := range o.store.Unfinished() {
		if _, err := o.Run(r.SagaID); err != nil {
			return resumed, err
		}
		resumed++
	}
	return resumed, nil
}

// Redrive is the operator action on a dead letter: the downstream is back, try the remaining compensations again.
func (o *Orchestrator) Redrive(sagaID string) (*SagaRecord, error) {
	r, err := o.store.Load(sagaID)
	if err != nil {
		return nil, err
	}
	if r.State != DEAD_LETTERED {
		return nil, fmt.Errorf("%w: %s is %v, not DEAD_LETTERED", ErrIllegalState, sagaID, r.State)
	}
	r.State = COMPENSATING
	for _, s := range SagaSteps {
		r.CompensationAttempts[s] = 0
	}
	r.Journal = append(r.Journal, "redriven by operator")
	o.store.Save(r)
	kept := o.deadLetters[:0]
	for _, d := range o.deadLetters {
		if d.SagaID != sagaID {
			kept = append(kept, d)
		}
	}
	o.deadLetters = kept
	return o.Run(sagaID)
}

// ---------------------------------------------------------------------------------------------
// Demo
// ---------------------------------------------------------------------------------------------

func must(r *SagaRecord, err error) *SagaRecord {
	if err != nil {
		panic(err)
	}
	return r
}

func main() {
	clock := NewFakeClock(1_700_000_000_000)
	store := NewSagaStore()
	vault := NewFakeProcessor("vault")
	acquirer := NewFakeProcessor("acquirer")
	ledger := NewLedger()
	orch := NewOrchestrator(store, vault, acquirer, ledger, clock, 3)

	fmt.Println("== The saga: four steps, four compensations ==")
	for _, s := range SagaSteps {
		fmt.Printf("%-12v on %-9s undone by %s\n", s, s.System(), s.Compensation())
	}

	fmt.Println()
	fmt.Println("== Happy path: tokenize -> authorize -> capture -> ledger post ==")
	r := must(orch.Start("saga_1", "pay_1001", OfMinor(4999, USD), "4242424242424242"))
	fmt.Println(r)
	for _, line := range r.Journal {
		fmt.Println("  " + line)
	}
	fmt.Println("merchant_payable for pay_1001:", ledger.NetMerchantPayable("pay_1001", USD))

	fmt.Println()
	fmt.Println("== Decline at CAPTURE: compensations run in reverse order (void, delete token) ==")
	acquirer.FailNext("capture", DECLINE)
	r = must(orch.Start("saga_2", "pay_1002", OfMinor(1250, USD), "4000000000000002"))
	fmt.Printf("%v  reason: %s\n", r, r.FailureReason)
	for _, line := range r.Journal {
		fmt.Println("  " + line)
	}
	fmt.Println("merchant_payable for pay_1002:", ledger.NetMerchantPayable("pay_1002", USD))

	fmt.Println()
	fmt.Println("== Outage at AUTHORIZE: retried up to the budget, then compensated ==")
	acquirer.FailNext("authorize", OUTAGE).FailNext("authorize", OUTAGE)
	r = must(orch.Start("saga_3", "pay_1003", OfMinor(800, USD), "4242424242424242"))
	fmt.Printf("%v  authorize attempts: %d\n", r, r.ForwardAttempts[AUTHORIZE])
	acquirer.FailNext("authorize", OUTAGE).FailNext("authorize", OUTAGE).FailNext("authorize", OUTAGE)
	r = must(orch.Start("saga_4", "pay_1004", OfMinor(800, USD), "4242424242424242"))
	fmt.Printf("%v  reason: %s\n", r, r.FailureReason)

	fmt.Println()
	fmt.Println("== Crash after the acquirer committed the authorization but before we saved ==")
	authsBefore := acquirer.SideEffects("authorize")
	acquirer.FailNext("authorize", CRASH_AFTER_COMMIT)
	if _, err := orch.Start("saga_5", "pay_1005", OfMinor(2000, USD), "4242424242424242"); errors.Is(err, ErrProcessCrash) {
		fmt.Println("crash:", err)
	}
	persisted, _ := store.Load("saga_5")
	fmt.Printf("persisted after crash: %v  (AUTHORIZE still PENDING; acquirer holds 1 auth)\n", persisted)
	restarted := NewOrchestrator(store, vault, acquirer, ledger, clock, 3)
	resumed, _ := restarted.Recover()
	fmt.Printf("recover() resumed %d saga(s)\n", resumed)
	r, _ = store.Load("saga_5")
	fmt.Printf("after recovery: %v  acquirer authorizations: %d, idempotent replays: %d\n", r,
		acquirer.SideEffects("authorize")-authsBefore, acquirer.Replays())

	fmt.Println()
	fmt.Println("== Ledger post fails: the capture must be refunded, not voided ==")
	ledger.FailNext(OUTAGE).FailNext(OUTAGE).FailNext(OUTAGE)
	r = must(orch.Start("saga_6", "pay_1006", OfMinor(3000, USD), "4242424242424242"))
	fmt.Printf("%v  reason: %s\n", r, r.FailureReason)
	for _, line := range r.Journal {
		if strings.Contains(line, "compensated via") {
			fmt.Println("  " + line)
		}
	}
	fmt.Printf("acquirer refunds so far: %d, merchant_payable for pay_1006: %v\n", acquirer.SideEffects("refund"), ledger.NetMerchantPayable("pay_1006", USD))

	fmt.Println()
	fmt.Println("== Compensation itself fails: dead letter, then operator redrive ==")
	acquirer.FailNext("capture", DECLINE)
	for i := 0; i < 3; i++ {
		acquirer.FailNext("voidAuth", OUTAGE)
	}
	r = must(orch.Start("saga_7", "pay_1007", OfMinor(5500, USD), "4242424242424242"))
	fmt.Println(r)
	for _, d := range orch.DeadLetters() {
		fmt.Printf("  dead letter: %s at %s (%s)\n", d.SagaID, d.Step.Compensation(), d.Reason)
	}
	clock.Advance(15 * 60_000)
	r = must(orch.Redrive("saga_7"))
	fmt.Printf("after redrive: %v  dead letters remaining: %d\n", r, len(orch.DeadLetters()))

	fmt.Println()
	fmt.Println("== Running a finished saga again is a no-op ==")
	before := acquirer.SideEffects("capture")
	must(orch.Run("saga_1"))
	s1, _ := store.Load("saga_1")
	fmt.Printf("saga_1 still %v, captures unchanged: %t\n", s1.State, acquirer.SideEffects("capture") == before)
}
