# Stage 3 — Balances, Trial Balance, and Reversals

Continue editing the workspace created at stage 1. No new starter files. Everything below is added to `Ledger.java` (records above the class, methods inside it). Stage 2's `post` gains a snapshot step. Read `../docs/en.md` for the worked example and the rules.

## New signatures

```java
record BalanceSnapshot(long sequence, Map<String, Money> balances) {}   // compact ctor: balances = Map.copyOf(balances)

record TrialBalanceLine(Account account, Money debit, Money credit) {}

record TrialBalance(Currency currency, List<TrialBalanceLine> lines, Money totalDebits, Money totalCredits) {
    // compact ctor: lines = List.copyOf(lines)
    Money difference();                      // totalDebits.minus(totalCredits)
    boolean isBalanced();                    // difference().isZero()
}

final class Ledger {
    static final int DEFAULT_SNAPSHOT_EVERY = 100;
    Ledger(AccountRegistry accounts);                       // this(accounts, DEFAULT_SNAPSHOT_EVERY)
    Ledger(AccountRegistry accounts, int snapshotEvery);    // < 1 -> IllegalArgumentException
    int snapshotEvery();

    Money balance(String accountId);                        // signed on the account's normal side; latest snapshot + tail replay
    Money balanceAt(String accountId, long sequence);       // full replay through `sequence` inclusive; ignores snapshots
    List<BalanceSnapshot> snapshots();                      // unmodifiable copy, oldest first
    List<TrialBalance> trialBalance();                      // one per currency in the registry, ordered by currency code

    static String reversalId(String entryId);               // "rev:" + entryId
    PostedEntry reverse(String entryId);                    // post(mirror of the entry, same date, id reversalId(id)); idempotent
    boolean isReversed(String entryId);                     // hasEntry(reversalId(entryId))
}
```

## Rules in one screen

- Signed effect of a posting = `+amount` if `posting.side() == account.normalSide()`, else `-amount`.
- In `post`, after appending with sequence `s`: `if (s % snapshotEvery == 0)` record a snapshot holding **every** registry account (untouched ones at their currency's zero).
- Trial balance line: net = debits − credits per account; net ≥ 0 goes in the debit column, net < 0 goes (negated) in the credit column. Accounts in opening order within each currency group.
- `reverse` never edits the original. Reversing twice returns the first reversal because `post` de-duplicates on id.

## Grade

```bash
python3 scripts/project_test.py ledger-engine --lang java --stage 3 --path <workspace>
```
