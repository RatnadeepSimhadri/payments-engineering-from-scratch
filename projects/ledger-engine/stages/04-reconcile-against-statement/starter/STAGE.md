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

## Algorithm in one screen

0. Validate: unknown account → `UnknownAccountException`; `windowDays < 0` → `IllegalArgumentException`; statement currency != account currency → `CurrencyMismatchException`; unparseable date → `LedgerException`. `ledgerTotal` = sum of lines; `statementTotal` = sum of every statement line (duplicates included).
1. **Pass 1 (by ref), statement order:** ref repeated within the statement → `DUPLICATE`; ref matches a ledger line with equal amount → match `REF`; with a different amount → `AMOUNT_MISMATCH`; ref unknown → keep for pass 2. Matched or mismatched ledger lines are consumed.
2. **Pass 2 (by amount + date), statement order:** first unconsumed ledger line in journal order with equal signed amount and `|days apart| <= windowDays` → match `AMOUNT_AND_DATE`; none → `MISSING_IN_LEDGER`.
3. Every ledger line still unconsumed → `MISSING_IN_STATEMENT`.

Matches: pass 1 first, then pass 2. `reconcile` never changes the journal.

## Grade

```bash
python3 scripts/project_test.py ledger-engine --lang java --stage 4 --path <workspace>
python3 scripts/project_test.py ledger-engine --lang java --all --path <workspace>
```
