---
name: runbook-raft-membership-change
description: Operator runbook for adding, removing, or replacing servers in a production Raft cluster (etcd, HashiCorp Raft, Consul) and for recovering a cluster stuck mid-change or without quorum
phase: 6
lesson: 7
tags: [raft, membership, joint-consensus, snapshots, runbook, etcd, consul]
---

# Runbook: Changing Raft Cluster Membership

Vocabulary: C_old is the current configuration, C_new the target, C_old,new the joint configuration. etcd: `member add`, `member remove`, `member promote`, learners. HashiCorp Raft / Consul: `AddVoter`, `AddNonvoter`, `RemoveServer`, `consul operator raft`.

## Preconditions (every change)

- [ ] The cluster has a stable leader and a quorum of healthy voters. Never start a change while a server is already down unless the change is removing that server.
- [ ] No other membership change is in flight. etcd: no learner awaiting promotion, no `ConfChangeV2` pending. The lesson's leader refuses a change while the latest configuration is joint or uncommitted; your library may not.
- [ ] You know the quorum size before and after. 3 -> 4 raises the quorum from 2 to 3 with no extra fault tolerance; prefer odd sizes.
- [ ] A recent snapshot exists on the leader so a new server can catch up by snapshot, not by log replay. etcd: check `--snapshot-count` and the current log size.

## Adding a server

1. Start the new server with an **empty** data directory and the cluster's existing peer list. It must not be able to form a cluster by itself (etcd: `--initial-cluster-state existing`).
2. Add it as a **non-voter / learner** first (`etcdctl member add --learner`, `AddNonvoter`). It receives the log and snapshot but is not counted in quorums.
3. Wait until its match index is within a few entries of the leader's. etcd: `endpoint status` shows raft index per member; the gap should be close to zero.
4. Promote it to a voter (`member promote`, `AddVoter`). This is the configuration change. Watch for the leader to commit the new configuration (one new entry, then the next heartbeat).
5. Confirm every member reports the same member list and the same leader.

Why learner first: in the lesson's joint phase a new voter with an empty log is counted in C_new's quorum immediately, so a slow one delays commits for the whole cluster.

## Removing a server

1. If the server to remove is the leader, prefer to transfer leadership first (`etcdctl move-leader`, `LeadershipTransfer`). Raft can remove its own leader (it commits C_new without counting itself, then steps down), but the election that follows is a short unavailability you can avoid.
2. Issue the removal (`member remove`, `RemoveServer`). Watch for the configuration entry to commit.
3. Only then stop the process and delete its data directory. A removed server left running with its old configuration can disrupt the cluster with elections unless the library has pre-vote or leader stickiness.
4. Confirm the remaining members agree on the new list.

## Replacing a server (the common case)

Add the new one first (steps above), then remove the old one. The intermediate configuration has one more voter than usual; its quorum is larger, not smaller, so it tolerates the same number of failures. If your library supports joint consensus for two-server changes (etcd `ConfChangeV2`, CockroachDB), a single replace operation goes through C_old,new with the quorum requirement unchanged throughout.

## Stuck in a joint configuration

Symptom: the member list shows both old and new sets; new changes are refused.

- Cause: the leader that started the change failed before appending C_new, and the library did not auto-complete. etcd auto-leaves joint when `EnterJoint(autoLeave=true)` was used.
- Fix: issue the leave-joint operation on the current leader (etcd: an empty `ConfChangeV2`). The lesson's `afterCommit` does this automatically when it sees a committed joint configuration.
- Do not restart servers with an edited configuration file. That is the direct switch this lesson warns about.

## Lost quorum

Symptom: no leader for longer than several election timeouts; writes fail; the majority of voters are gone.

- This cannot be fixed by a membership change, because membership changes are themselves log entries that need a quorum.
- Recovery is a **manual override** that rewrites the configuration from a backup or a surviving server's data (etcd disaster recovery: restore from snapshot with `--force-new-cluster`; HashiCorp: `peers.json` recovery). It discards any entry not on the surviving server, so it can lose committed writes. Treat it as data loss and reconcile afterwards (Phase 13 reconciliation lessons).

## What to watch during any change

| Signal | Healthy | Investigate |
|--------|---------|-------------|
| Leader term | unchanged | increments: an election happened; find the disruptive server |
| Commit index | advancing | flat: the joint quorum cannot be formed (new servers not caught up?) |
| Match index of new server | approaching leader's | flat: snapshot transfer failing or too slow |
| Member list per server | identical on all | differs: a server has not received the configuration entry |
| Snapshot sends | a handful | continuous: a follower cannot install (disk full, version mismatch) |
