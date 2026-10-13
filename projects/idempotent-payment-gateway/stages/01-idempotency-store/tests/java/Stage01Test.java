// Stage 1 tests for projects/idempotent-payment-gateway: SHA-256 fingerprints, canonical form, and the
// NEW / IN_PROGRESS / REPLAY / CONFLICT decisions of the idempotency store with TTL on an injected clock.
// Run by scripts/project_test.py; exits 1 on failure.
import java.util.ArrayList;
import java.util.List;

public final class Stage01Test {
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
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ": " + t.getMessage() + ")");
        }
    }

    static ChargeRequest s1Request(String key, String amount) {
        return new ChargeRequest("m_1", key, Money.parse(amount, Currency.USD), "tok_visa", "order-42");
    }

    public static void main(String[] args) {
        // Fingerprints: SHA-256 (FIPS 180-4 test vector) over a canonical string that excludes the key
        check("sha256 of abc", Fingerprint.sha256Hex("abc").equals("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"));
        check("sha256 of empty string", Fingerprint.sha256Hex("").equals("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"));
        ChargeRequest req = s1Request("key-1", "19.99");
        check("canonical form", req.canonical().equals("1999\nUSD\ntok_visa\norder-42"));
        check("fingerprint is sha256 of canonical", req.fingerprint().equals(Fingerprint.sha256Hex(req.canonical())));
        check("fingerprint known value", req.fingerprint().equals("485e792d29b86326c3335f0b5e84d3a948d47121f7e4c4b57f889d7287e90b91"));
        check("fingerprint ignores the key", s1Request("key-2", "19.99").fingerprint().equals(req.fingerprint()));
        check("fingerprint changes with amount", !s1Request("key-1", "29.99").fingerprint().equals(req.fingerprint()));
        check("fingerprint changes with currency", !new ChargeRequest("m_1", "key-1", Money.parse("19.99", Currency.EUR), "tok_visa", "order-42").fingerprint().equals(req.fingerprint()));
        check("fingerprint changes with description", !new ChargeRequest("m_1", "key-1", Money.parse("19.99", Currency.USD), "tok_visa", "order-43").fingerprint().equals(req.fingerprint()));

        // Request validation
        checkThrows("zero amount rejected", IllegalArgumentException.class, () -> s1Request("key-1", "0.00"));
        checkThrows("negative amount rejected", IllegalArgumentException.class, () -> s1Request("key-1", "-1.00"));
        checkThrows("blank key rejected", IllegalArgumentException.class, () -> new ChargeRequest("m_1", " ", Money.parse("1.00", Currency.USD), "tok_visa", "x"));
        checkThrows("newline in description rejected", IllegalArgumentException.class, () -> new ChargeRequest("m_1", "k", Money.parse("1.00", Currency.USD), "tok_visa", "a\nb"));

        // Store decisions
        FakeClock clock = new FakeClock(1_000_000);
        IdempotencyStore store = new IdempotencyStore(clock, 60_000);
        checkThrows("ttl must be positive", IllegalArgumentException.class, () -> new IdempotencyStore(clock, 0));
        check("first begin is NEW", store.begin("m_1", "key-1", req.fingerprint()).decision() == Decision.NEW);
        check("NEW carries no response", store.begin("m_1", "key-9", req.fingerprint()).response() == null);
        check("second begin while open is IN_PROGRESS", store.begin("m_1", "key-1", req.fingerprint()).decision() == Decision.IN_PROGRESS);
        check("record is IN_PROGRESS", store.get("m_1", "key-1").map(r -> r.state() == KeyState.IN_PROGRESS).orElse(false));
        check("record expiry is created + ttl", store.get("m_1", "key-1").map(r -> r.expiresAt() == 1_060_000 && r.createdAt() == 1_000_000).orElse(false));
        check("different merchant, same key is NEW", store.begin("m_2", "key-1", req.fingerprint()).decision() == Decision.NEW);
        check("different fingerprint while open is CONFLICT", store.begin("m_1", "key-1", s1Request("key-1", "29.99").fingerprint()).decision() == Decision.CONFLICT);

        ApiResponse stored = new ApiResponse(201, "{\"payment_id\":\"pay_1\"}");
        store.complete("m_1", "key-1", stored);
        BeginResult replay = store.begin("m_1", "key-1", req.fingerprint());
        check("completed key replays", replay.decision() == Decision.REPLAY);
        check("replay returns the stored status and body", replay.response() != null && replay.response().status() == 201 && replay.response().body().equals(stored.body()));
        check("stored response is not marked replayed", replay.response() != null && !replay.response().replayed());
        check("asReplay flags the copy", replay.response().asReplay().replayed() && replay.response().asReplay().body().equals(stored.body()));
        check("replay is stable", store.begin("m_1", "key-1", req.fingerprint()).decision() == Decision.REPLAY);
        check("different fingerprint after completion is CONFLICT", store.begin("m_1", "key-1", s1Request("key-1", "29.99").fingerprint()).decision() == Decision.CONFLICT);
        check("record is COMPLETED", store.get("m_1", "key-1").map(r -> r.state() == KeyState.COMPLETED).orElse(false));

        // complete / release misuse
        checkThrows("complete twice", IllegalStateException.class, () -> store.complete("m_1", "key-1", stored));
        checkThrows("complete unknown key", IllegalStateException.class, () -> store.complete("m_1", "nope", stored));
        checkThrows("release completed key", IllegalStateException.class, () -> store.release("m_1", "key-1"));
        check("release unknown key is false", !store.release("m_1", "nope"));
        check("release in-progress key is true", store.release("m_1", "key-9"));
        check("released key is absent", store.get("m_1", "key-9").isEmpty());
        check("released key can be claimed again", store.begin("m_1", "key-9", req.fingerprint()).decision() == Decision.NEW);

        // TTL on the injected clock
        clock.advance(59_999);
        check("one ms before expiry still replays", store.begin("m_1", "key-1", req.fingerprint()).decision() == Decision.REPLAY);
        clock.advance(1);
        check("expired record reads as absent", store.get("m_1", "key-1").isEmpty());
        check("expired key begins fresh as NEW", store.begin("m_1", "key-1", s1Request("key-1", "29.99").fingerprint()).decision() == Decision.NEW);
        check("fresh record has the new fingerprint", store.get("m_1", "key-1").map(r -> r.fingerprint().equals(s1Request("key-1", "29.99").fingerprint())).orElse(false));
        check("size counts stale records", store.size() == 3);
        int purged = store.purgeExpired();
        check("purge removes exactly the expired records", purged == 2);
        check("size after purge", store.size() == 1);
        clock.advance(60_000);
        check("purge removes everything once all expire", store.purgeExpired() == 1 && store.size() == 0);

        System.out.println("Stage01Test: " + passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL " + f);
        if (!failures.isEmpty()) System.exit(1);
    }
}
