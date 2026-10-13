"""Lesson: phases/01-complexity-and-core-structures/13-tries/docs/en.md
Tries and Prefix Trees
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Fredkin, "Trie Memory" (CACM 1960); ISO/IEC 7812-1 (issuer identification numbers, PAN length 8-19
         digits); Mastercard 2-series BIN range 2221-2720 (public announcement, 2017).
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Generic, Optional, TypeVar

V = TypeVar("V")


@dataclass(frozen=True)
class Match(Generic[V]):
    """The result of a longest-prefix lookup: which stored prefix matched and the value stored under it."""

    prefix: str
    value: V


@dataclass
class _Node(Generic[V]):
    children: dict[str, "_Node[V]"] = field(default_factory=dict)
    terminal: bool = False
    value: Optional[V] = None


class Trie(Generic[V]):
    """A trie keyed by strings, one node per character. Invariant: every stored key corresponds to exactly one
    root-to-node path whose final node is terminal; every node that is neither terminal nor an ancestor of a
    terminal node has been pruned. Children are iterated in sorted order so results are deterministic."""

    def __init__(self) -> None:
        self._root: _Node[V] = _Node()
        self._size = 0        # number of keys
        self._node_count = 1  # including the root

    def insert(self, key: str, value: V) -> Optional[V]:
        """Insert or replace. Returns the previous value for this exact key, or None."""
        if key is None or value is None:
            raise TypeError("key and value must not be None")
        node = self._root
        for c in key:
            nxt = node.children.get(c)
            if nxt is None:
                nxt = _Node()
                node.children[c] = nxt
                self._node_count += 1
            node = nxt
        old = node.value if node.terminal else None
        if not node.terminal:
            self._size += 1
        node.terminal = True
        node.value = value
        return old

    def search(self, key: str) -> Optional[V]:
        """Exact-match lookup: the value stored under this key, or None. "41" does not match a stored "411"."""
        node = self._walk(key)
        return node.value if node is not None and node.terminal else None

    def contains(self, key: str) -> bool:
        return self.search(key) is not None

    def starts_with(self, prefix: str) -> bool:
        """True if any stored key begins with this prefix (including the prefix itself)."""
        return self._walk(prefix) is not None

    def longest_prefix_match(self, query: str) -> Optional[Match[V]]:
        """The longest stored key that is a prefix of the query, with its value. Walks the query once, remembering
        the deepest terminal node seen, so a stored "4" and a stored "4111" both match "41111..." and "4111" wins."""
        node = self._root
        best: Optional[Match[V]] = Match("", node.value) if node.terminal else None
        for i, c in enumerate(query):
            node = node.children.get(c)
            if node is None:
                break
            if node.terminal:
                best = Match(query[: i + 1], node.value)
        return best

    def delete(self, key: str) -> bool:
        """Remove a key. Returns False if it was not stored. Prunes every node on the path that is left with no
        children and no terminal mark, so shared prefixes of other keys are untouched and nothing leaks."""
        path = [self._root]
        node = self._root
        for c in key:
            node = node.children.get(c)
            if node is None:
                return False
            path.append(node)
        if not node.terminal:
            return False
        node.terminal = False
        node.value = None
        self._size -= 1
        for i in range(len(key), 0, -1):
            child = path[i]
            if child.terminal or child.children:
                break
            del path[i - 1].children[key[i - 1]]
            self._node_count -= 1
        return True

    def keys_with_prefix(self, prefix: str) -> list[str]:
        """Every stored key that begins with the prefix, in sorted order. The autocomplete operation."""
        out: list[str] = []
        start = self._walk(prefix)
        if start is not None:
            self._collect(start, prefix, out)
        return out

    def _collect(self, node: _Node[V], acc: str, out: list[str]) -> None:
        if node.terminal:
            out.append(acc)
        for c in sorted(node.children):
            self._collect(node.children[c], acc + c, out)

    def _walk(self, s: str) -> Optional[_Node[V]]:
        node: Optional[_Node[V]] = self._root
        for c in s:
            if node is None:
                return None
            node = node.children.get(c)
        return node

    def size(self) -> int:
        return self._size

    def node_count(self) -> int:
        return self._node_count


@dataclass(frozen=True)
class Route:
    """Where a card number routes: the network that owns the BIN, the issuer if known, and the prefix that decided it."""

    network: str
    issuer: str
    matched_prefix: str


class NoRouteError(LookupError):
    pass


def require_digits(s: str) -> str:
    if not s or not s.isdigit() or not s.isascii():
        raise ValueError(f"expected a non-empty digit string, got {s!r}")
    return s


def mask(pan: str) -> str:
    """First six and last four digits: the only form of a PAN that may ever be logged (PCI DSS 3.4)."""
    if len(pan) <= 10:
        return "*" * len(pan)
    return pan[:6] + "*" * (len(pan) - 10) + pan[-4:]


def range_to_prefixes(lo: str, hi: str) -> list[str]:
    """Decompose a closed numeric range [lo, hi] of equal-length digit strings into the fewest prefixes that
    cover exactly the numbers in it. Greedy: from lo, take the widest prefix (most trailing zeros replaced)
    whose block still ends at or before hi, emit it, and continue from the number after that block."""
    require_digits(lo)
    require_digits(hi)
    if len(lo) != len(hi):
        raise ValueError(f"lo and hi must have equal length: {lo}, {hi}")
    if lo > hi:
        raise ValueError(f"lo must not exceed hi: {lo} > {hi}")
    length = len(lo)
    out: list[str] = []
    cur = lo
    while cur <= hi:
        k = 0  # number of trailing digits the prefix will leave free
        while k < length and cur[length - 1 - k] == "0" and cur[: length - 1 - k] + "9" * (k + 1) <= hi:
            k += 1
        prefix = cur[: length - k]
        out.append(prefix)
        block_end = prefix + "9" * k
        if block_end == "9" * length:  # the block ran to the end of the digit space
            break
        cur = str(int(block_end) + 1).zfill(length)
    return out


class BinRouter:
    """BIN routing: the leading digits of a PAN (the Bank Identification Number, ISO/IEC 7812 "issuer
    identification number") decide which network and which issuer a transaction goes to. Networks publish both
    prefixes ("4" is Visa) and numeric ranges (Mastercard's 2221-2720); ranges are decomposed into the minimal
    set of prefixes so one longest-prefix-match answers every lookup."""

    def __init__(self) -> None:
        self._networks: Trie[str] = Trie()
        self._issuers: Trie[str] = Trie()

    def add_network(self, prefix: str, network: str) -> None:
        self._networks.insert(require_digits(prefix), network)

    def add_network_range(self, lo: str, hi: str, network: str) -> None:
        for prefix in range_to_prefixes(lo, hi):
            self._networks.insert(prefix, network)

    def add_issuer(self, prefix: str, issuer: str) -> None:
        self._issuers.insert(require_digits(prefix), issuer)

    def route(self, pan: str) -> Route:
        require_digits(pan)
        if not 12 <= len(pan) <= 19:
            raise ValueError(f"PAN must be 12-19 digits, got {len(pan)}")
        network = self._networks.longest_prefix_match(pan)
        if network is None:
            raise NoRouteError(f"no network owns a prefix of {mask(pan)}")
        issuer = self._issuers.longest_prefix_match(pan)
        return Route(network.value, "UNKNOWN" if issuer is None else issuer.value, network.prefix)

    def network_prefix_count(self) -> int:
        return self._networks.size()


def sample_router() -> BinRouter:
    """Test BINs and PANs: these are the publicly documented test numbers every gateway sandbox accepts."""
    r = BinRouter()
    r.add_network("4", "VISA")
    r.add_network_range("51", "55", "MASTERCARD")
    r.add_network_range("2221", "2720", "MASTERCARD")
    r.add_network("34", "AMEX")
    r.add_network("37", "AMEX")
    r.add_network("6011", "DISCOVER")
    r.add_network("65", "DISCOVER")
    r.add_network_range("644", "649", "DISCOVER")
    r.add_network_range("3528", "3589", "JCB")
    r.add_issuer("411111", "Visa sandbox issuer")
    r.add_issuer("555555", "Mastercard sandbox issuer")
    r.add_issuer("378282", "Amex sandbox issuer")
    return r


def _show(x) -> str:
    if x is None:
        return "(none)"
    if isinstance(x, Match):
        return f'"{x.prefix}" -> {x.value}'
    return str(x)


def _fmt_list(xs: list[str]) -> str:
    return "[" + ", ".join(xs) + "]"


def main() -> None:
    print("== A trie of card prefixes ==")
    t: Trie[str] = Trie()
    t.insert("4", "VISA")
    t.insert("4111", "VISA (sandbox range)")
    t.insert("41", "VISA (sub-range)")
    t.insert("51", "MASTERCARD")
    print(f"keys={t.size()} nodes={t.node_count()}")
    print(f'search("41")   = {_show(t.search("41"))}')
    print(f'search("411")  = {_show(t.search("411"))}   (a path, not a key)')
    print(f'startsWith("411") = {str(t.starts_with("411")).lower()}')
    print(f'longestPrefixMatch("4111111111111111") = {_show(t.longest_prefix_match("4111111111111111"))}')
    print(f'longestPrefixMatch("4000000000000002") = {_show(t.longest_prefix_match("4000000000000002"))}')
    print(f'longestPrefixMatch("6011000000000004") = {_show(t.longest_prefix_match("6011000000000004"))}')
    print(f'keysWithPrefix("4") = {_fmt_list(t.keys_with_prefix("4"))}')
    t.delete("4111")
    print(f'after delete("4111"): keys={t.size()} nodes={t.node_count()} '
          f'search("41")={_show(t.search("41"))} startsWith("41")={str(t.starts_with("41")).lower()}')

    print("\n== A numeric range becomes a handful of prefixes ==")
    ms = range_to_prefixes("2221", "2720")
    print(f"2221-2720 -> {len(ms)} prefixes: {_fmt_list(ms)}")
    print(f"51-55     -> {_fmt_list(range_to_prefixes('51', '55'))}")
    print(f"3528-3589 -> {_fmt_list(range_to_prefixes('3528', '3589'))}")

    print("\n== BIN routing with longest-prefix match ==")
    router = sample_router()
    print(f"network prefixes stored: {router.network_prefix_count()}")
    for pan in ("4111111111111111", "5555555555554444", "2221000000000009", "2720999999999996",
                "378282246310005", "6011111111111117", "6451111111111110", "3530111333300000"):
        route = router.route(pan)
        print(f"{mask(pan):<20} -> {route.network:<10} issuer={route.issuer:<27} via prefix {route.matched_prefix}")
    for pan in ("2721000000000008", "9999000000000000"):
        try:
            router.route(pan)
        except NoRouteError as exc:
            print(f"rejected: {exc}")


if __name__ == "__main__":
    main()
