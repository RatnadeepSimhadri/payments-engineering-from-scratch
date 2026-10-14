"""Lesson: phases/02-algorithms/12-string-matching/docs/en.md
String Matching: KMP, Rabin-Karp, and Aho-Corasick
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Knuth, Morris, Pratt (SIAM J. Comput. 1977); Karp & Rabin (IBM J. R&D 1987); Aho & Corasick, "Efficient
string matching: an aid to bibliographic search" (CACM 1975). Python 3.11+, standard library only. Same design as Java.
"""

from __future__ import annotations

from collections import deque
from dataclasses import dataclass
from typing import Any


@dataclass(frozen=True)
class Match:
    """A match of one pattern in a text: the pattern and the index of its first character."""

    pattern: str
    start: int

    def end(self) -> int:
        return self.start + len(self.pattern)

    def __str__(self) -> str:
        return f"Match[pattern={self.pattern}, start={self.start}]"  # the Java record's toString


class NaiveMatcher:
    """Baseline: compare the pattern at every alignment. O(n * m) worst case; the oracle the others are tested against."""

    @staticmethod
    def search(text: str, pattern: str) -> list[int]:
        NaiveMatcher.require_non_empty(pattern)
        hits: list[int] = []
        i = 0
        while i + len(pattern) <= len(text):
            j = 0
            while j < len(pattern) and text[i + j] == pattern[j]:
                j += 1
            if j == len(pattern):
                hits.append(i)
            i += 1
        return hits

    @staticmethod
    def require_non_empty(pattern: str) -> None:
        if not pattern:
            raise ValueError("pattern must be non-empty")


class KmpMatcher:
    """
    Knuth-Morris-Pratt. fail[i] is the length of the longest proper prefix of pattern[0..i] that is also a suffix
    of it. On a mismatch the pattern slides; the text index never moves backwards. O(n + m), no hashing.
    """

    def __init__(self, pattern: str) -> None:
        NaiveMatcher.require_non_empty(pattern)
        self._pattern = pattern
        self._fail = KmpMatcher.failure_function(pattern)

    @staticmethod
    def failure_function(p: str) -> list[int]:
        fail = [0] * len(p)
        k = 0  # length of the current longest prefix-suffix
        for i in range(1, len(p)):
            while k > 0 and p[i] != p[k]:
                k = fail[k - 1]
            if p[i] == p[k]:
                k += 1
            fail[i] = k
        return fail

    def failure(self) -> list[int]:
        return list(self._fail)

    def search(self, text: str) -> list[int]:
        pattern, fail = self._pattern, self._fail
        hits: list[int] = []
        j = 0  # characters of pattern matched so far
        for i, c in enumerate(text):
            while j > 0 and c != pattern[j]:
                j = fail[j - 1]
            if c == pattern[j]:
                j += 1
            if j == len(pattern):
                hits.append(i - j + 1)
                j = fail[j - 1]  # keep going: matches may overlap
        return hits


class RabinKarpMatcher:
    """
    Rabin-Karp. Polynomial rolling hash of the pattern and every text window; compare characters only when the
    hashes agree. Expected O(n + m). The verification step makes it exact; the counters make collisions visible.
    """

    BASE = 256
    MOD = 1_000_000_007  # a large prime so collisions are ~ 1 / MOD per window

    def __init__(self, pattern: str) -> None:
        NaiveMatcher.require_non_empty(pattern)
        self._pattern = pattern
        self._pattern_hash = RabinKarpMatcher.hash(pattern)
        h = 1
        for _ in range(1, len(pattern)):
            h = (h * self.BASE) % self.MOD
        self._high_power = h  # BASE^(m-1) mod MOD, the weight of the outgoing char
        self._hash_matches = 0
        self._false_positives = 0

    @staticmethod
    def hash(s: str) -> int:
        h = 0
        for c in s:
            h = (h * RabinKarpMatcher.BASE + ord(c)) % RabinKarpMatcher.MOD
        return h

    def roll(self, window_hash: int, out: str, in_: str) -> int:
        """Slide the window one character: drop `out`, shift, add `in_`. O(1)."""
        h = (window_hash - (ord(out) * self._high_power) % self.MOD + self.MOD) % self.MOD
        return (h * self.BASE + ord(in_)) % self.MOD

    def search(self, text: str) -> list[int]:
        hits: list[int] = []
        m, n = len(self._pattern), len(text)
        if m > n:
            return hits
        window = RabinKarpMatcher.hash(text[:m])
        i = 0
        while True:
            if window == self._pattern_hash:
                self._hash_matches += 1
                if text.startswith(self._pattern, i):  # String.regionMatches
                    hits.append(i)
                else:
                    self._false_positives += 1
            if i + m >= n:
                break
            window = self.roll(window, text[i], text[i + m])
            i += 1
        return hits

    def hash_matches(self) -> int:
        return self._hash_matches

    def false_positives(self) -> int:
        return self._false_positives


class _Node:
    """A trie node of the automaton."""

    __slots__ = ("next", "fail", "output", "patterns", "depth")

    def __init__(self, depth: int) -> None:
        self.next: dict[str, _Node] = {}
        self.fail: _Node | None = None
        self.output: _Node | None = None  # nearest node up the fail chain that ends a pattern, or None
        self.patterns: list[str] = []  # patterns ending exactly here
        self.depth = depth


