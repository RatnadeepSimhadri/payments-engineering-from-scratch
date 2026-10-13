# SLIs, SLOs, and Error Budgets

> Availability is a number you choose and then pay for.

**Type:** Learn
**Languages:** —
**Prerequisites:** Phase 5, Lesson 01 (Failure Models), Phase 9 README
**Time:** ~60 minutes

## Learning Objectives

- Define an SLI as a ratio of good events to valid events and write three for a payment authorization service: availability, latency, and correctness
- Choose an SLO target and window and compute the resulting error budget in minutes and in failed requests
- Compute a burn rate from an observed error ratio and state how long until the budget is exhausted
- Design a multi-window, multi-burn-rate alert and explain why a single threshold on raw error rate pages too often and too late at the same time
- Distinguish SLI, SLO, SLA, and error budget policy, and say who owns each

## The Problem

A card-authorization service advertises "99.99% availability" on a slide. Nobody wrote down what a good request is. The on-call rotation has an alert: page if the HTTP 5xx rate exceeds 1% for five minutes. In one quarter it fires 71 times. Forty of those are a batch job that retries a dead merchant endpoint every night at 02:00 and produces a burst of 5xx against a background of almost no traffic; the ratio spikes, the page fires, the engineer wakes up, nothing is wrong for any customer. Meanwhile, for eleven days, 0.4% of authorizations return `HTTP 200` with a body saying `"status": "declined", "reason": "processor_timeout"`, because a downstream connection pool is misconfigured. Those are counted as successes by the alert. The merchants notice first. The eleven days cost 0.4% of all authorization volume: a few hundred thousand dollars of declined legitimate payments and a churned enterprise account.

The team has two problems, and both come from not having defined the number on the slide. First, their indicator measures the wrong thing. A `200` with a timeout decline is a failure for the merchant; a `503` to a batch job retrying a dead endpoint is noise. Second, their alert measures the right quantity in the wrong units. "1% for five minutes" says nothing about how much of the quarter's acceptable failure the current incident is consuming. A 1% error rate when you promise 99.99% is burning the budget 100 times faster than sustainable; a 0.4% error rate for eleven days consumed the entire quarter's budget four times over without ever crossing the threshold.

Service level objectives fix both problems by making the number on the slide operational. An SLI defines, in code, what counts as a good event. An SLO sets a target for that SLI over a window. The error budget is the arithmetic consequence: how many bad events you can afford before you break the promise. Alerts are then expressed as rates of budget consumption, which is a unit that directly answers the question an on-call engineer actually has: if this continues, when do we break the promise, and is that soon enough to wake up for?

## The Concept

### Four words that get mixed up

| Term | Who sets it | What it is | Example |
|------|-------------|------------|---------|
| SLI (indicator) | engineers | A measurement: good events / valid events, over a window | Fraction of authorization requests that returned a definitive approve or decline within 800 ms |
| SLO (objective) | product + engineering | A target for an SLI over a window, used internally | 99.95% over a rolling 30 days |
| SLA (agreement) | legal + sales | A contract with a customer, with consequences if an SLO-like target is missed | 99.9% monthly, with service credits; always looser than the SLO |
| Error budget | arithmetic | `1 − SLO`, expressed as allowed bad events or allowed downtime in the window | 0.05% of 30 days = 21.6 minutes, or 0.05% of requests |

The SLA is looser than the SLO so that the SLO is breached, and the team acts, before any money changes hands. If the SLO equals the SLA, every internal miss is a contractual miss.

### Writing an SLI

Google's SRE workbook gives the shape: **SLI = good events / valid events**, as a percentage. The two decisions are what is *valid* (which events count at all) and what is *good*.

For an authorization service, three SLIs cover most of what matters:

1. **Availability.** Valid: every authorization request that reached the service with a well-formed body. Good: the response was a definitive approve or decline from the issuer path. Not good: 5xx, timeouts, and *any* decline whose reason is an internal failure (`processor_timeout`, `internal_error`), even if the HTTP status was 200. Excluded from valid: requests rejected for malformed input (`400`), because a client bug is not a service failure, and health checks.
2. **Latency.** Valid: the same set. Good: the response was returned within 800 ms. This is written as "fraction of requests under 800 ms", not as "p99 latency", because a fraction can be averaged over a window and a percentile cannot. A target of 99% under 800 ms is a p99 of 800 ms, stated in a form you can budget.
3. **Correctness.** Valid: every authorization that reached a terminal state. Good: the amount, currency, and merchant on the authorization match the amount, currency, and merchant on the issuer's record for that transaction, as checked by the nightly reconciliation (Phase 13). This SLI is slow (it is known a day late) but it is the one that measures whether money moved correctly. For money movement, a correctness SLI is not optional.

