// Stage 04 tests for projects/raft-kv: commitIndex by majority with the current-term rule, the KV state machine,
// client PUT/GET through the leader, and a linearizability check on the recorded history.
// Run by the grader after Stage01Test, Stage02Test, and Stage03Test.
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeMap;

public final class Stage04Test {
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

    static RaftNode electedLeader(Storage storage, long seed) {
        RaftNode n = standalone(0, storage, seed);
        n.startElection(1);
        n.handle(Message.voteReply(1, 0, n.currentTerm(), true), 2);
        n.handle(Message.voteReply(2, 0, n.currentTerm(), true), 3);
        return n;
    }

    static boolean appliedPrefixMatchesLog(RaftNode n) {
        if (n.lastApplied > n.commitIndex || n.commitIndex > n.lastLogIndex()) return false;
        if (n.stateMachine.appliedCommands.size() != n.lastApplied) return false;
        for (int k = 1; k <= n.lastApplied; k++) if (!n.stateMachine.appliedCommands.get(k - 1).equals(n.log().get(k).command())) return false;
        return true;
    }

    static boolean respectsRealTime(History h, List<Integer> order) {
        for (int a = 0; a < order.size(); a++) {
            for (int b = a + 1; b < order.size(); b++) {
                if (h.op(order.get(b)).precedes(h.op(order.get(a)))) return false;
            }
        }
        return order.size() == h.size();
    }

