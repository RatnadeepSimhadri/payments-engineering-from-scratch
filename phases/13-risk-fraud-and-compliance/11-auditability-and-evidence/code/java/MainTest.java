// Tests for phases/13-risk-fraud-and-compliance/11-auditability-and-evidence (see docs/en.md).
// Run: javac -d .out *.java && java -cp .out MainTest
// Self-contained harness: counts failures and exits 1 if any check fails.
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Base64;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;

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

    static List<byte[]> leaves(int n) {
        List<byte[]> out = new ArrayList<>();
        for (int i = 0; i < n; i++) out.add(("leaf-" + i).getBytes(StandardCharsets.UTF_8));
        return out;
    }

    public static void main(String[] args) {
        // --- canonical encoding ---
        check("netstrings are injective: (ab,c) != (a,bc)",
                !java.util.Arrays.equals(Crypto.netstrings("ab", "c"), Crypto.netstrings("a", "bc")));
        check("netstring format", new String(Crypto.netstrings("hello", ""), StandardCharsets.UTF_8).equals("5:hello,0:,"));
        check("netstrings count UTF-8 bytes, not chars", new String(Crypto.netstrings("€"), StandardCharsets.UTF_8).startsWith("3:"));
        check("netstrings round-trip", Crypto.parseNetstrings(Crypto.netstrings("a", "b:c,", "")).equals(List.of("a", "b:c,", "")));
        checkThrows("truncated netstring rejected", IllegalArgumentException.class, () -> Crypto.parseNetstrings("5:abc,".getBytes()));
        check("SHA-256 known answer (empty string)", Crypto.HEX.formatHex(Crypto.sha256())
                .equals("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"));

        Entry a = Entry.create(0, 1000, Crypto.ZERO_HASH, "risk.decision", "pay_1", Map.of("outcome", "APPROVE", "score", "12"));
        Entry b = Entry.create(0, 1000, Crypto.ZERO_HASH, "risk.decision", "pay_1", new TreeMap<>(Map.of("score", "12", "outcome", "APPROVE")));
        check("payload key order does not change the hash", a.hash().equals(b.hash()));
        check("entry round-trips through canonical bytes", Entry.fromCanonical(a.canonical()).equals(a));
        check("hash covers the payload", !a.hash().equals(Entry.create(0, 1000, Crypto.ZERO_HASH, "risk.decision", "pay_1",
                Map.of("outcome", "DECLINE", "score", "12")).hash()));
        check("hash covers the timestamp", !a.hash().equals(Entry.create(0, 1001, Crypto.ZERO_HASH, "risk.decision", "pay_1",
                Map.of("outcome", "APPROVE", "score", "12")).hash()));

        // --- Merkle tree (RFC 6962) ---
        check("empty tree root is SHA-256 of nothing", java.util.Arrays.equals(Merkle.root(List.of()), Crypto.sha256()));
        byte[] l0 = "x".getBytes();
        check("one-leaf root is the leaf hash", java.util.Arrays.equals(Merkle.root(List.of(l0)), Merkle.leafHash(l0)));
        byte[] concat = new byte[64];
        check("leaf and node hashes are domain-separated",
                !java.util.Arrays.equals(Merkle.leafHash(concat), Merkle.nodeHash(new byte[32], new byte[32])));
        check("split(5) = 4, split(8) = 4, split(2) = 1", Merkle.split(5) == 4 && Merkle.split(8) == 4 && Merkle.split(2) == 1);
        List<byte[]> three = leaves(3);
        byte[] manual = Merkle.nodeHash(Merkle.nodeHash(Merkle.leafHash(three.get(0)), Merkle.leafHash(three.get(1))), Merkle.leafHash(three.get(2)));
        check("3-leaf root = H(1|H(1|L0|L1)|L2)", java.util.Arrays.equals(Merkle.root(three), manual));
        boolean allProofs = true;
        int pathLenAt7 = -1;
        for (int n = 1; n <= 17; n++) {
            List<byte[]> ls = leaves(n);
            byte[] root = Merkle.root(ls);
            for (int m = 0; m < n; m++) {
                List<byte[]> path = Merkle.auditPath(ls, m);
                allProofs &= Merkle.verifyInclusion(ls.get(m), m, n, path, root);
                if (n == 16 && m == 7) pathLenAt7 = path.size();
            }
        }
        check("every leaf of every tree size 1..17 proves inclusion", allProofs);
        check("audit path in a 16-leaf tree has log2(16) = 4 hashes", pathLenAt7 == 4);
        List<byte[]> nine = leaves(9);
        byte[] root9 = Merkle.root(nine);
        check("proof for leaf 3 fails at index 4", !Merkle.verifyInclusion(nine.get(3), 4, 9, Merkle.auditPath(nine, 3), root9));
        check("proof fails for different leaf data", !Merkle.verifyInclusion("evil".getBytes(), 3, 9, Merkle.auditPath(nine, 3), root9));
        check("proof fails against a tree size of a different shape", !Merkle.verifyInclusion(nine.get(3), 3, 17, Merkle.auditPath(nine, 3), root9));
        check("index outside the tree is rejected", !Merkle.verifyInclusion(nine.get(3), 9, 9, List.of(), root9));

        // --- the log ---
        ManualClock clock = new ManualClock(Main.T0);
        DecisionLog log = Main.buildDemoLog(clock, Main.demoTsa(clock));
        List<Entry> entries = log.entries();
        String key = log.tsaPublicKey();
        check("demo log has 9 entries and 2 checkpoints", entries.size() == 9 && log.checkpoints().size() == 2);
        check("genesis links to the zero hash", entries.get(0).prevHash().equals(Crypto.ZERO_HASH));
        check("each entry links to its predecessor", entries.get(6).prevHash().equals(entries.get(5).hash()));
        check("log is deterministic across runs", Main.buildDemoLog(new ManualClock(Main.T0), Main.demoTsa(new ManualClock(Main.T0)))
                .entries().get(8).hash().equals(entries.get(8).hash()));
        check("entries() is a read-only copy", isUnmodifiable(log.entries()));
        check("untouched log verifies", Verifier.verifyLog(entries, log.checkpoints(), key).ok());

        clock.set(Main.T0 + 5_000);
        checkThrows("backdated append rejected", IllegalStateException.class,
                () -> log.append("risk.decision", "pay_x", Map.of("outcome", "APPROVE")));
        check("rejected append left the log unchanged", log.entries().size() == 9);
        checkThrows("sealing an empty log rejected", IllegalStateException.class,
                () -> new DecisionLog(new ManualClock(0), Main.demoTsa(new ManualClock(0))).seal());

        // --- tamper detection ---
        List<Entry> edited = new ArrayList<>(entries);
        edited.set(3, edited.get(3).withPayloadKeepingHash("outcome", "APPROVE"));
        Report r1 = Verifier.verifyLog(edited, log.checkpoints(), key);
        check("edit without rehash detected at #3", !r1.ok() && r1.firstBadSeq() == 3);

        List<Entry> rehashed = new ArrayList<>(entries);
        Entry e3 = entries.get(3);
        Map<String, String> p3 = new TreeMap<>(e3.payload());
        p3.put("outcome", "APPROVE");
        rehashed.set(3, Entry.create(3, e3.timestampMillis(), e3.prevHash(), e3.kind(), e3.subject(), p3));
        Report r2 = Verifier.verifyLog(rehashed, log.checkpoints(), key);
        check("edit with rehash breaks the link at #4", !r2.ok() && r2.firstBadSeq() == 3
                && r2.problems().stream().anyMatch(s -> s.contains("#4 does not link")));

        List<Entry> rewritten = new ArrayList<>(rehashed.subList(0, 4));
        for (int i = 4; i < entries.size(); i++) {
            Entry o = entries.get(i);
            rewritten.add(Entry.create(o.seq(), o.timestampMillis(), rewritten.get(i - 1).hash(), o.kind(), o.subject(), o.payload()));
        }
        Report noAnchors = Verifier.verifyLog(rewritten, List.of(), key);
        check("a fully rewritten chain is internally consistent", noAnchors.ok());
        Report r3 = Verifier.verifyLog(rewritten, log.checkpoints(), key);
        check("the external seal catches the rewrite", !r3.ok() && r3.problems().stream().anyMatch(s -> s.contains("Merkle root")));

        List<Entry> deleted = new ArrayList<>(entries);
        deleted.remove(5);
        Report r4 = Verifier.verifyLog(deleted, log.checkpoints(), key);
        check("deleting #5 is detected at position 5", !r4.ok() && r4.firstBadSeq() == 5);

        Report r5 = Verifier.verifyLog(entries.subList(0, 7), log.checkpoints(), key);
        check("truncating the tail is detected", !r5.ok() && r5.problems().stream().anyMatch(s -> s.contains("truncated")));

        List<Entry> swapped = new ArrayList<>(entries);
        swapped.set(1, entries.get(2));
        swapped.set(2, entries.get(1));
        check("reordering is detected", !Verifier.verifyLog(swapped, log.checkpoints(), key).ok());

        Checkpoint cp = log.checkpoints().get(0);
        Checkpoint forgedTime = new Checkpoint(cp.size(), cp.merkleRootHex(), cp.headHashHex(),
                new TimestampToken(cp.token().digestHex(), cp.token().timeMillis() - 3_600_000, cp.token().signatureBase64()));
        check("moving a seal's time breaks its signature", !Verifier.verifyLog(entries, List.of(forgedTime), key).ok());
        TimestampAuthority impostor = new TimestampAuthority(999L, () -> Main.T0);
        check("a seal from a different authority is rejected", !Verifier.verifyLog(entries, log.checkpoints(), impostor.publicKeyBase64()).ok());

        // --- evidence bundles ---
        Checkpoint cp2 = log.checkpoints().get(1);
        String bundle = EvidenceBundle.export(log, "pay_0002", cp2);
        check("bundle discloses exactly the subject's 2 entries", bundle.lines().filter(l -> l.startsWith("entry ")).count() == 2);
        check("bundle does not leak other subjects", !bundle.contains(Base64.getEncoder().encodeToString(entries.get(3).canonical())));
        check("bundle verifies with the trusted key", Verifier.verifyBundle(bundle, key).ok());
        check("bundle export is deterministic", bundle.equals(EvidenceBundle.export(log, "pay_0002", cp2)));
        check("bundle for the last entry checks the head", Verifier.verifyBundle(EvidenceBundle.export(log, "pay_0005", cp2), key).ok());

        String changedEntry = bundle.replace(Base64.getEncoder().encodeToString(entries.get(5).canonical()),
                Base64.getEncoder().encodeToString(Entry.create(5, entries.get(5).timestampMillis(), entries.get(5).prevHash(),
                        "review.outcome", "pay_0002", Map.of("result", "DECLINE", "analyst", "analyst-31", "minutes", "4")).canonical()));
        check("edited entry in a bundle fails", !Verifier.verifyBundle(changedEntry, key).ok());
        String otherRoot = bundle.replace(cp2.merkleRootHex(), log.checkpoints().get(0).merkleRootHex());
        check("swapped Merkle root fails the seal", !Verifier.verifyBundle(otherRoot, key).ok());
        String otherKey = bundle.replace(key, impostor.publicKeyBase64());
        check("bundle naming an untrusted key fails", !Verifier.verifyBundle(otherKey, key).ok());
        String dropped = bundle.lines().filter(l -> !l.startsWith("proof 5")).reduce("", (x, y) -> x + y + "\n");
        check("entry without a proof fails", !Verifier.verifyBundle(dropped, key).ok());
        check("garbage is unreadable, not accepted", !Verifier.verifyBundle("hello", key).ok());
        check("describe decodes the review outcome", Verifier.describe(bundle).get(1).contains("result=APPROVE"));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }

    static boolean isUnmodifiable(List<Entry> list) {
        try {
            list.add(null);
            return false;
        } catch (UnsupportedOperationException | NullPointerException e) {
            return true;
        }
    }
}
