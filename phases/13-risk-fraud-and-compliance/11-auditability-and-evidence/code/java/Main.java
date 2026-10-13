// Lesson: phases/13-risk-fraud-and-compliance/11-auditability-and-evidence/docs/en.md
// Auditability: Immutable Logs and Evidence Bundles
// Run:  javac -d .out *.java && java -cp .out Main        Verify a bundle: java -cp .out Verifier bundle.txt [trusted-key]
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: RFC 6962 / RFC 9162 (Certificate Transparency Merkle trees, audit paths); RFC 3161 (time-stamp protocol);
// RFC 8032 (Ed25519); D. J. Bernstein, netstrings. Java 21, standard library only.

import java.nio.charset.StandardCharsets;
import java.security.GeneralSecurityException;
import java.security.KeyFactory;
import java.security.KeyPair;
import java.security.KeyPairGenerator;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.security.PublicKey;
import java.security.SecureRandom;
import java.security.Signature;
import java.security.spec.NamedParameterSpec;
import java.security.spec.X509EncodedKeySpec;
import java.util.ArrayList;
import java.util.Base64;
import java.util.Collections;
import java.util.HexFormat;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.SortedMap;
import java.util.TreeMap;

/** ISO 4217 currency, copied from Phase 10, Lesson 01 (subset). */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);
}

/** Integer minor units plus a currency. Never a float. Copied from Phase 10, Lesson 01 (formatting subset). */
record Money(long minorUnits, Currency currency) {
    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, Objects.requireNonNull(currency)); }

    @Override public String toString() {
        long scale = 1;
        for (int i = 0; i < currency.exponent(); i++) scale *= 10;
        long abs = Math.abs(minorUnits);
        String sign = minorUnits < 0 ? "-" : "";
        if (currency.exponent() == 0) return sign + abs + " " + currency.code();
        String frac = Long.toString(abs % scale);
        while (frac.length() < currency.exponent()) frac = "0" + frac;
        return sign + (abs / scale) + "." + frac + " " + currency.code();
    }
}

/** Injected time source. Production uses the system clock; demos and tests step it by hand. */
interface Clock { long millis(); }

/** A clock the caller moves explicitly. Can be moved backwards, which is exactly what the log must reject. */
final class ManualClock implements Clock {
    private long now;
    ManualClock(long start) { this.now = start; }
    public long millis() { return now; }
    void advance(long ms) { now += ms; }
    void set(long ms) { now = ms; }
}

/** SHA-256, hex, and the netstring canonical encoding every hash in this lesson is computed over. */
final class Crypto {
    static final HexFormat HEX = HexFormat.of();
    static final String ZERO_HASH = "0".repeat(64);

    static byte[] sha256(byte[]... parts) {
        try {
            MessageDigest md = MessageDigest.getInstance("SHA-256");
            for (byte[] p : parts) md.update(p);
            return md.digest();
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException(e);
        }
    }

    static String sha256Hex(byte[] data) { return HEX.formatHex(sha256(data)); }

    /** Netstrings: "5:hello,". Length-prefixing makes the encoding injective: ("ab","c") and ("a","bc") differ. */
    static byte[] netstrings(String... fields) {
        StringBuilder sb = new StringBuilder();
        for (String f : fields) {
            byte[] utf8 = f.getBytes(StandardCharsets.UTF_8);
            sb.append(utf8.length).append(':').append(f).append(',');
        }
        return sb.toString().getBytes(StandardCharsets.UTF_8);
    }

    /** Inverse of netstrings; rejects anything malformed rather than guessing. */
    static List<String> parseNetstrings(byte[] data) {
        List<String> out = new ArrayList<>();
        int i = 0;
        while (i < data.length) {
            int colon = i;
            while (colon < data.length && data[colon] != ':') colon++;
            if (colon == data.length || colon == i) throw new IllegalArgumentException("bad netstring length at " + i);
            int len = Integer.parseInt(new String(data, i, colon - i, StandardCharsets.US_ASCII));
            int end = colon + 1 + len;
            if (len < 0 || end >= data.length || data[end] != ',') throw new IllegalArgumentException("bad netstring body at " + i);
            out.add(new String(data, colon + 1, len, StandardCharsets.UTF_8));
            i = end + 1;
        }
        return out;
    }
}

