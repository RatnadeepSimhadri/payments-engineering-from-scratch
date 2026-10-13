---
name: checklist-dev-environment
description: Onboarding checklist for a machine or CI image that must build and test Java 21, Python 3.11+, and Go 1.22+ code with the standard library only
phase: 0
lesson: 1
tags: [setup, toolchain, onboarding, ci]
---

# Dev Environment Checklist

Run through this once per machine and once per CI image. Every row has a command and the output that means "pass".

## Toolchains

| Check | Command | Pass looks like | If it fails |
|-------|---------|-----------------|-------------|
| JDK runtime | `java -version` (prints to **stderr**) | `openjdk version "21.x.y"` or newer | Install a JDK 21+ from https://adoptium.net or `sudo apt install openjdk-21-jdk`; put its `bin/` first on `PATH` |
| JDK compiler | `javac -version` | `javac 21.x.y` | You have a JRE, not a JDK. Install the full JDK; `java` alone cannot compile `Main.java` |
| Same JDK in both | compare the two numbers above | identical major version | `PATH` and `JAVA_HOME` point at different installs; fix `PATH` order or `JAVA_HOME` |
| Python | `python3 --version` | `Python 3.11.x` or newer | Install from https://www.python.org/downloads/ or `sudo apt install python3.12`; if `python3` is a `pyenv` shim, run `pyenv global 3.12` |
| Go | `go version` | `go version go1.22.x` or newer | Install from https://go.dev/dl/; add `/usr/local/go/bin` to `PATH` |
| Preflight | `cd phases/00-setup-and-tooling/01-dev-environment/code/python && python3 main.py` | every row `OK`, exit code 0 | Follow the fix text in each non-OK row; the program never exits non-zero |

- [ ] All six rows pass, or the only non-OK rows are for languages you are not learning (your choice is recorded in `LEARNING.md`).
- [ ] CI images pass all rows: CI runs every Build lesson in every language.

## Repository layout

- [ ] `languages.json` exists at the root (the marker the scripts use to find the root).
- [ ] `phases/`, `scripts/`, and `curriculum/curriculum.json` exist.
- [ ] `python3 scripts/run_lesson.py phases/00-setup-and-tooling/01-dev-environment` prints `PASS` for each installed language and `SKIP` for a missing toolchain, never `FAIL`.

## Traps

- [ ] `java -version` output is read from stderr. A script that captures only stdout will think Java is broken.
- [ ] `Picked up JAVA_TOOL_OPTIONS: ...` is a harmless first line on machines where that variable is set. Ignore it; the version is on the next line.
- [ ] Version comparison is numeric, never lexical: `3.9` is older than `3.11`.
- [ ] A Go toolchain older than a module's `go` directive fails with `invalid go version` on `go.mod`. The fix is a newer Go, not an edit to `go.mod`.
- [ ] No lesson in this course needs network access at build or test time. If a build reaches for the network, the lesson is wrong.

## For a new team member

1. Clone the repository.
2. Run the preflight (any of the three implementations; the Python one needs only `python3`).
3. Fix what it reports.
4. Run `python3 scripts/run_lesson.py phases/00-setup-and-tooling/01-dev-environment`.
5. Record your language choice in `LEARNING.md` (the `start-learning` skill does this).