class AhoCorasick:
    """
    Aho-Corasick automaton: a trie of all patterns plus failure links and output links. One pass over the text finds
    every occurrence of every pattern: O(n + m_total + z) where z is the number of matches.
    """

    def __init__(self, patterns: list[str]) -> None:
        self._root = _Node(0)
        self._node_count = 1
        self._patterns: list[str] = []
        for p in patterns:
            self._insert(p)
        self._build_links()

    def _insert(self, p: str) -> None:
        NaiveMatcher.require_non_empty(p)
        cur = self._root
        for c in p:
            child = cur.next.get(c)
            if child is None:
                child = _Node(cur.depth + 1)
                cur.next[c] = child
                self._node_count += 1
            cur = child
        if p not in cur.patterns:
            cur.patterns.append(p)
            self._patterns.append(p)

    def _build_links(self) -> None:
        """BFS from the root: a node's fail link follows its parent's fail chain until a node has the same edge."""
        root = self._root
        queue: deque[_Node] = deque()
        root.fail = root
        for child in root.next.values():
            child.fail = root
            queue.append(child)
        while queue:
            node = queue.popleft()
            assert node.fail is not None
            node.output = node.fail if node.fail is not root and node.fail.patterns else node.fail.output
            for c, child in node.next.items():
                f = node.fail
                while f is not root and c not in f.next:
                    assert f.fail is not None
                    f = f.fail
                target = f.next.get(c)
                child.fail = root if (target is None or target is child) else target
                queue.append(child)
        root.output = None

    def node_count(self) -> int:
        return self._node_count

    def patterns(self) -> tuple[str, ...]:
        return tuple(self._patterns)

    def search(self, text: str) -> list[Match]:
        """Every occurrence of every pattern, in order of the position where the match ends."""
        root = self._root
        hits: list[Match] = []
        cur = root
        for i, c in enumerate(text):
            while cur is not root and c not in cur.next:
                assert cur.fail is not None
                cur = cur.fail
            step = cur.next.get(c)
            cur = root if step is None else step
            out = cur.output if not cur.patterns else cur
            while out is not None:
                for p in out.patterns:
                    hits.append(Match(p, i - len(p) + 1))
                out = out.output
        return hits


class SanctionsScreener:
    """
    Sanctions screening: normalize a counterparty name, run the automaton over it, and report which list entries
    appear as whole tokens. Normalization here is the minimum (case, punctuation, whitespace).
    """

    def __init__(self, listed_names: list[str]) -> None:
        self._automaton = AhoCorasick([SanctionsScreener.normalize(n) for n in listed_names])

    @staticmethod
    def normalize(name: str) -> str:
        if name is None:
            raise TypeError("name")
        out: list[str] = []
        last_space = True
        for c in name.upper():
            if c.isalnum():  # Character.isLetterOrDigit
                out.append(c)
                last_space = False
            elif not last_space:
                out.append(" ")
                last_space = True
        return "".join(out).strip()

    def screen(self, counterparty_name: str) -> list[Match]:
        """Listed names found as whole-token sequences inside the normalized name, in order of appearance."""
        padded = " " + SanctionsScreener.normalize(counterparty_name) + " "
        whole: list[Match] = []
        for m in self._automaton.search(padded):
            start, end = m.start, m.end()
            # whole-token: bounded by spaces on both sides (the padding guarantees indices are in range)
            if padded[start - 1] == " " and padded[end] == " ":
                whole.append(Match(m.pattern, start - 1))
        return whole

    def automaton_size(self) -> int:
        return self._automaton.node_count()


def sample_list() -> list[str]:
    return [
        "Ivanov Trading LLC",
        "Ivanov",
        "Northern Star Shipping",
        "Star Shipping",
        "Al-Rashid Exchange",
        "Rashid",
    ]


def jstr(x: Any) -> str:
    """List.toString: [a, b]."""
    if isinstance(x, list):
        return "[" + ", ".join(jstr(e) for e in x) + "]"
    return str(x)


def main() -> None:
    print("== Naive vs KMP on a pathological input ==")
    text = "a" * 30 + "b"
    pattern = "aaaab"
    print(f"text = a*30 + b, pattern = {pattern}")
    print(f"naive hits:  {jstr(NaiveMatcher.search(text, pattern))}")
    kmp = KmpMatcher(pattern)
    print(f"kmp hits:    {jstr(kmp.search(text))}  failure function {jstr(kmp.failure())}")
    abab = KmpMatcher("ABABAC")
    print(f"failure(ABABAC) = {jstr(abab.failure())}")

    print()
    print("== Rabin-Karp: hash the window, verify on hash match ==")
    rk = RabinKarpMatcher("ACQ")
    ledger = "ACQ-1001 CAP-1001 ACQ-1002 REF-1001 ACQ-1003"
    print(f"text = {ledger}")
    hits = rk.search(ledger)
    print(f"hits {jstr(hits)}, hash matches {rk.hash_matches()}, false positives {rk.false_positives()}")

    print()
    print("== Aho-Corasick: every pattern in one pass ==")
    ac = AhoCorasick(["he", "she", "his", "hers"])
    print(f"patterns he, she, his, hers -> {ac.node_count()} trie nodes")
    print(f'search("ushers") = {jstr(ac.search("ushers"))}')

    print()
    print(f"== Sanctions screening: {len(sample_list())} listed names, one pass per payment ==")
    screener = SanctionsScreener(sample_list())
    print(f"automaton nodes: {screener.automaton_size()}")
    beneficiaries = [
        "IVANOV TRADING, LLC",
        "Northern Star Shipping Co.",
        "Dmitri Ivanova",
        "Star Shipping Partners",
        "al rashid exchange house",
        "Rashida Patel",
        "Acme Widgets Inc",
    ]
    for b in beneficiaries:
        names = [m.pattern for m in screener.screen(b)]
        print(f"{b:<30} -> {'clear' if not names else 'HIT ' + jstr(names)}")


if __name__ == "__main__":
    main()
