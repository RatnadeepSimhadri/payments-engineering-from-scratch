# Python Port Plan (deferred)

**Status (2026-10-13):** deferred. The learner is taking the course in Java first; resume this plan later.
**Scope:** 103 Build lessons without `code/python/` + Python versions of the 3 projects. 40 Build lessons already have Python (and Go).
**Done when:** all 143 Build lessons and 3 projects pass `--lang python`; then set `"ci": true` for Python in `languages.json`.

## Partial work already done

Branch `wip/python-port` (not merged) holds ports started before the pause: 00/05, 02/01, 02/02, 03/02, 04/08, 06/02 and 07/01 pass their Python tests but have not been checked for output parity with Java or had their docs finished; 06/03 quorums has `main.py` only. Start there: `git checkout wip/python-port`, verify those eight against the brief, then continue with the remaining batches.

## How to resume
1. `python3 scripts/progress.py` shows current coverage (the "Python implementations" row).
2. List what is missing:
   ```bash
   python3 -c "import json,pathlib;c=json.load(open('catalog.json'));print('\n'.join(l['path'] for p in c['phases'] for l in p['lessons'] if l['type']=='Build' and not pathlib.Path(l['path'],'code/python/main.py').is_file()))"
   ```
   (run `python3 scripts/build_catalog.py` first so `catalog.json` exists)
3. Hand each batch to an agent with the brief below, two waves of six agents, verifying and committing after each wave.

## Batches

| Wave | Batch | Lessons |
|---|---|---|
| 1 | A | 00/05 benchmarking; 01/03 dynamic arrays, 01/04 linked lists, 01/05 stacks-queues-deques, 01/08 BST, 01/10 disjoint sets, 01/11 bit manipulation; 04/03 CAS & lock-free, 04/04 concurrent hash maps, 04/06 executors |
| 1 | B | 02/01 sorting, 02/02 binary search, 02/03 two pointers, 02/04 recursion, 02/07 greedy, 02/10 topo sort, 02/11 MST, 02/12 string matching, 02/13 intervals, 02/14 randomized |
| 1 | C | 03/02 CMS & HLL, 03/03 balanced trees, 03/04 B-trees, 03/05 LSM, 03/07 skip lists, 03/09 Merkle, 03/10 segment/Fenwick, 03/12 timing wheels, 03/13 IDs, 03/15 serialization |
| 1 | D | 04/08 actors, 04/11 structured concurrency, 04/12 testing concurrent code; 05/04 HLC, 05/07 RPC, 05/08 timeouts, 05/12 leases, 05/13 gossip |
| 1 | E | 06/02 replication lag, 06/03 quorums, 06/07 Raft safety, 06/08 CRDTs, 06/09 conflict resolution, 06/10 chain replication; 13/03 risk scoring, 13/06 AML, 13/11 auditability (Ed25519 → HMAC seal substitution) |
| 1 | F | 07/01 partitioning, 07/04 isolation levels, 07/05 2PL, 07/06 MVCC, 07/07 SSI, 07/10 secondary indexes, 07/11 CDC, 07/12 sagas, 07/13 outbox |
| 2 | G | 08/01–08/10 (all ten Build lessons) |
| 2 | H | 09/03, 09/04, 09/05, 09/06, 09/07, 09/08, 09/09, 09/13 |
| 2 | I | 10/02, 10/05, 10/06, 10/08, 10/09, 10/10, 10/11; 11/04, 11/05, 11/07 |
| 2 | J | 11/09, 11/13, 11/14; 14/04, 14/05, 14/06, 14/08, 14/10 |
| 2 | K | 12/03, 12/04, 12/05, 12/06, 12/07, 12/08, 12/09, 12/10, 12/11, 12/12, 12/14 |
| 2 | L | Projects: ledger-engine, idempotent-payment-gateway, raft-kv (`starter/python`, `tests/python`, `solution/python`; add "Python" to `project.json` languages) |

## Agent brief (use verbatim)

### Python port brief

Repository: `/home/claude/payments-engineering-from-scratch`. Every lesson below is complete in Java (docs, `code/java/Main.java`, `code/java/MainTest.java`, quiz, outputs). Your job is to add a **faithful Python port** of each assigned lesson. Do not change the Java code, the quiz, or the outputs.

## Read first
1. `AGENTS.md` (code rules) and `languages.json` (Python run/test commands).
2. The reference three-language lesson: `phases/10-money-and-ledgers/01-representing-money/` — compare `code/java/` with `code/python/` to see how one design maps to both, and read its `### In Python` subsection in `docs/en.md`.
3. For each assigned lesson: its full `docs/en.md`, `code/java/*.java` (all files), before writing anything.

