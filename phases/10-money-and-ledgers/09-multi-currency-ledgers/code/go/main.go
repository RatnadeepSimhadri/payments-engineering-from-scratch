// Lesson: phases/10-money-and-ledgers/09-multi-currency-ledgers/docs/en.md
// Multi-Currency Ledgers and Revaluation
// Run:  go run .
// Test: go test ./...
// Sources: ISO 4217 (codes, exponents); IAS 21 / ASC 830 (foreign-currency monetary items, exchange differences);
// Money from Lesson 01 (money.go), FX rate shape from Lesson 02. Go 1.22, standard library only.
package main

import (
	"errors"
	"fmt"
)

// ---------------------------------------------------------------------------------------------
// Per-currency sub-ledgers. An entry has exactly one currency. Cross-currency business events are
// transactions made of one entry per currency, joined through FX clearing accounts.
// ---------------------------------------------------------------------------------------------

type Side int

const (
	Debit Side = iota
	Credit
)

type AccountType int

const (
	Asset AccountType = iota
	Expense
	Liability
	Equity
	Revenue
)

func (t AccountType) NormalBalance() Side {
	if t == Asset || t == Expense {
		return Debit
	}
	return Credit
}

// Account holds exactly one currency for its whole life. Clearing marks FX clearing accounts.
type Account struct {
	ID       string
	Name     string
	Type     AccountType
	Currency Currency
	Clearing bool
}

// Sentinel errors. ErrLedger is the root (Java's LedgerException); ErrMixedCurrency wraps it and stays distinct.
var (
	ErrLedger        = errors.New("ledger")
	ErrMixedCurrency = fmt.Errorf("%w: mixed currency", ErrLedger)
)

func ledgerErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrLedger, fmt.Sprintf(format, args...))
}

type Posting struct {
	AccountID string
	Side      Side
	Amount    Money
}

func NewPosting(accountID string, side Side, amount Money) (Posting, error) {
	if !amount.IsPositive() {
		return Posting{}, ledgerErr("posting amount must be positive, got %v", amount)
	}
	return Posting{accountID, side, amount}, nil
}

func DebitPosting(accountID string, amount Money) (Posting, error) {
	return NewPosting(accountID, Debit, amount)
}
func CreditPosting(accountID string, amount Money) (Posting, error) {
	return NewPosting(accountID, Credit, amount)
}

func (p Posting) SignedMinor() int64 {
	if p.Side == Debit {
		return p.Amount.MinorUnits
	}
	return -p.Amount.MinorUnits
}

// JournalEntry is a single-currency balanced entry. Mixing currencies inside one entry is rejected, not netted.
type JournalEntry struct {
	ID       string
	Currency Currency
	Postings []Posting
}

func NewJournalEntry(id string, currency Currency, postings []Posting) (JournalEntry, error) {
	if len(postings) < 2 {
		return JournalEntry{}, ledgerErr("entry %s needs at least two postings", id)
	}
	var net int64
	for _, p := range postings {
		if p.Amount.Currency != currency {
			return JournalEntry{}, fmt.Errorf("%w: entry %s is %s but has a %s posting; use one entry per currency",
				ErrMixedCurrency, id, currency.Code, p.Amount.Currency.Code)
		}
		var err error
		if net, err = addChecked(net, p.SignedMinor()); err != nil {
			return JournalEntry{}, err
		}
	}
	if net != 0 {
		return JournalEntry{}, ledgerErr("entry %s unbalanced by %v", id, OfMinor(net, currency))
	}
	return JournalEntry{id, currency, append([]Posting(nil), postings...)}, nil
}

// EntryOf builds an entry in the currency of its first posting (JournalEntry.of in Java).
func EntryOf(id string, postings ...Posting) (JournalEntry, error) {
	if len(postings) == 0 {
		return JournalEntry{}, ledgerErr("entry %s needs at least two postings", id)
	}
	return NewJournalEntry(id, postings[0].Amount.Currency, postings)
}

func (e JournalEntry) Equal(o JournalEntry) bool {
	if e.ID != o.ID || e.Currency != o.Currency || len(e.Postings) != len(o.Postings) {
		return false
	}
	for i := range e.Postings {
		if e.Postings[i] != o.Postings[i] {
			return false
		}
	}
	return true
}

