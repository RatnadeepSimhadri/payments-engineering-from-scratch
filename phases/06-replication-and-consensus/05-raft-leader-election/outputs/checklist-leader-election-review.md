---
name: checklist-leader-election-review
description: Design-review checklist for any service that elects, promotes, or fails over a primary, with the failure each item prevents
phase: 6
lesson: 5
tags: [raft, leader-election, failover, consensus, review]
---

# Leader Election and Failover Review Checklist

Use on any design document, pull request, or incident review involving a primary, a leader, a lock holder, or a failover. It applies whether the mechanism is Raft, a lease in a database, a ZooKeeper/etcd lock, or a homegrown sidecar. Each item names the failure it prevents. One unchecked box is a reason to request changes.

## Identity of the leader

- [ ] "Leader" is a property the cluster agrees on, not one each node computes from its own timeouts. Prevents: two primaries after a partition that looks like a crash from the minority side.
- [ ] Every leadership claim carries a monotonically increasing term (epoch, fencing token, generation). Prevents: a stale leader's writes being accepted after a newer leader exists.
- [ ] Every node adopts a higher term it sees in any message and steps down if it was leader or candidate. Prevents: an old leader continuing to serve after rejoining.
- [ ] Every node rejects any request carrying a lower term than its own, and the rejection includes its current term. Prevents: stale candidates and leaders never learning they are behind.
- [ ] Downstream systems that receive writes from the leader (storage, queues, other services) check the term and refuse lower ones. Prevents: the "two primaries both wrote" outcome even when election itself was correct.

## Votes and majorities

- [ ] A node grants at most one vote per term, and a repeat request from the same candidate is treated as the same vote. Prevents: two candidates each assembling a majority.
- [ ] The majority is computed over the configured membership, not over the nodes currently reachable. Prevents: a minority partition electing its own leader.
- [ ] Membership changes go through a protocol that keeps majorities overlapping (joint consensus or single-server changes); a node never just edits its peer list. Prevents: two disjoint majorities during a reconfiguration.
- [ ] A candidate's log must be at least as up-to-date as the voter's before the vote is granted. Prevents: electing a leader that lacks committed entries.
- [ ] Cluster size is odd (3 or 5) unless there is a written reason. Prevents: 2-2 split votes and the extra unavailability they cause.

## Persistence

- [ ] `currentTerm` and `votedFor` are written to stable storage, with the write completed (fsync or equivalent) before the vote reply is sent. Prevents: a double vote after a crash between reply and write.
- [ ] A restart restores term and vote from storage before the node handles any message. Prevents: a restarted node joining at term 0 and granting votes it already spent.
- [ ] Tests include a crash between "decide" and "persist" and show the system either refuses the action or recovers it. Prevents: the ordering being right in the design and wrong in the code.

## Timing

- [ ] Election timeouts are randomized per node over an interval and redrawn on every reset. Prevents: repeated split votes between nodes that collided once.
- [ ] Heartbeat interval is well below the minimum election timeout (ratio of at least 3, with message latency included in the budget). Prevents: spurious elections on a healthy cluster.
- [ ] The election timeout is well below the mean time between failures. Prevents: a cluster that spends more time electing than serving.
- [ ] Timers use a monotonic clock; wall-clock jumps, VM pauses, and GC pauses are considered in the timeout budget. Prevents: a leader that appears dead because its process was paused, then returns and conflicts.
- [ ] A leader that cannot reach a majority within an election timeout steps down (CheckQuorum or lease expiry). Prevents: a partitioned leader serving stale reads indefinitely.
- [ ] Rejoining nodes do not disrupt a healthy leader (Pre-Vote or equivalent). Prevents: one flapping node forcing an election every time it reconnects.

## Testing and operations

- [ ] The election logic is testable without threads, sockets, or sleeps: time, randomness, and the network are injected. Prevents: nondeterministic tests and unreproducible incidents.
- [ ] A safety check runs after every simulated delivery, not only at the end of a run: at most one leader per term. Prevents: violations that are repaired before the final assertion and therefore never seen.
- [ ] The test suite includes: partition with the leader in the minority, heal, leader crash and restart, message loss of at least 20%, and many seeds. Prevents: passing tests that never exercised the failure path.
- [ ] Any failing seed is kept as a permanent regression test. Prevents: the same race reappearing after a refactor.
- [ ] Metrics exist for term changes per hour, election duration, and time without a leader; an alert fires on elections that are not explained by a deploy or a known failure. Prevents: silent flapping discovered only through duplicate writes.
- [ ] The runbook states what clients experience during an election (writes rejected or retried, not accepted and lost) and how long that window is expected to be. Prevents: "the system was down" being the only post-incident description available.
