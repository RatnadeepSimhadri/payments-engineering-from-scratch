"""Lesson: phases/12-payment-systems-design/14-testing-payment-systems/docs/en.md
Testing Payment Systems: Simulators, Property Tests, and Invariants
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Claessen & Hughes, "QuickCheck: A Lightweight Tool for Random Testing of Haskell Programs" (ICFP 2000);
         Hughes, "Experiences with QuickCheck: Testing the Hard Stuff and Staying Sane" (2016).
Python 3.11+, standard library only. Same design as the Java version; JavaRandom reproduces java.util.Random.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from typing import Callable, Generic, Optional, Protocol, TypeVar

T = TypeVar("T")


# ---------------------------------------------------------------------------------------------
# Money, copied from Phase 10 Lesson 01: integer minor units plus a currency. Never a float.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Currency:
    code: str
    exponent: int


USD = Currency("USD", 2)


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
            raise ValueError(f"cannot combine {self.currency.code} with {other.currency.code}")

    def plus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units + other.minor_units, self.currency)

    def minus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units - other.minor_units, self.currency)

    def negate(self) -> "Money":
        return Money(-self.minor_units, self.currency)

    def is_zero(self) -> bool:
        return self.minor_units == 0

    def is_negative(self) -> bool:
        return self.minor_units < 0

    def basis_points(self, bps: int) -> "Money":
        """Percentage in basis points (1 bp = 0.01%), rounded HALF_EVEN. Java's `/` and `%` truncate toward
        zero, so work on the magnitude and reapply the sign instead of using Python's flooring `//`."""
        product = self.minor_units * bps
        sign = -1 if product < 0 else 1
        q, r = divmod(abs(product), 10_000)
        q *= sign
        twice = r * 2
        if twice > 10_000 or (twice == 10_000 and q % 2 != 0):
            q += sign
        return Money(q, self.currency)

    def compare_to(self, other: "Money") -> int:
        self._require_same_currency(other)
        return (self.minor_units > other.minor_units) - (self.minor_units < other.minor_units)

    def __str__(self) -> str:
        scale = 10 ** self.currency.exponent
        magnitude = abs(self.minor_units)
        frac = "" if self.currency.exponent == 0 else "." + str(magnitude % scale).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{magnitude // scale}{frac} {self.currency.code}"

    __repr__ = __str__


# ---------------------------------------------------------------------------------------------
# java.util.Random, bit for bit, so the seeded property runs find the same counterexamples as Java.
# ---------------------------------------------------------------------------------------------

_MASK48 = (1 << 48) - 1
_MULTIPLIER = 0x5DEECE66D


def _to_int32(x: int) -> int:
    x &= 0xFFFFFFFF
    return x - (1 << 32) if x >= (1 << 31) else x


class JavaRandom:
    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ _MULTIPLIER) & _MASK48

    def next(self, bits: int) -> int:
        self._seed = (self._seed * _MULTIPLIER + 0xB) & _MASK48
        return _to_int32(self._seed >> (48 - bits))

    def next_int(self, bound: int) -> int:
        if bound <= 0:
            raise ValueError("bound must be positive")
        r = self.next(31)
        m = bound - 1
        if (bound & m) == 0:
            return _to_int32((bound * r) >> 31)
        u = r
        r = u % bound
        while _to_int32(u - r + m) < 0:
            u = self.next(31)
            r = u % bound
        return r

    def next_double(self) -> float:
        return ((self.next(26) << 27) + self.next(27)) * (1.0 / (1 << 53))


# ---------------------------------------------------------------------------------------------
# Step 1: the system under test. A payment with a ledger, talking to a processor that can fail.
# ---------------------------------------------------------------------------------------------


class Account(Enum):
    CUSTOMER = "customer"
    MERCHANT = "merchant"
    PLATFORM_FEES = "platform_fees"

    def __str__(self) -> str:
        return self.name


class IllegalStateError(RuntimeError):
    """The Java IllegalStateException: an operation that the current state does not allow."""


