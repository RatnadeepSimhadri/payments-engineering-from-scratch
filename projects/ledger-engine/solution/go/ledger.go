// Project: projects/ledger-engine — the ledger: append-only journal, derived balances, reconciliation.
// Stage 2: Ledger.Post.  Stage 3: Balance, BalanceAt, Snapshots, TrialBalance, Reverse.  Stage 4: Lines, Reconcile.
// Docs: projects/ledger-engine/stages/*/docs/en.md
// Run:  go run .
// Test: python3 scripts/project_test.py ledger-engine --lang go --all --solution
// Go 1.22+, standard library only. Same design as the Java and Python solutions.
package main

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"time"
)

// BalanceSnapshot holds the signed balances of every account after the entry with this sequence number was applied.
type BalanceSnapshot struct {
	Sequence int64
	Balances map[string]Money
}

// TrialBalanceLine is one row of a trial balance: the account's net balance placed in the debit or the credit column.
type TrialBalanceLine struct {
	Account Account
	Debit   Money
	Credit  Money
}

// TrialBalance is all accounts of one currency with their column totals. The two totals agree, or an entry got in unbalanced.
type TrialBalance struct {
	Currency     Currency
	Lines        []TrialBalanceLine
	TotalDebits  Money
	TotalCredits Money
}

func (tb TrialBalance) Difference() Money { return must(tb.TotalDebits.Minus(tb.TotalCredits)) }

func (tb TrialBalance) IsBalanced() bool { return tb.Difference().IsZero() }

// DefaultSnapshotEvery is K when the caller does not choose one: a snapshot after every 100th posted entry.
const DefaultSnapshotEvery = 100

// Ledger is an in-memory double-entry ledger: a chart of accounts and an append-only journal. Not thread-safe.
type Ledger struct {
	accounts      *AccountRegistry
	snapshotEvery int
	journal       []PostedEntry
	byID          map[string]PostedEntry
	snapshots     []BalanceSnapshot
}

func NewLedger(accounts *AccountRegistry) (*Ledger, error) {
	return NewLedgerWithSnapshots(accounts, DefaultSnapshotEvery)
}

// NewLedgerWithSnapshots: snapshotEvery is K; a running-balance snapshot is taken after every K-th posted entry.
func NewLedgerWithSnapshots(accounts *AccountRegistry, snapshotEvery int) (*Ledger, error) {
	if accounts == nil {
		return nil, errors.New("ledger needs an account registry")
	}
	if snapshotEvery < 1 {
		return nil, errors.New("snapshotEvery must be >= 1")
	}
	return &Ledger{accounts: accounts, snapshotEvery: snapshotEvery, byID: map[string]PostedEntry{}}, nil
}

func (l *Ledger) Accounts() *AccountRegistry { return l.accounts }

func (l *Ledger) SnapshotEvery() int { return l.snapshotEvery }

// ---------------------------------------------------------------- stage 2: posting

// Post appends an entry to the journal and gives it the next sequence number. Posting an id that is already
// in the journal is a no-op that returns the record already there. Every rule is checked before anything is
// written, so a rejected entry leaves the journal exactly as it was.
func (l *Ledger) Post(entry JournalEntry) (PostedEntry, error) {
	if existing, ok := l.byID[entry.ID]; ok {
		return existing, nil
	}
	if err := l.validate(entry); err != nil {
		return PostedEntry{}, err
	}
	posted := PostedEntry{int64(len(l.journal)) + 1, JournalEntry{entry.ID, entry.Date, slices.Clone(entry.Postings)}}
	l.journal = append(l.journal, posted)
	l.byID[entry.ID] = posted
	if posted.Sequence%int64(l.snapshotEvery) == 0 {
		l.snapshots = append(l.snapshots, BalanceSnapshot{posted.Sequence, l.balancesAt(posted.Sequence)})
	}
	return posted, nil
}

