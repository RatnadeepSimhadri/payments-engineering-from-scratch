---
name: interview-card-timestamp-ordered-transactions
description: One-page interview and design-review card on Percolator, Spanner, and CockroachDB transaction protocols, with the questions interviewers ask and the answers that distinguish levels
phase: 7
lesson: 9
tags: [percolator, spanner, cockroachdb, truetime, external-consistency, interview]
---

# Timestamp-Ordered Transactions: Interview Card

Use before a distributed-systems or payments-infrastructure interview, or when reviewing a design that proposes cross-shard transactions.

## The one-sentence versions

- **Percolator**: snapshot isolation over a sharded store with single-row atomicity, where the commit decision is a cell in the primary row and any reader can finish or undo a stalled transaction.
- **Spanner**: 2PC over Paxos groups, with a commit timestamp from a bounded-uncertainty clock and a commit wait that makes timestamp order equal real-time order (external consistency).
- **CockroachDB**: Spanner's shape on commodity clocks: hybrid logical clocks, uncertainty restarts for readers, read refresh for serializability, no commit wait by default.

## Questions you will be asked

| Question | Weak answer | Strong answer |
|----------|-------------|---------------|
| How does Percolator avoid the 2PC blocking problem? | "It has no coordinator." | The client is the coordinator but the *decision* is the primary cell's lock and write columns. A reader finding a stale secondary lock reads the primary: lock present and owner alive, wait; lock present and owner dead, roll back by erasing the primary lock; write record present, roll forward. Committer and cleaner cannot both win because both act on the primary row atomically. |
| What is the commit point in Percolator? | "When all cells are written." | The single row transaction on the primary that writes `write@commit_ts = start_ts` and erases `lock@start_ts`. Secondaries are cleanup. |
| What isolation does Percolator give? | "Serializable, it uses 2PC." | Snapshot isolation. Prewrite checks for writes committed after `start_ts` (first-committer-wins) but never validates the read set, so write skew is possible. |
| What is TrueTime? | "Google's accurate clock." | An API returning an interval `[earliest, latest]` guaranteed to contain true time, half-width epsilon of a few ms, from GPS and atomic clocks plus a drift model. The point is the bound, not the accuracy. |
| Why does Spanner commit-wait? | "To let replicas catch up." | To guarantee external consistency: after `TT.after(s)`, every machine's `TT.now().latest > s`, so any transaction that starts after the client is acknowledged gets a timestamp above `s` and sees the commit. Expected wait about 2 epsilon. |
| Define external consistency. | "Strong consistency." | If T1's commit is acknowledged before T2 starts in real time, then `ts(T1) < ts(T2)`. The database's order matches any observer's order. Strict serializability for transactions. |
| How does a Spanner read-only transaction avoid locks? | "It reads a snapshot." | It picks `s_read = TT.now().latest` and reads from any replica whose safe time (min of last applied Paxos timestamp and smallest outstanding prepare timestamp minus one) has passed `s_read`. Commit wait is what makes `s_read` guaranteed to include every acknowledged commit. |
| How does CockroachDB cope without TrueTime? | "It uses NTP and hopes." | HLC timestamps; `max_offset` (500 ms default) as the tolerated skew, nodes beyond it self-terminate; readers treat values in `(t, t + max_offset]` as uncertain and restart above them; read refresh validates the read set when a transaction's timestamp moves, giving serializable; parallel commits for one consensus round. |
| What does CockroachDB give up? | "Nothing, it is Spanner." | External consistency between causally unrelated transactions within `max_offset` (a causal reverse through an out-of-band channel). Linearizable mode restores it by commit-waiting `max_offset`. |
| Where would you use each in a payments system? | "Spanner everywhere." | Cross-shard transfers: any of the three. Reads that must see a commit learned of out-of-band: Spanner or CockroachDB linearizable. Reconciliation: any snapshot read. Idempotency: a unique constraint on a single shard, not a distributed transaction. Floor checks across rows: not plain Percolator. |

## Numbers to have ready

- Spanner epsilon: about 4 ms average, 6 to 7 ms bound in the 2012 paper; commit wait about 2 epsilon.
- CockroachDB `max_offset`: 500 ms default; a node exceeding it shuts down.
- Percolator's timestamp oracle: about 2 million timestamps per second with batching; a single process, so it is a single point of failure unless replicated (TiDB's Placement Driver).
- Classic 2PC: 2 round trips plus 2 forced log writes on the coordinator; participants block for the full coordinator outage.

## Design-review checklist for a proposal that says "we will do cross-shard transactions"

- [ ] Where does the commit decision live, and can a second client or a new leader read it after the committer dies?
- [ ] What isolation level results, in anomaly terms (dirty read, lost update, write skew, phantom), not in vendor names?
- [ ] Which clock orders commits, what is its error bound, and what happens to a node whose clock exceeds it?
- [ ] Does a reader who learned of a commit out of band see it? If not, is that acceptable for fraud, limits, and customer-facing balances?
- [ ] What fraction of transactions are expected to be single-shard, and does the key design (account id as the shard key, postings co-located with balances) make that true?
- [ ] What is the cleanup story for locks or intents left by crashed clients, and is it bounded?
