// Lesson: phases/03-systems-data-structures/09-merkle-trees/docs/en.md
// Merkle Trees and Content-Addressed Verification
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: RFC 6962 (Certificate Transparency: leaf/node domain separation, audit paths); FIPS 180-4 (SHA-256);
// CVE-2012-2459 (Bitcoin duplicate-last-node ambiguity). Java 21, standard library only.

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.HexFormat;
import java.util.List;
import java.util.Objects;

/** ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code must be three letters");
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4");
    }
}

/** Integer minor units plus a currency. Never a float. */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    @Override public String toString() {
        long scale = 1;
        for (int i = 0; i < currency.exponent(); i++) scale *= 10;
        long abs = Math.abs(minorUnits);
        String whole = Long.toString(abs / scale);
        if (currency.exponent() == 0) return (minorUnits < 0 ? "-" : "") + whole + " " + currency.code();
        String frac = Long.toString(abs % scale);
        while (frac.length() < currency.exponent()) frac = "0" + frac;
        return (minorUnits < 0 ? "-" : "") + whole + "." + frac + " " + currency.code();
    }
}

/**
 * One ledger posting. The leaf of the tree is the hash of {@link #canonicalBytes()}, not of toString():
 * a hash is only comparable across systems if both systems hash exactly the same byte sequence.
 */
record Posting(String id, String debitAccount, String creditAccount, Money amount) {
    byte[] canonicalBytes() {
        String canonical = id + "|" + debitAccount + "|" + creditAccount + "|"
                + amount.minorUnits() + "|" + amount.currency().code();
        return canonical.getBytes(StandardCharsets.UTF_8);
    }
}

/** One step of an inclusion proof: the sibling hash and which side the sibling sits on. */
record ProofStep(byte[] sibling, boolean siblingOnLeft) {
    @Override public boolean equals(Object o) {
        return o instanceof ProofStep p && siblingOnLeft == p.siblingOnLeft && Arrays.equals(sibling, p.sibling);
    }
    @Override public int hashCode() { return 31 * Arrays.hashCode(sibling) + (siblingOnLeft ? 1 : 0); }
    @Override public String toString() { return (siblingOnLeft ? "L:" : "R:") + Main.hex(sibling).substring(0, 8); }
}

/** Result of comparing two trees: which leaf indexes differ, and how many hash comparisons it took. */
record Diff(List<Integer> divergentLeaves, int comparisons) {}

/**
 * A binary hash tree with RFC 6962 domain separation: leaves are H(0x00 || data), internal nodes are
 * H(0x01 || left || right). An odd node at any level is carried up unchanged, never duplicated.
 */
final class MerkleTree {
    static final byte LEAF_PREFIX = 0x00;
    static final byte NODE_PREFIX = 0x01;

    /** levels.get(0) is the leaf hashes; levels.get(levels.size()-1) has exactly one entry, the root. */
    private final List<byte[][]> levels = new ArrayList<>();

    MerkleTree(List<byte[]> leafData) {
        if (leafData.isEmpty()) throw new IllegalArgumentException("a Merkle tree needs at least one leaf");
        byte[][] leaves = new byte[leafData.size()][];
        for (int i = 0; i < leafData.size(); i++) leaves[i] = hashLeaf(leafData.get(i));
        levels.add(leaves);
        byte[][] current = leaves;
        while (current.length > 1) {
            byte[][] next = new byte[(current.length + 1) / 2][];
            for (int i = 0; i < current.length; i += 2) {
                if (i + 1 < current.length) next[i / 2] = hashNode(current[i], current[i + 1]);
                else next[i / 2] = current[i];                 // odd node: carried up, not duplicated
            }
            levels.add(next);
            current = next;
        }
    }

    static MerkleTree ofPostings(List<Posting> postings) {
        List<byte[]> data = new ArrayList<>(postings.size());
        for (Posting p : postings) data.add(p.canonicalBytes());
        return new MerkleTree(data);
    }

