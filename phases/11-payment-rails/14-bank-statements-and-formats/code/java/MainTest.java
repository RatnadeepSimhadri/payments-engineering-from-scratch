// Tests for phases/11-payment-rails/14-bank-statements-and-formats. Run: javac -d .out *.java && java -cp .out MainTest
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

    static List<Long> amounts(Statement s) { return s.lines().stream().map(l -> l.amount().minorUnits()).toList(); }

    public static void main(String[] args) {
        // Detection
        check("detect MT940", StatementFormat.detect(Samples.MT940) == StatementFormat.MT940);
        check("detect camt.053", StatementFormat.detect(Samples.CAMT053) == StatementFormat.CAMT053);
        check("detect BAI2", StatementFormat.detect(Samples.BAI2) == StatementFormat.BAI2);
        check("detect OFX", StatementFormat.detect(Samples.OFX) == StatementFormat.OFX);
        checkThrows("CSV is not a bank statement format", StatementParseException.class, () -> StatementFormat.detect("Date,Amount\n"));

        // All four normalize to the same movements
        List<Long> expected = List.of(-150000L, 325075L, -1250L);
        for (String sample : List.of(Samples.MT940, Samples.CAMT053, Samples.BAI2, Samples.OFX)) {
            Statement s = StatementParsers.parse(sample);
            check(s.source() + " movements", amounts(s).equals(expected));
            check(s.source() + " closing 14238.25", s.closing().minorUnits() == 1423825);
            check(s.source() + " continuity holds", s.continuityErrors().isEmpty());
        }

        // MT940 specifics
        Statement mt = Mt940Parser.parse(Samples.MT940);
        check("MT940 account from :25:", mt.accountId().equals("DE89370400440532013000"));
        check("MT940 statement id includes :28C:", mt.statementId().equals("STMT261012/00287/001"));
        check("MT940 opening date from :60F:", mt.openingDate().equals(LocalDate.of(2026, 10, 9)));
        check("MT940 :86: continuation joined", mt.lines().get(0).description().equals("Supplier payment INV-4411 Fournitures Lyon SARL"));
        check("MT940 NONREF becomes empty reference", mt.lines().get(2).reference().isEmpty());
        check("MT940 bank reference after //", mt.lines().get(1).bankReference().equals("COBA0002"));
        Statement rc = Mt940Parser.parse(Samples.MT940.replace("2610121012C3250,75", "2610121012RC3250,75")
                .replace(":62F:C261012EUR14238,25", ":62F:C261012EUR7736,75"));
        check("MT940 RC (reversal of credit) is a debit", rc.lines().get(1).amount().minorUnits() == -325075 && rc.continuityErrors().isEmpty());
        Statement yearEnd = Mt940Parser.parse(Samples.MT940.replace("2610121012D1500,00", "2701021231D1500,00"));
        check("MT940 entry date 1231 with value date 270102 is 2026-12-31", yearEnd.lines().get(0).bookingDate().equals(LocalDate.of(2026, 12, 31)));
        Statement debitOpen = Mt940Parser.parse(Samples.MT940.replace(":60F:C261009EUR12500,00", ":60F:D261009EUR100,00")
                .replace(":62F:C261012EUR14238,25", ":62F:C261012EUR1638,25"));
        check("MT940 D balance is negative (overdrawn)", debitOpen.opening().minorUnits() == -10000 && debitOpen.continuityErrors().isEmpty());
        checkThrows("MT940 three decimals rejected", StatementParseException.class, () -> Mt940Parser.parse(Samples.MT940.replace("D12,50NCHG", "D12,505NCHG")));
        checkThrows("MT940 missing closing balance", StatementParseException.class,
                () -> Mt940Parser.parse(Samples.MT940.replace(":62F:C261012EUR14238,25\n", "")));

        // Continuity and chains
        Statement broken = Mt940Parser.parse(Samples.MT940.replace(":62F:C261012EUR14238,25", ":62F:C261012EUR14283,25"));
        check("transposed closing balance detected", broken.continuityErrors().size() == 1 && broken.continuityErrors().get(0).contains("difference 45.00 EUR"));
        List<Statement> chain = Mt940Parser.parseAll(Samples.MT940 + String.format(Samples.MT940_DAY2, "14238,25", "14338,25"));
        check("two statements in one MT940 file", chain.size() == 2);
        check("continuous chain has no gaps", StatementChain.gaps(chain).isEmpty());
        List<Statement> gapped = Mt940Parser.parseAll(Samples.MT940 + String.format(Samples.MT940_DAY2, "14250,75", "14350,75"));
        check("gapped day 2 is internally continuous", gapped.get(1).continuityErrors().isEmpty());
        check("chain gap of 12.50 detected", StatementChain.gaps(gapped).size() == 1 && StatementChain.gaps(gapped).get(0).contains("missing 12.50 EUR"));
        check("out-of-order chain detected", !StatementChain.gaps(List.of(chain.get(1), chain.get(0))).isEmpty());

        // camt.053 specifics
        Statement camt = Camt053Parser.parse(Samples.CAMT053);
        check("camt PDNG entry excluded", camt.lines().size() == 3);
        check("camt EndToEndId is the reference", camt.lines().get(0).reference().equals("INV-4411"));
        Statement reversed = Camt053Parser.parse(Samples.CAMT053.replace("<Sts><Cd>BOOK</Cd></Sts>\n        <BookgDt><Dt>2026-10-12</Dt></BookgDt><ValDt><Dt>2026-10-12</Dt></ValDt>\n        <AcctSvcrRef>COBA0003",
                "<Sts><Cd>BOOK</Cd></Sts><RvslInd>true</RvslInd>\n        <BookgDt><Dt>2026-10-12</Dt></BookgDt><ValDt><Dt>2026-10-12</Dt></ValDt>\n        <AcctSvcrRef>COBA0003"));
        check("camt RvslInd flips the sign", reversed.lines().get(2).amount().minorUnits() == 1250);
        checkThrows("camt amount in wrong currency", StatementParseException.class,
                () -> Camt053Parser.parse(Samples.CAMT053.replace("<Amt Ccy=\"EUR\">3250.75", "<Amt Ccy=\"USD\">3250.75")));
        checkThrows("camt DOCTYPE rejected", StatementParseException.class,
                () -> Camt053Parser.parse("<?xml version=\"1.0\"?><!DOCTYPE d [<!ENTITY x SYSTEM \"file:///etc/passwd\">]><Document xmlns=\"" + Camt053Parser.NS + "\">&x;</Document>"));

        // BAI2 specifics
        Statement bai = Bai2Parser.parse(Samples.BAI2);
        check("BAI2 88 continuation appended to 16 text", bai.lines().get(0).description().equals("Supplier payment INV-4411 Fournitures Lyon SARL"));
        check("BAI2 type 495 is a debit, 195 a credit", bai.lines().get(0).amount().isNegative() && bai.lines().get(1).amount().isPositive());
        checkThrows("BAI2 account control total", StatementParseException.class, () -> Bai2Parser.parse(Samples.BAI2.replace("49,3150150,6/", "49,3150151,6/")));
        checkThrows("BAI2 record count includes 88", StatementParseException.class, () -> Bai2Parser.parse(Samples.BAI2.replace("49,3150150,6/", "49,3150150,5/")));
        checkThrows("BAI2 file record count", StatementParseException.class, () -> Bai2Parser.parse(Samples.BAI2.replace("99,3150150,1,10/", "99,3150150,1,9/")));
        checkThrows("BAI2 missing 99 trailer", StatementParseException.class, () -> Bai2Parser.parse(Samples.BAI2.replace("99,3150150,1,10/\n", "")));

        // OFX specifics
        Statement ofx = OfxParser.parse(Samples.OFX);
        check("OFX opening is derived, not reported", !ofx.openingReported() && ofx.opening().minorUnits() == 1250000);
        check("OFX FITID is the bank reference", ofx.lines().get(2).bankReference().equals("COBA0003"));
        Statement ofxXml = OfxParser.parse(Samples.OFX.replace("<TRNAMT>-12.50", "<TRNAMT>-12.50</TRNAMT>").replace("<CURDEF>USD", "<CURDEF>USD</CURDEF>"));
        check("OFX 2.x style closing tags on leaves parse the same", amounts(ofxXml).equals(expected));
        checkThrows("OFX without LEDGERBAL", StatementParseException.class,
                () -> OfxParser.parse(Samples.OFX.replace("<LEDGERBAL><BALAMT>14238.25<DTASOF>20261012220000</LEDGERBAL>", "")));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
