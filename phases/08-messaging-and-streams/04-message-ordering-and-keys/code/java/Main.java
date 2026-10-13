// Lesson: phases/08-messaging-and-streams/04-message-ordering-and-keys/docs/en.md
// Message Ordering, Keys, and Head-of-Line Blocking
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Apache Kafka documentation, section 4.4 "Message Delivery Semantics" and producer configs
//          max.in.flight.requests.per.connection / enable.idempotence; KIP-98 (idempotent producer sequence numbers).
// Java 21, standard library only. Time is simulated with a discrete-event clock; nothing sleeps or touches the network.

import java.nio.charset.StandardCharsets;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Deque;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Random;
import java.util.Set;
import java.util.TreeSet;

/** ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01; lessons are self-contained. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code must be three letters: " + code);
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }
}

/** Integer minor units plus a currency. Never a float. */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    @Override
    public String toString() {
        long scale = 1;
        for (int i = 0; i < currency.exponent(); i++) scale *= 10;
        long abs = Math.abs(minorUnits);
        String frac = Long.toString(abs % scale);
        while (frac.length() < currency.exponent()) frac = "0" + frac;
        return (minorUnits < 0 ? "-" : "") + (abs / scale) + "." + frac + " " + currency.code();
    }
}

// ---------------------------------------------------------------------------------------------------------------------
// Records and the partitioner. The partition is a pure function of the key, so one key has one home and one order.
// ---------------------------------------------------------------------------------------------------------------------

/**
 * One payment event as the producer hands it to the log. {@code seq} is the producer's per-key sequence number: the
 * n-th event the producer emitted for this key. The log does not assign it; the producer does, before any retry.
 */
record Message(String key, long seq, String type, Money amount, long producedAt, long costMillis) {
    Message {
        Objects.requireNonNull(key, "key");
        if (seq < 0) throw new IllegalArgumentException("seq must be non-negative");
        if (costMillis < 0) throw new IllegalArgumentException("costMillis must be non-negative");
    }
}

/** A message as it sits in a partition: the log's offset plus the producer's message. */
record StoredMessage(long offset, Message message) {}

/** FNV-1a 32-bit over UTF-8 bytes; the same function Lesson 01 uses, so a key lands where Lesson 01 would put it. */
final class Partitioner {
    static int fnv1a32(byte[] bytes) {
        int hash = 0x811C9DC5;
        for (byte b : bytes) {
            hash ^= (b & 0xFF);
            hash *= 0x01000193;
        }
        return hash;
    }

    /** Partition for a key: hash masked to non-negative, then mod N. Same key, same partition, while N is fixed. */
    static int partitionFor(String key, int partitionCount) {
        if (partitionCount <= 0) throw new IllegalArgumentException("partitionCount must be positive");
        int hash = fnv1a32(key.getBytes(StandardCharsets.UTF_8));
        return (hash & 0x7FFFFFFF) % partitionCount;
    }
}

/** A minimal single-partition log: dense offsets, append-only, read by offset. Lesson 01 has the segmented version. */
final class Partition {
    final int id;
    private final List<StoredMessage> records = new ArrayList<>();

    Partition(int id) { this.id = id; }

    long append(Message message) {
        long offset = records.size();
        records.add(new StoredMessage(offset, message));
        return offset;
    }

    long logEndOffset() { return records.size(); }

    StoredMessage get(long offset) {
        if (offset < 0 || offset >= records.size()) throw new IndexOutOfBoundsException("offset " + offset + " not in [0, " + records.size() + ")");
        return records.get((int) offset);
    }

    List<StoredMessage> readAll() { return Collections.unmodifiableList(records); }

    /** The sequence of seq values for one key, in offset order. Per-key order holds iff this is strictly increasing by one. */
    List<Long> sequenceOf(String key) {
        List<Long> out = new ArrayList<>();
        for (StoredMessage s : records) if (s.message().key().equals(key)) out.add(s.message().seq());
        return out;
    }
}

/** A topic with N partitions. Appending routes by key. */
final class Topic {
    final String name;
    private final List<Partition> partitions = new ArrayList<>();

    Topic(String name, int partitionCount) {
        this.name = name;
        for (int i = 0; i < partitionCount; i++) partitions.add(new Partition(i));
    }

    int partitionCount() { return partitions.size(); }

    Partition partition(int id) { return partitions.get(id); }

    int append(Message message) {
        int p = Partitioner.partitionFor(message.key(), partitions.size());
        partitions.get(p).append(message);
        return p;
    }
}

