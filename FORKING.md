# Forking Guide

This course is MIT licensed. Fork it and adapt it.

## For payments teams (internal onboarding)

1. Fork the repository.
2. Keep Tracks A and B as-is; replace the payments examples in Track C with your own rails, processors, and ledger conventions.
3. Add your internal runbooks as `outputs/` artifacts and your incident postmortems as `## The Problem` scenarios.
4. Point `start-learning` at your own entry points by editing `skills/find-your-level/SKILL.md`'s score mapping.
5. Keep the attribution.

## For another implementation language

1. Add the language to `languages.json` (`id`, `dir`, `entry`, `testEntry`, `run`, `test`; set `ci: false` until every complete lesson has it).
2. Port lessons one at a time: same design, same names, same test cases. Start with `phases/10-money-and-ledgers/01-representing-money/`.
3. `scripts/run_lesson.py --lang <id>` and `scripts/project_test.py --lang <id>` work as soon as the registry entry exists.

## For universities and bootcamps

1. Map phases to weeks; each phase README has the hour total.
2. Use `check-understanding` quizzes and project `completion.json` reports as graded evidence.
3. The capstones in Phase 15 are term projects with rubrics already written.

## Keeping your fork updated

```bash
git remote add upstream https://github.com/<owner>/payments-engineering-from-scratch.git
git fetch upstream
git merge upstream/main
python3 scripts/build_catalog.py
```

## Attribution

Not required by MIT, but appreciated:

```text
Based on Payments Engineering from Scratch
https://github.com/<owner>/payments-engineering-from-scratch
```
