// Tests for phases/07-storage-and-transactions/12-saga-pattern. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
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

    /** A fresh world: alice has 100.00 USD, five widgets in stock, one orchestrator over one log. */
    static final class World {
        final SimClock clock = new SimClock(0);
        final WalletService wallet = new WalletService(Map.of("alice", Main.usd("100.00")));
        final ShippingService shipping = new ShippingService(Map.of("widget", 5));
        final SagaLog log = new SagaLog(clock);
        final SagaOrchestrator saga = new SagaOrchestrator(log,
                List.of(new ReserveFunds(wallet), new CaptureFunds(wallet), new ShipOrder(shipping)), clock, 10);
        Order order(String id, int qty, String amount) { return new Order(id, "alice", "widget", qty, Main.usd(amount)); }
        List<String> kinds(String sagaId) { return log.forSaga(sagaId).stream().map(r -> r.kind() + (r.step().isEmpty() ? "" : ":" + r.step())).toList(); }
    }

    public static void main(String[] args) {
        { // happy path
            World w = new World();
            SagaOutcome out = w.saga.run(w.order("s1", 2, "30.00"));
            check("happy path completes", out.status() == SagaStatus.COMPLETED);
            check("funds captured", w.wallet.balance("alice").equals(Main.usd("70.00")));
            check("no hold remains", !w.wallet.hasHold("s1") && w.wallet.available("alice").equals(Main.usd("70.00")));
            check("stock shipped", w.shipping.stockOf("widget") == 3 && w.shipping.isShipped("s1"));
            check("log has a started and completed record per step", w.kinds("s1").equals(List.of(
                    "SAGA_STARTED", "STEP_STARTED:reserve-funds", "STEP_COMPLETED:reserve-funds",
                    "STEP_STARTED:capture-funds", "STEP_COMPLETED:capture-funds",
                    "STEP_STARTED:ship-order", "STEP_COMPLETED:ship-order", "SAGA_COMPLETED")));
            check("clock advanced once per step", w.clock.now() == 30);
        }
        { // failure at step three compensates in reverse order
            World w = new World();
            SagaOutcome out = w.saga.run(w.order("s2", 9, "40.00"));
            check("out of stock compensates", out.status() == SagaStatus.COMPENSATED && out.failedStep().equals("ship-order"));
            check("balance restored by refund", w.wallet.balance("alice").equals(Main.usd("100.00")));
            check("hold released", w.wallet.available("alice").equals(Main.usd("100.00")) && !w.wallet.hasHold("s2"));
            check("stock untouched", w.shipping.stockOf("widget") == 5);
            List<String> kinds = w.kinds("s2");
            check("compensations run in reverse order", kinds.indexOf("COMP_COMPLETED:capture-funds") < kinds.indexOf("COMP_STARTED:reserve-funds"));
            check("failed step is not compensated", !kinds.contains("COMP_STARTED:ship-order"));
            check("saga ends COMPENSATED", kinds.get(kinds.size() - 1).equals("SAGA_COMPENSATED"));
        }
        { // failure at step one: nothing to compensate
            World w = new World();
            SagaOutcome out = w.saga.run(w.order("s3", 1, "250.00"));
            check("insufficient funds fails at reserve", out.status() == SagaStatus.COMPENSATED && out.failedStep().equals("reserve-funds"));
            check("no compensation records when nothing completed", w.kinds("s3").stream().noneMatch(k -> k.startsWith("COMP_")));
            check("balance untouched", w.wallet.balance("alice").equals(Main.usd("100.00")));
        }
        { // crash after capture executed but before STEP_COMPLETED: recovery re-executes, idempotency prevents a double debit
            World w = new World();
            w.saga.crashPoint = CrashPoint.afterExecute("capture-funds");
            checkThrows("crash point fires", CrashException.class, () -> w.saga.run(w.order("s4", 1, "25.00")));
            check("capture already happened before the crash", w.wallet.balance("alice").equals(Main.usd("75.00")));
            check("log shows the saga still RUNNING", SagaOrchestrator.statusOf(w.log.forSaga("s4")) == SagaStatus.RUNNING);
            int callsBefore = w.wallet.calls;
            List<SagaOutcome> resumed = w.saga.recover();
            check("recovery completes the saga", resumed.size() == 1 && resumed.get(0).status() == SagaStatus.COMPLETED);
            check("capture was replayed exactly once", w.wallet.calls == callsBefore + 1);
            check("no double debit", w.wallet.balance("alice").equals(Main.usd("75.00")));
            check("shipping happened after recovery", w.shipping.stockOf("widget") == 4);
            check("recovery is idempotent", w.saga.recover().isEmpty());
        }
        { // crash during compensation: refund done, COMP_COMPLETED not logged
            World w = new World();
            w.saga.crashPoint = CrashPoint.afterCompensate("capture-funds");
            checkThrows("crash during compensation", CrashException.class, () -> w.saga.run(w.order("s5", 9, "10.00")));
            check("refund already applied", w.wallet.balance("alice").equals(Main.usd("100.00")));
            check("log shows COMPENSATING", SagaOrchestrator.statusOf(w.log.forSaga("s5")) == SagaStatus.COMPENSATING);
            List<SagaOutcome> resumed = w.saga.recover();
            check("recovery finishes compensation", resumed.get(0).status() == SagaStatus.COMPENSATED && resumed.get(0).failedStep().equals("ship-order"));
            check("no double refund", w.wallet.balance("alice").equals(Main.usd("100.00")));
            check("hold released by resumed compensation", !w.wallet.hasHold("s5") && w.wallet.available("alice").equals(Main.usd("100.00")));
        }
        { // the semantic lock: a pending hold is visible to a second saga
            World w = new World();
            w.saga.crashPoint = CrashPoint.afterExecute("reserve-funds");
            checkThrows("saga A stalls after reserving", CrashException.class, () -> w.saga.run(w.order("A", 1, "80.00")));
            check("balance unchanged but available reduced", w.wallet.balance("alice").equals(Main.usd("100.00")) && w.wallet.available("alice").equals(Main.usd("20.00")));
            SagaOutcome b = w.saga.run(w.order("B", 1, "50.00"));
            check("saga B refused by the semantic lock", b.status() == SagaStatus.COMPENSATED && b.reason().contains("insufficient available"));
            w.saga.recover();
            check("saga A completed after recovery and the lock is gone", w.wallet.balance("alice").equals(Main.usd("20.00")) && w.wallet.available("alice").equals(Main.usd("20.00")));
        }
        { // idempotency of the services themselves
            WalletService wallet = new WalletService(Map.of("alice", Main.usd("10.00")));
            wallet.reserve("k", "alice", Main.usd("4.00"));
            wallet.reserve("k", "alice", Main.usd("4.00"));
            check("duplicate reserve holds once", wallet.available("alice").equals(Main.usd("6.00")));
            wallet.capture("k"); wallet.capture("k");
            check("duplicate capture debits once", wallet.balance("alice").equals(Main.usd("6.00")));
            wallet.refund("k", "alice"); wallet.refund("k", "alice");
            check("duplicate refund credits once", wallet.balance("alice").equals(Main.usd("10.00")));
            checkThrows("capture without hold fails the step", StepFailedException.class, () -> wallet.capture("never-reserved"));
            wallet.release("never-reserved");
            check("release of a hold that never existed is a no-op", wallet.available("alice").equals(Main.usd("10.00")));
        }
        { // money discipline
            checkThrows("currency mismatch is rejected", CurrencyMismatchException.class,
                    () -> Main.usd("1.00").plus(Money.ofMinor(100, new Currency("EUR", 2))));
            check("toString pads minor units", Main.usd("0.05").toString().equals("0.05 USD"));
        }

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
