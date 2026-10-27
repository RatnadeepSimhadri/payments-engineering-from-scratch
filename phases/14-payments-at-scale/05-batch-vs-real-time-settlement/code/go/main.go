// Lesson: phases/14-payments-at-scale/05-batch-vs-real-time-settlement/docs/en.md
// Batch vs Real-Time Settlement Pipelines
// Run:  go run .
// Test: go test ./...
// Sources: Nacha Operating Rules (batch/file control records, entry hash); ISO 20022 pain.001 (payment initiation).
// Go 1.22+, standard library only. Money is integer minor units + currency (Phase 10, Lesson 01).
package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Currency is an ISO 4217 currency: three-letter code and minor-unit exponent. Copied from Phase 10, Lesson 01.
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

type Rounding int

const (
	HalfEven Rounding = iota
	HalfUp
	Down
	Up
)

var (
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrOverflow         = errors.New("int64 overflow")
)

// Money is an exact amount of one currency as a signed count of minor units. Copied from Phase 10, Lesson 01.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }
func Zero(currency Currency) Money                      { return Money{0, currency} }

// Parse reads "120.00" or "-1.00" exactly; it never goes through a float64.
func Parse(text string, currency Currency) (Money, error) {
	s := strings.TrimSpace(text)
	negative := strings.HasPrefix(s, "-")
	if negative {
		s = s[1:]
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	if len(frac) > currency.Exponent {
		return Money{}, fmt.Errorf("too many decimals: %s", text)
	}
	frac += strings.Repeat("0", currency.Exponent-len(frac))
	minor, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil {
		return Money{}, fmt.Errorf("not a decimal amount: %q", text)
	}
	if negative {
		minor = -minor
	}
	return Money{minor, currency}, nil
}

func addChecked(a, b int64) (int64, error) {
	sum := a + b
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum >= 0) {
		return 0, ErrOverflow
	}
	return sum, nil
}

func mulChecked(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	product := a * b
	if product/b != a {
		return 0, ErrOverflow
	}
	return product, nil
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
	diff, err := addChecked(m.MinorUnits, -other.MinorUnits)
	return Money{diff, m.Currency}, err
}

func (m Money) Negate() Money    { return Money{-m.MinorUnits, m.Currency} }
func (m Money) IsPositive() bool { return m.MinorUnits > 0 }
func (m Money) IsNegative() bool { return m.MinorUnits < 0 }

func (m Money) Times(numerator, denominator int64, rounding Rounding) (Money, error) {
	product, err := mulChecked(m.MinorUnits, numerator)
	if err != nil {
		return Money{}, err
	}
	return Money{DivideRounded(product, denominator, rounding), m.Currency}, nil
}

