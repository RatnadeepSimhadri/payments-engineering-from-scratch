// Lesson: phases/11-payment-rails/14-bank-statements-and-formats/docs/en.md
// Bank Statement Formats: MT940, CAMT.053, BAI2, OFX parsed into one normalized model with balance continuity checks.
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: SWIFT MT940 (Category 9 user handbook); ISO 20022 camt.053.001.08; BAI2 Cash Management Balance Reporting
// Specifications (X9.121 lineage); OFX Specification 1.0.2 / 2.x. Java 21, standard library only. Samples are fictional.

import java.io.StringReader;
import java.time.LocalDate;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Deque;
import java.util.List;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import javax.xml.XMLConstants;
import javax.xml.parsers.DocumentBuilderFactory;
import org.w3c.dom.Document;
import org.w3c.dom.Element;
import org.w3c.dom.Node;
import org.xml.sax.InputSource;

final class StatementParseException extends RuntimeException {
    StatementParseException(String message) { super(message); }
    StatementParseException(String message, Throwable cause) { super(message, cause); }
}

enum StatementFormat {
    MT940, CAMT053, BAI2, OFX;

    /** Sniff the format from the first bytes. Every format announces itself; none needs a file extension. */
    static StatementFormat detect(String text) {
        String head = text.stripLeading();
        if (head.startsWith("{1:") || head.startsWith(":20:")) return MT940;
        if (head.startsWith("01,")) return BAI2;
        if (head.startsWith("OFXHEADER") || head.startsWith("<OFX>") || head.contains("<?OFX")) return OFX;
        if (head.startsWith("<") && head.contains("camt.053")) return CAMT053;
        throw new StatementParseException("unrecognised statement format: " + head.substring(0, Math.min(20, head.length())));
    }
}

/** One booked movement. Amount is signed: positive is a credit to the account, negative a debit. */
record StatementLine(LocalDate bookingDate, LocalDate valueDate, Money amount, String reference,
                     String bankReference, String description) {}

/**
 * The normalized statement every format is parsed into. openingReported is false when the format gives only a
 * closing balance (OFX) and the opening was derived; such a statement cannot prove its own continuity.
 */
record Statement(StatementFormat source, String accountId, Currency currency, String statementId,
                 LocalDate openingDate, Money opening, LocalDate closingDate, Money closing,
                 List<StatementLine> lines, boolean openingReported) {
    Statement {
        lines = List.copyOf(lines);
    }

    Money movement() {
        Money sum = Money.ofMinor(0, currency);
        for (StatementLine l : lines) sum = sum.plus(l.amount());
        return sum;
    }

    /** The continuity invariant: opening + sum(lines) == closing. Returns problems rather than throwing. */
    List<String> continuityErrors() {
        List<String> errors = new ArrayList<>();
        Money expected = opening.plus(movement());
        if (!expected.equals(closing)) {
            errors.add(statementId + ": opening " + opening + " + movements " + movement() + " = " + expected
                    + ", but closing is " + closing + " (difference " + closing.minus(expected) + ")");
        }
        for (StatementLine l : lines) {
            if (l.bookingDate().isAfter(closingDate)) errors.add(statementId + ": line " + l.bankReference() + " booked after closing date");
        }
        return errors;
    }
}

final class StatementChain {
    /** Across consecutive statements of one account: each closing balance must be the next opening balance. */
    static List<String> gaps(List<Statement> statements) {
        List<String> errors = new ArrayList<>();
        for (int i = 1; i < statements.size(); i++) {
            Statement prev = statements.get(i - 1), next = statements.get(i);
            if (!prev.accountId().equals(next.accountId())) {
                errors.add("account changed between " + prev.statementId() + " and " + next.statementId());
                continue;
            }
            if (!next.closingDate().isAfter(prev.closingDate())) {
                errors.add(next.statementId() + " closes on " + next.closingDate() + ", not after " + prev.statementId());
            }
            if (!prev.closing().equals(next.opening())) {
                errors.add("gap between " + prev.statementId() + " closing " + prev.closing() + " and "
                        + next.statementId() + " opening " + next.opening() + " (missing " + next.opening().minus(prev.closing()) + ")");
            }
        }
        return errors;
    }
}

/** Shared helpers for the four parsers. */
final class Parse {
    /** "1500,00" (MT940), "1500.00" (camt, OFX), "-1500" -> exact Money. Never via a double. */
    static Money decimal(String text, Currency c) {
        try {
            return Money.parse(text.strip().replace(',', '.'), c);
        } catch (RuntimeException e) {
            throw new StatementParseException("bad amount '" + text + "': " + e.getMessage(), e);
        }
    }

    /** YYMMDD, pivoting at 2000 (all formats here are 21st-century). */
    static LocalDate yymmdd(String s) {
        return LocalDate.of(2000 + Integer.parseInt(s.substring(0, 2)), Integer.parseInt(s.substring(2, 4)), Integer.parseInt(s.substring(4, 6)));
    }

    static String normalizeNewlines(String text) { return text.replace("\r\n", "\n").replace('\r', '\n'); }
}

