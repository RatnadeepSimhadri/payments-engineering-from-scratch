// Lesson: phases/07-storage-and-transactions/10-secondary-indexes/docs/en.md
// Secondary Indexes: Local vs Global over a partitioned payment store.
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Java 21, standard library only. Hashing: FNV-1a 64-bit (Fowler/Noll/Vo, IETF draft-eastlake-fnv).
// Design references: DynamoDB LSI/GSI documentation; Kleppmann, Designing Data-Intensive Applications, ch. 6.

import java.nio.charset.StandardCharsets;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Comparator;
import java.util.Deque;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.TreeMap;
import java.util.TreeSet;

/** ISO 4217 currency: three-letter code and minor-unit exponent. Copied from Phase 10, Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);

    Currency {
        if (code == null || code.length() != 3 || !code.chars().allMatch(Character::isUpperCase)) {
            throw new IllegalArgumentException("currency code must be three uppercase letters: " + code);
        }
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

/** Integer minor units plus a currency. Never a float. */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    Money plus(Money other) {
        if (!currency.equals(other.currency)) {
            throw new IllegalArgumentException("cannot combine " + currency.code() + " with " + other.currency.code());
        }
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    @Override public String toString() {
        long scale = currency.minorUnitsPerMajor();
        long abs = Math.abs(minorUnits);
        String whole = Long.toString(abs / scale);
        if (currency.exponent() == 0) return (minorUnits < 0 ? "-" : "") + whole + " " + currency.code();
        String frac = Long.toString(abs % scale);
        while (frac.length() < currency.exponent()) frac = "0" + frac;
        return (minorUnits < 0 ? "-" : "") + whole + "." + frac + " " + currency.code();
    }
}

/** One row of the primary table. The primary key is {@code id}; {@code customerId} is the indexed attribute. */
record Payment(String id, String customerId, String merchantId, Money amount, long createdAt) {
    Payment {
        Objects.requireNonNull(id, "id");
        Objects.requireNonNull(customerId, "customerId");
        Objects.requireNonNull(merchantId, "merchantId");
        Objects.requireNonNull(amount, "amount");
    }
}

/** Where a row lives: which shard, and its primary key there. A global index entry points at one of these. */
record Locator(int shardId, String paymentId) implements Comparable<Locator> {
    @Override public int compareTo(Locator o) {
        int c = Integer.compare(shardId, o.shardId);
        return c != 0 ? c : paymentId.compareTo(o.paymentId);
    }
}

/** How the global index is kept in step with the primary table. */
enum IndexMode {
    /** The index partition is written inside the same request as the row. Costs an extra remote write per put. */
    SYNC,
    /** The index change is queued and applied later. Puts are cheap; the index lags by the queue depth. */
    ASYNC
}

/** One change to the global index. {@code delete=false} inserts the locator under the customer; {@code delete=true} removes it. */
record IndexUpdate(long seq, String customerId, Locator locator, boolean delete) {}

/** The answer to a query plus what it cost: how many shard requests and index-partition requests were made. */
record QueryResult(List<Payment> payments, int shardRequests, int indexRequests) {
    int totalRequests() { return shardRequests + indexRequests; }
}

/** Result of checking the global index against the primary table. */
record IndexAudit(int entries, List<Locator> dangling, List<Locator> missing) {
    boolean consistent() { return dangling.isEmpty() && missing.isEmpty(); }
}

/** Deterministic 64-bit FNV-1a over UTF-8 bytes, so every language port routes a key to the same shard. */
final class Hashing {
    private static final long FNV_OFFSET = 0xcbf29ce484222325L;
    private static final long FNV_PRIME = 0x100000001b3L;

    private Hashing() {}

    static long fnv1a64(String key) {
        long h = FNV_OFFSET;
        for (byte b : key.getBytes(StandardCharsets.UTF_8)) {
            h ^= (b & 0xff);
            h *= FNV_PRIME;
        }
        return h;
    }

    static int partitionFor(String key, int partitions) {
        return Math.floorMod(fnv1a64(key), partitions);
    }
}

/**
 * One shard of the primary table. Owns its rows and a LOCAL secondary index (customerId -> ids) that is updated
 * in the same write as the row, so within a shard the index is never stale. Every method call that a client
 * would make over the network increments {@code requests}.
 */