// validate checks, in order: posting count, unknown account, mixed currencies, account currency, balance.
func (l *Ledger) validate(entry JournalEntry) error {
	if len(entry.Postings) < 2 {
		return fmt.Errorf("%w: entry %s rejected: needs at least two postings, got %d", ErrInvalidEntry, entry.ID, len(entry.Postings))
	}
	currency, _ := entry.Currency()
	debits := OfMinor(0, currency)
	credits := OfMinor(0, currency)
	for _, posting := range entry.Postings {
		account, err := l.accounts.Get(posting.AccountID)
		if err != nil {
			return err
		}
		if posting.Amount.Currency != currency {
			return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, currency.Code, posting.Amount.Currency.Code)
		}
		if account.Currency != currency {
			return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, account.Currency.Code, currency.Code)
		}
		if posting.Side == Debit {
			if debits, err = debits.Plus(posting.Amount); err != nil {
				return err
			}
		} else if credits, err = credits.Plus(posting.Amount); err != nil {
			return err
		}
	}
	if debits != credits {
		return fmt.Errorf("%w: entry %s is unbalanced: debits %v != credits %v", ErrUnbalancedEntry, entry.ID, debits, credits)
	}
	return nil
}

// Journal is the journal in sequence order. A copy: entries are never edited or removed.
func (l *Ledger) Journal() []PostedEntry { return slices.Clone(l.journal) }

func (l *Ledger) Entry(id string) (PostedEntry, error) {
	posted, ok := l.byID[id]
	if !ok {
		return PostedEntry{}, fmt.Errorf("%w: %s", ErrUnknownEntry, id)
	}
	return posted, nil
}

func (l *Ledger) HasEntry(id string) bool {
	_, ok := l.byID[id]
	return ok
}

func (l *Ledger) Size() int { return len(l.journal) }

// ---------------------------------------------------------------- stage 3: balances, trial balance, reversals

// signed is the effect of one posting on its account: positive on the account's normal side.
func signed(posting Posting, account Account) Money {
	if posting.Side == account.NormalSide() {
		return posting.Amount
	}
	return posting.Amount.Negate()
}

// Balance is derived, never stored directly: the latest snapshot plus a replay of everything after it.
func (l *Ledger) Balance(accountID string) (Money, error) {
	account, err := l.accounts.Get(accountID)
	if err != nil {
		return Money{}, err
	}
	zero := OfMinor(0, account.Currency)
	balance := zero
	var from int64
	if n := len(l.snapshots); n > 0 {
		base := l.snapshots[n-1]
		if b, ok := base.Balances[accountID]; ok {
			balance = b
		}
		from = base.Sequence
	}
	for i := int(from); i < len(l.journal); i++ {
		for _, posting := range l.journal[i].Entry.Postings {
			if posting.AccountID == accountID {
				balance = must(balance.Plus(signed(posting, account)))
			}
		}
	}
	return balance, nil
}

// BalanceAt is a full recomputation from the start of the journal through sequence inclusive. Ignores snapshots.
func (l *Ledger) BalanceAt(accountID string, sequence int64) (Money, error) {
	account, err := l.accounts.Get(accountID)
	if err != nil {
		return Money{}, err
	}
	balance := OfMinor(0, account.Currency)
	for _, posted := range l.journal {
		if posted.Sequence > sequence {
			break
		}
		for _, posting := range posted.Entry.Postings {
			if posting.AccountID == accountID {
				balance = must(balance.Plus(signed(posting, account)))
			}
		}
	}
	return balance, nil
}

// balancesAt is the signed balance of every known account after sequence, by full replay.
func (l *Ledger) balancesAt(sequence int64) map[string]Money {
	balances := make(map[string]Money, l.accounts.Size())
	for _, account := range l.accounts.All() {
		balances[account.ID], _ = l.BalanceAt(account.ID, sequence)
	}
	return balances
}

// Snapshots returns a deep copy: editing the returned maps never touches the ledger.
func (l *Ledger) Snapshots() []BalanceSnapshot {
	out := make([]BalanceSnapshot, 0, len(l.snapshots))
	for _, s := range l.snapshots {
		out = append(out, BalanceSnapshot{s.Sequence, maps.Clone(s.Balances)})
	}
	return out
}