/**
 * SWIFT MT940 Customer Statement. Fields are ":tag:value" lines; a line that does not start a tag continues the
 * previous field. :60F/:60M opening, :61 statement line, :86 information to account owner, :62F/:62M closing.
 */
final class Mt940Parser {
    private static final Pattern TAG = Pattern.compile("^:(\\d{2}[A-Z]?):(.*)$");
    private static final Pattern BALANCE = Pattern.compile("^([CD])(\\d{6})([A-Z]{3})(\\d+,\\d{0,2})$");
    /** :61: value date, [entry date], mark (C, D, RC, RD), [funds code], amount, type, customer ref, [//bank ref], [\n supplementary]. */
    private static final Pattern LINE = Pattern.compile(
            "^(\\d{6})(\\d{4})?(RC|RD|C|D)([A-Z])?(\\d+,\\d{0,2})([NFS][A-Z0-9]{3})([^\\n]*?)(?://([^\\n]{1,16}))?(?:\\n(.*))?$", Pattern.DOTALL);

    record Field(String tag, String value) {}

    static List<Field> fields(String block) {
        List<Field> out = new ArrayList<>();
        String tag = null;
        StringBuilder value = new StringBuilder();
        for (String line : block.split("\n")) {
            Matcher m = TAG.matcher(line);
            if (m.matches()) {
                if (tag != null) out.add(new Field(tag, value.toString()));
                tag = m.group(1);
                value = new StringBuilder(m.group(2));
            } else if (tag != null && !line.isEmpty()) {
                value.append('\n').append(line);
            }
        }
        if (tag != null) out.add(new Field(tag, value.toString()));
        return out;
    }

    static List<Statement> parseAll(String text) {
        // Strip SWIFT envelopes ({1:..}{2:..}[{3:..}]{4: ... -}); a file may concatenate several messages.
        String body = Parse.normalizeNewlines(text)
                .replaceAll("\\{1:[^}]*\\}(\\{2:[^}]*\\})?(\\{3:(\\{[^}]*\\})*\\})?\\{4:", "")
                .replaceAll("(?m)^-\\}.*$", "");
        List<Statement> statements = new ArrayList<>();
        List<Field> current = new ArrayList<>();
        for (Field f : fields(body)) {
            if (f.tag().equals("20") && !current.isEmpty()) {
                statements.add(build(current));
                current = new ArrayList<>();
            }
            current.add(f);
        }
        if (!current.isEmpty()) statements.add(build(current));
        if (statements.isEmpty()) throw new StatementParseException("MT940: no :20: field found");
        return statements;
    }

    static Statement parse(String text) {
        List<Statement> all = parseAll(text);
        if (all.size() != 1) throw new StatementParseException("MT940: expected one statement, found " + all.size());
        return all.get(0);
    }

    private static Statement build(List<Field> fields) {
        String ref = null, account = null, seq = "";
        Matcher opening = null, closing = null;
        List<StatementLine> lines = new ArrayList<>();
        Currency currency = null;
        Matcher pendingLine = null;
        String pendingInfo = null;
        for (Field f : fields) {
            switch (f.tag()) {
                case "20" -> ref = f.value().strip();
                case "25" -> account = f.value().strip();
                case "28C" -> seq = f.value().strip();
                case "60F", "60M" -> {
                    opening = balance(f);
                    currency = Currency.of(opening.group(3));
                }
                case "61" -> {
                    if (pendingLine != null) lines.add(line(pendingLine, pendingInfo, currency));
                    pendingLine = LINE.matcher(f.value());
                    if (!pendingLine.matches()) throw new StatementParseException("MT940: bad :61: " + f.value());
                    pendingInfo = null;
                }
                case "86" -> {
                    if (pendingLine != null) pendingInfo = f.value().replace('\n', ' ').strip();
                }
                case "62F", "62M" -> closing = balance(f);
                default -> { } // :21:, :64:, :65: and others are not needed for the normalized statement
            }
        }
        if (pendingLine != null) lines.add(line(pendingLine, pendingInfo, currency));
        if (ref == null || account == null || opening == null || closing == null) {
            throw new StatementParseException("MT940: statement is missing :20:, :25:, :60a: or :62a:");
        }
        if (!closing.group(3).equals(opening.group(3))) throw new StatementParseException("MT940: opening and closing currencies differ");
        return new Statement(StatementFormat.MT940, account, currency, ref + (seq.isEmpty() ? "" : "/" + seq),
                Parse.yymmdd(opening.group(2)), signed(opening.group(1), Parse.decimal(opening.group(4), currency)),
                Parse.yymmdd(closing.group(2)), signed(closing.group(1), Parse.decimal(closing.group(4), currency)),
                lines, true);
    }

    private static Matcher balance(Field f) {
        Matcher m = BALANCE.matcher(f.value().strip());
        if (!m.matches()) throw new StatementParseException("MT940: bad balance :" + f.tag() + ": " + f.value());
        return m;
    }

    private static Money signed(String mark, Money m) { return mark.equals("D") ? m.negate() : m; }

