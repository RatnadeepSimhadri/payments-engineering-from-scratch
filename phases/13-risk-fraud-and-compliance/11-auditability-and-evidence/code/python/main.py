"""Lesson: phases/13-risk-fraud-and-compliance/11-auditability-and-evidence/docs/en.md
Auditability: Immutable Logs and Evidence Bundles
Run:  python3 main.py        Verify a bundle: python3 main.py verify bundle.txt <trusted-tsa-key>
Test: python3 -m unittest -v
Sources: RFC 6962 / RFC 9162 (Certificate Transparency Merkle trees, audit paths); RFC 3161 (time-stamp protocol);
RFC 2104 (HMAC, standing in for the Java version's RFC 8032 Ed25519); D. J. Bernstein, netstrings. Python 3.11+, stdlib only.
"""

from __future__ import annotations

import base64
import binascii
import hashlib
import hmac
import re
import secrets
import sys
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Callable, Optional

LONG_MAX = (1 << 63) - 1


def java_split(s: str, sep: str) -> list[str]:
    """Java's String.split: trailing empty strings are dropped (but a string with no separator comes back whole)."""
    parts = s.split(sep)
    if len(parts) == 1:
        return parts
    while parts and parts[-1] == "":
        parts.pop()
    return parts


def instant_str(millis: int) -> str:
    """java.time.Instant.toString(): ISO-8601 in UTC, milliseconds only when non-zero."""
    seconds, ms = divmod(millis, 1000)
    text = datetime.fromtimestamp(seconds, tz=timezone.utc).strftime("%Y-%m-%dT%H:%M:%S")
    return text + (f".{ms:03d}" if ms else "") + "Z"


def fmt_map(m: dict) -> str:
    """Java's TreeMap.toString(): "{a=1, b=2}"."""
    return "{" + ", ".join(f"{k}={v}" for k, v in sorted(m.items())) + "}"


def b64(data: bytes) -> str:
    return base64.b64encode(data).decode("ascii")


