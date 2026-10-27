// Project: projects/ledger-engine — stage 1 starter (Go): account types, normal balance side, account registry.
// Docs: projects/ledger-engine/stages/01-money-and-accounts/docs/en.md
// Run:  go run .
// Grade: python3 scripts/project_test.py ledger-engine --lang go --stage 1 --path <workspace>
// Every function below panics until you implement it. Keep the names: the graders of every stage use them.
// Go 1.22+, standard library only.
package main

import (
	"errors"
	"fmt"
)

// Side is which column a posting lands in. Debit and credit are directions, not "minus" and "plus".
type Side int

const (
	Debit Side = iota
	Credit
)

// Opposite: Debit <-> Credit.
func (s Side) Opposite() Side {
	panic("not implemented: TODO stage 01 Side.Opposite")
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
	panic("not implemented: TODO stage 01 AccountType.NormalSide")
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

// NewAccount rejects a blank (or whitespace-only) id and a zero Currency{} with a plain error.
func NewAccount(id string, accountType AccountType, currency Currency) (Account, error) {
	panic("not implemented: TODO stage 01 NewAccount: reject a blank id and a zero currency")
}

// NormalSide is the side on which this account's balance grows (delegates to the type).
func (a Account) NormalSide() Side {
	panic("not implemented: TODO stage 01 Account.NormalSide")
}

// ErrLedger is the base of every rule the ledger enforces, so callers can test "the ledger said no" in one
// place: every more specific sentinel below wraps it, so errors.Is(err, ErrLedger) holds for all of them.
// Given; later stages add more sentinels that wrap ErrLedger the same way.
var (
	ErrLedger           = errors.New("ledger rule violated")
	ErrDuplicateAccount = fmt.Errorf("%w: account already exists", ErrLedger)
	ErrUnknownAccount   = fmt.Errorf("%w: unknown account", ErrLedger)
)

// AccountRegistry is the chart of accounts: every account the ledger knows, in opening order. Ids are unique. Not thread-safe.
// Go maps do not keep insertion order, so the registry keeps the ids in a slice next to the map.
type AccountRegistry struct {
	order    []string
	accounts map[string]Account
}

func NewAccountRegistry() *AccountRegistry {
	return &AccountRegistry{accounts: map[string]Account{}}
}

// Open an account. The same id can never be opened twice, even with identical attributes -> ErrDuplicateAccount
// (wrapped with the id: fmt.Errorf("%w: %s", ErrDuplicateAccount, id)), checked before anything changes.
func (r *AccountRegistry) Open(id string, accountType AccountType, currency Currency) (Account, error) {
	panic("not implemented: TODO stage 01 AccountRegistry.Open")
}

// Get looks an account up; an unknown id is ErrUnknownAccount, never a zero Account.
func (r *AccountRegistry) Get(id string) (Account, error) {
	panic("not implemented: TODO stage 01 AccountRegistry.Get")
}

func (r *AccountRegistry) Contains(id string) bool {
	panic("not implemented: TODO stage 01 AccountRegistry.Contains")
}

func (r *AccountRegistry) Size() int {
	panic("not implemented: TODO stage 01 AccountRegistry.Size")
}

// All returns the accounts in opening order as a fresh slice, so the chart cannot be edited from outside.
func (r *AccountRegistry) All() []Account {
	panic("not implemented: TODO stage 01 AccountRegistry.All")
}
