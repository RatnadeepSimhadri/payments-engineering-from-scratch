# Signed Webhooks, At-Least-Once

**Stage 4 of 4.** Plan about 3 hours.

A charge that succeeds synchronously is the easy case. Real payment flows finish later: 3-D Secure challenges, bank transfers, disputes, and refunds that the processor confirms hours after you asked. The gateway tells the merchant about those by calling *them*: a webhook, an HTTP POST from your system to a URL they registered. Webhooks invert every assumption from the first three stages. Now you are the unreliable client and the merchant is the server; their endpoint will be down, slow, or behind a firewall that drops your requests, and the thing you must guarantee is that every event reaches them **at least once**, and that they can turn that into **exactly once** on their side.

Two problems to solve. First, authenticity: anyone who can find the merchant's webhook URL can POST `{"type":"payment.succeeded"}` to it and ship goods for free. The fix is an HMAC-SHA256 signature over the body using a shared secret, with the timestamp bound into the signed string so a captured, valid request cannot be replayed a day later. The merchant verifies with the same secret, a constant-time comparison, and a tolerance window on the timestamp. Second, delivery: the dispatcher queues each event, attempts delivery, and on failure retries on a fixed schedule from the injected clock; when the schedule is exhausted the event is dead-lettered for a human. Because retries mean the merchant will sometimes receive the same event twice (the first attempt succeeded but the 200 was lost), the consumer dedupes by event id.

The invariant: **every event enqueued is eventually in exactly one of `delivered` or `deadLetters`; a consumer with the right secret processes each event id at most once; a body or timestamp the producer did not sign is rejected before it is parsed.** This is the shape of Stripe's `Stripe-Signature` header (`t=…,v1=…`), of Adyen's HMAC notifications, and of GitHub's `X-Hub-Signature-256`; RFC 2104 defines HMAC and RFC 4231 gives the SHA-256 test vectors.

## Worked example

**The body.** Fixed-order JSON so the same event always produces the same bytes; a real system would use a canonical JSON serializer, here a five-field template is enough.

```text
WebhookEvent(id="evt_1", type="payment.succeeded", paymentId="pay_1", data="amount=19.99 USD", createdAt=1700000000000)
body = {"id":"evt_1","type":"payment.succeeded","payment_id":"pay_1","data":"amount=19.99 USD","created_at":1700000000000}
```

**The signature** with secret `whsec_test` at timestamp t = 1,700,000,000,000:

```text
signed string = "1700000000000." + body
v1            = HMAC-SHA256(secret, signed string) = c189aab766a9d122c3d7fed8734fab41c76bd4290e69b3639d4e841f309816fd
header        = t=1700000000000,v1=c189aab7…09816fd
```

Change any byte of the body, the timestamp, or the secret and `v1` changes completely. A reference vector to check your HMAC before anything else: `HMAC-SHA256("key", "The quick brown fox jumps over the lazy dog")` is `f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8`.

**Verification** with tolerance 5 minutes (300,000 ms), consumer clock at t:

| Consumer clock | Header | Result | Why |
|---|---|---|---|
| t | `t=t,v1=<correct>` | `OK` | within window, signature matches |
| t | same header, body with `pay_1` → `pay_2` | `BAD_SIGNATURE` | body changed |
| t | `t=t+1,v1=<signature for t>` | `BAD_SIGNATURE` | timestamp is part of the signed string |
| t | `t=t` | `MALFORMED_HEADER` | no `v1` part |
| t | `t=now,v1=…` | `MALFORMED_HEADER` | timestamp not a number |
| t + 300,000 | `t=t,v1=<correct>` | `OK` | exactly at the edge is inside |
| t + 300,001 | `t=t,v1=<correct>` | `STALE_TIMESTAMP` | one ms too old |
| t − 300,001 | `t=t,v1=<correct>` | `STALE_TIMESTAMP` | from the future counts too (`abs`) |

The window check runs *before* the HMAC so a stale request costs no crypto; the signature compare uses `MessageDigest.isEqual`, which takes the same time whether the first byte or the last byte differs.

**Delivery** of `evt_10` to an endpoint scripted `"FFS"` (fail, fail, succeed) with `retryDelays = [60 s, 300 s]`, so `maxAttempts = 3`:

| Clock | Call | Attempt | Endpoint | Pending afterwards | Delivered | Dead |
|---|---|---|---|---|---|---|
| t | `enqueue(evt_10)` | — | — | `[evt_10: attempts 0, due t]` | — | — |
| t | `runDue()` → 1 | 1 | F | `[evt_10: attempts 1, due t + 60,000]` | — | — |
| t | `runDue()` → 0 | — | not called | unchanged | — | — |
| t + 60,000 | `runDue()` → 1 | 2 | F | `[evt_10: attempts 2, due t + 360,000]` | — | — |
| t + 360,000 | `runDue()` → 1 | 3 | S | `[]` | `[evt_10]` | — |

