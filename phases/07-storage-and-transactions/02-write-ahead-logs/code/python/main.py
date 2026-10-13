"""Lesson: phases/07-storage-and-transactions/02-write-ahead-logs/docs/en.md
Write-Ahead Logs and Crash Recovery
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Mohan et al., "ARIES: A Transaction Recovery Method" (ACM TODS 17(1), 1992); PostgreSQL manual ch. 28
         "Reliability and the Write-Ahead Log"; Pillai et al., "All File Systems Are Not Created Equal" (OSDI 2014);
         CRC-32 per ISO 3309 / ITU-T V.42 (the polynomial behind zlib.crc32, java.util.zip.CRC32 and Go hash/crc32).
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

import struct
import zlib
from dataclasses import dataclass, field
from typing import Callable, Optional, Protocol

HEADER_BYTES = 16
MAX_PAYLOAD_BYTES = 1 << 20  # 1 MiB: anything larger is a corrupt length, not a record
_HEADER = struct.Struct(">IIQ")  # u32 payload length | u32 crc32(seq || payload) | u64 seq, big-endian


# ---------------------------------------------------------------- money (trimmed copy of Phase 10, Lesson 01)


@dataclass(frozen=True)
class Currency:
    code: str

    def __post_init__(self) -> None:
        if len(self.code) != 3 or not self.code.isalpha() or not self.code.isupper():
            raise ValueError(f"currency code must be three uppercase letters: {self.code!r}")


USD = Currency("USD")


@dataclass(frozen=True)
class Money:
    """An exact amount: signed integer minor units plus a currency. Never a float."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    def _require_same_currency(self, other: "Money") -> None:
        if self.currency != other.currency:
            raise ValueError(f"cannot combine {self.currency.code} with {other.currency.code}")

    def plus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units + other.minor_units, self.currency)

    def minus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units - other.minor_units, self.currency)

    def is_negative(self) -> bool:
        return self.minor_units < 0

    def __str__(self) -> str:
        sign = "-" if self.minor_units < 0 else ""
        mag = abs(self.minor_units)
        return f"{sign}{mag // 100}.{mag % 100:02d} {self.currency.code}"


# ---------------------------------------------------------------- storage


class Storage(Protocol):
    """The durability boundary. write() is buffered; bytes are durable only once sync() returns (fsync(2))."""

    def size(self) -> int: ...
    def read(self, offset: int, length: int) -> bytes: ...
    def write(self, offset: int, data: bytes) -> None: ...
    def truncate(self, size: int) -> None: ...
    def sync(self) -> None: ...


class MemoryStorage:
    """In-memory Storage with two copies of the bytes: what the process sees, and what has reached the disk."""

    def __init__(self) -> None:
        self._visible = bytearray()
        self._durable = bytearray()
        self.sync_count = 0

    def size(self) -> int:
        return len(self._visible)

    def durable_size(self) -> int:
        return len(self._durable)

    def read(self, offset: int, length: int) -> bytes:
        if offset < 0 or offset + length > len(self._visible):
            raise IndexError("read past end")
        return bytes(self._visible[offset : offset + length])

    def write(self, offset: int, data: bytes) -> None:
        end = offset + len(data)
        if end > len(self._visible):
            self._visible.extend(b"\x00" * (end - len(self._visible)))
        self._visible[offset:end] = data

    def truncate(self, size: int) -> None:
        if size < len(self._visible):
            del self._visible[size:]

    def sync(self) -> None:
        self._durable = bytearray(self._visible)
        self.sync_count += 1

    def crash(self) -> None:
        """kill -9 or power loss: everything that was not synced is gone."""
        self._visible = bytearray(self._durable)

    def crash_keeping_unsynced(self, keep_bytes: int) -> None:
        """Power loss during a write: the first keep_bytes of the unsynced tail reached the disk, the rest did not."""
        keep = min(keep_bytes, max(0, len(self._visible) - len(self._durable)))
        tail = self._visible[len(self._durable) : len(self._durable) + keep]
        self._visible = bytearray(self._durable) + tail
        self._durable = bytearray(self._visible)

    def flip_bit(self, offset: int) -> None:
        """Bit rot: flip one bit in a byte that is already durable."""
        self._visible[offset] ^= 0x01
        self._durable = bytearray(self._visible)


# ---------------------------------------------------------------- the log


@dataclass(frozen=True)
class Record:
    """One record as replay hands it back: its sequence number, where it starts, and the caller's bytes."""

    seq: int
    offset: int
    payload: bytes


class CorruptRecordError(Exception):
    """A fully written record was damaged, or the sequence is not contiguous. Recovery must stop, not guess."""

    def __init__(self, offset: int, reason: str) -> None:
        super().__init__(f"corrupt record at offset {offset}: {reason}")
        self.offset = offset


