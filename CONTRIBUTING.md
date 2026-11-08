# Contributing

Thank you for helping build Payments Engineering from Scratch. The lessons are the product; `AGENTS.md` is the operating manual and `LESSON_TEMPLATE.md` the shape. Read both first.

## Ways to contribute

1. **Author a planned lesson.** `ROADMAP.md` lists every ⬚ Planned lesson. Each already has a title, hook, time estimate, and objectives in `curriculum/curriculum.json` and an outline under `phases/`. Run `python3 scripts/scaffold_lesson.py <phase> <lesson> --force` to replace the outline with the full skeleton, then write it to the contract.
2. **Port a lesson to Python or Go.** Java ships first. A port adds `code/python/` (`main.py`, `test_main.py`) or `code/go/` (`go.mod`, `main.go`, `main_test.go`) with the same design and the same test cases as the Java version, and updates the lesson's `**Languages:**` header. The reference lesson `phases/10-money-and-ledgers/01-representing-money/` has all three.
3. **Deepen a complete lesson.** Better worked example, a missing failure mode, a primary source, a sharper quiz distractor.
4. **Add a project** following `projects/AUTHORING.md`.
5. **Fix a bug** in a lesson's code or a script.

## Checklist before opening a PR

```bash
python3 scripts/audit_lessons.py                          # 0 issues
python3 scripts/run_lesson.py phases/NN-.../MM-... --lang java   # and python/go if you ported
python3 scripts/build_catalog.py                          # regenerates README Contents, ROADMAP, phase READMEs
python3 scripts/sync_skills.py --check                    # if you touched skills/
python3 scripts/build_site.py --check                     # every lesson renders on the website; required when you touch docs/, outputs/, or site-src/
```

- One commit per lesson directory: `feat(phase-NN/MM): <slug>`; ports use `feat(phase-NN/MM): <slug> python port`.
- Standard library only. No frameworks, no package managers.
- Money is integer minor units with a currency. Never a float.
- Deterministic demos: inject clocks and seeds; no network; `main` exits 0 in under ten seconds.
- Cite primary sources (RFCs, ISO standards, papers, official docs). Never copy from other curricula.
- Quizzes: six questions, 1 pre / 3 check / 2 post, distractors of comparable length.

## Review standard

A reviewer asks three questions: Could a strong engineer who has never seen this topic build the mechanism from the lesson alone? Does the test suite catch the failure the lesson warns about? Would the `outputs/` artifact be useful at work on Monday? If any answer is no, the lesson is not done.

## Code of conduct

Be direct and kind. Critique the lesson, not the author. Assume good faith.