def unb64(text: str) -> bytes:
    """Strict decoding like java.util.Base64.getDecoder(): a non-alphabet character is an error, not skipped."""
    return base64.b64decode(text, validate=True)


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency, copied from Phase 10, Lesson 01 (subset)."""

    code: str
    exponent: int


USD = Currency("USD", 2)
EUR = Currency("EUR", 2)


@dataclass(frozen=True)
class Money:
    """Integer minor units plus a currency. Never a float. Copied from Phase 10, Lesson 01 (formatting subset)."""

    minor_units: int
    currency: Currency

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        if currency is None:
            raise TypeError("currency")
        return Money(minor_units, currency)

    def __str__(self) -> str:
        whole, frac = divmod(abs(self.minor_units), 10 ** self.currency.exponent)
        sign = "-" if self.minor_units < 0 else ""
        if self.currency.exponent == 0:
            return f"{sign}{whole} {self.currency.code}"
        return f"{sign}{whole}.{frac:0{self.currency.exponent}d} {self.currency.code}"


Clock = Callable[[], int]


class ManualClock:
    """A clock the caller moves explicitly. Can be moved backwards, which is exactly what the log must reject."""

    def __init__(self, start: int) -> None:
        self._now = start

    def millis(self) -> int:
        return self._now

    def __call__(self) -> int:
        return self._now

    def advance(self, ms: int) -> None:
        self._now += ms

    def set(self, ms: int) -> None:
        self._now = ms


class Crypto:
    """SHA-256, hex, and the netstring canonical encoding every hash in this lesson is computed over."""

    ZERO_HASH = "0" * 64

    @staticmethod
    def sha256(*parts: bytes) -> bytes:
        md = hashlib.sha256()
        for p in parts:
            md.update(p)
        return md.digest()

    @staticmethod
    def sha256_hex(data: bytes) -> str:
        return hashlib.sha256(data).hexdigest()

    @staticmethod
    def netstrings(*fields: str) -> bytes:
        """Netstrings: "5:hello,". Length-prefixing makes the encoding injective. Lengths count UTF-8 bytes."""
        out = bytearray()
        for f in fields:
            utf8 = f.encode("utf-8")
            out += str(len(utf8)).encode("ascii") + b":" + utf8 + b","
        return bytes(out)

    @staticmethod
    def parse_netstrings(data: bytes) -> list[str]:
        """Inverse of netstrings; rejects anything malformed rather than guessing."""
        out: list[str] = []
        i = 0
        while i < len(data):
            colon = i
            while colon < len(data) and data[colon] != ord(":"):
                colon += 1
            if colon == len(data) or colon == i:
                raise ValueError(f"bad netstring length at {i}")
            digits = data[i:colon].decode("ascii", errors="replace")
            if not re.fullmatch(r"[+-]?[0-9]+", digits):   # Integer.parseInt rejects spaces and underscores; int() would not
                raise ValueError(f"bad netstring length at {i}")
            length = int(digits)
            end = colon + 1 + length
            if length < 0 or end >= len(data) or data[end] != ord(","):
                raise ValueError(f"bad netstring body at {i}")
            out.append(data[colon + 1:end].decode("utf-8", errors="replace"))
            i = end + 1
        return out


@dataclass(frozen=True)
class Entry:
    """One immutable log record. The hash covers every other field, including the previous entry's hash, so changing any
    byte of any earlier entry changes every hash after it."""

    seq: int
    timestamp_millis: int
    prev_hash: str
    kind: str
    subject: str
    payload: dict = field(default_factory=dict)
    hash: str = ""

    VERSION = "pefs-log-v1"

    def __post_init__(self) -> None:
        object.__setattr__(self, "payload", dict(sorted(self.payload.items())))   # a sorted, private copy

    @staticmethod
    def create(seq: int, ts: int, prev_hash: str, kind: str, subject: str, payload: dict) -> "Entry":
        srt = dict(sorted(payload.items()))
        h = Crypto.sha256_hex(Entry.canonical_of(seq, ts, prev_hash, kind, subject, srt))
        return Entry(seq, ts, prev_hash, kind, subject, srt, h)

    @staticmethod
    def canonical_of(seq: int, ts: int, prev_hash: str, kind: str, subject: str, payload: dict) -> bytes:
        """The exact bytes that are hashed. Field order is fixed; payload keys are sorted; every field is length-prefixed."""
        fields = [Entry.VERSION, str(seq), str(ts), prev_hash, kind, subject, str(len(payload))]
        for k in sorted(payload):
            fields += [k, payload[k]]
        return Crypto.netstrings(*fields)

    def canonical(self) -> bytes:
        return Entry.canonical_of(self.seq, self.timestamp_millis, self.prev_hash, self.kind, self.subject, self.payload)

    def recompute_hash(self) -> str:
        return Crypto.sha256_hex(self.canonical())

    @staticmethod
    def from_canonical(data: bytes) -> "Entry":
        """Parse canonical bytes back into an entry whose hash is recomputed, never trusted."""
        f = Crypto.parse_netstrings(data)
        if len(f) < 7 or f[0] != Entry.VERSION:
            raise ValueError(f"not a {Entry.VERSION} entry")
        n = int(f[6])
        if len(f) != 7 + 2 * n:
            raise ValueError("payload count mismatch")
        payload = {f[7 + 2 * i]: f[8 + 2 * i] for i in range(n)}
        return Entry.create(int(f[1]), int(f[2]), f[3], f[4], f[5], payload)

    def with_payload_keeping_hash(self, key: str, value: str) -> "Entry":
        """For tamper demonstrations only: same hash, different content."""
        p = dict(self.payload)
        p[key] = value
        return Entry(self.seq, self.timestamp_millis, self.prev_hash, self.kind, self.subject, p, self.hash)


class Merkle:
    """RFC 6962 Merkle tree hash with leaf/node domain separation, audit paths, and RFC 9162 path verification."""

    @staticmethod
    def leaf_hash(data: bytes) -> bytes:
        return Crypto.sha256(b"\x00", data)

    @staticmethod
    def node_hash(left: bytes, right: bytes) -> bytes:
        return Crypto.sha256(b"\x01", left, right)

    @staticmethod
    def split(n: int) -> int:
        """Largest power of two strictly less than n (n >= 2)."""
        k = 1
        while k << 1 < n:
            k <<= 1
        return k

    @staticmethod
    def root(leaves: list, lo: int = 0, hi: Optional[int] = None) -> bytes:
        """MTH(D[lo:hi]). Empty tree is SHA-256 of the empty string."""
        if hi is None:
            hi = len(leaves)
        n = hi - lo
        if n == 0:
            return Crypto.sha256()
        if n == 1:
            return Merkle.leaf_hash(leaves[lo])
        k = Merkle.split(n)
        return Merkle.node_hash(Merkle.root(leaves, lo, lo + k), Merkle.root(leaves, lo + k, hi))

    @staticmethod
    def _audit_path(leaves: list, m: int, lo: int, hi: int) -> list[bytes]:
        n = hi - lo
        if n <= 1:
            return []
        k = Merkle.split(n)
        if m < k:
            path = Merkle._audit_path(leaves, m, lo, lo + k)
            path.append(Merkle.root(leaves, lo + k, hi))
        else:
            path = Merkle._audit_path(leaves, m - k, lo + k, hi)
            path.append(Merkle.root(leaves, lo, lo + k))
        return path

    @staticmethod
    def audit_path(leaves: list, m: int) -> list[bytes]:
        """PATH(m, D[0:n]): the sibling hashes from leaf m up to the root, bottom first."""
        if m < 0 or m >= len(leaves):
            raise IndexError(f"leaf {m} of {len(leaves)}")
        return Merkle._audit_path(leaves, m, 0, len(leaves))

    @staticmethod
    def verify_inclusion(leaf_data: bytes, index: int, tree_size: int, path: list[bytes], expected_root: bytes) -> bool:
        """RFC 9162 section 2.1.3.2: recompute the root from one leaf and its path. O(log n) hashes."""
        if index < 0 or index >= tree_size:
            return False
        fn, sn = index, tree_size - 1
        r = Merkle.leaf_hash(leaf_data)
        for p in path:
            if sn == 0:
                return False
            if fn & 1 == 1 or fn == sn:
                r = Merkle.node_hash(p, r)
                if fn & 1 == 0:
                    while fn & 1 == 0 and fn != 0:
                        fn >>= 1
                        sn >>= 1
            else:
                r = Merkle.node_hash(r, p)
            fn >>= 1
            sn >>= 1
        return sn == 0 and hmac.compare_digest(r, expected_root)


@dataclass(frozen=True)
class TimestampToken:
    """A sealed statement "this digest existed no later than time T", in the spirit of an RFC 3161 time-stamp token."""

    digest_hex: str
    time_millis: int
    signature_base64: str

    def signed_bytes(self) -> bytes:
        return Crypto.netstrings("pefs-tsa-v1", self.digest_hex, str(self.time_millis))


def key_id(key_base64: str) -> str:
    """A fingerprint of an HMAC key. Bundles name the key by fingerprint; the key itself never leaves its holders."""
    return Crypto.sha256_hex(Crypto.netstrings("pefs-tsa-key-id-v1", key_base64))


class TimestampAuthority:
    """An independent party with its own clock and a secret HMAC-SHA256 key (the Java version uses an Ed25519 key pair;
    the standard library has no Ed25519). The log operator never holds the key, so it cannot forge a seal; the verifier
    must hold it to check one, which is the trade-off the lesson's In Python section spells out."""

    def __init__(self, seed: Optional[int], clock: Clock) -> None:
        if seed is None:
            self._key = secrets.token_bytes(32)            # production: a random key from the OS
        else:                                              # demos and tests: derived from the seed, so output replays
            self._key = Crypto.sha256(Crypto.netstrings("pefs-tsa-hmac-key-v1", str(seed)))
        self._clock = clock

    def verification_key_base64(self) -> str:
        """The key a verifier must be given out of band. Unlike an Ed25519 public key, it can also create seals."""
        return b64(self._key)

    def stamp(self, digest_hex: str) -> TimestampToken:
        t = self._clock()
        unsigned = TimestampToken(digest_hex, t, "")
        mac = hmac.new(self._key, unsigned.signed_bytes(), hashlib.sha256).digest()
        return TimestampToken(digest_hex, t, b64(mac))

    @staticmethod
    def verify(key_base64: Optional[str], token: TimestampToken) -> bool:
        try:
            key = unb64(key_base64)
            sig = unb64(token.signature_base64)
        except (binascii.Error, ValueError, TypeError):
            return False
        expected = hmac.new(key, token.signed_bytes(), hashlib.sha256).digest()
        return hmac.compare_digest(expected, sig)