class Ledger:
    """Three accounts, double-entry: every posting debits one and credits another by the same amount."""

    def __init__(self, currency: Currency) -> None:
        self._balances: dict[Account, Money] = {a: Money.zero(currency) for a in Account}
        self.postings: list[str] = []

    def post(self, from_account: Account, to_account: Account, amount: Money, memo: str) -> None:
        if amount.is_negative():
            raise ValueError(f"posting amount must not be negative: {amount}")
        self._balances[from_account] = self._balances[from_account].minus(amount)
        self._balances[to_account] = self._balances[to_account].plus(amount)
        self.postings.append(f"{memo}: {from_account.name} -> {to_account.name} {amount}")

    def credit_only(self, to_account: Account, amount: Money, memo: str) -> None:
        """A deliberately unbalanced write, reachable only through Bugs.unbalanced_fee."""
        self._balances[to_account] = self._balances[to_account].plus(amount)
        self.postings.append(f"{memo}: (nothing) -> {to_account.name} {amount}")

    def balance(self, a: Account) -> Money:
        return self._balances[a]

    def total(self) -> Money:
        t = Money.zero(self._balances[Account.CUSTOMER].currency)
        for m in self._balances.values():
            t = t.plus(m)
        return t


class ProcessorBehaviour(Enum):
    """How the processor behaves on one call. A timeout is ambiguous: the operation may or may not have been applied."""

    APPROVE = "approve"                            # applied, success returned
    DECLINE = "decline"                            # not applied, decline returned
    TIMEOUT_AFTER_APPLY = "timeout_after_apply"    # applied, but the response was lost on the way back
    TIMEOUT_BEFORE_APPLY = "timeout_before_apply"  # the request never arrived; nothing applied
    ERROR = "error"                                # not applied, 5xx returned

    def __str__(self) -> str:
        return self.name


class ProcessorTimeout(RuntimeError):
    def __init__(self, op: str) -> None:
        super().__init__(f"timed out waiting for processor response to {op}")


class ProcessorPayment:
    """The processor's own record of a payment. This is the external truth the ledger must reconcile to."""

    def __init__(self, c: Currency) -> None:
        self.authorized = Money.zero(c)
        self.captured = Money.zero(c)
        self.refunded = Money.zero(c)
        self.voided = False


class FakeProcessor:
    """A processor whose behaviour per call is scripted. Exhausting the script means APPROVE."""

    def __init__(self, currency: Currency) -> None:
        self._script: list[ProcessorBehaviour] = []
        self._cursor = 0
        self.record = ProcessorPayment(currency)
        self.calls = 0

    def script(self, *behaviours: ProcessorBehaviour) -> "FakeProcessor":
        self._script.extend(behaviours)
        return self

    def _next(self) -> ProcessorBehaviour:
        self.calls += 1
        if self._cursor < len(self._script):
            b = self._script[self._cursor]
            self._cursor += 1
            return b
        return ProcessorBehaviour.APPROVE

    def call(self, op: str, apply: Callable[[], None]) -> bool:
        """Applies the operation if the processor would, then returns or raises according to the behaviour."""
        b = self._next()
        if b is ProcessorBehaviour.APPROVE:
            apply()
            return True
        if b is ProcessorBehaviour.DECLINE:
            return False
        if b is ProcessorBehaviour.TIMEOUT_AFTER_APPLY:
            apply()
            raise ProcessorTimeout(op)
        if b is ProcessorBehaviour.TIMEOUT_BEFORE_APPLY:
            raise ProcessorTimeout(op)
        raise IllegalStateError(f"processor 5xx on {op}")

    def authorize(self, amount: Money) -> bool:
        def apply() -> None:
            self.record.authorized = amount
        return self.call("authorize", apply)

    def capture(self, amount: Money) -> bool:
        def apply() -> None:
            self.record.captured = self.record.captured.plus(amount)
        return self.call("capture", apply)

    def refund(self, amount: Money) -> bool:
        def apply() -> None:
            self.record.refunded = self.record.refunded.plus(amount)
        return self.call("refund", apply)

    def void_auth(self) -> bool:
        def apply() -> None:
            self.record.voided = True
        return self.call("void", apply)


