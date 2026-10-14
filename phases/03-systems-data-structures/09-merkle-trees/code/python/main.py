"""Lesson: phases/03-systems-data-structures/09-merkle-trees/docs/en.md
Merkle Trees and Content-Addressed Verification
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: RFC 6962 (Certificate Transparency: leaf/node domain separation, audit paths); FIPS 180-4 (SHA-256);
CVE-2012-2459 (Bitcoin duplicate-last-node ambiguity). Python 3.11+, standard library only.
"""

from __future__ import annotations

import hashlib
import hmac
from dataclasses import dataclass, field


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3:
            raise ValueError("currency code must be three letters")
        if not 0 <= self.exponent <= 4:
            raise ValueError("exponent must be 0..4")


USD = Currency("USD", 2)
EUR = Currency("EUR", 2)


@dataclass(frozen=True)
class Money:
    """Integer minor units plus a currency. Never a float."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if self.currency is None:
            raise TypeError("currency")
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    def __str__(self) -> str:
        scale = 10 ** self.currency.exponent
        sign = "-" if self.minor_units < 0 else ""
        whole, frac = divmod(abs(self.minor_units), scale)
        if self.currency.exponent == 0:
            return f"{sign}{whole} {self.currency.code}"
        return f"{sign}{whole}.{frac:0{self.currency.exponent}d} {self.currency.code}"


@dataclass(frozen=True)
class Posting:
    """One ledger posting. The leaf is the hash of canonical_bytes(), not of str()."""

    id: str
    debit_account: str
    credit_account: str
    amount: Money

    def canonical_bytes(self) -> bytes:
        canonical = (f"{self.id}|{self.debit_account}|{self.credit_account}|"
                     f"{self.amount.minor_units}|{self.amount.currency.code}")
        return canonical.encode("utf-8")


@dataclass(frozen=True, repr=False)
class ProofStep:
    """One step of an inclusion proof: the sibling hash and which side the sibling sits on."""

    sibling: bytes
    sibling_on_left: bool

    def __repr__(self) -> str:
        return ("L:" if self.sibling_on_left else "R:") + self.sibling.hex()[:8]

    __str__ = __repr__


@dataclass(frozen=True)
class Diff:
    """Result of comparing two trees: which leaf indexes differ, and how many hash comparisons it took."""

    divergent_leaves: list[int] = field(default_factory=list)
    comparisons: int = 0


LEAF_PREFIX = b"\x00"
NODE_PREFIX = b"\x01"


class MerkleTree:
    """Binary hash tree with RFC 6962 domain separation. An odd node is carried up unchanged, never duplicated."""

    LEAF_PREFIX = LEAF_PREFIX
    NODE_PREFIX = NODE_PREFIX

    def __init__(self, leaf_data: list[bytes]) -> None:
        if not leaf_data:
            raise ValueError("a Merkle tree needs at least one leaf")
        # levels[0] is the leaf hashes; levels[-1] has exactly one entry, the root. bytes are immutable,
        # so handing them out needs no defensive copy (Java clones its byte[]).
        self._levels: list[list[bytes]] = [[MerkleTree.hash_leaf(d) for d in leaf_data]]
        current = self._levels[0]
        while len(current) > 1:
            nxt = []
            for i in range(0, len(current), 2):
                if i + 1 < len(current):
                    nxt.append(MerkleTree.hash_node(current[i], current[i + 1]))
                else:
                    nxt.append(current[i])  # odd node: carried up, not duplicated
            self._levels.append(nxt)
            current = nxt

    @staticmethod
    def of_postings(postings: list[Posting]) -> "MerkleTree":
        return MerkleTree([p.canonical_bytes() for p in postings])

    @staticmethod
    def hash_leaf(data: bytes) -> bytes:
        md = hashlib.sha256()
        md.update(LEAF_PREFIX)
        md.update(data)
        return md.digest()

    @staticmethod
    def hash_node(left: bytes, right: bytes) -> bytes:
        md = hashlib.sha256()
        md.update(NODE_PREFIX)
        md.update(left)
        md.update(right)
        return md.digest()

    def root(self) -> bytes:
        return self._levels[-1][0]

    def root_hex(self) -> str:
        return self.root().hex()

    def leaf_count(self) -> int:
        return len(self._levels[0])

    def height(self) -> int:
        return len(self._levels)

    def leaf_hash(self, index: int) -> bytes:
        return self._levels[0][index]

    def proof(self, index: int) -> list[ProofStep]:
        """Audit path for leaf index: one sibling per level where the node has a sibling."""
        if not 0 <= index < self.leaf_count():  # Objects.checkIndex; Python would accept negative indexes
            raise IndexError(f"Index {index} out of bounds for length {self.leaf_count()}")
        path = []
        i = index
        for level in range(len(self._levels) - 1):
            nodes = self._levels[level]
            sibling = i + 1 if i % 2 == 0 else i - 1
            if sibling < len(nodes):
                path.append(ProofStep(nodes[sibling], sibling < i))
            i //= 2
        return path

    @staticmethod
    def verify(leaf_data: bytes, proof: list[ProofStep], expected_root: bytes) -> bool:
        """Recompute the root from a leaf's data and its audit path; constant-time compare with the expected root."""
        h = MerkleTree.hash_leaf(leaf_data)
        for step in proof:
            h = MerkleTree.hash_node(step.sibling, h) if step.sibling_on_left else MerkleTree.hash_node(h, step.sibling)
        return hmac.compare_digest(h, expected_root)

    def diff(self, other: "MerkleTree") -> Diff:
        """Every leaf index where the trees disagree, descending only into subtrees whose hashes differ."""
        if other.leaf_count() != self.leaf_count():
            raise ValueError(f"cannot diff trees with {self.leaf_count()} and {other.leaf_count()} leaves")
        out: list[int] = []
        comparisons = [0]
        self._diff_node(other, len(self._levels) - 1, 0, out, comparisons)
        return Diff(out, comparisons[0])

    def _diff_node(self, other: "MerkleTree", level: int, index: int, out: list[int], comparisons: list[int]) -> None:
        comparisons[0] += 1
        if self._levels[level][index] == other._levels[level][index]:
            return
        if level == 0:
            out.append(index)
            return
        below = len(self._levels[level - 1])
        left, right = 2 * index, 2 * index + 1
        if right >= below:  # this node was carried up: one child only
            self._diff_node(other, level - 1, left, out, comparisons)
            return
        self._diff_node(other, level - 1, left, out, comparisons)
        self._diff_node(other, level - 1, right, out, comparisons)


