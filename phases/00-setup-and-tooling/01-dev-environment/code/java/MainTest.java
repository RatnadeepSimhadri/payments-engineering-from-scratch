// Tests for phases/00-setup-and-tooling/01-dev-environment. Run: javac -d .out *.java && java -cp .out MainTest
import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;
import java.util.Optional;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    static Toolchain tool(String name) {
        return Preflight.TOOLCHAINS.stream().filter(t -> t.name().equals(name)).findFirst().orElseThrow();
    }

    /** A runner that answers from a fixed script: argv[0] -> output, or absent when the tool is "not installed". */
    static CommandRunner fake(String executable, String output) {
        return argv -> argv.get(0).equals(executable) ? Optional.of(output) : Optional.empty();
    }

    public static void main(String[] args) throws IOException {
        // version-string parsing: the part that is mocked, because real toolchains vary per machine
        check("java 21.0.2", tool("java").parseVersion("openjdk version \"21.0.2\" 2024-01-16\nOpenJDK Runtime Environment").get().equals(Version.of(21, 0, 2)));
        check("java ignores JAVA_TOOL_OPTIONS noise line",
                tool("java").parseVersion("Picked up JAVA_TOOL_OPTIONS: -Dfoo=bar\nopenjdk version \"21.0.12.1\" 2026-08-18").get().equals(Version.of(21, 0, 12)));
        check("java early access 21-ea", tool("java").parseVersion("openjdk version \"21-ea\" 2023-09-19").get().equals(Version.of(21, 0, 0)));
        check("java legacy 1.8.0_292 is Java 8", tool("java").parseVersion("java version \"1.8.0_292\"").get().equals(Version.of(8, 0, 0)));
        check("javac 21.0.2", tool("javac").parseVersion("javac 21.0.2").get().equals(Version.of(21, 0, 2)));
        check("python 3.12.1", tool("python3").parseVersion("Python 3.12.1").get().equals(Version.of(3, 12, 1)));
        check("python release candidate 3.13.0rc1", tool("python3").parseVersion("Python 3.13.0rc1").get().equals(Version.of(3, 13, 0)));
        check("go 1.22.3", tool("go").parseVersion("go version go1.22.3 linux/amd64").get().equals(Version.of(1, 22, 3)));
        check("go without patch", tool("go").parseVersion("go version go1.22 darwin/arm64").get().equals(Version.of(1, 22, 0)));
        check("go release candidate", tool("go").parseVersion("go version go1.23rc1 linux/amd64").get().equals(Version.of(1, 23, 0)));
        check("garbage is unparseable", tool("go").parseVersion("command not found").isEmpty());
        check("Version.parse rejects empty", Version.parse("").isEmpty());

        // ordering
        check("3.11.0 meets 3.11", Version.of(3, 11, 0).atLeast(Preflight.PYTHON_MIN));
        check("3.10.12 is below 3.11", !Version.of(3, 10, 12).atLeast(Preflight.PYTHON_MIN));
        check("3.9.18 is below 3.11 (minor compares numerically, not lexically)", !Version.of(3, 9, 18).atLeast(Preflight.PYTHON_MIN));
        check("22.0.0 meets 21", Version.of(22, 0, 0).atLeast(Preflight.JAVA_MIN));
        check("1.21.13 is below 1.22", !Version.of(1, 21, 13).atLeast(Preflight.GO_MIN));
        check("1.22.0 meets 1.22", Version.of(1, 22, 0).atLeast(Preflight.GO_MIN));
        check("toString", Version.of(21, 0, 2).toString().equals("21.0.2"));

        // check outcomes through an injected runner
        CheckResult ok = Preflight.checkToolchain(tool("go"), fake("go", "go version go1.22.3 linux/amd64"));
        check("OK status", ok.status() == Status.OK && ok.found().get().equals(Version.of(1, 22, 3)));
        CheckResult old = Preflight.checkToolchain(tool("python3"), fake("python3", "Python 3.10.12"));
        check("TOO_OLD status", old.status() == Status.TOO_OLD);
        check("TOO_OLD carries the fix", old.detail().contains("python.org"));
        CheckResult missing = Preflight.checkToolchain(tool("java"), fake("python3", "Python 3.12.1"));
        check("MISSING status when the executable is absent", missing.status() == Status.MISSING && missing.found().isEmpty());
        check("MISSING carries the fix", missing.detail().contains("JDK 21+"));
        CheckResult weird = Preflight.checkToolchain(tool("javac"), fake("javac", "error: could not create the Java Virtual Machine"));
        check("UNPARSEABLE status", weird.status() == Status.UNPARSEABLE);
        List<CheckResult> all = Preflight.checkToolchains(argv -> Optional.empty());
        check("all four toolchains checked", all.size() == 4 && all.stream().allMatch(r -> r.status() == Status.MISSING));
        check("summary counts", Preflight.summary(all).equals("Summary: 0 OK 0 TOO_OLD 0 UNPARSEABLE 4 MISSING"));
        check("formatted row names the tool", ok.formatted().contains("go") && ok.formatted().startsWith("OK"));

        // repository layout on a temp tree
        Path good = Files.createTempDirectory("preflight-good");
        Files.createDirectories(good.resolve("phases"));
        Files.createDirectories(good.resolve("scripts"));
        Files.createDirectories(good.resolve("curriculum"));
        Files.writeString(good.resolve("languages.json"), "{}");
        Files.writeString(good.resolve("curriculum/curriculum.json"), "{}");
        check("layout OK", Preflight.checkLayout(good).stream().allMatch(CheckResult::ok));
        Path nested = good.resolve("phases/00-x/01-y/code/java");
        Files.createDirectories(nested);
        check("findRepoRoot walks up to languages.json", Preflight.findRepoRoot(nested).equals(good.toAbsolutePath().normalize()));
        Path bad = Files.createTempDirectory("preflight-bad");
        Files.createDirectories(bad.resolve("phases"));
        List<CheckResult> badLayout = Preflight.checkLayout(bad);
        check("layout reports scripts/ missing", badLayout.stream().anyMatch(r -> r.name().equals("scripts/") && r.status() == Status.MISSING));
        check("layout reports phases/ present", badLayout.stream().anyMatch(r -> r.name().equals("phases/") && r.ok()));
        check("findRepoRoot falls back to start", Preflight.findRepoRoot(bad).equals(bad.toAbsolutePath().normalize()));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
