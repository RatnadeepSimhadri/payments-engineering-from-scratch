"""Lesson: phases/14-payments-at-scale/08-cost-and-authorization-optimization/docs/en.md
Cost and Authorization-Rate Optimization
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Visa decline-code categories and retry limits; EMVCo Payment Tokenisation Specification;
Federal Reserve Regulation II (12 CFR 235, debit interchange cap). Python 3.11+, standard library only.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from functools import total_ordering


# ---------- Money (copied from Phase 10, Lesson 01) ----------


@dataclass(frozen=True)
class Currency:
    code: str
    exponent: int

    def __post_init__(self) -> None:
        if len(self.code) != 3 or not self.code.isalpha() or not self.code.isupper():
            raise ValueError(f"currency code must be three uppercase letters: {self.code!r}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


USD = Currency("USD", 2)
EUR = Currency("EUR", 2)


class Rounding(Enum):
    HALF_EVEN = "half_even"
    HALF_UP = "half_up"
    DOWN = "down"
    UP = "up"


class CurrencyMismatchError(ValueError):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


def divide_rounded(dividend: int, divisor: int, rounding: Rounding) -> int:
    """Integer division with an explicit rounding rule, on magnitudes so Python's floor division matches Java."""
    sign = -1 if (dividend < 0) != (divisor < 0) else 1
    quotient, remainder = divmod(abs(dividend), abs(divisor))
    abs_divisor = abs(divisor)
    if remainder == 0:
        return sign * quotient
    twice = remainder * 2
    if rounding is Rounding.UP:
        quotient += 1
    elif rounding is Rounding.HALF_UP:
        if twice >= abs_divisor:
            quotient += 1
    elif rounding is Rounding.HALF_EVEN:
        if twice > abs_divisor or (twice == abs_divisor and quotient % 2 == 1):
            quotient += 1
    return sign * quotient


@total_ordering
@dataclass(frozen=True)
class Money:
    minor_units: int
    currency: Currency

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    @staticmethod
    def zero(currency: Currency) -> "Money":
        return Money(0, currency)

    def _require_same_currency(self, other: "Money") -> None:
        if self.currency != other.currency:
            raise CurrencyMismatchError(self.currency, other.currency)

    def plus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units + other.minor_units, self.currency)

    def minus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units - other.minor_units, self.currency)

    def times(self, numerator: int, denominator: int, rounding: Rounding) -> "Money":
        return Money(divide_rounded(self.minor_units * numerator, denominator, rounding), self.currency)

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


# ---------------------------------------------------------------------------------------------
# Response codes, card types, and the price list
# ---------------------------------------------------------------------------------------------


class DeclineClass(Enum):
    """How a decline may be treated. Mirrors the card networks' grouping of response codes."""

    NONE = 0         # approved
    NEVER_RETRY = 1  # the issuer will never approve this card for this merchant: lost, stolen, closed
    RETRY_LATER = 2  # the issuer cannot approve now: funds, issuer down, system error
    FIX_DATA = 3     # the credential is wrong or stale: retry only with new data
    GENERIC = 4      # no reason given; one retry is tolerated, more is abuse


class ResponseCode(Enum):
    """ISO 8583 field 39 response codes used in this lesson."""

    APPROVED = ("00", DeclineClass.NONE)
    DO_NOT_HONOR = ("05", DeclineClass.GENERIC)
    LOST_OR_STOLEN = ("43", DeclineClass.NEVER_RETRY)
    INSUFFICIENT_FUNDS = ("51", DeclineClass.RETRY_LATER)
    EXPIRED_CARD = ("54", DeclineClass.FIX_DATA)
    NOT_PERMITTED = ("57", DeclineClass.NEVER_RETRY)
    ISSUER_UNAVAILABLE = ("91", DeclineClass.RETRY_LATER)
    PROCESSOR_TIMEOUT = ("96", DeclineClass.RETRY_LATER)

    def __init__(self, code: str, decline_class: DeclineClass) -> None:
        self.code = code
        self.decline_class = decline_class

    def approved(self) -> bool:
        return self is ResponseCode.APPROVED

    def __str__(self) -> str:
        return self.name


