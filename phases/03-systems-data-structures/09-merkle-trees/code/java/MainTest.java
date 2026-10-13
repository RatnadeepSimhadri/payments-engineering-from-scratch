// Tests for phases/03-systems-data-structures/09-merkle-trees. Run: javac -d .out *.java && java -cp .out MainTest
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;

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

    static byte[] b(String s) { return s.getBytes(StandardCharsets.UTF_8); }

    static List<byte[]> leaves(String... items) {
        List<byte[]> out = new ArrayList<>();
        for (String s : items) out.add(b(s));
        return out;
    }

    public static void main(String[] args) {
        // Hashing primitives and domain separation
        check("leaf hash is SHA-256 of 0x00||data (32 bytes)", MerkleTree.hashLeaf(b("a")).length == 32);
        check("leaf hash differs from raw sha256 of data", !Arrays.equals(MerkleTree.hashLeaf(b("a")), MerkleTree.hashNode(b(""), b("a"))));
        check("leaf and node prefixes separate domains",
                !Arrays.equals(MerkleTree.hashLeaf(b("xy")), MerkleTree.hashNode(b("x"), b("y"))));
        check("node hash is order-sensitive",
                !Arrays.equals(MerkleTree.hashNode(b("a"), b("b")), MerkleTree.hashNode(b("b"), b("a"))));

        // Deterministic structure
        MerkleTree one = new MerkleTree(leaves("only"));
        check("single leaf: root equals leaf hash", Arrays.equals(one.root(), MerkleTree.hashLeaf(b("only"))));
        check("single leaf: height 1, empty proof", one.height() == 1 && one.proof(0).isEmpty());
        check("single leaf: empty proof verifies", MerkleTree.verify(b("only"), one.proof(0), one.root()));

        MerkleTree two = new MerkleTree(leaves("a", "b"));
        check("two leaves: root is H(1||Ha||Hb)",
                Arrays.equals(two.root(), MerkleTree.hashNode(MerkleTree.hashLeaf(b("a")), MerkleTree.hashLeaf(b("b")))));

        MerkleTree eight = new MerkleTree(leaves("p0", "p1", "p2", "p3", "p4", "p5", "p6", "p7"));
        check("eight leaves: height 4", eight.height() == 4);
        check("same leaves twice give same root", Arrays.equals(eight.root(), new MerkleTree(leaves("p0", "p1", "p2", "p3", "p4", "p5", "p6", "p7")).root()));
        check("order matters: swapped leaves change the root",
                !Arrays.equals(eight.root(), new MerkleTree(leaves("p1", "p0", "p2", "p3", "p4", "p5", "p6", "p7")).root()));
        check("root() returns a defensive copy", eight.root() != eight.root());
        checkThrows("empty tree is rejected", IllegalArgumentException.class, () -> new MerkleTree(new ArrayList<>()));

        // Inclusion proofs
        for (int i = 0; i < 8; i++) {
            List<ProofStep> proof = eight.proof(i);
            check("eight leaves: proof(" + i + ") has 3 siblings", proof.size() == 3);
            check("eight leaves: proof(" + i + ") verifies", MerkleTree.verify(b("p" + i), proof, eight.root()));
        }
        check("proof(0) first sibling is on the right", !eight.proof(0).get(0).siblingOnLeft());
        check("proof(1) first sibling is on the left", eight.proof(1).get(0).siblingOnLeft());
        check("proof fails for wrong leaf data", !MerkleTree.verify(b("p9"), eight.proof(2), eight.root()));
        check("proof fails against wrong root", !MerkleTree.verify(b("p2"), eight.proof(2), two.root()));
        check("proof for leaf 2 does not verify leaf 3", !MerkleTree.verify(b("p3"), eight.proof(2), eight.root()));
        List<ProofStep> tampered = new ArrayList<>(eight.proof(2));
        tampered.set(1, new ProofStep(tampered.get(1).sibling(), !tampered.get(1).siblingOnLeft()));
        check("flipping a side bit breaks the proof", !MerkleTree.verify(b("p2"), tampered, eight.root()));
        checkThrows("proof index out of range", IndexOutOfBoundsException.class, () -> eight.proof(8));

        // Odd counts: carry up, never duplicate
        MerkleTree five = new MerkleTree(leaves("p0", "p1", "p2", "p3", "p4"));
        check("five leaves: height 4", five.height() == 4);
        check("five leaves: proof(4) has one sibling (carried up twice)", five.proof(4).size() == 1);
        check("five leaves: proof(0) has three siblings", five.proof(0).size() == 3);
        for (int i = 0; i < 5; i++) check("five leaves: proof(" + i + ") verifies", MerkleTree.verify(b("p" + i), five.proof(i), five.root()));
        MerkleTree sixDup = new MerkleTree(leaves("p0", "p1", "p2", "p3", "p4", "p4"));
        check("[p0..p4] and [p0..p4,p4] have different roots (no CVE-2012-2459 ambiguity)",
                !Arrays.equals(five.root(), sixDup.root()));
        check("leafCount reflects the input", five.leafCount() == 5 && sixDup.leafCount() == 6);

        // Diff
        check("identical trees: no divergence, one comparison", eight.diff(new MerkleTree(leaves("p0", "p1", "p2", "p3", "p4", "p5", "p6", "p7"))).equals(new Diff(List.of(), 1)));
        Diff d5 = eight.diff(new MerkleTree(leaves("p0", "p1", "p2", "p3", "p4", "X", "p6", "p7")));
        check("one bad leaf found at index 5", d5.divergentLeaves().equals(List.of(5)));
        check("one bad leaf costs 1 + 2*3 = 7 comparisons", d5.comparisons() == 7);
        Diff d2 = eight.diff(new MerkleTree(leaves("p0", "X", "p2", "p3", "p4", "p5", "p6", "Y")));
        check("two bad leaves in different halves, ascending order", d2.divergentLeaves().equals(List.of(1, 7)));
        check("two bad leaves in different halves cost 1 + 2*5 = 11 comparisons", d2.comparisons() == 11);
        Diff d45 = five.diff(new MerkleTree(leaves("p0", "p1", "p2", "p3", "Z")));
        check("odd tree: divergent carried-up leaf is found", d45.divergentLeaves().equals(List.of(4)));
        check("odd tree: root, two children, then one per carried level = 5 comparisons", d45.comparisons() == 5);
        checkThrows("diff requires equal leaf counts", IllegalArgumentException.class, () -> eight.diff(five));

        // Postings and canonical bytes
        Posting p = new Posting("txn-0001", "alice", "bob", Money.ofMinor(1250, Currency.USD));
        check("canonical bytes use minor units, not a formatted amount",
                new String(p.canonicalBytes(), StandardCharsets.UTF_8).equals("txn-0001|alice|bob|1250|USD"));
        Posting offByOneCent = new Posting("txn-0001", "alice", "bob", Money.ofMinor(1251, Currency.USD));
        check("one cent changes the leaf hash", !Arrays.equals(MerkleTree.hashLeaf(p.canonicalBytes()), MerkleTree.hashLeaf(offByOneCent.canonicalBytes())));
        check("Money toString pads minor units", Money.ofMinor(1205, Currency.USD).toString().equals("12.05 USD"));
        check("Money toString negative", Money.ofMinor(-7, Currency.USD).toString().equals("-0.07 USD"));
        MerkleTree primary = MerkleTree.ofPostings(Main.samplePostings(8));
        List<Posting> replica = new ArrayList<>(Main.samplePostings(8));
        replica.set(5, offByOneCent);
        check("postings diff locates the off-by-one-cent row", primary.diff(MerkleTree.ofPostings(replica)).divergentLeaves().equals(List.of(5)));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
