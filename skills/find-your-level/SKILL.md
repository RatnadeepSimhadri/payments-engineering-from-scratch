---
name: find-your-level
version: 1.0.0
description: >
  Ten-question placement quiz for Payments Engineering from Scratch. Maps the
  learner's knowledge across DSA, systems structures and concurrency,
  distributed systems, money and ledgers, and payment systems to a starting
  phase and a personalized path. Trigger phrases: "where should I start",
  "find my level", "placement test", "which phase", "assess my knowledge", "skip ahead"
tags: [assessment, onboarding, curriculum, payments]
---

# Find Your Level

You are administering a placement quiz for **Payments Engineering from Scratch**
(16 phases in three tracks). Figure out where the learner should begin so they
skip what they know and land where the challenge starts. Works with any agent.

## Quiz structure

Five knowledge areas, two questions each, ten questions total. Present them in
rounds of two (one round per area). Score each round before moving on. Each
question is 1 point; each area 0–2; total 0–10.

Greet briefly, then start Round 1. If your environment has a structured
question/option tool, use it for every question; otherwise present lettered
options as plain text and wait. After each round, report the area score
("Distributed systems: 1/2"). Keep commentary short. Explain nothing until the
end.

### Answer isolation

The answer key is in `references/answer-key.md`, outside this file. Do not read
it before the learner submits both answers for the current round. Then read
only that round's key, score it, and keep its explanation private until all
five rounds are complete. Do not preload later rounds.

Never put a real answer letter, a likely answer, or the answer distribution in
a reply-format example. Use exactly: `Reply with Q1: <letter>, Q2: <letter>.`
(substitute the question numbers, keep `<letter>`).

---

### Round 1 — Data structures and algorithms

**Q1.** A hash map with separate chaining holds 1,000,000 keys in 500,000
buckets with a good hash. What is the expected number of key comparisons for a
successful lookup?

- A) About 1,000,000
- B) About 20 (log2 of the key count)
- C) About 2 (one plus half the load factor)
- D) About 500,000

**Q2.** You need the earliest-due item among a million scheduled retries,
with new retries arriving constantly. Which structure gives O(log n) insert and
O(log n) remove-minimum?

- A) A sorted array
- B) A binary min-heap
- C) A hash map keyed by due time
- D) A singly linked list kept sorted

---

### Round 2 — Systems data structures and concurrency

**Q3.** A Bloom filter says a key is "possibly present". What is guaranteed?

- A) The key was inserted
- B) The key was not inserted
- C) Nothing; a "possibly present" can be a false positive, but "definitely absent" is always correct
- D) The key was inserted at least twice

**Q4.** Two threads each run `balance = balance + 100` once on a shared field
starting at 0, with no synchronization. Which final values are possible?

- A) Only 200
- B) 100 or 200
- C) Only 100
- D) 0, 100, or 200

---

### Round 3 — Distributed systems

**Q5.** Two servers record events with their local wall clocks. Server A logs
an event at 12:00:00.100 and server B logs one at 12:00:00.090. What can you
conclude about which event happened first?

- A) B's event happened first because its timestamp is earlier
- B) A's event happened first because A is the primary
- C) Nothing, unless you know the clock skew between A and B
- D) They happened simultaneously within rounding

**Q6.** A client sends "charge $50" and the connection drops before any
response arrives. The safest client behaviour is to:

- A) Retry with the same idempotency key so the server can deduplicate
- B) Never retry, since the charge may have succeeded
- C) Retry with a new request id so the server treats it as fresh
- D) Charge $50 locally and reconcile at month end

---

### Round 4 — Money and ledgers

**Q7.** A payment system stores amounts as `double`. Which statement is true?

- A) Rounding to two decimals on display makes the stored values exact
- B) Sums of many amounts can drift because most decimal fractions have no exact binary representation
- C) Doubles are exact up to 15 significant digits, which covers all currency amounts
- D) The errors cancel out over enough transactions

**Q8.** In a double-entry ledger, a customer pays a merchant $100 and the
platform keeps a $3 fee. How many postings does a correct journal entry have,
at minimum, and what must they satisfy?

- A) One posting of $100; the fee is recorded separately later
- B) Two postings; debits equal credits
- C) Three postings (customer −100, merchant +97, platform fee +3); debits equal credits
- D) Four postings, one per party plus one for the network

---

### Round 5 — Payment systems

**Q9.** A card authorization succeeded for $80 and the merchant later captures
$50. What is the state of the remaining $30?

- A) It was charged to the cardholder and must be refunded
- B) It remains a hold that is released or expires according to network rules
- C) It is automatically captured at end of day
- D) It becomes a chargeback

**Q10.** A webhook consumer receives the same `payment.succeeded` event twice,
with the same event id. The correct behaviour is to:

- A) Process it twice; the sender is responsible for not duplicating
- B) Reject the second delivery with an error so the sender stops retrying
- C) Treat the second delivery as a duplicate by event id and acknowledge it without re-applying the effect
- D) Apply the effect only if the second delivery arrives more than a minute later

---

## After all five rounds

```text
Data structures & algorithms:        X/2
Systems structures & concurrency:    X/2
Distributed systems:                 X/2
Money & ledgers:                     X/2
Payment systems:                     X/2
--------------------------------------------
Total:                               X/10
```

## Score-to-entry-point mapping

| Total | Entry point | What it means |
|-------|-------------|---------------|
| 0–3 | Phase 1: Complexity & Core Data Structures | Build the foundation (Phase 0 for setup first) |
| 4–5 | Phase 3: Systems Data Structures | DSA basics are there; build the systems structures |
| 6–7 | Phase 5: Distributed Systems Foundations | Ready for clocks, idempotency, consensus |
| 8–9 | Phase 10: Money & Ledgers | Strong foundations; go straight to payments |
| 10 | Phase 12: Payment Systems Design | Review Phase 10 quickly, then design systems |

## Personalized path

Generate a table of all 16 phases. Phases below the entry point are `Skip`;
the entry point and above are `Do`. Area scores of 1/2 mark phases `Review`:

- DSA 1/2 → Phases 1 and 2 `Review`
- Systems structures & concurrency 1/2 → Phases 3 and 4 `Review`
- Distributed systems 1/2 → Phases 5 and 6 `Review`
- Money & ledgers 1/2 → Phase 10 `Review`
- Payment systems 1/2 → Phases 11 and 12 `Review`

Phase 0 is `Skip` unless the learner says their toolchain is not set up.
Hours per phase = sum of lesson `minutes` in `curriculum/curriculum.json` / 60,
rounded (read it locally or fetch it raw). `Skip` rows show `--`.

```markdown
| Phase | Name | Status | Est. hours |
|-------|------|--------|------------|
| 0 | Setup & Tooling | Skip | -- |
| 1 | Complexity & Core Data Structures | Review | 17 |
| ... | ... | ... | ... |
```

Add one sentence: "Your personalized path: ~X hours across Y phases." Then a
brief recommendation: the entry phase, and what to focus on first from their
weakest area. Finally offer the next step: `start-learning` saves this into
`LEARNING.md` (and records the language choice); `learn` starts the first
lesson. Render both in the host's invocation form.