    private static StatementLine line(Matcher m, String info, Currency currency) {
        LocalDate value = Parse.yymmdd(m.group(1));
        LocalDate booking = value;
        if (m.group(2) != null) {
            int month = Integer.parseInt(m.group(2).substring(0, 2)), day = Integer.parseInt(m.group(2).substring(2, 4));
            int year = value.getYear();
            if (month - value.getMonthValue() > 6) year--;       // value 2027-01-02, entry 1231 -> 2026-12-31
            if (value.getMonthValue() - month > 6) year++;       // value 2026-12-31, entry 0102 -> 2027-01-02
            booking = LocalDate.of(year, month, day);
        }
        Money amount = Parse.decimal(m.group(5), currency);
        // C credit, D debit, RC reversal of a credit (a debit), RD reversal of a debit (a credit)
        boolean credit = m.group(3).equals("C") || m.group(3).equals("RD");
        String customerRef = m.group(7).strip();
        String supplementary = m.group(9) == null ? "" : m.group(9).strip();
        String description = info != null ? info : supplementary;
        return new StatementLine(booking, value, credit ? amount : amount.negate(),
                customerRef.equals("NONREF") ? "" : customerRef, m.group(8) == null ? "" : m.group(8), description);
    }
}

/** ISO 20022 camt.053.001.08 Bank to Customer Statement. Only booked (BOOK) entries move the balance. */
final class Camt053Parser {
    static final String NS = "urn:iso:std:iso:20022:tech:xsd:camt.053.001.08";

    static Statement parse(String xml) {
        Element doc = parseRoot(xml);
        Element stmt = child(child(doc, "BkToCstmrStmt"), "Stmt");
        Element acct = child(stmt, "Acct");
        String account = text(child(child(acct, "Id"), "IBAN"));
        Currency currency = Currency.of(text(child(acct, "Ccy")));
        Money opening = null, closing = null;
        LocalDate openingDate = null, closingDate = null;
        for (Element bal : children(stmt, "Bal")) {
            String code = text(child(child(child(bal, "Tp"), "CdOrPrtry"), "Cd"));
            Money amount = amount(bal, currency);
            LocalDate date = LocalDate.parse(text(child(child(bal, "Dt"), "Dt")));
            if (code.equals("OPBD") || code.equals("PRCD")) { opening = amount; openingDate = date; }
            if (code.equals("CLBD")) { closing = amount; closingDate = date; }
        }
        if (opening == null || closing == null) throw new StatementParseException("camt.053: missing OPBD/PRCD or CLBD balance");
        List<StatementLine> lines = new ArrayList<>();
        for (Element ntry : children(stmt, "Ntry")) {
            String status = text(child(child(ntry, "Sts"), "Cd"));
            if (!status.equals("BOOK")) continue; // PDNG and INFO entries are information, not balance movements
            Money amount = amount(ntry, currency);
            Element rvsl = optChild(ntry, "RvslInd");
            if (rvsl != null && text(rvsl).equals("true")) amount = amount.negate();
            Element txDtls = optChild(optChild(ntry, "NtryDtls"), "TxDtls");
            Element refs = optChild(txDtls, "Refs");
            String e2e = refs == null || optChild(refs, "EndToEndId") == null ? "" : text(child(refs, "EndToEndId"));
            Element addtl = optChild(ntry, "AddtlNtryInf");
            Element rmt = optChild(optChild(txDtls, "RmtInf"), "Ustrd");
            String description = addtl != null ? text(addtl) : rmt != null ? text(rmt) : "";
            lines.add(new StatementLine(LocalDate.parse(text(child(child(ntry, "BookgDt"), "Dt"))),
                    LocalDate.parse(text(child(child(ntry, "ValDt"), "Dt"))), amount,
                    e2e.equals("NOTPROVIDED") ? "" : e2e, text(child(ntry, "AcctSvcrRef")), description));
        }
        return new Statement(StatementFormat.CAMT053, account, currency, text(child(stmt, "Id")),
                openingDate, opening, closingDate, closing, lines, true);
    }

    /** Amt with Ccy attribute plus CdtDbtInd CRDT/DBIT; the currency must match the account. */
    private static Money amount(Element parent, Currency currency) {
        Element amt = child(parent, "Amt");
        if (!amt.getAttribute("Ccy").equals(currency.code())) {
            throw new StatementParseException("camt.053: amount in " + amt.getAttribute("Ccy") + " on a " + currency.code() + " account");
        }
        Money m = Parse.decimal(amt.getTextContent(), currency);
        String ind = text(child(parent, "CdtDbtInd"));
        if (!ind.equals("CRDT") && !ind.equals("DBIT")) throw new StatementParseException("camt.053: bad CdtDbtInd " + ind);
        return ind.equals("DBIT") ? m.negate() : m;
    }

