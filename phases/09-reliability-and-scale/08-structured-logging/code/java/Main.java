// Lesson: phases/09-reliability-and-scale/08-structured-logging/docs/en.md
// Structured Logging and Log Correlation: JSON records, bound context, trace correlation, PAN/secret redaction, sampling, query
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: PCI DSS v4.0 Requirement 3.3/3.4 (PAN rendering and storage); RFC 8259 (JSON); RFC 5424 (syslog severity levels);
//          W3C Trace Context; OpenTelemetry Logs Data Model. Java 21, standard library only.

import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/** Injected time source. */
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

/** Severity in increasing order. Numeric codes follow RFC 5424 so a reader can compare across systems. */
enum Level {
    DEBUG(7), INFO(6), WARN(4), ERROR(3);
    final int syslog;
    Level(int syslog) { this.syslog = syslog; }
    boolean atLeast(Level other) { return this.ordinal() >= other.ordinal(); }
}

/** One log event. Fields keep insertion order so the serialized form is stable and diffable. */
final class LogRecord {
    final long timestampMillis;
    final Level level;
    final String service;
    final String message;
    final Map<String, String> fields;

    LogRecord(long timestampMillis, Level level, String service, String message, Map<String, String> fields) {
        this.timestampMillis = timestampMillis;
        this.level = level;
        this.service = service;
        this.message = message;
        this.fields = Collections.unmodifiableMap(new LinkedHashMap<>(fields));
    }

    String field(String key) { return fields.get(key); }

    /** Hand-written JSON (RFC 8259): one line, fixed key order, every string escaped. */
    String toJson() {
        StringBuilder sb = new StringBuilder(160);
        sb.append('{');
        Json.pair(sb, "ts", Long.toString(timestampMillis), false).append(',');
        Json.pair(sb, "level", level.name(), true).append(',');
        Json.pair(sb, "service", service, true).append(',');
        Json.pair(sb, "msg", message, true);
        for (Map.Entry<String, String> e : fields.entrySet()) {
            sb.append(',');
            Json.pair(sb, e.getKey(), e.getValue(), true);
        }
        return sb.append('}').toString();
    }
}

final class Json {
    static StringBuilder pair(StringBuilder sb, String key, String value, boolean quoteValue) {
        sb.append('"').append(escape(key)).append("\":");
        if (value == null) return sb.append("null");
        return quoteValue ? sb.append('"').append(escape(value)).append('"') : sb.append(value);
    }

    static String escape(String s) {
        StringBuilder out = new StringBuilder(s.length() + 8);
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            switch (c) {
                case '"' -> out.append("\\\"");
                case '\\' -> out.append("\\\\");
                case '\n' -> out.append("\\n");
                case '\r' -> out.append("\\r");
                case '\t' -> out.append("\\t");
                default -> {
                    if (c < 0x20) out.append(String.format("\\u%04x", (int) c));
                    else out.append(c);
                }
            }
        }
        return out.toString();
    }
}

/**
 * Removes card numbers and secrets before a record is written. Two rules:
 *  1. Any field whose key names a secret is replaced wholesale.
 *  2. Any run of 13-19 digits (allowing spaces or dashes) that passes the Luhn check is masked to first six + last four,
 *     the maximum PCI DSS permits to be displayed.
 */
final class Redactor {
    static final Set<String> SECRET_KEYS = Set.of("password", "secret", "token", "cvv", "cvc", "api_key", "authorization", "pan");
    private static final Pattern CANDIDATE = Pattern.compile("(?<!\\d)\\d(?:[ -]?\\d){12,18}(?!\\d)");
    int redactions;

    String redactValue(String key, String value) {
        if (value == null) return null;
        if (SECRET_KEYS.contains(key.toLowerCase())) {
            redactions++;
            return "[REDACTED]";
        }
        return maskPans(value);
    }

