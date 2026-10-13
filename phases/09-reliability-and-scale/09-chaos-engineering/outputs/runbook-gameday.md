---
name: runbook-gameday
description: Plan, pre-flight checklist, roles, and report skeleton for a chaos GameDay on a payment flow
phase: 9
lesson: 9
tags: [chaos, gameday, fault-injection, resilience, runbook]
---

# GameDay Runbook

Fill in every section at least one week before the GameDay. An experiment without a written hypothesis and abort condition does not run.

## 1. Scope

- **System under test:** (service, environment, build or version)
- **Steady state** (numbers, with the dashboard that shows them):
  - good-rate SLI >= ____ % over ____ minutes
  - p99 latency <= ____ ms over ____ minutes
  - any correctness invariant to watch (for example: authorizations per payment <= 1, ledger balanced)
- **Out of scope:** (dependencies, tenants, or regions that must not be touched)

## 2. Roles

| Role | Name | Responsibility |
|------|------|----------------|
| Incident commander | | Owns the go/no-go and the abort; the only person who says "inject" and "clear" |
| Injector | | Operates the tooling; confirms blast radius before arming; confirms clear after |
| Observer | | Watches the steady-state dashboard and calls out every checkpoint |
| Scribe | | Timeline with timestamps: arm, each checkpoint, abort or complete, clear, recovery |
| On-call | | Treats everything as a real incident; follows runbooks; does not know the schedule in detail |

## 3. Experiment table

| # | Hypothesis (mechanism + measurable result) | Fault | Target | Blast radius | Abort condition | Duration | Owner |
|---|--------------------------------------------|-------|--------|--------------|-----------------|----------|-------|
| 1 | | Latency ___ ms | | ___ % of requests / tenants ___ | good rate < ___ % at any ___-s checkpoint | | |
| 2 | | Error ___ | | | | | |
| 3 | | Partition | | | | | |
| 4 | | Clock skew ___ | | | | | |

Order experiments from smallest blast radius to largest, and from most confident hypothesis to least.

## 4. Pre-flight checklist (the morning of)

- [ ] Steady state currently holds and has held for the previous 24 hours.
- [ ] No deploys, migrations, or vendor maintenance windows overlap the GameDay.
- [ ] Error budget remaining is at least twice what the worst-case experiment could consume.
- [ ] The abort path has been exercised today: arm a no-op fault, clear it, confirm the injector reports disarmed.
- [ ] The abort path does not depend on anything an experiment can break (no abort through the mesh you are partitioning).
- [ ] Canary tenants have been told the window and have a contact.
- [ ] Support and the on-call for every downstream dependency know the window.
- [ ] Every hypothesis in the table is a falsifiable sentence, not "it will be fine".

## 5. Per-experiment protocol

1. Observer confirms steady state on the dashboard; scribe records the baseline numbers.
2. Commander says "inject"; injector arms the fault with the agreed blast radius and reads it back.
3. Observer calls out the checkpoint metrics at the agreed interval.
4. If the abort condition triggers, the commander says "clear" immediately; nobody debates the hypothesis first.
5. At the planned duration, commander says "clear"; injector confirms disarmed.
6. Observer confirms recovery to steady state; scribe records how long recovery took.
7. Team records the verdict: held, violated, or aborted, with one sentence on why.

## 6. Report skeleton (one per experiment)

```text
Experiment:     <name>
Hypothesis:     <sentence>
Fault:          <fault> on <target>, blast radius <radius>
Baseline:       good=__% p99=__ms n=__
Experimental:   good=__% p99=__ms n=__   (aborted after __ if applicable)
Recovery:       good=__% p99=__ms, reached steady state after __ s
Invariant:      <held / violated, with evidence>
Verdict:        HELD | VIOLATED | ABORTED | NOT RUN
Finding:        <what the system did that we did not expect>
Action item:    <change, owner, due date>  (every VIOLATED or ABORTED verdict has at least one)
```

## 7. After the GameDay

- [ ] Every violated or aborted experiment has an action item in the tracker with an owner.
- [ ] Runbook gaps the on-call hit are filed as action items too.
- [ ] Verdicts that held are recorded as evidence with the build version; re-run them after the next major change.
- [ ] The filled report is attached to the postmortem template (Phase 9, Lesson 12) and shared with the dependency teams that were in scope.
