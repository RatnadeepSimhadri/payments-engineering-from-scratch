"""Lesson: phases/11-payment-rails/13-open-banking-and-account-aggregation/docs/en.md
Open Banking: OAuth Consent, Account Aggregation, and Data Access
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: RFC 6749 (OAuth 2.0), RFC 7636 (PKCE), RFC 6750 (Bearer tokens), FDX API v6 and UK Open Banking Read/Write
API v3.1 (payload shapes). Python 3.11+, standard library only. No network: the bank, its auth server, and the
aggregator are in-memory objects sharing an injected clock. Money is integer minor units (Phase 10, Lesson 01).
"""

from __future__ import annotations

import base64
import hashlib
import hmac
from dataclasses import dataclass
from enum import Enum
from typing import Callable, Generic, Optional, TypeVar

T = TypeVar("T")

# ---------- Money (subset of Phase 10, Lesson 01) ----------


@dataclass(frozen=True)
class Currency:
    code: str
    exponent: int

    @staticmethod
    def of(code: str) -> "Currency":
        return {"USD": USD, "GBP": GBP, "EUR": EUR}.get(code) or Currency(code, 2)

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


USD = Currency("USD", 2)
GBP = Currency("GBP", 2)
EUR = Currency("EUR", 2)


@dataclass(frozen=True)
class Money:
    minor_units: int
    currency: Currency

    @staticmethod
    def parse(text: str, currency: Currency) -> "Money":
        s = text.strip()
        negative = s.startswith("-")
        if negative or s.startswith("+"):
            s = s[1:]
        whole, _, frac = s.partition(".")
        whole = whole or "0"
        if not whole.isdigit() or (frac and not frac.isdigit()):
            raise ValueError(text)
        if len(frac) > currency.exponent:
            raise ArithmeticError(f"{text} has more decimals than {currency.code} allows")
        frac = frac.ljust(currency.exponent, "0")
        minor = int(whole) * currency.minor_units_per_major() + (int(frac) if frac else 0)
        return Money(-minor if negative else minor, currency)

    def negate(self) -> "Money":
        return Money(-self.minor_units, self.currency)

    def plus(self, o: "Money") -> "Money":
        if self.currency != o.currency:
            raise ValueError("currency mismatch")
        return Money(self.minor_units + o.minor_units, self.currency)

    def __str__(self) -> str:
        whole, frac = divmod(abs(self.minor_units), self.currency.minor_units_per_major())
        return f"{'-' if self.minor_units < 0 else ''}{whole}.{str(frac).rjust(self.currency.exponent, '0')} {self.currency.code}"

    __repr__ = __str__


# ---------- Clock and the seeded generator ----------


class Clock:
    def now_epoch_seconds(self) -> int:
        raise NotImplementedError


class ManualClock(Clock):
    def __init__(self, epoch_seconds: int) -> None:
        self.now = epoch_seconds

    def now_epoch_seconds(self) -> int:
        return self.now

    def advance_seconds(self, seconds: int) -> None:
        self.now += seconds

    def advance_days(self, days: int) -> None:
        self.now += days * 86_400


