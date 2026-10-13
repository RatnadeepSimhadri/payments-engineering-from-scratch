# Glossary

The canonical definitions for Payments Engineering from Scratch. Every term here appears in the `## Key Terms` table of at least one complete lesson; where a term appears in several lessons, this entry is the one definition they all agree on, and the lesson's own table gives the local emphasis. Use it three ways: look a word up when a lesson or an interview question uses it, follow **Taught in** to the lesson that builds the mechanism, and follow **Related terms** sideways when two words keep getting confused. Each entry carries one of ten categories: **Data structures & algorithms** (Phases 0-3), **Concurrency & runtime** (Phase 4), **Distributed systems** (Phases 5-6), **Storage & transactions** (Phase 7), **Messaging & streams** (Phase 8), **Reliability & operations** (Phase 9 and the testing lessons), **Money & ledgers** (Phase 10), **Payment rails** (Phase 11), **Payment systems** (Phases 12 and 14), and **Risk & compliance** (Phase 13). The category is where the idea is defined, not the only place it is used: an idempotency key is a payment-systems term that Phase 5 introduces, and a trial balance is a ledger term that Phase 14's exactly-once lesson leans on. Definitions follow the lessons, and the lessons follow primary sources (RFCs, ISO standards, network rulebooks, papers); where the industry uses a word loosely, **Common confusion** says what people usually mean and what the word actually means.

## A

### ABA routing transit number
- **Category:** Payment rails
- **What it actually means:** The nine-digit identifier of a US financial institution used on ACH entries and wires; the first digits come from Federal Reserve district ranges and the ninth is a check digit from a 3-7-1 weighted sum of the first eight.
- **Why it matters:** Every NACHA entry carries one for the receiving bank, and the entry hash that validates a batch is computed over them.
- **Common confusion:** "The routing number is checked" means its check digit is valid, which proves transcription, not that the bank exists or the account is open.
- **Taught in:** `phases/11-payment-rails/11-iban-bic-and-account-validation`, `phases/11-payment-rails/08-ach-and-nacha`
- **Related terms:** Check digit, IBAN, ODFI / RDFI

### Access token
- **Category:** Payment rails
- **What it actually means:** A short-lived bearer credential that an open-banking resource server checks on every API call; its expiry is the primary limit on the damage of a leak.
- **Why it matters:** Account aggregation moves customer financial data on the strength of this token alone, so its lifetime and scope are the security boundary.
- **Common confusion:** "The token" usually conflates the access token with the refresh token; only the refresh token is presented to the token endpoint, and only the access token is presented to the resource server.
- **Taught in:** `phases/11-payment-rails/13-open-banking-and-account-aggregation`
- **Related terms:** Refresh token, Authorization code, Consent

### Adjusting entry
- **Category:** Money & ledgers
- **What it actually means:** A new, dated journal entry that posts a confirmed reconciliation break into the books so the ledger agrees with the external record.
- **Why it matters:** It is the only legitimate way to fix a ledger, because the original entries are append-only and are never edited.
- **Common confusion:** "Fix the ledger" sounds like editing a row; it means appending an entry whose effect corrects the balance while the history stays intact.
- **Taught in:** `phases/10-money-and-ledgers/08-reconciliation`, `phases/10-money-and-ledgers/03-double-entry-ledger`
- **Related terms:** Break, Reversal, Append-only journal, Reconciliation

### Aggregate
- **Category:** Messaging & streams
- **What it actually means:** The unit of consistency in an event-sourced system: one event stream, one version counter, and one `decide` function that sees the whole folded state before emitting new events.
- **Why it matters:** An account's no-overdraft invariant can only be enforced inside the aggregate, because that is the one place a decision sees every prior event for that account.
- **Common confusion:** "The domain object" suggests a mutable entity; an aggregate is a fold over a stream, and its state is rebuilt from events, not stored as the truth.
- **Taught in:** `phases/08-messaging-and-streams/05-event-sourcing`
- **Related terms:** Event sourcing, Optimistic concurrency, Command vs event

### Ambiguous outcome
- **Category:** Distributed systems
- **What it actually means:** The state a caller is in after a timeout, when it cannot distinguish "the request never arrived", "it arrived and is executing", and "it executed and the response was lost".
- **Why it matters:** It is the root condition behind every duplicate charge: a client that retries blindly from this state repeats a side effect that may already have happened.
- **Common confusion:** "The request timed out" is read as "it failed"; a timeout says nothing about whether the server acted.
- **Taught in:** `phases/05-distributed-foundations/01-the-fallacies-of-distributed-computing`
- **Related terms:** Unknown outcome, Idempotency key, Fallacies of distributed computing

### Append-only journal
- **Category:** Money & ledgers
- **What it actually means:** A ledger whose entries are only ever added at the end; balances are derived from the log rather than stored as the truth, so any balance can be recomputed and checked.
- **Why it matters:** An auditor can replay the journal and arrive at the same balances, which is impossible when rows are edited in place.
- **Common confusion:** "Immutable log" is sometimes taken to mean nothing can be undone; mistakes are undone by appending a reversal, which is still an append.
- **Taught in:** `phases/10-money-and-ledgers/03-double-entry-ledger`
- **Related terms:** Journal entry, Reversal, Event sourcing, Write-ahead

### At-least-once delivery
- **Category:** Messaging & streams
- **What it actually means:** A delivery guarantee in which the sender retries until it observes an acknowledgement, so the receiver may see the same message more than once.
- **Why it matters:** It is the only honest guarantee a webhook producer, an outbox relay, or a retrying client can give; the consumer must absorb the duplicates.
- **Common confusion:** "It might send twice" is heard as a defect; it is the contract, and the defect is a consumer that is not idempotent.
- **Taught in:** `phases/05-distributed-foundations/09-idempotency-keys`, `phases/07-storage-and-transactions/13-transactional-outbox`, `phases/12-payment-systems-design/05-webhooks-and-callbacks`
- **Related terms:** Exactly-once, Idempotent consumer, Inbox, Transactional outbox

### Atomic commitment
- **Category:** Storage & transactions
- **What it actually means:** The problem of getting several participants to agree on a single COMMIT or ABORT such that every participant that decides reaches the same outcome, and COMMIT requires a unanimous YES.
- **Why it matters:** Debiting one bank's ledger and crediting another's is an atomic commitment problem; two-phase commit is its classic solution and its blocking behaviour is why sagas exist.
- **Common confusion:** "Distributed transaction" is used for both 2PC and sagas; only 2PC solves atomic commitment, a saga gives all-complete or all-compensated.
- **Taught in:** `phases/07-storage-and-transactions/08-two-phase-commit`
- **Related terms:** Prepare, Blocking problem, Saga

### Atomicity
- **Category:** Concurrency & runtime
- **What it actually means:** The property that other threads observe either none or all of an operation; `AtomicLong.addAndGet` and a `LOCK XADD` instruction provide it, a `volatile` read-modify-write does not.
- **Why it matters:** A balance update that is not atomic can lose one thread's delta while leaving a perfectly legal-looking number behind.
- **Common confusion:** "Thread-safe" is used for any class with locks inside; atomicity is a property of one operation, and two atomic operations in sequence are not atomic together.
- **Taught in:** `phases/04-concurrency/01-threads-and-the-memory-model`, `phases/04-concurrency/02-locks-and-atomic-counters`
- **Related terms:** Compare-and-swap, Lost update, Happens-before, Check-then-act

### Authorization
- **Category:** Payment rails
- **What it actually means:** A real-time promise by the card issuer to pay a specific amount if the merchant clears correctly and within the validity window; it places a hold on the cardholder's available balance and moves no money.
- **Why it matters:** The whole card lifecycle (capture, void, incremental, refund) is defined relative to this promise, and the hold is what the ledger lesson models as an authorization hold.
- **Common confusion:** "The payment went through" means the authorization was approved; nothing settles until clearing and settlement, which can be days later.
- **Taught in:** `phases/11-payment-rails/01-how-a-card-payment-works`, `phases/11-payment-rails/02-card-authorization-lifecycle`
- **Related terms:** Capture, Authorization hold, Void, Incremental authorization

### Authorization code
- **Category:** Payment rails
- **What it actually means:** The single-use, short-lived OAuth credential returned in the redirect after a user consents, bound to the client, redirect URI, and PKCE challenge, and exchanged once over a back channel for tokens.
- **Why it matters:** Open-banking consent flows rest on it: the bank never hands the aggregator a token through the user's browser, only this code.
- **Common confusion:** "The code in the redirect" is not a token; it is worthless without the client's PKCE verifier and is dead after one exchange or ten minutes.
- **Taught in:** `phases/11-payment-rails/13-open-banking-and-account-aggregation`
- **Related terms:** PKCE, Access token, Refresh token, Consent

### Authorization hold
- **Category:** Money & ledgers
- **What it actually means:** A reservation of funds against a future capture, with an amount and an expiry, that reduces available balance without posting anything to the ledger.
- **Why it matters:** Holds are the mechanism by which an approved card authorization prevents the same funds from being promised twice before any money moves.
- **Common confusion:** "Pending charge" suggests the ledger already moved; the ledger balance is unchanged until capture, only the available balance is lower.
- **Taught in:** `phases/10-money-and-ledgers/07-holds-and-available-balance`
- **Related terms:** Available balance, Ledger balance, Capture, Void

### Available balance
- **Category:** Money & ledgers
- **What it actually means:** Ledger balance minus the sum of active holds; the only number an authorization decision may be made against.
- **Why it matters:** The no-overdraft invariant is about available balance, not ledger balance, and a system that checks the wrong one will double-promise funds.
- **Common confusion:** "The balance" on a statement is the ledger balance; the amount a customer can spend right now is the available balance, and the two differ by exactly the open holds.
- **Taught in:** `phases/10-money-and-ledgers/07-holds-and-available-balance`, `phases/08-messaging-and-streams/05-event-sourcing`
- **Related terms:** Ledger balance, Authorization hold, Check-and-reserve

## B

### Backpressure
- **Category:** Concurrency & runtime
- **What it actually means:** The mechanism by which a bounded consumer's inability to keep up is propagated to the producer as blocked time or refusals, instead of accumulating as unbounded memory.
- **Why it matters:** A payment pipeline without backpressure turns a slow downstream into an out-of-memory crash upstream; a bounded queue turns it into latency the caller can see.
- **Common confusion:** "Slow down the producer" is often implemented as a sleep; real backpressure is the producer blocking on `put` or receiving `false` from `offer`.
- **Taught in:** `phases/04-concurrency/05-producers-consumers-and-bounded-queues`
- **Related terms:** Load shedding, Condition variable, Bulkhead, Little's law

### Ballot
- **Category:** Distributed systems
- **What it actually means:** A totally ordered, globally unique proposal number in Paxos, built here as `(round, proposerId)` so two proposers can never issue the same one.
- **Why it matters:** Every Paxos safety argument is a comparison of ballots: an acceptor promises never to accept anything lower than the highest ballot it has promised.
- **Common confusion:** "The round" sounds like a global counter; each proposer picks its own ballots, and the tie-breaking id is what keeps them distinct.
- **Taught in:** `phases/06-replication-and-consensus/04-paxos`
- **Related terms:** Quorum, Livelock, Randomized election timeout

### Banker's rounding
- **Category:** Money & ledgers
- **What it actually means:** The `HALF_EVEN` rule: exact ties round to the even neighbour, so over many operations the expected rounding error is zero.
- **Why it matters:** `HALF_UP` drifts up by half a minor unit per tie, and across millions of fee calculations that drift is money that belongs to someone.
- **Common confusion:** "Banker's rounding fixes float errors" is wrong; it fixes rounding bias on ties and applies to decimals and integers alike, while float error is a property of the format.
- **Taught in:** `phases/10-money-and-ledgers/01-representing-money`, `phases/00-setup-and-tooling/07-decimal-and-integer-arithmetic`
- **Related terms:** Minor unit, Largest-remainder method, Fixed-point, Overflow

### Big-O
- **Category:** Data structures & algorithms
- **What it actually means:** An asymptotic upper bound up to a constant: `f = O(g)` means `f <= c * g` for all sufficiently large `n`.
- **Why it matters:** It predicts which structure survives a quarter-end batch, but constants, cache lines, and branch misprediction decide which one is faster at the sizes a payment system actually sees.
- **Common confusion:** "The complexity" is usually meant as a tight bound (Big-Theta); strictly, `O(n^2)` is a true statement about binary search.
- **Taught in:** `phases/01-complexity-and-core-structures/01-complexity-and-big-o`
- **Related terms:** Tail latency, Load factor, Heap

### BIN / IIN
- **Category:** Payment rails
- **What it actually means:** The issuer identification number at the start of a PAN under ISO/IEC 7812: six digits historically and eight since 2017, identifying the issuing institution and usually the product.
- **Why it matters:** BIN lookup drives network detection, routing, and risk rules, and a trie with range decomposition is how the lookup is made fast.
- **Common confusion:** "The first six digits" is out of date; BINs are now up to eight digits and are assigned in ranges that do not align to digit positions.
- **Taught in:** `phases/11-payment-rails/04-pan-luhn-and-bin-ranges`, `phases/01-complexity-and-core-structures/13-tries`
- **Related terms:** PAN, Trie, Longest-prefix match

### Blocking problem
- **Category:** Storage & transactions
- **What it actually means:** In two-phase commit, a prepared participant must wait for a dead coordinator to recover, holding its locks, because no timeout lets it safely choose between COMMIT and ABORT.
- **Why it matters:** It is the reason 2PC is avoided across organisational boundaries and why payment systems reach for sagas and outboxes instead.
- **Common confusion:** "2PC is slow" blames the round trips; the real cost is that one coordinator crash can leave locks held indefinitely.
- **Taught in:** `phases/07-storage-and-transactions/08-two-phase-commit`
- **Related terms:** In doubt, Prepare, Saga

### Break
- **Category:** Money & ledgers
- **What it actually means:** One unmatched record in a reconciliation, classified by which side has it and why it is likely there; every break has an owner and a resolution.
- **Why it matters:** Breaks are the output of reconciliation that people act on, and a typed break (missing ours, missing theirs, amount mismatch) tells the operator where to look.
- **Common confusion:** "A discrepancy" implies an error; many breaks are timing differences that resolve themselves the next day and must not be booked as adjustments.
- **Taught in:** `phases/10-money-and-ledgers/08-reconciliation`
- **Related terms:** Reconciliation, Adjusting entry

### Bulkhead
- **Category:** Distributed systems
- **What it actually means:** A bounded, non-blocking semaphore per dependency that caps how much of the caller's capacity one dependency can hold, independent of whether that dependency is healthy.
- **Why it matters:** When one processor slows down, a bulkhead keeps its connections from consuming every thread in the gateway, so other processors' traffic still flows.
- **Common confusion:** "A limit on concurrent calls" is often implemented as a circuit breaker; a breaker reacts to failures, a bulkhead reserves capacity regardless.
- **Taught in:** `phases/05-distributed-foundations/11-circuit-breakers-and-bulkheads`
- **Related terms:** Circuit breaker, Cascading failure, Backpressure, Little's law

