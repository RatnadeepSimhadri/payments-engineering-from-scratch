# Project Authoring Contract

A project is a staged build with a grader. Learners implement each stage in **their chosen language**; the grader runs the tests of every stage up to the one requested, so regressions surface immediately. `scripts/project_test.py` is the only grader; the `build-project` skill drives it.

## Layout

```text
projects/<project-id>/
  project.json                       # manifest (schema below)
  README.md                          # overview, prerequisites, commands, what you keep
  stages/
    01-<slug>/
      docs/en.md                     # the stage lesson
      starter/
        java/   Main.java            # stage 1 only: the files --init copies into the workspace
        python/ main.py
        go/     go.mod  main.go
      tests/
        java/   Stage01Test.java     # run against the learner's workspace sources
        python/ test_stage_01.py
        go/     stage01_test.go
    02-<slug>/
      docs/en.md
      starter/STAGE.md               # "Continue editing the workspace created at stage 1" + new signatures
      tests/{java,python,go}/
    ...
  solution/
    java/   Main.java (+ helpers)    # passes every stage
    python/ main.py
    go/     go.mod  main.go
```

## How grading works

For stage N in language L, the grader creates a temp directory, copies the learner's source files from the workspace (`*.java` excluding `*Test.java`; `*.py` excluding `test_*.py`; `*.go` excluding `*_test.go`, plus `go.mod`), copies `stages/<stage>/tests/<L>/*` on top, and runs the language's test command:

| Language | Command in the sandbox |
|---|---|
| Java | `javac -d .out *.java && java -cp .out Stage01Test && java -cp .out Stage02Test …` (every `*Test` class present) |
| Python | `python3 -m unittest -v` |
| Go | `go test ./...` |

Consequences for authors:

- **Tests import the learner's code by its public names.** Fix the type and function names in the stage doc's "Your task" section and keep them identical across languages modulo casing (`Ledger.post` / `ledger.post` / `Ledger.Post`).
- **Java test classes must be named `StageNNTest`** and contain a `main` that exits 1 on failure (same harness pattern as lesson tests). Several test classes compile together, so helper classes in tests must have unique names (prefix them with the stage number).
- **Python test files are `test_stage_NN.py`** and import from `main`.
- **Go test files are `stageNN_test.go`** in `package main`; the learner's `go.mod` is copied, so the module name does not matter.
- Stage 1's `starter/<lang>/` must compile and run, with every function stubbed to fail its test visibly (throw `UnsupportedOperationException` / `raise NotImplementedError` / `panic("not implemented")`), never to pass by accident.
- `solution/<lang>/` must pass `python3 scripts/project_test.py <id> --lang <L> --all --solution` for every language in `project.json`.
- Tests are deterministic: inject clocks and seeds; no sleeping, no network, no filesystem outside the sandbox.

## project.json schema

```json
{
  "id": "ledger-engine",
  "title": "Ledger Engine",
  "level": 2,
  "levelName": "Builder",
  "tagline": "One sentence a learner reads in the project list.",
  "summary": "Two or three sentences on what the finished artifact does.",
  "youWillBuild": "The finished artifact in one sentence.",
  "hours": 8,
  "languages": ["Java", "Python", "Go"],
  "languageWhy": {"Java": "...", "Python": "...", "Go": "..."},
  "status": "ready",
  "skills": ["Verb phrase per stage"],
  "prerequisites": [{"title": "Lesson title", "path": "phases/NN-.../MM-..."}],
  "stages": [
    {
      "id": "01-money-and-accounts",
      "title": "...",
      "summary": "One sentence.",
      "hours": 2,
      "difficulty": "starter | core | stretch",
      "concepts": ["..."]
    }
  ]
}
```

Levels: 1 Explorer (one concept, two stages), 2 Builder (one system, four stages), 3 Advanced (several interacting mechanisms, four to six stages).

## Stage doc contract (`stages/NN-slug/docs/en.md`)

```markdown
# <Stage title>

**Stage N of M.** Plan about H hours.

<Two or three paragraphs: what this stage adds, the invariant it must preserve, where real systems use it.>

## Worked example

<A concrete trace with real numbers the learner can predict before coding.>

## Your task

<The exact signatures in Java, Python, and Go. What each must do. What must fail and how.>

## Run the grader

```bash
python3 scripts/project_test.py <id> --lang <java|python|go> --stage N --path <workspace>
```

## Check yourself

<Two or three questions the build-project skill asks after the stage passes.>

## Going further (last stage only)

<One idea that extends the project into a later phase.>
```

## Rules

- Learners never need `solution/`. The `build-project` skill may read it to diagnose, and shows at most one function after two honest attempts.
- Every stage adds behaviour; no stage is refactoring only.
- Money is integer minor units with a currency (Phase 10, Lesson 01). Copy the `Money` design into the starter; projects are self-contained.
- One commit per stage directory: `feat(project/<id>): stage NN <slug>`.
