---
name: checklist-pan-handling
description: Review checklist for any code path that accepts, validates, routes, stores, or displays a card number
phase: 11
lesson: 4
tags: [cards, pan, luhn, bin, pci-dss, review]
---

# PAN Handling Review Checklist

Use on any pull request that touches a card number. Each unchecked box is a reason to request changes, and several are a reason to involve whoever owns PCI scope.

## Input

- [ ] The raw input is normalized once (spaces and hyphens removed) and anything else is rejected, not stripped.
- [ ] The PAN is held as a digit string, never parsed to a numeric type except for the fixed-width BIN key.
- [ ] Length is checked against 12-19 (ISO/IEC 7812) before Luhn, so error messages distinguish "too short" from "typo".
- [ ] Luhn runs at the edge (browser or API gateway) and a failure never reaches the acquirer.

## Routing

- [ ] Network detection uses a range table, not `startsWith` chains; the Mastercard 2221-2720 range and Discover 644-649 are present.
- [ ] Per-network length rules are applied as a separate check from prefix detection.
- [ ] The issuer BIN table supports 8-digit BINs and has a documented refresh cadence and source; the last refresh date is visible in monitoring.
- [ ] Overlapping ranges are rejected at load time, and the load fails loudly rather than silently keeping the old table.
- [ ] An unknown BIN is a routable default, not an error; an unknown network is a reject.

## Display and logging

- [ ] The PAN type's `toString` / `__str__` / `String()` returns the masked form; the full digits are behind one explicitly named accessor.
- [ ] `grep` for that accessor in the diff: every call site is in a path that needs the full PAN (vault, HSM, authorization message builder).
- [ ] Masks show at most 8 leading digits plus the last 4 and never fewer than one hidden digit.
- [ ] Exception messages, audit events, metrics labels, and trace attributes carry the masked form or a token, never the PAN.

## Storage

- [ ] The full PAN is stored only in the tokenization vault; everything else holds a token plus BIN and last four.
- [ ] Test suites use only published test PANs (4111111111111111, 4242424242424242, 5555555555554444, 2223003122003222, 378282246310005, 6011111111111117); a CI check fails on any other Luhn-valid 15-19 digit number in fixtures.
- [ ] Log retention and search tooling have been checked for historical leaks after any change to logging in this path.

## Questions to ask in review

1. If this code threw an exception with the input in the message, what would appear in the log?
2. When the network table changes next, how many files change?
3. Which test asserts that the full PAN does not appear in the output of this function?