### Burn rate
- **Category:** Reliability & operations
- **What it actually means:** The observed error ratio divided by the sustainable error ratio `1 - SLO`; a burn rate of 1 spends the error budget exactly at the end of the window.
- **Why it matters:** Alerting on burn rate rather than raw error rate pages for incidents that matter to the budget and stays quiet for noise that does not.
- **Common confusion:** "How fast we're failing" is often quoted as an error percentage; burn rate is relative to the SLO, so the same 1% error rate is a burn of 10 at a 99.9% SLO and 1 at 99%.
- **Taught in:** `phases/09-reliability-and-scale/01-slis-slos-and-error-budgets`
- **Related terms:** Error budget, SLO, SLI

## C

### camt.053
- **Category:** Money & ledgers
- **What it actually means:** The ISO 20022 end-of-day bank account statement message; each `Ntry` element is one line on the bank's side of a reconciliation.
- **Why it matters:** It is the external record a ledger is reconciled against, and the only evidence that money the ledger says arrived actually did.
- **Common confusion:** "The bank file" is often assumed to be a flat CSV; camt.053 is structured XML with its own identifiers, and the match key is chosen from its fields.
- **Taught in:** `phases/10-money-and-ledgers/08-reconciliation`
- **Related terms:** Reconciliation, Match key, Break, Settlement

### Capture
- **Category:** Payment rails
- **What it actually means:** The merchant's presentment of a clearing record against an open authorization for the final amount; it consumes the hold, creates the settlement obligation, and releases any uncaptured remainder.
- **Why it matters:** Capture is where the ledger finally moves and where refund limits and over-capture tolerance are measured.
- **Common confusion:** "Settling the payment" conflates capture with settlement; capture is an instruction sent in batch, settlement is the later net movement of funds between banks.
- **Taught in:** `phases/11-payment-rails/02-card-authorization-lifecycle`, `phases/11-payment-rails/01-how-a-card-payment-works`, `phases/10-money-and-ledgers/07-holds-and-available-balance`
- **Related terms:** Authorization, Clearing, Settlement

### Card testing
- **Category:** Risk & compliance
- **What it actually means:** Using a merchant's authorization endpoint to validate stolen card numbers with small purchases; also called carding or a BIN attack.
- **Why it matters:** The signal is attempts per card or per device over a window, not any one transaction, which is exactly what a velocity rule measures.
- **Common confusion:** "Fraud detection" is imagined as scoring one transaction; card testing is only visible as a rate across many.
- **Taught in:** `phases/13-risk-fraud-and-compliance/02-rules-engine-and-velocity-checks`
- **Related terms:** Velocity, Sliding window, Rate limiter

### Cascading failure
- **Category:** Distributed systems
- **What it actually means:** A dependency's latency consuming the caller's capacity (threads, connections, memory) so that unrelated features of the caller fail too.
- **Why it matters:** One slow processor can take down refunds, balance queries, and webhooks that never touch it, unless bulkheads and breakers isolate it.
- **Common confusion:** "One service took everything down" is blamed on the dependency; the mechanism is the caller waiting out timeouts instead of failing fast.
- **Taught in:** `phases/05-distributed-foundations/11-circuit-breakers-and-bulkheads`
- **Related terms:** Bulkhead, Circuit breaker, Retry storm

### CDE
- **Category:** Risk & compliance
- **What it actually means:** The cardholder data environment: every person, process, and technology that stores, processes, or transmits cardholder data or sensitive authentication data, plus connected-to and security-impacting systems.
- **Why it matters:** PCI DSS scope is the CDE, so every engineering decision that keeps a PAN out of a system shrinks the audit.
- **Common confusion:** "The payment system" is assumed to be the only in-scope component; logging, monitoring, and admin workstations connected to it are in the CDE too.
- **Taught in:** `phases/13-risk-fraud-and-compliance/08-pci-dss-for-engineers`
- **Related terms:** PCI DSS, PAN, Tokenization

### Change data capture
- **Category:** Messaging & streams
- **What it actually means:** Reading a database's write-ahead log to observe committed changes, so an outbox row becomes a published event without a polling relay.
- **Why it matters:** It keeps the outbox guarantee (published if and only if committed) while removing the relay's polling latency and load.
- **Common confusion:** "Debezium" is one implementation; CDC is the technique, and it still needs the outbox table to control what is published.
- **Taught in:** `phases/07-storage-and-transactions/13-transactional-outbox`
- **Related terms:** Transactional outbox, Relay, Write-ahead, Replication log

### Chart of accounts
- **Category:** Money & ledgers
- **What it actually means:** The schema of the ledger: every account that may be posted to, with its number, type, position in the hierarchy, and the meaning of its balance.
- **Why it matters:** It encodes which accounts are customer liabilities, which are the firm's cash, and which must reconcile to each other daily.
- **Common confusion:** "The account list" sounds like data; it is a schema, and adding an account is a change with regulatory meaning.
- **Taught in:** `phases/10-money-and-ledgers/04-chart-of-accounts`
- **Related terms:** Normal balance, Control account, Safeguarding

### Check digit
- **Category:** Data structures & algorithms
- **What it actually means:** One or more extra symbols appended to an identifier so the whole satisfies an equation; it detects accidental corruption of proven error classes and never detects adversarial change.
- **Why it matters:** Luhn, mod-97, and the ABA weighted sum each catch a different set of keying errors, and a validator must know which.
- **Common confusion:** "The number is valid" means its check digit is right; existence and ownership are proved by Confirmation of Payee or an authorization, never by arithmetic.
- **Taught in:** `phases/02-algorithms/15-numerical-algorithms`, `phases/11-payment-rails/11-iban-bic-and-account-validation`
- **Related terms:** Luhn check, Mod-97, ABA routing transit number

### Check-and-reserve
- **Category:** Payment systems
- **What it actually means:** The single atomic step that looks for an existing record (an idempotency key, a hold against available balance) and, finding room, inserts a reservation, so two concurrent callers cannot both succeed.
- **Why it matters:** It is the one line that prevents duplicate charges and double-spent balances; split into a read and a write, it is a race.
- **Common confusion:** "Check if the key exists" then "insert" is two steps; the mechanism is one conditional insert, and atomicity is the entire point.
- **Taught in:** `phases/05-distributed-foundations/09-idempotency-keys`, `phases/10-money-and-ledgers/07-holds-and-available-balance`, `phases/12-payment-systems-design/01-idempotent-payment-api`
- **Related terms:** Check-then-act, Idempotency key, Available balance, Compare-and-swap

### Check-then-act
- **Category:** Concurrency & runtime
- **What it actually means:** A decision based on a value read earlier, followed by a write; correct only if the read and the write are in the same critical section or the write's CAS expects the value that was read.
- **Why it matters:** "If balance is enough, subtract" is the canonical payments race, and every double-spend is this pattern with the lock missing.
- **Common confusion:** "Each step is thread-safe" does not make the pair safe; atomicity of the parts says nothing about atomicity of the whole.
- **Taught in:** `phases/04-concurrency/02-locks-and-atomic-counters`
- **Related terms:** Check-and-reserve, Lost update, Compare-and-swap, Mutex

### Checkpoint
- **Category:** Storage & transactions
- **What it actually means:** Writing the full state tagged with the log position it covers, then truncating the log before that position; the tag is what keeps replay idempotent.
- **Why it matters:** Without checkpoints a write-ahead log or event stream grows forever and recovery time grows with it.
- **Common confusion:** "Snapshot" in event sourcing is the same idea, but there the snapshot is a cache that can be deleted; a WAL checkpoint is the new base and the truncated log is gone.
- **Taught in:** `phases/07-storage-and-transactions/02-write-ahead-logs`, `phases/08-messaging-and-streams/05-event-sourcing`
- **Related terms:** Write-ahead, Event sourcing, fsync

### Circuit breaker
- **Category:** Distributed systems
- **What it actually means:** A per-caller state machine (CLOSED, OPEN, HALF_OPEN) that converts a predictable failure into an instant rejection once a sliding window of outcomes crosses a failure-rate threshold with enough samples.
- **Why it matters:** It stops a gateway from spending threads on a processor that is already known to be down and lets a few probes discover when it is back.
- **Common confusion:** "It stops calling the service when it's down" skips the sample-size guard; a breaker that trips on one error is a liability, which is why minimum calls exists.
- **Taught in:** `phases/05-distributed-foundations/11-circuit-breakers-and-bulkheads`
- **Related terms:** Bulkhead, Sliding window, Cascading failure, Retryable outcome

### Clearing
- **Category:** Payment rails
- **What it actually means:** Transmitting, matching, and confirming payment instructions and computing the resulting obligations between participants; no money moves.
- **Why it matters:** Clearing produces the net positions that settlement later discharges, and a payment can be cleared for days before it is settled.
- **Common confusion:** "Processing the payment" uses clearing and settlement interchangeably; clearing computes who owes what, settlement pays it.
- **Taught in:** `phases/11-payment-rails/15-settlement-and-clearing`, `phases/11-payment-rails/01-how-a-card-payment-works`
- **Related terms:** Settlement, Capture, Multilateral netting, Deferred net settlement

### Command vs event
- **Category:** Payment systems
- **What it actually means:** A command is an imperative request that the system may refuse; an event is a past-tense fact that is already true and is never validated again.
- **Why it matters:** A merchant's capture request can be rejected, but a processor's webhook saying the capture settled is a fact the state machine must record or recognise as a duplicate, never ignore.
- **Common confusion:** "API call vs webhook" captures the typical sources, but the distinction is about authority: who gets to say no.
- **Taught in:** `phases/08-messaging-and-streams/05-event-sourcing`, `phases/12-payment-systems-design/02-payment-state-machine`
- **Related terms:** State machine, Aggregate, Guard, Transition

### Compare-and-swap
- **Category:** Concurrency & runtime
- **What it actually means:** One hardware instruction that stores a new value only if the word still holds the expected value, reporting success or failure; the basis of `AtomicLong` and of every lock-free algorithm.
- **Why it matters:** A CAS loop makes a counter or a balance update atomic without a lock, and counting its retries measures contention directly.
- **Common confusion:** "An atomic" is any `Atomic*` class; CAS is the primitive underneath, and a CAS-based check-then-act is only correct if the CAS expects the value the check read.
- **Taught in:** `phases/04-concurrency/02-locks-and-atomic-counters`
- **Related terms:** Atomicity, Lost update, Contention, Optimistic concurrency

### Compensation
- **Category:** Storage & transactions
- **What it actually means:** A new forward transaction that semantically reverses a committed saga step; both the step and its compensation stay in history, and the compensation must eventually succeed and be safe to repeat.
- **Why it matters:** A saga that reserved funds and then failed to capture does not roll back; it releases the hold with a second committed action.
- **Common confusion:** "The rollback" implies the original disappears; compensation is a reversal in the ledger sense, and an auditor sees both.
- **Taught in:** `phases/07-storage-and-transactions/12-saga-pattern`
- **Related terms:** Saga, Reversal, Authorization hold

### Condition variable
- **Category:** Concurrency & runtime
- **What it actually means:** A waiting room attached to a lock: `await` atomically releases the lock and parks the thread, `signal` moves one parked thread back to competing for the lock.
- **Why it matters:** It is how a bounded queue blocks producers when full and consumers when empty without spinning, and the `while` around every `await` is what makes it correct.
- **Common confusion:** "wait/notify" is treated as a message; a signal carries no data, and a spurious wakeup is permitted, so the predicate must be re-checked.
- **Taught in:** `phases/04-concurrency/05-producers-consumers-and-bounded-queues`
- **Related terms:** Mutex, Backpressure

### Confirmation of Payee
- **Category:** Payment rails
- **What it actually means:** A pre-payment API call to the receiving bank that answers whether the named party owns the destination account.
- **Why it matters:** It is the only control in the validation chain that addresses misdirected and authorised-push-payment fraud, because a checksum cannot prove ownership.
- **Common confusion:** "Name checking" is assumed to be part of IBAN validation; IBAN validation is arithmetic on the string, CoP is a network call about the account.
- **Taught in:** `phases/11-payment-rails/11-iban-bic-and-account-validation`
- **Related terms:** IBAN, Check digit, ABA routing transit number

### Consent
- **Category:** Payment rails
- **What it actually means:** The durable record of user, client, scopes, and expiry that every open-banking token points back to; it is what the user revokes in their banking app.
- **Why it matters:** Tokens are derived credentials; revoking the consent must invalidate every token and refresh-token family issued under it.
- **Common confusion:** "The connection" is thought of as the token; the consent outlives any individual token and is the thing with legal meaning under PSD2.
- **Taught in:** `phases/11-payment-rails/13-open-banking-and-account-aggregation`
- **Related terms:** Access token, Refresh token, Authorization code

### Contention
- **Category:** Concurrency & runtime
- **What it actually means:** Multiple threads needing the same lock or memory word at the same time; visible as parked threads on a mutex, retries on a CAS, and operations per second that stop scaling with cores.
- **Why it matters:** A hot ledger account is contention on one row, and the fix (striping, single-writer partitioning) is the same shape at every scale.
- **Common confusion:** "The lock is hot" is diagnosed from CPU; contention shows up as idle cores and a flat throughput curve, not high CPU.
- **Taught in:** `phases/04-concurrency/02-locks-and-atomic-counters`
- **Related terms:** Lock striping, Compare-and-swap, Hot account, Mutex

### Control account
- **Category:** Money & ledgers
- **What it actually means:** A general-ledger account whose balance must equal the sum of a sub-ledger's account balances; the daily check between them is an engineering invariant.
- **Why it matters:** Customer funds held as one liability in the GL must equal the sum of every customer's sub-ledger balance, or the safeguarding reconciliation fails.
- **Common confusion:** "The GL total" is treated as a report; it is an account that receives postings, and the sub-ledger rolls up to it by rule.
- **Taught in:** `phases/10-money-and-ledgers/04-chart-of-accounts`
- **Related terms:** Chart of accounts, Safeguarding, Trial balance

### CTR threshold
- **Category:** Risk & compliance
- **What it actually means:** The cash amount at or above which a currency transaction report is filed automatically (31 CFR 1010.311 in the US sets it at $10,000); the filing is routine, not an accusation.
- **Why it matters:** Monitoring scenarios for structuring are defined relative to this threshold, because the crime is splitting cash to stay under it.
- **Common confusion:** "The ten thousand dollar rule" is thought to be about suspicion; a CTR is mechanical, a SAR is the judgement call.
- **Taught in:** `phases/13-risk-fraud-and-compliance/06-aml-transaction-monitoring`
- **Related terms:** Structuring, SAR
## D

