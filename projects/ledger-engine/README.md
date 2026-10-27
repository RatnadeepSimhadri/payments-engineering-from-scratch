# Ledger Engine

> A double-entry ledger that refuses to go out of balance, then reconciles itself against a bank statement.

**Level 2 — Builder.** Four stages, about 8 hours. **Languages: Java 21 or Python 3.11+** (standard library only; Go port follows later).

## Overview

Every payments company runs on a ledger. Payment state machines, payouts, refunds, fee splits, and the month-end close all reduce to one question: *which accounts moved, by how much, and does it still balance?* In this project you build that core from nothing: exact integer money, a chart of accounts, an append-only journal that rejects unbalanced entries, balances and a trial balance derived from the journal, reversals that never edit history, and a reconciliation that compares your books with the processor's statement and reports every break with totals that tie out.

Nothing here is a toy simplification of a "real" ledger. The invariants you enforce (debits equal credits per entry, per currency; balances are derived, never stored as truth; corrections are new entries; every minor unit on a statement lands in exactly one match or one break) are the same ones production ledgers at banks and processors enforce, and the same ones an auditor checks.

## What you will build

| Stage | Title | You add | Hours |
|---|---|---|---|
| 1 | Money and the Chart of Accounts | `Money.parse / plus / minus / times / divideRounded / allocate`; `Side`, `AccountType.normalSide`, `Account`, `AccountRegistry` | 2 |
| 2 | Balanced Journal Entries | `Posting`, `JournalEntry`, `PostedEntry`; `Ledger.post` with validation, dense sequence numbers, and idempotent ids | 2 |
| 3 | Balances, Trial Balance, and Reversals | `Ledger.balance`, `balanceAt`, periodic `BalanceSnapshot`s, `trialBalance()` per currency, `reverse` | 2 |
| 4 | Reconcile Against a Statement | `StatementLine`, `LedgerLine`, `Ledger.lines`, two-pass `Ledger.reconcile`, `ReconciliationReport` with `tiesOut()` | 2 |

The finished engine, driven from `Main`, opens a chart of accounts, posts a marketplace sale with a 2.9% + 30c fee, pays a seller out, rejects an unbalanced entry, prints a trial balance whose columns agree, reverses a sale, and reconciles the bank account against a five-line statement that contains a renamed payout, a duplicate line, and a refund the ledger never saw.

## Prerequisites

Work through these lessons first; the project assumes their vocabulary and reuses the `Money` design from the first one verbatim.

- `phases/10-money-and-ledgers/01-representing-money` — Representing Money: Minor Units, Currencies, and Rounding
- `phases/10-money-and-ledgers/03-double-entry-ledger` — The Double-Entry Ledger from Scratch
- `phases/10-money-and-ledgers/08-reconciliation` — Reconciliation: Matching Your Ledger Against Theirs

Helpful but not required: `04-chart-of-accounts` (why assets grow on the debit side) and `06-immutability-and-reversals`.

## Commands

All commands run from the repository root. Pick any directory outside the repository for your workspace.

```bash
# 1. create a workspace from the stage-1 starter (Java: Money.java, Accounts.java, Main.java; Python: one main.py)
python3 scripts/project_test.py ledger-engine --lang java --init ~/ledger-engine-java
python3 scripts/project_test.py ledger-engine --lang python --init ~/ledger-engine-py

# 2. read the stage lesson, implement, then grade the stage
#    (the grader runs every stage up to N, so a regression in stage 1 shows up while you work on stage 3)
python3 scripts/project_test.py ledger-engine --lang java --stage 1 --path ~/ledger-engine-java
python3 scripts/project_test.py ledger-engine --lang python --stage 1 --path ~/ledger-engine-py
python3 scripts/project_test.py ledger-engine --lang java --stage 2 --path ~/ledger-engine-java
python3 scripts/project_test.py ledger-engine --lang python --stage 2 --path ~/ledger-engine-py
python3 scripts/project_test.py ledger-engine --lang java --stage 3 --path ~/ledger-engine-java
python3 scripts/project_test.py ledger-engine --lang python --stage 3 --path ~/ledger-engine-py
python3 scripts/project_test.py ledger-engine --lang java --stage 4 --path ~/ledger-engine-java
python3 scripts/project_test.py ledger-engine --lang python --stage 4 --path ~/ledger-engine-py

# 3. run everything at once
python3 scripts/project_test.py ledger-engine --lang java --all --path ~/ledger-engine-java
python3 scripts/project_test.py ledger-engine --lang python --all --path ~/ledger-engine-py

# run your own Main while you work (from inside the workspace)
javac -d .out *.java && java -cp .out Main   # Java
python3 main.py                                # Python
```