@dataclass(frozen=True)
class ReplayResult:
    """Records delivered, where the valid prefix ends, torn bytes cut off, and the last sequence number seen."""

    records_replayed: int
    valid_length: int
    truncated_bytes: int
    last_seq: int

    @property
    def torn(self) -> bool:
        return self.truncated_bytes > 0


def crc32(seq: int, payload: bytes) -> int:
    return zlib.crc32(struct.pack(">Q", seq) + payload) & 0xFFFFFFFF


def encode(seq: int, payload: bytes) -> bytes:
    """Frame a payload. Exposed so tests can inspect the exact bytes."""
    if len(payload) > MAX_PAYLOAD_BYTES:
        raise ValueError(f"payload too large: {len(payload)}")
    return _HEADER.pack(len(payload), crc32(seq, payload), seq) + payload


class WriteAheadLog:
    """An append-only log of framed records on top of a Storage.

    Frame layout, all integers big-endian: u32 payloadLength | u32 crc32(seq || payload) | u64 seq | payload.
    append never rewrites earlier bytes; sync() is the only durability point; sequence numbers are contiguous
    within the log; replay accepts the longest valid prefix, truncates an incomplete or checksum-failed FINAL
    frame (a torn write), and refuses to continue past a damaged frame that has data after it.
    """

    def __init__(self, storage: Storage) -> None:
        self._storage = storage
        self._end = storage.size()
        self._next_seq = 1

    def size(self) -> int:
        return self._end

    def next_seq(self) -> int:
        return self._next_seq

    def append(self, payload: bytes) -> int:
        """Append one frame at the end and return its sequence number. Not durable until sync()."""
        seq = self._next_seq
        frame = encode(seq, payload)
        self._storage.write(self._end, frame)
        self._end += len(frame)
        self._next_seq += 1
        return seq

    def sync(self) -> None:
        """The durability point. Everything appended before this call survives a crash after it returns."""
        self._storage.sync()

    def replay(self, consumer: Callable[[Record], None]) -> ReplayResult:
        """Hand every frame to the consumer in order; truncate a torn tail; raise on corruption in the middle."""
        file_size = self._storage.size()
        replayed = 0
        offset = 0
        last_seq = 0
        first = True
        while offset < file_size:
            remaining = file_size - offset
            if remaining < HEADER_BYTES:
                break  # torn: header cut off
            length, stored_crc, seq = _HEADER.unpack(self._storage.read(offset, HEADER_BYTES))
            if length > MAX_PAYLOAD_BYTES:
                raise CorruptRecordError(offset, f"implausible length {length}")
            if remaining - HEADER_BYTES < length:
                break  # torn: payload cut off
            payload = self._storage.read(offset + HEADER_BYTES, length)
            actual_crc = crc32(seq, payload)
            is_last = offset + HEADER_BYTES + length == file_size
            if actual_crc != stored_crc:
                if is_last:
                    break  # torn: tail record half-written
                after = file_size - (offset + HEADER_BYTES + length)
                raise CorruptRecordError(
                    offset, f"crc mismatch (stored {stored_crc:08x}, computed {actual_crc:08x}) with {after} bytes after it"
                )
            if not first and seq != last_seq + 1:
                raise CorruptRecordError(offset, f"sequence gap: expected {last_seq + 1}, found {seq}")
            consumer(Record(seq, offset, payload))
            replayed += 1
            last_seq = seq
            first = False
            offset += HEADER_BYTES + length
        truncated = file_size - offset
        if truncated > 0:
            self._storage.truncate(offset)
            self._storage.sync()
        self._end = offset
        if replayed > 0:
            self._next_seq = last_seq + 1
        return ReplayResult(replayed, offset, truncated, last_seq)

    def reset(self, next_seq: int) -> None:
        """Throw away every frame after a checkpoint made them redundant; numbering continues from next_seq."""
        if self._end > 0:
            self._storage.truncate(0)
            self._storage.sync()
            self._end = 0
        self._next_seq = next_seq


# ---------------------------------------------------------------- the ledger on top

OP_CREDIT = 1
OP_DEBIT = 2
OP_TRANSFER = 3
_OP_MIN_BYTES = 1 + 2 + 2 + 3 + 8


def encode_op(op: int, source: str, target: str, amount: Money) -> bytes:
    """Payload layout: u8 op | u16 fromLen | from | u16 toLen | to | 3 bytes currency | i64 minorUnits."""
    f = source.encode("utf-8")
    t = target.encode("utf-8")
    return (
        struct.pack(">BH", op, len(f)) + f + struct.pack(">H", len(t)) + t
        + amount.currency.code.encode("ascii") + struct.pack(">q", amount.minor_units)
    )