// ---------------------------------------------------------------------------------------------------------------------
// Retry-induced reordering. A producer with several sends in flight that retries a failed send lands it after sends
// that were queued behind it. Sequence numbers detect this at the consumer and prevent it at the broker.
// ---------------------------------------------------------------------------------------------------------------------

/** Decides which sends fail on their first attempt. Deterministic: either an explicit set or a seeded Random. */
final class FaultInjector {
    private final Set<Long> failFirstAttempt;

    FaultInjector(Set<Long> failFirstAttempt) { this.failFirstAttempt = new HashSet<>(failFirstAttempt); }

    /** Fail roughly {@code probability} of the first {@code count} sends, chosen by a seeded Random. */
    static FaultInjector random(long seed, int count, double probability) {
        Random random = new Random(seed);
        Set<Long> fails = new HashSet<>();
        for (long i = 0; i < count; i++) if (random.nextDouble() < probability) fails.add(i);
        return new FaultInjector(fails);
    }

    boolean failsFirstAttempt(long sendIndex) { return failFirstAttempt.contains(sendIndex); }
}

/** What one simulated producer run produced: the final partition contents and how many retries and rejections happened. */
record SendOutcome(List<Message> appended, int retries, int rejected) {}

/**
 * Simulates a producer sending one key's messages to one partition with up to {@code maxInFlight} sends outstanding.
 * A send that fails its first attempt is retried after the rest of the in-flight window has been acknowledged, which
 * is exactly how a retry overtakes its successors. With {@code sequenceGuard} the broker rejects any send whose seq is
 * not lastSeq + 1 (KIP-98's idempotent producer), so the producer must re-send in order and the log stays ordered.
 */
final class ProducerSim {
    private static final class Send {
        final long index;
        final Message message;
        boolean attempted;

        Send(long index, Message message) { this.index = index; this.message = message; }
    }

    static SendOutcome deliver(List<Message> messages, int maxInFlight, FaultInjector faults, boolean sequenceGuard) {
        if (maxInFlight <= 0) throw new IllegalArgumentException("maxInFlight must be positive");
        Deque<Send> pending = new ArrayDeque<>();
        for (int i = 0; i < messages.size(); i++) pending.addLast(new Send(i, messages.get(i)));
        List<Message> appended = new ArrayList<>();
        Map<String, Long> lastSeq = new HashMap<>();
        int retries = 0, rejected = 0;
        while (!pending.isEmpty()) {
            List<Send> window = new ArrayList<>();
            while (!pending.isEmpty() && window.size() < maxInFlight) window.add(pending.pollFirst());
            List<Send> resend = new ArrayList<>();
            for (Send send : window) {
                if (!send.attempted && faults.failsFirstAttempt(send.index)) {
                    send.attempted = true;
                    retries++;
                    resend.add(send);
                    continue;
                }
                send.attempted = true;
                String key = send.message.key();
                long expected = lastSeq.getOrDefault(key, -1L) + 1;
                if (sequenceGuard && send.message.seq() != expected) {
                    rejected++;
                    resend.add(send);
                    continue;
                }
                lastSeq.put(key, send.message.seq());
                appended.add(send.message);
            }
            resend.sort((a, b) -> Long.compare(a.index, b.index));
            for (int i = resend.size() - 1; i >= 0; i--) pending.addFirst(resend.get(i));
        }
        return new SendOutcome(appended, retries, rejected);
    }
}

/** Consumer-side check that a key's messages arrive with seq = last + 1. Classifies every message it sees. */
final class SequenceTracker {
    enum Verdict { IN_ORDER, DUPLICATE, GAP }

    private final Map<String, Long> lastSeq = new HashMap<>();
    private int duplicates = 0, gaps = 0;

    Verdict observe(Message m) {
        long expected = lastSeq.getOrDefault(m.key(), -1L) + 1;
        if (m.seq() == expected) {
            lastSeq.put(m.key(), m.seq());
            return Verdict.IN_ORDER;
        }
        if (m.seq() < expected) {
            duplicates++;
            return Verdict.DUPLICATE;
        }
        gaps++;
        lastSeq.put(m.key(), m.seq());
        return Verdict.GAP;
    }

    int duplicates() { return duplicates; }

    int gaps() { return gaps; }

    /** True iff every message in the list for every key was IN_ORDER. */
    static boolean isOrdered(List<Message> messages) {
        SequenceTracker t = new SequenceTracker();
        for (Message m : messages) if (t.observe(m) != Verdict.IN_ORDER) return false;
        return true;
    }
}

