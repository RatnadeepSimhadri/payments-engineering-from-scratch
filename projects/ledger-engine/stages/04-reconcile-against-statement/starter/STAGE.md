# Stage 4 — Reconcile Against a Statement

Continue editing the workspace created at stage 1. No new starter files. Put the value types in a new `Reconciliation.java`; add `lines` and `reconcile` to `Ledger`. Read `../docs/en.md` for the two-pass algorithm and the worked example with the numbers the grader checks.

## New signatures

```java
record StatementLine(String ref, Money amount, String date) {}   // blank ref/date -> IllegalArgumentException; date is ISO-8601 "2026-10-01"
record LedgerLine(String ref, Money amount, String date) {}       // ref = entry id; amount = entry's net effect, signed on the account's normal side

enum BreakType { MISSING_IN_LEDGER, MISSING_IN_STATEMENT, AMOUNT_MISMATCH, DUPLICATE }
enum MatchedBy { REF, AMOUNT_AND_DATE }

record ReconciliationMatch(String ledgerRef, String statementRef, Money amount, MatchedBy by) {}
record ReconciliationBreak(BreakType type, String ref, Money ledgerAmount, Money statementAmount) {}   // absent side = currency zero

record ReconciliationReport(String accountId, Currency currency, List<ReconciliationMatch> matches,
                            List<ReconciliationBreak> breaks, Money ledgerTotal, Money statementTotal) {
    // compact ctor: matches = List.copyOf(matches); breaks = List.copyOf(breaks)
    Money matchedTotal();
    Money ledgerBreakTotal();                 // sum of breaks' ledgerAmount
    Money statementBreakTotal();              // sum of breaks' statementAmount
    List<ReconciliationBreak> breaksOf(BreakType type);
    boolean tiesOut();                        // ledgerTotal == matched + ledgerBreaks && statementTotal == matched + statementBreaks
    boolean isClean();                        // breaks.isEmpty()
}

final class Ledger {
    List<LedgerLine> lines(String accountId);                                                   // one per entry touching the account, journal order
    ReconciliationReport reconcile(String accountId, List<StatementLine> statement, int windowDays);
}
```

## New signatures (Python)

Keep everything in the one `main.py` the stage-1 starter gave you.

```python
@dataclass(frozen=True)
class StatementLine:              # ref: str, amount: Money, date: str; blank ref or date -> ValueError
@dataclass(frozen=True)
class LedgerLine:                 # ref: str, amount: Money, date: str; ref = entry id, amount signed on the account's normal side

class BreakType(Enum): MISSING_IN_LEDGER, MISSING_IN_STATEMENT, AMOUNT_MISMATCH, DUPLICATE
class MatchedBy(Enum): REF, AMOUNT_AND_DATE

@dataclass(frozen=True)
class ReconciliationMatch:        # ledger_ref: str, statement_ref: str, amount: Money, by: MatchedBy
@dataclass(frozen=True)
class ReconciliationBreak:        # type: BreakType, ref: str, ledger_amount: Money, statement_amount: Money

@dataclass(frozen=True)
class ReconciliationReport:       # account_id, currency, matches: tuple[...], breaks: tuple[...], ledger_total: Money, statement_total: Money
    def matched_total(self) -> Money: ...
    def ledger_break_total(self) -> Money: ...          # sum of breaks' ledger_amount
    def statement_break_total(self) -> Money: ...       # sum of breaks' statement_amount
    def breaks_of(self, type: BreakType) -> list[ReconciliationBreak]: ...
    def ties_out(self) -> bool: ...
    def is_clean(self) -> bool: ...                     # not self.breaks

class Ledger:
    def lines(self, account_id: str) -> list[LedgerLine]: ...
    def reconcile(self, account_id: str, statement: Sequence[StatementLine], window_days: int) -> ReconciliationReport: ...
    # window_days < 0 -> ValueError; wrong currency -> CurrencyMismatchError; bad date -> LedgerError (datetime.date.fromisoformat)
```

