// Lesson: phases/10-money-and-ledgers/06-immutability-and-reversals/docs/en.md
// Immutability, Reversals, and Corrections
// Run:  go run .
// Test: go test ./...
// Sources: Pacioli (1494) on reversing entries; FIPS 180-4 (SHA-256) for the hash chain; Money from Lesson 01 (money.go).
// Go 1.22, standard library only. Same design as the Java and Python versions.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Zero is the zero amount of a currency.
func Zero(currency Currency) Money { return Money{0, currency} }

// ---------------------------------------------------------------------------------------------
// Entries that can never change, and the three ways to undo one: full reversal, partial reversal,
// and correction (reverse + replace). Every undo is a new entry linked to the one it undoes.
// ---------------------------------------------------------------------------------------------

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

type Account struct {
	ID       string
	Name     string
	Type     AccountType
	Currency Currency
}

// Sentinel errors. ErrLedger is the root every ledger rule wraps (Java's LedgerException); the three
// specific failures the lesson warns about get their own sentinels so callers can tell them apart.
var (
	ErrLedger              = errors.New("ledger")
	ErrIdempotencyConflict = fmt.Errorf("%w: idempotency conflict", ErrLedger)
	ErrAlreadyReversed     = fmt.Errorf("%w: already reversed", ErrLedger)
	ErrExcessiveReversal   = fmt.Errorf("%w: excessive reversal", ErrLedger)
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

func (p Posting) Canonical() string {
	return fmt.Sprintf("%s:%v:%d:%s", p.AccountID, p.Side, p.Amount.MinorUnits, p.Amount.Currency.Code)
}

// EntryKind: Original records a business event; Reversal undoes (part of) one; Correction replaces one that was reversed.
type EntryKind int

const (
	Original EntryKind = iota
	Reversal
	Correction
)

func (k EntryKind) String() string {
	return [...]string{"ORIGINAL", "REVERSAL", "CORRECTION"}[k]
}

// JournalEntry is immutable and balanced by construction: the postings slice is unexported and Postings()
// returns a copy. A Reversal or Correction must name the entry it relates to (RelatesTo != "") and a reason:
// an undo without a link or a why is indistinguishable from fraud in an audit.
type JournalEntry struct {
	ID          string
	Description string
	postings    []Posting
	Kind        EntryKind
	RelatesTo   string // "" for an original entry (Java: null)
	Reason      string
}

func NewJournalEntry(id, description string, postings []Posting, kind EntryKind, relatesTo, reason string) (JournalEntry, error) {
	if strings.TrimSpace(id) == "" {
		return JournalEntry{}, ledgerErr("entry id required")
	}
	if len(postings) < 2 {
		return JournalEntry{}, ledgerErr("entry %s needs at least two postings", id)
	}
	if kind == Original && relatesTo != "" {
		return JournalEntry{}, ledgerErr("an original entry relates to nothing")
	}
	if kind != Original && (relatesTo == "" || strings.TrimSpace(reason) == "") {
		return JournalEntry{}, ledgerErr("%v %s must name the entry it relates to and a reason", kind, id)
	}
	net := map[Currency]int64{}
	var order []Currency
	for _, p := range postings {
		if _, seen := net[p.Amount.Currency]; !seen {
			order = append(order, p.Amount.Currency)
		}
		sum, err := addChecked(net[p.Amount.Currency], p.SignedMinor())
		if err != nil {
			return JournalEntry{}, err
		}
		net[p.Amount.Currency] = sum
	}
	for _, c := range order {
		if net[c] != 0 {
			return JournalEntry{}, ledgerErr("entry %s unbalanced: debits minus credits in %s = %v", id, c.Code, OfMinor(net[c], c))
		}
	}
	return JournalEntry{id, description, append([]Posting(nil), postings...), kind, relatesTo, reason}, nil
}

func OriginalEntry(id, description string, postings []Posting) (JournalEntry, error) {
	return NewJournalEntry(id, description, postings, Original, "", "")
}

// Postings returns a copy: callers cannot reach into an entry.
func (e JournalEntry) Postings() []Posting { return append([]Posting(nil), e.postings...) }

func (e JournalEntry) Currency() Currency { return e.postings[0].Amount.Currency }

// DebitTotal is the sum of the debit legs: the "size" of the entry, used to bound partial reversals.
func (e JournalEntry) DebitTotal() int64 {
	var acc int64
	for _, p := range e.postings {
		if p.Side == Debit {
			acc += p.Amount.MinorUnits
		}
	}
	return acc
}

// Equal is Java record equality: every field, postings in order.
func (e JournalEntry) Equal(o JournalEntry) bool {
	if e.ID != o.ID || e.Description != o.Description || e.Kind != o.Kind || e.RelatesTo != o.RelatesTo || e.Reason != o.Reason || len(e.postings) != len(o.postings) {
		return false
	}
	for i := range e.postings {
		if e.postings[i] != o.postings[i] {
			return false
		}
	}
	return true
}

// Canonical is byte-identical to the Java version (a missing link prints as "null"), so the hash chain matches.
func (e JournalEntry) Canonical() string {
	var sb strings.Builder
	relates, reason := e.RelatesTo, e.Reason
	if e.Kind == Original {
		relates, reason = "null", "null"
	}
	sb.WriteString(e.ID + "|" + e.Kind.String() + "|" + relates + "|" + reason + "|" + e.Description)
	for _, p := range e.postings {
		sb.WriteString("|" + p.Canonical())
	}
	return sb.String()
}

// JournalRecord is a journal slot: the entry, its position, and a SHA-256 link to the previous slot. Tampering breaks the chain.
type JournalRecord struct {
	Seq      int64
	Entry    JournalEntry
	PrevHash string
	Hash     string
}

const Genesis = "0000000000000000000000000000000000000000000000000000000000000000"

func HashOf(seq int64, entry JournalEntry, prevHash string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s", prevHash, seq, entry.Canonical())))
	return hex.EncodeToString(digest[:])
}

// Ledger is an append-only journal. The public write methods are Post (originals), Reverse, ReversePartially,
// and Correct; none of them modifies or removes an existing entry. Every write is idempotent on its entry id.
type Ledger struct {
	accountOrder []string
	accounts     map[string]Account
	journal      []JournalRecord
	indexByID    map[string]int
	netDebits    map[string]int64
	// For each reversible entry: how much of each posting (by index) has been reversed so far, in minor units.
	reversedSoFar map[string][]int64
	children      map[string][]string
}

func NewLedger() *Ledger {
	return &Ledger{
		accounts: map[string]Account{}, indexByID: map[string]int{}, netDebits: map[string]int64{},
		reversedSoFar: map[string][]int64{}, children: map[string][]string{},
	}
}

func (l *Ledger) OpenAccount(id, name string, typ AccountType, currency Currency) (Account, error) {
	if _, exists := l.accounts[id]; exists {
		return Account{}, ledgerErr("account exists: %s", id)
	}
	account := Account{id, name, typ, currency}
	l.accounts[id] = account
	l.accountOrder = append(l.accountOrder, id)
	l.netDebits[id] = 0
	return account, nil
}

func (l *Ledger) Account(id string) (Account, error) {
	a, ok := l.accounts[id]
	if !ok {
		return Account{}, ledgerErr("unknown account: %s", id)
	}
	return a, nil
}

func (l *Ledger) Balance(accountID string) (Money, error) {
	a, err := l.Account(accountID)
	if err != nil {
		return Money{}, err
	}
	net := l.netDebits[accountID]
	if a.Type.NormalBalance() == Credit {
		net = -net
	}
	return OfMinor(net, a.Currency), nil
}

func (l *Ledger) Entry(id string) (JournalEntry, error) {
	i, ok := l.indexByID[id]
	if !ok {
		return JournalEntry{}, ledgerErr("unknown entry: %s", id)
	}
	return l.journal[i].Entry, nil
}

// Records returns a copy of the journal: a caller can edit the copy (the tamper demo does) but never the ledger.
func (l *Ledger) Records() []JournalRecord { return append([]JournalRecord(nil), l.journal...) }

func (l *Ledger) Size() int { return len(l.journal) }

// Post a new business event. Same id + same content is a no-op (returns false); same id + different content is a conflict.
func (l *Ledger) Post(entry JournalEntry) (bool, error) {
	if entry.Kind != Original {
		return false, ledgerErr("use Reverse, ReversePartially, or Correct for %v", entry.Kind)
	}
	replayed, err := l.replayed(entry)
	if err != nil || replayed {
		return false, err
	}
	if err := l.validateAccounts(entry); err != nil {
		return false, err
	}
	return true, l.append(entry)
}

// Reverse whatever remains of an entry. Idempotent on reversalID.
func (l *Ledger) Reverse(originalID, reversalID, reason string) (JournalEntry, error) {
	existing, found, err := l.existingChild(reversalID, originalID, Reversal)
	if err != nil || found {
		return existing, err
	}
	original, err := l.reversible(originalID)
	if err != nil {
		return JournalEntry{}, err
	}
	done := l.reversedSoFar[originalID]
	var flipped []Posting
	for i, p := range original.postings {
		if remaining := p.Amount.MinorUnits - done[i]; remaining > 0 {
			flipped = append(flipped, Posting{p.AccountID, p.Side.Opposite(), OfMinor(remaining, p.Amount.Currency)})
		}
	}
	if len(flipped) == 0 {
		return JournalEntry{}, fmt.Errorf("%w: entry %s is already fully reversed", ErrAlreadyReversed, originalID)
	}
	reversal, err := NewJournalEntry(reversalID, "reversal of "+originalID, flipped, Reversal, originalID, reason)
	if err != nil {
		return JournalEntry{}, err
	}
	return reversal, l.append(reversal)
}

// ReversePartially reverses amount of an entry (a partial refund). Each side's legs are allocated in proportion
// to what remains unreversed on each leg (largest remainder, Lesson 01), so no leg is ever over-reversed and the
// reversal is balanced. Idempotent on reversalID.
func (l *Ledger) ReversePartially(originalID, reversalID string, amount Money, reason string) (JournalEntry, error) {
	existing, found, err := l.existingChild(reversalID, originalID, Reversal)
	if err != nil {
		return JournalEntry{}, err
	}
	if found {
		if existing.DebitTotal() != amount.MinorUnits {
			return JournalEntry{}, fmt.Errorf("%w: entry id %s already posted with different content", ErrIdempotencyConflict, reversalID)
		}
		return existing, nil
	}
	original, err := l.reversible(originalID)
	if err != nil {
		return JournalEntry{}, err
	}
	if amount.Currency != original.Currency() {
		return JournalEntry{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, amount.Currency.Code, original.Currency().Code)
	}
	if !amount.IsPositive() {
		return JournalEntry{}, ledgerErr("partial reversal amount must be positive")
	}
	done := l.reversedSoFar[originalID]
	var remainingDebits int64
	for i, p := range original.postings {
		if p.Side == Debit {
			remainingDebits += p.Amount.MinorUnits - done[i]
		}
	}
	if remainingDebits == 0 {
		return JournalEntry{}, fmt.Errorf("%w: entry %s is already fully reversed", ErrAlreadyReversed, originalID)
	}
	if amount.MinorUnits > remainingDebits {
		return JournalEntry{}, fmt.Errorf("%w: cannot reverse %v of %s: only %v remains",
			ErrExcessiveReversal, amount, originalID, OfMinor(remainingDebits, amount.Currency))
	}
	var flipped []Posting
	for _, side := range []Side{Debit, Credit} {
		var legs []int
		for i, p := range original.postings {
			if p.Side == side {
				legs = append(legs, i)
			}
		}
		ratios := make([]int64, len(legs))
		for k, i := range legs {
			ratios[k] = original.postings[i].Amount.MinorUnits - done[i]
		}
		parts, err := amount.Allocate(ratios...)
		if err != nil {
			return JournalEntry{}, err
		}
		for k, i := range legs {
			if parts[k].IsZero() {
				continue
			}
			p := original.postings[i]
			flipped = append(flipped, Posting{p.AccountID, side.Opposite(), parts[k]})
		}
	}
	reversal, err := NewJournalEntry(reversalID, "partial reversal of "+originalID, flipped, Reversal, originalID, reason)
	if err != nil {
		return JournalEntry{}, err
	}
	return reversal, l.append(reversal)
}

// Correct an entry that was wrong when posted: fully reverse it and post the replacement, linked to the original,
// as one atomic step. Both are validated before either is appended. Idempotent on (reversalID, replacementID).
func (l *Ledger) Correct(originalID, reversalID, replacementID string, replacementPostings []Posting, reason string) (JournalEntry, error) {
	replacement, err := NewJournalEntry(replacementID, "correction of "+originalID, replacementPostings, Correction, originalID, reason)
	if err != nil {
		return JournalEntry{}, err
	}
	_, priorReversal, err := l.existingChild(reversalID, originalID, Reversal)
	if err != nil {
		return JournalEntry{}, err
	}
	_, replacementExists := l.indexByID[replacementID]
	if priorReversal && replacementExists {
		existing, _ := l.Entry(replacementID)
		if !existing.Equal(replacement) {
			return JournalEntry{}, fmt.Errorf("%w: entry id %s already posted with different content", ErrIdempotencyConflict, replacementID)
		}
		return replacement, nil
	}
	if priorReversal || replacementExists {
		return JournalEntry{}, ledgerErr("correction of %s was half-applied; ids already in use", originalID)
	}
	original, err := l.reversible(originalID)
	if err != nil {
		return JournalEntry{}, err
	}
	for _, v := range l.reversedSoFar[originalID] {
		if v != 0 {
			return JournalEntry{}, ledgerErr("entry %s already has reversals; correct the remaining amount explicitly", originalID)
		}
	}
	if err := l.validateAccounts(replacement); err != nil {
		return JournalEntry{}, err
	}
	flipped := make([]Posting, 0, len(original.postings))
	for _, p := range original.postings {
		flipped = append(flipped, Posting{p.AccountID, p.Side.Opposite(), p.Amount})
	}
	reversal, err := NewJournalEntry(reversalID, "reversal of "+originalID+" for correction", flipped, Reversal, originalID, reason)
	if err != nil {
		return JournalEntry{}, err
	}
	if err := l.append(reversal); err != nil {
		return JournalEntry{}, err
	}
	return replacement, l.append(replacement)
}

func (l *Ledger) IsFullyReversed(id string) (bool, error) {
	e, err := l.Entry(id)
	if err != nil {
		return false, err
	}
	done, ok := l.reversedSoFar[id]
	if !ok {
		return false, nil
	}
	for i, p := range e.postings {
		if done[i] != p.Amount.MinorUnits {
			return false, nil
		}
	}
	return true, nil
}

// Remaining is the amount of the entry not yet reversed (debit side), e.g. what can still be refunded.
func (l *Ledger) Remaining(id string) (Money, error) {
	e, err := l.Entry(id)
	if err != nil {
		return Money{}, err
	}
	done := l.reversedSoFar[id]
	var rem int64
	for i, p := range e.postings {
		if p.Side == Debit {
			rem += p.Amount.MinorUnits
			if done != nil {
				rem -= done[i]
			}
		}
	}
	return OfMinor(rem, e.Currency()), nil
}

// History is the entry and everything that relates to it, transitively, in journal order: the audit story of one event.
func (l *Ledger) History(id string) ([]JournalRecord, error) {
	if _, err := l.Entry(id); err != nil {
		return nil, err
	}
	var ids []string
	l.collect(id, &ids)
	out := make([]JournalRecord, 0, len(ids))
	for _, x := range ids {
		out = append(out, l.journal[l.indexByID[x]])
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Seq < out[b].Seq })
	return out, nil
}

