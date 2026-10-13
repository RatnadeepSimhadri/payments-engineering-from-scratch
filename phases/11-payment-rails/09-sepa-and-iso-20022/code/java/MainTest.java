// Tests for phases/11-payment-rails/09-sepa-and-iso-20022. Run: javac -d .out *.java && java -cp .out MainTest
import java.time.LocalDate;
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
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ": " + t.getMessage() + ")");
        }
    }

    static Money eur(String s) { return Money.parse(s, Currency.EUR); }

    static PaymentInitiation with(CreditTransfer t) {
        PaymentInitiation p = Main.demoInitiation();
        return new PaymentInitiation(p.messageId(), p.createdAt(), p.initiatingParty(), p.paymentInfoId(),
                p.requestedExecutionDate(), p.debtor(), List.of(t));
    }

    public static void main(String[] args) {
        // Identifiers
        check("valid DE IBAN", Iban.isValid("DE89370400440532013000"));
        check("valid FR IBAN with letter in BBAN", Iban.isValid("FR1420041010050500013M02606"));
        check("one digit changed fails mod-97", !Iban.isValid("DE89370400440532013001"));
        check("wrong length for country fails", !Iban.isValid("DE8937040044053201300"));
        check("lowercase rejected (normalize first)", !Iban.isValid("de89370400440532013000"));
        check("BIC8 valid", Bic.isValid("ABNANL2A"));
        check("BIC11 valid", Bic.isValid("COBADEFFXXX"));
        check("BIC with 9 chars invalid", !Bic.isValid("COBADEFFX"));
        check("DE bank code is the Bankleitzahl", Iban.bankCode("DE89370400440532013000").equals("37040044"));

        // SEPA rules
        PaymentInitiation p = Main.demoInitiation();
        check("demo initiation has no violations", SepaRules.violations(p).isEmpty());
        check("control sum is exact", p.controlSum().equals(eur("13600.49")));
        Party cred = new Party("Delft Sensors BV", "NL91ABNA0417164300", "ABNANL2A");
        check("non-EUR rejected", SepaRules.violations(with(new CreditTransfer("E2E-1", Money.parse("1.00", Currency.USD), cred, null)))
                .stream().anyMatch(v -> v.contains("must be EUR")));
        check("zero amount rejected", SepaRules.violations(with(new CreditTransfer("E2E-1", eur("0.00"), cred, null)))
                .stream().anyMatch(v -> v.contains("out of range")));
        check("max amount 999999999.99 accepted", SepaRules.violations(with(new CreditTransfer("E2E-1", eur("999999999.99"), cred, null))).isEmpty());
        check("1000000000.00 rejected", !SepaRules.violations(with(new CreditTransfer("E2E-1", eur("1000000000.00"), cred, null))).isEmpty());
        check("umlaut outside SEPA character set", SepaRules.violations(with(new CreditTransfer("E2E-1", eur("1.00"),
                new Party("Z\u00fcrich AG", "NL91ABNA0417164300", null), null))).stream().anyMatch(v -> v.contains("outside the SEPA set")));
        check("36-char EndToEndId rejected", !SepaRules.violations(with(new CreditTransfer("X".repeat(36), eur("1.00"), cred, null))).isEmpty());
        check("'//' in EndToEndId rejected", !SepaRules.violations(with(new CreditTransfer("A//B", eur("1.00"), cred, null))).isEmpty());
        check("141-char remittance rejected", !SepaRules.violations(with(new CreditTransfer("E2E-1", eur("1.00"), cred, "R".repeat(141)))).isEmpty());
        check("bad creditor IBAN rejected", !SepaRules.violations(with(new CreditTransfer("E2E-1", eur("1.00"),
                new Party("X", "NL91ABNA0417164301", null), null))).isEmpty());
        checkThrows("write refuses a message with violations", Iso20022Exception.class,
                () -> Pain001.write(with(new CreditTransfer("E2E-1", eur("0.00"), cred, null))));

        // pain.001 round trip and totals
        String pain = Pain001.write(p);
        check("pain.001 namespace", pain.contains("urn:iso:std:iso:20022:tech:xsd:pain.001.001.09"));
        check("pain.001 amount format", pain.contains("<InstdAmt Ccy=\"EUR\">12000.50</InstdAmt>"));
        check("pain.001 IBAN-only creditor has no CdtrAgt", pain.split("<CdtrAgt>").length == 3);
        check("pain.001 round trip", Pain001.parse(pain).equals(p));
        checkThrows("CtrlSum mismatch rejected", Iso20022Exception.class,
                () -> Pain001.parse(pain.replace("<CtrlSum>13600.49</CtrlSum>", "<CtrlSum>13600.50</CtrlSum>")));
        checkThrows("NbOfTxs mismatch rejected", Iso20022Exception.class,
                () -> Pain001.parse(pain.replaceFirst("<NbOfTxs>3</NbOfTxs>", "<NbOfTxs>4</NbOfTxs>")));
        checkThrows("three decimals rejected", Iso20022Exception.class, () -> Pain001.parse(pain.replace("349.99", "349.999")));
        checkThrows("non-EUR Ccy attribute rejected", Iso20022Exception.class, () -> Pain001.parse(pain.replace("Ccy=\"EUR\">349.99", "Ccy=\"USD\">349.99")));
        checkThrows("missing mandatory element rejected", Iso20022Exception.class, () -> Pain001.parse(pain.replace("<PmtInfId>PAYRUN-42</PmtInfId>", "")));
        checkThrows("malformed XML rejected", Iso20022Exception.class, () -> Pain001.parse(pain.substring(0, pain.length() / 2)));
        checkThrows("DOCTYPE rejected (XXE)", Iso20022Exception.class, () -> Pain001.parse(
                "<?xml version=\"1.0\"?><!DOCTYPE d [<!ENTITY x SYSTEM \"file:///etc/passwd\">]><Document xmlns=\"" + Pain001.NS + "\">&x;</Document>"));

        // pacs.008 mapping and round trip
        InterbankMessage m = Pacs008.fromPain001(p, "COBA-1", Main.NOW, Main.demoDirectory());
        check("EndToEndId passes through", m.transfers().get(0).endToEndId().equals("INV-2026-0912"));
        check("TxId assigned by debtor bank", m.transfers().get(2).transactionId().equals("COBA-1-0003"));
        check("settlement date is requested execution date", m.settlementDate().equals(LocalDate.of(2026, 10, 14)));
        check("IBAN-only creditor BIC derived from directory", m.transfers().get(2).creditor().bic().equals("CAIXESBBXXX"));
        check("total settlement amount equals control sum", m.totalSettlementAmount().equals(p.controlSum()));
        String pacs = Pacs008.write(m);
        check("pacs.008 has SLEV and CLRG", pacs.contains("<ChrgBr>SLEV</ChrgBr>") && pacs.contains("<SttlmMtd>CLRG</SttlmMtd>"));
        check("pacs.008 round trip", Pacs008.parse(pacs).equals(m));
        checkThrows("TtlIntrBkSttlmAmt mismatch rejected", Iso20022Exception.class,
                () -> Pacs008.parse(pacs.replace(">13600.49<", ">13600.48<")));
        checkThrows("pain.001 is not a pacs.008", Iso20022Exception.class, () -> Pacs008.parse(pain));
        checkThrows("unknown bank in directory", Iso20022Exception.class, () -> Pacs008.fromPain001(
                with(new CreditTransfer("E2E-1", eur("1.00"), new Party("X", "BE68539007547034", null), null)), "M", Main.NOW, Main.demoDirectory()));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