    public static void main(String[] args) {
        scenario("KVStore applies PUT, GET, NOOP and rejects garbage", () -> {
            KVStore kv = new KVStore();
            check("PUT returns OK and stores", kv.apply("PUT balance:alice 100").equals("OK") && "100".equals(kv.get("balance:alice")));
            check("GET returns the stored value", kv.apply("GET balance:alice").equals("100"));
            check("GET of an absent key returns the empty string", kv.apply("GET nobody").isEmpty() && kv.get("nobody") == null);
            check("NOOP returns the empty string and changes nothing", kv.apply("NOOP").isEmpty() && kv.data.size() == 1);
            check("a PUT value may contain spaces", kv.apply("PUT note hello world").equals("OK") && "hello world".equals(kv.get("note")));
            check("PUT overwrites", kv.apply("PUT balance:alice 75").equals("OK") && "75".equals(kv.get("balance:alice")));
            check("applied commands and results are recorded in order", kv.applied() == 6 && kv.appliedCommands.get(1).equals("GET balance:alice") && kv.results.get(1).equals("100") && kv.results.get(2).isEmpty());
            checkThrows("PUT without a value is rejected", IllegalArgumentException.class, () -> kv.apply("PUT lonely"));
            checkThrows("GET with extra arguments is rejected", IllegalArgumentException.class, () -> kv.apply("GET a b"));
            checkThrows("unknown commands are rejected", IllegalArgumentException.class, () -> kv.apply("TRANSFER a b 5"));
            check("a rejected command is not recorded", kv.applied() == 6);
        });

        scenario("a leader commits an entry once a majority holds it, then applies in order", () -> {
            Storage ls = new Storage();
            RaftNode l = electedLeader(ls, 1);
            long term = l.currentTerm();
            int i1 = l.propose("PUT a 1");
            int i2 = l.propose("PUT a 2");
            l.handle(Message.appendReply(1, 0, term, true, i2), 10);
            check("two of five replicas (leader + one) is not a majority: nothing commits", l.commitIndex == 0 && l.lastApplied == 0 && l.stateMachine.applied() == 0);
            l.handle(Message.appendReply(2, 0, term, true, i1), 11);
            check("three of five hold index i1: commitIndex advances to i1, not i2", l.commitIndex == i1 && l.lastApplied == i1);
            check("the state machine applied everything through i1 in log order", appliedPrefixMatchesLog(l) && "1".equals(l.stateMachine.get("a")));
            l.handle(Message.appendReply(3, 0, term, true, i2), 12);
            check("a third holder of i2 commits it", l.commitIndex == i2 && "2".equals(l.stateMachine.get("a")) && appliedPrefixMatchesLog(l));
            l.handle(Message.appendReply(4, 0, term, true, i1), 13);
            check("a later, lower matchIndex never moves commitIndex backwards", l.commitIndex == i2);
            check("lastApplied never exceeds commitIndex", l.lastApplied <= l.commitIndex);
        });

        scenario("only current-term entries are committed by counting replicas (section 5.4.2)", () -> {
            Storage s = new Storage();
            s.append(new LogEntry(2, "PUT x 1"));
            s.append(new LogEntry(2, "PUT x 2"));
            s.save(4, 0);
            RaftNode l = standalone(0, s, 2);
            l.startElection(1);
            l.handle(Message.voteReply(1, 0, l.currentTerm(), true), 2);
            l.handle(Message.voteReply(2, 0, l.currentTerm(), true), 3);
            check("precondition: leader of term 5 with two term-2 entries", l.role == Role.LEADER && l.currentTerm() == 5 && s.termAt(2) == 2);
            l.handle(Message.appendReply(1, 0, 5, true, 2), 4);
            l.handle(Message.appendReply(2, 0, 5, true, 2), 5);
            check("term-2 entries on 3/5 replicas do NOT commit under a term-5 leader", l.commitIndex == 0 && l.stateMachine.applied() == 0);
            int cur = l.propose("PUT x 3");
            check("the new entry carries the current term", s.termAt(cur) == 5);
            l.handle(Message.appendReply(1, 0, 5, true, cur), 6);
            l.handle(Message.appendReply(2, 0, 5, true, cur), 7);
            check("a current-term entry on 3/5 commits itself and everything before it", l.commitIndex == cur && l.lastApplied == cur);
            check("the older entries were applied first, in order", l.stateMachine.appliedCommands.get(0).equals("PUT x 1") && "3".equals(l.stateMachine.get("x")) && appliedPrefixMatchesLog(l));
        });

        scenario("a follower commits up to min(leaderCommit, last new index) and applies in order", () -> {
            Storage fs = new Storage();
            RaftNode f = standalone(1, fs, 3);
            f.handle(Message.appendEntries(0, 1, 1, 0, 0, List.of(new LogEntry(1, "PUT a 1"), new LogEntry(1, "PUT b 2")), 5), 10);
            check("leaderCommit beyond what the follower holds is capped at the last new entry", f.commitIndex == 2 && f.lastApplied == 2);
            check("the follower's state machine reflects the two applied writes", "1".equals(f.stateMachine.get("a")) && "2".equals(f.stateMachine.get("b")));
            f.handle(Message.appendEntries(0, 1, 1, 2, 1, List.of(new LogEntry(1, "PUT a 3")), 2), 11);
            check("a new entry with an unchanged leaderCommit is stored but not applied", fs.lastIndex() == 3 && f.commitIndex == 2 && "1".equals(f.stateMachine.get("a")));
            f.handle(Message.appendEntries(0, 1, 1, 3, 1, List.of(), 3), 12);
            check("a heartbeat that raises leaderCommit applies the pending entry", f.commitIndex == 3 && "3".equals(f.stateMachine.get("a")) && appliedPrefixMatchesLog(f));
            f.handle(Message.appendEntries(0, 1, 1, 3, 1, List.of(), 1), 13);
            check("a lower leaderCommit never moves commitIndex backwards", f.commitIndex == 3);
            f.handle(Message.appendEntries(0, 1, 1, 7, 1, List.of(), 9), 14);
            check("a refused AppendEntries (consistency check) does not touch commitIndex", f.commitIndex == 3 && f.lastApplied == 3);
            check("applied commands are exactly log[1..lastApplied]", appliedPrefixMatchesLog(f));
        });

        scenario("client PUT/GET through the leader on a quiet cluster", () -> {
            Cluster c = new Cluster(5, 7, 0.0, 5);
            c.runUntilStableLeader(2000);
            int o1 = c.submit("alice", "PUT balance:alice 100");
            int o2 = c.submit("bob", "PUT balance:bob 50");
            int o3 = c.submit("alice", "PUT balance:alice 75");
            int o4 = c.submit("audit", "GET balance:alice");
            check("the leader accepted four client ops", o1 >= 0 && o2 >= 0 && o3 >= 0 && o4 >= 0 && c.pendingCount() == 4);
            int settled = c.runUntilSettled(1000);
            check("every op completes once the leader applies it", settled > 0 && c.pendingCount() == 0 && c.completed.size() == 4 && c.failed.isEmpty());
            Op read = c.completed.stream().filter(o -> o.kind() == OpKind.GET).findFirst().orElse(null);
            check("the read observes the latest committed write", read != null && read.value().equals("75"));
            check("ops return after they were invoked", c.completed.stream().allMatch(o -> o.returnedAt() > o.invokedAt()));
            RaftNode leader = c.stableLeader();
            check("the leader's commitIndex and lastApplied agree and cover the ops", leader.commitIndex == leader.lastApplied && leader.commitIndex >= 4);
            c.runUntilApplied(leader.lastLogIndex(), 1000);
            check("every follower applied the same prefix", c.stateMachinesConverged() && c.nodes.stream().allMatch(Stage04Test::appliedPrefixMatchesLog));
            check("every store agrees on the balances", c.nodes.stream().allMatch(n -> "75".equals(n.stateMachine.get("balance:alice")) && "50".equals(n.stateMachine.get("balance:bob"))));
            check("the recorded history is linearizable", new Checker().check(c.history()).ok());
            check("a follower refuses client ops: submit needs a stable leader", c.node((leader.id + 1) % 5).propose("PUT x 1") == -1);
        });

        scenario("commit needs a majority of matchIndex, not a reply from anyone", () -> {
            Cluster q = new Cluster(5, 11, 0.0, 5);
            q.runUntilStableLeader(2000);
            RaftNode lead = q.stableLeader();
            List<Integer> others = new ArrayList<>(lead.peers);
            q.crash(others.get(0)); q.crash(others.get(1)); q.crash(others.get(2));
            int idx = q.propose("PUT balance 1000");
            q.run(400);
            check("with 3 of 5 crashed the entry replicates to 2/5 and does not commit", idx > 0 && lead.commitIndex < idx && lead.replicatedCount(idx) == 2);
            check("nothing applied the uncommitted write", q.nodes.stream().allMatch(n -> n.stateMachine.get("balance") == null));
            q.restart(others.get(0));
            q.run(400);
            check("one node back (3 live) commits and applies it", lead.commitIndex >= idx && "1000".equals(lead.stateMachine.get("balance")));
            check("the restarted node applied it too, from its rebuilt state", "1000".equals(q.node(others.get(0)).stateMachine.get("balance")));
        });

        scenario("a restart rebuilds the state machine from the persisted log", () -> {
            Cluster c = new Cluster(5, 5, 0.0, 5);
            c.runUntilStableLeader(2000);
            for (int k = 1; k <= 3; k++) c.submit("c", "PUT k" + k + " v" + k);
            c.runUntilSettled(1000);
            c.runUntilApplied(c.stableLeader().lastLogIndex(), 1000);
            int victim = (c.stableLeader().id + 1) % 5;
            check("precondition: the follower applied three writes", c.node(victim).lastApplied >= 3 && "v3".equals(c.node(victim).stateMachine.get("k3")));
            c.crash(victim);
            RaftNode back = c.restart(victim);
            check("volatile state is gone after restart: commitIndex 0, empty store, log intact", back.commitIndex == 0 && back.lastApplied == 0 && back.stateMachine.applied() == 0 && back.lastLogIndex() == c.storages.get(victim).lastIndex());
            c.run(300);
            check("the next heartbeats carry leaderCommit and the node re-applies its log", back.lastApplied == c.stableLeader().lastApplied && "v3".equals(back.stateMachine.get("k3")) && appliedPrefixMatchesLog(back));
            check("state machines converged", c.stateMachinesConverged());
        });

        scenario("a stale leader's write is never applied and the client learns it failed", () -> {
            Cluster c = new Cluster(5, 7, 0.0, 5);
            c.runUntilStableLeader(2000);
            c.submit("alice", "PUT balance:alice 100");
            c.runUntilSettled(1000);
            RaftNode old = c.stableLeader();
            c.partition(Set.of(old.id, (old.id + 1) % 5));
            int lost = c.submit("mallory", "PUT balance:alice 999");
            check("the stale leader still accepts the write (it does not know it is stale)", lost >= 0);
            c.run(600);
            check("the write stays pending inside the minority", c.pendingCount() == 1 && old.stateMachine.get("balance:alice").equals("100"));
            c.heal();
            c.runUntilStableLeader(2000);
            c.submitUntilAccepted("carol", "PUT balance:carol 7", 100);
            c.submitUntilAccepted("audit", "GET balance:alice", 100);
            int settled = c.runUntilSettled(2000);
            check("after heal every op settles", settled > 0);
            check("the minority write failed rather than completing", c.failed.size() == 1 && c.failed.get(0).command().equals("PUT balance:alice 999"));
            c.runUntilApplied(c.stableLeader().lastLogIndex(), 1000);
            check("no state machine ever applied the overwritten write", c.nodes.stream().allMatch(n -> "100".equals(n.stateMachine.get("balance:alice"))) && c.stateMachinesConverged());
            Op read = c.completed.stream().filter(o -> o.kind() == OpKind.GET).findFirst().orElse(null);
            check("the post-heal read sees 100", read != null && read.value().equals("100"));
            check("the completed history is linearizable", new Checker().check(c.history()).ok());
        });

        scenario("the checker: sequential specification and real-time order", () -> {
            Map<String, String> s0 = new TreeMap<>();
            Map<String, String> s1 = Checker.step(s0, new Op(0, "a", OpKind.PUT, "x", "1", 0, 1));
            check("step applies a PUT to a copy, leaving the input alone", s1 != null && "1".equals(s1.get("x")) && s0.isEmpty());
            check("step accepts a GET that matches and rejects one that does not", Checker.step(s1, new Op(1, "b", OpKind.GET, "x", "1", 2, 3)) == s1 && Checker.step(s1, new Op(2, "b", OpKind.GET, "x", "2", 2, 3)) == null);
            check("a GET of an absent key must observe the empty string", Checker.step(s0, new Op(3, "b", OpKind.GET, "y", "", 0, 1)) != null && Checker.step(s0, new Op(4, "b", OpKind.GET, "y", "0", 0, 1)) == null);

            History good = new History().put("alice", "x", "1", 0, 5).put("bob", "x", "2", 6, 10).get("carol", "x", "2", 11, 15);
            CheckResult rg = new Checker().check(good);
            check("a read that sees the latest completed write is linearizable", rg.ok() && respectsRealTime(good, rg.order()));
            History stale = new History().put("alice", "x", "1", 0, 5).put("bob", "x", "2", 6, 10).get("carol", "x", "1", 11, 15);
            CheckResult rs = new Checker().check(stale);
            check("a stale read is not linearizable", !rs.ok() && rs.order().isEmpty());
            check("the verdict names the longest prefix the search could linearize", rs.longestPrefix().size() == 2);
            History window = new History().put("alice", "x", "1", 0, 5).put("bob", "x", "2", 6, 20).get("carol", "x", "1", 11, 15);
            check("a read that overlaps a write may see the old value", new Checker().check(window).ok());
            History order = new History().put("alice", "x", "1", 0, 5).get("carol", "x", "1", 6, 10).put("bob", "x", "2", 2, 12).get("dave", "x", "2", 13, 14);
            CheckResult ro = new Checker().check(order);
            check("the witness order respects every real-time precedence", ro.ok() && respectsRealTime(order, ro.order()) && ro.order().indexOf(1) < ro.order().indexOf(2));
            History twoKeys = new History().put("a", "x", "1", 0, 2).put("b", "y", "9", 0, 2).get("c", "y", "", 3, 4);
            check("keys are independent registers: an absent read after a completed write is not linearizable", !new Checker().check(twoKeys).ok());
            check("the empty history is linearizable", new Checker().check(new History()).ok());
            check("explored counts search states", rg.explored() >= 1 && rs.explored() >= 1);
        });

        scenario("lossy seeds with a leader crash: histories linearizable, state machines converged", () -> {
            int linearizable = 0, converged = 0;
            final int seeds = 12;
            for (long seed = 1; seed <= seeds; seed++) {
                Cluster s = new Cluster(5, seed, 0.2, 10);
                s.runUntilStableLeader(3000);
                for (int k = 1; k <= 5; k++) { s.submitUntilAccepted("c" + (k % 2), "PUT k" + k + " v" + k, 100); s.run(30); }
                RaftNode victim = s.stableLeader() != null ? s.stableLeader() : s.node(0);
                s.crash(victim.id);
                s.runUntilStableLeader(3000);
                for (int k = 1; k <= 5; k++) { s.submitUntilAccepted("c" + (k % 2), "GET k" + k, 100); s.run(30); }
                s.restart(victim.id);
                s.runUntilStableLeader(3000);
                s.runUntilSettled(3000);
                if (s.pendingCount() == 0 && new Checker().check(s.history()).ok()) linearizable++;
                if (s.stableLeader() != null) s.runUntilApplied(s.stableLeader().lastLogIndex(), 3000);
                if (s.stateMachinesConverged() && s.nodes.stream().allMatch(Stage04Test::appliedPrefixMatchesLog)) converged++;
            }
            check(seeds + " lossy seeds: every recorded history is linearizable", linearizable == seeds);
            check(seeds + " lossy seeds: every state machine converged on the same applied prefix", converged == seeds);
        });

        System.out.println("Stage04Test: " + passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL " + f);
        System.exit(failures.isEmpty() ? 0 : 1);
    }
}
