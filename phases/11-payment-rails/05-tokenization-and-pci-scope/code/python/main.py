"""Lesson: phases/11-payment-rails/05-tokenization-and-pci-scope/docs/en.md
Tokenization and PCI DSS Scope Reduction
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: PCI DSS v4.0.1 (Req. 3, 7, 10); PCI SSC Tokenization Product Security Guidelines (2015); NIST SP 800-38D
(AES-GCM, which the Java version uses); RFC 2104 (HMAC). Python 3.11+, stdlib only: the standard library has no AES, so
the vault's AEAD is an HMAC-SHA256 keystream cipher with an HMAC tag that keeps GCM's properties (see docs, In Python).
"""

from __future__ import annotations

import hashlib
import hmac
from dataclasses import dataclass
from enum import Enum
from typing import Dict, FrozenSet, List, Mapping, Optional, Sequence


class Clock:
    """Injected time source so expiry is testable without sleeping. Seconds since the Unix epoch."""

    def now_epoch_seconds(self) -> int:
        raise NotImplementedError


class FixedClock(Clock):
    def __init__(self, start: int) -> None:
        self._now = start

    def now_epoch_seconds(self) -> int:
        return self._now

    def advance_seconds(self, seconds: int) -> None:
        self._now += seconds


class JavaRandom:
    """java.util.Random's 48-bit LCG. Injected: seeded in demos and tests; production uses `secrets`.
    Ported exactly so the demo mints the same tokens and nonces as the Java version."""

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ 0x5DEECE66D) & ((1 << 48) - 1)

    def next(self, bits: int) -> int:
        self._seed = (self._seed * 0x5DEECE66D + 0xB) & ((1 << 48) - 1)
        r = self._seed >> (48 - bits)
        return r - (1 << 32) if r >= (1 << 31) else r

    def next_int(self, bound: Optional[int] = None) -> int:
        if bound is None:
            return self.next(32)
        if bound <= 0:
            raise ValueError("bound must be positive")
        r = self.next(31)
        m = bound - 1
        if (bound & m) == 0:
            return (bound * r) >> 31
        u = r
        r = u % bound
        while u - r + m >= (1 << 31):
            u = self.next(31)
            r = u % bound
        return r

    def next_bytes(self, n: int) -> bytes:
        out = bytearray()
        while len(out) < n:
            rnd = self.next(32) & 0xFFFFFFFF
            for _ in range(min(n - len(out), 4)):
                out.append(rnd & 0xFF)
                rnd >>= 8
        return bytes(out)


class Luhn:
    """ISO/IEC 7812 Annex B check digit. Also used to make tokens deliberately Luhn-invalid."""

    @staticmethod
    def is_valid(digits: str) -> bool:
        total = 0
        dbl = False
        for c in reversed(digits):
            d = ord(c) - 48
            if dbl:
                d *= 2
                if d > 9:
                    d -= 9
            total += d
            dbl = not dbl
        return total % 10 == 0


class Pan:
    """A primary account number that has been validated (12-19 digits, Luhn-correct) and that can never be
    printed by accident: str() and repr(), and therefore f-strings, containers and loggers, produce the mask.
    The digits are only reachable through digits(), which is the single call site reviewers grep for."""

    __slots__ = ("_digits",)

    def __init__(self, digits: str) -> None:
        self._digits = digits

    @staticmethod
    def of(raw: str) -> "Pan":
        s = raw.replace(" ", "").replace("-", "")
        if len(s) < 12 or len(s) > 19 or not s.isdigit() or not s.isascii():
            raise ValueError("PAN must be 12-19 digits")
        if not Luhn.is_valid(s):
            raise ValueError("PAN fails the Luhn check")
        return Pan(s)

    def digits(self) -> str:
        """The full PAN. Only the vault's encrypt path and an authorized detokenize caller should call this."""
        return self._digits

    def last_four(self) -> str:
        return self._digits[-4:]

    def bin(self) -> str:
        return self._digits[:6]

    def length(self) -> int:
        return len(self._digits)

    def masked(self) -> str:
        """First six and last four visible, as PCI DSS Requirement 3.4 allows for display; the rest masked."""
        return self.bin() + "*" * (len(self._digits) - 10) + self.last_four()

    def __str__(self) -> str:
        return self.masked()

    __repr__ = __str__

    def __eq__(self, other: object) -> bool:
        return isinstance(other, Pan) and hmac.compare_digest(self._digits.encode("ascii"), other._digits.encode("ascii"))

    def __hash__(self) -> int:
        return hash(self._digits)


