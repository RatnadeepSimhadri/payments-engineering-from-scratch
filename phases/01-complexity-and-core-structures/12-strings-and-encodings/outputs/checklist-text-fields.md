---
name: checklist-text-fields
description: Review checklist for any field that carries a human name or free text across a payments boundary (files, wire formats, databases, screening)
phase: 1
lesson: 12
tags: [unicode, encoding, normalization, names, review]
---

# Text Field Review Checklist

Use on any change that stores, formats, compares, or transmits a name, address, descriptor, or free-text field. Each item has caused a returned file, a missed screening match, or a corrupted name in production.

## Define the field

- [ ] The maximum length states its unit: bytes, code points, UTF-16 units, or graphemes. "22 characters" is not a specification.
- [ ] The permitted alphabet is named (printable ASCII, SWIFT X set, Latin-1, Unicode) and the source document for it is cited.
- [ ] The behaviour for input that does not fit is stated: truncate at a character boundary, reject with a specific error, or substitute. It is deterministic and logged.
- [ ] The behaviour for input outside the alphabet is stated: transliterate by a versioned table, reject, or substitute. The original value is kept.

## Store

- [ ] Text is normalised to NFC once, at ingestion, after the full value is assembled. NFKC is never the stored form.
- [ ] A separate matching key column (`casefold(NFKC(value))`, with whitespace collapsed and, if required, accents stripped or confusables skeletonised) is stored and indexed. Unique constraints and lookups use it, not the raw column.
- [ ] Database column types and lengths match the field definition's unit (`VARCHAR(n)` is characters in PostgreSQL, bytes in some MySQL collations, UTF-16 units in SQL Server `NVARCHAR`). MySQL uses `utf8mb4`, never `utf8`.
- [ ] Identifiers that arrive from clients (idempotency keys, external references) are stored as received, byte for byte, with no normalization.

## Encode and decode

- [ ] Every `new String(byte[])`, `getBytes()`, reader, writer, and stream constructor names its charset explicitly.
- [ ] Decoding untrusted input uses a strict decoder (`CodingErrorAction.REPORT`); lenient substitution with U+FFFD is used only for display and logging.
- [ ] JSON is produced and consumed as UTF-8; escapes of code points above U+FFFF are surrogate pairs; a lone surrogate is rejected.
- [ ] Files state their encoding by format: ASCII for NACHA, UTF-8 with declaration for ISO 20022 XML, the agreed code page for EBCDIC hosts. A byte order mark is not emitted unless the consumer requires it.
- [ ] Code page tables for EBCDIC interfaces are versioned and tested on the characters where the variants disagree (`[`, `]`, `^`, `|`, `!`).

## Truncate and pad

- [ ] Truncation by bytes backs up over continuation bytes (`(b & 0xC0) == 0x80`) and never emits a partial sequence.
- [ ] Truncation by UTF-16 units never splits a surrogate pair.
- [ ] Padding is applied after truncation, in the target encoding, with the format's pad character (space for NACHA and ISO 8583 `ans` fields).
- [ ] A truncated value is flagged in the record or the log so reconciliation can explain a name mismatch.

## Compare and sort

- [ ] Equality for matching uses the matching key, never `equals` on raw strings.
- [ ] Case operations pass `Locale.ROOT` for machine comparison; user-facing case and sort use the user's locale via `Collator`.
- [ ] Screening and deduplication pipelines apply steps in this order: trim and collapse whitespace, NFKC, case fold, strip punctuation, then (only for screening) strip accents and apply the UTS #39 skeleton.
- [ ] Each use case (display, dedupe, screening, idempotency) has its own named normalization function; none of them share a function that silently strips accents.

## Tests

- [ ] Fixtures include `Müller`, `Nguyễn` in NFC and NFD, `Łukasz`, a joined emoji sequence, Turkish `İ`/`ı`, fullwidth digits, a right-to-left override, 50 stacked combining marks, the empty string, and a value of exactly the maximum length in the field's unit.
- [ ] For each fixture the test asserts the byte length, code-point count, and UTF-16 length, and that truncated output decodes strictly.
- [ ] Round trips through every boundary (JSON, database, file, queue) return the NFC original byte for byte.
- [ ] NFC and NFD spellings of the same name produce an identical matching key and a single row under the unique constraint.
