---
name: template-postmortem
description: Blameless postmortem template for a payments incident, with a mandatory financial reconciliation section, contributing factors instead of a root cause, and tracked actions
phase: 9
lesson: 12
tags: [incident, postmortem, blameless, runbook, reconciliation]
---

# Postmortem: `<incident id>` — `<one-line title, impact first>`

| | |
|-|-|
| Severity | `<SEV1 / SEV2>` |
| Impact window | `<start UTC>` to `<end UTC>` (`<N>` minutes) |
| Response window | `<alert UTC>` to `<closed UTC>` |
| Incident Commander | `<role or name>` |
| Postmortem owner | `<name>` |
| Review meeting | `<date, within 5 business days for SEV1>` |
| Status | Draft / Reviewed / Actions complete |

This document is blameless. It describes what the system and the process did, and what will change. It does not name a person as a cause. If you find yourself writing "X should have", rewrite it as the condition that made X's action reasonable at the time.

## 1. Summary

Three sentences. What customers experienced, for how long, and what was done. Written for someone who reads nothing else.

## 2. Impact

- Merchants affected: `<count, and which classes>`
- Transactions affected: `<count declined / delayed / duplicated>`, estimated value `<amount minor units + currency>`
- Cardholders affected: `<count, and how: pending holds, duplicate charges>`
- SLO budget consumed: `<fraction of the 30-day error budget for each affected SLI>` (Lesson 01)
- Partner impact: `<acquirers, networks, banks notified; any contractual window>`
- Regulatory: `<notifiable? which rule, which deadline, notified when>`

## 3. Financial reconciliation (mandatory for payments incidents)

Status: `<open / complete on date>`. The incident is not closed until this section is complete.

| Question | Answer | Evidence |
|----------|--------|----------|
| Did any capture or payout happen on the platform but not at the counterparty? | | reconciliation job run id |
| Did any happen at the counterparty but not on the platform? | | |
| Were any amounts wrong (rounding, currency, duplicates)? | | |
| Are there orphaned holds or reservations? | | |
| Does the ledger balance (every transaction sums to zero)? | | |
| Remediation performed (reversals, re-postings, merchant credits) | | tickets |

## 4. Timeline

UTC. One line per observation, decision, action, or communication, with links. Pasted from the incident channel and cleaned; do not reorder or omit entries that look embarrassing.

```text
HH:MM:SS  [alert]     ...
HH:MM:SS  [IC]        ...
HH:MM:SS  [obs]       ... (link)
HH:MM:SS  [decision]  ...
HH:MM:SS  [action]    ...
HH:MM:SS  [comms]     ...
```

## 5. Detection

- How was the incident first noticed: `<alert name / customer report / engineer>`
- Time from first impact to detection: `<N min>`
- Should an alert have fired earlier? `<yes/no, which SLI, why it did not>`
- Was there a leading signal (change event, dependency latency) that could have alerted before customer impact?

## 6. Response

What went well and what did not, about the response itself (roles, communication, decisions, tooling), kept separate from the cause.

- Time from detection to declaration: `<N min>`; roles filled at `<time>`
- Time from declaration to first effective mitigation: `<N min>`
- Mitigations attempted, in order, with effect: `<list>`
- Communication: status page first update at `<time>` (target 15 min); support briefed at `<time>`
- Handovers: `<none / who to whom when>`
- Runbook used: `<link>`; was it accurate? `<gaps found>`

## 7. Contributing factors

List every condition that was necessary for the incident to happen or to last as long as it did. Several, not one. Each is a property of a system, a process, a tool, or an organisational default. Group them if helpful: trigger, amplifiers, detection gaps, response gaps.

1. `<e.g. the config field 'timeout' has no unit in its name and accepts any positive integer>`
2. `<e.g. config changes apply to 100% of instances with no canary or validation>`
3. `<e.g. no alert fires on config change events>`
4. `<e.g. decline metrics do not distinguish issuer declines from our own timeouts>`
5. `<e.g. no IC was declared for the first 20 minutes>`
6. `<e.g. support had no channel from engineering>`

## 8. What went well

The controls, alerts, runbooks, traces, or decisions that worked. These are the things to keep and to copy.

## 9. Actions

Every action has one owner, a due date, a priority, and a ticket in the normal work tracker. Actions change systems or processes, never a person's attentiveness. Reviewed weekly until complete.

| # | Action | Addresses factor | Owner | Due | Priority | Ticket | Status |
|---|--------|------------------|-------|-----|----------|--------|--------|
| 1 | | | | | | | |
| 2 | | | | | | | |

## 10. Lessons for other teams

The generalisable finding, in one or two sentences, and where else it might apply. `<e.g. audit every numeric config field for a missing unit>`

## 11. Review meeting notes

Facilitator: `<someone who was not a responder>`. Attendees. Disagreements about contributing factors and how they were resolved. Actions added or changed in the meeting.

## Appendix

- Links: incident channel, dashboards, traces (`trace_id`), log queries (`payment_id`), change log entries, status page history.
- Glossary of internal names used above, for readers outside the team.
