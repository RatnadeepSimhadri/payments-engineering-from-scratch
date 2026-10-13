// Lesson: phases/00-setup-and-tooling/01-dev-environment/docs/en.md
// Dev Environment: Java 21, Python 3.12, Go 1.22 -- a preflight checker
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: JEP 223 (JDK version-string scheme); PEP 440 (Python version identifiers); go.dev/doc/toolchain.
// Java 21, standard library only. Same design as the Python and Go versions.

import java.io.IOException;
import java.io.InputStream;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.util.ArrayList;
import java.util.List;
import java.util.Optional;
import java.util.concurrent.TimeUnit;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/** A three-part version. Pre-release suffixes ("-ea", "rc1", "_292") are dropped. */
record Version(int major, int minor, int patch) implements Comparable<Version> {
    private static final Pattern LEADING = Pattern.compile("^(\\d+)(?:\\.(\\d+))?(?:\\.(\\d+))?");

    static Version of(int major, int minor, int patch) {
        return new Version(major, minor, patch);
    }

    /** "21.0.2" -> 21.0.2; "21-ea" -> 21.0.0; "3.13.0rc1" -> 3.13.0; "1.22" -> 1.22.0. Missing parts are zero. */
    static Optional<Version> parse(String text) {
        if (text == null) return Optional.empty();
        Matcher m = LEADING.matcher(text.strip());
        if (!m.find()) return Optional.empty();
        int major = Integer.parseInt(m.group(1));
        int minor = m.group(2) == null ? 0 : Integer.parseInt(m.group(2));
        int patch = m.group(3) == null ? 0 : Integer.parseInt(m.group(3));
        return Optional.of(new Version(major, minor, patch));
    }

    boolean atLeast(Version minimum) {
        return compareTo(minimum) >= 0;
    }

    @Override
    public int compareTo(Version o) {
        if (major != o.major) return Integer.compare(major, o.major);
        if (minor != o.minor) return Integer.compare(minor, o.minor);
        return Integer.compare(patch, o.patch);
    }

    @Override
    public String toString() {
        return major + "." + minor + "." + patch;
    }
}

/** Outcome of one check. The order is the order of severity. */
enum Status { OK, TOO_OLD, UNPARSEABLE, MISSING }

/** One executable the course depends on, how to ask it for its version, and how to read the answer. */
record Toolchain(String name, List<String> argv, Version minimum, Pattern versionPattern, String fix) {
    /** Extract the version text from the command's combined stdout+stderr, ignoring noise lines. */
    Optional<Version> parseVersion(String output) {
        Matcher m = versionPattern.matcher(output);
        if (!m.find()) return Optional.empty();
        return Version.parse(m.group(1));
    }
}

record CheckResult(String name, Status status, Optional<Version> found, String detail) {
    boolean ok() { return status == Status.OK; }

    String formatted() {
        String version = found.map(Version::toString).orElse("-");
        return String.format("%-11s %-27s %-9s %s", status, name, version, detail);
    }
}

/** Runs a command and returns its combined output, or empty when the executable does not exist. */
interface CommandRunner {
    Optional<String> run(List<String> argv);
}

final class Preflight {
    // The minimums are the course contract (see languages.json); hard-coded so the preflight has no dependencies.
    static final Version JAVA_MIN = Version.of(21, 0, 0);
    static final Version PYTHON_MIN = Version.of(3, 11, 0);
    static final Version GO_MIN = Version.of(1, 22, 0);

    static final List<Toolchain> TOOLCHAINS = List.of(
            new Toolchain("java", List.of("java", "-version"), JAVA_MIN,
                    Pattern.compile("version \"(?:1\\.)?([^\"]+)\""),   // legacy "1.8.0_292" means Java 8
                    "install a JDK 21+ (https://adoptium.net or `sudo apt install openjdk-21-jdk`) and put its bin/ on PATH"),
            new Toolchain("javac", List.of("javac", "-version"), JAVA_MIN,
                    Pattern.compile("javac\\s+(\\S+)"),
                    "a JRE alone is not enough: install the full JDK 21+ so `javac` exists"),
            new Toolchain("python3", List.of("python3", "--version"), PYTHON_MIN,
                    Pattern.compile("Python\\s+(\\S+)"),
                    "install Python 3.11+ (https://www.python.org/downloads/ or `sudo apt install python3.12`)"),
            new Toolchain("go", List.of("go", "version"), GO_MIN,
                    Pattern.compile("go version go(\\S+)"),
                    "install Go 1.22+ from https://go.dev/dl/ and add /usr/local/go/bin to PATH"));

    static final List<String> REQUIRED_DIRS = List.of("phases", "scripts");
    static final List<String> REQUIRED_FILES = List.of("languages.json", "curriculum/curriculum.json");

    /** The real runner: spawns the process, merges stderr into stdout (java -version writes to stderr). */
    static final CommandRunner REAL_RUNNER = argv -> {
        try {
            Process p = new ProcessBuilder(argv).redirectErrorStream(true).start();
            try (InputStream in = p.getInputStream()) {
                String out = new String(in.readAllBytes(), StandardCharsets.UTF_8);
                if (!p.waitFor(10, TimeUnit.SECONDS)) p.destroyForcibly();
                return Optional.of(out);
            }
        } catch (IOException e) {
            return Optional.empty();                     // executable not on PATH
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return Optional.empty();
        }
    };