How grading works: for each stage the grader copies your sources (`*.java` except `*Test.java`, or `*.py` except `test_*.py`) into a temporary directory, adds that stage's tests (`StageNNTest.java`, or `test_stage_NN.py`), and runs them (`javac` + `java`, or `python3 -m unittest -v`). The run exits 1 on the first stage with failures and prints every failing check by name. You never need `solution/`; the `build-project` skill may read it to diagnose, and shows at most one function after two honest attempts.

Stages 2 to 4 ship no starter files. You keep editing the workspace you created at stage 1; each later stage's `starter/STAGE.md` lists the new types and signatures the tests expect, and the stage lesson explains them.

## What the learner keeps

- A self-contained, dependency-free double-entry ledger in about 600 lines of Java (`Money`, `Accounts`, `Journal`, `Ledger`, `Reconciliation`, `Main`) or one `main.py` of about 550 lines in Python.
- A `Money` type with explicit rounding and lossless allocation that you can lift into any later project.
- A worked mental model of the four ledger invariants and the exact exception each violation raises.
- A reconciliation routine whose report proves, by arithmetic, that no line was dropped: `ledgerTotal == matchedTotal + ledgerBreakTotal` and `statementTotal == matchedTotal + statementBreakTotal`.
- Four grader suites (211 checks in all) you can keep running as you extend the engine.

## Run with your own inputs

`Main.java` (or the `main()` of `main.py`) is yours; the grader never runs it. Once stage 4 passes, try these from your workspace:

1. **Your own chart of accounts.** Open the accounts a small marketplace needs (`bank`, `receivable:cards`, `payable:sellers`, `revenue:fees`, `expense:chargebacks`) and post a week of activity: five sales with `times(29, 1000, HALF_EVEN)` fees plus a fixed 30 minor units, two payouts, one refund. Print `trialBalance()` and confirm `isBalanced()` after every entry.
2. **Split a payout.** Use `Money.allocate(70, 20, 10)` to split one 100.01 USD payout three ways and post it as a four-leg entry (one credit to `bank`, three debits to seller sub-accounts). The ledger accepts it only because `allocate` never loses a minor unit; try building the same entry from three `times(...)` calls and watch `UnbalancedEntryException` catch the missing cent.
3. **A statement in a different shape.** Write a `List<StatementLine>` the way a processor would send it: their own references, settlement dates one or two days after your booking dates, one line they split in two, one line they sent twice. Call `reconcile("bank", statement, 2)` and then `3`; count how the breaks move between `MISSING_IN_LEDGER` and `AMOUNT_AND_DATE` matches. `tiesOut()` must stay true for every window.
4. **Read a statement from a file.** Replace the hard-coded list with a tiny CSV reader (`java.nio.file.Files.readAllLines`, split on commas, `Money.parse` for the amount). Keep the `StatementLine` record unchanged so `reconcile` does not care where the lines came from.
5. **Point-in-time audit.** After reversing an entry, print `balanceAt(account, seq)` for every sequence number and show that history still contains the original posting and its mirror.

## Layout

```text
projects/ledger-engine/
  project.json
  README.md
  stages/
    01-money-and-accounts/           docs/en.md  starter/{java,python}/  tests/{java,python}/
    02-balanced-journal-entries/     docs/en.md  starter/STAGE.md        tests/{java,python}/
    03-balances-and-trial-balance/   docs/en.md  starter/STAGE.md        tests/{java,python}/
    04-reconcile-against-statement/  docs/en.md  starter/STAGE.md        tests/{java,python}/
  solution/java/   solution/python/   reference implementations; each passes every stage
```
