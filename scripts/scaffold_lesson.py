#!/usr/bin/env python3
"""Scaffold a lesson directory from curriculum.json in every CI language.

Usage:
    python3 scripts/scaffold_lesson.py 10 3            # phase 10, lesson 3
    python3 scripts/scaffold_lesson.py 10 3 --planned  # docs outline only, marked Planned
    python3 scripts/scaffold_lesson.py --all-planned   # outline docs for every lesson with no directory

Creates docs/en.md, quiz.json, code/<lang>/ stubs with the canonical entry and test files, and an
empty outputs/. The stubs compile and pass so the audit and runner are green from the first commit;
replace the TODO bodies with the real implementation.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from _lib import ROOT, iter_lessons, lesson_dir, load_curriculum, load_languages

DOCS_TEMPLATE = """# {title}

> {hook}

**Type:** {type}
**Languages:** {languages}
**Prerequisites:** {prereqs}
**Time:** ~{minutes} minutes
{status_line}
## Learning Objectives

- Explain the problem this lesson solves and where it appears in a payment system
- Build the core mechanism from scratch in your chosen language
- Measure or test the invariant the mechanism must preserve
- Compare the from-scratch version with the standard-library or production equivalent

## The Problem

TODO: two or three paragraphs. A concrete scenario where not knowing this costs money or uptime.

## The Concept

TODO: the mental model, with a mermaid diagram or table. No code yet.

## Build It

### Step 1: TODO

```{code_tag}
// TODO
```

## Use It

TODO: the standard-library or production equivalent, and what it hides.

## Ship It

TODO: the reusable artifact in outputs/.

## Exercises

1. TODO (reinforce)
2. TODO (apply)
3. TODO (extend)

## Key Terms

| Term | What people say | What it actually means |
|------|-----------------|------------------------|
| TODO | | |

## Further Reading

- TODO
"""

PLANNED_TEMPLATE = """# {title}

> {hook}

**Type:** {type}
**Languages:** —
**Prerequisites:** See the phase README
**Time:** ~{minutes} minutes
**Status:** Planned

## Learning Objectives

- Explain the problem this lesson solves and where it appears in a payment or distributed system
- Build or trace the core mechanism in Java, Python, or Go
- Verify the invariant the mechanism must preserve with a test or a worked example
- Connect the mechanism to the production system that depends on it

## Outline

This lesson is registered in `curriculum/curriculum.json` and waiting for an author. Follow
`AGENTS.md` and `LESSON_TEMPLATE.md`, then run `python3 scripts/scaffold_lesson.py {phase} {n}` to
replace this outline with the full skeleton. The hook above states the lesson's thesis; the
objectives state what a finished lesson must deliver.
"""

QUIZ_TEMPLATE = {
    "lesson": "",
    "title": "",
    "questions": [
        {"stage": "pre", "question": "TODO", "options": ["A", "B", "C", "D"], "correct": 0, "explanation": "TODO"},
        {"stage": "check", "question": "TODO", "options": ["A", "B", "C", "D"], "correct": 1, "explanation": "TODO"},
        {"stage": "check", "question": "TODO", "options": ["A", "B", "C", "D"], "correct": 2, "explanation": "TODO"},
        {"stage": "check", "question": "TODO", "options": ["A", "B", "C", "D"], "correct": 3, "explanation": "TODO"},
        {"stage": "post", "question": "TODO", "options": ["A", "B", "C", "D"], "correct": 0, "explanation": "TODO"},
        {"stage": "post", "question": "TODO", "options": ["A", "B", "C", "D"], "correct": 1, "explanation": "TODO"},
    ],
}

JAVA_MAIN = """// Lesson: {rel}/docs/en.md
// {title}
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Java 21, standard library only.

public final class Main {{
    static int placeholder(int x) {{
        return x; // TODO: replace with the lesson's implementation
    }}

    public static void main(String[] args) {{
        System.out.println("{title}: " + placeholder(42));
    }}
}}
"""

JAVA_TEST = """// Tests for {rel}. Standard-library test harness: run with `java -cp .out MainTest`.
import java.util.ArrayList;
import java.util.List;

public final class MainTest {{
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {{
        if (condition) passed++; else failures.add(name);
    }}

    public static void main(String[] args) {{
        check("placeholder returns input", Main.placeholder(7) == 7);
        check("placeholder handles zero", Main.placeholder(0) == 0);
        check("placeholder handles negatives", Main.placeholder(-3) == -3);
        check("placeholder is deterministic", Main.placeholder(5) == Main.placeholder(5));
        check("placeholder large", Main.placeholder(1_000_000) == 1_000_000);
        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }}
}}
"""

PY_MAIN = '''"""Lesson: {rel}/docs/en.md
{title}
Run:  python3 main.py
Test: python3 -m unittest -v
Python 3.11+, standard library only.
"""


def placeholder(x: int) -> int:
    return x  # TODO: replace with the lesson's implementation


if __name__ == "__main__":
    print("{title}:", placeholder(42))
'''

PY_TEST = '''"""Tests for {rel}."""
import unittest

from main import placeholder


class PlaceholderTest(unittest.TestCase):
    def test_returns_input(self):
        self.assertEqual(placeholder(7), 7)

    def test_zero(self):
        self.assertEqual(placeholder(0), 0)

    def test_negative(self):
        self.assertEqual(placeholder(-3), -3)

    def test_deterministic(self):
        self.assertEqual(placeholder(5), placeholder(5))

    def test_large(self):
        self.assertEqual(placeholder(1_000_000), 1_000_000)


if __name__ == "__main__":
    unittest.main()
'''

GO_MAIN = """// Lesson: {rel}/docs/en.md
// {title}
// Run:  go run .
// Test: go test ./...
// Go 1.22+, standard library only.
package main

