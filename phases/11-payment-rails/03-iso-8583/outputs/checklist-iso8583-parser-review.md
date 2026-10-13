---
name: checklist-iso8583-parser-review
description: Review checklist for any code that builds, parses, forwards, or logs an ISO 8583 card network message
phase: 11
lesson: 3
tags: [iso8583, card-networks, parsing, bitmap, pci]
---

# ISO 8583 Parser Review Checklist

Use this on every pull request that touches an ISO 8583 packager, switch, or gateway, when onboarding a new acquirer or network connection, and again whenever a network publishes a specification update. One unchecked box is a reason to request changes.

## Dialect and dictionary

- [ ] The code names the dialect it implements (network, specification document, version, and date), and the version is pinned in configuration or a constant.
- [ ] Every data element the network can send is in the dictionary with its number, length rule (fixed, LLVAR, LLLVAR), length or maximum, and character class.
- [ ] The dictionary is data (a table, XML, or config), not field reads hard-coded in sequence.
- [ ] Private-use elements (DE48, DE60-63, DE120-127) are defined from the network spec, not guessed as LLLVAR.
- [ ] The wire variant is explicit: ASCII hex bitmap versus 8 raw bytes, ASCII versus BCD numerics, ASCII versus EBCDIC text.

## MTI

- [ ] The MTI is validated as four digits before anything else is read.
- [ ] The version digit matches the dialect (0 for 1987, 1 for 1993); a mismatch is rejected, not ignored.
- [ ] Response MTIs are derived as request function digit plus one, and a response with no outstanding request is flagged.

## Bitmap

- [ ] Presence is decided only by the bitmap; no code assumes a field is present because "it always is".
- [ ] Bit 1 is computed from the field set at encode time, never set by hand.
- [ ] A secondary bitmap is read if and only if bit 1 is set, and a missing one is an error.
- [ ] A tertiary bitmap (bit 65) is rejected unless the dialect defines one.
- [ ] A bit set for an element the dictionary does not define is a named error, not a skip.

## Variable-length elements

- [ ] Length prefixes are checked to be numeric before conversion.
- [ ] The announced length is checked against the dictionary maximum before any bytes are read.
- [ ] Every read is bounds-checked against the remaining message length.
- [ ] After the last present element, the position equals the message length; trailing bytes are an error.

## Values

- [ ] Every value is validated against its dictionary row on set and again on encode.
- [ ] Fixed-length elements are never padded or trimmed silently by the parser.
- [ ] Non-ASCII bytes in an ASCII variant fail at the boundary.

## Amounts

- [ ] DE4 (and DE5, DE6 if used) are parsed as integer minor units and combined with the currency in DE49 (DE50, DE51) into a `Money` value in exactly one place.
- [ ] The exponent comes from the ISO 4217 table, never a default two-decimal rule.
- [ ] No float or double appears anywhere between decode and encode.
- [ ] Negative amounts are rejected by the builder; direction is carried by MTI and processing code.
- [ ] Tests cover a zero-decimal (JPY 392) and a three-decimal (KWD 414) currency.

## Matching and reversals

- [ ] STAN (DE11) and RRN (DE37) are stored for every outbound request.
- [ ] A reversal (0400/0420) copies DE4, DE11, DE37, and the original data elements from the stored request, not from a re-computed amount.
- [ ] Response matching uses MTI, STAN, terminal ID, and transmission time, not STAN alone (STAN wraps at 999999).

## Logging and PCI

- [ ] The PAN is masked to at most the first six and last four digits in every log, dump, trace, and exception message.
- [ ] Track data (DE35, DE45), PIN blocks (DE52), and card security codes never appear in logs, even masked.
- [ ] Exception messages name the element number and the problem, never the raw value.
- [ ] Raw message hex dumps are disabled in production or pass through the same masking.

## Tests

- [ ] Round trip asserts `encode(decode(bytes)) == bytes` byte for byte, for a request and its response.
- [ ] At least one message with a secondary bitmap round-trips.
- [ ] Each malformed case has a test: non-hex bitmap, undefined element, non-numeric prefix, over-maximum prefix, truncated element, trailing bytes, missing secondary bitmap.
- [ ] A captured message from the network's certification test suite decodes and round-trips.

## Rollout

- [ ] The new parser runs in shadow mode against production traffic, comparing decoded fields with the old one, before it is switched on.
- [ ] Format-error declines (response code 30) are alerted on by terminal and by firmware version, so a vendor change is caught in minutes, not hours.