    String maskPans(String text) {
        Matcher m = CANDIDATE.matcher(text);
        StringBuilder out = new StringBuilder();
        int last = 0;
        while (m.find()) {
            String digits = m.group().replaceAll("[ -]", "");
            if (luhnValid(digits)) {
                out.append(text, last, m.start()).append(mask(digits));
                last = m.end();
                redactions++;
            }
        }
        out.append(text.substring(last));
        return out.toString();
    }

    static String mask(String digits) {
        return digits.substring(0, 6) + "*".repeat(digits.length() - 10) + digits.substring(digits.length() - 4);
    }

    /** Luhn mod-10 (ISO/IEC 7812-1). Rejects most random digit runs so order ids and timestamps are left alone. */
    static boolean luhnValid(String digits) {
        int sum = 0;
        boolean dbl = false;
        for (int i = digits.length() - 1; i >= 0; i--) {
            int d = digits.charAt(i) - '0';
            if (dbl) { d *= 2; if (d > 9) d -= 9; }
            sum += d;
            dbl = !dbl;
        }
        return sum % 10 == 0;
    }
}

/**
 * Keeps or drops a record. ERROR and WARN are always kept. DEBUG and INFO are kept for 1 in `rate` traces, decided by
 * hashing the trace id, so a trace is logged completely or not at all. Records without a trace id use the message hash.
 */
final class Sampler {
    private final int debugRate;
    private final int infoRate;
    int dropped;

    Sampler(int debugRate, int infoRate) {
        if (debugRate < 1 || infoRate < 1) throw new IllegalArgumentException("rates are 1-in-N, N >= 1");
        this.debugRate = debugRate;
        this.infoRate = infoRate;
    }

    boolean keep(Level level, String traceId, String message) {
        if (level.atLeast(Level.WARN)) return true;
        int rate = level == Level.DEBUG ? debugRate : infoRate;
        if (rate == 1) return true;
        String key = traceId != null ? traceId : message;
        boolean keep = Math.floorMod(fnv1a(key), rate) == 0;
        if (!keep) dropped++;
        return keep;
    }

    /** FNV-1a: stable across JVM runs, unlike String.hashCode in principle and unlike identity hashes in practice. */
    static int fnv1a(String s) {
        int h = 0x811c9dc5;
        for (int i = 0; i < s.length(); i++) { h ^= s.charAt(i); h *= 0x01000193; }
        return h;
    }
}

/** In-memory destination with an index by field value: the part of a log platform the lesson needs. */
final class LogStore {
    private final List<LogRecord> records = new ArrayList<>();
    private final Map<String, Map<String, List<LogRecord>>> index = new LinkedHashMap<>();   // field -> value -> records
    private final Set<String> indexed;

    LogStore(Set<String> indexedFields) { this.indexed = indexedFields; }

    void write(LogRecord r) {
        records.add(r);
        for (String f : indexed) {
            String v = r.field(f);
            if (v != null) index.computeIfAbsent(f, k -> new LinkedHashMap<>()).computeIfAbsent(v, k -> new ArrayList<>()).add(r);
        }
    }

    List<LogRecord> query(String field, String value) {
        return index.getOrDefault(field, Map.of()).getOrDefault(value, List.of());
    }

    List<LogRecord> all() { return Collections.unmodifiableList(records); }
    int size() { return records.size(); }
}

/**
 * The logger a service holds. `with` returns a child logger carrying bound fields (payment_id, trace_id, span_id)
 * so every record emitted inside a request scope carries them without each call site repeating them.
 */
final class Logger {
    private final String service;
    private final Clock clock;
    private final Level minimum;
    private final Redactor redactor;
    private final Sampler sampler;
    private final LogStore store;
    private final Map<String, String> bound;

    Logger(String service, Clock clock, Level minimum, Redactor redactor, Sampler sampler, LogStore store) {
        this(service, clock, minimum, redactor, sampler, store, new LinkedHashMap<>());
    }