### Data race
- **Category:** Concurrency & runtime
- **What it actually means:** Two conflicting accesses (at least one a write) to the same variable with no happens-before order between them; a property of the program under the language's memory model, not of any particular run.
- **Why it matters:** A program with a data race has no defined behaviour under the Java and Go memory models, so a balance read may be stale, torn, or impossible, and a test that passes proves nothing.
- **Common confusion:** "A race condition" is used as a synonym; a race condition is a timing bug between correctly synchronised operations, and `volatile` fixes neither of them for compound updates.
- **Taught in:** `phases/04-concurrency/01-threads-and-the-memory-model`, `phases/00-setup-and-tooling/03-testing-from-the-standard-library`
- **Related terms:** Race condition, Happens-before, Lost update

### Dead-letter queue
- **Category:** Messaging & streams
- **What it actually means:** Where a message or webhook delivery goes after its final attempt fails; it exists so lost events are visible rather than silent.
- **Why it matters:** A settlement event that cannot be delivered is money nobody is accounting for until someone reads the dead-letter queue.
- **Common confusion:** "The failed ones" suggests the queue is an error log; it is a work queue with an owner, and an empty one is a monitored condition.
- **Taught in:** `phases/12-payment-systems-design/05-webhooks-and-callbacks`
- **Related terms:** At-least-once delivery, Idempotent consumer, Retry storm, Unknown outcome

### Deadlock
- **Category:** Concurrency & runtime
- **What it actually means:** A set of threads each waiting for a resource held by another member of the set, so none can proceed without an external action.
- **Why it matters:** Two transfers that lock accounts in opposite orders deadlock under load, and the symptom is a payment service that is alive, idle, and processing nothing.
- **Common confusion:** "It hung" covers deadlock, livelock, and a slow dependency; only a cycle in the wait-for graph is a deadlock.
- **Taught in:** `phases/04-concurrency/07-deadlocks-and-lock-ordering`
- **Related terms:** Lock ordering, Livelock, Mutex

### Debit / credit
- **Category:** Money & ledgers
- **What it actually means:** The two columns of a ledger entry: a debit increases asset and expense accounts and decreases the others, a credit does the reverse; neither carries a meaning of good or bad.
- **Why it matters:** Every journal entry must have equal debits and credits in every currency it touches, and that equality is the invariant the trial balance proves.
- **Common confusion:** "Money in / money out" is the bank-statement view, which is from the bank's perspective; in the firm's own books a customer deposit is a credit to a liability.
- **Taught in:** `phases/10-money-and-ledgers/03-double-entry-ledger`
- **Related terms:** Normal balance, Posting, Journal entry, Trial balance

### Deferred net settlement
- **Category:** Payment rails
- **What it actually means:** Netting followed by settlement at fixed cycles; cheap in liquidity, but it creates interbank credit exposure until the cycle settles.
- **Why it matters:** Card schemes and ACH settle this way, which is why a cleared payment is still a claim against a counterparty until the settlement run completes.
- **Common confusion:** "End-of-day settlement" is heard as a scheduling detail; it is a risk choice that trades liquidity for exposure, and the opposite choice is RTGS.
- **Taught in:** `phases/11-payment-rails/15-settlement-and-clearing`
- **Related terms:** Multilateral netting, Real-time gross settlement, Settlement finality, Herstatt risk

### Degradation ladder
- **Category:** Payment systems
- **What it actually means:** A pre-decided order in which request classes are refused under overload, which never includes the paths that converge in-flight money.
- **Why it matters:** Under load a gateway can drop reporting and new authorizations before it drops capture confirmations and webhook processing, because the latter resolve money already in motion.
- **Common confusion:** "Load shedding" is often a single global limit; a ladder is ordered by what each request class costs to abandon.
- **Taught in:** `phases/14-payments-at-scale/11-payments-system-design-interviews`, `phases/14-payments-at-scale/01-high-availability-payment-gateway`
- **Related terms:** Load shedding, Unknown outcome, Bulkhead, Rate limiter

### Deterministic simulation
- **Category:** Reliability & operations
- **What it actually means:** Replacing every source of nondeterminism (time, scheduling, network, randomness) with a model driven from one seed, so a run is a pure function of the seed and can be replayed exactly.
- **Why it matters:** It is how this course tests Raft, sagas, and a whole payment system: a failing seed is a permanent, one-line reproduction of a distributed bug.
- **Common confusion:** "We test with a fake clock" is one ingredient; a simulation is deterministic only if the network, the scheduler, and every random draw are seeded too.
- **Taught in:** `phases/14-payments-at-scale/10-simulation-testing-of-payment-systems`, `phases/06-replication-and-consensus/05-raft-leader-election`, `phases/05-distributed-foundations/10-retries-backoff-and-jitter`
- **Related terms:** Invariant, Fake, Property-based test, Priority queue

### Dual write
- **Category:** Storage & transactions
- **What it actually means:** Two writes to two systems (a database row and a message broker, say) with no transaction spanning them; a crash between them leaves one without the other, forever.
- **Why it matters:** "Save the payment, then publish the event" is a dual write, and it is the origin of most ledger-versus-notification disagreements.
- **Common confusion:** "We save and then publish" is believed safe because both calls are reliable; the failure is the gap between them, not either call.
- **Taught in:** `phases/07-storage-and-transactions/13-transactional-outbox`, `phases/14-payments-at-scale/03-exactly-once-money-movement`
- **Related terms:** Transactional outbox, Relay, Atomic commitment

### Dual-message system
- **Category:** Payment rails
- **What it actually means:** A card rail where authorization and clearing are separate messages at separate times, so the final amount can differ from the authorized amount.
- **Why it matters:** Tips, incremental holds, and partial captures only exist because credit rails are dual-message; a single-message rail has none of them.
- **Common confusion:** "Credit card processing" is thought to describe all cards; PIN debit is single-message, and the same plastic can run on either.
- **Taught in:** `phases/11-payment-rails/01-how-a-card-payment-works`
- **Related terms:** Authorization, Capture

## E

### Error budget
- **Category:** Reliability & operations
- **What it actually means:** `1 - SLO` over the window, expressed in events or minutes; the quantity that alerts, launch decisions, and freeze rules are stated in.
- **Why it matters:** It turns reliability into a currency the team spends deliberately on deploys and feature launches instead of arguing about each incident.
- **Common confusion:** "How much we can break" is heard as permission; the budget policy is what decides what happens when it is exhausted.
- **Taught in:** `phases/09-reliability-and-scale/01-slis-slos-and-error-budgets`
- **Related terms:** SLO, Burn rate, SLI

### Event sourcing
- **Category:** Messaging & streams
- **What it actually means:** Treating the ordered, immutable event stream as the system of record and deriving every piece of current state by folding over it, with `replay(all) == replay(prefix).applyAll(suffix)`.
- **Why it matters:** A ledger is naturally event-sourced: the postings are the facts, the balances are a fold, and any balance can be recomputed and audited.
- **Common confusion:** "Store the events" is often done alongside a mutable table that is treated as the truth; event sourcing means the events are the truth and the table is a projection.
- **Taught in:** `phases/08-messaging-and-streams/05-event-sourcing`
- **Related terms:** Aggregate, Optimistic concurrency, Append-only journal

### Eventual consistency
- **Category:** Distributed systems
- **What it actually means:** If writes stop, replicas converge; there is no promise about what any read returns in the meantime or how long "eventually" is.
- **Why it matters:** A balance read from an eventually consistent replica can be older than a write the same client already saw acknowledged, which is a stale read, not a bug in the replica.
- **Common confusion:** "It'll sort itself out" is treated as a weak but usable guarantee; without session guarantees it offers nothing to any single reader.
- **Taught in:** `phases/05-distributed-foundations/05-consistency-models`
- **Related terms:** Linearizability, Sequential consistency

### Eviction
- **Category:** Data structures & algorithms
- **What it actually means:** Removal of a cache entry because the cache is at capacity; the policy (LRU, LFU, TinyLFU) chooses the victim, and the eviction count tells you whether the cache is too small.
- **Why it matters:** Evicting a hot BIN-table entry during a batch scan is the difference between a cache that helps and one that adds a round trip.
- **Common confusion:** "The cache dropped it" also covers expiration, which is a freshness decision against a clock; the two counts diagnose different problems.
- **Taught in:** `phases/03-systems-data-structures/08-lru-and-lfu-caches`
- **Related terms:** Working set, Scan resistance, TTL

### Exactly-once
- **Category:** Messaging & streams
- **What it actually means:** The effect happens once: delivery is at-least-once, and a deduplication record written atomically with the effect absorbs the extra deliveries.
- **Why it matters:** Exactly-once money movement is achievable, but only as a property of the receiver's transaction, never of the network.
- **Common confusion:** "The message is delivered once" is impossible under the fallacies; what is possible is effectively-once processing by an idempotent consumer.
- **Taught in:** `phases/14-payments-at-scale/03-exactly-once-money-movement`, `phases/07-storage-and-transactions/13-transactional-outbox`
- **Related terms:** At-least-once delivery, Inbox, Idempotent consumer, Transactional outbox

### Exponential backoff
- **Category:** Distributed systems
- **What it actually means:** `min(cap, base * 2^attempt)`: the deterministic part of a retry delay, which spaces retry waves apart but does not desynchronise the clients in each wave.
- **Why it matters:** Backoff alone turns one retry storm into a series of smaller ones at predictable instants; jitter is what breaks the synchrony.
- **Common confusion:** "Wait longer each time" is believed to be the fix for thundering herds; without jitter every client waits the same longer time.
- **Taught in:** `phases/05-distributed-foundations/10-retries-backoff-and-jitter`
- **Related terms:** Jitter, Retry storm, Retryable outcome

## F

### Failover
- **Category:** Distributed systems
- **What it actually means:** Detecting a leader failure (a timeout), choosing the most advanced eligible follower, and reconfiguring clients and the old leader; each step can lose writes or create two leaders.
- **Why it matters:** For a ledger database, the choice between losing acknowledged writes and waiting for a synchronous replica is a failover design decision made long before the outage.
- **Common confusion:** "Promote a replica" sounds like one step; detection, selection, and fencing are three, and skipping fencing is how split brain happens.
- **Taught in:** `phases/06-replication-and-consensus/01-replication-strategies`
- **Related terms:** Split brain, Fencing token, Synchronous replica, Replication log

### Fake
- **Category:** Reliability & operations
- **What it actually means:** A working in-memory implementation of an interface with controllable behaviour: a `FakeClock` really keeps time, a fake processor really approves and declines.
- **Why it matters:** The course tests payment flows against fakes with scripted outcomes (timeouts, duplicate webhooks), which a mock that only records calls cannot express.
- **Common confusion:** "A mock" is used for both; a mock records calls and asserts on expectations, a fake behaves.
- **Taught in:** `phases/00-setup-and-tooling/03-testing-from-the-standard-library`, `phases/12-payment-systems-design/14-testing-payment-systems`
- **Related terms:** Deterministic simulation, Property-based test

### Fallacies of distributed computing
- **Category:** Distributed systems
- **What it actually means:** The eight assumptions (Deutsch 1994, Gosling 1997) that hold for a local call and fail for a remote one: reliable network, zero latency, infinite bandwidth, secure network, fixed topology, one administrator, zero transport cost, homogeneous network.
- **Why it matters:** Each one maps to a payments failure (duplicate charges, tail latency, split brain, format drift), and together they are the pre-mortem checklist.
- **Common confusion:** "The network is flaky" is the first fallacy only; the others are about time, topology, and ownership, and a process cannot detect any of them from the inside.
- **Taught in:** `phases/05-distributed-foundations/01-the-fallacies-of-distributed-computing`
- **Related terms:** Ambiguous outcome, Tail latency, Split brain, Fingerprint

### False positive
- **Category:** Data structures & algorithms
- **What it actually means:** A Bloom filter reporting "possibly present" for a key that was never added; the only error the filter can make, at a rate set by `m`, `k`, and `n`.
- **Why it matters:** The filter can stand in front of an idempotency store because it never produces a false negative, so a miss is a guaranteed miss.
- **Common confusion:** "The filter was wrong" is treated as a defect; the rate is a design parameter, and the defect is saturation when the filter is fed far more keys than it was sized for.
- **Taught in:** `phases/03-systems-data-structures/01-bloom-filters`
- **Related terms:** False-positive rate, Idempotency key

### False-positive rate
- **Category:** Risk & compliance
- **What it actually means:** `fp / (fp + tn)` for a screening or monitoring threshold: the share of innocent cases that raise an alert, and therefore the analyst cost of the threshold.
- **Why it matters:** A sanctions threshold is chosen from labelled data by trading this rate against recall, because an analyst team has finite hours.
- **Common confusion:** "Too many alerts" is often measured as precision (`tp / (tp + fp)`, the fraction of alerts worth raising); the two move differently when the base rate changes.
- **Taught in:** `phases/13-risk-fraud-and-compliance/04-sanctions-screening`
- **Related terms:** Fuzzy match, Jaro-Winkler, False positive

### Fencing token
- **Category:** Distributed systems
- **What it actually means:** A monotonically increasing number issued with each lease or leader term that the protected resource checks on every write, so a stale holder's writes are rejected.
- **Why it matters:** Timeouts cannot prevent split brain; only the resource refusing a write with an old token can, and a ledger database is the resource.
- **Common confusion:** "The lock has an epoch" is only half of it; fencing requires the downstream resource to check the epoch, not just the lock service to issue it.
- **Taught in:** `phases/05-distributed-foundations/01-the-fallacies-of-distributed-computing`, `phases/06-replication-and-consensus/01-replication-strategies`
- **Related terms:** Split brain, Failover, Randomized election timeout, Lease

### Fingerprint
- **Category:** Payment systems
- **What it actually means:** A SHA-256 over a canonical encoding of a request's effectful fields, excluding the idempotency key itself, used to detect a key reused for a different operation.
- **Why it matters:** Two requests with the same key and different fingerprints are a client bug (`422`), never a replay, and returning the stored response for them would charge the wrong amount.
- **Common confusion:** "A hash of the body" would include timestamps and client metadata; the fingerprint covers only the fields that change the effect.
- **Taught in:** `phases/05-distributed-foundations/09-idempotency-keys`, `phases/12-payment-systems-design/01-idempotent-payment-api`, `phases/14-payments-at-scale/03-exactly-once-money-movement`
- **Related terms:** Idempotency key, Replay, Deterministic simulation

