---
name: checklist-wire-protocol
description: Review checklist for any service that speaks a custom framed or binary protocol over TCP, or that configures a framework's framing limits
phase: 5
lesson: 7
tags: [rpc, framing, serialization, timeouts, review]
---

# Wire Protocol Review Checklist

Use when reviewing a hand-rolled TCP protocol, a custom codec, or the configuration of gRPC, Kafka, or a database driver. One unchecked box is a reason to request changes.

## Framing

- [ ] The protocol defines how message boundaries are found (length prefix, delimiter, or self-describing). "One write is one message" is not a framing scheme.
- [ ] The reader is incremental: it accepts any segmentation of the stream and never emits a partial message.
- [ ] A length prefix is validated against a documented maximum **before** any allocation. The maximum is small (single-digit MiB or less) and configurable.
- [ ] A framing error closes the connection; the code does not try to resynchronise a stream that has no delimiter.
- [ ] There is a test that feeds the same bytes one byte at a time and all at once and asserts identical output.

## Codec

- [ ] Byte order is fixed and documented (big-endian unless there is a reason), and multi-byte integers are reconstructed with masks (`& 0xFF`) so signed bytes do not sign-extend.
- [ ] Every read checks that enough bytes remain and fails with a protocol-level error type, never with an array or buffer exception.
- [ ] Strings and byte arrays are length-prefixed and the length is checked against the remaining buffer before reading.
- [ ] Trailing bytes after a complete message are rejected, not ignored.
- [ ] Amounts are `i64` minor units plus a currency code and exponent. No floats, no decimal strings parsed with a float parser.
- [ ] If the schema may evolve, fields carry tags or the message carries a version; otherwise the review states that old and new clients will never coexist.

## Request/response

- [ ] Every request carries a client-chosen id and every response echoes it.
- [ ] The id counter is per client (or per process), not per connection, so a reply on an old connection cannot match a request on a new one.
- [ ] Application errors travel back as a typed error response with the request's id; only protocol errors close the connection.
- [ ] Responses whose id is not pending are counted and discarded, never delivered to the wrong caller.

## Deadlines and half-open connections

- [ ] Every call has a deadline. There is no code path that blocks on a read without one.
- [ ] The timeout error says "outcome unknown", and callers of non-idempotent operations (anything that moves money) treat it as unknown, not as failure. Retries require an idempotency key (Phase 5, Lesson 09) or a reversal.
- [ ] Idle connections are probed with application-level heartbeats (gRPC keepalive, HTTP/2 PING, or your own) at an interval measured in seconds, because TCP keepalive defaults to two hours.
- [ ] After a timeout the client reconnects rather than continuing to write into a connection that may be half-open.
- [ ] Connection pools evict connections that have timed out; a half-open connection returned to the pool poisons the next borrower.

## Operability

- [ ] Metrics exist for: frames rejected by length, decode errors, timeouts, late responses discarded, reconnects.
- [ ] A hex dump of one request and one response is in the protocol's documentation, annotated field by field.
