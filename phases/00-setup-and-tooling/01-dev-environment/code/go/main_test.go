package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fake is a runner that answers from a fixed script: argv[0] -> output, or "not installed" otherwise.
func fake(executable, output string) CommandRunner {
	return func(argv []string) (string, bool) {
		if argv[0] == executable {
			return output, true
		}
		return "", false
	}
}

func TestParseVersionStrings(t *testing.T) {
	cases := []struct {
		tool, output string
		want         Version
	}{
		{"java", "openjdk version \"21.0.2\" 2024-01-16\nOpenJDK Runtime Environment (build 21.0.2+13)", VersionOf(21, 0, 2)},
		{"java", "Picked up JAVA_TOOL_OPTIONS: -Dfoo=bar\nopenjdk version \"21.0.12.1\" 2026-08-18", VersionOf(21, 0, 12)},
		{"java", "openjdk version \"21-ea\" 2023-09-19", VersionOf(21, 0, 0)},
		{"java", "java version \"1.8.0_292\"", VersionOf(8, 0, 0)},
		{"javac", "javac 21.0.2", VersionOf(21, 0, 2)},
		{"python3", "Python 3.12.1", VersionOf(3, 12, 1)},
		{"python3", "Python 3.13.0rc1", VersionOf(3, 13, 0)},
		{"go", "go version go1.22.3 linux/amd64", VersionOf(1, 22, 3)},
		{"go", "go version go1.22 darwin/arm64", VersionOf(1, 22, 0)},
		{"go", "go version go1.23rc1 linux/amd64", VersionOf(1, 23, 0)},
	}
	for _, c := range cases {
		got, ok := toolByName(c.tool).ParseVersion(c.output)
		if !ok || got != c.want {
			t.Errorf("%s.ParseVersion(%q) = %v, %v; want %v", c.tool, c.output, got, ok, c.want)
		}
	}
}

func TestUnparseable(t *testing.T) {
	if _, ok := toolByName("go").ParseVersion("command not found"); ok {
		t.Error("garbage should be unparseable")
	}
	if _, ok := ParseVersion(""); ok {
		t.Error("empty string should be unparseable")
	}
}

func TestVersionOrdering(t *testing.T) {
	cases := []struct {
		v, min Version
		want   bool
	}{
		{VersionOf(3, 11, 0), PythonMin, true},
		{VersionOf(3, 10, 12), PythonMin, false},
		{VersionOf(3, 9, 18), PythonMin, false}, // numeric, not lexical
		{VersionOf(22, 0, 0), JavaMin, true},
		{VersionOf(1, 21, 13), GoMin, false},
		{VersionOf(1, 22, 0), GoMin, true},
	}
	for _, c := range cases {
		if got := c.v.AtLeast(c.min); got != c.want {
			t.Errorf("%v.AtLeast(%v) = %v, want %v", c.v, c.min, got, c.want)
		}
	}
	if VersionOf(21, 0, 2).String() != "21.0.2" {
		t.Error("String()")
	}
}

func TestCheckToolchainOK(t *testing.T) {
	r := CheckToolchain(toolByName("go"), fake("go", "go version go1.22.3 linux/amd64"))
	if r.Status != OK || r.Found == nil || *r.Found != VersionOf(1, 22, 3) {
		t.Fatalf("got %+v", r)
	}
	if !strings.HasPrefix(r.Formatted(), "OK") || !strings.Contains(r.Formatted(), "go") {
		t.Errorf("Formatted() = %q", r.Formatted())
	}
}

func TestCheckToolchainTooOldCarriesFix(t *testing.T) {
	r := CheckToolchain(toolByName("python3"), fake("python3", "Python 3.10.12"))
	if r.Status != TooOld || !strings.Contains(r.Detail, "python.org") {
		t.Fatalf("got %+v", r)
	}
}

func TestCheckToolchainMissingCarriesFix(t *testing.T) {
	r := CheckToolchain(toolByName("java"), fake("python3", "Python 3.12.1"))
	if r.Status != Missing || r.Found != nil || !strings.Contains(r.Detail, "JDK 21+") {
		t.Fatalf("got %+v", r)
	}
}

func TestCheckToolchainUnparseable(t *testing.T) {
	r := CheckToolchain(toolByName("javac"), fake("javac", "error: could not create the Java Virtual Machine"))
	if r.Status != Unparseable {
		t.Fatalf("got %+v", r)
	}
}

func TestCheckToolchainsAndSummary(t *testing.T) {
	results := CheckToolchains(func([]string) (string, bool) { return "", false })
	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Status != Missing {
			t.Errorf("%s should be MISSING", r.Name)
		}
	}
	if got := Summary(results); got != "Summary: 0 OK 0 TOO_OLD 0 UNPARSEABLE 4 MISSING" {
		t.Errorf("Summary() = %q", got)
	}
}

func TestLayoutGoodAndRootDiscovery(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"phases", "scripts", "curriculum"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(root, "languages.json"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(root, "curriculum", "curriculum.json"), []byte("{}"), 0o644)
	for _, r := range CheckLayout(root) {
		if !r.OK() {
			t.Errorf("%s should be OK: %+v", r.Name, r)
		}
	}
	nested := filepath.Join(root, "phases", "00-x", "01-y", "code", "go")
	os.MkdirAll(nested, 0o755)
	if got := FindRepoRoot(nested); got != root {
		t.Errorf("FindRepoRoot(%q) = %q, want %q", nested, got, root)
	}
}

func TestLayoutBadReportsEachMissingEntry(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "phases"), 0o755)
	byName := map[string]CheckResult{}
	for _, r := range CheckLayout(root) {
		byName[r.Name] = r
	}
	if !byName["phases/"].OK() {
		t.Error("phases/ should be OK")
	}
	if byName["scripts/"].Status != Missing || byName["languages.json"].Status != Missing {
		t.Errorf("missing entries not reported: %+v", byName)
	}
	if got := FindRepoRoot(root); got != root {
		t.Errorf("FindRepoRoot should fall back to start, got %q", got)
	}
}
