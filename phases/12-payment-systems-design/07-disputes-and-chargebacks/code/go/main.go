// Lesson: phases/12-payment-systems-design/07-disputes-and-chargebacks/docs/en.md
// Disputes and Chargebacks: The Lifecycle and the Evidence
// Run:  go run .
// Test: go test ./...
// Sources: Visa Core Rules (Dispute Resolution: Allocation and Collaboration flows, reason codes 10-13);
// Mastercard Chargeback Guide (reason codes 4834, 4837, 4853; second presentment; pre-arbitration). Go 1.22+, stdlib only.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var (
	ErrInvalidArgument = errors.New("invalid argument")
	ErrIllegalState    = errors.New("illegal state")
)

// ---------------------------------------------------------------------------------------------
// Time is injected as a calendar date (Java's LocalDate): midnight UTC time.Time values.
// ---------------------------------------------------------------------------------------------

type Clock interface{ Today() time.Time }

func Date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

type FakeClock struct{ today time.Time }

func NewFakeClock(start time.Time) *FakeClock { return &FakeClock{today: start} }
func (c *FakeClock) Today() time.Time         { return c.today }
func (c *FakeClock) AdvanceDays(days int)     { c.today = c.today.AddDate(0, 0, days) }

func fmtDate(t time.Time) string { return t.Format("2006-01-02") }

// DaysBetween is ChronoUnit.DAYS.between(a, b).
func DaysBetween(a, b time.Time) int64 { return int64(b.Sub(a).Hours() / 24) }

// ---------------------------------------------------------------------------------------------
// Minimal double-entry ledger (Phase 10, Lesson 03).
// ---------------------------------------------------------------------------------------------

type Side int

const (
	DEBIT Side = iota
	CREDIT
)

type AccountType int

const (
	ASSET AccountType = iota
	LIABILITY
	REVENUE
)

func (a AccountType) Normal() Side {
	if a == ASSET {
		return DEBIT
	}
	return CREDIT
}

type Posting struct {
	Account string
	Side    Side
	Amount  Money
}

func Debit(account string, amount Money) Posting  { return Posting{account, DEBIT, amount} }
func Credit(account string, amount Money) Posting { return Posting{account, CREDIT, amount} }

func (p Posting) String() string {
	if p.Side == DEBIT {
		return "Dr " + p.Account + " " + p.Amount.String()
	}
	return "   Cr " + p.Account + " " + p.Amount.String()
}

type JournalEntry struct {
	ID          string
	Date        time.Time
	Description string
	Postings    []Posting
}

func NewJournalEntry(id string, date time.Time, description string, postings []Posting) (JournalEntry, error) {
	var net int64
	for _, p := range postings {
		if !p.Amount.IsPositive() {
			return JournalEntry{}, fmt.Errorf("%w: posting amount must be positive: %v", ErrInvalidArgument, p.Amount)
		}
		if p.Side == DEBIT {
			net += p.Amount.MinorUnits
		} else {
			net -= p.Amount.MinorUnits
		}
	}
	if len(postings) < 2 || net != 0 {
		return JournalEntry{}, fmt.Errorf("%w: entry %s is unbalanced or too short", ErrInvalidArgument, id)
	}
	return JournalEntry{id, date, description, append([]Posting(nil), postings...)}, nil
}

type Ledger struct {
	order    []string
	accounts map[string]AccountType
	balances map[string]int64
	journal  []JournalEntry
	currency Currency
}

func NewLedger(currency Currency) *Ledger {
	return &Ledger{accounts: map[string]AccountType{}, balances: map[string]int64{}, currency: currency}
}

func (l *Ledger) Open(account string, t AccountType) {
	if _, exists := l.accounts[account]; !exists {
		l.order = append(l.order, account)
	}
	l.accounts[account] = t
	l.balances[account] = 0
}

func (l *Ledger) Post(e JournalEntry) error {
	for _, p := range e.Postings {
		if _, ok := l.accounts[p.Account]; !ok {
			return fmt.Errorf("%w: unknown account %s", ErrInvalidArgument, p.Account)
		}
	}
	for _, p := range e.Postings {
		if _, err := addChecked(l.balances[p.Account], l.signed(p)); err != nil {
			return err
		}
	}
	l.journal = append(l.journal, e)
	for _, p := range e.Postings {
		l.balances[p.Account] += l.signed(p)
	}
	return nil
}

func (l *Ledger) signed(p Posting) int64 {
	if p.Side == l.accounts[p.Account].Normal() {
		return p.Amount.MinorUnits
	}
	return -p.Amount.MinorUnits
}

func (l *Ledger) Balance(account string) Money { return OfMinor(l.balances[account], l.currency) }