@dataclass(frozen=True)
class Checkpoint:
    """A sealed prefix of the log: its size, Merkle root, head hash, and a time-stamp over all three."""

    size: int
    merkle_root_hex: str
    head_hash_hex: str
    token: TimestampToken

    @staticmethod
    def statement_digest(size: int, root_hex: str, head_hex: str) -> str:
        return Crypto.sha256_hex(Crypto.netstrings("pefs-checkpoint-v1", str(size), str(root_hex), str(head_hex)))


class DecisionLog:
    """The append-only decision log. There is no update and no delete; the only mutations are append and seal."""

    def __init__(self, clock: Clock, tsa: TimestampAuthority) -> None:
        self._entries: list[Entry] = []
        self._checkpoints: list[Checkpoint] = []
        self._clock = clock
        self._tsa = tsa

    def append(self, kind: str, subject: str, payload: dict) -> Entry:
        ts = self._clock()
        last = self._entries[-1] if self._entries else None
        if last is not None and ts < last.timestamp_millis:
            raise RuntimeError(f"clock went backwards: {ts} < {last.timestamp_millis}")
        e = Entry.create(len(self._entries), ts, Crypto.ZERO_HASH if last is None else last.hash, kind, subject, payload)
        self._entries.append(e)
        return e

    def seal(self) -> Checkpoint:
        """Seal everything appended so far: compute the Merkle root and have the independent authority time-stamp it."""
        if not self._entries:
            raise RuntimeError("nothing to seal")
        size = len(self._entries)
        root = Merkle.root(DecisionLog.leaf_data(self._entries, size)).hex()
        head = self._entries[-1].hash
        cp = Checkpoint(size, root, head, self._tsa.stamp(Checkpoint.statement_digest(size, root, head)))
        self._checkpoints.append(cp)
        return cp

    @staticmethod
    def leaf_data(entries, size: int) -> list[bytes]:
        return [bytes.fromhex(entries[i].hash) for i in range(size)]

    def entries(self) -> tuple:
        """A read-only copy (Java's List.copyOf)."""
        return tuple(self._entries)

    def checkpoints(self) -> tuple:
        return tuple(self._checkpoints)

    def tsa_verification_key(self) -> str:
        return self._tsa.verification_key_base64()


