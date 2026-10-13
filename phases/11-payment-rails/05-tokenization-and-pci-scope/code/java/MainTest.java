// Tests for phases/11-payment-rails/05-tokenization-and-pci-scope. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Random;

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
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ": " + t.getMessage() + ")");
        }
    }

    static TokenVault freshVault(FixedClock clock) {
        return new TokenVault(Main.demoKey(1), Main.demoKey(2), new Random(7), clock, 3600);
    }

    public static void main(String[] args) {
        FixedClock clock = new FixedClock(1_000_000L);
        TokenVault vault = freshVault(clock);
        Principal checkout = Principal.of("checkout", Scope.TOKENIZE);
        Principal gateway = Principal.of("gateway", Scope.DETOKENIZE);
        Principal ops = Principal.of("ops", Scope.MANAGE);
        Principal nobody = Principal.of("nobody");

        // --- PAN type: validation and masking --------------------------------------------------
        Pan visa = Pan.of("4111 1111 1111 1111");
        check("PAN strips spaces", visa.digits().equals("4111111111111111"));
        check("PAN last four", visa.lastFour().equals("1111"));
        check("PAN bin", visa.bin().equals("411111"));
        check("PAN toString is masked", visa.toString().equals("411111******1111"));
        check("PAN concatenation never leaks digits", !("card=" + visa).contains("4111111111111111"));
        check("Amex 15-digit masked", Pan.of("378282246310005").toString().equals("378282*****0005"));
        checkThrows("PAN rejects Luhn failure", IllegalArgumentException.class, () -> Pan.of("4111111111111112"));
        checkThrows("PAN rejects too short", IllegalArgumentException.class, () -> Pan.of("41111111111"));
        checkThrows("PAN rejects letters", IllegalArgumentException.class, () -> Pan.of("4111 1111 1111 111a"));
        check("Luhn accepts 4242424242424242", Luhn.isValid("4242424242424242"));
        check("Luhn rejects 4242424242424241", !Luhn.isValid("4242424242424241"));

        // --- tokens: format-preserving, Luhn-invalid, idempotent ------------------------------
        String t1 = vault.tokenize(visa, checkout);
        check("token preserves length", t1.length() == 16);
        check("token preserves last four", t1.endsWith("1111"));
        check("token is all digits", t1.chars().allMatch(Character::isDigit));
        check("token is not the PAN", !t1.equals(visa.digits()));
        check("token fails Luhn by construction", !Luhn.isValid(t1));
        check("same PAN -> same token", vault.tokenize(visa, checkout).equals(t1));
        check("idempotent tokenize does not grow the vault", vault.size() == 1);
        String t2 = vault.tokenize(Pan.of("4242424242424242"), checkout);
        check("different PAN -> different token", !t2.equals(t1));
        check("different PAN with same last four still distinct", t2.endsWith("4242") && vault.size() == 2);
        // 1000 minted tokens for the same last-four: every one fails Luhn and none collide
        TokenVault many = freshVault(new FixedClock(0));
        java.util.Set<String> seen = new java.util.HashSet<>();
        boolean allInvalid = true;
        Pan sample = Pan.of("4111111111111111");
        for (int i = 0; i < 1000; i++) {
            String t = many.mintToken(sample);
            allInvalid &= !Luhn.isValid(t);
            seen.add(t);
        }
        check("1000 minted tokens all Luhn-invalid", allInvalid);
        check("1000 minted tokens all distinct", seen.size() == 1000);

        // --- authorization -------------------------------------------------------------------
        check("detokenize with scope returns the PAN", vault.detokenize(t1, gateway, "auth").equals(visa));
        checkThrows("detokenize without scope is denied", AuthorizationException.class, () -> vault.detokenize(t1, checkout, "auth"));
        checkThrows("tokenize without scope is denied", AuthorizationException.class, () -> vault.tokenize(visa, nobody));
        checkThrows("suspend without MANAGE is denied", AuthorizationException.class, () -> vault.suspend(t1, gateway));
        checkThrows("unknown token", TokenStateException.class, () -> vault.detokenize("0000000000001111", gateway, "auth"));

        // --- audit log never contains a PAN --------------------------------------------------
        String auditText = vault.auditLog().toString();
        check("audit log records denials", vault.auditLog().stream().anyMatch(e -> !e.allowed()));
        check("audit log never contains a PAN", !auditText.contains("4111111111111111") && !auditText.contains("4242424242424242"));
        check("audit log contains the token", auditText.contains(t1));

        // --- lifecycle -----------------------------------------------------------------------
        vault.suspend(t1, ops);
        check("status after suspend", vault.status(t1) == TokenStatus.SUSPENDED);
        checkThrows("suspended token cannot be detokenized", TokenStateException.class, () -> vault.detokenize(t1, gateway, "auth"));
        checkThrows("cannot suspend twice", TokenStateException.class, () -> vault.suspend(t1, ops));
        vault.reactivate(t1, ops);
        check("reactivated token works", vault.detokenize(t1, gateway, "auth").equals(visa));
        clock.advanceSeconds(3600);
        checkThrows("expired token cannot be detokenized", TokenStateException.class, () -> vault.detokenize(t1, gateway, "auth"));
        vault.revoke(t2, ops);
        check("status after revoke", vault.status(t2) == TokenStatus.REVOKED);
        byte[] shredded = vault.recordFor(t2).ciphertext;
        boolean allZero = true;
        for (byte b : shredded) allZero &= b == 0;
        check("revoke shreds the ciphertext", allZero);
        checkThrows("revoked token cannot be detokenized", TokenStateException.class, () -> vault.detokenize(t2, gateway, "auth"));
        checkThrows("cannot revoke twice", TokenStateException.class, () -> vault.revoke(t2, ops));
        String t3 = vault.tokenize(Pan.of("4242424242424242"), checkout);
        check("re-tokenizing a revoked PAN mints a new token", !t3.equals(t2));
        check("last four visible without detokenizing", vault.lastFour(t3).equals("4242"));

        // --- crypto invariants ---------------------------------------------------------------
        TokenVault v2 = freshVault(new FixedClock(0));
        String tok = v2.tokenize(visa, checkout);
        TokenRecord rec = v2.recordFor(tok);
        check("ciphertext is PAN length + 16-byte tag", rec.ciphertext.length == 16 + 16);
        check("ciphertext does not contain the PAN bytes", !new String(rec.ciphertext, java.nio.charset.StandardCharsets.ISO_8859_1).contains("4111111111111111"));
        byte[] tampered = rec.ciphertext.clone();
        tampered[3] ^= 0x01;
        checkThrows("flipping one ciphertext bit fails authentication", SecurityException.class, () -> v2.decrypt(tampered, rec.nonce, tok));
        checkThrows("ciphertext bound to its token via AAD", SecurityException.class, () -> v2.decrypt(rec.ciphertext, rec.nonce, "9999999999991111"));
        TokenVault otherKey = new TokenVault(Main.demoKey(99), Main.demoKey(2), new Random(7), new FixedClock(0), 3600);
        checkThrows("wrong key fails authentication", SecurityException.class, () -> otherKey.decrypt(rec.ciphertext, rec.nonce, tok));
        byte[] n1 = v2.nextNonce(), n2 = v2.nextNonce();
        check("nonces never repeat", !java.util.Arrays.equals(n1, n2));
        check("nonce is 96 bits", n1.length == 12);
        check("same PAN hashes to same index", v2.hmacHex("4111111111111111").equals(v2.hmacHex("4111111111111111")));
        check("different index key -> different index", !v2.hmacHex("4111111111111111").equals(
                new TokenVault(Main.demoKey(1), Main.demoKey(3), new Random(7), new FixedClock(0), 3600).hmacHex("4111111111111111")));
        checkThrows("data key must be 256-bit", IllegalArgumentException.class, () -> new TokenVault(new byte[16], Main.demoKey(2), new Random(1), clock, 10));

        // --- scope inventory -----------------------------------------------------------------
        SystemInventory inv = new SystemInventory(java.util.Map.of("vault", true, "orders", false, "crm", false));
        check("only PAN-holding systems are in scope", inv.inScope() == 1 && inv.inScopeSystems().equals(List.of("vault")));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
