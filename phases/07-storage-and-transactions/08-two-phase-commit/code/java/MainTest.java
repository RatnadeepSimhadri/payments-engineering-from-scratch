// Tests for phases/07-storage-and-transactions/08-two-phase-commit. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

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

    /** A fresh two-ledger world: alice holds 500.00 at bank-a, bob holds 100.00 at bank-b. */
    static final class World {
        final SimClock clock = new SimClock(0);
        final SimNetwork net = new SimNetwork(clock, 5, 1_000);
        final Ledger a = new Ledger("bank-a", Map.of("alice", Main.usd("500.00")));
        final Ledger b = new Ledger("bank-b", Map.of("bob", Main.usd("100.00")));
        Coordinator coord = new Coordinator(net);
        World() { net.register(a); net.register(b); }
        Money total() { return a.total(Currency.USD).plus(b.total(Currency.USD)); }
    }

    public static void main(String[] args) {
        // happy path: atomic across two ledgers, money conserved
        {
            World w = new World();
            Outcome out = w.coord.transfer("tx-1", w.a, "alice", w.b, "bob", Main.usd("150.00"));
            check("happy path commits", out.decision() == Decision.COMMIT && out.complete());
            check("alice debited", w.a.balance("alice").equals(Main.usd("350.00")));
            check("bob credited", w.b.balance("bob").equals(Main.usd("250.00")));
            check("total conserved", w.total().equals(Main.usd("600.00")));
            check("locks released after commit", !w.a.isLocked("alice") && !w.b.isLocked("bob"));
            check("coordinator log is BEGIN, DECISION, END", w.coord.logRecords().size() == 3
                    && w.coord.logRecords().get(1).kind().equals("DECISION")
                    && w.coord.logRecords().get(2).kind().equals("END"));
            check("participant log is PREPARED then COMMITTED", w.a.logRecords().get(0).kind().equals("PREPARED")
                    && w.a.logRecords().get(1).kind().equals("COMMITTED"));
        }

        // participant votes NO: nothing moves, nothing stays locked
        {
            World w = new World();
            Outcome out = w.coord.transfer("tx-2", w.a, "alice", w.b, "bob", Main.usd("900.00"));
            check("insufficient funds aborts", out.decision() == Decision.ABORT && out.reason().contains("voted NO"));
            check("abort leaves balances untouched", w.a.balance("alice").equals(Main.usd("500.00")) && w.b.balance("bob").equals(Main.usd("100.00")));
            check("abort leaves no locks", !w.a.isLocked("alice") && !w.b.isLocked("bob"));
            check("abort is logged at the coordinator", w.coord.decisionOf("tx-2").orElse(null) == Decision.ABORT);
        }

        // prepare timeout is a NO vote
        {
            World w = new World();
            w.net.partition("bank-b");
            Outcome out = w.coord.transfer("tx-3", w.a, "alice", w.b, "bob", Main.usd("10.00"));
            check("prepare timeout aborts", out.decision() == Decision.ABORT && out.reason().contains("timed out"));
            check("clock advanced by the timeout", w.clock.now() >= 1_000);
            check("bank-a released its lock after abort", w.a.stateOf("tx-3") == TxState.ABORTED && !w.a.isLocked("alice"));
            check("abort delivery to the partitioned node is pending", w.coord.pendingTransactions().contains("tx-3"));
            w.net.heal("bank-b");
            check("retry finishes delivery", w.coord.retryPending() == 1 && w.coord.pendingTransactions().isEmpty());
        }

        // the blocking problem: coordinator dies between votes and decision
        {
            World w = new World();
            w.coord.crashPoint = CrashPoint.AFTER_VOTES_BEFORE_DECISION_LOG;
            checkThrows("coordinator crash surfaces", CrashException.class,
                    () -> w.coord.transfer("tx-4", w.a, "alice", w.b, "bob", Main.usd("25.00")));
            check("both participants are in doubt", w.a.inDoubt().contains("tx-4") && w.b.inDoubt().contains("tx-4"));
            check("in-doubt accounts stay locked", w.a.isLocked("alice") && w.b.isLocked("bob"));
            check("a second transaction on a locked account is refused",
                    w.a.prepare("tx-5", new Posting("alice", Main.usd("-1.00"))) == Vote.NO);
            Ledger recoveredB = w.b.crashAndRecover();
            check("participant recovery keeps the in-doubt lock", recoveredB.inDoubt().contains("tx-4") && recoveredB.isLocked("bob"));
            check("participant recovery keeps the balance", recoveredB.balance("bob").equals(Main.usd("100.00")));
            w.coord = Coordinator.recover(w.coord, w.net);
            check("coordinator recovery presumes abort", w.coord.decisionOf("tx-4").orElse(null) == Decision.ABORT);
            check("recovery releases the participants", w.a.stateOf("tx-4") == TxState.ABORTED && w.b.stateOf("tx-4") == TxState.ABORTED
                    && !w.a.isLocked("alice") && !w.b.isLocked("bob"));
            check("total conserved through crash", w.total().equals(Main.usd("600.00")));
        }

        // coordinator dies after the decision record: recovery redelivers COMMIT
        {
            World w = new World();
            w.coord.crashPoint = CrashPoint.AFTER_DECISION_LOG_BEFORE_SEND;
            checkThrows("crash after decision log", CrashException.class,
                    () -> w.coord.transfer("tx-6", w.a, "alice", w.b, "bob", Main.usd("40.00")));
            check("participants still prepared", w.a.stateOf("tx-6") == TxState.PREPARED && w.b.stateOf("tx-6") == TxState.PREPARED);
            w.coord = Coordinator.recover(w.coord, w.net);
            check("recovery redelivers COMMIT", w.a.stateOf("tx-6") == TxState.COMMITTED && w.b.stateOf("tx-6") == TxState.COMMITTED);
            check("committed balances after recovery", w.a.balance("alice").equals(Main.usd("460.00")) && w.b.balance("bob").equals(Main.usd("140.00")));
            check("recovered log ends with END", w.coord.logRecords().get(w.coord.logRecords().size() - 1).kind().equals("END"));
            check("recovery is idempotent", Coordinator.recover(w.coord, w.net).pendingTransactions().isEmpty()
                    && w.a.balance("alice").equals(Main.usd("460.00")));
        }

        // participant crashes after voting YES: decision is final, delivery retries, commit is idempotent
        {
            World w = new World();
            w.net.crashAfterVote("bank-b");
            Outcome out = w.coord.transfer("tx-7", w.a, "alice", w.b, "bob", Main.usd("5.00"));
            check("decision stands despite unreachable participant", out.decision() == Decision.COMMIT && !out.complete());
            check("reachable participant committed, other still prepared", w.a.stateOf("tx-7") == TxState.COMMITTED && w.b.stateOf("tx-7") == TxState.PREPARED);
            check("retry while partitioned makes no progress", w.coord.retryPending() == 0);
            Ledger recoveredB = w.b.crashAndRecover();
            check("recovered participant is in doubt with the lock held", recoveredB.inDoubt().contains("tx-7") && recoveredB.isLocked("bob"));
            w.net.replace(recoveredB);
            w.net.heal("bank-b");
            check("retry after heal completes", w.coord.retryPending() == 1 && recoveredB.stateOf("tx-7") == TxState.COMMITTED);
            check("recovered participant applied the posting once", recoveredB.balance("bob").equals(Main.usd("105.00")));
            recoveredB.commit("tx-7");
            check("duplicate commit is a no-op", recoveredB.balance("bob").equals(Main.usd("105.00")));
            checkThrows("abort after commit is refused", IllegalStateException.class, () -> recoveredB.abort("tx-7"));
            checkThrows("commit without prepare is refused", IllegalStateException.class, () -> w.a.commit("never-prepared"));
        }

        // a three-participant run with one NO in the middle: earlier YES voters are released
        {
            World w = new World();
            Ledger c = new Ledger("bank-c", Map.of("carol", Main.usd("0.00")));
            w.net.register(c);
            Map<String, Posting> postings = new LinkedHashMap<>();
            postings.put("bank-a", new Posting("alice", Main.usd("-10.00")));
            postings.put("bank-c", new Posting("carol", Main.usd("-1.00")));   // carol cannot pay
            postings.put("bank-b", new Posting("bob", Main.usd("11.00")));
            Outcome out = w.coord.run("tx-8", postings);
            check("one NO aborts the whole transaction", out.decision() == Decision.ABORT);
            check("earlier YES voter is released", w.a.stateOf("tx-8") == TxState.ABORTED && !w.a.isLocked("alice"));
            check("later participant never prepared but is told to abort", w.b.stateOf("tx-8") == TxState.ABORTED);
        }

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
