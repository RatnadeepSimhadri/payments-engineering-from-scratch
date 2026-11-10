# Payouts, Balances, and Negative Balance Handling

> Pay the merchant out of funds that have actually settled.

**Type:** Build
**Languages:** Java, Python, Go
**Prerequisites:** Phase 12 Lesson 06 (Refunds, Partial Refunds, and Refund Windows), Phase 12 Lesson 07 (Disputes and Chargebacks), Phase 10 Lesson 07 (Holds and Available Balance), Phase 10 Lesson 08 (Reconciliation), Phase 11 Lesson 08 (ACH and the NACHA File Format)
**Time:** ~90 minutes

## Learning Objectives

- Separate a merchant's balance into pending, available, reserve, and in-transit, each a ledger account, and state which events move money between them
- Make funds available only when a settlement report that reconciles against the captures says the scheme has paid, never because a date has passed
- Hold a rolling reserve as dated tranches whose sum always equals the reserve account, and release or apply them on an injected calendar
- Schedule payouts (daily or weekly, with a minimum) that never exceed available funds, and emit a payout file with control totals through a hook
- Handle a negative balance in order: net against future settlements, apply the reserve, pause payouts, and collect by debit after a grace period, including when the debit is returned

## The Problem

A marketplace pays its sellers every business day. The payout job runs at 06:00 and computes each seller's payout as "captures from two days ago, minus refunds, minus fees", because card settlement is usually T+2. For eighteen months this works. Then a Monday bank holiday in the scheme's settlement country delays one settlement file by a day. The payout job does not know; it pays sellers for Thursday's and Friday's captures as usual, 1.2 million USD of payouts funded by money the marketplace has not received. The operating account goes overdrawn, the bank charges overdraft interest and calls the treasurer, and payouts for the rest of the week are held while finance works out what happened. The job paid out of funds that were *expected* to settle. It should have paid out of funds that *had*.

The fix, "wait for the settlement file", exposes the second problem. A furniture seller sells 40,000 USD of goods in December and is paid out promptly. In February, customers start disputing orders that never arrived; chargebacks of 18,000 USD arrive over three weeks (Lesson 07). Each one debits the seller's balance, which is now zero because everything was paid out. The balance goes to -18,000 USD. The seller has stopped trading and is not answering email. The marketplace's acquirer debits the marketplace for every chargeback regardless, so the marketplace is out 18,000 USD of its own money. The payout system had no reserve, no rule for negative balances, and no way to ask the seller's bank for the money back.

And a third, smaller one: a payout to a seller's bank account is returned three days later with NACHA code R03, "no account / unable to locate account". The payout job, seeing the seller's available balance restored by the return, pays it again the next morning to the same wrong account. It is returned again. This repeats for a week, each attempt costing a return fee, until someone notices the pattern.

All three are balance-management bugs, and all three have the same cure: a merchant balance that is a set of ledger accounts with explicit rules for what moves money between them. This lesson builds that balance, the payout run that reads it, the reserve that protects against what it cannot see, and the procedure for when it goes below zero.

## The Concept

### Four balances, not one

"The merchant's balance" is four numbers, and every bug above comes from collapsing them into one:

| Balance | Meaning | Becomes something else when |
|---------|---------|-----------------------------|
| Pending | Net of captures the scheme has not yet paid us for | A settlement report names the capture: moves to available and reserve |
| Available | Settled, unreserved, not yet paid out; the only payable balance | A payout is initiated (to in transit); a refund or chargeback arrives (debited, may go negative) |
| Reserve | Settled but held back for a period as protection against future debits | Its tranche's release date arrives (to available), or available goes negative (applied early) |
| In transit | Payout sent to the bank, not yet confirmed | The bank confirms (cash leaves) or returns it (back to available) |

The total owed to the merchant is the sum of all four, and it is a liability on our books from the moment of capture. The four-way split decides *when* that liability may be discharged.

```mermaid
stateDiagram-v2
    direction LR
    [*] --> Pending: capture (net of fee)
    Pending --> Available: settlement report (net minus reserve share)
    Pending --> Reserve: settlement report (reserve share)
    Reserve --> Available: tranche release date, or applied to a negative balance
    Available --> InTransit: payout initiated
    InTransit --> [*]: bank confirms
    InTransit --> Available: bank returns (R01, R02, R03)
    Available --> [*]: refund, chargeback, fee debit
```

### The ledger behind it

Each merchant has four liability accounts; the platform has three asset accounts and fee revenue:

| Account | Type | Means |
|---------|------|-------|
| `scheme_receivable` | Asset | Captured but not yet paid to us by the scheme; refunds and chargebacks reduce it (the scheme nets them) |
| `bank_cash` | Asset | Our operating bank account |
| `collections_receivable` | Asset | Debits we have asked merchants' banks to pay us |
| `fee_revenue` | Revenue | Processing fees |
| `<m>:pending`, `<m>:available`, `<m>:reserve`, `<m>:in_transit` | Liability | The four balances of merchant `m` |

| Event | Debit | Credit |
|-------|-------|--------|
| Capture 100.00, fee 3.20 | `scheme_receivable` 100.00 | `m:pending` 96.80, `fee_revenue` 3.20 |
| Settlement report, 10% reserve | `bank_cash` 100.00, `m:pending` 96.80 | `scheme_receivable` 100.00, `m:available` 87.12, `m:reserve` 9.68 |
| Payout initiated | `m:available` 87.12 | `m:in_transit` 87.12 |
| Payout confirmed | `m:in_transit` 87.12 | `bank_cash` 87.12 |
| Payout returned | `m:in_transit` 87.12 | `m:available` 87.12 |
| Chargeback or refund | `m:available` | `scheme_receivable` |
| Reserve release or application | `m:reserve` | `m:available` |
| Collection initiated | `collections_receivable` | `m:available` |
| Collection received | `bank_cash` | `collections_receivable` |
| Collection returned | `m:available` | `collections_receivable` |

Every balance in this lesson is read from the ledger. There is no `available_balance` column that a job updates; there is the `m:available` account, whose balance is the sum of its postings. That is what makes the fourth incident (the one where the column and the ledger disagree) impossible to have.

### Settled means "the scheme said so"

The bank-holiday incident came from treating settlement as a function of the calendar. In this lesson, nothing is settled until `settle` receives a `SettlementReport`: the scheme's (or acquirer's) statement that it paid a total for a list of captures on a value date. Before it moves money, `settle` reconciles (Phase 10, Lesson 08):

1. Every capture in the report is known.
2. No capture in the report has already been settled.
3. The report's total equals the sum of the captures' gross amounts.

A report that fails any check is refused with nothing changed. The test advances the calendar ten days past the captures and asserts they are still pending and nothing is paid. In production the comparison is richer (the report carries interchange and scheme fees that must also reconcile, and some captures arrive in a later file than expected), but the rule is the same: the report moves pending to available, and the report is checked before it is believed.

### Reserves as tranches

