"""Lesson: phases/00-setup-and-tooling/01-dev-environment/docs/en.md
Dev Environment: Java 21, Python 3.12, Go 1.22 -- a preflight checker
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: JEP 223 (JDK version-string scheme); PEP 440 (Python version identifiers); go.dev/doc/toolchain.
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

import re
import subprocess
from dataclasses import dataclass
from enum import Enum
from pathlib import Path
from typing import Callable, Optional

_LEADING = re.compile(r"^(\d+)(?:\.(\d+))?(?:\.(\d+))?")


@dataclass(frozen=True, order=True)
class Version:
    """A three-part version. Pre-release suffixes ("-ea", "rc1", "_292") are dropped."""

    major: int
    minor: int
    patch: int

    @staticmethod
    def of(major: int, minor: int, patch: int) -> "Version":
        return Version(major, minor, patch)

    @staticmethod
    def parse(text: Optional[str]) -> Optional["Version"]:
        """"21.0.2" -> 21.0.2; "21-ea" -> 21.0.0; "3.13.0rc1" -> 3.13.0; "1.22" -> 1.22.0. Missing parts are zero."""
        if text is None:
            return None
        m = _LEADING.match(text.strip())
        if not m:
            return None
        major, minor, patch = (int(g) if g is not None else 0 for g in m.groups())
        return Version(major, minor, patch)

    def at_least(self, minimum: "Version") -> bool:
        return self >= minimum  # dataclass(order=True) compares field by field, numerically

    def __str__(self) -> str:
        return f"{self.major}.{self.minor}.{self.patch}"


class Status(Enum):
    """Outcome of one check, in order of severity."""

    OK = "OK"
    TOO_OLD = "TOO_OLD"
    UNPARSEABLE = "UNPARSEABLE"
    MISSING = "MISSING"


@dataclass(frozen=True)
class Toolchain:
    """One executable the course depends on, how to ask it for its version, and how to read the answer."""

    name: str
    argv: tuple[str, ...]
    minimum: Version
    version_pattern: re.Pattern[str]
    fix: str

    def parse_version(self, output: str) -> Optional[Version]:
        """Extract the version text from combined stdout+stderr, ignoring noise lines."""
        m = self.version_pattern.search(output)
        return Version.parse(m.group(1)) if m else None


@dataclass(frozen=True)
class CheckResult:
    name: str
    status: Status
    found: Optional[Version]
    detail: str

    def ok(self) -> bool:
        return self.status is Status.OK

    def formatted(self) -> str:
        version = str(self.found) if self.found else "-"
        return f"{self.status.value:<11} {self.name:<27} {version:<9} {self.detail}"


# A runner takes argv and returns the combined output, or None when the executable does not exist.
CommandRunner = Callable[[tuple[str, ...]], Optional[str]]

# The minimums are the course contract (see languages.json); hard-coded so the preflight has no dependencies.
JAVA_MIN = Version.of(21, 0, 0)
PYTHON_MIN = Version.of(3, 11, 0)
GO_MIN = Version.of(1, 22, 0)

TOOLCHAINS: tuple[Toolchain, ...] = (
    Toolchain("java", ("java", "-version"), JAVA_MIN,
              re.compile(r'version "(?:1\.)?([^"]+)"'),  # legacy "1.8.0_292" means Java 8
              "install a JDK 21+ (https://adoptium.net or `sudo apt install openjdk-21-jdk`) and put its bin/ on PATH"),
    Toolchain("javac", ("javac", "-version"), JAVA_MIN,
              re.compile(r"javac\s+(\S+)"),
              "a JRE alone is not enough: install the full JDK 21+ so `javac` exists"),
    Toolchain("python3", ("python3", "--version"), PYTHON_MIN,
              re.compile(r"Python\s+(\S+)"),
              "install Python 3.11+ (https://www.python.org/downloads/ or `sudo apt install python3.12`)"),
    Toolchain("go", ("go", "version"), GO_MIN,
              re.compile(r"go version go(\S+)"),
              "install Go 1.22+ from https://go.dev/dl/ and add /usr/local/go/bin to PATH"),
)

REQUIRED_DIRS = ("phases", "scripts")
REQUIRED_FILES = ("languages.json", "curriculum/curriculum.json")


def real_runner(argv: tuple[str, ...]) -> Optional[str]:
    """Spawn the process and merge stderr into stdout (java -version writes to stderr)."""
    try:
        proc = subprocess.run(list(argv), capture_output=True, text=True, timeout=10)
    except FileNotFoundError:
        return None  # executable not on PATH
    except subprocess.TimeoutExpired:
        return ""
    return proc.stdout + proc.stderr


def check_toolchain(tool: Toolchain, runner: CommandRunner) -> CheckResult:
    output = runner(tool.argv)
    if output is None:
        return CheckResult(tool.name, Status.MISSING, None, f"not on PATH (minimum {tool.minimum}) -> {tool.fix}")
    found = tool.parse_version(output)
    if found is None:
        return CheckResult(tool.name, Status.UNPARSEABLE, None,
                           f"ran but printed no recognisable version; run `{' '.join(tool.argv)}` by hand")
    if not found.at_least(tool.minimum):
        return CheckResult(tool.name, Status.TOO_OLD, found, f"below minimum {tool.minimum} -> {tool.fix}")
    return CheckResult(tool.name, Status.OK, found, f"(minimum {tool.minimum})")


def check_toolchains(runner: CommandRunner) -> list[CheckResult]:
    return [check_toolchain(t, runner) for t in TOOLCHAINS]


def find_repo_root(start: Path) -> Path:
    """Walk upward from start until a directory holding languages.json appears; fall back to start."""
    start = start.resolve()
    for d in (start, *start.parents):
        if (d / "languages.json").is_file():
            return d
    return start


def check_layout(root: Path) -> list[CheckResult]:
    results = []
    for d in REQUIRED_DIRS:
        present = (root / d).is_dir()
        results.append(CheckResult(d + "/", Status.OK if present else Status.MISSING, None,
                                   "directory present" if present
                                   else "missing -> re-clone the repository; you may be running outside it"))
    for f in REQUIRED_FILES:
        present = (root / f).is_file()
        results.append(CheckResult(f, Status.OK if present else Status.MISSING, None,
                                   "file present" if present
                                   else "missing -> re-clone the repository; this file is the course's registry"))
    return results


def summary(results: list[CheckResult]) -> str:
    counts = {s: 0 for s in Status}
    for r in results:
        counts[r.status] += 1
    return "Summary:" + "".join(f" {counts[s]} {s.value}" for s in Status)


def main() -> None:
    print("== Parsing version strings (deterministic) ==")
    samples = [
        ("java", 'openjdk version "21.0.2" 2024-01-16\nOpenJDK Runtime Environment (build 21.0.2+13)'),
        ("java", 'java version "1.8.0_292"'),
        ("python3", "Python 3.12.1"),
        ("go", "go version go1.22.3 linux/amd64"),
        ("go", "go version go1.21.13 linux/amd64"),
    ]
    by_name = {t.name: t for t in TOOLCHAINS}
    for name, output in samples:
        tool = by_name[name]
        v = tool.parse_version(output)
        verdict = "unparseable" if v is None else (f"meets {tool.minimum}" if v.at_least(tool.minimum) else f"below {tool.minimum}")
        first_line = output.split("\n")[0]
        print(f"{name:<8} {chr(34) + first_line + chr(34):<45} -> {v if v else '?'} ({verdict})")

    print("\n== Preflight: toolchains on this machine ==")
    tools = check_toolchains(real_runner)
    for r in tools:
        print(r.formatted())

    print("\n== Preflight: repository layout ==")
    root = find_repo_root(Path.cwd())
    print(f"repository root: {root}")
    layout = check_layout(root)
    for r in layout:
        print(r.formatted())

    everything = tools + layout
    print()
    print(summary(everything))
    if all(r.ok() for r in everything):
        print("Every toolchain and directory is in place. Run any lesson with scripts/run_lesson.py.")
    else:
        print("Something is missing. You need only the language recorded in LEARNING.md to follow the course;"
              " CI needs all three.")
    # A preflight reports; it never fails. Exit 0 regardless of findings.


if __name__ == "__main__":
    main()