class CardType(Enum):
    CONSUMER_CREDIT = 0
    REGULATED_DEBIT = 1
    COMMERCIAL = 2


class Lever(Enum):
    NETWORK_TOKEN = 0
    ACCOUNT_UPDATER = 1
    SOFT_DECLINE_RETRY = 2
    ROUTING = 3
    LEVEL3_DATA = 4


NO_LEVERS: frozenset = frozenset()
ALL_LEVERS: frozenset = frozenset(Lever)


def levers_str(levers: frozenset) -> str:
    """EnumSet.toString: declaration order inside square brackets."""
    return "[" + ", ".join(l.name for l in Lever if l in levers) + "]"


@dataclass(frozen=True)
class Txn:
    """One transaction in the experiment. Its base outcome and three uniform draws in [0, 10000) are fixed when the
    population is generated, so every arm of the experiment sees the same customer, the same card, and the same
    luck. This is the common-random-numbers technique: differences between arms are caused by the levers only."""

    id: str
    amount: Money
    card_type: CardType
    token_eligible: bool
    base_outcome: ResponseCode
    draw_token: int
    draw_retry: int
    draw_updater: int

    def __post_init__(self) -> None:
        for d in (self.draw_token, self.draw_retry, self.draw_updater):
            if d < 0 or d >= 10_000:
                raise ValueError("draws are basis points in [0, 10000)")


@dataclass(frozen=True)
class PriceList:
    """Prices and probabilities. Probabilities are in basis points. Fees marked illustrative are plausible orders
    of magnitude, not any provider's price list; the regulated-debit cap is Regulation II's 0.21 USD + 5 bps + 0.01."""

    auth_fee: Money                      # per attempt, charged by the processor (illustrative)
    token_fee: Money                     # per tokenized attempt (illustrative)
    updater_hit_fee: Money               # per successful account-updater match (illustrative)
    token_recover_do_not_honor_bps: int  # share of 05 declines approved when the token is used
    updater_match_bps: int               # share of stale credentials the updater can refresh
    retry_insufficient_funds_bps: int
    retry_issuer_unavailable_bps: int
    retry_do_not_honor_bps: int
    retry_timeout_same_processor_bps: int
    failover_timeout_bps: int            # approval when a timeout is re-sent via the secondary processor
    premium_network_bps: int             # network fee on the default debit network (illustrative)
    least_cost_network_bps: int          # network fee on the cheaper debit network (illustrative)
    interchange: dict                    # CardType -> (bps, fixed minor)
    commercial_level3: tuple             # (bps, fixed minor) with full line-item data

    @staticmethod
    def standard(c: Currency) -> "PriceList":
        ic = {
            CardType.CONSUMER_CREDIT: (180, 10),  # 1.80% + 0.10 (illustrative)
            CardType.REGULATED_DEBIT: (5, 22),    # 0.05% + 0.22 (Regulation II cap with fraud adjustment)
            CardType.COMMERCIAL: (265, 10),       # 2.65% + 0.10, level 1 data only (illustrative)
        }
        return PriceList(Money.of_minor(5, c), Money.of_minor(1, c), Money.of_minor(15, c),
                         2_500, 8_000, 1_500, 7_000, 1_000, 5_000, 9_000, 13, 6, ic, (185, 10))


@dataclass(frozen=True)
class ArmResult:
    """The outcome of running one arm of the experiment over the whole population."""

    levers: frozenset
    transactions: int
    attempts: int
    approvals: int
    approved_volume: Money
    auth_fees: Money
    token_fees: Money
    updater_fees: Money
    network_fees: Money
    interchange: Money

    def total_cost(self) -> Money:
        return self.auth_fees.plus(self.token_fees).plus(self.updater_fees).plus(self.network_fees).plus(self.interchange)

    def approval_rate_bps(self) -> int:
        """Approval rate per transaction (not per attempt), in basis points."""
        return 0 if self.transactions == 0 else self.approvals * 10_000 // self.transactions


@dataclass(frozen=True)
class TxnTrace:
    """Per-transaction trace: what happened, how many attempts, what it cost. Used by the tests and the demo."""

    final_outcome: ResponseCode
    attempts: int
    token_used: bool
    updater_hit: bool
    failed_over: bool
    retried: bool
    fees: Money