Two principles. First, measure as close to the user as you can afford: a load-balancer log sees failures the application never logged (it crashed). Second, the SLI must be computable from data you already keep, with no judgement call per event, or it will not be computed.

### Authorization success: classifying every outcome

The availability SLI lives or dies on the classification table. "Declined" is not one outcome; it is a dozen, and only some of them are the service's fault. Write the table down, check it into the repository next to the SLI query, and review it whenever a new reason code appears.

| Outcome | HTTP | Valid? | Good? | Why |
|---------|------|--------|-------|-----|
| Approved by issuer | 200 | yes | yes | the service did its job |
| Declined: insufficient funds, do not honor, expired card | 200 | yes | yes | a definitive answer from the issuer is success for the service |
| Declined by our fraud engine (rule or model) | 200 | yes | yes | a deliberate decision, measured by a separate fraud-quality SLI |
| Declined: `processor_timeout`, `issuer_unavailable`, `internal_error` | 200 | yes | **no** | the merchant got a decline because our path failed |
| Stand-in approval (STIP) after issuer timeout | 200 | yes | yes, but count it | approved, but track the rate: STIP shifts risk onto us |
| 5xx, connection reset, load balancer timeout | 5xx | yes | no | the classic failure |
| Response after the client's timeout (we answered in 9 s, client gave up at 8 s) | 200 | yes | no | the merchant saw a timeout; the latency SLI also counts it |
| 400 malformed body, unknown currency, bad card number format | 4xx | no | — | client bug; tracked on a per-merchant integration dashboard |
| 401/403 bad API key | 4xx | no | — | client configuration, unless our key store is down (watch the rate) |
| 409 idempotency conflict (Phase 12) | 409 | no | — | the protocol working as designed |
| 429 rate limited | 429 | yes | depends | good if the merchant exceeded its contracted rate; bad if our global limiter shed load |

Two rows deserve a second look. A spike in 401s is usually a merchant rotating keys badly, but if every merchant gets 401 at once your credential cache is broken; exclusions need their own volume alarm, or a failure can hide inside the "not valid" bucket. And 429 is ambiguous on purpose: the classification must look at *why* the limiter fired, which means the limiter has to log the reason.

### Money-movement correctness

Availability asks "did we answer?" Correctness asks "did the money end up where we said it would?" For a payments system the second question matters more, and it has different mechanics:

- **Unit.** One event per money movement (an authorization, a capture, a refund, a payout), not per HTTP request. Retries collapse into one event through the idempotency key.
- **Valid.** Movements that have reached a terminal state and whose external record has arrived (the processor settlement file, the bank statement line). Movements still inside the settlement window are not yet valid; they are *pending*, and they are reported separately so a stuck file does not look like perfect correctness.
- **Good.** Ledger amount, currency, direction, and counterparty all equal the external record, matched by reconciliation within the agreed window (T+1 for card settlement files, T+2 for some ACH returns).
- **Two measures.** Count the fraction of movements that matched, and also the fraction of *value* that matched. One unmatched 50,000.00 USD payout and 10,000 matched 200.00 USD captures is 99.99% by count and 97.6% by value; both numbers belong on the dashboard.

Correctness targets are tighter than availability targets, typically 99.999% by count, with any single unmatched movement above a value threshold paged regardless of budget. A failed request can be retried by the merchant; a misbooked payout is a debt someone has to chase. The budget is still useful: it tells the team whether reconciliation breaks are a background rate to grind down or a regression that started with last Tuesday's deploy.

The latency of the correctness SLI changes how it is alerted. It is known a day late, so a fast-burn page is impossible; the alert is a daily check of breaks against budget, plus an immediate page from the reconciliation job when the day's break count exceeds, say, ten times the trailing median.

### Choosing the SLO

The target is a product decision disguised as an engineering one. Three constraints bound it:

