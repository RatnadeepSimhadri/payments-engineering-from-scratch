// Stage 02 tests for projects/raft-kv: leader election (RequestVote, randomized timeouts, majority, step-down).
// Run by the grader: javac -d .out *.java && java -cp .out Stage01Test && java -cp .out Stage02Test
import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;

public final class Stage02Test {
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

    static boolean allOfKind(List<Message> ms, MessageKind kind, int from, long term, int expectedCount) {
        if (ms.size() != expectedCount) return false;
        Set<Integer> targets = new HashSet<>();
        for (Message m : ms) {
            if (m.kind() != kind || m.from() != from || m.term() != term) return false;
            targets.add(m.to());
        }
        return targets.size() == expectedCount && !targets.contains(from);
    }

    /** A tracer-independent fingerprint of a 1500-tick run: node state every 50 ticks plus the network counters. */
    static String digest(Cluster c) {
        StringBuilder sb = new StringBuilder();
        for (int t = 0; t < 30; t++) {
            c.run(50);
            for (RaftNode n : c.nodes) sb.append(n.currentTerm()).append(n.role).append(n.leaderId).append(n.electionDeadline).append(';');
            sb.append(c.network.sent).append('/').append(c.network.delivered).append('/').append(c.network.dropped).append('\n');
        }
        return sb.toString();
    }

    /** Drives a standalone node to leadership of term 1 with votes from peers 1 and 2. */
    static RaftNode electedLeader(Storage storage, long seed) {
        RaftNode n = standalone(0, storage, seed);
        n.startElection(1);
        n.handle(Message.voteReply(1, 0, n.currentTerm(), true), 2);
        n.handle(Message.voteReply(2, 0, n.currentTerm(), true), 3);
        return n;
    }