// AuditView is one line per record: seq, kind, id, link, reason, net effect. What an auditor reads.
func (l *Ledger) AuditView() []string {
	lines := make([]string, 0, len(l.journal))
	for _, r := range l.journal {
		e := r.Entry
		link := ""
		if e.RelatesTo != "" {
			link = "-> " + e.RelatesTo
		}
		lines = append(lines, fmt.Sprintf("#%d %-10v %-9s %-13s %-31s debits %-11v hash %s",
			r.Seq, e.Kind, e.ID, link, e.Reason, OfMinor(e.DebitTotal(), e.Currency()), r.Hash[:12]))
	}
	return lines
}

// VerifyChain recomputes every hash from the genesis value. Returns the seq of the first broken record, or -1 if intact.
func VerifyChain(records []JournalRecord) int64 {
	prev := Genesis
	for i, r := range records {
		if r.Seq != int64(i+1) || r.PrevHash != prev || r.Hash != HashOf(r.Seq, r.Entry, prev) {
			return int64(i + 1)
		}
		prev = r.Hash
	}
	return -1
}

// ---- internals ----

func (l *Ledger) replayed(entry JournalEntry) (bool, error) {
	i, ok := l.indexByID[entry.ID]
	if !ok {
		return false, nil
	}
	if l.journal[i].Entry.Equal(entry) {
		return true, nil
	}
	return false, fmt.Errorf("%w: entry id %s already posted with different content", ErrIdempotencyConflict, entry.ID)
}