@dataclass(frozen=True)
class Bugs:
    """Switches that re-introduce real bugs so the property tests have something to find. All False in production."""

    allow_over_capture: bool
    allow_over_refund: bool
    unbalanced_fee: bool
    post_on_timeout: bool

    def __str__(self) -> str:
        flags = ", ".join(f"{k}={str(v).lower()}" for k, v in self.__dict__.items())
        return f"Bugs[{flags}]"


NO_BUGS = Bugs(False, False, False, False)
Bugs.NONE = NO_BUGS  # type: ignore[attr-defined]


class PaymentState(Enum):
    NEW = "new"
    AUTHORIZED = "authorized"
    PARTIALLY_CAPTURED = "partially_captured"
    CAPTURED = "captured"
    VOIDED = "voided"
    UNKNOWN = "unknown"

    def __str__(self) -> str:
        return self.name


class PaymentService:
    FEE_BPS = 290  # 2.9%

    def __init__(self, currency: Currency, processor: FakeProcessor, bugs: Bugs) -> None:
        self.currency = currency
        self.processor = processor
        self.bugs = bugs
        self.ledger = Ledger(currency)
        self.state = PaymentState.NEW
        self.authorized = Money.zero(currency)
        self.captured = Money.zero(currency)
        self.refunded = Money.zero(currency)
        self.last_unknown_op: Optional[str] = None

    @staticmethod
    def _require_positive(m: Money) -> None:
        if m.minor_units <= 0:
            raise ValueError(f"amount must be positive: {m}")

    def authorize(self, amount: Money) -> bool:
        self._require_positive(amount)
        if self.state is not PaymentState.NEW:
            raise IllegalStateError(f"authorize in state {self.state.name}")
        try:
            if not self.processor.authorize(amount):
                return False
        except ProcessorTimeout:
            self.state = PaymentState.UNKNOWN
            self.last_unknown_op = f"authorize:{amount.minor_units}"
            return False
        self.authorized = amount
        self.state = PaymentState.AUTHORIZED
        return True

    def capture(self, amount: Money) -> bool:
        self._require_positive(amount)
        if self.state not in (PaymentState.AUTHORIZED, PaymentState.PARTIALLY_CAPTURED):
            raise IllegalStateError(f"capture in state {self.state.name}")
        if not self.bugs.allow_over_capture and self.captured.plus(amount).compare_to(self.authorized) > 0:
            raise ValueError(f"capture {amount} exceeds remaining authorization {self.authorized.minus(self.captured)}")
        try:
            if not self.processor.capture(amount):
                return False
        except ProcessorTimeout:
            if self.bugs.post_on_timeout:
                self._post_capture(amount)  # the bug: assuming a timeout meant success
            self.state = PaymentState.UNKNOWN
            self.last_unknown_op = f"capture:{amount.minor_units}"
            return False
        self._post_capture(amount)
        return True

    def _post_capture(self, amount: Money) -> None:
        fee = amount.basis_points(PaymentService.FEE_BPS)
        self.ledger.post(Account.CUSTOMER, Account.MERCHANT, amount, "capture")
        if self.bugs.unbalanced_fee:
            self.ledger.credit_only(Account.PLATFORM_FEES, fee, "fee")
        else:
            self.ledger.post(Account.MERCHANT, Account.PLATFORM_FEES, fee, "fee")
        self.captured = self.captured.plus(amount)
        self.state = PaymentState.CAPTURED if self.captured.compare_to(self.authorized) >= 0 else PaymentState.PARTIALLY_CAPTURED

    def refund(self, amount: Money) -> bool:
        self._require_positive(amount)
        if self.state not in (PaymentState.PARTIALLY_CAPTURED, PaymentState.CAPTURED):
            raise IllegalStateError(f"refund in state {self.state.name}")
        if not self.bugs.allow_over_refund and self.refunded.plus(amount).compare_to(self.captured) > 0:
            raise ValueError(f"refund {amount} exceeds refundable {self.captured.minus(self.refunded)}")
        try:
            if not self.processor.refund(amount):
                return False
        except ProcessorTimeout:
            self.state = PaymentState.UNKNOWN
            self.last_unknown_op = f"refund:{amount.minor_units}"
            return False
        self._post_refund(amount)
        return True

    def _post_refund(self, amount: Money) -> None:
        self.ledger.post(Account.MERCHANT, Account.CUSTOMER, amount, "refund")
        self.refunded = self.refunded.plus(amount)

    def void_authorization(self) -> bool:
        if self.state is not PaymentState.AUTHORIZED:
            raise IllegalStateError(f"void in state {self.state.name}")
        try:
            if not self.processor.void_auth():
                return False
        except ProcessorTimeout:
            self.state = PaymentState.UNKNOWN
            self.last_unknown_op = "void"
            return False
        self.state = PaymentState.VOIDED
        return True

    def reconcile(self) -> None:
        """After a timeout, ask the processor what actually happened and bring the ledger into line."""
        if self.state is not PaymentState.UNKNOWN:
            return
        p = self.processor.record
        if p.authorized != self.authorized:
            self.authorized = p.authorized
        missing_capture = p.captured.minus(self.captured)
        if missing_capture.minor_units > 0:
            self._post_capture(missing_capture)
        missing_refund = p.refunded.minus(self.refunded)
        if missing_refund.minor_units > 0:
            self._post_refund(missing_refund)
        if p.voided:
            self.state = PaymentState.VOIDED
        elif self.captured.is_zero():
            self.state = PaymentState.NEW if self.authorized.is_zero() else PaymentState.AUTHORIZED
        else:
            self.state = PaymentState.CAPTURED if self.captured.compare_to(self.authorized) >= 0 else PaymentState.PARTIALLY_CAPTURED
        self.last_unknown_op = None


