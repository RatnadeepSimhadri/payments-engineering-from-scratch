"""Lesson: phases/07-storage-and-transactions/10-secondary-indexes/docs/en.md
Secondary Indexes: Local vs Global over a partitioned payment store.
Run:  python3 main.py
Test: python3 -m unittest -v
Python 3.11+, standard library only. Hashing: FNV-1a 64-bit (Fowler/Noll/Vo, IETF draft-eastlake-fnv).
Design references: DynamoDB LSI/GSI documentation; Kleppmann, Designing Data-Intensive Applications, ch. 6.
"""

from __future__ import annotations

from collections import deque
from dataclasses import dataclass
from enum import Enum

MASK64 = 0xFFFFFFFFFFFFFFFF
INT64_MIN, INT64_MAX = -(1 << 63), (1 << 63) - 1
INT32_MAX = (1 << 31) - 1


def to_int64(x: int) -> int:
    """Reinterpret the low 64 bits as a signed Java long."""
    x &= MASK64
    return x - (1 << 64) if x >= (1 << 63) else x


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: three-letter code and minor-unit exponent. Copied from Phase 10, Lesson 01."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3 or not all(c.isupper() for c in self.code):
            raise ValueError(f"currency code must be three uppercase letters: {self.code}")
        if self.exponent < 0 or self.exponent > 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


Currency.USD = Currency("USD", 2)
Currency.EUR = Currency("EUR", 2)


@dataclass(frozen=True)
class Money:
    """Integer minor units plus a currency. Never a float."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")
        if self.currency is None:
            raise TypeError("currency")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    def plus(self, other: "Money") -> "Money":
        if self.currency != other.currency:
            raise ValueError(f"cannot combine {self.currency.code} with {other.currency.code}")
        r = self.minor_units + other.minor_units
        if r < INT64_MIN or r > INT64_MAX:  # Math.addExact
            raise OverflowError("long overflow")
        return Money(r, self.currency)

    def __str__(self) -> str:
        scale = self.currency.minor_units_per_major()
        a = abs(self.minor_units)
        sign = "-" if self.minor_units < 0 else ""
        if self.currency.exponent == 0:
            return f"{sign}{a // scale} {self.currency.code}"
        return f"{sign}{a // scale}.{a % scale:0{self.currency.exponent}d} {self.currency.code}"


@dataclass(frozen=True)
class Payment:
    """One row of the primary table. The primary key is id; customer_id is the indexed attribute."""

    id: str
    customer_id: str
    merchant_id: str
    amount: Money
    created_at: int

    def __post_init__(self) -> None:
        for name in ("id", "customer_id", "merchant_id", "amount"):
            if getattr(self, name) is None:
                raise TypeError(name)  # Java: Objects.requireNonNull -> NullPointerException


@dataclass(frozen=True, order=True)
class Locator:
    """Where a row lives: which shard, and its primary key there. Ordered by (shard_id, payment_id)."""

    shard_id: int
    payment_id: str


class IndexMode(Enum):
    SYNC = "SYNC"  # the index partition is written inside the same request as the row
    ASYNC = "ASYNC"  # the index change is queued and applied later; the index lags by the queue depth


@dataclass(frozen=True)
class IndexUpdate:
    """One change to the global index. delete=False inserts the locator under the customer; True removes it."""

    seq: int
    customer_id: str
    locator: Locator
    delete: bool


@dataclass(frozen=True)
class QueryResult:
    """The answer to a query plus what it cost: shard requests and index-partition requests."""

    payments: tuple[Payment, ...]
    shard_requests: int
    index_requests: int

    def total_requests(self) -> int:
        return self.shard_requests + self.index_requests


@dataclass(frozen=True)
class IndexAudit:
    entries: int
    dangling: list[Locator]
    missing: list[Locator]

    def consistent(self) -> bool:
        return not self.dangling and not self.missing


class Hashing:
    """Deterministic 64-bit FNV-1a over UTF-8 bytes, so every language port routes a key to the same shard."""

    FNV_OFFSET = 0xCBF29CE484222325
    FNV_PRIME = 0x100000001B3

    @staticmethod
    def fnv1a64(key: str) -> int:
        """Returned as the signed 64-bit value Java's long holds."""
        h = Hashing.FNV_OFFSET
        for b in key.encode("utf-8"):
            h ^= b
            h = (h * Hashing.FNV_PRIME) & MASK64
        return to_int64(h)

    @staticmethod
    def partition_for(key: str, partitions: int) -> int:
        # Math.floorMod on the signed long: Python's % with a positive divisor already floors.
        return Hashing.fnv1a64(key) % partitions


