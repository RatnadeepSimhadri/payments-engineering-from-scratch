"""Tests for phases/00-setup-and-tooling/01-dev-environment. Run: python3 -m unittest -v"""
import tempfile
import unittest
from pathlib import Path

from main import (GO_MIN, JAVA_MIN, PYTHON_MIN, TOOLCHAINS, CheckResult, Status, Version, check_layout,
                  check_toolchain, check_toolchains, find_repo_root, summary)

BY_NAME = {t.name: t for t in TOOLCHAINS}


def fake(executable: str, output: str):
    """A runner that answers from a fixed script: argv[0] -> output, or None when the tool is "not installed"."""
    return lambda argv: output if argv[0] == executable else None


class VersionParsingTest(unittest.TestCase):
    def test_java_21(self):
        out = 'openjdk version "21.0.2" 2024-01-16\nOpenJDK Runtime Environment (build 21.0.2+13)'
        self.assertEqual(BY_NAME["java"].parse_version(out), Version.of(21, 0, 2))

    def test_java_ignores_java_tool_options_noise(self):
        out = 'Picked up JAVA_TOOL_OPTIONS: -Dfoo=bar\nopenjdk version "21.0.12.1" 2026-08-18'
        self.assertEqual(BY_NAME["java"].parse_version(out), Version.of(21, 0, 12))

    def test_java_early_access(self):
        self.assertEqual(BY_NAME["java"].parse_version('openjdk version "21-ea" 2023-09-19'), Version.of(21, 0, 0))

    def test_java_legacy_scheme_is_java_8(self):
        self.assertEqual(BY_NAME["java"].parse_version('java version "1.8.0_292"'), Version.of(8, 0, 0))

    def test_javac(self):
        self.assertEqual(BY_NAME["javac"].parse_version("javac 21.0.2"), Version.of(21, 0, 2))

    def test_python(self):
        self.assertEqual(BY_NAME["python3"].parse_version("Python 3.12.1"), Version.of(3, 12, 1))
        self.assertEqual(BY_NAME["python3"].parse_version("Python 3.13.0rc1"), Version.of(3, 13, 0))

    def test_go(self):
        self.assertEqual(BY_NAME["go"].parse_version("go version go1.22.3 linux/amd64"), Version.of(1, 22, 3))
        self.assertEqual(BY_NAME["go"].parse_version("go version go1.22 darwin/arm64"), Version.of(1, 22, 0))
        self.assertEqual(BY_NAME["go"].parse_version("go version go1.23rc1 linux/amd64"), Version.of(1, 23, 0))

    def test_garbage_is_unparseable(self):
        self.assertIsNone(BY_NAME["go"].parse_version("command not found"))
        self.assertIsNone(Version.parse(""))
        self.assertIsNone(Version.parse(None))

    def test_str(self):
        self.assertEqual(str(Version.of(21, 0, 2)), "21.0.2")


class VersionOrderingTest(unittest.TestCase):
    def test_python_minimum(self):
        self.assertTrue(Version.of(3, 11, 0).at_least(PYTHON_MIN))
        self.assertFalse(Version.of(3, 10, 12).at_least(PYTHON_MIN))

    def test_minor_compares_numerically_not_lexically(self):
        self.assertFalse(Version.of(3, 9, 18).at_least(PYTHON_MIN))

    def test_java_and_go_minimums(self):
        self.assertTrue(Version.of(22, 0, 0).at_least(JAVA_MIN))
        self.assertFalse(Version.of(1, 21, 13).at_least(GO_MIN))
        self.assertTrue(Version.of(1, 22, 0).at_least(GO_MIN))


class CheckToolchainTest(unittest.TestCase):
    def test_ok(self):
        r = check_toolchain(BY_NAME["go"], fake("go", "go version go1.22.3 linux/amd64"))
        self.assertIs(r.status, Status.OK)
        self.assertEqual(r.found, Version.of(1, 22, 3))
        self.assertTrue(r.formatted().startswith("OK"))
        self.assertIn("go", r.formatted())

    def test_too_old_carries_fix(self):
        r = check_toolchain(BY_NAME["python3"], fake("python3", "Python 3.10.12"))
        self.assertIs(r.status, Status.TOO_OLD)
        self.assertIn("python.org", r.detail)

    def test_missing_carries_fix(self):
        r = check_toolchain(BY_NAME["java"], fake("python3", "Python 3.12.1"))
        self.assertIs(r.status, Status.MISSING)
        self.assertIsNone(r.found)
        self.assertIn("JDK 21+", r.detail)

    def test_unparseable(self):
        r = check_toolchain(BY_NAME["javac"], fake("javac", "error: could not create the Java Virtual Machine"))
        self.assertIs(r.status, Status.UNPARSEABLE)

    def test_all_four_and_summary(self):
        results = check_toolchains(lambda argv: None)
        self.assertEqual(len(results), 4)
        self.assertTrue(all(r.status is Status.MISSING for r in results))
        self.assertEqual(summary(results), "Summary: 0 OK 0 TOO_OLD 0 UNPARSEABLE 4 MISSING")


class LayoutTest(unittest.TestCase):
    def test_good_layout_and_root_discovery(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "phases").mkdir()
            (root / "scripts").mkdir()
            (root / "curriculum").mkdir()
            (root / "languages.json").write_text("{}")
            (root / "curriculum" / "curriculum.json").write_text("{}")
            self.assertTrue(all(r.ok() for r in check_layout(root)))
            nested = root / "phases" / "00-x" / "01-y" / "code" / "python"
            nested.mkdir(parents=True)
            self.assertEqual(find_repo_root(nested), root.resolve())

    def test_bad_layout_reports_each_missing_entry(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "phases").mkdir()
            results = {r.name: r for r in check_layout(root)}
            self.assertTrue(results["phases/"].ok())
            self.assertIs(results["scripts/"].status, Status.MISSING)
            self.assertIs(results["languages.json"].status, Status.MISSING)
            self.assertEqual(find_repo_root(root), root.resolve())


if __name__ == "__main__":
    unittest.main()
