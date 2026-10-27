# Stage 04 starter

Continue editing the same workspace: add a new file `Webhooks.java` (uses `javax.crypto.Mac` and
`java.security.MessageDigest.isEqual`). Then give `Gateway` an `attachWebhooks(WebhookDispatcher)` method and
emit `payment.succeeded` / `payment.failed` events from `charge`. The Python and Go ports have the same surface in snake_case and exported CamelCase (signatures below).

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

## New signatures (Go)

Add `webhooks.go`; add `AttachWebhooks` and event emission to `Gateway` in `resilience.go`.

```go
type WebhookEvent struct{ ID, Type, PaymentID, Data string; CreatedAt int64 }
func NewWebhookEvent(id, eventType, paymentID, data string, createdAt int64) (WebhookEvent, error)
func (e WebhookEvent) ToBody() string
var ErrMalformedBody error
func ParseWebhookEvent(body string) (WebhookEvent, error)       // inverse of ToBody; ErrMalformedBody otherwise

func HmacSha256Hex(secret []byte, message string) string
func NewWebhookSigner(secret string) (*WebhookSigner, error)     // empty secret -> error
func (s *WebhookSigner) Signature(timestampMillis int64, body string) string   // HMAC over "<ts>.<body>"
func (s *WebhookSigner) Header(timestampMillis int64, body string) string      // "t=<ts>,v1=<hex>"

type VerifyResult int; const ( VerifyOK VerifyResult = iota; MalformedHeader; StaleTimestamp; BadSignature )
func NewWebhookVerifier(secret string, toleranceMillis int64, clock Clock) (*WebhookVerifier, error)
func (v *WebhookVerifier) Verify(body, header string) VerifyResult

type ReceiveStatus int; const ( Accepted ReceiveStatus = iota; Duplicate; Rejected )
type ReceiveResult struct{ Status ReceiveStatus; Verification VerifyResult; Event *WebhookEvent }   // Event nil when Rejected
func NewWebhookConsumer(verifier *WebhookVerifier) *WebhookConsumer
func (c *WebhookConsumer) Receive(body, header string) ReceiveResult
func (c *WebhookConsumer) Processed() []WebhookEvent

type WebhookEndpoint interface{ Deliver(body, header string) bool }
type Delivery struct{ Body, Header string }
func NewScriptedEndpoint(script string) (*ScriptedEndpoint, error)   // 'S' 'F'; Deliver, Deliveries() []Delivery
func NewConsumerEndpoint(consumer *WebhookConsumer) *ConsumerEndpoint

type DeliveryJob struct{ Event WebhookEvent; Attempts int; NextAttemptAt int64 }
func NewWebhookDispatcher(signer *WebhookSigner, endpoint WebhookEndpoint, clock Clock, retryDelaysMillis []int64) (*WebhookDispatcher, error)
func (d *WebhookDispatcher) MaxAttempts() int; Enqueue(event WebhookEvent); RunDue() int
func (d *WebhookDispatcher) Pending() []DeliveryJob; Delivered() []WebhookEvent; DeadLetters() []DeliveryJob   // copies
func (g *Gateway) AttachWebhooks(dispatcher *WebhookDispatcher)
```

`RunDue` cannot remove from the slice it is iterating the way Java removes from `pending`; build two new slices (jobs not yet due, in order; then the retries it scheduled) and assign `d.pending = append(remaining, retries...)`. The grader checks the order through `Pending()`.