final class Shard {
    final int id;
    private final TreeMap<String, Payment> rows = new TreeMap<>();
    private final TreeMap<String, TreeSet<String>> byCustomer = new TreeMap<>();
    private int requests = 0;

    Shard(int id) { this.id = id; }

    int requests() { return requests; }
    int size() { return rows.size(); }

    /** Insert or replace. Maintains the local index atomically with the row (single-threaded, same call). */
    Payment put(Payment p) {
        requests++;
        Payment previous = rows.put(p.id(), p);
        if (previous != null && !previous.customerId().equals(p.customerId())) {
            unindex(previous);
        }
        byCustomer.computeIfAbsent(p.customerId(), k -> new TreeSet<>()).add(p.id());
        return previous;
    }

    Payment get(String paymentId) {
        requests++;
        return rows.get(paymentId);
    }

    /** Fetch several rows in one request: the batch-get a client would issue to one node. */
    List<Payment> getMany(List<String> ids) {
        requests++;
        List<Payment> out = new ArrayList<>();
        for (String id : ids) {
            Payment p = rows.get(id);
            if (p != null) out.add(p);
        }
        return out;
    }

    Payment delete(String paymentId) {
        requests++;
        Payment removed = rows.remove(paymentId);
        if (removed != null) unindex(removed);
        return removed;
    }

    /** Local index lookup: only rows that live on this shard. */
    List<Payment> lookupCustomer(String customerId) {
        requests++;
        TreeSet<String> ids = byCustomer.get(customerId);
        if (ids == null) return List.of();
        List<Payment> out = new ArrayList<>(ids.size());
        for (String id : ids) out.add(rows.get(id));
        return out;
    }

    /** Full scan, used only to bootstrap a global index. */
    List<Payment> scan() {
        requests++;
        return new ArrayList<>(rows.values());
    }

    private void unindex(Payment p) {
        TreeSet<String> ids = byCustomer.get(p.customerId());
        if (ids != null) {
            ids.remove(p.id());
            if (ids.isEmpty()) byCustomer.remove(p.customerId());
        }
    }
}

/**
 * GLOBAL secondary index, partitioned by the indexed attribute (customerId), not by the primary key.
 * All locators for one customer live in one index partition, so a customer query touches exactly one.
 */
final class GlobalIndex {
    private final List<TreeMap<String, TreeSet<Locator>>> partitions = new ArrayList<>();
    private final int[] requests;

    GlobalIndex(int partitionCount) {
        if (partitionCount <= 0) throw new IllegalArgumentException("need at least one index partition");
        for (int i = 0; i < partitionCount; i++) partitions.add(new TreeMap<>());
        requests = new int[partitionCount];
    }

    int partitionCount() { return partitions.size(); }
    int partitionFor(String customerId) { return Hashing.partitionFor(customerId, partitions.size()); }
    int requests(int partition) { return requests[partition]; }

    int totalRequests() {
        int sum = 0;
        for (int r : requests) sum += r;
        return sum;
    }

    void apply(IndexUpdate u) {
        int p = partitionFor(u.customerId());
        requests[p]++;
        TreeMap<String, TreeSet<Locator>> part = partitions.get(p);
        if (u.delete()) {
            TreeSet<Locator> set = part.get(u.customerId());
            if (set != null) {
                set.remove(u.locator());
                if (set.isEmpty()) part.remove(u.customerId());
            }
        } else {
            part.computeIfAbsent(u.customerId(), k -> new TreeSet<>()).add(u.locator());
        }
    }

    /** One request to one partition. Returns locators in (shardId, paymentId) order. */
    List<Locator> lookup(String customerId) {
        int p = partitionFor(customerId);
        requests[p]++;
        TreeSet<Locator> set = partitions.get(p).get(customerId);
        return set == null ? List.of() : new ArrayList<>(set);
    }

    int entryCount() {
        int n = 0;
        for (TreeMap<String, TreeSet<Locator>> part : partitions) for (TreeSet<Locator> s : part.values()) n += s.size();
        return n;
    }

