"""Tests for phases/07-storage-and-transactions/02-write-ahead-logs. Run: python3 -m unittest -v"""
import struct
import unittest

from main import (
    HEADER_BYTES,
    MAX_PAYLOAD_BYTES,
    OP_CREDIT,
    OP_TRANSFER,
    USD,
    CorruptRecordError,
    Ledger,
    MemoryStorage,
    Money,
    WriteAheadLog,
    crc32,
    encode,
    encode_op,
)


def usd(cents: int) -> Money:
    return Money.of_minor(cents, USD)


def replay_strings(log: WriteAheadLog) -> list[str]:
    seen: list[str] = []
    log.replay(lambda r: seen.append(r.payload.decode()))
    return seen


class FramingTest(unittest.TestCase):
    def test_crc32_covers_seq_and_payload(self) -> None:
        self.assertNotEqual(crc32(1, b"hello"), crc32(2, b"hello"))
        self.assertEqual(crc32(1, b"hello"), 0x341AC4BC)  # same value as the Java and Go versions

    def test_frame_layout(self) -> None:
        frame = encode(1, b"hello")
        self.assertEqual(len(frame), HEADER_BYTES + 5)
        self.assertEqual(struct.unpack_from(">I", frame, 0)[0], 5)
        self.assertEqual(struct.unpack_from(">Q", frame, 8)[0], 1)
        with self.assertRaises(ValueError):
            encode(1, b"\x00" * (MAX_PAYLOAD_BYTES + 1))


class WriteAheadLogTest(unittest.TestCase):
    def test_round_trip_with_contiguous_sequence_numbers(self) -> None:
        disk = MemoryStorage()
        log = WriteAheadLog(disk)
        self.assertEqual([log.append(b"a"), log.append(b"bb"), log.append(b"")], [1, 2, 3])
        self.assertEqual(log.size(), 17 + 18 + 16)
        log.sync()
        reopened = WriteAheadLog(disk)
        seen: list[str] = []
        result = reopened.replay(lambda r: seen.append(f"{r.seq}:{r.payload.decode()}"))
        self.assertEqual(seen, ["1:a", "2:bb", "3:"])
        self.assertFalse(result.torn)
        self.assertEqual((result.records_replayed, result.valid_length, result.last_seq), (3, 51, 3))
        self.assertEqual(reopened.next_seq(), 4)

    def test_empty_log_replays_nothing(self) -> None:
        result = WriteAheadLog(MemoryStorage()).replay(lambda r: None)
        self.assertEqual((result.records_replayed, result.valid_length, result.torn), (0, 0, False))

    def test_unsynced_append_is_lost_on_crash(self) -> None:
        disk = MemoryStorage()
        log = WriteAheadLog(disk)
        log.append(b"synced")
        log.sync()
        log.append(b"not synced")
        disk.crash()
        self.assertEqual(replay_strings(WriteAheadLog(disk)), ["synced"])

    def test_torn_header_is_dropped_and_storage_truncated(self) -> None:
        disk = MemoryStorage()
        log = WriteAheadLog(disk)
        log.append(b"one")
        log.append(b"two")
        log.sync()
        log.append(b"three")
        disk.crash_keeping_unsynced(7)
        reopened = WriteAheadLog(disk)
        self.assertEqual(replay_strings(reopened), ["one", "two"])
        self.assertEqual(disk.size(), 19 + 19)
        self.assertEqual(reopened.append(b"x"), 3, "the torn seq is reused")

    def test_torn_payload_is_dropped_and_reported(self) -> None:
        disk = MemoryStorage()
        log = WriteAheadLog(disk)
        log.append(b"first")
        log.sync()
        log.append(b"second")
        disk.crash_keeping_unsynced(HEADER_BYTES + 3)
        seen: list[str] = []
        result = WriteAheadLog(disk).replay(lambda r: seen.append(r.payload.decode()))
        self.assertEqual(seen, ["first"])
        self.assertTrue(result.torn)
        self.assertEqual(result.truncated_bytes, 19)

    def test_corrupt_middle_record_stops_replay_with_offset(self) -> None:
        disk = MemoryStorage()
        log = WriteAheadLog(disk)
        for p in (b"one", b"two", b"three"):
            log.append(p)
        log.sync()
        disk.flip_bit(19 + HEADER_BYTES)
        seen: list[str] = []
        with self.assertRaises(CorruptRecordError) as ctx:
            WriteAheadLog(disk).replay(lambda r: seen.append(r.payload.decode()))
        self.assertEqual(ctx.exception.offset, 19)
        self.assertIn("crc mismatch", str(ctx.exception))
        self.assertEqual(seen, ["one"])
        self.assertEqual(disk.size(), 19 + 19 + 21, "a corrupt log is never truncated")

    def test_checksum_failed_final_frame_is_treated_as_torn(self) -> None:
        disk = MemoryStorage()
        log = WriteAheadLog(disk)
        log.append(b"first")
        log.append(b"last")
        log.sync()
        disk.flip_bit(disk.size() - 1)
        seen: list[str] = []
        result = WriteAheadLog(disk).replay(lambda r: seen.append(r.payload.decode()))
        self.assertEqual(seen, ["first"])
        self.assertTrue(result.torn)

    def test_sequence_gap_is_corruption(self) -> None:
        disk = MemoryStorage()
        disk.write(0, encode(1, b"one"))
        disk.write(19, encode(3, b"three"))
        disk.sync()
        with self.assertRaises(CorruptRecordError) as ctx:
            WriteAheadLog(disk).replay(lambda r: None)
        self.assertIn("sequence gap", str(ctx.exception))

    def test_implausible_length_is_corruption(self) -> None:
        disk = MemoryStorage()
        disk.write(0, struct.pack(">IIQ", 0xFFFFFFFF, 0, 1) + b"\x00" * 20)
        disk.sync()
        with self.assertRaises(CorruptRecordError):
            WriteAheadLog(disk).replay(lambda r: None)

    def test_replay_is_idempotent(self) -> None:
        disk = MemoryStorage()
        log = WriteAheadLog(disk)
        for p in (b"a", b"b", b"c"):
            log.append(p)
        log.sync()
        self.assertEqual(replay_strings(WriteAheadLog(disk)), replay_strings(WriteAheadLog(disk)))


