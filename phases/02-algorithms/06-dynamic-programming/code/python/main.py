"""Lesson: phases/02-algorithms/06-dynamic-programming/docs/en.md
Dynamic Programming: From Recurrence to Table
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Bellman, "Dynamic Programming" (1957); CLRS ch. 15 (LCS); Kellerer, Pferschy, Pisinger,
"Knapsack Problems" (2004). Money design copied from phases/10-money-and-ledgers/01-representing-money.
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from functools import total_ordering
from typing import Sequence

INF = 2**31  # "unreachable" sentinel for the recurrence forms; Python ints never overflow
UNREACHABLE = -1


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: code and minor-unit exponent. Trimmed copy of Phase 10 Lesson 01."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if len(self.code) != 3:
            raise ValueError("currency code must be three letters")
        if not 0 <= self.exponent <= 4:
            raise ValueError("exponent must be 0..4")

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


USD = Currency("USD", 2)
EUR = Currency("EUR", 2)
INR = Currency("INR", 2)


class CurrencyMismatchError(ValueError):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


@total_ordering
@dataclass(frozen=True)
class Money:
    """Exact amount of one currency as a signed count of minor units. Never a float."""

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
            raise CurrencyMismatchError(self.currency, other.currency)

    def plus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units + other.minor_units, self.currency)

    def __lt__(self, other: "Money") -> bool:
        self._require_same_currency(other)
        return self.minor_units < other.minor_units

    def __str__(self) -> str:
        magnitude = abs(self.minor_units)
        whole, frac = divmod(magnitude, self.currency.minor_units_per_major())
        text = str(whole)
        if self.currency.exponent > 0:
            text += "." + str(frac).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{text} {self.currency.code}"

    __repr__ = __str__


@dataclass(frozen=True)
class ChangeResult:
    """How many instruments, and how many of each denomination (parallel to the input list)."""

    coin_count: int
    used: tuple[int, ...]

    def reachable(self) -> bool:
        return self.coin_count != UNREACHABLE

    def coins(self, denominations: Sequence[int]) -> list[int]:
        """Expand the counts into face values, largest first, for display."""
        out: list[int] = []
        for d, k in zip(denominations, self.used):
            out.extend([d] * k)
        return sorted(out, reverse=True)


def _validate(denominations: Sequence[int], amount: int) -> None:
    if amount < 0:
        raise ValueError(f"amount must be non-negative: {amount}")
    if not denominations:
        raise ValueError("need at least one denomination")
    for d in denominations:
        if d <= 0:
            raise ValueError(f"denominations must be positive: {d}")


def min_coins_recursive(denominations: Sequence[int], amount: int, calls: list[int]) -> int:
    """The recurrence as written: f(0) = 0, f(a) = 1 + min over d <= a of f(a - d). Exponential."""
    _validate(denominations, amount)

    def recurse(a: int) -> int:
        calls[0] += 1
        if a == 0:
            return 0
        best = INF
        for d in denominations:
            if d <= a:
                sub = recurse(a - d)
                if sub != INF:
                    best = min(best, sub + 1)
        return best

    return recurse(amount)


def min_coins_memo(denominations: Sequence[int], amount: int, calls: list[int]) -> int:
    """Same recurrence; every subproblem solved once. O(amount * len(denominations))."""
    _validate(denominations, amount)
    memo: dict[int, int] = {}

    def memoised(a: int) -> int:
        calls[0] += 1
        if a == 0:
            return 0
        if a in memo:
            return memo[a]
        best = INF
        for d in denominations:
            if d <= a:
                sub = memoised(a - d)
                if sub != INF:
                    best = min(best, sub + 1)
        memo[a] = best
        return best

    return memoised(amount)


def min_coins_table(denominations: Sequence[int], amount: int) -> ChangeResult:
    """Bottom-up table plus a choice array for reconstruction. Ties go to the lowest index."""
    _validate(denominations, amount)
    best = [INF] * (amount + 1)
    choice = [-1] * (amount + 1)
    best[0] = 0
    for a in range(1, amount + 1):
        for i, d in enumerate(denominations):
            if d <= a and best[a - d] != INF and best[a - d] + 1 < best[a]:
                best[a] = best[a - d] + 1
                choice[a] = i
    used = [0] * len(denominations)
    if best[amount] == INF:
        return ChangeResult(UNREACHABLE, tuple(used))
    a = amount
    while a > 0:
        used[choice[a]] += 1
        a -= denominations[choice[a]]
    return ChangeResult(best[amount], tuple(used))


def count_ways(denominations: Sequence[int], amount: int) -> int:
    """Distinct multisets summing to amount. Denominations OUTER so {1,2} and {2,1} count once."""
    _validate(denominations, amount)
    ways = [0] * (amount + 1)
    ways[0] = 1
    for d in denominations:
        for a in range(d, amount + 1):
            ways[a] += ways[a - d]
    return ways[amount]


def greedy_coins(denominations: Sequence[int], amount: int) -> int:
    """Largest-first greedy, for comparison. Correct for canonical systems, wrong in general."""
    _validate(denominations, amount)
    count = 0
    for d in sorted(denominations, reverse=True):
        count += amount // d
        amount %= d
    return count if amount == 0 else UNREACHABLE


@dataclass(frozen=True)
class Payout:
    """A pending payout: amount (knapsack weight) and priority score (knapsack value)."""

    id: str
    amount: Money
    priority: int


@dataclass(frozen=True)
class Batch:
    chosen: list[Payout]
    total: Money
    priority: int


def best_batch(payouts: Sequence[Payout], limit: Money) -> Batch:
    """0/1 knapsack over payout amounts in minor units. O(n * limit) time and space: pseudo-polynomial."""
    if limit.minor_units < 0:
        raise ValueError("limit must be non-negative")
    for p in payouts:
        if p.amount.currency != limit.currency:
            raise CurrencyMismatchError(limit.currency, p.amount.currency)
        if p.amount.minor_units <= 0:
            raise ValueError(f"payout amounts must be positive: {p.id}")
        if p.priority < 0:
            raise ValueError(f"priority must be non-negative: {p.id}")
    n, w = len(payouts), limit.minor_units
    best = [[0] * (w + 1) for _ in range(n + 1)]  # best[i][c]: max priority, first i payouts, capacity c
    for i in range(1, n + 1):
        p = payouts[i - 1]
        weight, prev, row = p.amount.minor_units, best[i - 1], best[i]
        for c in range(w + 1):
            skip = prev[c]
            take = prev[c - weight] + p.priority if weight <= c else -1
            row[c] = skip if skip >= take else take
    chosen: list[Payout] = []
    total = Money.of_minor(0, limit.currency)
    c = w
    for i in range(n, 0, -1):
        if best[i][c] != best[i - 1][c]:  # this row's value changed: the item was taken
            p = payouts[i - 1]
            chosen.insert(0, p)
            total = total.plus(p.amount)
            c -= p.amount.minor_units
    return Batch(chosen, total, best[n][w])


def greedy_batch(payouts: Sequence[Payout], limit: Money) -> Batch:
    """Highest-priority-first greedy, for comparison."""
    chosen: list[Payout] = []
    total = Money.of_minor(0, limit.currency)
    priority = 0
    for p in sorted(payouts, key=lambda p: -p.priority):
        if total.plus(p.amount) <= limit:
            chosen.append(p)
            total = total.plus(p.amount)
            priority += p.priority
    return Batch(chosen, total, priority)


@dataclass(frozen=True)
class Alignment:
    common: list[str] = field(default_factory=list)
    left_only: list[str] = field(default_factory=list)
    right_only: list[str] = field(default_factory=list)


def lcs_table(a: Sequence[str], b: Sequence[str]) -> list[list[int]]:
    """length[i][j] = LCS of a[:i] and b[:j]. O(|a| * |b|) time and space."""
    length = [[0] * (len(b) + 1) for _ in range(len(a) + 1)]
    for i in range(1, len(a) + 1):
        for j in range(1, len(b) + 1):
            if a[i - 1] == b[j - 1]:
                length[i][j] = length[i - 1][j - 1] + 1
            else:
                length[i][j] = max(length[i - 1][j], length[i][j - 1])
    return length


def lcs_length(a: Sequence[str], b: Sequence[str]) -> int:
    return lcs_table(a, b)[len(a)][len(b)]


def lcs_align(a: Sequence[str], b: Sequence[str]) -> Alignment:
    """Walk the table back from the corner. Ties prefer consuming from a, so the result is deterministic."""
    length = lcs_table(a, b)
    common: list[str] = []
    left_only: list[str] = []
    right_only: list[str] = []
    i, j = len(a), len(b)
    while i > 0 and j > 0:
        if a[i - 1] == b[j - 1]:
            common.append(a[i - 1])
            i -= 1
            j -= 1
        elif length[i - 1][j] >= length[i][j - 1]:
            left_only.append(a[i - 1])
            i -= 1
        else:
            right_only.append(b[j - 1])
            j -= 1
    left_only.extend(reversed(a[:i]))
    right_only.extend(reversed(b[:j]))
    return Alignment(common[::-1], left_only[::-1], right_only[::-1])


def ids(payouts: Sequence[Payout]) -> list[str]:
    return [p.id for p in payouts]


def main() -> None:
    print("== Recurrence -> memo -> table: fewest coins for 20 with {1, 3, 4} ==")
    odd = [1, 3, 4]
    naive, memo = [0], [0]
    r1 = min_coins_recursive(odd, 20, naive)
    r2 = min_coins_memo(odd, 20, memo)
    r3 = min_coins_table(odd, 20)
    print(f"recursive: {r1} coins after {naive[0]} calls")
    print(f"memoised : {r2} coins after {memo[0]} calls")
    print(f"table    : {r3.coin_count} coins = {r3.coins(odd)}")

    print("\n== Greedy is wrong for non-canonical denominations ==")
    print(f"{{1,3,4}} for 6: greedy={greedy_coins(odd, 6)} (4+1+1), optimal={min_coins_table(odd, 6).coin_count} (3+3)")
    vouchers = [100, 1500, 2500]  # 1.00, 15.00, 25.00 USD gift instruments, in cents
    plan = min_coins_table(vouchers, 3000)
    print(f"30.00 USD in {{1, 15, 25}} vouchers: greedy={greedy_coins(vouchers, 3000)} instruments, "
          f"table={plan.coin_count} -> {plan.coins(vouchers)} cents")
    us = [1, 5, 10, 25]
    sixty_three = min_coins_table(us, 63)
    print(f"0.63 USD in US coins: {sixty_three.coin_count} coins = {sixty_three.coins(us)} (greedy agrees: {greedy_coins(us, 63)})")
    print(f"unreachable: 0.03 with only {{2}} -> coin_count={min_coins_table([2], 3).coin_count}")

    print("\n== Number of ways ==")
    print(f"ways to make 5 from {{1,2,5}} = {count_ways([1, 2, 5], 5)}  (5, 2+2+1, 2+1+1+1, 1+1+1+1+1)")
    print(f"ways to make 1.00 USD from US coins = {count_ways(us, 100)}")

    print("\n== 0/1 knapsack: payout batch under a 100.00 USD settlement limit ==")
    payouts = [
        Payout("P-A", Money.of_minor(6000, USD), 100),
        Payout("P-B", Money.of_minor(5000, USD), 90),
        Payout("P-C", Money.of_minor(5000, USD), 90),
        Payout("P-D", Money.of_minor(3000, USD), 10),
        Payout("P-E", Money.of_minor(1000, USD), 5),
        Payout("P-F", Money.of_minor(12000, USD), 999),
    ]
    limit = Money.of_minor(10000, USD)
    greedy = greedy_batch(payouts, limit)
    best = best_batch(payouts, limit)
    print(f"greedy by priority: {ids(greedy.chosen)} total={greedy.total} priority={greedy.priority}")
    print(f"knapsack table   : {ids(best.chosen)} total={best.total} priority={best.priority}")

    print("\n== LCS: statement lines in order ==")
    bank = ["DEP 500.00", "CARD 19.99", "CARD 42.10", "FEE 2.50", "CARD 7.00"]
    ledger = ["DEP 500.00", "CARD 42.10", "CARD 19.99", "CARD 7.00", "REFUND 19.99"]
    al = lcs_align(bank, ledger)
    print(f"bank   : {bank}")
    print(f"ledger : {ledger}")
    print(f"matched in order ({len(al.common)}): {al.common}")
    print(f"bank only   : {al.left_only}")
    print(f"ledger only : {al.right_only}")


if __name__ == "__main__":
    main()
