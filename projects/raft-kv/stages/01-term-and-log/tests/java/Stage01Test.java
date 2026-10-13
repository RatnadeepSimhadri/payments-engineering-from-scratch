// Stage 01 tests for projects/raft-kv: persistent state (Storage) and the provided simulation scaffolding (Sim.java).
// Run by the grader: javac -d .out *.java && java -cp .out Stage01Test
import java.util.ArrayList;
import java.util.List;
import java.util.Set;

/** A minimal network participant for the scaffolding tests. Records what it receives; echoes AppendEntries. */
final class Stage01Probe implements Node {
    final int id;
    final List<String> received = new ArrayList<>();
    int ticks = 0;

    Stage01Probe(int id) { this.id = id; }

    @Override public int id() { return id; }

    @Override public List<Message> tick(int now) { ticks++; return List.of(); }

    @Override public List<Message> handle(Message m, int now) {
        received.add(now + ":" + m.kind() + ":" + m.from() + ":" + m.term());
        if (m.kind() == MessageKind.APPEND_ENTRIES) return List.of(Message.appendReply(id, m.from(), m.term(), true, 0));
        return List.of();
    }
}

public final class Stage01Test {
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

    /** Runs one scenario; an unexpected exception fails the scenario instead of killing the whole run. */
    static void scenario(String name, Runnable body) {
        try { body.run(); } catch (Throwable t) { failures.add(name + " blew up: " + t.getClass().getSimpleName() + ": " + t.getMessage()); }
    }

    static Storage logged(long... terms) {
        Storage s = new Storage();
        int i = 0;
        for (long t : terms) s.append(new LogEntry(t, "cmd" + (++i)));
        return s;
    }

