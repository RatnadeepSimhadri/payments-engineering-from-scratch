// Project: projects/ledger-engine — stage 1: account types, normal balance side, account registry.
// Docs: projects/ledger-engine/stages/01-money-and-accounts/docs/en.md
// Run:  go run .
// Test: python3 scripts/project_test.py ledger-engine --lang go --all --solution
// Go 1.22+, standard library only. Same design as the Java and Python solutions.
package main

import (
	"errors"
	"fmt"
	"strings"
)

// Side is which column a posting lands in. Debit and credit are directions, not "minus" and "plus".
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

// AccountType is one of the five account classes of double-entry bookkeeping. The class decides which side increases the account.
type AccountType int

const (
	Asset AccountType = iota
	Liability
	Equity
	Revenue
	Expense
)

// NormalSide: assets and expenses grow on the debit side; liabilities, equity, and revenue grow on the credit side.
func (t AccountType) NormalSide() Side {
	if t == Asset || t == Expense {
		return Debit
	}
	return Credit
}

func (t AccountType) String() string {
	return [...]string{"ASSET", "LIABILITY", "EQUITY", "REVENUE", "EXPENSE"}[t]
}

// Account identity never changes: id, class, and currency are fixed when it is opened.
type Account struct {
	ID       string
	Type     AccountType
	Currency Currency
}

// NewAccount validates what the Java compact constructor validates: a blank id or a zero currency is rejected.
func NewAccount(id string, accountType AccountType, currency Currency) (Account, error) {
	if strings.TrimSpace(id) == "" {
		return Account{}, errors.New("account id must not be blank")
	}
	if currency == (Currency{}) {
		return Account{}, errors.New("account needs a currency")
	}
	return Account{id, accountType, currency}, nil
}

// NormalSide is the side on which this account's balance grows.
func (a Account) NormalSide() Side { return a.Type.NormalSide() }

// ErrLedger is the base of every rule the ledger enforces, so callers can test "the ledger said no" in one
// place: every more specific sentinel below wraps it, so errors.Is(err, ErrLedger) holds for all of them.
var (
	ErrLedger           = errors.New("ledger rule violated")
	ErrDuplicateAccount = fmt.Errorf("%w: account already exists", ErrLedger)
	ErrUnknownAccount   = fmt.Errorf("%w: unknown account", ErrLedger)
)

// AccountRegistry is the chart of accounts: every account the ledger knows, in opening order. Ids are unique. Not thread-safe.
type AccountRegistry struct {
	order    []string
	accounts map[string]Account
}

func NewAccountRegistry() *AccountRegistry {
	return &AccountRegistry{accounts: map[string]Account{}}
}

// Open an account. The same id can never be opened twice, even with identical attributes.
func (r *AccountRegistry) Open(id string, accountType AccountType, currency Currency) (Account, error) {
	account, err := NewAccount(id, accountType, currency)
	if err != nil {
		return Account{}, err
	}
	if _, exists := r.accounts[account.ID]; exists {
		return Account{}, fmt.Errorf("%w: %s", ErrDuplicateAccount, account.ID)
	}
	r.accounts[account.ID] = account
	r.order = append(r.order, account.ID)
	return account, nil
}

// Get looks an account up; an unknown id is an error, never a zero Account.
func (r *AccountRegistry) Get(id string) (Account, error) {
	account, ok := r.accounts[id]
	if !ok {
		return Account{}, fmt.Errorf("%w: %s", ErrUnknownAccount, id)
	}
	return account, nil
}

func (r *AccountRegistry) Contains(id string) bool {
	_, ok := r.accounts[id]
	return ok
}

func (r *AccountRegistry) Size() int { return len(r.accounts) }

// All returns the accounts in opening order; a fresh slice, so the chart cannot be edited from outside.
func (r *AccountRegistry) All() []Account {
	out := make([]Account, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.accounts[id])
	}
	return out
}
