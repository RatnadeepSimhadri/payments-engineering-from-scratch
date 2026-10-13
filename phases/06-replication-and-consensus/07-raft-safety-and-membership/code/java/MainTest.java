// Tests for phases/06-replication-and-consensus/07-raft-safety-and-membership. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeSet;

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

    static Set<Integer> set(Integer... xs) { return new TreeSet<>(List.of(xs)); }

    public static void main(String[] args) {
        // ---- configurations and quorums (section 6)
        Configuration three = Configuration.simple(set(0, 1, 2));
        Configuration joint = Configuration.joint(set(0, 1, 2), set(0, 1, 2, 3, 4));
        check("simple majority of 3 is 2", three.hasQuorum(set(0, 1)) && !three.hasQuorum(set(0)));
        check("members outside the configuration are not counted", !three.hasQuorum(set(0, 3, 4)));
        check("joint needs majority of old AND new", joint.hasQuorum(set(0, 1, 3)) && !joint.hasQuorum(set(0, 1)) && !joint.hasQuorum(set(2, 3, 4)));
        check("joint union is everyone", joint.all().equals(set(0, 1, 2, 3, 4)));
        check("direct switch 3->5 admits disjoint majorities (Figure 10)", Configuration.disjointQuorumsPossible(set(0, 1, 2), set(0, 1, 2, 3, 4)));
        check("adding ONE server never admits disjoint majorities (thesis 4.1: single-server changes are safe)", !Configuration.disjointQuorumsPossible(set(0, 1, 2), set(0, 1, 2, 3)));
        check("removing one server is safe too", !Configuration.disjointQuorumsPossible(set(0, 1, 2, 3, 4), set(0, 1, 2, 3)));
        check("two quorums of the same configuration always intersect", !Configuration.disjointQuorumsPossible(set(0, 1, 2), set(0, 1, 2)));
        check("config round-trips through the log as a command", Configuration.decode(joint.encode()).equals(joint) && Configuration.decode(three.encode()).equals(three));
        check("decoded joint is joint", Configuration.decode("CFG 0,1,2 -> 1,2,3").isJoint());

        // ---- storage with compaction (section 7)
        Storage st = new Storage();
        for (int i = 1; i <= 6; i++) st.append(new LogEntry(1, i == 3 ? "CFG 0,1,2" : "SET k" + i + " v"));
        Snapshot snap = new Snapshot(4, 1, Map.of("k1", "v"), Configuration.simple(set(0, 1, 2)));
        st.compact(snap);
        check("compact keeps entries after the snapshot", st.snapshotIndex == 4 && st.lastIndex() == 6 && st.firstIndex() == 5);
        check("termAt the snapshot index is the snapshot term", st.termAt(4) == 1 && st.termAt(6) == 1);
        checkThrows("termAt below the snapshot is an error", IllegalStateException.class, () -> st.termAt(2));
        check("configAt falls back to the snapshot's configuration", st.configAt(6, null).equals(Configuration.simple(set(0, 1, 2))) && st.latestConfigIndex() == -1);
        st.append(new LogEntry(2, "CFG 0,1,2 -> 0,1"));
        check("configAt finds the latest config in the log", st.configAt(7, null).isJoint() && st.latestConfigIndex() == 7);
        check("configAt at an earlier index ignores later configs", !st.configAt(6, null).isJoint());

        // ---- a server with no configuration never campaigns
        Cluster idle = new Cluster(3, Set.of(0, 1), 1, 0.0, 3, Config.DEFAULT);
        idle.run(1500);
        check("spare server with no config stays follower at term 0", idle.node(2).role == Role.FOLLOWER && idle.node(2).currentTerm() <= idle.node(0).currentTerm());
        check("the two configured servers elected a leader", idle.stableLeader() != null);

        // ---- grow 3 -> 5 with joint consensus under traffic
        Set<Integer> five = set(0, 1, 2, 3, 4);
        Cluster c = new Cluster(5, Set.of(0, 1, 2), 7, 0.0, 5, new Config(150, 300, 50, 1000));
        c.runUntilStableLeader(2000);
        c.propose("SET alice 100");
        RaftNode growLeader = c.stableLeader();
        int cfgIdx = c.changeMembership(five);
        check("leader accepts the change and logs C_old,new", cfgIdx > 0 && growLeader.configuration().isJoint());
        check("a second change is refused while the first is in flight", growLeader.changeMembership(set(0, 1, 2, 3)) == -1);
        c.propose("SET bob 50");
        int done = c.runUntilConfigured(five, 3000);
        check("C_new is committed", done > 0 && c.stableLeader().configuration().equals(Configuration.simple(five)));
        c.propose("SET carol 7");
        c.runUntilApplied(c.stableLeader().lastLogIndex(), 2000);
        check("all five servers hold the same configuration", c.nodes.stream().allMatch(n -> n.configuration().equals(Configuration.simple(five))));
        check("all five state machines converged including entries proposed mid-change", c.stateMachinesConverged(five) && c.node(3).stateMachine.get("bob").equals("50"));
        check("the joint entry precedes the simple entry in the log", c.node(4).storage.get(cfgIdx).command().contains("->") && !c.node(4).storage.get(cfgIdx + 1).command().contains("->"));

        // ---- remove the leader: it steps down after committing C_new and never campaigns again
        RaftNode oldLeader = c.stableLeader();
        Set<Integer> four = new TreeSet<>(five);
        four.remove(oldLeader.id);
        check("removal change accepted", c.changeMembership(four) > 0);
        check("C_new committed without the leader's own vote counting", c.runUntilConfigured(four, 3000) > 0 || oldLeader.steppedDownAfterRemoval);
        c.runUntilStableLeader(3000);
        check("old leader stepped down", oldLeader.steppedDownAfterRemoval && oldLeader.role != Role.LEADER);
        RaftNode newLeader = c.stableLeader();
        check("a new leader was elected among the remaining four", newLeader != null && newLeader.id != oldLeader.id && four.contains(newLeader.id));
        c.run(1500);
        check("removed server does not disrupt the cluster with elections", oldLeader.role == Role.FOLLOWER && c.stableLeader() != null && c.stableLeader().currentTerm() == newLeader.currentTerm());
        int after = c.propose("SET dave 1");
        check("cluster keeps accepting writes", after > 0 && c.runUntilApplied(after, 2000) > 0);

        // ---- snapshots: independent compaction, InstallSnapshot to a lagging follower (rule 7), convergence
        Cluster s = new Cluster(3, 11, 0.0, 5);
        s.runUntilStableLeader(2000);
        for (int k = 1; k <= 5; k++) s.propose("SET k" + k + " v" + k);
        s.runUntilApplied(s.stableLeader().lastLogIndex(), 1000);
        RaftNode lead = s.stableLeader();
        int follower = (lead.id + 1) % 3;
        s.crash(follower);
        for (int k = 6; k <= 16; k++) { s.propose("SET k" + k + " v" + k); s.run(20); }
        s.run(200);
        check("leader compacted its log", lead.snapshotsTaken >= 1 && lead.storage.snapshotIndex >= 8);
        check("leader's state machine is intact after compaction", lead.stateMachine.get("k1").equals("v1") && lead.stateMachine.get("k16").equals("v16"));
        s.restart(follower);
        s.runUntilApplied(lead.lastLogIndex(), 3000);
        check("lagging follower received a snapshot", s.node(follower).snapshotsInstalled >= 1 && lead.snapshotsSent >= 1);
        check("follower's log starts after the snapshot", s.node(follower).storage.snapshotIndex >= 8 && s.node(follower).storage.firstIndex() > 8);
        check("follower state machine rebuilt from the snapshot plus the tail", s.node(follower).stateMachine.get("k1").equals("v1") && s.stateMachinesConverged(Set.of(0, 1, 2)));
        check("snapshot carries the configuration", s.node(follower).storage.snapshot.config().equals(Configuration.simple(set(0, 1, 2))));

        // ---- rule 6: a snapshot whose last entry the follower already has keeps the later entries
        Storage r6 = new Storage();
        for (int i = 1; i <= 5; i++) r6.append(new LogEntry(1, "SET k" + i + " v"));
        RaftNode f6 = new RaftNode(0, Configuration.simple(set(0, 1, 2)), r6, new Rng(1), Config.DEFAULT, 0, (t, m) -> {});
        f6.storage.save(1, -1);
        f6.handle(Message.installSnapshot(1, 0, 1, new Snapshot(3, 1, Map.of("k1", "v", "k2", "v", "k3", "v"), Configuration.simple(set(0, 1, 2)))), 1);
        check("rule 6: matching snapshot keeps the following entries", f6.storage.snapshotIndex == 3 && f6.lastLogIndex() == 5 && f6.storage.get(5).command().equals("SET k5 v"));
        check("rule 8: state machine reset to the snapshot and lastApplied advanced", f6.lastApplied == 3 && f6.stateMachine.get("k3").equals("v") && f6.commitIndex == 3);
        f6.handle(Message.installSnapshot(1, 0, 1, new Snapshot(2, 1, Map.of(), Configuration.simple(set(0, 1, 2)))), 2);
        check("an older snapshot is ignored", f6.storage.snapshotIndex == 3);

        // ---- a restarted server reloads its snapshot into the state machine
        RaftNode re = new RaftNode(0, Configuration.simple(set(0, 1, 2)), r6, new Rng(2), Config.DEFAULT, 0, (t, m) -> {});
        check("restart from storage rebuilds the state machine from the snapshot", re.lastApplied == 3 && re.stateMachine.get("k2").equals("v"));

        // ---- the checker really fires: two leaders in one term is caught
        Cluster forged = new Cluster(2, 3, 0.0, 2);
        forged.node(0).storage.save(5, 0); forged.node(0).role = Role.LEADER;
        forged.node(1).storage.save(5, 1); forged.node(1).role = Role.LEADER;
        checkThrows("Election Safety violation is detected", IllegalStateException.class, forged::checkInvariants);

        // ---- Leader Completeness violation is caught: a leader missing a committed entry
        Cluster forged2 = new Cluster(2, 4, 0.0, 2);
        forged2.node(0).storage.append(new LogEntry(1, "SET x 1")); forged2.node(0).commitIndex = 1; forged2.node(0).storage.save(1, -1);
        forged2.checkInvariants();                                   // records index 1 as committed
        forged2.node(1).storage.save(2, 1); forged2.node(1).role = Role.LEADER;   // empty log, claims term 2
        checkThrows("Leader Completeness violation is detected", IllegalStateException.class, forged2::checkInvariants);

        // ---- safety and convergence across seeds with everything at once
        int converged = 0, changes = 0;
        for (long seed = 1; seed <= 8; seed++) {
            Cluster r = new Cluster(5, Set.of(0, 1, 2), seed, 0.2, 10, Config.DEFAULT);
            r.runUntilStableLeader(4000);
            for (int k = 1; k <= 5; k++) { Main.proposeUntilAccepted(r, "SET k" + k + " v" + k); r.run(30); }
            Main.changeUntilAccepted(r, five);
            for (int k = 6; k <= 10; k++) { Main.proposeUntilAccepted(r, "SET k" + k + " v" + k); r.run(30); }
            if (r.runUntilConfigured(five, 6000) > 0) changes++;
            RaftNode victim = r.stableLeader() != null ? r.stableLeader() : r.node(0);
            r.crash(victim.id);
            r.runUntilStableLeader(4000);
            for (int k = 11; k <= 18; k++) { Main.proposeUntilAccepted(r, "SET k" + k + " v" + k); r.run(30); }
            r.restart(victim.id);
            r.runUntilStableLeader(4000);
            Set<Integer> fourB = new TreeSet<>(five);
            fourB.remove((victim.id + 1) % 5);
            Main.changeUntilAccepted(r, fourB);
            if (r.runUntilConfigured(fourB, 6000) > 0) changes++;
            Main.proposeUntilAccepted(r, "SET final yes");
            RaftNode fin = r.stableLeader();
            if (fin != null) r.runUntilApplied(fin.lastLogIndex(), 4000);
            if (fin != null && r.stateMachinesConverged(fourB) && "yes".equals(fin.stateMachine.get("final")) && "v18".equals(fin.stateMachine.get("k18"))) converged++;
        }
        check("8/8 seeds converge through 3->5->4 with drops, a crash, and snapshots", converged == 8);
        check("16/16 membership changes completed", changes == 16);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