class Shard:
    """One shard of the primary table with a LOCAL secondary index updated in the same write as the row.

    Every method a client would call over the network increments requests.
    """

    def __init__(self, id: int) -> None:
        self.id = id
        self._rows: dict[str, Payment] = {}
        self._by_customer: dict[str, set[str]] = {}
        self._requests = 0

    def requests(self) -> int:
        return self._requests

    def size(self) -> int:
        return len(self._rows)

    def put(self, p: Payment) -> Payment | None:
        """Insert or replace. Maintains the local index atomically with the row (single-threaded, same call)."""
        self._requests += 1
        previous = self._rows.get(p.id)
        self._rows[p.id] = p
        if previous is not None and previous.customer_id != p.customer_id:
            self._unindex(previous)
        self._by_customer.setdefault(p.customer_id, set()).add(p.id)
        return previous

    def get(self, payment_id: str) -> Payment | None:
        self._requests += 1
        return self._rows.get(payment_id)

    def get_many(self, ids: list[str]) -> list[Payment]:
        """Fetch several rows in one request: the batch-get a client would issue to one node."""
        self._requests += 1
        return [self._rows[i] for i in ids if i in self._rows]

    def delete(self, payment_id: str) -> Payment | None:
        self._requests += 1
        removed = self._rows.pop(payment_id, None)
        if removed is not None:
            self._unindex(removed)
        return removed

    def lookup_customer(self, customer_id: str) -> list[Payment]:
        """Local index lookup: only rows that live on this shard, in id order."""
        self._requests += 1
        return [self._rows[i] for i in sorted(self._by_customer.get(customer_id, ()))]

    def scan(self) -> list[Payment]:
        """Full scan, used only to bootstrap a global index."""
        self._requests += 1
        return [self._rows[k] for k in sorted(self._rows)]

    def _unindex(self, p: Payment) -> None:
        ids = self._by_customer.get(p.customer_id)
        if ids is not None:
            ids.discard(p.id)
            if not ids:
                del self._by_customer[p.customer_id]


class GlobalIndex:
    """GLOBAL secondary index, partitioned by the indexed attribute (customer_id), not by the primary key."""

    def __init__(self, partition_count: int) -> None:
        if partition_count <= 0:
            raise ValueError("need at least one index partition")
        self._partitions: list[dict[str, set[Locator]]] = [{} for _ in range(partition_count)]
        self._requests = [0] * partition_count

    def partition_count(self) -> int:
        return len(self._partitions)

    def partition_for(self, customer_id: str) -> int:
        return Hashing.partition_for(customer_id, len(self._partitions))

    def requests(self, partition: int) -> int:
        return self._requests[partition]

    def total_requests(self) -> int:
        return sum(self._requests)

    def apply(self, u: IndexUpdate) -> None:
        p = self.partition_for(u.customer_id)
        self._requests[p] += 1
        part = self._partitions[p]
        if u.delete:
            s = part.get(u.customer_id)
            if s is not None:
                s.discard(u.locator)
                if not s:
                    del part[u.customer_id]
        else:
            part.setdefault(u.customer_id, set()).add(u.locator)

    def lookup(self, customer_id: str) -> list[Locator]:
        """One request to one partition. Returns locators in (shard_id, payment_id) order."""
        p = self.partition_for(customer_id)
        self._requests[p] += 1
        return sorted(self._partitions[p].get(customer_id, ()))

    def entry_count(self) -> int:
        return sum(len(s) for part in self._partitions for s in part.values())

    def all_entries(self) -> dict[Locator, str]:
        """Every (locator, customer) pair in the index, in locator order, for auditing."""
        out = {loc: cust for part in self._partitions for cust, s in part.items() for loc in s}
        return dict(sorted(out.items()))

    def clear(self) -> None:
        for part in self._partitions:
            part.clear()


