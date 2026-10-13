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

## Validation order inside `post`

1. Id already posted → return the existing `PostedEntry`, change nothing.
2. Fewer than two postings → `InvalidEntryException`.
3. For each posting: unknown account → `UnknownAccountException`; posting currency differs from the entry's → `CurrencyMismatchException`; account currency differs from the entry's → `CurrencyMismatchException`.
4. Sum of debits != sum of credits → `UnbalancedEntryException`.
5. Append, index by id, return. Rejected entries never consume a sequence number.

## Grade

```bash
python3 scripts/project_test.py ledger-engine --lang java --stage 2 --path <workspace>
```