// DivideRounded is integer division with an explicit rounding rule; Go's / and % truncate like Java's.
func DivideRounded(dividend, divisor int64, rounding Rounding) int64 {
	quotient := dividend / divisor
	remainder := dividend % divisor
	if remainder == 0 {
		return quotient
	}
	sign := int64(1)
	if (dividend < 0) != (divisor < 0) {
		sign = -1
	}
	twice, abs := abs64(remainder)*2, abs64(divisor)
	switch rounding {
	case Down:
		return quotient
	case Up:
		return quotient + sign
	case HalfUp:
		if twice >= abs {
			return quotient + sign
		}
		return quotient
	default: // HalfEven
		if twice > abs {
			return quotient + sign
		}
		if twice < abs || quotient%2 == 0 {
			return quotient
		}
		return quotient + sign
	}
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// Compare panics on a currency mismatch, as the Java compareTo throws: it is a programming error.
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
	magnitude := abs64(m.MinorUnits)
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

// need panics on an error; catch at an API boundary turns the panic back into the returned error.
func need[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func catch(err *error) {
	if r := recover(); r != nil {
		e, ok := r.(error)
		if !ok {
			panic(r)
		}
		*err = e
	}
}

// ---------------------------------------------------------------------------------------------
// Domain: merchants, settlement events, fees
// ---------------------------------------------------------------------------------------------

// Merchant is a merchant and the bank account its payouts go to. Routing is a 9-digit ABA routing number.
type Merchant struct {
	Id      string
	Routing string
	Account string
	OnHold  bool
}

func NewMerchant(id, routing, account string, onHold bool) (Merchant, error) {
	if len(routing) != 9 || strings.Trim(routing, "0123456789") != "" {
		return Merchant{}, fmt.Errorf("routing number must be 9 digits: %s", routing)
	}
	return Merchant{id, routing, account, onHold}, nil
}

type EventType int

const (
	Capture EventType = iota
	Refund
	Chargeback
)

func (t EventType) String() string { return [...]string{"CAPTURE", "REFUND", "CHARGEBACK"}[t] }

// SettlementEvent is one money-moving event that the acquirer will settle with the platform: amount is always positive.
type SettlementEvent struct {
	Id         string
	MerchantId string
	Type       EventType
	Amount     Money
	At         time.Time
}

func NewSettlementEvent(id, merchantId string, t EventType, amount Money, at time.Time) (SettlementEvent, error) {
	if !amount.IsPositive() {
		return SettlementEvent{}, fmt.Errorf("event amounts are positive; the type gives the sign: %s", id)
	}
	return SettlementEvent{id, merchantId, t, amount, at}, nil
}

// FeeSchedule is a percentage fee (numerator/denominator) plus a fixed fee per capture, and a flat chargeback fee.
type FeeSchedule struct {
	PercentNumerator   int64
	PercentDenominator int64
	Fixed              Money
	ChargebackFee      Money
}

func StandardFees(c Currency) FeeSchedule {
	return FeeSchedule{29, 1000, OfMinor(30, c), OfMinor(1500, c)} // 2.9% + 0.30, 15.00
}

// FeeFor is the fee the platform keeps on this event. Refunds return no fee (the common card-acquiring policy).
func (f FeeSchedule) FeeFor(e SettlementEvent) (Money, error) {
	switch e.Type {
	case Capture:
		pct, err := e.Amount.Times(f.PercentNumerator, f.PercentDenominator, HalfEven)
		if err != nil {
			return Money{}, err
		}
		return pct.Plus(f.Fixed)
	case Refund:
		return Zero(e.Amount.Currency), nil
	default:
		return f.ChargebackFee, nil
	}
}

// MerchantEffect is the signed effect of the event on what the platform owes the merchant.
func (f FeeSchedule) MerchantEffect(e SettlementEvent) (Money, error) {
	switch e.Type {
	case Capture:
		fee, err := f.FeeFor(e)
		if err != nil {
			return Money{}, err
		}
		return e.Amount.Minus(fee)
	case Refund:
		return e.Amount.Negate(), nil
	default:
		sum, err := e.Amount.Plus(f.ChargebackFee)
		return sum.Negate(), err
	}
}

// ProcessorEffect is the signed effect of the event on what the acquirer owes the platform.
func ProcessorEffect(e SettlementEvent) Money {
	if e.Type == Capture {
		return e.Amount
	}
	return e.Amount.Negate()
}

// ---------------------------------------------------------------------------------------------
// A minimal double-entry ledger: debit positive, credit negative, every journal sums to zero.
// ---------------------------------------------------------------------------------------------

type Posting struct {
	Account string
	Minor   int64
}

// ErrUnbalancedJournal is returned by Post when the postings do not sum to zero.
var ErrUnbalancedJournal = errors.New("journal does not balance")

const (
	ProcessorReceivable = "processor_receivable"
	FeeRevenue          = "fee_revenue"
	PayoutsInTransit    = "payouts_in_transit"
	Cash                = "cash:settlement_bank"
)

func MerchantPayable(merchantId string) string { return "merchant_payable:" + merchantId }

type AccountBalance struct {
	Account string
	Minor   int64
}

type Ledger struct {
	Currency Currency
	balances map[string]int64
	journals map[string][]Posting
	minCash  int64
}

func NewLedger(currency Currency) *Ledger {
	return &Ledger{Currency: currency, balances: map[string]int64{}, journals: map[string][]Posting{}}
}

// Post posts a balanced journal once. Returns false (and changes nothing) if the id was already posted.
func (l *Ledger) Post(journalId string, postings []Posting) (bool, error) {
	if _, dup := l.journals[journalId]; dup {
		return false, nil
	}
	var sum int64
	for _, p := range postings {
		var err error
		if sum, err = addChecked(sum, p.Minor); err != nil {
			return false, err
		}
	}
	if sum != 0 {
		return false, fmt.Errorf("%w: journal %s does not balance: sum %d", ErrUnbalancedJournal, journalId, sum)
	}
	for _, p := range postings {
		next, err := addChecked(l.balances[p.Account], p.Minor)
		if err != nil {
			return false, err
		}
		l.balances[p.Account] = next
	}
	l.journals[journalId] = append([]Posting{}, postings...)
	l.minCash = min(l.minCash, l.balances[Cash])
	return true, nil
}

func (l *Ledger) Balance(account string) Money { return OfMinor(l.balances[account], l.Currency) }

// OwedTo is what the platform owes the merchant: the credit balance of its payable account, as a positive number.
func (l *Ledger) OwedTo(merchantId string) Money {
	return l.Balance(MerchantPayable(merchantId)).Negate()
}

func (l *Ledger) TrialBalance() int64 {
	var sum int64
	for _, v := range l.balances {
		sum += v
	}
	return sum
}

func (l *Ledger) JournalCount() int { return len(l.journals) }

// PeakPrefunding is the most negative the settlement bank account has been: how much the platform had to prefund.
func (l *Ledger) PeakPrefunding() Money { return OfMinor(-l.minCash, l.Currency) }

// BalancesView lists every account in sorted order (what Java's TreeMap gives for free).
func (l *Ledger) BalancesView() []AccountBalance {
	out := make([]AccountBalance, 0, len(l.balances))
	for acct, v := range l.balances {
		out = append(out, AccountBalance{acct, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Account < out[j].Account })
	return out
}

// ErrUnknownMerchant is returned when an event names a merchant the platform does not know.
var ErrUnknownMerchant = errors.New("unknown merchant")

// Platform books events into the ledger as they happen. Shared by both settlement styles.
type Platform struct {
	Ledger    *Ledger
	Fees      FeeSchedule
	Merchants map[string]Merchant
	Events    []SettlementEvent
}

func NewPlatform(currency Currency, fees FeeSchedule) *Platform {
	return &Platform{Ledger: NewLedger(currency), Fees: fees, Merchants: map[string]Merchant{}}
}

func (p *Platform) AddMerchant(m Merchant) { p.Merchants[m.Id] = m }

func (p *Platform) merchantIds() []string {
	ids := make([]string, 0, len(p.Merchants))
	for id := range p.Merchants {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Book posts the event's journal at the moment it happens. Idempotent on event id.
func (p *Platform) Book(e SettlementEvent) (bool, error) {
	if _, ok := p.Merchants[e.MerchantId]; !ok {
		return false, fmt.Errorf("%w %s", ErrUnknownMerchant, e.MerchantId)
	}
	if e.Amount.Currency != p.Ledger.Currency {
		return false, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, p.Ledger.Currency.Code, e.Amount.Currency.Code)
	}
	payable := MerchantPayable(e.MerchantId)
	amount := e.Amount.MinorUnits
	feeMoney, err := p.Fees.FeeFor(e)
	if err != nil {
		return false, err
	}
	fee := feeMoney.MinorUnits
	var postings []Posting
	switch e.Type {
	case Capture:
		postings = []Posting{{ProcessorReceivable, amount}, {payable, -(amount - fee)}, {FeeRevenue, -fee}}
	case Refund:
		postings = []Posting{{payable, amount}, {ProcessorReceivable, -amount}}
	default:
		postings = []Posting{{payable, amount + fee}, {ProcessorReceivable, -amount}, {FeeRevenue, -fee}}
	}
	fresh, err := p.Ledger.Post("evt:"+e.Id, postings)
	if err != nil {
		return false, err
	}
	if fresh {
		p.Events = append(p.Events, e)
	}
	return fresh, nil
}

// ReceiveFunding: the acquirer pays what it owes for a set of events: cash in, receivable down.
func (p *Platform) ReceiveFunding(fundingId string, amount Money) error {
	_, err := p.Ledger.Post("fund:"+fundingId, []Posting{{Cash, amount.MinorUnits}, {ProcessorReceivable, -amount.MinorUnits}})
	return err
}

// ---------------------------------------------------------------------------------------------
// Batch settlement: cutoff, per-merchant netting, payout instructions, settlement file.
// ---------------------------------------------------------------------------------------------

type PayoutInstruction struct {
	Sequence   int
	MerchantId string
	Routing    string
	Account    string
	Amount     Money
}

type SettlementBatch struct {
	BatchId          string
	BusinessDate     time.Time
	Cutoff           time.Time
	EventIds         []string
	Instructions     []PayoutInstruction
	CarriedForward   map[string]Money
	ProcessorFunding Money
}

func (b *SettlementBatch) Total() Money {
	sum := Zero(b.ProcessorFunding.Currency)
	for _, p := range b.Instructions {
		sum = need(sum.Plus(p.Amount))
	}
	return sum
}

// CarriedMerchants lists the carried-forward merchants in sorted order.
func (b *SettlementBatch) CarriedMerchants() []string {
	ids := make([]string, 0, len(b.CarriedForward))
	for id := range b.CarriedForward {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

type BatchSettler struct {
	platform      *Platform
	minimumPayout Money
	settledBy     map[string]string // event id -> batch id
	carry         map[string]Money  // merchant -> carried net
	batches       map[string]*SettlementBatch
}

func NewBatchSettler(platform *Platform, minimumPayout Money) *BatchSettler {
	return &BatchSettler{platform, minimumPayout, map[string]string{}, map[string]Money{}, map[string]*SettlementBatch{}}
}

func (s *BatchSettler) Carried(merchantId string) Money {
	if m, ok := s.carry[merchantId]; ok {
		return m
	}
	return Zero(s.platform.Ledger.Currency)
}

func (s *BatchSettler) IsSettled(eventId string) bool { _, ok := s.settledBy[eventId]; return ok }

func isoDate(t time.Time) string { return t.Format("2006-01-02") }

// Run closes the books for one business date. Every booked, unsettled event before the cutoff for a merchant
// not on hold is netted into one amount per merchant. Positive nets at or above the minimum become payout
// instructions; the rest are carried to the next batch. Running the same business date twice returns the
// first batch unchanged: the batch id is the idempotency key.
func (s *BatchSettler) Run(businessDate, cutoff time.Time) (batch *SettlementBatch, err error) {
	defer catch(&err)
	batchId := "BATCH-" + isoDate(businessDate)
	if existing, ok := s.batches[batchId]; ok {
		return existing, nil
	}

	c := s.platform.Ledger.Currency
	net := map[string]Money{}
	included := map[string][]string{}
	funding := Zero(c)
	addNet := func(id string, m Money) {
		cur, ok := net[id]
		if !ok {
			net[id] = m
			return
		}
		net[id] = need(cur.Plus(m))
	}
	for _, e := range s.platform.Events {
		if s.IsSettled(e.Id) || !e.At.Before(cutoff) {
			continue
		}
		if s.platform.Merchants[e.MerchantId].OnHold {
			continue
		}
		addNet(e.MerchantId, need(s.platform.Fees.MerchantEffect(e)))
		included[e.MerchantId] = append(included[e.MerchantId], e.Id)
		funding = need(funding.Plus(ProcessorEffect(e)))
	}
	for id, carried := range s.carry {
		if !s.platform.Merchants[id].OnHold {
			addNet(id, carried)
		}
	}

	merchantIds := make([]string, 0, len(net))
	for id := range net {
		merchantIds = append(merchantIds, id)
	}
	sort.Strings(merchantIds) // TreeMap order: one deterministic sequence per merchant id
	instructions := []PayoutInstruction{}
	carriedOut := map[string]Money{}
	for _, merchantId := range merchantIds {
		amount := net[merchantId]
		m := s.platform.Merchants[merchantId]
		switch {
		case amount.IsPositive() && amount.Compare(s.minimumPayout) >= 0:
			instructions = append(instructions, PayoutInstruction{len(instructions) + 1, merchantId, m.Routing, m.Account, amount})
			need(s.platform.Ledger.Post("payout:"+batchId+":"+merchantId, []Posting{
				{MerchantPayable(merchantId), amount.MinorUnits}, {PayoutsInTransit, -amount.MinorUnits}}))
			delete(s.carry, merchantId)
		case amount.MinorUnits != 0:
			s.carry[merchantId] = amount
			carriedOut[merchantId] = amount
		default:
			delete(s.carry, merchantId)
		}
	}
	eventIds := []string{}
	includedIds := make([]string, 0, len(included))
	for id := range included {
		includedIds = append(includedIds, id)
	}
	sort.Strings(includedIds)
	for _, merchantId := range includedIds {
		for _, id := range included[merchantId] {
			s.settledBy[id] = batchId
			eventIds = append(eventIds, id)
		}
	}
	batch = &SettlementBatch{batchId, businessDate, cutoff, eventIds, instructions, carriedOut, funding}
	s.batches[batchId] = batch
	return batch, nil
}

// ConfirmPayouts: the bank confirms the payout file was executed: in-transit clears against cash.
func (s *BatchSettler) ConfirmPayouts(batch *SettlementBatch) error {
	total := batch.Total().MinorUnits
	if total == 0 {
		return nil
	}
	_, err := s.platform.Ledger.Post("confirm:"+batch.BatchId, []Posting{{PayoutsInTransit, total}, {Cash, -total}})
	return err
}

// TiesOut is the tie-out: for every merchant, the ledger's payable balance must equal what is carried plus
// the effect of events not yet settled. If this is false, a payout was lost or paid twice.
func (s *BatchSettler) TiesOut() bool {
	expected := map[string]Money{}
	for _, id := range s.platform.merchantIds() {
		expected[id] = s.Carried(id)
	}
	for _, e := range s.platform.Events {
		if !s.IsSettled(e.Id) {
			expected[e.MerchantId] = need(expected[e.MerchantId].Plus(need(s.platform.Fees.MerchantEffect(e))))
		}
	}
	for id, amount := range expected {
		if s.platform.Ledger.OwedTo(id) != amount {
			return false
		}
	}
	return s.platform.Ledger.TrialBalance() == 0
}

// SettlementFile renders a batch as a fixed-layout text file in the spirit of a Nacha file: a header, one
// detail line per payout, and a control record with entry count, total, and entry hash (the sum of the first
// eight digits of every routing number, modulo 10^10). The receiving bank recomputes all three.
var SettlementFile settlementFile

type settlementFile struct{}

func (settlementFile) Render(batch *SettlementBatch) string {
	var out strings.Builder
	fmt.Fprintf(&out, "H|%s|%s|%s\n", batch.BatchId, isoDate(batch.BusinessDate), batch.ProcessorFunding.Currency.Code)
	var total int64
	for _, p := range batch.Instructions {
		fmt.Fprintf(&out, "D|%06d|%s|%s|%s|%d\n", p.Sequence, p.MerchantId, p.Routing, p.Account, p.Amount.MinorUnits)
		total = need(addChecked(total, p.Amount.MinorUnits))
	}
	fmt.Fprintf(&out, "T|%d|%d|%010d\n", len(batch.Instructions), total, SettlementFile.EntryHash(batch.Instructions))
	return out.String()
}

func (settlementFile) EntryHash(instructions []PayoutInstruction) int64 {
	var hash int64
	for _, p := range instructions {
		n, _ := strconv.ParseInt(p.Routing[:8], 10, 64)
		hash += n
	}
	return hash % 10_000_000_000
}

// Validate is the receiver-side validation: recompute count, total and hash from the detail lines.
func (settlementFile) Validate(file string) bool {
	var count, total, hash int64
	var trailer []string
	for _, line := range strings.Split(file, "\n") {
		f := strings.Split(line, "|")
		switch f[0] {
		case "D":
			if len(f) < 6 || len(f[3]) < 8 {
				return false
			}
			amount, err1 := strconv.ParseInt(f[5], 10, 64)
			routing, err2 := strconv.ParseInt(f[3][:8], 10, 64)
			if err1 != nil || err2 != nil {
				return false
			}
			count++
			total += amount
			hash += routing
		case "T":
			trailer = f
		}
	}
	if trailer == nil || len(trailer) < 4 {
		return false
	}
	n, e1 := strconv.ParseInt(trailer[1], 10, 64)
	t, e2 := strconv.ParseInt(trailer[2], 10, 64)
	h, e3 := strconv.ParseInt(trailer[3], 10, 64)
	return e1 == nil && e2 == nil && e3 == nil && n == count && t == total && h == hash%10_000_000_000
}

// ---------------------------------------------------------------------------------------------
// Real-time settlement: pay each merchant's positive balance the moment it appears.
// ---------------------------------------------------------------------------------------------

type RealTimeSettler struct {
	platform  *Platform
	transfers int
	grossPaid Money
}

func NewRealTimeSettler(platform *Platform) *RealTimeSettler {
	return &RealTimeSettler{platform: platform, grossPaid: Zero(platform.Ledger.Currency)}
}

// OnEvent books the event, then sweeps the merchant's positive balance over an instant rail.
func (r *RealTimeSettler) OnEvent(e SettlementEvent) error {
	fresh, err := r.platform.Book(e)
	if err != nil || !fresh {
		return err
	}
	owed := r.platform.Ledger.OwedTo(e.MerchantId)
	if !owed.IsPositive() {
		return nil
	}
	r.transfers++
	if r.grossPaid, err = r.grossPaid.Plus(owed); err != nil {
		return err
	}
	_, err = r.platform.Ledger.Post("rt:"+e.Id, []Posting{{MerchantPayable(e.MerchantId), owed.MinorUnits}, {Cash, -owed.MinorUnits}})
	return err
}

func (r *RealTimeSettler) Transfers() int   { return r.transfers }
func (r *RealTimeSettler) GrossPaid() Money { return r.grossPaid }

// ClawbackExposure is the sum of negative merchant balances: money already paid out that must be clawed back.
func (r *RealTimeSettler) ClawbackExposure() Money {
	exposure := Zero(r.platform.Ledger.Currency)
	for _, id := range r.platform.merchantIds() {
		if owed := r.platform.Ledger.OwedTo(id); owed.IsNegative() {
			exposure = need(exposure.Minus(owed))
		}
	}
	return exposure
}

// LiquidityComparison is the side-by-side numbers for the two pipelines over the same day of events.
type LiquidityComparison struct {
	BatchTransfers         int
	BatchPaid              Money
	BatchPeakPrefunding    Money
	BatchCashAfter         Money
	RealTimeTransfers      int
	RealTimePaid           Money
	RealTimePeakPrefunding Money
	RealTimeExposure       Money
	RealTimeCashAfter      Money
}

func RunLiquidityComparison(merchants []Merchant, day []SettlementEvent, date, cutoff time.Time, c Currency) (cmp LiquidityComparison, err error) {
	defer catch(&err)
	batchPlatform := NewPlatform(c, StandardFees(c))
	for _, m := range merchants {
		batchPlatform.AddMerchant(m)
	}
	settler := NewBatchSettler(batchPlatform, Zero(c))
	for _, e := range day {
		need(batchPlatform.Book(e))
	}
	batch := need(settler.Run(date, cutoff))
	need(0, batchPlatform.ReceiveFunding(batch.BatchId, batch.ProcessorFunding)) // T+1 morning: acquirer pays first
	need(0, settler.ConfirmPayouts(batch))                                       // T+1 afternoon: payout file executes

	rtPlatform := NewPlatform(c, StandardFees(c))
	for _, m := range merchants {
		rtPlatform.AddMerchant(m)
	}
	rt := NewRealTimeSettler(rtPlatform)
	rtFunding := Zero(c)
	for _, e := range day {
		if !e.At.Before(cutoff) {
			continue // same scope as the batch, so the comparison is fair
		}
		need(0, rt.OnEvent(e))
		rtFunding = need(rtFunding.Plus(ProcessorEffect(e)))
	}
	exposure := rt.ClawbackExposure()
	rtPeak := rtPlatform.Ledger.PeakPrefunding()
	need(0, rtPlatform.ReceiveFunding("T+1", rtFunding))

	return LiquidityComparison{len(batch.Instructions), batch.Total(), batchPlatform.Ledger.PeakPrefunding(),
		batchPlatform.Ledger.Balance(Cash), rt.Transfers(), rt.GrossPaid(), rtPeak, exposure,
		rtPlatform.Ledger.Balance(Cash)}, nil
}

// ---------------------------------------------------------------------------------------------
// Demo
// ---------------------------------------------------------------------------------------------

var (
	Day    = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	Cutoff = time.Date(2026, 3, 31, 23, 0, 0, 0, time.UTC)
)

func at(hhmm string) time.Time {
	t, err := time.Parse(time.RFC3339, "2026-03-31T"+hhmm+":00Z")
	if err != nil {
		panic(err)
	}
	return t
}

func usd(s string) Money { return need(Parse(s, USD)) }

func DemoMerchants() []Merchant {
	return []Merchant{
		need(NewMerchant("m_acme", "021000021", "100200300", false)),
		need(NewMerchant("m_bolt", "026009593", "555000111", false)),
		need(NewMerchant("m_cora", "011000138", "987654321", false)),
	}
}

func DemoDay() []SettlementEvent {
	return []SettlementEvent{
		need(NewSettlementEvent("c1", "m_acme", Capture, usd("120.00"), at("09:15"))),
		need(NewSettlementEvent("c3", "m_bolt", Capture, usd("45.50"), at("10:05"))),
		need(NewSettlementEvent("c2", "m_acme", Capture, usd("80.00"), at("11:40"))),
		need(NewSettlementEvent("c4", "m_cora", Capture, usd("1000.00"), at("12:30"))),
		need(NewSettlementEvent("c6", "m_cora", Capture, usd("250.00"), at("13:45"))),
		need(NewSettlementEvent("r1", "m_acme", Refund, usd("20.00"), at("15:05"))),
		need(NewSettlementEvent("cb1", "m_bolt", Chargeback, usd("60.00"), at("16:20"))),
		need(NewSettlementEvent("c5", "m_cora", Capture, usd("10.00"), at("23:30"))),
	}
}

// GeneratedDay is a seeded day: many merchants, many events, a realistic mix of refunds and chargebacks.
func GeneratedDay(merchants []Merchant, count int, seed int64) []SettlementEvent {
	rnd := NewJavaRandom(seed)
	out := make([]SettlementEvent, 0, count)
	dayStart := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC).Unix()
	for i := 0; i < count; i++ {
		m := merchants[rnd.NextIntBound(int32(len(merchants)))]
		roll := rnd.NextIntBound(1000)
		var t EventType
		switch {
		case roll < 930:
			t = Capture
		case roll < 995:
			t = Refund
		default:
			t = Chargeback
		}
		minor := 500 + int64(rnd.NextIntBound(49_500)) // 5.00 .. 499.99
		when := time.Unix(dayStart+int64(i)*86_400/int64(count), 0).UTC()
		out = append(out, SettlementEvent{"g" + strconv.Itoa(i), m.Id, t, OfMinor(minor, USD), when})
	}
	return out
}

func GeneratedMerchants(n int) []Merchant {
	out := make([]Merchant, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, need(NewMerchant(fmt.Sprintf("m_%04d", i), fmt.Sprintf("0%08d", 21000021+i), "ACCT"+strconv.Itoa(i), false)))
	}
	return out
}

func printComparison(cmp LiquidityComparison) {
	fmt.Printf("  %-26s %-18s %-18s\n", "", "batch (T+1)", "real time")
	fmt.Printf("  %-26s %-18d %-18d\n", "transfers sent", cmp.BatchTransfers, cmp.RealTimeTransfers)
	fmt.Printf("  %-26s %-18s %-18s\n", "value paid to merchants", cmp.BatchPaid, cmp.RealTimePaid)
	fmt.Printf("  %-26s %-18s %-18s\n", "peak prefunding", cmp.BatchPeakPrefunding, cmp.RealTimePeakPrefunding)
	fmt.Printf("  %-26s %-18s %-18s\n", "clawback exposure", "0.00 USD", cmp.RealTimeExposure)
	fmt.Printf("  %-26s %-18s %-18s\n", "cash after T+1 funding", cmp.BatchCashAfter, cmp.RealTimeCashAfter)
}

func main() {
	fmt.Println("== One business day, three merchants, cutoff 23:00 ==")
	platform := NewPlatform(USD, StandardFees(USD))
	for _, m := range DemoMerchants() {
		platform.AddMerchant(m)
	}
	for _, e := range DemoDay() {
		need(platform.Book(e))
		fmt.Printf("%s %-10s %-6s %-12s fee %-9s merchant %s\n", e.At.Format("15:04"), e.Type, e.MerchantId,
			e.Amount, need(platform.Fees.FeeFor(e)), need(platform.Fees.MerchantEffect(e)))
	}
	settler := NewBatchSettler(platform, Zero(USD))
	batch := need(settler.Run(Day, Cutoff))
	fmt.Printf("\nbatch %s: %d events, %d payouts totalling %v\n", batch.BatchId, len(batch.EventIds), len(batch.Instructions), batch.Total())
	for _, p := range batch.Instructions {
		fmt.Printf("  pay %s %v\n", p.MerchantId, p.Amount)
	}
	for _, m := range batch.CarriedMerchants() {
		fmt.Printf("  carry %s %v (merchant owes the platform)\n", m, batch.CarriedForward[m])
	}
	fmt.Printf("  c5 at 23:30 settled? %v (after cutoff: next batch)\n", settler.IsSettled("c5"))
	fmt.Println("acquirer owes for this batch:", batch.ProcessorFunding)

	fmt.Println("\n== Settlement file ==")
	file := SettlementFile.Render(batch)
	fmt.Print(file)
	fmt.Println("receiver validation:", SettlementFile.Validate(file))

	need(0, platform.ReceiveFunding(batch.BatchId, batch.ProcessorFunding))
	need(0, settler.ConfirmPayouts(batch))
	fmt.Println("\n== Ledger after funding and payout ==")
	for _, b := range platform.Ledger.BalancesView() {
		fmt.Printf("  %-28s %v\n", b.Account, OfMinor(b.Minor, USD))
	}
	fmt.Printf("trial balance: %d   ties out: %v\n", platform.Ledger.TrialBalance(), settler.TiesOut())
	again := need(settler.Run(Day, Cutoff))
	fmt.Printf("rerun of %s returns the same batch: %v, journals still %d\n", again.BatchId, again == batch, platform.Ledger.JournalCount())

	fmt.Println("\n== Batch vs real time over the same events ==")
	printComparison(need(RunLiquidityComparison(DemoMerchants(), DemoDay(), Day, Cutoff, USD)))

	fmt.Println("\n== 200 merchants, 50,000 events, seed 7 ==")
	many := GeneratedMerchants(200)
	big := GeneratedDay(many, 50_000, 7)
	bigPlatform := NewPlatform(USD, StandardFees(USD))
	for _, m := range many {
		bigPlatform.AddMerchant(m)
	}
	for _, e := range big {
		need(bigPlatform.Book(e))
	}
	bigSettler := NewBatchSettler(bigPlatform, usd("1.00"))
	midnight := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	bigBatch := need(bigSettler.Run(Day, midnight))
	fmt.Printf("payouts %d, total %v, carried %d, ties out %v, file valid %v\n", len(bigBatch.Instructions), bigBatch.Total(),
		len(bigBatch.CarriedForward), bigSettler.TiesOut(), SettlementFile.Validate(SettlementFile.Render(bigBatch)))
	printComparison(need(RunLiquidityComparison(many, big, Day, midnight, USD)))
}
