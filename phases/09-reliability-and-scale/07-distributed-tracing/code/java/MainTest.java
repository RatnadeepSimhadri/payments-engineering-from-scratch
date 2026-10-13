// Tests for phases/09-reliability-and-scale/07-distributed-tracing. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;

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

    public static void main(String[] args) {
        // --- ids ---
        IdGenerator ids = new IdGenerator(1);
        String tid = ids.traceId(), sid = ids.spanId();
        check("trace id is 32 lowercase hex chars", SpanContext.isHex(tid, 32));
        check("span id is 16 lowercase hex chars", SpanContext.isHex(sid, 16));
        check("same seed gives same ids", new IdGenerator(1).traceId().equals(tid));
        check("different seeds give different ids", !new IdGenerator(2).traceId().equals(tid));

        // --- traceparent ---
        SpanContext ctx = new SpanContext("4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7", true);
        check("traceparent format", ctx.toTraceparent().equals("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"));
        check("unsampled flag is 00", new SpanContext(ctx.traceId(), ctx.spanId(), false).toTraceparent().endsWith("-00"));
        Optional<SpanContext> parsed = SpanContext.parseTraceparent(ctx.toTraceparent());
        check("round trip preserves ids and flag", parsed.isPresent() && parsed.get().equals(ctx));
        check("parse trims whitespace", SpanContext.parseTraceparent("  " + ctx.toTraceparent() + " ").isPresent());
        check("parse reads sampled bit from flags with other bits set", SpanContext.parseTraceparent("00-" + ctx.traceId() + "-" + ctx.spanId() + "-03").get().sampled());
        check("reject version ff", SpanContext.parseTraceparent("ff-" + ctx.traceId() + "-" + ctx.spanId() + "-01").isEmpty());
        check("reject all-zero trace id", SpanContext.parseTraceparent("00-" + "0".repeat(32) + "-" + ctx.spanId() + "-01").isEmpty());
        check("reject all-zero span id", SpanContext.parseTraceparent("00-" + ctx.traceId() + "-" + "0".repeat(16) + "-01").isEmpty());
        check("reject uppercase hex", SpanContext.parseTraceparent("00-" + ctx.traceId().toUpperCase() + "-" + ctx.spanId() + "-01").isEmpty());
        check("reject short trace id", SpanContext.parseTraceparent("00-abc-" + ctx.spanId() + "-01").isEmpty());
        check("reject version 00 with extra field", SpanContext.parseTraceparent(ctx.toTraceparent() + "-extra").isEmpty());
        check("accept future version with extra field", SpanContext.parseTraceparent("01-" + ctx.traceId() + "-" + ctx.spanId() + "-01-future").isPresent());
        check("null header is empty", SpanContext.parseTraceparent(null).isEmpty());
        checkThrows("constructor rejects bad trace id", IllegalArgumentException.class, () -> new SpanContext("zz", ctx.spanId(), true));

        // --- spans across six services ---
        PaymentSystem sys = new PaymentSystem(42);
        Map<String, String> incoming = new HashMap<>();
        incoming.put(SpanContext.STATE_HEADER, "merchant=acme");
        String traceId = sys.charge(incoming, "pay_1", 1999, "USD");
        List<Span> spans = sys.collector.spansFor(traceId);
        TraceTree tree = new TraceTree(traceId, spans);
        check("eight spans in one trace", spans.size() == 8);
        check("all spans share the trace id", spans.stream().allMatch(s -> s.traceId.equals(traceId)));
        check("exactly one root", tree.roots.size() == 1 && tree.roots.get(0).service.equals("api-gateway"));
        check("no orphans when every hop propagates", tree.orphans.isEmpty());
        java.util.Set<String> services = new java.util.HashSet<>();
        for (Span s : spans) services.add(s.service);
        check("six distinct services", services.size() == 6);
        Span root = tree.roots.get(0);
        Span payments = tree.childrenOf(root).get(0);
        check("payments is child of gateway", payments.service.equals("payments") && payments.parentSpanId.equals(root.spanId));
        check("payments has four direct children", tree.childrenOf(payments).size() == 4);
        check("model.infer is at depth 3", tree.spans.stream().filter(s -> s.name.equals("model.infer")).allMatch(s -> tree.depth(s) == 3));
        check("child starts after parent and ends before it",
                tree.spans.stream().filter(s -> !s.isRoot()).allMatch(s -> {
                    Span p = tree.byId.get(s.parentSpanId);
                    return s.startMillis >= p.startMillis && s.endMillis <= p.endMillis;
                }));
        check("root duration is 92 ms", root.durationMillis() == 92);

        // --- exclusive time ---
        long sumExclusive = 0;
        for (Span s : tree.spans) sumExclusive += tree.exclusiveMillis(s);
        check("exclusive times sum to root duration for sequential children", sumExclusive == root.durationMillis());
        check("gateway exclusive is its own 3 ms", tree.exclusiveMillis(root) == 3);
        check("hottest span is the card network round trip", tree.hottest().name.equals("network.roundtrip") && tree.exclusiveMillis(tree.hottest()) == 45);
        check("attributes carried", payments.attributes.get("payment.id").equals("pay_1") && payments.attributes.get("risk.decision").equals("APPROVE"));

        // --- timeline ---
        String timeline = tree.renderTimeline(40);
        check("timeline names every service", services.stream().allMatch(timeline::contains));
        check("timeline shows total", timeline.contains("total 92 ms"));
        check("timeline indents by depth", timeline.contains("      risk / model.infer"));

        // --- determinism ---
        PaymentSystem again = new PaymentSystem(42);
        String t2 = again.charge(new HashMap<>(incoming), "pay_1", 1999, "USD");
        check("same seed reproduces the trace id", t2.equals(traceId));

        // --- broken propagation ---
        PaymentSystem broken = new PaymentSystem(42);
        broken.notificationsDropsHeader = true;
        String t3 = broken.charge(new HashMap<>(), "pay_1", 1999, "USD");
        check("dropped header: one span missing from the trace", broken.collector.spansFor(t3).size() == 7);
        check("dropped header: the lost span became a second trace", broken.collector.traceIds().size() == 2);

        // --- orphan detection when a parent span is lost in transit ---
        List<Span> partial = new ArrayList<>(spans);
        partial.removeIf(s -> s.name.equals("score"));
        TraceTree partialTree = new TraceTree(traceId, partial);
        check("missing parent yields exactly one orphan", partialTree.orphans.size() == 1 && partialTree.orphans.get(0).name.equals("model.infer"));
        check("timeline reports the orphan", partialTree.renderTimeline(40).contains("ORPHAN risk / model.infer"));

        // --- joining a merchant-supplied trace, tracestate passthrough ---
        PaymentSystem joined = new PaymentSystem(7);
        Map<String, String> fromMerchant = Propagator.inject(new SpanContext("a".repeat(32), "b".repeat(16), true), "vendor=xyz");
        String t4 = joined.charge(fromMerchant, "pay_2", 500, "EUR");
        check("gateway joins the incoming trace", t4.equals("a".repeat(32)));
        Span gw = new TraceTree(t4, joined.collector.spansFor(t4)).spans.get(0);
        check("gateway's parent is the merchant's span id", "b".repeat(16).equals(gw.parentSpanId));
        check("tracestate is forwarded unchanged", Propagator.inject(gw.context(true), fromMerchant.get(SpanContext.STATE_HEADER)).get("tracestate").equals("vendor=xyz"));
        check("inject omits empty tracestate", !Propagator.inject(gw.context(true), null).containsKey("tracestate"));

        // --- lifecycle guards ---
        Tracer t = joined.tracer("ledger");
        Span open = t.startRoot("open");
        checkThrows("duration of an open span is an error", IllegalStateException.class, open::durationMillis);
        checkThrows("exporting an open span is an error", IllegalStateException.class, () -> joined.collector.export(open));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
