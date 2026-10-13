"""Shared helpers for the curriculum scripts. Standard library only."""

from __future__ import annotations

import json
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
PHASES_DIR = ROOT / "phases"
PROJECTS_DIR = ROOT / "projects"
CURRICULUM_FILE = ROOT / "curriculum" / "curriculum.json"
LANGUAGES_FILE = ROOT / "languages.json"

SLUG_RE = re.compile(r"^[0-9]{2}-[a-z0-9][a-z0-9-]*[a-z0-9]$")


def load_json(path: Path) -> dict:
    with path.open(encoding="utf-8") as handle:
        return json.load(handle)


def load_curriculum() -> dict:
    return load_json(CURRICULUM_FILE)


def load_languages(ci_only: bool = False) -> list[dict]:
    langs = load_json(LANGUAGES_FILE)["languages"]
    return [lang for lang in langs if lang.get("ci") or not ci_only]


def language_by_id(lang_id: str) -> dict:
    for lang in load_languages():
        if lang["id"] == lang_id or lang["dir"] == lang_id or lang["name"].lower() == lang_id.lower():
            return lang
    raise KeyError(f"unknown language {lang_id!r}; known: {[l['id'] for l in load_languages()]}")


def lesson_dir(phase: dict, lesson: dict) -> Path:
    return PHASES_DIR / phase["slug"] / lesson["slug"]


def lesson_languages(path: Path) -> list[dict]:
    """Languages whose code folder exists with an entry file inside this lesson."""
    present = []
    for lang in load_languages():
        folder = path / "code" / lang["dir"]
        if (folder / lang["entry"]).is_file():
            present.append(lang)
    return present


def lesson_status(path: Path, lesson_type: str) -> str:
    """Complete, In Progress, or Planned based on what exists on disk."""
    docs = path / "docs" / "en.md"
    quiz = path / "quiz.json"
    if not docs.is_file():
        return "Planned"
    text = docs.read_text(encoding="utf-8")
    if re.search(r"^\*\*Status:\*\*\s*Planned", text, re.MULTILINE):
        return "Planned"
    if "TODO" in text:
        return "In Progress"
    if lesson_type in ("Learn", "Capstone"):
        return "Complete" if quiz.is_file() else "In Progress"
    required = {lang["id"] for lang in load_languages(ci_only=True)}
    have = set()
    for lang in lesson_languages(path):
        entry = path / "code" / lang["dir"] / lang["entry"]
        if "TODO: replace with the lesson" not in entry.read_text(encoding="utf-8"):
            have.add(lang["id"])
    if quiz.is_file() and required <= have:
        return "Complete"
    return "In Progress"


def iter_lessons():
    """Yield (phase, lesson, path) for every lesson in curriculum order."""
    curriculum = load_curriculum()
    for phase in curriculum["phases"]:
        for lesson in phase["lessons"]:
            yield phase, lesson, lesson_dir(phase, lesson)


def minutes_to_hours(minutes: int) -> str:
    hours = minutes / 60
    if hours < 1:
        return f"{minutes} min"
    return f"{hours:.0f} h" if hours == int(hours) else f"{hours:.1f} h"
