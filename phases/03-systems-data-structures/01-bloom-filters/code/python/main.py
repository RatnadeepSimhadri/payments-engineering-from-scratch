"""Lesson: phases/03-systems-data-structures/01-bloom-filters/docs/en.md
Bloom Filters from Scratch
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Bloom (1970), CACM 13(7); Kirsch & Mitzenmacher (2006), "Less Hashing, Same Performance";
FNV-1a (Fowler/Noll/Vo, IETF draft-eastlake-fnv); Swamidass & Baldi (2007) for the cardinality estimate.
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

import math

MASK64 = (1 << 64) - 1
FNV_OFFSET = 0xCBF29CE484222325
FNV_PRIME = 0x100000001B3


def fnv1a64(data: bytes) -> int:
    """FNV-1a, 64-bit: xor the byte in, then multiply by the prime. Returns an unsigned 64-bit int."""
    h = FNV_OFFSET
    for b in data:
        h ^= b
        h = (h * FNV_PRIME) & MASK64
    return h


def mix64(h: int) -> int:
    """MurmurHash3's fmix64 finalizer: a bijection on 64-bit words that spreads every input bit
    to every output bit. Used to derive the second hash from the first (double hashing)."""
    h &= MASK64
    h ^= h >> 33
    h = (h * 0xFF51AFD7ED558CCD) & MASK64
    h ^= h >> 33
    h = (h * 0xC4CEB9FE1A85EC53) & MASK64
    h ^= h >> 33
    return h


class BloomFilter:
    """A fixed-size bit array with k hash functions. Answers "definitely not present" or
    "possibly present"; never "definitely present". No deletes."""

    def __init__(self, bits: int, hashes: int) -> None:
        if bits <= 0:
            raise ValueError(f"bits must be positive: {bits}")
        if hashes <= 0:
            raise ValueError(f"hashes must be positive: {hashes}")
        self.m = bits
        self.k = hashes
        self._words = bytearray((bits + 7) // 8)
        self.insertions = 0

    # ----- sizing -------------------------------------------------------------------------

    @staticmethod
    def optimal_bits(expected_items: int, false_positive_rate: float) -> int:
        """m = -n ln p / (ln 2)^2, rounded up."""
        if expected_items <= 0:
            raise ValueError("expected_items must be positive")
        if not 0.0 < false_positive_rate < 1.0:
            raise ValueError("false_positive_rate must be in (0, 1)")
        return int(math.ceil(-expected_items * math.log(false_positive_rate) / (math.log(2) ** 2)))

    @staticmethod
    def optimal_hashes(bits: int, expected_items: int) -> int:
        """k = (m / n) ln 2, rounded to the nearest integer, at least 1."""
        return max(1, round(bits / expected_items * math.log(2)))

    @staticmethod
    def expected_false_positive_rate(bits: int, hashes: int, items: int) -> float:
        """p = (1 - e^(-kn/m))^k: the classic approximation, exact enough for sizing."""
        if items == 0:
            return 0.0
        return (1.0 - math.exp(-hashes * items / bits)) ** hashes

    @classmethod
    def create(cls, expected_items: int, false_positive_rate: float) -> "BloomFilter":
        bits = cls.optimal_bits(expected_items, false_positive_rate)
        return cls(bits, cls.optimal_hashes(bits, expected_items))

    # ----- hashing -----------------------------------------------------------------------

    def indexes(self, key: bytes) -> list[int]:
        """The k bit positions for a key: index_i = (h1 + i * h2) mod m, Kirsch-Mitzenmacher
        double hashing. Two 64-bit hashes stand in for k independent ones."""
        h1 = fnv1a64(key)
        h2 = mix64(h1)
        return [((h1 + i * h2) & MASK64) % self.m for i in range(self.k)]

    # ----- membership ----------------------------------------------------------------------

    def add(self, key: bytes | str) -> bool:
        """Set the k bits. Returns True if any bit changed (the key was definitely new),
        False if every bit was already set (the key is possibly a duplicate)."""
        data = key.encode("utf-8") if isinstance(key, str) else key
        changed = False
        for idx in self.indexes(data):
            byte, bit = divmod(idx, 8)
            mask = 1 << bit
            if not self._words[byte] & mask:
                self._words[byte] |= mask
                changed = True
        self.insertions += 1
        return changed

    def might_contain(self, key: bytes | str) -> bool:
        """False means definitely never added. True means possibly added."""
        data = key.encode("utf-8") if isinstance(key, str) else key
        for idx in self.indexes(data):
            byte, bit = divmod(idx, 8)
            if not self._words[byte] & (1 << bit):
                return False
        return True

    # ----- introspection -------------------------------------------------------------------

    def bit_count(self) -> int:
        """Number of set bits (the filter's fill)."""
        return sum(bin(b).count("1") for b in self._words)

    def fill_ratio(self) -> float:
        return self.bit_count() / self.m

    def estimated_false_positive_rate(self) -> float:
        """The formula applied to this filter's own insertion count."""
        return self.expected_false_positive_rate(self.m, self.k, self.insertions)

    def approximate_count(self) -> int:
        """Swamidass & Baldi: n* = -(m / k) ln(1 - X / m) where X is the number of set bits.
        Lets a filter rebuilt from a snapshot report how full it is without a counter."""
        x = self.bit_count()
        if x >= self.m:
            return self.m  # saturated; the estimate is unbounded
        return int(round(-(self.m / self.k) * math.log(1.0 - x / self.m)))

    def union(self, other: "BloomFilter") -> "BloomFilter":
        """OR two filters built with identical m and k. The result answers for both key sets."""
        if self.m != other.m or self.k != other.k:
            raise ValueError(f"cannot union filters with different parameters: ({self.m},{self.k}) vs ({other.m},{other.k})")
        result = BloomFilter(self.m, self.k)
        result._words = bytearray(a | b for a, b in zip(self._words, other._words))
        result.insertions = self.insertions + other.insertions
        return result

    def bits_per_item(self, items: int) -> float:
        return self.m / items

    def __repr__(self) -> str:
        return f"BloomFilter(m={self.m}, k={self.k}, insertions={self.insertions}, set_bits={self.bit_count()})"


def measure_false_positive_rate(bf: BloomFilter, inserted: int, probes: int) -> float:
    """Insert `inserted` synthetic keys, then probe `probes` keys that were never inserted.
    Deterministic: keys are 'k-<i>' and 'q-<i>', so every language measures the same number."""
    for i in range(inserted):
        bf.add(f"k-{i}")
    positives = sum(1 for i in range(probes) if bf.might_contain(f"q-{i}"))
    return positives / probes


def main() -> None:
    print("== Sizing a filter for 10,000 idempotency keys at 1% false positives ==")
    n, p = 10_000, 0.01
    m = BloomFilter.optimal_bits(n, p)
    k = BloomFilter.optimal_hashes(m, n)
    print(f"m = {m} bits ({m / 8 / 1024:.1f} KiB), k = {k} hashes, {m / n:.2f} bits per key")
    print(f"formula predicts p = {BloomFilter.expected_false_positive_rate(m, k, n):.5f}")

    print("\n== Where a key lands ==")
    bf = BloomFilter(1000, 3)
    key = b"idem-key-1"
    print(f"fnv1a64({key!r}) = 0x{fnv1a64(key):016x}")
    print(f"mix64(h1)          = 0x{mix64(fnv1a64(key)):016x}")
    print(f"indexes in m=1000, k=3: {bf.indexes(key)}")

    print("\n== Definitely not seen vs possibly seen ==")
    idem = BloomFilter.create(n, p)
    for req in ("pay_7f3a", "pay_7f3b", "pay_7f3c"):
        print(f"add({req!r}) -> changed bits: {idem.add(req)}")
    for req in ("pay_7f3a", "pay_9999"):
        verdict = "possibly seen -> check the database" if idem.might_contain(req) else "definitely new -> skip the lookup"
        print(f"might_contain({req!r}) = {idem.might_contain(req)}: {verdict}")
    print(f"re-adding 'pay_7f3a' changes bits: {idem.add('pay_7f3a')}")

    print("\n== Measured false-positive rate against the formula ==")
    probe = BloomFilter.create(n, p)
    measured = measure_false_positive_rate(probe, n, 50_000)
    print(f"{probe}")
    print(f"fill ratio = {probe.fill_ratio():.4f} (theory: 1 - e^(-kn/m) = {1 - math.exp(-k * n / m):.4f})")
    print(f"measured p = {measured:.5f}, formula p = {probe.estimated_false_positive_rate():.5f}")
    print(f"approximate_count = {probe.approximate_count()} (actual {n})")

    print("\n== What happens when you overfill it ==")
    for factor in (1, 2, 5, 10):
        over = BloomFilter(m, k)
        for i in range(n * factor):
            over.add(f"k-{i}")
        print(f"{factor:>2}x capacity: fill = {over.fill_ratio():.3f}, predicted p = {over.estimated_false_positive_rate():.4f}")

    print("\n== Union of two shards' filters ==")
    a, b = BloomFilter(4096, 4), BloomFilter(4096, 4)
    a.add("shard-a-key")
    b.add("shard-b-key")
    both = a.union(b)
    print(f"union might_contain shard-a-key: {both.might_contain('shard-a-key')}, shard-b-key: {both.might_contain('shard-b-key')}")
    try:
        a.union(BloomFilter(2048, 4))
    except ValueError as exc:
        print(f"rejected: {exc}")


if __name__ == "__main__":
    main()
