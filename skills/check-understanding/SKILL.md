---
name: check-understanding
version: 1.0.0
description: >
  Phase quiz for Payments Engineering from Scratch: eight questions generated
  from the phase's lesson docs, with feedback and lessons to review. Trigger
  with "quiz me on phase 10", "test phase 5", "check my understanding of
  ledgers", "am I ready for the next phase", or `/check-understanding <phase>`.
tags: [assessment, quiz, curriculum, payments]
---

# Check Understanding

Test the learner's knowledge of one phase of **Payments Engineering from
Scratch**. Works with any agent.

## Input

A phase number (0–15) or a phase name/keyword. If missing, list the 16 phases
from `curriculum/curriculum.json` (number and title) and ask.

## Phase map

Resolve the argument against `curriculum/curriculum.json` (`phases[].number`,
`phases[].slug`, `phases[].title`). Accept these keyword aliases too:

| Keywords | Phase |
|---|---|
| setup, tooling | 0 |
| dsa, structures, complexity, big-o | 1 |
| algorithms, sorting, graphs, dp | 2 |
| systems structures, bloom, caches, rate limiting | 3 |
| concurrency, threads, locks | 4 |
| distributed, clocks, idempotency, retries | 5 |
| replication, consensus, raft, paxos | 6 |
| storage, transactions, wal, sagas, outbox | 7 |
| messaging, streams, kafka, event sourcing | 8 |
| reliability, slo, observability, scale | 9 |
| money, ledgers, reconciliation | 10 |
| rails, cards, iso 8583, ach, open banking | 11 |
| payment design, state machine, webhooks | 12 |
| risk, fraud, compliance, aml, pci | 13 |
| scale, exactly-once, simulation, interviews | 14 |
| capstone | 15 |

Out of range or unknown: say so and show the list.

## Procedure

### Step 1: Read the phase

If the repo is cloned, read every `docs/en.md` under `phases/<phase-dir>/` that
is not a Planned outline (`**Status:** Planned`). Otherwise fetch them from the
raw GitHub base (owner from README.md). If a phase has many complete lessons,
read a representative spread: first, middle, last. If the phase has no complete
lessons, say "Phase N has no completed lesson content yet. Pick another phase."
and stop.

Also read `LEARNING.md` if present, for the learner's language; practical
questions should use that language's idioms (e.g. `Math.addExact` for Java,
`(value, error)` returns for Go).

### Step 2: Generate 8 questions

Exactly 8 multiple-choice questions grounded in the docs you read (not general
knowledge), 4 options each, exactly one correct, distractors plausible and of
comparable length. Tag each with its source lesson.

- **Q1–4 conceptual** (what/why): invariants, trade-offs, definitions that
  distinguish neighbours ("why HALF_EVEN rather than HALF_UP for fees").
- **Q5–8 practical** (how/build/debug): "what does this code do", "which step is
  missing", "you observe X in production; which mechanism failed". At least one
  should present a short code fragment or a trace with real numbers from the
  lesson's worked example.

### Step 3: Present one at a time

Use a structured question tool if available; otherwise:

```text
Question 1/8 (Conceptual) — from Lesson 03: The Double-Entry Ledger

Why must a journal entry have at least two postings?

A) ...
B) ...
C) ...
D) ...
```

Wait for the answer. Keep the correct option and explanation private until
then. For a plain-text hint use exactly `Reply with one letter: <A|B|C|D>.`
Never put a real answer letter or the answer distribution in a hint.

### Step 4: Score and grade

| Correct | Grade | Message |
|---|---|---|
| 7–8 | Mastered | "Strong grasp of Phase N. Ready for Phase N+1: <title>." (Phase 15: congratulate; full-course completion is claimed only if LEARNING.md shows Phases 0–14 Done or Skip.) |
| 5–6 | Almost | "Solid foundation. Review these before moving on:" + lessons of missed questions |
| 3–4 | Developing | "Revisit these lessons:" + each missed question's lesson |
| 0–2 | Start over | "Work through the phase again, focusing on:" + all missed topics |

### Step 5: Wrong-answer breakdown

For each miss:

```text
Question N: <abbreviated question>
Your answer: B
Correct answer: C — <option text>
Why: <1–2 sentences grounded in the lesson>
Review: Lesson NN — <title> (phases/<phase-dir>/<lesson-dir>/docs/en.md)
```

### Step 6: Record and offer next

If `LEARNING.md` exists, append a Progress log row: date, `phase-N quiz`,
language, `N/8`, note with the missed lessons; add misses to the Review queue.
Then offer: retake with fresh questions, try another phase, or explain a missed
topic. Wait for the choice.

## Rules

- Questions come from the docs, never from general knowledge.
- No repeated questions on a retake until the pool is exhausted.
- Wrong options must be plausible; no joke answers.
- Never reveal an answer before the learner responds.
