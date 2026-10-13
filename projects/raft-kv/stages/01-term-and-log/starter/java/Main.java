// Project: projects/raft-kv — Raft-replicated key-value store on a deterministic simulated network.
// Run:   javac -d .out *.java && java -cp .out Main
// Grade: python3 scripts/project_test.py raft-kv --lang java --stage 1 --path <this workspace>
// This starter main only exercises the PROVIDED scaffolding (Sim.java). Replace it with your own demo as the stages
// land: the solution's Main runs an election, replicates client writes, and checks the recorded history.
// Java 21, standard library only. Python and Go ports follow later.

import java.util.ArrayList;
import java.util.List;

public final class Main {
    /** A trivial participant: replies to every APPEND_ENTRIES with a reply carrying the same term, sends nothing on tick. */
    static final class Echo implements Node {
        final int id;
        final List<String> seen = new ArrayList<>();

        Echo(int id) { this.id = id; }

        @Override public int id() { return id; }

        @Override public List<Message> tick(int now) { return List.of(); }

        @Override public List<Message> handle(Message m, int now) {
            seen.add("t=" + now + " n" + id + " got " + m.kind() + " term " + m.term() + " from n" + m.from());
            if (m.kind() == MessageKind.APPEND_ENTRIES) return List.of(Message.appendReply(id, m.from(), m.term(), true, 0));
            return List.of();
        }
    }

    public static void main(String[] args) {
        System.out.println("== The seeded Rng: the same seed gives the same stream, forever ==");
        Rng a = new Rng(42), b = new Rng(42);
        for (int i = 0; i < 3; i++) System.out.printf("draw %d: %d == %d%n", i, a.nextInt(1000), b.nextInt(1000));

        System.out.println();
        System.out.println("== The simulated network: delivery order is (deliverAt, sequence); nothing depends on wall-clock time ==");
        Clock clock = new Clock();
        Network net = new Network(clock, 7, 0.0, 5);
        Echo n0 = new Echo(0), n1 = new Echo(1);
        net.add(n0);
        net.add(n1);
        net.send(Message.appendEntries(0, 1, 1, 0, 0, List.of(), 0));
        net.send(Message.appendEntries(0, 1, 1, 0, 0, List.of(), 0));
        for (int i = 0; i < 12; i++) net.tick();
        for (String s : n1.seen) System.out.println(s);
        for (String s : n0.seen) System.out.println(s);
        System.out.printf("sent=%d delivered=%d dropped=%d in-flight=%d at tick %d%n", net.sent, net.delivered, net.dropped, net.inFlight(), clock.now());

        System.out.println();
        System.out.println("Stage 1: implement Storage.java, then run the grader:");
        System.out.println("  python3 scripts/project_test.py raft-kv --lang java --stage 1 --path <workspace>");
    }
}