Every attempt is signed with the clock *at send time* (`t=` differs per attempt) over the *same body*, so a slow retry still verifies on the consumer. An endpoint scripted `"F"` with `retryDelays = [1,000]` dead-letters after 2 attempts: `deadLetters().get(0).attempts() == 2`.

**Consumer dedupe.** The first `receive(body, header)` for `evt_1` is `ACCEPTED` and the event is appended to `processed()`. The second, even with a fresh signature at a later timestamp, is `DUPLICATE`; `processed()` still has one entry. Both are 2xx from the dispatcher's point of view, so a duplicate is not retried. `REJECTED` (bad signature, stale, malformed, or verified-but-unparsable) is the only non-2xx.

## Your task

Add `Webhooks.java`, then extend `Gateway` with `attachWebhooks` and event emission. Stages 1 to 3 are graded again first. Python and Go ports of this project follow later; this pass is Java only.

```java
record WebhookEvent(String id, String type, String paymentId, String data, long createdAt) {
    // blank id/type or a quote/backslash in any field -> IllegalArgumentException
    String toBody();                                // the fixed-order JSON above
    static WebhookEvent parse(String body);        // inverse of toBody(); anything else -> IllegalArgumentException
}

final class WebhookSigner {
    WebhookSigner(String secret);                   // empty -> IllegalArgumentException
    static String hmacSha256Hex(byte[] secret, String message);   // javax.crypto.Mac "HmacSHA256", lowercase hex
    String signature(long timestampMillis, String body);          // hmac over timestamp + "." + body
    String header(long timestampMillis, String body);             // "t=<millis>,v1=<signature>"
}

enum VerifyResult { OK, MALFORMED_HEADER, STALE_TIMESTAMP, BAD_SIGNATURE }

final class WebhookVerifier {
    WebhookVerifier(String secret, long toleranceMillis, Clock clock);   // tolerance < 0 -> IllegalArgumentException
    VerifyResult verify(String body, String header);
}

enum ReceiveStatus { ACCEPTED, DUPLICATE, REJECTED }
record ReceiveResult(ReceiveStatus status, VerifyResult verification, WebhookEvent event) {}

final class WebhookConsumer {
    WebhookConsumer(WebhookVerifier verifier);
    ReceiveResult receive(String body, String header);
    List<WebhookEvent> processed();                 // unmodifiable, in acceptance order
}

interface WebhookEndpoint { boolean deliver(String body, String header); }   // true == 2xx

/** 'S' succeed, 'F' fail; the last character repeats. */
final class ScriptedEndpoint implements WebhookEndpoint {
    record Delivery(String body, String header) {}
    ScriptedEndpoint(String script);
    List<Delivery> deliveries();
}

final class ConsumerEndpoint implements WebhookEndpoint {   // ACCEPTED and DUPLICATE -> true; REJECTED -> false
    ConsumerEndpoint(WebhookConsumer consumer);
}

record DeliveryJob(WebhookEvent event, int attempts, long nextAttemptAt) {}

final class WebhookDispatcher {
    WebhookDispatcher(WebhookSigner signer, WebhookEndpoint endpoint, Clock clock, long[] retryDelaysMillis);  // negative delay -> IllegalArgumentException
    int maxAttempts();                              // retryDelays.length + 1
    void enqueue(WebhookEvent event);               // first attempt due now
    int runDue();                                   // attempt every due job once; returns attempts made
    List<DeliveryJob> pending();
    List<WebhookEvent> delivered();
    List<DeliveryJob> deadLetters();
}

// In Resilience.java:
final class Gateway {
    void attachWebhooks(WebhookDispatcher dispatcher);
    // charge(): after store.complete(), enqueue "payment.succeeded" (201) or "payment.failed" (402);
    // nothing for replays (the event was already emitted) or for released 503s (no final state).
}
```

Rules the tests pin down:

- `verify` order: null body or header → `MALFORMED_HEADER`; header must be exactly two comma-separated parts `t=<long>` and `v1=<hex>`, else `MALFORMED_HEADER`; `|now - t| > tolerance` → `STALE_TIMESTAMP`; then compute the expected signature over `t + "." + body` and compare with `MessageDigest.isEqual` on the UTF-8 bytes → `OK` or `BAD_SIGNATURE`. Tolerance 0 accepts only the exact instant.
- `receive`: anything but `OK` → `REJECTED` with the `VerifyResult` and a null event; a body that verifies but does not parse → `REJECTED` with `OK`; a seen id → `DUPLICATE` with the event; otherwise append and `ACCEPTED`. The seen-id set is the dedupe store; in production it is a unique index on `event_id`.
- `runDue`: snapshot the pending list, skip jobs with `nextAttemptAt > now`, remove each due job, sign `event.toBody()` with `header(now, body)`, call the endpoint. Success → `delivered`. Failure with `attempts + 1 >= maxAttempts()` → `deadLetters` with the final attempt count. Otherwise re-queue with `attempts + 1` and `nextAttemptAt = now + retryDelays[attempts]` (the delay index is the number of attempts *already made* minus one after incrementing; the table above shows the resulting times).
- Gateway events: ids `evt_1`, `evt_2`, … per gateway; `paymentId` is the payment's id; `data` is `"amount=" + payment.amount()` (`"amount=19.99 USD"`); `createdAt` is `clock.nowMillis()`. Emit *after* `store.complete`, so a crash between the two would leave a completed key and a missing event rather than a duplicate charge; the "Going further" section is about closing that gap.

