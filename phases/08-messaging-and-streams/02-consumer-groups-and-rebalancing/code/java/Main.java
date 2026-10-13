// Lesson: phases/08-messaging-and-streams/02-consumer-groups-and-rebalancing/docs/en.md
// Consumer Groups, Offsets, and Rebalancing
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Apache Kafka documentation, section 4.5 "Consumer group protocol"; KIP-54 (sticky assignor); KIP-429
//          (incremental cooperative rebalancing); Kafka Consumer configs session.timeout.ms / heartbeat.interval.ms.
// Java 21, standard library only. Time is injected through ManualClock; nothing sleeps.

import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;
import java.util.TreeSet;

/** Injected time source. Tests and the demo advance it by hand. */
final class ManualClock {
    private long now;

    ManualClock(long startMillis) { this.now = startMillis; }

    long now() { return now; }

    void advance(long millis) {
        if (millis < 0) throw new IllegalArgumentException("clock only moves forward");
        now += millis;
    }
}

/** What a member receives after a successful join: the generation it belongs to and the partitions it owns. */
record Assignment(int generation, List<Integer> partitions) {}

/** Answer to a heartbeat. A member that sees anything but OK must stop processing and act. */
enum HeartbeatResult { OK, REBALANCE_IN_PROGRESS, UNKNOWN_MEMBER }

final class UnknownMemberException extends RuntimeException {
    UnknownMemberException(String memberId) { super("unknown member " + memberId); }
}

final class IllegalGenerationException extends RuntimeException {
    IllegalGenerationException(String memberId, int sent, int current) {
        super("member " + memberId + " sent generation " + sent + " but the group is at " + current);
    }
}

final class NotOwnerException extends RuntimeException {
    NotOwnerException(String memberId, int partition, String owner) {
        super("member " + memberId + " does not own partition " + partition + " (owner: " + owner + ")");
    }
}

/**
 * A partition assignment strategy. Input: the sorted member ids, the partition count, and the previous
 * assignment (empty on the first generation). Output: member id -> sorted partitions. Deterministic by construction:
 * the same inputs produce the same map on every coordinator, every language, every run.
 */
interface Assignor {
    String name();

    Map<String, List<Integer>> assign(List<String> members, int partitionCount, Map<String, List<Integer>> previous);

    /** Number of partitions whose owner changed between two assignments (newly created partitions do not count). */
    static int moved(Map<String, List<Integer>> before, Map<String, List<Integer>> after) {
        Map<Integer, String> ownerBefore = owners(before);
        Map<Integer, String> ownerAfter = owners(after);
        int moved = 0;
        for (Map.Entry<Integer, String> e : ownerBefore.entrySet()) {
            if (!e.getValue().equals(ownerAfter.get(e.getKey()))) moved++;
        }
        return moved;
    }

    static Map<Integer, String> owners(Map<String, List<Integer>> assignment) {
        Map<Integer, String> owners = new TreeMap<>();
        for (Map.Entry<String, List<Integer>> e : assignment.entrySet()) {
            for (int p : e.getValue()) {
                if (owners.put(p, e.getKey()) != null) throw new IllegalStateException("partition " + p + " assigned twice");
            }
        }
        return owners;
    }
}

/** Contiguous ranges: the first (partitions mod members) members receive one extra partition. Kafka's default before 2.4. */
final class RangeAssignor implements Assignor {
    @Override
    public String name() { return "range"; }

    @Override
    public Map<String, List<Integer>> assign(List<String> members, int partitionCount, Map<String, List<Integer>> previous) {
        Map<String, List<Integer>> out = new LinkedHashMap<>();
        if (members.isEmpty()) return out;
        int perMember = partitionCount / members.size();
        int extra = partitionCount % members.size();
        int next = 0;
        for (int i = 0; i < members.size(); i++) {
            int count = perMember + (i < extra ? 1 : 0);
            List<Integer> mine = new ArrayList<>();
            for (int k = 0; k < count; k++) mine.add(next++);
            out.put(members.get(i), mine);
        }
        return out;
    }
}

/** Deal partitions like cards: partition p goes to member p mod n. Balanced, but a membership change moves almost everything. */
final class RoundRobinAssignor implements Assignor {
    @Override
    public String name() { return "round-robin"; }

    @Override
    public Map<String, List<Integer>> assign(List<String> members, int partitionCount, Map<String, List<Integer>> previous) {
        Map<String, List<Integer>> out = new LinkedHashMap<>();
        if (members.isEmpty()) return out;
        for (String m : members) out.put(m, new ArrayList<>());
        for (int p = 0; p < partitionCount; p++) out.get(members.get(p % members.size())).add(p);
        return out;
    }
}

/**
 * Keep what survivors already own, hand orphaned partitions to the least-loaded member, then move single partitions
 * from the most-loaded to the least-loaded member until the spread is at most one. Minimises movement (KIP-54).
 */
final class StickyAssignor implements Assignor {
    @Override
    public String name() { return "sticky"; }