- **Dependencies.** A service cannot promise more than the product of its hard dependencies. If the issuer gateway offers 99.95% and the database 99.99%, the authorization path cannot exceed roughly 99.94% without redundancy around one of them.
- **Cost.** Each additional nine roughly multiplies the cost of achieving it: more replicas, more regions, more engineering time on failover. 99.99% leaves 4.3 minutes per month, which is less than one human-paced incident response.
- **User perception.** Below some threshold the user cannot tell the difference. A customer whose mobile network drops 1% of requests cannot distinguish 99.99% from 99.999%.

The window matters as much as the number. A 30-day rolling window is the common default: long enough that one bad hour does not dominate, short enough that a bad month is forgotten by the next one. Calendar windows (per month) reset the budget abruptly and invite end-of-month risk-taking; rolling windows do not.

### The error budget: worked arithmetic

Take the availability SLO: 99.95% over 30 days.

```text
window           = 30 days = 43,200 minutes
error budget     = 1 - 0.9995 = 0.0005 = 0.05%
budget in time   = 0.0005 * 43,200 min = 21.6 minutes of total outage
                   (or any equivalent: 43.2 minutes at 50% failure, 216 minutes at 10%, ...)
```

The same budget in requests, at 2,000 authorizations per second:

```text
requests / 30 d  = 2,000 * 86,400 * 30 = 5,184,000,000
budget in events = 0.0005 * 5,184,000,000 = 2,592,000 failed authorizations
```

The two forms are the same budget. The time form is intuitive; the event form is what the SLI actually measures, and it is what makes partial outages legible: a 10% failure rate for 216 minutes spends exactly the budget.

Each extra nine divides the budget by ten. Over a 30-day window, for a large merchant path at 2,000 TPS (5.184 billion requests) and a regional service at 50 TPS (129.6 million requests):

| SLO | Budget | Full-outage minutes per 30 d | Failed requests at 2,000 TPS | Failed requests at 50 TPS |
|-----|--------|------------------------------|------------------------------|---------------------------|
| 99% | 1% | 432 | 51,840,000 | 1,296,000 |
| 99.5% | 0.5% | 216 | 25,920,000 | 648,000 |
| 99.9% | 0.1% | 43.2 | 5,184,000 | 129,600 |
| 99.95% | 0.05% | 21.6 | 2,592,000 | 64,800 |
| 99.99% | 0.01% | 4.32 | 518,400 | 12,960 |
| 99.999% | 0.001% | 0.432 (26 s) | 51,840 | 1,296 |

Read the 99.99% row against your incident history. If the median time from page to mitigation is 20 minutes, a single total outage spends nearly five months of budget. A 99.99% target is achievable only if most failures are partial (one shard, one region, one issuer) or are mitigated automatically.

Budgets are spent by many small incidents, not one big one. A month for the 99.95% service, in events:

| Day | Incident | Error ratio | Duration | Failed requests | Budget spent | Cumulative |
|-----|----------|-------------|----------|-----------------|--------------|------------|
| 3 | one issuer gateway node flapping | 0.5% | 90 min | 54,000 | 2.1% | 2.1% |
| 8 | bad deploy, rolled back | 30% | 6 min | 216,000 | 8.3% | 10.4% |
| 15 | regional database failover | 100% | 4 min | 480,000 | 18.5% | 28.9% |
| 22 | certificate expiry on one tokenization endpoint | 12% | 25 min | 360,000 | 13.9% | 42.8% |
| all | background (0.01% every hour of the month) | 0.01% | 30 d | 518,400 | 20.0% | 62.8% |

The arithmetic is `ratio × duration_s × 2,000` failed requests, divided by 2,592,000. Note the last row: a 0.01% background rate that nobody would ever page for is a fifth of the budget. That is the argument for the 3-day ticket alert, and for keeping a running "top budget consumers" report.

### A decision procedure for the target

1. List the hard dependencies on the request path and their measured (not advertised) availability over the last 90 days. Multiply them: `0.9995 × 0.9999 × 0.9998 = 0.9992`. That is your ceiling without redundancy.
2. Take the last 90 days of your own SLI. The first SLO should be achievable on that history with some budget left: if you ran at 99.97%, start at 99.95%, not 99.99%.
3. Check the SLA. The SLO must be strictly tighter than any customer contract, with room for at least one more incident of typical size.
4. Check the human loop. Budget minutes per window must exceed your page-to-mitigation time for a typical incident; otherwise every page is a breach.
5. Write the policy (what happens at 0% remaining) before publishing the number. If nobody will agree to freeze launches at that target, the target is too tight or the policy is fiction.
6. Revisit quarterly. Tighten when you have consistently spent less than half the budget and users still complain; loosen when you spend it every month and users do not notice.