def sample_postings(n: int) -> list[Posting]:
    accounts = ["alice", "bob", "carol", "dave"]
    postings = []
    for i in range(n):
        frm = accounts[i % len(accounts)]
        to = accounts[(i + 1) % len(accounts)]
        minor = 1250 + 37 * i  # deterministic, looks like real cents
        postings.append(Posting(f"txn-{i + 1:04d}", frm, to, Money.of_minor(minor, USD)))
    return postings


def java_list(xs: list) -> str:
    return "[" + ", ".join(str(x) for x in xs) + "]"


def java_bool(b: bool) -> str:
    return "true" if b else "false"


def main() -> None:
    print("== 1. Eight postings, one root ==")
    primary = sample_postings(8)
    primary_tree = MerkleTree.of_postings(primary)
    for p in primary:
        print(f"  {p.id} {p.debit_account} -> {p.credit_account} {p.amount}")
    print(f"  root = {primary_tree.root_hex()}")
    print(f"  height = {primary_tree.height()} levels for {primary_tree.leaf_count()} leaves")

    print()
    print("== 2. The replica agrees: one 32-byte comparison instead of eight rows ==")
    replica_same = MerkleTree.of_postings(sample_postings(8))
    print(f"  roots equal = {java_bool(primary_tree.root() == replica_same.root())}")

    print()
    print("== 3. The replica is one cent off on txn-0006 ==")
    replica = list(primary)
    bad = replica[5]
    replica[5] = Posting(bad.id, bad.debit_account, bad.credit_account,
                         Money.of_minor(bad.amount.minor_units + 1, USD))
    replica_tree = MerkleTree.of_postings(replica)
    print(f"  primary root = {primary_tree.root_hex()[:16]}...")
    print(f"  replica root = {replica_tree.root_hex()[:16]}...")
    d = primary_tree.diff(replica_tree)
    print(f"  divergent leaves = {java_list(d.divergent_leaves)} ({primary[5].id})")
    print(f"  hash comparisons = {d.comparisons} (a row-by-row compare would read all 8 rows on both sides)")
    print(f"  primary says {primary[5].amount}, replica says {replica[5].amount}")

    print()
    print("== 4. Inclusion proof: txn-0003 is in the signed daily root ==")
    proof = primary_tree.proof(2)
    print(f"  proof length = {len(proof)} siblings {java_list(proof)}")
    ok = MerkleTree.verify(primary[2].canonical_bytes(), proof, primary_tree.root())
    print(f"  verify(txn-0003, proof, root) = {java_bool(ok)}")
    forged = Posting("txn-0003", "alice", "bob", Money.of_minor(999_99, USD))
    print(f"  verify(forged txn-0003 with 999.99 USD) = "
          f"{java_bool(MerkleTree.verify(forged.canonical_bytes(), proof, primary_tree.root()))}")

    print()
    print("== 5. Odd leaf counts: carry up, never duplicate ==")
    five = MerkleTree.of_postings(sample_postings(5))
    print(f"  5 leaves: height {five.height()}, proof lengths "
          f"{len(five.proof(0))},{len(five.proof(3))},{len(five.proof(4))} (leaf 4 is carried up twice)")
    six = sample_postings(5)
    six.append(six[4])
    six_dup = MerkleTree.of_postings(six)
    print(f"  [p0..p4] vs [p0..p4,p4] roots equal = {java_bool(five.root() == six_dup.root())}"
          "  <- duplication would make this true (CVE-2012-2459)")

    print()
    print("== 6. Scale: 1,000,000 postings disagree on one row ==")
    n = 1_000_000
    hashes_per_side, level_size = 0, n
    while level_size > 1:
        hashes_per_side += level_size // 2
        level_size = (level_size + 1) // 2
    height, lvl = 1, n
    while lvl > 1:
        lvl = (lvl + 1) // 2
        height += 1
    print(f"  tree height = {height} levels, internal hashes to build = {hashes_per_side}")
    print(f"  hashes exchanged to locate one bad row ~ 2 per level = {2 * (height - 1)}, "
          f"versus {n} rows shipped for a full compare")


if __name__ == "__main__":
    main()
