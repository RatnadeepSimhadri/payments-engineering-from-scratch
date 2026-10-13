// Lesson: phases/11-payment-rails/05-tokenization-and-pci-scope/docs/en.md
// Tokenization and PCI DSS Scope Reduction
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: PCI DSS v4.0.1 (Req. 3, 7, 10); PCI SSC Tokenization Product Security Guidelines (2015);
//          NIST SP 800-38D (AES-GCM); ISO/IEC 7812 (PAN structure); RFC 2104 (HMAC). Java 21, stdlib only.

import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.security.GeneralSecurityException;
import java.security.MessageDigest;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.EnumSet;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Random;
import java.util.Set;
import javax.crypto.Cipher;
import javax.crypto.Mac;
import javax.crypto.spec.GCMParameterSpec;
import javax.crypto.spec.SecretKeySpec;

/** Injected time source so expiry is testable without sleeping. Seconds since the Unix epoch. */
interface Clock {
    long nowEpochSeconds();
}

final class FixedClock implements Clock {
    private long now;
    FixedClock(long start) { this.now = start; }
    @Override public long nowEpochSeconds() { return now; }
    void advanceSeconds(long seconds) { now += seconds; }
}

/**
 * A primary account number that has been validated (12-19 digits, Luhn-correct) and that can never be
 * printed by accident: toString, and therefore string concatenation and loggers, produce the mask.
 * The digits are only reachable through {@link #digits()}, which is the single call site reviewers grep for.
 */
final class Pan {
    private final String digits;

    private Pan(String digits) { this.digits = digits; }

    static Pan of(String raw) {
        String s = raw.replace(" ", "").replace("-", "");
        if (s.length() < 12 || s.length() > 19 || !s.chars().allMatch(Character::isDigit)) {
            throw new IllegalArgumentException("PAN must be 12-19 digits");
        }
        if (!Luhn.isValid(s)) throw new IllegalArgumentException("PAN fails the Luhn check");
        return new Pan(s);
    }

    /** The full PAN. Only the vault's encrypt path and an authorized detokenize caller should call this. */
    String digits() { return digits; }
    String lastFour() { return digits.substring(digits.length() - 4); }
    String bin() { return digits.substring(0, 6); }
    int length() { return digits.length(); }

    /** First six and last four visible, as PCI DSS Requirement 3.4 allows for display; the rest masked. */
    String masked() {
        StringBuilder sb = new StringBuilder(bin());
        for (int i = 6; i < digits.length() - 4; i++) sb.append('*');
        return sb.append(lastFour()).toString();
    }

    @Override public String toString() { return masked(); }
    @Override public boolean equals(Object o) { return o instanceof Pan p && MessageDigest.isEqual(digits.getBytes(StandardCharsets.US_ASCII), p.digits.getBytes(StandardCharsets.US_ASCII)); }
    @Override public int hashCode() { return digits.hashCode(); }
}

/** ISO/IEC 7812 Annex B check digit. Also used to make tokens deliberately Luhn-invalid. */
final class Luhn {
    private Luhn() {}