# ---------------------------------------------------------------------------------------------
# Step 2: the invariants. Each returns None when it holds, or a sentence naming the violation.
# ---------------------------------------------------------------------------------------------


class Invariants:
    @staticmethod
    def check(s: PaymentService) -> Optional[str]:
        if not s.ledger.total().is_zero():
            return f"money not conserved: ledger total {s.ledger.total()}"
        if s.captured.compare_to(s.authorized) > 0:
            return f"captured {s.captured} > authorized {s.authorized}"
        if s.refunded.compare_to(s.captured) > 0:
            return f"refunded {s.refunded} > captured {s.captured}"
        customer_expected = s.captured.minus(s.refunded).negate()
        if s.ledger.balance(Account.CUSTOMER) != customer_expected:
            return f"customer balance {s.ledger.balance(Account.CUSTOMER)} != -(captured - refunded) {customer_expected}"
        if s.ledger.balance(Account.PLATFORM_FEES).is_negative():
            return "negative fee balance"
        if s.state is not PaymentState.UNKNOWN:
            p = s.processor.record
            if p.captured != s.captured:
                return f"ledger captured {s.captured} != processor captured {p.captured}"
            if p.refunded != s.refunded:
                return f"ledger refunded {s.refunded} != processor refunded {p.refunded}"
        return None


# ---------------------------------------------------------------------------------------------
# Step 3: a property-based testing harness. Generators produce values and know how to shrink them.
# ---------------------------------------------------------------------------------------------


class Gen(Protocol[T]):
    def generate(self, rng: JavaRandom) -> T: ...
    def shrink(self, value: T) -> list[T]:
        """Smaller candidates, simplest first. Empty when the value cannot be made smaller."""
        ...