// TrialBalance, per currency (sorted by code): each account's net (debits minus credits) goes in the debit
// column if positive, the credit column if negative. Column totals must agree because every posted entry was balanced.
func (l *Ledger) TrialBalance() []TrialBalance {
	net := map[string]Money{} // account id -> debits minus credits
	for _, account := range l.accounts.All() {
		net[account.ID] = OfMinor(0, account.Currency)
	}
	for _, posted := range l.journal {
		for _, posting := range posted.Entry.Postings {
			current := net[posting.AccountID]
			if posting.Side == Debit {
				net[posting.AccountID] = must(current.Plus(posting.Amount))
			} else {
				net[posting.AccountID] = must(current.Minus(posting.Amount))
			}
		}
	}
	lines := map[string][]TrialBalanceLine{} // currency code -> lines
	currencies := map[string]Currency{}
	for _, account := range l.accounts.All() {
		balance := net[account.ID]
		zero := OfMinor(0, account.Currency)
		line := TrialBalanceLine{account, balance, zero}
		if balance.IsNegative() {
			line = TrialBalanceLine{account, zero, balance.Negate()}
		}
		lines[account.Currency.Code] = append(lines[account.Currency.Code], line)
		currencies[account.Currency.Code] = account.Currency
	}
	codes := make([]string, 0, len(lines))
	for code := range lines {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	result := make([]TrialBalance, 0, len(codes))
	for _, code := range codes {
		currency := currencies[code]
		debits := OfMinor(0, currency)
		credits := OfMinor(0, currency)
		for _, line := range lines[code] {
			debits = must(debits.Plus(line.Debit))
			credits = must(credits.Plus(line.Credit))
		}
		result = append(result, TrialBalance{currency, lines[code], debits, credits})
	}
	return result
}

// ReversalID is the id a reversal of entryID always gets, so reversing twice finds the first reversal.
func ReversalID(entryID string) string { return "rev:" + entryID }

// Reverse posts the mirror of an entry: same postings, every side flipped, same date as the original, id
// rev:<entryID>. The original stays in the journal untouched. Reversing an already-reversed entry returns
// the existing reversal (Post de-duplicates on id), so the operation is idempotent.
func (l *Ledger) Reverse(entryID string) (PostedEntry, error) {
	original, err := l.Entry(entryID)
	if err != nil {
		return PostedEntry{}, err
	}
	mirrored := make([]Posting, 0, len(original.Entry.Postings))
	for _, posting := range original.Entry.Postings {
		mirrored = append(mirrored, posting.Mirrored())
	}
	entry, err := NewJournalEntry(ReversalID(entryID), original.Entry.Date, mirrored...)
	if err != nil {
		return PostedEntry{}, err
	}
	return l.Post(entry)
}

func (l *Ledger) IsReversed(entryID string) bool { return l.HasEntry(ReversalID(entryID)) }

// ---------------------------------------------------------------- stage 4: reconciliation

// Lines is our side of a statement: one line per journal entry that touches the account, in journal order,
// with the entry's net effect on the account in the account's normal direction.
func (l *Ledger) Lines(accountID string) ([]LedgerLine, error) {
	account, err := l.accounts.Get(accountID)
	if err != nil {
		return nil, err
	}
	out := []LedgerLine{}
	for _, posted := range l.journal {
		net := OfMinor(0, account.Currency)
		touches := false
		for _, posting := range posted.Entry.Postings {
			if posting.AccountID != accountID {
				continue
			}
			touches = true
			net = must(net.Plus(signed(posting, account)))
		}
		if touches {
			out = append(out, LedgerLine{posted.ID(), net, posted.Entry.Date})
		}
	}
	return out, nil
}

// ErrBadDate wraps ErrLedger: a statement or entry date that is not ISO-8601 (yyyy-mm-dd).
var ErrBadDate = fmt.Errorf("%w: not an ISO-8601 date", ErrLedger)

// ParseDate reads "2026-10-01" strictly, like java.time.LocalDate.parse.
func ParseDate(text string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", text)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %s", ErrBadDate, text)
	}
	return t, nil
}