    private static Element parseRoot(String xml) {
        Document doc;
        try {
            DocumentBuilderFactory f = DocumentBuilderFactory.newInstance();
            f.setNamespaceAware(true);
            f.setFeature("http://apache.org/xml/features/disallow-doctype-decl", true);
            f.setFeature(XMLConstants.FEATURE_SECURE_PROCESSING, true);
            f.setXIncludeAware(false);
            f.setExpandEntityReferences(false);
            var builder = f.newDocumentBuilder();
            builder.setErrorHandler(null);
            doc = builder.parse(new InputSource(new StringReader(xml)));
        } catch (Exception e) {
            throw new StatementParseException("camt.053: malformed XML: " + e.getMessage(), e);
        }
        Element root = doc.getDocumentElement();
        if (!NS.equals(root.getNamespaceURI())) throw new StatementParseException("camt.053: unexpected namespace " + root.getNamespaceURI());
        return root;
    }

    static Element optChild(Element parent, String name) {
        if (parent == null) return null;
        for (Node n = parent.getFirstChild(); n != null; n = n.getNextSibling()) {
            if (n instanceof Element e && name.equals(e.getLocalName())) return e;
        }
        return null;
    }

    static Element child(Element parent, String name) {
        Element e = optChild(parent, name);
        if (e == null) throw new StatementParseException("camt.053: missing " + name + " under " + (parent == null ? "?" : parent.getLocalName()));
        return e;
    }

    static List<Element> children(Element parent, String name) {
        List<Element> out = new ArrayList<>();
        for (Node n = parent.getFirstChild(); n != null; n = n.getNextSibling()) {
            if (n instanceof Element e && name.equals(e.getLocalName())) out.add(e);
        }
        return out;
    }

    static String text(Element e) { return e.getTextContent().strip(); }
}

/**
 * BAI2 (Bank Administration Institute, version 2). Comma-delimited records ending in '/', 88 continues the
 * previous record. 01 file header, 02 group header, 03 account identifier and summaries, 16 transaction detail,
 * 49 account trailer, 98 group trailer, 99 file trailer. Amounts are integers in minor units, no decimal point.
 */
final class Bai2Parser {
    /** One logical record (88 continuations joined) and how many physical lines it used. */
    record LogicalRecord(String type, List<String> fields, String text, int physicalLines) {}

    static List<LogicalRecord> records(String text) {
        List<LogicalRecord> out = new ArrayList<>();
        for (String raw : Parse.normalizeNewlines(text).split("\n")) {
            if (raw.isBlank()) continue;
            String line = raw.strip();
            String type = line.substring(0, 2);
            if (type.equals("88")) {
                if (out.isEmpty()) throw new StatementParseException("BAI2: 88 continuation with no previous record");
                LogicalRecord prev = out.remove(out.size() - 1);
                String rest = line.substring(3);
                if (prev.type().equals("16")) {
                    out.add(new LogicalRecord("16", prev.fields(), (prev.text() + " " + rest).strip(), prev.physicalLines() + 1));
                } else {
                    List<String> merged = new ArrayList<>(prev.fields());
                    merged.addAll(split(rest));
                    out.add(new LogicalRecord(prev.type(), merged, null, prev.physicalLines() + 1));
                }
                continue;
            }
            if (type.equals("16")) {
                // 16: type code, amount, funds type, [funds fields], bank ref, customer ref, text-to-end-of-record
                String[] parts = line.split(",", -1);
                int fundsExtra = switch (parts[3]) {
                    case "0", "1", "2", "Z", "" -> 0;
                    case "V" -> 2;
                    case "S" -> 3;
                    default -> throw new StatementParseException("BAI2: unsupported funds type " + parts[3]);
                };
                int fixed = 4 + fundsExtra + 2; // record code, type, amount, funds, extras, bank ref, customer ref
                String[] head = line.split(",", fixed + 1);
                List<String> fields = new ArrayList<>(List.of(head).subList(1, fixed));
                String txt = head.length > fixed ? head[fixed] : "";
                if (txt.endsWith("/")) txt = txt.substring(0, txt.length() - 1);
                out.add(new LogicalRecord("16", fields, txt.strip(), 1));
            } else {
                out.add(new LogicalRecord(type, split(line.substring(3)), null, 1));
            }
        }
        return out;
    }

    private static List<String> split(String body) {
        String b = body.endsWith("/") ? body.substring(0, body.length() - 1) : body;
        return new ArrayList<>(List.of(b.split(",", -1)));
    }

    static long amount(String s) {
        if (s.isEmpty()) return 0;
        try {
            return Long.parseLong(s.startsWith("+") ? s.substring(1) : s);
        } catch (NumberFormatException e) {
            throw new StatementParseException("BAI2: bad amount " + s, e);
        }
    }