@dataclass(frozen=True)
class Report:
    """What a verifier found. ok is True only if problems is empty. first_bad_seq is -1 when no entry is implicated."""

    ok: bool
    first_bad_seq: int
    problems: tuple

    @staticmethod
    def of(problems: list[str], first_bad: int) -> "Report":
        return Report(not problems, -1 if not problems else first_bad, tuple(problems))


class EvidenceBundle:
    """Everything about one subject, provable against a sealed checkpoint without disclosing any other entry."""

    MAGIC = "pefs-evidence-bundle v1"

    @staticmethod
    def export(log: DecisionLog, subject: str, cp: Checkpoint) -> str:
        all_entries = log.entries()
        leaves = DecisionLog.leaf_data(all_entries, cp.size)
        lines = [EvidenceBundle.MAGIC,
                 f"subject {subject}",
                 f"tree-size {cp.size}",
                 f"merkle-root {cp.merkle_root_hex}",
                 f"head-hash {cp.head_hash_hex}",
                 f"tsa-time {cp.token.time_millis}",
                 f"tsa-signature {cp.token.signature_base64}",
                 f"tsa-key-id {key_id(log.tsa_verification_key())}"]
        for i in range(cp.size):
            e = all_entries[i]
            if e.subject != subject:
                continue
            lines.append(f"entry {e.seq} {b64(e.canonical())} {e.hash}")
            hex_path = [p.hex() for p in Merkle.audit_path(leaves, i)]
            lines.append(f"proof {e.seq} {','.join(hex_path) if hex_path else '-'}")
        return "\n".join(lines) + "\n"


@dataclass(frozen=True)
class Parsed:
    """Parsed header and lines of a bundle."""

    header: dict
    entries: list
    proofs: dict


