---
name: checklist-screening-matcher
description: Review checklist for a sanctions-screening or multi-pattern matching service, from algorithm choice to what the matcher cannot decide
phase: 2
lesson: 12
tags: [strings, screening, sanctions, aho-corasick, review]
---

# Screening Matcher Review Checklist

For any service that scans free text (names, remittance fields, messages, logs) against a list of patterns. Each unchecked box is a question for the author.

## Algorithm fits the shape

- [ ] One pattern with a hard latency bound: KMP (or Boyer-Moore for natural text). Many patterns: Aho-Corasick. Many equal-length patterns or 2-D data: Rabin-Karp with verification. The choice is written down with the pattern count and text length that justified it.
- [ ] No `contains` or regex-alternation loop over the pattern list; per-text cost is independent of list size.
- [ ] Regex, if used at all, runs on an automaton engine (RE2, Hyperscan) or is bounded; a backtracking engine never sees untrusted input.
- [ ] Any hash-based match is verified character by character before it is reported.

## Normalization is explicit and tested

- [ ] Case folding uses a fixed locale (`Locale.ROOT`), never the JVM default; the Turkish dotless-i case is in the tests.
- [ ] Punctuation, whitespace runs, and common company suffixes (LLC, LTD, GMBH, SA) are handled by a documented rule, and the list is normalized with the same function as the input.
- [ ] Unicode: accents are decomposed and stripped (`Normalizer.Form.NFD`) or deliberately kept, full-width and compatibility characters are folded, and the decision is recorded.
- [ ] Transliteration variants for the scripts in your list (Cyrillic, Arabic, Chinese) are either expanded into the pattern set or handled by the fuzzy layer, and someone has checked which.

## Match policy is a decision, not an accident

- [ ] Substring versus whole-token matching is a stated policy, with the `RASHID` in `RASHIDA` and the `AL RASHID` versus `ALRASHID` cases both in the tests.
- [ ] When several patterns end at the same position, the service reports the longest, or all, by rule; analysts are not flooded with every sub-name.
- [ ] Output is capped per field (dense pattern sets can produce `O(n * patterns)` matches) and a cap hit is itself logged.
- [ ] Every reported match carries the pattern, the list entry id, the field, and the offset, so an analyst can see what matched where.

## Worst cases and inputs

- [ ] Pathological inputs (long runs of one character, 10 KB remittance fields, empty fields, fields that are entirely separators) are in the test suite and run within the latency budget.
- [ ] The empty pattern is rejected at list-load time; a list entry that normalizes to empty is reported, not silently dropped.
- [ ] List reloads build a new automaton and swap it atomically; the service never screens against a half-built structure.
- [ ] Automaton size (nodes, memory) is logged after each build and alarmed on a step change.

## What the matcher cannot decide

- [ ] Fuzzy matching (edit distance, Jaro-Winkler, phonetic keys) is a separate, downstream layer with its own thresholds and tests; the exact matcher is the candidate generator, not the decision.
- [ ] Identifiers on the list entry (date of birth, nationality, passport number, vessel IMO) are compared by a separate rule set to confirm or discount a name hit.
- [ ] Every hit goes to a human review queue with an SLA; the release decision is recorded with the reviewer and the reason.
- [ ] False-positive rate and true-positive rate are measured against a labelled sample monthly, and threshold changes are reviewed by compliance, not shipped as code tweaks.