func (l *Ledger) existingChild(childID, originalID string, kind EntryKind) (JournalEntry, bool, error) {
	i, ok := l.indexByID[childID]
	if !ok {
		return JournalEntry{}, false, nil
	}
	e := l.journal[i].Entry
	if e.Kind != kind || e.RelatesTo != originalID {
		return JournalEntry{}, false, fmt.Errorf("%w: entry id %s already posted with different content", ErrIdempotencyConflict, childID)
	}
	return e, true, nil
}

func (l *Ledger) reversible(id string) (JournalEntry, error) {
	e, err := l.Entry(id)
	if err != nil {
		return JournalEntry{}, err
	}
	if e.Kind == Reversal {
		return JournalEntry{}, ledgerErr("entry %s is a reversal; post a new original instead of reversing a reversal", id)
	}
	return e, nil
}

func (l *Ledger) validateAccounts(entry JournalEntry) error {
	for _, p := range entry.postings {
		a, err := l.Account(p.AccountID)
		if err != nil {
			return err
		}
		if a.Currency != p.Amount.Currency {
			return ledgerErr("entry %s: account %s is not %s", entry.ID, p.AccountID, p.Amount.Currency.Code)
		}
	}
	return nil
}

func (l *Ledger) append(entry JournalEntry) error {
	seq := int64(len(l.journal) + 1)
	prev := Genesis
	if len(l.journal) > 0 {
		prev = l.journal[len(l.journal)-1].Hash
	}
	for _, p := range entry.postings {
		if _, err := addChecked(l.netDebits[p.AccountID], p.SignedMinor()); err != nil {
			return err
		}
	}
	l.journal = append(l.journal, JournalRecord{seq, entry, prev, HashOf(seq, entry, prev)})
	l.indexByID[entry.ID] = int(seq - 1)
	for _, p := range entry.postings {
		l.netDebits[p.AccountID] += p.SignedMinor()
	}
	if entry.Kind != Reversal {
		l.reversedSoFar[entry.ID] = make([]int64, len(entry.postings))
	}
	if entry.RelatesTo != "" {
		l.children[entry.RelatesTo] = append(l.children[entry.RelatesTo], entry.ID)
		if entry.Kind == Reversal {
			l.applyReversal(entry)
		}
	}
	return nil
}