class Verifier:
    """The program a regulator or auditor runs. It trusts nothing in the log except the time-stamp authority's key."""

    @staticmethod
    def verify_log(entries, checkpoints, trusted_key: str) -> Report:
        """Verify a whole log against its checkpoints: hashes, links, sequence, time order, Merkle roots, seals."""
        problems: list[str] = []
        first_bad = LONG_MAX
        for i, e in enumerate(entries):
            if e.seq != i:
                problems.append(f"position {i} holds seq {e.seq} (entry missing or reordered)")
                first_bad = min(first_bad, i)
            if e.recompute_hash() != e.hash:
                problems.append(f"entry #{i} content does not match its hash")
                first_bad = min(first_bad, i)
            expected_prev = Crypto.ZERO_HASH if i == 0 else entries[i - 1].hash
            if e.prev_hash != expected_prev:
                problems.append(f"entry #{i} does not link to the entry before it")
                first_bad = min(first_bad, i - 1 if e.seq == i and i > 0 else i)   # either side of a broken link may be the edit
            if i > 0 and e.timestamp_millis < entries[i - 1].timestamp_millis:
                problems.append(f"entry #{i} is timestamped before entry #{i - 1}")
                first_bad = min(first_bad, i)
        last_good_size = 0
        root_failure_seen = False
        for cp in checkpoints:
            digest = Checkpoint.statement_digest(cp.size, cp.merkle_root_hex, cp.head_hash_hex)
            if digest != cp.token.digest_hex or not TimestampAuthority.verify(trusted_key, cp.token):
                problems.append(f"checkpoint size {cp.size}: time-stamp signature does not verify")
                continue
            if cp.size > len(entries):
                problems.append(f"log truncated: checkpoint covers {cp.size} entries, log has {len(entries)}")
                first_bad = min(first_bad, len(entries))
                continue
            root = Merkle.root(DecisionLog.leaf_data(entries, cp.size)).hex()
            good = True
            if root != cp.merkle_root_hex:
                problems.append(f"checkpoint size {cp.size}: Merkle root differs from the sealed root (history rewritten)")
                good = False
            if entries[cp.size - 1].hash != cp.head_hash_hex:
                problems.append(f"checkpoint size {cp.size}: head hash differs from the sealed head")
                good = False
            if entries[cp.size - 1].timestamp_millis > cp.token.time_millis:
                problems.append(f"checkpoint size {cp.size}: an entry claims a time after its own seal")
                good = False
            if good and not root_failure_seen:
                last_good_size = cp.size
            if not good and not root_failure_seen:
                root_failure_seen = True
                # Entry-level evidence is more precise. Without it, the sealed prefix that still verifies bounds the change.
                if first_bad == LONG_MAX:
                    first_bad = last_good_size
        return Report.of(problems, first_bad)

    @staticmethod
    def parse(text: str) -> Parsed:
        lines = java_split(text, "\n")
        if not lines or lines[0] != EvidenceBundle.MAGIC:
            raise ValueError("not an evidence bundle")
        header: dict[str, str] = {}
        entries: list[list[str]] = []
        proofs: dict[int, str] = {}
        for i in range(1, len(lines)):
            parts = java_split(lines[i], " ")
            if parts[0] == "entry":
                if len(parts) != 4:
                    raise ValueError(f"bad entry line {i}")
                entries.append(parts)
            elif parts[0] == "proof":
                if len(parts) != 3:
                    raise ValueError(f"bad proof line {i}")
                proofs[int(parts[1])] = parts[2]
            else:
                if len(parts) != 2:
                    raise ValueError(f"bad header line {i}")
                header[parts[0]] = parts[1]
        return Parsed(header, entries, proofs)

    @staticmethod
    def verify_bundle(text: str, trusted_key: Optional[str]) -> Report:
        """Verify a bundle: the seal checks under the trusted key, and every disclosed entry is inside the sealed tree."""
        problems: list[str] = []
        first_bad = LONG_MAX
        try:
            p = Verifier.parse(text)
        except Exception as ex:   # Java catches RuntimeException here
            return Report.of([f"unreadable bundle: {ex}"], 0)
        h = p.header
        subject, root, head = h.get("subject"), h.get("merkle-root"), h.get("head-hash")
        size = int(h.get("tree-size", "0"))
        tsa_time = int(h.get("tsa-time", "0"))
        if trusted_key is None:
            problems.append("no trusted key: an HMAC seal can only be checked with the time-stamp authority's key")
        elif key_id(trusted_key) != h.get("tsa-key-id"):
            problems.append("bundle names a different time-stamp key than the one the verifier trusts")
        token = TimestampToken(Checkpoint.statement_digest(size, root, head), tsa_time, h.get("tsa-signature", ""))
        if not TimestampAuthority.verify(trusted_key, token):
            problems.append("seal signature does not verify (size, root, head, or time altered)")
        if not p.entries:
            problems.append("bundle discloses no entries")
        previous: Optional[Entry] = None
        for line in p.entries:
            seq = int(line[1])
            try:
                e = Entry.from_canonical(unb64(line[2]))
            except Exception as ex:
                problems.append(f"entry #{seq} is malformed: {ex}")
                first_bad = min(first_bad, seq)
                continue
            if e.hash != line[3] or e.seq != seq:
                problems.append(f"entry #{seq} content does not match its stated hash")
                first_bad = min(first_bad, seq)
            if e.subject != subject:
                problems.append(f"entry #{seq} is about {e.subject}, not {subject}")
            if e.timestamp_millis > tsa_time:
                problems.append(f"entry #{seq} claims a time after the seal")
            proof = p.proofs.get(seq)
            path = [] if proof is None or proof == "-" else [bytes.fromhex(x) for x in proof.split(",")]
            if proof is None or not Merkle.verify_inclusion(bytes.fromhex(e.hash), seq, size, path, bytes.fromhex(root)):
                problems.append(f"entry #{seq} is not provably inside the sealed tree")
                first_bad = min(first_bad, seq)
            if seq == size - 1 and e.hash != head:
                problems.append("last entry does not match the sealed head")
            if previous is not None and previous.seq + 1 == seq and e.prev_hash != previous.hash:
                problems.append(f"entries #{previous.seq} and #{seq} are adjacent but not linked")
            previous = e
        return Report.of(problems, 0 if first_bad == LONG_MAX else first_bad)

    @staticmethod
    def describe(text: str) -> list[str]:
        """Human-readable lines for the disclosed entries, decoded from the canonical bytes (the only source of truth)."""
        p = Verifier.parse(text)
        out = []
        for line in p.entries:
            e = Entry.from_canonical(unb64(line[2]))
            out.append(f"#{e.seq} {instant_str(e.timestamp_millis)} {e.kind} {fmt_map(e.payload)}")
        out.append(f"sealed: {p.header.get('tree-size')} entries, no later than {instant_str(int(p.header.get('tsa-time')))}")
        return out

    @staticmethod
    def cli(argv: list[str]) -> int:
        """python3 main.py verify bundle.txt <trusted-key>. Exit 0 when every check passes, 1 otherwise, 2 on bad usage."""
        if len(argv) < 2:
            print("usage: python3 main.py verify bundle.txt <trusted-tsa-key-base64>", file=sys.stderr)
            return 2
        with open(argv[0], encoding="utf-8") as fh:
            text = fh.read()
        r = Verifier.verify_bundle(text, argv[1])
        if r.ok:
            for line in Verifier.describe(text):
                print(line)
        for problem in r.problems:
            print(f"PROBLEM: {problem}")
        print("VERIFIED" if r.ok else "FAILED")
        return 0 if r.ok else 1