/**
 * One immutable log record. The hash covers every other field, including the previous entry's hash,
 * so changing any byte of any earlier entry changes every hash after it.
 */
record Entry(long seq, long timestampMillis, String prevHash, String kind, String subject,
             SortedMap<String, String> payload, String hash) {
    static final String VERSION = "pefs-log-v1";

    Entry {
        payload = Collections.unmodifiableSortedMap(new TreeMap<>(payload));
    }

    static Entry create(long seq, long ts, String prevHash, String kind, String subject, Map<String, String> payload) {
        SortedMap<String, String> sorted = new TreeMap<>(payload);
        String hash = Crypto.sha256Hex(canonical(seq, ts, prevHash, kind, subject, sorted));
        return new Entry(seq, ts, prevHash, kind, subject, sorted, hash);
    }

    /** The exact bytes that are hashed. Field order is fixed; payload keys are sorted; every field is length-prefixed. */
    static byte[] canonical(long seq, long ts, String prevHash, String kind, String subject, SortedMap<String, String> payload) {
        List<String> fields = new ArrayList<>(List.of(VERSION, Long.toString(seq), Long.toString(ts), prevHash, kind, subject,
                Integer.toString(payload.size())));
        for (Map.Entry<String, String> e : payload.entrySet()) { fields.add(e.getKey()); fields.add(e.getValue()); }
        return Crypto.netstrings(fields.toArray(new String[0]));
    }

    byte[] canonical() { return canonical(seq, timestampMillis, prevHash, kind, subject, payload); }

    String recomputeHash() { return Crypto.sha256Hex(canonical()); }

    /** Parse canonical bytes back into an entry whose hash is recomputed, never trusted. */
    static Entry fromCanonical(byte[] bytes) {
        List<String> f = Crypto.parseNetstrings(bytes);
        if (f.size() < 7 || !f.get(0).equals(VERSION)) throw new IllegalArgumentException("not a " + VERSION + " entry");
        int n = Integer.parseInt(f.get(6));
        if (f.size() != 7 + 2 * n) throw new IllegalArgumentException("payload count mismatch");
        SortedMap<String, String> payload = new TreeMap<>();
        for (int i = 0; i < n; i++) payload.put(f.get(7 + 2 * i), f.get(8 + 2 * i));
        return create(Long.parseLong(f.get(1)), Long.parseLong(f.get(2)), f.get(3), f.get(4), f.get(5), payload);
    }

    /** For tamper demonstrations only: same hash, different content. */
    Entry withPayloadKeepingHash(String key, String value) {
        SortedMap<String, String> p = new TreeMap<>(payload);
        p.put(key, value);
        return new Entry(seq, timestampMillis, prevHash, kind, subject, p, hash);
    }
}

/** RFC 6962 Merkle tree hash with leaf/node domain separation, audit paths, and RFC 9162 path verification. */
final class Merkle {
    static byte[] leafHash(byte[] data) { return Crypto.sha256(new byte[] {0x00}, data); }

    static byte[] nodeHash(byte[] left, byte[] right) { return Crypto.sha256(new byte[] {0x01}, left, right); }

    /** Largest power of two strictly less than n (n >= 2). */
    static int split(int n) {
        int k = 1;
        while (k << 1 < n) k <<= 1;
        return k;
    }

    /** MTH(D[lo:hi]). Empty tree is SHA-256 of the empty string. */
    static byte[] root(List<byte[]> leaves, int lo, int hi) {
        int n = hi - lo;
        if (n == 0) return Crypto.sha256();
        if (n == 1) return leafHash(leaves.get(lo));
        int k = split(n);
        return nodeHash(root(leaves, lo, lo + k), root(leaves, lo + k, hi));
    }