### Fixed-point
- **Category:** Money & ledgers
- **What it actually means:** A decimal represented as an integer with an implied fixed number of decimal places; `Money` takes its scale from the currency's ISO 4217 exponent.
- **Why it matters:** It is the course's rule that money is never a float: integer minor units are exact, overflow-checked, and identical in Java, Python, and Go.
- **Common confusion:** "Integer cents" assumes two decimal places; JPY has zero and KWD has three, so the scale must come from the currency.
- **Taught in:** `phases/00-setup-and-tooling/07-decimal-and-integer-arithmetic`, `phases/10-money-and-ledgers/01-representing-money`
- **Related terms:** Minor unit, Overflow, Banker's rounding

### fsync
- **Category:** Storage & transactions
- **What it actually means:** `fsync(2)`: block until the storage device acknowledges every dirty page of the file; `write(2)` and a buffered `flush()` do not do this.
- **Why it matters:** A write-ahead log is only write-ahead if the record is synced before the state changes; without fsync an acknowledged payment can vanish on power loss.
- **Common confusion:** "Flush to disk" is said of `flush()`, which empties the process buffer into the kernel's page cache and makes no durability promise.
- **Taught in:** `phases/07-storage-and-transactions/02-write-ahead-logs`
- **Related terms:** Write-ahead, Torn write, Synchronous replica

### Fuzzy match
- **Category:** Risk & compliance
- **What it actually means:** A pairing made on a similarity score from a defined measure (Jaro-Winkler for names, amount plus a date window for reconciliation) compared against a threshold chosen from labelled data.
- **Why it matters:** Sanctions lists and bank statements rarely share an exact key, and a fuzzy match is how the rest gets paired, provided the report labels it as such.
- **Common confusion:** "Close enough" hides the threshold; a fuzzy match is a heuristic with a measured false-positive rate and recall, not a judgement call.
- **Taught in:** `phases/13-risk-fraud-and-compliance/04-sanctions-screening`, `phases/10-money-and-ledgers/08-reconciliation`
- **Related terms:** Jaro-Winkler, Match key, False-positive rate

## G

### Guard
- **Category:** Payment systems
- **What it actually means:** A predicate attached to a state-machine transition rule that must hold over the current state and the event for the rule to fire; evaluated once at apply time and never re-evaluated on replay.
- **Why it matters:** "Capture amount within tolerance of the authorization" is a guard, and recording the transition rather than re-checking the guard on replay is what keeps history stable when the rules change.
- **Common confusion:** "The business rule check" is often put in the API handler; in the machine it is part of the transition table, so every path is checked the same way.
- **Taught in:** `phases/12-payment-systems-design/02-payment-state-machine`
- **Related terms:** State machine, Transition, Command vs event, Invariant

## H

### Happens-before
- **Category:** Concurrency & runtime
- **What it actually means:** A partial order over actions: within a thread by program order, across threads by unlock-then-lock, volatile write-then-read, start, join, and channel send-then-receive; across processes (Lamport) by send-then-receive. A read ordered after a write by this relation must see it.
- **Why it matters:** It is the only reason a balance written under one lock is visible to a reader under the same lock; hardware folklore about caches is not a guarantee.
- **Common confusion:** "Memory barrier" and "a came before b in time" are both wrong; happens-before is about possible causation defined by the language or protocol, not about wall-clock order.
- **Taught in:** `phases/04-concurrency/01-threads-and-the-memory-model`, `phases/05-distributed-foundations/03-lamport-and-vector-clocks`
- **Related terms:** Data race, Lamport clock, Vector clock

### Hash ring
- **Category:** Data structures & algorithms
- **What it actually means:** A sorted array of `(hash, node)` points over the 64-bit hash space treated as a circle; a key belongs to the first point clockwise from its own hash.
- **Why it matters:** Sharding a ledger by account across a changing set of nodes with a ring moves about `1/(N+1)` of keys when a node joins, where mod-N moves nearly all of them.
- **Common confusion:** "Consistent hashing" is often described as the ring; the ring is one implementation, rendezvous hashing is another with the same monotonicity property.
- **Taught in:** `phases/03-systems-data-structures/06-consistent-hashing`
- **Related terms:** Split brain, Partition

### Heap
- **Category:** Data structures & algorithms
- **What it actually means:** A complete binary tree stored in an array where every parent is less than or equal to its children; only the root's position is guaranteed.
- **Why it matters:** A retry scheduler, a timer wheel, and Dijkstra's frontier are all heaps, and decrease-key with an index map is what makes rescheduling O(log n).
- **Common confusion:** "A sorted thing you pop from" is wrong; the array is not sorted, and iterating it in order yields garbage.
- **Taught in:** `phases/01-complexity-and-core-structures/09-heaps-and-priority-queues`
- **Related terms:** Priority queue, Negative cycle, Retry storm

### Herstatt risk
- **Category:** Payment rails
- **What it actually means:** Loss of the full principal when one leg of a two-legged transaction settles and the other does not; named for the 1974 failure of Bankhaus Herstatt, which cost counterparties about 620 million dollars.
- **Why it matters:** It is why FX settles through payment-versus-payment at CLS and why a single balanced journal entry on one ledger is the safest form of atomic exchange.
- **Common confusion:** "Settlement risk" is used for any settlement failure; Herstatt risk is specifically the asymmetry between two legs.
- **Taught in:** `phases/11-payment-rails/15-settlement-and-clearing`
- **Related terms:** Settlement finality, Deferred net settlement, Atomic commitment

### Histogram
- **Category:** Reliability & operations
- **What it actually means:** A fixed set of ranges with a count per range; it answers "how many at or below x" and therefore any percentile within bucket resolution, and two histograms with the same layout merge by element-wise addition.
- **Why it matters:** Fleet-wide p99 authorization latency can only be computed from merged histograms; averaging per-host percentiles is wrong.
- **Common confusion:** "Latency buckets" are assumed to be linear; log-linear bucketing gives a bounded relative error at every magnitude with fixed memory.
- **Taught in:** `phases/09-reliability-and-scale/06-metrics-and-histograms`
- **Related terms:** Tail latency, SLI, Little's law

### HMAC
- **Category:** Payment systems
- **What it actually means:** RFC 2104's keyed message authentication code, `H((K xor opad) || H((K xor ipad) || m))`, which is immune to length extension and proves origin and integrity of a message.
- **Why it matters:** A webhook signature is an HMAC over the timestamp and body; it proves the processor sent it, and binding the timestamp is what defeats replay.
- **Common confusion:** "A signed hash" suggests freshness too; an HMAC says nothing about when the message was made, so the tolerance window is a separate control.
- **Taught in:** `phases/12-payment-systems-design/05-webhooks-and-callbacks`
- **Related terms:** Fingerprint, Idempotent consumer, Dead-letter queue

### Hot account
- **Category:** Payment systems
- **What it actually means:** A single ledger account receiving a write rate that exceeds one row's serialization capacity (roughly a thousand writes per second on a typical relational primary).
- **Why it matters:** A platform's fee-revenue account or a marketplace's settlement account is hot by construction, and read replicas do nothing for it.
- **Common confusion:** "Scaling problem" invites sharding the whole ledger; the fix is single-writer partitioning or sharded credit sub-accounts that roll up, which keeps the trial balance intact.
- **Taught in:** `phases/14-payments-at-scale/11-payments-system-design-interviews`
- **Related terms:** Contention, Lock striping, Control account
## I

### IBAN
- **Category:** Payment rails
- **What it actually means:** ISO 13616's International Bank Account Number: a two-letter country code, two mod-97 check digits, and the country's domestic BBAN; a wrapper that adds a checksum, not a new account number.
- **Why it matters:** SEPA payments route on it, and validating one correctly means checking length by country, the mod-97 remainder, and nothing about ownership.
- **Common confusion:** "The international account number" is thought to replace the local one; the BBAN inside it is what the receiving bank actually routes on.
- **Taught in:** `phases/11-payment-rails/11-iban-bic-and-account-validation`
- **Related terms:** Mod-97, Check digit, Confirmation of Payee

### Idempotency key
- **Category:** Payment systems
- **What it actually means:** A client-chosen opaque token that names one operation across all of its retries; the server stores its first response under `(scope, key)` and replays it for a retry with the same fingerprint, returning a conflict for a different one.
- **Why it matters:** It is the receiver-side construction that makes "safe to retry" true for a charge, since the network cannot deliver exactly once.
- **Common confusion:** "A unique request ID" is generated per attempt; an idempotency key must be the same across every retry of the same intent, and it is scoped per merchant so two merchants' keys never collide.
- **Taught in:** `phases/05-distributed-foundations/09-idempotency-keys`, `phases/12-payment-systems-design/01-idempotent-payment-api`, `phases/14-payments-at-scale/03-exactly-once-money-movement`, `phases/07-storage-and-transactions/12-saga-pattern`
- **Related terms:** Fingerprint, Replay, Check-and-reserve, Lease

### Idempotent consumer
- **Category:** Messaging & streams
- **What it actually means:** A handler that records each event id atomically with the state change it causes, so a redelivered event is a no-op instead of a second effect.
- **Why it matters:** Webhooks and outbox relays deliver at least once; the consumer's inbox table is the only thing that makes that safe for money.
- **Common confusion:** "Dedupe" is often a cache check before the handler; if the id is not written in the same transaction as the effect, a crash between them re-applies the event.
- **Taught in:** `phases/12-payment-systems-design/05-webhooks-and-callbacks`, `phases/14-payments-at-scale/03-exactly-once-money-movement`
- **Related terms:** Inbox, At-least-once delivery, Exactly-once, Transition

### Idempotent operation
- **Category:** Distributed systems
- **What it actually means:** An operation whose effect after N executions equals its effect after one; the receiver, not the network, must construct this property.
- **Why it matters:** It is the only answer to the ambiguous outcome: if the operation is idempotent, the client can retry from a timeout without knowing what happened.
- **Common confusion:** "Safe to retry" is assumed of reads and denied of writes; a write becomes idempotent by remembering what it did under a key.
- **Taught in:** `phases/05-distributed-foundations/01-the-fallacies-of-distributed-computing`
- **Related terms:** Idempotency key, Ambiguous outcome, Idempotent post, Checkpoint

### Idempotent post
- **Category:** Money & ledgers
- **What it actually means:** Posting the same journal entry id with the same content twice has the effect of posting it once; the same id with different content is a conflict, not a silent overwrite.
- **Why it matters:** A retried ledger write must not double-post, and a mismatched retry must be loud, because a double-posted entry still balances and the trial balance will not catch it.
- **Common confusion:** "Safe to retry" is sometimes implemented as "ignore duplicates by id", which silently accepts a retry with a different amount.
- **Taught in:** `phases/10-money-and-ledgers/03-double-entry-ledger`
- **Related terms:** Journal entry, Idempotency key, Fingerprint, Trial balance

### In doubt
- **Category:** Storage & transactions
- **What it actually means:** A 2PC participant in PREPARED that has not heard the coordinator's decision; it holds its locks and can resolve nothing by itself.
- **Why it matters:** In-doubt transactions are the concrete cost of the blocking problem, and the reason participants must log PREPARED durably before voting.
- **Common confusion:** "Stuck transaction" is sometimes resolved by timeout; a timeout cannot choose safely, which is why the only exits are coordinator recovery or a heuristic outcome.
- **Taught in:** `phases/07-storage-and-transactions/08-two-phase-commit`
- **Related terms:** Prepare, Blocking problem

### Inbox
- **Category:** Messaging & streams
- **What it actually means:** A consumer-side table of handled event ids written in the same transaction as the side effect; the mirror image of the outbox.
- **Why it matters:** It is the component that turns at-least-once delivery into exactly-once effect for money movement.
- **Common confusion:** "Processed-messages table" is often populated after the effect commits; the write must be in the same transaction or a crash between them double-applies.
- **Taught in:** `phases/14-payments-at-scale/03-exactly-once-money-movement`
- **Related terms:** Transactional outbox, Idempotent consumer, Exactly-once, Transition

### Incremental authorization
- **Category:** Payment rails
- **What it actually means:** A second approval request against an open authorization that raises the held amount, subject to the same available-balance check; it resets the validity window and is permitted only in defined merchant categories and only before any capture.
- **Why it matters:** Hotels and car rentals depend on it, and modelling it as growing one hold rather than opening a second keeps capture matching unambiguous.
- **Common confusion:** "They added to the hold" is imagined as a new authorization; a new authorization would need a new capture and a new matching rule.
- **Taught in:** `phases/11-payment-rails/02-card-authorization-lifecycle`, `phases/10-money-and-ledgers/07-holds-and-available-balance`
- **Related terms:** Authorization, Authorization hold, Capture

### Interchange
- **Category:** Payment rails
- **What it actually means:** The fee the acquirer pays the issuer per card transaction, set by the network's published rate tables by card type, channel, and merchant category.
- **Why it matters:** It is the largest piece of the merchant discount rate and the number that changes when a capture is late or a card is downgraded.
- **Common confusion:** "The card fee" conflates interchange with the merchant discount rate, which adds scheme fees and the acquirer's margin on top.
- **Taught in:** `phases/11-payment-rails/01-how-a-card-payment-works`
- **Related terms:** Merchant discount rate, Settlement, Late presentment

### Invariant
- **Category:** Reliability & operations
- **What it actually means:** A predicate over system state that every reachable state satisfies; a safety invariant must hold at every instant, a quiescence invariant holds once every message has been delivered or dropped and every timer has fired.
- **Why it matters:** "Debits equal credits", "available balance is never negative", and "no idempotency key charges twice" are the oracles that property tests and simulations check.
- **Common confusion:** "The thing that should always be true" is tested at the end; a safety invariant must be checked after every step, or the violation is missed.
- **Taught in:** `phases/12-payment-systems-design/14-testing-payment-systems`, `phases/14-payments-at-scale/10-simulation-testing-of-payment-systems`
- **Related terms:** Property-based test, Deterministic simulation, Trial balance, Linearizability

## J

### Jaro-Winkler
- **Category:** Risk & compliance
- **What it actually means:** A name-similarity score: Jaro similarity is `(m/a + m/b + (m - t)/m) / 3` over matches `m` within a window and half-transpositions `t`, and the Winkler boost adds `p * 0.1 * (1 - jaro)` for a common prefix of up to four characters.
- **Why it matters:** Sanctions screening scores every candidate name with it, and the threshold chosen from labelled data sets the trade between recall and analyst load.
- **Common confusion:** "Edit distance for names" is Levenshtein; Jaro-Winkler charges less for transpositions and rewards matching prefixes, which suits transliterated names.
- **Taught in:** `phases/13-risk-fraud-and-compliance/04-sanctions-screening`
- **Related terms:** Fuzzy match, False-positive rate

