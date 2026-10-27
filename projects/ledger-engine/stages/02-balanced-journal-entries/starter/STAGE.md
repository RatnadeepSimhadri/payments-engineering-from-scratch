# Stage 2 — Balanced Journal Entries

Continue editing the workspace created at stage 1 (`--init`). There are no new starter files; add the types below and keep every stage-1 name unchanged. Read `../docs/en.md` for the rules and the worked example.

Suggested new files: `Journal.java` (value types and exceptions) and `Ledger.java` (the journal). Java 21, standard library only.

## New signatures

```java
record Posting(String accountId, Side side, Money amount) {
    // guards: blank accountId -> IllegalArgumentException; amount must be positive -> IllegalArgumentException
    Posting mirrored();                       // same account and amount, opposite side
}

record JournalEntry(String id, String date, List<Posting> postings) {
    // guards: blank id or date -> IllegalArgumentException; postings = List.copyOf(postings)
    JournalEntry(String id, String date, Posting... postings);
    Currency currency();                      // first posting's currency; IllegalStateException if none
}

record PostedEntry(long sequence, JournalEntry entry) {
    String id();
}

final class InvalidEntryException    extends LedgerException { InvalidEntryException(String id, String why); }
final class UnbalancedEntryException extends LedgerException { UnbalancedEntryException(String id, Money debits, Money credits); }
final class UnknownEntryException    extends LedgerException { UnknownEntryException(String id); }

final class Ledger {
    Ledger(AccountRegistry accounts);         // null -> IllegalArgumentException
    AccountRegistry accounts();
    PostedEntry post(JournalEntry entry);     // idempotent on id; validates before writing; sequence = size + 1
    List<PostedEntry> journal();              // unmodifiable copy in sequence order
    PostedEntry entry(String id);             // UnknownEntryException if absent
    boolean hasEntry(String id);
    int size();
}
```

## New signatures (Python)

Keep everything in the one `main.py` the stage-1 starter gave you.

```python
@dataclass(frozen=True)
class Posting:                    # account_id: str, side: Side, amount: Money; blank id or non-positive amount -> ValueError
    def mirrored(self) -> Posting: ...

@dataclass(frozen=True)
class JournalEntry:               # id: str, date: str, postings: tuple[Posting, ...] (copied from any sequence); blank id/date -> ValueError
    @staticmethod
    def of(id: str, date: str, *postings: Posting) -> JournalEntry: ...   # the varargs constructor
    def currency(self) -> Currency: ...                                    # first posting's; RuntimeError if none

@dataclass(frozen=True)
class PostedEntry:                # sequence: int, entry: JournalEntry
    @property
    def id(self) -> str: ...      # entry.id

class InvalidEntryError(LedgerError):     def __init__(self, entry_id: str, why: str)
class UnbalancedEntryError(LedgerError):  def __init__(self, entry_id: str, debits: Money, credits: Money)
class UnknownEntryError(LedgerError):     def __init__(self, entry_id: str)

class Ledger:
    def __init__(self, accounts: AccountRegistry) -> None: ...   # not a registry -> ValueError
    def accounts(self) -> AccountRegistry: ...
    def post(self, entry: JournalEntry) -> PostedEntry: ...
    def journal(self) -> tuple[PostedEntry, ...]: ...           # sequence order, immutable copy
    def entry(self, entry_id: str) -> PostedEntry: ...          # UnknownEntryError if absent
    def has_entry(self, entry_id: str) -> bool: ...
    def size(self) -> int: ...
```

## New signatures (Go)

Add `journal.go` and `ledger.go` next to the stage-1 files; same `package main`.

```go
type Posting struct{ AccountID string; Side Side; Amount Money }
func NewPosting(accountID string, side Side, amount Money) (Posting, error)   // blank id or non-positive amount -> error
func (p Posting) Mirrored() Posting

type JournalEntry struct{ ID, Date string; Postings []Posting }
func NewJournalEntry(id, date string, postings ...Posting) (JournalEntry, error)   // blank id/date -> error; clones the postings
func (e JournalEntry) Currency() (Currency, error)                                 // first posting's; ErrNoPostings if none
func (e JournalEntry) Equal(other JournalEntry) bool                               // record equality (a struct holding a slice cannot use ==)

type PostedEntry struct{ Sequence int64; Entry JournalEntry }
func (p PostedEntry) ID() string
func (p PostedEntry) Equal(other PostedEntry) bool

var ErrInvalidEntry, ErrUnbalancedEntry, ErrUnknownEntry, ErrNoPostings error   // the first three wrap ErrLedger

func NewLedger(accounts *AccountRegistry) (*Ledger, error)   // nil -> error
func (l *Ledger) Accounts() *AccountRegistry
func (l *Ledger) Post(entry JournalEntry) (PostedEntry, error)
func (l *Ledger) Journal() []PostedEntry                     // sequence order, a fresh slice
func (l *Ledger) Entry(id string) (PostedEntry, error)       // ErrUnknownEntry if absent
func (l *Ledger) HasEntry(id string) bool
func (l *Ledger) Size() int
```

Go has no unmodifiable collections: "read-only" means `Journal()` returns `slices.Clone(l.journal)` and `Post` stores a clone of the entry's postings, so neither the caller's slice nor the returned one aliases the journal. The grader edits what it is given and checks the ledger did not move.

## Validation order inside `post`

1. Id already posted → return the existing `PostedEntry`, change nothing.
2. Fewer than two postings → `InvalidEntryException`.
3. For each posting: unknown account → `UnknownAccountException`; posting currency differs from the entry's → `CurrencyMismatchException`; account currency differs from the entry's → `CurrencyMismatchException`.
4. Sum of debits != sum of credits → `UnbalancedEntryException`.
5. Append, index by id, return. Rejected entries never consume a sequence number.

## Grade

```bash
python3 scripts/project_test.py ledger-engine --lang java --stage 2 --path <workspace>
python3 scripts/project_test.py ledger-engine --lang python --stage 2 --path <workspace>
python3 scripts/project_test.py ledger-engine --lang go --stage 2 --path <workspace>
```
