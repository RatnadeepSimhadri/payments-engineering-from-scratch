// Verifier for phases/13-risk-fraud-and-compliance/11-auditability-and-evidence (see docs/en.md).
// The program a regulator or auditor runs. It trusts nothing in the log except the time-stamp authority's public key.
// Run: javac -d .out *.java && java -cp .out Verifier bundle.txt [trusted-tsa-public-key-base64]
// Exit code 0 means every check passed; 1 means at least one problem was found.

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Base64;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

public final class Verifier {
    private Verifier() {}

    /** Verify a whole log against its checkpoints: hashes, links, sequence, time order, Merkle roots, signatures. */
    static Report verifyLog(List<Entry> entries, List<Checkpoint> checkpoints, String trustedKey) {
        List<String> problems = new ArrayList<>();
        long firstBad = Long.MAX_VALUE;
        for (int i = 0; i < entries.size(); i++) {
            Entry e = entries.get(i);
            if (e.seq() != i) {
                problems.add("position " + i + " holds seq " + e.seq() + " (entry missing or reordered)");
                firstBad = Math.min(firstBad, i);
            }
            if (!e.recomputeHash().equals(e.hash())) {
                problems.add("entry #" + i + " content does not match its hash");
                firstBad = Math.min(firstBad, i);
            }
            String expectedPrev = i == 0 ? Crypto.ZERO_HASH : entries.get(i - 1).hash();
            if (!e.prevHash().equals(expectedPrev)) {
                problems.add("entry #" + i + " does not link to the entry before it");
                firstBad = Math.min(firstBad, e.seq() == i && i > 0 ? i - 1 : i);   // either side of a broken link may be the edit
            }
            if (i > 0 && e.timestampMillis() < entries.get(i - 1).timestampMillis()) {
                problems.add("entry #" + i + " is timestamped before entry #" + (i - 1));
                firstBad = Math.min(firstBad, i);
            }
        }
        long lastGoodSize = 0;
        boolean rootFailureSeen = false;
        for (Checkpoint cp : checkpoints) {
            String digest = Checkpoint.statementDigest(cp.size(), cp.merkleRootHex(), cp.headHashHex());
            if (!digest.equals(cp.token().digestHex()) || !TimestampAuthority.verify(trustedKey, cp.token())) {
                problems.add("checkpoint size " + cp.size() + ": time-stamp signature does not verify");
                continue;
            }
            if (cp.size() > entries.size()) {
                problems.add("log truncated: checkpoint covers " + cp.size() + " entries, log has " + entries.size());
                firstBad = Math.min(firstBad, entries.size());
                continue;
            }
            String root = Crypto.HEX.formatHex(Merkle.root(DecisionLog.leafData(entries, cp.size())));
            boolean good = true;
            if (!root.equals(cp.merkleRootHex())) {
                problems.add("checkpoint size " + cp.size() + ": Merkle root differs from the sealed root (history rewritten)");
                good = false;
            }
            if (!entries.get((int) cp.size() - 1).hash().equals(cp.headHashHex())) {
                problems.add("checkpoint size " + cp.size() + ": head hash differs from the sealed head");
                good = false;
            }
            if (entries.get((int) cp.size() - 1).timestampMillis() > cp.token().timeMillis()) {
                problems.add("checkpoint size " + cp.size() + ": an entry claims a time after its own seal");
                good = false;
            }
            if (good && !rootFailureSeen) lastGoodSize = cp.size();
            if (!good && !rootFailureSeen) {
                rootFailureSeen = true;
                // Entry-level evidence is more precise. Without it, the sealed prefix that still verifies bounds the change.
                if (firstBad == Long.MAX_VALUE) firstBad = lastGoodSize;
            }
        }
        return Report.of(problems, firstBad);
    }

    /** Parsed header and lines of a bundle. */
    record Parsed(Map<String, String> header, List<String[]> entries, Map<Long, String> proofs) {}

    static Parsed parse(String text) {
        String[] lines = text.split("\n");
        if (lines.length == 0 || !lines[0].equals(EvidenceBundle.MAGIC)) throw new IllegalArgumentException("not an evidence bundle");
        Map<String, String> header = new LinkedHashMap<>();
        List<String[]> entries = new ArrayList<>();
        Map<Long, String> proofs = new LinkedHashMap<>();
        for (int i = 1; i < lines.length; i++) {
            String[] parts = lines[i].split(" ");
            switch (parts[0]) {
                case "entry" -> { if (parts.length != 4) throw new IllegalArgumentException("bad entry line " + i); entries.add(parts); }
                case "proof" -> { if (parts.length != 3) throw new IllegalArgumentException("bad proof line " + i); proofs.put(Long.parseLong(parts[1]), parts[2]); }
                default -> { if (parts.length != 2) throw new IllegalArgumentException("bad header line " + i); header.put(parts[0], parts[1]); }
            }
        }
        return new Parsed(header, entries, proofs);
    }

