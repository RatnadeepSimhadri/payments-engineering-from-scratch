---
name: runbook-saga-dead-letters
description: On-call runbook for a dead-lettered payment saga whose compensation could not complete
phase: 12
lesson: 3
tags: [saga, orchestration, dead-letter, on-call, compensation]
---

# Runbook: Dead-Lettered Payment Saga

**Alert:** the dead-letter table is non-empty, or a dead letter is older than its SLA.
**Severity:** a dead letter is money or cardholder data left in an unknown state with a named owner. Treat each one as customer-impacting until proven otherwise.
**Golden rule:** never edit the saga record by hand. Every change goes through redrive or a recorded manual compensation, so the record stays the source of truth.

## 1. Triage (first 10 minutes)

1. Count dead letters and group them by `step` and by the first words of `reason`. Many sagas on one step and one reason is a downstream incident; a single saga is usually a model error.
2. Check the downstream's status page and your own error-rate dashboard for that step's system (vault, acquirer, ledger).
3. If one downstream is down, open or join that incident, and do not redrive until it is healthy. Redriving into an outage only re-dead-letters the sagas.
4. Note the oldest dead letter's age against the deadlines below.

| Step whose compensation failed | Compensation | Hard deadline |
|--------------------------------|--------------|---------------|
| `TOKENIZE` | delete token | Vault retention policy (PCI finding if exceeded) |
| `AUTHORIZE` | void authorization | Before the hold drops off (typically 7 days), and before the customer calls their bank |
| `CAPTURE` | refund | Before the cardholder's dispute window becomes the refund path |
| `LEDGER_POST` | reversal entry | Before the day's ledger close and reconciliation run |

## 2. Read the record

For one saga, load the record and write down:

- [ ] Saga id, payment id, amount and currency.
- [ ] Status of each step (`PENDING`, `DONE`, `COMPENSATED`) and its downstream reference.
- [ ] Forward and compensation attempt counts per step.
- [ ] `failureReason`: the original forward failure that started compensation.
- [ ] The dead letter's `reason`: why the compensation itself failed.

Classify the compensation failure:

| Compensation failure | Meaning | Go to |
|----------------------|---------|-------|
| Unavailable past budget (timeouts, 5xx) | Downstream was down; the model is probably right | Section 3 (redrive) |
| Declined (`already_captured`, `not_found`, `already_refunded`) | Our model of the downstream is wrong | Section 4 (investigate) |

## 3. Redrive

1. Confirm the downstream is healthy (status page green, error rate back to baseline for 15 minutes).
2. Redrive one saga first. Redrive resets compensation counters and resumes from the record; completed compensations are not repeated.
3. Confirm it reached `COMPENSATED` and that the downstream shows the effect (void, refund, delete, or reversal) under the expected idempotency key `<sagaId>:<step>:comp`.
4. Redrive the rest in batches small enough not to trip the downstream's rate limits.
5. Any saga that dead-letters again on redrive moves to Section 4.

## 4. Investigate a declined compensation

Query the downstream directly by the step's reference and the compensation key, then decide:

- [ ] **Effect already happened** (for example the auth was captured and the capture already refunded): the compensation is a semantic no-op. Record it as a manual compensation with evidence, and file a bug, because the orchestrator should have known.
- [ ] **Effect is impossible** (void on a captured auth): perform the correct semantic compensation (refund the capture) through the normal tooling with an idempotency key, then record it.
- [ ] **Reference unknown downstream** (`not_found`): the forward step may never have committed. Check for side effects under the forward key before concluding nothing happened.

## 5. Verify the invariant

For every saga you resolve:

- [ ] The ledger nets to zero for the payment id.
- [ ] The acquirer shows no live authorization and no unrefunded capture for the payment.
- [ ] The vault holds no token for the payment.
- [ ] The saga is `COMPENSATED` and its dead letter is gone.

## 6. Customer and merchant

- [ ] If a hold was visible for more than a day, tell customer support which payment ids were affected and when the hold was released.
- [ ] If a refund was issued, give support the refund reference and expected arrival time.

## 7. Incident log

Record: alert time, count of sagas, step and reason grouping, root cause (downstream outage or model error), the action per saga (redrive or manual compensation with references), invariant checks, and any customer contact. A model error always produces a follow-up ticket to teach the orchestrator the missing semantic case.

## Do not

- Do not delete dead-letter rows to clear the alert.
- Do not call the downstream with a new idempotency key to "force" a compensation.
- Do not enable automatic redrive on restart to make the alert go away.
