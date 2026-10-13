// Tests for phases/07-storage-and-transactions/10-secondary-indexes. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
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

    static Payment pay(String id, String customer, long cents, long at) {
        return new Payment(id, customer, "m_test", Money.ofMinor(cents, Currency.USD), at);
    }

    static List<String> ids(QueryResult r) {
        List<String> out = new ArrayList<>();
        for (Payment p : r.payments()) out.add(p.id());
        return out;
    }

    public static void main(String[] args) {
        // hashing is deterministic and well-defined on every key
        check("fnv1a64 of empty string is the offset basis", Hashing.fnv1a64("") == 0xcbf29ce484222325L);
        check("fnv1a64 of 'a' matches the published vector", Hashing.fnv1a64("a") == 0xaf63dc4c8601ec8cL);
        check("partitionFor is stable", Hashing.partitionFor("pay_001", 4) == Hashing.partitionFor("pay_001", 4));
        check("partitionFor is in range for negative hashes",
                Hashing.partitionFor("pay_002", 4) >= 0 && Hashing.partitionFor("pay_002", 4) < 4);

        // primary-key access is one shard request; the local index is updated in the same write
        PartitionedStore sync = new PartitionedStore(4, 2, IndexMode.SYNC);
        for (Payment p : Main.sampleData()) sync.put(p);
        int before = sync.totalShardRequests();
        check("get by primary key finds the row", sync.get("pay_005").customerId().equals("cust_alice"));
        check("get by primary key costs exactly one shard request", sync.totalShardRequests() - before == 1);
        check("row lives on the shard the hash says", sync.shard(sync.shardFor("pay_005")).get("pay_005") != null);
        check("get of unknown id returns null", sync.get("pay_999") == null);

        // both strategies return the same rows in the same order
        QueryResult scatter = sync.findByCustomerScatterGather("cust_alice");
        QueryResult global = sync.findByCustomerGlobal("cust_alice");
        check("scatter-gather finds all five alice payments", scatter.payments().size() == 5);
        check("global finds the same five", global.payments().equals(scatter.payments()));
        check("results ordered by createdAt",
                ids(scatter).equals(List.of("pay_001", "pay_003", "pay_005", "pay_008", "pay_011")));
        check("scatter-gather always costs N shard requests", scatter.shardRequests() == 4 && scatter.indexRequests() == 0);
        check("global costs one index request", global.indexRequests() == 1);
        check("global shard requests equal distinct shards holding hits", global.shardRequests() <= 4 && global.shardRequests() >= 1);

        QueryResult erinScatter = sync.findByCustomerScatterGather("cust_erin");
        QueryResult erinGlobal = sync.findByCustomerGlobal("cust_erin");
        check("single-payment customer: scatter still 4 requests", erinScatter.totalRequests() == 4);
        check("single-payment customer: global is 2 requests", erinGlobal.totalRequests() == 2 && erinGlobal.payments().size() == 1);
        check("unknown customer: global returns empty with no shard fetch",
                sync.findByCustomerGlobal("cust_nobody").payments().isEmpty()
                        && sync.findByCustomerGlobal("cust_nobody").shardRequests() == 0);
        check("unknown customer: scatter returns empty", sync.findByCustomerScatterGather("cust_nobody").payments().isEmpty());

        // sync mode: every put also writes the index partition, and the index is never behind
        check("SYNC: 12 puts produce 12 index-partition writes", sync.globalIndex().totalRequests() >= 12);
        check("SYNC: nothing queued", sync.pendingIndexUpdates() == 0);
        check("SYNC: index audit is consistent", sync.auditGlobalIndex().consistent());
        check("SYNC: index has one entry per row", sync.globalIndex().entryCount() == 12);

        // async mode: puts are cheap, and the index lags by the queue depth
        PartitionedStore async = new PartitionedStore(4, 2, IndexMode.ASYNC);
        for (Payment p : Main.sampleData()) async.put(p);
        check("ASYNC: no index writes during puts", async.globalIndex().totalRequests() == 0);
        check("ASYNC: 12 updates queued", async.pendingIndexUpdates() == 12);
        check("ASYNC: global query before drain sees nothing", async.findByCustomerGlobal("cust_alice").payments().isEmpty());
        check("ASYNC: scatter-gather before drain sees everything", async.findByCustomerScatterGather("cust_alice").payments().size() == 5);
        check("ASYNC: audit reports 12 missing before drain", async.auditGlobalIndex().missing().size() == 12);
        check("ASYNC: partial drain applies in order", async.drainIndexUpdates(3) == 3 && async.pendingIndexUpdates() == 9);
        check("ASYNC: drainAll applies the rest", async.drainAll() == 9 && async.pendingIndexUpdates() == 0);
        check("ASYNC: after drain global matches scatter",
                async.findByCustomerGlobal("cust_alice").payments().equals(async.findByCustomerScatterGather("cust_alice").payments()));
        check("ASYNC: audit consistent after drain", async.auditGlobalIndex().consistent());

        // staleness window on a fresh write
        async.put(pay("pay_013", "cust_erin", 1800, 13));
        check("fresh row readable by primary key", async.get("pay_013") != null);
        check("fresh row invisible to global index until drain", async.findByCustomerGlobal("cust_erin").payments().size() == 1);
        check("fresh row visible to local index immediately", async.findByCustomerScatterGather("cust_erin").payments().size() == 2);
        async.drainAll();
        check("fresh row visible to global index after drain", async.findByCustomerGlobal("cust_erin").payments().size() == 2);

        // dangling entries: delete then query before the index hears about it
        check("delete returns true for existing row", async.delete("pay_013"));
        check("delete returns false for missing row", !async.delete("pay_013"));
        check("index still holds the locator before drain", async.globalIndex().lookup("cust_erin").size() == 2);
        check("audit sees one dangling entry", async.auditGlobalIndex().dangling().size() == 1);
        check("query filters the dangling locator", async.findByCustomerGlobal("cust_erin").payments().size() == 1);
        async.drainAll();
        check("drain removes the dangling locator", async.globalIndex().lookup("cust_erin").size() == 1);
        check("local index drops the deleted row", async.shard(async.shardFor("pay_013")).lookupCustomer("cust_erin").isEmpty()
                || async.shard(async.shardFor("pay_013")).lookupCustomer("cust_erin").stream().noneMatch(p -> p.id().equals("pay_013")));

        // changing the indexed attribute emits a delete for the old key and an insert for the new one
        PartitionedStore move = new PartitionedStore(2, 2, IndexMode.ASYNC);
        move.put(pay("pay_100", "cust_x", 100, 1));
        move.drainAll();
        move.put(pay("pay_100", "cust_y", 100, 1));
        check("re-attribution queues two updates", move.pendingIndexUpdates() == 2);
        check("before drain old customer still indexed (stale)", move.globalIndex().lookup("cust_x").size() == 1);
        check("before drain audit: 1 dangling, 1 missing",
                move.auditGlobalIndex().dangling().size() == 1 && move.auditGlobalIndex().missing().size() == 1);
        move.drainAll();
        check("after drain old customer gone", move.globalIndex().lookup("cust_x").isEmpty());
        check("after drain new customer indexed", move.globalIndex().lookup("cust_y").size() == 1);
        check("local index also moved", move.findByCustomerScatterGather("cust_x").payments().isEmpty()
                && move.findByCustomerScatterGather("cust_y").payments().size() == 1);
        check("same-customer overwrite queues nothing", moveNoop());

        // rebuild from scan restores a corrupted or empty index
        PartitionedStore rebuild = new PartitionedStore(3, 2, IndexMode.ASYNC);
        for (Payment p : Main.sampleData()) rebuild.put(p);
        check("rebuild returns one entry per row", rebuild.rebuildGlobalIndex() == 12);
        check("rebuild discards the queue", rebuild.pendingIndexUpdates() == 0);
        check("rebuild yields a consistent index", rebuild.auditGlobalIndex().consistent());
        check("rebuild is idempotent", rebuild.rebuildGlobalIndex() == 12 && rebuild.globalIndex().entryCount() == 12);

        // all locators for one customer land in one index partition
        GlobalIndex gi = rebuild.globalIndex();
        int p0 = gi.partitionFor("cust_alice");
        int reqBefore = gi.requests(p0);
        gi.lookup("cust_alice");
        check("customer lookup hits exactly its own partition", gi.requests(p0) == reqBefore + 1);

        // money discipline and construction guards
        check("Money toString", Money.ofMinor(12414, Currency.USD).toString().equals("124.14 USD"));
        checkThrows("Money refuses to mix currencies", IllegalArgumentException.class,
                () -> Money.ofMinor(1, Currency.USD).plus(Money.ofMinor(1, Currency.EUR)));
        checkThrows("store needs at least one shard", IllegalArgumentException.class,
                () -> new PartitionedStore(0, 1, IndexMode.SYNC));
        checkThrows("index needs at least one partition", IllegalArgumentException.class,
                () -> new GlobalIndex(0));
        checkThrows("payment requires a customer", NullPointerException.class,
                () -> new Payment("p", null, "m", Money.ofMinor(1, Currency.USD), 1));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }

    static boolean moveNoop() {
        PartitionedStore s = new PartitionedStore(2, 2, IndexMode.ASYNC);
        s.put(pay("pay_200", "cust_x", 100, 1));
        s.drainAll();
        s.put(pay("pay_200", "cust_x", 250, 1));
        return s.pendingIndexUpdates() == 0 && s.get("pay_200").amount().minorUnits() == 250;
    }
}