### Jitter
- **Category:** Distributed systems
- **What it actually means:** A named random distribution over the backoff interval (full: `uniform[0, exp]`; equal; decorrelated: `min(cap, uniform[base, 3 * previous])`) so clients denied at the same instant do not retry at the same instant.
- **Why it matters:** It is the client half of retry-storm prevention; the server half is Retry-After and a retry budget.
- **Common confusion:** "Add some randomness" is done with a few milliseconds; the spread must be on the order of the backoff itself to desynchronise a wave.
- **Taught in:** `phases/05-distributed-foundations/10-retries-backoff-and-jitter`, `phases/03-systems-data-structures/11-rate-limiter-algorithms`, `phases/06-replication-and-consensus/05-raft-leader-election`
- **Related terms:** Exponential backoff, Retry storm, Randomized election timeout, Rate limiter

### Journal entry
- **Category:** Money & ledgers
- **What it actually means:** Two or more postings with an id and a description, valid only when debits equal credits in every currency it touches.
- **Why it matters:** The entry is the unit of atomicity in a ledger: either all its postings are in the journal or none are, which is what keeps the trial balance balanced.
- **Common confusion:** "A transaction" in the database sense is the mechanism that makes the entry atomic; the entry is the accounting object.
- **Taught in:** `phases/10-money-and-ledgers/03-double-entry-ledger`
- **Related terms:** Posting, Debit / credit, Reversal, Idempotent post

## L

### Lamport clock
- **Category:** Distributed systems
- **What it actually means:** A per-process counter that ticks on local events, travels on sends, and becomes `max(local, remote) + 1` on receive; it guarantees `a -> b` implies `C(a) < C(b)` and not the converse.
- **Why it matters:** It gives a cluster of ledger nodes an ordering consistent with causality without trusting any wall clock, and `(counter, nodeId)` turns it into a total order.
- **Common confusion:** "A logical timestamp" is read as a time; two events with Lamport stamps 5 and 9 may be concurrent, and the one stamped 9 may have happened first by the wall.
- **Taught in:** `phases/05-distributed-foundations/03-lamport-and-vector-clocks`
- **Related terms:** Happens-before, Vector clock, Sequential consistency

### Largest-remainder method
- **Category:** Money & ledgers
- **What it actually means:** Allocating an amount into parts by flooring every share, then giving each leftover minor unit to the shares with the largest fractional remainder; the parts sum to the whole by construction.
- **Why it matters:** Splitting a $10.00 fee three ways must produce three integer amounts that add to exactly 1000 cents, and rounding each share independently does not.
- **Common confusion:** "Spread the pennies" is sometimes done by dumping the remainder on the last share; largest remainder is the rule that makes the allocation fair and deterministic.
- **Taught in:** `phases/10-money-and-ledgers/01-representing-money`
- **Related terms:** Minor unit, Banker's rounding, Fixed-point

### Late presentment
- **Category:** Payment rails
- **What it actually means:** A clearing record presented after the authorization's validity window; the consequence is downgraded interchange or a chargeback right for the issuer.
- **Why it matters:** It is the business reason a payment state machine tracks authorization expiry and why "capture eventually" is not an acceptable retry policy.
- **Common confusion:** "The capture was late" is thought to be rejected outright; it is usually accepted and penalised, which is worse because the loss is invisible until reconciliation.
- **Taught in:** `phases/11-payment-rails/02-card-authorization-lifecycle`
- **Related terms:** Capture, Authorization, Interchange, Authorization hold

### Lease
- **Category:** Payment systems
- **What it actually means:** How long an `IN_PROGRESS` idempotency record protects its owner from being superseded; after it expires a retry may reclaim the key and run recovery instead of receiving `409`.
- **Why it matters:** Set too short, two executions overlap; set shorter than the longest honest processor call, a healthy request is reclaimed mid-flight.
- **Common confusion:** "The lock timeout" suggests the lease is a lock; it is a trust window, and the reclaiming caller must still ask the processor what happened.
- **Taught in:** `phases/05-distributed-foundations/09-idempotency-keys`, `phases/12-payment-systems-design/01-idempotent-payment-api`
- **Related terms:** Idempotency key, Unknown outcome, Fencing token, TTL

### Ledger balance
- **Category:** Money & ledgers
- **What it actually means:** The sum of settled postings to an account; the number the trial balance proves and the statement shows.
- **Why it matters:** It is the accounting truth, but it is not the number an authorization may be decided against, because it ignores open holds.
- **Common confusion:** "The balance" means this to an accountant and the available balance to a cardholder, and systems that confuse them double-promise funds.
- **Taught in:** `phases/10-money-and-ledgers/07-holds-and-available-balance`
- **Related terms:** Available balance, Authorization hold, Trial balance, Posting

### Linearizability
- **Category:** Distributed systems
- **What it actually means:** There exists a single sequential order of all operations that the specification accepts and that respects real time: if `a` returned before `b` was invoked, `a` comes first; the system behaves as one copy.
- **Why it matters:** It is the consistency a balance check needs, and it is checkable from a client-side history, which is how the course tests its replicated ledger.
- **Common confusion:** "Strong consistency" is also used for sequential consistency, which drops the real-time requirement and permits a lagging replica.
- **Taught in:** `phases/05-distributed-foundations/05-consistency-models`, `phases/06-replication-and-consensus/12-linearizability-checking`
- **Related terms:** Sequential consistency, Eventual consistency, Invariant

### Little's law
- **Category:** Reliability & operations
- **What it actually means:** In-flight work equals arrival rate times residence time, in a stable system.
- **Why it matters:** At 2,500 authorizations per second and a processor p99 of one second, the gateway holds 2,500 requests open; pools sized for the 0.35-second average saturate exactly when the processor slows down.
- **Common confusion:** "Concurrency" is sized from the mean; the law is applied to the slow case, because that is when the pool is the bottleneck.
- **Taught in:** `phases/14-payments-at-scale/01-high-availability-payment-gateway`
- **Related terms:** Bulkhead, Tail latency, Backpressure, Cascading failure

### Livelock
- **Category:** Concurrency & runtime
- **What it actually means:** Threads (or Paxos proposers) that repeatedly retry and fail without blocking, so the system is busy and makes no progress.
- **Why it matters:** Try-lock trades deadlock for livelock, and duelling proposers livelock Paxos; randomised backoff breaks both.
- **Common confusion:** "Busy but stuck" is diagnosed as a deadlock; a deadlock shows idle threads, a livelock shows full CPUs and no throughput.
- **Taught in:** `phases/04-concurrency/07-deadlocks-and-lock-ordering`, `phases/06-replication-and-consensus/04-paxos`
- **Related terms:** Deadlock, Ballot, Jitter

### Load factor
- **Category:** Data structures & algorithms
- **What it actually means:** `size / capacity` for a hash table; for a probing table the meaningful figure is `(size + tombstones) / capacity`, because a probe walks through tombstones.
- **Why it matters:** It is the single number that predicts probe length, and a table with many deletions can be slow at a low nominal load because of tombstones.
- **Common confusion:** "How full the table is" ignores deleted markers; a table that is 30% live and 50% tombstones probes like one that is 80% full.
- **Taught in:** `phases/01-complexity-and-core-structures/06-hash-maps-from-scratch`
- **Related terms:** Open addressing, Hash ring, Big-O

### Load shedding
- **Category:** Concurrency & runtime
- **What it actually means:** Refusing work at admission (`offer` returning false, an HTTP 429) so the system stays within capacity, delegating the retry to the caller.
- **Why it matters:** A payment gateway that sheds new authorizations under overload stays alive to finish the captures already in flight.
- **Common confusion:** "Return 429" is treated as an error path; it is a designed outcome with a Retry-After, and which requests to shed first is the degradation ladder.
- **Taught in:** `phases/04-concurrency/05-producers-consumers-and-bounded-queues`, `phases/14-payments-at-scale/11-payments-system-design-interviews`
- **Related terms:** Backpressure, Degradation ladder, Rate limiter

### Lock ordering
- **Category:** Concurrency & runtime
- **What it actually means:** A total order on locks that every thread respects when acquiring more than one; it makes circular wait impossible by a three-line proof.
- **Why it matters:** A transfer between two accounts locks both; locking them by ascending account id is the whole deadlock prevention strategy.
- **Common confusion:** "Always lock A before B" is agreed informally; it must be a rule on all locks (by id, by address) that code can follow without knowing the business meaning.
- **Taught in:** `phases/04-concurrency/07-deadlocks-and-lock-ordering`
- **Related terms:** Deadlock, Livelock, Mutex

### Lock striping
- **Category:** Concurrency & runtime
- **What it actually means:** A fixed array of N locks where key `k` is guarded by `locks[hash(k) mod N]`; contention is divided by N for keys that hash apart, with no per-key lock allocation.
- **Why it matters:** Per-account striping lets thousands of accounts update concurrently while any single account stays serialised.
- **Common confusion:** "Sharded locks" are assumed to allow a consistent total; summing stripe by stripe is not a snapshot read, because other stripes change while you walk.
- **Taught in:** `phases/04-concurrency/02-locks-and-atomic-counters`
- **Related terms:** Contention, Mutex, Hot account, Hash ring

### Log Matching
- **Category:** Distributed systems
- **What it actually means:** The Raft property that if two logs share an entry's index and term, they are identical up to and including that index; logs may still diverge above their last common entry.
- **Why it matters:** It is what lets a follower accept a batch of entries after checking only the predecessor, and it is established by induction from the consistency check on every AppendEntries.
- **Common confusion:** "The logs agree" is read as full agreement; the property is a prefix guarantee, and the tail can be overwritten until committed.
- **Taught in:** `phases/06-replication-and-consensus/06-raft-log-replication`
- **Related terms:** Replication log, Randomized election timeout

### Longest-prefix match
- **Category:** Data structures & algorithms
- **What it actually means:** Among all stored keys that are prefixes of the query, the longest one; computed in a single downward walk of a trie.
- **Why it matters:** BIN lookup is longest-prefix match: a more specific product range overrides the network's broad range.
- **Common confusion:** "Most specific rule wins" is often implemented as a sorted linear scan; the trie makes it O(key length) regardless of table size.
- **Taught in:** `phases/01-complexity-and-core-structures/13-tries`
- **Related terms:** Trie, BIN / IIN, PAN

### Lost update
- **Category:** Concurrency & runtime
- **What it actually means:** Two read-modify-write sequences interleave so the second store overwrites the first; the loss is exactly one operation's delta, and the resulting value is still a legal number.
- **Why it matters:** A lost update on a balance is money that silently disappears, and nothing in the stored value reveals it.
- **Common confusion:** "The counter is off" is attributed to a bug in the arithmetic; the arithmetic is correct and the interleaving is the bug.
- **Taught in:** `phases/04-concurrency/02-locks-and-atomic-counters`
- **Related terms:** Check-then-act, Compare-and-swap, Mutex, Data race

### Luhn check
- **Category:** Payment rails
- **What it actually means:** The ISO/IEC 7812 check-digit scheme on a PAN: double every second digit from the right, sum the digits, and require a total divisible by ten; it detects every single-digit error and every adjacent transposition except 09/90.
- **Why it matters:** It catches keying errors before a card number reaches the network, and it is the first filter a card-testing attacker's generated numbers must pass.
- **Common confusion:** "Mod 10" is sometimes treated as a security control; it proves nothing about whether the card exists, and anyone can generate Luhn-valid numbers.
- **Taught in:** `phases/11-payment-rails/04-pan-luhn-and-bin-ranges`, `phases/02-algorithms/15-numerical-algorithms`
- **Related terms:** PAN, Check digit, Mod-97, Card testing

## M

### Match key
- **Category:** Money & ledgers
- **What it actually means:** The field present on both sides of a reconciliation that identifies one event; its uniqueness determines how much of the first matching pass can be trusted.
- **Why it matters:** An end-to-end reference carried from the payment through the bank statement is the single most valuable field a payment system persists.
- **Common confusion:** "The id we join on" is assumed to exist; when it does not, the fallback is a fuzzy match on amount and date that must be labelled as such.
- **Taught in:** `phases/10-money-and-ledgers/08-reconciliation`
- **Related terms:** Reconciliation, Fuzzy match, ODFI / RDFI, STAN / RRN

### Merchant discount rate
- **Category:** Payment rails
- **What it actually means:** The all-in percentage plus fixed fee the acquirer charges the merchant: interchange, plus scheme fees, plus the acquirer's margin.
- **Why it matters:** It is the number on the merchant's statement and the one a ledger must split into its components to reconcile against the network's settlement report.
- **Common confusion:** "The processing fee" is often equated with interchange; interchange goes to the issuer and is only the largest part.
- **Taught in:** `phases/11-payment-rails/01-how-a-card-payment-works`
- **Related terms:** Interchange, Settlement, Capture

### Minor unit
- **Category:** Money & ledgers
- **What it actually means:** The smallest indivisible unit of a currency as defined by ISO 4217's exponent: 0, 2, or 3 decimal places depending on the currency.
- **Why it matters:** Money in this course is an integer count of minor units, which is exact, overflow-checked, and identical across Java, Python, and Go.
- **Common confusion:** "Cents" assumes two decimal places; Japanese yen has none and Kuwaiti dinar has three.
- **Taught in:** `phases/10-money-and-ledgers/01-representing-money`
- **Related terms:** Fixed-point, Overflow, Largest-remainder method

### Mod-97
- **Category:** Payment rails
- **What it actually means:** ISO/IEC 7064 MOD 97-10, the IBAN checksum: rearrange, convert letters to two digits each, treat the whole string as an integer, and require a remainder of 1 on division by 97; it detects all single errors and all adjacent transpositions.
- **Why it matters:** It is stronger than Luhn and is computed digit by digit with Horner's rule so the 30-digit number is never formed.
- **Common confusion:** "The IBAN checksum" is sometimes computed with floating point or a big-integer library; modular reduction at each digit needs neither.
- **Taught in:** `phases/11-payment-rails/11-iban-bic-and-account-validation`, `phases/02-algorithms/15-numerical-algorithms`
- **Related terms:** IBAN, Check digit, Luhn check

### MTI
- **Category:** Payment rails
- **What it actually means:** The ISO 8583 message type indicator: four digits for version, class, function, and origin; `0200` is a 1987 financial request from an acquirer, and the response is the same with the third digit plus one.
- **Why it matters:** The MTI tells the parser which fields to expect and the matcher which message a response or reversal belongs to.
- **Common confusion:** "The message type" is read as the transaction type; the transaction type (purchase, refund) is in the processing code, data element 3.
- **Taught in:** `phases/11-payment-rails/03-iso-8583`
- **Related terms:** STAN / RRN, Authorization

### Multilateral netting
- **Category:** Payment rails
- **What it actually means:** Replacing each participant's many bilateral obligations with one net position against the system; positions sum to zero and the liquidity needed falls to the sum of net debits.
- **Why it matters:** It is what a card scheme's daily settlement computes, and why Visa and Mastercard are netting engines rather than banks.
- **Common confusion:** "Netting out" is imagined pairwise; multilateral netting nets each participant against everyone at once, which is what collapses liquidity need.
- **Taught in:** `phases/11-payment-rails/15-settlement-and-clearing`
- **Related terms:** Clearing, Deferred net settlement, Settlement, Real-time gross settlement