class LongGen:
    """Uniform in [lo, hi]; shrinks toward lo by halving the distance, then by single steps."""

    def __init__(self, lo: int, hi: int) -> None:
        self._lo = lo
        self._hi = hi

    def generate(self, rng: JavaRandom) -> int:
        return self._lo + int(rng.next_double() * (self._hi - self._lo + 1))

    def shrink(self, v: int) -> list[int]:
        out: list[int] = []
        if v <= self._lo:
            return out
        out.append(self._lo)
        step = (v - self._lo) // 2
        while step > 0:
            out.append(v - step)
            step //= 2
        if v - 1 not in out:
            out.append(v - 1)
        return out


class OneOfGen(Generic[T]):
    def __init__(self, values: list[T]) -> None:
        self._values = list(values)

    def generate(self, rng: JavaRandom) -> T:
        return self._values[rng.next_int(len(self._values))]

    def shrink(self, v: T) -> list[T]:
        out: list[T] = []
        for c in self._values:  # earlier entries are "simpler"
            if c == v:
                break
            out.append(c)
        return out


class ListGen(Generic[T]):
    """Lists of 1..max_len elements; shrinks by dropping chunks, then single elements, then shrinking elements."""

    def __init__(self, element: Gen[T], max_len: int) -> None:
        self._element = element
        self._max_len = max_len

    def generate(self, rng: JavaRandom) -> list[T]:
        n = 1 + rng.next_int(self._max_len)
        return [self._element.generate(rng) for _ in range(n)]

    def shrink(self, v: list[T]) -> list[list[T]]:
        out: list[list[T]] = []
        n = len(v)
        chunk = n // 2
        while chunk >= 1:
            start = 0
            while start + chunk <= n:
                c = v[:start] + v[start + chunk:]
                if c:
                    out.append(c)
                start += chunk
            chunk //= 2
        for i in range(n):
            for smaller in self._element.shrink(v[i]):
                c = list(v)
                c[i] = smaller
                out.append(c)
        return out


class Gens:
    @staticmethod
    def longs(lo: int, hi: int) -> LongGen:
        return LongGen(lo, hi)

    @staticmethod
    def one_of(values: list[T]) -> OneOfGen[T]:
        return OneOfGen(values)

    @staticmethod
    def lists(element: Gen[T], max_len: int) -> ListGen[T]:
        return ListGen(element, max_len)


def _show(value: object) -> str:
    if isinstance(value, list):
        return "[" + ", ".join(str(x) for x in value) + "]"
    return str(value)


@dataclass(frozen=True)
class Counterexample(Generic[T]):
    original: T
    shrunk: T
    shrink_steps: int
    reason: str


@dataclass(frozen=True)
class PropertyResult(Generic[T]):
    name: str
    passed: bool
    seed: int
    trials_run: int
    counterexample: Optional[Counterexample[T]]

    def summary(self) -> str:
        if self.passed:
            return f"{self.name}: OK, {self.trials_run} trials (seed {self.seed})"
        assert self.counterexample is not None
        c = self.counterexample
        return (f"{self.name}: FAILED at trial {self.trials_run} (seed {self.seed})\n  original: {_show(c.original)}"
                f"\n  shrunk ({c.shrink_steps} steps): {_show(c.shrunk)}\n  reason: {c.reason}")


class Property:
    MAX_SHRINK_STEPS = 2_000

    @staticmethod
    def for_all(name: str, gen: Gen[T], check: Callable[[T], Optional[str]], seed: int, trials: int) -> PropertyResult[T]:
        """Runs `check` on `trials` generated values. `check` returns None when the property holds."""
        rng = JavaRandom(seed)
        for i in range(1, trials + 1):
            value = gen.generate(rng)
            reason = Property._safe_check(check, value)
            if reason is None:
                continue
            current = value
            current_reason = reason
            steps = 0
            progressed = True
            while progressed and steps < Property.MAX_SHRINK_STEPS:
                progressed = False
                for candidate in gen.shrink(current):
                    steps += 1
                    r = Property._safe_check(check, candidate)
                    if r is not None:
                        current, current_reason, progressed = candidate, r, True
                        break
                    if steps >= Property.MAX_SHRINK_STEPS:
                        break
            return PropertyResult(name, False, seed, i, Counterexample(value, current, steps, current_reason))
        return PropertyResult(name, True, seed, trials, None)

    @staticmethod
    def _safe_check(check: Callable[[T], Optional[str]], value: T) -> Optional[str]:
        try:
            return check(value)
        except (ValueError, IllegalStateError):
            return None  # the system rejected the input on purpose: the property is about accepted inputs
        except Exception as crash:  # noqa: BLE001 - any other exception is a failure the property must report
            return f"crash: {type(crash).__name__}: {crash}"