    static List<Statement> parseAll(String text) {
        List<LogicalRecord> recs = records(text);
        if (recs.isEmpty() || !recs.get(0).type().equals("01")) throw new StatementParseException("BAI2: file must start with 01");
        List<Statement> out = new ArrayList<>();
        LocalDate asOf = null;
        Currency groupCurrency = null;
        String account = null;
        Currency currency = null;
        Money opening = null, closing = null;
        List<StatementLine> lines = null;
        long accountTotal = 0, groupTotal = 0, fileTotal = 0;
        int accountRecords = 0, groupRecords = 0, fileRecords = 0, groups = 0, accounts = 0;
        for (LogicalRecord r : recs) {
            fileRecords += r.physicalLines();
            groupRecords += r.physicalLines();
            accountRecords += r.physicalLines();
            List<String> f = r.fields();
            switch (r.type()) {
                case "01" -> { }
                case "02" -> {
                    asOf = Parse.yymmdd(f.get(3));
                    groupCurrency = f.get(5).isEmpty() ? Currency.USD : Currency.of(f.get(5));
                    groupRecords = r.physicalLines();
                    groupTotal = 0;
                    accounts = 0;
                    groups++;
                }
                case "03" -> {
                    account = f.get(0);
                    currency = f.get(1).isEmpty() ? groupCurrency : Currency.of(f.get(1));
                    accountRecords = r.physicalLines();
                    accountTotal = 0;
                    lines = new ArrayList<>();
                    opening = null;
                    closing = null;
                    // summary triplets: type code, amount, item count, funds type (only "" or 0 supported here)
                    for (int i = 2; i + 3 < f.size() + 1 && !f.get(i).isEmpty(); i += 4) {
                        long amt = amount(f.get(i + 1));
                        accountTotal += amt;
                        if (f.get(i).equals("010")) opening = Money.ofMinor(amt, currency);
                        if (f.get(i).equals("015")) closing = Money.ofMinor(amt, currency);
                    }
                }
                case "16" -> {
                    if (lines == null) throw new StatementParseException("BAI2: 16 record outside an account");
                    int typeCode = Integer.parseInt(f.get(0));
                    long amt = amount(f.get(1));
                    accountTotal += amt;
                    boolean credit = typeCode >= 100 && typeCode <= 399;
                    boolean debit = typeCode >= 400 && typeCode <= 699;
                    if (!credit && !debit) throw new StatementParseException("BAI2: type code " + typeCode + " is not a credit or debit detail");
                    int n = f.size();
                    lines.add(new StatementLine(asOf, asOf, Money.ofMinor(credit ? amt : -amt, currency), f.get(n - 1), f.get(n - 2), r.text()));
                }
                case "49" -> {
                    long declared = amount(f.get(0));
                    int declaredRecords = Integer.parseInt(f.get(1));
                    if (declared != accountTotal) throw new StatementParseException("BAI2: account " + account + " control total " + declared + " != computed " + accountTotal);
                    if (declaredRecords != accountRecords) throw new StatementParseException("BAI2: account " + account + " record count " + declaredRecords + " != " + accountRecords);
                    if (opening == null || closing == null) throw new StatementParseException("BAI2: account " + account + " lacks 010 opening or 015 closing ledger");
                    out.add(new Statement(StatementFormat.BAI2, account, currency, "BAI2-" + account + "-" + asOf, asOf, opening, asOf, closing, lines, true));
                    groupTotal += accountTotal;
                    accounts++;
                    lines = null;
                }
                case "98" -> {
                    if (amount(f.get(0)) != groupTotal) throw new StatementParseException("BAI2: group control total " + f.get(0) + " != computed " + groupTotal);
                    if (Integer.parseInt(f.get(1)) != accounts) throw new StatementParseException("BAI2: group account count mismatch");
                    if (Integer.parseInt(f.get(2)) != groupRecords) throw new StatementParseException("BAI2: group record count " + f.get(2) + " != " + groupRecords);
                    fileTotal += groupTotal;
                }
                case "99" -> {
                    if (amount(f.get(0)) != fileTotal) throw new StatementParseException("BAI2: file control total " + f.get(0) + " != computed " + fileTotal);
                    if (Integer.parseInt(f.get(1)) != groups) throw new StatementParseException("BAI2: file group count mismatch");
                    if (Integer.parseInt(f.get(2)) != fileRecords) throw new StatementParseException("BAI2: file record count " + f.get(2) + " != " + fileRecords);
                }
                default -> throw new StatementParseException("BAI2: unknown record type " + r.type());
            }
        }
        if (!recs.get(recs.size() - 1).type().equals("99")) throw new StatementParseException("BAI2: file must end with 99");
        return out;
    }

    static Statement parse(String text) {
        List<Statement> all = parseAll(text);
        if (all.size() != 1) throw new StatementParseException("BAI2: expected one account, found " + all.size());
        return all.get(0);
    }
}

/**
 * OFX 1.x (SGML: leaf elements have no closing tag) and 2.x (XML). A tiny tokenizer handles both.
 * OFX reports only a closing (ledger) balance, so the opening balance is derived and cannot be verified.
 */
final class OfxParser {
    static final class OfxNode {
        final String name;
        final String value;
        final List<OfxNode> children = new ArrayList<>();
        OfxNode(String name, String value) { this.name = name; this.value = value; }

        OfxNode find(String n) {
            if (name.equals(n)) return this;
            for (OfxNode c : children) {
                OfxNode hit = c.find(n);
                if (hit != null) return hit;
            }
            return null;
        }