// FxRate is an exchange rate as an exact rational: 1 unit of Base = Numerator/Denominator units of Quote.
// Source and timestamp travel with it so every conversion can be audited (Lesson 02).
type FxRate struct {
	Base        Currency
	Quote       Currency
	Numerator   int64
	Denominator int64
	Source      string
	AsOf        int64
}

func NewFxRate(base, quote Currency, numerator, denominator int64, source string, asOf int64) (FxRate, error) {
	if base == quote {
		return FxRate{}, errors.New("rate needs two different currencies")
	}
	if numerator <= 0 || denominator <= 0 {
		return FxRate{}, errors.New("rate must be positive")
	}
	return FxRate{base, quote, numerator, denominator, source, asOf}, nil
}

// Convert an amount of base or quote into the other currency, in minor units, rounding once.
func (r FxRate) Convert(amount Money, rounding Rounding) (Money, error) {
	from := amount.Currency
	var to Currency
	switch from {
	case r.Base:
		to = r.Quote
	case r.Quote:
		to = r.Base
	default:
		return Money{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, from.Code, r.Base.Code)
	}
	num, den := r.Numerator, r.Denominator
	if from != r.Base {
		num, den = r.Denominator, r.Numerator
	}
	// minor(to) = minor(from) * rate * 10^(exp(to) - exp(from))
	shift := to.Exponent - from.Exponent
	var err error
	for range abs64(int64(shift)) {
		if shift > 0 {
			num, err = mulChecked(num, 10)
		} else {
			den, err = mulChecked(den, 10)
		}
		if err != nil {
			return Money{}, err
		}
	}
	product, err := mulChecked(amount.MinorUnits, num)
	if err != nil {
		return Money{}, err
	}
	return OfMinor(DivideRounded(product, den, rounding), to), nil
}

func (r FxRate) String() string {
	return fmt.Sprintf("1 %s = %d/%d %s (%s)", r.Base.Code, r.Numerator, r.Denominator, r.Quote.Code, r.Source)
}

// Transaction is a business event: one entry per currency, posted all-or-nothing. Rate is nil for single-currency events.
type Transaction struct {
	ID          string
	Description string
	Entries     []JournalEntry
	Rate        *FxRate
}

func NewTransaction(id, description string, entries []JournalEntry, rate *FxRate) Transaction {
	return Transaction{id, description, append([]JournalEntry(nil), entries...), rate}
}

func (t Transaction) Equal(o Transaction) bool {
	if t.ID != o.ID || t.Description != o.Description || len(t.Entries) != len(o.Entries) || (t.Rate == nil) != (o.Rate == nil) {
		return false
	}
	if t.Rate != nil && *t.Rate != *o.Rate {
		return false
	}
	for i := range t.Entries {
		if !t.Entries[i].Equal(o.Entries[i]) {
			return false
		}
	}
	return true
}

// SubLedger is one currency's journal and balances. Knows nothing about other currencies.
type SubLedger struct {
	Currency     Currency
	journal      []JournalEntry
	accountOrder []string
	netDebits    map[string]int64
}

func NewSubLedger(currency Currency) *SubLedger {
	return &SubLedger{Currency: currency, netDebits: map[string]int64{}}
}

func (s *SubLedger) Open(accountID string) {
	if _, seen := s.netDebits[accountID]; !seen {
		s.accountOrder = append(s.accountOrder, accountID)
	}
	s.netDebits[accountID] = 0
}

func (s *SubLedger) Append(e JournalEntry) error {
	for _, p := range e.Postings {
		if _, err := addChecked(s.netDebits[p.AccountID], p.SignedMinor()); err != nil {
			return err
		}
	}
	s.journal = append(s.journal, e)
	for _, p := range e.Postings {
		if _, seen := s.netDebits[p.AccountID]; !seen {
			s.accountOrder = append(s.accountOrder, p.AccountID)
		}
		s.netDebits[p.AccountID] += p.SignedMinor()
	}
	return nil
}

func (s *SubLedger) NetDebit(accountID string) int64 { return s.netDebits[accountID] }