class LedgerTest(unittest.TestCase):
    def test_commit_syncs_before_memory_changes(self) -> None:
        wal, snap = MemoryStorage(), MemoryStorage()
        ledger = Ledger.open(wal, snap, USD)
        before = wal.sync_count
        ledger.credit("acme", usd(10_000))
        self.assertEqual(wal.sync_count, before + 1)
        self.assertEqual(wal.durable_size(), wal.size())
        self.assertEqual(ledger.balance("acme"), usd(10_000))

    def test_balances_survive_crash(self) -> None:
        wal, snap = MemoryStorage(), MemoryStorage()
        ledger = Ledger.open(wal, snap, USD)
        ledger.credit("acme", usd(10_000))
        ledger.credit("ann", usd(5_000))
        ledger.transfer("ann", "acme", usd(1_999))
        ledger.debit("acme", usd(1))
        wal.crash()
        recovered = Ledger.open(wal, snap, USD)
        self.assertEqual(recovered.balance("acme"), usd(11_998))
        self.assertEqual(recovered.balance("ann"), usd(3_001))
        assert recovered.last_recovery is not None
        self.assertEqual(recovered.last_recovery.records_replayed, 4)
        self.assertEqual(recovered.applied_seq(), 4)

    def test_rejected_operations_append_nothing(self) -> None:
        wal, snap = MemoryStorage(), MemoryStorage()
        ledger = Ledger.open(wal, snap, USD)
        ledger.credit("ann", usd(100))
        size = wal.size()
        with self.assertRaises(RuntimeError):
            ledger.debit("ann", usd(101))
        with self.assertRaises(ValueError):
            ledger.credit("ann", usd(0))
        self.assertEqual(wal.size(), size)

    def test_torn_transfer_changes_neither_balance(self) -> None:
        wal, snap = MemoryStorage(), MemoryStorage()
        ledger = Ledger.open(wal, snap, USD)
        ledger.credit("acme", usd(1_000))
        clean = wal.size()
        raw = WriteAheadLog(wal)
        raw.replay(lambda r: None)
        raw.append(encode_op(OP_TRANSFER, "acme", "ann", usd(500)))
        wal.crash_keeping_unsynced(20)
        recovered = Ledger.open(wal, snap, USD)
        self.assertEqual(recovered.balance("acme"), usd(1_000))
        self.assertEqual(recovered.balance("ann"), usd(0))
        assert recovered.last_recovery is not None
        self.assertTrue(recovered.last_recovery.torn)
        self.assertEqual(recovered.wal_size(), clean)

    def test_checkpoint_truncates_wal_and_recovers_from_snapshot(self) -> None:
        wal, snap = MemoryStorage(), MemoryStorage()
        ledger = Ledger.open(wal, snap, USD)
        ledger.credit("acme", usd(10_000))
        for _ in range(20):
            ledger.credit("acme", usd(1))
        bulky = ledger.wal_size()
        ledger.checkpoint()
        self.assertEqual(ledger.wal_size(), 0)
        self.assertGreater(bulky, 0)
        self.assertEqual(ledger.snapshot_count(), 1)
        ledger.credit("bob", usd(700))
        recovered = Ledger.open(wal, snap, USD)
        self.assertEqual(recovered.balance("acme"), usd(10_020))
        self.assertEqual(recovered.balance("bob"), usd(700))
        assert recovered.last_recovery is not None
        self.assertEqual(recovered.last_recovery.records_replayed, 1, "only post-checkpoint records come from the wal")
        self.assertEqual(recovered.applied_seq(), 22, "sequence numbers continue across the checkpoint")

    def test_records_covered_by_snapshot_are_skipped_not_double_applied(self) -> None:
        # Simulate a crash after the snapshot was synced but before the WAL was truncated.
        wal, snap = MemoryStorage(), MemoryStorage()
        ledger = Ledger.open(wal, snap, USD)
        ledger.credit("a", usd(100))
        ledger.credit("a", usd(100))
        wal_bytes = wal.size()
        Ledger.open(wal, snap, USD).checkpoint()  # snapshot durable, wal reset...
        stale = MemoryStorage()  # ...but on this disk the truncate never landed
        rebuild = WriteAheadLog(stale)
        rebuild.append(encode_op(OP_CREDIT, "a", "", usd(100)))
        rebuild.append(encode_op(OP_CREDIT, "a", "", usd(100)))
        rebuild.sync()
        self.assertEqual(stale.size(), wal_bytes)
        recovered = Ledger.open(stale, snap, USD)
        self.assertEqual(recovered.balance("a"), usd(200))
        self.assertEqual(recovered.wal_size(), 0, "interrupted checkpoint is finished on open")
        recovered.credit("a", usd(1))
        self.assertEqual(recovered.applied_seq(), 3)

    def test_opening_twice_yields_same_state(self) -> None:
        wal, snap = MemoryStorage(), MemoryStorage()
        ledger = Ledger.open(wal, snap, USD)
        ledger.credit("k", usd(1))
        ledger.credit("k", usd(2))
        first = Ledger.open(wal, snap, USD).view()
        second = Ledger.open(wal, snap, USD).view()
        self.assertEqual(first, second)
        self.assertEqual(first["k"], usd(3))


if __name__ == "__main__":
    unittest.main()
