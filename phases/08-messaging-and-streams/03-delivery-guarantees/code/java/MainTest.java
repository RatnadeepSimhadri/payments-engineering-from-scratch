// Tests for phases/08-messaging-and-streams/03-delivery-guarantees. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    static void checkThrows(String name, Class<? extends Throwable> type, Runnable action) {
        try {
            action.run();
            failures.add(name + " (no exception)");
        } catch (Throwable t) {
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ")");
        }
    }

    public static void main(String[] args) {
        Currency USD = Currency.USD;
        Log log = Main.sampleLog(10);
        Money expected = Money.zero(USD);
        for (Record r : log.read(0, 10)) expected = expected.plus(r.amount());

        // the log itself
        check("log has 10 records", log.endOffset() == 10);
        check("read at end is empty", log.read(10, 4).isEmpty());
        check("read respects batch size", log.read(0, 4).size() == 4 && log.read(8, 4).size() == 2);
        checkThrows("read past end is an error", IllegalArgumentException.class, () -> log.read(11, 1));

        // no crash: every strategy produces the same ledger
        for (Strategy s : Strategy.values()) {
            Outcome o = Simulation.run(s, log, -1, 4);
            check(s + " without a crash is effectively once", o.effectivelyOnce() && o.posted() == 10 && o.crashes() == 0);
            check(s + " without a crash has the expected total", o.balance().equals(expected));
        }

        // crash after offset 5 (middle of batch 4..7)
        Outcome amo = Simulation.run(Strategy.AT_MOST_ONCE, log, 5, 4);
        check("at-most-once loses the rest of the batch", amo.lost() == 2 && amo.duplicates() == 0 && amo.posted() == 8);
        check("at-most-once total is short", amo.balance().minorUnits() < expected.minorUnits());
        Outcome alo = Simulation.run(Strategy.AT_LEAST_ONCE, log, 5, 4);
        check("at-least-once re-posts the start of the batch", alo.duplicates() == 2 && alo.lost() == 0 && alo.posted() == 12);
        check("at-least-once total is over", alo.balance().minorUnits() > expected.minorUnits());
        Outcome idem = Simulation.run(Strategy.AT_LEAST_ONCE_IDEMPOTENT, log, 5, 4);
        check("idempotent consumer absorbs the redelivery", idem.effectivelyOnce() && idem.posted() == 10);
        check("idempotent consumer total is exact", idem.balance().equals(expected));
        Outcome tx = Simulation.run(Strategy.TRANSACTIONAL, log, 5, 4);
        check("transactional consumer never double-posts", tx.effectivelyOnce() && tx.posted() == 10);
        check("transactional consumer total is exact", tx.balance().equals(expected));
        check("every strategy crashed exactly once", amo.crashes() == 1 && alo.crashes() == 1 && idem.crashes() == 1 && tx.crashes() == 1);

        // crash at the last record of a batch: at-most-once happens to be fine, at-least-once still duplicates
        check("at-most-once crash at batch end loses nothing", Simulation.run(Strategy.AT_MOST_ONCE, log, 7, 4).lost() == 0);
        check("at-least-once crash at batch end duplicates the whole batch", Simulation.run(Strategy.AT_LEAST_ONCE, log, 7, 4).duplicates() == 4);
        check("at-least-once crash at offset 0 duplicates exactly one", Simulation.run(Strategy.AT_LEAST_ONCE, log, 0, 4).duplicates() == 1);

        // the sweep: effectively-once strategies are right at every crash point
        for (Strategy s : List.of(Strategy.AT_LEAST_ONCE_IDEMPOTENT, Strategy.TRANSACTIONAL)) {
            boolean all = true;
            for (long at = 0; at < 10; at++) all &= Simulation.run(s, log, at, 4).effectivelyOnce();
            check(s + " is correct at every crash offset", all);
        }
        boolean aloEverRight = false;
        for (long at = 0; at < 10; at++) aloEverRight |= Simulation.run(Strategy.AT_LEAST_ONCE, log, at, 4).effectivelyOnce();
        check("plain at-least-once is never right when it crashes mid-flight", !aloEverRight);
        check("batch size 1 does not save at-least-once", Simulation.run(Strategy.AT_LEAST_ONCE, log, 3, 1).duplicates() == 1);
        check("batch size 1 does not save at-most-once either", Simulation.run(Strategy.AT_MOST_ONCE, log, 3, 1).lost() == 0
                && Simulation.run(Strategy.AT_MOST_ONCE, log, 3, 2).lost() == 0
                && Simulation.run(Strategy.AT_MOST_ONCE, log, 2, 2).lost() == 1);

        // the store's primitives
        Store store = new Store();
        check("postIfNew accepts the first", store.postIfNew(new Posting("pay-1", Money.ofMinor(100, USD))));
        check("postIfNew rejects the duplicate", !store.postIfNew(new Posting("pay-1", Money.ofMinor(100, USD))));
        check("duplicate did not change the ledger", store.postings().size() == 1 && store.balance(USD).minorUnits() == 100);
        Store.Transaction t = store.begin();
        t.post(new Posting("pay-2", Money.ofMinor(250, USD)));
        t.commitOffset(7);
        check("uncommitted transaction is invisible", store.postings().size() == 1 && store.committedOffset() == 0);
        t.commit();
        check("committed transaction applies both writes", store.postings().size() == 2 && store.committedOffset() == 7);
        checkThrows("transaction cannot be reused", IllegalStateException.class, () -> t.post(new Posting("pay-3", Money.ofMinor(1, USD))));
        checkThrows("negative offset commit rejected", IllegalArgumentException.class, () -> store.commitOffset(-1));

        // the fault injector fires once
        FaultInjector f = new FaultInjector(2);
        f.afterSideEffect(1);
        checkThrows("fault fires at its offset", CrashException.class, () -> f.afterSideEffect(2));
        f.afterSideEffect(2);
        check("fault fires only once", f.fired());

        // money discipline
        check("Money.parse 19.99", Money.parse("19.99", USD).minorUnits() == 1999);
        checkThrows("Money rejects excess precision", ArithmeticException.class, () -> Money.parse("1.005", USD));
        checkThrows("Money rejects currency mix", CurrencyMismatchException.class,
                () -> Money.ofMinor(1, USD).plus(Money.ofMinor(1, new Currency("EUR", 2))));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f2 -> System.out.println("FAIL: " + f2));
        if (!failures.isEmpty()) System.exit(1);
    }
}