    public static void main(String[] args) {
        scenario("fresh storage", () -> {
            Storage s = new Storage();
            check("fresh currentTerm is 0", s.currentTerm == 0);
            check("fresh votedFor is null (-1)", s.votedFor == -1);
            check("fresh lastIndex is 0", s.lastIndex() == 0);
            check("fresh lastTerm is 0", s.lastTerm() == 0);
            check("sentinel at index 0 has term 0", s.termAt(0) == 0);
            check("fresh describe is []", "[]".equals(s.describe()));
            check("fresh entriesFrom(1) is empty", s.entriesFrom(1).isEmpty());
            check("no writes yet", s.writes == 0);
        });

        scenario("save persists term and vote", () -> {
            Storage s = new Storage();
            s.save(3, 2);
            check("save sets currentTerm", s.currentTerm == 3);
            check("save sets votedFor", s.votedFor == 2);
            check("save counts one stable write", s.writes == 1);
            s.save(3, 2);
            check("re-saving the same pair still writes (it is a persist, not a cache)", s.writes == 2);
            s.save(4, -1);
            check("a new term clears votedFor when asked", s.currentTerm == 4 && s.votedFor == -1);
            checkThrows("term must never go backwards", IllegalArgumentException.class, () -> s.save(2, 0));
            check("a rejected save changes nothing", s.currentTerm == 4 && s.votedFor == -1 && s.writes == 3);
        });

        scenario("append and the 1-indexed log", () -> {
            Storage s = new Storage();
            s.append(new LogEntry(1, "PUT a 1"));
            s.append(new LogEntry(1, "PUT b 2"));
            s.append(new LogEntry(3, "NOOP"));
            check("lastIndex counts entries, not the sentinel", s.lastIndex() == 3);
            check("lastTerm is the last entry's term", s.lastTerm() == 3);
            check("termAt(1) is 1", s.termAt(1) == 1);
            check("termAt(3) is 3", s.termAt(3) == 3);
            check("each append is a stable write", s.writes == 3);
            check("describe lists entries from index 1", "[t1:PUT a 1, t1:PUT b 2, t3:NOOP]".equals(s.describe()));
            check("log list itself holds sentinel + 3", s.log.size() == 4 && s.log.get(0).term() == 0);
            checkThrows("terms in a log never decrease", IllegalArgumentException.class, () -> s.append(new LogEntry(2, "late")));
            check("a rejected append changes nothing", s.lastIndex() == 3 && s.writes == 3);
            s.append(new LogEntry(3, "PUT c 3"));
            check("same term may repeat", s.lastIndex() == 4 && s.lastTerm() == 3);
        });

        scenario("termAt bounds", () -> {
            Storage s = logged(1, 1);
            checkThrows("termAt beyond last index throws IndexOutOfBounds", IndexOutOfBoundsException.class, () -> s.termAt(3));
            checkThrows("termAt negative throws IndexOutOfBounds", IndexOutOfBoundsException.class, () -> s.termAt(-1));
        });

        scenario("truncate", () -> {
            Storage s = logged(1, 1, 2, 2, 2);
            int before = s.writes;
            s.truncate(3);
            check("truncate(3) removes index 3 and after", s.lastIndex() == 2 && s.lastTerm() == 1);
            check("truncate is one stable write", s.writes == before + 1);
            check("truncate keeps earlier entries intact", "[t1:cmd1, t1:cmd2]".equals(s.describe()));
            s.truncate(7);
            check("truncating past the end is a no-op", s.lastIndex() == 2 && s.writes == before + 1);
            s.truncate(1);
            check("truncate(1) empties the log but keeps the sentinel", s.lastIndex() == 0 && s.lastTerm() == 0 && s.log.size() == 1);
            checkThrows("the sentinel cannot be truncated", IllegalArgumentException.class, () -> s.truncate(0));
            s.append(new LogEntry(5, "after"));
            check("log is usable after a full truncate", s.lastIndex() == 1 && s.termAt(1) == 5);
        });

        scenario("entriesFrom", () -> {
            Storage s = logged(1, 2, 2);
            List<LogEntry> tail = s.entriesFrom(2);
            check("entriesFrom(2) returns indexes 2..3", tail.size() == 2 && tail.get(0).command().equals("cmd2") && tail.get(1).command().equals("cmd3"));
            check("entriesFrom(1) returns everything", s.entriesFrom(1).size() == 3);
            check("entriesFrom(lastIndex+1) is empty (a heartbeat)", s.entriesFrom(4).isEmpty());
            tail.clear();
            check("entriesFrom returns a copy, not a view", s.lastIndex() == 3);
            checkThrows("entriesFrom(0) would ship the sentinel", IndexOutOfBoundsException.class, () -> s.entriesFrom(0));
            checkThrows("entriesFrom beyond lastIndex+1 throws", IndexOutOfBoundsException.class, () -> s.entriesFrom(5));
        });

        scenario("up-to-date rule (section 5.4.1)", () -> {
            Storage s = logged(1, 2, 2);   // ends at (3, t2)
            check("higher last term wins even with a shorter log", s.upToDate(1, 3));
            check("same last term, longer log wins", s.upToDate(4, 2));
            check("same last term, same length is up-to-date", s.upToDate(3, 2));
            check("same last term, shorter log loses", !s.upToDate(2, 2));
            check("lower last term loses even with a longer log", !s.upToDate(10, 1));
            check("an empty log is up-to-date with another empty log", new Storage().upToDate(0, 0));
            check("an empty candidate is not up-to-date with a non-empty voter", !s.upToDate(0, 0));
        });

        scenario("Rng is SplitMix64 and deterministic", () -> {
            Rng a = new Rng(42), b = new Rng(42);
            check("same seed, same stream", a.nextLong() == b.nextLong() && a.nextInt(1000) == b.nextInt(1000) && a.nextDouble() == b.nextDouble());
            check("SplitMix64(1) first output", new Rng(1).nextLong() == 0x910A2DEC89025CC1L);
            Rng c = new Rng(42);
            check("Rng(42).nextInt(1000) stream is 451, 953, 371", c.nextInt(1000) == 451 && c.nextInt(1000) == 953 && c.nextInt(1000) == 371);
            Rng d = new Rng(9);
            boolean inRange = true;
            for (int i = 0; i < 1000; i++) { int v = d.nextInt(7); if (v < 0 || v >= 7) inRange = false; double x = d.nextDouble(); if (x < 0 || x >= 1) inRange = false; }
            check("nextInt stays in [0, bound) and nextDouble in [0, 1)", inRange);
        });

        scenario("Clock and Network scaffolding", () -> {
            Clock clock = new Clock();
            check("clock starts at 0", clock.now() == 0);
            Network net = new Network(clock, 7, 0.0, 5);
            Stage01Probe n0 = new Stage01Probe(0), n1 = new Stage01Probe(1);
            net.add(n0); net.add(n1);
            net.send(Message.appendEntries(0, 1, 1, 0, 0, List.of(), 0));
            net.send(Message.appendEntries(0, 1, 1, 0, 0, List.of(), 0));
            check("messages are in flight, not delivered synchronously", net.inFlight() == 2 && n1.received.isEmpty());
            for (int i = 0; i < 12; i++) net.tick();
            check("clock advanced one tick per tick()", clock.now() == 12);
            check("both messages reached n1 at seed-7's schedule (tick 3, in send order)", n1.received.equals(List.of("3:APPEND_ENTRIES:0:1", "3:APPEND_ENTRIES:0:1")));
            check("replies came back to n0 at ticks 4 and 6", n0.received.equals(List.of("4:APPEND_ENTRIES_REPLY:1:1", "6:APPEND_ENTRIES_REPLY:1:1")));
            check("every live node ticks once per tick", n0.ticks == 12 && n1.ticks == 12);
            check("counters: 4 sent, 4 delivered, 0 dropped", net.sent == 4 && net.delivered == 4 && net.dropped == 0);
            check("delay is at least one tick: nothing is delivered in the tick it is sent", n1.received.stream().noneMatch(r -> r.startsWith("0:")));

            Clock c2 = new Clock();
            Network lossy = new Network(c2, 7, 1.0, 5);
            Stage01Probe p0 = new Stage01Probe(0), p1 = new Stage01Probe(1);
            lossy.add(p0); lossy.add(p1);
            lossy.send(Message.requestVote(0, 1, 1, 0, 0));
            for (int i = 0; i < 10; i++) lossy.tick();
            check("dropRate 1.0 drops everything", lossy.dropped == 1 && p1.received.isEmpty());

            Clock c3 = new Clock();
            Network parted = new Network(c3, 7, 0.0, 5);
            Stage01Probe q0 = new Stage01Probe(0), q1 = new Stage01Probe(1), q2 = new Stage01Probe(2);
            parted.add(q0); parted.add(q1); parted.add(q2);
            parted.partition(Set.of(0));
            parted.send(Message.requestVote(0, 1, 1, 0, 0));
            parted.send(Message.requestVote(1, 2, 1, 0, 0));
            for (int i = 0; i < 10; i++) parted.tick();
            check("a partition drops messages across the cut and delivers the rest", q1.received.isEmpty() && q2.received.size() == 1);
            parted.heal();
            parted.send(Message.requestVote(0, 1, 1, 0, 0));
            for (int i = 0; i < 10; i++) parted.tick();
            check("after heal the cut is gone", q1.received.size() == 1);
            parted.crash(2);
            parted.send(Message.requestVote(1, 2, 2, 0, 0));
            for (int i = 0; i < 10; i++) parted.tick();
            check("a crashed node receives nothing and does not tick", q2.received.size() == 1 && parted.isDown(2) && q2.ticks == 20);
            Stage01Probe q2b = new Stage01Probe(2);
            parted.restart(q2b);
            parted.send(Message.requestVote(1, 2, 3, 0, 0));
            for (int i = 0; i < 10; i++) parted.tick();
            check("a restarted node is live again", !parted.isDown(2) && q2b.received.size() == 1 && q2b.ticks == 10);
        });

        scenario("Message helpers", () -> {
            Message rv = Message.requestVote(1, 2, 5, 7, 4);
            check("requestVote carries the candidate's last (index, term) in prevLogIndex/prevLogTerm", rv.kind() == MessageKind.REQUEST_VOTE && rv.prevLogIndex() == 7 && rv.prevLogTerm() == 4 && rv.term() == 5);
            Message ae = Message.appendEntries(0, 1, 2, 3, 1, List.of(new LogEntry(2, "PUT k v")), 3);
            check("appendEntries carries prev (index, term), entries, and leaderCommit", ae.prevLogIndex() == 3 && ae.prevLogTerm() == 1 && ae.entries().size() == 1 && ae.leaderCommit() == 3);
            checkThrows("Config rejects a heartbeat interval that is not well below the election timeout", IllegalArgumentException.class, () -> new Config(100, 200, 100));
            check("default config is 150-300 / 50", Config.DEFAULT.electionTimeoutMin() == 150 && Config.DEFAULT.electionTimeoutMax() == 300 && Config.DEFAULT.heartbeatInterval() == 50);
        });

        System.out.println("Stage01Test: " + passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL " + f);
        System.exit(failures.isEmpty() ? 0 : 1);
    }
}
