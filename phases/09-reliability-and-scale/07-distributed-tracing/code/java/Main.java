// Lesson: phases/09-reliability-and-scale/07-distributed-tracing/docs/en.md
// Distributed Tracing and Context Propagation: trace/span ids, W3C traceparent, parent/child spans, exclusive time
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: W3C Trace Context Level 1 (https://www.w3.org/TR/trace-context/); OpenTelemetry Tracing API spec;
//          Sigelman et al., "Dapper, a Large-Scale Distributed Systems Tracing Infrastructure" (Google, 2010). Java 21, stdlib only.

import java.util.ArrayList;
import java.util.Collections;
import java.util.Comparator;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Random;

/** Injected time source. Spans record start and end from it; tests advance it by exact amounts. */
interface Clock {
    long nowMillis();
}

final class FakeClock implements Clock {
    private long now;
    FakeClock(long start) { this.now = start; }
    @Override public long nowMillis() { return now; }
    void advance(long millis) {
        if (millis < 0) throw new IllegalArgumentException("time does not go backwards");
        now += millis;
    }
}

/** Seeded id generator: 16-byte trace ids and 8-byte span ids as lowercase hex, never all zeros. */
final class IdGenerator {
    private final Random random;
    IdGenerator(long seed) { this.random = new Random(seed); }

    String traceId() { return hex(16); }
    String spanId() { return hex(8); }

    private String hex(int bytes) {
        while (true) {
            byte[] buf = new byte[bytes];
            random.nextBytes(buf);
            StringBuilder sb = new StringBuilder(bytes * 2);
            boolean allZero = true;
            for (byte b : buf) {
                if (b != 0) allZero = false;
                sb.append(Character.forDigit((b >> 4) & 0xF, 16)).append(Character.forDigit(b & 0xF, 16));
            }
            if (!allZero) return sb.toString();
        }
    }
}

/**
 * The part of a span that crosses process boundaries. Serialized as the W3C traceparent header:
 * version-traceid-parentid-flags, e.g. 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01
 */
record SpanContext(String traceId, String spanId, boolean sampled) {
    static final String HEADER = "traceparent";
    static final String STATE_HEADER = "tracestate";

    SpanContext {
        if (!isHex(traceId, 32) || traceId.equals("0".repeat(32))) throw new IllegalArgumentException("bad trace id: " + traceId);
        if (!isHex(spanId, 16) || spanId.equals("0".repeat(16))) throw new IllegalArgumentException("bad span id: " + spanId);
    }

    static boolean isHex(String s, int len) {
        if (s == null || s.length() != len) return false;
        for (int i = 0; i < len; i++) {
            char c = s.charAt(i);
            if (!((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'))) return false;
        }
        return true;
    }

    String toTraceparent() {
        return "00-" + traceId + "-" + spanId + "-" + (sampled ? "01" : "00");
    }

    /** Strict parse per W3C Trace Context Level 1; returns empty on any violation so the receiver starts a new trace. */
    static Optional<SpanContext> parseTraceparent(String header) {
        if (header == null) return Optional.empty();
        String h = header.trim();
        String[] parts = h.split("-", -1);
        if (parts.length < 4) return Optional.empty();
        String version = parts[0];
        if (!isHex(version, 2) || version.equals("ff")) return Optional.empty();
        if (version.equals("00") && parts.length != 4) return Optional.empty();   // version 00 has exactly four fields
        if (!isHex(parts[1], 32) || parts[1].equals("0".repeat(32))) return Optional.empty();
        if (!isHex(parts[2], 16) || parts[2].equals("0".repeat(16))) return Optional.empty();
        if (!isHex(parts[3], 2)) return Optional.empty();
        boolean sampled = (Integer.parseInt(parts[3], 16) & 0x01) == 1;
        return Optional.of(new SpanContext(parts[1], parts[2], sampled));
    }
}

/** One unit of work. Mutable while open; immutable in effect once ended. */
final class Span {
    final String traceId;
    final String spanId;
    final String parentSpanId;     // null for a root span
    final String service;
    final String name;
    final long startMillis;
    long endMillis = -1;
    final Map<String, String> attributes = new LinkedHashMap<>();
    String status = "OK";

    Span(String traceId, String spanId, String parentSpanId, String service, String name, long startMillis) {
        this.traceId = traceId;
        this.spanId = spanId;
        this.parentSpanId = parentSpanId;
        this.service = service;
        this.name = name;
        this.startMillis = startMillis;
    }

    boolean isRoot() { return parentSpanId == null; }
    boolean ended() { return endMillis >= 0; }
    long durationMillis() {
        if (!ended()) throw new IllegalStateException("span still open: " + name);
        return endMillis - startMillis;
    }
    SpanContext context(boolean sampled) { return new SpanContext(traceId, spanId, sampled); }
    void setAttribute(String key, String value) { attributes.put(key, value); }
    void setError(String message) { status = "ERROR"; attributes.put("error.message", message); }
}

/** Receives finished spans. In production this is the exporter to Jaeger, Tempo, or a vendor; here it is a list. */
final class Collector {
    private final List<Span> spans = new ArrayList<>();
    void export(Span span) {
        if (!span.ended()) throw new IllegalStateException("cannot export an open span");
        spans.add(span);
    }
    List<Span> spansFor(String traceId) {
        List<Span> out = new ArrayList<>();
        for (Span s : spans) if (s.traceId.equals(traceId)) out.add(s);
        return out;
    }
    List<Span> all() { return Collections.unmodifiableList(spans); }
    java.util.Set<String> traceIds() {
        java.util.Set<String> ids = new java.util.LinkedHashSet<>();
        for (Span s : spans) ids.add(s.traceId);
        return ids;
    }
    int size() { return spans.size(); }
}

/** Creates spans in one service. Every service in the demo has its own Tracer sharing one Collector. */
final class Tracer {
    private final String service;
    private final Clock clock;
    private final IdGenerator ids;
    private final Collector collector;

    Tracer(String service, Clock clock, IdGenerator ids, Collector collector) {
        this.service = service;
        this.clock = clock;
        this.ids = ids;
        this.collector = collector;
    }

    /** Start a root span: a new trace id, no parent. */
    Span startRoot(String name) {
        return new Span(ids.traceId(), ids.spanId(), null, service, name, clock.nowMillis());
    }

    /** Start a child of an incoming context: same trace id, parent = the caller's span id. */
    Span startChild(String name, SpanContext parent) {
        return new Span(parent.traceId(), ids.spanId(), parent.spanId(), service, name, clock.nowMillis());
    }

    /** Start a span from a carrier; if the header is missing or malformed, a new trace begins (and the break is visible). */
    Span startFromCarrier(String name, Map<String, String> carrier) {
        Optional<SpanContext> parent = SpanContext.parseTraceparent(carrier.get(SpanContext.HEADER));
        return parent.map(ctx -> startChild(name, ctx)).orElseGet(() -> startRoot(name));
    }

    void end(Span span) {
        span.endMillis = clock.nowMillis();
        collector.export(span);
    }
}

/** Injects and extracts the W3C headers into a carrier (HTTP headers, Kafka record headers, a queue message's properties). */
final class Propagator {
    static Map<String, String> inject(SpanContext ctx, String tracestate) {
        Map<String, String> carrier = new HashMap<>();
        carrier.put(SpanContext.HEADER, ctx.toTraceparent());
        if (tracestate != null && !tracestate.isEmpty()) carrier.put(SpanContext.STATE_HEADER, tracestate);
        return carrier;
    }
}

/** A trace reassembled from its spans: tree structure, exclusive time, orphans, and a text timeline. */
final class TraceTree {
    final String traceId;
    final List<Span> spans;
    final Map<String, Span> byId = new HashMap<>();
    final Map<String, List<Span>> children = new HashMap<>();
    final List<Span> roots = new ArrayList<>();
    final List<Span> orphans = new ArrayList<>();

    TraceTree(String traceId, List<Span> spans) {
        this.traceId = traceId;
        this.spans = new ArrayList<>(spans);
        this.spans.sort(Comparator.comparingLong((Span s) -> s.startMillis).thenComparing(s -> s.spanId));
        for (Span s : this.spans) byId.put(s.spanId, s);
        for (Span s : this.spans) {
            if (s.isRoot()) roots.add(s);
            else if (byId.containsKey(s.parentSpanId)) children.computeIfAbsent(s.parentSpanId, k -> new ArrayList<>()).add(s);
            else orphans.add(s);   // parent never arrived: a service dropped the header or its span was lost
        }
    }

    List<Span> childrenOf(Span s) { return children.getOrDefault(s.spanId, Collections.emptyList()); }

    /** Time spent in this span itself, not in any child: duration minus the union of child intervals. */
    long exclusiveMillis(Span s) {
        List<Span> kids = new ArrayList<>(childrenOf(s));
        kids.sort(Comparator.comparingLong(k -> k.startMillis));
        long covered = 0, cursor = s.startMillis;
        for (Span k : kids) {
            long from = Math.max(cursor, k.startMillis), to = Math.min(s.endMillis, k.endMillis);
            if (to > from) { covered += to - from; cursor = to; }
        }
        return s.durationMillis() - covered;
    }

    int depth(Span s) {
        int d = 0;
        Span cur = s;
        while (cur.parentSpanId != null && byId.containsKey(cur.parentSpanId)) { cur = byId.get(cur.parentSpanId); d++; }
        return d;
    }

    /** The span with the largest exclusive time: where the trace actually spent its wall clock. */
    Span hottest() {
        Span best = null;
        for (Span s : spans) if (best == null || exclusiveMillis(s) > exclusiveMillis(best)) best = s;
        return best;
    }

    /** Text timeline: one row per span, depth-indented, with a bar scaled to the root's duration. */
    String renderTimeline(int width) {
        if (roots.isEmpty()) return "(no root span for trace " + traceId + ")";
        Span root = roots.get(0);
        long origin = root.startMillis, total = Math.max(1, root.durationMillis());
        StringBuilder sb = new StringBuilder();
        sb.append("trace ").append(traceId).append("  total ").append(total).append(" ms\n");
        sb.append(String.format("%-46s %7s %6s  %s%n", "service / span", "start", "dur", "timeline"));
        for (Span s : spans) {
            if (!s.isRoot() && !byId.containsKey(s.parentSpanId)) continue;   // orphans listed separately
            String label = "  ".repeat(depth(s)) + s.service + " / " + s.name;
            int from = (int) ((s.startMillis - origin) * width / total);
            int len = Math.max(1, (int) (s.durationMillis() * width / total));
            String bar = " ".repeat(Math.min(from, width)) + "#".repeat(Math.min(len, width - Math.min(from, width)));
            sb.append(String.format("%-46s %5d ms %4d ms  %s%s%n", label, s.startMillis - origin, s.durationMillis(), bar,
                    s.status.equals("ERROR") ? "  ERROR" : ""));
        }
        for (Span o : orphans) {
            sb.append(String.format("ORPHAN %s / %s (parent %s never arrived)%n", o.service, o.name, o.parentSpanId));
        }
        return sb.toString();
    }
}

/**
 * The six simulated services of a card payment. Each one extracts the context from the incoming carrier,
 * starts a child span, does some clock-advancing "work", and injects a fresh carrier for its downstream calls.
 */
final class PaymentSystem {
    final FakeClock clock;
    final Collector collector = new Collector();
    final IdGenerator ids;
    final Map<String, Tracer> tracers = new LinkedHashMap<>();
    boolean notificationsDropsHeader = false;     // fault switch for the demo: the notifications consumer "forgets" to copy the header

    PaymentSystem(long seed) {
        this.clock = new FakeClock(1_700_000_000_000L);
        this.ids = new IdGenerator(seed);
        for (String svc : new String[] {"api-gateway", "payments", "risk", "ledger", "processor-adapter", "notifications"}) {
            tracers.put(svc, new Tracer(svc, clock, ids, collector));
        }
    }

    Tracer tracer(String svc) { return tracers.get(svc); }

    /** Entry point: an HTTP request with (possibly) a traceparent from the merchant's client. Returns the trace id. */
    String charge(Map<String, String> incoming, String paymentId, long amountMinor, String currency) {
        Tracer gw = tracer("api-gateway");
        Span root = gw.startFromCarrier("POST /charges", incoming);
        root.setAttribute("payment.id", paymentId);
        root.setAttribute("http.method", "POST");
        clock.advance(2);                                           // auth, parsing
        payments(Propagator.inject(root.context(true), incoming.get(SpanContext.STATE_HEADER)), paymentId, amountMinor, currency);
        clock.advance(1);                                           // serialize response
        gw.end(root);
        return root.traceId;
    }

    void payments(Map<String, String> carrier, String paymentId, long amountMinor, String currency) {
        Tracer t = tracer("payments");
        Span s = t.startFromCarrier("authorize", carrier);
        s.setAttribute("payment.id", paymentId);
        s.setAttribute("amount.minor", Long.toString(amountMinor));
        s.setAttribute("currency", currency);
        clock.advance(3);                                           // load merchant config, idempotency check
        String decision = risk(Propagator.inject(s.context(true), carrier.get(SpanContext.STATE_HEADER)), paymentId);
        s.setAttribute("risk.decision", decision);
        clock.advance(1);
        ledger(Propagator.inject(s.context(true), null), paymentId, amountMinor);
        clock.advance(1);
        processor(Propagator.inject(s.context(true), null), paymentId);
        clock.advance(2);
        notifications(Propagator.inject(s.context(true), null), paymentId);
        clock.advance(1);
        t.end(s);
    }

    String risk(Map<String, String> carrier, String paymentId) {
        Tracer t = tracer("risk");
        Span s = t.startFromCarrier("score", carrier);
        clock.advance(4);                                           // feature lookup
        Span model = t.startChild("model.infer", s.context(true));
        clock.advance(18);                                          // the slow part
        t.end(model);
        clock.advance(1);
        t.end(s);
        return "APPROVE";
    }

    void ledger(Map<String, String> carrier, String paymentId, long amountMinor) {
        Tracer t = tracer("ledger");
        Span s = t.startFromCarrier("post-hold", carrier);
        s.setAttribute("amount.minor", Long.toString(amountMinor));
        clock.advance(6);                                           // write journal entries
        t.end(s);
    }

    void processor(Map<String, String> carrier, String paymentId) {
        Tracer t = tracer("processor-adapter");
        Span s = t.startFromCarrier("iso8583.auth", carrier);
        clock.advance(2);
        Span net = t.startChild("network.roundtrip", s.context(true));
        clock.advance(45);                                          // the card network
        t.end(net);
        clock.advance(2);
        t.end(s);
    }

    void notifications(Map<String, String> carrier, String paymentId) {
        Tracer t = tracer("notifications");
        // Header-drop fault: when enabled, the carrier is replaced with an empty one, as if a queue consumer never copied it.
        Span s = t.startFromCarrier("enqueue-webhook", notificationsDropsHeader ? new HashMap<>() : carrier);
        clock.advance(3);
        t.end(s);
    }
}

public final class Main {
    public static void main(String[] args) {
        System.out.println("== W3C traceparent round trip ==");
        SpanContext ctx = new SpanContext("4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7", true);
        String header = ctx.toTraceparent();
        System.out.println("header:  " + header);
        System.out.println("parsed:  " + SpanContext.parseTraceparent(header).orElseThrow());
        System.out.println("bad:     " + SpanContext.parseTraceparent("00-" + "0".repeat(32) + "-00f067aa0ba902b7-01").isPresent() + "  (all-zero trace id rejected)");
        System.out.println();

        System.out.println("== One payment through six services (seed 42) ==");
        PaymentSystem sys = new PaymentSystem(42);
        Map<String, String> incoming = new HashMap<>();
        incoming.put(SpanContext.STATE_HEADER, "merchant=acme");
        String traceId = sys.charge(incoming, "pay_8f3k2", 1999, "USD");
        TraceTree tree = new TraceTree(traceId, sys.collector.spansFor(traceId));
        System.out.print(tree.renderTimeline(48));
        System.out.println();

        Span root = tree.roots.get(0);
        System.out.println("spans exported: " + tree.spans.size() + ", root duration: " + root.durationMillis() + " ms");
        long sumExclusive = 0;
        for (Span s : tree.spans) sumExclusive += tree.exclusiveMillis(s);
        System.out.println("sum of exclusive times: " + sumExclusive + " ms  (equals root duration when children are sequential)");
        Span hot = tree.hottest();
        System.out.println("hottest span: " + hot.service + " / " + hot.name + " with " + tree.exclusiveMillis(hot) + " ms exclusive");
        System.out.println();

        System.out.println("== The same payment when notifications drops the header (seed 42) ==");
        PaymentSystem broken = new PaymentSystem(42);
        broken.notificationsDropsHeader = true;
        String t2 = broken.charge(new HashMap<>(), "pay_8f3k2", 1999, "USD");
        TraceTree brokenTree = new TraceTree(t2, broken.collector.spansFor(t2));
        System.out.println("spans in trace " + t2.substring(0, 8) + "...: " + brokenTree.spans.size() + " (one fewer)");
        System.out.println("total traces in collector: " + broken.collector.traceIds().size() + " (the notification span became its own root)");
        System.out.println();

        System.out.println("== Context extracted from a merchant-supplied traceparent ==");
        PaymentSystem joined = new PaymentSystem(7);
        Map<String, String> fromMerchant = Propagator.inject(new SpanContext("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", true), "vendor=xyz");
        String t3 = joined.charge(fromMerchant, "pay_join1", 500, "EUR");
        Span gateway = new TraceTree(t3, joined.collector.spansFor(t3)).spans.get(0);
        System.out.println("gateway span joined trace " + t3 + " with parent " + gateway.parentSpanId);
    }
}
