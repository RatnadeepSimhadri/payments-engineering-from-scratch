---
name: checklist-log-hygiene
description: Review checklist for logging changes in a payment service, covering record shape, mandatory fields, context propagation, redaction, sampling, indexing, and the PCI DSS requirements a log statement can violate
phase: 9
lesson: 8
tags: [logging, observability, pci-dss, redaction, review]
---

# Log Hygiene Review Checklist

Use on any pull request that adds, changes, or removes a log statement, and on any change to the logging configuration or pipeline.

## Record shape

- [ ] Every record is one line of JSON (or the team's agreed structured format). No multi-line records; stack traces live in a field.
- [ ] The message is a constant template (`authorize started`), not a sentence with interpolated values. Everything that varies is a named field.
- [ ] Field names follow the team's contract (`payment_id`, `merchant_id`, `trace_id`, `span_id`, `amount_minor`, `currency`); no `paymentId`/`pid`/`payment` variants.
- [ ] Timestamps are epoch milliseconds (or RFC 3339 with offset) from a monotonic-adjusted clock, never a local time without zone.
- [ ] Amounts are integer minor units plus an ISO 4217 code. No `19.99`, no floats, no currency symbols.

## Context

- [ ] `payment_id` (or the equivalent business key) is bound once at the request edge and present on every record emitted for that request. A test asserts it.
- [ ] `trace_id` and `span_id` from the active span are on every record inside a request so logs join to traces (Lesson 07).
- [ ] Work handed to another thread, executor, future, or queue consumer carries the logging context explicitly. If the framework's MDC is a `ThreadLocal`, a context-copying executor or an explicit copy is in place and `MDC.clear()` runs in a `finally`.
- [ ] Records emitted outside any request (startup, scheduled jobs) carry a `job_id` or `run_id` so they can be grouped.

## Redaction

- [ ] No PAN, CVV/CVC, PIN, track data, full account number, password, API key, bearer token, or webhook secret is passed to any log call, at any level, in any field, including request and response bodies and exception messages.
- [ ] A redactor runs on every value before the record is written: wholesale replacement for secret-named keys, first-six-last-four masking for Luhn-valid 13-19 digit runs found anywhere in a value.
- [ ] The redactor's tests include: PAN with spaces, PAN with dashes, two PANs in one value, PAN inside a JSON body, 13- and 19-digit PANs, and a non-Luhn digit run that must be left alone.
- [ ] A test asserts that the raw PAN string never appears anywhere in the store after a full request is logged.
- [ ] Centralised redaction (collector or pipeline transform) exists as a second layer behind the in-process redactor.
- [ ] Any new field named `body`, `payload`, `request`, `response`, `headers`, or `message` from an external system triggers a second reviewer.

## Levels and sampling

- [ ] Each statement's level is justified: `ERROR` means a failed operation someone should look at; `WARN` means handled-but-unexpected; `INFO` marks a state transition; `DEBUG` is for diagnosis and is assumed to be enabled in production during an incident.
- [ ] `WARN` and `ERROR` are never sampled out.
- [ ] `DEBUG` and `INFO` sampling is per trace (hash of `trace_id`), not per record, so a kept request's story is complete.
- [ ] The production minimum level and sampling rates are configuration, changeable at runtime without a deploy, and the current values are visible on a dashboard.
- [ ] Projected volume of the change is estimated (records per request x requests per second x bytes per record) and compared against the ingestion budget.

## Indexing and query

- [ ] The indexed-field set is explicit and small (`payment_id`, `trace_id`, `merchant_id`, `service`, `level`). New high-cardinality indexed fields need a cost justification.
- [ ] "Show me everything for payment X" is a single saved query that returns records from every service in time order, and someone has run it this quarter.
- [ ] Retention per log class is documented and enforced; cardholder-data-adjacent logs do not outlive their justification.

## PCI DSS touchpoints

- [ ] Requirement 3.3.1: no sensitive authentication data (CVV, PIN, track) after authorisation, anywhere, including logs.
- [ ] Requirement 3.4.1: PAN displayed with at most first six and last four digits; masking tests are part of the evidence package.
- [ ] Requirement 10.2/10.3: audit logs of access to cardholder data are generated, protected from modification, and retained for at least twelve months with three months immediately available.
- [ ] Access to the log platform follows least privilege; vendor and contractor access is reviewed with the same rigour as production database access.
