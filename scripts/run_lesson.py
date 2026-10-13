#!/usr/bin/env python3
"""Run a lesson's code and tests in one or every language.

Usage:
    python3 scripts/run_lesson.py phases/10-money-and-ledgers/03-double-entry-ledger
    python3 scripts/run_lesson.py phases/10-money-and-ledgers/03-double-entry-ledger --lang java
    python3 scripts/run_lesson.py --all                 # every complete lesson, every language
    python3 scripts/run_lesson.py --all --lang go       # every lesson that has Go code
    python3 scripts/run_lesson.py --all --json          # machine-readable report
    python3 scripts/run_lesson.py --phase 10            # one phase

For each selected language the canonical `run` and `test` commands from languages.json are
executed inside code/<lang>/. A lesson passes when both exit 0. Missing toolchains are
reported as `skipped`, never as failures, unless --strict is given.

Exit codes:
    0 — every selected run passed (or was skipped without --strict)
    1 — at least one failure
"""

from __future__ import annotations

import argparse
import json
import shutil
import subprocess
import sys
import time
from dataclasses import asdict, dataclass
from pathlib import Path

from _lib import ROOT, iter_lessons, language_by_id, lesson_languages, load_languages


@dataclass
class Result:
    lesson: str
    language: str
    phase: str
    status: str  # pass | fail | skipped
    seconds: float
    detail: str = ""


def toolchain_available(lang: dict) -> bool:
    return shutil.which(lang["versionCommand"][0]) is not None


def run_command(argv: list[str], cwd: Path, timeout: int) -> tuple[int, str]:
    try:
        proc = subprocess.run(argv, cwd=cwd, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        return 124, f"timed out after {timeout}s"
    output = (proc.stdout + proc.stderr).strip()
    return proc.returncode, output[-4000:]


def run_lesson_language(path: Path, lang: dict, timeout: int, verbose: bool) -> Result:
    rel = path.relative_to(ROOT).as_posix()
    folder = path / "code" / lang["dir"]
    if not (folder / lang["entry"]).is_file():
        return Result(rel, lang["id"], "", "skipped", 0.0, f"no {lang['dir']}/ implementation")
    if not toolchain_available(lang):
        return Result(rel, lang["id"], "", "skipped", 0.0, f"{lang['versionCommand'][0]} not installed")
    started = time.perf_counter()
    for phase_name, argv in (("run", lang["run"]), ("test", lang["test"])):
        code, output = run_command(argv, folder, timeout)
        if verbose:
            print(f"--- {rel} [{lang['id']}] {phase_name} (exit {code})\n{output}\n")
        if code != 0:
            return Result(rel, lang["id"], phase_name, "fail", time.perf_counter() - started, output)
    shutil.rmtree(folder / ".out", ignore_errors=True)
    return Result(rel, lang["id"], "run+test", "pass", time.perf_counter() - started)


def select_lessons(args) -> list[Path]:
    if args.lesson:
        path = (ROOT / args.lesson).resolve()
        if not path.is_dir():
            raise SystemExit(f"not a lesson directory: {args.lesson}")
        return [path]
    chosen = []
    for phase, lesson, path in iter_lessons():
        if args.phase is not None and phase["number"] != args.phase:
            continue
        if lesson_languages(path):
            chosen.append(path)
    return chosen


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("lesson", nargs="?", help="lesson directory relative to the repo root")
    parser.add_argument("--all", action="store_true")
    parser.add_argument("--phase", type=int)
    parser.add_argument("--lang", help="java | python | go (default: every language present)")
    parser.add_argument("--timeout", type=int, default=180)
    parser.add_argument("--strict", action="store_true", help="treat skipped toolchains as failures")
    parser.add_argument("--json", action="store_true")
    parser.add_argument("-v", "--verbose", action="store_true")
    args = parser.parse_args()
    if not (args.lesson or args.all or args.phase is not None):
        parser.error("give a lesson path, --all, or --phase N")

    languages = [language_by_id(args.lang)] if args.lang else load_languages()
    results: list[Result] = []
    for path in select_lessons(args):
        for lang in languages:
            result = run_lesson_language(path, lang, args.timeout, args.verbose)
            results.append(result)
            if not args.json:
                mark = {"pass": "PASS", "fail": "FAIL", "skipped": "SKIP"}[result.status]
                extra = f" ({result.detail.splitlines()[0][:80]})" if result.status != "pass" and result.detail else ""
                print(f"{mark:5} {result.lesson} [{result.language}] {result.seconds:5.1f}s{extra}")

    failed = [r for r in results if r.status == "fail" or (args.strict and r.status == "skipped")]
    if args.json:
        print(json.dumps([asdict(r) for r in results], indent=2))
    else:
        passed = sum(1 for r in results if r.status == "pass")
        skipped = sum(1 for r in results if r.status == "skipped")
        print(f"\n{passed} passed, {len(failed)} failed, {skipped} skipped")
        for r in failed:
            print(f"\n=== {r.lesson} [{r.language}] {r.phase} ===\n{r.detail}")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