    private Logger(String service, Clock clock, Level minimum, Redactor redactor, Sampler sampler, LogStore store, Map<String, String> bound) {
        this.service = service;
        this.clock = clock;
        this.minimum = minimum;
        this.redactor = redactor;
        this.sampler = sampler;
        this.store = store;
        this.bound = bound;
    }

    Logger with(String key, String value) {
        Map<String, String> next = new LinkedHashMap<>(bound);
        next.put(key, value);
        return new Logger(service, clock, minimum, redactor, sampler, store, next);
    }

    Logger withTrace(String traceId, String spanId) {
        return with("trace_id", traceId).with("span_id", spanId);
    }

    void debug(String msg, String... kv) { log(Level.DEBUG, msg, kv); }
    void info(String msg, String... kv) { log(Level.INFO, msg, kv); }
    void warn(String msg, String... kv) { log(Level.WARN, msg, kv); }
    void error(String msg, String... kv) { log(Level.ERROR, msg, kv); }

    void log(Level level, String message, String... kv) {
        if (kv.length % 2 != 0) throw new IllegalArgumentException("fields must be key/value pairs");
        if (!level.atLeast(minimum)) return;
        if (!sampler.keep(level, bound.get("trace_id"), message)) return;
        Map<String, String> fields = new LinkedHashMap<>();
        for (Map.Entry<String, String> e : bound.entrySet()) fields.put(e.getKey(), redactor.redactValue(e.getKey(), e.getValue()));
        for (int i = 0; i < kv.length; i += 2) fields.put(kv[i], redactor.redactValue(kv[i], kv[i + 1]));
        store.write(new LogRecord(clock.nowMillis(), level, service, redactor.maskPans(message), fields));
    }
}

/** Six services logging one payment, with the trace ids from Lesson 07 bound into every record. */
final class PaymentFlow {
    final FakeClock clock = new FakeClock(1_700_000_000_000L);
    final Redactor redactor = new Redactor();
    final LogStore store = new LogStore(Set.of("payment_id", "trace_id", "merchant_id"));
    final Sampler sampler;
    final Level minimum;

    PaymentFlow(Sampler sampler, Level minimum) {
        this.sampler = sampler;
        this.minimum = minimum;
    }

    Logger logger(String service) { return new Logger(service, clock, minimum, redactor, sampler, store); }

    void charge(String traceId, String paymentId, String merchantId, long amountMinor, String currency, String rawPan, boolean processorFails) {
        Logger gw = logger("api-gateway").withTrace(traceId, "a1a1a1a1a1a1a1a1").with("payment_id", paymentId).with("merchant_id", merchantId);
        gw.info("request received", "method", "POST", "path", "/charges");
        clock.advance(2);

        Logger pay = logger("payments").withTrace(traceId, "b2b2b2b2b2b2b2b2").with("payment_id", paymentId).with("merchant_id", merchantId);
        pay.debug("idempotency key checked", "idempotency_key", "idem_" + paymentId);
        pay.info("authorize started", "amount_minor", Long.toString(amountMinor), "currency", currency);
        // A careless developer logs the raw request body. The redactor must catch the PAN.
        pay.debug("raw request body", "body", "{\"card\":\"" + rawPan + "\",\"amount\":" + amountMinor + "}");
        clock.advance(3);

        Logger risk = logger("risk").withTrace(traceId, "c3c3c3c3c3c3c3c3").with("payment_id", paymentId);
        risk.info("score computed", "score", "12", "decision", "APPROVE");
        clock.advance(23);

        Logger ledger = logger("ledger").withTrace(traceId, "d4d4d4d4d4d4d4d4").with("payment_id", paymentId);
        ledger.info("hold posted", "amount_minor", Long.toString(amountMinor), "currency", currency, "journal_id", "jrn_" + paymentId);
        clock.advance(6);

        Logger proc = logger("processor-adapter").withTrace(traceId, "e5e5e5e5e5e5e5e5").with("payment_id", paymentId);
        proc.debug("iso8583 message built", "mti", "0100", "pan", rawPan);
        clock.advance(45);
        if (processorFails) {
            proc.error("processor timeout", "processor", "acq-01", "timeout_ms", "45", "retryable", "true");
            Logger again = logger("ledger").withTrace(traceId, "d4d4d4d4d4d4d4d4").with("payment_id", paymentId);
            again.warn("hold reversed", "reason", "processor_timeout", "journal_id", "jrn_" + paymentId);
        } else {
            proc.info("authorization approved", "processor", "acq-01", "auth_code", "ZX91Q2", "pan", rawPan);
        }
        clock.advance(4);

        Logger notif = logger("notifications").withTrace(traceId, "f6f6f6f6f6f6f6f6").with("payment_id", paymentId);
        notif.info("webhook enqueued", "event", processorFails ? "payment.failed" : "payment.authorized", "secret", "whsec_live_0123456789");
        clock.advance(3);
    }
}