    /** Every (customer, locator) pair in the index, for auditing. */
    Map<Locator, String> allEntries() {
        Map<Locator, String> out = new TreeMap<>();
        for (TreeMap<String, TreeSet<Locator>> part : partitions) {
            for (Map.Entry<String, TreeSet<Locator>> e : part.entrySet()) {
                for (Locator l : e.getValue()) out.put(l, e.getKey());
            }
        }
        return out;
    }

    void clear() {
        for (TreeMap<String, TreeSet<Locator>> part : partitions) part.clear();
    }
}

/**
 * The partitioned payment store: N shards keyed by payment id, each with a local index, plus one global index
 * partitioned by customer id. Exposes both query strategies so their costs can be compared on the same data.
 */
final class PartitionedStore {
    private final List<Shard> shards = new ArrayList<>();
    private final GlobalIndex globalIndex;
    private final IndexMode mode;
    private final Deque<IndexUpdate> pending = new ArrayDeque<>();
    private long nextSeq = 1;

    PartitionedStore(int shardCount, int indexPartitions, IndexMode mode) {
        if (shardCount <= 0) throw new IllegalArgumentException("need at least one shard");
        for (int i = 0; i < shardCount; i++) shards.add(new Shard(i));
        this.globalIndex = new GlobalIndex(indexPartitions);
        this.mode = Objects.requireNonNull(mode);
    }

    int shardCount() { return shards.size(); }
    Shard shard(int id) { return shards.get(id); }
    GlobalIndex globalIndex() { return globalIndex; }
    IndexMode mode() { return mode; }
    int pendingIndexUpdates() { return pending.size(); }

    int shardFor(String paymentId) { return Hashing.partitionFor(paymentId, shards.size()); }

    int totalShardRequests() {
        int n = 0;
        for (Shard s : shards) n += s.requests();
        return n;
    }

    /** Write path. One shard write always; the global index write is inline (SYNC) or queued (ASYNC). */
    void put(Payment p) {
        int shardId = shardFor(p.id());
        Payment previous = shards.get(shardId).put(p);
        if (previous != null && !previous.customerId().equals(p.customerId())) {
            emit(new IndexUpdate(nextSeq++, previous.customerId(), new Locator(shardId, p.id()), true));
        }
        if (previous == null || !previous.customerId().equals(p.customerId())) {
            emit(new IndexUpdate(nextSeq++, p.customerId(), new Locator(shardId, p.id()), false));
        }
    }

    boolean delete(String paymentId) {
        int shardId = shardFor(paymentId);
        Payment removed = shards.get(shardId).delete(paymentId);
        if (removed == null) return false;
        emit(new IndexUpdate(nextSeq++, removed.customerId(), new Locator(shardId, paymentId), true));
        return true;
    }

    private void emit(IndexUpdate u) {
        if (mode == IndexMode.SYNC) globalIndex.apply(u); else pending.addLast(u);
    }

    /** Apply up to {@code max} queued index updates in order. Returns how many were applied. */
    int drainIndexUpdates(int max) {
        int applied = 0;
        while (applied < max && !pending.isEmpty()) {
            globalIndex.apply(pending.pollFirst());
            applied++;
        }
        return applied;
    }

    int drainAll() { return drainIndexUpdates(Integer.MAX_VALUE); }

    /** Primary-key lookup: exactly one shard request. */
    Payment get(String paymentId) {
        return shards.get(shardFor(paymentId)).get(paymentId);
    }

    /** Strategy A: ask every shard's local index and merge. Cost is always N shard requests. */
    QueryResult findByCustomerScatterGather(String customerId) {
        List<Payment> merged = new ArrayList<>();
        for (Shard s : shards) merged.addAll(s.lookupCustomer(customerId));
        merged.sort(Comparator.comparingLong(Payment::createdAt).thenComparing(Payment::id));
        return new QueryResult(Collections.unmodifiableList(merged), shards.size(), 0);
    }

