---
name: checklist-webhook-consumer
description: Design-review checklist for any endpoint that receives webhooks about money, plus the producer-side delivery runbook
phase: 12
lesson: 5
tags: [webhooks, hmac, idempotency, retries, review]
---

# Webhook Consumer Review Checklist

Use before a webhook endpoint goes public, and again whenever the handler or the framework underneath it changes. Every unchecked box is a way to ship goods that were not paid for.

## Verification

- [ ] The raw request body bytes are captured before any framework parses them, and the signature is computed over those bytes. Never over a re-serialized object.
- [ ] The MAC is HMAC-SHA256 (or the producer's documented scheme) over `timestamp.payload`, using the producer's shared secret from a secrets manager, not from source control.
- [ ] The timestamp is checked against the consumer's clock with a symmetric tolerance (300 seconds unless the producer documents otherwise) *before* the MAC is computed.
- [ ] Tag comparison is constant-time (`MessageDigest.isEqual`, `hmac.compare_digest`, `hmac.Equal`, or an audited equivalent). No `equals`, `==`, or `strcmp`.
- [ ] The header parser accepts multiple `v1=` entries so secret rotation does not cause an outage, and ignores unknown scheme names.
- [ ] A verification failure returns 4xx and is logged with the failure kind (malformed, stale, bad signature) and the source IP, without logging the body or the secret.

## Idempotency and ordering

- [ ] Each event id is recorded in the same transaction as the state change it causes. A crash between the two cannot leave the event half-applied.
- [ ] A redelivered event id returns 200 and performs no side effects (no second shipment, no second email, no second ledger posting).
- [ ] The handler does not assume events arrive in order. Either events carry a per-object sequence that the consumer enforces, or the handler fetches the object's current state from the producer's API and applies that.
- [ ] An event older than the applied state returns 200 and is not applied. It is not an error.
- [ ] The dedupe store has a retention period at least as long as the producer's maximum retry horizon (Stripe: 3 days) plus a safety margin.

## Response codes

| Situation | Return | Reason |
|-----------|--------|--------|
| Bad or missing signature, stale timestamp, malformed header | 400 | Producer must not retry |
| Duplicate event id | 200 | Stop the producer's retries |
| Older sequence than applied | 200 | Nothing to do |
| Applied successfully | 200 | Normal |
| Our dependency (database, queue) is down | 500 or 503 | We want the retry |
| Handler is slow | 200 immediately, process asynchronously | Avoid timeouts that cause duplicates |

- [ ] The handler acknowledges within the producer's timeout (typically 10-30 seconds). Slow work (shipping, email, third-party calls) is queued, not done inline.
- [ ] No code path returns 5xx for a condition that a retry cannot fix.

## Operations

- [ ] Alerting on verification failure rate (a spike means an attack or a rotated secret the consumer did not receive).
- [ ] Alerting on 5xx rate from the handler (the producer's retries will mask an outage for hours).
- [ ] A documented procedure to replay events from the producer's dashboard or API after a consumer outage longer than the producer's retry horizon.
- [ ] Secret rotation rehearsed: new secret added to the consumer first, then the producer switches, then the old secret is removed.

## Producer-side runbook (if you are the one sending)

- [ ] Events are written to an outbox table in the same transaction as the state change; a relay process hands them to the dispatcher.
- [ ] Each retry is signed at send time with a fresh timestamp.
- [ ] Backoff is exponential with a hard cap on attempts; the schedule and the cap are documented for consumers.
- [ ] Dead-lettered deliveries are visible in a dashboard and trigger a notification to the consumer's technical contact.
- [ ] Consumers can list and resend events by id from the dashboard or API.
- [ ] Payloads contain no PAN, CVV, or full account numbers (see Phase 13, Lesson 08).