func (l *Ledger) Balances() bool {
	var debitSide, creditSide int64
	for _, a := range l.order {
		if l.accounts[a].Normal() == DEBIT {
			debitSide += l.balances[a]
		} else {
			creditSide += l.balances[a]
		}
	}
	return debitSide == creditSide
}

func (l *Ledger) Journal() []JournalEntry { return append([]JournalEntry(nil), l.journal...) }

// ---------------------------------------------------------------------------------------------
// Reason codes and evidence. A reason code says what the cardholder claims; it decides which
// evidence can answer the claim. Each requirement is "at least one of" a set of evidence types.
// ---------------------------------------------------------------------------------------------

type ReasonCategory int

const (
	FRAUD ReasonCategory = iota
	AUTHORIZATION
	PROCESSING_ERROR
	CONSUMER_DISPUTE
)

func (c ReasonCategory) String() string {
	return [...]string{"FRAUD", "AUTHORIZATION", "PROCESSING_ERROR", "CONSUMER_DISPUTE"}[c]
}

type EvidenceType int

const (
	RECEIPT EvidenceType = iota
	PROOF_OF_DELIVERY
	SIGNED_DELIVERY
	PRODUCT_DESCRIPTION
	CUSTOMER_COMMUNICATION
	REFUND_POLICY
	THREE_DS_AUTHENTICATION
	PRIOR_UNDISPUTED_TRANSACTIONS
	AVS_CVV_MATCH
	AUTHORIZATION_RECORD
	DUPLICATE_EXPLANATION
	REFUND_ISSUED
)

func (e EvidenceType) String() string {
	return [...]string{"RECEIPT", "PROOF_OF_DELIVERY", "SIGNED_DELIVERY", "PRODUCT_DESCRIPTION", "CUSTOMER_COMMUNICATION",
		"REFUND_POLICY", "THREE_DS_AUTHENTICATION", "PRIOR_UNDISPUTED_TRANSACTIONS", "AVS_CVV_MATCH", "AUTHORIZATION_RECORD",
		"DUPLICATE_EXPLANATION", "REFUND_ISSUED"}[e]
}

// Requirement is a set of evidence types of which at least one must be present. It is kept sorted in
// declaration order, as Java's EnumSet iterates, so messages and the printed table are deterministic.
type Requirement []EvidenceType

func OneOf(types ...EvidenceType) Requirement {
	r := append(Requirement(nil), types...)
	sort.Slice(r, func(i, j int) bool { return r[i] < r[j] })
	return r
}