// TrialBalanceNet is the sum of net debits over every account in this currency. Zero, always, or the sub-ledger is corrupt.
func (s *SubLedger) TrialBalanceNet() int64 {
	var acc int64
	for _, id := range s.accountOrder {
		acc += s.netDebits[id]
	}
	return acc
}

func (s *SubLedger) Size() int { return len(s.journal) }

// NetDebits returns a copy, keyed by account id, in insertion order of ids (AccountIDs()).
func (s *SubLedger) NetDebits() map[string]int64 {
	out := make(map[string]int64, len(s.netDebits))
	for k, v := range s.netDebits {
		out[k] = v
	}
	return out
}

func (s *SubLedger) AccountIDs() []string { return append([]string(nil), s.accountOrder...) }

// Position, per foreign currency: the net position outside clearing, what it cost in functional currency, and revaluations booked.
type Position struct {
	Currency Currency
	Position Money
	Basis    Money
	Revalued Money
}

func (p Position) CarryingValue() Money {
	v, err := p.Basis.Plus(p.Revalued)
	if err != nil {
		panic(err)
	}
	return v
}

const (
	Unrealized = "4900"
	Realized   = "4910"
)

// MultiCurrencyLedger has one SubLedger per currency, a functional (reporting) currency, and per-currency FX clearing
// accounts. Exchange posts a two-entry transaction through clearing; Revalue books unrealized gain/loss in functional
// currency only; Realize books the realized result when a foreign position is flat.
type MultiCurrencyLedger struct {
	Functional        Currency
	currencyOrder     []Currency
	subLedgers        map[Currency]*SubLedger
	accountOrder      []string
	accounts          map[string]Account
	transactions      map[string]Transaction
	basis             map[Currency]int64 // functional minor units, signed like the position
	revalued          map[Currency]int64 // cumulative unrealized gain (+) / loss (-)
	adjustmentCounter int
}

func NewMultiCurrencyLedger(functional Currency) *MultiCurrencyLedger {
	l := &MultiCurrencyLedger{
		Functional: functional, subLedgers: map[Currency]*SubLedger{}, accounts: map[string]Account{},
		transactions: map[string]Transaction{}, basis: map[Currency]int64{}, revalued: map[Currency]int64{},
	}
	l.AddCurrency(functional)
	if _, err := l.OpenAccount(Unrealized, "Unrealized FX Gain/Loss", Revenue, functional); err != nil {
		panic(err)
	}
	if _, err := l.OpenAccount(Realized, "Realized FX Gain/Loss", Revenue, functional); err != nil {
		panic(err)
	}
	return l
}

func ClearingID(c Currency) string    { return "1900-" + c.Code }
func RevaluationID(c Currency) string { return "1950-" + c.Code }

// AddCurrency registers a currency: its sub-ledger, its clearing account, and (if foreign) its revaluation adjustment account.
func (l *MultiCurrencyLedger) AddCurrency(c Currency) {
	if _, known := l.subLedgers[c]; known {
		return
	}
	l.subLedgers[c] = NewSubLedger(c)
	l.currencyOrder = append(l.currencyOrder, c)
	l.openAccountInternal(Account{ClearingID(c), "FX Clearing " + c.Code, Asset, c, true})
	if c != l.Functional {
		l.openAccountInternal(Account{RevaluationID(c), "FX Revaluation Adjustment " + c.Code, Asset, l.Functional, false})
		l.basis[c] = 0
		l.revalued[c] = 0
	}
}

func (l *MultiCurrencyLedger) OpenAccount(id, name string, typ AccountType, currency Currency) (Account, error) {
	l.AddCurrency(currency)
	return l.openAccountInternal(Account{id, name, typ, currency, false})
}

func (l *MultiCurrencyLedger) openAccountInternal(a Account) (Account, error) {
	if _, exists := l.accounts[a.ID]; exists {
		return Account{}, ledgerErr("account exists: %s", a.ID)
	}
	l.accounts[a.ID] = a
	l.accountOrder = append(l.accountOrder, a.ID)
	l.subLedgers[a.Currency].Open(a.ID)
	return a, nil
}

