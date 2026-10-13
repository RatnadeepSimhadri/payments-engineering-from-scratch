# Phase 10: Money & Ledgers

> A ledger is a data structure with a legal obligation.

12 lessons, 5 complete, ~18 hours. Generated from `curriculum/curriculum.json` by `scripts/build_catalog.py`.

## Start this phase

**First lesson:** [Representing Money: Minor Units, Currencies, and Rounding](01-representing-money/)

Run the first complete lesson from the repository root:

```bash
python3 scripts/run_lesson.py phases/10-money-and-ledgers/01-representing-money --lang java
```

Keep the command, the exit code, and the test count as your first piece of evidence.

With an agent tutor: `/learn` (Claude Code), `learn` (Codex), or `Use learn to teach the next lesson.` The tutor reads your language from `LEARNING.md`.

## Lessons

| # | Lesson | Type | Time | Status | Hook |
|:---:|--------|:----:|:----:|:------:|------|
| 01 | [Representing Money: Minor Units, Currencies, and Rounding](01-representing-money/) | Build | ~90 min | ✅ | A Money type that makes 0.1 + 0.2 impossible to get wrong. |
| 02 | [Currencies, Exchange Rates, and FX Conversion](02-currency-and-fx/) | Build | ~90 min | ⬚ | ISO 4217, exponents, and where the rounding residue goes. |
| 03 | [The Double-Entry Ledger from Scratch](03-double-entry-ledger/) | Build | ~150 min | ✅ | Debits equal credits or the entry does not exist. |
| 04 | [Chart of Accounts and Account Types](04-chart-of-accounts/) | Learn | ~60 min | ✅ | Assets, liabilities, revenue: which direction increases each. |
| 05 | [Balances, Running Totals, and Snapshots](05-balances-and-snapshots/) | Build | ~90 min | ⬚ | Sum a million postings in milliseconds without summing a million postings. |
| 06 | [Immutability, Reversals, and Corrections](06-immutability-and-reversals/) | Build | ~75 min | ⬚ | You never update a posting; you post its opposite. |
| 07 | [Holds, Pending Transactions, and Available Balance](07-holds-and-available-balance/) | Build | ~90 min | ✅ | Authorized is not settled; available is not ledger balance. |
| 08 | [Reconciliation: Matching Your Ledger Against Theirs](08-reconciliation/) | Build | ~120 min | ✅ | The processor says 10,000 transactions; you have 9,998. Find the two. |
| 09 | [Multi-Currency Ledgers and Revaluation](09-multi-currency-ledgers/) | Build | ~90 min | ⬚ | One ledger, many currencies, no mixing. |
| 10 | [Fees, Splits, and Platform Payments](10-fees-and-splits/) | Build | ~90 min | ⬚ | Marketplace payouts with fee allocation that always sums. |
| 11 | [Ledger Concurrency: Hot Accounts and Balance Checks](11-ledger-concurrency/) | Build | ~90 min | ⬚ | Ten thousand payments into one merchant account per second. |
| 12 | [Audit Trails, Trial Balance, and Financial Close](12-audit-trails-and-financial-close/) | Learn | ~60 min | ⬚ | What the auditor asks for and how the ledger answers. |

Browse the [full curriculum](../../README.md#contents) or the [roadmap](../../ROADMAP.md).
