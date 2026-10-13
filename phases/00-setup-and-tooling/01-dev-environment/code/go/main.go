// Lesson: phases/00-setup-and-tooling/01-dev-environment/docs/en.md
// Dev Environment: Java 21, Python 3.12, Go 1.22 -- a preflight checker
// Run:  go run .
// Test: go test ./...
// Sources: JEP 223 (JDK version-string scheme); PEP 440 (Python version identifiers); go.dev/doc/toolchain.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Version is a three-part version. Pre-release suffixes ("-ea", "rc1", "_292") are dropped.
type Version struct {
	Major, Minor, Patch int
}

var leading = regexp.MustCompile(`^(\d+)(?:\.(\d+))?(?:\.(\d+))?`)

func VersionOf(major, minor, patch int) Version { return Version{major, minor, patch} }

// ParseVersion reads "21.0.2" -> 21.0.2; "21-ea" -> 21.0.0; "3.13.0rc1" -> 3.13.0; "1.22" -> 1.22.0.
// Missing parts are zero. ok is false when the text does not start with a number.
func ParseVersion(text string) (v Version, ok bool) {
	m := leading.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return Version{}, false
	}
	parts := [3]int{}
	for i := 0; i < 3; i++ {
		if m[i+1] != "" {
			parts[i], _ = strconv.Atoi(m[i+1]) // the regexp guarantees digits
		}
	}
	return Version{parts[0], parts[1], parts[2]}, true
}

// Compare returns -1, 0, or +1, comparing major, then minor, then patch numerically.
func (v Version) Compare(o Version) int {
	switch {
	case v.Major != o.Major:
		return cmp(v.Major, o.Major)
	case v.Minor != o.Minor:
		return cmp(v.Minor, o.Minor)
	default:
		return cmp(v.Patch, o.Patch)
	}
}

func cmp(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func (v Version) AtLeast(minimum Version) bool { return v.Compare(minimum) >= 0 }

func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

// Status is the outcome of one check, in order of severity.
type Status int

const (
	OK Status = iota
	TooOld
	Unparseable
	Missing
)

var statusNames = [...]string{"OK", "TOO_OLD", "UNPARSEABLE", "MISSING"}

func (s Status) String() string { return statusNames[s] }

// Toolchain is one executable the course depends on, how to ask it for its version, and how to read the answer.
type Toolchain struct {
	Name           string
	Argv           []string
	Minimum        Version
	VersionPattern *regexp.Regexp
	Fix            string
}

// ParseVersion extracts the version text from combined stdout+stderr, ignoring noise lines.
func (t Toolchain) ParseVersion(output string) (Version, bool) {
	m := t.VersionPattern.FindStringSubmatch(output)
	if m == nil {
		return Version{}, false
	}
	return ParseVersion(m[1])
}

// CheckResult is one row of the report. Found is nil when no version was read.
type CheckResult struct {
	Name   string
	Status Status
	Found  *Version
	Detail string
}

func (r CheckResult) OK() bool { return r.Status == OK }

func (r CheckResult) Formatted() string {
	version := "-"
	if r.Found != nil {
		version = r.Found.String()
	}
	return fmt.Sprintf("%-11s %-27s %-9s %s", r.Status, r.Name, version, r.Detail)
}

// CommandRunner runs argv and returns combined output; found is false when the executable does not exist.
type CommandRunner func(argv []string) (output string, found bool)

// The minimums are the course contract (see languages.json); hard-coded so the preflight has no dependencies.
var (
	JavaMin   = VersionOf(21, 0, 0)
	PythonMin = VersionOf(3, 11, 0)
	GoMin     = VersionOf(1, 22, 0)
)

var Toolchains = []Toolchain{
	{"java", []string{"java", "-version"}, JavaMin,
		regexp.MustCompile(`version "(?:1\.)?([^"]+)"`), // legacy "1.8.0_292" means Java 8
		"install a JDK 21+ (https://adoptium.net or `sudo apt install openjdk-21-jdk`) and put its bin/ on PATH"},
	{"javac", []string{"javac", "-version"}, JavaMin,
		regexp.MustCompile(`javac\s+(\S+)`),
		"a JRE alone is not enough: install the full JDK 21+ so `javac` exists"},
	{"python3", []string{"python3", "--version"}, PythonMin,
		regexp.MustCompile(`Python\s+(\S+)`),
		"install Python 3.11+ (https://www.python.org/downloads/ or `sudo apt install python3.12`)"},
	{"go", []string{"go", "version"}, GoMin,
		regexp.MustCompile(`go version go(\S+)`),
		"install Go 1.22+ from https://go.dev/dl/ and add /usr/local/go/bin to PATH"},
}

var (
	RequiredDirs  = []string{"phases", "scripts"}
	RequiredFiles = []string{"languages.json", "curriculum/curriculum.json"}
)

// RealRunner spawns the process and merges stderr into stdout (java -version writes to stderr).
func RealRunner(argv []string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) || errors.Is(err, exec.ErrNotFound) {
			return "", false // executable not on PATH
		}
	}
	return string(out), true // ran (perhaps with a non-zero exit); let the parser decide
}