### Burn rate

Burn rate is the ratio of the observed error rate to the error rate that would exactly spend the budget over the window.

```text
burn rate = observed error ratio / (1 - SLO)
```

For the 99.95% SLO, the sustainable error ratio is 0.0005. Some burn rates and what they mean:

| Observed error ratio | Burn rate | Budget exhausted after | Interpretation |
|----------------------|-----------|------------------------|----------------|
| 0.05% | 1 | 30 days | spending exactly on plan |
| 0.1% | 2 | 15 days | worth a ticket, not a page |
| 0.7% | 14.4 | 50 hours | page: 2% of monthly budget per hour |
| 1.0% | 20 | 36 hours | page |
| 3.0% | 60 | 12 hours | page, and probably an incident |
| 100% | 2,000 | 21.6 minutes | total outage |

Return to The Problem. The 0.4% decline-as-200 defect is a burn rate of 8: the budget was gone in 3.75 days, and the defect lasted eleven. The 02:00 batch spike, by contrast, is a 1% ratio but at 02:00 traffic is perhaps 50 requests per second: for five minutes that is 150 failed requests out of a budget of 2.6 million, which is 0.006% of the budget. Raw error rate could not tell these apart. Burn rate, applied to the right SLI, separates them completely.

```mermaid
flowchart LR
  A["Events<br/>(requests, with status, latency, reason)"] --> B["SLI<br/>good / valid"]
  B --> C["SLO<br/>target over window"]
  C --> D["Error budget<br/>1 - SLO, in events or minutes"]
  B --> E["Burn rate<br/>observed error / (1 - SLO)"]
  D --> F["Budget remaining"]
  E --> G["Alerts<br/>multi-window, multi-burn-rate"]
  F --> H["Error budget policy<br/>freeze features when budget < 0"]
```

### Alerting on burn rate: why multiple windows

A single alert, "page if burn rate over the last hour exceeds 14.4", has two failure modes. It is too slow for a total outage (you wait an hour before the one-hour average catches up) and it keeps paging after the incident is over (the one-hour average stays high for an hour after errors stop). A short window alone, "burn rate over 5 minutes exceeds 14.4", catches the outage fast but pages on every 5-minute blip.

The SRE workbook's answer is to combine windows and burn rates:

| Severity | Long window | Short window | Burn rate | Budget consumed by long window | Action |
|----------|-------------|--------------|-----------|-------------------------------|--------|
| Page | 1 hour | 5 minutes | 14.4 | 2% | wake someone up |
| Page | 6 hours | 30 minutes | 6 | 5% | wake someone up |
| Ticket | 3 days | 6 hours | 1 | 10% | fix during business hours |

