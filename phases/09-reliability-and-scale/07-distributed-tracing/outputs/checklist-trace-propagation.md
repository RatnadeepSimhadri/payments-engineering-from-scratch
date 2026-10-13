---
name: checklist-trace-propagation
description: Review checklist for a service joining a traced system, covering id generation, header handling, every hop type, attribute hygiene, sampling, and the two failure signatures
phase: 9
lesson: 7
tags: [tracing, observability, w3c-trace-context, opentelemetry, review]
---

# Trace Propagation Review Checklist

Use before a service's first deploy into a traced system, and whenever an incident review finds a trace with a hole in it.

## Identifiers and headers

- [ ] Trace ids are 16 random bytes and span ids 8 random bytes, rendered as lowercase hex; neither can be all zeros. No timestamps, hostnames, or sequence numbers inside the id.
- [ ] Incoming `traceparent` is parsed strictly (W3C Trace Context Section 3.2.2): wrong length, uppercase hex, all-zero ids, or version `ff` means restart the trace. A malformed header never fails the request.
- [ ] A future-version header with extra fields is parsed by its first four fields, not rejected.
- [ ] `tracestate` is forwarded unchanged on outbound calls inside the organisation. It is stripped on calls that leave the organisation (merchant webhooks, third-party APIs) unless a vendor contract requires it.

## Every hop, not just HTTP

- [ ] Synchronous HTTP and gRPC clients inject `traceparent` on every outbound request (instrumentation library or hand-written interceptor).
- [ ] Message producers put `traceparent` into record or message headers, and every consumer extracts it before starting its span. A consumer that reads only the payload is the most common dropped-header site.
- [ ] Work handed to a thread pool, executor, `CompletableFuture`, goroutine, or `asyncio` task carries the context explicitly (wrapped runnable, `Context.makeCurrent()`, `context.Context` parameter, `contextvars`). Thread-local state does not cross these boundaries on its own.
- [ ] Scheduled jobs, retries from a dead-letter queue, and batch processors start a new trace and attach a span link to the originating trace rather than pretending to be its child.
- [ ] Every span that is started is ended on every code path, including exceptions and early returns (`try`/`finally`). An unended span is never exported and its children become orphans.

## Attributes

- [ ] Spans carry `payment.id` (or the equivalent business key) so a trace can be found from a support ticket, and the same key appears in log records (Lesson 08).
- [ ] Amounts are recorded as minor-unit integers plus a currency code, never as floats.
- [ ] No PAN, CVV, account number, name, address, email, or secret appears in any span attribute or span name. Span names are low-cardinality templates (`POST /charges/{id}`), not literal URLs.
- [ ] Attribute names follow OpenTelemetry semantic conventions where one exists (`http.request.method`, `db.system`, `messaging.system`, `rpc.service`).

## Sampling

- [ ] The sampling strategy is written down: head-based rate, tail-based rules, or both, and which component makes the decision.
- [ ] Traces containing an `ERROR` status span are always kept, which requires a decision point that has seen the whole trace.
- [ ] The `sampled` flag received from upstream is honoured; a service does not re-decide for a trace that is already being recorded.
- [ ] The exporter's drop behaviour under back-pressure is known (batch processor queue size, drop policy) and the drop count is a metric with an alert.

## Reading a trace

- [ ] Engineers know to sort by exclusive (self) time, not duration, when hunting for the slow span.
- [ ] A trace with a gap between a parent's start and its first child's start is understood as uninstrumented time in the parent (connection acquisition, serialisation, queueing), not as a tracing bug.
- [ ] Clock skew between hosts is bounded (NTP monitored); a child that appears to start before its parent is flagged rather than silently clipped.

## Failure signatures to alert on

- [ ] **Dropped header:** a rise in single-span traces from a service that is never a legitimate entry point. Alert on root-span count per service for non-edge services.
- [ ] **Lost parent:** a rise in orphan spans (parent id matching nothing in the trace). Alert on the collector's orphan ratio; investigate exporter drops and unended spans first.
- [ ] **Silent loss of volume:** span export rate per service compared against request rate; a ratio that falls is an exporter or sampling problem before it is anything else.