### Mutex
- **Category:** Concurrency & runtime
- **What it actually means:** A primitive that admits one thread at a time to a region; its release happens-before the next acquisition, which provides both atomicity and visibility for the region.
- **Why it matters:** It is the simplest correct way to make a check-then-act on a balance atomic, and its reentrancy (or lack of it) differs by language.
- **Common confusion:** "A lock" is thought to be slow; an uncontended mutex costs tens of nanoseconds, and contention, not the lock, is what costs.
- **Taught in:** `phases/04-concurrency/02-locks-and-atomic-counters`, `phases/04-concurrency/07-deadlocks-and-lock-ordering`
- **Related terms:** Happens-before, Contention, Lock striping, Condition variable
## N

### Negative cycle
- **Category:** Data structures & algorithms
- **What it actually means:** A directed cycle whose edge weights sum below zero; shortest paths through it are undefined, and in an FX graph weighted by `-ln(rate)` it is an arbitrage loop whose rates multiply to more than one.
- **Why it matters:** Dijkstra silently gives wrong answers when any edge is negative, so FX routing uses Bellman-Ford, and finding a negative cycle is finding free money or a bad rate feed.
- **Common confusion:** "An infinite loop" is a runtime symptom; the cycle is a property of the graph, and Bellman-Ford detects it by relaxing one round too many.
- **Taught in:** `phases/02-algorithms/09-shortest-paths`
- **Related terms:** Heap, Priority queue, Trie

### Normal balance
- **Category:** Money & ledgers
- **What it actually means:** The side on which an account's increases are recorded: debit for assets and expenses, credit for liabilities, equity, and revenue, derived from the accounting equation.
- **Why it matters:** It is how a signed balance is read back into "how much" and "which way", and a customer wallet is a credit-normal liability, not a debit-normal asset.
- **Common confusion:** "Which way is positive" is taken to mean credits are negative; both sides are positive postings, and the normal balance says which side the account grows on.
- **Taught in:** `phases/10-money-and-ledgers/03-double-entry-ledger`, `phases/10-money-and-ledgers/04-chart-of-accounts`
- **Related terms:** Debit / credit, Chart of accounts, Posting, Trial balance

## O

### ODFI / RDFI
- **Category:** Payment rails
- **What it actually means:** The Originating and Receiving Depository Financial Institutions in ACH: the bank that accepts your NACHA file and the bank that posts the entry; the entry's SEC code names the authorization rules and its trace number (ODFI routing prefix plus a sequence) is the only identifier a return carries back.
- **Why it matters:** An originator's obligations under the NACHA rules flow through the ODFI, and a return can only be matched to the original if the trace number was persisted on send.
- **Common confusion:** "Our bank / their bank" hides that both are rule-bound participants with their own return rights and deadlines.
- **Taught in:** `phases/11-payment-rails/08-ach-and-nacha`
- **Related terms:** Return window, ABA routing transit number, Deferred net settlement, Match key

### Open addressing
- **Category:** Data structures & algorithms
- **What it actually means:** A hash table where colliding keys occupy other slots of the same array according to a probe sequence (linear probing is `home, home+1, ...`); the alternative, separate chaining, hangs a linked list off each slot.
- **Why it matters:** Probing is cache-friendly because every probe is a neighbouring slot, which is why an in-memory idempotency store built this way beats a chained map until the load factor climbs.
- **Common confusion:** "Everything in one array" sounds simpler; deletion needs tombstones, and lookups need free slots to terminate, so the table must resize well before it is full.
- **Taught in:** `phases/01-complexity-and-core-structures/06-hash-maps-from-scratch`
- **Related terms:** Load factor, Hash ring, Big-O

### Optimistic concurrency
- **Category:** Payment systems
- **What it actually means:** Letting concurrent handlers proceed without locks and rejecting the second writer when the version it expects is not the version the store holds; in SQL, `UPDATE ... WHERE version = expected`, in an event store, an expected stream length.
- **Why it matters:** Two gateway nodes applying the same webhook race on one payment, and the version check turns the loser into a no-op instead of a double transition.
- **Common confusion:** "Version check" done in application code after a read is not optimistic concurrency; the check must be in the same atomic statement as the write.
- **Taught in:** `phases/12-payment-systems-design/02-payment-state-machine`, `phases/08-messaging-and-streams/05-event-sourcing`, `phases/14-payments-at-scale/01-high-availability-payment-gateway`
- **Related terms:** Compare-and-swap, Transition, Aggregate, Stateless edge

### Orchestration
- **Category:** Storage & transactions
- **What it actually means:** A saga style in which one component holds the step list and the saga log (the append-only record of `*_STARTED` and `*_COMPLETED` entries around every action) and calls each participant; the alternative, choreography, has participants react to each other's events.
- **Why it matters:** The saga log is the only input recovery needs, and orchestration keeps it in one place, which is why it is the style this course builds for money movement.
- **Common confusion:** "The workflow engine" is thought of as a product; the mechanism is a durable log of step boundaries plus a loop that resumes from it.
- **Taught in:** `phases/07-storage-and-transactions/12-saga-pattern`
- **Related terms:** Saga, Compensation, Write-ahead, Transactional outbox

### Overflow
- **Category:** Money & ledgers
- **What it actually means:** A fixed-width integer exceeding its range; Java `long` and Go `int64` wrap silently in two's complement past 2^63-1 unless checked arithmetic (`Math.addExact`, a hand-written check) is used, and Python integers do not wrap at all.
- **Why it matters:** A balance in minor units can flip sign with no error, so every `Money` operation in the course is overflow-checked.
- **Common confusion:** "The number got too big" is expected to throw; both Java and Go specify wrapping, and the exception only comes from the checked variants.
- **Taught in:** `phases/10-money-and-ledgers/01-representing-money`, `phases/00-setup-and-tooling/07-decimal-and-integer-arithmetic`
- **Related terms:** Minor unit, Fixed-point, Banker's rounding

## P

### PAN
- **Category:** Payment rails
- **What it actually means:** The primary account number: 12 to 19 digits per ISO/IEC 7812, consisting of the major industry identifier, the issuer identification number, the account identifier, and a Luhn check digit; an identifier, never an arithmetic value.
- **Why it matters:** It is the datum PCI DSS exists to protect: stored only if rendered unreadable, displayed masked to BIN and last four, and never logged in full.
- **Common confusion:** "The card number" is handled as a number; leading zeros, lengths that vary by network, and the prohibition on storing it make it a string under strict custody.
- **Taught in:** `phases/11-payment-rails/04-pan-luhn-and-bin-ranges`, `phases/13-risk-fraud-and-compliance/08-pci-dss-for-engineers`, `phases/01-complexity-and-core-structures/13-tries`
- **Related terms:** BIN / IIN, Luhn check, Tokenization, CDE

### Partition
- **Category:** Messaging & streams
- **What it actually means:** One independent append-only log within a topic, with its own dense zero-based offsets and its own total order; the unit of parallelism and of ordering, and a partitioner maps each key to one of them.
- **Why it matters:** Ordering is guaranteed only within a partition, so all events for one payment must share a key, and the committed offset is what a consumer resumes from.
- **Common confusion:** "A shard of the topic" suggests offsets are global; an offset is meaningless outside its partition.
- **Taught in:** `phases/08-messaging-and-streams/01-the-append-only-log`
- **Related terms:** Hash ring, Relay, Event sourcing, Split brain

### PCI DSS
- **Category:** Risk & compliance
- **What it actually means:** The card brands' contractual security standard, enforced through acquirers; v4.0 has twelve requirements across six goals, is assessed annually, and forbids storing sensitive authentication data (full track, CVV2, PIN) after authorization under any encryption.
- **Why it matters:** It defines what an engineer may store, display, and log about a card, and the scope of the audit is the cardholder data environment the engineer designs.
- **Common confusion:** "PCI compliance" is treated as a legal requirement; it is a contract term, and the self-assessment type is dictated by how the PAN reaches the provider.
- **Taught in:** `phases/13-risk-fraud-and-compliance/08-pci-dss-for-engineers`
- **Related terms:** CDE, PAN, Tokenization

### PKCE
- **Category:** Payment rails
- **What it actually means:** RFC 7636's Proof Key for Code Exchange: the client keeps a random verifier, sends its SHA-256 on the authorize request, and presents the verifier at the token endpoint to prove it is the same client that started the flow.
- **Why it matters:** It defeats authorization-code interception, which is the attack on the one credential that passes through the user's browser in an open-banking consent flow.
- **Common confusion:** "Pixie" is thought to be for mobile apps only; it is now required for every client in OAuth 2.1 and in the open-banking profiles.
- **Taught in:** `phases/11-payment-rails/13-open-banking-and-account-aggregation`
- **Related terms:** Authorization code, Access token, Refresh token, Consent

### Posting
- **Category:** Money & ledgers
- **What it actually means:** One line of a journal entry: one account, one side (debit or credit), one positive amount of one currency; never signed, never zero.
- **Why it matters:** Postings are the atoms a ledger balance is summed from, and their unsigned form is what makes "debits equal credits" a checkable equation.
- **Common confusion:** "A line item" is often stored as a signed amount; a negative posting hides which side it is on and breaks the trial balance identity.
- **Taught in:** `phases/10-money-and-ledgers/03-double-entry-ledger`
- **Related terms:** Journal entry, Debit / credit, Normal balance, Append-only journal

### Prepare
- **Category:** Storage & transactions
- **What it actually means:** Phase one of two-phase commit: a participant checks every constraint it will ever check, takes its locks, writes a durable PREPARED record, and only then votes YES; after voting it has given up the right to abort on its own. The coordinator's durable DECISION record is the commit point, and a BEGIN with no decision is presumed to mean ABORT.
- **Why it matters:** A participant that votes YES without a durable record cannot honour its vote after a crash, which breaks atomic commitment for the whole transaction.
- **Common confusion:** "Phase one" is imagined as a readiness poll; it is a binding, logged promise that costs the participant its autonomy until the decision arrives.
- **Taught in:** `phases/07-storage-and-transactions/08-two-phase-commit`
- **Related terms:** Atomic commitment, In doubt, Blocking problem, Write-ahead

### Priority queue
- **Category:** Data structures & algorithms
- **What it actually means:** The abstract interface insert, find-min, delete-min, and (with an index map) decrease-key; a binary heap is one implementation, a sorted set or a timing wheel are others, and a sequence number added to the key makes ties FIFO.
- **Why it matters:** A retry scheduler is a priority queue keyed by next-attempt time, and a discrete-event simulation is a priority queue of timestamped events.
- **Common confusion:** "A queue with priorities" implies stable ordering; a bare heap does not preserve insertion order among equal priorities.
- **Taught in:** `phases/01-complexity-and-core-structures/09-heaps-and-priority-queues`, `phases/02-algorithms/09-shortest-paths`
- **Related terms:** Heap, Negative cycle, Deterministic simulation, Retry storm

### Property-based test
- **Category:** Reliability & operations
- **What it actually means:** Random generation of structured inputs from a generator, an invariant check on every run, and shrinking (greedy descent through smaller failing candidates) to a minimal counterexample.
- **Why it matters:** "No sequence of captures, voids, and duplicate webhooks ever leaves the ledger unbalanced" is a property, and a property test explores sequences no example-based test would list.
- **Common confusion:** "Fuzzing" usually means unstructured bytes and crash detection; a property test generates valid commands and checks a semantic oracle.
- **Taught in:** `phases/12-payment-systems-design/14-testing-payment-systems`
- **Related terms:** Invariant, Deterministic simulation, Fake, Trial balance

## Q

### Quorum
- **Category:** Distributed systems
- **What it actually means:** A majority, `floor(N/2) + 1` of the configured cluster (not of the reachable nodes), whose defining property is that any two quorums share at least one node; in leaderless replication, read and write set sizes with `R + W > N` so every read overlaps every write.
- **Why it matters:** The overlap is what makes Paxos safe, Raft's Election Safety hold, and a leaderless ledger read see the latest write.
- **Common confusion:** "Most of the nodes" is computed from the nodes that answered; a quorum is defined against the configured membership, or a partition can produce two of them.
- **Taught in:** `phases/06-replication-and-consensus/04-paxos`, `phases/06-replication-and-consensus/05-raft-leader-election`, `phases/06-replication-and-consensus/01-replication-strategies`
- **Related terms:** Ballot, Randomized election timeout, Split brain, Synchronous replica

## R

### Race condition
- **Category:** Concurrency & runtime
- **What it actually means:** An outcome that depends on the interleaving of correctly synchronised operations; fixed by widening the critical section or using a transaction, not by `volatile`.
- **Why it matters:** Two atomic reads followed by one atomic write is still a race on a balance, and the fix is in the design, not the primitives.
- **Common confusion:** "A timing bug" is used interchangeably with data race; a data race is undefined behaviour under the memory model, a race condition is a logic error in a data-race-free program.
- **Taught in:** `phases/04-concurrency/01-threads-and-the-memory-model`
- **Related terms:** Data race, Check-then-act, Lost update, Check-and-reserve

### Randomized election timeout
- **Category:** Distributed systems
- **What it actually means:** In Raft, a per-node deadline drawn uniformly from an interval and redrawn on every reset, so that candidates whose votes split in one term desynchronise in the next; Election Safety (at most one leader per term) follows from majority voting.
- **Why it matters:** It is the entire liveness argument for Raft leader election, and the same jitter idea breaks retry storms and Paxos duels.
- **Common confusion:** "Jittered heartbeat timeout" suggests the leader randomises; it is the followers' timeouts that are random, and the heartbeat interval is fixed and shorter.
- **Taught in:** `phases/06-replication-and-consensus/05-raft-leader-election`
- **Related terms:** Quorum, Jitter, Split brain, Livelock