class PartitionedStore:
    """N shards keyed by payment id, each with a local index, plus one global index partitioned by customer id."""

    def __init__(self, shard_count: int, index_partitions: int, mode: IndexMode) -> None:
        if shard_count <= 0:
            raise ValueError("need at least one shard")
        self._shards = [Shard(i) for i in range(shard_count)]
        self._global_index = GlobalIndex(index_partitions)
        if mode is None:
            raise TypeError("mode")
        self._mode = mode
        self._pending: deque[IndexUpdate] = deque()
        self._next_seq = 1

    def shard_count(self) -> int:
        return len(self._shards)

    def shard(self, id: int) -> Shard:
        return self._shards[id]

    def global_index(self) -> GlobalIndex:
        return self._global_index

    def mode(self) -> IndexMode:
        return self._mode

    def pending_index_updates(self) -> int:
        return len(self._pending)

    def shard_for(self, payment_id: str) -> int:
        return Hashing.partition_for(payment_id, len(self._shards))

    def total_shard_requests(self) -> int:
        return sum(s.requests() for s in self._shards)

    def _seq(self) -> int:
        s = self._next_seq
        self._next_seq += 1
        return s

    def put(self, p: Payment) -> None:
        """Write path. One shard write always; the global index write is inline (SYNC) or queued (ASYNC)."""
        shard_id = self.shard_for(p.id)
        previous = self._shards[shard_id].put(p)
        if previous is not None and previous.customer_id != p.customer_id:
            self._emit(IndexUpdate(self._seq(), previous.customer_id, Locator(shard_id, p.id), True))
        if previous is None or previous.customer_id != p.customer_id:
            self._emit(IndexUpdate(self._seq(), p.customer_id, Locator(shard_id, p.id), False))

    def delete(self, payment_id: str) -> bool:
        shard_id = self.shard_for(payment_id)
        removed = self._shards[shard_id].delete(payment_id)
        if removed is None:
            return False
        self._emit(IndexUpdate(self._seq(), removed.customer_id, Locator(shard_id, payment_id), True))
        return True

    def _emit(self, u: IndexUpdate) -> None:
        if self._mode is IndexMode.SYNC:
            self._global_index.apply(u)
        else:
            self._pending.append(u)

    def drain_index_updates(self, max: int) -> int:
        """Apply up to max queued index updates in order. Returns how many were applied."""
        applied = 0
        while applied < max and self._pending:
            self._global_index.apply(self._pending.popleft())
            applied += 1
        return applied

    def drain_all(self) -> int:
        return self.drain_index_updates(INT32_MAX)

    def get(self, payment_id: str) -> Payment | None:
        """Primary-key lookup: exactly one shard request."""
        return self._shards[self.shard_for(payment_id)].get(payment_id)

    def find_by_customer_scatter_gather(self, customer_id: str) -> QueryResult:
        """Strategy A: ask every shard's local index and merge. Cost is always N shard requests."""
        merged: list[Payment] = []
        for s in self._shards:
            merged.extend(s.lookup_customer(customer_id))
        merged.sort(key=lambda p: (p.created_at, p.id))
        return QueryResult(tuple(merged), len(self._shards), 0)

    def find_by_customer_global(self, customer_id: str) -> QueryResult:
        """Strategy B: one index-partition request, then one batch request per distinct shard holding a hit.

        Locators whose row has vanished (an index entry that outlived its row) are filtered out, never returned.
        """
        by_shard: dict[int, list[str]] = {}
        for loc in self._global_index.lookup(customer_id):
            by_shard.setdefault(loc.shard_id, []).append(loc.payment_id)
        merged: list[Payment] = []
        for shard_id, ids in by_shard.items():
            merged.extend(self._shards[shard_id].get_many(ids))
        merged.sort(key=lambda p: (p.created_at, p.id))
        return QueryResult(tuple(merged), len(by_shard), 1)

    def rebuild_global_index(self) -> int:
        """Bootstrap or repair: throw the index away and rebuild it from a scan of every shard."""
        self._global_index.clear()
        self._pending.clear()
        entries = 0
        for s in self._shards:
            for p in s.scan():
                self._global_index.apply(IndexUpdate(self._seq(), p.customer_id, Locator(s.id, p.id), False))
                entries += 1
        return entries

    def audit_global_index(self) -> IndexAudit:
        """Dangling = in index, row gone or re-attributed. Missing = row exists, no entry."""
        indexed = self._global_index.all_entries()
        dangling: list[Locator] = []
        missing: list[Locator] = []
        for loc, cust in indexed.items():
            row = self._shards[loc.shard_id].get(loc.payment_id)
            if row is None or row.customer_id != cust:
                dangling.append(loc)
        for s in self._shards:
            for p in s.scan():
                loc = Locator(s.id, p.id)
                if p.customer_id != indexed.get(loc):
                    missing.append(loc)
        return IndexAudit(len(indexed), dangling, missing)


SPEC = [
    ("pay_001", "cust_alice", "m_books", 1999, 1),
    ("pay_002", "cust_bob", "m_books", 4500, 2),
    ("pay_003", "cust_alice", "m_coffee", 475, 3),
    ("pay_004", "cust_carol", "m_coffee", 650, 4),
    ("pay_005", "cust_alice", "m_rides", 1250, 5),
    ("pay_006", "cust_bob", "m_rides", 2300, 6),
    ("pay_007", "cust_dave", "m_books", 9999, 7),
    ("pay_008", "cust_alice", "m_grocer", 8210, 8),
    ("pay_009", "cust_carol", "m_books", 1500, 9),
    ("pay_010", "cust_erin", "m_coffee", 525, 10),
    ("pay_011", "cust_alice", "m_coffee", 480, 11),
    ("pay_012", "cust_bob", "m_grocer", 6100, 12),
]


def sample_data() -> list[Payment]:
    return [Payment(i, c, m, Money.of_minor(cents, Currency.USD), at) for i, c, m, cents, at in SPEC]


def total(payments) -> Money:
    s = Money.of_minor(0, Currency.USD)
    for p in payments:
        s = s.plus(p.amount)
    return s