    @Override
    public Map<String, List<Integer>> assign(List<String> members, int partitionCount, Map<String, List<Integer>> previous) {
        Map<String, List<Integer>> out = new LinkedHashMap<>();
        if (members.isEmpty()) return out;
        for (String m : members) out.put(m, new ArrayList<>());
        TreeSet<Integer> unassigned = new TreeSet<>();
        for (int p = 0; p < partitionCount; p++) unassigned.add(p);
        // 1. survivors keep their partitions
        for (String m : members) {
            for (int p : previous.getOrDefault(m, List.of())) {
                if (p < partitionCount && unassigned.remove(p)) out.get(m).add(p);
            }
        }
        // 2. orphans go to whoever has the fewest (ties: lowest member id, because members are sorted)
        for (int p : unassigned) out.get(leastLoaded(members, out)).add(p);
        // 3. level: move one partition at a time from the heaviest to the lightest while the spread exceeds one
        while (true) {
            String most = mostLoaded(members, out), least = leastLoaded(members, out);
            if (out.get(most).size() - out.get(least).size() <= 1) break;
            List<Integer> from = out.get(most);
            int p = from.remove(from.size() - 1);
            out.get(least).add(p);
        }
        for (List<Integer> l : out.values()) Collections.sort(l);
        return out;
    }

    private static String leastLoaded(List<String> members, Map<String, List<Integer>> out) {
        String best = members.get(0);
        for (String m : members) if (out.get(m).size() < out.get(best).size()) best = m;
        return best;
    }

    private static String mostLoaded(List<String> members, Map<String, List<Integer>> out) {
        String best = members.get(0);
        for (String m : members) if (out.get(m).size() > out.get(best).size()) best = m;
        return best;
    }
}

/**
 * The group coordinator for one topic. Owns membership, liveness (heartbeats against an injected clock), the
 * generation counter, the current assignment, and the committed offsets. Every mutation that changes membership
 * bumps the generation and recomputes the assignment; commits carrying an older generation are fenced.
 */
final class GroupCoordinator {
    private final String groupId;
    private final int partitionCount;
    private final Assignor assignor;
    private final long sessionTimeoutMs;
    private final ManualClock clock;

    private int generation = 0;
    private final TreeMap<String, Long> lastHeartbeat = new TreeMap<>();   // memberId -> millis of last contact
    private Map<String, List<Integer>> assignment = new LinkedHashMap<>();
    private final Map<Integer, Long> committed = new TreeMap<>();            // partition -> next offset to read
    private final List<String> history = new ArrayList<>();                  // human-readable rebalance log

    GroupCoordinator(String groupId, int partitionCount, Assignor assignor, long sessionTimeoutMs, ManualClock clock) {
        if (partitionCount <= 0) throw new IllegalArgumentException("partitionCount must be positive");
        if (sessionTimeoutMs <= 0) throw new IllegalArgumentException("sessionTimeoutMs must be positive");
        this.groupId = groupId;
        this.partitionCount = partitionCount;
        this.assignor = assignor;
        this.sessionTimeoutMs = sessionTimeoutMs;
        this.clock = clock;
    }

    int generation() { return generation; }

    List<String> members() { return new ArrayList<>(lastHeartbeat.keySet()); }

    List<String> history() { return Collections.unmodifiableList(history); }

    Map<String, List<Integer>> assignment() {
        Map<String, List<Integer>> copy = new LinkedHashMap<>();
        assignment.forEach((m, ps) -> copy.put(m, List.copyOf(ps)));
        return copy;
    }

    /** A member joins (or re-joins after being told to). Membership changes trigger a rebalance; a re-join does not. */
    Assignment join(String memberId) {
        boolean isNew = !lastHeartbeat.containsKey(memberId);
        lastHeartbeat.put(memberId, clock.now());
        if (isNew) rebalance("join of " + memberId);
        return new Assignment(generation, List.copyOf(assignment.getOrDefault(memberId, List.of())));
    }

    /** A member announces it is leaving. Its partitions are reassigned immediately rather than after the session timeout. */
    void leave(String memberId) {
        if (lastHeartbeat.remove(memberId) == null) throw new UnknownMemberException(memberId);
        rebalance("leave of " + memberId);
    }

    /**
     * Heartbeat from a member that believes it is in {@code generation}. OK refreshes the session; REBALANCE_IN_PROGRESS
     * tells the member its generation is stale and it must re-join before touching any partition.
     */
    HeartbeatResult heartbeat(String memberId, int memberGeneration) {
        if (!lastHeartbeat.containsKey(memberId)) return HeartbeatResult.UNKNOWN_MEMBER;
        lastHeartbeat.put(memberId, clock.now());
        return memberGeneration == generation ? HeartbeatResult.OK : HeartbeatResult.REBALANCE_IN_PROGRESS;
    }

    /** Evict every member whose last heartbeat is older than the session timeout. Called on the coordinator's own tick. */
    List<String> expireSessions() {
        List<String> dead = new ArrayList<>();
        long deadline = clock.now() - sessionTimeoutMs;
        for (Map.Entry<String, Long> e : lastHeartbeat.entrySet()) {
            if (e.getValue() <= deadline) dead.add(e.getKey());
        }
        if (!dead.isEmpty()) {
            dead.forEach(lastHeartbeat::remove);
            rebalance("session timeout of " + dead);
        }
        return dead;
    }

