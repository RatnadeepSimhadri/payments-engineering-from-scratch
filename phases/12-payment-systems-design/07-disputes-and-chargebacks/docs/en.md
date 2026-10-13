# Disputes and Chargebacks: The Lifecycle and the Evidence

> A state machine that runs for 120 days.

**Type:** Build
**Languages:** Java
**Prerequisites:** Phase 12, Lesson 02 (The Payment State Machine); Phase 12, Lesson 06 (Refunds, Partial Refunds, and Refund Windows); Phase 10, Lesson 07 (Holds and Available Balance); Phase 11, Lesson 06 (3-D Secure and Strong Customer Authentication)
**Time:** ~90 minutes

## Learning Objectives

- Trace a card dispute through its cycles (inquiry, first chargeback, representment, pre-arbitration, arbitration) and name who acts at each step and what money moves
- Encode the dispute lifecycle as a transition table whose guards check evidence completeness and deadlines, and whose deadline transitions are applied by the system when the injected clock passes them
- Map a network reason code to the evidence that can answer it, and refuse a representment that cannot win before it is filed
- Post each money movement as a balanced journal entry, holding re-credited funds until the outcome instead of releasing them to the merchant
- Compute what a dispute costs the merchant at every possible outcome, and explain why winning is still not free

## The Problem

A furniture marketplace sells a 249.00 USD lamp. Five weeks later the cardholder calls their bank: the lamp never arrived. The issuing bank files a chargeback under Visa reason code 13.1, Merchandise/Services Not Received. Visa moves 249.00 USD from the marketplace's acquirer back to the issuer the same day, and the acquirer debits the marketplace's balance for 249.00 plus a 15.00 chargeback fee. The marketplace's operations team receives an email about it, which lands in a shared inbox during a holiday week. The response deadline passes on day 20. The dispute is lost by default. The carrier's signed proof of delivery, which would have won it in one step, was sitting in the shipping system the whole time.

The team builds a dispute queue. On the next dispute they submit the evidence on day 12 and the representment succeeds: Visa re-credits the 249.00 USD to the acquirer. The marketplace's code, written by someone who modelled disputes as "lost" or "won", marks the dispute won and releases the funds to the seller, who is paid out that evening. Twenty-nine days later the issuer files pre-arbitration: the cardholder says the signature on the delivery record is not theirs. The marketplace now needs the 249.00 USD again, and the seller's balance is empty. Two months after that, the case goes to arbitration, and the marketplace loses: 249.00 USD, the 15.00 fee, and a 500.00 USD arbitration fee, on a 249.00 USD lamp.

And one more: on a different order, a support agent refunds a customer who complained, not knowing a chargeback had already been filed for the same purchase. The customer receives the money twice: once from the refund (Lesson 06) and once from the chargeback. Nothing in the system connected the two.

A dispute is not an event, it is a process that runs for months, with deadlines on both sides, money that moves back and forth at specific steps, and evidence that must match the claim. That is exactly what Lesson 02's state machine is for, with two additions: transitions caused by the calendar, and a ledger that holds funds rather than releasing them while the outcome is open. This lesson builds both.

## The Concept

### The actors and the cycles

Five parties take part, and the merchant is the only one who does not get to start anything:

| Party | Role in a dispute |
|-------|-------------------|
| Cardholder | Claims the charge is wrong (fraud, not received, not as described, duplicate) |
| Issuer | The cardholder's bank; files the chargeback, reviews responses, may escalate |
| Network | Visa, Mastercard; moves the funds, sets reason codes and deadlines, rules in arbitration |
| Acquirer / PSP | The merchant's bank or payment service provider; passes the dispute down and the response up |
| Merchant | Accepts or responds with evidence, within deadlines |