func (l *MultiCurrencyLedger) Account(id string) (Account, error) {
	a, ok := l.accounts[id]
	if !ok {
		return Account{}, ledgerErr("unknown account: %s", id)
	}
	return a, nil
}

func (l *MultiCurrencyLedger) SubLedger(c Currency) (*SubLedger, error) {
	s, ok := l.subLedgers[c]
	if !ok {
		return nil, ledgerErr("no sub-ledger for %s", c.Code)
	}
	return s, nil
}

func (l *MultiCurrencyLedger) Balance(accountID string) (Money, error) {
	a, err := l.Account(accountID)
	if err != nil {
		return Money{}, err
	}
	net := l.subLedgers[a.Currency].NetDebit(accountID)
	if a.Type.NormalBalance() == Credit {
		net = -net
	}
	return OfMinor(net, a.Currency), nil
}

func (l *MultiCurrencyLedger) NetDebit(accountID string) (Money, error) {
	a, err := l.Account(accountID)
	if err != nil {
		return Money{}, err
	}
	return OfMinor(l.subLedgers[a.Currency].NetDebit(accountID), a.Currency), nil
}

// Post a transaction: validate every entry against its sub-ledger, then append all. Idempotent on transaction id.
func (l *MultiCurrencyLedger) Post(tx Transaction) (bool, error) {
	if existing, seen := l.transactions[tx.ID]; seen {
		if existing.Equal(tx) {
			return false, nil
		}
		return false, ledgerErr("transaction %s already posted with different content", tx.ID)
	}
	if len(tx.Entries) == 0 {
		return false, ledgerErr("transaction %s has no entries", tx.ID)
	}
	for _, e := range tx.Entries {
		if _, err := l.SubLedger(e.Currency); err != nil {
			return false, err
		}
		for _, p := range e.Postings {
			a, err := l.Account(p.AccountID)
			if err != nil {
				return false, err
			}
			if a.Currency != e.Currency {
				return false, ledgerErr("entry %s: account %s is not a %s account", e.ID, p.AccountID, e.Currency.Code)
			}
		}
	}
	for _, e := range tx.Entries {
		if err := l.subLedgers[e.Currency].Append(e); err != nil {
			return false, err
		}
	}
	l.transactions[tx.ID] = tx
	return true, nil
}

// Exchange is a cross-currency movement: debit debitAccount by amount (its currency A) and credit creditAccount with
// the converted amount (currency B), as two single-currency entries joined by the clearing accounts 1900-A and 1900-B.
// One of A, B must be the functional currency so the position's functional cost (basis) is known exactly.
func (l *MultiCurrencyLedger) Exchange(txID, description, debitAccount string, amount Money, creditAccount string, rate FxRate) (Transaction, error) {
	da, err := l.Account(debitAccount)
	if err != nil {
		return Transaction{}, err
	}
	ca, err := l.Account(creditAccount)
	if err != nil {
		return Transaction{}, err
	}
	a, b := da.Currency, ca.Currency
	if amount.Currency != a {
		return Transaction{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, amount.Currency.Code, a.Code)
	}
	if a == b {
		return Transaction{}, ledgerErr("exchange needs two currencies; post a single-currency entry instead")
	}
	if a != l.Functional && b != l.Functional {
		return Transaction{}, ledgerErr("exchange %s->%s must route through %s", a.Code, b.Code, l.Functional.Code)
	}
	converted, err := rate.Convert(amount, HalfEven)
	if err != nil {
		return Transaction{}, err
	}
	if converted.Currency != b {
		return Transaction{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, converted.Currency.Code, b.Code)
	}
	if !converted.IsPositive() {
		return Transaction{}, ledgerErr("converted amount rounds to zero")
	}
	legA, err := EntryOf(txID+"/"+a.Code, Posting{debitAccount, Debit, amount}, Posting{ClearingID(a), Credit, amount})
	if err != nil {
		return Transaction{}, err
	}
	legB, err := EntryOf(txID+"/"+b.Code, Posting{ClearingID(b), Debit, converted}, Posting{creditAccount, Credit, converted})
	if err != nil {
		return Transaction{}, err
	}
	tx := NewTransaction(txID, description, []JournalEntry{legA, legB}, &rate)
	posted, err := l.Post(tx)
	if err != nil {
		return Transaction{}, err
	}
	if posted {
		// Position outside clearing: A gains +amount, B loses -converted. Basis follows at the functional value.
		if b == l.Functional {
			l.basis[a], err = addChecked(l.basis[a], converted.MinorUnits)
		} else {
			l.basis[b], err = addChecked(l.basis[b], -amount.MinorUnits)
		}
		if err != nil {
			return Transaction{}, err
		}
	}
	return l.transactions[txID], nil
}

