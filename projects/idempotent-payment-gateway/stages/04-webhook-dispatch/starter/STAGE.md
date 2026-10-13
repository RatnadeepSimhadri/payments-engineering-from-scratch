# Stage 04 starter

Continue editing the same workspace: add a new file `Webhooks.java` (uses `javax.crypto.Mac` and
`java.security.MessageDigest.isEqual`). Then give `Gateway` an `attachWebhooks(WebhookDispatcher)` method and
emit `payment.succeeded` / `payment.failed` events from `charge`. Python and Go ports follow later; this pass is Java only.

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
