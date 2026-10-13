// Tests for phases/14-payments-at-scale/03-exactly-once-money-movement. Run: javac -d .out *.java && java -cp .out MainTest
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

    static PaymentRequest request(String key, long cents) {
        return new PaymentRequest(key, "cust-1", "merch-1", Money.ofMinor(cents, Currency.USD));
    }

    /** Wire a full pipeline whose only fault is the given point, firing every time until disarmed. */
    record Pipeline(FaultInjector faults, GatewayStore gateway, PaymentApi api, Bus bus, OutboxRelay relay,
                    LedgerStore downstream, LedgerConsumer consumer) {
        static Pipeline alwaysFailingAt(FaultPoint point, Defences defences) {
            FaultInjector faults = new FaultInjector(1, point == null ? Map.of() : Map.of(point, 1.0));
            GatewayStore gateway = new GatewayStore();
            Bus bus = new Bus(faults);
            LedgerStore downstream = new LedgerStore();
            return new Pipeline(faults, gateway, new PaymentApi(gateway, faults, defences), bus,
                    new OutboxRelay(gateway, bus, faults), downstream, new LedgerConsumer(downstream, faults, defences));
        }

        /** Drain relay + bus with faults off, then return how many times the one event was posted downstream. */
        int drainAndCount(String eventId) {
            faults.disarm();
            while (!gateway.unpublished().isEmpty() || bus.pending() > 0) {
                relay.relayOnce();
                bus.deliver(consumer, 64);
            }
            return downstream.ledger.countEntries(eventId);
        }
    }

    public static void main(String[] args) {
        // ---- Money discipline
        check("money plus", Money.ofMinor(1999, Currency.USD).plus(Money.ofMinor(1, Currency.USD)).minorUnits() == 2000);
        check("money toString", Money.ofMinor(1999, Currency.USD).toString().equals("19.99 USD"));
        checkThrows("money currency mismatch", CurrencyMismatchException.class,
                () -> Money.ofMinor(1, Currency.USD).plus(Money.ofMinor(1, Currency.EUR)));

        // ---- Ledger
        Ledger ledger = new Ledger(Currency.USD);
        ledger.post(JournalEntry.transfer("e1", "asset:a", "liability:b", Money.ofMinor(500, Currency.USD)));
        check("ledger trial balance zero", ledger.trialBalance().isZero());
        check("ledger balances signed", ledger.balance("asset:a").minorUnits() == 500 && ledger.balance("liability:b").minorUnits() == -500);
        check("ledger creditTotal", ledger.creditTotal("liability:").minorUnits() == 500);
        checkThrows("ledger rejects non-positive transfer", IllegalArgumentException.class,
                () -> JournalEntry.transfer("e2", "a", "b", Money.zero(Currency.USD)));
        ledger.post(JournalEntry.transfer("e1", "asset:a", "liability:b", Money.ofMinor(500, Currency.USD)));
        check("ledger itself does not dedupe (that is the layer in front)", ledger.countEntries("e1") == 2);

        // ---- Idempotent API, happy path
        GatewayStore store = new GatewayStore();
        PaymentApi api = new PaymentApi(store, FaultInjector.none(), Defences.all());
        Response first = api.createPayment(request("k1", 1000));
        Response again = api.createPayment(request("k1", 1000));
        check("first call is not a replay", !first.replayed());
        check("same key replays the same payment", again.replayed() && again.payment().paymentId().equals(first.payment().paymentId()));
        check("one payment, one key, one outbox row, one ledger entry",
                store.payments.size() == 1 && store.idempotencyKeys.size() == 1 && store.outbox.size() == 1 && store.ledger.journal.size() == 1);
        check("gateway ledger debits receivable and credits clearing",
                store.ledger.balance(GatewayStore.RECEIVABLE + "cust-1").minorUnits() == 1000
                        && store.ledger.balance(GatewayStore.CLEARING).minorUnits() == -1000);
        checkThrows("same key with a different amount is a conflict", IdempotencyConflictException.class,
                () -> api.createPayment(request("k1", 1001)));
        check("conflict wrote nothing", store.payments.size() == 1 && store.commits == 1);

        // ---- Fault point: crash before commit writes nothing
        Pipeline p = Pipeline.alwaysFailingAt(FaultPoint.API_BEFORE_COMMIT, Defences.all());
        checkThrows("API_BEFORE_COMMIT surfaces as a crash", CrashException.class, () -> p.api().createPayment(request("k1", 1000)));
        check("crash before commit wrote nothing", p.gateway().payments.isEmpty() && p.gateway().outbox.isEmpty() && p.gateway().ledger.journal.isEmpty());

        // ---- Fault point: crash after commit, client retries, exactly one payment and one downstream posting
        Pipeline q = Pipeline.alwaysFailingAt(FaultPoint.API_AFTER_COMMIT, Defences.all());
        checkThrows("API_AFTER_COMMIT surfaces as a crash", CrashException.class, () -> q.api().createPayment(request("k1", 1000)));
        check("crash after commit kept payment, ledger entry, and outbox row together",
                q.gateway().payments.size() == 1 && q.gateway().outbox.size() == 1 && q.gateway().ledger.journal.size() == 1);
        q.faults().disarm();
        Response retried = q.api().createPayment(request("k1", 1000));
        check("retry after lost response is a replay", retried.replayed() && q.gateway().payments.size() == 1);
        check("posted exactly once downstream after API crash", q.drainAndCount("evt-" + retried.payment().paymentId()) == 1);

        // ---- Fault point: relay crashes after publish -> duplicate on the bus -> consumer inbox absorbs it
        Pipeline r = Pipeline.alwaysFailingAt(FaultPoint.RELAY_AFTER_PUBLISH, Defences.all());
        Response rp = r.api().createPayment(request("k1", 1000));
        checkThrows("RELAY_AFTER_PUBLISH surfaces as a crash", CrashException.class, () -> r.relay().relayOnce());
        check("event is on the bus but the row is still unpublished", r.bus().pending() == 1 && r.gateway().unpublished().size() == 1);
        check("posted exactly once downstream after relay crash", r.drainAndCount("evt-" + rp.payment().paymentId()) == 1);
        check("consumer skipped the duplicate", r.consumer().skippedDuplicates == 1 && r.bus().published == 2);

        // ---- Fault point: bus duplicates
        Pipeline d = Pipeline.alwaysFailingAt(FaultPoint.BUS_DUPLICATE, Defences.all());
        Response dp = d.api().createPayment(request("k1", 1000));
        d.relay().relayOnce();
        check("bus duplicated the publish", d.bus().pending() == 2 && d.bus().duplicated == 1);
        check("posted exactly once downstream after bus duplicate", d.drainAndCount("evt-" + dp.payment().paymentId()) == 1);

        // ---- Fault point: consumer crashes before commit -> redelivery -> posted once
        Pipeline cb = Pipeline.alwaysFailingAt(FaultPoint.CONSUMER_BEFORE_COMMIT, Defences.all());
        Response cbp = cb.api().createPayment(request("k1", 1000));
        cb.relay().relayOnce();
        checkThrows("CONSUMER_BEFORE_COMMIT surfaces as a crash", CrashException.class, () -> cb.bus().deliver(cb.consumer(), 8));
        check("message was put back for redelivery", cb.bus().pending() == 1 && cb.bus().redelivered == 1 && cb.downstream().ledger.journal.isEmpty());
        check("posted exactly once downstream after consumer crash before commit", cb.drainAndCount("evt-" + cbp.payment().paymentId()) == 1);

        // ---- Fault point: consumer crashes after commit (ack lost) -> redelivery of an already-processed message
        Pipeline ca = Pipeline.alwaysFailingAt(FaultPoint.CONSUMER_AFTER_COMMIT, Defences.all());
        Response cap = ca.api().createPayment(request("k1", 1000));
        ca.relay().relayOnce();
        checkThrows("CONSUMER_AFTER_COMMIT surfaces as a crash", CrashException.class, () -> ca.bus().deliver(ca.consumer(), 8));
        check("committed once, yet redelivered", ca.downstream().ledger.journal.size() == 1 && ca.bus().pending() == 1);
        check("posted exactly once downstream after lost ack", ca.drainAndCount("evt-" + cap.payment().paymentId()) == 1);

        // ---- Fault point: reordering never changes the outcome
        Pipeline ro = Pipeline.alwaysFailingAt(FaultPoint.BUS_REORDER, Defences.all());
        for (int i = 0; i < 5; i++) ro.api().createPayment(request("k" + i, 100 + i));
        ro.relay().relayOnce();
        ro.bus().deliver(ro.consumer(), 8);
        check("reordered batch still posts every event once", ro.bus().reorderedBatches == 1 && ro.downstream().ledger.journal.size() == 5
                && ro.downstream().ledger.creditTotal(LedgerStore.PAYABLE).minorUnits() == 510);

        // ---- The harness: fixed 8% schedule and random schedules over many seeds hold every invariant
        boolean allPassed = true;
        int totalCrashes = 0, totalDuplicates = 0;
        for (long seed = 1; seed <= 12; seed++) {
            HarnessReport report = Harness.run(HarnessConfig.standard(seed, 1500));
            allPassed &= report.passed();
            totalCrashes += report.crashes();
            totalDuplicates += report.busDuplicated();
        }
        check("uniform fault schedule: 12 seeds, zero violations", allPassed);
        check("uniform fault schedule actually injected faults", totalCrashes > 100 && totalDuplicates > 10);
        boolean randomPassed = true;
        for (long seed = 100; seed < 120; seed++) randomPassed &= Harness.run(HarnessConfig.randomSchedule(seed, 1000)).passed();
        check("random fault schedules: 20 seeds, zero violations", randomPassed);
        HarnessReport one = Harness.run(HarnessConfig.standard(5, 1500));
        check("report trial balances are zero on both ledgers", one.gatewayTrialBalance().isZero() && one.downstreamTrialBalance().isZero());
        check("report entry counts match accepted payments", one.gatewayEntries() == one.acceptedPayments() && one.downstreamEntries() == one.acceptedPayments());
        check("same seed reproduces the same run", Harness.run(HarnessConfig.standard(5, 1500)).summary().equals(one.summary()));

        // ---- Remove one defence at a time: the harness fails and names the invariant
        HarnessReport noApiDedupe = Harness.run(HarnessConfig.standard(1, 1500).without("apiDedupe"));
        check("without apiDedupe the run fails", !noApiDedupe.passed());
        check("without apiDedupe the broken invariant is one-payment-per-key", noApiDedupe.firstBrokenInvariant().equals("one-payment-per-key"));
        HarnessReport noOutbox = Harness.run(HarnessConfig.standard(1, 1500).without("atomicOutbox"));
        check("without atomicOutbox the run fails", !noOutbox.passed());
        check("without atomicOutbox the broken invariant is payment-has-outbox-row", noOutbox.firstBrokenInvariant().equals("payment-has-outbox-row"));
        HarnessReport noInbox = Harness.run(HarnessConfig.standard(1, 1500).without("consumerDedupe"));
        check("without consumerDedupe the run fails", !noInbox.passed());
        check("without consumerDedupe the broken invariant is posted-once-downstream", noInbox.firstBrokenInvariant().equals("posted-once-downstream"));
        check("without consumerDedupe money is no longer conserved", noInbox.violations().stream().anyMatch(v -> v.startsWith("[money-conserved]")));
        checkThrows("unknown defence name is rejected", IllegalArgumentException.class, () -> Defences.all().without("ledger"));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
