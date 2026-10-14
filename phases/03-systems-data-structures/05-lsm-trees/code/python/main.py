"""Lesson: phases/03-systems-data-structures/05-lsm-trees/docs/en.md
LSM Trees: Memtables, SSTables, and Compaction
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: O'Neil et al. (1996), "The Log-Structured Merge-Tree"; Chang et al. (2006), "Bigtable"; RocksDB wiki
(Compaction, Bloom Filter); Cassandra SizeTieredCompactionStrategy; Bloom (1970). Python 3.11+, stdlib only.
"""

from __future__ import annotations

import heapq
import struct
from dataclasses import dataclass
from decimal import ROUND_HALF_UP, Decimal
from typing import Iterator, Optional

MASK64 = 0xFFFFFFFFFFFFFFFF


def fmt_fixed(x: float, digits: int) -> str:
    """Java's %.Nf: round the shortest decimal repr HALF_UP (Python's f-format rounds the binary value)."""
    q = Decimal(repr(x)).quantize(Decimal(1).scaleb(-digits), rounding=ROUND_HALF_UP)
    return f"{q:.{digits}f}"


def java_str(x: object) -> str:
    """String.valueOf: None prints as null, collections print as [a, b]."""
    if x is None:
        return "null"
    if isinstance(x, (list, tuple)):
        return "[" + ", ".join(java_str(e) for e in x) + "]"
    return str(x)


class JavaRandom:
    """java.util.Random, bit for bit: 48-bit LCG, so the same seed gives the same stream as Java."""

    _MULT = 0x5DEECE66D
    _MASK = (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ self._MULT) & self._MASK

    def _next(self, bits: int) -> int:
        self._seed = (self._seed * self._MULT + 0xB) & self._MASK
        r = self._seed >> (48 - bits)
        return r - (1 << 32) if r >= (1 << 31) else r  # (int) cast

    def next_int(self, bound: int) -> int:
        if bound <= 0:
            raise ValueError("bound must be positive")
        r = self._next(31)
        m = bound - 1
        if bound & m == 0:
            return (bound * r) >> 31
        u = r
        while True:
            r = u % bound
            if (u - r + m) & 0xFFFFFFFF < 0x80000000:  # Java: u - r + m >= 0 in 32-bit int
                return r
            u = self._next(31)


class IllegalStateError(Exception):
    """Java's IllegalStateException."""


@dataclass(frozen=True)
class Entry:
    """A value or a tombstone, stamped with the sequence number of the write that produced it. Higher seq wins."""

    value: Optional[str]
    tombstone: bool
    seq: int

    # Java's factories Entry.value(...) and Entry.tombstone(...) share names with the fields, which a
    # dataclass cannot express, so they are of_value and of_tombstone here.
    @staticmethod
    def of_value(v: str, seq: int) -> "Entry":
        return Entry(v, False, seq)

    @staticmethod
    def of_tombstone(seq: int) -> "Entry":
        return Entry(None, True, seq)


class Hashing:
    """FNV-1a and a 64-bit mixer, as in Lessons 01 and 02. Masked to 64 bits so values equal Java's longs."""

    @staticmethod
    def fnv1a64(key: str) -> int:
        h = 0xCBF29CE484222325
        for b in key.encode("utf-8"):
            h = ((h ^ b) * 0x100000001B3) & MASK64
        return h

    @staticmethod
    def mix64(h: int) -> int:
        h &= MASK64
        h ^= h >> 33
        h = (h * 0xFF51AFD7ED558CCD) & MASK64
        h ^= h >> 33
        h = (h * 0xC4CEB9FE1A85EC53) & MASK64
        h ^= h >> 33
        return h


