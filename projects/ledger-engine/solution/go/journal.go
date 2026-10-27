// Project: projects/ledger-engine — stage 2: postings, journal entries, and the posted record.
// Docs: projects/ledger-engine/stages/02-balanced-journal-entries/docs/en.md
// Run:  go run .
// Test: python3 scripts/project_test.py ledger-engine --lang go --all --solution
// Go 1.22+, standard library only. Same design as the Java and Python solutions.
package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Posting is one leg of an entry: a positive amount on one side of one account.
type Posting struct {
	AccountID string
	Side      Side
	Amount    Money
}

// NewPosting rejects a blank account id and a non-positive amount.
func NewPosting(accountID string, side Side, amount Money) (Posting, error) {
	if strings.TrimSpace(accountID) == "" {
		return Posting{}, errors.New("posting needs an account id")
	}
	if !amount.IsPositive() {
		return Posting{}, fmt.Errorf("posting amount must be positive: %v", amount)
	}
	return Posting{accountID, side, amount}, nil
}

// Mirrored is the posting with its side flipped: the building block of a reversal (stage 3).
func (p Posting) Mirrored() Posting { return Posting{p.AccountID, p.Side.Opposite(), p.Amount} }

// JournalEntry is an atomic group of postings identified by a caller-chosen id (the idempotency key)
// and an ISO-8601 booking date ("2026-10-01"). Immutable by convention: NewJournalEntry copies the
// postings, and Ledger.Post never hands out its own slice. Construction does not check balance;
// Ledger.Post does, so a bad entry can be built, offered, and refused without touching the journal.
type JournalEntry struct {
	ID       string
	Date     string
	Postings []Posting
}

// ErrNoPostings is returned by JournalEntry.Currency on an entry without postings (IllegalStateException in Java).
var ErrNoPostings = errors.New("entry has no postings")

// NewJournalEntry rejects a blank id or date and copies the postings.
func NewJournalEntry(id, date string, postings ...Posting) (JournalEntry, error) {
	if strings.TrimSpace(id) == "" {
		return JournalEntry{}, errors.New("entry id must not be blank")
	}
	if strings.TrimSpace(date) == "" {
		return JournalEntry{}, errors.New("entry date must not be blank")
	}
	return JournalEntry{id, date, slices.Clone(postings)}, nil
}

// Currency is the currency of the first posting; Ledger.Post guarantees every posting shares it.
func (e JournalEntry) Currency() (Currency, error) {
	if len(e.Postings) == 0 {
		return Currency{}, ErrNoPostings
	}
	return e.Postings[0].Amount.Currency, nil
}

// Equal is record equality: same id, date, and postings in order (a struct holding a slice cannot use ==).
func (e JournalEntry) Equal(other JournalEntry) bool {
	return e.ID == other.ID && e.Date == other.Date && slices.Equal(e.Postings, other.Postings)
}

// PostedEntry is what the journal actually stores: the entry plus the sequence number it was given on arrival (1, 2, 3, ...).
type PostedEntry struct {
	Sequence int64
	Entry    JournalEntry
}

func (p PostedEntry) ID() string { return p.Entry.ID }

func (p PostedEntry) Equal(other PostedEntry) bool {
	return p.Sequence == other.Sequence && p.Entry.Equal(other.Entry)
}

// The three ways Ledger.Post says no, all wrapping ErrLedger.
var (
	ErrInvalidEntry    = fmt.Errorf("%w: invalid entry", ErrLedger)
	ErrUnbalancedEntry = fmt.Errorf("%w: unbalanced entry", ErrLedger)
	ErrUnknownEntry    = fmt.Errorf("%w: unknown entry", ErrLedger)
)