## New signatures (Go)

Add `reconciliation.go`; add `Lines` and `Reconcile` to `ledger.go`.

```go
type StatementLine struct{ Ref string; Amount Money; Date string }
func NewStatementLine(ref string, amount Money, date string) (StatementLine, error)   // blank ref/date -> error
type LedgerLine struct{ Ref string; Amount Money; Date string }

type BreakType int; const ( MissingInLedger BreakType = iota; MissingInStatement; AmountMismatch; Duplicate )
type MatchedBy int; const ( ByRef MatchedBy = iota; ByAmountAndDate )
type ReconciliationMatch struct{ LedgerRef, StatementRef string; Amount Money; By MatchedBy }
type ReconciliationBreak struct{ Type BreakType; Ref string; LedgerAmount, StatementAmount Money }
type ReconciliationReport struct{ AccountID string; Currency Currency; Matches []ReconciliationMatch; Breaks []ReconciliationBreak; LedgerTotal, StatementTotal Money }
func (r ReconciliationReport) MatchedTotal() Money
func (r ReconciliationReport) LedgerBreakTotal() Money
func (r ReconciliationReport) StatementBreakTotal() Money
func (r ReconciliationReport) BreaksOf(kind BreakType) []ReconciliationBreak
func (r ReconciliationReport) TiesOut() bool
func (r ReconciliationReport) IsClean() bool

var ErrBadDate error   // wraps ErrLedger
func ParseDate(text string) (time.Time, error)                        // time.Parse("2006-01-02", text); ErrBadDate otherwise
func WithinWindow(a, b string, windowDays int) (bool, error)
func (l *Ledger) Lines(accountID string) ([]LedgerLine, error)
func (l *Ledger) Reconcile(accountID string, statement []StatementLine, windowDays int) (ReconciliationReport, error)
// windowDays < 0 -> error; wrong currency -> ErrCurrencyMismatch; bad date -> ErrBadDate (which errors.Is ErrLedger)
```

`time.Parse` with the layout `2006-01-02` is the strict ISO-8601 date parser; the day difference is `b.Sub(a).Hours() / 24` because both values are midnight UTC. Build the statement with struct literals in tests and demos (`StatementLine{"sale-1", amount, "2026-10-01"}`); `NewStatementLine` is the validating constructor for input you do not trust.

## Algorithm in one screen

0. Validate: unknown account → `UnknownAccountException`; `windowDays < 0` → `IllegalArgumentException`; statement currency != account currency → `CurrencyMismatchException`; unparseable date → `LedgerException`. `ledgerTotal` = sum of lines; `statementTotal` = sum of every statement line (duplicates included).
1. **Pass 1 (by ref), statement order:** ref repeated within the statement → `DUPLICATE`; ref matches a ledger line with equal amount → match `REF`; with a different amount → `AMOUNT_MISMATCH`; ref unknown → keep for pass 2. Matched or mismatched ledger lines are consumed.
2. **Pass 2 (by amount + date), statement order:** first unconsumed ledger line in journal order with equal signed amount and `|days apart| <= windowDays` → match `AMOUNT_AND_DATE`; none → `MISSING_IN_LEDGER`.
3. Every ledger line still unconsumed → `MISSING_IN_STATEMENT`.

Matches: pass 1 first, then pass 2. `reconcile` never changes the journal.

## Grade

```bash
python3 scripts/project_test.py ledger-engine --lang java --stage 4 --path <workspace>
python3 scripts/project_test.py ledger-engine --lang python --stage 4 --path <workspace>
python3 scripts/project_test.py ledger-engine --lang go --stage 4 --path <workspace>
python3 scripts/project_test.py ledger-engine --lang java --all --path <workspace>
python3 scripts/project_test.py ledger-engine --lang python --all --path <workspace>
python3 scripts/project_test.py ledger-engine --lang go --all --path <workspace>
```
