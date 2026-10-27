# Go Port Plan

**Status (2026-10-26):** in progress. 40 of 143 Build lessons and 0 of 3 projects have Go. Two waves of six agents; the batches mirror the Python plan (A–F then G–L). Agent brief follows.

Repository: `/home/claude/payments-engineering-from-scratch`. Every lesson below is complete in Java (and most in Python). Add a **faithful Go port** of each assigned lesson. Do not change the Java or Python code, the quiz, or the outputs.

## Read first
1. `AGENTS.md` (code rules) and `languages.json` (Go run/test commands).
2. The reference three-language lesson: `phases/10-money-and-ledgers/01-representing-money/` — compare `code/java/` with `code/go/` and read its `### In Go` subsection in `docs/en.md`.
3. For each assigned lesson: its full `docs/en.md` and all of `code/java/*.java` before writing anything. The Python port (if present) is a second reference for test cases.

## For each lesson, write (in `code/go/`)
- `go.mod`: `module pefs/<phase-slug-without-dashes>/<lesson-slug-without-dashes>` and `go 1.22`. No dependencies.
- `main.go` (`package main`): the same design as the Java code. Same type names (CamelCase), same method names (exported CamelCase), same constants, same algorithm, same worked example in `main()`. Header comment (4-6 lines) citing `phases/<phase>/<lesson>/docs/en.md`, `Run: go run .`, `Test: go test ./...`, and any spec cited by the Java header. Helper types may live in extra `.go` files in the same package.
- `main_test.go` (`package main`, `testing`): **port every check in `MainTest.java`** (same cases, same expected values); table-driven where natural; total assertions ≥ the Java count. Use `t.Helper()`, `t.Run` subtests. Deterministic: no sleeping, no network, injected clocks and seeded RNGs.

## Parity rules
- **Standard library only.** Go 1.22 language level (range-over-int is fine; no 1.23+ iterators).
- **Same deterministic output.** Run `main` in Java and Go and diff. Numbers, orderings, counts must match; formatting may differ trivially. Port the Java RNG exactly where a lesson seeds one (SplitMix64, or `java.util.Random`'s 48-bit LCG: `seed = (seed ^ 0x5DEECE66D) & (1<<48 - 1)`, `next(bits)`, `nextInt(bound)` with the rejection loop, `nextLong`, `nextDouble`, `nextGaussian`, and `Collections.shuffle`). Wall-clock timings may differ; everything else must not.
- **Integer semantics.** Go `/` and `%` truncate toward zero like Java, and `int64` wraps like Java's `long` — but Java's `Math.addExact`/`multiplyExact` throw on overflow; reproduce with checked helpers returning `error` (the reference lesson has `addChecked`/`mulChecked`). Java's `Math.floorMod` needs an explicit helper. Hash functions: use `uint32`/`uint64` for wraparound, convert to signed where Java compares signed values. **Note for 07/10 secondary indexes:** take the remainder of the *signed* int64 hash with floorMod, not `% uint64(n)`, so keys shard exactly as Java; the lesson's Porting note says otherwise and is wrong — fix that sentence.
- **Errors are values.** Java checked/unchecked exceptions become `(T, error)` returns with sentinel errors (`var ErrX = errors.New(...)`) supporting `errors.Is`, or `panic` only where the Java throws for a programming error that cannot be reported (see `Money.Compare` in the reference). Tests use `errors.Is`.
- **Money** is `int64` minor units + `Currency`; port the lesson's `Money` exactly (the reference `code/go/main.go` is the canonical Go Money). Never `float64` for amounts; `math/big` only where Java uses `BigDecimal`/`BigInteger`.
- **Records/sealed interfaces** → structs and interfaces (or a tagged struct with a kind enum); `Optional` → `(T, bool)` or pointer; `TreeMap` iteration order → sort keys explicitly; `LinkedHashMap` → slice of keys + map; `PriorityQueue` → `container/heap` with the same tie-break; `BigDecimal` → `math/big.Rat` or scaled ints.
- **Concurrency lessons:** goroutines, `sync.Mutex`, `sync/atomic` (real CAS — Go has it), channels, `context`, `sync.WaitGroup`, `errgroup`-style patterns written by hand. Tests must pass under `go test -race ./...` (run it). Where Java uses virtual threads or executors, use goroutines with a bounded semaphore channel. Races the Java lesson deliberately produces must still be demonstrable deterministically (an explicit interleaving scheduler is fine).
- **Crypto/encoding:** `crypto/hmac`, `crypto/sha256`, `crypto/aes` + `crypto/cipher` (GCM exists in Go — use it where the Java uses AES-GCM), `crypto/ed25519` (exists — use it where the Java uses Ed25519), `encoding/binary`, `encoding/xml` (disable DTDs: `decoder.Strict = true` and reject `<!DOCTYPE` by prescan as the Java does), `encoding/json`, `regexp`.
- **Generics:** use Go generics for the containers the Java makes generic (`Stack[T]`, `Heap[T]`, etc.) with `cmp.Ordered`/`comparable` constraints; keep API shape the same.

## Docs update (minimal, per lesson)
- Header line becomes `**Languages:** Java, Python, Go` (or `Java, Go` if no `code/python/`).
- Add a `### In Go` subsection inside `## Build It` after `### In Python` (or after the last Java step if there is no Python section) and before `### Porting notes`: 15-40 lines on the 2-4 things that differ (error values, integer/overflow handling, concurrency primitives, generics), with one or two short `go` snippets from your `main.go`, and the run/test commands. Then **delete the `### Porting notes` subsection entirely** (both ports now exist). Do not change anything else in the docs.

## Verify (all must pass, from the repo root)
```bash
python3 scripts/run_lesson.py <lesson path> --lang go       # PASS
(cd <lesson>/code/go && go vet ./... && gofmt -l . && go test -race ./...)   # clean, no files listed by gofmt, pass
python3 scripts/run_lesson.py <lesson path> --lang java     # still PASS
python3 scripts/audit_lessons.py --phase <N>                 # 0 issues for your lessons
```
Diff demos: write Java and Go output to a private scratch subfolder `/tmp/claude-0/-home-claude/09d2ed73-148a-5eb2-b960-d3e48b05bff4/scratchpad/go-<yourbatch>/` (never shared names) and compare numbers. Remove the subfolder when done.

## Do not
Touch README.md, ROADMAP.md, PROGRESS*.md, catalog.json, curriculum.json, languages.json, scripts/, projects/, or lessons not assigned to you. Run git. Delete anything outside your lessons' `code/go/`. Leave `.out/`, `__pycache__/` behind. Edit `code/python/`.

## Report (under 20 lines)
Per lesson: Java vs Go assertion counts, demo parity, `-race` result, substitutions. Then the audit summary line per phase.