// WithinWindow is true when the two dates are at most windowDays apart (inclusive, symmetric).
func WithinWindow(a, b string, windowDays int) (bool, error) {
	da, err := ParseDate(a)
	if err != nil {
		return false, err
	}
	db, err := ParseDate(b)
	if err != nil {
		return false, err
	}
	days := int64(db.Sub(da).Hours() / 24)
	return abs64(days) <= int64(windowDays), nil
}

// Reconcile matches the account's journal lines against an external statement. Pass 1 matches by reference
// (equal amounts match; unequal amounts are AmountMismatch; a repeated statement reference is a Duplicate).
// Pass 2 matches what is left by equal amount within windowDays of each other, first unmatched ledger line
// in journal order wins. Whatever remains is missing on one side. The report's totals tie out by
// construction: every line lands in exactly one match or one break.
func (l *Ledger) Reconcile(accountID string, statement []StatementLine, windowDays int) (ReconciliationReport, error) {
	account, err := l.accounts.Get(accountID)
	if err != nil {
		return ReconciliationReport{}, err
	}
	if windowDays < 0 {
		return ReconciliationReport{}, errors.New("windowDays must be >= 0")
	}
	currency := account.Currency
	zero := OfMinor(0, currency)

	ledgerLines, _ := l.Lines(accountID)
	ledgerByRef := map[string]LedgerLine{}
	ledgerTotal := zero
	for _, line := range ledgerLines {
		ledgerByRef[line.Ref] = line
		ledgerTotal = must(ledgerTotal.Plus(line.Amount))
	}
	statementTotal := zero
	for _, line := range statement {
		if line.Amount.Currency != currency {
			return ReconciliationReport{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, currency.Code, line.Amount.Currency.Code)
		}
		if _, err := ParseDate(line.Date); err != nil {
			return ReconciliationReport{}, err
		}
		if statementTotal, err = statementTotal.Plus(line.Amount); err != nil {
			return ReconciliationReport{}, err
		}
	}

	matches := []ReconciliationMatch{}
	breaks := []ReconciliationBreak{}
	consumedLedger := map[string]bool{}
	seenStatementRefs := map[string]bool{}
	var unmatchedStatement []StatementLine

	// pass 1: by reference
	for _, line := range statement {
		if seenStatementRefs[line.Ref] {
			breaks = append(breaks, ReconciliationBreak{Duplicate, line.Ref, zero, line.Amount})
			continue
		}
		seenStatementRefs[line.Ref] = true
		ours, ok := ledgerByRef[line.Ref]
		switch {
		case !ok:
			unmatchedStatement = append(unmatchedStatement, line)
		case ours.Amount == line.Amount:
			matches = append(matches, ReconciliationMatch{ours.Ref, line.Ref, line.Amount, ByRef})
			consumedLedger[ours.Ref] = true
		default:
			breaks = append(breaks, ReconciliationBreak{AmountMismatch, line.Ref, ours.Amount, line.Amount})
			consumedLedger[ours.Ref] = true
		}
	}

	// pass 2: by amount within the date window
	for _, line := range unmatchedStatement {
		found := false
		for _, ours := range ledgerLines {
			if consumedLedger[ours.Ref] || ours.Amount != line.Amount {
				continue
			}
			within, err := WithinWindow(ours.Date, line.Date, windowDays)
			if err != nil {
				return ReconciliationReport{}, err
			}
			if within {
				matches = append(matches, ReconciliationMatch{ours.Ref, line.Ref, line.Amount, ByAmountAndDate})
				consumedLedger[ours.Ref] = true
				found = true
				break
			}
		}
		if !found {
			breaks = append(breaks, ReconciliationBreak{MissingInLedger, line.Ref, zero, line.Amount})
		}
	}

	// whatever the ledger still holds, the statement never showed
	for _, ours := range ledgerLines {
		if !consumedLedger[ours.Ref] {
			breaks = append(breaks, ReconciliationBreak{MissingInStatement, ours.Ref, ours.Amount, zero})
		}
	}
	return ReconciliationReport{accountID, currency, matches, breaks, ledgerTotal, statementTotal}, nil
}