class Ledger:
    """A ledger of balances whose only durable state is two logs: the WAL and a snapshot log.

    Every mutation is framed, appended, and synced BEFORE the in-memory dict changes. checkpoint() appends the
    whole dict as one snapshot record (tagged with the WAL sequence it covers) and then empties the WAL.
    """

    def __init__(self, wal_storage: Storage, snapshot_storage: Storage, currency: Currency) -> None:
        self._wal = WriteAheadLog(wal_storage)
        self._snapshots = WriteAheadLog(snapshot_storage)
        self._currency = currency
        self._balances: dict[str, Money] = {}
        self._applied_seq = 0
        self.last_recovery: Optional[ReplayResult] = None

    @staticmethod
    def open(wal_storage: Storage, snapshot_storage: Storage, currency: Currency) -> "Ledger":
        """Recover: load the last complete snapshot, then replay WAL records newer than it."""
        ledger = Ledger(wal_storage, snapshot_storage, currency)
        latest: list[Record] = []
        ledger._snapshots.replay(lambda r: latest.append(r))
        if latest:
            ledger._load_snapshot(latest[-1].payload)
        covered = ledger._applied_seq

        def on_record(record: Record) -> None:
            if record.seq <= covered:
                return  # already inside the snapshot: skip, do not double-apply
            ledger._apply(record)

        ledger.last_recovery = ledger._wal.replay(on_record)
        if ledger.last_recovery.last_seq <= covered:
            ledger._wal.reset(covered + 1)  # every WAL record (if any) is inside the snapshot: finish the interrupted checkpoint
        return ledger

    def wal_size(self) -> int:
        return self._wal.size()

    def snapshot_count(self) -> int:
        return self._snapshots.next_seq() - 1

    def applied_seq(self) -> int:
        return self._applied_seq

    def view(self) -> dict[str, Money]:
        return dict(sorted(self._balances.items()))

    def balance(self, account: str) -> Money:
        return self._balances.get(account, Money.of_minor(0, self._currency))

    def credit(self, account: str, amount: Money) -> None:
        self._require_positive(amount)
        self._commit(encode_op(OP_CREDIT, account, "", amount))

    def debit(self, account: str, amount: Money) -> None:
        self._require_positive(amount)
        if self.balance(account).minus(amount).is_negative():
            raise RuntimeError(f"insufficient funds in {account}: {self.balance(account)} < {amount}")
        self._commit(encode_op(OP_DEBIT, account, "", amount))

    def transfer(self, source: str, target: str, amount: Money) -> None:
        """One record, two balance changes: a crash can never leave the money in flight."""
        self._require_positive(amount)
        if self.balance(source).minus(amount).is_negative():
            raise RuntimeError(f"insufficient funds in {source}: {self.balance(source)} < {amount}")
        self._commit(encode_op(OP_TRANSFER, source, target, amount))

    def _require_positive(self, amount: Money) -> None:
        if amount.currency != self._currency:
            raise ValueError(f"ledger is {self._currency.code}")
        if amount.minor_units <= 0:
            raise ValueError(f"amount must be positive: {amount}")

    def _commit(self, payload: bytes) -> None:
        """Write-ahead: append, sync, and only then mutate memory."""
        seq = self._wal.append(payload)
        self._wal.sync()
        self._apply(Record(seq, -1, payload))

    def _apply(self, record: Record) -> None:
        p = record.payload
        if len(p) < _OP_MIN_BYTES:
            raise CorruptRecordError(record.offset, "payload shorter than op header")
        op = p[0]
        pos = 1
        source, pos = _read_string(p, pos, record.offset)
        target, pos = _read_string(p, pos, record.offset)
        currency = Currency(p[pos : pos + 3].decode("ascii"))
        (minor,) = struct.unpack_from(">q", p, pos + 3)
        amount = Money.of_minor(minor, currency)
        if op == OP_CREDIT:
            self._balances[source] = self.balance(source).plus(amount)
        elif op == OP_DEBIT:
            self._balances[source] = self.balance(source).minus(amount)
        elif op == OP_TRANSFER:
            self._balances[source] = self.balance(source).minus(amount)
            self._balances[target] = self.balance(target).plus(amount)
        else:
            raise CorruptRecordError(record.offset, f"unknown op {op}")
        self._applied_seq = record.seq

    def checkpoint(self) -> None:
        """Snapshot payload: u64 coveredSeq | u32 count | repeated (u16 len | account | 3 bytes currency | i64 minor).

        Append it to the snapshot log, sync, then empty the WAL. Crash before the sync: the snapshot frame is torn
        and ignored, the WAL is intact. Crash after the sync but before the reset: the WAL still holds records with
        seq <= coveredSeq, and open() skips them. Either way the recovered state is identical.
        """
        out = bytearray(struct.pack(">QI", self._applied_seq, len(self._balances)))
        for account, money in sorted(self._balances.items()):
            k = account.encode("utf-8")
            out += struct.pack(">H", len(k)) + k + money.currency.code.encode("ascii") + struct.pack(">q", money.minor_units)
        self._snapshots.append(bytes(out))
        self._snapshots.sync()
        self._wal.reset(self._applied_seq + 1)

    def _load_snapshot(self, payload: bytes) -> None:
        self._applied_seq, count = struct.unpack_from(">QI", payload, 0)
        pos = 12
        for _ in range(count):
            account, pos = _read_string(payload, pos, -1)
            currency = Currency(payload[pos : pos + 3].decode("ascii"))
            (minor,) = struct.unpack_from(">q", payload, pos + 3)
            pos += 11
            self._balances[account] = Money.of_minor(minor, currency)