    static CheckResult checkToolchain(Toolchain tool, CommandRunner runner) {
        Optional<String> output = runner.run(tool.argv());
        if (output.isEmpty()) {
            return new CheckResult(tool.name(), Status.MISSING, Optional.empty(),
                    "not on PATH (minimum " + tool.minimum() + ") -> " + tool.fix());
        }
        Optional<Version> found = tool.parseVersion(output.get());
        if (found.isEmpty()) {
            return new CheckResult(tool.name(), Status.UNPARSEABLE, Optional.empty(),
                    "ran but printed no recognisable version; run `" + String.join(" ", tool.argv()) + "` by hand");
        }
        if (!found.get().atLeast(tool.minimum())) {
            return new CheckResult(tool.name(), Status.TOO_OLD, found,
                    "below minimum " + tool.minimum() + " -> " + tool.fix());
        }
        return new CheckResult(tool.name(), Status.OK, found, "(minimum " + tool.minimum() + ")");
    }

    static List<CheckResult> checkToolchains(CommandRunner runner) {
        List<CheckResult> results = new ArrayList<>();
        for (Toolchain t : TOOLCHAINS) results.add(checkToolchain(t, runner));
        return results;
    }

    /** Walk upward from start until a directory holding languages.json appears; fall back to start. */
    static Path findRepoRoot(Path start) {
        Path dir = start.toAbsolutePath().normalize();
        while (dir != null) {
            if (Files.isRegularFile(dir.resolve("languages.json"))) return dir;
            dir = dir.getParent();
        }
        return start.toAbsolutePath().normalize();
    }

    static List<CheckResult> checkLayout(Path root) {
        List<CheckResult> results = new ArrayList<>();
        for (String d : REQUIRED_DIRS) {
            boolean present = Files.isDirectory(root.resolve(d));
            results.add(new CheckResult(d + "/", present ? Status.OK : Status.MISSING, Optional.empty(),
                    present ? "directory present" : "missing -> re-clone the repository; you may be running outside it"));
        }
        for (String f : REQUIRED_FILES) {
            boolean present = Files.isRegularFile(root.resolve(f));
            results.add(new CheckResult(f, present ? Status.OK : Status.MISSING, Optional.empty(),
                    present ? "file present" : "missing -> re-clone the repository; this file is the course's registry"));
        }
        return results;
    }

    static String summary(List<CheckResult> results) {
        int[] counts = new int[Status.values().length];
        for (CheckResult r : results) counts[r.status().ordinal()]++;
        StringBuilder sb = new StringBuilder("Summary:");
        for (Status s : Status.values()) sb.append(' ').append(counts[s.ordinal()]).append(' ').append(s);
        return sb.toString();
    }
}

public final class Main {
    public static void main(String[] args) {
        System.out.println("== Parsing version strings (deterministic) ==");
        String[][] samples = {
                {"java", "openjdk version \"21.0.2\" 2024-01-16\nOpenJDK Runtime Environment (build 21.0.2+13)"},
                {"java", "java version \"1.8.0_292\""},
                {"python3", "Python 3.12.1"},
                {"go", "go version go1.22.3 linux/amd64"},
                {"go", "go version go1.21.13 linux/amd64"},
        };
        for (String[] s : samples) {
            Toolchain t = Preflight.TOOLCHAINS.stream().filter(x -> x.name().equals(s[0])).findFirst().orElseThrow();
            Optional<Version> v = t.parseVersion(s[1]);
            String firstLine = s[1].split("\n")[0];
            System.out.printf("%-8s %-45s -> %s (%s)%n", s[0], "\"" + firstLine + "\"", v.map(Version::toString).orElse("?"),
                    v.map(x -> x.atLeast(t.minimum()) ? "meets " + t.minimum() : "below " + t.minimum()).orElse("unparseable"));
        }

        System.out.println("\n== Preflight: toolchains on this machine ==");
        List<CheckResult> tools = Preflight.checkToolchains(Preflight.REAL_RUNNER);
        tools.forEach(r -> System.out.println(r.formatted()));

        System.out.println("\n== Preflight: repository layout ==");
        Path root = Preflight.findRepoRoot(Paths.get(""));
        System.out.println("repository root: " + root);
        List<CheckResult> layout = Preflight.checkLayout(root);
        layout.forEach(r -> System.out.println(r.formatted()));

        List<CheckResult> all = new ArrayList<>(tools);
        all.addAll(layout);
        System.out.println();
        System.out.println(Preflight.summary(all));
        System.out.println(all.stream().allMatch(CheckResult::ok)
                ? "Every toolchain and directory is in place. Run any lesson with scripts/run_lesson.py."
                : "Something is missing. You need only the language recorded in LEARNING.md to follow the course;"
                + " CI needs all three.");
        // A preflight reports; it never fails. Exit 0 regardless of findings.
    }
}
