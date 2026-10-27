# Progress Log

Hand-maintained record of build batches, newest first. The live numbers are in `PROGRESS.md` (generated).

## 2026-10-26 — Python port complete (Wave 2: 47 lessons + 3 projects)

- Ported batches G–L: Phases 8, 9, 10, 11, 12, 14 lessons and the three projects. Python coverage: 96 → **143 / 143** Build lessons; all 3 projects pass 4/4 stages in Python.
- `languages.json`: Python now `ci: true` — a lesson is Complete only with Java and Python present.
- Parity: every Java assertion ported (Python counts ≥ Java), demo output identical to Java in every lesson except documented cosmetic lines; bit-exact `java.util.Random`/SplitMix64/xorshift ports wherever a lesson seeds randomness.
- Substitutions: 11/05 AES-GCM → HMAC-SHA256 keystream + tag (no stdlib AES); 06/03 String.hashCode port; several `Rational`/`RoundingMode` reimplementations of BigDecimal behaviour.
- Verification: audit 199/199, 0 issues; Python 143/143; Java 143/143; projects 6/6 (java+python).
- Next: Go port (103 lessons + 3 projects), two waves.

## 2026-10-13 — Python port Wave 1 complete (Phases 0–7 + Phase 13 Build lessons)

- Ported 56 lessons to Python (batches A–F of `docs/porting/python-port-plan.md`), including the 8 partial ports from `wip/python-port`, which were verified and finished. Python coverage: 40 → 96 of 143 Build lessons.
- Parity: same class/method names, every Java test case ported (Python assertion counts ≥ Java), demo output diffed against Java and identical apart from wall-clock timings and three documented exceptions.
- Documented substitutions: 13/11 Ed25519 seal → HMAC-SHA256 (no stdlib Ed25519); 04/03 CAS emulated with a lock (no CPython CAS); 04/12 explicit preemption point because the GIL hides the race; 01/10 heuristics section runs at n=2,000 (Java n=20,000) for runtime; 02/04 Python recursion limit differs from the JVM stack.
- Bit-exact ports of `java.util.Random`, `Collections.shuffle`, Java `String.hashCode`, truncating division and 64-bit wraparound where lessons depend on them.
- Verification: audit 199/199, 0 issues; Python 96 passed / 0 failed / 47 skipped (not yet ported); Java 143/143; projects 3/3.
- Remaining: Wave 2 = 47 lessons (Phases 8–12, 14) + Python versions of the 3 projects. Python stays `ci: false` until then.
- Noted for later: 07/10 Go porting note suggests `% uint64(n)`, which would shard negative hashes differently from Java's `Math.floorMod`; fix when doing the Go port.

## 2026-10-13 — Python port paused; learner starts the course in Java

- Decision: take the course in Java now; Python and Go ports deferred.
- The Python port was started (wave 1, 6 agents) and stopped within minutes. Partial work parked on branch `wip/python-port`, not merged: 7 lessons pass Python tests (parity unchecked), 1 incomplete.
- `main` is unchanged: Java-only, 199/199 lessons, audit 0 issues, 143/143 Java Build lessons pass, 3/3 projects.
- Resume plan, batches, and the agent brief: `docs/porting/python-port-plan.md`.
- Reference-parity items added to the plan (not started): book/PDF, website, practice assessments, manuals, translations.

## 2026-10-13 — High-priority gap fixes (199/199, verified by the stricter audit)

- Audit tightened: now fails placeholder quizzes, docs below 350 (Build) / 300 (Learn, Capstone) lines, and Build lessons without an `outputs/` artifact. The tracker dropped from 199 to 183 complete, exposing the gaps.
- Fixed all 32 gaps across 19 lessons: 16 placeholder quizzes rewritten, 11 missing artifacts added, 5 short lessons expanded (9/01 192→355, 1/07 254→351, 10/04 268→376, 15/06 273→312, 15/04 298→313).
- Corrections found while fixing: 9/01 alert-latency claim (burn rate 8 fires after 4.5 h, not ~30 min); 15/06 tie-out identity signs; 1/07 SipHash arrived in Python 3.4 (PEP 456).
- Verification: audit 199/199, 0 issues; no TODO quizzes; 143/143 Java Build lessons pass; 3/3 projects 4/4; skills in sync.

## 2026-10-13 — Batch 3: Phases 10–14 complete — Java pass DONE

- Authored the final 38 Track C lessons: 161 → 199 complete. **Every lesson in the curriculum is complete in Java.**
- Verification: audit 199/199 lessons, 0 issues; all 143 Java Build lessons run and pass; 3/3 projects pass 4/4 stages; skills in sync.
- Incident: one agent's cleanup deleted the Python/Go ports of four Phase 11 lessons; restored from git and re-verified.
- To review before publishing: some Further Reading URLs and recent regulatory figures (FedNow limits, FCA safeguarding 2026, RBI e-mandate circular, EBA SCA opinion) were written without network access.
- Next: Python and Go ports (40 of 143 Build lessons already have them).

## 2026-10-13 — Batch 2: Phases 5–9 complete (Java)

- Authored the 24 remaining Track B lessons: 137 → 161 complete. Track B (Phases 5–9) is now 63/63.
- Audit: 199 lessons, 0 issues. Every Phase 5–9 Build lesson runs and passes in Java (49 lessons).
- Next: Batch 3 = Phases 10–14 (38 lessons), which finishes the Java pass.

## 2026-10-13 — Batch 1: Phases 0–4 complete (Java)

- Authored the 45 remaining Track A lessons (Phases 0–4) in Java: 73 → 118 complete.
- Every Phase 0–4 lesson now has deep docs, Java code + tests (31–66 assertions each), quiz, and an `outputs/` artifact.
- Tracker introduced: `scripts/progress.py` generates `PROGRESS.md`.
- Next: Batch 2 = Phases 5–9 (43 lessons); Batch 3 = Phases 10–14 (38 lessons).

## 2026-10-13 — Java-first pass, projects, skills, packaging

- Switched `languages.json` to Java-only `ci: true`; Python/Go registered, ports deferred.
- Finished the 38 stub lessons from the interrupted run; 73 lessons complete.
- All three projects (`ledger-engine`, `idempotent-payment-gateway`, `raft-kv`) complete in Java, 4 stages each, graders green.
- Nine skills written and mirrored; three learning-path manifests; glossary (175 entries); CI workflow; CONTRIBUTING/FORKING/LICENSE.
- Initial commit `c6a3f28`; delivered as tarball + git bundle; restored into `~/ClaudeProjects/LearnPE/payments-engineering-from-scratch` with history.

## 2026-10-11/12 — Foundation

- Studied the reference repo; designed the 199-lesson / 16-phase / 3-track curriculum (`curriculum/curriculum.json`).
- Built the tooling (`build_catalog`, `run_lesson`, `audit_lessons`, `scaffold_lesson`, `project_test`, `sync_skills`), `AGENTS.md`, `LESSON_TEMPLATE.md`, `projects/AUTHORING.md`.
- Wrote the reference lesson (Phase 10 Lesson 01, Java/Python/Go) and Phase 0 via the first agent batch.
