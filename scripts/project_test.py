#!/usr/bin/env python3
"""Grade staged projects cumulatively in the learner's chosen language.

Usage:
    python3 scripts/project_test.py ledger-engine --lang java --init my-ledger
    python3 scripts/project_test.py ledger-engine --lang java --stage 2 --path my-ledger
    python3 scripts/project_test.py ledger-engine --lang java --all --path my-ledger --strict
    python3 scripts/project_test.py ledger-engine --lang go --all --solution     # grade the reference
    python3 scripts/project_test.py --list

Layout (see projects/AUTHORING.md):
    projects/<id>/project.json
    projects/<id>/stages/<stage-id>/docs/en.md
    projects/<id>/stages/<stage-id>/starter/<lang>/      stage 1 only: files copied by --init
    projects/<id>/stages/<stage-id>/tests/<lang>/        test files run against the workspace
    projects/<id>/solution/<lang>/                        reference implementation

Grading stage N runs the tests of stages 1..N against the workspace, so new code that breaks an
earlier stage is reported as a regression. Each stage runs in a fresh temporary directory
containing the workspace sources plus that stage's test files; the workspace is never modified.

Exit codes: 0 all selected stages pass; 1 a stage failed (or --strict and something was skipped).
"""

from __future__ import annotations

import argparse
import hashlib
import json
import shutil
import subprocess
import sys
import tempfile
import time
from datetime import datetime, timezone
from pathlib import Path

from _lib import PROJECTS_DIR, ROOT, SLUG_RE, language_by_id, load_json, load_languages

SOURCE_SUFFIX = {"java": ".java", "python": ".py", "go": ".go"}


def load_project(project_id: str) -> tuple[Path, dict]:
    if not SLUG_RE.match(project_id) and not project_id.replace("-", "").isalnum():
        raise SystemExit(f"invalid project id {project_id!r}")
    root = PROJECTS_DIR / project_id
    manifest = root / "project.json"
    if not manifest.is_file():
        raise SystemExit(f"unknown project {project_id!r}; try --list")
    meta = load_json(manifest)
    if meta.get("id") != project_id or not meta.get("stages"):
        raise SystemExit(f"{manifest}: id must match the directory and stages must be non-empty")
    return root, meta


def list_projects() -> None:
    for folder in sorted(PROJECTS_DIR.iterdir()):
        manifest = folder / "project.json"
        if folder.name.startswith("_") or not manifest.is_file():
            continue
        meta = load_json(manifest)
        langs = ", ".join(meta.get("languages", []))
        print(f"{meta['id']:32} L{meta.get('level', '?')} {meta.get('levelName', ''):10} {len(meta['stages'])} stages  [{langs}]  {meta.get('tagline', '')}")


def copy_tree_files(src: Path, dst: Path) -> int:
    count = 0
    if not src.is_dir():
        return 0
    for item in src.rglob("*"):
        if item.is_file() and ".out" not in item.parts:
            target = dst / item.relative_to(src)
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(item, target)
            count += 1
    return count


def init_workspace(root: Path, meta: dict, lang: dict, workspace: Path) -> None:
    first = root / "stages" / meta["stages"][0]["id"] / "starter" / lang["dir"]
    if not first.is_dir():
        raise SystemExit(f"{meta['id']} has no {lang['name']} starter under {first.relative_to(ROOT)}")
    if workspace.exists() and any(workspace.iterdir()):
        raise SystemExit(f"{workspace} exists and is not empty; choose another --init path")
    workspace.mkdir(parents=True, exist_ok=True)
    count = copy_tree_files(first, workspace)
    print(f"initialized {workspace} with {count} {lang['name']} starter files")
    print(f"next: read {root.relative_to(ROOT)}/stages/{meta['stages'][0]['id']}/docs/en.md, then run")
    print(f"  python3 scripts/project_test.py {meta['id']} --lang {lang['id']} --stage 1 --path {workspace}")