class TokenStatus(Enum):
    ACTIVE = "active"
    SUSPENDED = "suspended"
    REVOKED = "revoked"


class Scope(Enum):
    """What a caller is allowed to do. Tokenize is cheap to grant; detokenize is the privilege that matters."""

    TOKENIZE = "tokenize"
    DETOKENIZE = "detokenize"
    MANAGE = "manage"


@dataclass(frozen=True)
class Principal:
    id: str
    scopes: FrozenSet[Scope]

    @staticmethod
    def of(id: str, *scopes: Scope) -> "Principal":
        return Principal(id, frozenset(scopes))

    def has(self, s: Scope) -> bool:
        return s in self.scopes


class AuthorizationError(RuntimeError):
    pass


class TokenStateError(RuntimeError):
    pass


class SecurityError(Exception):
    """Ciphertext failed authentication: tampered record, wrong token (AAD), or wrong key."""


class TokenRecord:
    """Everything the vault stores per token. Note what is absent: the PAN in clear, in any field."""

    __slots__ = ("token", "nonce", "ciphertext", "last_four", "pan_length", "created_at", "expires_at", "status")

    def __init__(self, token: str, nonce: bytes, ciphertext: bytearray, last_four: str, pan_length: int,
                 created_at: int, expires_at: int) -> None:
        self.token = token
        self.nonce = nonce              # 12 bytes, unique per key: 4 random + 8 counter
        self.ciphertext = ciphertext    # AEAD output: PAN bytes XOR keystream || 16-byte tag; AAD = token
        self.last_four = last_four
        self.pan_length = pan_length
        self.created_at = created_at
        self.expires_at = expires_at
        self.status = TokenStatus.ACTIVE


@dataclass(frozen=True)
class AuditEntry:
    """One line of the access log. Tokens and principals are safe to log; PANs never appear here."""

    at: int
    principal: str
    action: str
    token: str
    allowed: bool
    detail: str

    def __str__(self) -> str:
        return (f"{self.at} {self.principal} {self.action} {self.token} {'ALLOW' if self.allowed else 'DENY'}"
                f"{'' if not self.detail else ' ' + self.detail}")

    __repr__ = __str__


