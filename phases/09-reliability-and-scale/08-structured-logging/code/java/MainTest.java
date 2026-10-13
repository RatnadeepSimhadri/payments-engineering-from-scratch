// Tests for phases/09-reliability-and-scale/08-structured-logging. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Set;

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
        // --- JSON serialization ---
        LogRecord rec = new LogRecord(1700000000000L, Level.INFO, "payments", "he said \"hi\"\n", Map.of("k", "v\\w"));
        String json = rec.toJson();
        check("json starts with ts as a number", json.startsWith("{\"ts\":1700000000000,"));
        check("json escapes quotes and newlines in the message", json.contains("\"msg\":\"he said \\\"hi\\\"\\n\""));
        check("json escapes backslashes in values", json.contains("\"k\":\"v\\\\w\""));
        check("json is one line", !json.contains("\n"));
        check("control characters become \\u escapes", Json.escape("a\u0001b").equals("a\\u0001b"));
        check("null value serializes as null", Json.pair(new StringBuilder(), "x", null, true).toString().equals("\"x\":null"));

        // --- levels ---
        check("ERROR is at least WARN", Level.ERROR.atLeast(Level.WARN));
        check("DEBUG is not at least INFO", !Level.DEBUG.atLeast(Level.INFO));
        check("syslog codes follow RFC 5424", Level.ERROR.syslog == 3 && Level.DEBUG.syslog == 7);

        // --- redaction ---
        Redactor red = new Redactor();
        check("Luhn accepts a valid test PAN", Redactor.luhnValid("4111111111111111"));
        check("Luhn rejects a near miss", !Redactor.luhnValid("4111111111111112"));
        check("mask keeps first six and last four", Redactor.mask("4111111111111111").equals("411111******1111"));
        check("PAN with spaces is found and masked", red.maskPans("card 4111 1111 1111 1111 ok").equals("card 411111******1111 ok"));
        check("PAN with dashes is found and masked", red.maskPans("5555-5555-5555-4444").equals("555555******4444"));
        check("13-digit PAN is masked", red.maskPans("4222222222222").equals("422222***2222"));
        check("non-Luhn digit run is left alone", red.maskPans("order 1234567890123 shipped").equals("order 1234567890123 shipped"));
        check("12-digit number is too short to be a PAN", red.maskPans("ref 411111111111").equals("ref 411111111111"));
        check("digits embedded in a longer run are not a PAN", red.maskPans("id 94111111111111111").equals("id 94111111111111111"));
        check("two PANs in one message are both masked", red.maskPans("4111111111111111 and 5555555555554444").equals("411111******1111 and 555555******4444"));
        check("secret-named field is replaced wholesale", red.redactValue("api_key", "sk_live_x").equals("[REDACTED]"));
        check("secret key match is case-insensitive", red.redactValue("Authorization", "Bearer abc").equals("[REDACTED]"));
        check("ordinary field keeps its value", red.redactValue("merchant_id", "acme").equals("acme"));
        check("ordinary field still gets PAN masking", red.redactValue("note", "pan 4111111111111111").equals("pan 411111******1111"));
        check("redaction count tracks events", red.redactions == 8);

        // --- sampler ---
        Sampler s = new Sampler(4, 4);
        check("ERROR is always kept", s.keep(Level.ERROR, "0af7651916cd43dd8448eb211c80319c", "x"));
        check("WARN is always kept", s.keep(Level.WARN, "0af7651916cd43dd8448eb211c80319c", "x"));
        check("1-in-4 keeps the hash-selected trace", s.keep(Level.INFO, "1af7651916cd43dd8448eb211c80319c", "x"));
        check("1-in-4 drops a non-selected trace", !s.keep(Level.INFO, "0af7651916cd43dd8448eb211c80319c", "x"));
        check("decision is per trace, not per record", s.keep(Level.DEBUG, "1af7651916cd43dd8448eb211c80319c", "other") && !s.keep(Level.DEBUG, "0af7651916cd43dd8448eb211c80319c", "other"));
        check("rate 1 keeps everything", new Sampler(1, 1).keep(Level.DEBUG, "0af7651916cd43dd8448eb211c80319c", "x"));
        check("FNV-1a is stable", Sampler.fnv1a("payments") == Sampler.fnv1a("payments") && Sampler.fnv1a("a") != Sampler.fnv1a("b"));
        checkThrows("rate below 1 rejected", IllegalArgumentException.class, () -> new Sampler(0, 1));

        // --- logger: bound context, minimum level, store index ---
        FakeClock clock = new FakeClock(5);
        LogStore store = new LogStore(Set.of("payment_id", "trace_id"));
        Logger base = new Logger("ledger", clock, Level.INFO, new Redactor(), new Sampler(1, 1), store);
        Logger scoped = base.withTrace("4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7").with("payment_id", "pay_1");
        scoped.debug("not written: below minimum");
        scoped.info("hold posted", "amount_minor", "1999", "currency", "USD");
        base.info("no context here");
        check("minimum level filters DEBUG", store.size() == 2);
        LogRecord posted = store.query("payment_id", "pay_1").get(0);
        check("bound fields appear on the record", "4bf92f3577b34da6a3ce929d0e0e4736".equals(posted.field("trace_id")) && "00f067aa0ba902b7".equals(posted.field("span_id")));
        check("call-site fields appear after bound fields", posted.toJson().indexOf("\"payment_id\"") < posted.toJson().indexOf("\"amount_minor\""));
        check("child logger does not mutate the parent", store.query("payment_id", "pay_1").size() == 1 && store.all().get(1).field("payment_id") == null);
        check("query by trace id finds the same record", store.query("trace_id", "4bf92f3577b34da6a3ce929d0e0e4736").get(0) == posted);
        check("query on an unindexed value is empty", store.query("payment_id", "nope").isEmpty());
        check("timestamp comes from the injected clock", posted.timestampMillis == 5);
        checkThrows("odd key/value list rejected", IllegalArgumentException.class, () -> scoped.info("bad", "only_key"));

        // --- end to end: six services, one payment id ---
        PaymentFlow flow = new PaymentFlow(new Sampler(1, 1), Level.DEBUG);
        flow.charge("4bf92f3577b34da6a3ce929d0e0e4736", "pay_8f3k2", "acme", 1999, "USD", "4111 1111 1111 1111", false);
        List<LogRecord> byPayment = flow.store.query("payment_id", "pay_8f3k2");
        check("every record carries the payment id", byPayment.size() == flow.store.size() && byPayment.size() == 9);
        check("records are in time order", isSorted(byPayment));
        Set<String> services = new java.util.HashSet<>();
        for (LogRecord r : byPayment) services.add(r.service);
        check("six services appear", services.size() == 6);
        check("every record carries the trace id", byPayment.stream().allMatch(r -> "4bf92f3577b34da6a3ce929d0e0e4736".equals(r.field("trace_id"))));
        String everything = String.join("\n", byPayment.stream().map(LogRecord::toJson).toList());
        check("raw PAN never reaches the store", !everything.contains("4111111111111111") && !everything.contains("4111 1111 1111 1111"));
        check("PAN inside a JSON body field is masked", everything.contains("411111******1111"));
        check("pan-named field is redacted wholesale", byPayment.stream().filter(r -> r.field("pan") != null).allMatch(r -> r.field("pan").equals("[REDACTED]")));
        check("webhook secret is redacted", everything.contains("\"secret\":\"[REDACTED]\"") && !everything.contains("whsec_live"));
        check("amounts are logged as minor-unit strings, never floats", everything.contains("\"amount_minor\":\"1999\"") && !everything.contains("19.99"));

        // --- sampling preserves failures ---
        PaymentFlow sampled = new PaymentFlow(new Sampler(4, 4), Level.DEBUG);
        sampled.charge("0af7651916cd43dd8448eb211c80319c", "pay_s0", "acme", 500, "EUR", "5555555555554444", false);
        sampled.charge("2af7651916cd43dd8448eb211c80319c", "pay_s2", "acme", 502, "EUR", "5555555555554444", true);
        check("unsampled successful trace leaves no records", sampled.store.query("trace_id", "0af7651916cd43dd8448eb211c80319c").isEmpty());
        List<LogRecord> failed = sampled.store.query("payment_id", "pay_s2");
        check("failure's ERROR and WARN survive sampling", failed.size() == 2 && failed.get(0).level == Level.ERROR && failed.get(1).level == Level.WARN);
        check("dropped count accounts for every missing record", sampled.sampler.dropped + sampled.store.size() == 19);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }

    static boolean isSorted(List<LogRecord> recs) {
        for (int i = 1; i < recs.size(); i++) if (recs.get(i).timestampMillis < recs.get(i - 1).timestampMillis) return false;
        return true;
    }
}