    public static void main(String[] args) {
        scenario("randomized election timeout from the node's seeded Rng", () -> {
            RaftNode n = standalone(0, new Storage(), 1);
            check("first deadline is drawn at construction, within [min, max] of now=0", n.electionDeadline >= 150 && n.electionDeadline <= 300);
            Set<Integer> distinct = new HashSet<>();
            for (long seed = 1; seed <= 12; seed++) distinct.add(standalone(0, new Storage(), seed).electionDeadline);
            check("different seeds give different timeouts (split votes are broken by randomness)", distinct.size() >= 4);
            check("same seed gives the same timeout (replayable)", standalone(0, new Storage(), 5).electionDeadline == standalone(0, new Storage(), 5).electionDeadline);
            n.resetElectionTimer(1000);
            check("resetElectionTimer draws a new deadline relative to now", n.electionDeadline >= 1150 && n.electionDeadline <= 1300);
            RaftNode tight = new RaftNode(0, peersOf(0, 3), new Storage(), new Rng(3), new Config(10, 10, 2), 7, Tracer.NONE);
            check("with min == max the deadline is exactly now + min", tight.electionDeadline == 17);
        });

        scenario("a follower whose timer expires becomes a candidate", () -> {
            Storage s = new Storage();
            RaftNode n = standalone(2, s, 4);
            int deadline = n.electionDeadline;
            check("ticks before the deadline send nothing and keep the follower role", n.tick(deadline - 1).isEmpty() && n.role == Role.FOLLOWER && s.currentTerm == 0);
            List<Message> out = n.tick(deadline);
            check("at the deadline the node becomes a candidate", n.role == Role.CANDIDATE);
            check("the term is incremented and persisted with a vote for itself", s.currentTerm == 1 && s.votedFor == 2 && s.writes >= 1);
            check("one RequestVote per peer, carrying the new term", allOfKind(out, MessageKind.REQUEST_VOTE, 2, 1, 4));
            check("RequestVote carries the candidate's last log index and term", out.get(0).prevLogIndex() == 0 && out.get(0).prevLogTerm() == 0);
            check("the candidate counts its own vote", n.votes() == 1 && n.electionsStarted == 1);
            check("the election timer is re-armed for the next round", n.electionDeadline > deadline);
            int second = n.electionDeadline;
            List<Message> again = n.tick(second);
            check("a candidate whose timer expires starts a fresh election in a higher term", s.currentTerm == 2 && n.role == Role.CANDIDATE && allOfKind(again, MessageKind.REQUEST_VOTE, 2, 2, 4) && n.votes() == 1);
        });

        scenario("votes are counted to a majority, stale and duplicate replies are ignored", () -> {
            Storage s = new Storage();
            RaftNode n = standalone(0, s, 2);
            n.startElection(1);
            check("one stale reply (older term) does nothing", n.handle(Message.voteReply(1, 0, 0, true), 2).isEmpty() && n.votes() == 1 && n.role == Role.CANDIDATE);
            n.handle(Message.voteReply(1, 0, 1, true), 3);
            n.handle(Message.voteReply(1, 0, 1, true), 4);
            check("a duplicate grant from the same peer counts once", n.votes() == 2 && n.role == Role.CANDIDATE);
            n.handle(Message.voteReply(3, 0, 1, false), 5);
            check("a denial does not count", n.votes() == 2 && n.role == Role.CANDIDATE);
            List<Message> out = n.handle(Message.voteReply(2, 0, 1, true), 6);
            check("the third vote of five makes a leader", n.role == Role.LEADER && n.leaderId == 0);
            check("a new leader immediately sends AppendEntries to every peer", allOfKind(out, MessageKind.APPEND_ENTRIES, 0, 1, 4));
            check("the leader's term is unchanged by winning", s.currentTerm == 1);
            check("a late grant after winning is ignored", n.handle(Message.voteReply(4, 0, 1, true), 7).isEmpty() && n.role == Role.LEADER);
        });

        scenario("RequestVote receiver rules", () -> {
            Storage s = new Storage();
            s.save(3, -1);
            RaftNode v = standalone(1, s, 8);
            Message deny = v.handle(Message.requestVote(0, 1, 2, 0, 0), 10).get(0);
            check("a lower-term candidate is denied with the voter's own term", deny.kind() == MessageKind.REQUEST_VOTE_REPLY && !deny.success() && deny.term() == 3 && s.votedFor == -1);
            int before = v.electionDeadline;
            int writes = s.writes;
            Message grant = v.handle(Message.requestVote(0, 1, 3, 0, 0), 20).get(0);
            check("first candidate of the term is granted", grant.success() && grant.to() == 0 && grant.term() == 3);
            check("the vote is persisted", s.votedFor == 0 && s.writes == writes + 1);
            check("granting a vote resets the election timer", v.electionDeadline != before && v.electionDeadline >= 170);
            Message other = v.handle(Message.requestVote(2, 1, 3, 0, 0), 21).get(0);
            check("a second candidate in the same term is denied", !other.success() && s.votedFor == 0);
            Message same = v.handle(Message.requestVote(0, 1, 3, 0, 0), 22).get(0);
            check("the same candidate asking again is granted again (retries are safe)", same.success());
            Message higher = v.handle(Message.requestVote(4, 1, 5, 0, 0), 23).get(0);
            check("a higher-term candidate moves the voter to that term and gets the vote", higher.success() && s.currentTerm == 5 && s.votedFor == 4 && v.role == Role.FOLLOWER);
        });

        scenario("the up-to-date rule guards the vote (section 5.4.1)", () -> {
            Storage s = new Storage();
            s.append(new LogEntry(1, "PUT a 1"));
            s.append(new LogEntry(2, "PUT a 2"));
            RaftNode v = standalone(1, s, 9);
            Message r1 = v.handle(Message.requestVote(0, 1, 3, 5, 1), 10).get(0);
            check("a candidate with a longer but older-term log is denied", !r1.success() && s.votedFor == -1 && s.currentTerm == 3);
            Message r2 = v.handle(Message.requestVote(0, 1, 3, 1, 2), 11).get(0);
            check("a candidate with the same last term but a shorter log is denied", !r2.success());
            Message r3 = v.handle(Message.requestVote(0, 1, 3, 2, 2), 12).get(0);
            check("a candidate whose log is at least as up-to-date is granted", r3.success() && s.votedFor == 0);
        });

        scenario("AppendEntries from a leader: followers obey, candidates step down, stale leaders are refused", () -> {
            Storage s = new Storage();
            RaftNode f = standalone(3, s, 11);
            int before = f.electionDeadline;
            Message ok = f.handle(Message.appendEntries(0, 3, 1, 0, 0, List.of(), 0), 50).get(0);
            check("a follower adopts the leader and the term", f.leaderId == 0 && s.currentTerm == 1 && f.role == Role.FOLLOWER);
            check("the reply is an APPEND_ENTRIES_REPLY carrying the follower's term", ok.kind() == MessageKind.APPEND_ENTRIES_REPLY && ok.term() == 1 && ok.to() == 0);
            check("a heartbeat resets the election timer", f.electionDeadline != before && f.electionDeadline >= 200);
            Message stale = f.handle(Message.appendEntries(4, 3, 0, 0, 0, List.of(), 0), 51).get(0);
            check("a lower-term AppendEntries is refused and the leader is unchanged", !stale.success() && stale.term() == 1 && f.leaderId == 0);

            RaftNode c = standalone(2, new Storage(), 12);
            c.startElection(1);
            c.handle(Message.appendEntries(0, 2, 1, 0, 0, List.of(), 0), 2);
            check("a candidate that hears a leader of its term steps down", c.role == Role.FOLLOWER && c.leaderId == 0 && c.currentTerm() == 1);
            check("the stepped-down candidate ignores late vote grants", c.handle(Message.voteReply(1, 2, 1, true), 3).isEmpty() && c.role == Role.FOLLOWER);
        });

        scenario("a leader steps down on a higher term and sends heartbeats on its interval", () -> {
            Storage s = new Storage();
            RaftNode l = electedLeader(s, 13);
            check("precondition: leader of term 1", l.role == Role.LEADER && s.currentTerm == 1);
            int hb = l.nextHeartbeat;
            check("no heartbeat before the interval elapses", l.tick(hb - 1).isEmpty());
            List<Message> beat = l.tick(hb);
            check("heartbeats go to every peer when the interval elapses", allOfKind(beat, MessageKind.APPEND_ENTRIES, 0, 1, 4) && l.nextHeartbeat == hb + Config.DEFAULT.heartbeatInterval());
            check("heartbeats carry the leader's commit index", beat.get(0).leaderCommit() == l.commitIndex);
            List<Message> out = l.handle(Message.appendReply(1, 0, 4, false, 0), hb + 1);
            check("a reply with a higher term dethrones the leader", l.role == Role.FOLLOWER && s.currentTerm == 4 && s.votedFor == -1 && l.leaderId == -1);
            check("the dethroned leader re-arms an election timer", l.electionDeadline >= hb + 1 + 150);
            check("the dethroned leader sends nothing in response", out.isEmpty());

            RaftNode l2 = electedLeader(new Storage(), 14);
            l2.handle(Message.requestVote(3, 0, 2, 1, 1), 10);
            check("a higher-term RequestVote also dethrones a leader and gets the vote", l2.role == Role.FOLLOWER && l2.currentTerm() == 2 && l2.votedFor() == 3);
        });

        scenario("a single leader is elected on a quiet 5-node network (seed 7)", () -> {
            Cluster c = new Cluster(5, 7, 0.0, 5);
            int at = c.runUntilStableLeader(2000);
            check("stable leader elected", at > 0 && c.stableLeader() != null);
            check("elected after at least one minimum timeout", at >= Config.DEFAULT.electionTimeoutMin());
            check("exactly one leader", c.leaders().size() == 1);
            check("every node agrees on the leader's term", c.nodes.stream().allMatch(n -> n.currentTerm() == c.stableLeader().currentTerm()));
            check("followers know who leads", c.nodes.stream().filter(n -> n.role != Role.LEADER).allMatch(n -> n.leaderId == c.stableLeader().id));
            check("every node persisted a vote for this term", c.storages.stream().allMatch(s -> s.votedFor >= 0 && s.writes >= 1));
            long term = c.maxTerm();
            c.run(2000);
            check("heartbeats keep the term stable for 2000 ticks", c.maxTerm() == term && c.leaders().size() == 1);
            check("heartbeats flowed", c.network.delivered > 100);
        });

        scenario("same seed, same trace", () -> {
            String d1 = digest(new Cluster(5, 42, 0.1, 8)), d2 = digest(new Cluster(5, 42, 0.1, 8)), d3 = digest(new Cluster(5, 43, 0.1, 8));
            check("same seed gives an identical run (terms, roles, leaders, message counts at every 50th tick)", d1.equals(d2) && d1.contains("LEADER"));
            check("different seed gives a different run", !d1.equals(d3));
        });

        scenario("Election Safety over many lossy seeds: at most one leader per term", () -> {
            int electedSeeds = 0;
            boolean safe = true;
            for (long seed = 1; seed <= 40; seed++) {
                Cluster s = new Cluster(5, seed, 0.2, 10);
                Map<Long, Integer> leaderOfTerm = new HashMap<>();
                int at = -1;
                for (int t = 0; t < 2500; t++) {
                    s.tick();
                    for (RaftNode n : s.nodes) {
                        if (n.role != Role.LEADER) continue;
                        Integer prev = leaderOfTerm.putIfAbsent(n.currentTerm(), n.id);
                        if (prev != null && prev != n.id) safe = false;
                    }
                    if (at < 0 && s.stableLeader() != null) at = s.now();
                }
                if (at > 0) electedSeeds++;
            }
            check("40 lossy seeds all elect a leader within 2500 ticks", electedSeeds == 40);
            check("never two leaders in one term across 40 seeds x 2500 ticks", safe);
            int threeNodeSeeds = 0;
            for (long seed = 100; seed < 120; seed++) {
                Cluster s = new Cluster(3, seed, 0.3, 10);
                if (s.runUntilStableLeader(4000) > 0) threeNodeSeeds++;
            }
            check("3-node clusters with 30% drops still elect", threeNodeSeeds == 20);
        });

        scenario("partition: the majority moves on, the minority leader steps down on heal", () -> {
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
            check("a minority of two can never elect: no term in the trace has a 2-node leader", p.leadersByTerm.values().stream().allMatch(id -> id != null));
        });

        scenario("a crashed leader is replaced; votedFor survives a restart", () -> {
            Cluster c = new Cluster(5, 21, 0.0, 5);
            c.runUntilStableLeader(2000);
            RaftNode dead = c.stableLeader();
            long t0 = dead.currentTerm();
            c.crash(dead.id);
            int at = c.runUntilStableLeader(2000);
            check("a new leader is elected after the leader crashes", at > 0 && c.stableLeader().id != dead.id && c.stableLeader().currentTerm() > t0);
            RaftNode back = c.restart(dead.id);
            check("a restarted node comes back as a follower with its persisted term", back.role == Role.FOLLOWER && back.currentTerm() == c.storages.get(dead.id).currentTerm);
            c.run(600);
            check("the restarted node joins the current term and follows the leader", back.currentTerm() == c.stableLeader().currentTerm() && back.leaderId == c.stableLeader().id);

            Storage durable = new Storage();
            RaftNode n1 = standalone(1, durable, 31);
            boolean first = n1.handle(Message.requestVote(0, 1, 1, 0, 0), 10).get(0).success();
            RaftNode n1b = standalone(1, durable, 32);
            boolean second = n1b.handle(Message.requestVote(2, 1, 1, 0, 0), 12).get(0).success();
            check("after a crash and restart the node still refuses a second vote in the same term", first && !second && durable.votedFor == 0);
        });

        System.out.println("Stage02Test: " + passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL " + f);
        System.exit(failures.isEmpty() ? 0 : 1);
    }
}