// applyReversal attributes each reversal leg to the original's legs (same account, opposite side), never exceeding a leg.
func (l *Ledger) applyReversal(reversal JournalEntry) {
	original, _ := l.Entry(reversal.RelatesTo)
	done := l.reversedSoFar[original.ID]
	for _, r := range reversal.postings {
		left := r.Amount.MinorUnits
		for i := 0; i < len(original.postings) && left > 0; i++ {
			o := original.postings[i]
			if o.AccountID != r.AccountID || o.Side != r.Side.Opposite() {
				continue
			}
			take := min(left, o.Amount.MinorUnits-done[i])
			done[i] += take
			left -= take
		}
		if left != 0 {
			panic(fmt.Sprintf("reversal %s exceeds original %s", reversal.ID, original.ID)) // programming error, as in Java
		}
	}
}

func (l *Ledger) collect(id string, out *[]string) {
	*out = append(*out, id)
	for _, child := range l.children[id] {
		l.collect(child, out)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func printEntry(e JournalEntry) {
	line := fmt.Sprintf("%v %s", e.Kind, e.ID)
	if e.RelatesTo != "" {
		line += fmt.Sprintf(" -> %s (%s)", e.RelatesTo, e.Reason)
	}
	fmt.Println(line)
	for _, p := range e.Postings() {
		prefix := "      Cr "
		if p.Side == Debit {
			prefix = "  Dr "
		}
		fmt.Printf("%s%s  %v\n", prefix, p.AccountID, p.Amount)
	}
}

func ids(records []JournalRecord) []string {
	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, r.Entry.ID)
	}
	return out
}

