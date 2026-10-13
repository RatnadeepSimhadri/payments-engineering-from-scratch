# Phase 11: Payment Rails & Card Networks

> Every rail is a protocol, a rulebook, and a settlement cycle.

15 lessons, 15 complete, ~23 hours. Generated from `curriculum/curriculum.json` by `scripts/build_catalog.py`.

## Start this phase

**First lesson:** [How a Card Payment Works: Issuer, Acquirer, Network, Processor](01-how-a-card-payment-works/)


With an agent tutor: `/learn` (Claude Code), `learn` (Codex), or `Use learn to teach the next lesson.` The tutor reads your language from `LEARNING.md`.

## Lessons

| # | Lesson | Type | Time | Status | Hook |
|:---:|--------|:----:|:----:|:------:|------|
| 01 | [How a Card Payment Works: Issuer, Acquirer, Network, Processor](01-how-a-card-payment-works/) | Learn | ~75 min | ✅ | Follow one tap through seven parties in two seconds. |
| 02 | [Authorization, Capture, Void, Refund: The Card Lifecycle State Machine](02-card-authorization-lifecycle/) | Build | ~120 min | ✅ | Partial captures, incremental auths, and the states that make or lose money. |
| 03 | [ISO 8583: Parsing the Card Network Message](03-iso-8583/) | Build | ~150 min | ✅ | MTI, bitmaps, and data elements, parsed from raw bytes. |
| 04 | [PANs, Luhn, and BIN Range Routing](04-pan-luhn-and-bin-ranges/) | Build | ~75 min | ✅ | Validate a card number and route it to the right network. |
| 05 | [Tokenization and PCI DSS Scope Reduction](05-tokenization-and-pci-scope/) | Build | ~90 min | ✅ | Never store the PAN; here is what you store instead. |
| 06 | [3-D Secure and Strong Customer Authentication](06-3ds-and-strong-customer-authentication/) | Learn | ~75 min | ✅ | Frictionless vs challenge flows and liability shift. |
| 07 | [Interchange, Scheme Fees, and Merchant Pricing](07-interchange-and-scheme-fees/) | Build | ~75 min | ✅ | Who gets paid on a $100 transaction, to the cent. |
| 08 | [ACH and the NACHA File Format](08-ach-and-nacha/) | Build | ~120 min | ✅ | Fixed-width records, batch headers, and returns that arrive days later. |
| 09 | [SEPA, Wires, and ISO 20022 (pain.001, pacs.008)](09-sepa-and-iso-20022/) | Build | ~120 min | ✅ | The XML message format the whole world is migrating to. |
| 10 | [Real-Time Payments: RTP, FedNow, UPI, Faster Payments](10-real-time-payments/) | Learn | ~75 min | ✅ | Irrevocable in seconds changes every fraud control. |
| 11 | [IBAN, BIC, and Account Number Validation](11-iban-bic-and-account-validation/) | Build | ~60 min | ✅ | Mod-97 and country-specific BBAN rules. |
| 12 | [Wallets, Stored Value, and E-Money Regulation](12-wallets-and-stored-value/) | Learn | ~60 min | ✅ | A wallet balance is a liability on your books. |
| 13 | [Open Banking: OAuth Consent, Account Aggregation, and Data Access](13-open-banking-and-account-aggregation/) | Build | ~120 min | ✅ | Consent flows, token refresh, and the aggregator SDK pattern. |
| 14 | [Bank Statement Formats: MT940, CAMT.053, BAI2, OFX](14-bank-statements-and-formats/) | Build | ~90 min | ✅ | Parse what the bank sends you before you can reconcile it. |
| 15 | [Settlement and Clearing: Gross, Net, and Deferred](15-settlement-and-clearing/) | Learn | ~75 min | ✅ | When the money actually moves, and why T+1 exists. |

Browse the [full curriculum](../../README.md#contents) or the [roadmap](../../ROADMAP.md).
