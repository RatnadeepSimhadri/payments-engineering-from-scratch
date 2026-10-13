// Tests for phases/06-replication-and-consensus/05-raft-leader-election. Run: javac -d .out *.java && java -cp .out MainTest
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

    static RaftNode standalone(int id, Storage storage, long seed) {
        return new RaftNode(id, List.of(0, 1, 2, 3, 4).stream().filter(p -> p != id).toList(), storage, new Rng(seed), Config.DEFAULT, 0, (t, s) -> {});
    }

    public static void main(String[] args) {
        // --- a single leader is elected on a quiet network
        Cluster c = new Cluster(5, 7, 0.0, 5);
        int at = c.runUntilStableLeader(2000);
        check("stable leader elected", at > 0 && c.stableLeader() != null);
        check("elected after at least one minimum timeout", at >= Config.DEFAULT.electionTimeoutMin());
        check("exactly one leader", c.leaders().size() == 1);
        check("every node agrees on the leader's term", c.nodes.stream().allMatch(n -> n.currentTerm() == c.stableLeader().currentTerm()));
        check("followers know who leads", c.nodes.stream().filter(n -> n.role != Role.LEADER).allMatch(n -> n.leaderId == c.stableLeader().id));

        // --- heartbeats suppress new elections
        long term = c.maxTerm();
        c.run(2000);
        check("heartbeats keep the term stable for 2000 ticks", c.maxTerm() == term && c.leaders().size() == 1);

        // --- determinism: same seed, same trace
        Cluster c1 = new Cluster(5, 42, 0.1, 8), c2 = new Cluster(5, 42, 0.1, 8);
        c1.run(1500); c2.run(1500);
        check("same seed gives identical event trace", c1.events.equals(c2.events) && !c1.events.isEmpty());
        Cluster c3 = new Cluster(5, 43, 0.1, 8);
        c3.run(1500);
        check("different seed gives a different trace", !c1.events.equals(c3.events));

        // --- Election Safety over many seeds with drops and delays (the invariant is checked after every delivery)
        int electedSeeds = 0;
        for (long seed = 1; seed <= 40; seed++) {
            Cluster s = new Cluster(5, seed, 0.2, 10);
            if (s.runUntilStableLeader(2500) > 0) electedSeeds++;
            s.run(500);
            for (var e : s.leadersByTerm.entrySet()) check("one leader for term " + e.getKey() + " seed " + seed, e.getValue() != null);
        }
        check("40 lossy seeds all elect a leader within 2500 ticks", electedSeeds == 40);
        int threeNodeSeeds = 0;
        for (long seed = 100; seed < 120; seed++) {
            Cluster s = new Cluster(3, seed, 0.3, 10);
            if (s.runUntilStableLeader(4000) > 0) threeNodeSeeds++;
        }
        check("3-node clusters with 30% drops still elect", threeNodeSeeds == 20);

        // --- partition: majority elects a higher term; after heal the higher term wins and the old leader steps down
        Cluster p = new Cluster(5, 7, 0.0, 5);
        p.runUntilStableLeader(2000);
        RaftNode old = p.stableLeader();
        long oldTerm = old.currentTerm();
        p.partition(Set.of(old.id, (old.id + 1) % 5));
        p.run(800);
        check("stale leader keeps leading its minority side", old.role == Role.LEADER && old.currentTerm() == oldTerm);
        check("majority side elected a leader with a higher term", p.leaders().size() == 2 && p.maxTerm() > oldTerm);
        p.heal();
        int healed = p.runUntilStableLeader(2000);
        check("cluster converges after heal", healed > 0);
        check("old leader stepped down", old.role == Role.FOLLOWER);
        check("the higher term won", p.stableLeader().currentTerm() > oldTerm && p.stableLeader() != old);

        // --- a minority partition can never elect
        Cluster m = new Cluster(5, 11, 0.0, 5);
        m.partition(Set.of(0, 1));
        m.run(3000);
        check("minority side never elects a leader", m.leaders().stream().noneMatch(l -> l.id == 0 || l.id == 1));
        check("minority side keeps trying with growing terms", Math.max(m.node(0).currentTerm(), m.node(1).currentTerm()) > 3);
        check("majority side has a leader", m.leaders().size() == 1);

        // --- RequestVote receiver rules, Figure 2
        RaftNode n = standalone(1, new Storage(), 1);
        n.storage.save(5, -1);
        Message stale = n.handle(Message.requestVote(0, 1, 3, 0, 0), 10).get(0);
        check("stale term vote rejected (rule 1)", !stale.granted() && stale.term() == 5 && stale.kind() == MessageKind.REQUEST_VOTE_REPLY);
        check("rejecting a stale request does not change term", n.currentTerm() == 5 && n.votedFor() == -1);
        Message ok = n.handle(Message.requestVote(0, 1, 5, 0, 0), 11).get(0);
        check("first vote in a term granted", ok.granted() && n.votedFor() == 0);
        Message second = n.handle(Message.requestVote(2, 1, 5, 0, 0), 12).get(0);
        check("second candidate in the same term rejected", !second.granted() && n.votedFor() == 0);
        Message again = n.handle(Message.requestVote(0, 1, 5, 0, 0), 13).get(0);
        check("retransmitted request from the same candidate granted again", again.granted());
        Message higher = n.handle(Message.requestVote(3, 1, 6, 0, 0), 14).get(0);
        check("higher term resets votedFor and grants", higher.granted() && n.currentTerm() == 6 && n.votedFor() == 3);

        // --- log up-to-date check (section 5.4.1) using the stand-in lastLogIndex/lastLogTerm
        RaftNode u = standalone(2, new Storage(), 2);
        u.lastLogIndex = 10; u.lastLogTerm = 3;
        check("candidate with older last term rejected", !u.handle(Message.requestVote(0, 2, 4, 20, 2), 1).get(0).granted());
        check("candidate with same term but shorter log rejected", !u.handle(Message.requestVote(0, 2, 4, 9, 3), 2).get(0).granted());
        check("candidate with same term and same length granted", u.handle(Message.requestVote(0, 2, 4, 10, 3), 3).get(0).granted());
        RaftNode u2 = standalone(2, new Storage(), 2);
        u2.lastLogIndex = 10; u2.lastLogTerm = 3;
        check("candidate with higher last term and shorter log granted", u2.handle(Message.requestVote(1, 2, 4, 2, 4), 3).get(0).granted());

        // --- candidate steps down on a higher term, and on hearing a leader of its own term
        RaftNode cand = standalone(0, new Storage(), 3);
        List<Message> reqs = cand.startElection(100);
        check("candidate sends RequestVote to every peer", reqs.size() == 4 && reqs.stream().allMatch(r -> r.kind() == MessageKind.REQUEST_VOTE && r.term() == 1));
        check("candidate voted for itself and persisted it", cand.role == Role.CANDIDATE && cand.votedFor() == 0 && cand.storage.writes >= 1);
        cand.handle(Message.voteReply(1, 0, 1, true), 101);
        check("two votes of five is not a majority", cand.role == Role.CANDIDATE && cand.votes() == 2);
        cand.handle(Message.requestVote(3, 0, 2, 0, 0), 102);
        check("candidate steps down on higher term", cand.role == Role.FOLLOWER && cand.currentTerm() == 2);
        check("stepped-down candidate granted the vote to the higher-term candidate", cand.votedFor() == 3);
        RaftNode cand2 = standalone(0, new Storage(), 4);
        cand2.startElection(100);
        Message hb = cand2.handle(Message.heartbeat(4, 0, 1), 101).get(0);
        check("candidate hearing a leader of its term becomes follower", cand2.role == Role.FOLLOWER && cand2.leaderId == 4 && hb.granted());

        // --- stale vote replies are ignored; a majority of votes makes a leader that heartbeats
        RaftNode w = standalone(0, new Storage(), 5);
        w.startElection(50);
        w.startElection(300);   // second election: term 2
        w.handle(Message.voteReply(1, 0, 1, true), 301);
        check("vote reply from an earlier term ignored", w.votes() == 1 && w.role == Role.CANDIDATE);
        w.handle(Message.voteReply(1, 0, 2, true), 302);
        List<Message> hbs = w.handle(Message.voteReply(2, 0, 2, true), 303);
        check("third vote makes a leader", w.role == Role.LEADER && w.leaderId == 0);
        check("new leader immediately heartbeats every peer", hbs.size() == 4 && hbs.stream().allMatch(h -> h.kind() == MessageKind.APPEND_ENTRIES));
        check("leader ticks produce heartbeats only when due", w.tick(304).isEmpty() && w.tick(303 + Config.DEFAULT.heartbeatInterval()).size() == 4);
        Message rej = w.handle(Message.heartbeat(3, 0, 1), 310).get(0);
        check("leader rejects a stale-term heartbeat", !rej.granted() && w.role == Role.LEADER);
        w.handle(Message.heartbeat(3, 0, 3), 311);
        check("leader steps down on a higher-term heartbeat", w.role == Role.FOLLOWER && w.leaderId == 3 && w.currentTerm() == 3);

        // --- votedFor persistence across a crash prevents a double vote
        Storage durable = new Storage();
        RaftNode a = standalone(1, durable, 6);
        check("vote granted before crash", a.handle(Message.requestVote(0, 1, 1, 0, 0), 10).get(0).granted());
        RaftNode b = standalone(1, durable, 7);   // restart: fresh volatile state, same Storage
        check("restart restores currentTerm and votedFor", b.currentTerm() == 1 && b.votedFor() == 0 && b.role == Role.FOLLOWER);
        check("restarted node refuses a second candidate in the same term", !b.handle(Message.requestVote(2, 1, 1, 0, 0), 12).get(0).granted());
        RaftNode lost = standalone(1, new Storage(), 8);
        check("without persistence the double vote would be granted", lost.handle(Message.requestVote(2, 1, 1, 0, 0), 12).get(0).granted());

        // --- crash and restart inside a cluster: the cluster re-elects and Election Safety still holds
        Cluster r = new Cluster(5, 21, 0.05, 5);
        r.runUntilStableLeader(2000);
        RaftNode lead = r.stableLeader();
        r.crash(lead.id);
        int reelected = r.runUntilStableLeader(2000);
        check("cluster re-elects after the leader crashes", reelected > 0 && r.stableLeader().id != lead.id);
        RaftNode back = r.restart(lead.id);
        check("restarted node remembers its term from Storage", back.currentTerm() >= lead.currentTerm());
        r.runUntilStableLeader(2000);
        check("restarted old leader rejoins as follower", back.role == Role.FOLLOWER && back.currentTerm() == r.stableLeader().currentTerm());

        // --- the Election Safety checker itself fires on two leaders in one term
        checkThrows("two leaders in one term is detected", IllegalStateException.class, () -> {
            RaftNode l1 = standalone(0, new Storage(), 9);
            l1.startElection(1);
            l1.handle(Message.voteReply(1, 0, 1, true), 2);
            l1.handle(Message.voteReply(2, 0, 1, true), 3);
            l1.handle(Message.heartbeat(4, 0, 1), 4);   // a second leader for term 1 announces itself
        });

        // --- timing config validation and the rng contract
        checkThrows("heartbeat must be shorter than election timeout", IllegalArgumentException.class, () -> new Config(100, 200, 150));
        checkThrows("max timeout below min rejected", IllegalArgumentException.class, () -> new Config(300, 150, 50));
        Rng rng = new Rng(12345);
        check("SplitMix64 first output matches the reference sequence", rng.nextLong() == 2454886589211414944L);
        boolean inRange = true;
        Rng t = new Rng(1);
        for (int i = 0; i < 1000; i++) { int v = t.nextInt(151); if (v < 0 || v >= 151) inRange = false; }
        check("nextInt stays within bound", inRange);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
