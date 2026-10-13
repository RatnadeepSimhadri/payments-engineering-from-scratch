// Tests for phases/06-replication-and-consensus/06-raft-log-replication. Run: javac -d .out *.java && java -cp .out MainTest
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
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ": " + t.getMessage() + ")");
        }
    }

    static RaftNode standalone(int id, Storage storage) {
        return new RaftNode(id, List.of(0, 1, 2, 3, 4).stream().filter(p -> p != id).toList(), storage, new Rng(id + 1), Config.DEFAULT, 0, (t, s) -> {});
    }

    /** A follower with the given (term) sequence in its log, e.g. follower(1, 1, 2) has three entries with terms 1, 1, 2. */
    static RaftNode follower(long... terms) {
        Storage s = new Storage();
        for (int i = 0; i < terms.length; i++) s.append(new LogEntry(terms[i], "SET k" + (i + 1) + " old"));
        s.save(terms.length == 0 ? 0 : terms[terms.length - 1], -1);
        return standalone(1, s);
    }

    static RaftNode leaderOfTerm(long term, long... logTerms) {
        Storage s = new Storage();
        for (int i = 0; i < logTerms.length; i++) s.append(new LogEntry(logTerms[i], "SET k" + (i + 1) + " v"));
        s.save(term - 1, 0);
        RaftNode l = standalone(0, s);
        l.startElection(1);
        l.handle(Message.voteReply(1, 0, l.currentTerm(), true), 2);
        l.handle(Message.voteReply(2, 0, l.currentTerm(), true), 3);
        return l;
    }

    static boolean allLogsEqual(Cluster c) {
        String first = c.logOf(0);
        for (int i = 1; i < c.size; i++) if (!c.logOf(i).equals(first)) return false;
        return true;
    }

    public static void main(String[] args) {
        // --- happy path: three commands replicate, commit on a majority, and apply everywhere
        Cluster c = new Cluster(5, 7, 0.0, 5);
        c.runUntilStableLeader(2000);
        RaftNode leader = c.stableLeader();
        int i1 = c.propose("SET alice 100");
        int i2 = c.propose("SET bob 50");
        int i3 = c.propose("SET alice 75");
        check("indexes are consecutive after the election no-op", i1 == 2 && i2 == 3 && i3 == 4);
        check("entries carry the leader's term", leader.log().get(i3).term() == leader.currentTerm());
        check("all nodes apply within a few heartbeats", c.runUntilApplied(i3, 1000) > 0);
        check("all logs identical", allLogsEqual(c));
        check("all state machines identical", c.stateMachinesConverged());
        check("KV holds the last write", "75".equals(c.node(0).stateMachine.get("alice")) && "50".equals(c.node(3).stateMachine.get("bob")));
        check("commitIndex reached the last entry on every node", c.nodes.stream().allMatch(n -> n.commitIndex == i3 && n.lastApplied == i3));
        check("propose on a follower is refused", c.node((leader.id + 1) % 5).propose("SET x 1") == -1);

        // --- conflicting follower entries are overwritten; the leader's own log is never overwritten
        RaftNode old = leader;
        int minorityFollower = (old.id + 1) % 5;
        c.partition(Set.of(old.id, minorityFollower));
        int stale1 = c.proposeTo(old.id, "SET alice 999");
        int stale2 = c.proposeTo(old.id, "SET carol 1");
        c.run(600);
        check("stale leader accepted the writes into its log", stale1 == 5 && stale2 == 6 && old.lastLogIndex() == 6);
        check("stale leader could not commit them", old.commitIndex == 4);
        RaftNode fresh = null;
        for (RaftNode n : c.leaders()) if (n != old) fresh = n;
        check("majority side elected a new leader with a higher term", fresh != null && fresh.currentTerm() > old.currentTerm());
        check("new leader committed its no-op at the stale entries' index", fresh.commitIndex == 5 && fresh.log().get(5).command().equals("NOOP"));
        check("minority follower holds the divergent entries", c.node(minorityFollower).lastLogIndex() == 6);
        c.heal();
        c.runUntilStableLeader(2000);
        int dave = c.propose("SET dave 7");
        c.runUntilApplied(dave, 1000);
        check("after heal all logs identical", allLogsEqual(c));
        check("divergent entries were truncated and replaced", c.node(minorityFollower).log().get(5).command().equals("NOOP") && c.node(minorityFollower).log().get(6).command().equals("SET dave 7"));
        check("uncommitted write never reached any state machine", "75".equals(c.node(minorityFollower).stateMachine.get("alice")) && c.node(0).stateMachine.get("carol") == null);
        check("old leader stepped down and converged", old.role == Role.FOLLOWER && c.stateMachinesConverged());
        check("new leader's committed prefix unchanged by the merge", fresh.log().get(5).command().equals("NOOP") && fresh.role == Role.LEADER);

        // --- commit only on majority
        Cluster q = new Cluster(5, 11, 0.0, 5);
        q.runUntilStableLeader(2000);
        RaftNode lead = q.stableLeader();
        List<Integer> others = new ArrayList<>(lead.peers);
        q.crash(others.get(0)); q.crash(others.get(1)); q.crash(others.get(2));
        int idx = q.propose("SET balance 1000");
        q.run(400);
        check("with 2/5 replicas the entry is not committed", lead.commitIndex < idx && lead.replicatedCount(idx) == 2);
        check("leader has not applied the uncommitted entry", lead.stateMachine.get("balance") == null);
        q.restart(others.get(0));
        q.run(400);
        check("with 3/5 replicas the entry commits", lead.commitIndex >= idx && lead.replicatedCount(idx) >= 3);
        check("committed entry applied on the leader", "1000".equals(lead.stateMachine.get("balance")));
        check("restarted node rebuilt its state machine from the log", "1000".equals(q.node(others.get(0)).stateMachine.get("balance")));

        // --- restart keeps the log (persistent) and rebuilds the volatile state
        Cluster rs = new Cluster(3, 5, 0.0, 3);
        rs.runUntilStableLeader(2000);
        int w = rs.propose("SET k 1");
        rs.runUntilApplied(w, 500);
        int victim = (rs.stableLeader().id + 1) % 3;
        int lengthBefore = rs.node(victim).lastLogIndex();
        rs.crash(victim);
        RaftNode back = rs.restart(victim);
        check("log survives a restart", back.lastLogIndex() == lengthBefore && back.log().get(w).command().equals("SET k 1"));
        check("commitIndex and state machine are volatile", back.commitIndex == 0 && back.lastApplied == 0 && back.stateMachine.get("k") == null);
        rs.run(200);
        check("restarted follower re-applies from the leader's commit index", back.lastApplied >= w && "1".equals(back.stateMachine.get("k")));

        // --- AppendEntries receiver rules on a single follower
        RaftNode f = follower(1, 1, 2);
        Message r1 = f.handle(Message.appendEntries(0, 1, 2, 5, 2, List.of(), 0), 10).get(0);
        check("rule 2: prevLogIndex beyond the log is rejected", !r1.success() && f.consistencyRejections == 1);
        Message r2 = f.handle(Message.appendEntries(0, 1, 2, 3, 1, List.of(), 0), 11).get(0);
        check("rule 2: prevLogTerm mismatch is rejected", !r2.success() && f.lastLogIndex() == 3);
        Message r3 = f.handle(Message.appendEntries(0, 1, 2, 3, 2, List.of(), 0), 12).get(0);
        check("matching prevLogIndex/prevLogTerm accepted, matchIndex reported", r3.success() && r3.matchIndex() == 3);
        Message r4 = f.handle(Message.appendEntries(0, 1, 3, 1, 1, List.of(new LogEntry(3, "SET k2 new"), new LogEntry(3, "SET k3 new")), 0), 13).get(0);
        check("rule 3: conflicting entries truncated and replaced", r4.success() && f.lastLogIndex() == 3 && f.log().get(2).term() == 3 && f.log().get(3).command().equals("SET k3 new"));
        Message r5 = f.handle(Message.appendEntries(0, 1, 3, 1, 1, List.of(new LogEntry(3, "SET k2 new"), new LogEntry(3, "SET k3 new")), 0), 14).get(0);
        check("rule 4: a duplicate AppendEntries is idempotent", r5.success() && f.lastLogIndex() == 3);
        Message r6 = f.handle(Message.appendEntries(0, 1, 3, 3, 3, List.of(new LogEntry(3, "SET k4 new")), 10), 15).get(0);
        check("rule 5: commitIndex = min(leaderCommit, last new index)", r6.success() && f.commitIndex == 4 && f.lastApplied == 4);
        check("applied commands in log order", f.stateMachine.appliedCommands.equals(List.of("SET k1 old", "SET k2 new", "SET k3 new", "SET k4 new")));
        Message r7 = f.handle(Message.appendEntries(0, 1, 3, 2, 3, List.of(), 2), 16).get(0);
        check("heartbeat with an older prefix does not truncate newer matching entries", r7.success() && f.lastLogIndex() == 4 && f.commitIndex == 4);
        Message r8 = f.handle(Message.appendEntries(9, 1, 2, 4, 3, List.of(), 4), 17).get(0);
        check("rule 1: stale-term AppendEntries rejected without touching the log", !r8.success() && r8.term() == 3 && f.lastLogIndex() == 4);

        // --- leader side: nextIndex backs off on rejection and retries with an earlier prefix
        RaftNode l = leaderOfTerm(3, 1, 1, 2);
        check("leader initialises nextIndex to last index + 1 and appends a no-op", l.nextIndex.get(1) == 4 && l.lastLogIndex() == 4 && l.log().get(4).command().equals("NOOP"));
        List<Message> retry = l.handle(Message.appendReply(1, 0, 3, false, 0), 10);
        check("rejection decrements nextIndex and resends immediately", l.nextIndex.get(1) == 3 && retry.size() == 1 && retry.get(0).prevLogIndex() == 2 && retry.get(0).entries().size() == 2);
        l.handle(Message.appendReply(1, 0, 3, false, 0), 11);
        l.handle(Message.appendReply(1, 0, 3, false, 0), 12);
        List<Message> floor = l.handle(Message.appendReply(1, 0, 3, false, 0), 13);
        check("nextIndex never drops below 1", l.nextIndex.get(1) == 1 && floor.get(0).prevLogIndex() == 0 && floor.get(0).prevLogTerm() == 0);
        l.handle(Message.appendReply(1, 0, 3, true, 4), 14);
        check("success sets matchIndex and nextIndex", l.matchIndex.get(1) == 4 && l.nextIndex.get(1) == 5);
        check("one follower is not a majority of five", l.commitIndex == 0);
        l.handle(Message.appendReply(2, 0, 3, true, 4), 15);
        check("second follower makes a majority: commit through 4", l.commitIndex == 4 && l.lastApplied == 4);
        l.handle(Message.appendReply(3, 0, 3, true, 2), 16);
        check("an older matchIndex never moves commitIndex backwards", l.commitIndex == 4);
        l.handle(Message.appendReply(3, 0, 2, true, 4), 17);
        check("replies from an older term are ignored", l.matchIndex.get(3) == 2);
        Message stale = l.handle(Message.appendEntries(7, 0, 2, 0, 0, List.of(new LogEntry(2, "SET hijack 1")), 0), 18).get(0);
        check("leader rejects a stale leader's entries and keeps its log", !stale.success() && l.lastLogIndex() == 4 && l.role == Role.LEADER);

        // --- section 5.4.2: entries from earlier terms are not committed by counting replicas
        RaftNode f8 = leaderOfTerm(5, 2, 2);
        f8.handle(Message.appendReply(1, 0, 5, true, 2), 4);
        f8.handle(Message.appendReply(2, 0, 5, true, 2), 5);
        check("term-2 entries on 3/5 replicas are not committed in term 5", f8.commitIndex == 0);
        f8.handle(Message.appendReply(1, 0, 5, true, 3), 6);
        f8.handle(Message.appendReply(2, 0, 5, true, 3), 7);
        check("the current-term no-op commits itself and everything before it", f8.commitIndex == 3 && f8.stateMachine.appliedCommands.size() == 3);

        // --- election log check uses the real log
        RaftNode voter = follower(1, 2);
        check("candidate with a shorter log of the same last term is refused", !voter.handle(Message.requestVote(3, 1, 3, 1, 2), 1).get(0).success());
        check("candidate with a higher last term is granted", voter.handle(Message.requestVote(3, 1, 3, 1, 3), 2).get(0).success());

        // --- Log Matching and state-machine convergence over many lossy seeds with crashes (invariants checked per delivery)
        int converged = 0;
        for (long seed = 1; seed <= 25; seed++) {
            Cluster r = new Cluster(5, seed, 0.2, 10);
            r.runUntilStableLeader(3000);
            for (int k = 1; k <= 6; k++) { Main.proposeUntilAccepted(r, "SET k" + k + " v" + k); r.run(40); }
            RaftNode victimNode = r.stableLeader() != null ? r.stableLeader() : r.node(0);
            r.crash(victimNode.id);
            r.runUntilStableLeader(3000);
            for (int k = 7; k <= 12; k++) { Main.proposeUntilAccepted(r, "SET k" + k + " v" + k); r.run(40); }
            r.restart(victimNode.id);
            r.runUntilStableLeader(3000);
            RaftNode fin = r.stableLeader();
            if (fin != null) r.runUntilApplied(fin.lastLogIndex(), 3000);
            if (r.stateMachinesConverged() && fin != null && "v12".equals(fin.stateMachine.get("k12")) && "v1".equals(fin.stateMachine.get("k1"))) converged++;
        }
        check("25 lossy seeds with a leader crash all converge on the same 12 writes", converged == 25);
        Cluster d1 = new Cluster(5, 42, 0.15, 8), d2 = new Cluster(5, 42, 0.15, 8);
        d1.runUntilStableLeader(3000); d2.runUntilStableLeader(3000);
        d1.propose("SET a 1"); d2.propose("SET a 1");
        d1.run(500); d2.run(500);
        check("same seed gives the same trace with replication", d1.events.equals(d2.events) && d1.events.size() > 3);

        // --- the checkers themselves fire
        checkThrows("Log Matching checker detects a violation", IllegalStateException.class, () -> Cluster.checkLogMatching(
                List.of(new LogEntry(0, "<sentinel>"), new LogEntry(1, "SET a 1"), new LogEntry(2, "SET b 2")),
                List.of(new LogEntry(0, "<sentinel>"), new LogEntry(1, "SET a 9"), new LogEntry(2, "SET b 2")), 0, 1));
        Cluster.checkLogMatching(
                List.of(new LogEntry(0, "<sentinel>"), new LogEntry(1, "SET a 1"), new LogEntry(2, "SET b 2")),
                List.of(new LogEntry(0, "<sentinel>"), new LogEntry(1, "SET a 1"), new LogEntry(3, "SET c 3")), 0, 1);
        check("Log Matching allows divergence after the last common term", true);
        checkThrows("two leaders in one term detected", IllegalStateException.class, () -> leaderOfTerm(2).handle(Message.appendEntries(4, 0, 2, 0, 0, List.of(), 0), 9));

        // --- KV state machine
        KVStore kv = new KVStore();
        kv.apply("SET a 1"); kv.apply("SET b 2"); kv.apply("DEL a"); kv.apply("NOOP");
        check("KV applies SET, DEL, NOOP", kv.get("a") == null && "2".equals(kv.get("b")) && kv.appliedCommands.size() == 4);
        checkThrows("KV rejects unknown commands", IllegalArgumentException.class, () -> kv.apply("INCR a"));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(msg -> System.out.println("FAIL: " + msg));
        if (!failures.isEmpty()) System.exit(1);
    }
}
