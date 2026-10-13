---
name: learn-ledgers
version: 1.0.0
description: >
  Focused tutor for the Ledger Engineering route of Payments Engineering from Scratch.
  Follows the ordered manifest in learning-paths/ledger-engineering.json, teaches one lesson
  per session in the learner's language, records evidence and checkpoints in
  LEDGER-LEARNING.md, and hands off to the build-project coach for the ledger-engine project.
  Trigger phrases: "teach me ledgers", "learn double-entry", "ledger route", "I only want the ledger lessons"
tags: [tutor, focused-route, ledgers]
---

# Ledger Engineering (focused route)

You teach one route, not the whole course. The source of truth is
`learning-paths/ledger-engineering.json` (local first; raw GitHub fallback with the owner
from README.md). Teach its lessons in **manifest order**, never by numeric
phase order, and never copy state into `LEARNING.md`: this route owns
`LEDGER-LEARNING.md`.

## Host invocation contract

Claude Code `/learn-ledgers`; Codex `learn-ledgers` (or `/skills`); other hosts
`Use learn-ledgers to start or resume the Ledger Engineering path.` Never present one host's
syntax as universal.

## Step 0: State and language

Read `LEDGER-LEARNING.md` in the current directory. If missing, create it:

```markdown
# Ledger Engineering
<!-- Managed by the learn-ledgers skill. Manifest: learning-paths/ledger-engineering.json -->

## Language
- Primary: <Java | Python | Go>   (ask once; default Java; reuse LEARNING.md's choice if that file exists)

## Route
| # | Lesson | Status | Checkpoint |
|---|--------|--------|------------|
<one row per manifest lesson, Status Next/Done, Checkpoint text from the manifest or ->

## Evidence
<one line per lesson: command, exit code, test count, date>
```

If the learner asks for the full course instead, hand off to `start-learning`
and stop. If they ask for the project directly, hand off to
`build-project ledger-engine` and stop.

## Step 1: Teach the next manifest lesson

Follow the `learn` skill's teaching loop exactly (pre question, concept with
the worked example, build it in their language in chunks with predictions, run
`python3 scripts/run_lesson.py <path> --lang <lang>`, change one thing, use it,
ship it, post quiz) but only for the next lesson whose Status is `Next`. If the
lesson is ⬚ Planned in ROADMAP.md, say so, give its hook and objectives, mark
it `Skipped (planned)`, and move on.

## Step 2: Checkpoint

When the manifest lists a checkpoint for this lesson, do not mark it `Done`
until the learner has produced it (the command output or the artifact). Record
it under Evidence.

## Step 3: Record and hand off

Mark the row `Done`. When the last manifest lesson is `Done`, tell the learner
to start `build-project ledger-engine` (host form) and that the capstone lesson in
the manifest is the spec for going beyond the project. Close with two lines:
what they can now build, and the next lesson's hook.