A rolling reserve holds back a percentage of each settlement for a fixed number of days. It exists because refunds and chargebacks arrive long after the money was paid out (Lesson 07's disputes run for 120 days), and the platform cannot rely on future sales to cover them: the merchant may stop selling. Risk teams set reserves per merchant from its category, history, and dispute ratio; 5 to 10 percent for 90 to 180 days is a common shape for higher-risk merchants, and zero for most.

The reserve is a ledger account, but its release schedule is not a ledger concept: 9.68 USD from the 2026-03-12 settlement is releasable on 2026-06-10, and 7.73 from a later settlement on a later date. The service therefore keeps a list of `ReserveTranche(merchant, amount, releaseOn)` beside the ledger, and holds one invariant between them: the sum of a merchant's unreleased tranches equals its reserve account balance. Every operation that touches the reserve (settlement, release, application to a deficit) updates both, and the tests check the invariant after each one.

The reserve share is `floor(net x bps / 10000)`. Flooring favours the merchant by at most one minor unit per settlement, and the remainder goes to available, so pending is always moved in full.

### Payout scheduling

A merchant's `PayoutSchedule` is a frequency (daily on business days, or weekly on one weekday) and a minimum. On a due day, if the merchant is not paused, the run pays out *all of available* if it is at least the minimum. Three properties follow:

- **A payout never exceeds available at initiation**, because it is available. There is no computation that could make it larger.
- **Nothing pending or reserved is ever paid**, because neither is available.
- **A run is idempotent per day in effect**: a second run the same day finds available at zero.

The minimum exists because every payout costs money (an ACH entry, a wire fee, a return risk), and paying 0.37 USD daily is a loss. Weekly schedules exist for the same reason and for merchants whose accounting prefers it. Payout timing (`T+2`, `T+7`) in a merchant's contract is the gap between capture and first possible payout, which this design produces from settlement timing plus schedule, rather than from a date offset.

### The payout file hook

Payouts reach banks as files: NACHA batches in the US, ISO 20022 `pain.001` credit transfer initiations in SEPA and increasingly elsewhere. The lesson does not build either format (Phase 11 Lessons 08 and 09 do); it builds the hook. `PayoutFileSink` receives one file per run: a header, one detail line per payout or collection, and a trailer with the line count and the total credits and debits in minor units. The trailer is the control total the bank checks before it accepts the file, and the reconciliation job checks against the bank's acknowledgement afterwards; a NACHA file's batch and file control records serve exactly this purpose. The detail line carries a bank account *token*, not an account number: the number lives in a vault (Phase 11, Lesson 05) and the file writer that talks to the bank is the only component that detokenizes.

### Negative balances

A balance goes negative when debits (refunds, chargebacks, fees) exceed available. The procedure is ordered from cheapest to most expensive:

| Step | Mechanism | When |
|------|-----------|------|
| 1 | Net against future settlements | Automatic: settlement credits available, which reduces the deficit |
| 2 | Apply the reserve | Every run: move reserve to available, earliest-releasing tranche first, up to the deficit |
| 3 | Pause credit payouts | Automatic: a negative available can never meet a minimum |
| 4 | Collect by debit | After `collectAfterNegativeDays` continuously negative: a DEBIT line for the deficit |
| 5 | Write off and pursue | Outside this lesson: collections agency, legal, loss provisioning |

Step 2 uses the earliest-releasing tranches first because those are the ones the merchant would get back soonest; using later tranches first would leave early ones to be released into a balance that might go negative again. Step 4 waits because most deficits are temporary (a refund on Monday, sales on Tuesday), and a debit to a merchant's bank account is a costly, relationship-damaging event that may itself be returned. If the collection is returned (R01 insufficient funds), the deficit goes back on the balance and the clock restarts.

A returned *credit* is different. R03 (no account), R02 (account closed), and similar codes mean the destination is wrong; paying it again tomorrow will fail again. A returned credit therefore pauses the merchant's payouts until someone re-verifies the bank details, which is the fix for the third incident.

### Worked example by hand

Merchant `m_shoe`: daily payouts, 10.00 USD minimum, 10% reserve held 90 days. Fees 2.90% + 0.30.

| Date | Event | Pending | Available | Reserve | In transit |
|------|-------|---------|-----------|---------|------------|
| Mon 03-02 | captures 100.00, 250.00, 50.00 (fees 3.20, 7.55, 1.75) | 387.50 | 0.00 | 0.00 | 0.00 |
| Mon 03-02 | payout run: nothing available | 387.50 | 0.00 | 0.00 | 0.00 |
| Tue 03-03 | expected report did not arrive; payout run: nothing | 387.50 | 0.00 | 0.00 | 0.00 |
| Wed 03-04 | report totalling 399.99 refused (captures sum to 400.00) | 387.50 | 0.00 | 0.00 | 0.00 |
| Wed 03-04 | report reconciles; reserve floor(38750 x 1000 / 10000) = 3875 | 0.00 | 348.75 | 38.75 | 0.00 |
| Wed 03-04 | payout `po_0001` 348.75 | 0.00 | 0.00 | 38.75 | 348.75 |
| Thu 03-05 | bank confirms | 0.00 | 0.00 | 38.75 | 0.00 |
| Mon 03-16 | chargeback 600.00 | 0.00 | -600.00 | 38.75 | 0.00 |
| Mon 03-16 | run: reserve applied | 0.00 | -561.25 | 0.00 | 0.00 |
| Wed 03-18 | capture 80.00 (fee 2.62, net 77.38) settles: 69.65 available, 7.73 reserve; run applies the 7.73 | 0.00 | -483.87 | 0.00 | 0.00 |
| Mon 03-23 | seventh negative day: collection `co_0003` 483.87 | 0.00 | 0.00 | 0.00 | 0.00 |
| Wed 03-25 | collection received | 0.00 | 0.00 | 0.00 | 0.00 |

Check the bank account: 400.00 + 180.00 (another merchant's capture in the same report) + 80.00 settled, minus 348.75 paid out, plus 483.87 collected, is 795.12 USD, which the demo prints. The 600.00 chargeback appears as `scheme_receivable = -600.00`: the scheme will net it from a future settlement.

### Complexity

Every balance read is O(1) from the ledger's running totals. `settle` is O(c) in the captures named in the report. `releaseReserves` scans all tranches, O(T); production indexes tranches by release date (a B-tree, or a min-heap as in Phase 2) and touches only those due. `coverFromReserve` sorts a merchant's tranches, O(t log t) with t typically under 100. `runPayouts` is O(M) merchants per run, plus file writing. The tranche list costs O(settlements x reserve days) memory per merchant, bounded by the reserve period.

### What this service does not do

- It does not handle multiple currencies; a multi-currency merchant has one set of four accounts per currency (Phase 10, Lesson 09), and payouts in a currency other than the balance require FX.
- It does not compute interchange and scheme fees from the settlement report; it trusts the capture-time fee. Reconciling the two is Phase 14's reconciliation work.
- It does not support instant payouts (push-to-card or real-time rails, Phase 11 Lesson 10), which pay from available immediately for a fee and must pre-check the limit against available just like the scheduled run.
- It does not write off unrecoverable balances or provision for losses.
- It is not safe for concurrent runs. Two payout runs racing would both read the same available balance; production takes a per-merchant lock or uses a conditional ledger write (Lesson 06's pattern) when moving available to in transit.

## Build It

The walkthrough is in Java, the only implementation shipped for this lesson. All code is in `code/java/Main.java`; the tests are in `code/java/MainTest.java`.

### Step 1: Four accounts per merchant and a balance read from them

```java
void onboard(Merchant m) {
    merchants.put(m.id(), m);
    ledger.open(pendingAcct(m.id()), AccountType.LIABILITY);
    ledger.open(availableAcct(m.id()), AccountType.LIABILITY);
    ledger.open(reserveAcct(m.id()), AccountType.LIABILITY);
    ledger.open(inTransitAcct(m.id()), AccountType.LIABILITY);
}

Balance balance(String merchantId) {
    merchant(merchantId);
    return new Balance(ledger.balance(pendingAcct(merchantId)), ledger.balance(availableAcct(merchantId)),
            ledger.balance(reserveAcct(merchantId)), ledger.balance(inTransitAcct(merchantId)));
}
```

`Balance` is a value read from the ledger, never stored. `Balance.total()` is everything owed to the merchant, and the conservation test asserts that, after a settlement and a confirmed payout, gross captured equals paid out plus still owed plus fees, to the minor unit.

### Step 2: Capture into pending

```java
CaptureRecord c = new CaptureRecord(captureId, merchantId, gross, Money.ofMinor(variable + fixedFeeMinor, currency), clock.today());
List<Posting> postings = new ArrayList<>(List.of(Posting.debit(SCHEME, gross), Posting.credit(pendingAcct(merchantId), c.net())));
if (c.fee().isPositive()) postings.add(Posting.credit(FEES, c.fee()));
ledger.post(clock.today(), "capture " + captureId, postings);
```

The fee line is added only when the fee is positive, because the ledger's `Posting` refuses a zero amount: an entry with a 0.00 line is almost always a bug in whatever computed it, and the first version of this method tripped over exactly that check with a zero-fee test capture.

### Step 3: Settlement: reconcile, then move

```java
void settle(SettlementReport report) {
    Money sum = Money.zero(currency);
    for (String id : report.captureIds()) {
        CaptureRecord c = captures.get(id);
        if (c == null) throw new IllegalStateException("report " + report.id() + " names unknown capture " + id);
        if (isSettled(id)) throw new IllegalStateException("report " + report.id() + ": " + id + " already settled by " + settledBy.get(id));
        sum = sum.plus(c.gross());
    }
    if (!sum.equals(report.totalGross())) {
        throw new IllegalStateException("report " + report.id() + " does not reconcile: total " + report.totalGross() + " but captures sum to " + sum);
    }
    ...
    for (Map.Entry<String, Money> e : netByMerchant.entrySet()) {
        Money reserve = Money.ofMinor(Math.floorDiv(Math.multiplyExact(net.minorUnits(), m.reserve().basisPoints()), 10_000), currency);
        postings.add(Posting.debit(pendingAcct(m.id()), net));
        postings.add(Posting.credit(availableAcct(m.id()), net.minus(reserve)));
        if (reserve.isPositive()) {
            postings.add(Posting.credit(reserveAcct(m.id()), reserve));
            tranches.add(new ReserveTranche(m.id(), reserve, report.valueDate().plusDays(m.reserve().holdDays())));
        }
    }
    ledger.post(clock.today(), "settlement " + report.id(), postings);
}
```

All three reconciliation checks run before the first write. One report can settle captures of many merchants; it becomes one journal entry with the cash line and three lines per merchant, so the report either lands completely or not at all. The tranche's release date counts from the report's value date, the day the money actually arrived.

**Predict:** a report settles a merchant's 50.00 USD zero-fee capture under a 10% reserve. Which postings does the entry contain, and what tranche is created?

### Step 4: Debits may go negative

```java
void debitMerchant(String merchantId, Money amount, String reason) {
    merchant(merchantId);
    if (!amount.isPositive()) throw new IllegalArgumentException("debit must be positive");
    ledger.post(clock.today(), reason + " " + merchantId, List.of(
            Posting.debit(availableAcct(merchantId), amount), Posting.credit(SCHEME, amount)));
    trackNegative(merchantId);
}
```

A chargeback is not refusable: the scheme has already taken the money (Lesson 07). The design therefore lets available go below zero rather than pretending the debit can be rejected, and records the first day it went negative. `trackNegative` clears that date whenever available returns to zero or above, so the collection clock measures *continuous* negativity.

### Step 5: Apply the reserve, earliest tranche first

```java
Money coverFromReserve(String merchantId) {
    Money deficit = ledger.balance(availableAcct(merchantId)).negate();
    Money used = Money.zero(currency);
    if (!deficit.isPositive()) return used;
    List<ReserveTranche> mine = new ArrayList<>(tranches.stream().filter(t -> t.merchantId().equals(merchantId))
            .sorted((a, b) -> a.releaseOn().compareTo(b.releaseOn())).toList());
    for (ReserveTranche t : mine) {
        if (!deficit.isPositive()) break;
        Money take = t.amount().min(deficit);
        tranches.remove(t);
        if (take.compareTo(t.amount()) < 0) tranches.add(new ReserveTranche(merchantId, t.amount().minus(take), t.releaseOn()));
        used = used.plus(take);
        deficit = deficit.minus(take);
    }
    ...
}
```

A tranche partly used is split: the used part is removed, the remainder keeps its original release date. The ledger entry moves the total used from reserve to available, and the tranche list shrinks by the same amount, which keeps the tranche invariant.

**Predict:** a merchant has tranches of 5.00 (release 06-01) and 8.00 (release 06-15) and available of -6.00. What remains in the tranche list after `coverFromReserve`?

### Step 6: The payout run

```java
for (Merchant m : merchants.values()) {
    coverFromReserve(m.id());
    Money available = ledger.balance(availableAcct(m.id()));
    LocalDate since = negativeSince.get(m.id());
    if (available.isNegative() && since != null && !today.isBefore(since.plusDays(collectAfterNegativeDays))) {
        Payout debit = new Payout(String.format("co_%04d", ++payoutSeq), m.id(), available.negate(), PayoutDirection.DEBIT, today);
        ledger.post(today, "collection " + debit.id, List.of(
                Posting.debit(COLLECTIONS, debit.amount), Posting.credit(availableAcct(m.id()), debit.amount)));
        ...
        continue;
    }
    if (!m.schedule().dueOn(today) || isPaused(m.id())) continue;
    if (available.compareTo(m.schedule().minimumPayout()) < 0 || !available.isPositive()) continue;
    Payout credit = new Payout(String.format("po_%04d", ++payoutSeq), m.id(), available, PayoutDirection.CREDIT, today);
    ledger.post(today, "payout " + credit.id, List.of(
            Posting.debit(availableAcct(m.id()), available), Posting.credit(inTransitAcct(m.id()), available)));
    ...
}
```

The order per merchant is the negative-balance procedure: apply the reserve, then collect if the grace period is over, then pay only if due, not paused, positive, and at least the minimum. A collection zeroes available into `collections_receivable`, an asset: the merchant's debt to us has become a claim on its bank. The amount of a credit payout is `available` itself.

**Predict:** a merchant went negative on Monday 03-16 and `collectAfterNegativeDays` is 7. On which date does the run first emit a DEBIT line?

### Step 7: The file and the bank's answers

```java
lines.add("H," + today + "," + name + "," + currency.code());
for (Payout p : batch) {
    lines.add("D," + p.id + "," + p.merchantId + "," + merchant(p.merchantId).bankAccountToken() + "," + p.direction + "," + p.amount.minorUnits());
    if (p.direction == PayoutDirection.CREDIT) credits += p.amount.minorUnits(); else debits += p.amount.minorUnits();
}
lines.add("T," + batch.size() + "," + credits + "," + debits);
sink.write(name, lines);
```

```java
void returned(String payoutId, String returnCode) {
    ...
    if (p.direction == PayoutDirection.CREDIT) {
        ledger.post(clock.today(), "payout returned " + p.id + " " + returnCode, List.of(
                Posting.debit(inTransitAcct(p.merchantId), p.amount), Posting.credit(availableAcct(p.merchantId), p.amount)));
        paused.put(p.merchantId, "payout " + p.id + " returned " + returnCode);
    } else {
        ledger.post(clock.today(), "collection returned " + p.id + " " + returnCode, List.of(
                Posting.debit(availableAcct(p.merchantId), p.amount), Posting.credit(COLLECTIONS, p.amount)));
        negativeSince.remove(p.merchantId);
        trackNegative(p.merchantId);
    }
    ...
}
```

Amounts in the file are integer minor units, as NACHA amounts are. `confirm` is idempotent (a second confirmation of a `PAID` line is ignored) and refuses a returned line. A returned credit pauses payouts; a returned collection restores the deficit and restarts the negative clock from today.

### Step 8: Run it

```bash
cd phases/12-payment-systems-design/08-payouts-and-balance-management/code/java
javac -d .out *.java && java -cp .out Main
java -cp .out MainTest
```

Expected output, abbreviated:

```console
2026-03-02 MON after 3 captures (400.00 gross)        pending 387.50 USD  available 0.00 USD     reserve 0.00 USD   in transit 0.00 USD
payout run Monday -> []  (nothing settled, nothing to pay)
payout run Tuesday -> []  (the expected T+1 report did not arrive)
refused: report st_bad does not reconcile: total 399.99 USD but captures sum to 400.00 USD
2026-03-04 WED after settlement st_0302               pending 0.00 USD    available 348.75 USD   reserve 38.75 USD  in transit 0.00 USD
payout run Wednesday -> [po_0001 CREDIT m_shoe 348.75 USD IN_TRANSIT]
file payouts_2026-03-04_001.csv
   H,2026-03-04,payouts_2026-03-04_001.csv,USD
   D,po_0001,m_shoe,ba_tok_7Q2,CREDIT,34875
   T,1,34875,0
payout run Friday -> [po_0002 CREDIT m_art 174.48 USD IN_TRANSIT]
next Friday run -> []  (m_art paused: true)
2026-03-16 MON after the chargeback                   pending 0.00 USD    available -600.00 USD  reserve 38.75 USD  in transit 0.00 USD
2026-03-16 MON reserve applied, still negative        pending 0.00 USD    available -561.25 USD  reserve 0.00 USD   in transit 0.00 USD
2026-03-18 WED netted by st_0316, reserve applied     pending 0.00 USD    available -483.87 USD  reserve 0.00 USD   in transit 0.00 USD
payout run -> [co_0003 DEBIT m_shoe 483.87 USD IN_TRANSIT]
2026-03-25 WED collection received                    pending 0.00 USD    available 0.00 USD     reserve 0.00 USD   in transit 0.00 USD
   bank_cash                  795.12 USD
   ledger balances: true, journal entries: 16
```

The test suite has 49 assertions: pending is never paid and time alone settles nothing; each reconciliation failure leaves everything unchanged; reserve and available amounts to the minor unit; the file's header, detail, and control totals; confirmation idempotency; minimums and both schedule kinds; the reserve release boundary on day 89 and day 90; every step of the negative-balance procedure including the day-6 and day-7 boundary and a returned collection; a returned credit pausing and resuming payouts; and conservation of money across twenty captures.

### In Python

The Python port (`code/python/main.py`, tests in `test_main.py`) is the same design with four differences. **Dates are `datetime.date`** and `java.time.DayOfWeek` becomes a small `Enum` over `date.weekday()`, so `PayoutSchedule` keeps the same `weekly_day` field and the same weekend check; `release_on + timedelta(days=hold_days)` replaces `plusDays`, and "not before" is `not today < since + timedelta(...)`. **Integer arithmetic:** fees and reserves are `(x * bps + 5_000) // 10_000` and `(net * bps) // 10_000` on non-negative values; `//` is the floor `Math.floorDiv` gave, and `int` cannot overflow so `multiplyExact` goes away. **Tranche bookkeeping uses list comprehensions:** `release_reserves` collects the due tranches and keeps `[t for t in tranches if t not in due]` (the semantics of `removeAll`); `cover_from_reserve` sorts the merchant's tranches by `release_on` and `list.remove` finds them by dataclass equality, as `List.remove` did by record equality. **Exceptions:** reconciliation failures, duplicate captures and bad state changes (`IllegalStateException`) raise `RuntimeError`; unknown ids and bad arguments (`IllegalArgumentException`) raise `ValueError`; `confirm` on an already-`PAID` payout still returns silently.

Run it from `code/python/` with `python3 main.py`; test with `python3 -m unittest -v`.

#### Step 1: Four accounts per merchant and a balance read from them

`onboard` opens the four liability accounts whose names come from the static `*_acct` helpers; `balance` reads all four from the ledger and returns a frozen `Balance` dataclass. Nothing is cached, so the balance can never disagree with the journal.

```python
    def onboard(self, m: Merchant) -> None:
        self._merchants[m.id] = m
        self._ledger.open(BalanceService.pending_acct(m.id), AccountType.LIABILITY)
        self._ledger.open(BalanceService.available_acct(m.id), AccountType.LIABILITY)
        self._ledger.open(BalanceService.reserve_acct(m.id), AccountType.LIABILITY)
        self._ledger.open(BalanceService.in_transit_acct(m.id), AccountType.LIABILITY)

    def merchant(self, id: str) -> Merchant:
        m = self._merchants.get(id)
        if m is None:
            raise ValueError(f"unknown merchant {id}")
        return m

    def balance(self, merchant_id: str) -> Balance:
        self.merchant(merchant_id)
        return Balance(self._ledger.balance(BalanceService.pending_acct(merchant_id)),
                       self._ledger.balance(BalanceService.available_acct(merchant_id)),
                       self._ledger.balance(BalanceService.reserve_acct(merchant_id)),
                       self._ledger.balance(BalanceService.in_transit_acct(merchant_id)))
```

#### Step 2: Capture into pending

`capture` computes the fee with integer `//`, credits the net to the merchant's pending account, and appends the fee line only when the fee is positive, because `Posting` refuses a zero amount.

```python
    def capture(self, capture_id: str, merchant_id: str, gross: Money, fee_bps: int, fixed_fee_minor: int) -> CaptureRecord:
        """A capture: the scheme owes us the gross; the merchant is owed the net, but it is PENDING until settlement."""
        self.merchant(merchant_id)
        if capture_id in self._captures:
            raise RuntimeError(f"capture exists: {capture_id}")
        variable = (gross.minor_units * fee_bps + 5_000) // 10_000
        c = CaptureRecord(capture_id, merchant_id, gross, Money.of_minor(variable + fixed_fee_minor, self._currency), self._clock.today())
        postings = [Posting.debit(BalanceService.SCHEME, gross), Posting.credit(BalanceService.pending_acct(merchant_id), c.net())]
        if c.fee.is_positive():
            postings.append(Posting.credit(BalanceService.FEES, c.fee))
        self._ledger.post(self._clock.today(), f"capture {capture_id}", postings)
        self._captures[capture_id] = c
        return c
```

#### Step 3: Settlement: reconcile, then move

All three checks (known capture, not yet settled, total matches) raise `RuntimeError` before anything is written. The per-merchant nets accumulate in a dict (insertion-ordered, like the Java `LinkedHashMap`), the reserve is `(net * bps) // 10_000`, and the tranche's release date counts from the report's `value_date`.

```python
    def settle(self, report: SettlementReport) -> None:
        """Apply a settlement report. It must reconcile first: every capture known and unsettled, and the report's
        total equal to the sum of their gross amounts. Only then does cash arrive and pending become available,
        minus the merchant's rolling reserve, which is held as a tranche with its own release date."""
        total = Money.zero(self._currency)
        for cid in report.capture_ids:
            c = self._captures.get(cid)
            if c is None:
                raise RuntimeError(f"report {report.id} names unknown capture {cid}")
            if self.is_settled(cid):
                raise RuntimeError(f"report {report.id}: {cid} already settled by {self._settled_by[cid]}")
            total = total.plus(c.gross)
        if total != report.total_gross:
            raise RuntimeError(f"report {report.id} does not reconcile: total {report.total_gross} but captures sum to {total}")
        postings = [Posting.debit(BalanceService.BANK, total), Posting.credit(BalanceService.SCHEME, total)]
        net_by_merchant: dict[str, Money] = {}
        for cid in report.capture_ids:
            c = self._captures[cid]
            net_by_merchant[c.merchant_id] = net_by_merchant.get(c.merchant_id, Money.zero(self._currency)).plus(c.net())
            self._settled_by[cid] = report.id
        for merchant_id, net in net_by_merchant.items():
            m = self.merchant(merchant_id)
            reserve = Money.of_minor((net.minor_units * m.reserve.basis_points) // 10_000, self._currency)
            postings.append(Posting.debit(BalanceService.pending_acct(m.id), net))
            postings.append(Posting.credit(BalanceService.available_acct(m.id), net.minus(reserve)))
            if reserve.is_positive():
                postings.append(Posting.credit(BalanceService.reserve_acct(m.id), reserve))
                self._tranches.append(ReserveTranche(m.id, reserve, report.value_date + timedelta(days=m.reserve.hold_days)))
        self._ledger.post(self._clock.today(), f"settlement {report.id}", postings)
```

#### Step 4: Debits may go negative

`debit_merchant` posts against available without checking the balance and then calls `_track_negative`, which uses `setdefault` to record the first negative day and `pop` to clear it when the balance recovers.

```python
    def debit_merchant(self, merchant_id: str, amount: Money, reason: str) -> None:
        """A refund, chargeback, or fee: it comes out of available, which may go negative. The scheme nets it from what it owes us."""
        self.merchant(merchant_id)
        if not amount.is_positive():
            raise ValueError("debit must be positive")
        self._ledger.post(self._clock.today(), f"{reason} {merchant_id}", [
            Posting.debit(BalanceService.available_acct(merchant_id), amount), Posting.credit(BalanceService.SCHEME, amount)])
        self._track_negative(merchant_id)

    def _track_negative(self, merchant_id: str) -> None:
        if self._ledger.balance(BalanceService.available_acct(merchant_id)).is_negative():
            self._negative_since.setdefault(merchant_id, self._clock.today())
        else:
            self._negative_since.pop(merchant_id, None)
```

#### Step 5: Apply the reserve, earliest tranche first

`sorted(..., key=lambda t: t.release_on)` orders the merchant's tranches; `self._tranches.remove(t)` removes by dataclass equality and a partly used tranche is re-appended with its remainder and original date. The ledger entry moves exactly `used` from reserve to available.

```python
    def cover_from_reserve(self, merchant_id: str) -> Money:
        """Negative balance, step 1: use the merchant's own reserve, earliest-releasing tranches first, before
        asking anyone for money. That is what the reserve is for."""
        deficit = self._ledger.balance(BalanceService.available_acct(merchant_id)).negate()
        used = Money.zero(self._currency)
        if not deficit.is_positive():
            return used
        mine = sorted((t for t in self._tranches if t.merchant_id == merchant_id), key=lambda t: t.release_on)
        for t in mine:
            if not deficit.is_positive():
                break
            take = t.amount.min(deficit)
            self._tranches.remove(t)
            if take.compare_to(t.amount) < 0:
                self._tranches.append(ReserveTranche(merchant_id, t.amount.minus(take), t.release_on))
            used = used.plus(take)
            deficit = deficit.minus(take)
        if used.is_positive():
            self._ledger.post(self._clock.today(), f"reserve applied to negative balance {merchant_id}", [
                Posting.debit(BalanceService.reserve_acct(merchant_id), used),
                Posting.credit(BalanceService.available_acct(merchant_id), used)])
        self._track_negative(merchant_id)
        return used
```

#### Step 6: The payout run

Per merchant, in order: cover from reserve, collect if the balance has been negative for `collect_after_negative_days` (a `DEBIT` payout into `collections_receivable`), otherwise pay only if due, not paused, positive, and at least the minimum. The loop iterates `list(self._merchants.values())` so the dict is not mutated under iteration.

```python
    def run_payouts(self) -> list[Payout]:
        """The daily payout run. For each merchant: cover a negative balance from reserve; if it has been negative
        for collect_after_negative_days, ask its bank for the deficit (a DEBIT line); otherwise, if its schedule is
        due, it is not paused, and available meets the minimum, pay out all of available (a CREDIT line). One
        file per run, with a trailer carrying the counts and totals the bank checks."""
        today = self._clock.today()
        batch: list[Payout] = []
        for m in list(self._merchants.values()):
            self.cover_from_reserve(m.id)
            available = self._ledger.balance(BalanceService.available_acct(m.id))
            since = self._negative_since.get(m.id)
            if available.is_negative() and since is not None and not today < since + timedelta(days=self._collect_after_negative_days):
                self._payout_seq += 1
                debit = Payout(f"co_{self._payout_seq:04d}", m.id, available.negate(), PayoutDirection.DEBIT, today)
                self._ledger.post(today, f"collection {debit.id}", [
                    Posting.debit(BalanceService.COLLECTIONS, debit.amount),
                    Posting.credit(BalanceService.available_acct(m.id), debit.amount)])
                self._track_negative(m.id)
                self._payouts[debit.id] = debit
                batch.append(debit)
                continue
            if not m.schedule.due_on(today) or self.is_paused(m.id):
                continue
            if available.compare_to(m.schedule.minimum_payout) < 0 or not available.is_positive():
                continue
            self._payout_seq += 1
            credit = Payout(f"po_{self._payout_seq:04d}", m.id, available, PayoutDirection.CREDIT, today)
            self._ledger.post(today, f"payout {credit.id}", [
                Posting.debit(BalanceService.available_acct(m.id), available),
                Posting.credit(BalanceService.in_transit_acct(m.id), available)])
            self._payouts[credit.id] = credit
            batch.append(credit)
        if batch:
            self._write_file(today, batch)
        return batch
```

#### Step 7: The file and the bank's answers

`_write_file` emits the header, one `D` line per payout with the amount in integer minor units, and a trailer with counts and totals. `returned` reverses a credit back to available and pauses the merchant, or restores a collection's deficit and restarts the negative clock from today.

```python
    def _write_file(self, today: date, batch: list[Payout]) -> None:
        self._file_seq += 1
        name = f"payouts_{today}_{self._file_seq:03d}.csv"
        lines = [f"H,{today},{name},{self._currency.code}"]
        credits = 0
        debits = 0
        for p in batch:
            lines.append(f"D,{p.id},{p.merchant_id},{self.merchant(p.merchant_id).bank_account_token},{p.direction.name},{p.amount.minor_units}")
            if p.direction is PayoutDirection.CREDIT:
                credits += p.amount.minor_units
            else:
                debits += p.amount.minor_units
        lines.append(f"T,{len(batch)},{credits},{debits}")
        self._sink.write(name, lines)

    def returned(self, payout_id: str, return_code: str) -> None:
        """The bank returns a line (NACHA return codes: R01 insufficient funds, R02 account closed, R03 no account).
        A returned credit goes back to available and payouts pause until the bank details are fixed; a returned
        debit puts the deficit back on the merchant's balance."""
        p = self.payout(payout_id)
        if p.state is not PayoutState.IN_TRANSIT:
            raise RuntimeError(f"{payout_id} is {p.state.name}; cannot return")
        if p.direction is PayoutDirection.CREDIT:
            self._ledger.post(self._clock.today(), f"payout returned {p.id} {return_code}", [
                Posting.debit(BalanceService.in_transit_acct(p.merchant_id), p.amount),
                Posting.credit(BalanceService.available_acct(p.merchant_id), p.amount)])
            self._paused[p.merchant_id] = f"payout {p.id} returned {return_code}"
        else:
            self._ledger.post(self._clock.today(), f"collection returned {p.id} {return_code}", [
                Posting.debit(BalanceService.available_acct(p.merchant_id), p.amount),
                Posting.credit(BalanceService.COLLECTIONS, p.amount)])
            self._negative_since.pop(p.merchant_id, None)
            self._track_negative(p.merchant_id)
        p.state = PayoutState.RETURNED
        p.return_code = return_code
```

#### Step 8: Run it

```bash
cd phases/12-payment-systems-design/08-payouts-and-balance-management/code/python
python3 main.py
python3 -m unittest -v
```

The output matches the Java run line for line:

```console
2026-03-02 MON after 3 captures (400.00 gross)        pending 387.50 USD  available 0.00 USD     reserve 0.00 USD   in transit 0.00 USD
payout run Monday -> []  (nothing settled, nothing to pay)
payout run Tuesday -> []  (the expected T+1 report did not arrive)

== Wednesday: the settlement report arrives and reconciles; 10% of m_shoe's net goes to a 90-day reserve ==
refused: report st_bad does not reconcile: total 399.99 USD but captures sum to 400.00 USD
2026-03-04 WED after settlement st_0302               pending 0.00 USD    available 348.75 USD   reserve 38.75 USD  in transit 0.00 USD
payout run Wednesday -> [po_0001 CREDIT m_shoe 348.75 USD IN_TRANSIT]
```

### In Go

The Go port (`code/go/main.go`, `money.go`, tests in `main_test.go`) keeps the design with five differences. **Every mutation returns an `error`, and nothing is written until every check passes:** `Settle` reconciles, builds the full posting list, posts once, and only then marks captures settled and appends tranches; `RunPayouts` returns `([]*Payout, error)`; `Confirm` and `Returned` return `error`. Two sentinels cover the Java exceptions, `ErrIllegalState` (a report that does not reconcile, a double settlement, a line that cannot be confirmed) and `ErrInvalidArgument` (unknown merchant, zero debit, a reserve over 100%), and the tests match them with `errors.Is`. **`null` is `(T, bool)` or the zero value:** `NegativeSince` returns `(time.Time, bool)` instead of a nullable date; `Payout.ReturnCode` is `""` until the bank returns the line; a `DAILY` schedule simply ignores its `WeeklyDay` field where Java passed `null`. **Record equality becomes an explicit method:** Java's `tranches.remove(t)` removes the first element `equals` to a record; `removeTranche` does the same with `ReserveTranche.equal`, comparing the date with `time.Time.Equal` rather than `==`, and `ReleaseReserves` filters into a kept slice instead of `removeAll`; `LinkedHashMap`s (merchants, files, net-by-merchant in `Settle`) are a map plus an order slice so files and payouts come out in the Java order. **Dates and days:** `Date(2026, time.March, 2)` is `LocalDate.of`, `AddDate(0, 0, n)` is `plusDays`, `Weekday()` replaces `getDayOfWeek()`, and the demo prints `MON`/`TUE` with `strings.ToUpper(wd.String()[:3])`; everything is midnight UTC, so `Before`/`After` compare whole days. **Integer fees without `multiplyExact`:** the fee and the reserve share use `mulChecked` and `floorDiv` from `money.go`, so an overflow is an error rather than a silent wrap and the floor is a floor (Go's `/` truncates toward zero).

Run it from `code/go/` with `go run .`; test with `go test ./...`.

#### Step 1: Four accounts per merchant and a balance read from them

`Onboard` refuses a duplicate merchant, opens the four accounts in a loop, and records the merchant in `merchantOrder` so later runs iterate deterministically. `Balance` validates the id and reads the four balances with `mustBalance`, which is safe because `Onboard` opened them.

```go
func PendingAcct(m string) string   { return m + ":pending" }
func AvailableAcct(m string) string { return m + ":available" }
func ReserveAcct(m string) string   { return m + ":reserve" }
func InTransitAcct(m string) string { return m + ":in_transit" }

func (s *BalanceService) Ledger() *Ledger { return s.ledger }

func (s *BalanceService) Onboard(m Merchant) error {
	if _, exists := s.merchants[m.ID]; exists {
		return fmt.Errorf("%w: merchant exists: %s", ErrInvalidArgument, m.ID)
	}
	for _, acct := range []string{PendingAcct(m.ID), AvailableAcct(m.ID), ReserveAcct(m.ID), InTransitAcct(m.ID)} {
		if err := s.ledger.Open(acct, LIABILITY); err != nil {
			return err
		}
	}
	s.merchants[m.ID] = m
	s.merchantOrder = append(s.merchantOrder, m.ID)
	return nil
}

func (s *BalanceService) Merchant(id string) (Merchant, error) {
	m, ok := s.merchants[id]
	if !ok {
		return Merchant{}, fmt.Errorf("%w: unknown merchant %s", ErrInvalidArgument, id)
	}
	return m, nil
}

func (s *BalanceService) Balance(merchantID string) (Balance, error) {
	if _, err := s.Merchant(merchantID); err != nil {
		return Balance{}, err
	}
	return Balance{s.ledger.mustBalance(PendingAcct(merchantID)), s.ledger.mustBalance(AvailableAcct(merchantID)),
		s.ledger.mustBalance(ReserveAcct(merchantID)), s.ledger.mustBalance(InTransitAcct(merchantID))}, nil
}
```

#### Step 2: Capture into pending

`Capture` returns `(CaptureRecord, error)`; the fee goes through `mulChecked` and `floorDiv`, and the fee posting is appended only when positive.

```go
func (s *BalanceService) Capture(captureID, merchantID string, gross Money, feeBps, fixedFeeMinor int64) (CaptureRecord, error) {
	if _, err := s.Merchant(merchantID); err != nil {
		return CaptureRecord{}, err
	}
	if _, exists := s.captures[captureID]; exists {
		return CaptureRecord{}, fmt.Errorf("%w: capture exists: %s", ErrIllegalState, captureID)
	}
	scaled, err := mulChecked(gross.MinorUnits, feeBps)
	if err != nil {
		return CaptureRecord{}, err
	}
	variable := floorDiv(scaled+5_000, 10_000)
	c := CaptureRecord{captureID, merchantID, gross, OfMinor(variable+fixedFeeMinor, s.currency), s.clock.Today()}
	postings := []Posting{Debit(SCHEME, gross), Credit(PendingAcct(merchantID), c.Net())}
	if c.Fee.IsPositive() {
		postings = append(postings, Credit(FEES, c.Fee))
	}
	if err := s.ledger.Post(s.clock.Today(), "capture "+captureID, postings); err != nil {
		return CaptureRecord{}, err
	}
	s.captures[captureID] = c
	return c, nil
}
```

#### Step 3: Settlement: reconcile, then move

`Settle` runs the three checks and returns `ErrIllegalState` before any write. It builds the posting list and the new tranches into locals, posts once, and only then records `settledBy` and appends the tranches, so a failed post leaves no partial state. `merchantOrder` is the explicit insertion order a Go map lacks.

```go
func (s *BalanceService) Settle(report SettlementReport) error {
	sum := Zero(s.currency)
	for _, id := range report.CaptureIDs {
		c, ok := s.captures[id]
		if !ok {
			return fmt.Errorf("%w: report %s names unknown capture %s", ErrIllegalState, report.ID, id)
		}
		if s.IsSettled(id) {
			return fmt.Errorf("%w: report %s: %s already settled by %s", ErrIllegalState, report.ID, id, s.settledBy[id])
		}
		sum = mustPlus(sum, c.Gross)
	}
	if sum != report.TotalGross {
		return fmt.Errorf("%w: report %s does not reconcile: total %v but captures sum to %v", ErrIllegalState, report.ID, report.TotalGross, sum)
	}
	postings := []Posting{Debit(BANK, sum), Credit(SCHEME, sum)}
	var merchantOrder []string
	netByMerchant := map[string]Money{}
	for _, id := range report.CaptureIDs {
		c := s.captures[id]
		if prev, seen := netByMerchant[c.MerchantID]; seen {
			netByMerchant[c.MerchantID] = mustPlus(prev, c.Net())
		} else {
			merchantOrder = append(merchantOrder, c.MerchantID)
			netByMerchant[c.MerchantID] = c.Net()
		}
	}
	var newTranches []ReserveTranche
	for _, mid := range merchantOrder {
		m := s.merchants[mid]
		net := netByMerchant[mid]
		scaled, err := mulChecked(net.MinorUnits, m.Reserve.BasisPoints)
		if err != nil {
			return err
		}
		reserve := OfMinor(floorDiv(scaled, 10_000), s.currency)
		postings = append(postings, Debit(PendingAcct(m.ID), net), Credit(AvailableAcct(m.ID), mustMinus(net, reserve)))
		if reserve.IsPositive() {
			postings = append(postings, Credit(ReserveAcct(m.ID), reserve))
			newTranches = append(newTranches, ReserveTranche{m.ID, reserve, report.ValueDate.AddDate(0, 0, m.Reserve.HoldDays)})
		}
	}
	if err := s.ledger.Post(s.clock.Today(), "settlement "+report.ID, postings); err != nil {
		return err
	}
	for _, id := range report.CaptureIDs {
		s.settledBy[id] = report.ID
	}
	s.tranches = append(s.tranches, newTranches...)
	return nil
}
```

#### Step 4: Debits may go negative

`DebitMerchant` posts without a balance check and calls `trackNegative`, which records the first negative day only when not already tracked and deletes the entry once the balance recovers.

```go
func (s *BalanceService) DebitMerchant(merchantID string, amount Money, reason string) error {
	if _, err := s.Merchant(merchantID); err != nil {
		return err
	}
	if !amount.IsPositive() {
		return fmt.Errorf("%w: debit must be positive", ErrInvalidArgument)
	}
	if err := s.ledger.Post(s.clock.Today(), reason+" "+merchantID, []Posting{Debit(AvailableAcct(merchantID), amount), Credit(SCHEME, amount)}); err != nil {
		return err
	}
	s.trackNegative(merchantID)
	return nil
}

func (s *BalanceService) trackNegative(merchantID string) {
	if s.available(merchantID).IsNegative() {
		if _, tracked := s.negativeSince[merchantID]; !tracked {
			s.negativeSince[merchantID] = s.clock.Today()
		}
	} else {
		delete(s.negativeSince, merchantID)
	}
}
```

#### Step 5: Apply the reserve, earliest tranche first

`CoverFromReserve` sorts the merchant's tranches with `sort.SliceStable` on `ReleaseOn.Before`, removes each consumed tranche via `removeTranche` (first element `equal` to it, like `List.remove`), and re-appends any remainder with its original date.

```go
func (s *BalanceService) CoverFromReserve(merchantID string) (Money, error) {
	deficit := s.available(merchantID).Negate()
	used := Zero(s.currency)
	if !deficit.IsPositive() {
		return used, nil
	}
	var mine []ReserveTranche
	for _, t := range s.tranches {
		if t.MerchantID == merchantID {
			mine = append(mine, t)
		}
	}
	sort.SliceStable(mine, func(i, j int) bool { return mine[i].ReleaseOn.Before(mine[j].ReleaseOn) })
	for _, t := range mine {
		if !deficit.IsPositive() {
			break
		}
		take := t.Amount.Min(deficit)
		s.removeTranche(t)
		if take.Compare(t.Amount) < 0 {
			s.tranches = append(s.tranches, ReserveTranche{merchantID, mustMinus(t.Amount, take), t.ReleaseOn})
		}
		used = mustPlus(used, take)
		deficit = mustMinus(deficit, take)
	}
	if used.IsPositive() {
		if err := s.ledger.Post(s.clock.Today(), "reserve applied to negative balance "+merchantID,
			[]Posting{Debit(ReserveAcct(merchantID), used), Credit(AvailableAcct(merchantID), used)}); err != nil {
			return Zero(s.currency), err
		}
	}
	s.trackNegative(merchantID)
	return used, nil
}

// removeTranche drops the first tranche equal to t, as List.remove(Object) does for a record.
func (s *BalanceService) removeTranche(t ReserveTranche) {
	for i, x := range s.tranches {
		if x.equal(t) {
			s.tranches = append(s.tranches[:i], s.tranches[i+1:]...)
			return
		}
	}
}
```

#### Step 6: The payout run

`RunPayouts` walks `merchantOrder`, covers from reserve, collects when `negativeSince` is set and the grace period has elapsed (`!today.Before(since.AddDate(...))`), and otherwise pays out `available` when due, unpaused, positive, and at least the minimum. Any ledger error returns the batch so far.

```go
func (s *BalanceService) RunPayouts() ([]*Payout, error) {
	today := s.clock.Today()
	batch := []*Payout{}
	for _, mid := range s.merchantOrder {
		m := s.merchants[mid]
		if _, err := s.CoverFromReserve(m.ID); err != nil {
			return batch, err
		}
		available := s.available(m.ID)
		since, negative := s.negativeSince[m.ID]
		if available.IsNegative() && negative && !today.Before(since.AddDate(0, 0, s.collectAfterNegativeDays)) {
			s.payoutSeq++
			debit := &Payout{ID: fmt.Sprintf("co_%04d", s.payoutSeq), MerchantID: m.ID, Amount: available.Negate(), Direction: PAYOUT_DEBIT, InitiatedOn: today, State: IN_TRANSIT}
			if err := s.ledger.Post(today, "collection "+debit.ID, []Posting{Debit(COLLECTIONS, debit.Amount), Credit(AvailableAcct(m.ID), debit.Amount)}); err != nil {
				return batch, err
			}
			s.trackNegative(m.ID)
			s.payouts[debit.ID] = debit
			batch = append(batch, debit)
			continue
		}
		if !m.Schedule.DueOn(today) || s.IsPaused(m.ID) {
			continue
		}
		if available.Compare(m.Schedule.MinimumPayout) < 0 || !available.IsPositive() {
			continue
		}
		s.payoutSeq++
		credit := &Payout{ID: fmt.Sprintf("po_%04d", s.payoutSeq), MerchantID: m.ID, Amount: available, Direction: PAYOUT_CREDIT, InitiatedOn: today, State: IN_TRANSIT}
		if err := s.ledger.Post(today, "payout "+credit.ID, []Posting{Debit(AvailableAcct(m.ID), available), Credit(InTransitAcct(m.ID), available)}); err != nil {
			return batch, err
		}
		s.payouts[credit.ID] = credit
		batch = append(batch, credit)
	}
	if len(batch) > 0 {
		s.writeFile(today, batch)
	}
	return batch, nil
}
```

#### Step 7: The file and the bank's answers

`writeFile` prints amounts as `MinorUnits` integers with a trailer of counts and totals. `Returned` reverses a credit into available and pauses the merchant, or reverses a collection and restarts the negative clock; both paths end by setting `RETURNED` and the return code.

```go
func (s *BalanceService) writeFile(today time.Time, batch []*Payout) {
	s.fileSeq++
	name := fmt.Sprintf("payouts_%s_%03d.csv", fmtDate(today), s.fileSeq)
	lines := []string{"H," + fmtDate(today) + "," + name + "," + s.currency.Code}
	var credits, debits int64
	for _, p := range batch {
		lines = append(lines, fmt.Sprintf("D,%s,%s,%s,%v,%d", p.ID, p.MerchantID, s.merchants[p.MerchantID].BankAccountToken, p.Direction, p.Amount.MinorUnits))
		if p.Direction == PAYOUT_CREDIT {
			credits += p.Amount.MinorUnits
		} else {
			debits += p.Amount.MinorUnits
		}
	}
	lines = append(lines, fmt.Sprintf("T,%d,%d,%d", len(batch), credits, debits))
	s.sink.Write(name, lines)
}

func (s *BalanceService) Returned(payoutID, returnCode string) error {
	p, err := s.Payout(payoutID)
	if err != nil {
		return err
	}
	if p.State != IN_TRANSIT {
		return fmt.Errorf("%w: %s is %v; cannot return", ErrIllegalState, payoutID, p.State)
	}
	if p.Direction == PAYOUT_CREDIT {
		if err := s.ledger.Post(s.clock.Today(), "payout returned "+p.ID+" "+returnCode,
			[]Posting{Debit(InTransitAcct(p.MerchantID), p.Amount), Credit(AvailableAcct(p.MerchantID), p.Amount)}); err != nil {
			return err
		}
		s.paused[p.MerchantID] = "payout " + p.ID + " returned " + returnCode
	} else {
		if err := s.ledger.Post(s.clock.Today(), "collection returned "+p.ID+" "+returnCode,
			[]Posting{Debit(AvailableAcct(p.MerchantID), p.Amount), Credit(COLLECTIONS, p.Amount)}); err != nil {
			return err
		}
		delete(s.negativeSince, p.MerchantID)
		s.trackNegative(p.MerchantID)
	}
	p.State = RETURNED
	p.ReturnCode = returnCode
	return nil
}
```

#### Step 8: Run it

```bash
cd phases/12-payment-systems-design/08-payouts-and-balance-management/code/go
go run .
go test ./...
```

The output is identical to the Java run; the closing ledger reads:

```console
== Ledger ==
   scheme_receivable         -600.00 USD
   bank_cash                  795.12 USD
   collections_receivable       0.00 USD
   fee_revenue                 20.64 USD
   m_shoe                       0.00 USD owed in total
   m_art                      174.48 USD owed in total
   ledger balances: true, journal entries: 16
```

## Use It

**Stripe Connect.** A connected account's balance has `pending` and `available` per currency, and a separate `instant_available`; funds move from pending to available on the account's `available_on` date, which Stripe derives from its own settlement. Payout schedules are `daily`, `weekly` with a `weekly_anchor`, `monthly`, or `manual`, with a per-country minimum delay. Negative balances on connected accounts are debited from the platform's balance or from the connected account's bank account depending on configuration, and Stripe documents reserves as part of risk controls. Payouts have `in_transit`, `paid`, `failed`, and `canceled` statuses, and a failed payout carries a failure code such as `account_closed` or `no_account`, which correspond to this lesson's return codes.

**Adyen for Platforms.** Balance accounts hold funds per currency; sweeps (scheduled transfers) move available funds to a transfer instrument on a schedule with a trigger amount and can be configured to *pull* funds into a balance account that drops below a threshold, which is the collection step here. Adyen's documentation distinguishes `received`, `pending`, and `balance` amounts, which map to pending, in transit, and available.

**NACHA and ISO 20022.** A NACHA file's batch header, entry detail records, batch control, and file control records with entry counts, entry hash, and total debit and credit amounts are the production version of this lesson's H/D/T lines. Return reason codes (R01 insufficient funds, R02 account closed, R03 no account, R04 invalid account number, R10 unauthorized debit) come back in a return file days later. ISO 20022 `pain.001` (initiation) and `pain.002` (status report) serve the same roles for SEPA credit transfers and increasingly for other rails.

**Marketplace treasury.** Every large marketplace (Amazon, Etsy, Airbnb, Uber) runs some version of this: hold funds until a release condition (settlement, delivery confirmation, a stay completing), reserve against high-risk sellers, net refunds and chargebacks against future earnings, and collect from sellers' bank accounts only as a last resort. The design decisions are this lesson's: what is the release condition, how big is the reserve, and how long a seller may stay negative.

## Ship It

`outputs/design-doc-merchant-balances-and-payouts.md` is a design-document template for a merchant balance and payout system: the balance states and the events between them, the ledger accounts and their postings, the settlement release condition and its reconciliation checks, the reserve policy and its tranche invariant, the payout schedule and file format, and the negative-balance procedure with its thresholds. Fill it in before building payouts for a new platform, or use it to review an existing one against the three incidents in The Problem.

## Exercises

1. **Reinforce.** Add a fixed minimum reserve: a merchant must always keep at least 500.00 USD in reserve regardless of the percentage, topped up from available at settlement before anything else. Test that a merchant with 300.00 of settlements has 300.00 reserved and nothing available, and that the next 300.00 settlement leaves 200.00 reserved more and 100.00 available.
2. **Apply.** Add instant payouts: `instantPayout(merchant, amount)` pays immediately from available for a 1% fee (minimum 0.50 USD) posted to `fee_revenue`, refusing amounts above available after the fee. Then add a daily instant-payout limit per merchant, and test that a scheduled payout later the same day pays only what remains.
3. **Extend.** Connect to Lessons 06 and 07: have `RefundService` and `DisputeService` post their merchant debits through `debitMerchant`, so that a refund reservation and a chargeback both reduce `m:available` in this ledger, and Lesson 07's dispute hold is paid out only on `WON`. Write the scenario from The Problem: 40,000 USD of sales paid out, 18,000 USD of chargebacks over three weeks, a 10% reserve. How much does the platform recover from the reserve, how much from netting, and how much must it collect?

## Key Terms

| Term | What people say | What it actually means |
|------|-----------------|------------------------|
| Pending balance | "unsettled funds" | Net of captures the scheme has not yet paid us for; owed to the merchant but not payable |
| Available balance | "the balance" | Settled, unreserved funds not yet paid out; the only balance a payout may draw on, and the one debits hit |
| Settlement | "when the money comes in" | The scheme or acquirer paying us for a set of captures, evidenced by a report that must reconcile before funds are released |
| Rolling reserve | "held funds" | A percentage of each settlement held back for a fixed period, as dated tranches, to cover future refunds and chargebacks |
| In transit | "payout sent" | A payout the bank has been told to make but has not confirmed; still a liability, and returnable |
| Negative balance | "merchant owes us" | Available below zero after debits; recovered by netting, the reserve, and finally a collection debit |
| Return code | "the payout bounced" | The bank's reason a credit or debit failed (R01, R02, R03); a wrong-destination return pauses payouts, an insufficient-funds return restores the deficit |
| Control totals | "the trailer" | Counts and sums in a payment file's trailer that the bank checks before accepting the file and that reconciliation checks after |

## Further Reading

- [Nacha Operating Rules and guidelines](https://www.nacha.org/rules) — the ACH file structure (batch and file control records with counts and totals), and the return reason codes and time frames that govern payouts and collections in the US.
- [ISO 20022 message definitions: Payments Initiation (pain.001, pain.002)](https://www.iso20022.org/catalogue-messages/iso-20022-messages-archive?search=pain) — the customer credit transfer initiation and status report messages that replace proprietary payout files outside the US.
- [Stripe documentation: Connect payouts and balances](https://docs.stripe.com/connect/payouts-connected-accounts) — pending and available balances, payout schedules and minimums, negative balance handling, and payout failure codes in a production platform.
- [Adyen documentation: Balance platform sweeps and balance accounts](https://docs.adyen.com/platforms/payout-to-users/scheduled-payouts/) — scheduled push and pull sweeps with trigger amounts, which are this lesson's payout run and collection step.
- [Visa Core Rules and Visa Product and Service Rules](https://usa.visa.com/dam/VCOM/download/about-visa/visa-rules-public.pdf) — clearing and settlement between acquirer and issuer, and the acquirer's liability for its merchants' chargebacks, which is why the platform needs reserves at all.