    /**
     * Strategy B: one index-partition request, then one batch request per distinct shard that holds a hit.
     * Locators whose row has vanished (an index entry that outlived its row) are filtered out, never returned.
     */
    QueryResult findByCustomerGlobal(String customerId) {
        List<Locator> locators = globalIndex.lookup(customerId);
        Map<Integer, List<String>> byShard = new LinkedHashMap<>();
        for (Locator l : locators) byShard.computeIfAbsent(l.shardId(), k -> new ArrayList<>()).add(l.paymentId());
        List<Payment> merged = new ArrayList<>();
        for (Map.Entry<Integer, List<String>> e : byShard.entrySet()) {
            merged.addAll(shards.get(e.getKey()).getMany(e.getValue()));
        }
        merged.sort(Comparator.comparingLong(Payment::createdAt).thenComparing(Payment::id));
        return new QueryResult(Collections.unmodifiableList(merged), byShard.size(), 1);
    }

    /** Bootstrap or repair: throw the index away and rebuild it from a scan of every shard. */
    int rebuildGlobalIndex() {
        globalIndex.clear();
        pending.clear();
        int entries = 0;
        for (Shard s : shards) {
            for (Payment p : s.scan()) {
                globalIndex.apply(new IndexUpdate(nextSeq++, p.customerId(), new Locator(s.id, p.id()), false));
                entries++;
            }
        }
        return entries;
    }

    /** Compare index against primary table. Dangling = in index, row gone or re-attributed. Missing = row exists, no entry. */
    IndexAudit auditGlobalIndex() {
        Map<Locator, String> indexed = globalIndex.allEntries();
        List<Locator> dangling = new ArrayList<>();
        List<Locator> missing = new ArrayList<>();
        for (Map.Entry<Locator, String> e : indexed.entrySet()) {
            Payment row = shards.get(e.getKey().shardId()).get(e.getKey().paymentId());
            if (row == null || !row.customerId().equals(e.getValue())) dangling.add(e.getKey());
        }
        for (Shard s : shards) {
            for (Payment p : s.scan()) {
                Locator l = new Locator(s.id, p.id());
                if (!p.customerId().equals(indexed.get(l))) missing.add(l);
            }
        }
        return new IndexAudit(indexed.size(), dangling, missing);
    }
}

public final class Main {
    static List<Payment> sampleData() {
        Currency usd = Currency.USD;
        List<Payment> rows = new ArrayList<>();
        String[][] spec = {
            {"pay_001", "cust_alice", "m_books", "1999", "1"},
            {"pay_002", "cust_bob", "m_books", "4500", "2"},
            {"pay_003", "cust_alice", "m_coffee", "475", "3"},
            {"pay_004", "cust_carol", "m_coffee", "650", "4"},
            {"pay_005", "cust_alice", "m_rides", "1250", "5"},
            {"pay_006", "cust_bob", "m_rides", "2300", "6"},
            {"pay_007", "cust_dave", "m_books", "9999", "7"},
            {"pay_008", "cust_alice", "m_grocer", "8210", "8"},
            {"pay_009", "cust_carol", "m_books", "1500", "9"},
            {"pay_010", "cust_erin", "m_coffee", "525", "10"},
            {"pay_011", "cust_alice", "m_coffee", "480", "11"},
            {"pay_012", "cust_bob", "m_grocer", "6100", "12"},
        };
        for (String[] s : spec) {
            rows.add(new Payment(s[0], s[1], s[2], Money.ofMinor(Long.parseLong(s[3]), usd), Long.parseLong(s[4])));
        }
        return rows;
    }

    static Money total(List<Payment> payments) {
        Money sum = Money.ofMinor(0, Currency.USD);
        for (Payment p : payments) sum = sum.plus(p.amount());
        return sum;
    }

    static String placement(PartitionedStore store, String customerId) {
        TreeMap<Integer, List<String>> byShard = new TreeMap<>();
        for (int i = 0; i < store.shardCount(); i++) {
            for (Payment p : store.shard(i).lookupCustomer(customerId)) {
                byShard.computeIfAbsent(i, k -> new ArrayList<>()).add(p.id());
            }
        }
        return byShard.toString();
    }