    static byte[] root(List<byte[]> leaves) { return root(leaves, 0, leaves.size()); }

    /** PATH(m, D[lo:hi]): the sibling hashes from leaf m up to the root, bottom first. */
    static List<byte[]> auditPath(List<byte[]> leaves, int m, int lo, int hi) {
        int n = hi - lo;
        if (n <= 1) return new ArrayList<>();
        int k = split(n);
        List<byte[]> path;
        if (m < k) {
            path = auditPath(leaves, m, lo, lo + k);
            path.add(root(leaves, lo + k, hi));
        } else {
            path = auditPath(leaves, m - k, lo + k, hi);
            path.add(root(leaves, lo, lo + k));
        }
        return path;
    }

    static List<byte[]> auditPath(List<byte[]> leaves, int m) {
        if (m < 0 || m >= leaves.size()) throw new IndexOutOfBoundsException("leaf " + m + " of " + leaves.size());
        return auditPath(leaves, m, 0, leaves.size());
    }

    /** RFC 9162 section 2.1.3.2: recompute the root from one leaf and its path. O(log n) hashes. */
    static boolean verifyInclusion(byte[] leafData, long index, long treeSize, List<byte[]> path, byte[] expectedRoot) {
        if (index < 0 || index >= treeSize) return false;
        long fn = index, sn = treeSize - 1;
        byte[] r = leafHash(leafData);
        for (byte[] p : path) {
            if (sn == 0) return false;
            if ((fn & 1) == 1 || fn == sn) {
                r = nodeHash(p, r);
                if ((fn & 1) == 0) {
                    while ((fn & 1) == 0 && fn != 0) { fn >>= 1; sn >>= 1; }
                }
            } else {
                r = nodeHash(r, p);
            }
            fn >>= 1;
            sn >>= 1;
        }
        return sn == 0 && MessageDigest.isEqual(r, expectedRoot);
    }
}

/** A signed statement "this digest existed no later than time T", in the spirit of an RFC 3161 time-stamp token. */
record TimestampToken(String digestHex, long timeMillis, String signatureBase64) {
    byte[] signedBytes() { return Crypto.netstrings("pefs-tsa-v1", digestHex, Long.toString(timeMillis)); }
}

/** An independent party with its own clock and an Ed25519 key. The log operator cannot forge its signatures. */
final class TimestampAuthority {
    private final KeyPair keys;
    private final Clock clock;

    TimestampAuthority(long seed, Clock clock) {
        try {
            SecureRandom rng = SecureRandom.getInstance("SHA1PRNG");
            rng.setSeed(seed);
            KeyPairGenerator gen = KeyPairGenerator.getInstance("Ed25519");
            gen.initialize(NamedParameterSpec.ED25519, rng);
            this.keys = gen.generateKeyPair();
        } catch (GeneralSecurityException e) {
            throw new IllegalStateException(e);
        }
        this.clock = clock;
    }

    String publicKeyBase64() { return Base64.getEncoder().encodeToString(keys.getPublic().getEncoded()); }

    TimestampToken stamp(String digestHex) {
        long t = clock.millis();
        TimestampToken unsigned = new TimestampToken(digestHex, t, "");
        try {
            Signature s = Signature.getInstance("Ed25519");
            s.initSign(keys.getPrivate());
            s.update(unsigned.signedBytes());
            return new TimestampToken(digestHex, t, Base64.getEncoder().encodeToString(s.sign()));
        } catch (GeneralSecurityException e) {
            throw new IllegalStateException(e);
        }
    }

    static boolean verify(String publicKeyBase64, TimestampToken token) {
        try {
            PublicKey key = KeyFactory.getInstance("Ed25519")
                    .generatePublic(new X509EncodedKeySpec(Base64.getDecoder().decode(publicKeyBase64)));
            Signature s = Signature.getInstance("Ed25519");
            s.initVerify(key);
            s.update(token.signedBytes());
            return s.verify(Base64.getDecoder().decode(token.signatureBase64()));
        } catch (GeneralSecurityException | IllegalArgumentException e) {
            return false;
        }
    }
}

