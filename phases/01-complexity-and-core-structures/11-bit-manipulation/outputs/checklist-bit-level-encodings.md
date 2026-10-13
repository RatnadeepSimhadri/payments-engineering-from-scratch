---
name: checklist-bit-level-encodings
description: Review checklist for code that packs fields into integers, parses binary wire formats, or defines an id layout
phase: 1
lesson: 11
tags: [bits, encoding, iso8583, snowflake, review]
---

# Bit-Level Encoding Review Checklist

Use on any pull request that shifts, masks, packs, or parses bits: flag words, bitmaps, wire-format headers, id generators, hash mixing. Each box is a bug someone has shipped.

## Layout

- [ ] The layout is written down as a table (field, bit range, width, meaning, valid range) in a comment or doc next to the code, with the numbering direction stated: "bit 0 is least significant" or "bit 1 is the most significant bit of the first byte".
- [ ] Widths add up to the word size, or the unused bits are named and asserted zero.
- [ ] The sign bit of a signed id is reserved and always zero, so the value is positive in every language and database that will see it.
- [ ] For a time field: the epoch is a named constant in code (not configuration), the window is computed (`2^bits * tick`), and the exhaustion date is in the doc.

## Writing fields

- [ ] Every field insertion range-checks the value against its width before shifting; a value that does not fit throws rather than overflowing into a neighbour.
- [ ] Insertion clears the target bits before ORing (`(v & ~(mask << shift)) | (field << shift)`), so re-writing a field does not leave stale ones behind.
- [ ] Shift counts are constants or validated: a shift of 32 on an int or 64 on a long is a shift of zero in Java.
- [ ] `mask(64)` and `mask(32)` are special-cased or `-1` is used; `(1L << 64) - 1` is zero.

## Reading fields

- [ ] Extraction uses the logical shift (`>>>` in Java, `uint64` in Go) whenever the word may have its top bit set.
- [ ] Bytes are masked with `& 0xFF` before widening; `(byte) 0x80` is -128.
- [ ] Hex parsing of a full-width word uses an unsigned parser (`Long.parseUnsignedLong`); `Long.parseLong("F000000000000000", 16)` throws.
- [ ] Fixed-width rendering pads with leading zeros; `Long.toHexString(1)` is `"1"`, not `"0000000000000001"`.

## Flags and bitmaps

- [ ] "Has all of these flags" is `(bits & flags) == flags`; "has any" is `(bits & flags) != 0`; the method names say which.
- [ ] Unknown bits read from storage are rejected or logged, never silently ignored.
- [ ] A bitmap's capacity is bounded and the id space is dense; sparse or 64-bit ids use a hash set or a compressed bitmap (Roaring) over a surrogate key.
- [ ] Word index and bit offset are `i >>> 6` and `i & 63` (or the 32-bit equivalents) and the allocation is `(capacity + 63) >>> 6` words.

## Wire formats (ISO 8583 and friends)

- [ ] Bit numbering matches the specification's direction, verified against at least one published test vector, not only against your own encoder.
- [ ] Structural bits (ISO 8583 field 1 = secondary bitmap present) are derived by the encoder and cross-checked by the decoder against message length.
- [ ] The parser rejects inconsistent lengths instead of reading the next field as the missing one.
- [ ] Certification tests cover a message that uses fields in every byte of the bitmap, including the secondary bitmap, not one convenient message shape.

## Id generators

- [ ] Clock moving backwards has an explicit policy (refuse, wait, or borrow) and a test.
- [ ] Sequence exhaustion within one tick has an explicit policy (wait for the next tick) and a test with an injected clock.
- [ ] Worker ids are allocated so two processes cannot share one (lease, coordination service, or derived from a unique host attribute) and the range check on the worker field is tested.
- [ ] The generator is either single-threaded per instance or explicitly synchronised; the doc says which.
- [ ] Ids are not treated as secrets or capabilities; they leak creation time and fleet size.

## Arithmetic traps

- [ ] No `Math.abs` on a value that may be `MIN_VALUE` (use `Math.absExact` or mask).
- [ ] Midpoints are `lo + ((hi - lo) >>> 1)`, never `(lo + hi) / 2`.
- [ ] Odd/even and modulo on possibly negative values use `& 1` and `Math.floorMod`, not `% 2`.
- [ ] Shifts are not used as division on signed values unless flooring is the intended semantics.

## Tests

- [ ] Hand-rolled bit tricks are checked against a library oracle (`Long.bitCount`, `java.util.BitSet`, a reference encoder) on thousands of random inputs plus 0, -1, `MIN_VALUE`, `MAX_VALUE`.
- [ ] Round-trips: `decode(encode(x)) == x` for random x in every field's full range, including maximum values.
- [ ] A test exists for each rejected input: value too wide, shift out of range, inconsistent length, clock backwards.