// ---------------------------------------------------------------------------------------------------------------------
// Head-of-line blocking and per-key lanes. A discrete-event simulation: each message has an arrival time and a
// processing cost; the scheduler decides when it starts, and latency is completion minus arrival.
// ---------------------------------------------------------------------------------------------------------------------

/** When a message was processed and how long it waited. */
record Completion(long offset, String key, long startedAt, long completedAt, long latencyMillis) {}

/** Summary of completion latencies. All values are simulated milliseconds. */
record LatencyStats(int count, long max, long mean, long p50, long p99) {
    static LatencyStats of(List<Completion> completions) {
        if (completions.isEmpty()) return new LatencyStats(0, 0, 0, 0, 0);
        long[] lat = new long[completions.size()];
        long sum = 0;
        for (int i = 0; i < lat.length; i++) { lat[i] = completions.get(i).latencyMillis(); sum += lat[i]; }
        java.util.Arrays.sort(lat);
        return new LatencyStats(lat.length, lat[lat.length - 1], sum / lat.length, percentile(lat, 50), percentile(lat, 99));
    }

    private static long percentile(long[] sorted, int p) {
        int idx = (int) Math.ceil(p / 100.0 * sorted.length) - 1;
        return sorted[Math.max(0, Math.min(sorted.length - 1, idx))];
    }

    /** Stats restricted to completions whose key is not {@code excluded}: "how did everyone else fare?" */
    static LatencyStats excluding(List<Completion> completions, String excluded) {
        List<Completion> rest = new ArrayList<>();
        for (Completion c : completions) if (!c.key().equals(excluded)) rest.add(c);
        return of(rest);
    }
}

/**
 * Tracks which offsets of a partition have finished processing and reports the offset that may be committed: the
 * smallest offset not yet completed. With out-of-order completion this is the "committed offset is the next offset to
 * read" contract from Lesson 01 applied to parallel processing: commit the low-water mark, never the highest offset.
 */
final class CommitTracker {
    private long committable;
    private final TreeSet<Long> doneAhead = new TreeSet<>();

    CommitTracker(long startOffset) { this.committable = startOffset; }

    long complete(long offset) {
        if (offset < committable) throw new IllegalStateException("offset " + offset + " already committed");
        if (!doneAhead.add(offset)) throw new IllegalStateException("offset " + offset + " completed twice");
        while (!doneAhead.isEmpty() && doneAhead.first() == committable) {
            doneAhead.pollFirst();
            committable++;
        }
        return committable;
    }

    long committable() { return committable; }

    int completedAhead() { return doneAhead.size(); }
}

/** A processing strategy for one partition's records. */
interface Scheduler {
    List<Completion> run(Partition partition);
}

/** One consumer, one thread, offsets in order. The slow record at offset k delays every record after k. */
final class SequentialScheduler implements Scheduler {
    @Override
    public List<Completion> run(Partition partition) {
        List<Completion> out = new ArrayList<>();
        long busyUntil = 0;
        for (StoredMessage s : partition.readAll()) {
            Message m = s.message();
            long start = Math.max(busyUntil, m.producedAt());
            long end = start + m.costMillis();
            busyUntil = end;
            out.add(new Completion(s.offset(), m.key(), start, end, end - m.producedAt()));
        }
        return out;
    }
}

/**
 * Per-key lanes inside one partition: lane = hash(key) mod lanes. Each lane processes its records in offset order,
 * so per-key order holds; different lanes run independently, so a slow key blocks only the keys sharing its lane.
 * Also drives a CommitTracker so the demo can show how far behind the committable offset trails completion.
 */
final class KeyedLaneScheduler implements Scheduler {
    private final int lanes;
    private long finalCommittable;
    private int maxCompletedAhead;

    KeyedLaneScheduler(int lanes) {
        if (lanes <= 0) throw new IllegalArgumentException("lanes must be positive");
        this.lanes = lanes;
    }

    int laneFor(String key) { return Partitioner.partitionFor(key, lanes); }

    @Override
    public List<Completion> run(Partition partition) {
        long[] laneBusyUntil = new long[lanes];
        List<Completion> out = new ArrayList<>();
        for (StoredMessage s : partition.readAll()) {
            Message m = s.message();
            int lane = laneFor(m.key());
            long start = Math.max(laneBusyUntil[lane], m.producedAt());
            long end = start + m.costMillis();
            laneBusyUntil[lane] = end;
            out.add(new Completion(s.offset(), m.key(), start, end, end - m.producedAt()));
        }
        // Replay completions in time order through a CommitTracker to find how far the commit lags.
        List<Completion> byTime = new ArrayList<>(out);
        byTime.sort((a, b) -> a.completedAt() != b.completedAt() ? Long.compare(a.completedAt(), b.completedAt()) : Long.compare(a.offset(), b.offset()));
        CommitTracker tracker = new CommitTracker(0);
        maxCompletedAhead = 0;
        for (Completion c : byTime) {
            tracker.complete(c.offset());
            maxCompletedAhead = Math.max(maxCompletedAhead, tracker.completedAhead());
        }
        finalCommittable = tracker.committable();
        return out;
    }