/** A sealed prefix of the log: its size, Merkle root, head hash, and a time-stamp over all three. */
record Checkpoint(long size, String merkleRootHex, String headHashHex, TimestampToken token) {
    static String statementDigest(long size, String rootHex, String headHex) {
        return Crypto.sha256Hex(Crypto.netstrings("pefs-checkpoint-v1", Long.toString(size), rootHex, headHex));
    }
}

/** The append-only decision log. There is no update and no delete; the only mutations are append and seal. */
final class DecisionLog {
    private final List<Entry> entries = new ArrayList<>();
    private final List<Checkpoint> checkpoints = new ArrayList<>();
    private final Clock clock;
    private final TimestampAuthority tsa;

    DecisionLog(Clock clock, TimestampAuthority tsa) {
        this.clock = clock;
        this.tsa = tsa;
    }

    Entry append(String kind, String subject, Map<String, String> payload) {
        long ts = clock.millis();
        Entry last = entries.isEmpty() ? null : entries.get(entries.size() - 1);
        if (last != null && ts < last.timestampMillis()) {
            throw new IllegalStateException("clock went backwards: " + ts + " < " + last.timestampMillis());
        }
        Entry e = Entry.create(entries.size(), ts, last == null ? Crypto.ZERO_HASH : last.hash(), kind, subject, payload);
        entries.add(e);
        return e;
    }

    /** Seal everything appended so far: compute the Merkle root and have the independent authority time-stamp it. */
    Checkpoint seal() {
        if (entries.isEmpty()) throw new IllegalStateException("nothing to seal");
        String root = Crypto.HEX.formatHex(Merkle.root(leafData(entries, entries.size())));
        String head = entries.get(entries.size() - 1).hash();
        Checkpoint cp = new Checkpoint(entries.size(), root, head, tsa.stamp(Checkpoint.statementDigest(entries.size(), root, head)));
        checkpoints.add(cp);
        return cp;
    }

    static List<byte[]> leafData(List<Entry> entries, long size) {
        List<byte[]> leaves = new ArrayList<>();
        for (int i = 0; i < size; i++) leaves.add(Crypto.HEX.parseHex(entries.get(i).hash()));
        return leaves;
    }

    List<Entry> entries() { return List.copyOf(entries); }

    List<Checkpoint> checkpoints() { return List.copyOf(checkpoints); }

    String tsaPublicKey() { return tsa.publicKeyBase64(); }
}

/** What a verifier found. ok is true only if problems is empty. firstBadSeq is -1 when no entry is implicated. */
record Report(boolean ok, long firstBadSeq, List<String> problems) {
    static Report of(List<String> problems, long firstBad) { return new Report(problems.isEmpty(), problems.isEmpty() ? -1 : firstBad, List.copyOf(problems)); }
}

/** Everything about one subject, provable against a sealed checkpoint without disclosing any other entry. */
final class EvidenceBundle {
    static final String MAGIC = "pefs-evidence-bundle v1";

    static String export(DecisionLog log, String subject, Checkpoint cp) {
        List<Entry> all = log.entries();
        List<byte[]> leaves = DecisionLog.leafData(all, cp.size());
        StringBuilder sb = new StringBuilder();
        sb.append(MAGIC).append('\n');
        sb.append("subject ").append(subject).append('\n');
        sb.append("tree-size ").append(cp.size()).append('\n');
        sb.append("merkle-root ").append(cp.merkleRootHex()).append('\n');
        sb.append("head-hash ").append(cp.headHashHex()).append('\n');
        sb.append("tsa-time ").append(cp.token().timeMillis()).append('\n');
        sb.append("tsa-signature ").append(cp.token().signatureBase64()).append('\n');
        sb.append("tsa-public-key ").append(log.tsaPublicKey()).append('\n');
        for (int i = 0; i < cp.size(); i++) {
            Entry e = all.get(i);
            if (!e.subject().equals(subject)) continue;
            sb.append("entry ").append(e.seq()).append(' ')
              .append(Base64.getEncoder().encodeToString(e.canonical())).append(' ').append(e.hash()).append('\n');
            List<String> hexPath = new ArrayList<>();
            for (byte[] p : Merkle.auditPath(leaves, i)) hexPath.add(Crypto.HEX.formatHex(p));
            sb.append("proof ").append(e.seq()).append(' ').append(hexPath.isEmpty() ? "-" : String.join(",", hexPath)).append('\n');
        }
        return sb.toString();
    }
}

