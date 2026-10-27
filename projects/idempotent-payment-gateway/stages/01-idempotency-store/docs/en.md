# Idempotency Store

**Stage 1 of 4.** Plan about 2 hours.

A merchant's checkout page sends `POST /charges` for 19.99 USD. The request reaches your gateway, the card is charged, and the response is lost on the way back because the merchant's load balancer timed out at 10 seconds. The merchant's client does what every well-behaved HTTP client does: it retries. Without an idempotency store, the second request is indistinguishable from a new order and the customer is charged twice. With one, the second request is recognised as a *replay* of the first and gets the same 201 and the same body, and no money moves.

This stage builds that store. It is keyed by `(merchantId, idempotencyKey)` so two merchants who both pick `"order-1"` as a key never collide. Each record carries the SHA-256 fingerprint of the request's *canonical form*: the fields that define what the merchant meant (amount, currency, source, description) in a fixed order. The fingerprint is what lets the store tell a harmless replay apart from a dangerous *conflict*: the same key with a different amount, which is almost always a client bug and must be rejected rather than silently answered with the old response. Records also expire on an injected `Clock`, because a key reused a month later by a different code path should be treated as new, and because an unbounded store is a memory leak.

The invariant: **for a given live `(merchant, key)`, at most one caller is ever told `NEW`.** Every other caller is told `IN_PROGRESS` (the first one has not finished), `REPLAY` (here is the stored response), or `CONFLICT` (your body differs). Stripe, Adyen and PayPal all expose this contract on their charge endpoints; the Phase 5 lesson on idempotency keys built the concurrent version, and Phase 12 Lesson 01 designed the API shape. Here you build the store that Stage 3's `Gateway` will sit on top of.

## Worked example

A request and its canonical form. The key is deliberately *not* part of the canonical string: the whole point is to compare two requests that share a key.

```text
ChargeRequest(merchantId="m_1", idempotencyKey="key-1", amount=19.99 USD, source="tok_visa", description="order-42")
canonical   = "1999\nUSD\ntok_visa\norder-42"
fingerprint = sha256(canonical) = 485e792d29b86326c3335f0b5e84d3a948d47121f7e4c4b57f889d7287e90b91
```

Notice `1999`, not `19.99`: the amount is rendered as minor units from `Money.minorUnits()`, so there is no floating-point formatting to disagree about. Changing the amount to 29.99 changes the first line to `2999` and therefore the whole digest. Changing the key to `key-2` changes nothing.

Now a store with a 60-second TTL on a `FakeClock` starting at t = 1,000,000 ms:

| t (ms) | Call | Store before | Decision | Store after |
|---|---|---|---|---|
| 1,000,000 | `begin(m_1, key-1, fp_19.99)` | empty | `NEW` | `key-1: IN_PROGRESS, fp_19.99, expires 1,060,000` |
| 1,000,000 | `begin(m_1, key-1, fp_19.99)` | in progress | `IN_PROGRESS` | unchanged |
| 1,000,000 | `begin(m_2, key-1, fp_19.99)` | no record for m_2 | `NEW` | second record under `(m_2, key-1)` |
| 1,000,000 | `begin(m_1, key-1, fp_29.99)` | in progress | `CONFLICT` | unchanged |
| 1,000,000 | `complete(m_1, key-1, 201 {...})` | in progress | — | `key-1: COMPLETED, response 201` |
| 1,000,000 | `begin(m_1, key-1, fp_19.99)` | completed | `REPLAY` + stored 201 | unchanged |
| 1,059,999 | `begin(m_1, key-1, fp_19.99)` | completed, 1 ms left | `REPLAY` | unchanged |
| 1,060,000 | `get(m_1, key-1)` | expired (`now >= expiresAt`) | `Optional.empty()` | unchanged (lazy) |
| 1,060,000 | `begin(m_1, key-1, fp_29.99)` | expired | `NEW` | record replaced with `fp_29.99`, expires 1,120,000 |

Two details the table pins down. Expiry is `now >= expiresAt`, so a record created at 1,000,000 with a 60,000 ms TTL is live at 1,059,999 and gone at 1,060,000. And expiry is *lazy*: `get` and `begin` treat an expired record as absent without deleting it, so `size()` still counts it until `purgeExpired()` runs. In the test, after the sequence above `size()` is 3 and `purgeExpired()` returns 2.

The fourth operation, `release`, exists for Stage 3: when the processor is unreachable the gateway must *not* store a 503 as the permanent answer to this key, so it gives the key back and the merchant's retry is told `NEW` again. Releasing a `COMPLETED` key is a bug and throws.

## Your task

