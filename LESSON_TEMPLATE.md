# Lesson Template

Copy this structure for a new lesson, or run `python3 scripts/scaffold_lesson.py <phase> <lesson>` to generate it from `curriculum/curriculum.json`. The reference lesson that every other lesson is measured against is [`phases/10-money-and-ledgers/01-representing-money/`](phases/10-money-and-ledgers/01-representing-money/): read it first.

## Folder structure

```text
MM-lesson-slug/
├── docs/
│   └── en.md                 # the lesson
├── quiz.json                 # 6 questions: 1 pre, 3 check, 2 post
├── code/
│   ├── java/
│   │   ├── Main.java         # implementation + worked-example main(); helper types in extra files, no packages
│   │   └── MainTest.java     # self-contained assertion harness; exits 1 on any failure
│   ├── python/
│   │   ├── main.py           # same design, same names (snake_case)
│   │   └── test_main.py      # unittest
│   └── go/
│       ├── go.mod            # module pefs/<phase>/<lesson>
│       ├── main.go           # same design, same names (CamelCase); errors as values
│       └── main_test.go      # testing package
└── outputs/
    └── <kind>-<slug>.md      # optional: checklist, design-doc, runbook, interview-card
```

Learn lessons omit `code/` and ship only `docs/en.md`, `quiz.json`, and optional `outputs/`.

## docs/en.md

```markdown
# [Lesson Title]

> [One-line hook: the sentence a learner should remember a year later]

**Type:** Build | Learn
**Languages:** Java, Python, Go      (or — for Learn)
**Prerequisites:** [upstream lessons by phase and number, or None]
**Time:** ~[minutes] minutes

## Learning Objectives

- [4-6 bullets starting with a verb; each one is tested by the quiz or the code]

## The Problem

[2-4 paragraphs. A concrete failure with a named system, a specific input, and what breaks.
Prefer a payments scenario when it is natural. End with why the mechanism in this lesson is the fix.]

## The Concept

[The longest prose section. The mental model, the invariant, the complexity, the trade-offs.
One worked example by hand with real numbers. A mermaid diagram or comparison table.
A subsection on what the structure promises and what it does NOT do.]

## Build It

[Primary walkthrough in Java (or the language most natural to the topic), in chunks of 5-25 lines.
For each chunk: what it does, why it exists, one "Predict:" question.
Every chunk corresponds to code that exists in code/.]

### Step 1: [Name]
### Step 2: [Name]
### Step N: Run it
[The exact commands and the expected output, abbreviated.]

### In Python
[Only what differs: idioms, numeric model, concurrency model. Point to the full file.]

### In Go
[Only what differs. Point to the full file.]

## Use It

[The standard-library or production equivalent: what it does for you that the scratch version made
explicit, and what trap it has. Name real systems (java.util.concurrent, PostgreSQL, Kafka, Stripe).]

## Ship It

[The artifact in outputs/ and how to use it at work.]

## Exercises

1. **Reinforce.** [...]
2. **Apply.** [...]
3. **Extend.** [... combining a previous lesson]

## Key Terms

| Term | What people say | What it actually means |
|------|-----------------|------------------------|

## Further Reading

- [Primary source](url) — why it is worth reading
```

Target: 350-700 lines for Build, 250-450 for Learn.

## Code rules

- Header comment (4-6 lines) citing `docs/en.md`, the run and test commands, and any spec or paper.
- The three implementations share one design: same type names (modulo casing), same method names, same test cases.
- Inject time, randomness, and I/O. `main` runs in under ten seconds, touches no network, and exits 0.
- 8+ assertions per language: happy path, the invariant, a boundary, and a failure the lesson warns about.
- Money is always integer minor units with a currency. Reuse the Phase 10 Lesson 01 design by copying the type into the lesson (lessons are self-contained; there is no shared library).

## quiz.json

See `AGENTS.md`. Six questions, zero-indexed `correct`, spread across positions, distractors of comparable length, explanations that say why the best distractor is wrong.

## outputs/

```markdown
---
name: checklist-<slug>
description: One line on what it is for
phase: NN
lesson: MM
tags: [a, b]
---
```