Read the first row as: page when **both** the 1-hour burn rate and the 5-minute burn rate exceed 14.4. The long window says the problem is big enough to matter (2% of a month's budget in an hour). The short window says it is still happening right now, which stops the alert re-firing for the rest of the hour after a fix. The short window is conventionally one twelfth of the long one.

Where does 14.4 come from? It is the burn rate at which a one-hour incident consumes 2% of a 30-day budget: `(2% × 30 days) / 1 hour = 0.6 days / (1/24 day) = 14.4`. The three tiers then cover a total outage (detected in about 5 minutes by the first row), a moderate degradation (6 hours at 6x spends 5%), and a slow leak like the 0.4% defect (burn rate 8 is below the first row's 14.4, so it trips the 6-hour row after 4.5 hours, when that window holds 5% of the budget, and the 3-day row after 9 hours).

Detection time for a total outage under the first row: the 5-minute window reaches burn rate 14.4 after `14.4 × 0.0005 × 5 min ≈ 2.2 seconds` of 100% errors, and the 1-hour window after `14.4 × 0.0005 × 60 min ≈ 26 seconds`. In practice the scrape interval dominates, and you page in two to three minutes.

### Detection time for any error ratio

The general formula, for an error ratio `e` that starts abruptly against a clean background: a window of length `W` with threshold burn rate `B` fires once its average reaches `B × (1 − SLO)`, which takes

```text
t_detect = B × (1 − SLO) × W / e        (only if e > B × (1 − SLO); otherwise never)
```

The alert needs both windows, and the long one is always slower, so the long window sets detection time. For the 99.95% SLO (thresholds: row 1 fires above 0.72% errors, row 2 above 0.3%, row 3 above 0.05%):

| Error ratio (burn) | Row 1: 1 h / 5 m, B 14.4 | Row 2: 6 h / 30 m, B 6 | Row 3: 3 d / 6 h, B 1 | Budget spent before first alert |
|--------------------|--------------------------|------------------------|------------------------|---------------------------------|
| 100% (2,000) | 26 s | 65 s | 2.2 min | 2% |
| 10% (200) | 4.3 min | 10.8 min | 21.6 min | 2% |
| 2.5% (50) | 17.3 min | 43.2 min | 1.4 h | 2% |
| 1% (20) | 43.2 min | 1.8 h | 3.6 h | 2% |
| 0.4% (8) | never | 4.5 h | 9 h | 5% |
| 0.1% (2) | never | never | 36 h | 10% |
| 0.04% (0.8) | never | never | never | none: on track to meet the SLO |

The last column is the design property worth remembering. When the background is clean, the first alert fires once the long window has accumulated exactly that row's budget share: `B × W / 30 days`, so 2%, 5%, or 10%. The alerts are not tuned in error-ratio units at all; they are tuned in "how much of the month are we willing to lose before a human looks."

Resetting matters as much as firing. After the fix, the short window drains below its threshold within its own length (at most 5 minutes, 30 minutes, 6 hours), and the alert resolves even though the long window is still elevated. Without the short window, row 1 would keep paging for up to an hour after the rollback.

In Prometheus-style rules the first two rows look like this, with recording rules `slo:auth_errors:ratio_rate<window>` computing bad / valid over each window:

```yaml
- alert: AuthAvailabilityFastBurn
  expr: |
    slo:auth_errors:ratio_rate1h > (14.4 * 0.0005)
    and
    slo:auth_errors:ratio_rate5m > (14.4 * 0.0005)
  labels: {severity: page}
- alert: AuthAvailabilitySlowBurn
  expr: |
    slo:auth_errors:ratio_rate6h > (6 * 0.0005)
    and
    slo:auth_errors:ratio_rate30m > (6 * 0.0005)
  labels: {severity: page}
```

**Low traffic breaks ratios.** At 02:00 the service sees 50 requests per second, so the 5-minute window holds 15,000 requests and row 1's threshold is 108 failures. One merchant retrying a dead endpoint 200 times in a minute fires it. Three standard fixes: require a minimum event count in the short window before the alert can fire (for example 1,000 valid events); generate synthetic authorizations against a test issuer so the denominator never collapses; or, for genuinely tiny services, alert on the absolute count of failures in a longer window rather than on a ratio.

### The error budget policy

The budget is only useful if something happens when it runs out. The policy is written down before the quarter starts and signed by product and engineering, and it typically says:

- Budget remaining above 0: ship features at normal velocity; the on-call owns reliability work on a best-effort basis.
- Budget exhausted: feature launches for this service are frozen; the team's sprint is reliability work until the 30-day window has recovered above the SLO.
- Budget consumed by a dependency: the dependency's team is pulled into the postmortem; the SLO is not relaxed.

The freeze is what makes the number real. Without it, the SLO is the slide again.

### A payments SLO document

The SLO lives in a short document in the service's repository, reviewed like code. A complete one for the authorization service:

```yaml
service: card-authorization
owners: {engineering: payments-auth-team, product: acceptance-pm}
window: rolling 30 days
slis:
  availability:
    valid: "POST /v1/authorizations with a well-formed body; excludes 4xx validation, 401/403, 409, health checks"
    good: "definitive issuer or fraud-engine decision; NOT declines with reason in {processor_timeout, issuer_unavailable, internal_error}"
    source: "edge load-balancer logs joined to auth decision log on request_id"
    slo: 99.95%
  latency:
    valid: "same as availability"
    good: "response sent within 800 ms of request received at the edge"
    slo: 99%
  correctness:
    unit: "one money movement (auth, capture, refund), retries collapsed by idempotency key"
    valid: "terminal movements whose processor settlement record has arrived"
    good: "amount, currency, direction, merchant equal the settlement record"
    measured_by: "nightly reconciliation job (Phase 13)"
    slo: "99.999% by count; any unmatched movement over 10,000.00 USD pages immediately"
slices: [region, card_network, merchant_tier]
alerts:
  - {severity: page,   long: 1h, short: 5m,  burn: 14.4, min_events_short: 1000}
  - {severity: page,   long: 6h, short: 30m, burn: 6,    min_events_short: 1000}
  - {severity: ticket, long: 3d, short: 6h,  burn: 1}
sla_backed: "99.9% monthly availability for enterprise merchants (service credits)"
policy:
  budget_remaining_below_50pct: "release requires an on-call sign-off"
  budget_exhausted: "feature launches frozen; sprint is reliability work until back above SLO"
  dependency_caused: "dependency team joins postmortem; SLO not relaxed"
review: "quarterly, with the last quarter's budget consumers ranked"
```

Every field answers a question someone will ask during an incident: is this request counted, where does the number come from, who decides, what happens next.

### Failure modes of an SLO program

| Failure | Symptom | Fix |
|---------|---------|-----|
| SLI measured at the application, not the edge | the service crashes and the SLI shows 100% (no logs, no errors) | measure at the load balancer or a client-side probe; alert on missing data |
| "Good" defined by HTTP status only | timeout declines returned as 200 burn nothing | classify by decline reason (the table above) |
| Exclusions without volume alarms | a credential-cache outage turns every request into a 401 and disappears from "valid" | alert on the rate of each excluded class |
| Average over all merchants | one small merchant's 100% outage is 0.01% of global traffic | slice by merchant tier or region, with a per-slice SLO |
| Percentile SLO ("p99 under 800 ms") | cannot be summed across windows or replicas; budget arithmetic is wrong | state latency as a fraction of requests under a threshold |
| SLO equals SLA | every internal miss is a contractual payout | keep the SLO strictly tighter |
| Policy nobody enforces | budget exhausted in week two, launches continue | sign the policy before the quarter; make release tooling read the budget |
| Correctness counted only by events | a single large misbooked payout looks like noise | report by value as well as by count; page on large single breaks |
| Ratio alerts on low traffic | 02:00 pages from a handful of failures | minimum event counts, synthetic traffic, or count-based alerts |

### What SLOs do not do

- They do not catch a failure your SLI does not measure. The 0.4% defect was invisible until "declined with internal reason" was defined as not-good. Every incident postmortem should ask whether the SLI would have seen it.
- They do not replace capacity planning or chaos testing; they tell you when reliability is slipping, not why.
- A single SLO for a service with two very different user populations (a 10,000 TPS enterprise merchant and a long tail of small merchants) will be dominated by the large one. Slice the SLI by the dimension that matters, or the small merchants' outage will never register.
- They are not an SLA. Publishing your internal SLO as a contractual target removes the room you need to act before the customer notices.

## Worked Example

A payments platform runs an authorization service at 2,000 TPS. The SLO is 99.95% availability over a rolling 30 days, with the availability SLI defined above (declines with internal reasons count as bad).

**Day 1 to day 9.** Normal operation. The SLI runs at 99.985%: an observed error ratio of 0.015%, a burn rate of 0.3. After nine days the team has spent 9 × 0.3 / 30 = 9% of its budget.

**Day 10, 14:00.** A deploy introduces a connection-pool misconfiguration to the issuer gateway. 2.5% of requests time out and return `200 declined processor_timeout`. Error ratio 2.5%, burn rate 50.

- 14:02: the 5-minute window shows burn rate 50 (over 14.4); the 1-hour window reaches 14.4 after about 17 minutes at this rate (14.4 / 50 of an hour). At 14:17 both conditions of the first row hold and the page fires.
- Budget spent so far in this incident: 17 minutes at burn rate 50 = 17 × 50 / 43,200 of the window = 1.97% of the budget.
- 14:40: rollback completes. Errors stop. The 5-minute window drops below threshold within five minutes; the alert resolves at 14:45 even though the 1-hour window stays high until 15:40. Total incident: 40 minutes at burn rate 50 = 4.6% of the budget.

**Day 10 after the incident.** Budget spent: 9% + 4.6% = 13.6%. Remaining: 86.4%. No freeze.

**Days 11 to 30.** Suppose the same 0.4% slow defect from The Problem appears on day 15 and runs undetected by humans. Burn rate 8. Burn rate 8 is below the fast-burn threshold, but the 6-hour / 30-minute row fires about 4.5 hours after onset, once 5% of the budget is gone; the team finds it on day 15 instead of day 26. Had they not, 15 days at burn rate 8 would be 400% of the budget: the SLO would have been missed by a factor of four, and under the policy, feature work would be frozen for most of the following month.

The arithmetic a reviewer should be able to reproduce from this example: 21.6 minutes of budget, burn rate 50 for 40 minutes is 4.6% of it, and the difference between detection on day 15 and day 26 is the difference between a ticket and a frozen quarter.

## Exercises

1. **Reinforce.** The latency SLO is 99% of authorizations under 800 ms over 30 days. At 2,000 TPS, how many requests may exceed 800 ms in the window? If a slow database replica causes 5% of requests to exceed 800 ms, what is the burn rate and how long until the budget is gone? Then compute the burn-rate thresholds for a 2% / 1 hour and a 5% / 6 hour alert on this SLO (they are the same numbers as for availability; explain why).
2. **Apply.** Write the SLI definitions for a payout service (Phase 11) as three SQL-like predicates over a `payout_attempts` table with columns `status`, `failure_reason`, `latency_ms`, `amount_minor`, `reconciled_amount_minor`. Decide explicitly which `failure_reason` values are the service's fault and which are the beneficiary bank's, and write one sentence defending each exclusion.
3. **Extend.** Using the histogram from Phase 9, Lesson 06, show that "fraction of requests under 800 ms" can be computed exactly from a bucketed histogram if 800 ms is a bucket boundary and only approximately otherwise. Then compute, for a histogram with 2% precision, the maximum error in the latency SLI that bucket boundaries can introduce, and decide whether that error is acceptable against a 1% budget.

## Key Terms

| Term | What people say | What it actually means |
|------|-----------------|------------------------|
| SLI | "the metric" | A ratio of good events to valid events over a window, with both "good" and "valid" defined precisely enough to compute from logs without judgement |
| SLO | "the target" | An internal objective for an SLI over a window; breached before any contract is, so the team can act first |
| SLA | "the SLO" | A customer contract with consequences; deliberately looser than the SLO it is backed by |
| Error budget | "how much we can break" | `1 − SLO` over the window, in events or minutes; the quantity alerts and launch decisions are expressed in |
| Burn rate | "how fast we're failing" | Observed error ratio divided by the sustainable error ratio `1 − SLO`; burn rate 1 spends the budget exactly at the end of the window |
| Multi-window alert | "the two-threshold page" | Fire when both a long window (is it big enough to matter?) and a short window (is it still happening?) exceed the same burn rate |
| Error budget policy | "the freeze rule" | The pre-agreed consequence of exhausting the budget, usually a halt on feature launches until the window recovers |
| Correctness SLI | "reconciliation" | For money movement, the fraction of transactions whose recorded amount, currency, and counterparty match the external record; slow but non-negotiable |

## Further Reading

- [Google SRE Workbook, "Implementing SLOs"](https://sre.google/workbook/implementing-slos/) — the SLI specification shape, choosing targets, and the worked error-budget arithmetic this lesson follows.
- [Google SRE Workbook, "Alerting on SLOs"](https://sre.google/workbook/alerting-on-slos/) — the six iterations from a raw threshold to multi-window multi-burn-rate alerts, with the 14.4 / 6 / 1 derivation.
- [Google SRE Book, "Service Level Objectives"](https://sre.google/sre-book/service-level-objectives/) — the original SLI/SLO/SLA distinction and the argument for not over-achieving your SLO.
- [Google SRE Book, "Embracing Risk"](https://sre.google/sre-book/embracing-risk/) — why 100% is the wrong target and how error budgets resolve the product-versus-reliability negotiation.
- [Alex Hidalgo, *Implementing Service Level Objectives* (O'Reilly, 2020)](https://www.oreilly.com/library/view/implementing-service-level/9781492076803/) — the book-length treatment, including SLOs for data pipelines and correctness, which payments systems need.
- [PCI Security Standards Council, PCI DSS](https://www.pcisecuritystandards.org/document_library/) — requirement 10 on logging; the reason an authorization service already has the per-request records an SLI needs.