`Money.java`, `Clock.java` and `Rng.java` are complete and must not change. `Idempotency.java` holds the types below with the method bodies stubbed to throw `UnsupportedOperationException`. (Python: the one `main.py` holds the same given parts, with the same methods stubbed to `raise NotImplementedError`. Go: `money.go`, `clock.go`, `rng.go` are given and `idempotency.go` holds the same stubs as `panic("not implemented: ...")`.) The Python port has the same surface in snake_case in one `main.py`, and the Go port the same surface in exported CamelCase (signatures below).

```java
final class Fingerprint {
    static String sha256Hex(String canonical);   // 64 lowercase hex chars of SHA-256 over the UTF-8 bytes
}

record ChargeRequest(String merchantId, String idempotencyKey, Money amount, String source, String description) {
    String canonical();      // minorUnits + "\n" + currency code + "\n" + source + "\n" + description
    String fingerprint();    // Fingerprint.sha256Hex(canonical())
}

record ApiResponse(int status, String body, boolean replayed) { ApiResponse asReplay(); }   // given
enum KeyState { IN_PROGRESS, COMPLETED }                                                    // given
enum Decision { NEW, REPLAY, IN_PROGRESS, CONFLICT }                                        // given
record BeginResult(Decision decision, ApiResponse response)                                 // given; response only for REPLAY
record IdempotencyRecord(String merchantId, String key, String fingerprint, KeyState state,
                         ApiResponse response, long createdAt, long expiresAt) { boolean isExpired(long now); }  // given

final class IdempotencyStore {
    IdempotencyStore(Clock clock, long ttlMillis);                                  // given; ttl <= 0 throws
    synchronized BeginResult begin(String merchantId, String key, String fingerprint);
    synchronized void complete(String merchantId, String key, ApiResponse response);
    synchronized boolean release(String merchantId, String key);
    synchronized Optional<IdempotencyRecord> get(String merchantId, String key);
    synchronized int purgeExpired();
    synchronized int size();                                                        // given
}
```

The same surface in Python (one `main.py` module; methods and fields are snake_case, exceptions end in `Error`, read-only views are tuples):

```python
class Fingerprint:
    @staticmethod
    def sha256_hex(canonical: str) -> str: ...        # hashlib.sha256(...).hexdigest(): 64 lowercase hex chars

@dataclass(frozen=True)
class ChargeRequest:          # merchant_id, idempotency_key, amount: Money, source, description; guards -> ValueError
    def canonical(self) -> str: ...                    # f"{amount.minor_units}\n{currency code}\n{source}\n{description}"
    def fingerprint(self) -> str: ...                  # Fingerprint.sha256_hex(self.canonical())

@dataclass(frozen=True)
class ApiResponse:            # status: int, body: str, replayed: bool = False; as_replay() -> ApiResponse   (given)
class KeyState(Enum):  IN_PROGRESS, COMPLETED                                                               (given)
class Decision(Enum):  NEW, REPLAY, IN_PROGRESS, CONFLICT                                                   (given)
@dataclass(frozen=True)
class BeginResult:            # decision: Decision, response: ApiResponse | None = None; BeginResult.of(decision)  (given)
@dataclass(frozen=True)
class IdempotencyRecord:      # merchant_id, key, fingerprint, state, response, created_at, expires_at; is_expired(now)  (given)

class IdempotencyStore:
    def __init__(self, clock: Clock, ttl_millis: int) -> None: ...        # given; ttl <= 0 -> ValueError; holds a threading.Lock
    def begin(self, merchant_id: str, key: str, fingerprint: str) -> BeginResult: ...
    def complete(self, merchant_id: str, key: str, response: ApiResponse) -> None: ...   # misuse -> RuntimeError
    def release(self, merchant_id: str, key: str) -> bool: ...                          # COMPLETED -> RuntimeError
    def get(self, merchant_id: str, key: str) -> IdempotencyRecord | None: ...         # None instead of Optional.empty()
    def purge_expired(self) -> int: ...
    def size(self) -> int: ...                                                          # given
```

The same surface in Go (`package main`, one file per Java file; exported CamelCase; constructors return `(value, error)`; `Optional` becomes `(value, ok)`; `IllegalStateException` becomes the sentinel `ErrIllegalState` tested with `errors.Is`):