### Rate limiter
- **Category:** Data structures & algorithms
- **What it actually means:** A per-client admission decision with two outputs: admit now, or deny with the earliest time a retry can succeed (RFC 9110's Retry-After, rounded up, never zero). A token bucket admits bursts up to `capacity` and refills at a steady rate; a leaky-bucket shaper admits into a queue drained at a constant rate; a sliding window counts requests in `[now - W, now)`.
- **Why it matters:** It is the first defence against card testing and retry storms, and its one-second ceiling for a token bucket is `capacity + rate`, which design reviews routinely get wrong.
- **Common confusion:** "Throttling" is imagined as slowing clients down; most limiters refuse, and only the shaper delays, at a queueing cost a payment authorization cannot afford.
- **Taught in:** `phases/03-systems-data-structures/11-rate-limiter-algorithms`
- **Related terms:** Sliding window, Retry storm, Load shedding, Velocity

### Real-time gross settlement
- **Category:** Payment rails
- **What it actually means:** Each payment settled individually and immediately in central bank money; no interbank credit exposure, high liquidity need, and central bank intraday credit to avoid gridlock.
- **Why it matters:** Fedwire, TARGET2, and CHAPS are RTGS systems, and a wire's finality and cost both follow from this design.
- **Common confusion:** "Wire" is used for any bank transfer; an ACH credit is deferred net settlement and can be returned, an RTGS payment is final on settlement.
- **Taught in:** `phases/11-payment-rails/15-settlement-and-clearing`
- **Related terms:** Deferred net settlement, Settlement finality, Settlement, Herstatt risk

### Reconciliation
- **Category:** Money & ledgers
- **What it actually means:** Pairing every record in one system with its counterpart in an independent system and classifying what will not pair; the tie-out identity `ledger total - statement total = ours unmatched - theirs unmatched` holds by construction when every record is in exactly one match or break.
- **Why it matters:** It is the only control that checks a ledger against the outside world, the only correct resolution of a processor timeout, and the correctness SLI for money movement.
- **Common confusion:** "Checking the numbers match" is done on totals; equal totals with offsetting breaks is the case reconciliation exists to catch.
- **Taught in:** `phases/10-money-and-ledgers/08-reconciliation`, `phases/12-payment-systems-design/14-testing-payment-systems`, `phases/14-payments-at-scale/11-payments-system-design-interviews`
- **Related terms:** Break, Match key, Three-way match, Adjusting entry

### Refresh token
- **Category:** Payment rails
- **What it actually means:** A long-lived credential presented only to the token endpoint to obtain a new access token; rotated on every use, and a replay of an old one revokes its whole family.
- **Why it matters:** An aggregator keeps a connection alive for months on refresh tokens, and rotation with family revocation is what limits the damage when one leaks.
- **Common confusion:** "The long-lived token" is sometimes sent to resource servers; it has no meaning there, and exposing it widens the blast radius.
- **Taught in:** `phases/11-payment-rails/13-open-banking-and-account-aggregation`
- **Related terms:** Access token, Authorization code, Consent, PKCE

### Refund
- **Category:** Payment rails
- **What it actually means:** A new credit transaction referencing the original, bounded by the captured amount, never by the authorized amount; after capture it is the only way to return funds, where before capture a void releases the hold.
- **Why it matters:** The refund limit is a state-machine guard, and a refund is a new clearing record with its own settlement, not an edit to the original.
- **Common confusion:** "Giving the money back" is modelled as reversing the capture; the capture has settled, so the refund is a second movement in the other direction.
- **Taught in:** `phases/11-payment-rails/02-card-authorization-lifecycle`
- **Related terms:** Capture, Void, Reversal, Transition

### Relay
- **Category:** Messaging & streams
- **What it actually means:** The process that reads unpublished outbox rows in commit order, publishes each to the broker, then marks it published; publish-before-mark is what gives at-least-once.
- **Why it matters:** It is the component that turns a database row into an event without a dual write, and its crash window is exactly the duplicate a consumer must dedupe.
- **Common confusion:** "The publisher job" is sometimes written mark-before-publish; that gives at-most-once, which loses events.
- **Taught in:** `phases/07-storage-and-transactions/13-transactional-outbox`
- **Related terms:** Transactional outbox, At-least-once delivery, Change data capture, Partition

### Replay
- **Category:** Payment systems
- **What it actually means:** Returning the stored status and body of a completed idempotent request unchanged, without executing, marked with a header (`Idempotent-Replayed: true`) so the client can tell; it applies to writes only, never to reads.
- **Why it matters:** The client's retry after a timeout gets the same `201` and the same payment id, which is how one intent produces one charge.
- **Common confusion:** "Cached response" suggests a cache that may be evicted; the replay record must survive for the whole TTL or the retry executes again.
- **Taught in:** `phases/05-distributed-foundations/09-idempotency-keys`, `phases/12-payment-systems-design/01-idempotent-payment-api`
- **Related terms:** Idempotency key, Fingerprint, TTL, Lease

### Replication log
- **Category:** Distributed systems
- **What it actually means:** The ordered sequence of changes with positions (LSN, GTID, oplog timestamp) that followers of a leader consume; followers hold a prefix of the leader's history, and the position on each node is what a failover compares.
- **Why it matters:** Choosing the most advanced follower to promote means comparing log positions, and any acknowledged write beyond the chosen follower's position is lost.
- **Common confusion:** "The binlog" is treated as an implementation detail; it is the contract between leader and follower, and its position is a durability measurement.
- **Taught in:** `phases/06-replication-and-consensus/01-replication-strategies`
- **Related terms:** Synchronous replica, Failover, Write-ahead, Change data capture

### Retry storm
- **Category:** Distributed systems
- **What it actually means:** A population of clients synchronised by a shared failure, retrying in lockstep so each wave exceeds the dependency's capacity and prolongs the outage; a retry budget (first attempts deposit a fraction of a token, retries withdraw one) bounds retry load to a fixed share of organic traffic.
- **Why it matters:** The retries that follow a processor blip can be the thing that keeps it down, and the gateway's own retry policy is a load multiplier it controls.
- **Common confusion:** "Thundering herd" is blamed on client count; the problem is synchrony, which jitter breaks, and volume, which the budget caps.
- **Taught in:** `phases/05-distributed-foundations/10-retries-backoff-and-jitter`, `phases/03-systems-data-structures/11-rate-limiter-algorithms`
- **Related terms:** Exponential backoff, Jitter, Rate limiter, Cascading failure

### Retryable outcome
- **Category:** Distributed systems
- **What it actually means:** A failure whose next attempt might succeed: timeouts, connection resets, 429, 5xx. A 400, 401, or a card decline will fail identically and must not be retried.
- **Why it matters:** Retrying a decline is a scheme-rule violation and a fraud signal; retrying a timeout without an idempotency key is a duplicate charge.
- **Common confusion:** "Transient error" is decided from the status code alone; a timeout is retryable only because the operation is idempotent.
- **Taught in:** `phases/05-distributed-foundations/10-retries-backoff-and-jitter`
- **Related terms:** Ambiguous outcome, Idempotency key, Exponential backoff, Unknown outcome

### Return window
- **Category:** Payment rails
- **What it actually means:** How long an ACH entry can be returned after settlement: two banking days for most reasons, sixty calendar days for an unauthorized consumer debit (R05, R07, R10, R11); an R01 or R09 return may be re-presented at most twice within 180 days.
- **Why it matters:** An ACH debit is not final when it settles, so a ledger must treat the funds as provisional for the window and model the return as a later event.
- **Common confusion:** "How long until it is final" is answered with the two-day figure; the sixty-day unauthorized window is the one that bites a platform.
- **Taught in:** `phases/11-payment-rails/08-ach-and-nacha`
- **Related terms:** ODFI / RDFI, Settlement finality, Deferred net settlement, State machine

### Reversal
- **Category:** Money & ledgers
- **What it actually means:** A new journal entry whose postings are the original's with each side flipped; the original is never modified, and both remain in the journal.
- **Why it matters:** It is how a ledger undoes anything, from a mistaken fee to a saga compensation, while keeping the history an auditor can replay.
- **Common confusion:** "Undo" suggests deletion; a reversal adds an entry and the net effect is zero, which is visible and dated.
- **Taught in:** `phases/10-money-and-ledgers/03-double-entry-ledger`
- **Related terms:** Journal entry, Compensation, Adjusting entry, Append-only journal

## S

### Safeguarding
- **Category:** Risk & compliance
- **What it actually means:** The regulatory requirement that customer funds be held apart from the firm's own money in an identifiable account and reconciled against the customer liability at least daily.
- **Why it matters:** It makes the control-account-equals-sum-of-sub-ledger check a legal obligation, not just an engineering invariant.
- **Common confusion:** "Segregated funds" is understood as a bank account; the obligation is the daily reconciliation between that account and the ledger's customer liability.
- **Taught in:** `phases/10-money-and-ledgers/04-chart-of-accounts`
- **Related terms:** Control account, Chart of accounts, Reconciliation, Trial balance

### Saga
- **Category:** Storage & transactions
- **What it actually means:** A sequence of local transactions, each committed independently, with a compensation per step and a pivot transaction after which the saga completes rather than compensates; it guarantees all-complete or all-compensated, not atomicity or isolation.
- **Why it matters:** Reserve funds, capture at the processor, post to the ledger is a saga, and the lack of isolation is why holds exist as semantic locks.
- **Common confusion:** "A distributed transaction" implies rollback; a saga's intermediate states are visible to everyone and are undone by new forward transactions.
- **Taught in:** `phases/07-storage-and-transactions/12-saga-pattern`
- **Related terms:** Compensation, Orchestration, Atomic commitment, Authorization hold

### SAR
- **Category:** Risk & compliance
- **What it actually means:** A suspicious activity report to the financial intelligence unit, with a narrative describing specific transactions as evidence; due within 30 days of detection in the US.
- **Why it matters:** The monitoring system must attach the exact transactions that satisfied a scenario to the alert, or the investigator cannot file in time.
- **Common confusion:** "Filing on someone" is thought to require certainty; it requires suspicion, and the decision not to file must be documented too.
- **Taught in:** `phases/13-risk-fraud-and-compliance/06-aml-transaction-monitoring`
- **Related terms:** Structuring, CTR threshold, Velocity

### Scan resistance
- **Category:** Data structures & algorithms
- **What it actually means:** A cache policy's ability to keep its working set through one pass over many never-reused keys; plain LRU has none, segmented LRU and TinyLFU (which adds an admission policy comparing a new key's frequency with the victim's) do.
- **Why it matters:** A nightly BIN-table refresh or a batch reconciliation that touches every merchant will flush an LRU cache, and the morning's authorization latency pays for it.
- **Common confusion:** "A batch job killed the cache" is treated as a sizing problem; a bigger LRU is flushed by a bigger scan, only the policy fixes it.
- **Taught in:** `phases/03-systems-data-structures/08-lru-and-lfu-caches`
- **Related terms:** Working set, Eviction, TTL

### Sequential consistency
- **Category:** Distributed systems
- **What it actually means:** A single total order of operations that respects each client's program order but not real time; a replica lagging by one second is sequentially consistent and not linearizable. In a single process, the memory model guarantees it only for data-race-free programs.
- **Why it matters:** A balance read from a replica can be sequentially consistent and still show money that was already spent, which is why authorization reads go to the leader.
- **Common confusion:** "Everyone sees the same order" is taken to be strong consistency; without the real-time constraint a client may read a value another client has already overwritten.
- **Taught in:** `phases/05-distributed-foundations/05-consistency-models`, `phases/06-replication-and-consensus/12-linearizability-checking`, `phases/04-concurrency/01-threads-and-the-memory-model`
- **Related terms:** Linearizability, Eventual consistency, Happens-before

### Settlement
- **Category:** Payment rails
- **What it actually means:** The discharge of an obligation by transfer of funds, typically central bank reserves, between network members through a settlement bank; the moment a claim is extinguished, followed separately by the acquirer funding the merchant.
- **Why it matters:** Nothing before settlement is money, and the ledger's "settled" state, the reconciliation's bank statement, and the correctness SLI all hang on it.
- **Common confusion:** "The payout" is the merchant's view; settlement is between banks, and merchant funding is a further step with its own timing.
- **Taught in:** `phases/11-payment-rails/15-settlement-and-clearing`, `phases/11-payment-rails/01-how-a-card-payment-works`
- **Related terms:** Clearing, Settlement finality, Capture, Multilateral netting

### Settlement finality
- **Category:** Payment rails
- **What it actually means:** The legally defined point after which a settled transfer cannot be unwound, even in the payer's insolvency; the PFMI require a system's rules to state it.
- **Why it matters:** An RTGS payment is final on settlement, an ACH debit is not final for up to sixty days, and a ledger must model the difference as state.
- **Common confusion:** "It's done" is said when funds appear; finality is a legal property of the rail, not a visible balance.
- **Taught in:** `phases/11-payment-rails/15-settlement-and-clearing`
- **Related terms:** Settlement, Real-time gross settlement, Return window, Herstatt risk

### SLI
- **Category:** Reliability & operations
- **What it actually means:** A service level indicator: a ratio of good events to valid events over a window, with "good" and "valid" defined precisely enough to compute from logs without judgement.
- **Why it matters:** For money movement the indicator that matters most is a correctness SLI, the fraction of transactions whose recorded amount, currency, and counterparty match the external record, which reconciliation computes.
- **Common confusion:** "The metric" is any gauge; an SLI is a ratio with an explicit definition of both numerator and denominator.
- **Taught in:** `phases/09-reliability-and-scale/01-slis-slos-and-error-budgets`
- **Related terms:** SLO, Error budget, Histogram, Reconciliation

### Sliding window
- **Category:** Risk & compliance
- **What it actually means:** A window `[now - W, now)` that moves with every query, as opposed to a fixed window that resets on the clock and lets a burst straddle the reset; a bucketed ring of `n` slots approximates it with up to one bucket of undercount at the trailing edge.
- **Why it matters:** Velocity checks, rate limiters, and circuit breakers all count outcomes over one, and the fixed-window boundary problem admits twice the limit around a reset.
- **Common confusion:** "The last ten minutes" is implemented as a counter reset every ten minutes; that is a fixed window, and an attacker can time the reset.
- **Taught in:** `phases/13-risk-fraud-and-compliance/02-rules-engine-and-velocity-checks`, `phases/05-distributed-foundations/11-circuit-breakers-and-bulkheads`, `phases/03-systems-data-structures/11-rate-limiter-algorithms`
- **Related terms:** Velocity, Rate limiter, Circuit breaker, Card testing

### SLO
- **Category:** Reliability & operations
- **What it actually means:** A service level objective: an internal target for an SLI over a window, deliberately tighter than the SLA (the customer contract with consequences) it backs, so the team breaches its own target before any contract is breached.
- **Why it matters:** The SLO defines the error budget, and the error budget policy (usually a launch freeze when exhausted) is what makes reliability a decision rather than an aspiration.
- **Common confusion:** "The SLO" and "the SLA" are used interchangeably; the SLA is looser, external, and has penalties, the SLO is internal and actionable.
- **Taught in:** `phases/09-reliability-and-scale/01-slis-slos-and-error-budgets`
- **Related terms:** SLI, Error budget, Burn rate

### Source of truth
- **Category:** Payment systems
- **What it actually means:** The one component whose record of a payment's state is authoritative; every other component's state is derivable from it, and messages leave it only through its transaction log or outbox.
- **Why it matters:** Naming it in the first ten minutes of a design pre-empts most of the hard questions, because the dual-write and reconciliation problems are all about what disagrees with it.
- **Common confusion:** "The database" is assumed; in an event-sourced system it is the stream, and the database is a projection.
- **Taught in:** `phases/14-payments-at-scale/01-high-availability-payment-gateway`, `phases/14-payments-at-scale/11-payments-system-design-interviews`
- **Related terms:** Transactional outbox, Event sourcing, Three-way match, Stateless edge

### Split brain
- **Category:** Distributed systems
- **What it actually means:** Two nodes each believing it is the leader and accepting writes for the same data, because the old leader was partitioned rather than dead and was not fenced; the histories diverge and must be reconciled by hand. The routing analogue is two clients with different membership views computing different shard owners for the same key.
- **Why it matters:** For a ledger it means two conflicting balances with no rule to pick between them; fencing by term or token is the prevention, timeouts are not.
- **Common confusion:** "Two primaries" is expected to be detected by the primaries; neither can tell from the inside, which is why the resource must do the fencing.
- **Taught in:** `phases/05-distributed-foundations/01-the-fallacies-of-distributed-computing`, `phases/06-replication-and-consensus/01-replication-strategies`, `phases/03-systems-data-structures/06-consistent-hashing`
- **Related terms:** Fencing token, Failover, Quorum, Hash ring

### STAN / RRN
- **Category:** Payment rails
- **What it actually means:** Two ISO 8583 identifiers: the system trace audit number (DE11) is a six-digit sequence assigned by the sender and echoed in the response and any reversal, and the retrieval reference number (DE37) is a twelve-character reference assigned by the acquirer that follows the transaction into clearing and disputes.
- **Why it matters:** The STAN is how a response or reversal is matched to its request; the RRN is the match key that survives into reconciliation and chargebacks.
- **Common confusion:** "The trace number" is assumed unique; a STAN wraps at a million and is unique only with the date and terminal, which is why the RRN exists.
- **Taught in:** `phases/11-payment-rails/03-iso-8583`
- **Related terms:** MTI, Match key, Authorization, Reconciliation

### State machine
- **Category:** Payment systems
- **What it actually means:** A closed set of states and events with a partial transition function; every pair not in the table is an error, not a no-op, and a guard predicate may further restrict a rule.
- **Why it matters:** A payment's lifecycle (authorized, captured, voided, refunded, unknown) as an explicit machine makes illegal transitions and duplicate webhooks detectable instead of silently corrupting state.
- **Common confusion:** "The status flow" is drawn as a diagram and implemented as `if` statements; the machine is a table, and the table is what tests enumerate.
- **Taught in:** `phases/12-payment-systems-design/02-payment-state-machine`
- **Related terms:** Transition, Guard, Command vs event, Unknown outcome

### Stateless edge
- **Category:** Payment systems
- **What it actually means:** A gateway node that holds nothing between requests, so it can be killed at any instant and the only effect is that in-flight requests fail and are retried with the same idempotency key.
- **Why it matters:** High availability at the edge is cheap only if every bit of state lives in the source of truth and every retry is idempotent.
- **Common confusion:** "The API layer" is often holding an in-memory idempotency cache; that makes it stateful, and a retry that lands on another node double-charges.
- **Taught in:** `phases/14-payments-at-scale/01-high-availability-payment-gateway`
- **Related terms:** Source of truth, Idempotency key, Optimistic concurrency, Synchronous replica

### Structuring
- **Category:** Risk & compliance
- **What it actually means:** Splitting cash transactions to stay under the currency-transaction reporting threshold; a crime of intent under 31 U.S.C. 5324 regardless of the source of funds, and "smurfing" specifically means using several people to do it.
- **Why it matters:** The structuring scenario is the canonical AML rule: a rolling-window sum of sub-threshold cash deposits per customer, with the exact transactions attached as evidence.
- **Common confusion:** "Smurfing" is used for all structuring; and structuring is assumed to require dirty money, which it does not.
- **Taught in:** `phases/13-risk-fraud-and-compliance/06-aml-transaction-monitoring`
- **Related terms:** CTR threshold, SAR, Sliding window

### Synchronous replica
- **Category:** Distributed systems
- **What it actually means:** A standby that must acknowledge a write durably before the primary acknowledges it to the client; semi-synchronous means at least one (or a quorum) of the followers, asynchronous means none.
- **Why it matters:** With asynchronous replication every acknowledged write the followers lacked is lost on failover, and for a ledger that is a loss to explain to an auditor; semi-synchronous is the floor.
- **Common confusion:** "HA database" is assumed to mean no data loss; it means that only if the replication is synchronous, which is a configuration choice.
- **Taught in:** `phases/06-replication-and-consensus/01-replication-strategies`, `phases/14-payments-at-scale/01-high-availability-payment-gateway`
- **Related terms:** Replication log, Failover, Quorum, fsync

## T

### Tail latency
- **Category:** Distributed systems
- **What it actually means:** The latency of the slowest 1% (p99) or 0.1% of requests; it compounds across sequential calls because the probability that at least one hop is slow grows with the number of hops.
- **Why it matters:** An authorization that fans out to a risk service, a BIN lookup, and a processor sees the worst of all three, and pool sizing by Little's law must use this number.
- **Common confusion:** "p99" is averaged across hosts; percentiles do not average, and only merged histograms give a fleet p99.
- **Taught in:** `phases/05-distributed-foundations/01-the-fallacies-of-distributed-computing`, `phases/09-reliability-and-scale/06-metrics-and-histograms`
- **Related terms:** Histogram, Little's law, Bulkhead, Circuit breaker

### Three-way match
- **Category:** Money & ledgers
- **What it actually means:** Comparing the payment store, the ledger, and the external party's statement, keyed on external reference and then on amount and date, with typed breaks for each pairwise disagreement.
- **Why it matters:** Two-way reconciliation can agree while both sides are wrong in the same way; the third source, the processor or bank, is independent.
- **Common confusion:** "Reconciliation" is assumed to be two-way; in a payment system there are three records of every movement, and they fail differently.
- **Taught in:** `phases/14-payments-at-scale/11-payments-system-design-interviews`
- **Related terms:** Reconciliation, Source of truth, Break, Match key

### Tokenization
- **Category:** Risk & compliance
- **What it actually means:** Replacing the PAN with a surrogate that cannot be reversed without the provider's vault; it removes downstream systems from PCI scope only if the PAN never passed through them.
- **Why it matters:** It is the single most effective way to shrink the cardholder data environment, provided the token is minted before the PAN reaches your servers.
- **Common confusion:** "We don't store cards" is said of systems that receive the PAN and forward it; transmitting is in scope, and the token must be created at the edge or by the browser.
- **Taught in:** `phases/13-risk-fraud-and-compliance/08-pci-dss-for-engineers`
- **Related terms:** PAN, CDE, PCI DSS

### Torn write
- **Category:** Storage & transactions
- **What it actually means:** A multi-sector write that was partially applied at power loss; in an append-only log it can only occur after the last sync, at the tail, which is why recovery can truncate it safely.
- **Why it matters:** A checksum per frame and the longest-valid-prefix rule are what let a ledger's write-ahead log distinguish a torn tail from corruption in the middle.
- **Common confusion:** "Half the record got written" is assumed to be impossible with a journaling filesystem; the filesystem protects its own metadata, not your record boundaries.
- **Taught in:** `phases/07-storage-and-transactions/02-write-ahead-logs`
- **Related terms:** Write-ahead, fsync, Checkpoint

### Transactional outbox
- **Category:** Storage & transactions
- **What it actually means:** A table in the business database that holds pending events, written in the same transaction as the business row and drained by a separate relay, so a message is published if and only if the change committed.
- **Why it matters:** It is the standard cure for the dual write, and the only way a payment's state change and its notification can be made to agree.
- **Common confusion:** "The outbox table" is sometimes written in a second transaction; then it is a dual write with an extra table.
- **Taught in:** `phases/07-storage-and-transactions/13-transactional-outbox`, `phases/14-payments-at-scale/01-high-availability-payment-gateway`, `phases/14-payments-at-scale/03-exactly-once-money-movement`
- **Related terms:** Dual write, Relay, Inbox, Change data capture

### Transition
- **Category:** Payment systems
- **What it actually means:** The persisted record of one state change: version, event id, from-state, to-state, amount, reason, time; the payment's current state is derived from the ordered list of them, and a redelivered event is recognised by its id as a no-op.
- **Why it matters:** The transition log is the audit trail, the replay source, and the deduplication store for webhooks all at once.
- **Common confusion:** "The audit row" is treated as a side effect of updating a status column; here the transitions are the truth and the status is derived.
- **Taught in:** `phases/12-payment-systems-design/02-payment-state-machine`
- **Related terms:** State machine, Guard, Optimistic concurrency, Idempotent consumer

### Trial balance
- **Category:** Money & ledgers
- **What it actually means:** A report listing every account's net in a debit or credit column; the column totals are equal (equivalently, all signed balances sum to zero) if and only if every journal entry balanced.
- **Why it matters:** It is the cheapest ledger invariant to check continuously, and it is necessary but not sufficient: a double-posted entry is still balanced.
- **Common confusion:** "The books balance" is taken as proof of correctness; it proves only that no entry was one-sided, and reconciliation is what proves the amounts are right.
- **Taught in:** `phases/10-money-and-ledgers/03-double-entry-ledger`, `phases/14-payments-at-scale/03-exactly-once-money-movement`
- **Related terms:** Journal entry, Debit / credit, Reconciliation, Invariant

### Trie
- **Category:** Data structures & algorithms
- **What it actually means:** A tree where each edge is one character and each stored key is a root-to-terminal path, so keys sharing a prefix share nodes; a radix tree compresses single-child chains, and range decomposition splits `[lo, hi]` into the fewest prefix blocks that cover it exactly.
- **Why it matters:** BIN lookup is a trie of decomposed ranges with longest-prefix match, and it answers in time proportional to the key length no matter how many ranges exist.
- **Common confusion:** "Prefix tree" is thought to store strings; it stores paths, and a deleted key must be pruned or it leaves nodes that corrupt prefix counts.
- **Taught in:** `phases/01-complexity-and-core-structures/13-tries`
- **Related terms:** Longest-prefix match, BIN / IIN, PAN

### TTL
- **Category:** Payment systems
- **What it actually means:** The bounded retention window after which an idempotency key is forgotten and may be reused with any body; measured from creation, not last access. For a cache entry it is the freshness window after which the entry expires.
- **Why it matters:** After the TTL the same request executes again, so it must exceed the longest honest client retry horizon, and the client must know what it is.
- **Common confusion:** "Key expiry" is set to a cache-like few minutes; a payments idempotency TTL is typically 24 hours or more.
- **Taught in:** `phases/05-distributed-foundations/09-idempotency-keys`, `phases/12-payment-systems-design/01-idempotent-payment-api`, `phases/03-systems-data-structures/08-lru-and-lfu-caches`
- **Related terms:** Idempotency key, Lease, Replay, Eviction

## U

### Unknown outcome
- **Category:** Payment systems
- **What it actually means:** A durable, non-terminal payment state meaning the processor was contacted and the result is not known; resolved by querying the processor by a deterministic reference or by voiding, never by assuming decline.
- **Why it matters:** It is the state-machine form of the ambiguous outcome, and persisting it before the call is what lets a retry discover whether the charge happened instead of redoing it.
- **Common confusion:** "It timed out" is mapped to FAILED; a failed state that was actually a success is a captured payment the customer never receives goods for.
- **Taught in:** `phases/14-payments-at-scale/01-high-availability-payment-gateway`, `phases/14-payments-at-scale/11-payments-system-design-interviews`
- **Related terms:** Ambiguous outcome, State machine, Reconciliation, Lease

## V

### Vector clock
- **Category:** Distributed systems
- **What it actually means:** One counter per process; `V[i]` is how many of process `i`'s events the holder has observed, `a -> b` iff `V(a) < V(b)` componentwise, and the merge is the componentwise maximum, which is commutative, associative, and idempotent.
- **Why it matters:** Unlike a Lamport clock it detects concurrency, so a replicated ledger can tell a stale update from a conflicting one; stored per item as a version vector it survives replica churn.
- **Common confusion:** "An array of counters" is thought to need one slot per machine forever; a version vector keyed by replica id lets the set change.
- **Taught in:** `phases/05-distributed-foundations/03-lamport-and-vector-clocks`
- **Related terms:** Lamport clock, Happens-before, Eventual consistency

### Velocity
- **Category:** Risk & compliance
- **What it actually means:** A count or sum of events per key over a trailing window; the key (card, device, merchant), the window, and the metric are three independent choices, and a rule fires when the value crosses a threshold.
- **Why it matters:** Card testing, account takeover, and structuring are all invisible per transaction and obvious as a velocity.
- **Common confusion:** "How fast the card is being used" is one velocity; the same event feeds several keyed by different attributes, and an absent counter must never fire a rule.
- **Taught in:** `phases/13-risk-fraud-and-compliance/02-rules-engine-and-velocity-checks`
- **Related terms:** Sliding window, Card testing, Rate limiter, Structuring

### Void
- **Category:** Payment rails
- **What it actually means:** Cancellation of an authorization hold before any capture; the full amount returns to available balance and the ledger never moved. After capture the equivalent is a refund, with different rules.
- **Why it matters:** Voiding an unknown-outcome authorization is the safe resolution when the merchant cannot fulfil, and it is free where a refund costs interchange.
- **Common confusion:** "Cancelling the payment" is used for both void and refund; one releases a promise, the other moves money back.
- **Taught in:** `phases/11-payment-rails/02-card-authorization-lifecycle`, `phases/10-money-and-ledgers/07-holds-and-available-balance`
- **Related terms:** Authorization hold, Refund, Capture, Unknown outcome

## W

### Working set
- **Category:** Data structures & algorithms
- **What it actually means:** The set of keys accessed in a recent window (Denning 1968); a cache smaller than the working set thrashes regardless of policy, and the hit rate is a property of the policy and the workload together.
- **Why it matters:** Sizing a BIN or merchant-config cache starts from the working set, and only after that does the eviction policy matter.
- **Common confusion:** "The hot keys" are assumed stable; the working set moves, and a policy with frequency inertia keeps yesterday's.
- **Taught in:** `phases/03-systems-data-structures/08-lru-and-lfu-caches`
- **Related terms:** Eviction, Scan resistance, TTL

### Write-ahead
- **Category:** Storage & transactions
- **What it actually means:** The invariant that no in-memory state changes until the record describing the change is durable; the order is append, fsync, apply, and recovery replays the log from the last checkpoint with sequence numbers making replay idempotent.
- **Why it matters:** It is the mechanism beneath every durable ledger, every replication log, and every event store in the course.
- **Common confusion:** "Log it first" is satisfied by a `write()` call; without fsync the log is in the page cache and the invariant does not hold.
- **Taught in:** `phases/07-storage-and-transactions/02-write-ahead-logs`
- **Related terms:** fsync, Checkpoint, Torn write, Replication log
