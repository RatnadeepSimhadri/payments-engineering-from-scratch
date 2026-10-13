---
name: checklist-account-identifier-intake
description: Review checklist for any form, API, or file import that accepts a bank account identifier (IBAN, BIC, routing number, local account number)
phase: 11
lesson: 11
tags: [iban, bic, aba, validation, payouts, onboarding]
---

# Account Identifier Intake Checklist

Use on every surface that accepts a bank account identifier: onboarding forms, payout APIs, bulk CSV importers, support tools. Each one should give the same answer for the same input.

## Normalization

- [ ] Whitespace, hyphens, and line breaks are stripped and letters upper-cased before any check; the paper form (`GB82 WEST 1234 ...`) and the machine form are the same identifier.
- [ ] Only the machine form is stored. The paper form is regenerated for display (groups of four).
- [ ] Non-ASCII characters (Unicode digits, look-alike letters, zero-width spaces pasted from a PDF) are rejected as `INVALID_CHARACTERS`, not silently dropped.

## Checks, in order

- [ ] Empty, invalid characters, unknown country, and wrong length each stop validation immediately and are reported alone.
- [ ] Structural check digits (`00`, `01`, `99` on an IBAN), BBAN group structure, and the mod-97 checksum are all evaluated and all reported together.
- [ ] The country table is loaded from the SWIFT IBAN Registry (or a vendored copy with a recorded version), not hard-coded, and there is a scheduled diff against the published registry.
- [ ] Bank and branch identifiers are extracted with the registry's per-country offsets, never as "the first N characters".
- [ ] A BIC is validated for length 8 or 11, alphanumeric content, a real ISO 3166-1 country, and the test-BIC convention (location code ending in `0`) is surfaced as a warning.
- [ ] An IBAN and BIC submitted together are checked for matching country, as a warning, only when both are individually valid.
- [ ] A US routing number is checked for nine digits, a Federal Reserve prefix range, and the 3-7-1 check digit, and then looked up in the Fed's routing directory for the service you intend to use (ACH versus Fedwire).

## Results

- [ ] The validator returns structured reasons (a stable code plus a message), never a bare boolean.
- [ ] Rejections (`ERROR`) and advisories (`WARNING`) are distinguished, and only errors block the submission.
- [ ] The user-facing message names what to fix ("should be 22 characters, you entered 21"), not just "invalid IBAN".
- [ ] Rejections are logged by reason code so that a spike in `WRONG_LENGTH` from one import source is visible.

## What a checksum cannot tell you

- [ ] Account existence and ownership are verified out of band before the first payout: Confirmation of Payee (UK), Verification of Payee (SEPA), micro-deposits or an account-verification provider (US).
- [ ] A passing checksum is never shown to the user as "account verified".
- [ ] Bank reachability (is this BIC reachable for SEPA Instant? is this routing number live for ACH?) is checked against the scheme's directory, not inferred from the identifier's shape.

## Tests the validator must have

- [ ] One registry sample IBAN per supported country validates with zero issues and regenerates its own check digits.
- [ ] Every supported country has a sample, enforced by a test that fails when the table gains a country without one.
- [ ] A single-character mutation and an adjacent transposition of every sample are rejected.
- [ ] The failure-ordering cases: short IBAN reports only `WRONG_LENGTH`; letter in a digit group reports `BBAN_STRUCTURE` and `CHECKSUM_FAILED` together.
- [ ] Routing number cases: a valid check digit, a transposition that the 3-7-1 scheme catches, a prefix outside the Fed ranges, and the one it cannot catch (two digits differing by 5 under weights 3 and 7) documented as a known limit.
