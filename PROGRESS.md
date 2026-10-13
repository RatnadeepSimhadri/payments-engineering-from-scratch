# Progress Tracker

Generated 2026-10-13T18:04:58+00:00 by `python3 scripts/progress.py` from the repository state. Do not edit by hand; rerun the script after any batch of work.

## Overall

**Lessons complete: 137 / 199**  `█████████████████████░░░░░░░░░` 68%

| Area | Status |
|---|---|
| Java implementations | 118 / 143 Build lessons (primary, gates completion) |
| Python implementations | 40 / 143 Build lessons (port in progress, not gating) |
| Go implementations | 40 / 143 Build lessons (port in progress, not gating) |
| Projects | 3 ready: `idempotent-payment-gateway` (4 stages, Java), `ledger-engine` (4 stages, Java), `raft-kv` (4 stages, Java) |
| Skills | 9 / 9 present and mirrored to `.claude/skills/` |
| Git | HEAD `c6a3f28 feat: payments-engineering-from-scratch — curriculum, tooling, skills, 73 lessons, 3 projects`; 330 uncommitted files |

## By track and phase

| Phase | Title | Complete | Progress | Hours |
|:---:|---|:---:|---|:---:|
| | **Track A: Data Structures & Algorithms** | **65 / 65** | `████████████████████` | |
| 0 | [Setup & Tooling](phases/00-setup-and-tooling/) | 8 / 8 | `████████████████████` | ~7 |
| 1 | [Complexity & Core Data Structures](phases/01-complexity-and-core-structures/) | 14 / 14 | `████████████████████` | ~16 |
| 2 | [Algorithms](phases/02-algorithms/) | 16 / 16 | `████████████████████` | ~20 |
| 3 | [Systems Data Structures](phases/03-systems-data-structures/) | 15 / 15 | `████████████████████` | ~22 |
| 4 | [Concurrency & Runtime Internals](phases/04-concurrency/) | 12 / 12 | `████████████████████` | ~15 |
| | **Track B: Distributed Systems** | **39 / 63** | `████████████░░░░░░░░` | |
| 5 | [Distributed Systems Foundations](phases/05-distributed-foundations/) | 11 / 13 | `█████████████████░░░` | ~18 |
| 6 | [Replication & Consensus](phases/06-replication-and-consensus/) | 9 / 12 | `███████████████░░░░░` | ~19 |
| 7 | [Partitioning, Storage & Transactions](phases/07-storage-and-transactions/) | 8 / 14 | `███████████░░░░░░░░░` | ~22 |
| 8 | [Messaging, Streams & Event-Driven Systems](phases/08-messaging-and-streams/) | 4 / 11 | `███████░░░░░░░░░░░░░` | ~15 |
| 9 | [Reliability, Observability & Scale](phases/09-reliability-and-scale/) | 7 / 13 | `███████████░░░░░░░░░` | ~16 |
| | **Track C: Payments Engineering** | **33 / 71** | `█████████░░░░░░░░░░░` | |
| 10 | [Money & Ledgers](phases/10-money-and-ledgers/) | 5 / 12 | `████████░░░░░░░░░░░░` | ~18 |
| 11 | [Payment Rails & Card Networks](phases/11-payment-rails/) | 8 / 15 | `███████████░░░░░░░░░` | ~23 |
| 12 | [Payment Systems Design](phases/12-payment-systems-design/) | 4 / 14 | `██████░░░░░░░░░░░░░░` | ~22 |
| 13 | [Risk, Fraud & Compliance](phases/13-risk-fraud-and-compliance/) | 4 / 11 | `███████░░░░░░░░░░░░░` | ~15 |
| 14 | [Payments at Scale](phases/14-payments-at-scale/) | 4 / 11 | `███████░░░░░░░░░░░░░` | ~17 |
| 15 | [Capstone Projects](phases/15-capstone-projects/) | 8 / 8 | `████████████████████` | ~60 |

## Remaining lessons