    long finalCommittable() { return finalCommittable; }

    /** The most offsets that were ever finished but uncommittable because an earlier offset was still running. */
    int maxCompletedAhead() { return maxCompletedAhead; }
}

/** Builds the demo's workload and verifies per-key order in a list of completions. */
final class Workload {
    /**
     * {@code count} messages arriving one every {@code gapMillis}, spread round-robin over {@code keys}. Every message
     * costs {@code fastCost}, except messages for {@code slowKey}, which cost {@code slowCost}.
     */
    static List<Message> build(List<String> keys, int count, long gapMillis, long fastCost, String slowKey, long slowCost) {
        Map<String, Long> seqs = new HashMap<>();
        List<Message> out = new ArrayList<>();
        for (int i = 0; i < count; i++) {
            String key = keys.get(i % keys.size());
            long seq = seqs.merge(key, 1L, Long::sum) - 1;
            long cost = key.equals(slowKey) ? slowCost : fastCost;
            String type = seq % 3 == 0 ? "AUTH" : seq % 3 == 1 ? "CAPTURE" : "REVERSAL";
            out.add(new Message(key, seq, type, Money.ofMinor(1999 + i, Currency.USD), i * gapMillis, cost));
        }
        return out;
    }

    /** True iff, for every key, completions happen in offset order (startedAt non-decreasing along the key's offsets). */
    static boolean perKeyOrderHolds(Partition partition, List<Completion> completions) {
        Map<Long, Completion> byOffset = new HashMap<>();
        for (Completion c : completions) byOffset.put(c.offset(), c);
        Map<String, Long> lastEnd = new HashMap<>();
        for (StoredMessage s : partition.readAll()) {
            Completion c = byOffset.get(s.offset());
            String key = s.message().key();
            long previousEnd = lastEnd.getOrDefault(key, Long.MIN_VALUE);
            if (c.startedAt() < previousEnd) return false;
            lastEnd.put(key, c.completedAt());
        }
        return true;
    }
}