    static byte[] hashLeaf(byte[] data) {
        MessageDigest md = sha256();
        md.update(LEAF_PREFIX);
        md.update(data);
        return md.digest();
    }

    static byte[] hashNode(byte[] left, byte[] right) {
        MessageDigest md = sha256();
        md.update(NODE_PREFIX);
        md.update(left);
        md.update(right);
        return md.digest();
    }

    private static MessageDigest sha256() {
        try {
            return MessageDigest.getInstance("SHA-256");
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException("SHA-256 is mandatory in every JRE", e);
        }
    }

    byte[] root() { return levels.get(levels.size() - 1)[0].clone(); }

    String rootHex() { return Main.hex(root()); }

    int leafCount() { return levels.get(0).length; }

    int height() { return levels.size(); }

    byte[] leafHash(int index) { return levels.get(0)[index].clone(); }

    /**
     * The audit path for leaf {@code index}: one sibling per level where the node has a sibling.
     * Levels where the node was carried up contribute no step, so proofs vary in length.
     */
    List<ProofStep> proof(int index) {
        Objects.checkIndex(index, leafCount());
        List<ProofStep> path = new ArrayList<>();
        int i = index;
        for (int level = 0; level < levels.size() - 1; level++) {
            byte[][] nodes = levels.get(level);
            int sibling = (i % 2 == 0) ? i + 1 : i - 1;
            if (sibling < nodes.length) path.add(new ProofStep(nodes[sibling].clone(), sibling < i));
            i /= 2;
        }
        return path;
    }

    /** Recompute the root from a leaf's data and its audit path; true iff it equals the expected root. */
    static boolean verify(byte[] leafData, List<ProofStep> proof, byte[] expectedRoot) {
        byte[] h = hashLeaf(leafData);
        for (ProofStep step : proof) {
            h = step.siblingOnLeft() ? hashNode(step.sibling(), h) : hashNode(h, step.sibling());
        }
        return MessageDigest.isEqual(h, expectedRoot);
    }

    /**
     * Find every leaf index where this tree and {@code other} disagree, descending only into subtrees
     * whose hashes differ. Both trees must have the same leaf count: the count is part of what the
     * two sides must agree on before a hash comparison means anything.
     */
    Diff diff(MerkleTree other) {
        if (other.leafCount() != leafCount()) {
            throw new IllegalArgumentException("cannot diff trees with " + leafCount() + " and " + other.leafCount() + " leaves");
        }
        List<Integer> out = new ArrayList<>();
        int[] comparisons = {0};
        diffNode(other, levels.size() - 1, 0, out, comparisons);
        return new Diff(out, comparisons[0]);
    }

    private void diffNode(MerkleTree other, int level, int index, List<Integer> out, int[] comparisons) {
        comparisons[0]++;
        if (Arrays.equals(levels.get(level)[index], other.levels.get(level)[index])) return;
        if (level == 0) { out.add(index); return; }
        int below = levels.get(level - 1).length;
        int left = 2 * index, right = 2 * index + 1;
        if (right >= below) {                             // this node was carried up: one child only
            diffNode(other, level - 1, left, out, comparisons);
            return;
        }
        diffNode(other, level - 1, left, out, comparisons);
        diffNode(other, level - 1, right, out, comparisons);
    }
}

public final class Main {
    static String hex(byte[] bytes) { return HexFormat.of().formatHex(bytes); }

    static List<Posting> samplePostings(int n) {
        List<Posting> postings = new ArrayList<>();
        String[] accounts = {"alice", "bob", "carol", "dave"};
        for (int i = 0; i < n; i++) {
            String from = accounts[i % accounts.length];
            String to = accounts[(i + 1) % accounts.length];
            long minor = 1250 + 37L * i;                   // deterministic, looks like real cents
            postings.add(new Posting(String.format("txn-%04d", i + 1), from, to, Money.ofMinor(minor, Currency.USD)));
        }
        return postings;
    }

