// Lesson: phases/12-payment-systems-design/08-payouts-and-balance-management/docs/en.md
// Payouts, Balances, and Negative Balance Handling
// Run:  go run .
// Test: go test ./...
// Sources: Nacha Operating Rules (batch/entry/control records, return reason codes R01-R03);
// Visa and Mastercard settlement (clearing vs settlement). Go 1.22+, stdlib only. Same design as the Java version.
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

// ---------------------------------------------------------------------------------------------
// Minimal double-entry ledger (Phase 10, Lesson 03). Each merchant has four sub-accounts.
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
	seq      int
}

func NewLedger(currency Currency) *Ledger {
	return &Ledger{accounts: map[string]AccountType{}, balances: map[string]int64{}, currency: currency}
}

func (l *Ledger) Open(account string, t AccountType) error {
	if _, exists := l.accounts[account]; exists {
		return fmt.Errorf("%w: account exists: %s", ErrInvalidArgument, account)
	}
	l.order = append(l.order, account)
	l.accounts[account] = t
	l.balances[account] = 0
	return nil
}

// Post one balanced entry; every posting amount is positive (a zero line is refused by NewJournalEntry).
func (l *Ledger) Post(date time.Time, description string, postings []Posting) error {
	for _, p := range postings {
		if _, ok := l.accounts[p.Account]; !ok {
			return fmt.Errorf("%w: unknown account %s", ErrInvalidArgument, p.Account)
		}
	}
	e, err := NewJournalEntry(fmt.Sprintf("je_%04d", l.seq+1), date, description, postings)
	if err != nil {
		return err
	}
	for _, p := range e.Postings {
		if _, err := addChecked(l.balances[p.Account], l.signed(p)); err != nil {
			return err
		}
	}
	l.seq++
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

func (l *Ledger) Balance(account string) (Money, error) {
	b, ok := l.balances[account]
	if !ok {
		return Money{}, fmt.Errorf("%w: unknown account %s", ErrInvalidArgument, account)
	}
	return OfMinor(b, l.currency), nil
}

// mustBalance is for accounts the service itself opened; an unknown account there is a bug.
func (l *Ledger) mustBalance(account string) Money {
	b, err := l.Balance(account)
	if err != nil {
		panic(err)
	}
	return b
}

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
// Merchants, captures, settlement reports, reserves, payouts.
// ---------------------------------------------------------------------------------------------

type PayoutFrequency int

const (
	DAILY PayoutFrequency = iota
	WEEKLY
)

// PayoutSchedule: when a merchant is paid: every business day, or one weekday; never less than the minimum.
type PayoutSchedule struct {
	Frequency     PayoutFrequency
	WeeklyDay     time.Weekday // ignored for DAILY
	MinimumPayout Money
}

func (s PayoutSchedule) DueOn(d time.Time) bool {
	wd := d.Weekday()
	businessDay := wd != time.Saturday && wd != time.Sunday
	return businessDay && (s.Frequency == DAILY || wd == s.WeeklyDay)
}

// ReservePolicy: rolling reserve: a share of each settlement is held back for a number of days before it becomes available.
type ReservePolicy struct {
	BasisPoints int64
	HoldDays    int
}

func NewReservePolicy(basisPoints int64, holdDays int) (ReservePolicy, error) {
	if basisPoints < 0 || basisPoints > 10_000 {
		return ReservePolicy{}, fmt.Errorf("%w: reserve bps must be 0..10000", ErrInvalidArgument)
	}
	if holdDays < 0 {
		return ReservePolicy{}, fmt.Errorf("%w: holdDays must be >= 0", ErrInvalidArgument)
	}
	return ReservePolicy{basisPoints, holdDays}, nil
}

type Merchant struct {
	ID               string
	BankAccountToken string
	Schedule         PayoutSchedule
	Reserve          ReservePolicy
}

// CaptureRecord: a capture's net is owed to the merchant, but only becomes payable once the scheme has actually paid us for it.
type CaptureRecord struct {
	ID         string
	MerchantID string
	Gross      Money
	Fee        Money
	CapturedOn time.Time
}

func (c CaptureRecord) Net() Money { return mustMinus(c.Gross, c.Fee) }

// SettlementReport is the scheme's statement that it paid us for these captures. The authority on "settled".
type SettlementReport struct {
	ID         string
	ValueDate  time.Time
	CaptureIDs []string
	TotalGross Money
}

type ReserveTranche struct {
	MerchantID string
	Amount     Money
	ReleaseOn  time.Time
}

func (t ReserveTranche) equal(o ReserveTranche) bool {
	return t.MerchantID == o.MerchantID && t.Amount == o.Amount && t.ReleaseOn.Equal(o.ReleaseOn)
}

type PayoutDirection int

const (
	PAYOUT_CREDIT PayoutDirection = iota
	PAYOUT_DEBIT
)

func (d PayoutDirection) String() string { return [...]string{"CREDIT", "DEBIT"}[d] }

type PayoutState int

const (
	IN_TRANSIT PayoutState = iota
	PAID
	RETURNED
)

func (s PayoutState) String() string { return [...]string{"IN_TRANSIT", "PAID", "RETURNED"}[s] }

type Payout struct {
	ID          string
	MerchantID  string
	Amount      Money
	Direction   PayoutDirection
	InitiatedOn time.Time
	State       PayoutState
	ReturnCode  string // "" until returned (Java null)
}

func (p *Payout) String() string {
	s := fmt.Sprintf("%s %v %s %v %v", p.ID, p.Direction, p.MerchantID, p.Amount, p.State)
	if p.ReturnCode != "" {
		s += " " + p.ReturnCode
	}
	return s
}

// PayoutFileSink is the hook to the bank: a payout file per run. The real one writes NACHA or ISO 20022 pain.001.
type PayoutFileSink interface {
	Write(fileName string, lines []string)
}

type InMemoryPayoutFileSink struct {
	Names []string
	Files map[string][]string
}

func NewInMemoryPayoutFileSink() *InMemoryPayoutFileSink {
	return &InMemoryPayoutFileSink{Files: map[string][]string{}}
}

func (s *InMemoryPayoutFileSink) Write(fileName string, lines []string) {
	if _, exists := s.Files[fileName]; !exists {
		s.Names = append(s.Names, fileName)
	}
	s.Files[fileName] = append([]string(nil), lines...)
}

// Last returns the most recently written file's lines.
func (s *InMemoryPayoutFileSink) Last() []string { return s.Files[s.Names[len(s.Names)-1]] }

type Balance struct {
	Pending   Money
	Available Money
	Reserve   Money
	InTransit Money
}

// Total is everything we owe the merchant, in any stage.
func (b Balance) Total() Money {
	return mustPlus(mustPlus(mustPlus(b.Pending, b.Available), b.Reserve), b.InTransit)
}

const (
	SCHEME      = "scheme_receivable"      // asset: captured, not yet paid to us by the scheme
	BANK        = "bank_cash"              // asset: our operating bank account
	COLLECTIONS = "collections_receivable" // asset: debits we have asked merchants' banks for
	FEES        = "fee_revenue"
)

type BalanceService struct {
	ledger                   *Ledger
	clock                    Clock
	sink                     PayoutFileSink
	collectAfterNegativeDays int
	currency                 Currency
	merchantOrder            []string
	merchants                map[string]Merchant
	captures                 map[string]CaptureRecord
	settledBy                map[string]string
	tranches                 []ReserveTranche
	payouts                  map[string]*Payout
	negativeSince            map[string]time.Time
	paused                   map[string]string
	payoutSeq                int
	fileSeq                  int
}

func NewBalanceService(ledger *Ledger, clock Clock, sink PayoutFileSink, collectAfterNegativeDays int, currency Currency) *BalanceService {
	s := &BalanceService{ledger: ledger, clock: clock, sink: sink, collectAfterNegativeDays: collectAfterNegativeDays, currency: currency,
		merchants: map[string]Merchant{}, captures: map[string]CaptureRecord{}, settledBy: map[string]string{},
		payouts: map[string]*Payout{}, negativeSince: map[string]time.Time{}, paused: map[string]string{}}
	for _, a := range []struct {
		name string
		t    AccountType
	}{{SCHEME, ASSET}, {BANK, ASSET}, {COLLECTIONS, ASSET}, {FEES, REVENUE}} {
		if err := ledger.Open(a.name, a.t); err != nil {
			panic(err)
		}
	}
	return s
}

func PendingAcct(m string) string   { return m + ":pending" }
func AvailableAcct(m string) string { return m + ":available" }
func ReserveAcct(m string) string   { return m + ":reserve" }
func InTransitAcct(m string) string { return m + ":in_transit" }

func (s *BalanceService) Ledger() *Ledger { return s.ledger }

func (s *BalanceService) Onboard(m Merchant) error {
	if _, exists := s.merchants[m.ID]; exists {
		return fmt.Errorf("%w: merchant exists: %s", ErrInvalidArgument, m.ID)
	}
	for _, acct := range []string{PendingAcct(m.ID), AvailableAcct(m.ID), ReserveAcct(m.ID), InTransitAcct(m.ID)} {
		if err := s.ledger.Open(acct, LIABILITY); err != nil {
			return err
		}
	}
	s.merchants[m.ID] = m
	s.merchantOrder = append(s.merchantOrder, m.ID)
	return nil
}

func (s *BalanceService) Merchant(id string) (Merchant, error) {
	m, ok := s.merchants[id]
	if !ok {
		return Merchant{}, fmt.Errorf("%w: unknown merchant %s", ErrInvalidArgument, id)
	}
	return m, nil
}

func (s *BalanceService) Balance(merchantID string) (Balance, error) {
	if _, err := s.Merchant(merchantID); err != nil {
		return Balance{}, err
	}
	return Balance{s.ledger.mustBalance(PendingAcct(merchantID)), s.ledger.mustBalance(AvailableAcct(merchantID)),
		s.ledger.mustBalance(ReserveAcct(merchantID)), s.ledger.mustBalance(InTransitAcct(merchantID))}, nil
}

func (s *BalanceService) available(merchantID string) Money {
	return s.ledger.mustBalance(AvailableAcct(merchantID))
}

// Capture: the scheme owes us the gross; the merchant is owed the net, but it is PENDING until settlement.
func (s *BalanceService) Capture(captureID, merchantID string, gross Money, feeBps, fixedFeeMinor int64) (CaptureRecord, error) {
	if _, err := s.Merchant(merchantID); err != nil {
		return CaptureRecord{}, err
	}
	if _, exists := s.captures[captureID]; exists {
		return CaptureRecord{}, fmt.Errorf("%w: capture exists: %s", ErrIllegalState, captureID)
	}
	scaled, err := mulChecked(gross.MinorUnits, feeBps)
	if err != nil {
		return CaptureRecord{}, err
	}
	variable := floorDiv(scaled+5_000, 10_000)
	c := CaptureRecord{captureID, merchantID, gross, OfMinor(variable+fixedFeeMinor, s.currency), s.clock.Today()}
	postings := []Posting{Debit(SCHEME, gross), Credit(PendingAcct(merchantID), c.Net())}
	if c.Fee.IsPositive() {
		postings = append(postings, Credit(FEES, c.Fee))
	}
	if err := s.ledger.Post(s.clock.Today(), "capture "+captureID, postings); err != nil {
		return CaptureRecord{}, err
	}
	s.captures[captureID] = c
	return c, nil
}

// floorDiv is Java's Math.floorDiv: rounds toward negative infinity, unlike Go's / which truncates.
func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func (s *BalanceService) IsSettled(captureID string) bool {
	_, ok := s.settledBy[captureID]
	return ok
}

// Settle applies a settlement report. It must reconcile first: every capture known and unsettled, and the report's
// total equal to the sum of their gross amounts. Only then does cash arrive and pending become available,
// minus the merchant's rolling reserve, which is held as a tranche with its own release date.
func (s *BalanceService) Settle(report SettlementReport) error {
	sum := Zero(s.currency)
	for _, id := range report.CaptureIDs {
		c, ok := s.captures[id]
		if !ok {
			return fmt.Errorf("%w: report %s names unknown capture %s", ErrIllegalState, report.ID, id)
		}
		if s.IsSettled(id) {
			return fmt.Errorf("%w: report %s: %s already settled by %s", ErrIllegalState, report.ID, id, s.settledBy[id])
		}
		sum = mustPlus(sum, c.Gross)
	}
	if sum != report.TotalGross {
		return fmt.Errorf("%w: report %s does not reconcile: total %v but captures sum to %v", ErrIllegalState, report.ID, report.TotalGross, sum)
	}
	postings := []Posting{Debit(BANK, sum), Credit(SCHEME, sum)}
	var merchantOrder []string
	netByMerchant := map[string]Money{}
	for _, id := range report.CaptureIDs {
		c := s.captures[id]
		if prev, seen := netByMerchant[c.MerchantID]; seen {
			netByMerchant[c.MerchantID] = mustPlus(prev, c.Net())
		} else {
			merchantOrder = append(merchantOrder, c.MerchantID)
			netByMerchant[c.MerchantID] = c.Net()
		}
	}
	var newTranches []ReserveTranche
	for _, mid := range merchantOrder {
		m := s.merchants[mid]
		net := netByMerchant[mid]
		scaled, err := mulChecked(net.MinorUnits, m.Reserve.BasisPoints)
		if err != nil {
			return err
		}
		reserve := OfMinor(floorDiv(scaled, 10_000), s.currency)
		postings = append(postings, Debit(PendingAcct(m.ID), net), Credit(AvailableAcct(m.ID), mustMinus(net, reserve)))
		if reserve.IsPositive() {
			postings = append(postings, Credit(ReserveAcct(m.ID), reserve))
			newTranches = append(newTranches, ReserveTranche{m.ID, reserve, report.ValueDate.AddDate(0, 0, m.Reserve.HoldDays)})
		}
	}
	if err := s.ledger.Post(s.clock.Today(), "settlement "+report.ID, postings); err != nil {
		return err
	}
	for _, id := range report.CaptureIDs {
		s.settledBy[id] = report.ID
	}
	s.tranches = append(s.tranches, newTranches...)
	return nil
}

// DebitMerchant: a refund, chargeback, or fee: it comes out of available, which may go negative. The scheme nets it from what it owes us.
func (s *BalanceService) DebitMerchant(merchantID string, amount Money, reason string) error {
	if _, err := s.Merchant(merchantID); err != nil {
		return err
	}
	if !amount.IsPositive() {
		return fmt.Errorf("%w: debit must be positive", ErrInvalidArgument)
	}
	if err := s.ledger.Post(s.clock.Today(), reason+" "+merchantID, []Posting{Debit(AvailableAcct(merchantID), amount), Credit(SCHEME, amount)}); err != nil {
		return err
	}
	s.trackNegative(merchantID)
	return nil
}

// ReleaseReserves releases every tranche due today or earlier into available.
func (s *BalanceService) ReleaseReserves() (Money, error) {
	released := Zero(s.currency)
	today := s.clock.Today()
	var kept []ReserveTranche
	for _, t := range s.tranches {
		if t.ReleaseOn.After(today) {
			kept = append(kept, t)
			continue
		}
		if err := s.ledger.Post(today, "reserve release "+t.MerchantID+" due "+fmtDate(t.ReleaseOn),
			[]Posting{Debit(ReserveAcct(t.MerchantID), t.Amount), Credit(AvailableAcct(t.MerchantID), t.Amount)}); err != nil {
			return released, err
		}
		released = mustPlus(released, t.Amount)
		s.trackNegative(t.MerchantID)
	}
	s.tranches = kept
	return released, nil
}

// CoverFromReserve: negative balance, step 1: use the merchant's own reserve, earliest-releasing tranches first,
// before asking anyone for money. That is what the reserve is for.
func (s *BalanceService) CoverFromReserve(merchantID string) (Money, error) {
	deficit := s.available(merchantID).Negate()
	used := Zero(s.currency)
	if !deficit.IsPositive() {
		return used, nil
	}
	var mine []ReserveTranche
	for _, t := range s.tranches {
		if t.MerchantID == merchantID {
			mine = append(mine, t)
		}
	}
	sort.SliceStable(mine, func(i, j int) bool { return mine[i].ReleaseOn.Before(mine[j].ReleaseOn) })
	for _, t := range mine {
		if !deficit.IsPositive() {
			break
		}
		take := t.Amount.Min(deficit)
		s.removeTranche(t)
		if take.Compare(t.Amount) < 0 {
			s.tranches = append(s.tranches, ReserveTranche{merchantID, mustMinus(t.Amount, take), t.ReleaseOn})
		}
		used = mustPlus(used, take)
		deficit = mustMinus(deficit, take)
	}
	if used.IsPositive() {
		if err := s.ledger.Post(s.clock.Today(), "reserve applied to negative balance "+merchantID,
			[]Posting{Debit(ReserveAcct(merchantID), used), Credit(AvailableAcct(merchantID), used)}); err != nil {
			return Zero(s.currency), err
		}
	}
	s.trackNegative(merchantID)
	return used, nil
}

// removeTranche drops the first tranche equal to t, as List.remove(Object) does for a record.
func (s *BalanceService) removeTranche(t ReserveTranche) {
	for i, x := range s.tranches {
		if x.equal(t) {
			s.tranches = append(s.tranches[:i], s.tranches[i+1:]...)
			return
		}
	}
}

func (s *BalanceService) trackNegative(merchantID string) {
	if s.available(merchantID).IsNegative() {
		if _, tracked := s.negativeSince[merchantID]; !tracked {
			s.negativeSince[merchantID] = s.clock.Today()
		}
	} else {
		delete(s.negativeSince, merchantID)
	}
}

// NegativeSince reports when the merchant's available balance went negative; ok is false when it is not negative.
func (s *BalanceService) NegativeSince(merchantID string) (time.Time, bool) {
	t, ok := s.negativeSince[merchantID]
	return t, ok
}

// ScheduledReserve is the sum of the merchant's unreleased tranches. Must always equal the reserve account's balance.
func (s *BalanceService) ScheduledReserve(merchantID string) Money {
	total := Zero(s.currency)
	for _, t := range s.tranches {
		if t.MerchantID == merchantID {
			total = mustPlus(total, t.Amount)
		}
	}
	return total
}

func (s *BalanceService) IsPaused(merchantID string) bool {
	_, paused := s.paused[merchantID]
	return paused
}

func (s *BalanceService) Resume(merchantID, why string) { delete(s.paused, merchantID) }

func (s *BalanceService) Payout(id string) (*Payout, error) {
	p, ok := s.payouts[id]
	if !ok {
		return nil, fmt.Errorf("%w: unknown payout %s", ErrInvalidArgument, id)
	}
	return p, nil
}

// RunPayouts is the daily payout run. For each merchant: cover a negative balance from reserve; if it has been
// negative for collectAfterNegativeDays, ask its bank for the deficit (a DEBIT line); otherwise, if its schedule
// is due, it is not paused, and available meets the minimum, pay out all of available (a CREDIT line). One file
// per run, with a trailer carrying the counts and totals the bank checks.
func (s *BalanceService) RunPayouts() ([]*Payout, error) {
	today := s.clock.Today()
	batch := []*Payout{}
	for _, mid := range s.merchantOrder {
		m := s.merchants[mid]
		if _, err := s.CoverFromReserve(m.ID); err != nil {
			return batch, err
		}
		available := s.available(m.ID)
		since, negative := s.negativeSince[m.ID]
		if available.IsNegative() && negative && !today.Before(since.AddDate(0, 0, s.collectAfterNegativeDays)) {
			s.payoutSeq++
			debit := &Payout{ID: fmt.Sprintf("co_%04d", s.payoutSeq), MerchantID: m.ID, Amount: available.Negate(), Direction: PAYOUT_DEBIT, InitiatedOn: today, State: IN_TRANSIT}
			if err := s.ledger.Post(today, "collection "+debit.ID, []Posting{Debit(COLLECTIONS, debit.Amount), Credit(AvailableAcct(m.ID), debit.Amount)}); err != nil {
				return batch, err
			}
			s.trackNegative(m.ID)
			s.payouts[debit.ID] = debit
			batch = append(batch, debit)
			continue
		}
		if !m.Schedule.DueOn(today) || s.IsPaused(m.ID) {
			continue
		}
		if available.Compare(m.Schedule.MinimumPayout) < 0 || !available.IsPositive() {
			continue
		}
		s.payoutSeq++
		credit := &Payout{ID: fmt.Sprintf("po_%04d", s.payoutSeq), MerchantID: m.ID, Amount: available, Direction: PAYOUT_CREDIT, InitiatedOn: today, State: IN_TRANSIT}
		if err := s.ledger.Post(today, "payout "+credit.ID, []Posting{Debit(AvailableAcct(m.ID), available), Credit(InTransitAcct(m.ID), available)}); err != nil {
			return batch, err
		}
		s.payouts[credit.ID] = credit
		batch = append(batch, credit)
	}
	if len(batch) > 0 {
		s.writeFile(today, batch)
	}
	return batch, nil
}

func (s *BalanceService) writeFile(today time.Time, batch []*Payout) {
	s.fileSeq++
	name := fmt.Sprintf("payouts_%s_%03d.csv", fmtDate(today), s.fileSeq)
	lines := []string{"H," + fmtDate(today) + "," + name + "," + s.currency.Code}
	var credits, debits int64
	for _, p := range batch {
		lines = append(lines, fmt.Sprintf("D,%s,%s,%s,%v,%d", p.ID, p.MerchantID, s.merchants[p.MerchantID].BankAccountToken, p.Direction, p.Amount.MinorUnits))
		if p.Direction == PAYOUT_CREDIT {
			credits += p.Amount.MinorUnits
		} else {
			debits += p.Amount.MinorUnits
		}
	}
	lines = append(lines, fmt.Sprintf("T,%d,%d,%d", len(batch), credits, debits))
	s.sink.Write(name, lines)
}

// Confirm: the bank confirms a line. A credit leaves our cash; a debit (collection) arrives in it.
func (s *BalanceService) Confirm(payoutID string) error {
	p, err := s.Payout(payoutID)
	if err != nil {
		return err
	}
	if p.State == PAID {
		return nil
	}
	if p.State != IN_TRANSIT {
		return fmt.Errorf("%w: %s is %v", ErrIllegalState, payoutID, p.State)
	}
	if p.Direction == PAYOUT_CREDIT {
		err = s.ledger.Post(s.clock.Today(), "payout paid "+p.ID, []Posting{Debit(InTransitAcct(p.MerchantID), p.Amount), Credit(BANK, p.Amount)})
	} else {
		err = s.ledger.Post(s.clock.Today(), "collection received "+p.ID, []Posting{Debit(BANK, p.Amount), Credit(COLLECTIONS, p.Amount)})
	}
	if err != nil {
		return err
	}
	p.State = PAID
	return nil
}

// Returned: the bank returns a line (NACHA return codes: R01 insufficient funds, R02 account closed, R03 no account).
// A returned credit goes back to available and payouts pause until the bank details are fixed; a returned
// debit puts the deficit back on the merchant's balance.
func (s *BalanceService) Returned(payoutID, returnCode string) error {
	p, err := s.Payout(payoutID)
	if err != nil {
		return err
	}
	if p.State != IN_TRANSIT {
		return fmt.Errorf("%w: %s is %v; cannot return", ErrIllegalState, payoutID, p.State)
	}
	if p.Direction == PAYOUT_CREDIT {
		if err := s.ledger.Post(s.clock.Today(), "payout returned "+p.ID+" "+returnCode,
			[]Posting{Debit(InTransitAcct(p.MerchantID), p.Amount), Credit(AvailableAcct(p.MerchantID), p.Amount)}); err != nil {
			return err
		}
		s.paused[p.MerchantID] = "payout " + p.ID + " returned " + returnCode
	} else {
		if err := s.ledger.Post(s.clock.Today(), "collection returned "+p.ID+" "+returnCode,
			[]Posting{Debit(AvailableAcct(p.MerchantID), p.Amount), Credit(COLLECTIONS, p.Amount)}); err != nil {
			return err
		}
		delete(s.negativeSince, p.MerchantID)
		s.trackNegative(p.MerchantID)
	}
	p.State = RETURNED
	p.ReturnCode = returnCode
	return nil
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

func orPanic(err error) {
	if err != nil {
		panic(err)
	}
}

func payoutList(ps []*Payout) string {
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = p.String()
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func show(svc *BalanceService, clock *FakeClock, m, label string) {
	b := must(svc.Balance(m))
	fmt.Printf("%s %-3s %-38s pending %-11s available %-12s reserve %-10s in transit %v\n",
		fmtDate(clock.Today()), strings.ToUpper(clock.Today().Weekday().String()[:3]), label, b.Pending, b.Available, b.Reserve, b.InTransit)
}

func main() {
	clock := NewFakeClock(Date(2026, time.March, 2)) // a Monday
	files := NewInMemoryPayoutFileSink()
	svc := NewBalanceService(NewLedger(USD), clock, files, 7, USD)
	orPanic(svc.Onboard(Merchant{"m_shoe", "ba_tok_7Q2", PayoutSchedule{DAILY, time.Sunday, usd(1_000)}, must(NewReservePolicy(1_000, 90))}))
	orPanic(svc.Onboard(Merchant{"m_art", "ba_tok_X91", PayoutSchedule{WEEKLY, time.Friday, usd(2_500)}, must(NewReservePolicy(0, 0))}))

	fmt.Println("== Captures are owed to the merchant but not payable: they are PENDING until the scheme pays us ==")
	must(svc.Capture("cap_1", "m_shoe", usd(10_000), 290, 30))
	must(svc.Capture("cap_2", "m_shoe", usd(25_000), 290, 30))
	must(svc.Capture("cap_3", "m_shoe", usd(5_000), 290, 30))
	must(svc.Capture("cap_4", "m_art", usd(18_000), 290, 30))
	show(svc, clock, "m_shoe", "after 3 captures (400.00 gross)")
	fmt.Println("payout run Monday -> " + payoutList(must(svc.RunPayouts())) + "  (nothing settled, nothing to pay)")
	clock.AdvanceDays(1)
	fmt.Println("payout run Tuesday -> " + payoutList(must(svc.RunPayouts())) + "  (the expected T+1 report did not arrive)")

	fmt.Println()
	fmt.Println("== Wednesday: the settlement report arrives and reconciles; 10% of m_shoe's net goes to a 90-day reserve ==")
	clock.AdvanceDays(1)
	if err := svc.Settle(SettlementReport{"st_bad", clock.Today(), []string{"cap_1", "cap_2", "cap_3"}, usd(39_999)}); errors.Is(err, ErrIllegalState) {
		fmt.Println("refused:", strings.TrimPrefix(err.Error(), ErrIllegalState.Error()+": "))
	}
	orPanic(svc.Settle(SettlementReport{"st_0302", clock.Today(), []string{"cap_1", "cap_2", "cap_3", "cap_4"}, usd(58_000)}))
	show(svc, clock, "m_shoe", "after settlement st_0302")
	wed := must(svc.RunPayouts())
	fmt.Println("payout run Wednesday -> " + payoutList(wed))
	show(svc, clock, "m_shoe", "after the payout run")
	for _, name := range files.Names {
		fmt.Println("file " + name)
		for _, l := range files.Files[name] {
			fmt.Println("   " + l)
		}
	}
	clock.AdvanceDays(1)
	orPanic(svc.Confirm(wed[0].ID))
	show(svc, clock, "m_shoe", "bank confirmed the payout")

	fmt.Println()
	fmt.Println("== Friday: m_art's weekly payout is returned R03 (no account); payouts pause ==")
	clock.AdvanceDays(1)
	fri := must(svc.RunPayouts())
	fmt.Println("payout run Friday -> " + payoutList(fri))
	orPanic(svc.Returned(fri[0].ID, "R03"))
	show(svc, clock, "m_art", "after the return")
	clock.AdvanceDays(7)
	fmt.Printf("next Friday run -> %s  (m_art paused: %t)\n", payoutList(must(svc.RunPayouts())), svc.IsPaused("m_art"))
	svc.Resume("m_art", "bank details re-verified")
	fmt.Println("after resume, Friday run would pay", must(svc.Balance("m_art")).Available)

	fmt.Println()
	fmt.Println("== A 600.00 chargeback drives m_shoe negative; the reserve covers what it can ==")
	clock.AdvanceDays(3) // Monday 2026-03-16
	orPanic(svc.DebitMerchant("m_shoe", usd(60_000), "chargeback dp_9"))
	show(svc, clock, "m_shoe", "after the chargeback")
	must(svc.RunPayouts())
	show(svc, clock, "m_shoe", "reserve applied, still negative")
	since, _ := svc.NegativeSince("m_shoe")
	fmt.Println("negative since", fmtDate(since))

	fmt.Println()
	fmt.Println("== New settlements net against the deficit; after 7 negative days the rest is collected ==")
	must(svc.Capture("cap_5", "m_shoe", usd(8_000), 290, 30))
	clock.AdvanceDays(2)
	orPanic(svc.Settle(SettlementReport{"st_0316", clock.Today(), []string{"cap_5"}, usd(8_000)}))
	must(svc.RunPayouts())
	show(svc, clock, "m_shoe", "netted by st_0316, reserve applied")
	clock.AdvanceDays(5) // Monday 2026-03-23, 7 days negative
	collect := must(svc.RunPayouts())
	fmt.Println("payout run -> " + payoutList(collect))
	show(svc, clock, "m_shoe", "collection initiated")
	clock.AdvanceDays(2)
	orPanic(svc.Confirm(collect[0].ID))
	show(svc, clock, "m_shoe", "collection received")

	fmt.Println()
	l := svc.Ledger()
	fmt.Println("== Ledger ==")
	for _, a := range []string{SCHEME, BANK, COLLECTIONS, FEES} {
		fmt.Printf("   %-24s %12s\n", a, must(l.Balance(a)))
	}
	for _, m := range []string{"m_shoe", "m_art"} {
		fmt.Printf("   %-24s %12s owed in total\n", m, must(svc.Balance(m)).Total())
	}
	fmt.Printf("   ledger balances: %t, journal entries: %d\n", l.Balances(), len(l.Journal()))
}