public final class Main {
    static final long T0 = 1_767_225_600_000L;   // 2026-01-01T00:00:00Z, fixed so output is reproducible

    static Map<String, String> decision(String outcome, int score, Money amount) {
        Map<String, String> p = new LinkedHashMap<>();
        p.put("outcome", outcome);
        p.put("score", Integer.toString(score));
        p.put("amount", amount.toString());
        p.put("model", "scorecard-2026-01-a");
        p.put("thresholds", "review>=60,decline>=130");
        return p;
    }

    /** The authority runs its own clock; here it reads 250 ms after the log clock, as if the request crossed a network. */
    static TimestampAuthority demoTsa(ManualClock logClock) {
        return new TimestampAuthority(2026L, () -> logClock.millis() + 250);
    }

    /** The demo log: a threshold change, six decisions, two review outcomes, sealed twice. */
    static DecisionLog buildDemoLog(ManualClock clock, TimestampAuthority tsa) {
        DecisionLog log = new DecisionLog(clock, tsa);
        log.append("config.threshold", "risk-engine", Map.of("review", "60", "decline", "130", "approved_by", "risk-lead-7", "ticket", "RISK-412"));
        clock.advance(1_000);
        log.append("risk.decision", "pay_0001", decision("APPROVE", 13, Money.ofMinor(4_250, Currency.USD)));
        clock.advance(1_000);
        log.append("risk.decision", "pay_0002", decision("REVIEW", 88, Money.ofMinor(31_900, Currency.USD)));
        clock.advance(1_000);
        log.append("risk.decision", "pay_0003", decision("DECLINE", 861, Money.ofMinor(129_999, Currency.USD)));
        clock.advance(1_000);
        log.append("risk.decision", "pay_0004", decision("APPROVE", 41, Money.ofMinor(1_999, Currency.EUR)));
        clock.advance(60_000);
        log.seal();                                                    // checkpoint 1 over entries 0..4
        clock.advance(240_000);
        log.append("review.outcome", "pay_0002", Map.of("result", "APPROVE", "analyst", "analyst-31", "minutes", "4"));
        clock.advance(1_000);
        log.append("risk.decision", "pay_0005", decision("REVIEW", 97, Money.ofMinor(55_000, Currency.USD)));
        clock.advance(1_000);
        log.append("risk.decision", "pay_0006", decision("APPROVE", 7, Money.ofMinor(899, Currency.USD)));
        clock.advance(120_000);
        log.append("review.outcome", "pay_0005", Map.of("result", "DECLINE", "analyst", "analyst-12", "minutes", "6"));
        clock.advance(60_000);
        log.seal();                                                    // checkpoint 2 over entries 0..8
        return log;
    }

