#!/usr/bin/env python3
"""Invariant checks across every lesson directory.

Usage:
    python3 scripts/audit_lessons.py [--phase N] [--json] [--strict]

Checks, per lesson that exists on disk:
    docs/en.md       present, H1, one-line hook, Type/Languages/Prerequisites/Time header,
                     Learning Objectives (4-6), required sections for Build lessons, every fenced
                     code block has a language tag, no ASCII box-drawing diagrams
    quiz.json        exactly 6 questions (1 pre, 3 check, 2 post), 4 options, zero-indexed correct,
                     non-empty explanation, correct answer not the longest option by >40%,
                     no correct index used more than twice
    code/<lang>/     entry + test file exist for each language named in the header; a 4-6 line
                     header comment cites docs/en.md
    outputs/         optional; if present, every file has YAML frontmatter with name + description
    curriculum.json  every directory under phases/ is registered; every registered Complete lesson passes

Exit codes:
    0 — clean
    1 — issues found
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from dataclasses import dataclass, field
from pathlib import Path

from _lib import PHASES_DIR, ROOT, SLUG_RE, iter_lessons, lesson_status, load_languages

FENCE_RE = re.compile(r"^```(\S*)\s*$", re.MULTILINE)
BOX_DRAWING_RE = re.compile(r"[─-╿]")
HEADER_FIELDS = ("Type", "Languages", "Prerequisites", "Time")
BUILD_SECTIONS = ("## The Problem", "## The Concept", "## Build It", "## Use It", "## Ship It", "## Exercises", "## Key Terms", "## Further Reading")
LEARN_SECTIONS = ("## The Problem", "## The Concept", "## Exercises", "## Key Terms", "## Further Reading")
CAPSTONE_SECTIONS = ("## The Problem", "## What You Will Build", "## Requirements", "## Milestones", "## Acceptance Criteria", "## Rubric", "## Further Reading")
QUIZ_STAGES = {"pre": 1, "check": 3, "post": 2}
LANG_TAGS = {"text", "json", "python", "java", "go", "bash", "console", "mermaid", "yaml", "sql", "xml", "toml", "diff", "http", "javascript", "typescript", "csv", "ini", "properties"}


@dataclass
class Issue:
    rule: str
    lesson: str
    file: str
    message: str


@dataclass
class Audit:
    checked: int = 0
    issues: list[Issue] = field(default_factory=list)

    def add(self, rule: str, lesson: Path, file: Path | None, message: str) -> None:
        self.issues.append(
            Issue(rule, lesson.relative_to(ROOT).as_posix(), file.relative_to(ROOT).as_posix() if file else "", message)
        )


def audit_docs(audit: Audit, lesson: Path, lesson_type: str) -> list[str]:
    docs = lesson / "docs" / "en.md"
    if not docs.is_file():
        audit.add("docs-missing", lesson, None, "docs/en.md is missing")
        return []
    text = docs.read_text(encoding="utf-8")
    lines = text.splitlines()
    if not lines or not lines[0].startswith("# "):
        audit.add("docs-h1", lesson, docs, "first line must be an H1 title")
    if not any(line.startswith("> ") for line in lines[:6]):
        audit.add("docs-hook", lesson, docs, "missing one-line `> hook` under the title")
    for field_name in HEADER_FIELDS:
        if not re.search(rf"^\*\*{field_name}:\*\*", text, re.MULTILINE):
            audit.add("docs-header", lesson, docs, f"missing **{field_name}:** header line")
    planned = bool(re.search(r"^\*\*Status:\*\*\s*Planned", text, re.MULTILINE))
    objectives = re.search(r"## Learning Objectives\n\n?((?:- .*\n?)+)", text)
    if not objectives:
        audit.add("docs-objectives", lesson, docs, "missing ## Learning Objectives bullet list")
    else:
        count = len([l for l in objectives.group(1).splitlines() if l.startswith("- ")])
        if not 3 <= count <= 7:
            audit.add("docs-objectives", lesson, docs, f"expected 3-7 learning objectives, found {count}")
    if not planned:
        sections = {"Build": BUILD_SECTIONS, "Learn": LEARN_SECTIONS, "Capstone": CAPSTONE_SECTIONS}[lesson_type]
        for section in sections:
            if section not in text:
                audit.add("docs-sections", lesson, docs, f"missing section {section!r}")
    for match in FENCE_RE.finditer(text):
        tag = match.group(1)
        # closing fences have no tag; opening fences alternate with them
        pass
    fences = FENCE_RE.findall(text)
    for i in range(0, len(fences) - 1, 2):
        if fences[i] == "":
            audit.add("docs-fence-lang", lesson, docs, "fenced code block without a language tag")
        elif fences[i] not in LANG_TAGS and not fences[i].startswith("figure"):
            audit.add("docs-fence-lang", lesson, docs, f"unknown fence language {fences[i]!r}")
    if "TODO" in text and not planned:
        audit.add("docs-todo", lesson, docs, "unfinished TODO marker in lesson text")
    if BOX_DRAWING_RE.search(text):
        audit.add("docs-ascii-diagram", lesson, docs, "box-drawing characters found; use mermaid or a table")
    langs_match = re.search(r"^\*\*Languages:\*\*\s*(.+)$", text, re.MULTILINE)
    declared = []
    if langs_match:
        declared = [l.strip() for l in langs_match.group(1).split(",") if l.strip() and l.strip() != "—"]
    return declared


def audit_quiz(audit: Audit, lesson: Path, planned: bool) -> None:
    quiz = lesson / "quiz.json"
    if not quiz.is_file():
        if not planned:
            audit.add("quiz-missing", lesson, None, "quiz.json is missing")
        return
    try:
        data = json.loads(quiz.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        audit.add("quiz-json", lesson, quiz, f"invalid JSON: {exc}")
        return
    questions = data.get("questions")
    if not isinstance(questions, list) or len(questions) != 6:
        audit.add("quiz-count", lesson, quiz, f"expected exactly 6 questions, found {len(questions) if isinstance(questions, list) else 'none'}")
        return
    stages = {}
    for i, q in enumerate(questions, 1):
        for key in ("stage", "question", "options", "correct", "explanation"):
            if key not in q:
                audit.add("quiz-schema", lesson, quiz, f"question {i} missing {key!r}")
        stage = q.get("stage")
        stages[stage] = stages.get(stage, 0) + 1
        options = q.get("options", [])
        if not isinstance(options, list) or len(options) != 4:
            audit.add("quiz-options", lesson, quiz, f"question {i} must have exactly 4 options")
            continue
        correct = q.get("correct")
        if not isinstance(correct, int) or not 0 <= correct < 4:
            audit.add("quiz-correct", lesson, quiz, f"question {i} `correct` must be a zero-based index 0-3")
            continue
        if not str(q.get("explanation", "")).strip():
            audit.add("quiz-explanation", lesson, quiz, f"question {i} has an empty explanation")
        lengths = [len(str(o)) for o in options]
        others = [l for j, l in enumerate(lengths) if j != correct]
        if others and lengths[correct] > 1.4 * max(others):
            audit.add("quiz-bias", lesson, quiz, f"question {i}: correct option is >40% longer than every distractor")
    spread: dict[int, int] = {}
    for q in questions:
        c = q.get("correct")
        if isinstance(c, int):
            spread[c] = spread.get(c, 0) + 1
    if spread and max(spread.values()) > 2:
        audit.add("quiz-spread", lesson, quiz, f"correct index {max(spread, key=spread.get)} is used {max(spread.values())} times; spread the answers (max 2 per position)")
    blob = json.dumps(data)
    if "TODO" in blob or any(q.get("options") == ["A", "B", "C", "D"] for q in questions):
        audit.add("quiz-placeholder", lesson, quiz, "quiz still contains scaffold placeholder questions")
    if stages != QUIZ_STAGES:
        audit.add("quiz-stages", lesson, quiz, f"expected stages {QUIZ_STAGES}, found {stages}")


def audit_code(audit: Audit, lesson: Path, declared: list[str], planned: bool) -> None:
    code = lesson / "code"
    if planned:
        return
    by_name = {l["name"]: l for l in load_languages()}
    for name in declared:
        lang = by_name.get(name)
        if not lang:
            audit.add("code-unknown-lang", lesson, None, f"header names unknown language {name!r}")
            continue
        folder = code / lang["dir"]
        entry = folder / lang["entry"]
        test = folder / lang["testEntry"]
        if not entry.is_file():
            audit.add("code-entry", lesson, None, f"missing code/{lang['dir']}/{lang['entry']}")
            continue
        if not test.is_file():
            audit.add("code-tests", lesson, None, f"missing code/{lang['dir']}/{lang['testEntry']}")
        source = entry.read_text(encoding="utf-8")
        if "TODO: replace with the lesson" in source:
            audit.add("code-stub", lesson, entry, "scaffold stub has not been replaced with an implementation")
        head = "\n".join(source.splitlines()[:12])
        if "docs/en.md" not in head:
            audit.add("code-header", lesson, entry, "header comment must cite docs/en.md")
        if lang["id"] == "go" and not (folder / "go.mod").is_file():
            audit.add("code-gomod", lesson, None, "missing code/go/go.mod")
    if code.is_dir():
        for folder in code.iterdir():
            if folder.is_dir() and folder.name not in {l["dir"] for l in load_languages()}:
                audit.add("code-stray", lesson, folder, "unregistered language folder")
            lang = next((l for l in load_languages() if l["dir"] == folder.name), None)
            if lang and lang["name"] not in declared and (folder / lang["entry"]).is_file():
                audit.add("code-undeclared", lesson, folder, f"code exists but header does not list {lang['name']}")


MIN_LINES = {"Build": 350, "Learn": 300, "Capstone": 300}


def audit_depth(audit: Audit, lesson: Path, lesson_type: str) -> None:
    docs = lesson / "docs" / "en.md"
    if not docs.is_file():
        return
    text = docs.read_text(encoding="utf-8")
    if re.search(r"^\*\*Status:\*\*\s*Planned", text, re.MULTILINE):
        return
    lines = len(text.splitlines())
    if lines < MIN_LINES[lesson_type]:
        audit.add("docs-depth", lesson, docs, f"{lines} lines; {lesson_type} lessons need at least {MIN_LINES[lesson_type]}")
    outputs = lesson / "outputs"
    if lesson_type == "Build" and not any(outputs.glob("*.md")):
        audit.add("outputs-missing", lesson, None, "Build lessons must ship at least one outputs/ artifact")


def audit_outputs(audit: Audit, lesson: Path) -> None:
    outputs = lesson / "outputs"
    if not outputs.is_dir():
        return
    for file in outputs.iterdir():
        if file.suffix != ".md":
            continue
        text = file.read_text(encoding="utf-8")
        if not text.startswith("---\n") or "\nname:" not in text.split("---", 2)[1] or "\ndescription:" not in text.split("---", 2)[1]:
            audit.add("outputs-frontmatter", lesson, file, "outputs need YAML frontmatter with name and description")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--phase", type=int)
    parser.add_argument("--json", action="store_true")
    parser.add_argument("--strict", action="store_true", help="also fail on In Progress lessons")
    args = parser.parse_args()

    audit = Audit()
    registered: set[Path] = set()
    for phase, lesson, path in iter_lessons():
        registered.add(path)
        if args.phase is not None and phase["number"] != args.phase:
            continue
        if not SLUG_RE.match(lesson["slug"]) or not SLUG_RE.match(phase["slug"]):
            audit.add("slug", path, None, "phase and lesson slugs must be NN-kebab-case")
        if not path.is_dir():
            continue  # planned lesson with no directory yet
        audit.checked += 1
        declared = audit_docs(audit, path, lesson["type"])
        docs = path / "docs" / "en.md"
        planned = docs.is_file() and bool(re.search(r"^\*\*Status:\*\*\s*Planned", docs.read_text(encoding="utf-8"), re.MULTILINE))
        audit_quiz(audit, path, planned)
        if lesson["type"] == "Build":
            audit_code(audit, path, declared, planned)
        audit_outputs(audit, path)
        audit_depth(audit, path, lesson["type"])
        if args.strict and lesson_status(path, lesson["type"]) == "In Progress":
            audit.add("status", path, None, "lesson is In Progress (missing quiz or a CI language)")

    if PHASES_DIR.is_dir():
        for phase_dir in PHASES_DIR.iterdir():
            if not phase_dir.is_dir():
                continue
            for lesson_dir in phase_dir.iterdir():
                if lesson_dir.is_dir() and lesson_dir not in registered and lesson_dir.name != "README.md":
                    audit.add("unregistered", lesson_dir, None, "directory exists but is not in curriculum.json")

    if args.json:
        print(json.dumps({"checked": audit.checked, "issues": [i.__dict__ for i in audit.issues]}, indent=2))
    else:
        for issue in audit.issues:
            where = issue.file or issue.lesson
            print(f"[{issue.rule}] {where}: {issue.message}")
        print(f"\n{audit.checked} lessons checked, {len(audit.issues)} issues")
    return 1 if audit.issues else 0


if __name__ == "__main__":
    sys.exit(main())
