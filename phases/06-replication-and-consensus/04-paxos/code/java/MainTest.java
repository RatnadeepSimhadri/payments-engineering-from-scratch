// Tests for phases/06-replication-and-consensus/04-paxos. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.function.Predicate;

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
        // SplitMix64 reference vector (seed 0) so all three languages share one random stream
        Rng rng = new Rng(0);
        check("splitmix64 seed 0 first output", rng.nextLong() == 0xE220A8397B1DCDAFL);
        check("splitmix64 seed 0 second output", rng.nextLong() == 0x6E789E6AA1B965F4L);
        Rng r1 = new Rng(42), r2 = new Rng(42);
        check("same seed same stream", r1.nextInt(1000) == r2.nextInt(1000) && r1.nextDouble() == r2.nextDouble());

        // ballots are totally ordered and unique per proposer
        check("ballot: proposer id breaks ties", new Ballot(1, 4).compareTo(new Ballot(1, 3)) > 0);
        check("ballot: round dominates proposer id", new Ballot(2, 0).compareTo(new Ballot(1, 9)) > 0);
        check("ballot: NONE is below every real ballot", Ballot.NONE.compareTo(new Ballot(1, 0)) < 0 && Ballot.NONE.isNone());

        // acceptor: promise 1 (never accept below what was promised)
        Acceptor a = new Acceptor(0, new int[] {});
        List<Message> out = a.handle(Message.prepare(3, 0, new Ballot(2, 3)), 0);
        check("acceptor promises a higher ballot", out.size() == 1 && out.get(0).kind() == MessageKind.PROMISE && a.promised.equals(new Ballot(2, 3)));
        check("first promise reports nothing accepted", out.get(0).priorBallot().isNone() && out.get(0).value() == null);
        out = a.handle(Message.prepare(4, 0, new Ballot(1, 4)), 0);
        check("acceptor NACKs a lower prepare", out.get(0).kind() == MessageKind.NACK && out.get(0).priorBallot().equals(new Ballot(2, 3)));
        out = a.handle(Message.accept(4, 0, new Ballot(1, 4), "X"), 0);
        check("acceptor refuses to accept below its promise", out.get(0).kind() == MessageKind.NACK && a.acceptedBallot.isNone());
        out = a.handle(Message.accept(3, 0, new Ballot(2, 3), "Y"), 0);
        check("acceptor accepts the ballot it promised", out.get(0).kind() == MessageKind.ACCEPTED && "Y".equals(a.acceptedValue));
        check("acceptor forwards ACCEPTED to every learner", new Acceptor(1, new int[] {7, 8}).handle(Message.accept(3, 1, new Ballot(1, 3), "v"), 0).size() == 3);

        // acceptor: promise 2 (report what was accepted so the next proposer must adopt it)
        out = a.handle(Message.prepare(5, 0, new Ballot(3, 5)), 0);
        check("later promise carries the accepted ballot and value", out.get(0).priorBallot().equals(new Ballot(2, 3)) && "Y".equals(out.get(0).value()));

        // proposer adopts the highest-ballot value reported in promises, not its own
        Proposer p = new Proposer(3, new int[] {0, 1, 2}, 10);
        List<Message> prepares = p.propose("mine", 0);
        check("propose broadcasts PREPARE to every acceptor", prepares.size() == 3 && prepares.stream().allMatch(m -> m.kind() == MessageKind.PREPARE));
        Ballot b = p.ballot;
        p.handle(Message.promise(0, 3, b, new Ballot(1, 9), "old"), 1);
        List<Message> accepts = p.handle(Message.promise(1, 3, b, new Ballot(2, 9), "newer"), 1);
        check("majority of promises triggers ACCEPT broadcast", accepts.size() == 3 && accepts.get(0).kind() == MessageKind.ACCEPT);
        check("proposer adopts the value of the highest prior ballot", "newer".equals(p.proposing) && "newer".equals(accepts.get(0).value()));
        check("stale replies for an old ballot are ignored", p.handle(Message.promise(2, 3, new Ballot(0, 3), Ballot.NONE, null), 1).isEmpty());

        // proposer retries on NACK by jumping past the promised ballot
        Proposer q = new Proposer(4, new int[] {0, 1, 2}, 10);
        q.propose("q", 0);
        List<Message> retry = q.handle(Message.nack(0, 4, q.ballot, new Ballot(7, 3)), 1);
        check("NACK restarts with a round above the promised one", q.ballot.equals(new Ballot(8, 4)) && retry.size() == 3 && q.restarts == 1);
        check("timeout restarts a stalled round", q.tick(11).size() == 3 && q.round == 9);
        check("no timeout before the deadline", q.tick(12).isEmpty());

        // single proposer, lossless: chosen quickly, learner agrees, proposer decided
        PaxosCluster c1 = new PaxosCluster(3, 1, 1, 1, 0.0, 1, 10);
        c1.propose(0, "only");
        check("single proposer decides", c1.run(20) && "only".equals(c1.firstChosen));
        check("chosen within two round trips", c1.chosenAt <= 4);
        check("learner learns the chosen value", "only".equals(c1.learners.get(0).learned));
        check("proposer sees its own decision", "only".equals(c1.proposers.get(0).decided));

        // two proposers, lossless: exactly one value, and the loser adopts it on retry
        PaxosCluster c2 = new PaxosCluster(3, 2, 1, 7, 0.0, 1, 10);
        c2.propose(0, "A");
        c2.propose(1, "B");
        for (int i = 0; i < 12; i++) c2.tick();
        check("two proposers: one value chosen", c2.firstChosen != null && c2.firstChosen.equals(c2.chosenValue()));
        check("losing proposer decides the same value", c2.proposers.get(0).decided != null && c2.proposers.get(0).decided.equals(c2.proposers.get(1).decided));

        // safety across many seeded lossy runs (checkSafety throws on a second distinct chosen value)
        int decided = 0;
        boolean learnersAgree = true;
        for (long seed = 1; seed <= 200; seed++) {
            PaxosCluster c = new PaxosCluster(5, 3, 2, seed, 0.25, 3, 12);
            c.propose(0, "A"); c.propose(1, "B"); c.propose(2, "C");
            if (c.run(400)) decided++;
            for (Learner l : c.learners) if (l.learned != null && !l.learned.equals(c.firstChosen)) learnersAgree = false;
            for (Proposer pr : c.proposers) if (pr.decided != null && !pr.decided.equals(c.firstChosen)) learnersAgree = false;
        }
        check("200 lossy runs: never two chosen values (observer would have thrown)", true);
        check("200 lossy runs: learners and proposers only ever learn the chosen value", learnersAgree);
        check("200 lossy runs: liveness is probabilistic but good (>= 180 decided)", decided >= 180);

        // same seed replays the same run
        PaxosCluster d1 = new PaxosCluster(5, 3, 1, 99, 0.3, 3, 12);
        PaxosCluster d2 = new PaxosCluster(5, 3, 1, 99, 0.3, 3, 12);
        d1.propose(0, "A"); d1.propose(1, "B"); d1.propose(2, "C"); d1.run(400);
        d2.propose(0, "A"); d2.propose(1, "B"); d2.propose(2, "C"); d2.run(400);
        check("determinism: identical seed gives identical outcome and message counts",
                d1.network.delivered == d2.network.delivered && d1.network.dropped == d2.network.dropped
                        && d1.chosenAt == d2.chosenAt && String.valueOf(d1.firstChosen).equals(String.valueOf(d2.firstChosen)));

        // partition: a minority of acceptors cannot choose; healing lets the majority proceed
        PaxosCluster part = new PaxosCluster(5, 1, 1, 3, 0.0, 2, 8);
        part.network.isolate(0); part.network.isolate(1); part.network.isolate(2);
        part.propose(0, "P");
        boolean minorityDecided = part.run(100);
        check("three of five acceptors isolated: nothing can be chosen", !minorityDecided && part.chosenValue() == null);
        part.network.heal();
        check("after heal the proposer's retries succeed", part.run(100) && "P".equals(part.firstChosen));

        // constructed duel: no decision while prepares keep interleaving, decision as soon as the schedule relaxes
        PaxosCluster duel = new PaxosCluster(3, 2, 1, 1, 0.0, 1, 1_000);
        Predicate<Message> notAccept = m -> m.kind() != MessageKind.ACCEPT;
        duel.propose(0, "A"); duel.network.drain(notAccept);
        duel.propose(1, "B"); duel.network.drain(notAccept);
        for (int k = 0; k < 4; k++) {
            duel.network.drain(m -> m.kind() == MessageKind.ACCEPT && m.from() == 3); duel.network.drain(notAccept);
            duel.network.drain(m -> m.kind() == MessageKind.ACCEPT && m.from() == 4); duel.network.drain(notAccept);
        }
        check("duel: four exchanges, rounds climb, nothing chosen", duel.chosenValue() == null && duel.maxRound() >= 8);
        check("duel: no acceptor has accepted anything", duel.acceptors.stream().allMatch(ac -> ac.acceptedBallot.isNone()));
        duel.network.drain(m -> true);
        check("duel: released schedule chooses exactly one value", duel.chosenValue() != null && duel.chosenValue().equals(duel.firstChosen));

        // the invariant checker itself: a learner fed a majority for two different values must throw
        checkThrows("learner refuses to learn two values", IllegalStateException.class, () -> {
            Learner l = new Learner(9, 3);
            l.handle(Message.accepted(0, 9, new Ballot(1, 3), "X"), 0);
            l.handle(Message.accepted(1, 9, new Ballot(1, 3), "X"), 0);
            l.handle(Message.accepted(0, 9, new Ballot(2, 4), "Y"), 0);
            l.handle(Message.accepted(2, 9, new Ballot(2, 4), "Y"), 0);
        });

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