# ---------------------------------------------------------------------------------------------
# Step 4: scenarios as data. A command list plus a processor script replays identically every time.
# ---------------------------------------------------------------------------------------------


class CommandKind(Enum):
    AUTHORIZE = "authorize"
    CAPTURE = "capture"
    REFUND = "refund"
    VOID = "void"
    RECONCILE = "reconcile"

    def __str__(self) -> str:
        return self.name


@dataclass(frozen=True)
class Command:
    kind: CommandKind
    amount_minor: int
    behaviour: ProcessorBehaviour

    def __str__(self) -> str:
        suffix = "" if self.behaviour is ProcessorBehaviour.APPROVE else f"/{self.behaviour.name}"
        if self.kind in (CommandKind.VOID, CommandKind.RECONCILE):
            return f"{self.kind.name}{suffix}"
        return f"{self.kind.name} {self.amount_minor}{suffix}"

    __repr__ = __str__


@dataclass(frozen=True)
class Scenario:
    name: str
    commands: tuple[Command, ...]

    def __post_init__(self) -> None:
        object.__setattr__(self, "commands", tuple(self.commands))


@dataclass(frozen=True)
class ScenarioRun:
    scenario: Scenario
    service: PaymentService
    log: tuple[str, ...]
    violation: Optional[str]

    def passed(self) -> bool:
        return self.violation is None


class CommandGen:
    def __init__(self) -> None:
        self._amounts = Gens.longs(1, 10_000)
        self._kinds = Gens.one_of([CommandKind.AUTHORIZE, CommandKind.CAPTURE, CommandKind.REFUND, CommandKind.RECONCILE, CommandKind.VOID])
        self._behaviours = Gens.one_of([ProcessorBehaviour.APPROVE, ProcessorBehaviour.DECLINE, ProcessorBehaviour.TIMEOUT_AFTER_APPLY,
                                        ProcessorBehaviour.TIMEOUT_BEFORE_APPLY, ProcessorBehaviour.ERROR])

    def generate(self, rng: JavaRandom) -> Command:
        k = self._kinds.generate(rng)
        # bias toward the happy path so sequences get deep enough to reach refunds
        b = ProcessorBehaviour.APPROVE if rng.next_int(10) < 7 else self._behaviours.generate(rng)
        return Command(k, self._amounts.generate(rng), b)

    def shrink(self, c: Command) -> list[Command]:
        out = [Command(c.kind, c.amount_minor, b) for b in self._behaviours.shrink(c.behaviour)]
        out.extend(Command(c.kind, a, c.behaviour) for a in self._amounts.shrink(c.amount_minor))
        return out