def run(argv: list[str], cwd: Path, timeout: int) -> tuple[int, str]:
    try:
        proc = subprocess.run(argv, cwd=cwd, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        return 124, f"timed out after {timeout}s"
    return proc.returncode, (proc.stdout + proc.stderr)[-6000:]


def test_command(lang: dict, sandbox: Path) -> list[str]:
    if lang["id"] == "java":
        test_classes = sorted(p.stem for p in sandbox.glob("*Test.java"))
        runs = " && ".join(f"java -cp .out {cls}" for cls in test_classes) or "true"
        return ["sh", "-c", f"javac -d .out *.java && {runs}"]
    if lang["id"] == "python":
        return ["python3", "-m", "unittest", "-v"]
    if lang["id"] == "go":
        return ["go", "test", "./..."]
    return lang["test"]


def grade_stage(root: Path, meta: dict, lang: dict, stage: dict, source: Path, timeout: int, verbose: bool) -> dict:
    tests = root / "stages" / stage["id"] / "tests" / lang["dir"]
    if not tests.is_dir():
        return {"stage": stage["id"], "status": "skipped", "detail": f"no {lang['name']} tests"}
    started = time.perf_counter()
    with tempfile.TemporaryDirectory(prefix="pefs-grade-") as tmp:
        sandbox = Path(tmp)
        copied = 0
        for item in source.iterdir():
            if item.is_file() and item.suffix == SOURCE_SUFFIX[lang["id"]] and not item.stem.endswith("Test") and not item.name.startswith("test_") and not item.name.endswith("_test.go"):
                shutil.copy2(item, sandbox / item.name)
                copied += 1
            elif item.is_file() and item.name in {"go.mod", "go.sum"}:
                shutil.copy2(item, sandbox / item.name)
        if lang["id"] == "go" and not (sandbox / "go.mod").is_file():
            (sandbox / "go.mod").write_text(f"module pefs/{meta['id'].replace('-', '')}\n\ngo 1.22\n", encoding="utf-8")
        copy_tree_files(tests, sandbox)
        if copied == 0:
            return {"stage": stage["id"], "status": "fail", "detail": f"no {lang['name']} sources found in {source}"}
        code, output = run(test_command(lang, sandbox), sandbox, timeout)
    result = {"stage": stage["id"], "status": "pass" if code == 0 else "fail", "seconds": round(time.perf_counter() - started, 2), "detail": output.strip()}
    if verbose or code != 0:
        print(f"--- {stage['id']} [{lang['id']}] exit {code}\n{output.strip()}\n")
    return result


def workspace_fingerprint(source: Path, lang: dict) -> str:
    digest = hashlib.sha256()
    for item in sorted(source.rglob("*")):
        if item.is_file() and item.suffix == SOURCE_SUFFIX[lang["id"]]:
            digest.update(item.relative_to(source).as_posix().encode())
            digest.update(item.read_bytes())
    return digest.hexdigest()[:16]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("project", nargs="?")
    parser.add_argument("--list", action="store_true")
    parser.add_argument("--lang", help="java | python | go")
    parser.add_argument("--init", metavar="WORKSPACE", help="copy the stage-1 starter here")
    parser.add_argument("--path", metavar="WORKSPACE", help="learner workspace to grade")
    parser.add_argument("--stage", type=int, help="grade stages 1..N")
    parser.add_argument("--all", action="store_true", help="grade every stage")
    parser.add_argument("--solution", action="store_true", help="grade the reference solution instead of a workspace")
    parser.add_argument("--strict", action="store_true")
    parser.add_argument("--report", metavar="FILE", help="write a JSON completion report")
    parser.add_argument("--timeout", type=int, default=240)
    parser.add_argument("-v", "--verbose", action="store_true")
    args = parser.parse_args()

    if args.list:
        list_projects()
        return 0
    if not args.project:
        parser.error("project id required (or --list)")
    root, meta = load_project(args.project)
    if not args.lang:
        parser.error(f"--lang is required; this project supports {meta.get('languages')}")
    lang = language_by_id(args.lang)
    if lang["name"] not in meta.get("languages", []):
        raise SystemExit(f"{meta['id']} does not ship {lang['name']}; supported: {meta.get('languages')}")

    if args.init:
        init_workspace(root, meta, lang, Path(args.init).resolve())
        return 0

    if args.solution:
        source = root / "solution" / lang["dir"]
    elif args.path:
        source = Path(args.path).resolve()
    else:
        parser.error("give --path WORKSPACE, --solution, or --init WORKSPACE")
    if not source.is_dir():
        raise SystemExit(f"not a directory: {source}")

    stages = meta["stages"]
    upto = len(stages) if args.all or args.stage is None else args.stage
    if not 1 <= upto <= len(stages):
        raise SystemExit(f"--stage must be between 1 and {len(stages)}")
    results = []
    for stage in stages[:upto]:
        result = grade_stage(root, meta, lang, stage, source, args.timeout, args.verbose)
        results.append(result)
        mark = {"pass": "PASS", "fail": "FAIL", "skipped": "SKIP"}[result["status"]]
        print(f"{mark:5} stage {stage['id']} [{lang['id']}]")
        if result["status"] == "fail":
            break

    failed = [r for r in results if r["status"] == "fail" or (args.strict and r["status"] == "skipped")]
    passed = sum(1 for r in results if r["status"] == "pass")
    print(f"\n{passed}/{upto} stages pass in {lang['name']}" + ("" if not failed else " — fix the first failure and rerun"))

    if args.report:
        report = {
            "project": meta["id"],
            "language": lang["id"],
            "gradedAt": datetime.now(timezone.utc).isoformat(timespec="seconds"),
            "workspace": str(source),
            "referenceSolution": bool(args.solution),
            "eligibleForCompletion": bool(not args.solution and not failed and upto == len(stages)),
            "fingerprint": workspace_fingerprint(source, lang),
            "stages": results,
        }
        Path(args.report).write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        print(f"report written to {args.report}")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