class AuthExperiment:
    def __init__(self, prices: PriceList, currency: Currency) -> None:
        self.prices = prices
        self.currency = currency

    def simulate(self, t: Txn, levers: frozenset) -> TxnTrace:
        """Decide one transaction's path under a set of levers. Pure: same txn, same levers, same trace."""
        prices = self.prices
        outcome = t.base_outcome
        use_token = Lever.NETWORK_TOKEN in levers and t.token_eligible
        updater_hit = False
        fees = Money.zero(self.currency)

        # 1. Credential levers run before the first attempt.
        if outcome is ResponseCode.EXPIRED_CARD:
            if use_token:
                outcome = ResponseCode.APPROVED  # the token's lifecycle management already holds the new card
            elif Lever.ACCOUNT_UPDATER in levers and t.draw_updater < prices.updater_match_bps:
                outcome = ResponseCode.APPROVED
                updater_hit = True
                fees = fees.plus(prices.updater_hit_fee)
        if use_token and outcome is ResponseCode.DO_NOT_HONOR and t.draw_token < prices.token_recover_do_not_honor_bps:
            outcome = ResponseCode.APPROVED  # issuers approve more tokenized card-not-present traffic

        # 2. First attempt.
        attempts = 1
        fees = fees.plus(prices.auth_fee)
        if use_token:
            fees = fees.plus(prices.token_fee)

        # 3. Timeout at the primary: fail over to the secondary processor.
        failed_over = False
        if outcome is ResponseCode.PROCESSOR_TIMEOUT and Lever.ROUTING in levers:
            attempts += 1
            failed_over = True
            fees = fees.plus(prices.auth_fee)
            if use_token:
                fees = fees.plus(prices.token_fee)
            outcome = ResponseCode.APPROVED if t.draw_retry < prices.failover_timeout_bps else ResponseCode.ISSUER_UNAVAILABLE

        # 4. One retry for soft declines only. NEVER_RETRY and FIX_DATA are never re-sent unchanged.
        retried = False
        if not failed_over and Lever.SOFT_DECLINE_RETRY in levers and AuthExperiment.retryable(outcome):
            attempts += 1
            retried = True
            fees = fees.plus(prices.auth_fee)
            if use_token:
                fees = fees.plus(prices.token_fee)
            if t.draw_retry < self.retry_success_bps(outcome):
                outcome = ResponseCode.APPROVED
        return TxnTrace(outcome, attempts, use_token, updater_hit, failed_over, retried, fees)

    @staticmethod
    def retryable(r: ResponseCode) -> bool:
        return r.decline_class is DeclineClass.RETRY_LATER or r.decline_class is DeclineClass.GENERIC

    def retry_success_bps(self, r: ResponseCode) -> int:
        return {
            ResponseCode.INSUFFICIENT_FUNDS: self.prices.retry_insufficient_funds_bps,
            ResponseCode.ISSUER_UNAVAILABLE: self.prices.retry_issuer_unavailable_bps,
            ResponseCode.DO_NOT_HONOR: self.prices.retry_do_not_honor_bps,
            ResponseCode.PROCESSOR_TIMEOUT: self.prices.retry_timeout_same_processor_bps,
        }.get(r, 0)

    def interchange(self, amount: Money, card_type: CardType, level3: bool) -> Money:
        """Interchange on an approved transaction, given whether level 3 data was sent."""
        rate = self.prices.commercial_level3 if card_type is CardType.COMMERCIAL and level3 else self.prices.interchange[card_type]
        return amount.times(rate[0], 10_000, Rounding.HALF_EVEN).plus(Money.of_minor(rate[1], amount.currency))

    def network_fee(self, amount: Money, card_type: CardType, least_cost: bool) -> Money:
        """Network fee on an approved transaction; regulated debit can be routed to the cheaper network."""
        bps = self.prices.least_cost_network_bps if card_type is CardType.REGULATED_DEBIT and least_cost else self.prices.premium_network_bps
        return amount.times(bps, 10_000, Rounding.HALF_EVEN)

    def run(self, population: list, levers: frozenset) -> ArmResult:
        c = self.currency
        attempts = approvals = 0
        # Accumulated as minor-unit ints inside the loop (700,000 simulate calls in the demo); wrapped as Money once.
        volume = auth = token = updater = network = ic = 0
        token_fee, auth_fee, updater_fee = self.prices.token_fee.minor_units, self.prices.auth_fee.minor_units, self.prices.updater_hit_fee.minor_units
        level3, least_cost = Lever.LEVEL3_DATA in levers, Lever.ROUTING in levers
        for t in population:
            tr = self.simulate(t, levers)
            attempts += tr.attempts
            token_attempts = tr.attempts if tr.token_used else 0
            token += token_fee * token_attempts
            if tr.updater_hit:
                updater += updater_fee
            auth += auth_fee * tr.attempts
            if tr.final_outcome.approved():
                approvals += 1
                volume += t.amount.minor_units
                network += self.network_fee(t.amount, t.card_type, least_cost).minor_units
                ic += self.interchange(t.amount, t.card_type, level3).minor_units
        return ArmResult(frozenset(levers), len(population), attempts, approvals, Money.of_minor(volume, c), Money.of_minor(auth, c),
                         Money.of_minor(token, c), Money.of_minor(updater, c), Money.of_minor(network, c), Money.of_minor(ic, c))

    @staticmethod
    def population(n: int, seed: int, c: Currency) -> list:
        """Seeded population. The base outcome mix is fixed by the seed; see the lesson for the shares."""
        rnd = JavaRandom(seed)
        out = []
        for i in range(n):
            type_roll = rnd.next_int(100)
            card_type = CardType.CONSUMER_CREDIT if type_roll < 60 else CardType.REGULATED_DEBIT if type_roll < 90 else CardType.COMMERCIAL
            minor = 20_000 + rnd.next_int(480_000) if card_type is CardType.COMMERCIAL else 1_000 + rnd.next_int(19_000)
            token_eligible = rnd.next_int(100) < 70
            u = rnd.next_int(10_000)
            if u < 400:
                base = ResponseCode.EXPIRED_CARD           # 4.0% stale credentials on file
            elif u < 800:
                base = ResponseCode.DO_NOT_HONOR           # 4.0%
            elif u < 1100:
                base = ResponseCode.INSUFFICIENT_FUNDS     # 3.0%
            elif u < 1250:
                base = ResponseCode.ISSUER_UNAVAILABLE     # 1.5%
            elif u < 1400:
                base = ResponseCode.PROCESSOR_TIMEOUT      # 1.5%
            elif u < 1500:
                base = ResponseCode.LOST_OR_STOLEN         # 1.0%
            elif u < 1600:
                base = ResponseCode.NOT_PERMITTED          # 1.0%
            else:
                base = ResponseCode.APPROVED               # 84.0%
            draw_token = rnd.next_int(10_000)
            draw_retry = rnd.next_int(10_000)
            draw_updater = rnd.next_int(10_000)
            out.append(Txn(f"t{i:06d}", Money.of_minor(minor, c), card_type, token_eligible, base, draw_token, draw_retry, draw_updater))
        return out


