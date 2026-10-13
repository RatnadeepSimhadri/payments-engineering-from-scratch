---
name: interview-card-settlement
description: Interview questions and model answers on clearing, settlement, netting, and finality for payments engineering roles
phase: 11
lesson: 15
tags: [settlement, clearing, netting, interview, payments]
---

# Interview Card: Settlement and Clearing

Use to prepare for or conduct a payments-engineering interview. Each question has the signal a strong answer carries. Model answers are terse on purpose; the candidate should be able to expand any of them.

## Warm-up

**Q: What is the difference between clearing and settlement?**
Clearing is computing and confirming who owes whom; settlement is the transfer that extinguishes the obligation. Signal: the candidate mentions that a claim exists between the two and that someone bears credit risk during the gap.

**Q: A card authorization was approved. Has any money moved?**
No. The issuer has promised to pay. Money moves at interbank settlement (T+1, net) and again at acquirer payout. Signal: the candidate can name the receivable the merchant and acquirer each hold in between.

## Core

**Q: Four banks exchange nine payments totalling 255. Multilateral netting moves 35. Explain why, and what the catch is.**
Each bank's net position is receipts minus payments; positions sum to zero because every payment is a debit and a credit; only net debtors pay in and only net creditors are paid out. The catch is that a participant's failure before the cycle breaks the netting, so the system needs collateral, loss sharing, or prefunding (Lamfalussy "cover one"). Signal: the candidate volunteers the failure case without prompting.

**Q: Why did large-value systems move from deferred net settlement to RTGS in the 1990s, and what did it cost?**
To remove interbank credit exposure on payments large enough to be systemic. The cost is liquidity: each payment needs cover when sent, so central banks provide collateralized intraday credit and systems add queue offsetting to avoid gridlock. Signal: mentions gridlock or daylight overdrafts.

**Q: What is Herstatt risk and how was it fixed?**
Loss of full principal when one leg of a two-legged transaction settles and the other does not, named for the 1974 FX failure where Mark legs had settled in Frankfurt before dollar legs in New York. Fixed by payment-versus-payment through CLS (2002): both legs settle together or neither does. Signal: the candidate generalizes to DvP and to atomic two-leg operations in their own systems.

**Q: An ACH credit for 5,000 arrived in your platform's account this morning. Can you release the funds to the user?**
Funds are available but not final; most entries can be returned by the second banking day after settlement, and unauthorized consumer debits for 60 days. Releasing today is extending credit; the policy should depend on the user's history and the entry type. Signal: distinguishes availability from finality.

## Design

**Q: You are building a marketplace that charges buyers by card and pays sellers by bank transfer. Where is the settlement risk?**
In the two legs: the card charge may be charged back for months after the payout has become irrevocable. The platform is the party extending credit. Mitigations: hold payout until the card settles (T+1 or T+2) and reserve against chargeback rates, or make payout and charge a single balanced entry on the platform ledger and fund payouts from a prefunded pool. Signal: names which account on the ledger carries the exposure.

**Q: How would you design settlement for a closed-loop wallet where users pay each other?**
On-ledger: both parties' balances are liabilities of the platform, so a transfer is one balanced journal entry and is final instantly with no interbank step. The interbank question moves to the edges: top-ups (ACH or card, with their return and chargeback windows) and withdrawals. Signal: recognizes that a shared ledger is a settlement institution.

**Q: Your reconciliation against a card scheme shows a MISSING_THEIRS on every Friday's last transactions. Error or not?**
Almost certainly timing: Friday captures after the clearing cut-off settle in Monday's cycle. Carry forward, do not adjust. Signal: asks about the cut-off time before answering.

## Red flags

- Uses "settled" to mean "the API returned 200".
- Cannot say who extends credit between authorization and settlement.
- Proposes netting without a plan for a participant's failure.
- Treats a two-leg operation as safe because "both calls succeeded in testing".