public final class Main {
    public static void main(String[] args) {
        System.out.println("== 1. Keys choose partitions; a partition is the unit of order ==");
        Topic topic = new Topic("payments.events", 3);
        List<String> accounts = List.of("acct-1001", "acct-1002", "acct-1003", "acct-1004", "acct-1005", "acct-1006");
        for (String a : accounts) System.out.printf("%-10s -> partition %d%n", a, Partitioner.partitionFor(a, 3));
        List<Message> keyed = Workload.build(accounts, 18, 10, 5, "none", 5);
        for (Message m : keyed) topic.append(m);
        for (int p = 0; p < topic.partitionCount(); p++) {
            Partition part = topic.partition(p);
            StringBuilder sb = new StringBuilder();
            for (StoredMessage s : part.readAll()) sb.append(s.message().key().substring(5)).append('#').append(s.message().seq()).append(' ');
            System.out.printf("partition %d (%d records): %s%n", p, part.logEndOffset(), sb.toString().trim());
        }
        System.out.println("acct-1001 seqs in its partition: " + topic.partition(Partitioner.partitionFor("acct-1001", 3)).sequenceOf("acct-1001"));
        System.out.printf("same keys with 4 partitions: %s%n", accounts.stream().map(a -> a.substring(5) + "->" + Partitioner.partitionFor(a, 4)).toList());

        System.out.println("\n== 2. Head-of-line blocking: one slow account in a partition of fast ones ==");
        Partition hol = new Partition(0);
        List<String> keys = List.of("acct-fast-1", "acct-fast-2", "acct-fast-3", "acct-slow", "acct-fast-4", "acct-fast-5");
        for (Message m : Workload.build(keys, 60, 10, 2, "acct-slow", 40)) hol.append(m);
        System.out.println("60 messages, one every 10 ms; fast accounts cost 2 ms, acct-slow costs 40 ms");
        List<Completion> sequential = new SequentialScheduler().run(hol);
        LatencyStats seqAll = LatencyStats.of(sequential);
        LatencyStats seqFast = LatencyStats.excluding(sequential, "acct-slow");
        System.out.printf("sequential   : all p50=%d p99=%d max=%d | fast accounts only p50=%d max=%d%n",
                seqAll.p50(), seqAll.p99(), seqAll.max(), seqFast.p50(), seqFast.max());
        Completion fastVictim = sequential.get(4);
        System.out.printf("  offset 4 (%s, cost 2 ms) arrived at %d ms, started at %d ms: it waited behind acct-slow at offset 3%n",
                fastVictim.key(), hol.get(4).message().producedAt(), fastVictim.startedAt());

        System.out.println("\n== 3. Per-key lanes inside the partition: order per key, parallelism across keys ==");
        for (int lanes : new int[] {2, 4, 8}) {
            KeyedLaneScheduler scheduler = new KeyedLaneScheduler(lanes);
            List<Completion> laned = scheduler.run(hol);
            LatencyStats fast = LatencyStats.excluding(laned, "acct-slow");
            LatencyStats slow = LatencyStats.of(laned.stream().filter(c -> c.key().equals("acct-slow")).toList());
            System.out.printf("%d lanes: fast accounts p50=%d max=%d | acct-slow max=%d | per-key order holds=%b | committable at end=%d, max finished-but-uncommittable=%d%n",
                    lanes, fast.p50(), fast.max(), slow.max(), Workload.perKeyOrderHolds(hol, laned), scheduler.finalCommittable(), scheduler.maxCompletedAhead());
            if (lanes == 2) {
                Set<String> sharing = new TreeSet<>();
                for (String k : keys) if (scheduler.laneFor(k) == scheduler.laneFor("acct-slow") && !k.equals("acct-slow")) sharing.add(k);
                System.out.println("  keys sharing acct-slow's lane (still blocked): " + sharing);
            }
        }

        System.out.println("\n== 4. Commit the low-water mark, not the highest finished offset ==");
        CommitTracker tracker = new CommitTracker(0);
        for (long off : new long[] {1, 2, 0, 4, 3}) {
            System.out.printf("offset %d done -> committable %d (finished ahead: %d)%n", off, tracker.complete(off), tracker.completedAhead());
        }

        System.out.println("\n== 5. Retries reorder when more than one send is in flight ==");
        List<Message> sends = Workload.build(List.of("acct-7"), 8, 1, 1, "none", 1);
        FaultInjector faults = new FaultInjector(Set.of(2L, 5L));
        for (int inFlight : new int[] {1, 5}) {
            SendOutcome outcome = ProducerSim.deliver(sends, inFlight, faults, false);
            System.out.printf("maxInFlight=%d, sends 2 and 5 fail once: log seqs=%s retries=%d ordered=%b%n",
                    inFlight, outcome.appended().stream().map(Message::seq).toList(), outcome.retries(), SequenceTracker.isOrdered(outcome.appended()));
        }
        SendOutcome reordered = ProducerSim.deliver(sends, 5, faults, false);
        SequenceTracker tracker2 = new SequenceTracker();
        StringBuilder verdicts = new StringBuilder();
        for (Message m : reordered.appended()) verdicts.append(m.seq()).append(':').append(tracker2.observe(m)).append(' ');
        System.out.println("consumer-side SequenceTracker on the reordered log: " + verdicts.toString().trim());
        System.out.printf("  gaps=%d duplicates=%d (a GAP followed by a lower seq is the signature of a reordered retry)%n", tracker2.gaps(), tracker2.duplicates());
        SendOutcome guarded = ProducerSim.deliver(sends, 5, faults, true);
        System.out.printf("maxInFlight=5 with broker sequence guard: log seqs=%s retries=%d rejected=%d ordered=%b%n",
                guarded.appended().stream().map(Message::seq).toList(), guarded.retries(), guarded.rejected(), SequenceTracker.isOrdered(guarded.appended()));

        System.out.println("\n== 6. The same thing at scale, seeded ==");
        List<Message> many = Workload.build(List.of("acct-9"), 2000, 1, 1, "none", 1);
        FaultInjector seeded = FaultInjector.random(42, 2000, 0.05);
        SendOutcome unguarded = ProducerSim.deliver(many, 5, seeded, false);
        SendOutcome guardedMany = ProducerSim.deliver(many, 5, seeded, true);
        SequenceTracker t3 = new SequenceTracker();
        for (Message m : unguarded.appended()) t3.observe(m);
        System.out.printf("2000 sends, 5%% fail once, maxInFlight=5: unguarded retries=%d gaps seen by consumer=%d; guarded rejected=%d ordered=%b%n",
                unguarded.retries(), t3.gaps(), guardedMany.rejected(), SequenceTracker.isOrdered(guardedMany.appended()));
    }
}
