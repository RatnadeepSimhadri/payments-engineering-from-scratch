// Tests for phases/04-concurrency/08-the-actor-model. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;

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

    static Money usd(long minor) { return Money.ofMinor(minor, Currency.USD); }
    static AccountActor account(ActorSystem s, String name) { return (AccountActor) Main.actorOf(s, name); }
    static ProbeActor probe(ActorSystem s) { return (ProbeActor) Main.actorOf(s, "probe"); }

    public static void main(String[] args) throws Exception {
        money();
        singleAccount();
        transfers();
        determinism();
        conservationAcrossSeeds();
        supervision();
        systemRules();
        threadedHotAccount();
        threadedTransfers();

        System.out.println(passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL: " + f);
        System.exit(failures.isEmpty() ? 0 : 1);
    }

    static void money() {
        check("toString", usd(12_345).toString().equals("123.45 USD"));
        check("minus can go negative but isNegative reports it", usd(100).minus(usd(150)).isNegative());
        checkThrows("currency mismatch on plus", CurrencyMismatchException.class, () -> usd(1).plus(Money.ofMinor(1, Currency.EUR)));
    }

    static void singleAccount() {
        DeterministicScheduler sched = new DeterministicScheduler(0);
        ActorSystem s = new ActorSystem(sched, true);
        ActorRef probe = s.spawn("probe", ProbeActor::new, SupervisorStrategy.RESUME);
        ActorRef acct = s.spawn("acct-1", () -> new AccountActor("acct-1", usd(10_000)), SupervisorStrategy.RESUME);
        acct.tell(new Deposit("d1", usd(2_500), probe));
        acct.tell(new Withdraw("w1", usd(1_999), probe));
        acct.tell(new Withdraw("w2", usd(50_000), probe));
        acct.tell(new GetBalance(probe));
        check("nothing happens until the scheduler runs", account(s, "acct-1").balance().equals(usd(10_000)) && probe(s).received.isEmpty());
        int delivered = sched.runUntilIdle();
        check("every message delivered exactly once", delivered == 8 && sched.steps() == 8);
        List<Message> got = probe(s).received;
        check("deposit reply carries the new balance", got.get(0) instanceof Deposited d && d.balance().equals(usd(12_500)));
        check("withdraw reply carries amount and balance", got.get(1) instanceof Withdrawn w && w.amount().equals(usd(1_999)) && w.balance().equals(usd(10_501)));
        check("overdraft is refused with the real balance", got.get(2) instanceof InsufficientFunds f && f.requested().equals(usd(50_000)) && f.balance().equals(usd(10_501)));
        check("balance query sees the serialised result", got.get(3) instanceof BalanceIs b && b.balance().equals(usd(10_501)));
        check("refused withdrawal left the balance untouched", account(s, "acct-1").balance().equals(usd(10_501)));
        List<String> acctTrace = s.trace().stream().filter(line -> line.startsWith("acct-1 <- ")).toList();
        check("messages are processed in mailbox order", acctTrace.get(0).startsWith("acct-1 <- Deposit") && acctTrace.get(1).startsWith("acct-1 <- Withdraw(w1")
                && acctTrace.get(2).startsWith("acct-1 <- Withdraw(w2") && acctTrace.get(3).equals("acct-1 <- GetBalance"));
        checkThrows("opening balance must not be negative", IllegalArgumentException.class, () -> new AccountActor("x", usd(-1)));
    }

    static void transfers() {
        DeterministicScheduler sched = new DeterministicScheduler(0);
        ActorSystem s = Main.bankWith(sched, true);
        ActorRef probe = s.spawn("probe", ProbeActor::new, SupervisorStrategy.RESUME);
        s.ref("transfers").tell(new Transfer("ok", "acct-A", "acct-B", usd(7_500), probe));
        s.ref("transfers").tell(new Transfer("bad", "acct-C", "acct-A", usd(99_999), probe));
        sched.runUntilIdle();
        List<TransferResult> results = probe(s).transferResults();
        check("two results", results.size() == 2);
        TransferResult ok = results.stream().filter(r -> r.transferId().equals("ok")).findFirst().get();
        TransferResult bad = results.stream().filter(r -> r.transferId().equals("bad")).findFirst().get();
        check("successful transfer reports ok", ok.ok());
        check("source debited and destination credited by exactly the amount", account(s, "acct-A").balance().equals(usd(2_500)) && account(s, "acct-B").balance().equals(usd(17_500)));
        check("failed transfer explains itself", !bad.ok() && bad.reason().equals("acct-C has 100.00 USD, needs 999.99 USD"));
        check("failed transfer moved nothing", account(s, "acct-C").balance().equals(usd(10_000)));
        TransferCoordinator coordinator = (TransferCoordinator) Main.actorOf(s, "transfers");
        check("coordinator bookkeeping", coordinator.completed() == 1 && coordinator.rejected() == 1 && coordinator.inFlight() == 0);
        check("transfer is two hops: withdraw then deposit", s.trace().contains("acct-A <- Withdraw(ok, 75.00 USD)") && s.trace().contains("acct-B <- Deposit(ok, 75.00 USD)"));
    }

    static List<String> runScript(long seed) {
        DeterministicScheduler sched = new DeterministicScheduler(seed);
        ActorSystem s = Main.bankWith(sched, true);
        ActorRef probe = s.spawn("probe", ProbeActor::new, SupervisorStrategy.RESUME);
        for (Transfer t : Main.scriptedTransfers(probe)) s.ref("transfers").tell(t);
        sched.runUntilIdle();
        return new ArrayList<>(s.trace());
    }

    static void determinism() {
        check("same seed, same trace", runScript(42).equals(runScript(42)));
        check("round-robin seed 0 is reproducible too", runScript(0).equals(runScript(0)));
        boolean someDifference = false;
        List<String> base = runScript(1);
        for (long seed = 2; seed <= 10 && !someDifference; seed++) someDifference = !runScript(seed).equals(base);
        check("different seeds explore different interleavings", someDifference);
        check("every run delivers the same number of messages", runScript(3).size() == 32 && runScript(9).size() == 32);
    }

    static void conservationAcrossSeeds() {
        boolean conserved = true, neverNegative = true, allAnswered = true, nothingInFlight = true;
        for (long seed = 0; seed < 25; seed++) {
            DeterministicScheduler sched = new DeterministicScheduler(seed);
            ActorSystem s = Main.bankWith(sched, false);
            ActorRef probe = s.spawn("probe", ProbeActor::new, SupervisorStrategy.RESUME);
            for (Transfer t : Main.scriptedTransfers(probe)) s.ref("transfers").tell(t);
            sched.runUntilIdle();
            conserved &= Main.total(s, "acct-A", "acct-B", "acct-C").equals(usd(30_000));
            for (String a : List.of("acct-A", "acct-B", "acct-C")) neverNegative &= !account(s, a).balance().isNegative();
            allAnswered &= probe(s).transferResults().size() == 6;
            nothingInFlight &= ((TransferCoordinator) Main.actorOf(s, "transfers")).inFlight() == 0;
        }
        check("money is conserved under 25 interleavings", conserved);
        check("no balance ever goes negative", neverNegative);
        check("every transfer gets exactly one result", allAnswered);
        check("coordinator finishes with nothing in flight", nothingInFlight);
    }

    static void supervision() {
        DeterministicScheduler sched = new DeterministicScheduler(0);
        ActorSystem s = new ActorSystem(sched, false);
        ActorRef probe = s.spawn("probe", ProbeActor::new, SupervisorStrategy.RESUME);
        ActorRef acct = s.spawn("acct-X", () -> new AccountActor("acct-X", usd(5_000)), SupervisorStrategy.RESUME);
        ActorRef resumed = s.spawn("c-resume", CounterActor::new, SupervisorStrategy.RESUME);
        ActorRef restarted = s.spawn("c-restart", CounterActor::new, SupervisorStrategy.RESTART);
        ActorRef stopped = s.spawn("c-stop", CounterActor::new, SupervisorStrategy.STOP);
        acct.tell(new Deposit("eur", Money.ofMinor(100, Currency.EUR), probe));
        acct.tell(new Deposit("usd", usd(100), probe));
        for (ActorRef c : List.of(resumed, restarted, stopped)) {
            c.tell(new Increment()); c.tell(new Increment()); c.tell(new Crash("boom")); c.tell(new Increment()); c.tell(new GetBalance(probe));
        }
        sched.runUntilIdle();
        check("a throwing message is dropped and the next one is processed", probe(s).received.stream().anyMatch(m -> m instanceof Deposited d && d.correlationId().equals("usd") && d.balance().equals(usd(5_100))));
        check("RESUME keeps the account's state", account(s, "acct-X").balance().equals(usd(5_100)) && s.cell("acct-X").failures.get() == 1 && s.cell("acct-X").restarts.get() == 0);
        List<Long> counts = new ArrayList<>();
        for (Message m : probe(s).received) if (m instanceof CountIs c) counts.add(c.count());
        check("RESUME counter keeps 2 and adds 1", counts.size() == 2 && counts.get(0) == 3L);
        check("RESTART counter lost its state and counted only the post-restart increment", counts.get(1) == 1L && s.cell("c-restart").restarts.get() == 1);
        check("STOP discards the rest of the mailbox as dead letters", s.cell("c-stop").stopped && s.cell("c-stop").deadLetters.get() == 2 && s.deadLetters() == 2);
        stopped.tell(new Increment());
        sched.runUntilIdle();
        check("messages to a stopped actor are dead letters too", s.cell("c-stop").deadLetters.get() == 3);
        check("failures are recorded for operators", s.failures().size() == 4 && s.failures().get(0).contains("acct-X failed on Deposit(eur"));
        check("the wrong-currency deposit never reached the probe", probe(s).received.stream().noneMatch(m -> m instanceof Deposited d && d.correlationId().equals("eur")));
    }

    static void systemRules() {
        DeterministicScheduler sched = new DeterministicScheduler(0);
        ActorSystem s = new ActorSystem(sched, false);
        ActorRef a = s.spawn("a", CounterActor::new, SupervisorStrategy.RESUME);
        checkThrows("duplicate actor names are rejected", IllegalArgumentException.class, () -> s.spawn("a", CounterActor::new, SupervisorStrategy.RESUME));
        checkThrows("lookup of an unknown actor fails loudly", IllegalArgumentException.class, () -> s.ref("nobody"));
        checkThrows("null messages are rejected at tell()", NullPointerException.class, () -> a.tell(null));
        check("an idle scheduler reports no step", !sched.step());
        a.tell(new Increment());
        check("one message, one step", sched.step() && !sched.step());
    }

    static void threadedHotAccount() throws InterruptedException {
        try (ExecutorService pool = Executors.newFixedThreadPool(4)) {
            ActorSystem s = new ActorSystem(new ThreadedScheduler(pool, 32), false);
            ActorRef acct = s.spawn("hot", () -> new AccountActor("hot", usd(0)), SupervisorStrategy.RESUME);
            int threads = 4, perThread = 10_000;
            CountDownLatch done = new CountDownLatch(threads);
            Thread[] producers = new Thread[threads];
            for (int t = 0; t < threads; t++) {
                producers[t] = new Thread(() -> {
                    for (int i = 0; i < perThread; i++) acct.tell(new Deposit("x", usd(1), null));
                    done.countDown();
                });
                producers[t].start();
            }
            done.await();
            ActorRef probe = s.spawn("probe", () -> new ProbeActor(1), SupervisorStrategy.RESUME);
            acct.tell(new GetBalance(probe));
            check("probe answered in time", probe(s).latch.await(10, TimeUnit.SECONDS));
            check("40,000 concurrent deposits produce an exact balance with no lock", probe(s).balances().get("hot").equals(usd(threads * perThread)));
            check("receive() never ran on two threads at once", s.cell("hot").maxConcurrentReceives.get() == 1);
            check("every message processed exactly once", s.cell("hot").processed.get() == threads * perThread + 1);
        }
    }

    static void threadedTransfers() throws InterruptedException {
        try (ExecutorService pool = Executors.newFixedThreadPool(4)) {
            ActorSystem s = Main.bankWith(new ThreadedScheduler(pool, 16), false);
            int transfers = 3_000;
            ActorRef probe = s.spawn("probe", () -> new ProbeActor(transfers), SupervisorStrategy.RESUME);
            String[] names = {"acct-A", "acct-B", "acct-C"};
            Thread[] producers = new Thread[3];
            for (int t = 0; t < 3; t++) {
                final int id = t;
                producers[t] = new Thread(() -> {
                    for (int i = 0; i < transfers / 3; i++) {
                        String from = names[(id + i) % 3], to = names[(id + i + 1) % 3];
                        s.ref("transfers").tell(new Transfer("t" + id + "-" + i, from, to, usd(1 + (i % 700)), probe));
                    }
                });
                producers[t].start();
            }
            for (Thread p : producers) p.join();
            check("all transfer results arrived", probe(s).latch.await(10, TimeUnit.SECONDS));
            Map<String, Money> balances;
            ActorRef probe2 = s.spawn("probe2", () -> new ProbeActor(3), SupervisorStrategy.RESUME);
            for (String n : names) s.ref(n).tell(new GetBalance(probe2));
            ProbeActor p2 = (ProbeActor) Main.actorOf(s, "probe2");
            check("balances reported", p2.latch.await(10, TimeUnit.SECONDS));
            balances = p2.balances();
            Money total = usd(0);
            for (Money m : balances.values()) total = total.plus(m);
            check("money conserved across 3,000 concurrent transfers", total.equals(usd(30_000)));
            check("no account went negative", balances.values().stream().noneMatch(Money::isNegative));
            check("each account processed on one thread at a time", s.cell("acct-A").maxConcurrentReceives.get() == 1 && s.cell("transfers").maxConcurrentReceives.get() == 1);
        }
    }
}