        String req(String n) {
            OfxNode hit = find(n);
            if (hit == null || hit.value == null) throw new StatementParseException("OFX: missing <" + n + ">");
            return hit.value;
        }

        String opt(String n) {
            OfxNode hit = find(n);
            return hit == null || hit.value == null ? "" : hit.value;
        }
    }

    static OfxNode tree(String text) {
        int start = text.indexOf("<OFX>");
        if (start < 0) throw new StatementParseException("OFX: no <OFX> root");
        OfxNode root = new OfxNode("#root", null);
        Deque<OfxNode> stack = new ArrayDeque<>();
        stack.push(root);
        int i = start;
        while (i < text.length()) {
            int lt = text.indexOf('<', i);
            if (lt < 0) break;
            int gt = text.indexOf('>', lt);
            if (gt < 0) throw new StatementParseException("OFX: unterminated tag");
            String tag = text.substring(lt + 1, gt).strip();
            int next = text.indexOf('<', gt);
            String value = text.substring(gt + 1, next < 0 ? text.length() : next).strip();
            if (tag.startsWith("/")) {
                String name = tag.substring(1);
                // close an aggregate; a closing tag for a leaf (OFX 2.x XML) matches nothing on the stack and is ignored
                if (stack.stream().anyMatch(n -> n.name.equals(name))) {
                    while (!stack.peek().name.equals(name)) stack.pop();
                    stack.pop();
                }
            } else if (!value.isEmpty()) {
                stack.peek().children.add(new OfxNode(tag, value)); // leaf: <TRNAMT>-1500.00
            } else {
                OfxNode agg = new OfxNode(tag, null);           // aggregate: <STMTTRN>
                stack.peek().children.add(agg);
                stack.push(agg);
            }
            i = next < 0 ? text.length() : next;
        }
        return root;
    }

    static LocalDate date(String ofxDate) {
        return LocalDate.of(Integer.parseInt(ofxDate.substring(0, 4)), Integer.parseInt(ofxDate.substring(4, 6)), Integer.parseInt(ofxDate.substring(6, 8)));
    }

    static Statement parse(String text) {
        OfxNode root = tree(Parse.normalizeNewlines(text));
        OfxNode stmt = root.find("STMTRS");
        if (stmt == null) throw new StatementParseException("OFX: no <STMTRS> (bank statement response)");
        Currency currency = Currency.of(stmt.req("CURDEF"));
        String account = stmt.req("ACCTID");
        OfxNode list = stmt.find("BANKTRANLIST");
        if (list == null) throw new StatementParseException("OFX: no <BANKTRANLIST>");
        List<StatementLine> lines = new ArrayList<>();
        for (OfxNode t : list.children) {
            if (!t.name.equals("STMTTRN")) continue;
            LocalDate posted = date(t.req("DTPOSTED"));
            String user = t.opt("DTUSER");
            String name = t.opt("NAME"), memo = t.opt("MEMO");
            String description = (name + " " + memo).strip();
            lines.add(new StatementLine(posted, user.isEmpty() ? posted : date(user), Parse.decimal(t.req("TRNAMT"), currency),
                    t.opt("REFNUM"), t.req("FITID"), description));
        }
        OfxNode ledger = stmt.find("LEDGERBAL");
        if (ledger == null) throw new StatementParseException("OFX: no <LEDGERBAL>");
        Money closing = Parse.decimal(ledger.req("BALAMT"), currency);
        LocalDate closingDate = date(ledger.req("DTASOF"));
        Money movement = Money.ofMinor(0, currency);
        for (StatementLine l : lines) movement = movement.plus(l.amount());
        return new Statement(StatementFormat.OFX, account, currency, "OFX-" + account + "-" + list.req("DTEND"),
                date(list.req("DTSTART")), closing.minus(movement), closingDate, closing, lines, false);
    }
}

final class StatementParsers {
    static Statement parse(String text) {
        return switch (StatementFormat.detect(text)) {
            case MT940 -> Mt940Parser.parse(text);
            case CAMT053 -> Camt053Parser.parse(text);
            case BAI2 -> Bai2Parser.parse(text);
            case OFX -> OfxParser.parse(text);
        };
    }
}

/** One business day on one account (opening 12,500.00; -1,500.00; +3,250.75; -12.50; closing 14,238.25) in four formats. */
final class Samples {
    static final String MT940 = """
            {1:F01COBADEFFAXXX0000000000}{2:O9402200261012COBADEFFAXXX00000000002610122200N}{4:
            :20:STMT261012
            :25:DE89370400440532013000
            :28C:00287/001
            :60F:C261009EUR12500,00
            :61:2610121012D1500,00NTRFINV-4411//COBA0001
            :86:Supplier payment INV-4411
            Fournitures Lyon SARL
            :61:2610121012C3250,75NTRFORDER-77//COBA0002
            :86:Customer receipt ORDER-77 Delft Sensors BV
            :61:2610121012D12,50NCHGNONREF//COBA0003
            :86:Account maintenance fee
            :62F:C261012EUR14238,25
            :64:C261012EUR14238,25
            -}
            """;

