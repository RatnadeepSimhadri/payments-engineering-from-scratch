#!/usr/bin/env python3
"""Mirror skills/ into .claude/skills/ so Claude Code discovers the same portable skills.

Usage:
    python3 scripts/sync_skills.py           # copy skills/ -> .claude/skills/
    python3 scripts/sync_skills.py --check   # exit 1 if the two trees differ
"""

from __future__ import annotations

import argparse
import filecmp
import shutil
import sys
from pathlib import Path

from _lib import ROOT

SRC = ROOT / "skills"
DST = ROOT / ".claude" / "skills"


def differences() -> list[str]:
    diffs: list[str] = []
    for src_file in SRC.rglob("*"):
        if src_file.is_dir():
            continue
        dst_file = DST / src_file.relative_to(SRC)
        if not dst_file.is_file() or not filecmp.cmp(src_file, dst_file, shallow=False):
            diffs.append(src_file.relative_to(SRC).as_posix())
    for dst_file in DST.rglob("*"):
        if dst_file.is_file() and not (SRC / dst_file.relative_to(DST)).is_file():
            diffs.append(f"(extra) {dst_file.relative_to(DST).as_posix()}")
    return diffs


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    diffs = differences()
    if args.check:
        for d in diffs:
            print(f"drift: {d}")
        print("skills in sync" if not diffs else f"{len(diffs)} files drifted; run python3 scripts/sync_skills.py")
        return 1 if diffs else 0
    if DST.exists():
        shutil.rmtree(DST)
    shutil.copytree(SRC, DST)
    print(f"copied {sum(1 for p in SRC.rglob('*') if p.is_file())} files to {DST.relative_to(ROOT)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
