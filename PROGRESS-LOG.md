# Progress Log

Hand-maintained record of build batches, newest first. The live numbers are in `PROGRESS.md` (generated).

## 2026-11-09 — Build It walkthroughs in Python and Go (all 143 Build lessons)

- Every Build lesson's `### In Python` and `### In Go` sections are now full step-by-step walkthroughs mirroring the Java steps one for one (`#### Step N: <same title>`), with chunks copied verbatim from the shipped ports and prose written for a reader of that language; the existing difference notes were kept as each block's intro. 7 agents, 142 lessons (10/01 hand-converted as the model); +64,000 lines of docs, no code touched.
- New `scripts/check_walkthroughs.py` (stdlib) enforces step count/title parity, fence language, and that ≥60% of each chunk's identifiers exist in `code/<lang>/`; wired into CI and the per-PR checklist; AGENTS.md Build It contract updated; the `learn` skill teaches from the learner's language block.
- Site: the language picker now switches the whole Build It between the three walkthroughs (switch bar with "Also in" links and "Show all three"); table of contents follows what is visible.
- Side corrections found while quoting real output: 03/15 JSON size 1638646 bytes / 2.89x (docs said 1618646 / 2.85x); 06/04 Paxos `chosen ... at t=3`; 06/07 disjoint-majority line; 07/08 Go intro (commit-from-NONE returns ErrIllegalState, not a panic).
- Verification: check_walkthroughs 143/143 0 problems; audit 199/199 0 issues; Python 143/143; Go 143/143; site 228 pages 0 problems.

## 2026-11-09 — Site: progress export/import and LEARNING.md sync

- Progress page: download progress as JSON (v2 payload: completed lessons, quiz scores, language) and import a file on another device; copy/paste text kept; v1 payloads still import. Imports merge and show a preview before applying.
- `LEARNING.md` sync: loads or pastes `LEARNING.md` (also `LEDGER-`/`CONSENSUS-LEARNING.md`), resolves Progress-log rows by `NN/MM`, slug path, "Phase N Lesson MM" or title, and phases marked `Done` in the Path table; unknown rows are listed, not guessed.
- Quizzes now store the last full score per lesson in the browser and show it above the quiz and in progress lists.

## 2026-11-09 — Full validation pass (curriculum fidelity, content quality, site QA)

- Three independent reviewers (fresh agents) plus the full mechanical matrix: audit 199/199 0 issues; 143/143 Build lessons pass in Java, Python and Go (429 runs); 9/9 project grader runs; curriculum ↔ disk 199/199 with no missing or extra lessons; 995/995 Build It code chunks correspond to shipped code; learning paths, skills, capstone references all resolve.
- Content spot review (16 lessons, every worked number recomputed): grade A−. Fixed: 11/02 card lifecycle — an expired partial capture now lands in CAPTURED (captured funds stay refundable) instead of terminal EXPIRED, in all three languages, tests, docs, quiz and checklist; 01/07 multiply-shift bucket numbers, treeify wording, HashDoS timing, JEP 180 attribution; 12/01 canonical form states newline-terminated lines and the forgery caveat; 10/03 the capture mirror is labelled a reversal (true refund path explained); 06/07 C_new-committed test made deterministic (was `A || B`); 07/08 END is a lazy write (2n+1 forced).
- Metadata: every `**Prerequisites:**` line normalised to `Phase N Lesson MM (Label)`; 172 labels normalised, 18 pointers renumbered to the lesson their label named, 2 dangling fixed, 27 Track A/B references to Phase 10 L01 replaced by Phase 0 L07, 5 forward references turned into "pairs with" sentences; 09/13 time 90→75; 01/07 trimmed to three exercises; book/paper Further Reading entries given publisher/DOI URLs; scaffold emits the canonical form.
- Quizzes: correct-answer positions rebalanced (index 1 held 45% of answers; 51 quizzes had ≥4 of 6 on one index) — 171 questions reordered with option sets and explanations preserved (letter references remapped); `audit_lessons.py` now enforces max 2 per position; AGENTS.md updated (capstone section contract, Key Terms 4–10, Further Reading 3–8 with URLs, quiz spread rule).
- Mermaid: 13 diagrams with syntax that mermaid 10 rejects (escaped quotes, semicolons in sequence messages, unquoted parentheses in edge labels) fixed; 227/227 diagrams render.
- Site QA (Playwright, desktop + phone, light + dark): 228 pages, 0 console errors, 0 broken internal links, quiz/search/glossary/progress/code tabs/prev-next all pass, body contrast ≥ 6.3:1. Fixed: task-list items broke into columns (renderer now wraps item text; grid layout), duplicate H1 on glossary, tap targets raised to ≥ 40px, phone nav wraps instead of hidden horizontal scroll.
- Open: glossary refresh (every Key Terms row → entry) still pending from 2026-11-01.

