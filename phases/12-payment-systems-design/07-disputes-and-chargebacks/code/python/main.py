"""Lesson: phases/12-payment-systems-design/07-disputes-and-chargebacks/docs/en.md
Disputes and Chargebacks: The Lifecycle and the Evidence
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Visa Core Rules (Dispute Resolution: Allocation and Collaboration flows, reason codes 10-13);
         Mastercard Chargeback Guide (reason codes 4834, 4837, 4853; second presentment; pre-arbitration).
Python 3.11+, standard library only. Same design as the Java version.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import date, timedelta
from enum import Enum
from typing import Optional, Protocol


# ---------------------------------------------------------------------------------------------
# Money: integer minor units + ISO 4217 currency (Phase 10, Lesson 01). Copied, not imported.
# ---------------------------------------------------------------------------------------------


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


class CurrencyMismatchError(ValueError):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


@dataclass(frozen=True)
class Money:
    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")

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

    def negate(self) -> "Money":
        return Money(-self.minor_units, self.currency)

    def is_positive(self) -> bool:
        return self.minor_units > 0

    def is_zero(self) -> bool:
        return self.minor_units == 0

    def compare_to(self, other: "Money") -> int:
        self._require_same_currency(other)
        return (self.minor_units > other.minor_units) - (self.minor_units < other.minor_units)

    def __str__(self) -> str:
        magnitude = abs(self.minor_units)
        whole, frac = divmod(magnitude, self.currency.minor_units_per_major())
        text = str(whole)
        if self.currency.exponent > 0:
            text += "." + str(frac).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{text} {self.currency.code}"

    __repr__ = __str__


# ---------------------------------------------------------------------------------------------
# A dispute lives for months. The clock is a calendar date that tests move forward by days.
# ---------------------------------------------------------------------------------------------


class Clock(Protocol):
    def today(self) -> date: ...


class FakeClock:
    def __init__(self, start: date) -> None:
        self._today = start

    def today(self) -> date:
        return self._today

    def advance_days(self, days: int) -> None:
        self._today = self._today + timedelta(days=days)


# ---------------------------------------------------------------------------------------------
# Minimal double-entry ledger (Phase 10, Lesson 03).
# ---------------------------------------------------------------------------------------------


class Side(Enum):
    DEBIT = "debit"
    CREDIT = "credit"


class AccountType(Enum):
    ASSET = ("asset", Side.DEBIT)
    LIABILITY = ("liability", Side.CREDIT)
    REVENUE = ("revenue", Side.CREDIT)

    def __init__(self, _label: str, normal: Side) -> None:
        self.normal = normal


@dataclass(frozen=True)
class Posting:
    account: str
    side: Side
    amount: Money

    def __post_init__(self) -> None:
        if not self.amount.is_positive():
            raise ValueError(f"posting amount must be positive: {self.amount}")

    @staticmethod
    def debit(account: str, amount: Money) -> "Posting":
        return Posting(account, Side.DEBIT, amount)

    @staticmethod
    def credit(account: str, amount: Money) -> "Posting":
        return Posting(account, Side.CREDIT, amount)

    def __str__(self) -> str:
        return ("Dr " if self.side is Side.DEBIT else "   Cr ") + f"{self.account} {self.amount}"


@dataclass(frozen=True)
class JournalEntry:
    id: str
    date: date
    description: str
    postings: tuple[Posting, ...]

    def __post_init__(self) -> None:
        object.__setattr__(self, "postings", tuple(self.postings))
        net = sum(p.amount.minor_units if p.side is Side.DEBIT else -p.amount.minor_units for p in self.postings)
        if len(self.postings) < 2 or net != 0:
            raise ValueError(f"entry {self.id} is unbalanced or too short")


class Ledger:
    def __init__(self, currency: Currency) -> None:
        self._accounts: dict[str, AccountType] = {}
        self._balances: dict[str, int] = {}
        self._journal: list[JournalEntry] = []
        self._currency = currency

    def open(self, account: str, type_: AccountType) -> None:
        self._accounts[account] = type_
        self._balances[account] = 0

    def post(self, e: JournalEntry) -> None:
        for p in e.postings:
            if p.account not in self._accounts:
                raise ValueError(f"unknown account {p.account}")
        self._journal.append(e)
        for p in e.postings:
            signed = p.amount.minor_units if p.side is self._accounts[p.account].normal else -p.amount.minor_units
            self._balances[p.account] += signed

    def balance(self, account: str) -> Money:
        return Money.of_minor(self._balances[account], self._currency)

    def balances(self) -> bool:
        debit_side = sum(self._balances[a] for a, t in self._accounts.items() if t.normal is Side.DEBIT)
        credit_side = sum(self._balances[a] for a, t in self._accounts.items() if t.normal is Side.CREDIT)
        return debit_side == credit_side

    def journal(self) -> tuple[JournalEntry, ...]:
        return tuple(self._journal)


# ---------------------------------------------------------------------------------------------
# Reason codes and evidence. A reason code says what the cardholder claims; it decides which
# evidence can answer the claim. Each requirement is "at least one of" a set of evidence types.
# ---------------------------------------------------------------------------------------------


class ReasonCategory(Enum):
    FRAUD = "fraud"
    AUTHORIZATION = "authorization"
    PROCESSING_ERROR = "processing_error"
    CONSUMER_DISPUTE = "consumer_dispute"

    def __str__(self) -> str:
        return self.name


class EvidenceType(Enum):
    RECEIPT = 0
    PROOF_OF_DELIVERY = 1
    SIGNED_DELIVERY = 2
    PRODUCT_DESCRIPTION = 3
    CUSTOMER_COMMUNICATION = 4
    REFUND_POLICY = 5
    THREE_DS_AUTHENTICATION = 6
    PRIOR_UNDISPUTED_TRANSACTIONS = 7
    AVS_CVV_MATCH = 8
    AUTHORIZATION_RECORD = 9
    DUPLICATE_EXPLANATION = 10
    REFUND_ISSUED = 11

    def __str__(self) -> str:
        return self.name


Requirement = frozenset[EvidenceType]


def format_groups(groups: list[Requirement] | tuple[Requirement, ...]) -> str:
    """Java's EnumSet prints in declaration order; a frozenset does not, so sort by the enum's position."""
    return "[" + ", ".join("[" + ", ".join(t.name for t in sorted(g, key=lambda t: t.value)) + "]" for g in groups) + "]"


@dataclass(frozen=True)
class ReasonCode:
    network: str
    code: str
    title: str
    category: ReasonCategory
    requirements: tuple[Requirement, ...]

    def __post_init__(self) -> None:
        object.__setattr__(self, "requirements", tuple(frozenset(r) for r in self.requirements))

    def key(self) -> str:
        return f"{self.network} {self.code}"


class ReasonCodes:
    TABLE: dict[str, ReasonCode] = {}

    @staticmethod
    def _add(network: str, code: str, title: str, cat: ReasonCategory, *reqs: set[EvidenceType]) -> None:
        rc = ReasonCode(network, code, title, cat, tuple(frozenset(r) for r in reqs))
        ReasonCodes.TABLE[rc.key()] = rc

    @staticmethod
    def get(network: str, code: str) -> ReasonCode:
        rc = ReasonCodes.TABLE.get(f"{network} {code}")
        if rc is None:
            raise ValueError(f"unknown reason code {network} {code}")
        return rc


_E = EvidenceType
ReasonCodes._add("VISA", "10.4", "Other Fraud: Card-Absent Environment", ReasonCategory.FRAUD,
                 {_E.THREE_DS_AUTHENTICATION, _E.PRIOR_UNDISPUTED_TRANSACTIONS}, {_E.RECEIPT})
ReasonCodes._add("VISA", "11.3", "No Authorization", ReasonCategory.AUTHORIZATION, {_E.AUTHORIZATION_RECORD})
ReasonCodes._add("VISA", "12.6.1", "Duplicate Processing", ReasonCategory.PROCESSING_ERROR, {_E.DUPLICATE_EXPLANATION})
ReasonCodes._add("VISA", "13.1", "Merchandise/Services Not Received", ReasonCategory.CONSUMER_DISPUTE,
                 {_E.PROOF_OF_DELIVERY, _E.SIGNED_DELIVERY}, {_E.RECEIPT})
ReasonCodes._add("VISA", "13.3", "Not as Described or Defective", ReasonCategory.CONSUMER_DISPUTE,
                 {_E.PRODUCT_DESCRIPTION}, {_E.CUSTOMER_COMMUNICATION, _E.REFUND_POLICY})
ReasonCodes._add("VISA", "13.6", "Credit Not Processed", ReasonCategory.CONSUMER_DISPUTE,
                 {_E.REFUND_ISSUED, _E.REFUND_POLICY})
ReasonCodes._add("MASTERCARD", "4837", "No Cardholder Authorization", ReasonCategory.FRAUD,
                 {_E.THREE_DS_AUTHENTICATION, _E.PRIOR_UNDISPUTED_TRANSACTIONS})
ReasonCodes._add("MASTERCARD", "4834", "Point-of-Interaction Error", ReasonCategory.PROCESSING_ERROR,
                 {_E.DUPLICATE_EXPLANATION, _E.RECEIPT})
ReasonCodes._add("MASTERCARD", "4853", "Cardholder Dispute", ReasonCategory.CONSUMER_DISPUTE,
                 {_E.PROOF_OF_DELIVERY, _E.PRODUCT_DESCRIPTION}, {_E.CUSTOMER_COMMUNICATION})


@dataclass(frozen=True)
class EvidenceItem:
    type: EvidenceType
    reference: str


@dataclass(frozen=True)
class EvidenceBundle:
    items: tuple[EvidenceItem, ...]

    def __post_init__(self) -> None:
        object.__setattr__(self, "items", tuple(self.items))

    @staticmethod
    def of(*items: EvidenceItem) -> "EvidenceBundle":
        return EvidenceBundle(tuple(items))

    def has(self, t: EvidenceType) -> bool:
        return any(i.type is t for i in self.items)

    def missing_for(self, reason: ReasonCode) -> list[Requirement]:
        """Requirements not met by any item: each entry is a set of which at least one must be present."""
        return [req for req in reason.requirements if not any(self.has(t) for t in req)]

    def liability_shift(self, reason: ReasonCode) -> bool:
        """A fraud claim against a transaction the issuer itself authenticated with 3-D Secure shifts liability to the issuer."""
        return reason.category is ReasonCategory.FRAUD and self.has(EvidenceType.THREE_DS_AUTHENTICATION)


# ---------------------------------------------------------------------------------------------
# The dispute state machine (the Lesson 02 design: a table of rules, guards, a transition log,
# event-id deduplication), with one addition: most states carry a deadline, and when the clock
# passes it the system itself applies DEADLINE_PASSED.
# ---------------------------------------------------------------------------------------------


class DisputeState(Enum):
    INQUIRY = "inquiry"
    INQUIRY_RESPONDED = "inquiry_responded"
    CHARGEBACK = "chargeback"
    REPRESENTED = "represented"
    PRE_ARBITRATION = "pre_arbitration"
    PRE_ARB_RESPONDED = "pre_arb_responded"
    ARBITRATION = "arbitration"
    CLOSED_NO_CHARGEBACK = "closed_no_chargeback"
    WON = "won"
    LOST = "lost"

    def is_terminal(self) -> bool:
        return self in (DisputeState.CLOSED_NO_CHARGEBACK, DisputeState.WON, DisputeState.LOST)

    def __str__(self) -> str:
        return self.name


class DisputeEventKind(Enum):
    RESPOND_INQUIRY = "respond_inquiry"
    INQUIRY_CLOSED = "inquiry_closed"
    CHARGEBACK_RECEIVED = "chargeback_received"
    ACCEPT = "accept"
    SUBMIT_REPRESENTMENT = "submit_representment"
    ISSUER_ACCEPTED = "issuer_accepted"
    PRE_ARB_RECEIVED = "pre_arb_received"
    RESPOND_PRE_ARB = "respond_pre_arb"
    ISSUER_WITHDREW = "issuer_withdrew"
    ARBITRATION_FILED = "arbitration_filed"
    RULING_FOR_MERCHANT = "ruling_for_merchant"
    RULING_FOR_ISSUER = "ruling_for_issuer"
    DEADLINE_PASSED = "deadline_passed"

    def __str__(self) -> str:
        return self.name


class DisputeGuard(Enum):
    ALWAYS = "always"
    EVIDENCE_COMPLETE_IN_TIME = "evidence_complete_in_time"
    DEADLINE_ELAPSED = "deadline_elapsed"


@dataclass(frozen=True)
class DisputeRule:
    from_state: DisputeState
    kind: DisputeEventKind
    guard: DisputeGuard
    to_state: DisputeState


@dataclass(frozen=True)
class DisputeEvent:
    event_id: str
    kind: DisputeEventKind
    evidence: Optional[EvidenceBundle]
    note: str

    @staticmethod
    def of(event_id: str, kind: DisputeEventKind, note: str) -> "DisputeEvent":
        return DisputeEvent(event_id, kind, None, note)

    @staticmethod
    def with_evidence(event_id: str, kind: DisputeEventKind, evidence: EvidenceBundle, note: str) -> "DisputeEvent":
        if evidence is None:
            raise TypeError("evidence")
        return DisputeEvent(event_id, kind, evidence, note)


@dataclass(frozen=True)
class DisputeTransition:
    version: int
    date: date
    event_id: str
    kind: Optional[DisputeEventKind]
    from_state: Optional[DisputeState]
    to_state: DisputeState
    note: str

    def __str__(self) -> str:
        kind = "OPENED" if self.kind is None else self.kind.name
        frm = "-" if self.from_state is None else self.from_state.name
        return f"v{self.version:<2} {self.date}  {kind:<20} {frm:<17} -> {self.to_state.name:<17} {self.note}"


class IllegalDisputeTransitionError(RuntimeError):
    pass


class ApplyOutcome(Enum):
    APPLIED = "applied"
    DUPLICATE = "duplicate"

    def __str__(self) -> str:
        return self.name


@dataclass(frozen=True)
class DeadlinePolicy:
    """Days allowed in each state before DEADLINE_PASSED fires. A state absent from the map has no automatic deadline."""

    days: dict[DisputeState, int]

    @staticmethod
    def standard() -> "DeadlinePolicy":
        return DeadlinePolicy({
            DisputeState.INQUIRY: 20,            # respond to a retrieval request (shown as overdue only)
            DisputeState.CHARGEBACK: 20,         # merchant response; the network gives the acquirer 30
            DisputeState.REPRESENTED: 30,        # issuer may answer with pre-arbitration
            DisputeState.PRE_ARBITRATION: 30,    # merchant accepts or responds
            DisputeState.PRE_ARB_RESPONDED: 10,  # issuer may escalate to arbitration
        })


class Dispute:
    def __init__(self, id: str, payment_id: str, amount: Money, reason: ReasonCode, initial: DisputeState, today: date) -> None:
        self.id = id
        self.payment_id = payment_id
        self.amount = amount
        self.reason = reason
        self.state = initial
        self.state_entered_on = today
        self.deadline: Optional[date] = None  # None when the current state has no deadline
        self.evidence: Optional[EvidenceBundle] = None
        self.history: list[DisputeTransition] = []
        self.applied_event_ids: set[str] = set()


class DisputeService:
    MERCHANT = "merchant_payable"   # liability: what we owe the merchant
    SCHEME = "scheme_settlement"    # asset: net position with the card scheme
    HOLD = "dispute_hold"           # liability: funds re-credited by the scheme, held until the outcome
    FEES = "fee_revenue"            # revenue: chargeback fees

    RULES: tuple[DisputeRule, ...] = (
        DisputeRule(DisputeState.INQUIRY, DisputeEventKind.RESPOND_INQUIRY, DisputeGuard.EVIDENCE_COMPLETE_IN_TIME, DisputeState.INQUIRY_RESPONDED),
        DisputeRule(DisputeState.INQUIRY, DisputeEventKind.CHARGEBACK_RECEIVED, DisputeGuard.ALWAYS, DisputeState.CHARGEBACK),
        DisputeRule(DisputeState.INQUIRY_RESPONDED, DisputeEventKind.INQUIRY_CLOSED, DisputeGuard.ALWAYS, DisputeState.CLOSED_NO_CHARGEBACK),
        DisputeRule(DisputeState.INQUIRY_RESPONDED, DisputeEventKind.CHARGEBACK_RECEIVED, DisputeGuard.ALWAYS, DisputeState.CHARGEBACK),
        DisputeRule(DisputeState.CHARGEBACK, DisputeEventKind.ACCEPT, DisputeGuard.ALWAYS, DisputeState.LOST),
        DisputeRule(DisputeState.CHARGEBACK, DisputeEventKind.SUBMIT_REPRESENTMENT, DisputeGuard.EVIDENCE_COMPLETE_IN_TIME, DisputeState.REPRESENTED),
        DisputeRule(DisputeState.CHARGEBACK, DisputeEventKind.DEADLINE_PASSED, DisputeGuard.DEADLINE_ELAPSED, DisputeState.LOST),
        DisputeRule(DisputeState.REPRESENTED, DisputeEventKind.ISSUER_ACCEPTED, DisputeGuard.ALWAYS, DisputeState.WON),
        DisputeRule(DisputeState.REPRESENTED, DisputeEventKind.PRE_ARB_RECEIVED, DisputeGuard.ALWAYS, DisputeState.PRE_ARBITRATION),
        DisputeRule(DisputeState.REPRESENTED, DisputeEventKind.DEADLINE_PASSED, DisputeGuard.DEADLINE_ELAPSED, DisputeState.WON),
        DisputeRule(DisputeState.PRE_ARBITRATION, DisputeEventKind.ACCEPT, DisputeGuard.ALWAYS, DisputeState.LOST),
        DisputeRule(DisputeState.PRE_ARBITRATION, DisputeEventKind.RESPOND_PRE_ARB, DisputeGuard.EVIDENCE_COMPLETE_IN_TIME, DisputeState.PRE_ARB_RESPONDED),
        DisputeRule(DisputeState.PRE_ARBITRATION, DisputeEventKind.DEADLINE_PASSED, DisputeGuard.DEADLINE_ELAPSED, DisputeState.LOST),
        DisputeRule(DisputeState.PRE_ARB_RESPONDED, DisputeEventKind.ISSUER_WITHDREW, DisputeGuard.ALWAYS, DisputeState.WON),
        DisputeRule(DisputeState.PRE_ARB_RESPONDED, DisputeEventKind.ARBITRATION_FILED, DisputeGuard.ALWAYS, DisputeState.ARBITRATION),
        DisputeRule(DisputeState.PRE_ARB_RESPONDED, DisputeEventKind.DEADLINE_PASSED, DisputeGuard.DEADLINE_ELAPSED, DisputeState.WON),
        DisputeRule(DisputeState.ARBITRATION, DisputeEventKind.RULING_FOR_MERCHANT, DisputeGuard.ALWAYS, DisputeState.WON),
        DisputeRule(DisputeState.ARBITRATION, DisputeEventKind.RULING_FOR_ISSUER, DisputeGuard.ALWAYS, DisputeState.LOST),
    )

    def __init__(self, ledger: Ledger, clock: Clock, deadlines: DeadlinePolicy, chargeback_fee: Money, arbitration_fee: Money) -> None:
        self._ledger = ledger
        self._clock = clock
        self._deadlines = deadlines
        self._chargeback_fee = chargeback_fee
        self._arbitration_fee = arbitration_fee
        self._disputes: dict[str, Dispute] = {}
        self._entry_seq = 0
        ledger.open(DisputeService.SCHEME, AccountType.ASSET)
        ledger.open(DisputeService.MERCHANT, AccountType.LIABILITY)
        ledger.open(DisputeService.HOLD, AccountType.LIABILITY)
        ledger.open(DisputeService.FEES, AccountType.REVENUE)

    def ledger(self) -> Ledger:
        return self._ledger

    def dispute(self, id: str) -> Dispute:
        d = self._disputes.get(id)
        if d is None:
            raise ValueError(f"unknown dispute {id}")
        return d

    def open_inquiry(self, id: str, payment_id: str, amount: Money, reason: ReasonCode) -> Dispute:
        """A retrieval request: the issuer asks for information. No money moves."""
        return self._open(id, payment_id, amount, reason, DisputeState.INQUIRY)

    def open_chargeback(self, id: str, payment_id: str, amount: Money, reason: ReasonCode) -> Dispute:
        """A first chargeback: the scheme has already taken the money back from us."""
        d = self._open(id, payment_id, amount, reason, DisputeState.CHARGEBACK)
        self._post_money(d, DisputeState.INQUIRY, DisputeState.CHARGEBACK)
        return d

    def _open(self, id: str, payment_id: str, amount: Money, reason: ReasonCode, initial: DisputeState) -> Dispute:
        if id in self._disputes:
            raise RuntimeError(f"dispute exists: {id}")
        if not amount.is_positive():
            raise ValueError("dispute amount must be positive")
        today = self._clock.today()
        d = Dispute(id, payment_id, amount, reason, initial, today)
        d.deadline = self._deadline_for(initial, today)
        d.history.append(DisputeTransition(0, today, f"open:{id}", None, None, initial, f"opened: {reason.key()} {reason.title}"))
        self._disputes[id] = d
        return d

    def _deadline_for(self, s: DisputeState, entered: date) -> Optional[date]:
        days = self._deadlines.days.get(s)
        return None if days is None else entered + timedelta(days=days)

    def resolve(self, d: Dispute, e: DisputeEvent) -> DisputeRule:
        """Pick the rule, check its guard, or explain precisely why not. Pure: never mutates."""
        if d.state.is_terminal():
            raise IllegalDisputeTransitionError(f"{d.id} is {d.state.name} (terminal); {e.kind.name} refused")
        rule = next((r for r in DisputeService.RULES if r.from_state is d.state and r.kind is e.kind), None)
        if rule is None:
            raise IllegalDisputeTransitionError(f"{d.id}: {e.kind.name} is not legal from {d.state.name}")
        today = self._clock.today()
        if rule.guard is DisputeGuard.DEADLINE_ELAPSED and (d.deadline is None or not today > d.deadline):
            raise IllegalDisputeTransitionError(f"{d.id}: DEADLINE_PASSED on {today} but the deadline is {d.deadline}")
        if rule.guard is DisputeGuard.EVIDENCE_COMPLETE_IN_TIME:
            if d.deadline is not None and today > d.deadline:
                raise IllegalDisputeTransitionError(f"{d.id}: {e.kind.name} on {today} is after the deadline {d.deadline}")
            if e.evidence is None:
                raise IllegalDisputeTransitionError(f"{d.id}: {e.kind.name} needs an evidence bundle")
            missing = e.evidence.missing_for(d.reason)
            if missing:
                raise IllegalDisputeTransitionError(
                    f"{d.id}: evidence for {d.reason.key()} is incomplete; missing one of {format_groups(missing)}")
        return rule

    def apply(self, dispute_id: str, e: DisputeEvent) -> ApplyOutcome:
        d = self.dispute(dispute_id)
        if e.event_id in d.applied_event_ids:
            return ApplyOutcome.DUPLICATE
        rule = self.resolve(d, e)  # raises before any mutation
        frm = d.state
        today = self._clock.today()
        self._post_money(d, frm, rule.to_state)
        d.state = rule.to_state
        d.state_entered_on = today
        d.deadline = None if rule.to_state.is_terminal() else self._deadline_for(rule.to_state, today)
        if e.evidence is not None:
            d.evidence = e.evidence
        d.applied_event_ids.add(e.event_id)
        d.history.append(DisputeTransition(len(d.history), today, e.event_id, e.kind, frm, rule.to_state, e.note))
        return ApplyOutcome.APPLIED

    def sweep_deadlines(self) -> list[str]:
        """Apply DEADLINE_PASSED to every open dispute whose deadline is before today and whose state has a
        deadline rule. The event id names the state and the deadline, so running the sweep twice is harmless.
        INQUIRY has a deadline but no rule: it is reported overdue, and only the issuer can move it."""
        fired: list[str] = []
        today = self._clock.today()
        for d in list(self._disputes.values()):
            if d.state.is_terminal() or d.deadline is None or not today > d.deadline:
                continue
            has_rule = any(r.from_state is d.state and r.kind is DisputeEventKind.DEADLINE_PASSED for r in DisputeService.RULES)
            if not has_rule:
                fired.append(f"{d.id} overdue in {d.state.name} (no automatic outcome)")
                continue
            before = d.state
            self.apply(d.id, DisputeEvent.of(f"deadline:{d.id}:{d.state.name}:{d.deadline}", DisputeEventKind.DEADLINE_PASSED,
                                             f"deadline {d.deadline} passed"))
            fired.append(f"{d.id} {before.name} -> {d.state.name}")
        return fired

    def _post_money(self, d: Dispute, frm: DisputeState, to: DisputeState) -> None:
        """The money that moves on each transition. The first chargeback debits the merchant (plus a fee);
        a representment brings the funds back from the scheme into a hold, not to the merchant; the
        outcome either releases the hold to the merchant or returns it to the scheme."""
        p: list[Posting] = []
        if to is DisputeState.CHARGEBACK:
            what = "chargeback debited"
            p.append(Posting.debit(DisputeService.MERCHANT, d.amount))
            p.append(Posting.credit(DisputeService.SCHEME, d.amount))
            p.append(Posting.debit(DisputeService.MERCHANT, self._chargeback_fee))
            p.append(Posting.credit(DisputeService.FEES, self._chargeback_fee))
        elif to is DisputeState.REPRESENTED:
            what = "representment: funds re-credited, held"
            p.append(Posting.debit(DisputeService.SCHEME, d.amount))
            p.append(Posting.credit(DisputeService.HOLD, d.amount))
        elif to is DisputeState.WON and frm is not DisputeState.CHARGEBACK:
            what = "won: hold released to merchant"
            p.append(Posting.debit(DisputeService.HOLD, d.amount))
            p.append(Posting.credit(DisputeService.MERCHANT, d.amount))
        elif to is DisputeState.LOST and frm is not DisputeState.CHARGEBACK:
            what = "lost after representment: hold returned to scheme"
            p.append(Posting.debit(DisputeService.HOLD, d.amount))
            p.append(Posting.credit(DisputeService.SCHEME, d.amount))
            if frm is DisputeState.ARBITRATION:
                p.append(Posting.debit(DisputeService.MERCHANT, self._arbitration_fee))
                p.append(Posting.credit(DisputeService.SCHEME, self._arbitration_fee))
        else:
            return  # inquiry steps, pre-arbitration, arbitration filing, and LOST from CHARGEBACK move no money
        self._entry_seq += 1
        self._ledger.post(JournalEntry(f"je_{self._entry_seq:03d}", self._clock.today(), f"{d.id} {what}", tuple(p)))

    def merchant_impact(self, dispute_id: str) -> Money:
        """What this dispute has cost the merchant so far: the net of every merchant_payable posting tagged with its id."""
        net = Money.zero(self.dispute(dispute_id).amount.currency)
        for e in self._ledger.journal():
            if not e.description.startswith(dispute_id + " "):
                continue
            for p in e.postings:
                if p.account != DisputeService.MERCHANT:
                    continue
                net = net.plus(p.amount) if p.side is Side.CREDIT else net.minus(p.amount)
        return net

    @staticmethod
    def days_between(a: date, b: date) -> int:
        return (b - a).days


# ---------------------------------------------------------------------------------------------
# Demo
# ---------------------------------------------------------------------------------------------


def usd(minor: int) -> Money:
    return Money.of_minor(minor, USD)


def service(clock: FakeClock) -> DisputeService:
    return DisputeService(Ledger(USD), clock, DeadlinePolicy.standard(), usd(1_500), usd(50_000))


def _fmt_list(items: list[str]) -> str:
    return "[" + ", ".join(items) + "]"


def step(svc: DisputeService, clock: FakeClock, start: date, id: str, label: str) -> None:
    d = svc.dispute(id)
    deadline = "-" if d.deadline is None else str(d.deadline)
    print(f"day {DisputeService.days_between(start, clock.today()):3d}  {label:<40} state {d.state.name:<17} "
          f"deadline {deadline:<10} hold {str(svc.ledger().balance(DisputeService.HOLD)):<11} merchant {svc.merchant_impact(id)}")


def main() -> None:
    print("== Reason codes and the evidence that answers them (each group: at least one of) ==")
    for rc in ReasonCodes.TABLE.values():
        print(f"{rc.key():<17} {rc.category.name:<17} {rc.title:<38} {format_groups(rc.requirements)}")

    print("\n== A 'not received' dispute that runs the whole course: 249.00 USD, Visa 13.1 ==")
    start = date(2026, 2, 2)
    clock = FakeClock(start)
    svc = service(clock)
    not_received = ReasonCodes.get("VISA", "13.1")
    svc.open_chargeback("dp_1", "pay_77", usd(24_900), not_received)
    step(svc, clock, start, "dp_1", "first chargeback received")
    clock.advance_days(9)
    thin = EvidenceBundle.of(EvidenceItem(EvidenceType.RECEIPT, "inv_77.pdf"))
    try:
        svc.apply("dp_1", DisputeEvent.with_evidence("ev_1", DisputeEventKind.SUBMIT_REPRESENTMENT, thin, "receipt only"))
    except IllegalDisputeTransitionError as e:
        print(f"         refused: {e}")
    clock.advance_days(3)
    full = EvidenceBundle.of(EvidenceItem(EvidenceType.RECEIPT, "inv_77.pdf"),
                             EvidenceItem(EvidenceType.SIGNED_DELIVERY, "carrier POD 1Z999 signed J. DOE"))
    svc.apply("dp_1", DisputeEvent.with_evidence("ev_2", DisputeEventKind.SUBMIT_REPRESENTMENT, full, "receipt + signed delivery"))
    step(svc, clock, start, "dp_1", "representment submitted")
    clock.advance_days(29)
    svc.apply("dp_1", DisputeEvent.of("iss_3", DisputeEventKind.PRE_ARB_RECEIVED, "issuer: cardholder says signature not theirs"))
    step(svc, clock, start, "dp_1", "pre-arbitration from issuer")
    clock.advance_days(15)
    more = EvidenceBundle.of(EvidenceItem(EvidenceType.RECEIPT, "inv_77.pdf"),
                             EvidenceItem(EvidenceType.SIGNED_DELIVERY, "carrier POD 1Z999 signed J. DOE"),
                             EvidenceItem(EvidenceType.CUSTOMER_COMMUNICATION, "email 2026-01-30 confirming arrival"))
    svc.apply("dp_1", DisputeEvent.with_evidence("ev_4", DisputeEventKind.RESPOND_PRE_ARB, more, "pre-arb declined with new evidence"))
    step(svc, clock, start, "dp_1", "pre-arbitration response")
    clock.advance_days(7)
    svc.apply("dp_1", DisputeEvent.of("iss_5", DisputeEventKind.ARBITRATION_FILED, "issuer filed with Visa"))
    step(svc, clock, start, "dp_1", "issuer files arbitration")
    clock.advance_days(55)
    svc.apply("dp_1", DisputeEvent.of("net_6", DisputeEventKind.RULING_FOR_MERCHANT, "Visa ruling: merchant"))
    step(svc, clock, start, "dp_1", "network ruling for the merchant")
    print("history:")
    for t in svc.dispute("dp_1").history:
        print(f"   {t}")
    print(f"duplicate ruling delivery -> {svc.apply('dp_1', DisputeEvent.of('net_6', DisputeEventKind.RULING_FOR_MERCHANT, 'again'))}")

    print("\n== Missed deadline: a fraud chargeback nobody answers ==")
    s2 = clock.today()
    svc.open_chargeback("dp_2", "pay_88", usd(8_000), ReasonCodes.get("VISA", "10.4"))
    clock.advance_days(20)
    print(f"day 20 sweep: {_fmt_list(svc.sweep_deadlines())}")
    clock.advance_days(1)
    print(f"day 21 sweep: {_fmt_list(svc.sweep_deadlines())}")
    step(svc, clock, s2, "dp_2", "after the sweep")

    print("\n== 3-D Secure: a fraud claim on an authenticated payment ==")
    s3 = clock.today()
    fraud = ReasonCodes.get("MASTERCARD", "4837")
    svc.open_chargeback("dp_3", "pay_99", usd(12_000), fraud)
    three_ds = EvidenceBundle.of(EvidenceItem(EvidenceType.THREE_DS_AUTHENTICATION, "ECI 02, authentication value present"))
    print(f"liability shift: {str(three_ds.liability_shift(fraud)).lower()}")
    clock.advance_days(4)
    svc.apply("dp_3", DisputeEvent.with_evidence("ev_31", DisputeEventKind.SUBMIT_REPRESENTMENT, three_ds, "3DS authenticated"))
    clock.advance_days(31)
    print(f"day 35 sweep: {_fmt_list(svc.sweep_deadlines())}")
    step(svc, clock, s3, "dp_3", "issuer silent past its deadline")

    print("\n== Inquiry answered, issuer closes it: no money moves ==")
    s4 = clock.today()
    svc.open_inquiry("dp_4", "pay_11", usd(4_500), ReasonCodes.get("VISA", "13.3"))
    svc.apply("dp_4", DisputeEvent.with_evidence("ev_41", DisputeEventKind.RESPOND_INQUIRY,
                                                 EvidenceBundle.of(EvidenceItem(EvidenceType.PRODUCT_DESCRIPTION, "listing snapshot"),
                                                                   EvidenceItem(EvidenceType.REFUND_POLICY, "returns page")), "answered"))
    clock.advance_days(6)
    svc.apply("dp_4", DisputeEvent.of("iss_42", DisputeEventKind.INQUIRY_CLOSED, "issuer satisfied"))
    step(svc, clock, s4, "dp_4", "inquiry closed")

    print("\n== Arbitration lost: amount, chargeback fee, and arbitration fee ==")
    s5 = clock.today()
    svc.open_chargeback("dp_5", "pay_55", usd(30_000), ReasonCodes.get("MASTERCARD", "4853"))
    spec_and_chat = EvidenceBundle.of(EvidenceItem(EvidenceType.PRODUCT_DESCRIPTION, "spec sheet"),
                                      EvidenceItem(EvidenceType.CUSTOMER_COMMUNICATION, "chat log"))
    svc.apply("dp_5", DisputeEvent.with_evidence("ev_51", DisputeEventKind.SUBMIT_REPRESENTMENT, spec_and_chat, "represented"))
    svc.apply("dp_5", DisputeEvent.of("iss_52", DisputeEventKind.PRE_ARB_RECEIVED, "issuer disagrees"))
    svc.apply("dp_5", DisputeEvent.with_evidence("ev_53", DisputeEventKind.RESPOND_PRE_ARB, spec_and_chat, "declined pre-arb"))
    svc.apply("dp_5", DisputeEvent.of("iss_54", DisputeEventKind.ARBITRATION_FILED, "filed"))
    svc.apply("dp_5", DisputeEvent.of("net_55", DisputeEventKind.RULING_FOR_ISSUER, "ruling: issuer"))
    step(svc, clock, s5, "dp_5", "arbitration lost")

    print("\n== Ledger ==")
    for a in (DisputeService.SCHEME, DisputeService.MERCHANT, DisputeService.HOLD, DisputeService.FEES):
        print(f"   {a:<18} {str(svc.ledger().balance(a)):>12}")
    print(f"   ledger balances: {str(svc.ledger().balances()).lower()}, journal entries: {len(svc.ledger().journal())}")


if __name__ == "__main__":
    main()
