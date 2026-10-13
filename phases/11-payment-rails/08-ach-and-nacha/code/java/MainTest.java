// Tests for phases/11-payment-rails/08-ach-and-nacha. Run: javac -d .out *.java && java -cp .out MainTest
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

    static EntryDetail credit(String routing, String cents) {
        return EntryDetail.of(TransactionCode.CHECKING_CREDIT, routing, "123", Money.ofMinor(Long.parseLong(cents), Currency.USD), "ID", "NAME");
    }

    static EntryDetail debit(String routing, String cents) {
        return EntryDetail.of(TransactionCode.CHECKING_DEBIT, routing, "123", Money.ofMinor(Long.parseLong(cents), Currency.USD), "ID", "NAME");
    }

    static BatchHeader header(int serviceClass, SecCode sec) {
        return new BatchHeader(serviceClass, "ACME", "", "1234567890", sec, "PAYROLL", "", "261013", "02199999", 1);
    }

    public static void main(String[] args) {
        // routing number check digit: ABA mod-10 with weights 3-7-1
        check("check digit of 02199999 is 6", RoutingNumber.checkDigit("02199999") == 6);
        check("check digit of 01199999 is 3", RoutingNumber.checkDigit("01199999") == 3);
        check("check digit of 12199999 is 3", RoutingNumber.checkDigit("12199999") == 3);
        check("021999996 valid", RoutingNumber.isValid("021999996"));
        check("061999998 valid", RoutingNumber.isValid("061999998"));
        check("transposition 021999969 invalid", !RoutingNumber.isValid("021999969"));
        check("single digit error 021999992 invalid", !RoutingNumber.isValid("021999992"));
        check("wrong length invalid", !RoutingNumber.isValid("02199999"));
        check("letters invalid", !RoutingNumber.isValid("02199999A"));
        checkThrows("entry with bad check digit rejected", NachaFormatException.class, () -> credit("021999969", "100"));

        // fixed-width helpers behave like the spec tables
        check("alpha left-justifies and space-fills", Fixed.alpha("abc", 5).equals("ABC  "));
        check("alpha truncates", Fixed.alpha("ABCDEFG", 5).equals("ABCDE"));
        check("numeric right-justifies and zero-fills", Fixed.numeric(42, 6).equals("000042"));
        checkThrows("numeric never truncates", NachaFormatException.class, () -> Fixed.numeric(1234567, 6));
        check("field uses 1-based inclusive positions", Fixed.field("abcdef", 2, 4).equals("bcd"));

        // transaction codes
        check("22 is a checking credit", TransactionCode.fromCode(22) == TransactionCode.CHECKING_CREDIT && TransactionCode.CHECKING_CREDIT.isCredit());
        check("27 is a checking debit", TransactionCode.fromCode(27).isDebit() && !TransactionCode.fromCode(27).isCredit());
        check("23 is a prenote", TransactionCode.CHECKING_PRENOTE_CREDIT.isPrenote());
        check("26 is a return and a debit", TransactionCode.CHECKING_RETURN_DEBIT.isReturn() && TransactionCode.CHECKING_RETURN_DEBIT.isDebit());
        checkThrows("unknown transaction code rejected", NachaFormatException.class, () -> TransactionCode.fromCode(25));
        checkThrows("prenote with non-zero amount rejected", NachaFormatException.class,
                () -> EntryDetail.of(TransactionCode.CHECKING_PRENOTE_CREDIT, "021999996", "1", Money.ofMinor(1, Currency.USD), "", "X"));
        checkThrows("negative amount rejected", NachaFormatException.class,
                () -> EntryDetail.of(TransactionCode.CHECKING_CREDIT, "021999996", "1", Money.ofMinor(-1, Currency.USD), "", "X"));
        checkThrows("amount over ten digits rejected", NachaFormatException.class,
                () -> EntryDetail.of(TransactionCode.CHECKING_CREDIT, "021999996", "1", Money.ofMinor(10_000_000_000L, Currency.USD), "", "X"));

        // every record is exactly 94 characters
        AchFile file = Main.payrollFile();
        check("every line is 94 characters", file.toLines().stream().allMatch(l -> l.length() == 94));
        check("file header starts with 101", file.toLines().get(0).startsWith("101"));
        check("record size / blocking / format fields", Fixed.field(file.toLines().get(0), 35, 40).equals("094101"));

        // entry hash and control totals
        Batch batch = file.batches().get(0);
        check("entry hash sums 8-digit DFI ids", batch.entryHash() == 1199999L + 6199999L + 12199999L);
        check("entry hash keeps rightmost 10 digits", Batch.entryHash(List.of("99999999", "99999999", "00000003")) == 200000001L
                && Batch.entryHash(java.util.Collections.nCopies(101, "99999999")) == (101L * 99999999L) % 10_000_000_000L);
        check("entry/addenda count counts addenda", batch.entryAddendaCount() == 4);
        check("total credit", batch.totalCredit().equals(Money.parse("7387.87", Currency.USD)));
        check("total debit is zero in a credits-only batch", batch.totalDebit().isZero());
        check("file control carries the same totals", file.control().totalCredit().equals(batch.totalCredit()) && file.control().entryHash() == batch.entryHash());
        check("trace numbers are ODFI id + sequence", batch.entries().get(0).traceNumber().equals("021999990000001")
                && batch.entries().get(2).traceNumber().equals("021999990000003"));
        Batch mixed = new Batch(header(BatchHeader.MIXED, SecCode.CCD), List.of(credit("021999996", "1000"), debit("011999993", "250")));
        check("mixed batch splits debit and credit totals", mixed.totalCredit().minorUnits() == 1000 && mixed.totalDebit().minorUnits() == 250);
        checkThrows("debit in a 220 credits-only batch rejected", NachaFormatException.class,
                () -> new Batch(header(BatchHeader.CREDITS_ONLY, SecCode.PPD), List.of(debit("021999996", "1"))));

        // block padding: total records padded with 9-filler to a multiple of 10
        check("8 records -> 1 block, 10 lines", file.recordCount() == 8 && file.blockCount() == 1 && file.toLines().size() == 10);
        check("filler lines are all nines", file.toLines().get(9).equals("9".repeat(94)));
        List<EntryDetail> nine = new ArrayList<>();
        for (int i = 0; i < 9; i++) nine.add(credit("021999996", "100"));
        AchFile bigger = new AchFile(file.header(), List.of(new Batch(header(BatchHeader.CREDITS_ONLY, SecCode.PPD), nine)));
        check("13 records -> 2 blocks, 20 lines", bigger.recordCount() == 13 && bigger.blockCount() == 2 && bigger.toLines().size() == 20);
        List<EntryDetail> six = new ArrayList<>();
        for (int i = 0; i < 6; i++) six.add(credit("021999996", "100"));
        AchFile exact = new AchFile(file.header(), List.of(new Batch(header(BatchHeader.CREDITS_ONLY, SecCode.PPD), six)));
        check("exactly 10 records -> no filler", exact.recordCount() == 10 && exact.toLines().size() == 10 && !exact.toLines().get(9).startsWith("9999"));

        // round trip
        String text = file.write();
        AchFile parsed = AchFile.parse(text);
        check("parse(write(file)) equals file", parsed.equals(file));
        check("write(parse(text)) equals text", parsed.write().equals(text));
        check("addenda survives the round trip", parsed.batches().get(0).entries().get(2).addenda().info().equals("BONUS INCLUDED Q3"));
        check("CRLF line endings accepted", AchFile.parse(text.replace("\n", "\r\n")).equals(file));

        // tampering is detected by the control totals
        String control = batch.control().toLine();
        String creditBumped = control.substring(0, 43) + (char) (control.charAt(43) + 1) + control.substring(44);
        checkThrows("mismatched credit total rejected", NachaFormatException.class, () -> AchFile.parse(text.replace(control, creditBumped)));
        String hashBumped = control.substring(0, 19) + (char) (control.charAt(19) + 1) + control.substring(20);
        checkThrows("mismatched entry hash rejected", NachaFormatException.class, () -> AchFile.parse(text.replace(control, hashBumped)));
        String countBumped = control.substring(0, 9) + '5' + control.substring(10);
        checkThrows("mismatched entry count rejected", NachaFormatException.class, () -> AchFile.parse(text.replace(control, countBumped)));
        String entryLine = file.toLines().get(2);
        String badDigit = entryLine.substring(0, 11) + (entryLine.charAt(11) == '9' ? '0' : (char) (entryLine.charAt(11) + 1)) + entryLine.substring(12);
        checkThrows("entry with bad routing check digit rejected on parse", NachaFormatException.class, () -> AchFile.parse(text.replace(entryLine, badDigit)));
        checkThrows("short line rejected", NachaFormatException.class, () -> AchFile.parse(text.replace(entryLine, entryLine.substring(0, 93))));
        checkThrows("addenda after an entry with indicator 0 rejected", NachaFormatException.class,
                () -> AchFile.parse(text.replace(file.toLines().get(5), file.toLines().get(2))));
        checkThrows("file without control rejected", NachaFormatException.class,
                () -> AchFile.parse(String.join("\n", file.toLines().subList(0, 7)) + "\n"));

        // return entry with a 99 addenda
        EntryDetail original = debit("011999993", "4999").withTrace("02199999", 1);
        EntryDetail returned = EntryDetail.of(TransactionCode.CHECKING_RETURN_DEBIT, "011999993", "123", original.amount(), "ID", "NAME")
                .withAddenda(Addenda.returned("R01", original.traceNumber(), "02199999", ""));
        Batch returnBatch = new Batch(new BatchHeader(BatchHeader.DEBITS_ONLY, "ACME", "", "1234567890", SecCode.WEB, "RETURN", "", "261015", "01199999", 1), List.of(returned));
        List<String> lines = returnBatch.toLines();
        check("return addenda carries reason code and original trace", lines.get(2).startsWith("799R01" + original.traceNumber()));
        check("return addenda round trips", Addenda.parse(lines.get(2)).equals(returned.addenda()));
        check("return counts as a debit in the control", returnBatch.totalDebit().minorUnits() == 4999 && returnBatch.entryAddendaCount() == 2);
        checkThrows("addenda type other than 05/99 rejected", NachaFormatException.class, () -> new Addenda("02", "", "", "", ""));
        check("parsed return exposes its reason", Addenda.parse(lines.get(2)).reason().description().equals("Insufficient funds"));
        checkThrows("reason() on a payment addenda rejected", NachaFormatException.class, () -> Addenda.payment("X").reason());

        // return reason codes and the return window
        java.time.LocalDate thursday = java.time.LocalDate.of(2026, 10, 15);
        check("R01 is a two-banking-day return and re-presentable", ReturnReason.lookup("R01").windowDays() == 2 && ReturnReason.lookup("R01").representable());
        check("R02 and R03 are not re-presentable", !ReturnReason.lookup("R02").representable() && !ReturnReason.lookup("R03").representable());
        check("R10 has the extended 60-day window", ReturnReason.lookup("R10").extendedWindow() && ReturnReason.lookup("R10").windowDays() == 60);
        check("two banking days from Thursday skips the weekend", ReturnReason.lookup("R01").deadline(thursday).equals(java.time.LocalDate.of(2026, 10, 19)));
        check("sixty calendar days from Thursday", ReturnReason.lookup("R10").deadline(thursday).equals(java.time.LocalDate.of(2026, 12, 14)));
        check("return on the deadline is timely", ReturnReason.lookup("R01").isTimely(thursday, java.time.LocalDate.of(2026, 10, 19)));
        check("return after the deadline is late", !ReturnReason.lookup("R01").isTimely(thursday, java.time.LocalDate.of(2026, 10, 20)));
        check("unknown reason code is not known", !ReturnReason.isKnown("R99"));
        checkThrows("unknown reason code lookup rejected", NachaFormatException.class, () -> ReturnReason.lookup("R99"));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