class TokenVault:
    """The token vault: the one system in the estate that holds PANs, and it holds them only under authenticated encryption.

    Invariants:
     1. Tokens preserve length and last four, and fail Luhn, so a token can never be mistaken for a PAN.
     2. The same PAN tokenizes to the same token (lookup through an HMAC index, never a plaintext PAN).
     3. Ciphertext is bound to its token through additional authenticated data; moving a record breaks it.
     4. Detokenize requires Scope.DETOKENIZE and an ACTIVE, unexpired token; every attempt is audited.
     5. Revoke destroys the ciphertext (crypto-shred); the record remains as a tombstone for audit."""

    TAG_BYTES = 16          # GCM_TAG_BITS / 8 in the Java version
    NONCE_BYTES = 12

    def __init__(self, data_key: bytes, index_key: bytes, random: JavaRandom, clock: Clock, ttl_seconds: int) -> None:
        if len(data_key) != 32:
            raise ValueError("data key must be 32 bytes (256-bit)")
        if len(index_key) < 32:
            raise ValueError("index key must be at least 32 bytes")
        # Key separation, as a KMS would do for AES-GCM: one subkey for the keystream, one for the tag.
        self._enc_key = hmac.new(data_key, b"pefs-vault-keystream", hashlib.sha256).digest()
        self._mac_key = hmac.new(data_key, b"pefs-vault-tag", hashlib.sha256).digest()
        self._index_key = index_key
        self._random = random
        self._clock = clock
        self._ttl_seconds = ttl_seconds
        self._by_token: Dict[str, TokenRecord] = {}
        self._token_by_pan_hmac: Dict[str, str] = {}
        self._audit: List[AuditEntry] = []
        self._nonce_counter = 0

    # ---- tokenize -------------------------------------------------------------------------------

    def tokenize(self, pan: Pan, who: Principal) -> str:
        """Returns the existing token for this PAN or mints a new one. Idempotent per PAN."""
        if not who.has(Scope.TOKENIZE):
            self._log(who, "TOKENIZE", "-", False, "missing scope TOKENIZE")
            raise AuthorizationError(f"{who.id} may not tokenize")
        index = self.hmac_hex(pan.digits())
        existing = self._token_by_pan_hmac.get(index)
        if existing is not None and self._by_token[existing].status is not TokenStatus.REVOKED:
            self._log(who, "TOKENIZE", existing, True, "existing")
            return existing
        token = self.mint_token(pan)
        now = self._clock.now_epoch_seconds()
        nonce = self.next_nonce()
        ciphertext = self.encrypt(pan.digits().encode("ascii"), nonce, token)
        self._by_token[token] = TokenRecord(token, nonce, ciphertext, pan.last_four(), pan.length(), now, now + self._ttl_seconds)
        self._token_by_pan_hmac[index] = token
        self._log(who, "TOKENIZE", token, True, "new")
        return token

    def mint_token(self, pan: Pan) -> str:
        """Format-preserving: same length, same last four, random middle, and Luhn-INVALID by construction."""
        for _ in range(100):
            t = [chr(48 + self._random.next_int(10)) for _ in range(pan.length() - 4)] + list(pan.last_four())
            candidate = "".join(t)
            if Luhn.is_valid(candidate):
                # bump one non-preserved digit: a single-digit change always flips the Luhn sum mod 10
                pos = len(t) - 5
                t[pos] = chr(48 + (ord(t[pos]) - 48 + 1) % 10)
                candidate = "".join(t)
            if candidate not in self._by_token:
                return candidate
        raise RuntimeError("token space exhausted for this last-four")

    # ---- detokenize -----------------------------------------------------------------------------

    def detokenize(self, token: str, who: Principal, purpose: str) -> Pan:
        if not who.has(Scope.DETOKENIZE):
            self._log(who, "DETOKENIZE", token, False, "missing scope DETOKENIZE")
            raise AuthorizationError(f"{who.id} may not detokenize")
        rec = self._by_token.get(token)
        if rec is None:
            self._log(who, "DETOKENIZE", token, False, "unknown token")
            raise TokenStateError("unknown token")
        if rec.status is not TokenStatus.ACTIVE:
            self._log(who, "DETOKENIZE", token, False, f"status {rec.status.name}")
            raise TokenStateError(f"token is {rec.status.name}")
        if self._clock.now_epoch_seconds() >= rec.expires_at:
            self._log(who, "DETOKENIZE", token, False, "expired")
            raise TokenStateError("token expired")
        plain = self.decrypt(rec.ciphertext, rec.nonce, token)
        self._log(who, "DETOKENIZE", token, True, f"purpose={purpose}")
        return Pan.of(plain.decode("ascii"))

    # ---- lifecycle ------------------------------------------------------------------------------

    def suspend(self, token: str, who: Principal) -> None:
        self._transition(token, who, TokenStatus.ACTIVE, TokenStatus.SUSPENDED, "SUSPEND")

    def reactivate(self, token: str, who: Principal) -> None:
        self._transition(token, who, TokenStatus.SUSPENDED, TokenStatus.ACTIVE, "REACTIVATE")

    def revoke(self, token: str, who: Principal) -> None:
        """Revocation is a crypto-shred: zero the ciphertext so no future key compromise can recover the PAN."""
        rec = self._require(token, who, "REVOKE")
        if rec.status is TokenStatus.REVOKED:
            raise TokenStateError("already revoked")
        rec.ciphertext[:] = bytes(len(rec.ciphertext))
        rec.status = TokenStatus.REVOKED
        self._token_by_pan_hmac = {k: v for k, v in self._token_by_pan_hmac.items() if v != token}
        self._log(who, "REVOKE", token, True, "ciphertext shredded")

    def _transition(self, token: str, who: Principal, from_: TokenStatus, to: TokenStatus, action: str) -> None:
        rec = self._require(token, who, action)
        if rec.status is not from_:
            raise TokenStateError(f"cannot {action} a token that is {rec.status.name}")
        rec.status = to
        self._log(who, action, token, True, "")

    def _require(self, token: str, who: Principal, action: str) -> TokenRecord:
        if not who.has(Scope.MANAGE):
            self._log(who, action, token, False, "missing scope MANAGE")
            raise AuthorizationError(f"{who.id} may not {action.lower()}")
        rec = self._by_token.get(token)
        if rec is None:
            raise TokenStateError("unknown token")
        return rec

    def status(self, token: str) -> TokenStatus:
        rec = self._by_token.get(token)
        if rec is None:
            raise TokenStateError("unknown token")
        return rec.status

    def last_four(self, token: str) -> str:
        """What a token-only system may know: the last four, for "card ending 1111" on a receipt."""
        return self._by_token[token].last_four

    def size(self) -> int:
        return len(self._by_token)

    def audit_log(self) -> List[AuditEntry]:
        return list(self._audit)

    def record_for(self, token: str) -> TokenRecord:
        """Exposed for tests of the tamper invariant; a production vault would not have this method."""
        return self._by_token[token]

    # ---- crypto ---------------------------------------------------------------------------------

    def next_nonce(self) -> bytes:
        """96-bit nonce: 4 random bytes || 64-bit counter. Never repeats under one key within this process."""
        nonce = (self._random.next_int() & 0xFFFFFFFF).to_bytes(4, "big") + self._nonce_counter.to_bytes(8, "big")
        self._nonce_counter += 1
        return nonce

    def _keystream(self, nonce: bytes, length: int) -> bytes:
        """HMAC-SHA256(enc_key, nonce || block counter) blocks, concatenated: a PRF keystream, as AES-CTR uses AES."""
        out = bytearray()
        block = 0
        while len(out) < length:
            out += hmac.new(self._enc_key, nonce + block.to_bytes(4, "big"), hashlib.sha256).digest()
            block += 1
        return bytes(out[:length])

    def _tag(self, nonce: bytes, aad: bytes, ciphertext: bytes) -> bytes:
        """HMAC-SHA256(mac_key, nonce || len(aad) || aad || ciphertext), truncated to 16 bytes like a GCM tag."""
        msg = nonce + len(aad).to_bytes(4, "big") + aad + ciphertext
        return hmac.new(self._mac_key, msg, hashlib.sha256).digest()[: self.TAG_BYTES]

    def encrypt(self, plaintext: bytes, nonce: bytes, aad: str) -> bytearray:
        """Encrypt-then-MAC with the token as additional authenticated data. Output: ciphertext || 16-byte tag."""
        stream = self._keystream(nonce, len(plaintext))
        body = bytes(p ^ k for p, k in zip(plaintext, stream))
        return bytearray(body + self._tag(nonce, aad.encode("ascii"), body))

    def decrypt(self, ciphertext: bytes, nonce: bytes, aad: str) -> bytes:
        if len(ciphertext) < self.TAG_BYTES:
            raise SecurityError("ciphertext failed authentication: tampered record or wrong key")
        body, tag = bytes(ciphertext[: -self.TAG_BYTES]), bytes(ciphertext[-self.TAG_BYTES:])
        if not hmac.compare_digest(tag, self._tag(nonce, aad.encode("ascii"), body)):
            raise SecurityError("ciphertext failed authentication: tampered record or wrong key")
        stream = self._keystream(nonce, len(body))
        return bytes(c ^ k for c, k in zip(body, stream))

    def hmac_hex(self, pan: str) -> str:
        """Keyed hash of the PAN for the dedup index. Unkeyed SHA-256 would be brute-forceable: 10^15 PANs per BIN."""
        return hmac.new(self._index_key, pan.encode("ascii"), hashlib.sha256).hexdigest()

    def _log(self, who: Principal, action: str, token: str, allowed: bool, detail: str) -> None:
        self._audit.append(AuditEntry(self._clock.now_epoch_seconds(), who.id, action, token, allowed, detail))


