---
name: runbook-audit-sampling
description: Runbook for producing, recording, and reproducing a defensible random sample of transactions for internal or external audit
phase: 2
lesson: 14
tags: [sampling, audit, randomness, runbook, compliance]
---

# Audit Sampling Runbook

For the engineer asked to "pull a random sample of transactions" by audit, compliance, or a regulator. The goal is a sample that is uniform (or deliberately weighted), reproducible on demand, and defensible under ISA 530 or PCAOB AS 2315: every item has a known non-zero probability of selection and the method can be re-run.

## 1. Agree the population and the method

- [ ] Write down the population: date range, legal entities, transaction types, statuses (captured only? including reversals?). The sample is only as defensible as the population definition.
- [ ] Agree uniform versus amount-weighted (monetary-unit sampling). Uniform answers "what fraction of transactions have a defect"; weighted answers "what fraction of value is exposed". Many audits want both; run two samplers over the same pass.
- [ ] Agree `k`. For a defect-rate estimate at 95% confidence with a 2% margin, around 2,400 items; the auditor's statistician sets this, not you.
- [ ] Agree the stream order. Reproducibility requires that the same items arrive in the same order on a re-run: a Kafka topic partition with recorded offsets, or a query with a deterministic `ORDER BY` on a unique key.

## 2. Generate the seed and escrow it

- [ ] Generate the seed from a cryptographic source (`SecureRandom`), once, immediately before the run. Record it with the run id, population definition, stream order, `k`, sampler version (git SHA), and timestamp.
- [ ] Store the record somewhere the business cannot edit after the fact and the auditor can read: a write-once log, a ticket with history, or the auditor's own system.
- [ ] Do not reuse a seed across runs or days. Do not derive the seed from the date or the run id.
- [ ] The seed is confidential within the business until the run is complete, so nobody can place or time transactions to dodge or hit the sample. It is disclosed to the auditor on request.

## 3. Run the sampler

- [ ] Use Algorithm R (uniform) or A-Res (weighted) with the seeded, portable generator from this lesson, in one pass over the stream.
- [ ] Record `seen` (the population count) alongside the sample; it is part of the evidence and the denominator in every rate.
- [ ] Emit the sample as the full transaction records plus the stream position of each one, so an auditor can confirm each sampled item exists in the population.
- [ ] Log the generator's first three outputs after seeding. A re-run that disagrees on those has the wrong seed or the wrong generator; a re-run that agrees on those but not on the sample has a different stream.

## 4. Reproduce on request

- [ ] Re-run with the recorded seed, population, stream order, and sampler version. The output must be byte-identical. If it is not, stop and investigate before explaining; do not produce a "corrected" sample.
- [ ] Common causes of divergence: stream reordering (a non-deterministic `ORDER BY`, a topic compaction), a generator change (someone swapped in `java.util.Random`), a population change (late-arriving records), a library upgrade that changed `Math.pow` on an exotic platform.
- [ ] Keep the sampler version runnable for the retention period of the audit (often seven years). A container image pinned by digest is the practical answer.

## 5. Validate the sampler itself, once per release

- [ ] Uniform: run 30,000 streams of 10 items with `k = 3` and confirm every item is selected 30% of the time within 5%, and that the last item is not favoured (the off-by-one check).
- [ ] Weighted: run 20,000 streams of two items with weights 9:1 and confirm the heavy one is selected about 90% of the time.
- [ ] Generator: confirm the published first output for seed 0 (`0xE220A8397B1DCDAF` for SplitMix64), and that `nextInt(7)` over 70,000 draws is within 5% of uniform in every bucket.
- [ ] Keep these as tests in the repository, seeded, so they are deterministic and run on every change.

## 6. Questions the auditor will ask

| Question | Answer the method provides |
|---|---|
| Did every transaction have a chance of selection? | Yes: `k / n` for uniform, `w / W` for weighted, for every item in the stream. |
| Could someone have influenced which items were chosen? | Only by knowing the seed before the run; the seed was generated cryptographically and escrowed. |
| Can you show me how this sample was produced? | Re-run with the recorded inputs; the output is identical. |
| Why is the population count what it is? | `seen` was recorded by the sampler in the same pass that selected the items. |
| Does the sample cover the whole day? | Reservoir sampling is uniform over stream position; a histogram of sampled timestamps against population timestamps is attached. |
| What about transactions sampled last month? | Deduplication against prior samples changes the selection probabilities; if required, it is documented as stratification, not silently applied. |

## 7. What this does not cover

A random sample estimates rates; it does not find rare events. Sanctions hits, fraud rings, and specific suspect transactions need exhaustive screening (Phase 2, Lesson 12) and targeted queries, and an auditor who asks for "a sample to check for fraud" should be told so in writing.
