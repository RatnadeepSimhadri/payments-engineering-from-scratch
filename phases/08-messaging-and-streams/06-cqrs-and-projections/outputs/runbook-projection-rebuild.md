---
name: runbook-projection-rebuild
description: Operational procedure for rebuilding a versioned read-model projection from the event log without downtime
phase: 8
lesson: 6
tags: [cqrs, projections, event-sourcing, rebuild, runbook, read-model]
---

# Projection Rebuild Runbook

Use when a projection's code changes (new column, new rule, bug fix) or when its rows are suspected wrong. The procedure never patches rows in place: a new version is built from position 0 beside the live one and promoted when it has caught up. Keep the old version until rollback is no longer plausible.

## Before you start

- [ ] The change is a new **version** of the projection (`<name>@v<N+1>`), with its own checkpoint key and its own tables, indexes, or keyspace. The live version's storage is not touched.
- [ ] `apply` for the new version is idempotent: it ignores any event at or below the position it last applied to that row (or the view and checkpoint are updated in one transaction).
- [ ] Rebuild time is estimated: `log length / projector throughput`. Know whether this is minutes or hours before you begin. If hours, confirm the log's retention will not delete the head of the log during the rebuild.
- [ ] The downstream consumers of this view are listed, and the ones with read-your-own-writes expectations are known (they wait on a position and will see the new version only after promotion).
- [ ] Storage headroom exists for two full copies of the view.

## Procedure

1. **Deploy the new version in building mode.** It registers under `<name>` as *building*, not *live*. Confirm `checkpoint(<name>@v<N+1>) = 0` and that the live version is unchanged.
2. **Start the rebuild.** The projector reads from position 0 in batches. Record the start time and the log's current end position.
3. **Monitor lag.** Lag = `log end position - checkpoint`. It must fall monotonically once the projector is faster than the producer. If it plateaus or grows, the projector is slower than the producer; stop and address throughput (batch size, parallel lanes per stream, snapshot) before continuing.
4. **Spot-check correctness while building.** Pick three streams whose history you can fold by hand or with a script; compare the new version's rows for those streams at its current checkpoint. Do this before promotion, not after.
5. **Attempt promotion.** Promotion is refused while lag is non-zero; this is correct. Retry when lag reaches 0 (or below the agreed threshold for a continuously written log, typically "within one poll interval").
6. **Promote.** The pointer for `<name>` moves to `v<N+1>`. Queries now read the new version. The old version keeps its checkpoint and its storage; stop its projector but do not delete anything.
7. **Verify after promotion.** Compare a sample of query results between old and new versions; the differences should be exactly the intended ones (new column populated, new threshold applied, bug fixed). Any other difference is a defect in the new version: roll back.
8. **Rollback, if needed.** Move the pointer back to `v<N>` and restart its projector; it resumes from its own checkpoint and catches up in seconds. Nothing in the new version needs to be undone.
9. **Retire the old version.** After the agreed soak period (a business day for most views, a month-end close for finance views), delete the old version's storage and checkpoint. Record the retirement.

## Failure modes and responses

| Symptom | Likely cause | Response |
|---------|--------------|----------|
| Lag stops falling during rebuild | Projector throughput below producer rate | Increase batch size; parallelise per stream with per-key lanes and a low-water-mark checkpoint; add a snapshot source |
| Rows double-counted after a projector restart | `apply` not idempotent, checkpoint saved after apply | Fix the guard (per-row applied-through position), bump the version, rebuild |
| New version missing rows for early positions | Log retention deleted the head during the rebuild | Rebuild from a snapshot that predates the retention boundary; fix retention for logs that feed rebuildable views |
| Promotion succeeded but a client sees old data | Client cached the old version's rows, or waited on a position with the old projector | Expire client caches; read-your-own-writes must wait on the live projector after promotion |
| Query latency rises after promotion | New version lacks an index the old one had | Add the index to the new version's storage; roll back meanwhile |

## Record

For each rebuild, record: projection name, old and new version, log end position at start, start and promote times, who spot-checked which streams, rollback decision, retirement date. This record is what lets the next rebuild estimate its duration and what an auditor asks for when the view feeds a report.