@dataclass(frozen=True)
class SystemInventory:
    """Which systems hold PAN, and therefore which are inside the PCI DSS cardholder data environment."""

    sees_pan: Mapping[str, bool]

    def in_scope(self) -> int:
        return sum(1 for b in self.sees_pan.values() if b)

    def in_scope_systems(self) -> List[str]:
        return sorted(k for k, v in self.sees_pan.items() if v)


def demo_key(seed: int) -> bytes:
    return JavaRandom(seed).next_bytes(32)  # demo only; production keys come from an HSM or KMS


def _hex(data: bytes) -> str:
    return bytes(data).hex()


def _bool(b: bool) -> str:
    return str(b).lower()


def main() -> None:
    clock = FixedClock(1_700_000_000)
    vault = TokenVault(demo_key(1), demo_key(2), JavaRandom(42), clock, 365 * 24 * 3600)
    checkout = Principal.of("checkout-api", Scope.TOKENIZE)
    gateway = Principal.of("auth-gateway", Scope.DETOKENIZE)
    ops = Principal.of("vault-ops", Scope.MANAGE)

    print("== Tokenize test PANs ==")
    visa = Pan.of("4111 1111 1111 1111")
    mc = Pan.of("5555555555554444")
    amex = Pan.of("378282246310005")
    t_visa = vault.tokenize(visa, checkout)
    t_mc = vault.tokenize(mc, checkout)
    t_amex = vault.tokenize(amex, checkout)
    print(f"PAN {visa} -> token {t_visa} (Luhn valid? {_bool(Luhn.is_valid(t_visa))})")
    print(f"PAN {mc} -> token {t_mc} (Luhn valid? {_bool(Luhn.is_valid(t_mc))})")
    print(f"PAN {amex} -> token {t_amex} (length {len(t_amex)})")
    print(f"same PAN again -> {vault.tokenize(visa, checkout)} (idempotent)")

    print("\n== What is stored ==")
    rec = vault.record_for(t_visa)
    print(f"nonce      = {_hex(rec.nonce)}")
    print(f"ciphertext = {_hex(rec.ciphertext)} ({len(rec.ciphertext)} bytes = 16 PAN + 16 tag)")
    print(f"last four  = {rec.last_four}, status = {rec.status.name}")

    print("\n== Detokenize: authorization decides ==")
    back = vault.detokenize(t_visa, gateway, "authorization")
    print(f"auth-gateway detokenized {t_visa} -> {back} (equals original: {_bool(back == visa)})")
    try:
        vault.detokenize(t_visa, checkout, "curiosity")
    except AuthorizationError as e:
        print(f"checkout-api denied: {e}")

    print("\n== Lifecycle ==")
    vault.suspend(t_mc, ops)
    try:
        vault.detokenize(t_mc, gateway, "authorization")
    except TokenStateError as e:
        print(f"suspended: {e}")
    vault.reactivate(t_mc, ops)
    clock.advance_seconds(366 * 24 * 3600)
    try:
        vault.detokenize(t_mc, gateway, "authorization")
    except TokenStateError as e:
        print(f"after 366 days: {e}")
    vault.revoke(t_amex, ops)
    print(f"revoked {t_amex}: ciphertext now {_hex(vault.record_for(t_amex).ciphertext)[:16]}...")

    print("\n== Audit log (tokens, never PANs) ==")
    for e in vault.audit_log():
        print(f"  {e}")

    print("\n== PCI scope: before and after ==")
    before = {s: True for s in ("web-checkout", "order-db", "auth-gateway", "analytics", "crm", "support-tool", "backup", "token-vault")}
    after = dict(before)
    for s in ("order-db", "analytics", "crm", "support-tool", "backup"):
        after[s] = False
    b, a = SystemInventory(before), SystemInventory(after)
    print(f"systems storing PAN before tokenization: {b.in_scope()} [{', '.join(b.in_scope_systems())}]")
    print(f"systems storing PAN after tokenization:  {a.in_scope()} [{', '.join(a.in_scope_systems())}]")


if __name__ == "__main__":
    main()