What must fail and how:

| Situation | Expected |
|---|---|
| `new WebhookSigner("")`, `WebhookVerifier` with negative tolerance, negative retry delay | `IllegalArgumentException` |
| `WebhookEvent` with a quote in a field or a blank id | `IllegalArgumentException` |
| `WebhookEvent.parse("{\"id\":\"evt_1\"}")` | `IllegalArgumentException` |
| Tampered body, wrong secret, or a timestamp that does not match the signature | `BAD_SIGNATURE` |
| Header older or newer than the tolerance | `STALE_TIMESTAMP` (checked before the HMAC) |
| Redelivery of a processed event, with any valid signature | `DUPLICATE`, `processed()` unchanged |
| Endpoint fails every attempt | job in `deadLetters` after `maxAttempts()` attempts, never retried again |
| Replayed or released charge | no event enqueued |

## Design notes

**Why sign `timestamp.body` and not just the body.** A signature over the body alone is valid forever. An attacker who captures one legitimate `payment.succeeded` for a 5.00 USD order can replay it against the merchant's endpoint whenever they like; if the merchant's handler is "mark order paid by `payment_id`", the dedupe catches it, but many handlers are not that careful. Binding the timestamp and refusing anything outside a few minutes shrinks the replay window to the tolerance. The consumer's dedupe on event id closes the window the rest of the way.

**Why constant-time comparison.** `String.equals` returns as soon as it finds a differing byte. An attacker who can measure response time can learn the expected signature one byte at a time, a few hundred requests per byte. `MessageDigest.isEqual` (and `hmac.compare_digest` in Python, `hmac.Equal` in Go) compares every byte regardless. It costs nothing and removing it would be the first finding in any security review of a webhook receiver.

**Why at-least-once and not exactly-once delivery.** Exactly-once *delivery* over a network is not achievable: the acknowledgement can always be lost after the receiver has acted. What is achievable is at-least-once delivery plus an idempotent receiver, which gives exactly-once *processing*. That is the same split as Stage 1, seen from the other side: there, the merchant retried and your store deduped; here, you retry and the merchant's consumer dedupes. Both halves need a stable id.

**Why a fixed schedule and a dead-letter list rather than retrying forever.** Webhook endpoints stay down for days when a merchant decommissions a server and forgets the registration. Retrying forever leaks queue memory and hides the problem. A schedule that stretches over hours (Stripe retries for up to three days with increasing gaps) followed by a dead-letter queue the merchant can inspect and replay from a dashboard is the standard shape.

**Why the dispatcher is pull-based (`runDue`) with no thread.** A real dispatcher runs a worker that polls a durable queue. Modelling it as "call `runDue()` when time has passed" keeps the project deterministic and makes every schedule step a visible assertion; the worker loop is a `while (true) { runDue(); clock.sleep(...) }` away.

## Run the grader

```bash
python3 scripts/project_test.py idempotent-payment-gateway --lang java --stage 4 --path my-gateway
python3 scripts/project_test.py idempotent-payment-gateway --lang java --all --path my-gateway --report gateway-report.json
```

Stages 1 to 3 run again (148 assertions), then Stage 4's 50. The `--report` file is what the `build-project` skill reads to mark the project complete.

## Check yourself

1. The dispatcher signs with `now` at each attempt rather than with `event.createdAt()`. What would go wrong with a 5-minute tolerance if the third attempt, six minutes later, were signed with `createdAt`?
2. A merchant's endpoint returns 200 but then crashes before committing the order to its database. Which of your mechanisms notices, and which does not? What does this tell you about where the consumer's dedupe write must sit relative to its business logic?
3. The gateway completes the idempotency key, then enqueues the event. The process dies in between. What does the merchant observe, and how would you detect and repair it?
4. Why is a `DUPLICATE` returned to the dispatcher as success (2xx) rather than as a failure?

## Going further

The gap in question 3 is closed by the **transactional outbox** pattern: write the event into the same database transaction that completes the payment, and have the dispatcher read from that table. Phase 8 (streams and messaging) builds exactly that on top of a log, and Phase 13's reconciliation lesson shows how to detect the events that still went missing. A second extension is to replace the fixed-order JSON with a canonical JSON serializer and verify that `parse(toBody(e)).equals(e)` for randomly generated events, which is the property test a real webhook library ships with.