T0 = 1_767_225_600_000   # 2026-01-01T00:00:00Z, fixed so output is reproducible


def decision(outcome: str, score: int, amount: Money) -> dict:
    return {"outcome": outcome, "score": str(score), "amount": str(amount), "model": "scorecard-2026-01-a",
            "thresholds": "review>=60,decline>=130"}


def demo_tsa(log_clock: ManualClock) -> TimestampAuthority:
    """The authority runs its own clock; here it reads 250 ms after the log clock, as if the request crossed a network."""
    return TimestampAuthority(2026, lambda: log_clock.millis() + 250)


def build_demo_log(clock: ManualClock, tsa: TimestampAuthority) -> DecisionLog:
    """The demo log: a threshold change, six decisions, two review outcomes, sealed twice."""
    log = DecisionLog(clock, tsa)
    log.append("config.threshold", "risk-engine", {"review": "60", "decline": "130", "approved_by": "risk-lead-7", "ticket": "RISK-412"})
    clock.advance(1_000)
    log.append("risk.decision", "pay_0001", decision("APPROVE", 13, Money.of_minor(4_250, USD)))
    clock.advance(1_000)
    log.append("risk.decision", "pay_0002", decision("REVIEW", 88, Money.of_minor(31_900, USD)))
    clock.advance(1_000)
    log.append("risk.decision", "pay_0003", decision("DECLINE", 861, Money.of_minor(129_999, USD)))
    clock.advance(1_000)
    log.append("risk.decision", "pay_0004", decision("APPROVE", 41, Money.of_minor(1_999, EUR)))
    clock.advance(60_000)
    log.seal()                                                     # checkpoint 1 over entries 0..4
    clock.advance(240_000)
    log.append("review.outcome", "pay_0002", {"result": "APPROVE", "analyst": "analyst-31", "minutes": "4"})
    clock.advance(1_000)
    log.append("risk.decision", "pay_0005", decision("REVIEW", 97, Money.of_minor(55_000, USD)))
    clock.advance(1_000)
    log.append("risk.decision", "pay_0006", decision("APPROVE", 7, Money.of_minor(899, USD)))
    clock.advance(120_000)
    log.append("review.outcome", "pay_0005", {"result": "DECLINE", "analyst": "analyst-12", "minutes": "6"})
    clock.advance(60_000)
    log.seal()                                                     # checkpoint 2 over entries 0..8
    return log