```go
func Sha256Hex(canonical string) string                       // crypto/sha256 + encoding/hex, lowercase

type ChargeRequest struct{ MerchantID, IdempotencyKey string; Amount Money; Source, Description string }
func NewChargeRequest(merchantID, idempotencyKey string, amount Money, source, description string) (ChargeRequest, error)   // given
func (r ChargeRequest) Canonical() string                     // "1999\nUSD\ntok_visa\norder-42"
func (r ChargeRequest) Fingerprint() string                   // Sha256Hex(Canonical())

type ApiResponse struct{ Status int; Body string; Replayed bool }   // given, with AsReplay()
type KeyState int; const ( KeyInProgress KeyState = iota; KeyCompleted )
type Decision int; const ( DecisionNew Decision = iota; DecisionReplay; DecisionInProgress; DecisionConflict )
type BeginResult struct{ Decision Decision; Response *ApiResponse }   // Response is nil unless DecisionReplay
type IdempotencyRecord struct{ MerchantID, Key, Fingerprint string; State KeyState; Response *ApiResponse; CreatedAt, ExpiresAt int64 }
func (r IdempotencyRecord) IsExpired(now int64) bool

var ErrIllegalState = errors.New("illegal state")
func NewIdempotencyStore(clock Clock, ttlMillis int64) (*IdempotencyStore, error)   // given; ttl <= 0 -> error
func (s *IdempotencyStore) TTLMillis() int64
func (s *IdempotencyStore) Begin(merchantID, key, fingerprint string) BeginResult
func (s *IdempotencyStore) Complete(merchantID, key string, response ApiResponse) error   // ErrIllegalState
func (s *IdempotencyStore) Release(merchantID, key string) (bool, error)                 // false if absent; ErrIllegalState if completed
func (s *IdempotencyStore) Get(merchantID, key string) (IdempotencyRecord, bool)
func (s *IdempotencyStore) PurgeExpired() int
func (s *IdempotencyStore) Size() int
```

The store holds a `sync.Mutex` and every method locks it: that is Java's `synchronized` written out, and it is what makes two concurrent `Begin` calls with the same key admit exactly one. `FakeClock` is `NewFakeClock(start)` with `Advance`, `Set`, `NowMillis`, `Sleep`; `Rng` is `NewRng(seed)` with `NextLong()` and `NextBelow(bound)` on a `uint64` state so the shifts are logical like Java's `>>>`.

What each must do:

- `Fingerprint.sha256Hex`: `MessageDigest.getInstance("SHA-256")` over `canonical.getBytes(UTF_8)`, hex-encoded lowercase. `sha256Hex("abc")` is the FIPS 180-4 vector `ba7816bf…f20015ad`; the empty string digests to `e3b0c442…7852b855`.
- `canonical()`: exactly four lines joined by `\n`, in the order amount-minor-units, currency code, source, description. The record's compact constructor already rejects newlines inside `source` and `description`, so the canonical form is unambiguous.
- `begin`: look up `(merchantId, key)`; an expired record counts as absent and is removed. Absent → insert an `IN_PROGRESS` record with `createdAt = now`, `expiresAt = now + ttl`, return `NEW`. Present with a different fingerprint → `CONFLICT` (checked *before* the state, so a conflicting body is never told `IN_PROGRESS`). Same fingerprint and `IN_PROGRESS` → `IN_PROGRESS`. Same fingerprint and `COMPLETED` → `REPLAY` with the stored response.
- `complete`: the record must exist, be live, and be `IN_PROGRESS`; otherwise `IllegalStateException`. Store the response with `replayed = false` (the gateway marks the copy it hands out with `asReplay()`), keep `createdAt` and `expiresAt` unchanged: completing does not extend the TTL.
- `release`: remove an `IN_PROGRESS` record and return `true`; return `false` if absent; throw `IllegalStateException` for a `COMPLETED` record.
- `get`: the live record or `Optional.empty()`; never deletes.
- `purgeExpired`: remove every record with `isExpired(now)` and return the count.

What must fail and how:

| Situation | Expected |
|---|---|
| `new IdempotencyStore(clock, 0)` | `IllegalArgumentException` |
| `ChargeRequest` with zero or negative amount, blank key, or a newline in `description` | `IllegalArgumentException` from the constructor |
| `complete` on an unknown, expired, or already completed key | `IllegalStateException` |
| `release` on a completed key | `IllegalStateException` |
| `begin` with the same key and a different fingerprint, whether in progress or completed | `Decision.CONFLICT`, store unchanged |

## Build it

Work in this order; each step unlocks a block of tests.

**1. The digest.** `MessageDigest` is in `java.security`; the hex loop is the only fiddly part.

```java
MessageDigest digest = MessageDigest.getInstance("SHA-256");
byte[] bytes = digest.digest(canonical.getBytes(StandardCharsets.UTF_8));
StringBuilder hex = new StringBuilder(bytes.length * 2);
for (byte b : bytes) hex.append(String.format("%02x", b));
return hex.toString();
```

`NoSuchAlgorithmException` is checked but cannot happen for SHA-256 (every JDK ships it); wrap it in an `IllegalStateException` rather than letting it leak into every signature.

**2. The canonical form.** Four fields, fixed order, newline separators, minor units for the amount:

```java
return amount.minorUnits() + "\n" + amount.currency().code() + "\n" + source + "\n" + description;
```