class BloomFilter:
    """Sized for ~1% false positives: 10 bits per key, 7 hashes, double hashing."""

    def __init__(self, expected_keys: int) -> None:
        self._bits = max(64, expected_keys * 10)
        self._words = [0] * ((self._bits + 63) // 64)
        self._hashes = 7

    def _index(self, h1: int, h2: int, i: int) -> int:
        return ((h1 + i * h2) & MASK64) % self._bits  # Long.remainderUnsigned of the wrapped sum

    def add(self, key: str) -> None:
        h1 = Hashing.mix64(Hashing.fnv1a64(key))
        h2 = Hashing.mix64(h1)
        for i in range(self._hashes):
            b = self._index(h1, h2, i)
            self._words[b >> 6] |= 1 << (b & 63)

    def might_contain(self, key: str) -> bool:
        h1 = Hashing.mix64(Hashing.fnv1a64(key))
        h2 = Hashing.mix64(h1)
        for i in range(self._hashes):
            b = self._index(h1, h2, i)
            if self._words[b >> 6] & (1 << (b & 63)) == 0:
                return False
        return True


class SimulatedDisk:
    """A directory of immutable byte files with I/O counters. Stands in for the file system."""

    def __init__(self) -> None:
        self._files: dict[str, bytes] = {}
        self.bytes_written = self.bytes_read = self.files_written = self.files_deleted = 0

    def write(self, name: str, data: bytes) -> None:
        if name in self._files:
            raise IllegalStateError(f"files are immutable: {name}")
        self._files[name] = bytes(data)
        self.bytes_written += len(data)
        self.files_written += 1

    def read(self, name: str) -> bytes:
        data = self._files.get(name)
        if data is None:
            raise IllegalStateError(f"no such file: {name}")
        self.bytes_read += len(data)
        return data

    def delete(self, name: str) -> None:
        if self._files.pop(name, None) is not None:
            self.files_deleted += 1

    def file_count(self) -> int:
        return len(self._files)

    def total_bytes(self) -> int:
        return sum(len(f) for f in self._files.values())


def _utf16_len(s: str) -> int:
    """String.length() counts UTF-16 code units, so a character outside the BMP counts twice."""
    return len(s) + sum(1 for ch in s if ord(ch) > 0xFFFF)


class Memtable:
    """The in-memory write buffer. Python has no sorted map in the stdlib, so it is a dict sorted at flush time."""

    def __init__(self) -> None:
        self._map: dict[str, Entry] = {}
        self._approximate_bytes = 0

    def put(self, key: str, entry: Entry) -> None:
        old = self._map.get(key)
        self._map[key] = entry
        self._approximate_bytes += _utf16_len(key) + (0 if entry.value is None else _utf16_len(entry.value)) + 16
        if old is not None:
            self._approximate_bytes -= _utf16_len(key) + (0 if old.value is None else _utf16_len(old.value)) + 16

    def get(self, key: str) -> Optional[Entry]:
        return self._map.get(key)

    def size(self) -> int:
        return len(self._map)

    def approximate_bytes(self) -> int:
        return self._approximate_bytes

    def is_empty(self) -> bool:
        return not self._map

    def entries(self) -> dict[str, Entry]:
        """A key-ordered snapshot (dicts keep insertion order), the counterpart of TreeMap iteration."""
        return dict(sorted(self._map.items()))


# ----- DataOutputStream / DataInputStream wire format: big-endian ints, modified UTF-8 strings ---------

def write_utf(out: bytearray, s: str) -> None:
    """DataOutputStream.writeUTF: u16 length, then modified UTF-8 of the UTF-16 code units."""
    units = s.encode("utf-16-be")
    body = bytearray()
    for i in range(0, len(units), 2):
        c = (units[i] << 8) | units[i + 1]
        if 0x0001 <= c <= 0x007F:
            body.append(c)
        elif c <= 0x07FF:  # includes U+0000, written as two bytes in modified UTF-8
            body += bytes((0xC0 | (c >> 6), 0x80 | (c & 0x3F)))
        else:
            body += bytes((0xE0 | (c >> 12), 0x80 | ((c >> 6) & 0x3F), 0x80 | (c & 0x3F)))
    if len(body) > 0xFFFF:
        raise ValueError("encoded string too long")
    out += struct.pack(">H", len(body))
    out += body


def read_utf(data: bytes, pos: int) -> tuple[str, int]:
    """DataInputStream.readUTF: returns the string and the new position."""
    (n,) = struct.unpack_from(">H", data, pos)
    pos += 2
    end = pos + n
    units = bytearray()
    while pos < end:
        a = data[pos]
        if a < 0x80:
            c, pos = a, pos + 1
        elif a & 0xE0 == 0xC0:
            c, pos = ((a & 0x1F) << 6) | (data[pos + 1] & 0x3F), pos + 2
        else:
            c, pos = ((a & 0x0F) << 12) | ((data[pos + 1] & 0x3F) << 6) | (data[pos + 2] & 0x3F), pos + 3
        units += struct.pack(">H", c)
    return units.decode("utf-16-be", errors="surrogatepass"), pos


class SSTable:
    """Immutable sorted run on disk with a Bloom filter and a sparse index in memory."""

    BLOCK_ENTRIES = 16

    def __init__(self, file_name: str, disk: SimulatedDisk, entry_count: int, nbytes: int,
                 min_key: Optional[str], max_key: Optional[str], bloom: BloomFilter) -> None:
        self.file_name = file_name
        self._disk = disk
        self.entry_count = entry_count
        self.bytes = nbytes
        self.min_key = min_key
        self.max_key = max_key
        self._bloom = bloom
        self._sparse_keys: list[str] = []      # first key of every block
        self._sparse_offsets: list[int] = []   # byte offset of every block
        self.bloom_negatives = self.range_negatives = self.block_reads = 0

    @staticmethod
    def write(disk: SimulatedDisk, file_name: str, sorted_entries: dict[str, Entry]) -> "SSTable":
        """Write a key-ordered mapping as one file and return the handle."""
        out = bytearray()
        bloom = BloomFilter(len(sorted_entries))
        sparse_keys: list[str] = []
        sparse_offsets: list[int] = []
        out += struct.pack(">i", len(sorted_entries))
        for i, (key, e) in enumerate(sorted_entries.items()):
            if i % SSTable.BLOCK_ENTRIES == 0:
                sparse_keys.append(key)
                sparse_offsets.append(len(out))
            write_utf(out, key)
            out += struct.pack(">q?", e.seq, e.tombstone)
            write_utf(out, "" if e.tombstone else e.value)
            bloom.add(key)
        data = bytes(out)
        disk.write(file_name, data)
        keys = list(sorted_entries)
        table = SSTable(file_name, disk, len(sorted_entries), len(data),
                        keys[0] if keys else None, keys[-1] if keys else None, bloom)
        table._sparse_keys.extend(sparse_keys)
        table._sparse_offsets.extend(sparse_offsets)
        return table

    def get(self, key: str) -> Optional[Entry]:
        """Point lookup: key range, then Bloom, then one block. None when the key is not in this table."""
        if self.min_key is None or key < self.min_key or key > self.max_key:
            self.range_negatives += 1
            return None
        if not self._bloom.might_contain(key):
            self.bloom_negatives += 1
            return None
        for k, e in self.read_block(self.block_for(key)).items():
            if k == key:
                return e
            if k > key:
                break
        return None

    def block_for(self, key: str) -> int:
        """Index of the last sparse key <= key (binary search over the in-memory index)."""
        lo, hi = 0, len(self._sparse_keys) - 1
        while lo < hi:
            mid = (lo + hi + 1) >> 1
            if self._sparse_keys[mid] <= key:
                lo = mid
            else:
                hi = mid - 1
        return lo

    def read_block(self, block: int) -> dict[str, Entry]:
        """Decode one block of up to BLOCK_ENTRIES entries starting at the block's offset."""
        self.block_reads += 1
        data = self._disk.read(self.file_name)
        pos = self._sparse_offsets[block]
        remaining = min(SSTable.BLOCK_ENTRIES, self.entry_count - block * SSTable.BLOCK_ENTRIES)
        out: dict[str, Entry] = {}
        for _ in range(remaining):
            k, pos = read_utf(data, pos)
            seq, tomb = struct.unpack_from(">q?", data, pos)
            pos += 9
            v, pos = read_utf(data, pos)
            out[k] = Entry.of_tombstone(seq) if tomb else Entry.of_value(v, seq)
        return out

    def read_all(self) -> dict[str, Entry]:
        """Every entry, in key order. Used by compaction and range scans."""
        out: dict[str, Entry] = {}
        for b in range(len(self._sparse_keys)):
            out.update(self.read_block(b))
        return out

    def block_count(self) -> int:
        return len(self._sparse_keys)


class LsmTree:
    """One mutable memtable, a list of immutable SSTables, and size-tiered compaction."""

    def __init__(self, disk: SimulatedDisk, memtable_limit: int, tier_threshold: int) -> None:
        if memtable_limit <= 0 or tier_threshold < 2:
            raise ValueError("bad limits")
        self.disk = disk
        self.memtable_limit = memtable_limit
        self.tier_threshold = tier_threshold
        self._memtable = Memtable()
        self._tables: list[SSTable] = []  # seq numbers, not position, decide recency
        self._seq = 0
        self._next_file_id = 0
        self.flushes = self.compactions = self.bytes_compacted = 0
        self.user_bytes_written = self.tables_consulted = self.gets = 0

    def put(self, key: str, value: str) -> None:
        if value is None:
            raise ValueError("use delete for tombstones")
        self.user_bytes_written += _utf16_len(key) + _utf16_len(value)
        self._seq += 1
        self._memtable.put(key, Entry.of_value(value, self._seq))
        self._maybe_flush()

    def delete(self, key: str) -> None:
        self.user_bytes_written += _utf16_len(key)
        self._seq += 1
        self._memtable.put(key, Entry.of_tombstone(self._seq))  # a delete is a write
        self._maybe_flush()

    def _maybe_flush(self) -> None:
        if self._memtable.size() >= self.memtable_limit:
            self.flush()

    def flush(self) -> None:
        """Freeze the memtable, write it as a new SSTable, start an empty memtable, then check for compaction."""
        if self._memtable.is_empty():
            return
        t = SSTable.write(self.disk, f"sst-{self._next_file_id}.db", self._memtable.entries())
        self._next_file_id += 1
        self._tables.append(t)
        self._memtable = Memtable()
        self.flushes += 1
        self.maybe_compact()

    def get(self, key: str) -> Optional[str]:
        """Memtable answers outright; otherwise consult every table and the highest seq wins."""
        self.gets += 1
        e = self._memtable.get(key)
        if e is not None:
            return None if e.tombstone else e.value
        best: Optional[Entry] = None
        for t in self._tables:
            self.tables_consulted += 1
            found = t.get(key)
            if found is not None and (best is None or found.seq > best.seq):
                best = found
        return None if best is None or best.tombstone else best.value

    def maybe_compact(self) -> None:
        while True:
            tier = self._full_tier()
            if tier is None:
                return
            self.compact(tier)

    def _full_tier(self) -> Optional[list[SSTable]]:
        """Group tables whose entry counts are less than twice the smallest in the group."""
        ordered = sorted(self._tables, key=lambda t: t.entry_count)  # stable, like List.sort
        for i in range(len(ordered)):
            base = max(1, ordered[i].entry_count)
            tier = []
            j = i
            while j < len(ordered) and ordered[j].entry_count < 2 * base:
                tier.append(ordered[j])
                j += 1
            if len(tier) >= self.tier_threshold:
                return tier
        return None

    def compact(self, chosen: list[SSTable]) -> None:
        """k-way merge; highest seq wins per key; drop tombstones only when every table is being compacted."""
        everything = len(chosen) == len(self._tables)
        heap: list[tuple[str, int, int, Entry, Iterator]] = []
        for idx, t in enumerate(chosen):
            self.bytes_compacted += t.bytes
            it = iter(t.read_all().items())
            head = next(it, None)
            if head is not None:
                heap.append((head[0], -head[1].seq, idx, head[1], it))  # key, then newest first on ties
        heapq.heapify(heap)
        merged: dict[str, Entry] = {}
        last_key: Optional[str] = None
        while heap:
            k, _, idx, e, it = heapq.heappop(heap)
            if k != last_key:  # first occurrence of k is the newest
                last_key = k
                if not (e.tombstone and everything):
                    merged[k] = e
            nxt = next(it, None)
            if nxt is not None:
                heapq.heappush(heap, (nxt[0], -nxt[1].seq, idx, nxt[1], it))
        for t in chosen:
            self._tables.remove(t)
            self.disk.delete(t.file_name)
        if merged:
            self._tables.append(SSTable.write(self.disk, f"sst-{self._next_file_id}.db", merged))
            self._next_file_id += 1
        self.compactions += 1

    def range(self, lo: str, hi: str) -> dict[str, str]:
        """Keys in [lo, hi] with live values: merge of memtable and every table, highest seq wins."""
        merged: dict[str, Entry] = {}

        def merge_in(k: str, e: Entry) -> None:
            cur = merged.get(k)
            if cur is None or e.seq > cur.seq:  # Java: (a, b) -> a.seq() >= b.seq() ? a : b
                merged[k] = e

        for t in self._tables:
            for k, e in t.read_all().items():
                if lo <= k <= hi:
                    merge_in(k, e)
        for k, e in self._memtable.entries().items():
            if lo <= k <= hi:
                merge_in(k, e)
        return {k: merged[k].value for k in sorted(merged) if not merged[k].tombstone}

    def table_count(self) -> int:
        return len(self._tables)

    def tables(self) -> list[SSTable]:
        return list(self._tables)

    def memtable_size(self) -> int:
        return self._memtable.size()

    def write_amplification(self) -> float:
        """Bytes written to disk divided by bytes the user wrote: the cost of compaction."""
        return 0.0 if self.user_bytes_written == 0 else self.disk.bytes_written / self.user_bytes_written

    def bloom_negatives(self) -> int:
        return sum(t.bloom_negatives for t in self._tables)

    def range_negatives(self) -> int:
        return sum(t.range_negatives for t in self._tables)

    def block_reads(self) -> int:
        return sum(t.block_reads for t in self._tables)

    def check_invariants(self) -> bool:
        """No empty tables; no full tier after compaction; disk holds exactly the live tables."""
        for t in self._tables:
            if t.entry_count == 0:
                raise IllegalStateError(f"empty table {t.file_name}")
        if self._full_tier() is not None:
            raise IllegalStateError("a tier is still full after compaction")
        if self.disk.file_count() != len(self._tables):
            raise IllegalStateError(f"disk files {self.disk.file_count()} != tables {len(self._tables)}")
        return True


def main() -> None:
    rng = JavaRandom(42)
    disk = SimulatedDisk()
    lsm = LsmTree(disk, 1_000, 4)

    print("== Payment status store: 20,000 payments, each updated three times ==")
    for state in ("created", "authorized", "captured"):
        for i in range(20_000):
            lsm.put(f"pay_{i:06d}", f"{state}@{rng.next_int(1_000_000)}")
    print(f"flushes={lsm.flushes} compactions={lsm.compactions} tables={lsm.table_count()} "
          f"memtable={lsm.memtable_size()} entries")
    print(f"disk: {disk.file_count()} files, {disk.total_bytes():,} bytes; user wrote {lsm.user_bytes_written:,} bytes; "
          f"write amplification {fmt_fixed(lsm.write_amplification(), 2)}x")
    print(f"table sizes (entries): {java_str([t.entry_count for t in lsm.tables()])}")

    print()
    print("== Read path: highest seq wins, Bloom filters skip the rest ==")
    v = lsm.get("pay_000042")
    print(f"pay_000042 -> {java_str(v)}  (the third write; earlier versions are shadowed)")
    before, blocks_before = lsm.tables_consulted, lsm.block_reads()
    for i in range(1_000):
        lsm.get(f"pay_{i:06d}x")  # never written
    print(f"1000 misses consulted {lsm.tables_consulted - before} tables: {lsm.range_negatives()} rejected by key range, "
          f"{lsm.bloom_negatives()} by Bloom, {lsm.block_reads() - blocks_before} block reads (false positives)")

    print()
    print("== Deletes are writes: tombstones ==")
    lsm.delete("pay_000042")
    print(f"after delete, pay_000042 -> {java_str(lsm.get('pay_000042'))} (tombstone in the memtable shadows the table)")
    lsm.flush()
    print(f"after flush, pay_000042 -> {java_str(lsm.get('pay_000042'))} (tombstone now in the newest table)")

    print()
    print("== Range scan merges every level ==")
    print(f"pay_000040..pay_000044 -> {java_str(list(lsm.range('pay_000040', 'pay_000044')))}  (42 is gone)")

    print()
    print("== Forcing compaction down to one table drops the tombstone ==")
    disk2 = SimulatedDisk()
    small = LsmTree(disk2, 4, 2)
    for k, val in (("a", "1"), ("b", "2"), ("c", "3"), ("d", "4")):  # flush 1
        small.put(k, val)
    small.delete("b")                                                  # flush 2 -> compaction
    for k, val in (("e", "5"), ("f", "6"), ("g", "7")):
        small.put(k, val)
    print(f"tables={small.table_count()}, keys={java_str(list(small.range('a', 'z')))}, b -> {java_str(small.get('b'))}")
    print(f"files written={disk2.files_written} deleted={disk2.files_deleted} (compaction rewrote, then removed the inputs)")


if __name__ == "__main__":
    main()