def print_report(label: str, r: Report) -> None:
    print(f"{label + ':':<38} ok={str(r.ok).lower():<5} first bad=#{r.first_bad_seq}  {r.problems[0] if r.problems else ''}")


def main() -> None:
    log_clock = ManualClock(T0)
    log = build_demo_log(log_clock, demo_tsa(log_clock))

    print("== The hash-chained log ==")
    for e in log.entries():
        print(f"#{e.seq} t=+{(e.timestamp_millis - T0) // 1000:<6d} {e.kind:<17} {e.subject:<11} "
              f"prev={e.prev_hash[:8]}.. hash={e.hash[:8]}..")
    print()
    for cp in log.checkpoints():
        print(f"checkpoint size={cp.size} root={cp.merkle_root_hex[:12]}.. head={cp.head_hash_hex[:12]}.. "
              f"stamped t=+{(cp.token.time_millis - T0) // 1000}s")

    key = log.tsa_verification_key()
    print("\n== Full-log verification ==")
    clean = Verifier.verify_log(log.entries(), log.checkpoints(), key)
    print(f"untouched log: ok={str(clean.ok).lower()}")

    entries = log.entries()
    edited = list(entries)
    edited[3] = edited[3].with_payload_keeping_hash("outcome", "APPROVE")
    print_report("edit #3 outcome DECLINE->APPROVE", Verifier.verify_log(edited, log.checkpoints(), key))

    rehashed = list(entries)
    e3 = rehashed[3]
    p3 = dict(e3.payload)
    p3["outcome"] = "APPROVE"
    rehashed[3] = Entry.create(3, e3.timestamp_millis, e3.prev_hash, e3.kind, e3.subject, p3)
    print_report("edit #3 and recompute its hash", Verifier.verify_log(rehashed, log.checkpoints(), key))

    rewritten = list(rehashed[:4])
    for i in range(4, len(entries)):
        o = entries[i]
        rewritten.append(Entry.create(o.seq, o.timestamp_millis, rewritten[i - 1].hash, o.kind, o.subject, o.payload))
    print_report("edit #3 and rewrite the whole chain", Verifier.verify_log(rewritten, log.checkpoints(), key))

    deleted = list(entries)
    del deleted[5]
    print_report("delete #5 (the review of pay_0002)", Verifier.verify_log(deleted, log.checkpoints(), key))

    print("\n== Clock anchoring ==")
    try:
        log_clock.set(T0 + 5_000)
        log.append("risk.decision", "pay_0099", decision("APPROVE", 1, Money.of_minor(100, USD)))
    except RuntimeError as ex:
        print(f"backdated append rejected: {ex}")
    cp1 = log.checkpoints()[0]
    print(f"entries 0..{cp1.size - 1} existed no later than t=+{(cp1.token.time_millis - T0) // 1000}s (signed by the TSA, not by us)")

    print("\n== Evidence bundle for pay_0002 ==")
    cp2 = log.checkpoints()[1]
    bundle = EvidenceBundle.export(log, "pay_0002", cp2)
    for line in java_split(bundle, "\n"):
        print(line[:107] + "..." if len(line) > 110 else line)
    b = Verifier.verify_bundle(bundle, key)
    print(f"\nregulator verifies bundle with the TSA's shared key: ok={str(b.ok).lower()}")
    for line in Verifier.describe(bundle):
        print(f"  {line}")

    e5 = entries[5]
    forged = bundle.replace(b64(e5.canonical()), b64(Entry.create(5, e5.timestamp_millis, e5.prev_hash, "review.outcome", "pay_0002",
                                                                  {"result": "DECLINE", "analyst": "analyst-31", "minutes": "4"}).canonical()))
    print_report("bundle with the review result changed", Verifier.verify_bundle(forged, key))


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "verify":
        sys.exit(Verifier.cli(sys.argv[2:]))
    main()