import "fmt"

func placeholder(x int) int {{
	return x // TODO: replace with the lesson's implementation
}}

func main() {{
	fmt.Println("{title}:", placeholder(42))
}}
"""

GO_TEST = """package main

import "testing"

func TestPlaceholderReturnsInput(t *testing.T) {{
	if placeholder(7) != 7 {{
		t.Fatal("expected 7")
	}}
}}

func TestPlaceholderZero(t *testing.T) {{
	if placeholder(0) != 0 {{
		t.Fatal("expected 0")
	}}
}}

func TestPlaceholderNegative(t *testing.T) {{
	if placeholder(-3) != -3 {{
		t.Fatal("expected -3")
	}}
}}

func TestPlaceholderDeterministic(t *testing.T) {{
	if placeholder(5) != placeholder(5) {{
		t.Fatal("expected determinism")
	}}
}}

func TestPlaceholderLarge(t *testing.T) {{
	if placeholder(1000000) != 1000000 {{
		t.Fatal("expected 1000000")
	}}
}}
"""

GO_MOD = "module {module}\n\ngo 1.22\n"


def write(path: Path, content: str, force: bool) -> None:
    if path.exists() and not force:
        return
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding="utf-8")


def scaffold(phase: dict, lesson: dict, planned: bool, force: bool) -> Path:
    path = lesson_dir(phase, lesson)
    rel = path.relative_to(ROOT).as_posix()
    langs = load_languages(ci_only=True)
    if planned or lesson["type"] == "Learn" and planned:
        write(path / "docs" / "en.md", PLANNED_TEMPLATE.format(phase=phase["number"], **lesson), force)
        return path
    prereqs = "None" if lesson["n"] == 1 else f"Phase {phase['number']}, Lesson {lesson['n'] - 1:02d}"
    write(
        path / "docs" / "en.md",
        DOCS_TEMPLATE.format(
            languages=", ".join(l["name"] for l in langs) if lesson["type"] == "Build" else "—",
            prereqs=prereqs,
            status_line="",
            code_tag="java",
            **lesson,
        ),
        force,
    )
    quiz = dict(QUIZ_TEMPLATE)
    quiz["lesson"] = lesson["slug"]
    quiz["title"] = lesson["title"]
    write(path / "quiz.json", json.dumps(quiz, indent=2) + "\n", force)
    (path / "outputs").mkdir(parents=True, exist_ok=True)
    if lesson["type"] == "Build":
        fmt = {"rel": rel, "title": lesson["title"]}
        write(path / "code" / "java" / "Main.java", JAVA_MAIN.format(**fmt), force)
        write(path / "code" / "java" / "MainTest.java", JAVA_TEST.format(**fmt), force)
        write(path / "code" / "python" / "main.py", PY_MAIN.format(**fmt), force)
        write(path / "code" / "python" / "test_main.py", PY_TEST.format(**fmt), force)
        write(path / "code" / "go" / "main.go", GO_MAIN.format(**fmt), force)
        write(path / "code" / "go" / "main_test.go", GO_TEST.format(**fmt), force)
        module = f"pefs/{phase['slug']}/{lesson['slug']}".replace("-", "")
        write(path / "code" / "go" / "go.mod", GO_MOD.format(module=module), force)
    return path


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("phase", nargs="?", type=int)
    parser.add_argument("lesson", nargs="?", type=int)
    parser.add_argument("--planned", action="store_true", help="write only a Planned outline")
    parser.add_argument("--all-planned", action="store_true", help="outline every lesson that has no directory")
    parser.add_argument("--force", action="store_true", help="overwrite existing files")
    args = parser.parse_args()

    if args.all_planned:
        created = 0
        for phase, lesson, path in iter_lessons():
            if not path.exists():
                scaffold(phase, lesson, planned=True, force=False)
                created += 1
        print(f"outlined {created} planned lessons")
        return 0

    if args.phase is None or args.lesson is None:
        parser.error("give <phase> <lesson> or --all-planned")
    curriculum = load_curriculum()
    phase = next((p for p in curriculum["phases"] if p["number"] == args.phase), None)
    if not phase:
        raise SystemExit(f"no phase {args.phase}")
    lesson = next((l for l in phase["lessons"] if l["n"] == args.lesson), None)
    if not lesson:
        raise SystemExit(f"no lesson {args.lesson} in phase {args.phase}")
    path = scaffold(phase, lesson, planned=args.planned, force=args.force)
    print(f"scaffolded {path.relative_to(ROOT)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