// PositionOf is the net debit of every non-clearing account in a foreign currency: what we actually hold (+) or owe (-).
func (l *MultiCurrencyLedger) PositionOf(c Currency) Money {
	var acc int64
	for _, id := range l.accountOrder {
		a := l.accounts[id]
		if a.Currency == c && !a.Clearing {
			acc += l.subLedgers[c].NetDebit(a.ID)
		}
	}
	return OfMinor(acc, c)
}

func (l *MultiCurrencyLedger) PositionReport(c Currency) (Position, error) {
	if err := l.requireForeign(c); err != nil {
		return Position{}, err
	}
	return Position{c, l.PositionOf(c), OfMinor(l.basis[c], l.Functional), OfMinor(l.revalued[c], l.Functional)}, nil
}

// Revalue marks the foreign position to the closing rate. The difference between its value at that rate and its
// carrying value (basis + earlier revaluations) is booked as unrealized gain/loss, in functional currency only:
// gain -> Dr 1950-c / Cr 4900; loss -> Dr 4900 / Cr 1950-c. Returns the gain (negative for a loss).
func (l *MultiCurrencyLedger) Revalue(c Currency, closingRate FxRate) (Money, error) {
	if err := l.requireForeign(c); err != nil {
		return Money{}, err
	}
	fair, err := closingRate.Convert(l.PositionOf(c), HalfEven)
	if err != nil {
		return Money{}, err
	}
	carrying, err := addChecked(l.basis[c], l.revalued[c])
	if err != nil {
		return Money{}, err
	}
	gain, err := addChecked(fair.MinorUnits, -carrying)
	if err != nil {
		return Money{}, err
	}
	if gain != 0 {
		g := OfMinor(abs64(gain), l.Functional)
		l.adjustmentCounter++
		id := fmt.Sprintf("reval-%d-%s", l.adjustmentCounter, c.Code)
		var e JournalEntry
		if gain > 0 {
			e, err = EntryOf(id, Posting{RevaluationID(c), Debit, g}, Posting{Unrealized, Credit, g})
		} else {
			e, err = EntryOf(id, Posting{Unrealized, Debit, g}, Posting{RevaluationID(c), Credit, g})
		}
		if err != nil {
			return Money{}, err
		}
		if _, err := l.Post(NewTransaction(id, fmt.Sprintf("revalue %s at %v", c.Code, closingRate), []JournalEntry{e}, &closingRate)); err != nil {
			return Money{}, err
		}
		l.revalued[c] += gain
	}
	return OfMinor(gain, l.Functional), nil
}

