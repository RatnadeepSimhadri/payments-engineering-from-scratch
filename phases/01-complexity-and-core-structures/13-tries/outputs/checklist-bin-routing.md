---
name: checklist-bin-routing
description: Review checklist for any component that routes, prices, or classifies transactions by card-number prefix (BIN)
phase: 1
lesson: 13
tags: [trie, bin, routing, pci, review]
---

# BIN Routing Review Checklist

Use when reviewing a routing service, a fee or interchange calculator, a fraud rule keyed on BIN, or any table that maps card-number prefixes to anything. One unchecked box is a question for the author.

## Input handling

- [ ] The PAN is normalised (spaces, dashes removed) *before* lookup, and the lookup itself rejects non-digits rather than stripping them.
- [ ] Length is validated against ISO/IEC 7812 (up to 19 digits; practical minimum 12) and the Luhn check is applied before routing is attempted.
- [ ] Non-ASCII digits are rejected (`isdigit()` and `Character.isDigit` both accept them).

## Table semantics

- [ ] Rules are stored at their natural length (1 to 8+ digits), not padded to a fixed six or eight digits.
- [ ] Ranges (`2221`-`2720`) are decomposed into exact prefix blocks, and a test proves every number in the range is covered exactly once and every neighbour is not.
- [ ] The lookup is longest-prefix match in one pass; there is no "retry with a shorter prefix" loop and no ordering assumption about which table is consulted first.
- [ ] A query with no matching network is an error that stops processing; a query with no matching issuer routes with an explicit `UNKNOWN`, and the two cases are logged differently.
- [ ] The empty prefix (default route), if used at all, is a deliberate, documented entry.

## Updates and deletions

- [ ] Removing a rule removes exactly the prefixes that were added for it and cannot remove nodes shared with another rule; there is a test for two adjacent ranges where one is removed.
- [ ] Table reloads are atomic from the reader's point of view (build a new trie, swap a pointer; or lock) so a lookup never sees a half-loaded table.
- [ ] The source and version of the BIN table are recorded with every routing decision that is persisted, so a misroute can be traced to the table that caused it.
- [ ] Eight-digit BINs (mandatory since April 2022) are handled; nothing in the code assumes "the first six digits".

## Boundaries that have broken real systems

- [ ] Mastercard 2-series (`2221`-`2720`) routes to Mastercard; `2220...` and `2721...` do not.
- [ ] Discover's `644`-`649` and `65` ranges and the `6011` prefix are all present and distinct from each other.
- [ ] Amex `34` and `37` PANs are 15 digits and are not rejected by a 16-digit assumption.
- [ ] A test exists for a PAN one digit either side of every range boundary in the table.

## PAN handling (PCI DSS requirement 3)

- [ ] The full PAN never appears in a log line, exception message, metric label, or trace span; at most the first six and last four digits are shown.
- [ ] The routing component does not persist the PAN; it persists the matched prefix, the decision, and a token or reference.
- [ ] Test fixtures use the published sandbox PANs (`4111111111111111`, `5555555555554444`, `378282246310005`, `6011111111111117`, `3530111333300000`), never real card numbers.

## Performance

- [ ] Lookup cost is bounded by PAN length, not table size; a benchmark (Phase 0, Lesson 05) with the full production table confirms the p99.
- [ ] Memory for the full table is measured; if a plain trie is too large, a radix tree or sorted range array is used and the choice is recorded.