    public static void main(String[] args) {
        System.out.println("== 1. Eight postings, one root ==");
        List<Posting> primary = samplePostings(8);
        MerkleTree primaryTree = MerkleTree.ofPostings(primary);
        for (Posting p : primary) {
            System.out.printf("  %s %s -> %s %s%n", p.id(), p.debitAccount(), p.creditAccount(), p.amount());
        }
        System.out.println("  root = " + primaryTree.rootHex());
        System.out.println("  height = " + primaryTree.height() + " levels for " + primaryTree.leafCount() + " leaves");

        System.out.println();
        System.out.println("== 2. The replica agrees: one 32-byte comparison instead of eight rows ==");
        MerkleTree replicaSame = MerkleTree.ofPostings(samplePostings(8));
        System.out.println("  roots equal = " + Arrays.equals(primaryTree.root(), replicaSame.root()));

        System.out.println();
        System.out.println("== 3. The replica is one cent off on txn-0006 ==");
        List<Posting> replica = new ArrayList<>(primary);
        Posting bad = replica.get(5);
        replica.set(5, new Posting(bad.id(), bad.debitAccount(), bad.creditAccount(),
                Money.ofMinor(bad.amount().minorUnits() + 1, Currency.USD)));
        MerkleTree replicaTree = MerkleTree.ofPostings(replica);
        System.out.println("  primary root = " + primaryTree.rootHex().substring(0, 16) + "...");
        System.out.println("  replica root = " + replicaTree.rootHex().substring(0, 16) + "...");
        Diff d = primaryTree.diff(replicaTree);
        System.out.println("  divergent leaves = " + d.divergentLeaves() + " (" + primary.get(5).id() + ")");
        System.out.println("  hash comparisons = " + d.comparisons() + " (a row-by-row compare would read all 8 rows on both sides)");
        System.out.println("  primary says " + primary.get(5).amount() + ", replica says " + replica.get(5).amount());

        System.out.println();
        System.out.println("== 4. Inclusion proof: txn-0003 is in the signed daily root ==");
        List<ProofStep> proof = primaryTree.proof(2);
        System.out.println("  proof length = " + proof.size() + " siblings " + proof);
        boolean ok = MerkleTree.verify(primary.get(2).canonicalBytes(), proof, primaryTree.root());
        System.out.println("  verify(txn-0003, proof, root) = " + ok);
        Posting forged = new Posting("txn-0003", "alice", "bob", Money.ofMinor(999_99, Currency.USD));
        System.out.println("  verify(forged txn-0003 with 999.99 USD) = " + MerkleTree.verify(forged.canonicalBytes(), proof, primaryTree.root()));

        System.out.println();
        System.out.println("== 5. Odd leaf counts: carry up, never duplicate ==");
        MerkleTree five = MerkleTree.ofPostings(samplePostings(5));
        System.out.println("  5 leaves: height " + five.height() + ", proof lengths "
                + five.proof(0).size() + "," + five.proof(3).size() + "," + five.proof(4).size()
                + " (leaf 4 is carried up twice)");
        List<Posting> six = new ArrayList<>(samplePostings(5));
        six.add(six.get(4));
        MerkleTree sixDup = MerkleTree.ofPostings(six);
        System.out.println("  [p0..p4] vs [p0..p4,p4] roots equal = " + Arrays.equals(five.root(), sixDup.root())
                + "  <- duplication would make this true (CVE-2012-2459)");

        System.out.println();
        System.out.println("== 6. Scale: 1,000,000 postings disagree on one row ==");
        int n = 1_000_000;
        int hashesPerSide = 0, levelSize = n;
        while (levelSize > 1) { hashesPerSide += levelSize / 2; levelSize = (levelSize + 1) / 2; }
        int height = 1, l = n;
        while (l > 1) { l = (l + 1) / 2; height++; }
        System.out.println("  tree height = " + height + " levels, internal hashes to build = " + hashesPerSide);
        System.out.println("  hashes exchanged to locate one bad row ~ 2 per level = " + (2 * (height - 1)) + ", versus " + n + " rows shipped for a full compare");
    }
}
