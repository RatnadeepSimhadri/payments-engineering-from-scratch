---
name: runbook-membership-flapping
description: On-call runbook for a gossip-based cluster that is marking healthy nodes suspect or dead (membership flapping), with SWIM and memberlist parameter names side by side
phase: 5
lesson: 13
tags: [gossip, swim, failure-detection, runbook, consul, serf]
---

# Runbook: Membership Flapping in a Gossip Cluster

Symptoms: nodes alternate between alive, suspect, and dead in the membership log; work (shards, buckets, leases) migrates repeatedly; no process has actually crashed.

## 1. Confirm it is a false positive

- [ ] For each node reported dead, check from a third machine that the process is up and answering its health endpoint.
- [ ] Note **who** accused it. One accuser pointing at many targets is a slow prober. Many accusers pointing at one target is a slow or isolated target.
- [ ] Check whether the accused node's incarnation number is climbing (memberlist: `Incarnation` in the member list output). A climbing incarnation means it is hearing the accusations and refuting them, so dissemination works and the problem is timing.

## 2. Slow prober (one node accuses many)

Cause: the accusing node's probe loop is late (GC pause, CPU starvation, blocked event loop), so its ack timeout fires before the ack had a chance to arrive.

- [ ] Look at the accuser's GC and CPU metrics for pauses longer than the ack timeout (`ProbeTimeout` in memberlist; `ackTimeout` in the lesson).
- [ ] If the implementation has Lifeguard self-awareness (memberlist does), confirm it is enabled; the accuser should be lengthening its own timeouts.
- [ ] Short-term: drain traffic from the accuser. Do not shorten anyone's suspicion timeout.
- [ ] Long-term: move the gossip loop off the request-serving threads or process.

## 3. Slow or isolated target (many nodes accuse one)

Cause: the target's network path is degraded, or the target itself is paused.

- [ ] Check whether indirect probes are enabled and how many (`IndirectChecks` in memberlist; `indirectProbes` in the lesson). Fewer than 3 makes a single bad link look like a dead node.
- [ ] Check the target's uplink and switch for loss or latency. A partial partition (one rack cannot reach the target, others can) shows up as accusations from exactly one rack.
- [ ] Check the target's own GC and CPU. A paused target cannot ack anyone, and indirect probes do not help.

## 4. Knobs, and what they trade

| Lesson name | memberlist name | Raising it | Lowering it |
|-------------|-----------------|------------|-------------|
| `protocolPeriod` | `ProbeInterval` | fewer false positives, slower detection, less load | the opposite; must stay well above the max RTT plus the indirect round trip |
| `ackTimeout` | `ProbeTimeout` | fewer unnecessary indirect probes | more indirect probes; must stay above the p99 RTT |
| `indirectProbes` | `IndirectChecks` | tolerance to more bad paths, slightly more load on silence | a single bad link becomes a false positive |
| `suspicionPeriods` | `SuspicionMult` | more time to refute, slower confirmation of real crashes | faster eviction, more false evictions of slow nodes |
| `retransmitFactor` | `RetransmitMult` | faster, more reliable dissemination; more bytes per message | updates may fail to reach some members under loss |

Rule: a flapping cluster is fixed by raising `suspicionPeriods` or `indirectProbes`, never by lowering `protocolPeriod`.

## 5. After the incident

- [ ] Record detection time (crash to first suspicion), confirmation time (suspicion to dead), and dissemination time (dead at one node to dead everywhere) from the logs, in protocol periods. Compare with the lesson's simulation at the same N.
- [ ] If work placement depends on the membership view, confirm it is protected by a lease or fencing token (Phase 5, Lesson 12). Gossip tells you who is probably alive; it does not tell you who owns what.
- [ ] Add an alert on false-positive rate: deaths declared per hour for nodes whose health endpoint was answering at the time.