| Phase | # | Lesson | Type | Status |
|:---:|:---:|---|:---:|:---:|
| 5 | 07 | [RPC, Framing, and Serialization Over Raw Sockets](phases/05-distributed-foundations/07-rpc-and-serialization/) | Build | 🚧 In Progress |
| 5 | 13 | [Gossip Protocols and Failure Detection](phases/05-distributed-foundations/13-gossip-and-failure-detection/) | Build | 🚧 In Progress |
| 6 | 07 | [Raft III: Safety Proofs, Membership Changes, Snapshots](phases/06-replication-and-consensus/07-raft-safety-and-membership/) | Build | 🚧 In Progress |
| 6 | 10 | [Chain Replication and Primary-Backup](phases/06-replication-and-consensus/10-chain-and-primary-backup/) | Build | 🚧 In Progress |
| 6 | 11 | [Byzantine Fault Tolerance: PBFT in Outline](phases/06-replication-and-consensus/11-byzantine-fault-tolerance/) | Learn | 🚧 In Progress |
| 7 | 04 | [Isolation Levels and Their Anomalies](phases/07-storage-and-transactions/04-isolation-levels-and-anomalies/) | Build | 🚧 In Progress |
| 7 | 05 | [Two-Phase Locking and Deadlock Detection](phases/07-storage-and-transactions/05-two-phase-locking/) | Build | 🚧 In Progress |
| 7 | 09 | [Percolator and Spanner-Style Transactions](phases/07-storage-and-transactions/09-percolator-and-spanner-style-transactions/) | Learn | 🚧 In Progress |
| 7 | 10 | [Secondary Indexes: Local vs Global](phases/07-storage-and-transactions/10-secondary-indexes/) | Build | 🚧 In Progress |
| 7 | 11 | [Change Data Capture](phases/07-storage-and-transactions/11-change-data-capture/) | Build | 🚧 In Progress |
| 7 | 14 | [Schema Migrations at Scale: Expand, Migrate, Contract](phases/07-storage-and-transactions/14-schema-migrations-at-scale/) | Learn | 🚧 In Progress |
| 8 | 04 | [Message Ordering, Keys, and Head-of-Line Blocking](phases/08-messaging-and-streams/04-message-ordering-and-keys/) | Build | 🚧 In Progress |
| 8 | 06 | [CQRS and Projections](phases/08-messaging-and-streams/06-cqrs-and-projections/) | Build | 🚧 In Progress |
| 8 | 07 | [Stream Processing: Tumbling, Sliding, and Session Windows](phases/08-messaging-and-streams/07-stream-processing-windows/) | Build | 🚧 In Progress |
| 8 | 08 | [Watermarks, Event Time, and Late Data](phases/08-messaging-and-streams/08-watermarks-and-late-data/) | Build | 🚧 In Progress |
| 8 | 09 | [Backpressure and Flow Control](phases/08-messaging-and-streams/09-backpressure/) | Build | 🚧 In Progress |
| 8 | 10 | [Dead-Letter Queues and Poison Messages](phases/08-messaging-and-streams/10-dead-letter-queues-and-poison-messages/) | Build | 🚧 In Progress |
| 8 | 11 | [Schema Registries and Compatible Evolution](phases/08-messaging-and-streams/11-schema-registry-and-evolution/) | Learn | 🚧 In Progress |
| 9 | 05 | [Distributed Rate Limiting](phases/09-reliability-and-scale/05-distributed-rate-limiting/) | Build | 🚧 In Progress |
| 9 | 07 | [Distributed Tracing and Context Propagation](phases/09-reliability-and-scale/07-distributed-tracing/) | Build | 🚧 In Progress |
| 9 | 08 | [Structured Logging and Log Correlation](phases/09-reliability-and-scale/08-structured-logging/) | Build | 🚧 In Progress |
| 9 | 11 | [Multi-Region Architecture and Disaster Recovery](phases/09-reliability-and-scale/11-multi-region-and-disaster-recovery/) | Learn | 🚧 In Progress |
| 9 | 12 | [Incident Response and Blameless Postmortems](phases/09-reliability-and-scale/12-incident-response/) | Learn | 🚧 In Progress |
| 9 | 13 | [Graceful Degradation and Load Shedding](phases/09-reliability-and-scale/13-graceful-degradation/) | Build | 🚧 In Progress |
| 10 | 02 | [Currencies, Exchange Rates, and FX Conversion](phases/10-money-and-ledgers/02-currency-and-fx/) | Build | ⬚ Planned |
| 10 | 05 | [Balances, Running Totals, and Snapshots](phases/10-money-and-ledgers/05-balances-and-snapshots/) | Build | ⬚ Planned |
| 10 | 06 | [Immutability, Reversals, and Corrections](phases/10-money-and-ledgers/06-immutability-and-reversals/) | Build | ⬚ Planned |
| 10 | 09 | [Multi-Currency Ledgers and Revaluation](phases/10-money-and-ledgers/09-multi-currency-ledgers/) | Build | ⬚ Planned |
| 10 | 10 | [Fees, Splits, and Platform Payments](phases/10-money-and-ledgers/10-fees-and-splits/) | Build | ⬚ Planned |
| 10 | 11 | [Ledger Concurrency: Hot Accounts and Balance Checks](phases/10-money-and-ledgers/11-ledger-concurrency/) | Build | ⬚ Planned |
| 10 | 12 | [Audit Trails, Trial Balance, and Financial Close](phases/10-money-and-ledgers/12-audit-trails-and-financial-close/) | Learn | ⬚ Planned |
| 11 | 05 | [Tokenization and PCI DSS Scope Reduction](phases/11-payment-rails/05-tokenization-and-pci-scope/) | Build | ⬚ Planned |
| 11 | 06 | [3-D Secure and Strong Customer Authentication](phases/11-payment-rails/06-3ds-and-strong-customer-authentication/) | Learn | ⬚ Planned |
| 11 | 07 | [Interchange, Scheme Fees, and Merchant Pricing](phases/11-payment-rails/07-interchange-and-scheme-fees/) | Build | ⬚ Planned |
| 11 | 09 | [SEPA, Wires, and ISO 20022 (pain.001, pacs.008)](phases/11-payment-rails/09-sepa-and-iso-20022/) | Build | ⬚ Planned |
| 11 | 10 | [Real-Time Payments: RTP, FedNow, UPI, Faster Payments](phases/11-payment-rails/10-real-time-payments/) | Learn | ⬚ Planned |
| 11 | 12 | [Wallets, Stored Value, and E-Money Regulation](phases/11-payment-rails/12-wallets-and-stored-value/) | Learn | ⬚ Planned |
| 11 | 14 | [Bank Statement Formats: MT940, CAMT.053, BAI2, OFX](phases/11-payment-rails/14-bank-statements-and-formats/) | Build | ⬚ Planned |
| 12 | 03 | [Payment Orchestration Across Processors](phases/12-payment-systems-design/03-payment-orchestration/) | Build | ⬚ Planned |
| 12 | 04 | [Smart Routing, Cascading, and Retry Policies](phases/12-payment-systems-design/04-smart-routing-and-retries/) | Build | ⬚ Planned |
| 12 | 06 | [Refunds, Partial Refunds, and Refund Windows](phases/12-payment-systems-design/06-refunds-and-partial-refunds/) | Build | ⬚ Planned |
| 12 | 07 | [Disputes and Chargebacks: The Lifecycle and the Evidence](phases/12-payment-systems-design/07-disputes-and-chargebacks/) | Build | ⬚ Planned |
| 12 | 08 | [Payouts, Balances, and Negative Balance Handling](phases/12-payment-systems-design/08-payouts-and-balance-management/) | Build | ⬚ Planned |
| 12 | 09 | [Limits, Velocity Controls, and Exposure](phases/12-payment-systems-design/09-limits-and-velocity-controls/) | Build | ⬚ Planned |
| 12 | 10 | [Payment Method Abstraction and Processor Adapters](phases/12-payment-systems-design/10-payment-method-abstraction/) | Build | ⬚ Planned |
| 12 | 11 | [Recurring Payments, Subscriptions, and Dunning](phases/12-payment-systems-design/11-recurring-and-subscriptions/) | Build | ⬚ Planned |
| 12 | 12 | [Currency Conversion at Checkout and DCC](phases/12-payment-systems-design/12-currency-conversion-at-checkout/) | Build | ⬚ Planned |
| 12 | 13 | [API Design for Money: Versioning, Pagination, and Error Semantics](phases/12-payment-systems-design/13-api-design-for-money/) | Learn | ⬚ Planned |
| 13 | 01 | [Fraud Patterns: Card Testing, Account Takeover, Friendly Fraud](phases/13-risk-fraud-and-compliance/01-fraud-patterns/) | Learn | ⬚ Planned |
| 13 | 03 | [Risk Scoring, Thresholds, and Precision-Recall Trade-offs](phases/13-risk-fraud-and-compliance/03-risk-scoring-and-thresholds/) | Build | ⬚ Planned |
| 13 | 05 | [KYC, CIP, and Identity Verification Flows](phases/13-risk-fraud-and-compliance/05-kyc-and-identity-verification/) | Learn | ⬚ Planned |
| 13 | 07 | [Device Fingerprinting and Behavioral Signals](phases/13-risk-fraud-and-compliance/07-device-fingerprinting-and-signals/) | Learn | ⬚ Planned |
| 13 | 09 | [PSD2, SCA, and Regional Regulation](phases/13-risk-fraud-and-compliance/09-psd2-sca-and-regional-regulation/) | Learn | ⬚ Planned |
| 13 | 10 | [Data Privacy, Retention, and Right to Erasure in Financial Systems](phases/13-risk-fraud-and-compliance/10-data-privacy-and-retention/) | Learn | ⬚ Planned |
| 13 | 11 | [Auditability: Immutable Logs and Evidence Bundles](phases/13-risk-fraud-and-compliance/11-auditability-and-evidence/) | Build | ⬚ Planned |
| 14 | 02 | [Multi-Region Ledgers and Home-Region Routing](phases/14-payments-at-scale/02-multi-region-ledgers/) | Learn | ⬚ Planned |
| 14 | 04 | [Reconciliation at Scale: Streams, Buckets, and Breaks](phases/14-payments-at-scale/04-reconciliation-at-scale/) | Build | ⬚ Planned |
| 14 | 05 | [Batch vs Real-Time Settlement Pipelines](phases/14-payments-at-scale/05-batch-vs-real-time-settlement/) | Build | ⬚ Planned |
| 14 | 06 | [Observability for Money Movement](phases/14-payments-at-scale/06-observability-for-money-movement/) | Build | ⬚ Planned |
| 14 | 07 | [Incident Management for Payments](phases/14-payments-at-scale/07-payment-incident-management/) | Learn | ⬚ Planned |
| 14 | 08 | [Cost and Authorization-Rate Optimization](phases/14-payments-at-scale/08-cost-and-authorization-optimization/) | Build | ⬚ Planned |
| 14 | 09 | [Migrating a Live Ledger](phases/14-payments-at-scale/09-ledger-migrations/) | Learn | ⬚ Planned |

## Work log

Batches are recorded in `PROGRESS-LOG.md` (hand-maintained, newest first).
