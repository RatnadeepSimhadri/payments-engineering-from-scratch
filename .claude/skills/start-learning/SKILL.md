---
name: start-learning
version: 1.0.0
description: >
  One-time onboarding for the Payments Engineering from Scratch curriculum
  (data structures and algorithms, distributed systems, payments engineering;
  16 phases). Interviews the learner, records their implementation language
  (Java now; Python and Go when their ports land), runs the placement quiz,
  and writes LEARNING.md, the persistent study plan the learn skill drives.
  Trigger phrases: "start learning", "set up the course", "begin the
  curriculum", "onboard me", "create my learning plan", "start the payments course"
tags: [onboarding, curriculum, payments, distributed-systems, dsa, learning-plan]
---

# Start Learning

You are onboarding a learner into **Payments Engineering from Scratch**: three
tracks (A: Data Structures & Algorithms, Phases 0–4; B: Distributed Systems,
Phases 5–9; C: Payments Engineering, Phases 10–15). Your job is to produce
`LEARNING.md`, one file in the current directory that captures why they are
learning, **which language they will build in**, where they should start, and
what their path looks like. Every later `learn` session reads and updates this
file, so treat it as the learner's source of truth.

Works with any agent. If your environment has a structured question/option
tool, use it for every question; otherwise present lettered options as plain
text and wait for the reply.

## Host invocation contract

Skill names are portable; invocation syntax belongs to the host. Before showing
a next command, use the correct form:

- Claude Code: `/start-learning`, `/learn`, `/course-guide`, `/check-understanding 10`, `/build-project ledger-engine`.
- Codex: `start-learning`, `learn`, `course-guide`, or choose the skill from `/skills`.
- Other compatible hosts: natural language such as `Use learn to start my first lesson.`

Never present a slash command as universal syntax. When the host is unknown,
use the natural-language form.

## Content sources

Prefer local files when the repository is cloned (a `curriculum/curriculum.json`
exists in or above the current directory). Otherwise fetch from
`https://raw.githubusercontent.com/RatnadeepSimhadri/payments-engineering-from-scratch/main/<path>`.
The repository is private: the raw fallback works only for collaborators with a token, so prefer a local clone.

- Phase and lesson list, hooks, minutes: `curriculum/curriculum.json`
- Which lessons are complete and in which languages: `ROADMAP.md` (✅ rows) or `catalog.json` if present
- Implementation languages and their status: `languages.json` (`ci: true` means every complete Build lesson has that language today)

## Resume routing

Before onboarding, resolve any "resume" or "continue" request against these
state files and their owners:

- `LEARNING.md` → `learn` (full curriculum)
- `PROJECTS-LEARNING.md` → `build-project`
- `LEDGER-LEARNING.md` → `learn-ledgers`
- `CONSENSUS-LEARNING.md` → `learn-consensus`
- `INTERVIEW-PREP.md` → `interview-prep`

If the learner names a route, dispatch to its owner and stop. If exactly one
state file exists, resume that owner. If several exist, list them and ask which
to resume. If none exist, continue below. Never merge one route's progress into
another file.

If `LEARNING.md` already exists, do not overwrite it. Summarize it (mission,
language, entry point, progress) and offer exactly three paths:

- **Resume**: invoke `learn`; skip the interview and placement.
- **Re-run placement**: administer the quiz again, update only the Placement section and Path statuses.
- **Start over**: only after explicit confirmation, rename the file to `LEARNING-<YYYY-MM-DD>.md` and proceed. Never delete history silently.

## Step 1: The interview (4 questions, keep it short)

1. **Why are you learning this?** Free text. Offer examples: joining a payments
   team, preparing for system-design interviews, moving from application code
   to infrastructure, building a fintech product, filling DSA gaps. Capture the
   answer in their own words; it grounds every later explanation.
2. **Which language do you want to build in?** Read `languages.json`. Present
   every registered language, marking the ones with `ci: true` as "available
   for every complete lesson today" and the others as "ports in progress;
   lessons that already have it will use it, the rest fall back to Java with
   porting notes". Record the choice as `Language:` in LEARNING.md. The learner
   can change it later with `learn` ("switch me to Go"); `learn` re-teaches the
   current lesson's Build It in the new language.
3. **How much time per week?** Options: ~2 h, ~5 h, ~10 h, "as fast as
   possible". Used only to phrase the pace honestly, never to cut content.
4. **What do you most want to be able to build or explain by the end?** One
   line. A ledger, a payment gateway, Raft, "pass the Stripe/PayPal/Adyen
   system-design loop", "not sure yet" are all fine.

Do not ask more than these four. Placement measures knowledge; the interview
captures intent.

## Step 2: Placement

Run the placement quiz from the `find-your-level` skill (it installs alongside
this one): 5 areas (DSA, systems data structures & concurrency, distributed
systems, money & ledgers, payment systems), 2 questions each, mapped to an
entry phase. Preserve that skill's answer-isolation contract: do not read later
answer-key rounds early, and never put a real answer letter in a reply-format
hint.

If the learner already knows where to start ("start me at Phase 10"), respect
it and skip the quiz, with the same output contract so `learn` always finds a
well-formed plan:

- Validate the phase is 0–15 and resolve its canonical name from `curriculum.json`; otherwise list the 16 phases and ask.
- Path table: phases below the entry point are `Skip`, the entry point and above are `Do`; Est. hours is the sum of `Do` rows.
- Placement section: `Score: self-selected`.

Tracks are partially independent. If the learner's goal is payments and their
DSA/distributed scores are strong, the entry point can be Phase 10 with Phases
5 and 7 marked `Review`. If their goal is interviews, add `Review` on Phase 14.

## Step 3: Write LEARNING.md

Create `LEARNING.md` in the current directory with exactly these sections:

```markdown
# My Payments Engineering Path
<!-- Managed by the payments-engineering-from-scratch learning skills. -->

## Mission
<answer to question 1, in their words, plus the build goal from question 4>

## Language
- Primary: <Java | Python | Go>
- Fallback: Java (used when a lesson has no <Primary> implementation yet)
- Changed on: <YYYY-MM-DD> (append a line each time it changes)

## Placement
- Date: <YYYY-MM-DD>
- Score: <total>/10 with the area breakdown, or exactly `self-selected`
- Entry point: Phase <N>: <name>
- Pace: ~<hours>/week

## Path
| Phase | Name | Status | Est. hours |
|-------|------|--------|------------|
<all 16 phases; Status is Skip, Review, Do, or Done. Hours = sum of lesson
minutes in curriculum.json for that phase, divided by 60, rounded>

## Progress log
| Date | Lesson | Language | Quiz | Note |
|------|--------|----------|------|------|

## Review queue
<empty for now; learn adds lessons the quizzes flag>

## Evidence
<empty for now; learn appends one line per lesson: the command run, exit code,
and test count, so the learner has a record they can show>
```

## Step 4: Hand off

Close with four lines, nothing more:

- Their entry point, language, and total estimated hours for Review + Do phases.
- The host-correct invocation for `learn`, and that it starts the first lesson and picks up from this file every time.
- The host-correct invocation for `course-guide <topic>` to jump to a topic instead.
- The host-correct invocation for `build-project` and the project that matches their goal (ledger → `ledger-engine`, gateway → `idempotent-payment-gateway`, consensus → `raft-kv`).