    /** Verify a bundle: the seal is signed by the trusted authority, and every disclosed entry is inside the sealed tree. */
    static Report verifyBundle(String text, String trustedKey) {
        List<String> problems = new ArrayList<>();
        long firstBad = Long.MAX_VALUE;
        Parsed p;
        try {
            p = parse(text);
        } catch (RuntimeException ex) {
            return Report.of(List.of("unreadable bundle: " + ex.getMessage()), 0);
        }
        Map<String, String> h = p.header();
        String subject = h.get("subject"), root = h.get("merkle-root"), head = h.get("head-hash");
        long size = Long.parseLong(h.getOrDefault("tree-size", "0"));
        long tsaTime = Long.parseLong(h.getOrDefault("tsa-time", "0"));
        String key = trustedKey != null ? trustedKey : h.get("tsa-public-key");
        if (trustedKey != null && !trustedKey.equals(h.get("tsa-public-key"))) {
            problems.add("bundle names a different time-stamp key than the one the verifier trusts");
        }
        TimestampToken token = new TimestampToken(Checkpoint.statementDigest(size, root, head), tsaTime, h.getOrDefault("tsa-signature", ""));
        if (!TimestampAuthority.verify(key, token)) problems.add("seal signature does not verify (size, root, head, or time altered)");
        if (p.entries().isEmpty()) problems.add("bundle discloses no entries");
        Entry previous = null;
        for (String[] line : p.entries()) {
            long seq = Long.parseLong(line[1]);
            Entry e;
            try {
                e = Entry.fromCanonical(Base64.getDecoder().decode(line[2]));
            } catch (RuntimeException ex) {
                problems.add("entry #" + seq + " is malformed: " + ex.getMessage());
                firstBad = Math.min(firstBad, seq);
                continue;
            }
            if (!e.hash().equals(line[3]) || e.seq() != seq) {
                problems.add("entry #" + seq + " content does not match its stated hash");
                firstBad = Math.min(firstBad, seq);
            }
            if (!e.subject().equals(subject)) problems.add("entry #" + seq + " is about " + e.subject() + ", not " + subject);
            if (e.timestampMillis() > tsaTime) problems.add("entry #" + seq + " claims a time after the seal");
            String proof = p.proofs().get(seq);
            List<byte[]> path = new ArrayList<>();
            if (proof != null && !proof.equals("-")) for (String hex : proof.split(",")) path.add(Crypto.HEX.parseHex(hex));
            if (proof == null || !Merkle.verifyInclusion(Crypto.HEX.parseHex(e.hash()), seq, size, path, Crypto.HEX.parseHex(root))) {
                problems.add("entry #" + seq + " is not provably inside the sealed tree");
                firstBad = Math.min(firstBad, seq);
            }
            if (seq == size - 1 && !e.hash().equals(head)) problems.add("last entry does not match the sealed head");
            if (previous != null && previous.seq() + 1 == seq && !e.prevHash().equals(previous.hash())) {
                problems.add("entries #" + previous.seq() + " and #" + seq + " are adjacent but not linked");
            }
            previous = e;
        }
        return Report.of(problems, firstBad == Long.MAX_VALUE ? 0 : firstBad);
    }

    /** Human-readable lines for the disclosed entries, decoded from the canonical bytes (the only source of truth). */
    static List<String> describe(String text) {
        Parsed p = parse(text);
        List<String> out = new ArrayList<>();
        for (String[] line : p.entries()) {
            Entry e = Entry.fromCanonical(Base64.getDecoder().decode(line[2]));
            out.add("#" + e.seq() + " " + Instant.ofEpochMilli(e.timestampMillis()) + " " + e.kind() + " " + e.payload());
        }
        out.add("sealed: " + p.header().get("tree-size") + " entries, no later than " + Instant.ofEpochMilli(Long.parseLong(p.header().get("tsa-time"))));
        return out;
    }

    public static void main(String[] args) throws IOException {
        if (args.length < 1) {
            System.err.println("usage: java -cp .out Verifier bundle.txt [trusted-tsa-public-key-base64]");
            System.exit(2);
        }
        String text = Files.readString(Path.of(args[0]));
        String trusted = args.length > 1 ? args[1] : null;
        if (trusted == null) System.out.println("warning: no trusted key given; using the key inside the bundle (proves integrity, not origin)");
        Report r = verifyBundle(text, trusted);
        if (r.ok()) describe(text).forEach(System.out::println);
        r.problems().forEach(problem -> System.out.println("PROBLEM: " + problem));
        System.out.println(r.ok() ? "VERIFIED" : "FAILED");
        System.exit(r.ok() ? 0 : 1);
    }
}