// Realize: when a foreign position is flat (zero outside clearing), its remaining basis is the realized gain/loss.
// Book it from functional clearing to 4910 and reverse the unrealized revaluations, which are no longer unrealized.
// Returns the realized gain (negative for a loss).
func (l *MultiCurrencyLedger) Realize(c Currency) (Money, error) {
	if err := l.requireForeign(c); err != nil {
		return Money{}, err
	}
	if pos := l.PositionOf(c); !pos.IsZero() {
		return Money{}, ledgerErr("position in %s is %v; realize only a flat position", c.Code, pos)
	}
	realizedGain := -l.basis[c]
	var entries []JournalEntry
	l.adjustmentCounter++
	id := fmt.Sprintf("realize-%d-%s", l.adjustmentCounter, c.Code)
	if realizedGain != 0 {
		g := OfMinor(abs64(realizedGain), l.Functional)
		var e JournalEntry
		var err error
		if realizedGain > 0 {
			e, err = EntryOf(id+"/pnl", Posting{ClearingID(l.Functional), Debit, g}, Posting{Realized, Credit, g})
		} else {
			e, err = EntryOf(id+"/pnl", Posting{Realized, Debit, g}, Posting{ClearingID(l.Functional), Credit, g})
		}
		if err != nil {
			return Money{}, err
		}
		entries = append(entries, e)
	}
	if rev := l.revalued[c]; rev != 0 {
		r := OfMinor(abs64(rev), l.Functional)
		var e JournalEntry
		var err error
		if rev > 0 {
			e, err = EntryOf(id+"/unwind", Posting{Unrealized, Debit, r}, Posting{RevaluationID(c), Credit, r})
		} else {
			e, err = EntryOf(id+"/unwind", Posting{RevaluationID(c), Debit, r}, Posting{Unrealized, Credit, r})
		}
		if err != nil {
			return Money{}, err
		}
		entries = append(entries, e)
	}
	if len(entries) > 0 {
		if _, err := l.Post(NewTransaction(id, "realize "+c.Code+" position", entries, nil)); err != nil {
			return Money{}, err
		}
	}
	l.basis[c] = 0
	l.revalued[c] = 0
	return OfMinor(realizedGain, l.Functional), nil
}

// ClearingMatchesBasis is the invariant: functional clearing net debit == sum of open foreign bases
// (each exchange moves both by the same amount).
func (l *MultiCurrencyLedger) ClearingMatchesBasis() bool {
	var sum int64
	for _, c := range l.currencyOrder {
		sum += l.basis[c] // zero for the functional currency, which has no basis entry
	}
	return l.subLedgers[l.Functional].NetDebit(ClearingID(l.Functional)) == sum
}

// AllSubLedgersBalance: every sub-ledger nets to zero in its own currency.
func (l *MultiCurrencyLedger) AllSubLedgersBalance() bool {
	for _, c := range l.currencyOrder {
		if l.subLedgers[c].TrialBalanceNet() != 0 {
			return false
		}
	}
	return true
}

func (l *MultiCurrencyLedger) Currencies() []Currency {
	return append([]Currency(nil), l.currencyOrder...)
}

func (l *MultiCurrencyLedger) Accounts() []Account {
	out := make([]Account, 0, len(l.accountOrder))
	for _, id := range l.accountOrder {
		out = append(out, l.accounts[id])
	}
	return out
}