class ScenarioRunner:
    COMMANDS = CommandGen()

    @staticmethod
    def sequences(max_len: int) -> ListGen[Command]:
        return Gens.lists(ScenarioRunner.COMMANDS, max_len)

    @staticmethod
    def run(scenario: Scenario, bugs: Bugs) -> ScenarioRun:
        """Applies the commands in order. Commands the service rejects as illegal are logged and skipped."""
        processor = FakeProcessor(USD)
        service = PaymentService(USD, processor, bugs)
        log: list[str] = []
        for c in scenario.commands:
            processor.script(c.behaviour)
            amount = Money.of_minor(c.amount_minor, USD)
            try:
                if c.kind is CommandKind.AUTHORIZE:
                    outcome = "ok" if service.authorize(amount) else "declined/unknown"
                elif c.kind is CommandKind.CAPTURE:
                    outcome = "ok" if service.capture(amount) else "declined/unknown"
                elif c.kind is CommandKind.REFUND:
                    outcome = "ok" if service.refund(amount) else "declined/unknown"
                elif c.kind is CommandKind.VOID:
                    outcome = "ok" if service.void_authorization() else "declined/unknown"
                else:
                    service.reconcile()
                    outcome = "ok"
            except (ValueError, IllegalStateError) as rejected:
                outcome = f"rejected: {rejected}"
            log.append(f"{c} -> {outcome} [{service.state.name}]")
            violation = Invariants.check(service)
            if violation is not None:
                return ScenarioRun(scenario, service, tuple(log), f"{violation} after {c}")
        return ScenarioRun(scenario, service, tuple(log), None)

    @staticmethod
    def invariants_hold(bugs: Bugs, seed: int, trials: int, max_len: int) -> PropertyResult[list[Command]]:
        """The property: no command sequence, with any processor behaviour, violates the invariants."""
        return Property.for_all(f"invariants hold under {bugs}", ScenarioRunner.sequences(max_len),
                                lambda cmds: ScenarioRunner.run(Scenario("generated", tuple(cmds)), bugs).violation, seed, trials)


# ---------------------------------------------------------------------------------------------
# Demo
# ---------------------------------------------------------------------------------------------


def main() -> None:
    print("== One scripted scenario: capture times out, reconcile repairs the ledger ==")
    timeout = Scenario("timeout then reconcile", (
        Command(CommandKind.AUTHORIZE, 5000, ProcessorBehaviour.APPROVE),
        Command(CommandKind.CAPTURE, 5000, ProcessorBehaviour.TIMEOUT_AFTER_APPLY),
        Command(CommandKind.RECONCILE, 0, ProcessorBehaviour.APPROVE),
        Command(CommandKind.REFUND, 1250, ProcessorBehaviour.APPROVE)))
    r = ScenarioRunner.run(timeout, NO_BUGS)
    for line in r.log:
        print(f"  {line}")
    ledger = r.service.ledger
    print(f"  ledger: customer {ledger.balance(Account.CUSTOMER)}, merchant {ledger.balance(Account.MERCHANT)}, "
          f"fees {ledger.balance(Account.PLATFORM_FEES)}, total {ledger.total()}")
    print(f"  invariants: {'hold' if r.passed() else r.violation}")

    print("\n== Property: invariants hold for 500 random sequences (seed 42) ==")
    print(ScenarioRunner.invariants_hold(NO_BUGS, 42, 500, 12).summary())

    print("\n== Same property with real bugs switched back on (500 trials) ==")
    for bug in (Bugs(True, False, False, False), Bugs(False, True, False, False), Bugs(False, False, True, False), Bugs(False, False, False, True)):
        print(ScenarioRunner.invariants_hold(bug, 42, 500, 12).summary())
    print("\n== The timeout bug needs AUTHORIZE, CAPTURE/TIMEOUT_BEFORE_APPLY, RECONCILE in that order: 2000 trials ==")
    print(ScenarioRunner.invariants_hold(Bugs(False, False, False, True), 42, 2000, 12).summary())

    print("\n== Shrinking a plain integer property: 'truncating the 2.9% fee equals HALF_EVEN' ==")

    def truncated_equals_rounded(n: int) -> Optional[str]:
        truncated = n * PaymentService.FEE_BPS // 10_000
        rounded = Money.of_minor(n, USD).basis_points(PaymentService.FEE_BPS).minor_units
        return None if truncated == rounded else f"n={n}: truncated {truncated} != rounded {rounded}"

    ints = Property.for_all("truncated fee == rounded fee", Gens.longs(1, 1_000_000), truncated_equals_rounded, 7, 10_000)
    print(ints.summary())
    print(f"\nFee on 100.00 USD at 2.9% = {Money.of_minor(10_000, USD).basis_points(PaymentService.FEE_BPS)}")


if __name__ == "__main__":
    main()
