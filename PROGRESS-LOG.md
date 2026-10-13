# Progress Log

Hand-maintained record of build batches, newest first. The live numbers are in `PROGRESS.md` (generated).

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
