# Stage 04 starter

Continue editing the same workspace: add a new file `Webhooks.java` (uses `javax.crypto.Mac` and
`java.security.MessageDigest.isEqual`). Then give `Gateway` an `attachWebhooks(WebhookDispatcher)` method and
emit `payment.succeeded` / `payment.failed` events from `charge`. The Python port has the same surface in snake_case (signatures below); Go port follows later.

Stages 01 to 03 are graded again first.

## New signatures (Java)

| Concept | Signature |
|---|---|
| Event | `record WebhookEvent(String id, String type, String paymentId, String data, long createdAt)`; `String toBody()`; `static WebhookEvent parse(String body)` |
| HMAC | `static String WebhookSigner.hmacSha256Hex(byte[] secret, String message)` |
| Signer | `new WebhookSigner(String secret)`; `String signature(long timestampMillis, String body)`; `String header(long timestampMillis, String body)` |
| Verify | `enum VerifyResult { OK, MALFORMED_HEADER, STALE_TIMESTAMP, BAD_SIGNATURE }`; `new WebhookVerifier(String secret, long toleranceMillis, Clock clock)`; `VerifyResult verify(String body, String header)` |
| Consumer | `enum ReceiveStatus { ACCEPTED, DUPLICATE, REJECTED }`; `record ReceiveResult(ReceiveStatus status, VerifyResult verification, WebhookEvent event)`; `new WebhookConsumer(WebhookVerifier)`; `ReceiveResult receive(String body, String header)`; `List<WebhookEvent> processed()` |
| Endpoint | `interface WebhookEndpoint { boolean deliver(String body, String header); }`; `new ScriptedEndpoint("FFS")` with nested `record Delivery(String body, String header)` and `List<Delivery> deliveries()`; `new ConsumerEndpoint(WebhookConsumer)` |
| Dispatcher | `record DeliveryJob(WebhookEvent event, int attempts, long nextAttemptAt)`; `new WebhookDispatcher(WebhookSigner, WebhookEndpoint, Clock, long[] retryDelaysMillis)`; `int maxAttempts()`, `void enqueue(WebhookEvent)`, `int runDue()`, `pending()`, `delivered()`, `deadLetters()` |
| Gateway hook | `gateway.attachWebhooks(WebhookDispatcher dispatcher)`; events `evt_1`, `evt_2`, … with `data = "amount=" + payment.amount()` |

The signed string is `"<timestamp>.<body>"`, the header is `t=<timestamp>,v1=<hex>`, the body is the fixed-order
JSON `{"id":"…","type":"…","payment_id":"…","data":"…","created_at":<millis>}`. Read `docs/en.md`.

## New signatures (Python)

Keep everything in the one `main.py` the stage-1 starter gave you; the names below are the Java ones in snake_case.

```python
@dataclass(frozen=True)
class WebhookEvent:           # id, type, payment_id, data: str, created_at: int; quotes/backslashes or blank id/type -> ValueError
    def to_body(self) -> str: ...
    @staticmethod
    def parse(body: str) -> WebhookEvent: ...                     # malformed -> ValueError

class WebhookSigner:
    def __init__(self, secret: str) -> None: ...                  # empty -> ValueError
    @staticmethod
    def hmac_sha256_hex(secret: bytes, message: str) -> str: ...   # hmac.new(secret, msg, hashlib.sha256).hexdigest()
    def signature(self, timestamp_millis: int, body: str) -> str: ...
    def header(self, timestamp_millis: int, body: str) -> str: ...  # "t=<millis>,v1=<hex>"

class VerifyResult(Enum): OK, MALFORMED_HEADER, STALE_TIMESTAMP, BAD_SIGNATURE
class WebhookVerifier:
    def __init__(self, secret: str, tolerance_millis: int, clock: Clock) -> None: ...
    def verify(self, body: str | None, header: str | None) -> VerifyResult: ...   # hmac.compare_digest

class ReceiveStatus(Enum): ACCEPTED, DUPLICATE, REJECTED
@dataclass(frozen=True)
class ReceiveResult:          # status, verification: VerifyResult, event: WebhookEvent | None
class WebhookConsumer:
    def __init__(self, verifier: WebhookVerifier) -> None: ...
    def receive(self, body: str, header: str) -> ReceiveResult: ...
    def processed(self) -> tuple[WebhookEvent, ...]: ...

class WebhookEndpoint(Protocol): def deliver(self, body: str, header: str) -> bool
@dataclass(frozen=True)
class Delivery:               # body, header  (also reachable as ScriptedEndpoint.Delivery)
class ScriptedEndpoint:       # 'S' succeed, 'F' fail; deliveries() -> tuple[Delivery, ...]
class ConsumerEndpoint:       # ConsumerEndpoint(consumer).deliver(...) is True unless REJECTED

@dataclass(frozen=True)
class DeliveryJob:            # event, attempts: int, next_attempt_at: int
class WebhookDispatcher:
    def __init__(self, signer: WebhookSigner, endpoint: WebhookEndpoint, clock: Clock, retry_delays_millis: Sequence[int]) -> None: ...
    def max_attempts(self) -> int; enqueue(event: WebhookEvent) -> None; run_due() -> int
    def pending(self) -> tuple[DeliveryJob, ...]; delivered() -> tuple[WebhookEvent, ...]; dead_letters() -> tuple[DeliveryJob, ...]

class Gateway:
    def attach_webhooks(self, dispatcher: WebhookDispatcher) -> None: ...   # charge() then emits payment.succeeded / payment.failed
```