@dataclass(frozen=True)
class Uplift:
    """Uplift of one arm over control: exact integer deltas, plus net value at a stated contribution margin."""

    arm: str
    extra_approvals: int
    rate_delta_bps: int
    extra_volume: Money
    extra_cost: Money
    net_value: Money

    @staticmethod
    def of(arm: str, control: ArmResult, treatment: ArmResult, margin_bps: int) -> "Uplift":
        extra_volume = treatment.approved_volume.minus(control.approved_volume)
        extra_cost = treatment.total_cost().minus(control.total_cost())
        value = extra_volume.times(margin_bps, 10_000, Rounding.HALF_EVEN).minus(extra_cost)
        return Uplift(arm, treatment.approvals - control.approvals,
                      treatment.approval_rate_bps() - control.approval_rate_bps(), extra_volume, extra_cost, value)


class JavaRandom:
    """java.util.Random, bit for bit (the JDK's 48-bit LCG), so seed 2026 builds the same population as the Java demo."""

    MULTIPLIER = 0x5DEECE66D
    ADDEND = 0xB
    MASK = (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self.seed = (seed ^ self.MULTIPLIER) & self.MASK

    def next(self, bits: int) -> int:
        self.seed = (self.seed * self.MULTIPLIER + self.ADDEND) & self.MASK
        value = self.seed >> (48 - bits)
        return value - (1 << 32) if value >= (1 << 31) else value  # Java's int is signed

    def next_int(self, bound: int) -> int:
        if bound <= 0:
            raise ValueError("bound must be positive")
        r = self.next(31)
        m = bound - 1
        if (bound & m) == 0:  # power of two
            return (bound * r) >> 31
        u = r
        r = u % bound
        while _to_int32(u - r + m) < 0:  # the overflow check Java performs in 32-bit arithmetic
            u = self.next(31)
            r = u % bound
        return r


def _to_int32(x: int) -> int:
    x &= 0xFFFFFFFF
    return x - (1 << 32) if x >= (1 << 31) else x


MARGIN_BPS = 3_000  # the merchant's contribution margin on recovered sales: 30%


def pct(bps: int) -> str:
    sign = "-" if bps < 0 else ""
    bps = abs(bps)
    return f"{sign}{bps // 100}.{bps % 100:02d}%"


def main() -> None:
    exp = AuthExperiment(PriceList.standard(USD), USD)

    print("== Level 2/3 data on one 1,000.00 USD commercial card purchase ==")
    ticket = Money.of_minor(100_000, USD)
    l1 = exp.interchange(ticket, CardType.COMMERCIAL, False)
    l3 = exp.interchange(ticket, CardType.COMMERCIAL, True)
    print(f"level 1 interchange {l1}, level 3 {l3}, saved {l1.minus(l3)}")

    print("\n== One expired card, three ways ==")
    stale = Txn("demo", Money.of_minor(4_999, USD), CardType.CONSUMER_CREDIT, True, ResponseCode.EXPIRED_CARD, 9_000, 9_000, 1_000)
    for levers in (NO_LEVERS, frozenset({Lever.SOFT_DECLINE_RETRY}), frozenset({Lever.ACCOUNT_UPDATER}), frozenset({Lever.NETWORK_TOKEN})):
        tr = exp.simulate(stale, levers)
        label = "control" if not levers else levers_str(levers)
        print(f"  {label:<22} -> {tr.final_outcome.name:<14} attempts {tr.attempts} fees {tr.fees}")

    print("\n== 100,000 transactions, seed 2026, same draws in every arm ==")
    pop = AuthExperiment.population(100_000, 2026, USD)
    control = exp.run(pop, NO_LEVERS)
    print(f"control: {control.approvals} approvals of {control.transactions} ({pct(control.approval_rate_bps())}), "
          f"{control.attempts} attempts, volume {control.approved_volume}, cost {control.total_cost()}")
    print(f"\n  {'arm':<20} {'+approve':>8} {'+rate':>8} {'+volume':>18} {'+cost':>14} {'net @30% margin':>16}")
    rows = [Uplift.of(l.name, control, exp.run(pop, frozenset({l})), MARGIN_BPS) for l in Lever]
    rows.append(Uplift.of("ALL", control, exp.run(pop, ALL_LEVERS), MARGIN_BPS))
    for u in rows:
        print(f"  {u.arm:<20} {u.extra_approvals:>8} {pct(u.rate_delta_bps):>8} {str(u.extra_volume):>18} {str(u.extra_cost):>14} {str(u.net_value):>16}")
    sum_of_singles = sum(u.extra_approvals for u in rows[:len(Lever)])
    print(f"\nsum of single-lever uplifts {sum_of_singles} vs all levers together {rows[-1].extra_approvals}"
          "\n(token and updater recover the same expired cards; routing and retry the same timeouts)")

    all_arm = exp.run(pop, ALL_LEVERS)
    print(f"cost per approval: control {Money.of_minor(control.total_cost().minor_units // control.approvals, USD)}, "
          f"all levers {Money.of_minor(all_arm.total_cost().minor_units // all_arm.approvals, USD)}")
    hard_resent = sum(1 for t in pop if t.base_outcome.decline_class is DeclineClass.NEVER_RETRY
                      and exp.simulate(t, ALL_LEVERS).attempts > 1)
    print(f"hard declines re-sent under ALL: {hard_resent}")


if __name__ == "__main__":
    main()
