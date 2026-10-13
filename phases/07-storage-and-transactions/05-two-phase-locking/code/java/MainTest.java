// Tests for phases/07-storage-and-transactions/05-two-phase-locking. Standard-library test harness: run with `java -cp .out MainTest`.
import java.util.ArrayList;
import java.util.List;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    public static void main(String[] args) {
        check("placeholder returns input", Main.placeholder(7) == 7);
        check("placeholder handles zero", Main.placeholder(0) == 0);
        check("placeholder handles negatives", Main.placeholder(-3) == -3);
        check("placeholder is deterministic", Main.placeholder(5) == Main.placeholder(5));
        check("placeholder large", Main.placeholder(1_000_000) == 1_000_000);
        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