## For each lesson, write
- `code/python/main.py`: the same design as the Java code. Same class names (CamelCase), same method/function names in snake_case, same constants, same algorithm, same worked example in `main()`. Header docstring (4-6 lines) citing `phases/<phase>/<lesson>/docs/en.md`, `Run: python3 main.py`, `Test: python3 -m unittest -v`, and any spec cited by the Java header. Guard with `if __name__ == "__main__": main()`.
- `code/python/test_main.py`: `unittest`, importing from `main`. **Port every check in `MainTest.java`** (same cases, same expected values); you may group several checks into one test method, but the total number of assertions must be at least the Java count. Deterministic: no sleeping, no network, injected clocks and seeded RNGs.

## Parity rules
- **Standard library only.** Target Python 3.11: no `type` statements, no PEP 695 generics, no 3.12+ only APIs.
- **Same deterministic output.** Run `main` in both languages and compare. Numbers, orderings, and counts in the demo must match. Formatting may differ trivially (e.g. list brackets). If the Java code uses a seeded RNG, port that exact generator (SplitMix64 if the lesson has one; for `java.util.Random` implement its 48-bit LCG: `seed = (seed ^ 0x5DEECE66D) & ((1<<48)-1)`, `next(bits)`, `nextInt(bound)`, `nextLong`, `nextDouble` exactly) so the same seed yields the same sequence. Wall-clock timings (benchmarks) may differ; everything else must not.
- **Integer semantics.** Python ints never overflow and `//` floors toward negative infinity. Where Java relies on truncation toward zero, `Math.floorMod`, 32/64-bit wraparound, or `Math.addExact` overflow errors, reproduce the Java behaviour explicitly (helpers such as `to_int64`, truncating division) and test it. Hash functions that depend on wraparound must mask (`& 0xFFFFFFFF`, `& 0xFFFFFFFFFFFFFFFF`).
- **Money** is integer minor units with a currency (port the lesson's `Money`/`Currency` exactly). Never `float` for amounts; `decimal.Decimal` only where the Java uses `BigDecimal`.
- **Concurrency lessons:** use `threading`, `queue`, `concurrent.futures`, `asyncio` as the closest equivalents to the Java primitives. Where the GIL changes the lesson's point (e.g. a race that is harder to reproduce), make the race reproducible deterministically (e.g. a yield point or an explicit interleaving scheduler) and explain it in the docs. Tests must be reliable: run the test file 3 times; all runs must pass.
- **Exceptions** map to Python exception classes with the same names where the Java defines its own; tests use `assertRaises`.
- **Records/sealed interfaces** map to `@dataclass(frozen=True)` and `Enum`; byte work uses `bytes`/`bytearray`/`struct`; XML uses `xml.etree.ElementTree` (reject DOCTYPE like the Java does); crypto uses `hmac`, `hashlib`, `secrets`. If the Java uses a JDK-only primitive with no stdlib Python equivalent (e.g. AES-GCM, Ed25519), implement only what is needed with stdlib (`hashlib`/`hmac`), document the substitution in the In Python section, and keep the lesson's security properties under test.

## Docs update (minimal, per lesson)
- Header line becomes `**Languages:** Java, Python` (keep `, Go` only if `code/go/` already exists).
- Add a `### In Python` subsection inside `## Build It` (after the last Java step, before `### Porting notes`): 15-40 lines naming the 2-4 things that actually differ (numeric model, error handling, concurrency/GIL, data types), with one or two short `python` snippets taken from your `main.py`, and the exact run/test commands. Remove the Python sentence(s) from `### Porting notes` so it only talks about Go. Do not change anything else in the docs.

## Verify (all must pass, from the repo root)
```bash
python3 scripts/run_lesson.py <lesson path> --lang python   # PASS
python3 scripts/run_lesson.py <lesson path> --lang java     # still PASS
python3 scripts/audit_lessons.py --phase <N>                 # 0 issues for your lessons
```
Also diff the two demos: `(cd <lesson>/code/java && javac -d .out *.java && java -cp .out Main) > /tmp/j.txt; (cd <lesson>/code/python && python3 main.py) > /tmp/p.txt` and check the numbers agree.

## Do not
Touch README.md, ROADMAP.md, PROGRESS*.md, catalog.json, curriculum.json, languages.json, scripts/, projects/, or lessons not assigned to you. Run git. Delete anything outside your lessons' `code/python/`. Leave `__pycache__/` or `.out/` behind.

## Report (under 20 lines)
Per lesson: Java assertion count vs Python assertion count, whether the demo output matches, and any substitution you had to make. Then the audit summary line per phase.