    public static void main(String[] args) {
        System.out.println("== Placement: payments hash to shards by payment id, not by customer ==");
        PartitionedStore sync = new PartitionedStore(4, 2, IndexMode.SYNC);
        for (Payment p : sampleData()) sync.put(p);
        int syncShardWrites = sync.totalShardRequests();
        int syncIndexWrites = sync.globalIndex().totalRequests();
        for (int i = 0; i < 4; i++) System.out.printf("shard %d holds %d rows%n", i, sync.shard(i).size());
        System.out.println("cust_alice rows by shard: " + placement(sync, "cust_alice"));
        System.out.println("cust_erin rows by shard:  " + placement(sync, "cust_erin"));

        System.out.println();
        System.out.println("== Query: all payments for cust_alice ==");
        int before = sync.totalShardRequests();
        QueryResult scatter = sync.findByCustomerScatterGather("cust_alice");
        System.out.printf("scatter-gather over local indexes: %d rows, %d shard requests, %d index requests, total %s%n",
                scatter.payments().size(), scatter.shardRequests(), scatter.indexRequests(), total(scatter.payments()));
        QueryResult global = sync.findByCustomerGlobal("cust_alice");
        System.out.printf("global index then fetch:           %d rows, %d shard requests, %d index requests, total %s%n",
                global.payments().size(), global.shardRequests(), global.indexRequests(), total(global.payments()));
        System.out.printf("same answer: %s; shard requests consumed by both queries: %d%n",
                scatter.payments().equals(global.payments()), sync.totalShardRequests() - before);

        System.out.println();
        System.out.println("== Query: cust_erin, a customer with a single payment ==");
        scatter = sync.findByCustomerScatterGather("cust_erin");
        global = sync.findByCustomerGlobal("cust_erin");
        System.out.printf("scatter-gather: %d requests; global: %d requests (1 index + %d shard)%n",
                scatter.totalRequests(), global.totalRequests(), global.shardRequests());

        System.out.println();
        System.out.println("== Write cost: SYNC index pays on every put ==");
        System.out.printf("SYNC store after 12 puts: shard requests %d, index-partition requests %d%n",
                syncShardWrites, syncIndexWrites);
        PartitionedStore async = new PartitionedStore(4, 2, IndexMode.ASYNC);
        for (Payment p : sampleData()) async.put(p);
        System.out.printf("ASYNC store after 12 puts: shard requests %d, index-partition requests %d, queued updates %d%n",
                async.totalShardRequests(), async.globalIndex().totalRequests(), async.pendingIndexUpdates());

        System.out.println();
        System.out.println("== Staleness window: the row is durable, the index has not heard yet ==");
        Payment fresh = new Payment("pay_013", "cust_erin", "m_rides", Money.ofMinor(1800, Currency.USD), 13);
        async.drainAll();
        async.put(fresh);
        System.out.printf("get(pay_013) by primary key: %s%n", async.get("pay_013") != null ? "found" : "missing");
        System.out.printf("global index query for cust_erin: %d rows (expected 2)  <- stale, %d update(s) queued%n",
                async.findByCustomerGlobal("cust_erin").payments().size(), async.pendingIndexUpdates());
        System.out.printf("scatter-gather for cust_erin:     %d rows (local indexes are never stale)%n",
                async.findByCustomerScatterGather("cust_erin").payments().size());
        async.drainAll();
        System.out.printf("after drain: global index query for cust_erin: %d rows%n",
                async.findByCustomerGlobal("cust_erin").payments().size());

        System.out.println();
        System.out.println("== Dangling entry: delete the row, index update still queued ==");
        async.delete("pay_013");
        System.out.printf("index entries for cust_erin: %d, pending updates: %d%n",
                async.globalIndex().lookup("cust_erin").size(), async.pendingIndexUpdates());
        IndexAudit audit = async.auditGlobalIndex();
        System.out.printf("audit: %d entries, %d dangling, %d missing%n", audit.entries(), audit.dangling().size(), audit.missing().size());
        System.out.printf("query filters the ghost: %d rows returned for cust_erin%n",
                async.findByCustomerGlobal("cust_erin").payments().size());
        async.drainAll();
        audit = async.auditGlobalIndex();
        System.out.printf("after drain: %d dangling, %d missing, consistent=%s%n",
                audit.dangling().size(), audit.missing().size(), audit.consistent());

        System.out.println();
        System.out.println("== Bootstrap: rebuild the global index from a scan of every shard ==");
        int rebuilt = async.rebuildGlobalIndex();
        System.out.printf("rebuilt %d entries from %d shards; consistent=%s%n",
                rebuilt, async.shardCount(), async.auditGlobalIndex().consistent());
    }
}