class JavaRandom:
    """java.util.Random, bit for bit: the 48-bit linear congruential generator from the JDK documentation,
    so a seed produces the same tokens here as in the Java version of this lesson."""

    MULTIPLIER = 0x5DEECE66D
    ADDEND = 0xB
    MASK = (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self.seed = (seed ^ self.MULTIPLIER) & self.MASK

    def next(self, bits: int) -> int:
        self.seed = (self.seed * self.MULTIPLIER + self.ADDEND) & self.MASK
        value = self.seed >> (48 - bits)
        return value - (1 << 32) if value >= (1 << 31) else value  # Java's int is signed

    def next_int(self, bound: Optional[int] = None) -> int:
        if bound is None:
            return self.next(32)
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

    def next_bytes(self, n: int) -> bytes:
        out = bytearray()
        while len(out) < n:
            rnd = self.next(32)
            for _ in range(min(n - len(out), 4)):
                out.append(rnd & 0xFF)
                rnd >>= 8
        return bytes(out)


def _to_int32(x: int) -> int:
    x &= 0xFFFFFFFF
    return x - (1 << 32) if x >= (1 << 31) else x


def b64url(data: bytes) -> str:
    """Base64url without padding, as RFC 7636 and token formats expect."""
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode("ascii")


# ---------- OAuth 2.0 primitives ----------


class Scope(Enum):
    """Scopes as the bank publishes them. The value is what goes on the wire (RFC 6749 section 3.3)."""

    ACCOUNTS_READ = "accounts:read"
    BALANCES_READ = "balances:read"
    TRANSACTIONS_READ = "transactions:read"
    PAYMENTS_WRITE = "payments:write"

    @property
    def wire(self) -> str:
        return self.value

    @staticmethod
    def from_wire(s: str) -> "Scope":
        for v in Scope:
            if v.value == s:
                return v
        raise OAuthException("invalid_scope", f"unknown scope {s}")


SCOPE_ORDER = list(Scope)  # declaration order, the order Java's EnumSet iterates in


class OAuthException(RuntimeError):
    """An OAuth error as RFC 6749 section 5.2 defines it: a short code plus a human description."""

    def __init__(self, error: str, description: str) -> None:
        super().__init__(f"{error}: {description}")
        self.error = error


class Pkce:
    """RFC 7636: Proof Key for Code Exchange."""

    UNRESERVED = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"

    @staticmethod
    def generate_verifier(random: JavaRandom) -> str:
        """A 64-character verifier from the unreserved set (RFC 7636 section 4.1 requires 43..128)."""
        return "".join(Pkce.UNRESERVED[random.next_int(len(Pkce.UNRESERVED))] for _ in range(64))

    @staticmethod
    def challenge_s256(verifier: str) -> str:
        """code_challenge = BASE64URL-ENCODE(SHA256(ASCII(code_verifier))), no padding (section 4.2)."""
        return b64url(hashlib.sha256(verifier.encode("ascii")).digest())

    @staticmethod
    def verify(verifier: Optional[str], challenge: str) -> bool:
        if verifier is None or len(verifier) < 43 or len(verifier) > 128:
            return False
        return hmac.compare_digest(Pkce.challenge_s256(verifier).encode("ascii"), challenge.encode("ascii"))


@dataclass(frozen=True)
class RegisteredClient:
    client_id: str
    redirect_uri: str


@dataclass(frozen=True)
class Consent:
    """What the user agreed to, with the bank's consent lifetime. UK Open Banking: 90 days before re-authentication."""

    consent_id: str
    user_id: str
    client_id: str
    scopes: frozenset
    granted_at: int
    expires_at: int

    def is_expired(self, now: int) -> bool:
        return now >= self.expires_at


@dataclass(frozen=True)
class AuthorizationCode:
    """The authorization code: single use, short lived, bound to the client, the redirect URI, and the PKCE challenge."""

    code: str
    consent_id: str
    client_id: str
    redirect_uri: str
    code_challenge: str
    expires_at: int


@dataclass(frozen=True)
class TokenResponse:
    access_token: str
    refresh_token: str
    expires_in: int
    scopes: frozenset


@dataclass(frozen=True)
class AccessGrant:
    """Server-side record of an access token: who, what scopes, until when."""

    consent_id: str
    scopes: frozenset
    expires_at: int


class AuthorizationServer:
    """The bank's authorization server. Implements the authorization-code grant with PKCE, refresh with rotation,
    and replay detection. Dicts keep insertion order, so iteration is deterministic in tests."""

    CODE_TTL_SECONDS = 600  # RFC 6749 section 4.1.2: a maximum of 10 minutes is recommended
    ACCESS_TTL_SECONDS = 300
    CONSENT_TTL_SECONDS = 90 * 86_400

    def __init__(self, clock: Clock, random: JavaRandom) -> None:
        self.clock = clock
        self.random = random
        self.clients: dict[str, RegisteredClient] = {}
        self.consents: dict[str, Consent] = {}
        self.codes: dict[str, AuthorizationCode] = {}
        self.access_tokens: dict[str, AccessGrant] = {}
        self.refresh_tokens: dict[str, str] = {}          # refresh token -> consent_id
        self.retired_refresh_tokens: dict[str, str] = {}  # rotated-out token -> consent_id (replay detection)
        self.sequence = 0

    def register_client(self, client: RegisteredClient) -> None:
        self.clients[client.client_id] = client

    def _new_token(self, prefix: str) -> str:
        self.sequence += 1
        return f"{prefix}_{self.sequence}_{b64url(self.random.next_bytes(18))}"

    def authorize(self, client_id: str, redirect_uri: str, requested_scopes: frozenset, code_challenge: Optional[str],
                  code_challenge_method: str, user_id: str) -> str:
        """The /authorize endpoint after the user has authenticated and clicked "allow" on the consent screen.
        Returns the authorization code the bank would send back on the redirect URI together with `state`."""
        client = self.clients.get(client_id)
        if client is None:
            raise OAuthException("invalid_client", f"unknown client {client_id}")
        if client.redirect_uri != redirect_uri:
            raise OAuthException("invalid_request", "redirect_uri does not match registration")
        if code_challenge_method != "S256":
            raise OAuthException("invalid_request", "code_challenge_method must be S256")
        if code_challenge is None or len(code_challenge) != 43:
            raise OAuthException("invalid_request", "code_challenge must be a 43-char base64url SHA-256")
        if not requested_scopes:
            raise OAuthException("invalid_scope", "at least one scope is required")
        if Scope.PAYMENTS_WRITE in requested_scopes:
            raise OAuthException("invalid_scope", "this client is not registered for payments:write")

        now = self.clock.now_epoch_seconds()
        self.sequence += 1
        consent_id = f"consent_{self.sequence}"
        self.consents[consent_id] = Consent(consent_id, user_id, client_id, frozenset(requested_scopes), now,
                                            now + self.CONSENT_TTL_SECONDS)
        code = self._new_token("code")
        self.codes[code] = AuthorizationCode(code, consent_id, client_id, redirect_uri, code_challenge, now + self.CODE_TTL_SECONDS)
        return code

    def exchange_code(self, client_id: str, code: str, redirect_uri: str, code_verifier: Optional[str]) -> TokenResponse:
        """The /token endpoint, grant_type=authorization_code."""
        stored = self.codes.pop(code, None)  # single use, whether or not the rest succeeds
        if stored is None:
            raise OAuthException("invalid_grant", "unknown or already used authorization code")
        if stored.client_id != client_id:
            raise OAuthException("invalid_grant", "code was issued to a different client")
        if stored.redirect_uri != redirect_uri:
            raise OAuthException("invalid_grant", "redirect_uri does not match the authorization request")
        if self.clock.now_epoch_seconds() >= stored.expires_at:
            raise OAuthException("invalid_grant", "authorization code expired")
        if not Pkce.verify(code_verifier, stored.code_challenge):
            raise OAuthException("invalid_grant", "PKCE verification failed")
        return self._issue_tokens(self.consents[stored.consent_id])

    def refresh(self, client_id: str, refresh_token: str) -> TokenResponse:
        """The /token endpoint, grant_type=refresh_token, with rotation and replay detection."""
        consent_id = self.refresh_tokens.pop(refresh_token, None)
        if consent_id is None:
            replayed_family = self.retired_refresh_tokens.get(refresh_token)
            if replayed_family is not None:
                self.revoke_consent(replayed_family)  # a replayed token means it leaked: kill the family
                raise OAuthException("invalid_grant", "refresh token replay detected; consent revoked")
            raise OAuthException("invalid_grant", "unknown refresh token")
        consent = self.consents.get(consent_id)
        if consent is None:
            raise OAuthException("invalid_grant", "consent revoked")
        if consent.client_id != client_id:
            raise OAuthException("invalid_grant", "refresh token belongs to another client")
        if consent.is_expired(self.clock.now_epoch_seconds()):
            raise OAuthException("invalid_grant", "consent expired; user must re-authorize")
        self.retired_refresh_tokens[refresh_token] = consent_id
        return self._issue_tokens(consent)

    def _issue_tokens(self, consent: Consent) -> TokenResponse:
        now = self.clock.now_epoch_seconds()
        access = self._new_token("at")
        refresh = self._new_token("rt")
        self.access_tokens[access] = AccessGrant(consent.consent_id, consent.scopes, now + self.ACCESS_TTL_SECONDS)
        self.refresh_tokens[refresh] = consent.consent_id
        return TokenResponse(access, refresh, self.ACCESS_TTL_SECONDS, consent.scopes)

    def introspect(self, access_token: str) -> Optional[AccessGrant]:
        """Token introspection used by the resource server (RFC 7662 in spirit): None if unknown, expired, or revoked."""
        grant = self.access_tokens.get(access_token)
        if grant is None:
            return None
        if self.clock.now_epoch_seconds() >= grant.expires_at:
            return None
        if grant.consent_id not in self.consents:
            return None
        return grant

    def revoke_consent(self, consent_id: str) -> None:
        self.consents.pop(consent_id, None)
        self.access_tokens = {k: g for k, g in self.access_tokens.items() if g.consent_id != consent_id}
        self.refresh_tokens = {k: c for k, c in self.refresh_tokens.items() if c != consent_id}

    def consent(self, consent_id: str) -> Optional[Consent]:
        return self.consents.get(consent_id)


# ---------- Resource servers: two banks with different payload shapes ----------


@dataclass(frozen=True)
class ApiResponse:
    """An HTTP-like response without HTTP: status, an optional WWW-Authenticate error, and a JSON-like body."""

    status: int
    auth_error: Optional[str]
    body: dict

    @staticmethod
    def ok(body: dict) -> "ApiResponse":
        return ApiResponse(200, None, body)

    @staticmethod
    def unauthorized() -> "ApiResponse":
        return ApiResponse(401, "invalid_token", {})

    @staticmethod
    def forbidden() -> "ApiResponse":
        return ApiResponse(403, "insufficient_scope", {})


class BankApi:
    """A bank's data API. `cursor` is opaque to the caller; each bank encodes paging its own way."""

    def bank_id(self) -> str:
        raise NotImplementedError

    def accounts(self, access_token: str, cursor: Optional[str]) -> ApiResponse:
        raise NotImplementedError

    def transactions(self, access_token: str, account_id: str, cursor: Optional[str]) -> ApiResponse:
        raise NotImplementedError


class AbstractBank(BankApi):
    """Common token checks every resource server performs (RFC 6750 section 3.1 error codes)."""

    def __init__(self, auth: AuthorizationServer, page_size: int) -> None:
        self.auth = auth
        self.page_size = page_size

    def require_scope(self, access_token: str, scope: Scope) -> Optional[ApiResponse]:
        grant = self.auth.introspect(access_token)
        if grant is None:
            return ApiResponse.unauthorized()
        if scope not in grant.scopes:
            return ApiResponse.forbidden()
        return None

    @staticmethod
    def page(all_items: list, offset: int, size: int) -> list:
        return all_items[min(offset, len(all_items)):min(offset + size, len(all_items))]


class FdxStyleBank(AbstractBank):
    """Bank A speaks an FDX-style dialect: camelCase, accounts wrapped by type, signed decimal amounts, offset paging."""

    def __init__(self, auth: AuthorizationServer, page_size: int) -> None:
        super().__init__(auth, page_size)
        self._accounts: list[dict] = []
        self._transactions: dict[str, list[dict]] = {}

    def bank_id(self) -> str:
        return "bank-a-fdx"

    def add_account(self, account_id: str, nickname: str, currency: str, account_type: str, masked_number: str) -> None:
        self._accounts.append({"depositAccount": {"accountId": account_id, "nickname": nickname, "currency": currency,
                                                  "accountType": account_type, "accountNumberDisplay": masked_number}})
        self._transactions[account_id] = []

    def add_transaction(self, account_id: str, tx_id: str, posted_date: str, signed_amount: str, description: str, status: str) -> None:
        """FDX carries a signed amount as a JSON number and a debitCreditMemo; we keep the digits as a string so no float exists."""
        self._transactions[account_id].append({"depositTransaction": {
            "transactionId": tx_id, "postedTimestamp": posted_date + "T00:00:00Z", "amount": signed_amount,
            "debitCreditMemo": "DEBIT" if signed_amount.startswith("-") else "CREDIT",
            "description": description, "status": status}})

    def accounts(self, token: str, cursor: Optional[str]) -> ApiResponse:
        err = self.require_scope(token, Scope.ACCOUNTS_READ)
        if err is not None:
            return err
        return self._page_of("accounts", self._accounts, cursor)

    def transactions(self, token: str, account_id: str, cursor: Optional[str]) -> ApiResponse:
        err = self.require_scope(token, Scope.TRANSACTIONS_READ)
        if err is not None:
            return err
        return self._page_of("transactions", self._transactions.get(account_id, []), cursor)

    def _page_of(self, key: str, all_items: list[dict], cursor: Optional[str]) -> ApiResponse:
        offset = 0 if cursor is None else int(cursor)
        items = self.page(all_items, offset, self.page_size)
        page_info: dict = {}
        if offset + self.page_size < len(all_items):
            page_info["nextOffset"] = str(offset + self.page_size)
        return ApiResponse.ok({key: items, "page": page_info})


class UkObStyleBank(AbstractBank):
    """Bank B speaks a UK Open Banking-style dialect: PascalCase under Data, amount object with Currency, Links.Next paging."""

    def __init__(self, auth: AuthorizationServer, page_size: int) -> None:
        super().__init__(auth, page_size)
        self._accounts: list[dict] = []
        self._transactions: dict[str, list[dict]] = {}

    def bank_id(self) -> str:
        return "bank-b-ukob"

    def add_account(self, account_id: str, nickname: str, currency: str, sub_type: str, sort_code_account_number: str) -> None:
        self._accounts.append({"AccountId": account_id, "Nickname": nickname, "Currency": currency, "AccountSubType": sub_type,
                               "Account": [{"SchemeName": "UK.OBIE.SortCodeAccountNumber", "Identification": sort_code_account_number}]})
        self._transactions[account_id] = []

    def add_transaction(self, account_id: str, tx_id: str, booking_date: str, unsigned_amount: str, credit_debit: str,
                        info: str, status: str) -> None:
        self._transactions[account_id].append({
            "TransactionId": tx_id, "BookingDateTime": booking_date + "T09:30:00+00:00",
            "Amount": {"Amount": unsigned_amount, "Currency": "GBP"}, "CreditDebitIndicator": credit_debit,
            "TransactionInformation": info, "Status": status})

    def accounts(self, token: str, cursor: Optional[str]) -> ApiResponse:
        err = self.require_scope(token, Scope.ACCOUNTS_READ)
        if err is not None:
            return err
        return self._page_of("Account", "/accounts", self._accounts, cursor)

    def transactions(self, token: str, account_id: str, cursor: Optional[str]) -> ApiResponse:
        err = self.require_scope(token, Scope.TRANSACTIONS_READ)
        if err is not None:
            return err
        return self._page_of("Transaction", f"/accounts/{account_id}/transactions", self._transactions.get(account_id, []), cursor)

    def _page_of(self, key: str, path: str, all_items: list[dict], cursor: Optional[str]) -> ApiResponse:
        page_no = 1 if cursor is None else int(cursor[cursor.index("page=") + 5:])
        offset = (page_no - 1) * self.page_size
        links = {"Self": f"{path}?page={page_no}"}
        if offset + self.page_size < len(all_items):
            links["Next"] = f"{path}?page={page_no + 1}"
        return ApiResponse.ok({"Data": {key: self.page(all_items, offset, self.page_size)}, "Links": links})


# ---------- The aggregator's unified schema and per-bank adapters ----------


class AccountType(Enum):
    CHECKING = "CHECKING"
    SAVINGS = "SAVINGS"
    CREDIT_CARD = "CREDIT_CARD"
    OTHER = "OTHER"


@dataclass(frozen=True)
class Account:
    bank_id: str
    account_id: str
    display_name: str
    currency: Currency
    type: AccountType
    masked_identifier: str

    def __str__(self) -> str:
        return (f"{self.bank_id:<12} {self.account_id:<10} {self.display_name:<18} {self.currency.code} "
                f"{self.type.name:<9} {self.masked_identifier}")


@dataclass(frozen=True)
class Transaction:
    """Signed amount: negative is money leaving the account. One sign convention for every bank."""

    bank_id: str
    account_id: str
    transaction_id: str
    booked_date: str
    amount: Money
    description: str
    pending: bool

    def __str__(self) -> str:
        return (f"{self.bank_id:<12} {self.account_id:<10} {self.transaction_id:<5} {self.booked_date} {str(self.amount):>13}  "
                f"{self.description:<18}{'  (pending)' if self.pending else ''}")


@dataclass(frozen=True)
class Page(Generic[T]):
    items: list
    next_cursor: Optional[str]


class BankAdapter:
    """Translates one bank's dialect into the unified schema and knows how that bank pages."""

    def accounts(self, response: ApiResponse, bank_id: str) -> Page:
        raise NotImplementedError

    def transactions(self, response: ApiResponse, bank_id: str, account_id: str) -> Page:
        raise NotImplementedError


class FdxAdapter(BankAdapter):
    def accounts(self, r: ApiResponse, bank_id: str) -> Page:
        out = []
        for wrapper in r.body["accounts"]:
            a = wrapper["depositAccount"]
            kind = {"CHECKING": AccountType.CHECKING, "SAVINGS": AccountType.SAVINGS}.get(a["accountType"], AccountType.OTHER)
            out.append(Account(bank_id, a["accountId"], a["nickname"], Currency.of(a["currency"]), kind, a["accountNumberDisplay"]))
        return Page(out, r.body["page"].get("nextOffset"))

    def transactions(self, r: ApiResponse, bank_id: str, account_id: str) -> Page:
        out = []
        for wrapper in r.body["transactions"]:
            t = wrapper["depositTransaction"]
            amount = Money.parse(t["amount"], USD)  # FDX amounts are already signed
            debit = t["debitCreditMemo"] == "DEBIT"
            if debit != (amount.minor_units < 0):
                raise RuntimeError(f"FDX sign and debitCreditMemo disagree on {t['transactionId']}")
            out.append(Transaction(bank_id, account_id, t["transactionId"], t["postedTimestamp"][:10],
                                   amount, t["description"], t["status"] == "PENDING"))
        return Page(out, r.body["page"].get("nextOffset"))


class UkObAdapter(BankAdapter):
    def accounts(self, r: ApiResponse, bank_id: str) -> Page:
        out = []
        for a in r.body["Data"]["Account"]:
            kind = {"CurrentAccount": AccountType.CHECKING, "Savings": AccountType.SAVINGS,
                    "CreditCard": AccountType.CREDIT_CARD}.get(a["AccountSubType"], AccountType.OTHER)
            ident = a["Account"][0]["Identification"]
            masked = "****" + ident[-4:]
            out.append(Account(bank_id, a["AccountId"], a["Nickname"], Currency.of(a["Currency"]), kind, masked))
        return Page(out, r.body["Links"].get("Next"))

    def transactions(self, r: ApiResponse, bank_id: str, account_id: str) -> Page:
        out = []
        for t in r.body["Data"]["Transaction"]:
            amt = t["Amount"]
            unsigned = Money.parse(amt["Amount"], Currency.of(amt["Currency"]))
            signed = unsigned.negate() if t["CreditDebitIndicator"] == "Debit" else unsigned  # UK OB amounts are unsigned
            out.append(Transaction(bank_id, account_id, t["TransactionId"], t["BookingDateTime"][:10],
                                   signed, t["TransactionInformation"], t["Status"] == "Pending"))
        return Page(out, r.body["Links"].get("Next"))


# ---------- The aggregator ----------


class ReauthorizationRequiredException(RuntimeError):
    """Raised when a refresh fails: the only fix is to send the user back through consent."""


class Connection:
    """One user's link to one bank: the tokens and the consent they came from. Mutable because tokens rotate."""

    def __init__(self, connection_id: str, user_id: str, bank: BankApi, adapter: BankAdapter,
                 auth: AuthorizationServer, tokens: TokenResponse) -> None:
        self.connection_id = connection_id
        self.user_id = user_id
        self.bank = bank
        self.adapter = adapter
        self.auth = auth
        self.access_token = tokens.access_token
        self.refresh_token = tokens.refresh_token
        self.refresh_count = 0


@dataclass(frozen=True)
class PendingAuthorization:
    """The authorization request the aggregator builds, and the state it must remember until the redirect comes back."""

    state: str
    code_verifier: str
    code_challenge: str
    scopes: frozenset


class Aggregator:
    CLIENT_ID = "aggregator-app"
    REDIRECT_URI = "https://aggregator.example/callback"

    def __init__(self, random: JavaRandom) -> None:
        self.random = random
        self.pending: dict[str, PendingAuthorization] = {}
        self.connections: dict[str, Connection] = {}
        self.sequence = 0

    def begin_authorization(self, scopes: frozenset) -> PendingAuthorization:
        """Step 1: build the /authorize request. The verifier never leaves this process; only its hash does."""
        state = b64url(self.random.next_bytes(12))
        verifier = Pkce.generate_verifier(self.random)
        p = PendingAuthorization(state, verifier, Pkce.challenge_s256(verifier), frozenset(scopes))
        self.pending[state] = p
        return p

    def authorization_url(self, bank_authorize_endpoint: str, p: PendingAuthorization) -> str:
        scope = " ".join(s.wire for s in sorted(p.scopes, key=SCOPE_ORDER.index))
        return (f"{bank_authorize_endpoint}?response_type=code&client_id={self.CLIENT_ID}&redirect_uri={self.REDIRECT_URI}"
                f"&scope={scope.replace(' ', '%20')}&state={p.state}"
                f"&code_challenge={p.code_challenge}&code_challenge_method=S256")

    def complete_authorization(self, user_id: str, returned_state: str, code: str, auth: AuthorizationServer,
                               bank: BankApi, adapter: BankAdapter) -> Connection:
        """Step 3: the redirect came back with code and state. Verify state (CSRF), exchange the code with the verifier."""
        p = self.pending.pop(returned_state, None)
        if p is None:
            raise OAuthException("invalid_request", "state does not match any pending authorization (possible CSRF)")
        tokens = auth.exchange_code(self.CLIENT_ID, code, self.REDIRECT_URI, p.code_verifier)
        if not p.scopes <= tokens.scopes:
            raise OAuthException("invalid_scope", "bank granted fewer scopes than requested")
        self.sequence += 1
        cid = f"conn_{self.sequence}"
        c = Connection(cid, user_id, bank, adapter, auth, tokens)
        self.connections[cid] = c
        return c

    def connection(self, cid: str) -> Optional[Connection]:
        return self.connections.get(cid)

    def fetch_accounts(self, connection_id: str) -> list[Account]:
        """Fetch every page of accounts, refreshing the token once on 401."""
        c = self.connections[connection_id]
        out: list[Account] = []
        cursor: Optional[str] = None
        while True:
            cur = cursor
            r = self._call_with_refresh(c, lambda token: c.bank.accounts(token, cur))
            page = c.adapter.accounts(r, c.bank.bank_id())
            out.extend(page.items)
            cursor = page.next_cursor
            if cursor is None:
                return out

    def fetch_transactions(self, connection_id: str, account_id: str) -> list[Transaction]:
        c = self.connections[connection_id]
        out: list[Transaction] = []
        cursor: Optional[str] = None
        while True:
            cur = cursor
            r = self._call_with_refresh(c, lambda token: c.bank.transactions(token, account_id, cur))
            page = c.adapter.transactions(r, c.bank.bank_id(), account_id)
            out.extend(page.items)
            cursor = page.next_cursor
            if cursor is None:
                return out

    def _call_with_refresh(self, c: Connection, call: Callable[[str], ApiResponse]) -> ApiResponse:
        """401 invalid_token -> refresh once and retry. 403 insufficient_scope -> never retry; the consent lacks the scope."""
        r = call(c.access_token)
        if r.status == 401:
            try:
                fresh = c.auth.refresh(self.CLIENT_ID, c.refresh_token)
            except OAuthException as e:
                raise ReauthorizationRequiredException(f"{c.connection_id}: {e}") from e
            c.access_token = fresh.access_token
            c.refresh_token = fresh.refresh_token  # rotation: store the new one, the old one is now dead
            c.refresh_count += 1
            r = call(c.access_token)
        if r.status == 403:
            raise OAuthException("insufficient_scope", "consent does not include the scope this call needs")
        if r.status != 200:
            raise RuntimeError(f"unexpected status {r.status}")
        return r


# ---------- Demo ----------


def bank_a(auth: AuthorizationServer) -> FdxStyleBank:
    a = FdxStyleBank(auth, 2)
    a.add_account("acc-chk-1", "Everyday Checking", "USD", "CHECKING", "****4321")
    a.add_account("acc-sav-1", "Rainy Day", "USD", "SAVINGS", "****8765")
    a.add_account("acc-cd-1", "12mo CD", "USD", "CD", "****1111")
    a.add_transaction("acc-chk-1", "t-1", "2026-03-02", "-42.17", "GROCERY MART", "POSTED")
    a.add_transaction("acc-chk-1", "t-2", "2026-03-03", "2500.00", "PAYROLL ACME", "POSTED")
    a.add_transaction("acc-chk-1", "t-3", "2026-03-04", "-1200.00", "RENT", "POSTED")
    a.add_transaction("acc-chk-1", "t-4", "2026-03-05", "-9.99", "STREAMING", "PENDING")
    a.add_transaction("acc-chk-1", "t-5", "2026-03-05", "-61.40", "FUEL", "POSTED")
    return a


def bank_b(auth: AuthorizationServer) -> UkObStyleBank:
    b = UkObStyleBank(auth, 2)
    b.add_account("22289", "Main Current", "GBP", "CurrentAccount", "80200110203345")
    b.add_account("31820", "Holiday Savings", "GBP", "Savings", "80200110203346")
    b.add_transaction("22289", "123", "2026-03-02", "10.00", "Credit", "Cash from Aubrey", "Booked")
    b.add_transaction("22289", "124", "2026-03-03", "36.50", "Debit", "TFL TRAVEL", "Booked")
    b.add_transaction("22289", "125", "2026-03-04", "1800.00", "Credit", "SALARY", "Booked")
    b.add_transaction("22289", "126", "2026-03-05", "4.20", "Debit", "COFFEE", "Pending")
    return b


def connect(agg: Aggregator, auth: AuthorizationServer, bank: BankApi, adapter: BankAdapter, user: str,
            scopes: frozenset, verbose: bool) -> Connection:
    p = agg.begin_authorization(scopes)
    url = agg.authorization_url(f"https://{bank.bank_id()}.example/authorize", p)
    if verbose:
        print(f"1. aggregator redirects user to:\n   {url}")
        print(f"   verifier stays server-side: {p.code_verifier[:12]}... ({len(p.code_verifier)} chars)")
    code = auth.authorize(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI, scopes, p.code_challenge, "S256", user)
    if verbose:
        print(f"2. user authenticates at the bank, approves scopes; bank redirects back with code={code[:12]}... state={p.state}")
    c = agg.complete_authorization(user, p.state, code, auth, bank, adapter)
    if verbose:
        print(f"3. aggregator exchanges code + verifier for tokens: access={c.access_token[:10]}... refresh={c.refresh_token[:10]}...")
    return c


def main() -> None:
    clock = ManualClock(1_772_000_000)  # a fixed instant; advanced by hand below
    random = JavaRandom(2026)
    auth = AuthorizationServer(clock, random)
    auth.register_client(RegisteredClient(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI))
    agg = Aggregator(random)
    scopes = frozenset({Scope.ACCOUNTS_READ, Scope.TRANSACTIONS_READ})

    print("== Consent flow: authorization code + PKCE (RFC 6749 section 4.1, RFC 7636) ==")
    a = connect(agg, auth, bank_a(auth), FdxAdapter(), "user-42", scopes, True)
    print()

    print("== Bank A (FDX-style, page size 2): paginated fetch, normalized ==")
    for acc in agg.fetch_accounts(a.connection_id):
        print(f"   {acc}")
    for t in agg.fetch_transactions(a.connection_id, "acc-chk-1"):
        print(f"   {t}")
    print()

    print("== Bank B (UK Open Banking-style, Links.Next paging): same schema out ==")
    b = connect(agg, auth, bank_b(auth), UkObAdapter(), "user-42", scopes, False)
    for acc in agg.fetch_accounts(b.connection_id):
        print(f"   {acc}")
    for t in agg.fetch_transactions(b.connection_id, "22289"):
        print(f"   {t}")
    print()

    print("== Token expiry and refresh ==")
    clock.advance_seconds(AuthorizationServer.ACCESS_TTL_SECONDS + 1)
    old_refresh = a.refresh_token
    before = a.refresh_count
    n = len(agg.fetch_accounts(a.connection_id))
    print(f"   after {AuthorizationServer.ACCESS_TTL_SECONDS + 1}s the access token is dead: first call got 401, refreshed "
          f"{a.refresh_count - before} time, retried, got {n} accounts")
    print(f"   refresh token rotated: {str(old_refresh != a.refresh_token).lower()}")
    try:
        auth.refresh(Aggregator.CLIENT_ID, old_refresh)
    except OAuthException as e:
        print(f"   replaying the old refresh token: {e}")
    print("   the replay revoked the whole consent, so A's current access token is now dead too: "
          f"{str(auth.introspect(a.access_token) is None).lower()}")
    print()

    print("== Consent expiry (90 days) ==")
    clock.advance_days(91)
    try:
        agg.fetch_transactions(b.connection_id, "22289")
    except ReauthorizationRequiredException as e:
        print(f"   {e}")
    print()

    print("== Things the bank refuses ==")
    p = agg.begin_authorization(scopes)
    code = auth.authorize(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI, scopes, p.code_challenge, "S256", "user-42")
    try:
        auth.exchange_code(Aggregator.CLIENT_ID, code, Aggregator.REDIRECT_URI, Pkce.generate_verifier(random))
    except OAuthException as e:
        print(f"   wrong verifier: {e}")
    try:
        auth.exchange_code(Aggregator.CLIENT_ID, code, Aggregator.REDIRECT_URI, p.code_verifier)
    except OAuthException as e:
        print(f"   same code again (it was consumed by the failed attempt): {e}")
    try:
        auth.authorize(Aggregator.CLIENT_ID, "https://evil.example/callback", scopes, p.code_challenge, "S256", "user-42")
    except OAuthException as e:
        print(f"   unregistered redirect_uri: {e}")
    try:
        auth.authorize(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI, frozenset({Scope.PAYMENTS_WRITE}), p.code_challenge, "S256", "user-42")
    except OAuthException as e:
        print(f"   scope the client is not registered for: {e}")


if __name__ == "__main__":
    main()