func CheckToolchain(tool Toolchain, runner CommandRunner) CheckResult {
	output, found := runner(tool.Argv)
	if !found {
		return CheckResult{tool.Name, Missing, nil,
			fmt.Sprintf("not on PATH (minimum %s) -> %s", tool.Minimum, tool.Fix)}
	}
	v, ok := tool.ParseVersion(output)
	if !ok {
		return CheckResult{tool.Name, Unparseable, nil,
			fmt.Sprintf("ran but printed no recognisable version; run `%s` by hand", strings.Join(tool.Argv, " "))}
	}
	if !v.AtLeast(tool.Minimum) {
		return CheckResult{tool.Name, TooOld, &v, fmt.Sprintf("below minimum %s -> %s", tool.Minimum, tool.Fix)}
	}
	return CheckResult{tool.Name, OK, &v, fmt.Sprintf("(minimum %s)", tool.Minimum)}
}

func CheckToolchains(runner CommandRunner) []CheckResult {
	results := make([]CheckResult, 0, len(Toolchains))
	for _, t := range Toolchains {
		results = append(results, CheckToolchain(t, runner))
	}
	return results
}

// FindRepoRoot walks upward from start until a directory holding languages.json appears; falls back to start.
func FindRepoRoot(start string) string {
	abs, err := filepath.Abs(start)
	if err != nil {
		return start
	}
	for dir := abs; ; dir = filepath.Dir(dir) {
		if info, err := os.Stat(filepath.Join(dir, "languages.json")); err == nil && !info.IsDir() {
			return dir
		}
		if filepath.Dir(dir) == dir {
			return abs
		}
	}
}

func CheckLayout(root string) []CheckResult {
	var results []CheckResult
	for _, d := range RequiredDirs {
		info, err := os.Stat(filepath.Join(root, d))
		present := err == nil && info.IsDir()
		r := CheckResult{d + "/", OK, nil, "directory present"}
		if !present {
			r = CheckResult{d + "/", Missing, nil, "missing -> re-clone the repository; you may be running outside it"}
		}
		results = append(results, r)
	}
	for _, f := range RequiredFiles {
		info, err := os.Stat(filepath.Join(root, f))
		present := err == nil && !info.IsDir()
		r := CheckResult{f, OK, nil, "file present"}
		if !present {
			r = CheckResult{f, Missing, nil, "missing -> re-clone the repository; this file is the course's registry"}
		}
		results = append(results, r)
	}
	return results
}

func Summary(results []CheckResult) string {
	counts := make([]int, len(statusNames))
	for _, r := range results {
		counts[r.Status]++
	}
	var sb strings.Builder
	sb.WriteString("Summary:")
	for s, name := range statusNames {
		fmt.Fprintf(&sb, " %d %s", counts[s], name)
	}
	return sb.String()
}

func toolByName(name string) Toolchain {
	for _, t := range Toolchains {
		if t.Name == name {
			return t
		}
	}
	panic("unknown toolchain " + name)
}

func main() {
	fmt.Println("== Parsing version strings (deterministic) ==")
	samples := [][2]string{
		{"java", "openjdk version \"21.0.2\" 2024-01-16\nOpenJDK Runtime Environment (build 21.0.2+13)"},
		{"java", "java version \"1.8.0_292\""},
		{"python3", "Python 3.12.1"},
		{"go", "go version go1.22.3 linux/amd64"},
		{"go", "go version go1.21.13 linux/amd64"},
	}
	for _, s := range samples {
		t := toolByName(s[0])
		v, ok := t.ParseVersion(s[1])
		verdict, shown := "unparseable", "?"
		if ok {
			shown = v.String()
			if v.AtLeast(t.Minimum) {
				verdict = "meets " + t.Minimum.String()
			} else {
				verdict = "below " + t.Minimum.String()
			}
		}
		firstLine := strings.SplitN(s[1], "\n", 2)[0]
		fmt.Printf("%-8s %-45s -> %s (%s)\n", s[0], "\""+firstLine+"\"", shown, verdict)
	}

	fmt.Println("\n== Preflight: toolchains on this machine ==")
	tools := CheckToolchains(RealRunner)
	for _, r := range tools {
		fmt.Println(r.Formatted())
	}

	fmt.Println("\n== Preflight: repository layout ==")
	root := FindRepoRoot(".")
	fmt.Println("repository root:", root)
	layout := CheckLayout(root)
	for _, r := range layout {
		fmt.Println(r.Formatted())
	}

	all := append(append([]CheckResult{}, tools...), layout...)
	fmt.Println()
	fmt.Println(Summary(all))
	allOK := true
	for _, r := range all {
		allOK = allOK && r.OK()
	}
	if allOK {
		fmt.Println("Every toolchain and directory is in place. Run any lesson with scripts/run_lesson.py.")
	} else {
		fmt.Println("Something is missing. You need only the language recorded in LEARNING.md to follow the course;" +
			" CI needs all three.")
	}
	// A preflight reports; it never fails. Exit 0 regardless of findings.
}
