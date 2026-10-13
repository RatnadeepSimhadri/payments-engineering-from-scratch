// Project: projects/raft-kv — Raft-replicated key-value store on a deterministic simulated network.
// Run:  javac -d .out *.java && java -cp .out Main
// Grade: python3 scripts/project_test.py raft-kv --lang java --all --path <workspace>
// Sources: Ongaro & Ousterhout, "In Search of an Understandable Consensus Algorithm" (USENIX ATC 2014);
//          Herlihy & Wing, "Linearizability" (TOPLAS 1990). Java 21, standard library only.

import java.util.Set;

public final class Main {
    static void printEvents(Cluster c, int from) { for (int i = from; i < c.events.size(); i++) System.out.println(c.events.get(i)); }

    static void printNodes(Cluster c) {
        for (RaftNode n : c.nodes) {
            System.out.printf("  %s %-9s term %d log=%s commit=%d applied=%d kv=%s%n", RaftNode.name(n.id), n.role, n.currentTerm(),
                    c.logOf(n.id), n.commitIndex, n.lastApplied, n.stateMachine);
        }
    }

    public static void main(String[] args) {
        System.out.println("== Stage 2: election on a 5-node cluster (seed 7, no drops, delay 1-5 ticks) ==");
        Cluster c = new Cluster(5, 7, 0.0, 5);
        int electedAt = c.runUntilStableLeader(2000);
        printEvents(c, 0);
        RaftNode leader = c.stableLeader();
        System.out.printf("leader %s, term %d, stable at tick %d; messages sent=%d delivered=%d dropped=%d%n",
                RaftNode.name(leader.id), leader.currentTerm(), electedAt, c.network.sent, c.network.delivered, c.network.dropped);

        System.out.println();
        System.out.println("== Stage 3-4: three client writes and a read through the leader ==");
        int before = c.events.size();
        c.submit("alice", "PUT balance:alice 100");
        c.submit("bob", "PUT balance:bob 50");
        c.submit("alice", "PUT balance:alice 75");
        c.submit("audit", "GET balance:alice");
        c.runUntilSettled(1000);
        c.runUntilApplied(c.stableLeader().lastLogIndex(), 1000);
        printEvents(c, before);
        for (Op o : c.completed) System.out.println("  " + o);
        printNodes(c);

        System.out.println();
        System.out.println("== A stale leader's uncommitted writes are overwritten, never applied ==");
        RaftNode old = c.stableLeader();
        int follower = (old.id + 1) % 5;
        before = c.events.size();
        c.partition(Set.of(old.id, follower));
        c.submit("mallory", "PUT balance:alice 999");
        c.run(600);
        RaftNode fresh = null;
        for (RaftNode l : c.leaders()) if (fresh == null || l.currentTerm() > fresh.currentTerm()) fresh = l;
        System.out.printf("minority side: %s still leads term %d with an uncommitted write; majority elected %s for term %d%n",
                RaftNode.name(old.id), old.currentTerm(), RaftNode.name(fresh.id), fresh.currentTerm());
        c.heal();
        c.runUntilStableLeader(2000);
        c.submitUntilAccepted("carol", "PUT balance:carol 7", 100);
        c.runUntilSettled(2000);
        c.runUntilApplied(c.stableLeader().lastLogIndex(), 1000);
        printEvents(c, before);
        printNodes(c);
        System.out.printf("failed client ops: %d (%s); state machines converged = %b%n", c.failed.size(),
                c.failed.isEmpty() ? "-" : c.failed.get(0).command(), c.stateMachinesConverged());

        System.out.println();
        System.out.println("== Linearizability of the recorded history ==");
        History h = c.history();
        CheckResult r = new Checker().check(h);
        System.out.println(r.describe(h));

        System.out.println();
        System.out.println("== A history no Raft cluster should ever produce: a stale read ==");
        History stale = new History()
                .put("alice", "x", "1", 0, 5)
                .put("bob", "x", "2", 6, 10)
                .get("carol", "x", "1", 11, 15);
        System.out.println(new Checker().check(stale).describe(stale));

        System.out.println();
        System.out.println("== 20 seeds, 5 nodes, 20% drops, delay 1-10, leader crash and restart, 10 client ops each ==");
        int linearizable = 0, converged = 0;
        for (long seed = 1; seed <= 20; seed++) {
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
            s.runUntilApplied(s.stableLeader().lastLogIndex(), 3000);
            if (s.stateMachinesConverged()) converged++;
        }
        System.out.printf("linearizable histories: %d/20; converged state machines: %d/20; no invariant check fired%n", linearizable, converged);
    }
}
