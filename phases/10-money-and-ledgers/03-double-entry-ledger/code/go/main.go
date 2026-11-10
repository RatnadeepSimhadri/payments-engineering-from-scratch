// Lesson: phases/10-money-and-ledgers/03-double-entry-ledger/docs/en.md
// The Double-Entry Ledger from Scratch
// Run:  go run .
// Test: go test ./...
// Sources: Pacioli, Summa de arithmetica (1494) for the debit=credit rule; ISO 4217 for currency exponents.
// Money type copied from Phase 10, Lesson 01. Go 1.22+, standard library only. Same design as Java and Python.
package main

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strings"
)

// ---------------------------------------------------------------------------------------------
// Money (copied from Phase 10, Lesson 01). Integer minor units + ISO 4217 currency. Never a float.
// ---------------------------------------------------------------------------------------------

type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	EUR = Currency{"EUR", 2}
	JPY = Currency{"JPY", 0}
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

var ErrCurrencyMismatch = errors.New("currency mismatch")
var ErrOverflow = errors.New("int64 overflow")

type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }
func Zero(currency Currency) Money                      { return Money{0, currency} }

func Parse(text string, currency Currency) (Money, error) {
	s := strings.TrimSpace(text)
	negative := strings.HasPrefix(s, "-")
	if negative || strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	if s == "" {
		return Money{}, errors.New("empty amount")
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	if !allDigits(whole) || (frac != "" && !allDigits(frac)) {
		return Money{}, fmt.Errorf("not a decimal amount: %q", text)
	}
	if len(frac) > currency.Exponent {
		return Money{}, fmt.Errorf("%s has more decimals than %s allows (%d)", text, currency.Code, currency.Exponent)
	}
	frac += strings.Repeat("0", currency.Exponent-len(frac))
	var minor int64
	for _, ch := range whole + frac {
		next := minor*10 + int64(ch-'0')
		if next < minor {
			return Money{}, ErrOverflow
		}
		minor = next
	}
	if negative {
		minor = -minor
	}
	return Money{minor, currency}, nil
}

func allDigits(s string) bool {
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return len(s) > 0
}

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

func mulChecked(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	product := a * b
	if product/b != a || (a == -1 && b == math.MinInt64) || (b == -1 && a == math.MinInt64) {
		return 0, ErrOverflow
	}
	return product, nil
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
func (m Money) IsZero() bool     { return m.MinorUnits == 0 }
func (m Money) IsNegative() bool { return m.MinorUnits < 0 }
func (m Money) IsPositive() bool { return m.MinorUnits > 0 }

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
	twice := abs64(remainder) * 2
	absDivisor := abs64(divisor)
	switch rounding {
	case Down:
		return quotient
	case Up:
		return quotient + sign
	case HalfUp:
		if twice >= absDivisor {
			return quotient + sign
		}
		return quotient
	default:
		if twice > absDivisor {
			return quotient + sign
		}
		if twice < absDivisor {
			return quotient
		}
		if quotient%2 == 0 {
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

func (m Money) Times(numerator, denominator int64, rounding Rounding) (Money, error) {
	if denominator <= 0 {
		return Money{}, errors.New("denominator must be positive")
	}
	product, err := mulChecked(m.MinorUnits, numerator)
	if err != nil {
		return Money{}, err
	}
	return Money{DivideRounded(product, denominator, rounding), m.Currency}, nil
}

func (m Money) String() string {
	magnitude := abs64(m.MinorUnits)
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

func must(m Money, err error) Money {
	if err != nil {
		panic(err)
	}
	return m
}

// ---------------------------------------------------------------------------------------------
// The ledger.
// ---------------------------------------------------------------------------------------------

// Side is which side of an entry a posting sits on. A posting is never signed: positive amount, one side.
type Side int

const (
	Debit Side = iota
	Credit
)

func (s Side) Opposite() Side {
	if s == Debit {
		return Credit
	}
	return Debit
}

func (s Side) String() string {
	if s == Debit {
		return "DEBIT"
	}
	return "CREDIT"
}

// AccountType is one of the five account types.
type AccountType int

const (
	Asset AccountType = iota
	Expense
	Liability
	Equity
	Revenue
)

// NormalBalance is the side that increases this account: debit for assets and expenses, credit otherwise.
func (t AccountType) NormalBalance() Side {
	if t == Asset || t == Expense {
		return Debit
	}
	return Credit
}

func (t AccountType) String() string {
	return [...]string{"ASSET", "EXPENSE", "LIABILITY", "EQUITY", "REVENUE"}[t]
}

// Sentinel errors: every rejection the ledger can produce supports errors.Is.
var (
	ErrUnbalanced          = errors.New("unbalanced entry")
	ErrInvalidPosting      = errors.New("invalid posting")
	ErrUnknownAccount      = errors.New("unknown account")
	ErrUnknownEntry        = errors.New("unknown entry")
	ErrIdempotencyConflict = errors.New("entry id already posted with different content")
	ErrAlreadyReversed     = errors.New("entry already reversed")
)

type Account struct {
	ID       string
	Name     string
	Type     AccountType
	Currency Currency
}

// Posting is one line of a journal entry: a positive amount of one currency on one side of one account.
type Posting struct {
	AccountID string
	Side      Side
	Amount    Money
}

func NewPosting(accountID string, side Side, amount Money) (Posting, error) {
	if !amount.IsPositive() {
		return Posting{}, fmt.Errorf("%w: posting amount must be positive, got %v", ErrInvalidPosting, amount)
	}
	return Posting{accountID, side, amount}, nil
}

// Dr and Cr build postings; they panic on a non-positive amount because the callers in this lesson
// hold literals. Production code goes through NewPosting.
func Dr(accountID string, amount Money) Posting {
	return mustPosting(NewPosting(accountID, Debit, amount))
}
func Cr(accountID string, amount Money) Posting {
	return mustPosting(NewPosting(accountID, Credit, amount))
}

func mustPosting(p Posting, err error) Posting {
	if err != nil {
		panic(err)
	}
	return p
}

func (p Posting) Flipped() Posting { return Posting{p.AccountID, p.Side.Opposite(), p.Amount} }

func (p Posting) SignedMinorUnits() int64 {
	if p.Side == Debit {
		return p.Amount.MinorUnits
	}
	return -p.Amount.MinorUnits
}

func (p Posting) String() string {
	if p.Side == Debit {
		return fmt.Sprintf("  Dr %s  %v", p.AccountID, p.Amount)
	}
	return fmt.Sprintf("      Cr %s  %v", p.AccountID, p.Amount)
}

// JournalEntry is an immutable, balanced set of postings. Only NewJournalEntry builds one, and it
// refuses anything whose debits do not equal its credits in every currency.
type JournalEntry struct {
	ID          string
	Description string
	postings    []Posting
	ReversalOf  string
}

func NewJournalEntry(id, description string, postings []Posting) (JournalEntry, error) {
	return newEntry(id, description, postings, "")
}

func newEntry(id, description string, postings []Posting, reversalOf string) (JournalEntry, error) {
	if strings.TrimSpace(id) == "" {
		return JournalEntry{}, errors.New("entry id required")
	}
	if len(postings) < 2 {
		return JournalEntry{}, fmt.Errorf("%w: entry %s needs at least two postings", ErrUnbalanced, id)
	}
	for _, p := range postings {
		if !p.Amount.IsPositive() {
			return JournalEntry{}, fmt.Errorf("%w: posting amount must be positive, got %v", ErrInvalidPosting, p.Amount)
		}
	}
	net := map[Currency]int64{}
	var order []Currency
	for _, p := range postings {
		if _, seen := net[p.Amount.Currency]; !seen {
			order = append(order, p.Amount.Currency)
		}
		sum, err := addChecked(net[p.Amount.Currency], p.SignedMinorUnits())
		if err != nil {
			return JournalEntry{}, err
		}
		net[p.Amount.Currency] = sum
	}
	var problems []string
	for _, c := range order {
		if net[c] != 0 {
			problems = append(problems, fmt.Sprintf("debits minus credits in %s = %v", c.Code, OfMinor(net[c], c)))
		}
	}
	if len(problems) > 0 {
		return JournalEntry{}, fmt.Errorf("%w: entry %s rejected: %s (must be zero per currency)", ErrUnbalanced, id, strings.Join(problems, "; "))
	}
	copied := make([]Posting, len(postings))
	copy(copied, postings)
	return JournalEntry{id, description, copied, reversalOf}, nil
}

// Postings returns a copy; the entry's own slice is never handed out.
func (e JournalEntry) Postings() []Posting {
	out := make([]Posting, len(e.postings))
	copy(out, e.postings)
	return out
}

// Reversal is the entry that undoes this one: same postings with every side flipped. Nothing is edited.
func (e JournalEntry) Reversal(newID, description string) (JournalEntry, error) {
	flipped := make([]Posting, len(e.postings))
	for i, p := range e.postings {
		flipped[i] = p.Flipped()
	}
	return newEntry(newID, description, flipped, e.ID)
}

func (e JournalEntry) TotalDebits(currency Currency) Money {
	acc := Zero(currency)
	for _, p := range e.postings {
		if p.Side == Debit && p.Amount.Currency == currency {
			acc = must(acc.Plus(p.Amount))
		}
	}
	return acc
}

// Equal is structural equality, used for idempotent replay detection.
func (e JournalEntry) Equal(other JournalEntry) bool {
	if e.ID != other.ID || e.Description != other.Description || e.ReversalOf != other.ReversalOf || len(e.postings) != len(other.postings) {
		return false
	}
	for i := range e.postings {
		if e.postings[i] != other.postings[i] {
			return false
		}
	}
	return true
}

type TrialBalanceRow struct {
	Account Account
	Debit   Money
	Credit  Money
}

// TrialBalance is the trial balance for one currency. Its column totals are equal or the ledger is corrupt.
type TrialBalance struct {
	Currency     Currency
	Rows         []TrialBalanceRow
	TotalDebits  Money
	TotalCredits Money
}

func (tb TrialBalance) Balances() bool { return tb.TotalDebits == tb.TotalCredits }

// Ledger is an append-only journal plus a chart of accounts. Balances are derived from postings and
// cached as two running totals per account, which the tests prove equal a fresh recomputation.
type Ledger struct {
	accounts     map[string]Account
	accountOrder []string
	journal      []JournalEntry
	indexByID    map[string]int
	reversedBy   map[string]string
	debitTotals  map[string]int64
	creditTotals map[string]int64
}

func NewLedger() *Ledger {
	return &Ledger{
		accounts:     map[string]Account{},
		indexByID:    map[string]int{},
		reversedBy:   map[string]string{},
		debitTotals:  map[string]int64{},
		creditTotals: map[string]int64{},
	}
}

func (l *Ledger) OpenAccount(id, name string, t AccountType, currency Currency) (Account, error) {
	if strings.TrimSpace(id) == "" {
		return Account{}, errors.New("account id required")
	}
	if _, exists := l.accounts[id]; exists {
		return Account{}, fmt.Errorf("account exists: %s", id)
	}
	a := Account{id, name, t, currency}
	l.accounts[id] = a
	l.accountOrder = append(l.accountOrder, id)
	l.debitTotals[id] = 0
	l.creditTotals[id] = 0
	return a, nil
}

func (l *Ledger) Account(id string) (Account, error) {
	a, ok := l.accounts[id]
	if !ok {
		return Account{}, fmt.Errorf("%w: %s", ErrUnknownAccount, id)
	}
	return a, nil
}

// Post appends an entry. It returns true if appended, false if an identical entry with the same id is
// already in the journal (idempotent replay). A different entry under the same id is a conflict.
// All validation runs before the first mutation, so a rejected entry leaves no trace.
func (l *Ledger) Post(entry JournalEntry) (bool, error) {
	if existing, ok := l.indexByID[entry.ID]; ok {
		if l.journal[existing].Equal(entry) {
			return false, nil
		}
		return false, fmt.Errorf("%w: %s", ErrIdempotencyConflict, entry.ID)
	}
	for _, p := range entry.postings {
		a, err := l.Account(p.AccountID)
		if err != nil {
			return false, err
		}
		if a.Currency != p.Amount.Currency {
			return false, fmt.Errorf("%w: entry %s rejected: account %s is %s but posting is %s",
				ErrInvalidPosting, entry.ID, a.ID, a.Currency.Code, p.Amount.Currency.Code)
		}
	}
	if entry.ReversalOf != "" {
		if _, ok := l.indexByID[entry.ReversalOf]; !ok {
			return false, fmt.Errorf("%w: %s", ErrUnknownEntry, entry.ReversalOf)
		}
		if _, done := l.reversedBy[entry.ReversalOf]; done {
			return false, fmt.Errorf("%w: %s", ErrAlreadyReversed, entry.ReversalOf)
		}
	}
	// Validation complete: now mutate, and mutate everything.
	l.journal = append(l.journal, entry)
	l.indexByID[entry.ID] = len(l.journal) - 1
	if entry.ReversalOf != "" {
		l.reversedBy[entry.ReversalOf] = entry.ID
	}
	for _, p := range entry.postings {
		if p.Side == Debit {
			l.debitTotals[p.AccountID] += p.Amount.MinorUnits
		} else {
			l.creditTotals[p.AccountID] += p.Amount.MinorUnits
		}
	}
	return true, nil
}

// Reverse posts the reversal of an existing entry. The original stays in the journal forever.
func (l *Ledger) Reverse(entryID, newEntryID, description string) (JournalEntry, error) {
	original, err := l.Entry(entryID)
	if err != nil {
		return JournalEntry{}, err
	}
	reversal, err := original.Reversal(newEntryID, description)
	if err != nil {
		return JournalEntry{}, err
	}
	if _, err := l.Post(reversal); err != nil {
		return JournalEntry{}, err
	}
	return reversal, nil
}

func (l *Ledger) Entry(id string) (JournalEntry, error) {
	i, ok := l.indexByID[id]
	if !ok {
		return JournalEntry{}, fmt.Errorf("%w: %s", ErrUnknownEntry, id)
	}
	return l.journal[i], nil
}

// Entries returns a copy of the journal in posting order.
func (l *Ledger) Entries() []JournalEntry {
	out := make([]JournalEntry, len(l.journal))
	copy(out, l.journal)
	return out
}

func (l *Ledger) Size() int { return len(l.journal) }

// Balance is the balance in the account's normal direction: positive means what you expect for that type.
func (l *Ledger) Balance(accountID string) (Money, error) {
	a, err := l.Account(accountID)
	if err != nil {
		return Money{}, err
	}
	debits, credits := l.debitTotals[accountID], l.creditTotals[accountID]
	if a.Type.NormalBalance() == Debit {
		return OfMinor(debits-credits, a.Currency), nil
	}
	return OfMinor(credits-debits, a.Currency), nil
}

// NetDebit is debits minus credits regardless of type. It sums to zero across all accounts of a currency.
func (l *Ledger) NetDebit(accountID string) (Money, error) {
	a, err := l.Account(accountID)
	if err != nil {
		return Money{}, err
	}
	return OfMinor(l.debitTotals[accountID]-l.creditTotals[accountID], a.Currency), nil
}

// NetDebitFromPostings recomputes by scanning every posting. The cache must always agree with this.
func (l *Ledger) NetDebitFromPostings(accountID string) (Money, error) {
	a, err := l.Account(accountID)
	if err != nil {
		return Money{}, err
	}
	var total int64
	for _, e := range l.journal {
		for _, p := range e.postings {
			if p.AccountID == accountID {
				total += p.SignedMinorUnits()
			}
		}
	}
	return OfMinor(total, a.Currency), nil
}

func (l *Ledger) TrialBalance(currency Currency) TrialBalance {
	tb := TrialBalance{Currency: currency, TotalDebits: Zero(currency), TotalCredits: Zero(currency)}
	for _, id := range l.accountOrder {
		a := l.accounts[id]
		if a.Currency != currency {
			continue
		}
		net, _ := l.NetDebit(id)
		debit, credit := net, Zero(currency)
		if net.IsNegative() {
			debit, credit = Zero(currency), net.Negate()
		}
		tb.Rows = append(tb.Rows, TrialBalanceRow{a, debit, credit})
		tb.TotalDebits = must(tb.TotalDebits.Plus(debit))
		tb.TotalCredits = must(tb.TotalCredits.Plus(credit))
	}
	return tb
}

// RandomEntries is a deterministic generator of balanced entries for stress tests: seeded, never the clock.
type RandomEntries struct {
	random     *rand.Rand
	accountIDs []string
	currency   Currency
	counter    int
}

func NewRandomEntries(seed int64, accountIDs []string, currency Currency) *RandomEntries {
	return &RandomEntries{rand.New(rand.NewSource(seed)), accountIDs, currency, 0}
}

func (g *RandomEntries) Next() JournalEntry {
	legs := 2 + g.random.Intn(3) // 2..4 postings
	var postings []Posting
	var debitSum int64
	for i := 0; i < legs-1; i++ {
		amount := int64(1 + g.random.Intn(100_000)) // 0.01 .. 1000.00
		postings = append(postings, Dr(g.accountIDs[g.random.Intn(len(g.accountIDs))], OfMinor(amount, g.currency)))
		debitSum += amount
	}
	postings = append(postings, Cr(g.accountIDs[g.random.Intn(len(g.accountIDs))], OfMinor(debitSum, g.currency)))
	entry, err := NewJournalEntry(fmt.Sprintf("rnd-%d", g.counter), "random balanced entry", postings)
	if err != nil {
		panic(err)
	}
	g.counter++
	return entry
}

func printEntry(e JournalEntry) {
	suffix := ""
	if e.ReversalOf != "" {
		suffix = "  (reverses " + e.ReversalOf + ")"
	}
	fmt.Printf("%s  %s%s\n", e.ID, e.Description, suffix)
	for _, p := range e.postings {
		fmt.Println(p)
	}
}

func printTrialBalance(l *Ledger, currency Currency) {
	tb := l.TrialBalance(currency)
	fmt.Printf("%-28s %14s %14s\n", "account", "debit", "credit")
	for _, row := range tb.Rows {
		debit, credit := "", ""
		if !row.Debit.IsZero() {
			debit = row.Debit.String()
		}
		if !row.Credit.IsZero() {
			credit = row.Credit.String()
		}
		fmt.Printf("%-28s %14s %14s\n", row.Account.ID+" "+row.Account.Name, debit, credit)
	}
	fmt.Printf("%-28s %14s %14s   balances=%v\n", "TOTAL", tb.TotalDebits, tb.TotalCredits, tb.Balances())
}

func mustEntry(e JournalEntry, err error) JournalEntry {
	if err != nil {
		panic(err)
	}
	return e
}

func main() {
	ledger := NewLedger()
	ledger.OpenAccount("1010", "Settlement Bank", Asset, USD)
	ledger.OpenAccount("1200", "Processor Receivable", Asset, USD)
	ledger.OpenAccount("2100", "Merchant Payable", Liability, USD)
	ledger.OpenAccount("4100", "Fee Revenue", Revenue, USD)

	fmt.Println("== A customer pays a merchant 100.00 USD; we keep a 2.9% fee ==")
	gross := must(Parse("100.00", USD))
	fee := must(gross.Times(29, 1000, HalfEven))
	net := must(gross.Minus(fee))
	sale := mustEntry(NewJournalEntry("pay-001", "card payment captured", []Posting{
		Dr("1200", gross), Cr("2100", net), Cr("4100", fee)}))
	posted, _ := ledger.Post(sale)
	fmt.Printf("posted=%v\n", posted)
	printEntry(sale)
	posted, _ = ledger.Post(sale)
	fmt.Printf("replay posted=%v  (same id, same content: no-op)\n", posted)

	settle := mustEntry(NewJournalEntry("stl-001", "processor settles to our bank", []Posting{Dr("1010", gross), Cr("1200", gross)}))
	ledger.Post(settle)
	printEntry(settle)
	payout := mustEntry(NewJournalEntry("out-001", "payout to merchant", []Posting{Dr("2100", net), Cr("1010", net)}))
	ledger.Post(payout)
	printEntry(payout)

	fmt.Println("\n== Balances (in each account's normal direction) ==")
	for _, id := range []string{"1010", "1200", "2100", "4100"} {
		a, _ := ledger.Account(id)
		b, _ := ledger.Balance(id)
		fmt.Printf("%s %-22s %-9v %v\n", id, a.Name, a.Type, b)
	}
	fmt.Println("\n== Trial balance ==")
	printTrialBalance(ledger, USD)

	fmt.Println("\n== Entries that cannot exist ==")
	if _, err := NewJournalEntry("bad-1", "unbalanced", []Posting{Dr("1200", gross), Cr("2100", net)}); err != nil {
		fmt.Println("rejected:", err)
	}
	if _, err := NewJournalEntry("bad-2", "mixed currency", []Posting{Dr("1200", gross), Cr("2100", must(Parse("100.00", EUR)))}); err != nil {
		fmt.Println("rejected:", err)
	}
	conflict := mustEntry(NewJournalEntry("pay-001", "same id, different content", []Posting{Dr("1200", net), Cr("2100", net)}))
	if _, err := ledger.Post(conflict); err != nil {
		fmt.Println("rejected:", err)
	}

	fmt.Println("\n== Reversal: the capture is voided; nothing is edited ==")
	before, _ := ledger.Balance("4100")
	reversal := mustEntry(ledger.Reverse("pay-001", "rev-001", "reversal: void pay-001"))
	printEntry(reversal)
	after, _ := ledger.Balance("4100")
	fmt.Printf("fee revenue before reversal %v, after %v\n", before, after)
	fmt.Printf("journal now holds %d entries; pay-001 is still entry #1\n", ledger.Size())

	fmt.Println("\n== 10,000 seeded random balanced entries: trial balance still sums to zero ==")
	gen := NewRandomEntries(42, []string{"1010", "1200", "2100", "4100"}, USD)
	for i := 0; i < 10_000; i++ {
		if _, err := ledger.Post(gen.Next()); err != nil {
			panic(err)
		}
	}
	tb := ledger.TrialBalance(USD)
	fmt.Printf("entries=%d totalDebits=%v totalCredits=%v balances=%v\n", ledger.Size(), tb.TotalDebits, tb.TotalCredits, tb.Balances())
}