def placement(store: PartitionedStore, customer_id: str) -> str:
    """Rows of one customer grouped by shard, printed like Java's TreeMap.toString."""
    by_shard: dict[int, list[str]] = {}
    for i in range(store.shard_count()):
        for p in store.shard(i).lookup_customer(customer_id):
            by_shard.setdefault(i, []).append(p.id)
    return "{" + ", ".join(f"{k}=[{', '.join(v)}]" for k, v in sorted(by_shard.items())) + "}"


def _b(flag: bool) -> str:
    return "true" if flag else "false"


def main() -> None:
    print("== Placement: payments hash to shards by payment id, not by customer ==")
    sync = PartitionedStore(4, 2, IndexMode.SYNC)
    for p in sample_data():
        sync.put(p)
    sync_shard_writes = sync.total_shard_requests()
    sync_index_writes = sync.global_index().total_requests()
    for i in range(4):
        print(f"shard {i} holds {sync.shard(i).size()} rows")
    print(f"cust_alice rows by shard: {placement(sync, 'cust_alice')}")
    print(f"cust_erin rows by shard:  {placement(sync, 'cust_erin')}")

    print()
    print("== Query: all payments for cust_alice ==")
    before = sync.total_shard_requests()
    scatter = sync.find_by_customer_scatter_gather("cust_alice")
    print(f"scatter-gather over local indexes: {len(scatter.payments)} rows, {scatter.shard_requests} shard requests, "
          f"{scatter.index_requests} index requests, total {total(scatter.payments)}")
    glob = sync.find_by_customer_global("cust_alice")
    print(f"global index then fetch:           {len(glob.payments)} rows, {glob.shard_requests} shard requests, "
          f"{glob.index_requests} index requests, total {total(glob.payments)}")
    print(f"same answer: {_b(scatter.payments == glob.payments)}; shard requests consumed by both queries: "
          f"{sync.total_shard_requests() - before}")

    print()
    print("== Query: cust_erin, a customer with a single payment ==")
    scatter = sync.find_by_customer_scatter_gather("cust_erin")
    glob = sync.find_by_customer_global("cust_erin")
    print(f"scatter-gather: {scatter.total_requests()} requests; global: {glob.total_requests()} requests "
          f"(1 index + {glob.shard_requests} shard)")

    print()
    print("== Write cost: SYNC index pays on every put ==")
    print(f"SYNC store after 12 puts: shard requests {sync_shard_writes}, index-partition requests {sync_index_writes}")
    asy = PartitionedStore(4, 2, IndexMode.ASYNC)
    for p in sample_data():
        asy.put(p)
    print(f"ASYNC store after 12 puts: shard requests {asy.total_shard_requests()}, index-partition requests "
          f"{asy.global_index().total_requests()}, queued updates {asy.pending_index_updates()}")

    print()
    print("== Staleness window: the row is durable, the index has not heard yet ==")
    fresh = Payment("pay_013", "cust_erin", "m_rides", Money.of_minor(1800, Currency.USD), 13)
    asy.drain_all()
    asy.put(fresh)
    print(f"get(pay_013) by primary key: {'found' if asy.get('pay_013') is not None else 'missing'}")
    print(f"global index query for cust_erin: {len(asy.find_by_customer_global('cust_erin').payments)} rows "
          f"(expected 2)  <- stale, {asy.pending_index_updates()} update(s) queued")
    print(f"scatter-gather for cust_erin:     {len(asy.find_by_customer_scatter_gather('cust_erin').payments)} rows "
          "(local indexes are never stale)")
    asy.drain_all()
    print(f"after drain: global index query for cust_erin: {len(asy.find_by_customer_global('cust_erin').payments)} rows")

    print()
    print("== Dangling entry: delete the row, index update still queued ==")
    asy.delete("pay_013")
    print(f"index entries for cust_erin: {len(asy.global_index().lookup('cust_erin'))}, "
          f"pending updates: {asy.pending_index_updates()}")
    audit = asy.audit_global_index()
    print(f"audit: {audit.entries} entries, {len(audit.dangling)} dangling, {len(audit.missing)} missing")
    print(f"query filters the ghost: {len(asy.find_by_customer_global('cust_erin').payments)} rows returned for cust_erin")
    asy.drain_all()
    audit = asy.audit_global_index()
    print(f"after drain: {len(audit.dangling)} dangling, {len(audit.missing)} missing, "
          f"consistent={_b(audit.consistent())}")

    print()
    print("== Bootstrap: rebuild the global index from a scan of every shard ==")
    rebuilt = asy.rebuild_global_index()
    print(f"rebuilt {rebuilt} entries from {asy.shard_count()} shards; "
          f"consistent={_b(asy.audit_global_index().consistent())}")


if __name__ == "__main__":
    main()
