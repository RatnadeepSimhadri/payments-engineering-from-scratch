---
name: checklist-processor-adapter
description: Review checklist for a pull request that adds or changes a payment processor or rail adapter
phase: 12
lesson: 10
tags: [adapters, processors, decline-codes, capabilities, conformance]
---

# Processor Adapter Review Checklist

Use on every pull request that adds a rail, adds a processor behind an existing rail, or changes how an adapter translates requests or responses. One unchecked box is a reason to request changes.

## The boundary

- [ ] No vendor type, code, or SDK class appears outside the adapter's file or package. `grep` for the vendor's name across product services returns nothing new.
- [ ] Product code branches only on `PaymentStatus`, `DeclineReason`, and `RetryAdvice`. Nothing reads `rawCode` except logging and support tooling.
- [ ] The adapter rejects a `PaymentMethod` of the wrong type with an explicit error, not a `ClassCastException`.

## Wire format

- [ ] Amounts are converted from `Money` minor units using the currency exponent. There is a test for a zero-exponent currency (JPY) and, if supported, a three-exponent one (KWD, BHD).
- [ ] Currency encoding matches the vendor: alpha (`USD`), numeric (`840`), or lowercase (`usd`). There is a test that pins the exact wire value.
- [ ] No float or double appears anywhere in the request or response path.

## Outcomes

- [ ] Every documented vendor code is mapped, and the mapping table is in one place, next to a link to the vendor's code list.
- [ ] Unmapped codes become `UNKNOWN` with `DO_NOT_RETRY`, and the `UNKNOWN` count has an alert.
- [ ] Codes that forbid re-attempts (fraud, stolen card, ACH R10/R29, closed account) map to a `DO_NOT_RETRY` reason.
- [ ] Customer-fixable declines (SCA soft decline, redirect approval) map to `REQUIRES_ACTION`, not `DECLINED`.
- [ ] Asynchronous rails return `PENDING` and declare `ASYNC_CONFIRMATION`; synchronous rails never return `PENDING`.

## Capabilities

- [ ] The adapter's `Capabilities` (flags and currencies) match the vendor contract for this merchant configuration, with a link to the vendor's documentation.
- [ ] Every entry point checks capabilities before the first vendor call. Tests assert zero vendor calls on refusal.

## Idempotency

- [ ] The port's reference reaches the vendor's idempotency mechanism, or, if the rail has none, the adapter dedupes by reference itself and the dedupe is persisted.
- [ ] Retrying a `charge` after a timeout cannot create a second vendor charge. There is a test.

## Events

- [ ] `translateEvent` returns empty for unknown, malformed, and foreign events and never throws.
- [ ] Signature verification (Phase 12, Lesson 05) runs before translation.
- [ ] Late reversals after a final status (ACH returns after settlement, chargebacks) are routed to the returns/disputes handler, not applied as a status flip.

## Conformance

- [ ] The adapter is registered in the conformance suite and passes it in CI.
- [ ] Any new contract this adapter needed was added to the suite for every adapter, not special-cased in this adapter's tests.
