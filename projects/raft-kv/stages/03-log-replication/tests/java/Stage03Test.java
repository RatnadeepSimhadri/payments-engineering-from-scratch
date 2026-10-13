// Stage 03 tests for projects/raft-kv: log replication (AppendEntries consistency check, nextIndex/matchIndex,
// follower truncation, Log Matching). Run by the grader after Stage01Test and Stage02Test.
import java.util.ArrayList;
import java.util.List;
import java.util.Set;

public final class Stage03Test {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    static void scenario(String name, Runnable body) {
        try { body.run(); } catch (Throwable t) { failures.add(name + " blew up: " + t.getClass().getSimpleName() + ": " + t.getMessage()); }
    }

    static List<Integer> peersOf(int id, int size) {
        List<Integer> ps = new ArrayList<>();
        for (int j = 0; j < size; j++) if (j != id) ps.add(j);
        return ps;
    }

    static RaftNode standalone(int id, Storage storage, long seed) {
        return new RaftNode(id, peersOf(id, 5), storage, new Rng(seed), Config.DEFAULT, 0, Tracer.NONE);
    }

    static Storage logged(String... entries) {   // "t1:PUT a 1"
        Storage s = new Storage();
        for (String e : entries) {
            int colon = e.indexOf(':');
            s.append(new LogEntry(Long.parseLong(e.substring(1, colon)), e.substring(colon + 1)));
        }
        return s;
    }

    static RaftNode electedLeader(Storage storage, long seed) {
        RaftNode n = standalone(0, storage, seed);
        n.startElection(1);
        n.handle(Message.voteReply(1, 0, n.currentTerm(), true), 2);
        n.handle(Message.voteReply(2, 0, n.currentTerm(), true), 3);
        return n;
    }

    static boolean logMatchingHolds(Cluster c) {
        try {
            for (int i = 0; i < c.size; i++) for (int j = i + 1; j < c.size; j++) Cluster.checkLogMatching(c.node(i).log(), c.node(j).log(), i, j);
            return true;
        } catch (IllegalStateException e) {
            return false;
        }
    }

    static boolean everyLogContains(Cluster c, String command) {
        for (RaftNode n : c.nodes) if (n.log().stream().noneMatch(e -> e.command().equals(command))) return false;
        return true;
    }

    static boolean noLogContains(Cluster c, String command) {
        for (RaftNode n : c.nodes) if (n.log().stream().anyMatch(e -> e.command().equals(command))) return false;
        return true;
    }

