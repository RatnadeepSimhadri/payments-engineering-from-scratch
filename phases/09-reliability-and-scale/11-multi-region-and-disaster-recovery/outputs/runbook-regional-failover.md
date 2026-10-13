---
name: runbook-regional-failover
description: Template runbook for failing a payments platform over to another region — decision criteria, ordered steps with owners, verification, communication, and the planned fail-back
phase: 9
lesson: 11
tags: [disaster-recovery, failover, multi-region, runbook, ledger]
---

# Regional Failover Runbook (template)

Fill in every `<...>` before this document is needed. Review it after every component failover and every game day. Keep a copy outside the primary region.

## Scope

- Primary region: `<region>`. Standby / pair region: `<region>`. Jurisdiction constraints: `<e.g. EU data stays in eu-west-1 / eu-central-1>`.
- Objectives: RPO `<0 postings for acknowledged writes>`; RTO `<15 minutes>` for merchants homed in the failed region; `<0>` impact for other regions.
- Decision owner: `<role, e.g. Incident Commander on the payments rotation>`. Deputy: `<role>`.

## 1. Detect

Automated. No human action required.

- [ ] Probes from `<2+ vantage points outside the region>` fail for `<30 s>`.
- [ ] Directory marks region `UNHEALTHY`; edges in all regions stop forwarding to it; writes for accounts homed there fail fast with `503 Retry-After: <60>`.
- [ ] On-call paged with: region, merchant count homed there, current replication state of the pair, provider status link.

## 2. Confirm (the only manual decision)

Owner: decision owner. Time budget: `<7 minutes>` from page.

Declare a regional failover only when all of:

- [ ] Two independent signals agree (e.g. external probes and the directory's own peer health), not one monitoring system.
- [ ] Provider status page or support confirms a regional event, or the region has been unreachable for `<5 minutes>` with no sign of recovery.
- [ ] The standby region is healthy: its ledger partition is current (`<replication lag metric>` is `<0 / under threshold>`), its application capacity is ready, its processor adapters report credentialed.

If any check fails, do not fail over. Hold, communicate, and re-evaluate every `<5 minutes>`. A partition that leaves the primary alive and writing is more dangerous than a longer outage.

Record: time of decision, decider, the two signals, replication lag at decision time.

## 3. Execute

Owner: `<role>`. Each step has an expected duration; if a step exceeds twice its expectation, escalate rather than improvise.

| # | Step | Command / location | Expected | Owner |
|---|------|--------------------|----------|-------|
| 1 | Freeze deploys platform-wide | `<link>` | 1 min | IC |
| 2 | Re-home all accounts from primary to standby in the directory | `<command>`; single batch commit | 1 min | `<role>` |
| 3 | Promote standby ledger partition to primary | `<command>`; confirm it reports `PRIMARY` and lag `0` | 2 min | `<role>` |
| 4 | Scale standby application capacity to 100% | `<command>` | 2 min | `<role>` |
| 5 | Confirm processor adapters in standby are live (test authorisation to each acquirer) | `<link>` | 2 min | `<role>` |
| 6 | Confirm edges route all traffic for re-homed accounts to standby | `<dashboard>` | 1 min | `<role>` |
| 7 | Unpause reconciliation and settlement jobs in standby | `<command>` | 1 min | `<role>` |

Do not: restart the old primary's ledger, re-point anything back, or replay WAL manually. If the old region reappears mid-procedure, it must come back as a replica (section 6), never as a writer.

## 4. Verify

- [ ] Synthetic authorisation, capture, refund, and payout for `<test merchant homed in standby>` succeed end to end.
- [ ] Error rate and p99 latency for re-homed merchants within SLO on `<dashboard>`.
- [ ] Directory shows zero accounts homed in the failed region.
- [ ] Reconciliation job's first run after failover reports `<0>` breaks attributable to lost postings (if non-zero, the RPO was not what was quoted: record the count).
- [ ] Declare recovered; record the RTO achieved.

## 5. Communicate

- Internal: incident channel update at decision, after step 3, after verification. Use the Lesson 12 incident roles.
- Merchants: status page update at decision ("degraded for some merchants, failing over") and at recovery. Do not name internal regions or levels.
- Partners: acquirers and networks notified if egress IPs or volumes shift materially; webhook consumers warned of a source change if applicable.
- Regulators: `<jurisdiction-specific notification requirements and deadlines>`.

## 6. Fail back (planned, never in a hurry)

- [ ] When the old region returns, it rejoins as a *replica* of the new primary and catches up. Confirm lag reaches `0`.
- [ ] Leave it as replica for at least `<one week>`; run a full reconciliation cycle in the new topology.
- [ ] Schedule the fail-back as a routine re-homing: off-peak window, `<batch size>` accounts per batch, verification between batches, same procedure as the weekly component failover drill.
- [ ] Update this runbook with everything that was slower, missing, or surprising.

## Dependency inventory

Every dependency the steps above touch, with where it lives. Any row marked "primary only" is an RTO risk to fix before the next game day.

| Dependency | Location | Workaround if primary is down |
|------------|----------|-------------------------------|
| Home directory | multi-region consensus (`<regions>`) | none needed |
| DNS / traffic manager control plane | `<...>` | `<...>` |
| Secrets (processor credentials, signing keys) | `<...>` | `<...>` |
| Configuration and feature flags | `<...>` | `<...>` |
| Engineer SSO and tooling access | `<...>` | `<...>` |
| CI / deploy pipeline | `<...>` | `<...>` |
| Dashboards, alerting, log search | `<...>` | `<...>` |
| Acquirer allow-lists for standby egress | `<...>` | `<...>` |
| This runbook | `<printed copy / doc store outside region>` | |

## Drill log

| Date | Type (tabletop / component / game day) | Accounts moved | RTO measured | Findings |
|------|----------------------------------------|----------------|--------------|----------|
| | | | | |