    static final String MT940_DAY2 = """
            {1:F01COBADEFFAXXX0000000000}{2:O9402200261013COBADEFFAXXX00000000002610132200N}{4:
            :20:STMT261013
            :25:DE89370400440532013000
            :28C:00288/001
            :60F:C261012EUR%s
            :61:2610131013C100,00NTRFORDER-78//COBA0004
            :86:Customer receipt ORDER-78
            :62F:C261013EUR%s
            -}
            """;

    static final String CAMT053 = """
            <?xml version="1.0" encoding="UTF-8"?>
            <Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.053.001.08">
              <BkToCstmrStmt>
                <GrpHdr><MsgId>COBA-STMT-261012</MsgId><CreDtTm>2026-10-12T22:00:00</CreDtTm></GrpHdr>
                <Stmt>
                  <Id>STMT261012</Id>
                  <ElctrncSeqNb>287</ElctrncSeqNb>
                  <CreDtTm>2026-10-12T22:00:00</CreDtTm>
                  <Acct><Id><IBAN>DE89370400440532013000</IBAN></Id><Ccy>EUR</Ccy></Acct>
                  <Bal>
                    <Tp><CdOrPrtry><Cd>OPBD</Cd></CdOrPrtry></Tp>
                    <Amt Ccy="EUR">12500.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><Dt><Dt>2026-10-12</Dt></Dt>
                  </Bal>
                  <Bal>
                    <Tp><CdOrPrtry><Cd>CLBD</Cd></CdOrPrtry></Tp>
                    <Amt Ccy="EUR">14238.25</Amt><CdtDbtInd>CRDT</CdtDbtInd><Dt><Dt>2026-10-12</Dt></Dt>
                  </Bal>
                  <Ntry>
                    <Amt Ccy="EUR">1500.00</Amt><CdtDbtInd>DBIT</CdtDbtInd><Sts><Cd>BOOK</Cd></Sts>
                    <BookgDt><Dt>2026-10-12</Dt></BookgDt><ValDt><Dt>2026-10-12</Dt></ValDt>
                    <AcctSvcrRef>COBA0001</AcctSvcrRef>
                    <NtryDtls><TxDtls><Refs><EndToEndId>INV-4411</EndToEndId></Refs>
                      <RmtInf><Ustrd>Invoice 4411</Ustrd></RmtInf></TxDtls></NtryDtls>
                    <AddtlNtryInf>Supplier payment INV-4411 Fournitures Lyon SARL</AddtlNtryInf>
                  </Ntry>
                  <Ntry>
                    <Amt Ccy="EUR">3250.75</Amt><CdtDbtInd>CRDT</CdtDbtInd><Sts><Cd>BOOK</Cd></Sts>
                    <BookgDt><Dt>2026-10-12</Dt></BookgDt><ValDt><Dt>2026-10-12</Dt></ValDt>
                    <AcctSvcrRef>COBA0002</AcctSvcrRef>
                    <NtryDtls><TxDtls><Refs><EndToEndId>ORDER-77</EndToEndId></Refs></TxDtls></NtryDtls>
                    <AddtlNtryInf>Customer receipt ORDER-77 Delft Sensors BV</AddtlNtryInf>
                  </Ntry>
                  <Ntry>
                    <Amt Ccy="EUR">12.50</Amt><CdtDbtInd>DBIT</CdtDbtInd><Sts><Cd>BOOK</Cd></Sts>
                    <BookgDt><Dt>2026-10-12</Dt></BookgDt><ValDt><Dt>2026-10-12</Dt></ValDt>
                    <AcctSvcrRef>COBA0003</AcctSvcrRef>
                    <AddtlNtryInf>Account maintenance fee</AddtlNtryInf>
                  </Ntry>
                  <Ntry>
                    <Amt Ccy="EUR">999.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><Sts><Cd>PDNG</Cd></Sts>
                    <BookgDt><Dt>2026-10-13</Dt></BookgDt><ValDt><Dt>2026-10-13</Dt></ValDt>
                    <AcctSvcrRef>COBA0099</AcctSvcrRef>
                    <AddtlNtryInf>Pending incoming transfer</AddtlNtryInf>
                  </Ntry>
                </Stmt>
              </BkToCstmrStmt>
            </Document>
            """;

    static final String BAI2 = """
            01,021000021,ACMEUS,261012,2200,1,80,1,2/
            02,ACMEUS,021000021,1,261012,2200,USD,2/
            03,0001234567,USD,010,1250000,,,015,1423825,,/
            16,495,150000,Z,COBA0001,INV-4411,Supplier payment INV-4411
            88,Fournitures Lyon SARL
            16,195,325075,Z,COBA0002,ORDER-77,Customer receipt ORDER-77 Delft Sensors BV
            16,698,1250,Z,COBA0003,,Account maintenance fee
            49,3150150,6/
            98,3150150,1,8/
            99,3150150,1,10/
            """;

