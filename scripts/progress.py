#!/usr/bin/env python3
"""Generate PROGRESS.md: the build tracker for the whole repository.

Usage:
    python3 scripts/progress.py            # rewrite PROGRESS.md and print the summary line
    python3 scripts/progress.py --json     # machine-readable progress

Everything is derived from disk state (curriculum.json, lesson directories, languages.json,
projects/, skills/), so the tracker can never drift from reality. Run it after any batch of work.
"""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

from _lib import PROJECTS_DIR, ROOT, iter_lessons, lesson_languages, lesson_status, load_json, load_languages

GLYPH = {"Complete": "✅", "In Progress": "🚧", "Planned": "⬚"}
TRACKS = {"A: Data Structures & Algorithms": range(0, 5), "B: Distributed Systems": range(5, 10), "C: Payments Engineering": range(10, 16)}
EXPECTED_SKILLS = ["start-learning", "learn", "course-guide", "find-your-level", "check-understanding", "build-project", "learn-ledgers", "learn-consensus", "interview-prep"]


def bar(done: int, total: int, width: int = 20) -> str:
    filled = round(width * done / total) if total else 0
    return "█" * filled + "░" * (width - filled)


def collect() -> dict:
    langs = load_languages()
    phases: dict[int, dict] = {}
    lang_counts = {l["id"]: 0 for l in langs}
    build_total = 0
    for phase, lesson, path in iter_lessons():
        p = phases.setdefault(phase["number"], {"title": phase["title"], "slug": phase["slug"], "lessons": [], "minutes": 0})
        status = lesson_status(path, lesson["type"])
        present = [l["id"] for l in lesson_languages(path)]
        if lesson["type"] == "Build":
            build_total += 1
            for lid in present:
                lang_counts[lid] += 1
        p["lessons"].append({**lesson, "status": status, "languages": present, "path": path.relative_to(ROOT).as_posix()})
        p["minutes"] += lesson["minutes"]
    projects = []
    for folder in sorted(PROJECTS_DIR.iterdir()):
        manifest = folder / "project.json"
        if folder.name.startswith("_") or not manifest.is_file():
            continue
        meta = load_json(manifest)
        projects.append({"id": meta["id"], "level": meta.get("levelName", ""), "stages": len(meta["stages"]), "languages": meta.get("languages", []), "hasReadme": (folder / "README.md").is_file()})
    skills = {s: (ROOT / "skills" / s / "SKILL.md").is_file() and (ROOT / ".claude" / "skills" / s / "SKILL.md").is_file() for s in EXPECTED_SKILLS}
    try:
        head = subprocess.run(["git", "log", "-1", "--format=%h %s"], cwd=ROOT, capture_output=True, text=True).stdout.strip()
        dirty = subprocess.run(["git", "status", "--porcelain"], cwd=ROOT, capture_output=True, text=True).stdout.strip()
    except OSError:
        head, dirty = "", ""
    total = sum(len(p["lessons"]) for p in phases.values())
    complete = sum(1 for p in phases.values() for l in p["lessons"] if l["status"] == "Complete")
    return {
        "generatedAt": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "lessons": {"total": total, "complete": complete},
        "buildLessons": build_total,
        "languageCoverage": {l["id"]: {"name": l["name"], "ci": l.get("ci", False), "buildLessonsWithCode": lang_counts[l["id"]]} for l in langs},
        "phases": phases,
        "projects": projects,
        "skills": skills,
        "git": {"head": head, "uncommittedFiles": len(dirty.splitlines()) if dirty else 0},
    }


def render(data: dict) -> str:
    L = data["lessons"]
    out = [
        "# Progress Tracker",
        "",
        f"Generated {data['generatedAt']} by `python3 scripts/progress.py` from the repository state. "
        "Do not edit by hand; rerun the script after any batch of work.",
        "",
        "## Overall",
        "",
        f"**Lessons complete: {L['complete']} / {L['total']}**  `{bar(L['complete'], L['total'], 30)}` {100 * L['complete'] // L['total']}%",
        "",
        "| Area | Status |",
        "|---|---|",
    ]
    for lid, cov in data["languageCoverage"].items():
        flag = "primary, gates completion" if cov["ci"] else "port in progress, not gating"
        out.append(f"| {cov['name']} implementations | {cov['buildLessonsWithCode']} / {data['buildLessons']} Build lessons ({flag}) |")
    ready = sum(1 for p in data["projects"] if p["hasReadme"])
    out.append(f"| Projects | {ready} ready: " + ", ".join(f"`{p['id']}` ({p['stages']} stages, {'/'.join(p['languages'])})" for p in data["projects"]) + " |")
    skills_ok = sum(1 for v in data["skills"].values() if v)
    out.append(f"| Skills | {skills_ok} / {len(data['skills'])} present and mirrored to `.claude/skills/` |")
    out.append(f"| Git | HEAD `{data['git']['head']}`; {data['git']['uncommittedFiles']} uncommitted files |")
    out.append("")
    out.append("## By track and phase")
    out.append("")
    out.append("| Phase | Title | Complete | Progress | Hours |")
    out.append("|:---:|---|:---:|---|:---:|")
    for track, rng in TRACKS.items():
        t_done = sum(1 for n in rng for l in data["phases"][n]["lessons"] if l["status"] == "Complete")
        t_total = sum(len(data["phases"][n]["lessons"]) for n in rng)
        out.append(f"| | **Track {track}** | **{t_done} / {t_total}** | `{bar(t_done, t_total)}` | |")
        for n in rng:
            p = data["phases"][n]
            done = sum(1 for l in p["lessons"] if l["status"] == "Complete")
            out.append(f"| {n} | [{p['title']}](phases/{p['slug']}/) | {done} / {len(p['lessons'])} | `{bar(done, len(p['lessons']))}` | ~{p['minutes'] // 60} |")
    out.append("")
    out.append("## Remaining lessons")
    out.append("")
    remaining = [(n, l) for n, p in data["phases"].items() for l in p["lessons"] if l["status"] != "Complete"]
    if not remaining:
        out.append("None. Every lesson is complete.")
    else:
        out.append("| Phase | # | Lesson | Type | Status |")
        out.append("|:---:|:---:|---|:---:|:---:|")
        for n, l in remaining:
            out.append(f"| {n} | {l['n']:02d} | [{l['title']}]({l['path']}/) | {l['type']} | {GLYPH[l['status']]} {l['status']} |")
    out.append("")
    out.append("## Work log")
    out.append("")
    out.append("Batches are recorded in `PROGRESS-LOG.md` (hand-maintained, newest first).")
    return "\n".join(out) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()
    data = collect()
    if args.json:
        print(json.dumps(data, indent=2, default=str))
        return 0
    (ROOT / "PROGRESS.md").write_text(render(data), encoding="utf-8")
    L = data["lessons"]
    print(f"PROGRESS.md: {L['complete']}/{L['total']} lessons complete; {len(data['projects'])} projects; "
          f"{sum(data['skills'].values())}/{len(data['skills'])} skills")
    return 0


if __name__ == "__main__":
    sys.exit(main())