    private void rebalance(String reason) {
        Map<String, List<Integer>> previous = assignment;
        assignment = assignor.assign(members(), partitionCount, previous);
        generation++;
        history.add("generation " + generation + " (" + reason + "): " + assignment + " moved=" + Assignor.moved(previous, assignment));
    }

    /** The member that owns {@code partition} in the current generation, or null when the group is empty. */
    String ownerOf(int partition) {
        if (partition < 0 || partition >= partitionCount) throw new IllegalArgumentException("no partition " + partition);
        return Assignor.owners(assignment).get(partition);
    }

    /**
     * Commit the next offset to read for a partition. Fenced three ways: the member must be known, must be in the
     * current generation, and must own the partition. A zombie that lost its partitions cannot overwrite the new
     * owner's progress.
     */
    void commitOffset(String memberId, int memberGeneration, int partition, long nextOffset) {
        if (!lastHeartbeat.containsKey(memberId)) throw new UnknownMemberException(memberId);
        if (memberGeneration != generation) throw new IllegalGenerationException(memberId, memberGeneration, generation);
        String owner = ownerOf(partition);
        if (!memberId.equals(owner)) throw new NotOwnerException(memberId, partition, owner);
        if (nextOffset < 0) throw new IllegalArgumentException("offset must be non-negative");
        committed.put(partition, nextOffset);
    }

    /** The committed next-offset-to-read for a partition, or 0 when nothing has been committed yet. */
    long committedOffset(int partition) { return committed.getOrDefault(partition, 0L); }

    String groupId() { return groupId; }
}

public final class Main {
    private static void printAssignment(GroupCoordinator g) {
        System.out.printf("  generation %d, members %s%n", g.generation(), g.members());
        g.assignment().forEach((m, ps) -> System.out.printf("    %-4s owns %s%n", m, ps));
    }

    public static void main(String[] args) {
        ManualClock clock = new ManualClock(1_700_000_000_000L);
        final int partitions = 8;
        final long sessionTimeout = 10_000;

        System.out.println("== Three consumers join group 'ledger-poster' (8 partitions, range assignor) ==");
        GroupCoordinator range = new GroupCoordinator("ledger-poster", partitions, new RangeAssignor(), sessionTimeout, clock);
        Assignment a1 = range.join("c1");
        range.join("c2");
        Assignment a3 = range.join("c3");
        printAssignment(range);
        System.out.println("  c1 joined in generation " + a1.generation() + " and was handed " + a1.partitions()
                + "; after c2 and c3 the group is at generation " + range.generation());
        System.out.println("  c1's heartbeat with generation " + a1.generation() + " -> " + range.heartbeat("c1", a1.generation()));
        a1 = range.join("c1");
        System.out.println("  c1 re-joins and now holds generation " + a1.generation() + " with " + a1.partitions());

        System.out.println("\n== c2 stops heartbeating; who owns partition 3? ==");
        clock.advance(4_000);
        range.heartbeat("c1", range.generation());
        range.heartbeat("c3", a3.generation());
        System.out.println("  t+4s : expired = " + range.expireSessions() + " (c2 still inside its 10s session)");
        clock.advance(6_000);
        range.heartbeat("c1", range.generation());
        range.heartbeat("c3", range.generation());
        System.out.println("  t+10s: expired = " + range.expireSessions());
        printAssignment(range);
        System.out.println("  partition 3 is owned by " + range.ownerOf(3));

        System.out.println("\n== The zombie: c2 comes back from its GC pause and tries to commit partition 3 ==");
        try {
            range.commitOffset("c2", 3, 3, 1_042);
        } catch (UnknownMemberException e) {
            System.out.println("  rejected: " + e.getMessage());
        }
        range.commitOffset(range.ownerOf(3), range.generation(), 3, 1_040);
        System.out.println("  new owner committed next offset " + range.committedOffset(3) + " for partition 3");
        Assignment zombie = range.join("c2");
        System.out.println("  c2 re-joins -> generation " + zombie.generation() + ", owns " + zombie.partitions());
        try {
            range.commitOffset("c2", zombie.generation() - 1, zombie.partitions().get(0), 7);
        } catch (IllegalGenerationException e) {
            System.out.println("  stale generation rejected: " + e.getMessage());
        }

        System.out.println("\n== Adding a fourth consumer: how many partitions move? ==");
        for (Assignor assignor : List.of(new RangeAssignor(), new RoundRobinAssignor(), new StickyAssignor())) {
            List<String> three = List.of("c1", "c2", "c3");
            Map<String, List<Integer>> before = assignor.assign(three, partitions, Map.of());
            Map<String, List<Integer>> after = assignor.assign(List.of("c1", "c2", "c3", "c4"), partitions, before);
            System.out.printf("  %-11s before=%s%n", assignor.name(), before);
            System.out.printf("  %-11s after =%s moved=%d%n", "", after, Assignor.moved(before, after));
        }

        System.out.println("\n== Rebalance history (range group) ==");
        range.history().forEach(h -> System.out.println("  " + h));
    }
}
