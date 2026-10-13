// Tests for phases/06-replication-and-consensus/10-chain-and-primary-backup. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeMap;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    /** Every update any client got a reply for must be in the tail's history, and every replica in the chain must agree on balances. */
    static boolean committedPreserved(ChainCluster c) {
        Set<String> inTail = new HashSet<>();
        for (Update u : c.tail().history) inTail.add(u.request().key());
        Set<String> knownUpdates = new HashSet<>();
        for (ChainNode n : c.nodes.values()) for (Update u : n.history) knownUpdates.add(u.request().key());
        for (ChainClient cl : c.clients.values()) {
            for (String k : cl.replies.keySet()) if (knownUpdates.contains(k) && !inTail.contains(k)) return false;   // a replied update is missing from the tail
        }
        String first = null;
        for (int id : c.master.chain) {
            String b = c.node(id).balances.toString();
            if (first == null) first = b; else if (!first.equals(b)) return false;
        }
        return true;
    }

    public static void main(String[] args) {
        // ---- money sanity (Phase 10 discipline)
        check("Money is integer minor units", Money.ofMinor(1_999, Currency.USD).plus(Money.ofMinor(1, Currency.USD)).minorUnits() == 2_000);
        check("Money prints canonically", Money.ofMinor(-7, Currency.USD).toString().equals("-0.07 USD"));

        // ---- happy path: updates at the head, replies from the tail, reads at the tail
        ChainCluster c = new ChainCluster(1, 3, 3, 1, 2);
        Request u1 = c.update(0, "alice", 10_000);
        Request u2 = c.update(0, "alice", -2_550);
        check("cluster settles", c.runUntilQuiet(500, 60) > 0);
        check("update replies carry the balance after the update", c.client(0).replyFor(u1).equals(Money.ofMinor(10_000, Currency.USD)) && c.client(0).replyFor(u2).equals(Money.ofMinor(7_450, Currency.USD)));
        check("every replica applied both updates in order", c.node(0).history.size() == 2 && c.node(2).history.get(1).request().equals(u2));
        check("head assigns increasing sequence numbers", c.node(2).history.get(0).seq() == 1 && c.node(2).history.get(1).seq() == 2);
        check("Sent is empty once the tail has acked", c.node(0).sent.isEmpty() && c.node(1).sent.isEmpty());
        Request q = c.query(1, "alice");
        c.runUntilQuiet(500, 60);
        check("tail answers queries with committed state", c.client(1).replyFor(q).equals(Money.ofMinor(7_450, Currency.USD)));
        check("only the tail handled the query", c.node(2).queriesHandled == 1 && c.node(0).queriesHandled == 0);
        check("no invariant violations", c.violations.isEmpty() && c.checks > 0);

        // ---- strong consistency: an update applied at the head but not at the tail is invisible to readers
        ChainCluster v = new ChainCluster(2, 8, 3, 0, 2);
        Request w = v.update(0, "bob", 5_000);
        while (v.head().history.isEmpty()) v.tick();
        check("head has applied, tail has not, writer has no reply", v.head().history.size() == 1 && v.tail().history.isEmpty() && !v.client(0).hasReply(w));
        Request r = v.query(1, "bob");
        v.runUntilQuiet(500, 100);
        check("a read issued before the write committed does not see it", v.client(1).replyFor(r).equals(Money.ofMinor(0, Currency.USD)));
        Request r2 = v.query(1, "bob");
        v.runUntilQuiet(500, 100);
        check("a read issued after the write's reply sees it", v.client(1).replyFor(r2).equals(Money.ofMinor(5_000, Currency.USD)));

        // ---- a stale client that queries a non-tail is redirected, never answered with uncommitted state
        ChainCluster st = new ChainCluster(4, 2, 3, 0, 1);
        st.update(0, "carol", 100);
        st.runUntilQuiet(500, 60);
        List<Message> out = new ArrayList<>();
        Request stale = st.client(0).query("carol", st.now(), out);
        st.network.send(Message.query(100, st.master.head(), stale));            // deliberately ask the head
        st.runUntilQuiet(500, 60);
        check("non-tail replies NOT_TAIL and the client re-asks the real tail", st.client(0).notTailRedirects == 1 && st.client(0).replyFor(stale).equals(Money.ofMinor(100, Currency.USD)));

        // ---- duplicate suppression: a retried update is applied once and answered from the cache
        ChainCluster d = new ChainCluster(5, 2, 3, 0, 1);
        Request dup = d.update(0, "dave", 300);
        d.runUntilQuiet(500, 60);
        d.network.send(Message.update(100, d.master.head(), new Update(0, dup)));  // the client's retry arrives after commit
        d.run(30);
        check("retry after commit is not re-applied", d.tail().history.size() == 1 && d.head().history.size() == 1);
        check("balance unchanged by the retry", d.tail().balance("dave").equals(Money.ofMinor(300, Currency.USD)));

        // ---- middle failure with updates in flight: predecessor resends Sent to the new successor
        ChainCluster f = new ChainCluster(3, 3, 4, 2, 3);
        for (int i = 0; i < 6; i++) f.update(i % 3, "acct" + (i % 2), 1_000 + i);
        f.runUntilQuiet(500, 60);
        for (int i = 0; i < 4; i++) f.update(i % 3, "acct0", 100);
        f.run(2);
        int middle = f.master.chain.get(1);
        f.crashAndDetect(middle, 20);
        check("middle failure settles", f.runUntilQuiet(1000, 60) > 0);
        check("chain shrank to three", f.master.chain.size() == 3 && !f.master.chain.contains(middle));
        check("all ten updates committed after the middle failure", f.tail().history.size() == 10 && committedPreserved(f));
        check("the invariants held through the repair", f.violations.isEmpty());

        // ---- tail failure: the predecessor becomes tail and replies for everything it had in flight
        for (int i = 0; i < 3; i++) f.update(i % 3, "acct1", 7);
        f.run(1);
        int oldTail = f.master.tail();
        f.crashAndDetect(oldTail, 20);
        check("tail failure settles", f.runUntilQuiet(1000, 60) > 0);
        check("new tail has every update and an empty Sent", f.tail().history.size() == 13 && f.tail().sent.isEmpty() && committedPreserved(f));
        check("every client got a reply for the in-flight updates", f.clients.values().stream().allMatch(cl -> cl.pending.isEmpty()));

        // ---- head failure: unforwarded requests are lost and clients retry them against the new head
        for (int i = 0; i < 3; i++) f.update(i % 3, "acct0", 3);
        f.run(1);
        int oldHead = f.master.head();
        f.crashAndDetect(oldHead, 20);
        check("head failure settles", f.runUntilQuiet(1000, 60) > 0);
        check("new head is the old second node", f.master.head() != oldHead);
        check("retries recovered the lost requests: 16 updates committed", f.tail().history.size() == 16 && committedPreserved(f));
        check("no committed update was lost across three failures", f.violations.isEmpty());

        // ---- extending the chain: the old tail keeps serving while the history is copied
        f.master.extend(4);
        f.run(1);
        check("old tail acts as tail during the transfer", f.node(f.master.chain.get(f.master.chain.size() - 2)).transferPending);
        f.runUntilQuiet(500, 60);
        check("transfer complete: handover done", !f.node(f.master.chain.get(f.master.chain.size() - 2)).transferPending && f.master.tail() == 4);
        check("new tail has the full history", f.node(4).history.size() == 16 && f.node(4).balances.equals(f.head().balances));
        f.update(0, "acct0", 1);
        f.runUntilQuiet(500, 60);
        check("new tail replies for new updates", f.node(4).history.size() == 17 && f.client(0).pending.isEmpty());
        check("invariants held through the extension", f.violations.isEmpty());

        // ---- load distribution
        ChainCluster lc = new ChainCluster(5, 2, 3, 0, 4);
        for (int i = 0; i < 50; i++) lc.update(i % 4, "a" + (i % 5), 100);
        for (int i = 0; i < 300; i++) lc.query(i % 4, "a" + (i % 5));
        lc.runUntilQuiet(20_000, 200);
        check("head handles updates, tail handles queries, nobody else sees queries", lc.head().updatesHandled == 50 && lc.tail().queriesHandled == 300 && lc.head().queriesHandled == 0 && lc.node(1).queriesHandled == 0);

        // ---- primary-backup: primary serves both; promotion re-syncs backups
        Network pbNet = new Network(9, 2);
        Map<Integer, PrimaryBackupNode> pbNodes = new TreeMap<>();
        for (int i = 0; i < 3; i++) { PrimaryBackupNode n = new PrimaryBackupNode(i); pbNodes.put(i, n); pbNet.add(n); }
        PrimaryBackupMaster pbMaster = new PrimaryBackupMaster(pbNet, pbNodes, List.of(0, 1, 2));
        Master facade = new Master(pbNet, List.of(0));
        ChainClient pc = new ChainClient(100, facade);
        pbNet.add(pc);
        for (int t = 0; t < 10; t++) pbNet.tick();
        List<Message> pbOut = new ArrayList<>();
        Request p1 = pc.update("erin", Money.ofMinor(2_000, Currency.USD), pbNet.now(), pbOut);
        Request p2 = pc.query("erin", pbNet.now(), pbOut);
        pbNet.sendAll(pbOut);
        while (!pbNet.idle()) pbNet.tick();
        check("primary replies after all backups ack", pc.replyFor(p1).equals(Money.ofMinor(2_000, Currency.USD)) && pbNodes.get(1).history.size() == 1 && pbNodes.get(2).history.size() == 1);
        check("primary handles both updates and queries", pbNodes.get(0).updatesHandled == 1 && pbNodes.get(0).queriesHandled == 1 && pc.hasReply(p2));
        // crash the primary with an update half-replicated: backup 1 got it, backup 2 did not
        PrimaryBackupNode prim = pbNodes.get(0);
        Update half = new Update(2, new Request(100, 99, false, "erin", Money.ofMinor(1, Currency.USD)));
        pbNodes.get(1).handle(Message.pbUpdate(0, 1, half), pbNet.now());
        pbNet.crash(0);
        pbMaster.fail(0);
        facade.chain.set(0, 1);
        while (!pbNet.idle()) pbNet.tick();
        check("promotion re-syncs backups from the new primary's history", pbNodes.get(1).isPrimary && pbNodes.get(2).history.size() == 2 && pbNodes.get(2).balances.equals(pbNodes.get(1).balances));

        // ---- across seeds: random failures, invariants hold, committed updates are never lost, replicas agree
        int clean = 0;
        for (long seed = 1; seed <= 10; seed++) {
            ChainCluster r1 = new ChainCluster(seed, 4, 4, 1, 3);
            for (int i = 0; i < 8; i++) r1.update(i % 3, "x" + (i % 3), 10 + i);
            r1.run(3);
            r1.crashAndDetect(r1.master.chain.get((int) (seed % 4)), 15);
            for (int i = 0; i < 6; i++) { r1.update(i % 3, "x" + (i % 3), 5); r1.query(i % 3, "x0"); }
            r1.run(2);
            r1.crashAndDetect(r1.master.chain.get((int) (seed % 3)), 15);
            r1.master.extend(4);
            for (int i = 0; i < 4; i++) r1.update(i % 3, "x1", 1);
            boolean settled = r1.runUntilQuiet(5000, 60) > 0;
            if (settled && r1.violations.isEmpty() && committedPreserved(r1)) clean++;
        }
        check("10/10 seeds: invariants hold and no committed update is lost", clean == 10);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(x -> System.out.println("FAIL: " + x));
        if (!failures.isEmpty()) System.exit(1);
    }
}
