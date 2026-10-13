---
name: checklist-oauth-aggregator-integration
description: Review checklist for any integration that connects to a bank data API or an account aggregator via OAuth 2.0
phase: 11
lesson: 13
tags: [open-banking, oauth, pkce, aggregation, review]
---

# OAuth Aggregator Integration Checklist

Use before merging a new bank adapter or aggregator SDK integration, and again before the first production consent.

## The authorize request

- [ ] `response_type=code`; the implicit grant is not used anywhere.
- [ ] `redirect_uri` is registered with the bank and compared by exact string match; no wildcards, no path prefixes.
- [ ] `state` is at least 96 bits of randomness from a CSPRNG, stored server-side with the pending authorization, and checked on return; a mismatch is logged as a possible CSRF and the code is discarded.
- [ ] PKCE with `S256` on every authorization; the verifier is generated per authorization, kept server-side, and never logged.
- [ ] Requested scopes are the minimum the feature needs and are listed in the design document with the reason for each.

## Tokens

- [ ] Access tokens are treated as expiring at any moment; the client handles `401 invalid_token` on every call, not only the first.
- [ ] Refresh happens at most once per logical operation and only on `401`; `403 insufficient_scope` is surfaced as a permission problem and never retried.
- [ ] Refresh-token rotation is handled: the new refresh token is persisted before the new access token is used, and persistence is atomic with the update.
- [ ] Concurrent syncs on one connection are serialized or single-flighted so two threads cannot both refresh and trigger a replay revocation.
- [ ] Refresh tokens are stored encrypted, access is audited, and they appear in no log, trace, or error message.
- [ ] A refresh failure maps to a "reauthorization required" state that the product surfaces to the user with a working path back through consent.

## Consent

- [ ] Consent expiry (90 days UK, provider-defined elsewhere) is stored and the product prompts for renewal before, not after, it lapses.
- [ ] User-initiated revocation at the bank is detected (401 followed by failed refresh) and the connection is marked, not retried on a schedule.
- [ ] Unattended call budgets per consent (regulatory floors and bank-specific limits) are modelled in the sync scheduler.

## Normalization

- [ ] One sign convention is written down (negative = outflow) and every adapter is tested against it with a debit and a credit.
- [ ] Amounts are parsed from strings into integer minor units; no `double` exists between the wire and the `Money` type; excess precision is rejected.
- [ ] Transaction currency comes from the payload when present and from the account otherwise; the adapter never assumes.
- [ ] Account identifiers are masked at the adapter boundary; full account numbers, sort codes, and IBANs are not stored unless a documented feature needs them.
- [ ] Unknown enumeration values map to `OTHER`/`UNKNOWN` and are counted in a metric rather than thrown.
- [ ] Pagination cursors are opaque to the fetch loop; the adapter is the only code that reads `nextOffset`, `Links.Next`, or `_links.next`.
- [ ] Pending-to-booked transitions and transaction-id changes are handled by the deduplication layer with a test per bank.

## Security profile

- [ ] Token endpoint authentication uses MTLS or private-key JWT as the bank's FAPI profile requires; client secrets in query strings are rejected in review.
- [ ] TLS certificate validation is on, with the bank's CA pinned where the standard requires it.
- [ ] Signed responses (detached JWS) are verified where the API specification mandates them.

## Questions to ask in review

1. If the bank's token endpoint is down for an hour, how many refresh attempts does this code make per connection?
2. Where is the refresh token at the moment after a successful refresh and before the database commit, and what happens if the process dies there?
3. Which test would fail if a bank started sending signed amounts where it used to send unsigned ones?
