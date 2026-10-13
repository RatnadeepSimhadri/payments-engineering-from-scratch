---
name: checklist-wallet-safeguarding
description: Design and operations checklist for a stored-value wallet ledger - chart of accounts, safeguarding invariant, daily reconciliation, and insolvency readiness
phase: 11
lesson: 12
tags: [wallets, e-money, safeguarding, ledger, reconciliation, checklist]
---

# Checklist: Wallet Ledger and Safeguarding

Use this when designing a stored-value product, reviewing a banking-as-a-service integration, or preparing for a regulator's safeguarding audit.

## Classification

- [ ] The product has been classified (pass-through, stored value, closed loop, bank deposit) in writing, with the legal basis (EMD2/EMRs, state money transmission, RBI PPI, limited network exclusion).
- [ ] If a partner issues the e-money or holds the funds, the contract states whose ledger is the record of customer entitlement and how it is reconciled with the partner's.

## Chart of accounts

- [ ] One liability account per customer per currency; no customer balances stored only as a column in a user table.
- [ ] Safeguarding, operating, clearing (top-ups, payouts), revenue, and expense accounts are separate.
- [ ] Every posting is a balanced journal entry in integer minor units; nothing updates a balance without a journal entry.
- [ ] Fees are separate entries to revenue accounts, never deducted silently from the amount issued (issue and redeem at par).

## The invariant

- [ ] Sum of positive customer balances <= safeguarding statement balance + top-ups in transit - payouts in transit, checked after every posting batch.
- [ ] Negative customer balances are receivables and are never netted against positive balances.
- [ ] In-transit items are explicit, aged, and each traceable to a clearing entry; items older than the regulatory limit (EU: five business days for card top-ups) alert.

## Daily reconciliation

- [ ] The safeguarding bank statement is ingested daily (Phase 11, Lesson 14) and reconciled to the ledger.
- [ ] Shortfalls are funded from operating funds before the regulatory cut-off; surpluses (earned fees) are swept out the same day.
- [ ] Unexplained differences open an incident with an owner, not a suspense entry that is never cleared.

## Risk

- [ ] Top-ups from new funding sources have holds or limits on withdrawal and P2P (Phase 10, Lesson 07).
- [ ] Chargebacks on top-ups are posted against the customer's wallet, with the resulting negative balance covered by the provider.
- [ ] Dormant balances remain liabilities; escheatment (US) or redemption rights (EU/UK) are implemented, not written off to revenue.

## Insolvency readiness

- [ ] An administrator could produce every customer's entitlement from the ledger within one day (UK resolution pack expectation).
- [ ] For US FBO accounts, records identify each beneficial owner and amount, and the bank can access them.
- [ ] Customer communications never claim deposit insurance unless pass-through conditions are met and documented.