func (l *MultiCurrencyLedger) requireForeign(c Currency) error {
	if _, foreign := l.basis[c]; c == l.Functional || !foreign {
		return ledgerErr("%s is not a foreign currency of this ledger", c.Code)
	}
	return nil
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func printTransaction(tx Transaction) {
	line := tx.ID + "  " + tx.Description
	if tx.Rate != nil {
		line += fmt.Sprintf("  @ %v", *tx.Rate)
	}
	fmt.Println(line)
	for _, e := range tx.Entries {
		fmt.Printf("  [%s sub-ledger] %s\n", e.Currency.Code, e.ID)
		for _, p := range e.Postings {
			prefix := "        Cr "
			if p.Side == Debit {
				prefix = "    Dr "
			}
			fmt.Printf("%s%s  %v\n", prefix, p.AccountID, p.Amount)
		}
	}
}

func printBalances(l *MultiCurrencyLedger) {
	for _, a := range l.Accounts() {
		if b := must(l.Balance(a.ID)); !b.IsZero() {
			fmt.Printf("  %-9s %-34s %v\n", a.ID, a.Name, b)
		}
	}
	for _, c := range l.Currencies() {
		fmt.Printf("  %s sub-ledger nets to %v\n", c.Code, OfMinor(must(l.SubLedger(c)).TrialBalanceNet(), c))
	}
}

func single(txID, description, entryID, debit string, credit string, amount Money) Transaction {
	e := must(EntryOf(entryID, Posting{debit, Debit, amount}, Posting{credit, Credit, amount}))
	return NewTransaction(txID, description, []JournalEntry{e}, nil)
}

func main() {
	var day int64 = 1_767_225_600
	ledger := NewMultiCurrencyLedger(USD)
	must(ledger.OpenAccount("1200-USD", "Processor Receivable", Asset, USD))
	must(ledger.OpenAccount("1010-USD", "Operating Bank", Asset, USD))
	must(ledger.OpenAccount("1010-EUR", "Operating Bank", Asset, EUR))
	must(ledger.OpenAccount("2100-EUR", "Merchant Payable", Liability, EUR))

	fmt.Println("== A US customer pays 110.00 USD; the German merchant is owed EUR at 1 EUR = 1.1000 USD ==")
	booked := must(NewFxRate(EUR, USD, 11_000, 10_000, "rate-desk", day))
	printTransaction(must(ledger.Exchange("fx-1", "cross-border card payment", "1200-USD", must(Parse("110.00", USD)), "2100-EUR", booked)))
	printBalances(ledger)
	open := must(ledger.PositionReport(EUR))
	fmt.Printf("  EUR position %v (we owe EUR), basis %v (its USD value when booked)\n", open.Position, open.Basis)

	fmt.Println("\n== Mixing currencies in one entry is rejected ==")
	if _, err := NewJournalEntry("bad", USD, []Posting{{"1200-USD", Debit, must(Parse("110.00", USD))},
		{"2100-EUR", Credit, must(Parse("100.00", EUR))}}); errors.Is(err, ErrMixedCurrency) {
		fmt.Println("rejected:", err)
	}

	fmt.Println("\n== Month end: EUR strengthens to 1.1200 USD; revalue the open EUR position ==")
	closing := must(NewFxRate(EUR, USD, 11_200, 10_000, "ECB reference", day+30*86_400))
	fmt.Printf("unrealized gain/loss booked: %v\n", must(ledger.Revalue(EUR, closing)))
	fmt.Printf("revalue again at the same rate: %v (idempotent: nothing left to book)\n", must(ledger.Revalue(EUR, closing)))
	p := must(ledger.PositionReport(EUR))
	fmt.Printf("  EUR position %v, basis %v, revalued %v, carrying %v\n", p.Position, p.Basis, p.Revalued, p.CarryingValue())
	fmt.Printf("  4900 Unrealized FX Gain/Loss %v\n", must(ledger.Balance("4900")))

	fmt.Println("\n== Processor settles 110.00 USD; treasury buys 100.00 EUR at 1.1150; merchant is paid ==")
	must(ledger.Post(single("stl-1", "processor settlement", "stl-1/USD", "1010-USD", "1200-USD", must(Parse("110.00", USD)))))
	market := must(NewFxRate(EUR, USD, 11_150, 10_000, "bank quote", day+31*86_400))
	printTransaction(must(ledger.Exchange("fx-2", "buy EUR for payout", "1010-EUR", must(Parse("100.00", EUR)), "1010-USD", market)))
	must(ledger.Post(single("out-1", "pay merchant", "out-1/EUR", "2100-EUR", "1010-EUR", must(Parse("100.00", EUR)))))
	fmt.Printf("EUR position now %v; functional clearing holds %v\n", ledger.PositionOf(EUR), must(ledger.NetDebit("1900-USD")))
	fmt.Printf("realized gain/loss: %v\n", must(ledger.Realize(EUR)))
	fmt.Printf("  4900 Unrealized %v, 4910 Realized %v, 1950-EUR %v, 1900-USD %v\n", must(ledger.Balance("4900")),
		must(ledger.Balance("4910")), must(ledger.Balance("1950-EUR")), must(ledger.NetDebit("1900-USD")))
	fmt.Printf("  operating bank 1010-USD %v: the 1.50 USD the round trip cost\n", must(ledger.Balance("1010-USD")))
	fmt.Printf("  every sub-ledger balances: %v; clearing matches basis: %v\n", ledger.AllSubLedgersBalance(), ledger.ClearingMatchesBasis())

	fmt.Println("\n== Exponents differ: 5,000 JPY to USD at 1 USD = 157.25 JPY ==")
	jpy := must(NewFxRate(USD, JPY, 15_725, 100, "rate-desk", day))
	fmt.Printf("5000 JPY -> %v; 31.80 USD -> %v\n", must(jpy.Convert(must(Parse("5000", JPY)), HalfEven)),
		must(jpy.Convert(must(Parse("31.80", USD)), HalfEven)))
}
