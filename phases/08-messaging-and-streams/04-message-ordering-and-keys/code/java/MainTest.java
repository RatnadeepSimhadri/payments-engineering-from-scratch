// Tests for phases/08-messaging-and-streams/04-message-ordering-and-keys. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Set;

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

    static List<Long> seqs(List<Message> messages) {
        List<Long> out = new ArrayList<>();
        for (Message m : messages) out.add(m.seq());
        return out;
    }

    public static void main(String[] args) {
        // partitioner: deterministic, in range, and the same function as Lesson 01
        check("fnv1a32('a') known vector", Partitioner.fnv1a32("a".getBytes()) == 0xE40C292C);
        check("same key, same partition", Partitioner.partitionFor("acct-1001", 3) == Partitioner.partitionFor("acct-1001", 3));
        boolean inRange = true;
        for (int i = 0; i < 1000; i++) {
            int p = Partitioner.partitionFor("k" + i, 7);
            if (p < 0 || p >= 7) inRange = false;
        }
        check("partition index always in [0, N)", inRange);
        boolean someMoved = false;
        for (int i = 0; i < 100 && !someMoved; i++) someMoved = Partitioner.partitionFor("k" + i, 3) != Partitioner.partitionFor("k" + i, 4);
        check("changing partition count moves keys", someMoved);
        checkThrows("partitionCount must be positive", IllegalArgumentException.class, () -> Partitioner.partitionFor("x", 0));

        // a topic keeps every key in one partition and that key's seqs in order
        Topic topic = new Topic("t", 4);
        List<String> keys = List.of("a", "b", "c", "d", "e", "f", "g");
        List<Message> keyed = Workload.build(keys, 70, 1, 1, "none", 1);
        for (Message m : keyed) topic.append(m);
        boolean oneHome = true, orderedPerKey = true;
        for (String k : keys) {
            int home = -1;
            for (int p = 0; p < 4; p++) {
                List<Long> s = topic.partition(p).sequenceOf(k);
                if (s.isEmpty()) continue;
                if (home != -1) oneHome = false;
                home = p;
                for (int i = 0; i < s.size(); i++) if (s.get(i) != i) orderedPerKey = false;
            }
        }
        check("each key lives in exactly one partition", oneHome);
        check("per-key seqs are 0,1,2,... in offset order", orderedPerKey);
        long total = 0;
        for (int p = 0; p < 4; p++) total += topic.partition(p).logEndOffset();
        check("offsets are dense: partition sizes sum to the message count", total == 70);

        // head-of-line blocking under the sequential scheduler
        Partition hol = new Partition(0);
        List<String> holKeys = List.of("fast-1", "fast-2", "fast-3", "slow", "fast-4", "fast-5");
        for (Message m : Workload.build(holKeys, 60, 10, 2, "slow", 40)) hol.append(m);
        List<Completion> sequential = new SequentialScheduler().run(hol);
        check("sequential: offset 4 (fast) starts only after offset 3 (slow) completes", sequential.get(4).startedAt() == sequential.get(3).completedAt());
        check("sequential: a fast record waits at least the slow cost minus its arrival gap", sequential.get(4).latencyMillis() >= 40 - 10 + 2);
        check("sequential: completions are in offset order", sequential.get(10).completedAt() <= sequential.get(11).startedAt());
        check("sequential: per-key order holds trivially", Workload.perKeyOrderHolds(hol, sequential));
        LatencyStats seqFast = LatencyStats.excluding(sequential, "slow");
        check("sequential: fast accounts' max latency is inflated by the slow key", seqFast.max() >= 30);

        // per-key lanes: order per key, no blocking across lanes
        KeyedLaneScheduler eight = new KeyedLaneScheduler(8);
        List<Completion> laned = eight.run(hol);
        check("lanes: per-key order holds", Workload.perKeyOrderHolds(hol, laned));
        LatencyStats lanedFast = LatencyStats.excluding(laned, "slow");
        boolean slowAlone = true;
        for (String k : holKeys) if (!k.equals("slow") && eight.laneFor(k) == eight.laneFor("slow")) slowAlone = false;
        check("lanes: with the slow key alone in its lane, fast accounts see only their own cost", !slowAlone || lanedFast.max() == 2);
        check("lanes: the slow key still pays its own cost", LatencyStats.of(laned.stream().filter(c -> c.key().equals("slow")).toList()).max() >= 40);
        check("lanes: everything was eventually committable", eight.finalCommittable() == 60);
        check("lanes: some offsets finished before an earlier one (commit must lag)", eight.maxCompletedAhead() > 0);
        check("lanes: the same key always maps to the same lane", eight.laneFor("slow") == eight.laneFor("slow"));
        checkThrows("lanes must be positive", IllegalArgumentException.class, () -> new KeyedLaneScheduler(0));

        // commit tracker: committable is the low-water mark
        CommitTracker tracker = new CommitTracker(0);
        check("completing offset 1 first commits nothing", tracker.complete(1) == 0 && tracker.completedAhead() == 1);
        check("completing offset 0 then releases 0 and 1", tracker.complete(0) == 2 && tracker.completedAhead() == 0);
        tracker.complete(3);
        check("gap at 2 holds the mark at 2", tracker.committable() == 2);
        check("filling the gap advances past everything finished", tracker.complete(2) == 4);
        checkThrows("completing an offset twice is a bug", IllegalStateException.class, () -> tracker.complete(3));
        checkThrows("completing an already-committed offset is a bug", IllegalStateException.class, () -> tracker.complete(0));

        // latency stats
        List<Completion> fixed = List.of(new Completion(0, "k", 0, 10, 10), new Completion(1, "k", 0, 30, 30), new Completion(2, "k", 0, 20, 20), new Completion(3, "k", 0, 40, 40));
        LatencyStats stats = LatencyStats.of(fixed);
        check("stats: max, mean, p50 over 10,20,30,40", stats.max() == 40 && stats.mean() == 25 && stats.p50() == 20 && stats.count() == 4);
        check("stats: empty list is all zeros", LatencyStats.of(List.of()).count() == 0);

        // retry-induced reordering and sequence numbers
        List<Message> sends = Workload.build(List.of("acct"), 8, 1, 1, "none", 1);
        FaultInjector faults = new FaultInjector(Set.of(2L, 5L));
        SendOutcome one = ProducerSim.deliver(sends, 1, faults, false);
        check("maxInFlight=1: retries keep order", seqs(one.appended()).equals(List.of(0L, 1L, 2L, 3L, 4L, 5L, 6L, 7L)) && one.retries() == 2);
        SendOutcome five = ProducerSim.deliver(sends, 5, faults, false);
        check("maxInFlight=5: a retried send lands after its successors", seqs(five.appended()).equals(List.of(0L, 1L, 3L, 4L, 2L, 6L, 7L, 5L)));
        check("nothing is lost or duplicated by the retry itself", five.appended().size() == 8);
        check("SequenceTracker flags the reordered log", !SequenceTracker.isOrdered(five.appended()) && SequenceTracker.isOrdered(one.appended()));
        SequenceTracker st = new SequenceTracker();
        for (Message m : five.appended()) st.observe(m);
        check("tracker counts 2 gaps and 2 late arrivals classified as duplicates", st.gaps() == 2 && st.duplicates() == 2);
        SendOutcome guarded = ProducerSim.deliver(sends, 5, faults, true);
        check("broker sequence guard restores order with the same faults", SequenceTracker.isOrdered(guarded.appended()) && guarded.appended().size() == 8);
        check("the guard works by rejecting out-of-sequence sends", guarded.rejected() == 3 && guarded.retries() == 2);
        SendOutcome noFaults = ProducerSim.deliver(sends, 5, new FaultInjector(Set.of()), false);
        check("no faults: no reordering even with 5 in flight", SequenceTracker.isOrdered(noFaults.appended()) && noFaults.retries() == 0);
        FaultInjector seeded = FaultInjector.random(42, 2000, 0.05);
        List<Message> many = Workload.build(List.of("acct"), 2000, 1, 1, "none", 1);
        SendOutcome unguarded = ProducerSim.deliver(many, 5, seeded, false);
        SendOutcome guardedMany = ProducerSim.deliver(many, 5, seeded, true);
        check("seeded run: unguarded is reordered, guarded is not, both complete", !SequenceTracker.isOrdered(unguarded.appended())
                && SequenceTracker.isOrdered(guardedMany.appended()) && unguarded.appended().size() == 2000 && guardedMany.appended().size() == 2000);
        check("seeded fault injector is deterministic", FaultInjector.random(42, 2000, 0.05).failsFirstAttempt(7) == seeded.failsFirstAttempt(7));
        checkThrows("maxInFlight must be positive", IllegalArgumentException.class, () -> ProducerSim.deliver(sends, 0, faults, false));

        // messages reject bad construction
        checkThrows("negative seq rejected", IllegalArgumentException.class, () -> new Message("k", -1, "AUTH", Money.ofMinor(1, Currency.USD), 0, 1));
        check("money prints in major units", Money.ofMinor(1999, Currency.USD).toString().equals("19.99 USD"));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
