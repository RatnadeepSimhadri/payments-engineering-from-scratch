"""Lesson: phases/06-replication-and-consensus/10-chain-and-primary-backup/docs/en.md
Chain Replication and Primary-Backup
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: van Renesse & Schneider, "Chain Replication for Supporting High Throughput and Availability" (OSDI 2004), sections 2-3;
         Alsberg & Day, "A Principle for Resilient Sharing of Distributed Resources" (ICSE 1976) for primary-backup;
         Budhiraja, Marzullo, Schneider, Toueg, "The Primary-Backup Approach" (1993).
Python 3.11+, standard library only. Deterministic simulated network; a simulated master plays the failure detector and reconfigurer.
"""

from __future__ import annotations

import heapq
from dataclasses import dataclass
from enum import Enum
from typing import Callable, Optional

MASK64 = (1 << 64) - 1
LONG_MIN = -(1 << 63)
LONG_MAX = (1 << 63) - 1

# ---------------------------------------------------------------------------------------------- money (Phase 10, Lesson 01)


@dataclass(frozen=True)
class Currency:
    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3:
            raise ValueError(f"currency code must be three letters: {self.code}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


USD = Currency("USD", 2)


@dataclass(frozen=True)
class Money:
    minor_units: int
    currency: Currency

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    def plus(self, other: "Money") -> "Money":
        if self.currency != other.currency:
            raise ValueError(f"cannot combine {self.currency.code} with {other.currency.code}")
        r = self.minor_units + other.minor_units
        if r < LONG_MIN or r > LONG_MAX:   # Math.addExact
            raise OverflowError("long overflow")
        return Money(r, self.currency)

    def __str__(self) -> str:
        whole, frac = divmod(abs(self.minor_units), self.currency.minor_units_per_major())
        sign = "-" if self.minor_units < 0 else ""
        if self.currency.exponent == 0:
            return f"{sign}{whole} {self.currency.code}"
        return f"{sign}{whole}.{frac:0{self.currency.exponent}d} {self.currency.code}"


ZERO_USD = Money(0, USD)


def fmt_map(m: dict) -> str:
    """Java's TreeMap.toString(): "{alice=74.50 USD}"."""
    return "{" + ", ".join(f"{k}={v}" for k, v in sorted(m.items())) + "}"


def fmt_list(xs) -> str:
    return "[" + ", ".join(str(x) for x in xs) + "]"


# ---------------------------------------------------------------------------------------------- deterministic infrastructure


class Rng:
    """SplitMix64 (Steele, Lea, Flood, OOPSLA 2014): the same stream for the same seed in every language."""

    def __init__(self, seed: int) -> None:
        self.state = seed & MASK64

    def next_long(self) -> int:
        self.state = (self.state + 0x9E3779B97F4A7C15) & MASK64
        z = self.state
        z = ((z ^ (z >> 30)) * 0xBF58476D1CE4E5B9) & MASK64
        z = ((z ^ (z >> 27)) * 0x94D049BB133111EB) & MASK64
        return z ^ (z >> 31)

    def next_int(self, bound: int) -> int:
        return (self.next_long() >> 33) % bound


@dataclass(frozen=True)
class Request:
    """One client request: a balance change (UPDATE) or a read (QUERY). (client_id, req_id) identifies it."""

    client_id: int
    req_id: int
    is_query: bool
    account: str
    delta: Optional[Money]

    def key(self) -> str:
        return f"{self.client_id}:{self.req_id}"

    def __str__(self) -> str:
        return ("Q" if self.is_query else "U") + self.key() + "(" + self.account + ("" if self.is_query else f",{self.delta}") + ")"


@dataclass(frozen=True)
class Update:
    """An update as it lives in a replica's history: the request plus the sequence number the head assigned."""

    seq: int
    request: Optional[Request]


class MessageKind(Enum):
    UPDATE = "UPDATE"
    ACK = "ACK"
    QUERY = "QUERY"
    REPLY = "REPLY"
    NOT_TAIL = "NOT_TAIL"
    SYNC = "SYNC"
    STATE_TRANSFER = "STATE_TRANSFER"
    SET_LINKS = "SET_LINKS"
    PB_UPDATE = "PB_UPDATE"
    PB_ACK = "PB_ACK"
    PB_SYNC = "PB_SYNC"


@dataclass(frozen=True)
class Message:
    """One message on the wire. Fields are used per kind; unused ones are None or zero."""

    kind: MessageKind
    from_: int
    to: int
    update: Optional[Update] = None
    updates: Optional[tuple] = None
    request: Optional[Request] = None
    balance: Optional[Money] = None
    predecessor: int = 0
    successor: int = 0
    is_head: bool = False
    is_tail: bool = False
    note: Optional[str] = None

    @staticmethod
    def update_msg(from_: int, to: int, u: Update) -> "Message":
        return Message(MessageKind.UPDATE, from_, to, update=u)

    @staticmethod
    def ack(from_: int, to: int, u: Update) -> "Message":
        return Message(MessageKind.ACK, from_, to, update=u)

    @staticmethod
    def query(from_: int, to: int, r: Request) -> "Message":
        return Message(MessageKind.QUERY, from_, to, request=r)

    @staticmethod
    def reply(from_: int, to: int, r: Request, balance: Money) -> "Message":
        return Message(MessageKind.REPLY, from_, to, request=r, balance=balance)

    @staticmethod
    def not_tail(from_: int, to: int, r: Request) -> "Message":
        return Message(MessageKind.NOT_TAIL, from_, to, request=r)

    @staticmethod
    def sync(from_: int, to: int, us: list) -> "Message":
        return Message(MessageKind.SYNC, from_, to, updates=tuple(us))

    @staticmethod
    def state_transfer(from_: int, to: int, history: list) -> "Message":
        return Message(MessageKind.STATE_TRANSFER, from_, to, updates=tuple(history))

    @staticmethod
    def set_links(to: int, pred: int, succ: int, note: str) -> "Message":
        return Message(MessageKind.SET_LINKS, -1, to, predecessor=pred, successor=succ, is_head=pred < 0, is_tail=succ < 0, note=note)

    @staticmethod
    def pb_update(from_: int, to: int, u: Update) -> "Message":
        return Message(MessageKind.PB_UPDATE, from_, to, update=u)

    @staticmethod
    def pb_ack(from_: int, to: int, u: Update) -> "Message":
        return Message(MessageKind.PB_ACK, from_, to, update=u)

    @staticmethod
    def pb_sync(from_: int, to: int, history: list) -> "Message":
        return Message(MessageKind.PB_SYNC, from_, to, updates=tuple(history))


class Network:
    """Seeded delays, crashes, FIFO per (from, to) pair. Chain replication assumes reliable FIFO links: no drops."""

    def __init__(self, seed: int, max_delay: int) -> None:
        self._rng = Rng(seed)
        self._max_delay = max(1, max_delay)
        self._nodes: dict = {}
        self._in_flight: list = []
        self._last_delivery_per_link: dict[tuple[int, int], int] = {}
        self._down: set[int] = set()
        self._observer: Callable[[], None] = lambda: None
        self._now = 0
        self._seq = 0
        self.sent = 0
        self.delivered = 0
        self.dropped = 0

    def add(self, n) -> None:
        self._nodes[n.id] = n

    def set_observer(self, r: Callable[[], None]) -> None:
        self._observer = r

    def now(self) -> int:
        return self._now

    def is_down(self, node_id: int) -> bool:
        return node_id in self._down

    def crash(self, node_id: int) -> None:
        self._down.add(node_id)

    def idle(self) -> bool:
        return not self._in_flight

    def send(self, m: Message) -> None:
        """FIFO per link: a message never arrives before an earlier one on the same (from, to) pair."""
        self.sent += 1
        if m.to in self._down or (m.from_ >= 0 and m.from_ in self._down):
            self.dropped += 1
            return
        link = (m.from_, m.to)   # Java packs the pair into one long; a tuple is the same injective key
        earliest = max(self._now + 1, self._last_delivery_per_link.get(link, 0))
        at = earliest + self._rng.next_int(self._max_delay)
        self._last_delivery_per_link[link] = at
        heapq.heappush(self._in_flight, (at, self._seq, m))
        self._seq += 1

    def send_all(self, ms: list) -> None:
        for m in ms:
            self.send(m)

    def tick(self) -> None:
        self._now += 1
        while self._in_flight and self._in_flight[0][0] <= self._now:
            m = heapq.heappop(self._in_flight)[2]
            if m.to in self._down:
                self.dropped += 1
                continue
            self.delivered += 1
            self.send_all(self._nodes[m.to].handle(m, self._now))
            self._observer()


# ---------------------------------------------------------------------------------------------- chain replication (van Renesse & Schneider)


class ChainNode:
    """One replica in a chain: balances, the history of applied updates (Hist_i), and the updates forwarded to the
    successor but not yet acknowledged by the tail (Sent_i). Invariants checked by ChainCluster:
      Update Propagation: for i before j in the chain, Hist_j is a prefix of Hist_i.
      Inprocess Requests:  Hist_i minus Hist_(i+1) is contained in Sent_i."""

    def __init__(self, id: int) -> None:
        self.id = id
        self.predecessor = -1
        self.successor = -1
        self.balances: dict[str, Money] = {}
        self.history: list[Update] = []
        self.seqs_seen: set[int] = set()
        self.requests_seen: set[str] = set()
        self.sent: dict[int, Update] = {}              # LinkedHashMap: insertion order matters for SYNC
        self.reply_values: dict[str, Money] = {}
        self.transfer_pending = False
        self.in_chain = False
        self.next_seq = 1
        self.updates_handled = 0
        self.queries_handled = 0
        self.acks_handled = 0
        self.messages_handled = 0

    def is_head(self) -> bool:
        return self.predecessor < 0

    def is_tail(self) -> bool:
        return self.successor < 0

    def acts_as_tail(self) -> bool:
        """Who answers clients: the real tail, or an old tail whose successor has not finished receiving the history."""
        return self.is_tail() or self.transfer_pending

    def balance(self, account: str) -> Money:
        return self.balances.get(account, ZERO_USD)

    def _apply(self, u: Update) -> None:
        self.history.append(u)
        self.seqs_seen.add(u.seq)
        self.requests_seen.add(u.request.key())
        self.balances[u.request.account] = self.balance(u.request.account).plus(u.request.delta)
        self.reply_values[u.request.key()] = self.balance(u.request.account)
        self.next_seq = max(self.next_seq, u.seq + 1)

    def handle(self, m: Message, now: int) -> list[Message]:
        self.messages_handled += 1
        out: list[Message] = []
        k = m.kind
        if k == MessageKind.UPDATE:
            self.updates_handled += 1
            u = m.update
            if m.from_ >= 100 and not self.is_head():
                return out                                   # stale client view: only the head accepts updates; the client retries
            if self.is_head() and m.from_ >= 100:            # from a client: assign the sequence number
                key = u.request.key()
                if key in self.requests_seen:                # duplicate retry: never apply twice
                    still_in_flight = any(s.request.key() == key for s in self.sent.values())
                    if not still_in_flight:
                        out.append(Message.reply(self.id, m.from_, u.request, self.reply_values.get(key)))   # committed: answer from the cache
                    return out                               # otherwise the tail's reply is on its way
                u = Update(self.next_seq, u.request)
            if u.seq in self.seqs_seen:
                return out                                   # already have it (resent after a reconfiguration)
            self._apply(u)
            out.extend(self._forward_or_commit(u))
        elif k == MessageKind.ACK:
            self.acks_handled += 1
            if m.update.seq < 0:                             # the new tail has the history: hand over
                self.transfer_pending = False
                return out
            if self.sent.pop(m.update.seq, None) is not None and not self.is_head():
                out.append(Message.ack(self.id, self.predecessor, m.update))
        elif k == MessageKind.QUERY:
            self.queries_handled += 1
            if self.acts_as_tail():
                out.append(Message.reply(self.id, m.from_, m.request, self.balance(m.request.account)))
            else:
                out.append(Message.not_tail(self.id, m.from_, m.request))   # a stale client view; it must re-ask the master
        elif k == MessageKind.SYNC:                          # predecessor resends Sent after a reconfiguration
            for u in m.updates:
                if u.seq in self.seqs_seen:
                    out.append(Message.ack(self.id, m.from_, u))   # we have it; the ack may have been lost with the dead node
                    continue
                self._apply(u)
                out.extend(self._forward_or_commit(u))
        elif k == MessageKind.STATE_TRANSFER:                # joining at the tail: adopt the old tail's history
            for u in m.updates:
                if u.seq not in self.seqs_seen:
                    self._apply(u)
            self.in_chain = True
            out.append(Message.ack(self.id, m.from_, Update(-1, None)))   # seq -1: "history received"
        elif k == MessageKind.SET_LINKS:
            was_tail = self.is_tail()
            self.predecessor = m.predecessor
            self.successor = m.successor
            self.in_chain = m.note != "extend-new"           # a joining tail is in the chain once its history arrives
            if self.is_tail() and not was_tail:              # the tail failed: everything we had in flight is now committed
                for u in list(self.sent.values()):
                    out.append(Message.reply(self.id, u.request.client_id, u.request, self.balance(u.request.account)))
                    if not self.is_head():
                        out.append(Message.ack(self.id, self.predecessor, u))
                self.sent.clear()
            elif not self.is_tail():                         # new successor: resend what it may be missing
                if was_tail and m.note is not None and m.note.startswith("extend"):
                    self.transfer_pending = True             # keep answering clients until the copy lands
                    out.append(Message.state_transfer(self.id, self.successor, self.history))
                elif self.sent:
                    out.append(Message.sync(self.id, self.successor, list(self.sent.values())))
        else:
            raise RuntimeError(f"chain node got {k.value}")
        return out

    def _forward_or_commit(self, u: Update) -> list[Message]:
        out: list[Message] = []
        if self.acts_as_tail():
            out.append(Message.reply(self.id, u.request.client_id, u.request, self.balance(u.request.account)))
            if self.is_tail() and not self.is_head():
                out.append(Message.ack(self.id, self.predecessor, u))
        if not self.is_tail():
            self.sent[u.seq] = u
            out.append(Message.update_msg(self.id, self.successor, u))
        return out


class Master:
    """The master: a reliable service that detects failures and tells nodes who their neighbours are. Here it is a plain
    object the test drives: fail(id), extend(id). Clients ask it for the head and tail."""

    def __init__(self, network: Network, initial_chain: list[int]) -> None:
        self.chain: list[int] = list(initial_chain)
        self.network = network
        self.events: list[str] = []
        self.reconfigurations = 0

    def head(self) -> int:
        return self.chain[0]

    def tail(self) -> int:
        return self.chain[-1]

    def length(self) -> int:
        return len(self.chain)

    def _pred(self, i: int) -> int:
        return -1 if i == 0 else self.chain[i - 1]

    def _succ(self, i: int) -> int:
        return -1 if i == len(self.chain) - 1 else self.chain[i + 1]

    def publish_links(self, note: str) -> None:
        """Push every node's links. Tail first so a new tail starts replying before its predecessor forwards."""
        self.reconfigurations += 1
        for i in range(len(self.chain) - 1, -1, -1):
            n = "extend-new" if note == "extend" and i == len(self.chain) - 1 else note
            self.network.send(Message.set_links(self.chain[i], self._pred(i), self._succ(i), n))

    def fail(self, node_id: int) -> None:
        """The failure detector fired for node_id. Head, tail, and middle failures each need a different repair."""
        if node_id not in self.chain:
            return
        i = self.chain.index(node_id)
        kind = "head" if i == 0 else "tail" if i == len(self.chain) - 1 else "middle"
        del self.chain[i]
        if not self.chain:
            raise RuntimeError("chain has no replicas left")
        self.events.append(f"t={self.network.now():<4d} master: n{node_id} ({kind}) failed -> chain {fmt_list(self.chain)}")
        self.publish_links("fail " + kind)

    def extend(self, node_id: int) -> None:
        """Add a fresh replica at the tail. Until the history transfer lands the old tail still answers queries."""
        self.chain.append(node_id)
        self.events.append(f"t={self.network.now():<4d} master: extend chain with n{node_id} at the tail -> {fmt_list(self.chain)}")
        self.publish_links("extend")


class ChainClient:
    """A client: sends updates to the head and queries to the tail, as the master currently reports them; retries."""

    def __init__(self, id: int, master: Master) -> None:
        self.id = id
        self.master = master
        self.pending_sent_at: dict[str, int] = {}
        self.pending: dict[str, Request] = {}
        self.replies: dict[str, Money] = {}
        self.replies_in_order: list[str] = []
        self.retries = 0
        self.not_tail_redirects = 0
        self.next_req = 1

    def update(self, account: str, delta: Money, now: int, out: list) -> Request:
        r = Request(self.id, self.next_req, False, account, delta)
        self.next_req += 1
        self.pending[r.key()] = r
        self.pending_sent_at[r.key()] = now
        out.append(Message.update_msg(self.id, self.master.head(), Update(0, r)))
        return r

    def query(self, account: str, now: int, out: list) -> Request:
        r = Request(self.id, self.next_req, True, account, None)
        self.next_req += 1
        self.pending[r.key()] = r
        self.pending_sent_at[r.key()] = now
        out.append(Message.query(self.id, self.master.tail(), r))
        return r

    def retry_stale(self, now: int, timeout: int) -> list[Message]:
        """Resend anything outstanding longer than `timeout`, to whoever the master now says is head/tail."""
        out: list[Message] = []
        for r in list(self.pending.values()):
            if now - self.pending_sent_at[r.key()] >= timeout:
                self.retries += 1
                self.pending_sent_at[r.key()] = now
                out.append(Message.query(self.id, self.master.tail(), r) if r.is_query
                           else Message.update_msg(self.id, self.master.head(), Update(0, r)))
        return out

    def handle(self, m: Message, now: int) -> list[Message]:
        out: list[Message] = []
        if m.kind == MessageKind.REPLY:
            if self.pending.pop(m.request.key(), None) is not None:   # duplicates after a tail failover are ignored
                self.replies[m.request.key()] = m.balance
                self.replies_in_order.append(m.request.key())
                self.pending_sent_at.pop(m.request.key(), None)
        elif m.kind == MessageKind.NOT_TAIL:
            self.not_tail_redirects += 1
            out.append(Message.query(self.id, self.master.tail(), m.request))
        else:
            raise RuntimeError(f"client got {m.kind.value}")
        return out

    def has_reply(self, r: Request) -> bool:
        return r.key() in self.replies

    def reply_for(self, r: Request) -> Optional[Money]:
        return self.replies.get(r.key())


# ---------------------------------------------------------------------------------------------- primary-backup, for comparison


class PrimaryBackupNode:
    """Primary-backup with synchronous replication: the primary applies an update, sends it to every backup, waits for
    all acks, then replies. Queries go to the primary. On promotion the new primary pushes its history to the backups."""

    def __init__(self, id: int) -> None:
        self.id = id
        self.is_primary = False
        self.backups: list[int] = []
        self.balances: dict[str, Money] = {}
        self.history: list[Update] = []
        self.seqs_seen: set[int] = set()
        self.requests_seen: set[str] = set()
        self.awaiting_acks: dict[int, set[int]] = {}
        self.awaiting_update: dict[int, Update] = {}
        self.next_seq = 1
        self.updates_handled = 0
        self.queries_handled = 0
        self.acks_handled = 0
        self.messages_handled = 0

    def balance(self, account: str) -> Money:
        return self.balances.get(account, ZERO_USD)

    def _apply(self, u: Update) -> None:
        self.history.append(u)
        self.seqs_seen.add(u.seq)
        self.requests_seen.add(u.request.key())
        self.balances[u.request.account] = self.balance(u.request.account).plus(u.request.delta)
        self.next_seq = max(self.next_seq, u.seq + 1)

    def handle(self, m: Message, now: int) -> list[Message]:
        self.messages_handled += 1
        out: list[Message] = []
        k = m.kind
        if k == MessageKind.UPDATE:                          # from a client
            self.updates_handled += 1
            if not self.is_primary:
                return out                                   # stale client view: silence, the client retries
            req = m.update.request
            if req.key() in self.requests_seen:
                out.append(Message.reply(self.id, m.from_, req, self.balance(req.account)))
                return out
            u = Update(self.next_seq, req)
            self._apply(u)
            if not self.backups:
                out.append(Message.reply(self.id, m.from_, u.request, self.balance(u.request.account)))
                return out
            self.awaiting_acks[u.seq] = set(self.backups)
            self.awaiting_update[u.seq] = u
            for b in self.backups:
                out.append(Message.pb_update(self.id, b, u))
        elif k == MessageKind.PB_UPDATE:
            self.updates_handled += 1
            if m.update.seq not in self.seqs_seen:
                self._apply(m.update)
            out.append(Message.pb_ack(self.id, m.from_, m.update))
        elif k == MessageKind.PB_ACK:
            self.acks_handled += 1
            waiting = self.awaiting_acks.get(m.update.seq)
            if waiting is None:
                return out
            waiting.discard(m.from_)
            if not waiting:
                del self.awaiting_acks[m.update.seq]
                u = self.awaiting_update.pop(m.update.seq)
                out.append(Message.reply(self.id, u.request.client_id, u.request, self.balance(u.request.account)))
        elif k == MessageKind.QUERY:
            self.queries_handled += 1
            if self.is_primary:
                out.append(Message.reply(self.id, m.from_, m.request, self.balance(m.request.account)))
            else:
                out.append(Message.not_tail(self.id, m.from_, m.request))
        elif k == MessageKind.PB_SYNC:
            for u in m.updates:
                if u.seq not in self.seqs_seen:
                    self._apply(u)
            out.append(Message.pb_ack(self.id, m.from_, Update(-1, None)))
        else:
            raise RuntimeError(f"primary-backup node got {k.value}")
        return out

    def promote(self, new_backups: list[int]) -> list[Message]:
        """Called by the master on promotion: become primary over `new_backups` and re-sync them from my history."""
        self.is_primary = True
        self.backups = list(new_backups)
        return [Message.pb_sync(self.id, b, self.history) for b in self.backups]


class PrimaryBackupMaster:
    """Master for the primary-backup group: holds the member list, promotes on failure."""

    def __init__(self, network: Network, nodes: dict[int, PrimaryBackupNode], members: list[int]) -> None:
        self.network = network
        self.nodes = nodes
        self.members: list[int] = list(members)
        self.events: list[str] = []
        network.send_all(nodes[self.members[0]].promote(self.members[1:]))

    def primary(self) -> int:
        return self.members[0]

    def fail(self, node_id: int) -> None:
        if node_id not in self.members:
            return
        self.members.remove(node_id)
        self.events.append(f"t={self.network.now():<4d} pb-master: n{node_id} failed -> primary n{self.members[0]}, "
                           f"backups {fmt_list(self.members[1:])}")
        self.network.send_all(self.nodes[self.members[0]].promote(self.members[1:]))


# ---------------------------------------------------------------------------------------------- the simulation harness


class ChainCluster:
    """A chain, its master, its clients, and the invariant checker, on one network."""

    def __init__(self, seed: int, max_delay: int, chain_length: int, spares: int, client_count: int) -> None:
        self.network = Network(seed, max_delay)
        self.nodes: dict[int, ChainNode] = {}
        self.clients: dict[int, ChainClient] = {}
        self.violations: list[str] = []
        self.checks = 0
        initial: list[int] = []
        for i in range(chain_length + spares):
            n = ChainNode(i)
            self.nodes[i] = n
            self.network.add(n)
            if i < chain_length:
                initial.append(i)
        self.master = Master(self.network, initial)
        for c in range(client_count):
            cl = ChainClient(100 + c, self.master)
            self.clients[cl.id] = cl
            self.network.add(cl)
        self.master.publish_links("initial")
        self.network.set_observer(self.check_invariants)
        self.run(max_delay + 2)

    def node(self, node_id: int) -> ChainNode:
        return self.nodes[node_id]

    def client(self, i: int) -> ChainClient:
        return self.clients[100 + i]

    def now(self) -> int:
        return self.network.now()

    def tick(self) -> None:
        self.network.tick()

    def run(self, ticks: int) -> None:
        for _ in range(ticks):
            self.tick()

    def run_until_quiet(self, max_ticks: int, timeout: int) -> int:
        """Run until no client has a pending request (retrying stale ones every `timeout` ticks) or max_ticks pass."""
        for _ in range(max_ticks):
            self.tick()
            for cid in sorted(self.clients):
                self.network.send_all(self.clients[cid].retry_stale(self.now(), timeout))
            quiet = self.network.idle() and all(not c.pending for c in self.clients.values())
            if quiet:
                return self.now()
        return -1

    def update(self, client: int, account: str, cents: int) -> Request:
        out: list[Message] = []
        r = self.client(client).update(account, Money.of_minor(cents, USD), self.now(), out)
        self.network.send_all(out)
        return r

    def query(self, client: int, account: str) -> Request:
        out: list[Message] = []
        r = self.client(client).query(account, self.now(), out)
        self.network.send_all(out)
        return r

    def crash(self, node_id: int) -> None:
        self.network.crash(node_id)

    def crash_and_detect(self, node_id: int, detection_delay: int) -> None:
        """The failure detector reports after `detection_delay` ticks; until then the chain is broken and clients wait."""
        self.crash(node_id)
        self.run(detection_delay)
        self.master.fail(node_id)

    def check_invariants(self) -> None:
        """Update Propagation and Inprocess Requests invariants, over the live chain, after every delivery."""
        self.checks += 1
        chain = self.master.chain
        for i in range(len(chain) - 1):
            pred, succ = self.nodes[chain[i]], self.nodes[chain[i + 1]]
            if self.network.is_down(pred.id) or self.network.is_down(succ.id) or pred.transfer_pending or not succ.in_chain:
                continue   # a joining tail is not in the chain yet
            if len(succ.history) > len(pred.history):
                self.violations.append(f"UPDATE PROPAGATION: n{succ.id} longer than n{pred.id}")
                return
            for k in range(len(succ.history)):
                if succ.history[k] != pred.history[k]:
                    self.violations.append(f"UPDATE PROPAGATION: n{succ.id} differs from n{pred.id} at {k}")
                    return
            for k in range(len(succ.history), len(pred.history)):
                u = pred.history[k]
                if u.seq not in pred.sent:
                    self.violations.append(f"INPROCESS REQUESTS: n{pred.id} has seq {u.seq} that n{succ.id} lacks, and it is not in Sent")
                    return

    def tail(self) -> ChainNode:
        return self.nodes[self.master.tail()]

    def head(self) -> ChainNode:
        return self.nodes[self.master.head()]


def role_label(n: ChainNode) -> str:
    return "HEAD" if n.is_head() else "TAIL" if n.is_tail() else "middle"


def print_chain(c: ChainCluster) -> None:
    for node_id in c.master.chain:
        n = c.node(node_id)
        print(f"  n{node_id} {role_label(n):<6} hist={len(n.history)} sent={len(n.sent)} balances={fmt_map(n.balances)}")


def total_replies(f: ChainCluster) -> int:
    return len(f.client(0).replies) + len(f.client(1).replies) + len(f.client(2).replies)


def total_retries(f: ChainCluster) -> int:
    return f.client(0).retries + f.client(1).retries + f.client(2).retries


def main() -> None:
    print("== A 3-replica chain: updates enter at the head, replies come from the tail (seed 1, delay 1-3) ==")
    c = ChainCluster(1, 3, 3, 1, 2)
    u1 = c.update(0, "alice", 10_000)
    u2 = c.update(0, "alice", -2_550)
    q1 = c.query(1, "alice")   # sent at the same instant as the updates
    c.run_until_quiet(500, 60)
    print(f"client 0: Credit alice 100.00 -> reply {c.client(0).reply_for(u1)}; Debit 25.50 -> reply {c.client(0).reply_for(u2)}")
    print(f"client 1: Query alice issued at t=0 -> {c.client(1).reply_for(q1)} (the tail had not yet seen the updates: reads show only committed state)")
    q2 = c.query(1, "alice")
    c.run_until_quiet(500, 60)
    print(f"client 1: Query alice after the replies -> {c.client(1).reply_for(q2)}")
    print_chain(c)

    print()
    print("== A read at the tail never shows an update the head has applied but the tail has not ==")
    v = ChainCluster(2, 8, 3, 0, 2)
    w = v.update(0, "bob", 5_000)
    while not v.head().history:
        v.tick()   # the head has applied it; the tail has not
    print(f"t={v.now()}: head history={len(v.head().history)}, tail history={len(v.tail().history)}, client reply yet? "
          f"{str(v.client(0).has_reply(w)).lower()}")
    sent_at = v.now()
    r = v.query(1, "bob")
    v.run_until_quiet(500, 100)
    print(f"query sent at t={sent_at} answered with {v.client(1).reply_for(r)}; the update's own reply was {v.client(0).reply_for(w)}"
          " -> a client never observes a write before the writer could")

    print()
    print("== Failures: head, middle, tail, each repaired by the master; no committed update is lost ==")
    f = ChainCluster(3, 3, 4, 2, 3)
    for i in range(6):
        f.update(i % 3, f"acct{i % 2}", 1_000 + i)
    f.run_until_quiet(500, 60)
    print(f"6 updates committed; chain {fmt_list(f.master.chain)}; tail history {len(f.tail().history)}")
    # middle failure while updates are in flight
    for i in range(4):
        f.update(i % 3, "acct0", 100)
    f.run(2)
    f.crash_and_detect(f.master.chain[1], 20)
    f.run_until_quiet(1000, 60)
    print(f.master.events[-1])
    print(f"  after middle failure: replies total={total_replies(f)}, tail history={len(f.tail().history)}, retries={total_retries(f)}")
    # tail failure with acks outstanding
    for i in range(3):
        f.update(i % 3, "acct1", 7)
    f.run(1)
    f.crash_and_detect(f.master.tail(), 20)
    f.run_until_quiet(1000, 60)
    print(f.master.events[-1])
    print(f"  after tail failure: the new tail replied for {len(f.tail().history)} entries; outstanding Sent at new tail={len(f.tail().sent)}")
    # head failure: requests the head took but had not forwarded are lost and retried by clients
    for i in range(3):
        f.update(i % 3, "acct0", 3)
    f.run(1)
    f.crash_and_detect(f.master.head(), 20)
    f.run_until_quiet(1000, 60)
    print(f.master.events[-1])
    print(f"  after head failure: chain {fmt_list(f.master.chain)}, every client request answered, retries total={total_retries(f)}")
    # extend back to length 3
    f.master.extend(4)
    f.run_until_quiet(500, 60)
    f.update(0, "acct0", 1)
    f.run_until_quiet(500, 60)
    print(f.master.events[-1])
    print_chain(f)
    print(f"  invariant checks run: {f.checks}, violations: {len(f.violations)}")
    for x in f.violations:
        print(f"  {x}")

    print()
    print("== Load: chain spreads updates (head) and reads (tail) over different replicas; primary-backup puts both on one ==")
    updates, queries = 200, 1000
    lc = ChainCluster(5, 2, 3, 0, 4)
    for i in range(updates):
        lc.update(i % 4, f"a{i % 10}", 100)
    for i in range(queries):
        lc.query(i % 4, f"a{i % 10}")
    lc.run_until_quiet(20_000, 200)
    print(f"chain (3 replicas), {updates} updates + {queries} queries:")
    for node_id in lc.master.chain:
        n = lc.node(node_id)
        print(f"  n{node_id} {role_label(n):<6} handled updates={n.updates_handled:<4d} queries={n.queries_handled:<4d} "
              f"acks={n.acks_handled:<4d} total={n.messages_handled}")
    pb_net = Network(5, 2)
    pb_nodes: dict[int, PrimaryBackupNode] = {}
    for i in range(3):
        n = PrimaryBackupNode(i)
        pb_nodes[i] = n
        pb_net.add(n)
    PrimaryBackupMaster(pb_net, pb_nodes, [0, 1, 2])
    facade = Master(pb_net, [0])   # clients ask "head" and "tail": both are the primary
    pb_clients: list[ChainClient] = []
    for i in range(4):
        cl = ChainClient(100 + i, facade)
        pb_clients.append(cl)
        pb_net.add(cl)
    for _ in range(10):
        pb_net.tick()
    out: list[Message] = []
    for i in range(updates):
        pb_clients[i % 4].update(f"a{i % 10}", Money.of_minor(100, USD), pb_net.now(), out)
    for i in range(queries):
        pb_clients[i % 4].query(f"a{i % 10}", pb_net.now(), out)
    pb_net.send_all(out)
    while not pb_net.idle():
        pb_net.tick()
    print("primary-backup (1 primary + 2 backups), same workload:")
    for node_id in sorted(pb_nodes):
        n = pb_nodes[node_id]
        print(f"  n{n.id} {'PRIMARY' if n.is_primary else 'backup':<7} handled updates={n.updates_handled:<4d} "
              f"queries={n.queries_handled:<4d} acks={n.acks_handled:<4d} total={n.messages_handled}")
    print(f"update latency: chain = one hop per replica plus the reply ({lc.master.length()} hops serial); "
          "primary-backup = primary -> backups -> primary -> client (3 hops, backups in parallel)")
    print("read consistency: chain tail reads and primary reads are both linearizable; chain backups never serve reads, p/b backups never serve anything")


if __name__ == "__main__":
    main()
