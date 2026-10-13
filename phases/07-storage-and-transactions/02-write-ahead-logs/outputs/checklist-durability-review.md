---
name: checklist-durability-review
description: Pull-request review checklist for any code that claims to persist state, covering the three stages of a write, record framing, torn-versus-corrupt recovery, write-ahead order, and checkpoint crash windows
phase: 7
lesson: 2
tags: [durability, wal, fsync, crash-recovery, review]
---

# Durability Review Checklist

Use on every pull request that adds or changes a file write, a log, a snapshot, or recovery code in a service that handles money. If the author cannot answer "what does a caller see if power fails on this line?", the change is not ready.

## 1. The three stages of a write

- [ ] Every claim of "saved" or "committed" corresponds to a device acknowledgement: `FileChannel.force(true)`, `os.fsync(fd)` after `flush()`, or `(*os.File).Sync()`. A returned `write()` or `flush()` is not durable.
- [ ] No buffered writer sits between the log and the sync without an explicit flush before it.
- [ ] Creating, renaming, or deleting a file is followed by an `fsync` on the parent **directory**, not only the file.
- [ ] The sync policy is stated in the code or config: per record, group commit with a bound (records or milliseconds), or none with a written justification.
- [ ] The storage layer is injectable (a `Storage` interface or equivalent) so tests can simulate crash, torn tail, and bit flip without real hardware.

## 2. Record framing

- [ ] Each record carries a length, a checksum, a sequence number, and the payload.
- [ ] The checksum covers the sequence number as well as the payload, so a frame moved to the wrong position fails validation.
- [ ] The reader enforces a maximum record length and treats anything larger as corruption, never as an allocation request.
- [ ] Integers in the header have a fixed width and byte order (big-endian is conventional) that is identical across every language that reads the file.
- [ ] Money in payloads is integer minor units plus currency, never a float.
- [ ] If the log must resist tampering, it uses an HMAC; a CRC detects accidents only.

## 3. Recovery: torn versus corrupt

| What recovery finds | Expected action | Reviewed |
|---------------------|-----------------|----------|
| Partial header at end of file | truncate, sync, continue | [ ] |
| Partial payload at end of file | truncate, sync, continue | [ ] |
| Checksum failure on the **last** frame | truncate, sync, continue | [ ] |
| Checksum failure with bytes **after** the frame | stop, raise with offset, do not truncate | [ ] |
| Length above the maximum | stop, raise, do not truncate | [ ] |
| Sequence gap or repeat | stop, raise, do not truncate | [ ] |

- [ ] Truncation is followed by a sync before the service accepts new writes.
- [ ] The next sequence number after recovery is `lastValidSeq + 1`.
- [ ] A corruption error names the file and byte offset and leaves the file untouched for a human.

## 4. Write-ahead order

- [ ] The order is **validate, append, sync, apply, respond**. Memory never changes before the sync returns.
- [ ] A rejected operation (insufficient funds, validation failure) appends nothing.
- [ ] One logical change is one record. A transfer that debits and credits is a single frame, never two.
- [ ] Idempotency keys for money-moving requests are written in the same record as the effect they guard, so they are recovered or lost together.

## 5. Checkpoints

- [ ] A snapshot is tagged with the sequence number it covers (`coveredSeq`).
- [ ] The snapshot is synced **before** the log is truncated.
- [ ] Recovery skips every log record with `seq <= coveredSeq`.
- [ ] Tests exist for each crash window: before the snapshot sync, between the snapshot sync and the log truncate, after the truncate. Each asserts that balances are identical to the pre-crash state.
- [ ] The checkpoint interval is chosen from a measured recovery time per record, and the resulting worst-case recovery time is written down.

## 6. Tests that must exist

- [ ] Crash after append and before sync: the record is absent after recovery and memory never reflected it.
- [ ] Torn tail at several byte positions inside the last frame, including inside the header.
- [ ] Bit flip in a middle record raises corruption and does not truncate.
- [ ] Recovery run twice in a row produces the same state (idempotent replay).
- [ ] A sync counter or equivalent shows exactly one durability point per commit (or per batch, under group commit).

## 7. Questions to ask the author

1. Point at the line that makes this durable. What is the worst-case latency of that call on our storage?
2. If the host loses power between this append and the response, what does the caller see on retry?
3. How does recovery tell a torn tail from a corrupt middle, and which one does this code path assume?
4. What happens to recovery time if checkpoints stop running for a week?