    public static void main(String[] args) {
        ManualClock logClock = new ManualClock(T0);
        DecisionLog log = buildDemoLog(logClock, demoTsa(logClock));

        System.out.println("== The hash-chained log ==");
        for (Entry e : log.entries()) {
            System.out.printf("#%d t=+%-6d %-17s %-11s prev=%s.. hash=%s..%n", e.seq(), (e.timestampMillis() - T0) / 1000,
                    e.kind(), e.subject(), e.prevHash().substring(0, 8), e.hash().substring(0, 8));
        }
        System.out.println();
        for (Checkpoint cp : log.checkpoints()) {
            System.out.printf("checkpoint size=%d root=%s.. head=%s.. stamped t=+%ds%n", cp.size(), cp.merkleRootHex().substring(0, 12),
                    cp.headHashHex().substring(0, 12), (cp.token().timeMillis() - T0) / 1000);
        }

        String key = log.tsaPublicKey();
        System.out.println("\n== Full-log verification ==");
        Report clean = Verifier.verifyLog(log.entries(), log.checkpoints(), key);
        System.out.println("untouched log: ok=" + clean.ok());

        List<Entry> edited = new ArrayList<>(log.entries());
        edited.set(3, edited.get(3).withPayloadKeepingHash("outcome", "APPROVE"));
        print("edit #3 outcome DECLINE->APPROVE", Verifier.verifyLog(edited, log.checkpoints(), key));

        List<Entry> rehashed = new ArrayList<>(log.entries());
        Entry e3 = rehashed.get(3);
        Map<String, String> p3 = new TreeMap<>(e3.payload());
        p3.put("outcome", "APPROVE");
        rehashed.set(3, Entry.create(3, e3.timestampMillis(), e3.prevHash(), e3.kind(), e3.subject(), p3));
        print("edit #3 and recompute its hash", Verifier.verifyLog(rehashed, log.checkpoints(), key));

        List<Entry> rewritten = new ArrayList<>(rehashed.subList(0, 4));
        for (int i = 4; i < log.entries().size(); i++) {
            Entry o = log.entries().get(i);
            rewritten.add(Entry.create(o.seq(), o.timestampMillis(), rewritten.get(i - 1).hash(), o.kind(), o.subject(), o.payload()));
        }
        print("edit #3 and rewrite the whole chain", Verifier.verifyLog(rewritten, log.checkpoints(), key));

        List<Entry> deleted = new ArrayList<>(log.entries());
        deleted.remove(5);
        print("delete #5 (the review of pay_0002)", Verifier.verifyLog(deleted, log.checkpoints(), key));

        System.out.println("\n== Clock anchoring ==");
        try {
            logClock.set(T0 + 5_000);
            log.append("risk.decision", "pay_0099", decision("APPROVE", 1, Money.ofMinor(100, Currency.USD)));
        } catch (IllegalStateException ex) {
            System.out.println("backdated append rejected: " + ex.getMessage());
        }
        Checkpoint cp1 = log.checkpoints().get(0);
        System.out.printf("entries 0..%d existed no later than t=+%ds (signed by the TSA, not by us)%n",
                cp1.size() - 1, (cp1.token().timeMillis() - T0) / 1000);

        System.out.println("\n== Evidence bundle for pay_0002 ==");
        Checkpoint cp2 = log.checkpoints().get(1);
        String bundle = EvidenceBundle.export(log, "pay_0002", cp2);
        for (String line : bundle.split("\n")) System.out.println(line.length() > 110 ? line.substring(0, 107) + "..." : line);
        Report b = Verifier.verifyBundle(bundle, key);
        System.out.println("\nregulator verifies bundle with the TSA's published key: ok=" + b.ok());
        for (String line : Verifier.describe(bundle)) System.out.println("  " + line);

        String forged = bundle.replace(Base64.getEncoder().encodeToString(log.entries().get(5).canonical()),
                Base64.getEncoder().encodeToString(Entry.create(5, log.entries().get(5).timestampMillis(), log.entries().get(5).prevHash(),
                        "review.outcome", "pay_0002", Map.of("result", "DECLINE", "analyst", "analyst-31", "minutes", "4")).canonical()));
        print("bundle with the review result changed", Verifier.verifyBundle(forged, key));
    }

    static void print(String label, Report r) {
        System.out.printf("%-38s ok=%-5s first bad=#%d  %s%n", label + ":", r.ok(), r.firstBadSeq(), r.problems().isEmpty() ? "" : r.problems().get(0));
    }
}
