#!/usr/bin/env python3
"""Check that every Build lesson has a step-by-step walkthrough in every CI language.

Contract (AGENTS.md, "Build It"): the Java steps are `### Step N: ...` headings under `## Build It`.
`### In Python` and `### In Go` each contain the same steps as `#### Step N: <same title>` with a
fenced code block in that language whose identifiers exist in `code/<lang>/`, plus prose.

    python3 scripts/check_walkthroughs.py            # all Build lessons
    python3 scripts/check_walkthroughs.py --phase 8  # one phase
    python3 scripts/check_walkthroughs.py phases/08-messaging-and-streams/02-consumer-groups-and-rebalancing

Exit 1 when any lesson fails. Standard library only.
"""
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
LANGS = {"python": ("In Python", "python", "code/python"), "go": ("In Go", "go", "code/go")}
STEP_RE = re.compile(r"^(#{3,4}) Step (\d+[a-z]?)[:.]\s*(.+?)\s*$")
IDENT_RE = re.compile(r"\b[A-Za-z_][A-Za-z0-9_]{3,}\b")
NOISE = {"self", "None", "True", "False", "return", "func", "package", "import", "from", "class", "else", "elif",
         "while", "with", "range", "print", "defer", "error", "string", "int64", "bool", "nil", "make", "append",
         "len", "errors", "fmt", "main", "test", "type", "struct", "interface", "const", "break", "continue",
         "pass", "raise", "yield", "lambda", "assert", "async", "await", "global", "except", "finally", "chan",
         "select", "switch", "case", "default", "goto", "fallthrough", "float64", "uint64", "int32", "byte",
         "rune", "iota", "var", "map", "sync", "time", "math", "sort", "strings", "strconv"}


def sections(md: str) -> dict:
    """Split the Build It section into: java steps, and per-language blocks with their steps and fences."""
    lines = md.split("\n")
    in_fence = False
    build = False
    current = "java"
    out = {"java": {"steps": [], "fences": []}, "python": {"steps": [], "fences": [], "present": False},
           "go": {"steps": [], "fences": [], "present": False}}
    fence_lang = None
    fence_buf: list[str] = []
    for line in lines:
        if line.startswith("```"):
            if not in_fence:
                in_fence = True
                fence_lang = line[3:].strip().split()[0] if line[3:].strip() else ""
                fence_buf = []
            else:
                in_fence = False
                if build:
                    out[current]["fences"].append((fence_lang, "\n".join(fence_buf)))
            continue
        if in_fence:
            fence_buf.append(line)
            continue
        if line.startswith("## "):
            build = line.strip() == "## Build It"
            current = "java"
            continue
        if not build:
            continue
        if line.startswith("### In Python"):
            current = "python"; out["python"]["present"] = True; continue
        if line.startswith("### In Go"):
            current = "go"; out["go"]["present"] = True; continue
        if line.startswith("### ") and not STEP_RE.match(line):
            if current != "java":
                current = "java"  # another ### after the language blocks (rare)
            continue
        m = STEP_RE.match(line)
        if m:
            out[current]["steps"].append((m.group(2), m.group(3)))
    return out


def identifiers(code: str) -> set[str]:
    return {w for w in IDENT_RE.findall(code) if w not in NOISE}


def check_lesson(path: Path, langs: list[str]) -> list[str]:
    problems: list[str] = []
    md_path = path / "docs" / "en.md"
    if not md_path.is_file():
        return [f"{path}: docs/en.md missing"]
    md = md_path.read_text(encoding="utf-8", errors="replace")
    s = sections(md)
    java_steps = s["java"]["steps"]
    if not java_steps:
        return [f"{path}: no `### Step N:` headings under Build It"]
    java_titles = [t.lower() for _, t in java_steps]
    for lang in langs:
        label, fence_tag, code_dir = LANGS[lang]
        if not (path / code_dir).is_dir():
            continue  # port not shipped; nothing to check
        block = s[lang]
        if not block["present"]:
            problems.append(f"{path}: no `### {label}` section")
            continue
        steps = block["steps"]
        if len(steps) != len(java_steps):
            problems.append(f"{path}: `{label}` has {len(steps)} steps, Java has {len(java_steps)}")
        titles = [t.lower() for _, t in steps]
        for n, t in steps:
            if t.lower() not in java_titles:
                problems.append(f"{path}: `{label}` step {n} title {t!r} does not match any Java step")
        fences = [c for tag, c in block["fences"] if tag == fence_tag]
        if len(fences) < max(1, len(java_steps) - 1):
            problems.append(f"{path}: `{label}` has {len(fences)} ```{fence_tag} blocks for {len(java_steps)} steps")
        wrong = [tag for tag, _ in block["fences"] if tag not in (fence_tag, "text", "console", "bash", "")]
        if wrong:
            problems.append(f"{path}: `{label}` contains non-{fence_tag} code fences: {sorted(set(wrong))}")
        code = ""
        for f in (path / code_dir).rglob("*"):
            if f.suffix in (".py", ".go") and f.is_file():
                code += f.read_text(encoding="utf-8", errors="replace") + "\n"
        code_ids = identifiers(code)
        for i, chunk in enumerate(fences, 1):
            ids = identifiers(chunk)
            if not ids:
                continue
            hit = len(ids & code_ids) / len(ids)
            if hit < 0.6:
                problems.append(f"{path}: `{label}` chunk {i}: only {hit:.0%} of its identifiers exist in {code_dir}/")
    return problems


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("paths", nargs="*")
    ap.add_argument("--phase", type=int)
    ap.add_argument("--lang", choices=sorted(LANGS), action="append")
    args = ap.parse_args()
    langs = args.lang or sorted(LANGS)
    if args.paths:
        lessons = [ROOT / p for p in args.paths]
    else:
        lessons = sorted(p.parent.parent for p in (ROOT / "phases").glob("*/*/code/java"))
        if args.phase is not None:
            lessons = [p for p in lessons if p.parent.name.startswith(f"{args.phase:02d}-")]
    problems: list[str] = []
    for lesson in lessons:
        problems.extend(check_lesson(lesson, langs))
    for p in problems:
        print(p)
    print(f"{len(lessons)} Build lessons checked, {len(problems)} problem(s)")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