    public static void main(String[] args) {
        scenario("propose appends to the leader's log only", () -> {
            Storage fs = new Storage();
            RaftNode follower = standalone(1, fs, 1);
            check("a follower refuses proposals", follower.propose("PUT a 1") == -1 && fs.lastIndex() == 0);
            Storage ls = new Storage();
            RaftNode leader = electedLeader(ls, 2);
            int base = leader.lastLogIndex();
            int i1 = leader.propose("PUT a 1");
            int i2 = leader.propose("PUT b 2");
            check("propose returns consecutive indexes", i1 == base + 1 && i2 == base + 2 && leader.lastLogIndex() == i2);
            check("entries carry the leader's current term and the command", ls.termAt(i2) == 1 && ls.log.get(i2).command().equals("PUT b 2"));
            check("propose persists (stable writes increased)", ls.writes >= 3);
        });

        scenario("a new leader initialises nextIndex/matchIndex and ships from nextIndex", () -> {
            Storage ls = logged("t1:PUT a 1", "t1:PUT b 2");
            ls.save(1, -1);
            RaftNode leader = standalone(0, ls, 3);
            leader.startElection(5);
            leader.handle(Message.voteReply(1, 0, 2, true), 6);
            List<Message> first = leader.handle(Message.voteReply(2, 0, 2, true), 7);
            check("nextIndex starts at (last index at election) + 1 for every peer", leader.peers.stream().allMatch(p -> leader.nextIndex.get(p) == 3));
            check("matchIndex starts at 0 for every peer", leader.peers.stream().allMatch(p -> leader.matchIndex.get(p) == 0));
            check("the first AppendEntries names the entry before nextIndex as (prevLogIndex, prevLogTerm)", first.stream().allMatch(m -> m.prevLogIndex() == 2 && m.prevLogTerm() == 1));
            check("the first AppendEntries ships exactly log[nextIndex..last]", first.stream().allMatch(m -> m.entries().size() == leader.lastLogIndex() - 2));
            leader.propose("PUT c 3");
            Message ae = leader.appendEntriesFor(1);
            check("appendEntriesFor ships everything from nextIndex, including the new proposal", ae.entries().get(ae.entries().size() - 1).command().equals("PUT c 3") && ae.prevLogIndex() == 2 && ae.term() == 2);
            leader.nextIndex.put(1, 1);
            Message back = leader.appendEntriesFor(1);
            check("with nextIndex 1 the prev is the sentinel (0, t0) and the whole log ships", back.prevLogIndex() == 0 && back.prevLogTerm() == 0 && back.entries().size() == leader.lastLogIndex());
        });

        scenario("AppendEntries consistency check (receiver rule 2)", () -> {
            Storage fs = logged("t1:PUT a 1", "t1:PUT b 2", "t2:PUT c 3");
            RaftNode f = standalone(1, fs, 4);
            Message r1 = f.handle(Message.appendEntries(0, 1, 3, 5, 2, List.of(new LogEntry(3, "PUT d 4")), 0), 10).get(0);
            check("prevLogIndex beyond the follower's log is refused", !r1.success() && f.consistencyRejections == 1);
            check("a refused AppendEntries still makes the sender the leader and adopts its term", f.leaderId == 0 && f.currentTerm() == 3);
            Message r2 = f.handle(Message.appendEntries(0, 1, 3, 3, 1, List.of(new LogEntry(3, "PUT d 4")), 0), 11).get(0);
            check("prevLogIndex present but with a different term is refused", !r2.success() && f.consistencyRejections == 2);
            check("a refusal never changes the follower's log", fs.lastIndex() == 3 && fs.termAt(3) == 2);
            Message r3 = f.handle(Message.appendEntries(0, 1, 3, 3, 2, List.of(new LogEntry(3, "PUT d 4")), 0), 12).get(0);
            check("a matching (prevLogIndex, prevLogTerm) is accepted", r3.success() && fs.lastIndex() == 4 && fs.termAt(4) == 3);
            check("a successful reply reports the index of the last entry now known to match", r3.matchIndex() == 4);
            Message hb = f.handle(Message.appendEntries(0, 1, 3, 4, 3, List.of(), 0), 13).get(0);
            check("a heartbeat with a matching prev succeeds with matchIndex = prevLogIndex and leaves the log alone", hb.success() && hb.matchIndex() == 4 && fs.lastIndex() == 4);
        });

        scenario("conflicting entries are truncated, duplicates are skipped (receiver rules 3 and 4)", () -> {
            Storage fs = logged("t1:PUT a 1", "t1:PUT b 2", "t2:PUT c 3");
            RaftNode f = standalone(2, fs, 5);
            Message r = f.handle(Message.appendEntries(0, 2, 3, 1, 1, List.of(new LogEntry(3, "PUT x 9")), 0), 10).get(0);
            check("a conflicting entry and everything after it are deleted, then the leader's entry is appended", r.success() && fs.lastIndex() == 2 && fs.describe().equals("[t1:PUT a 1, t3:PUT x 9]"));
            int writes = fs.writes;
            Message dup = f.handle(Message.appendEntries(0, 2, 3, 1, 1, List.of(new LogEntry(3, "PUT x 9")), 0), 11).get(0);
            check("re-delivering the same entries changes nothing (idempotent)", dup.success() && dup.matchIndex() == 2 && fs.lastIndex() == 2 && fs.writes == writes);
            Message mixed = f.handle(Message.appendEntries(0, 2, 3, 1, 1, List.of(new LogEntry(3, "PUT x 9"), new LogEntry(3, "PUT y 8")), 0), 12).get(0);
            check("a batch that overlaps the log appends only the new tail", mixed.success() && mixed.matchIndex() == 3 && fs.describe().equals("[t1:PUT a 1, t3:PUT x 9, t3:PUT y 8]"));
            Message older = f.handle(Message.appendEntries(0, 2, 3, 0, 0, List.of(new LogEntry(1, "PUT a 1")), 0), 13).get(0);
            check("an old, shorter batch that matches a prefix does not truncate the newer tail", older.success() && older.matchIndex() == 1 && fs.lastIndex() == 3);
        });

        scenario("the leader walks nextIndex back on refusal and forward on success", () -> {
            Storage ls = logged("t1:PUT a 1", "t1:PUT b 2", "t1:PUT c 3");
            ls.save(1, -1);
            RaftNode l = electedLeader(ls, 6);
            long term = l.currentTerm();
            int start = l.nextIndex.get(1);
            List<Message> retry = l.handle(Message.appendReply(1, 0, term, false, 0), 10);
            check("a refusal decrements nextIndex by one", l.nextIndex.get(1) == start - 1);
            check("and immediately resends with the earlier prev", retry.size() == 1 && retry.get(0).kind() == MessageKind.APPEND_ENTRIES && retry.get(0).to() == 1 && retry.get(0).prevLogIndex() == start - 2);
            for (int i = 0; i < 20; i++) l.handle(Message.appendReply(1, 0, term, false, 0), 11 + i);
            check("nextIndex never drops below 1", l.nextIndex.get(1) == 1);
            Message full = l.appendEntriesFor(1);
            check("at nextIndex 1 the whole log is shipped after the sentinel", full.prevLogIndex() == 0 && full.entries().size() == l.lastLogIndex());
            l.handle(Message.appendReply(1, 0, term, true, 3), 40);
            check("a success sets matchIndex and nextIndex = matchIndex + 1", l.matchIndex.get(1) == 3 && l.nextIndex.get(1) == 4);
            l.handle(Message.appendReply(1, 0, term, true, 2), 41);
            check("an older success (lower matchIndex) never moves matchIndex backwards", l.matchIndex.get(1) == 3 && l.nextIndex.get(1) == 4);
            check("replicatedCount counts the leader plus peers at or beyond the index", l.replicatedCount(3) == 2 && l.replicatedCount(4) == 1);
            l.handle(Message.appendReply(2, 0, term - 1, true, 3), 42);
            check("a reply from an older term is ignored", l.matchIndex.get(2) == 0);
            RaftNode f = standalone(3, new Storage(), 7);
            check("a non-leader ignores AppendEntries replies", f.handle(Message.appendReply(1, 3, 0, true, 5), 1).isEmpty());
        });

        scenario("three commands replicate to every log on a quiet 5-node cluster", () -> {
            Cluster c = new Cluster(5, 7, 0.0, 5);
            c.runUntilStableLeader(2000);
            int i1 = c.propose("PUT alice 100");
            int i2 = c.propose("PUT bob 50");
            int i3 = c.propose("PUT alice 75");
            check("proposals got consecutive indexes", i1 > 0 && i2 == i1 + 1 && i3 == i2 + 1);
            int at = c.runUntilLogsConverged(1000);
            check("all five logs converge", at > 0 && c.logsConverged());
            check("every log holds the three commands at the leader's indexes", c.nodes.stream().allMatch(n -> n.log().get(i1).command().equals("PUT alice 100") && n.log().get(i3).command().equals("PUT alice 75")));
            check("the entries carry the leader's term", c.nodes.stream().allMatch(n -> n.log().get(i2).term() == c.stableLeader().currentTerm()));
            c.run(20);   // let the last replies reach the leader
            check("the leader knows every follower matches through the last index", c.stableLeader().replicatedCount(i3) == 5);
            check("Log Matching holds", logMatchingHolds(c));
        });

        scenario("a lagging follower catches up through nextIndex back-off", () -> {
            Cluster c = new Cluster(5, 9, 0.0, 5);
            c.runUntilStableLeader(2000);
            RaftNode leader = c.stableLeader();
            int lag = (leader.id + 2) % 5;
            c.crash(lag);
            for (int k = 1; k <= 4; k++) c.propose("PUT k" + k + " v" + k);
            c.run(300);
            check("the crashed follower has none of the new entries", c.node(lag).log().stream().noneMatch(e -> e.command().startsWith("PUT k")));
            c.restart(lag);
            int at = c.runUntilLogsConverged(1500);
            check("after restart its log converges with the leader's", at > 0 && c.node(lag).log().equals(leader.log()));
            check("Log Matching holds after catch-up", logMatchingHolds(c));
        });

        scenario("a stale leader's unreplicated entries are overwritten after a partition heals", () -> {
            Cluster c = new Cluster(5, 7, 0.0, 5);
            c.runUntilStableLeader(2000);
            c.propose("PUT alice 100");
            c.runUntilLogsConverged(1000);
            RaftNode old = c.stableLeader();
            int follower = (old.id + 1) % 5;
            c.partition(Set.of(old.id, follower));
            int staleIdx = c.proposeTo(old.id, "PUT alice 999");
            c.proposeTo(old.id, "PUT carol 1");
            c.run(600);
            check("the stale leader accepted writes it can never replicate to a majority", staleIdx > 0 && old.log().get(staleIdx).command().equals("PUT alice 999"));
            RaftNode fresh = null;
            for (RaftNode l : c.leaders()) if (fresh == null || l.currentTerm() > fresh.currentTerm()) fresh = l;
            check("the majority elected a new leader in a higher term", fresh != null && fresh != old && fresh.currentTerm() > old.currentTerm());
            c.proposeTo(fresh.id, "PUT dave 7");
            c.run(200);
            check("the two sides diverge at the stale index", !old.log().equals(fresh.log()));
            c.heal();
            c.runUntilStableLeader(2000);
            int at = c.runUntilLogsConverged(2000);
            check("after heal every log converges", at > 0);
            check("the stale writes are gone from every log", noLogContains(c, "PUT alice 999") && noLogContains(c, "PUT carol 1"));
            check("the majority's write survives everywhere", everyLogContains(c, "PUT dave 7") && everyLogContains(c, "PUT alice 100"));
            check("the old leader is now a follower whose log was truncated", old.role == Role.FOLLOWER && old.log().equals(c.stableLeader().log()));
            check("Log Matching holds after the heal", logMatchingHolds(c));
        });

        scenario("Log Matching and convergence hold across lossy seeds with a leader crash", () -> {
            int converged = 0;
            for (long seed = 1; seed <= 12; seed++) {
                Cluster r = new Cluster(5, seed, 0.2, 10);
                r.runUntilStableLeader(3000);
                for (int k = 1; k <= 4; k++) {
                    for (int attempt = 0; attempt < 300 && r.propose("PUT k" + k + " v" + k) < 0; attempt++) r.run(10);
                    r.run(30);
                }
                RaftNode victim = r.stableLeader() != null ? r.stableLeader() : r.node(0);
                r.crash(victim.id);
                r.runUntilStableLeader(3000);
                for (int k = 5; k <= 8; k++) {
                    for (int attempt = 0; attempt < 300 && r.propose("PUT k" + k + " v" + k) < 0; attempt++) r.run(10);
                    r.run(30);
                }
                r.restart(victim.id);
                r.runUntilStableLeader(3000);
                if (r.runUntilLogsConverged(3000) > 0 && logMatchingHolds(r) && everyLogContains(r, "PUT k8 v8")) converged++;
            }
            check("12 lossy seeds: every log converged on the surviving writes with Log Matching intact", converged == 12);
        });

        System.out.println("Stage03Test: " + passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL " + f);
        System.exit(failures.isEmpty() ? 0 : 1);
    }
}