public final class Main {
    public static void main(String[] args) {
        System.out.println("== Unstructured vs structured ==");
        System.out.println("unstructured: 2026-10-13 14:02:11 INFO Charged 19.99 for pay_8f3k2 (merchant acme) ok");
        PaymentFlow flow = new PaymentFlow(new Sampler(1, 1), Level.DEBUG);
        flow.charge("4bf92f3577b34da6a3ce929d0e0e4736", "pay_8f3k2", "acme", 1999, "USD", "4111 1111 1111 1111", false);
        System.out.println("structured:   " + flow.store.all().get(1).toJson());
        System.out.println();

        System.out.println("== Every record for payment pay_8f3k2, across six services, in order ==");
        for (LogRecord r : flow.store.query("payment_id", "pay_8f3k2")) System.out.println(r.toJson());
        System.out.println();

        System.out.println("== Redaction ==");
        System.out.println("records written: " + flow.store.size() + ", redactions applied: " + flow.redactor.redactions);
        Redactor r = new Redactor();
        System.out.println("PAN in free text:    " + r.maskPans("declined card 4111111111111111 for order 1234567890123"));
        System.out.println("order id left alone: " + r.maskPans("order 1234567890123 shipped") + "  (fails Luhn)");
        System.out.println("secret by key:       " + r.redactValue("api_key", "sk_live_abcdef"));
        System.out.println();

        System.out.println("== A failed payment: ERROR and WARN survive sampling, DEBUG/INFO are sampled 1-in-4 traces ==");
        PaymentFlow sampled = new PaymentFlow(new Sampler(4, 4), Level.DEBUG);
        String[] traces = {"0af7651916cd43dd8448eb211c80319c", "1af7651916cd43dd8448eb211c80319c", "2af7651916cd43dd8448eb211c80319c", "3af7651916cd43dd8448eb211c80319c"};
        for (int i = 0; i < traces.length; i++) {
            sampled.charge(traces[i], "pay_s" + i, "acme", 500 + i, "EUR", "5555555555554444", i == 2);
        }
        System.out.println("records kept: " + sampled.store.size() + " of " + (sampled.store.size() + sampled.sampler.dropped) + " emitted");
        for (String t : traces) {
            List<LogRecord> recs = sampled.store.query("trace_id", t);
            long errors = recs.stream().filter(x -> x.level.atLeast(Level.WARN)).count();
            System.out.printf("trace %s...: %2d records kept, %d WARN/ERROR%n", t.substring(0, 8), recs.size(), errors);
        }
        System.out.println();
        System.out.println("trace 1af76519... was selected by the hash, so all nine of its records survive; the other traces kept only WARN/ERROR");
        System.out.println();
        System.out.println("== Query: what happened to pay_s2? (its INFO/DEBUG were sampled out; the failure was not) ==");
        for (LogRecord rec : sampled.store.query("payment_id", "pay_s2")) {
            System.out.printf("%-18s %-5s %s%n", rec.service, rec.level, rec.message);
        }
    }
}
