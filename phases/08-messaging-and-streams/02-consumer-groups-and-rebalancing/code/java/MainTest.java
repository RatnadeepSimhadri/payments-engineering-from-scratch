// Tests for phases/08-messaging-and-streams/02-consumer-groups-and-rebalancing. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

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
        List<String> three = List.of("c1", "c2", "c3");
        List<String> four = List.of("c1", "c2", "c3", "c4");

        // range: contiguous, first members get the extra partitions
        Map<String, List<Integer>> range3 = new RangeAssignor().assign(three, 8, Map.of());
        check("range 8/3 c1", range3.get("c1").equals(List.of(0, 1, 2)));
        check("range 8/3 c2", range3.get("c2").equals(List.of(3, 4, 5)));
        check("range 8/3 c3", range3.get("c3").equals(List.of(6, 7)));
        check("range covers every partition exactly once", Assignor.owners(range3).size() == 8);
        check("range more members than partitions leaves some idle",
                new RangeAssignor().assign(three, 2, Map.of()).get("c3").isEmpty());

        // round robin: p -> p mod n
        Map<String, List<Integer>> rr3 = new RoundRobinAssignor().assign(three, 8, Map.of());
        check("round-robin c1", rr3.get("c1").equals(List.of(0, 3, 6)));
        check("round-robin c3", rr3.get("c3").equals(List.of(2, 5)));

        // sticky: balanced, and movement is minimal when a member joins
        StickyAssignor sticky = new StickyAssignor();
        Map<String, List<Integer>> s3 = sticky.assign(three, 8, Map.of());
        check("sticky first generation is balanced", s3.get("c1").size() == 3 && s3.get("c2").size() == 3 && s3.get("c3").size() == 2);
        Map<String, List<Integer>> s4 = sticky.assign(four, 8, s3);
        check("sticky join moves exactly two partitions", Assignor.moved(s3, s4) == 2);
        check("sticky join stays balanced", s4.values().stream().allMatch(l -> l.size() == 2));
        check("sticky covers every partition exactly once", Assignor.owners(s4).size() == 8);
        Map<String, List<Integer>> r4 = new RangeAssignor().assign(four, 8, range3);
        check("range join moves five partitions", Assignor.moved(range3, r4) == 5);
        Map<String, List<Integer>> rr4 = new RoundRobinAssignor().assign(four, 8, rr3);
        check("round-robin join moves more than sticky", Assignor.moved(rr3, rr4) > Assignor.moved(s3, s4));
        Map<String, List<Integer>> sLeave = sticky.assign(List.of("c1", "c3"), 8, s3);
        check("sticky leave: survivors keep everything they had",
                sLeave.get("c1").containsAll(s3.get("c1")) && sLeave.get("c3").containsAll(s3.get("c3")));
        check("sticky leave: orphans are redistributed and balanced", sLeave.get("c1").size() == 4 && sLeave.get("c3").size() == 4);
        check("assignors are deterministic", sticky.assign(four, 8, s3).equals(s4) && new RangeAssignor().assign(three, 8, Map.of()).equals(range3));

        // coordinator: generations, heartbeats, session timeout
        ManualClock clock = new ManualClock(0);
        GroupCoordinator g = new GroupCoordinator("ledger-poster", 8, new RangeAssignor(), 10_000, clock);
        Assignment a1 = g.join("c1");
        check("first join is generation 1 and owns everything", a1.generation() == 1 && a1.partitions().size() == 8);
        g.join("c2");
        g.join("c3");
        check("each new member bumps the generation", g.generation() == 3);
        check("stale generation heartbeat demands a rebalance", g.heartbeat("c1", 1) == HeartbeatResult.REBALANCE_IN_PROGRESS);
        check("unknown member heartbeat", g.heartbeat("ghost", 3) == HeartbeatResult.UNKNOWN_MEMBER);
        Assignment a1b = g.join("c1");
        check("re-join does not bump the generation", a1b.generation() == 3 && g.generation() == 3);
        check("current generation heartbeat is OK", g.heartbeat("c1", 3) == HeartbeatResult.OK);
        check("partition 3 owned by c2 before failure", "c2".equals(g.ownerOf(3)));

        clock.advance(9_999);
        g.heartbeat("c1", 3);
        g.heartbeat("c3", 3);
        check("nobody expires before the session timeout", g.expireSessions().isEmpty() && g.generation() == 3);
        clock.advance(1);
        List<String> dead = g.expireSessions();
        check("c2 expires exactly at the session timeout", dead.equals(List.of("c2")));
        check("expiry bumps the generation", g.generation() == 4);
        check("partition 3 has a new owner and it is deterministic", "c1".equals(g.ownerOf(3)));
        check("every partition still has exactly one owner", Assignor.owners(g.assignment()).size() == 8);

        // commit fencing
        checkThrows("evicted zombie cannot commit", UnknownMemberException.class, () -> g.commitOffset("c2", 3, 3, 100));
        checkThrows("stale generation cannot commit", IllegalGenerationException.class, () -> g.commitOffset("c1", 3, 3, 100));
        checkThrows("non-owner cannot commit", NotOwnerException.class, () -> g.commitOffset("c3", 4, 0, 100));
        g.commitOffset("c1", 4, 3, 1_040);
        check("owner in current generation commits", g.committedOffset(3) == 1_040);
        check("uncommitted partition reads as 0", g.committedOffset(7) == 0);
        checkThrows("negative offset rejected", IllegalArgumentException.class, () -> g.commitOffset("c1", 4, 3, -1));

        // leave is immediate, no session timeout needed
        g.leave("c3");
        check("leave rebalances immediately", g.generation() == 5 && g.members().equals(List.of("c1")) && g.ownerOf(7).equals("c1"));
        checkThrows("leaving twice is an error", UnknownMemberException.class, () -> g.leave("c3"));
        checkThrows("ownerOf rejects bad partition", IllegalArgumentException.class, () -> g.ownerOf(8));
        check("history records every generation", g.history().size() == 5);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