**3. The store's lookup.** Every method starts the same way, so write it once:

```java
StoreKey storeKey = new StoreKey(merchantId, key);
long now = clock.nowMillis();
IdempotencyRecord existing = records.get(storeKey);
if (existing != null && existing.isExpired(now)) {
    records.remove(storeKey);
    existing = null;
}
```

**4. The decision ladder in `begin`.** Absent, then fingerprint, then state:

```java
if (existing == null) {
    records.put(storeKey, new IdempotencyRecord(merchantId, key, fingerprint, KeyState.IN_PROGRESS, null, now, now + ttlMillis));
    return BeginResult.of(Decision.NEW);
}
if (!existing.fingerprint().equals(fingerprint)) return BeginResult.of(Decision.CONFLICT);
if (existing.state() == KeyState.IN_PROGRESS) return BeginResult.of(Decision.IN_PROGRESS);
return new BeginResult(Decision.REPLAY, existing.response());
```

**5. `complete`, `release`, `get`, `purgeExpired`.** Records are immutable, so `complete` *replaces* the record with a copy whose state is `COMPLETED` and whose `createdAt`/`expiresAt` are carried over. `purgeExpired` iterates with an explicit `Iterator` so it can `remove()` while walking.

## Design notes

**Why a canonical string and not the JSON body.** Two JSON encoders can emit the same object with different key order, whitespace or number formatting (`19.99` vs `19.990`), and a merchant upgrading an SDK would suddenly see `CONFLICT` on every retry. Fingerprinting a canonical projection of the fields that matter makes the comparison stable across encoders and lets you leave out fields that legitimately vary between retries, such as a client-side timestamp or a trace id. Production APIs differ on this point: Stripe compares the raw body and parameters, which is simpler but stricter; the canonical approach is what you want when you control the SDK. Either way, the comparison must be *exact* on the fields that move money.

**Why the key is scoped to the merchant.** Idempotency keys are chosen by the client, and clients pick predictable values (`order-1`, UUIDs generated from the same seed, the string `"test"`). A global namespace would let one merchant's key collide with another's and replay a stranger's response. Scoping to the authenticated principal is the minimum; a real gateway also scopes to the endpoint, so a key used on `/charges` and `/refunds` is two keys.

**Why `IN_PROGRESS` is its own answer.** The first request holds the key while the processor call runs, which can be seconds. A concurrent duplicate must not be told `NEW` (two charges) and cannot be told `REPLAY` (there is no response yet). Returning `IN_PROGRESS` lets the HTTP layer answer `409 Conflict` with a `Retry-After`, and the client retries after the first call has completed, at which point it gets the replay. The `synchronized` keyword on every store method is what makes "at most one `NEW`" hold under real concurrency; a database-backed store achieves the same thing with a unique index on `(merchant_id, key)` and an insert that fails on duplicate.

**Why expiry is lazy.** Checking `isExpired(now)` at read time means no background thread, no timer and no wall clock in the hot path; the `Clock` is injected so the tests move time by calling `advance`. `purgeExpired()` exists so an operator (or a scheduled job) can reclaim memory; correctness never depends on it running.

**What the TTL should be.** Long enough that any client retry policy has given up (Stripe keeps keys for 24 hours), short enough that the store does not grow without bound. The demo uses 24 hours; the tests use 60 seconds so the expiry boundary is easy to see.

## Run the grader

```bash
python3 scripts/project_test.py idempotent-payment-gateway --lang java --init my-gateway
python3 scripts/project_test.py idempotent-payment-gateway --lang python --init my-gateway
python3 scripts/project_test.py idempotent-payment-gateway --lang go --init my-gateway
python3 scripts/project_test.py idempotent-payment-gateway --lang java --stage 1 --path my-gateway
python3 scripts/project_test.py idempotent-payment-gateway --lang python --stage 1 --path my-gateway
python3 scripts/project_test.py idempotent-payment-gateway --lang go --stage 1 --path my-gateway
```

The stage 1 test makes 43 assertions. A fresh workspace fails the first one with `UnsupportedOperationException: TODO stage 01: Fingerprint.sha256Hex` (Go: a panic `not implemented: TODO stage 01 Sha256Hex`); that is the expected starting point.

## Check yourself

1. Why is the idempotency key left out of the canonical form, and what would break if you included it?
2. A merchant sends the same key with the same body while the first request is still running. Why is `IN_PROGRESS` a safer answer than either `NEW` or `REPLAY`? What should the HTTP layer map it to?
3. The store checks the fingerprint before the state. Describe the request sequence that would be mishandled if the order were reversed.
4. `complete` does not extend `expiresAt`. The TTL is 60 s and a processor call takes 61 s. What does `complete` do, what does the merchant's retry see, and what would you change in Stage 3 to make that safe?