A dispute moves through up to five cycles. Network names differ, and the networks have each restructured their processes (Visa Claims Resolution in 2018 split disputes into an *allocation* flow for fraud and authorization claims and a *collaboration* flow for consumer disputes and processing errors; Mastercard's process has a first chargeback, a second presentment, and pre-arbitration and arbitration case filing). This lesson models the shape they share:

| Cycle | Initiated by | Money moves? | Lesson state |
|-------|--------------|--------------|--------------|
| Inquiry (retrieval request) | Issuer asks for information | No | `INQUIRY` |
| First chargeback | Issuer | Yes: funds return to the issuer, merchant debited plus a fee | `CHARGEBACK` |
| Representment (second presentment, dispute response) | Merchant, with evidence | Yes: funds re-credited to the acquirer | `REPRESENTED` |
| Pre-arbitration | Issuer, rejecting the evidence | No (in this model) | `PRE_ARBITRATION`, `PRE_ARB_RESPONDED` |
| Arbitration | Issuer files with the network | Ruling decides; loser pays a filing fee | `ARBITRATION` |

The cardholder typically has 120 days from the transaction processing date to dispute (longer for some reason codes, for example from the expected delivery date of goods), and each later cycle has its own response window, which is why a dispute that uses every cycle runs for four months after it is filed.

### The state machine

```mermaid
stateDiagram-v2
    [*] --> INQUIRY: retrieval request
    [*] --> CHARGEBACK: first chargeback
    INQUIRY --> INQUIRY_RESPONDED: RESPOND_INQUIRY [evidence, in time]
    INQUIRY --> CHARGEBACK: CHARGEBACK_RECEIVED
    INQUIRY_RESPONDED --> CLOSED_NO_CHARGEBACK: INQUIRY_CLOSED
    INQUIRY_RESPONDED --> CHARGEBACK: CHARGEBACK_RECEIVED
    CHARGEBACK --> REPRESENTED: SUBMIT_REPRESENTMENT [evidence, in time]
    CHARGEBACK --> LOST: ACCEPT
    CHARGEBACK --> LOST: DEADLINE_PASSED
    REPRESENTED --> WON: ISSUER_ACCEPTED
    REPRESENTED --> WON: DEADLINE_PASSED
    REPRESENTED --> PRE_ARBITRATION: PRE_ARB_RECEIVED
    PRE_ARBITRATION --> PRE_ARB_RESPONDED: RESPOND_PRE_ARB [evidence, in time]
    PRE_ARBITRATION --> LOST: ACCEPT
    PRE_ARBITRATION --> LOST: DEADLINE_PASSED
    PRE_ARB_RESPONDED --> WON: ISSUER_WITHDREW
    PRE_ARB_RESPONDED --> WON: DEADLINE_PASSED
    PRE_ARB_RESPONDED --> ARBITRATION: ARBITRATION_FILED
    ARBITRATION --> WON: RULING_FOR_MERCHANT
    ARBITRATION --> LOST: RULING_FOR_ISSUER
```

Ten states, three of them terminal (`CLOSED_NO_CHARGEBACK`, `WON`, `LOST`), thirteen event kinds, eighteen rules. The test drives every rule and enumerates the other 112 `(state, event)` pairs, asserting each is refused and leaves the dispute, its history, and the journal untouched. That is Lesson 02's exhaustive negative test, unchanged.

Read the `DEADLINE_PASSED` rules carefully, because they encode who carries the burden at each step. A deadline that passes while the *merchant* owes a response (`CHARGEBACK`, `PRE_ARBITRATION`) is a loss: silence is acceptance. A deadline that passes while the *issuer* owes a response (`REPRESENTED`, `PRE_ARB_RESPONDED`) is a win: the issuer failed to pursue. `ARBITRATION` has no deadline rule because the network rules on its own schedule. `INQUIRY` has a deadline (the merchant should answer a retrieval request) but no automatic outcome: only the issuer can turn an inquiry into a chargeback, so an overdue inquiry is reported, not moved.

### Guards: evidence and time

Three guards:

| Guard | On | Holds when |
|-------|----|------------|
| `ALWAYS` | issuer and network events, `ACCEPT` | always |
| `EVIDENCE_COMPLETE_IN_TIME` | `RESPOND_INQUIRY`, `SUBMIT_REPRESENTMENT`, `RESPOND_PRE_ARB` | today is on or before the deadline, and the evidence bundle satisfies every requirement of the reason code |
| `DEADLINE_ELAPSED` | `DEADLINE_PASSED` | the state has a deadline and today is after it |

The evidence guard is a policy decision worth defending. The network accepts any representment the merchant files; it is the issuer who decides whether the evidence answers the claim. But a representment that cannot win is worse than an acceptance: it takes the dispute into pre-arbitration and arbitration, where the merchant pays a 500.00 USD filing fee to lose a 249.00 USD argument. Refusing to file what cannot win, with a message that names what is missing, is the guard. The deadline guard on `DEADLINE_PASSED` means a hand-made or replayed deadline event cannot end a dispute early; only the clock can.

### Reason codes and evidence

A reason code is the cardholder's claim, as classified by the issuer. It determines which evidence can answer it. Each requirement in this lesson's table is a set of evidence types of which at least one must be present:

| Reason code | Claim | Category | Evidence that answers it |
|-------------|-------|----------|--------------------------|
| Visa 10.4 | Other fraud, card-absent | Fraud | (3-D Secure authentication or prior undisputed transactions) and receipt |
| Visa 11.3 | No authorization | Authorization | Authorization record |
| Visa 12.6.1 | Duplicate processing | Processing error | Explanation showing two distinct transactions |
| Visa 13.1 | Merchandise or services not received | Consumer dispute | (Proof of delivery or signed delivery) and receipt |
| Visa 13.3 | Not as described or defective | Consumer dispute | Product description and (customer communication or refund policy) |
| Visa 13.6 | Credit not processed | Consumer dispute | Refund issued or refund policy |
| Mastercard 4837 | No cardholder authorization | Fraud | 3-D Secure authentication or prior undisputed transactions |
| Mastercard 4834 | Point-of-interaction error | Processing error | Duplicate explanation or receipt |
| Mastercard 4853 | Cardholder dispute | Consumer dispute | (Proof of delivery or product description) and customer communication |

Three things to notice. First, evidence that answers one claim is useless against another: proof of delivery says nothing about whether the cardholder authorized the purchase, and an AVS/CVV match alone does not answer a card-absent fraud claim, because a fraudster with the card details matches both. Second, fraud claims have the strongest single answer: if the issuer itself authenticated the cardholder with 3-D Secure, liability for fraud shifts from the merchant to the issuer, and `EvidenceBundle.liabilityShift` reports it. Third, Visa's Compelling Evidence 3.0 rules (2023) give merchants a specific answer to 10.4 for repeat customers: two prior undisputed transactions, 120 to 365 days old, sharing device or IP data with the disputed one. That is the `PRIOR_UNDISPUTED_TRANSACTIONS` type.

The real rulebooks list dozens of reason codes with more precise conditions (time limits per code, minimum data elements, exclusions). The table here is the structure of the decision, not a substitute for the networks' guides.

### The money: debit, re-credit into a hold, then release or return

Four accounts, from the payment service provider's point of view:

| Account | Type | Means |
|---------|------|-------|
| `merchant_payable` | Liability | What we owe the merchant; debited when they lose money |
| `scheme_settlement` | Asset | Our net position with the network; funds taken back reduce it |
| `dispute_hold` | Liability | Funds the network has re-credited after a representment, held until the outcome |
| `fee_revenue` | Revenue | Chargeback fees we charge the merchant |

| Transition | Debit | Credit |
|------------|-------|--------|
| Enter `CHARGEBACK` | `merchant_payable` amount + 15.00 | `scheme_settlement` amount, `fee_revenue` 15.00 |
| Enter `REPRESENTED` | `scheme_settlement` amount | `dispute_hold` amount |
| `WON` after representment | `dispute_hold` amount | `merchant_payable` amount |
| `LOST` after representment | `dispute_hold` amount | `scheme_settlement` amount |
| `LOST` in `ARBITRATION`, additionally | `merchant_payable` 500.00 | `scheme_settlement` 500.00 |
| Inquiry steps, pre-arbitration, arbitration filing, `LOST` from `CHARGEBACK` | nothing | |

The second row is the fix for the second incident in The Problem. When the network re-credits the acquirer after a representment, the money is not the merchant's yet: it goes into `dispute_hold`, a liability the merchant cannot be paid out of. Only a final `WON` releases it. If pre-arbitration and arbitration go the issuer's way, the held funds return to the network and the merchant's balance is never touched a second time. This is Phase 10, Lesson 07's hold, used for a hold that lasts months. Winning still costs the 15.00 fee, which is why a dispute that is won is still a dispute the merchant should have prevented.

In this model pre-arbitration moves no money; in Mastercard's process the second presentment moves funds and pre-arbitration does not, and Visa's flows differ again by dispute type. What is common, and what the lesson preserves, is that money re-credited mid-dispute is provisional.

### Deadlines are transitions the clock makes

Every state with a deadline records it when the state is entered: `enteredOn + days`, from a `DeadlinePolicy`. The lesson's policy is 20 days to respond to an inquiry or a chargeback (the network allows the acquirer 30; the PSP takes 10 for itself, which is the normal arrangement), 30 days for the issuer to answer a representment, 30 for the merchant to answer pre-arbitration, and 10 for the issuer to file arbitration after a pre-arbitration response.

A daily job, `sweepDeadlines`, applies `DEADLINE_PASSED` to every open dispute whose deadline is before today. It runs on the injected clock, so the tests move the date one day at a time and watch the boundary: on the deadline day nothing fires, on the day after the dispute moves. The event id is `deadline:<dispute>:<state>:<date>`, so running the sweep twice in a day is a `DUPLICATE`, and a dispute that was answered just before the sweep (and so changed state) gets a different id and no transition. The deadline is part of the state, not a timer: nothing is scheduled, nothing can be lost on a restart, and the sweep can be re-run for any date.

### Worked example by hand

Dispute `dp_1`, 249.00 USD, Visa 13.1, chargeback received 2026-02-02 (day 0). Chargeback fee 15.00 USD, arbitration fee 500.00 USD.

| Day | Date | Event | State | Deadline | `dispute_hold` | Merchant net |
|-----|------|-------|-------|----------|----------------|--------------|
| 0 | 02-02 | first chargeback | `CHARGEBACK` | 02-22 | 0.00 | -264.00 |
| 9 | 02-11 | representment with receipt only: refused, missing proof of delivery | `CHARGEBACK` | 02-22 | 0.00 | -264.00 |
| 12 | 02-14 | representment with receipt and signed delivery | `REPRESENTED` | 03-16 | 249.00 | -264.00 |
| 41 | 03-15 | issuer files pre-arbitration | `PRE_ARBITRATION` | 04-14 | 249.00 | -264.00 |
| 56 | 03-30 | merchant responds with customer email | `PRE_ARB_RESPONDED` | 04-09 | 249.00 | -264.00 |
| 63 | 04-06 | issuer files arbitration | `ARBITRATION` | none | 249.00 | -264.00 |
| 118 | 05-31 | network rules for the merchant | `WON` | none | 0.00 | -15.00 |

The merchant was out 264.00 USD for 118 days and got 249.00 back. Had the ruling gone the other way, the hold would have returned to the network and the merchant would have been charged the filing fee: -264.00 - 500.00 = -764.00 USD on a 249.00 USD order. Had the merchant accepted on day 0: -264.00. That table is why the evidence guard exists.

### Complexity

`resolve` scans an 18-row table; a map keyed by `(state, kind)` makes it a hash lookup. `apply` is O(1) plus posting at most six lines. `sweepDeadlines` is O(D) over open disputes per run, which is fine once a day for thousands; at millions, production keeps an index on `deadline` (a B-tree in the database, or a min-heap in memory, Phase 2) and scans only the disputes due. `merchantImpact` scans the journal, O(J); production stores the dispute id on each entry and indexes it.

### What this machine does not do

- It does not prevent disputes. Pre-dispute alerts (Visa's Order Insight and Rapid Dispute Resolution through Verifi, Mastercard's Ethoca alerts) let a merchant refund before a chargeback is filed; that is cheaper than winning.
- It does not block refunds on disputed payments. Lesson 06's Exercise 2 adds the hold that does, and it is the fix for the third incident in The Problem.
- It does not model partial chargebacks against partially refunded captures, or multi-currency disputes where the chargeback amount differs from the original because of FX.
- It does not track the merchant's dispute ratio. Networks run monitoring programmes that fine acquirers, and eventually terminate merchants, whose disputes exceed thresholds on the order of one percent of transactions; Exercise 2 builds the counter.
- It does not persist across restarts or handle concurrent writers; Lesson 02's versioned store applies unchanged.

## Build It

The walkthrough is in Java, the only implementation shipped for this lesson. All code is in `code/java/Main.java`; the tests are in `code/java/MainTest.java`.

### Step 1: Reason codes as data

```java
record ReasonCode(String network, String code, String title, ReasonCategory category, List<Set<EvidenceType>> requirements) {
    ReasonCode {
        // EnumSet iterates in declaration order, so messages and the printed table are deterministic (Set.copyOf is not).
        requirements = requirements.stream().map(r -> Collections.unmodifiableSet(EnumSet.copyOf(r))).toList();
    }
    String key() { return network + " " + code; }
}
```

Each requirement is a set meaning "at least one of"; the list of requirements means "all of". The comment records a bug the first version of this file had: `Set.copyOf` returns a set whose iteration order changes between JVM runs, so the error message listing missing evidence changed from run to run. `EnumSet` iterates in declaration order, and the demo's output is now byte-identical across runs.

### Step 2: The evidence bundle

```java
List<Set<EvidenceType>> missingFor(ReasonCode reason) {
    List<Set<EvidenceType>> missing = new ArrayList<>();
    for (Set<EvidenceType> req : reason.requirements()) if (req.stream().noneMatch(this::has)) missing.add(req);
    return missing;
}

boolean liabilityShift(ReasonCode reason) {
    return reason.category() == ReasonCategory.FRAUD && has(EvidenceType.THREE_DS_AUTHENTICATION);
}
```

`missingFor` returns the unmet requirements, not a boolean, because the operator filing the response needs to know what to go and find. `liabilityShift` is deliberately category-scoped: a 3-D Secure authentication proves who the cardholder was, which answers "I didn't make this purchase" and says nothing about whether the lamp arrived.

**Predict:** for Visa 13.1, what does `missingFor` return for a bundle holding only `SIGNED_DELIVERY`?

### Step 3: The table

```java
static final List<DisputeRule> RULES = List.of(
        new DisputeRule(DisputeState.INQUIRY, DisputeEventKind.RESPOND_INQUIRY, DisputeGuard.EVIDENCE_COMPLETE_IN_TIME, DisputeState.INQUIRY_RESPONDED),
        new DisputeRule(DisputeState.INQUIRY, DisputeEventKind.CHARGEBACK_RECEIVED, DisputeGuard.ALWAYS, DisputeState.CHARGEBACK),
        ...
        new DisputeRule(DisputeState.CHARGEBACK, DisputeEventKind.SUBMIT_REPRESENTMENT, DisputeGuard.EVIDENCE_COMPLETE_IN_TIME, DisputeState.REPRESENTED),
        new DisputeRule(DisputeState.CHARGEBACK, DisputeEventKind.DEADLINE_PASSED, DisputeGuard.DEADLINE_ELAPSED, DisputeState.LOST),
        new DisputeRule(DisputeState.REPRESENTED, DisputeEventKind.DEADLINE_PASSED, DisputeGuard.DEADLINE_ELAPSED, DisputeState.WON),
        ...
        new DisputeRule(DisputeState.ARBITRATION, DisputeEventKind.RULING_FOR_ISSUER, DisputeGuard.ALWAYS, DisputeState.LOST));
```

Eighteen rows, the mermaid diagram as data. Unlike Lesson 02, no two rules share a `(state, kind)` pair, so `resolve` can take the first match; the guard decides only whether that one rule may fire.

### Step 4: Resolve with guards

```java
DisputeRule resolve(Dispute d, DisputeEvent e) {
    if (d.state.isTerminal()) throw new IllegalDisputeTransitionException(d.id + " is " + d.state + " (terminal); " + e.kind() + " refused");
    DisputeRule rule = RULES.stream().filter(r -> r.from() == d.state && r.kind() == e.kind()).findFirst()
            .orElseThrow(() -> new IllegalDisputeTransitionException(d.id + ": " + e.kind() + " is not legal from " + d.state));
    if (rule.guard() == DisputeGuard.DEADLINE_ELAPSED && (d.deadline == null || !clock.today().isAfter(d.deadline))) {
        throw new IllegalDisputeTransitionException(d.id + ": DEADLINE_PASSED on " + clock.today() + " but the deadline is " + d.deadline);
    }
    if (rule.guard() == DisputeGuard.EVIDENCE_COMPLETE_IN_TIME) {
        if (d.deadline != null && clock.today().isAfter(d.deadline)) {
            throw new IllegalDisputeTransitionException(d.id + ": " + e.kind() + " on " + clock.today() + " is after the deadline " + d.deadline);
        }
        if (e.evidence() == null) throw new IllegalDisputeTransitionException(d.id + ": " + e.kind() + " needs an evidence bundle");
        List<Set<EvidenceType>> missing = e.evidence().missingFor(d.reason);
        if (!missing.isEmpty()) {
            throw new IllegalDisputeTransitionException(d.id + ": evidence for " + d.reason.key() + " is incomplete; missing one of " + missing);
        }
    }
    return rule;
}
```

The same three refusals as Lesson 02 (terminal, no row, guard failed), with guard messages that tell an operator exactly what to do: file before a date, attach an item. Time is checked before evidence: a late response with perfect evidence is still late, and the test asserts it.

**Predict:** a chargeback was received on 2026-02-02 with a 20-day window. A complete representment is submitted on 2026-02-22. Accepted or refused? On 2026-02-23?

### Step 5: Apply: money, state, deadline, history

```java
ApplyOutcome apply(String disputeId, DisputeEvent e) {
    Dispute d = dispute(disputeId);
    if (d.appliedEventIds.contains(e.eventId())) return ApplyOutcome.DUPLICATE;
    DisputeRule rule = resolve(d, e);                       // throws before any mutation
    DisputeState from = d.state;
    postMoney(d, from, rule.to());
    d.state = rule.to();
    d.stateEnteredOn = clock.today();
    d.deadline = rule.to().isTerminal() ? null : deadlineFor(rule.to(), clock.today());
    if (e.evidence() != null) d.evidence = e.evidence();
    d.appliedEventIds.add(e.eventId());
    d.history.add(new DisputeTransition(d.history.size(), clock.today(), e.eventId(), e.kind(), from, rule.to(), e.note()));
    return ApplyOutcome.APPLIED;
}
```

Duplicate first, resolve second, mutate last: Lesson 02's order. The new deadline is computed from the date the state was entered, so each cycle gets its full window regardless of how early the previous step was answered. The journal entry is posted before the state changes; in a database both happen in one transaction.

### Step 6: The sweep

```java
List<String> sweepDeadlines() {
    List<String> fired = new ArrayList<>();
    for (Dispute d : disputes.values()) {
        if (d.state.isTerminal() || d.deadline == null || !clock.today().isAfter(d.deadline)) continue;
        boolean hasRule = RULES.stream().anyMatch(r -> r.from() == d.state && r.kind() == DisputeEventKind.DEADLINE_PASSED);
        if (!hasRule) { fired.add(d.id + " overdue in " + d.state + " (no automatic outcome)"); continue; }
        DisputeState before = d.state;
        apply(d.id, DisputeEvent.of("deadline:" + d.id + ":" + d.state + ":" + d.deadline, DisputeEventKind.DEADLINE_PASSED,
                "deadline " + d.deadline + " passed"));
        fired.add(d.id + " " + before + " -> " + d.state);
    }
    return fired;
}
```

The sweep is an ordinary caller of `apply`: it builds an event and lets the table and the guard decide. That is what makes deadline outcomes testable without special cases, and what lets an operator re-run yesterday's sweep after an outage.

**Predict:** the sweep runs twice on day 21 for a chargeback whose deadline was day 20. What does the second run return?

### Step 7: Posting the money

```java
if (to == DisputeState.CHARGEBACK) {
    what = "chargeback debited";
    p.add(Posting.debit(MERCHANT, d.amount));
    p.add(Posting.credit(SCHEME, d.amount));
    p.add(Posting.debit(MERCHANT, chargebackFee));
    p.add(Posting.credit(FEES, chargebackFee));
} else if (to == DisputeState.REPRESENTED) {
    what = "representment: funds re-credited, held";
    p.add(Posting.debit(SCHEME, d.amount));
    p.add(Posting.credit(HOLD, d.amount));
} else if (to == DisputeState.WON && from != DisputeState.CHARGEBACK) {
    what = "won: hold released to merchant";
    p.add(Posting.debit(HOLD, d.amount));
    p.add(Posting.credit(MERCHANT, d.amount));
} else if (to == DisputeState.LOST && from != DisputeState.CHARGEBACK) {
    ...   // hold back to the scheme, plus the arbitration fee if lost in ARBITRATION
} else {
    return;   // inquiry steps, pre-arbitration, arbitration filing, and LOST from CHARGEBACK move no money
}
```

The postings depend on the transition, not on the event: `WON` by issuer acceptance, by issuer silence, by withdrawal, and by ruling all release the hold the same way. Every entry's description starts with the dispute id, which `merchantImpact` uses to sum one dispute's effect on `merchant_payable`.

**Predict:** a dispute is won by `DEADLINE_PASSED` from `REPRESENTED`. Which entry is posted, and what is the merchant's net impact on a 120.00 USD dispute?

### Step 8: Run it

```bash
cd phases/12-payment-systems-design/07-disputes-and-chargebacks/code/java
javac -d .out *.java && java -cp .out Main
java -cp .out MainTest
```

Expected output, abbreviated:

```console
== A 'not received' dispute that runs the whole course: 249.00 USD, Visa 13.1 ==
day   0  first chargeback received                state CHARGEBACK        deadline 2026-02-22 hold 0.00 USD    merchant -264.00 USD
         refused: dp_1: evidence for VISA 13.1 is incomplete; missing one of [[PROOF_OF_DELIVERY, SIGNED_DELIVERY]]
day  12  representment submitted                  state REPRESENTED       deadline 2026-03-16 hold 249.00 USD  merchant -264.00 USD
day  41  pre-arbitration from issuer              state PRE_ARBITRATION   deadline 2026-04-14 hold 249.00 USD  merchant -264.00 USD
day  56  pre-arbitration response                 state PRE_ARB_RESPONDED deadline 2026-04-09 hold 249.00 USD  merchant -264.00 USD
day  63  issuer files arbitration                 state ARBITRATION       deadline -          hold 249.00 USD  merchant -264.00 USD
day 118  network ruling for the merchant          state WON               deadline -          hold 0.00 USD    merchant -15.00 USD
duplicate ruling delivery -> DUPLICATE

== Missed deadline: a fraud chargeback nobody answers ==
day 20 sweep: []
day 21 sweep: [dp_2 CHARGEBACK -> LOST]

== 3-D Secure: a fraud claim on an authenticated payment ==
liability shift: true
day 35 sweep: [dp_3 REPRESENTED -> WON]

== Arbitration lost: amount, chargeback fee, and arbitration fee ==
day   0  arbitration lost                         state LOST              deadline -          hold 0.00 USD    merchant -815.00 USD
```

The demo also prints the reason-code table, the full transition history of `dp_1`, an inquiry that closes with no money moved, and the final ledger, which balances. The test suite has 299 assertions, most of them from the exhaustive enumeration: every one of the 18 rules fires as written with the ledger balancing after it, and every one of the 112 illegal pairs is refused with state, history, and journal unchanged. The rest cover evidence matching and liability shift, the money at each step of the 118-day path, the cost of losing at each stage, the deadline boundary on the day and the day after, sweep idempotency, a forged early deadline event, late responses, and an inquiry escalated to a chargeback.

### Porting notes

A Python port keeps the table as a tuple of frozen dataclasses and the evidence requirements as `frozenset`s, but must sort them when formatting messages (`sorted(req, key=lambda t: t.value)`), because `frozenset` iteration order is not stable across runs with hash randomization: the same bug Step 1 fixed in Java. Dates are `datetime.date`. In Go, evidence types are an `iota` enum and a requirement is a `[]EvidenceType` slice kept in declaration order; `Apply` returns `(ApplyOutcome, error)` with an `ErrIllegalTransition` sentinel wrapped with the precise message; dates are `time.Time` at midnight UTC compared with `After`. The sweep is single-threaded; if it runs alongside webhook handlers, each dispute needs Lesson 02's version check so that a sweep and an issuer event racing on the same dispute cannot both apply.

## Use It

**Stripe.** A `Dispute` object has statuses `warning_needs_response`, `warning_under_review`, `warning_closed` (inquiries), `needs_response`, `under_review`, `won`, and `lost`, with an `evidence_details.due_by` timestamp and a structured `evidence` object (fields such as `shipping_documentation`, `customer_communication`, `refund_policy`) whose relevant subset depends on the reason. Stripe debits the disputed amount plus a fee from the balance when the dispute opens and returns the amount if the dispute is won, which is this lesson's chargeback and win postings. Submitting evidence is final, which is why the guard here refuses incomplete bundles rather than letting a second submission correct them.

**Adyen.** Dispute notifications arrive as webhooks with event codes `NOTIFICATION_OF_CHARGEBACK`, `CHARGEBACK`, `SECOND_CHARGEBACK`, `PREARBITRATION_WON`, `PREARBITRATION_LOST`, `CHARGEBACK_REVERSED`, and `INFORMATION_SUPPLIED`, each carrying the reason code and the defense period end date. Adyen's dispute API exposes, per reason code, the applicable defense reasons and the document types each accepts, which is this lesson's reason-code table served at run time.

**Network programmes.** Visa Claims Resolution and Mastercard's chargeback guide define the cycles, deadlines, reason codes, and fees; Visa's Compelling Evidence 3.0 and 3-D Secure liability shift are the strongest defenses to fraud claims; Verifi (Visa) and Ethoca (Mastercard) pre-dispute alerts let merchants refund before a chargeback is filed. Network monitoring programmes count disputes per merchant per month and impose fines and remediation plans above thresholds, which is why dispute ratio is a board-level metric for high-volume merchants.

**Chargeback management vendors (Chargebacks911, Midigator, Justt).** They automate evidence assembly from order, shipping, and login data per reason code and file representments within the PSP's deadline. The useful lesson from their existence: the hard part is not the state machine, it is getting the right evidence out of five internal systems before day 20.

## Ship It

`outputs/runbook-disputes.md` is an operations runbook for a dispute desk: the daily sweep and queue review, a per-reason-code evidence checklist, the accept-or-fight decision with its cost table, how to handle each network event, the ledger checks that must pass, and the escalation path when a deadline is about to be missed.

## Exercises

1. **Reinforce.** Add Visa 13.2 (Cancelled Recurring Transaction) to the reason-code table with requirements you justify from the Visa rules (for example, proof that the cancellation was not received before the billing date, and the recurring terms the cardholder accepted). Add a test that a bundle with only a receipt is refused and names both missing groups.
2. **Apply.** Track the dispute ratio: a `DisputeMonitor` that counts first chargebacks per merchant per calendar month against that month's transaction count (passed in), and reports a merchant whose ratio exceeds 0.9% with at least 100 disputes. Then connect to Lesson 06: give `RefundService` a check that refuses a refund on a capture with an open dispute, and test the double-refund scenario from The Problem.
3. **Extend.** Combine with Phase 12, Lesson 02's `PaymentStore`: persist dispute transitions with versions and optimistic concurrency, and write a test where the sweep and an `ISSUER_ACCEPTED` webhook race on a `REPRESENTED` dispute whose deadline has just passed. Exactly one must win; the loser must reload and find its event now illegal. Decide, and defend in a comment, which outcome the merchant would prefer and whether the system should care.

## Key Terms

| Term | What people say | What it actually means |
|------|-----------------|------------------------|
| Chargeback | "the customer disputed it" | The issuer reversing a cleared card transaction through the network, which takes the funds back from the acquirer without the merchant's consent |
| Retrieval request | "an inquiry" | The issuer asking for transaction information before or instead of a chargeback; no money moves |
| Representment | "fighting the chargeback" | The merchant's response through the acquirer with evidence that answers the reason code; the network re-credits the funds provisionally |
| Pre-arbitration | "second round" | The issuer rejecting the representment's evidence and asking the merchant to accept liability before the network is asked to rule |
| Arbitration | "the network decides" | The network ruling on a dispute both sides still contest; the loser pays a filing fee that can exceed the transaction amount |
| Reason code | "the dispute reason" | The network's classification of the cardholder's claim, which determines the evidence that can answer it and the time limits that apply |
| Liability shift | "3DS protection" | Fraud liability moving from merchant to issuer when the issuer authenticated the cardholder; it answers fraud claims only |
| Dispute hold | "held funds" | A liability account holding funds re-credited mid-dispute so they cannot be paid out until the outcome is final |

## Further Reading

- [Visa Core Rules and Visa Product and Service Rules](https://usa.visa.com/dam/VCOM/download/about-visa/visa-rules-public.pdf) — dispute rights, reason code categories, time limits, and the allocation and collaboration flows of Visa Claims Resolution.
- [Visa: Dispute Management Guidelines for Visa Merchants](https://usa.visa.com/dam/VCOM/global/support-legal/documents/merchants-dispute-management-guidelines.pdf) — the merchant-facing explanation of each reason code, the evidence that answers it, and how to prevent the dispute in the first place.
- [Mastercard Chargeback Guide](https://www.mastercard.us/content/dam/public/mastercardcom/na/global-site/documents/chargeback-guide.pdf) — first chargeback, second presentment, pre-arbitration and arbitration case filing, with the conditions and documentation for 4834, 4837, 4853 and the rest.
- [EMVCo: EMV 3-D Secure Protocol and Core Functions Specification](https://www.emvco.com/emv-technologies/3-d-secure/) — the authentication whose result (ECI and authentication value) is the evidence behind the fraud liability shift.
- [Stripe documentation: Disputes and evidence](https://docs.stripe.com/disputes) — a production dispute object, its statuses and due dates, and the evidence fields per dispute reason.
- [Adyen documentation: Dispute management](https://docs.adyen.com/risk-management/disputes-api/) — dispute webhooks per cycle, defense periods, and per-reason-code defense documents served by API.
