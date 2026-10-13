---
name: runbook-brownout
description: Operator runbook for a payment service with brownout levels — what each level sheds and degrades, the signals to watch, how to pin and restore levels during an incident, and what to record afterwards
phase: 9
lesson: 13
tags: [load-shedding, brownout, runbook, overload, incident]
---

# Brownout Runbook

For the on-call engineer when the service is overloaded or the brownout controller has changed level. Fill in the level table for your own service before you need it.

## Level table

| Level | Sheds at the door | Degrades | Client sees | Business cost |
|-------|-------------------|----------|-------------|---------------|
| `NORMAL` | nothing | nothing | normal | none |
| `SHED_LOW` | reports, exports, analytics | fraud scoring: full model -> rules only | `503 Retry-After: 30` on LOW endpoints | reports delayed; fraud precision reduced (agreed with risk team: ticket RISK-nnn) |
| `SHED_NORMAL` | plus balance reads, status polls | as above | `503` on NORMAL endpoints; dashboards stale | merchants see stale balances; support volume rises |
| `CRITICAL_ONLY` | plus refunds, webhooks | as above | `503` on refunds; webhooks delayed | refunds queue up for later; webhook SLA at risk |

Authorizations and captures are never shed by the controller. If they are failing, this runbook does not apply; go to the capacity and failover runbooks.

## Signals

Dashboard: `<link>`. Panels, in the order to read them:

1. **Current brownout level** and time at each level today (`millisAtLevel`). A level above `NORMAL` for more than two minutes is an incident.
2. **CRITICAL/HIGH sojourn, window max** (the controller's input). Above target (5 ms) means escalation is imminent or happening.
3. **Shed at door per class per second** and **dropped in queue per class per second**. Door shedding should dominate; in-queue drops above a few per second mean the controller is lagging.
4. **Offered rate per class** and **per merchant** (top 10). Overload from one merchant is a rate-limit problem (Lesson 05), not a brownout problem.
5. **Fraud mode flag** and **rules-only approval rate** versus the model's. The risk team watches this one too.
6. **Downstream latency** (processor, ledger). Overload caused by a slow dependency does not go away when you shed; it needs the dependency's runbook.

## Decision procedure

1. Confirm the level change is real: panels 1 and 2 agree, and the offered rate (panel 4) rose or a dependency slowed (panel 6).
2. Identify the source. One merchant or integration driving the load: apply or lower its rate limit; the controller should return to `NORMAL` within a few windows. Broad increase: proceed.
3. If the level is `SHED_LOW` or `SHED_NORMAL` and CRITICAL p99 is within SLO: do nothing. The system is working as designed. Post in the incident channel with the level and the expected duration.
4. If the level is `CRITICAL_ONLY` or CRITICAL p99 is breaching SLO: page the second responder, open an incident (Lesson 12), and consider pinning (below) and adding capacity.
5. If the level is oscillating (changing more than once a minute): pin it at the higher level until the load has clearly passed, then restore in stages.

## Manual controls

- **Pin a level:** `<command or flag>` sets the level and disables automatic de-escalation. Use when oscillating or when you know the load will persist (month-end, a planned bulk job). Record who pinned and why in the incident channel.
- **Force a feature flag:** `fraud.mode=rules_only` independently of level, when the fraud service itself is the slow dependency. Requires risk-team acknowledgement within 15 minutes.
- **Unpin and restore:** restore in stages, one lever per step, waiting at least the cooldown between steps: fraud model first, then admit NORMAL, then LOW. Watch panel 2 after each step. If sojourn climbs above target, step back and wait.

Never restore everything at once after a long brownout: the shed clients have been retrying with backoff and will arrive together.

## During the incident

- Tell affected merchants what is shed and for how long, using the client-facing wording agreed in advance; do not describe internal levels.
- Keep the shed classes' `Retry-After` honest. If the brownout will last ten minutes, say 60 seconds, not 1.
- Watch for retry storms: a rising offered rate on a shed class while its success rate is zero means clients are not honouring `Retry-After`. Identify them and rate-limit them.

## After the incident

Record in the postmortem (Lesson 12 template):

- Time at each level, and the wall-clock span of the brownout.
- Requests shed per class and estimated business impact (delayed reports, stale dashboards, refund backlog).
- Fraud rules-only duration and the risk team's assessment of exposure (approved transactions later charged back).
- Whether the controller oscillated, and the `cooldown`, `target`, and `window` values in force.
- The source of the overload and whether a rate limit or capacity change would have prevented the brownout entirely.
- Any manual pin or flag change, by whom, and whether the staged restore was followed.

Review the level table and thresholds against this data within one week; tuning decisions made during the incident are provisional.