    static final String OFX = """
            OFXHEADER:100
            DATA:OFXSGML
            VERSION:102
            SECURITY:NONE
            ENCODING:USASCII
            CHARSET:1252
            COMPRESSION:NONE
            OLDFILEUID:NONE
            NEWFILEUID:NONE

            <OFX>
            <SIGNONMSGSRSV1><SONRS><STATUS><CODE>0<SEVERITY>INFO</STATUS><DTSERVER>20261012220000<LANGUAGE>ENG</SONRS></SIGNONMSGSRSV1>
            <BANKMSGSRSV1><STMTTRNRS><TRNUID>1<STATUS><CODE>0<SEVERITY>INFO</STATUS>
            <STMTRS><CURDEF>USD
            <BANKACCTFROM><BANKID>021000021<ACCTID>0001234567<ACCTTYPE>CHECKING</BANKACCTFROM>
            <BANKTRANLIST><DTSTART>20261012<DTEND>20261012
            <STMTTRN><TRNTYPE>DEBIT<DTPOSTED>20261012<TRNAMT>-1500.00<FITID>COBA0001<REFNUM>INV-4411<NAME>Fournitures Lyon SARL<MEMO>Supplier payment INV-4411</STMTTRN>
            <STMTTRN><TRNTYPE>CREDIT<DTPOSTED>20261012<TRNAMT>3250.75<FITID>COBA0002<REFNUM>ORDER-77<NAME>Delft Sensors BV<MEMO>Customer receipt ORDER-77</STMTTRN>
            <STMTTRN><TRNTYPE>SRVCHG<DTPOSTED>20261012<TRNAMT>-12.50<FITID>COBA0003<NAME>Account maintenance fee</STMTTRN>
            </BANKTRANLIST>
            <LEDGERBAL><BALAMT>14238.25<DTASOF>20261012220000</LEDGERBAL>
            </STMTRS></STMTTRNRS></BANKMSGSRSV1></OFX>
            """;
}

public final class Main {
    static void show(Statement s) {
        System.out.printf("%s  account %s  %s  %s %s -> %s %s%s%n", s.source(), s.accountId(), s.statementId(),
                s.openingDate(), s.opening(), s.closingDate(), s.closing(), s.openingReported() ? "" : "  (opening derived)");
        for (StatementLine l : s.lines()) {
            System.out.printf("   %s  %12s  ref=%-9s bank=%-9s %s%n", l.bookingDate(), l.amount(), l.reference(), l.bankReference(), l.description());
        }
        List<String> errors = s.continuityErrors();
        System.out.println("   continuity: " + (errors.isEmpty() ? "OK (opening + " + s.movement() + " = closing)" : errors));
    }

    public static void main(String[] args) {
        System.out.println("== Four formats, one normalized model ==");
        for (String sample : List.of(Samples.MT940, Samples.CAMT053, Samples.BAI2, Samples.OFX)) {
            show(StatementParsers.parse(sample));
        }

        System.out.println("\n== Same day, same minor-unit movements? ==");
        for (String sample : List.of(Samples.MT940, Samples.CAMT053, Samples.BAI2, Samples.OFX)) {
            Statement s = StatementParsers.parse(sample);
            List<Long> amounts = s.lines().stream().map(l -> l.amount().minorUnits()).toList();
            System.out.println(s.source() + " " + amounts + " closing " + s.closing().minorUnits());
        }

        System.out.println("\n== A statement that does not add up ==");
        Statement broken = Mt940Parser.parse(Samples.MT940.replace(":62F:C261012EUR14238,25", ":62F:C261012EUR14283,25"));
        broken.continuityErrors().forEach(e -> System.out.println("   " + e));

        System.out.println("\n== Chains: day 2 must open where day 1 closed ==");
        String twoDays = Samples.MT940 + String.format(Samples.MT940_DAY2, "14238,25", "14338,25");
        List<Statement> chain = Mt940Parser.parseAll(twoDays);
        System.out.println("   statements: " + chain.size() + ", gaps: " + StatementChain.gaps(chain));
        String gapped = Samples.MT940 + String.format(Samples.MT940_DAY2, "14250,75", "14350,75");
        List<Statement> bad = Mt940Parser.parseAll(gapped);
        System.out.println("   each internally continuous: " + bad.stream().allMatch(s -> s.continuityErrors().isEmpty()));
        StatementChain.gaps(bad).forEach(g -> System.out.println("   " + g));

        System.out.println("\n== Rejections ==");
        try { Bai2Parser.parse(Samples.BAI2.replace("49,3150150,6/", "49,3150151,6/")); }
        catch (StatementParseException e) { System.out.println("   " + e.getMessage()); }
        try { Mt940Parser.parse(Samples.MT940.replace("D12,50NCHG", "D12,505NCHG")); }
        catch (StatementParseException e) { System.out.println("   " + e.getMessage()); }
        try { StatementParsers.parse("Date,Description,Amount\n2026-10-12,Fee,-12.50\n"); }
        catch (StatementParseException e) { System.out.println("   " + e.getMessage()); }
    }
}