    static boolean isValid(String digits) {
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

enum TokenStatus { ACTIVE, SUSPENDED, REVOKED }

/** What a caller is allowed to do. Tokenize is cheap to grant; detokenize is the privilege that matters. */
enum Scope { TOKENIZE, DETOKENIZE, MANAGE }

record Principal(String id, Set<Scope> scopes) {
    static Principal of(String id, Scope... scopes) {
        return new Principal(id, scopes.length == 0 ? EnumSet.noneOf(Scope.class) : EnumSet.copyOf(Arrays.asList(scopes)));
    }
    boolean has(Scope s) { return scopes.contains(s); }
}

final class AuthorizationException extends RuntimeException {
    AuthorizationException(String message) { super(message); }
}

final class TokenStateException extends IllegalStateException {
    TokenStateException(String message) { super(message); }
}

/** Everything the vault stores per token. Note what is absent: the PAN in clear, in any field. */
final class TokenRecord {
    final String token;
    final byte[] nonce;        // 12 bytes, unique per key: 4 random + 8 counter
    final byte[] ciphertext;   // AES-256-GCM output: PAN bytes || 16-byte tag; AAD = token
    final String lastFour;
    final int panLength;
    final long createdAt;
    final long expiresAt;
    TokenStatus status;

    TokenRecord(String token, byte[] nonce, byte[] ciphertext, String lastFour, int panLength, long createdAt, long expiresAt) {
        this.token = token; this.nonce = nonce; this.ciphertext = ciphertext; this.lastFour = lastFour;
        this.panLength = panLength; this.createdAt = createdAt; this.expiresAt = expiresAt; this.status = TokenStatus.ACTIVE;
    }
}

/** One line of the access log. Tokens and principals are safe to log; PANs never appear here. */
record AuditEntry(long at, String principal, String action, String token, boolean allowed, String detail) {
    @Override public String toString() {
        return at + " " + principal + " " + action + " " + token + " " + (allowed ? "ALLOW" : "DENY") + (detail.isEmpty() ? "" : " " + detail);
    }
}

/**
 * The token vault: the one system in the estate that holds PANs, and it holds them only under AES-GCM.
 *
 * Invariants:
 *  1. Tokens preserve length and last four, and fail Luhn, so a token can never be mistaken for a PAN.
 *  2. The same PAN tokenizes to the same token (lookup through an HMAC index, never a plaintext PAN).
 *  3. Ciphertext is bound to its token through GCM additional authenticated data; moving a record breaks it.
 *  4. Detokenize requires Scope.DETOKENIZE and an ACTIVE, unexpired token; every attempt is audited.
 *  5. Revoke destroys the ciphertext (crypto-shred); the record remains as a tombstone for audit.
 */
final class TokenVault {
    static final int GCM_TAG_BITS = 128;
    static final int NONCE_BYTES = 12;

    private final SecretKeySpec dataKey;      // AES-256 data-encrypting key (DEK)
    private final SecretKeySpec indexKey;     // HMAC-SHA256 key for the PAN -> token index
    private final Random random;              // injected: seeded in tests, SecureRandom in production
    private final Clock clock;
    private final long ttlSeconds;
    private final Map<String, TokenRecord> byToken = new HashMap<>();
    private final Map<String, String> tokenByPanHmac = new HashMap<>();
    private final List<AuditEntry> audit = new ArrayList<>();
    private long nonceCounter = 0;

    TokenVault(byte[] dataKey, byte[] indexKey, Random random, Clock clock, long ttlSeconds) {
        if (dataKey.length != 32) throw new IllegalArgumentException("data key must be 32 bytes (AES-256)");
        if (indexKey.length < 32) throw new IllegalArgumentException("index key must be at least 32 bytes");
        this.dataKey = new SecretKeySpec(dataKey, "AES");
        this.indexKey = new SecretKeySpec(indexKey, "HmacSHA256");
        this.random = Objects.requireNonNull(random);
        this.clock = Objects.requireNonNull(clock);
        this.ttlSeconds = ttlSeconds;
    }

    // ---- tokenize -------------------------------------------------------------------------------

    /** Returns the existing token for this PAN or mints a new one. Idempotent per PAN. */
    String tokenize(Pan pan, Principal who) {
        if (!who.has(Scope.TOKENIZE)) {
            log(who, "TOKENIZE", "-", false, "missing scope TOKENIZE");
            throw new AuthorizationException(who.id() + " may not tokenize");
        }
        String index = hmacHex(pan.digits());
        String existing = tokenByPanHmac.get(index);
        if (existing != null && byToken.get(existing).status != TokenStatus.REVOKED) {
            log(who, "TOKENIZE", existing, true, "existing");
            return existing;
        }
        String token = mintToken(pan);
        long now = clock.nowEpochSeconds();
        byte[] nonce = nextNonce();
        byte[] ciphertext = encrypt(pan.digits().getBytes(StandardCharsets.US_ASCII), nonce, token);
        byToken.put(token, new TokenRecord(token, nonce, ciphertext, pan.lastFour(), pan.length(), now, now + ttlSeconds));
        tokenByPanHmac.put(index, token);
        log(who, "TOKENIZE", token, true, "new");
        return token;
    }

    /** Format-preserving: same length, same last four, random middle, and Luhn-INVALID by construction. */
    String mintToken(Pan pan) {
        for (int attempt = 0; attempt < 100; attempt++) {
            char[] t = new char[pan.length()];
            for (int i = 0; i < t.length - 4; i++) t[i] = (char) ('0' + random.nextInt(10));
            pan.lastFour().getChars(0, 4, t, t.length - 4);
            String candidate = new String(t);
            if (Luhn.isValid(candidate)) {
                // bump one non-preserved digit: a single-digit change always flips the Luhn sum mod 10
                int pos = t.length - 5;
                t[pos] = (char) ('0' + ((t[pos] - '0' + 1) % 10));
                candidate = new String(t);
            }
            if (!byToken.containsKey(candidate)) return candidate;
        }
        throw new IllegalStateException("token space exhausted for this last-four");
    }

    // ---- detokenize -----------------------------------------------------------------------------

    Pan detokenize(String token, Principal who, String purpose) {
        if (!who.has(Scope.DETOKENIZE)) {
            log(who, "DETOKENIZE", token, false, "missing scope DETOKENIZE");
            throw new AuthorizationException(who.id() + " may not detokenize");
        }
        TokenRecord rec = byToken.get(token);
        if (rec == null) {
            log(who, "DETOKENIZE", token, false, "unknown token");
            throw new TokenStateException("unknown token");
        }
        if (rec.status != TokenStatus.ACTIVE) {
            log(who, "DETOKENIZE", token, false, "status " + rec.status);
            throw new TokenStateException("token is " + rec.status);
        }
        if (clock.nowEpochSeconds() >= rec.expiresAt) {
            log(who, "DETOKENIZE", token, false, "expired");
            throw new TokenStateException("token expired");
        }
        byte[] plain = decrypt(rec.ciphertext, rec.nonce, token);
        log(who, "DETOKENIZE", token, true, "purpose=" + purpose);
        return Pan.of(new String(plain, StandardCharsets.US_ASCII));
    }

    // ---- lifecycle ------------------------------------------------------------------------------

    void suspend(String token, Principal who) { transition(token, who, TokenStatus.ACTIVE, TokenStatus.SUSPENDED, "SUSPEND"); }
    void reactivate(String token, Principal who) { transition(token, who, TokenStatus.SUSPENDED, TokenStatus.ACTIVE, "REACTIVATE"); }

    /** Revocation is a crypto-shred: zero the ciphertext so no future key compromise can recover the PAN. */
    void revoke(String token, Principal who) {
        TokenRecord rec = require(token, who, "REVOKE");
        if (rec.status == TokenStatus.REVOKED) throw new TokenStateException("already revoked");
        Arrays.fill(rec.ciphertext, (byte) 0);
        rec.status = TokenStatus.REVOKED;
        tokenByPanHmac.values().removeIf(token::equals);
        log(who, "REVOKE", token, true, "ciphertext shredded");
    }

    private void transition(String token, Principal who, TokenStatus from, TokenStatus to, String action) {
        TokenRecord rec = require(token, who, action);
        if (rec.status != from) throw new TokenStateException("cannot " + action + " a token that is " + rec.status);
        rec.status = to;
        log(who, action, token, true, "");
    }

    private TokenRecord require(String token, Principal who, String action) {
        if (!who.has(Scope.MANAGE)) {
            log(who, action, token, false, "missing scope MANAGE");
            throw new AuthorizationException(who.id() + " may not " + action.toLowerCase());
        }
        TokenRecord rec = byToken.get(token);
        if (rec == null) throw new TokenStateException("unknown token");
        return rec;
    }

    TokenStatus status(String token) {
        TokenRecord rec = byToken.get(token);
        if (rec == null) throw new TokenStateException("unknown token");
        return rec.status;
    }

    /** What a token-only system may know: the last four, for "card ending 1111" on a receipt. */
    String lastFour(String token) { return byToken.get(token).lastFour; }
    int size() { return byToken.size(); }
    List<AuditEntry> auditLog() { return Collections.unmodifiableList(audit); }

    /** Exposed for tests of the tamper invariant; a production vault would not have this method. */
    TokenRecord recordFor(String token) { return byToken.get(token); }

    // ---- crypto ---------------------------------------------------------------------------------

    /** 96-bit nonce: 4 random bytes || 64-bit counter. Never repeats under one key within this process. */
    byte[] nextNonce() {
        ByteBuffer buf = ByteBuffer.allocate(NONCE_BYTES);
        buf.putInt(random.nextInt());
        buf.putLong(nonceCounter++);
        return buf.array();
    }

    byte[] encrypt(byte[] plaintext, byte[] nonce, String aad) {
        try {
            Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding");
            cipher.init(Cipher.ENCRYPT_MODE, dataKey, new GCMParameterSpec(GCM_TAG_BITS, nonce));
            cipher.updateAAD(aad.getBytes(StandardCharsets.US_ASCII));
            return cipher.doFinal(plaintext);
        } catch (GeneralSecurityException e) {
            throw new IllegalStateException("AES-GCM encrypt failed", e);
        }
    }

    byte[] decrypt(byte[] ciphertext, byte[] nonce, String aad) {
        try {
            Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding");
            cipher.init(Cipher.DECRYPT_MODE, dataKey, new GCMParameterSpec(GCM_TAG_BITS, nonce));
            cipher.updateAAD(aad.getBytes(StandardCharsets.US_ASCII));
            return cipher.doFinal(ciphertext);
        } catch (GeneralSecurityException e) {
            throw new SecurityException("ciphertext failed authentication: tampered record or wrong key", e);
        }
    }

    /** Keyed hash of the PAN for the dedup index. Unkeyed SHA-256 would be brute-forceable: 10^15 PANs per BIN. */
    String hmacHex(String pan) {
        try {
            Mac mac = Mac.getInstance("HmacSHA256");
            mac.init(indexKey);
            byte[] out = mac.doFinal(pan.getBytes(StandardCharsets.US_ASCII));
            StringBuilder sb = new StringBuilder(64);
            for (byte b : out) sb.append(String.format("%02x", b));
            return sb.toString();
        } catch (GeneralSecurityException e) {
            throw new IllegalStateException(e);
        }
    }

    private void log(Principal who, String action, String token, boolean allowed, String detail) {
        audit.add(new AuditEntry(clock.nowEpochSeconds(), who.id(), action, token, allowed, detail));
    }
}

/** Which systems hold PAN, and therefore which are inside the PCI DSS cardholder data environment. */
record SystemInventory(Map<String, Boolean> seesPan) {
    long inScope() { return seesPan.values().stream().filter(b -> b).count(); }
    List<String> inScopeSystems() { return seesPan.entrySet().stream().filter(Map.Entry::getValue).map(Map.Entry::getKey).sorted().toList(); }
}

public final class Main {
    static byte[] demoKey(int seed) {
        byte[] k = new byte[32];
        new Random(seed).nextBytes(k);   // demo only; production keys come from an HSM or KMS
        return k;
    }

    public static void main(String[] args) {
        FixedClock clock = new FixedClock(1_700_000_000L);
        TokenVault vault = new TokenVault(demoKey(1), demoKey(2), new Random(42), clock, 365L * 24 * 3600);
        Principal checkout = Principal.of("checkout-api", Scope.TOKENIZE);
        Principal gateway = Principal.of("auth-gateway", Scope.DETOKENIZE);
        Principal ops = Principal.of("vault-ops", Scope.MANAGE);

        System.out.println("== Tokenize test PANs ==");
        Pan visa = Pan.of("4111 1111 1111 1111");
        Pan mc = Pan.of("5555555555554444");
        Pan amex = Pan.of("378282246310005");
        String tVisa = vault.tokenize(visa, checkout);
        String tMc = vault.tokenize(mc, checkout);
        String tAmex = vault.tokenize(amex, checkout);
        System.out.println("PAN " + visa + " -> token " + tVisa + " (Luhn valid? " + Luhn.isValid(tVisa) + ")");
        System.out.println("PAN " + mc + " -> token " + tMc + " (Luhn valid? " + Luhn.isValid(tMc) + ")");
        System.out.println("PAN " + amex + " -> token " + tAmex + " (length " + tAmex.length() + ")");
        System.out.println("same PAN again -> " + vault.tokenize(visa, checkout) + " (idempotent)");

        System.out.println("\n== What is stored ==");
        TokenRecord rec = vault.recordFor(tVisa);
        System.out.println("nonce      = " + hex(rec.nonce));
        System.out.println("ciphertext = " + hex(rec.ciphertext) + " (" + rec.ciphertext.length + " bytes = 16 PAN + 16 tag)");
        System.out.println("last four  = " + rec.lastFour + ", status = " + rec.status);

        System.out.println("\n== Detokenize: authorization decides ==");
        Pan back = vault.detokenize(tVisa, gateway, "authorization");
        System.out.println("auth-gateway detokenized " + tVisa + " -> " + back + " (equals original: " + back.equals(visa) + ")");
        try {
            vault.detokenize(tVisa, checkout, "curiosity");
        } catch (AuthorizationException e) {
            System.out.println("checkout-api denied: " + e.getMessage());
        }

        System.out.println("\n== Lifecycle ==");
        vault.suspend(tMc, ops);
        try { vault.detokenize(tMc, gateway, "authorization"); } catch (TokenStateException e) { System.out.println("suspended: " + e.getMessage()); }
        vault.reactivate(tMc, ops);
        clock.advanceSeconds(366L * 24 * 3600);
        try { vault.detokenize(tMc, gateway, "authorization"); } catch (TokenStateException e) { System.out.println("after 366 days: " + e.getMessage()); }
        vault.revoke(tAmex, ops);
        System.out.println("revoked " + tAmex + ": ciphertext now " + hex(vault.recordFor(tAmex).ciphertext).substring(0, 16) + "...");

        System.out.println("\n== Audit log (tokens, never PANs) ==");
        vault.auditLog().forEach(e -> System.out.println("  " + e));

        System.out.println("\n== PCI scope: before and after ==");
        Map<String, Boolean> before = new HashMap<>();
        for (String s : List.of("web-checkout", "order-db", "auth-gateway", "analytics", "crm", "support-tool", "backup", "token-vault"))
            before.put(s, true);
        Map<String, Boolean> after = new HashMap<>(before);
        for (String s : List.of("order-db", "analytics", "crm", "support-tool", "backup")) after.put(s, false);
        SystemInventory b = new SystemInventory(before), a = new SystemInventory(after);
        System.out.println("systems storing PAN before tokenization: " + b.inScope() + " " + b.inScopeSystems());
        System.out.println("systems storing PAN after tokenization:  " + a.inScope() + " " + a.inScopeSystems());
    }

    static String hex(byte[] bytes) {
        StringBuilder sb = new StringBuilder();
        for (byte x : bytes) sb.append(String.format("%02x", x));
        return sb.toString();
    }
}