func (r Requirement) String() string {
	parts := make([]string, len(r))
	for i, t := range r {
		parts[i] = t.String()
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func (r Requirement) Equal(other Requirement) bool {
	if len(r) != len(other) {
		return false
	}
	for i := range r {
		if r[i] != other[i] {
			return false
		}
	}
	return true
}

func formatRequirements(reqs []Requirement) string {
	parts := make([]string, len(reqs))
	for i, r := range reqs {
		parts[i] = r.String()
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

type ReasonCode struct {
	Network      string
	Code         string
	Title        string
	Category     ReasonCategory
	Requirements []Requirement
}

func (rc ReasonCode) Key() string { return rc.Network + " " + rc.Code }

// ReasonTable is the ordered rule list (Java's LinkedHashMap); reasonByKey is its index.
var ReasonTable = []ReasonCode{
	{"VISA", "10.4", "Other Fraud: Card-Absent Environment", FRAUD, []Requirement{OneOf(THREE_DS_AUTHENTICATION, PRIOR_UNDISPUTED_TRANSACTIONS), OneOf(RECEIPT)}},
	{"VISA", "11.3", "No Authorization", AUTHORIZATION, []Requirement{OneOf(AUTHORIZATION_RECORD)}},
	{"VISA", "12.6.1", "Duplicate Processing", PROCESSING_ERROR, []Requirement{OneOf(DUPLICATE_EXPLANATION)}},
	{"VISA", "13.1", "Merchandise/Services Not Received", CONSUMER_DISPUTE, []Requirement{OneOf(PROOF_OF_DELIVERY, SIGNED_DELIVERY), OneOf(RECEIPT)}},
	{"VISA", "13.3", "Not as Described or Defective", CONSUMER_DISPUTE, []Requirement{OneOf(PRODUCT_DESCRIPTION), OneOf(CUSTOMER_COMMUNICATION, REFUND_POLICY)}},
	{"VISA", "13.6", "Credit Not Processed", CONSUMER_DISPUTE, []Requirement{OneOf(REFUND_ISSUED, REFUND_POLICY)}},
	{"MASTERCARD", "4837", "No Cardholder Authorization", FRAUD, []Requirement{OneOf(THREE_DS_AUTHENTICATION, PRIOR_UNDISPUTED_TRANSACTIONS)}},
	{"MASTERCARD", "4834", "Point-of-Interaction Error", PROCESSING_ERROR, []Requirement{OneOf(DUPLICATE_EXPLANATION, RECEIPT)}},
	{"MASTERCARD", "4853", "Cardholder Dispute", CONSUMER_DISPUTE, []Requirement{OneOf(PROOF_OF_DELIVERY, PRODUCT_DESCRIPTION), OneOf(CUSTOMER_COMMUNICATION)}},
}

var reasonByKey = func() map[string]ReasonCode {
	m := map[string]ReasonCode{}
	for _, rc := range ReasonTable {
		m[rc.Key()] = rc
	}
	return m
}()

func GetReasonCode(network, code string) (ReasonCode, error) {
	rc, ok := reasonByKey[network+" "+code]
	if !ok {
		return ReasonCode{}, fmt.Errorf("%w: unknown reason code %s %s", ErrInvalidArgument, network, code)
	}
	return rc, nil
}

type EvidenceItem struct {
	Type      EvidenceType
	Reference string
}

type EvidenceBundle struct{ Items []EvidenceItem }

func EvidenceOf(items ...EvidenceItem) *EvidenceBundle {
	return &EvidenceBundle{Items: append([]EvidenceItem(nil), items...)}
}

func (b *EvidenceBundle) Has(t EvidenceType) bool {
	for _, i := range b.Items {
		if i.Type == t {
			return true
		}
	}
	return false
}

// MissingFor: requirements not met by any item: each entry is a set of which at least one must be present.
func (b *EvidenceBundle) MissingFor(reason ReasonCode) []Requirement {
	var missing []Requirement
	for _, req := range reason.Requirements {
		satisfied := false
		for _, t := range req {
			if b.Has(t) {
				satisfied = true
				break
			}
		}
		if !satisfied {
			missing = append(missing, req)
		}
	}
	return missing
}

// LiabilityShift: a fraud claim against a transaction the issuer itself authenticated with 3-D Secure shifts liability to the issuer.
func (b *EvidenceBundle) LiabilityShift(reason ReasonCode) bool {
	return reason.Category == FRAUD && b.Has(THREE_DS_AUTHENTICATION)
}

// ---------------------------------------------------------------------------------------------
// The dispute state machine (the Lesson 02 design: a table of rules, guards, a transition log,
// event-id deduplication), with one addition: most states carry a deadline, and when the clock
// passes it the system itself applies DEADLINE_PASSED.
// ---------------------------------------------------------------------------------------------

type DisputeState int

const (
	INQUIRY DisputeState = iota
	INQUIRY_RESPONDED
	CHARGEBACK
	REPRESENTED
	PRE_ARBITRATION
	PRE_ARB_RESPONDED
	ARBITRATION
	CLOSED_NO_CHARGEBACK
	WON
	LOST
)

var DisputeStates = []DisputeState{INQUIRY, INQUIRY_RESPONDED, CHARGEBACK, REPRESENTED, PRE_ARBITRATION, PRE_ARB_RESPONDED, ARBITRATION, CLOSED_NO_CHARGEBACK, WON, LOST}

func (s DisputeState) String() string {
	return [...]string{"INQUIRY", "INQUIRY_RESPONDED", "CHARGEBACK", "REPRESENTED", "PRE_ARBITRATION", "PRE_ARB_RESPONDED",
		"ARBITRATION", "CLOSED_NO_CHARGEBACK", "WON", "LOST"}[s]
}

func (s DisputeState) IsTerminal() bool { return s == CLOSED_NO_CHARGEBACK || s == WON || s == LOST }

type DisputeEventKind int

const (
	RESPOND_INQUIRY DisputeEventKind = iota
	INQUIRY_CLOSED
	CHARGEBACK_RECEIVED
	ACCEPT
	SUBMIT_REPRESENTMENT
	ISSUER_ACCEPTED
	PRE_ARB_RECEIVED
	RESPOND_PRE_ARB
	ISSUER_WITHDREW
	ARBITRATION_FILED
	RULING_FOR_MERCHANT
	RULING_FOR_ISSUER
	DEADLINE_PASSED
)

var DisputeEventKinds = []DisputeEventKind{RESPOND_INQUIRY, INQUIRY_CLOSED, CHARGEBACK_RECEIVED, ACCEPT, SUBMIT_REPRESENTMENT, ISSUER_ACCEPTED,
	PRE_ARB_RECEIVED, RESPOND_PRE_ARB, ISSUER_WITHDREW, ARBITRATION_FILED, RULING_FOR_MERCHANT, RULING_FOR_ISSUER, DEADLINE_PASSED}

func (k DisputeEventKind) String() string {
	return [...]string{"RESPOND_INQUIRY", "INQUIRY_CLOSED", "CHARGEBACK_RECEIVED", "ACCEPT", "SUBMIT_REPRESENTMENT", "ISSUER_ACCEPTED",
		"PRE_ARB_RECEIVED", "RESPOND_PRE_ARB", "ISSUER_WITHDREW", "ARBITRATION_FILED", "RULING_FOR_MERCHANT", "RULING_FOR_ISSUER", "DEADLINE_PASSED"}[k]
}

type DisputeGuard int

const (
	ALWAYS DisputeGuard = iota
	EVIDENCE_COMPLETE_IN_TIME
	DEADLINE_ELAPSED
)

type DisputeRule struct {
	From  DisputeState
	Kind  DisputeEventKind
	Guard DisputeGuard
	To    DisputeState
}

// DisputeEvent: Evidence is nil (Java null) for events that carry none.
type DisputeEvent struct {
	EventID  string
	Kind     DisputeEventKind
	Evidence *EvidenceBundle
	Note     string
}

func EventOf(id string, kind DisputeEventKind, note string) DisputeEvent {
	return DisputeEvent{id, kind, nil, note}
}

func WithEvidence(id string, kind DisputeEventKind, evidence *EvidenceBundle, note string) DisputeEvent {
	if evidence == nil {
		panic("evidence must not be nil")
	}
	return DisputeEvent{id, kind, evidence, note}
}

// DisputeTransition: Kind and From are nil for the opening record (Java null).
type DisputeTransition struct {
	Version int
	Date    time.Time
	EventID string
	Kind    *DisputeEventKind
	From    *DisputeState
	To      DisputeState
	Note    string
}

func (t DisputeTransition) String() string {
	kind, from := "OPENED", "-"
	if t.Kind != nil {
		kind = t.Kind.String()
	}
	if t.From != nil {
		from = t.From.String()
	}
	return fmt.Sprintf("v%-2d %s  %-20s %-17s -> %-17v %s", t.Version, fmtDate(t.Date), kind, from, t.To, t.Note)
}

// ErrIllegalTransition is the sentinel every refused transition matches with errors.Is; the error's
// text is the precise reason, as the Java exception message was.
var ErrIllegalTransition = errors.New("illegal dispute transition")

type transitionError struct{ msg string }

func (e *transitionError) Error() string        { return e.msg }
func (e *transitionError) Is(target error) bool { return target == ErrIllegalTransition }

func illegal(format string, args ...any) error { return &transitionError{fmt.Sprintf(format, args...)} }

type ApplyOutcome int

const (
	APPLIED ApplyOutcome = iota
	DUPLICATE
)

func (o ApplyOutcome) String() string { return [...]string{"APPLIED", "DUPLICATE"}[o] }

// DeadlinePolicy: days allowed in each state before DEADLINE_PASSED fires. A state absent from the map has no automatic deadline.
type DeadlinePolicy struct{ Days map[DisputeState]int }

func StandardDeadlinePolicy() DeadlinePolicy {
	return DeadlinePolicy{Days: map[DisputeState]int{
		INQUIRY:           20, // respond to a retrieval request (shown as overdue only)
		CHARGEBACK:        20, // merchant response; the network gives the acquirer 30
		REPRESENTED:       30, // issuer may answer with pre-arbitration
		PRE_ARBITRATION:   30, // merchant accepts or responds
		PRE_ARB_RESPONDED: 10, // issuer may escalate to arbitration
	}}
}

type Dispute struct {
	ID              string
	PaymentID       string
	Amount          Money
	Reason          ReasonCode
	State           DisputeState
	StateEnteredOn  time.Time
	Deadline        *time.Time // nil when the current state has no deadline
	Evidence        *EvidenceBundle
	History         []DisputeTransition
	appliedEventIDs map[string]bool
}

const (
	MERCHANT = "merchant_payable"  // liability: what we owe the merchant
	SCHEME   = "scheme_settlement" // asset: net position with the card scheme
	HOLD     = "dispute_hold"      // liability: funds re-credited by the scheme, held until the outcome
	FEES     = "fee_revenue"       // revenue: chargeback fees
)

var RULES = []DisputeRule{
	{INQUIRY, RESPOND_INQUIRY, EVIDENCE_COMPLETE_IN_TIME, INQUIRY_RESPONDED},
	{INQUIRY, CHARGEBACK_RECEIVED, ALWAYS, CHARGEBACK},
	{INQUIRY_RESPONDED, INQUIRY_CLOSED, ALWAYS, CLOSED_NO_CHARGEBACK},
	{INQUIRY_RESPONDED, CHARGEBACK_RECEIVED, ALWAYS, CHARGEBACK},
	{CHARGEBACK, ACCEPT, ALWAYS, LOST},
	{CHARGEBACK, SUBMIT_REPRESENTMENT, EVIDENCE_COMPLETE_IN_TIME, REPRESENTED},
	{CHARGEBACK, DEADLINE_PASSED, DEADLINE_ELAPSED, LOST},
	{REPRESENTED, ISSUER_ACCEPTED, ALWAYS, WON},
	{REPRESENTED, PRE_ARB_RECEIVED, ALWAYS, PRE_ARBITRATION},
	{REPRESENTED, DEADLINE_PASSED, DEADLINE_ELAPSED, WON},
	{PRE_ARBITRATION, ACCEPT, ALWAYS, LOST},
	{PRE_ARBITRATION, RESPOND_PRE_ARB, EVIDENCE_COMPLETE_IN_TIME, PRE_ARB_RESPONDED},
	{PRE_ARBITRATION, DEADLINE_PASSED, DEADLINE_ELAPSED, LOST},
	{PRE_ARB_RESPONDED, ISSUER_WITHDREW, ALWAYS, WON},
	{PRE_ARB_RESPONDED, ARBITRATION_FILED, ALWAYS, ARBITRATION},
	{PRE_ARB_RESPONDED, DEADLINE_PASSED, DEADLINE_ELAPSED, WON},
	{ARBITRATION, RULING_FOR_MERCHANT, ALWAYS, WON},
	{ARBITRATION, RULING_FOR_ISSUER, ALWAYS, LOST},
}

func findRule(from DisputeState, kind DisputeEventKind) (DisputeRule, bool) {
	for _, r := range RULES {
		if r.From == from && r.Kind == kind {
			return r, true
		}
	}
	return DisputeRule{}, false
}

type DisputeService struct {
	ledger         *Ledger
	clock          Clock
	deadlines      DeadlinePolicy
	chargebackFee  Money
	arbitrationFee Money
	order          []string
	disputes       map[string]*Dispute
	entrySeq       int
}

func NewDisputeService(ledger *Ledger, clock Clock, deadlines DeadlinePolicy, chargebackFee, arbitrationFee Money) *DisputeService {
	s := &DisputeService{ledger: ledger, clock: clock, deadlines: deadlines, chargebackFee: chargebackFee, arbitrationFee: arbitrationFee, disputes: map[string]*Dispute{}}
	ledger.Open(SCHEME, ASSET)
	ledger.Open(MERCHANT, LIABILITY)
	ledger.Open(HOLD, LIABILITY)
	ledger.Open(FEES, REVENUE)
	return s
}

func (s *DisputeService) Ledger() *Ledger { return s.ledger }

func (s *DisputeService) Dispute(id string) (*Dispute, error) {
	d, ok := s.disputes[id]
	if !ok {
		return nil, fmt.Errorf("%w: unknown dispute %s", ErrInvalidArgument, id)
	}
	return d, nil
}

// OpenInquiry: a retrieval request: the issuer asks for information. No money moves.
func (s *DisputeService) OpenInquiry(id, paymentID string, amount Money, reason ReasonCode) (*Dispute, error) {
	return s.open(id, paymentID, amount, reason, INQUIRY)
}

// OpenChargeback: a first chargeback: the scheme has already taken the money back from us.
func (s *DisputeService) OpenChargeback(id, paymentID string, amount Money, reason ReasonCode) (*Dispute, error) {
	d, err := s.open(id, paymentID, amount, reason, CHARGEBACK)
	if err != nil {
		return nil, err
	}
	if err := s.postMoney(d, INQUIRY, CHARGEBACK); err != nil {
		return nil, err
	}
	return d, nil
}

func (s *DisputeService) open(id, paymentID string, amount Money, reason ReasonCode, initial DisputeState) (*Dispute, error) {
	if _, exists := s.disputes[id]; exists {
		return nil, fmt.Errorf("%w: dispute exists: %s", ErrIllegalState, id)
	}
	if !amount.IsPositive() {
		return nil, fmt.Errorf("%w: dispute amount must be positive", ErrInvalidArgument)
	}
	today := s.clock.Today()
	d := &Dispute{ID: id, PaymentID: paymentID, Amount: amount, Reason: reason, State: initial, StateEnteredOn: today, appliedEventIDs: map[string]bool{}}
	d.Deadline = s.deadlineFor(initial, today)
	d.History = append(d.History, DisputeTransition{0, today, "open:" + id, nil, nil, initial, "opened: " + reason.Key() + " " + reason.Title})
	s.disputes[id] = d
	s.order = append(s.order, id)
	return d, nil
}

func (s *DisputeService) deadlineFor(state DisputeState, entered time.Time) *time.Time {
	days, ok := s.deadlines.Days[state]
	if !ok {
		return nil
	}
	t := entered.AddDate(0, 0, days)
	return &t
}

// Resolve picks the rule, checks its guard, or explains precisely why not. Pure: never mutates.
func (s *DisputeService) Resolve(d *Dispute, e DisputeEvent) (DisputeRule, error) {
	if d.State.IsTerminal() {
		return DisputeRule{}, illegal("%s is %v (terminal); %v refused", d.ID, d.State, e.Kind)
	}
	rule, ok := findRule(d.State, e.Kind)
	if !ok {
		return DisputeRule{}, illegal("%s: %v is not legal from %v", d.ID, e.Kind, d.State)
	}
	today := s.clock.Today()
	if rule.Guard == DEADLINE_ELAPSED && (d.Deadline == nil || !today.After(*d.Deadline)) {
		return DisputeRule{}, illegal("%s: DEADLINE_PASSED on %s but the deadline is %s", d.ID, fmtDate(today), fmtDeadline(d.Deadline))
	}
	if rule.Guard == EVIDENCE_COMPLETE_IN_TIME {
		if d.Deadline != nil && today.After(*d.Deadline) {
			return DisputeRule{}, illegal("%s: %v on %s is after the deadline %s", d.ID, e.Kind, fmtDate(today), fmtDate(*d.Deadline))
		}
		if e.Evidence == nil {
			return DisputeRule{}, illegal("%s: %v needs an evidence bundle", d.ID, e.Kind)
		}
		if missing := e.Evidence.MissingFor(d.Reason); len(missing) > 0 {
			return DisputeRule{}, illegal("%s: evidence for %s is incomplete; missing one of %s", d.ID, d.Reason.Key(), formatRequirements(missing))
		}
	}
	return rule, nil
}

func fmtDeadline(t *time.Time) string {
	if t == nil {
		return "null"
	}
	return fmtDate(*t)
}

func (s *DisputeService) Apply(disputeID string, e DisputeEvent) (ApplyOutcome, error) {
	d, err := s.Dispute(disputeID)
	if err != nil {
		return DUPLICATE, err
	}
	if d.appliedEventIDs[e.EventID] {
		return DUPLICATE, nil
	}
	rule, err := s.Resolve(d, e) // fails before any mutation
	if err != nil {
		return DUPLICATE, err
	}
	from := d.State
	if err := s.postMoney(d, from, rule.To); err != nil {
		return DUPLICATE, err
	}
	today := s.clock.Today()
	d.State = rule.To
	d.StateEnteredOn = today
	if rule.To.IsTerminal() {
		d.Deadline = nil
	} else {
		d.Deadline = s.deadlineFor(rule.To, today)
	}
	if e.Evidence != nil {
		d.Evidence = e.Evidence
	}
	d.appliedEventIDs[e.EventID] = true
	kind := e.Kind
	d.History = append(d.History, DisputeTransition{len(d.History), today, e.EventID, &kind, &from, rule.To, e.Note})
	return APPLIED, nil
}

// SweepDeadlines applies DEADLINE_PASSED to every open dispute whose deadline is before today and whose
// state has a deadline rule. The event id names the state and the deadline, so running the sweep twice is
// harmless. INQUIRY has a deadline but no rule: it is reported overdue, and only the issuer can move it.
func (s *DisputeService) SweepDeadlines() ([]string, error) {
	fired := []string{}
	today := s.clock.Today()
	for _, id := range s.order {
		d := s.disputes[id]
		if d.State.IsTerminal() || d.Deadline == nil || !today.After(*d.Deadline) {
			continue
		}
		if _, hasRule := findRule(d.State, DEADLINE_PASSED); !hasRule {
			fired = append(fired, fmt.Sprintf("%s overdue in %v (no automatic outcome)", d.ID, d.State))
			continue
		}
		before := d.State
		deadline := fmtDate(*d.Deadline)
		if _, err := s.Apply(d.ID, EventOf(fmt.Sprintf("deadline:%s:%v:%s", d.ID, d.State, deadline), DEADLINE_PASSED, "deadline "+deadline+" passed")); err != nil {
			return fired, err
		}
		fired = append(fired, fmt.Sprintf("%s %v -> %v", d.ID, before, d.State))
	}
	return fired, nil
}

// postMoney: the money that moves on each transition. The first chargeback debits the merchant (plus a fee);
// a representment brings the funds back from the scheme into a hold, not to the merchant; the outcome
// either releases the hold to the merchant or returns it to the scheme.
func (s *DisputeService) postMoney(d *Dispute, from, to DisputeState) error {
	var p []Posting
	var what string
	switch {
	case to == CHARGEBACK:
		what = "chargeback debited"
		p = []Posting{Debit(MERCHANT, d.Amount), Credit(SCHEME, d.Amount), Debit(MERCHANT, s.chargebackFee), Credit(FEES, s.chargebackFee)}
	case to == REPRESENTED:
		what = "representment: funds re-credited, held"
		p = []Posting{Debit(SCHEME, d.Amount), Credit(HOLD, d.Amount)}
	case to == WON && from != CHARGEBACK:
		what = "won: hold released to merchant"
		p = []Posting{Debit(HOLD, d.Amount), Credit(MERCHANT, d.Amount)}
	case to == LOST && from != CHARGEBACK:
		what = "lost after representment: hold returned to scheme"
		p = []Posting{Debit(HOLD, d.Amount), Credit(SCHEME, d.Amount)}
		if from == ARBITRATION {
			p = append(p, Debit(MERCHANT, s.arbitrationFee), Credit(SCHEME, s.arbitrationFee))
		}
	default:
		return nil // inquiry steps, pre-arbitration, arbitration filing, and LOST from CHARGEBACK move no money
	}
	s.entrySeq++
	e, err := NewJournalEntry(fmt.Sprintf("je_%03d", s.entrySeq), s.clock.Today(), d.ID+" "+what, p)
	if err != nil {
		return err
	}
	return s.ledger.Post(e)
}

// MerchantImpact: what this dispute has cost the merchant so far: the net of every merchant_payable posting tagged with its id.
func (s *DisputeService) MerchantImpact(disputeID string) (Money, error) {
	d, err := s.Dispute(disputeID)
	if err != nil {
		return Money{}, err
	}
	net := Zero(d.Amount.Currency)
	for _, e := range s.ledger.journal {
		if !strings.HasPrefix(e.Description, disputeID+" ") {
			continue
		}
		for _, p := range e.Postings {
			if p.Account != MERCHANT {
				continue
			}
			if p.Side == CREDIT {
				net = mustPlus(net, p.Amount)
			} else {
				net = mustMinus(net, p.Amount)
			}
		}
	}
	return net, nil
}

// ---------------------------------------------------------------------------------------------
// Demo
// ---------------------------------------------------------------------------------------------

func usd(minor int64) Money { return OfMinor(minor, USD) }

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// Service builds the demo service: 15.00 USD chargeback fee, 500.00 USD arbitration fee, standard deadlines.
func Service(clock *FakeClock) *DisputeService {
	return NewDisputeService(NewLedger(USD), clock, StandardDeadlinePolicy(), usd(1_500), usd(50_000))
}

func step(svc *DisputeService, clock *FakeClock, start time.Time, id, label string) {
	d := must(svc.Dispute(id))
	deadline := "-"
	if d.Deadline != nil {
		deadline = fmtDate(*d.Deadline)
	}
	fmt.Printf("day %3d  %-40s state %-17v deadline %-10s hold %-11s merchant %v\n",
		DaysBetween(start, clock.Today()), label, d.State, deadline, svc.Ledger().Balance(HOLD), must(svc.MerchantImpact(id)))
}

func reason(network, code string) ReasonCode { return must(GetReasonCode(network, code)) }

func main() {
	fmt.Println("== Reason codes and the evidence that answers them (each group: at least one of) ==")
	for _, rc := range ReasonTable {
		fmt.Printf("%-17s %-17v %-38s %s\n", rc.Key(), rc.Category, rc.Title, formatRequirements(rc.Requirements))
	}

	fmt.Println()
	fmt.Println("== A 'not received' dispute that runs the whole course: 249.00 USD, Visa 13.1 ==")
	start := Date(2026, time.February, 2)
	clock := NewFakeClock(start)
	svc := Service(clock)
	notReceived := reason("VISA", "13.1")
	must(svc.OpenChargeback("dp_1", "pay_77", usd(24_900), notReceived))
	step(svc, clock, start, "dp_1", "first chargeback received")
	clock.AdvanceDays(9)
	thin := EvidenceOf(EvidenceItem{RECEIPT, "inv_77.pdf"})
	if _, err := svc.Apply("dp_1", WithEvidence("ev_1", SUBMIT_REPRESENTMENT, thin, "receipt only")); errors.Is(err, ErrIllegalTransition) {
		fmt.Println("         refused:", err)
	}
	clock.AdvanceDays(3)
	full := EvidenceOf(EvidenceItem{RECEIPT, "inv_77.pdf"}, EvidenceItem{SIGNED_DELIVERY, "carrier POD 1Z999 signed J. DOE"})
	must(svc.Apply("dp_1", WithEvidence("ev_2", SUBMIT_REPRESENTMENT, full, "receipt + signed delivery")))
	step(svc, clock, start, "dp_1", "representment submitted")
	clock.AdvanceDays(29)
	must(svc.Apply("dp_1", EventOf("iss_3", PRE_ARB_RECEIVED, "issuer: cardholder says signature not theirs")))
	step(svc, clock, start, "dp_1", "pre-arbitration from issuer")
	clock.AdvanceDays(15)
	more := EvidenceOf(EvidenceItem{RECEIPT, "inv_77.pdf"}, EvidenceItem{SIGNED_DELIVERY, "carrier POD 1Z999 signed J. DOE"},
		EvidenceItem{CUSTOMER_COMMUNICATION, "email 2026-01-30 confirming arrival"})
	must(svc.Apply("dp_1", WithEvidence("ev_4", RESPOND_PRE_ARB, more, "pre-arb declined with new evidence")))
	step(svc, clock, start, "dp_1", "pre-arbitration response")
	clock.AdvanceDays(7)
	must(svc.Apply("dp_1", EventOf("iss_5", ARBITRATION_FILED, "issuer filed with Visa")))
	step(svc, clock, start, "dp_1", "issuer files arbitration")
	clock.AdvanceDays(55)
	must(svc.Apply("dp_1", EventOf("net_6", RULING_FOR_MERCHANT, "Visa ruling: merchant")))
	step(svc, clock, start, "dp_1", "network ruling for the merchant")
	fmt.Println("history:")
	for _, t := range must(svc.Dispute("dp_1")).History {
		fmt.Println("   " + t.String())
	}
	fmt.Println("duplicate ruling delivery ->", must(svc.Apply("dp_1", EventOf("net_6", RULING_FOR_MERCHANT, "again"))))

	fmt.Println()
	fmt.Println("== Missed deadline: a fraud chargeback nobody answers ==")
	s2 := clock.Today()
	must(svc.OpenChargeback("dp_2", "pay_88", usd(8_000), reason("VISA", "10.4")))
	clock.AdvanceDays(20)
	fmt.Printf("day 20 sweep: [%s]\n", strings.Join(must(svc.SweepDeadlines()), ", "))
	clock.AdvanceDays(1)
	fmt.Printf("day 21 sweep: [%s]\n", strings.Join(must(svc.SweepDeadlines()), ", "))
	step(svc, clock, s2, "dp_2", "after the sweep")

	fmt.Println()
	fmt.Println("== 3-D Secure: a fraud claim on an authenticated payment ==")
	s3 := clock.Today()
	fraud := reason("MASTERCARD", "4837")
	must(svc.OpenChargeback("dp_3", "pay_99", usd(12_000), fraud))
	threeDs := EvidenceOf(EvidenceItem{THREE_DS_AUTHENTICATION, "ECI 02, authentication value present"})
	fmt.Println("liability shift:", threeDs.LiabilityShift(fraud))
	clock.AdvanceDays(4)
	must(svc.Apply("dp_3", WithEvidence("ev_31", SUBMIT_REPRESENTMENT, threeDs, "3DS authenticated")))
	clock.AdvanceDays(31)
	fmt.Printf("day 35 sweep: [%s]\n", strings.Join(must(svc.SweepDeadlines()), ", "))
	step(svc, clock, s3, "dp_3", "issuer silent past its deadline")

	fmt.Println()
	fmt.Println("== Inquiry answered, issuer closes it: no money moves ==")
	s4 := clock.Today()
	must(svc.OpenInquiry("dp_4", "pay_11", usd(4_500), reason("VISA", "13.3")))
	must(svc.Apply("dp_4", WithEvidence("ev_41", RESPOND_INQUIRY,
		EvidenceOf(EvidenceItem{PRODUCT_DESCRIPTION, "listing snapshot"}, EvidenceItem{REFUND_POLICY, "returns page"}), "answered")))
	clock.AdvanceDays(6)
	must(svc.Apply("dp_4", EventOf("iss_42", INQUIRY_CLOSED, "issuer satisfied")))
	step(svc, clock, s4, "dp_4", "inquiry closed")

	fmt.Println()
	fmt.Println("== Arbitration lost: amount, chargeback fee, and arbitration fee ==")
	s5 := clock.Today()
	must(svc.OpenChargeback("dp_5", "pay_55", usd(30_000), reason("MASTERCARD", "4853")))
	spec := EvidenceOf(EvidenceItem{PRODUCT_DESCRIPTION, "spec sheet"}, EvidenceItem{CUSTOMER_COMMUNICATION, "chat log"})
	must(svc.Apply("dp_5", WithEvidence("ev_51", SUBMIT_REPRESENTMENT, spec, "represented")))
	must(svc.Apply("dp_5", EventOf("iss_52", PRE_ARB_RECEIVED, "issuer disagrees")))
	must(svc.Apply("dp_5", WithEvidence("ev_53", RESPOND_PRE_ARB, spec, "declined pre-arb")))
	must(svc.Apply("dp_5", EventOf("iss_54", ARBITRATION_FILED, "filed")))
	must(svc.Apply("dp_5", EventOf("net_55", RULING_FOR_ISSUER, "ruling: issuer")))
	step(svc, clock, s5, "dp_5", "arbitration lost")

	fmt.Println()
	fmt.Println("== Ledger ==")
	for _, a := range []string{SCHEME, MERCHANT, HOLD, FEES} {
		fmt.Printf("   %-18s %12s\n", a, svc.Ledger().Balance(a))
	}
	fmt.Printf("   ledger balances: %t, journal entries: %d\n", svc.Ledger().Balances(), len(svc.Ledger().Journal()))
}