def _read_string(buf: bytes, pos: int, offset: int) -> tuple[str, int]:
    (length,) = struct.unpack_from(">H", buf, pos)
    pos += 2
    if pos + length > len(buf):
        raise CorruptRecordError(offset, f"string length {length} exceeds payload")
    return buf[pos : pos + length].decode("utf-8"), pos + length


# ---------------------------------------------------------------- demo


def usd(cents: int) -> Money:
    return Money.of_minor(cents, USD)


def fmt(view: dict[str, Money]) -> str:
    return "{" + ", ".join(f"{k}={v}" for k, v in view.items()) + "}"


def main() -> None:
    print("== One frame on disk ==")
    payload = b"hello"
    print(f'crc32(seq=1, "hello") = {crc32(1, payload):08x}')
    print(f"frame = {encode(1, payload).hex()}  (len | crc | seq | payload)")

    print("\n== Balances survive kill -9 ==")
    wal_disk = MemoryStorage()
    snap_disk = MemoryStorage()
    ledger = Ledger.open(wal_disk, snap_disk, USD)
    ledger.credit("merchant:acme", usd(10_000))
    ledger.credit("customer:ann", usd(5_000))
    ledger.transfer("customer:ann", "merchant:acme", usd(1_999))
    print(f"before crash: {fmt(ledger.view())}, wal bytes = {ledger.wal_size()}, syncs = {wal_disk.sync_count}")
    wal_disk.crash()  # the process dies; only synced bytes remain
    ledger = Ledger.open(wal_disk, snap_disk, USD)
    assert ledger.last_recovery is not None
    print(f"after restart: {fmt(ledger.view())}, replayed {ledger.last_recovery.records_replayed} records")

    print("\n== A torn final record is dropped, not guessed ==")
    clean_size = ledger.wal_size()
    raw = WriteAheadLog(wal_disk)
    raw.replay(lambda r: None)
    raw.append(encode_op(OP_CREDIT, "customer:bob", "", usd(700)))
    wal_disk.crash_keeping_unsynced(9)  # power fails 9 bytes into the write
    ledger = Ledger.open(wal_disk, snap_disk, USD)
    r = ledger.last_recovery
    assert r is not None
    print(f"replayed {r.records_replayed}, truncated {r.truncated_bytes} torn bytes, wal back to {r.valid_length} (clean size was {clean_size})")
    print(f"customer:bob = {ledger.balance('customer:bob')}")

    print("\n== A corrupt middle record stops replay ==")
    wal_disk.flip_bit(HEADER_BYTES + 5)  # flip one bit inside the first payload
    try:
        Ledger.open(wal_disk, snap_disk, USD)
        print("unexpected: replay succeeded")
    except CorruptRecordError as e:
        print(f"refused: {e}")
    wal_disk.flip_bit(HEADER_BYTES + 5)  # repair it so the demo can continue

    print("\n== Checkpoint compacts the log; sequence numbers make it safe ==")
    ledger = Ledger.open(wal_disk, snap_disk, USD)
    for _ in range(50):
        ledger.credit("merchant:acme", usd(1))
    print(f"50 credits: wal bytes = {ledger.wal_size()}, applied seq = {ledger.applied_seq()}")
    ledger.checkpoint()
    print(f"after checkpoint: wal bytes = {ledger.wal_size()}, snapshots = {ledger.snapshot_count()}, snapshot bytes = {snap_disk.size()}")
    ledger.credit("customer:ann", usd(100))
    ledger = Ledger.open(wal_disk, snap_disk, USD)
    assert ledger.last_recovery is not None
    print(
        f"after restart: {fmt(ledger.view())}, replayed {ledger.last_recovery.records_replayed} wal record(s) "
        f"on top of the snapshot, next seq = {ledger.applied_seq() + 1}"
    )


if __name__ == "__main__":
    main()