## 2026-11-01 — Fact-checks and static website

- Fact-checked the nine regulatory claims written without network access, against primary sources. Corrected: FedNow per-transaction limit (now 10,000,000 USD network maximum since Nov 2025, participant default 100,000 USD; was "500,000 at launch"), FCA PS25/12 URL, RBI e-mandate link (Aug 2019 circular repealed by the Digital Payments E-mandate Framework 2026), EBA SCA-opinion URL (two lessons), Modern Treasury and Adyen doc links. Confirmed correct: FCA 2026 rules, Wirecard Drucksache 19/30900, ECI values, all cited reporting deadlines (SAR, Reg E, Nacha, UK APP, GDPR/CCPA). Unverifiable: Elkan 2001 PDF link (citation confirmed; host blocks fetches).
- Static website: `scripts/build_site.py` (stdlib only, own Markdown renderer, `--check` validates every page and internal link) generates 228 pages into `site/` — index, roadmap, 16 phase pages, 199 lesson pages with Java/Python/Go code tabs and interactive quizzes, projects, learning paths, glossary with A–Z bar and filter, client-side search, local progress (localStorage). `.github/workflows/site.yml` deploys to GitHub Pages on push to `main` (Pages source must be set to "GitHub Actions" once in repo settings). Build ≈2 s.
- Glossary refresh (every Key Terms row → entry) was started and interrupted by a usage limit; to resume.

## 2026-10-27 — Go port complete (Wave 2: 47 lessons + 3 projects) — all three languages done

- Ported batches G–L: Phases 8, 9, 10, 11, 12, 14 lessons and the three projects. Go coverage: 96 → **143 / 143** Build lessons; all 3 projects pass 4/4 stages in Go (ledger-engine, idempotent-payment-gateway, raft-kv), Go starters compile and fail stage 1 as required.
- `languages.json`: Go now `ci: true` — a lesson is Complete only with Java, Python, and Go present. README, start-learning skill updated: every Build lesson ships all three languages.
- Parity: every Java assertion ported (Go counts ≥ Java), demo output byte-identical to Java in every lesson; bit-exact `java.util.Random`/SplitMix64 ports; Go-native AES-GCM (11/05) and Ed25519 (13/11); `encoding/xml` Strict + DOCTYPE prescan for ISO 20022/camt lessons; `go vet`, `gofmt`, `go test -race` clean everywhere.
- Incident: the first Wave 2 run was interrupted twice (session rate limit, then session stop) with 38 of 47 lessons partially on disk; a second set of agents ported the remaining 9 lessons and raft-kv's stage tests and verified every pre-existing file (11/04, 12/09, 14/06 were incomplete and were finished).
- Verification: audit 199/199, 0 issues; Go 143/143; Java 143/143; Python 143/143; projects 9/9 (3 projects × 3 languages).
- The course is now fully built in all three languages. Remaining items are polish (fact-checks, glossary refresh, first CI run) and the reference-parity extras (book/PDF, website, practice assessments, manuals, translations).

## 2026-10-27 — Go port Wave 1 complete (56 lessons, Phases 0–7 + Phase 13)

- Ported batches A–F of `docs/porting/go-port-plan.md`. Go coverage: 40 → **96 / 143** Build lessons; `languages.json` keeps Go `ci: false` until Wave 2.
- Parity: same type/method names, every Java assertion ported (Go counts ≥ Java), demo output byte-identical to Java in every lesson except documented cosmetic rounding lines (`%.2f`/`%.0f` of exact .5 values; 13/06 `Map.of` print order). Bit-exact `java.util.Random` (incl. `nextGaussian`), SplitMix64, `String.hashCode`, `floorMod`, modified-UTF-8 `writeUTF`, and JDK `SHA1PRNG` ports where lessons depend on them.
- Go-native where Java used JDK crypto: 13/11 uses `crypto/ed25519` (same key and signatures as Java — Go verifier verifies a Java-exported bundle); AES-GCM via `crypto/cipher`. No HMAC substitutions.
- Fixed the 07/10 secondary-index porting note: Go shards with `floorMod` on the signed hash, matching Java.
- Every Go lesson passes `go vet`, `gofmt -l`, and `go test -race`.
- Verification: audit 199/199, 0 issues; Go 96 passed / 0 failed / 47 skipped (Wave 2); Java 143/143; Python 143/143.
- Next: Go Wave 2 = 47 lessons (Phases 8–12, 14) + Go versions of the 3 projects; then Go `ci: true`.

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