func main() {
	ledger := NewLedger()
	must(ledger.OpenAccount("1200", "Processor Receivable", Asset, USD))
	must(ledger.OpenAccount("2100", "Merchant Payable", Liability, USD))
	must(ledger.OpenAccount("4100", "Fee Revenue", Revenue, USD))

	fmt.Println("== A 120.00 USD sale with a 2.9% fee ==")
	gross := must(Parse("120.00", USD))
	fee := must(gross.Times(29, 1000, HalfEven))
	sale := must(OriginalEntry("pay-100", "card payment captured", []Posting{
		must(DebitPosting("1200", gross)), must(CreditPosting("2100", must(gross.Minus(fee)))), must(CreditPosting("4100", fee))}))
	must(ledger.Post(sale))
	printEntry(sale)

	fmt.Println("\n== Partial refund of 50.00: fee and payable reversed in proportion ==")
	r1 := must(ledger.ReversePartially("pay-100", "ref-100-1", must(Parse("50.00", USD)), "customer returned 1 of 2 items"))
	printEntry(r1)
	again := must(ledger.ReversePartially("pay-100", "ref-100-1", must(Parse("50.00", USD)), "retry of the same refund"))
	fmt.Printf("retry of ref-100-1 returns the same entry: %v, journal size %d\n", again.Equal(r1), ledger.Size())
	fmt.Printf("remaining refundable: %v\n", must(ledger.Remaining("pay-100")))
	if _, err := ledger.ReversePartially("pay-100", "ref-100-2", must(Parse("80.00", USD)), "refund too large"); errors.Is(err, ErrExcessiveReversal) {
		fmt.Println("rejected:", err)
	}

	fmt.Println("\n== Full reversal of what remains (chargeback) ==")
	r2 := must(ledger.Reverse("pay-100", "cb-100", "chargeback 10.4 fraud"))
	printEntry(r2)
	fmt.Printf("fully reversed: %v; balances 1200 %v, 2100 %v, 4100 %v\n", must(ledger.IsFullyReversed("pay-100")),
		must(ledger.Balance("1200")), must(ledger.Balance("2100")), must(ledger.Balance("4100")))
	if _, err := ledger.Reverse("pay-100", "cb-100b", "duplicate chargeback notice"); errors.Is(err, ErrAlreadyReversed) {
		fmt.Println("rejected:", err)
	}

	fmt.Println("\n== Correction: a fee booked at 3.9% instead of 2.9% ==")
	gross2 := must(Parse("80.00", USD))
	wrongFee := must(gross2.Times(39, 1000, HalfEven))
	must(ledger.Post(must(OriginalEntry("pay-200", "card payment captured", []Posting{
		must(DebitPosting("1200", gross2)), must(CreditPosting("2100", must(gross2.Minus(wrongFee)))), must(CreditPosting("4100", wrongFee))}))))
	rightFee := must(gross2.Times(29, 1000, HalfEven))
	fixed := must(ledger.Correct("pay-200", "rev-200", "pay-200c", []Posting{
		must(DebitPosting("1200", gross2)), must(CreditPosting("2100", must(gross2.Minus(rightFee)))), must(CreditPosting("4100", rightFee))},
		"fee rate config error INC-4471"))
	printEntry(must(ledger.Entry("rev-200")))
	printEntry(fixed)
	fmt.Printf("fee revenue now %v (2.32 on pay-200, 0.00 net on pay-100)\n", must(ledger.Balance("4100")))

	fmt.Println("\n== Audit view: nothing was edited, everything is linked ==")
	for _, line := range ledger.AuditView() {
		fmt.Println(line)
	}
	fmt.Println("history of pay-100:", ids(must(ledger.History("pay-100"))))
	fmt.Printf("hash chain intact: %v\n", VerifyChain(ledger.Records()) == -1)

	tampered := ledger.Records()
	victim := tampered[2]
	edited := must(OriginalEntry("cb-100", "quietly edited", victim.Entry.Postings()))
	tampered[2] = JournalRecord{victim.Seq, edited, victim.PrevHash, victim.Hash}
	fmt.Printf("after editing record #3 in a copy, first broken seq = %d\n", VerifyChain(tampered))
}
