// Lesson: phases/11-payment-rails/09-sepa-and-iso-20022/docs/en.md
// SEPA, Wires, and ISO 20022: generate and parse pain.001 and pacs.008 with javax.xml, validate SEPA rules.
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: ISO 20022 message definitions pain.001.001.09 and pacs.008.001.08; EPC SEPA Credit Transfer Rulebook
// and Implementation Guidelines (EPC125-05, EPC115-06); ISO 13616 (IBAN); ISO 9362 (BIC). Java 21, stdlib only.

import java.io.StringReader;
import java.io.StringWriter;
import java.time.LocalDate;
import java.time.LocalDateTime;
import java.time.format.DateTimeFormatter;
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Set;
import java.util.function.Function;
import java.util.regex.Pattern;
import javax.xml.XMLConstants;
import javax.xml.parsers.DocumentBuilder;
import javax.xml.parsers.DocumentBuilderFactory;
import javax.xml.transform.OutputKeys;
import javax.xml.transform.Transformer;
import javax.xml.transform.TransformerFactory;
import javax.xml.transform.dom.DOMSource;
import javax.xml.transform.stream.StreamResult;
import org.w3c.dom.Document;
import org.w3c.dom.Element;
import org.w3c.dom.Node;
import org.xml.sax.InputSource;

/** Anything wrong with an ISO 20022 message: malformed XML, wrong message type, bad totals, rule violations. */
final class Iso20022Exception extends RuntimeException {
    Iso20022Exception(String message) { super(message); }
    Iso20022Exception(String message, Throwable cause) { super(message, cause); }
}

/** ISO 13616 IBAN check: country length table (SEPA subset) and mod-97 == 1. Lesson 11 builds the full registry. */
final class Iban {
    static final Map<String, Integer> LENGTHS = Map.ofEntries(
            Map.entry("AT", 20), Map.entry("BE", 16), Map.entry("DE", 22), Map.entry("ES", 24), Map.entry("FI", 18),
            Map.entry("FR", 27), Map.entry("GB", 22), Map.entry("IE", 22), Map.entry("IT", 27), Map.entry("LU", 20),
            Map.entry("NL", 18), Map.entry("PT", 25));

    static boolean isValid(String iban) {
        if (iban == null || iban.length() < 15 || iban.length() > 34) return false;
        if (!iban.chars().allMatch(c -> (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9'))) return false;
        Integer expected = LENGTHS.get(iban.substring(0, 2));
        if (expected == null || expected != iban.length()) return false;
        String rearranged = iban.substring(4) + iban.substring(0, 4);
        int remainder = 0;
        for (char c : rearranged.toCharArray()) {
            int value = Character.isDigit(c) ? c - '0' : c - 'A' + 10;
            remainder = (value < 10 ? remainder * 10 + value : remainder * 100 + value) % 97;
        }
        return remainder == 1;
    }

    /** The national bank code inside the BBAN, for the countries the demo directory knows. */
    static String bankCode(String iban) {
        return switch (iban.substring(0, 2)) {
            case "DE" -> iban.substring(4, 12); // Bankleitzahl, 8 digits
            case "FR" -> iban.substring(4, 9);  // code banque, 5 digits
            default -> iban.substring(4, 8);    // ES entidad, NL bank code, and most others: 4 characters
        };
    }
}

/** ISO 9362 BIC: 4 letters institution, 2 letters country, 2 alphanumeric location, optional 3 branch. */
final class Bic {
    private static final Pattern FORMAT = Pattern.compile("[A-Z]{4}[A-Z]{2}[A-Z0-9]{2}([A-Z0-9]{3})?");
    static boolean isValid(String bic) { return bic != null && FORMAT.matcher(bic).matches(); }
}

/** A debtor or creditor: name, account (IBAN), and the bank's BIC (optional for the customer since IBAN-only). */
record Party(String name, String iban, String bic) {
    Party {
        Objects.requireNonNull(name, "name");
        Objects.requireNonNull(iban, "iban");
    }
}

/** One payment inside a pain.001: CdtTrfTxInf. */
record CreditTransfer(String endToEndId, Money amount, Party creditor, String remittance) {}

/** A customer-to-bank credit transfer initiation (pain.001) with one payment information block. */
record PaymentInitiation(String messageId, LocalDateTime createdAt, String initiatingParty, String paymentInfoId,
                         LocalDate requestedExecutionDate, Party debtor, List<CreditTransfer> transfers) {
    PaymentInitiation {
        transfers = List.copyOf(transfers);
    }

    int numberOfTransactions() { return transfers.size(); }

    Money controlSum() {
        Money sum = Money.ofMinor(0, Currency.EUR);
        for (CreditTransfer t : transfers) sum = sum.plus(t.amount());
        return sum;
    }
}

/** One payment inside a pacs.008: the interbank view, with the transaction id the debtor bank assigned. */
record InterbankTransfer(String endToEndId, String transactionId, Money amount, Party debtor, Party creditor, String remittance) {}

/** A bank-to-bank customer credit transfer (pacs.008). */
record InterbankMessage(String messageId, LocalDateTime createdAt, LocalDate settlementDate, List<InterbankTransfer> transfers) {
    InterbankMessage {
        transfers = List.copyOf(transfers);
    }

    Money totalSettlementAmount() {
        Money sum = Money.ofMinor(0, Currency.EUR);
        for (InterbankTransfer t : transfers) sum = sum.plus(t.amount());
        return sum;
    }
}

/** SEPA Credit Transfer business rules from the EPC rulebook and implementation guidelines. */
final class SepaRules {
    /** The SEPA basic Latin character set (EPC217-08): letters, digits, and / - ? : ( ) . , ' + space. */
    static final Pattern LATIN = Pattern.compile("[A-Za-z0-9/\\-?:().,'+ ]*");
    static final long MAX_AMOUNT_MINOR = 99_999_999_999L; // 999,999,999.99 EUR

    static List<String> violations(PaymentInitiation p) {
        List<String> v = new ArrayList<>();
        id(v, "MsgId", p.messageId());
        id(v, "PmtInfId", p.paymentInfoId());
        text(v, "InitgPty/Nm", p.initiatingParty(), 70);
        party(v, "Dbtr", p.debtor());
        if (p.requestedExecutionDate().isBefore(p.createdAt().toLocalDate())) {
            v.add("ReqdExctnDt " + p.requestedExecutionDate() + " is before CreDtTm " + p.createdAt().toLocalDate());
        }
        if (p.transfers().isEmpty()) v.add("at least one CdtTrfTxInf is required");
        Set<String> seen = new HashSet<>();
        for (CreditTransfer t : p.transfers()) {
            String where = "CdtTrfTxInf[" + t.endToEndId() + "]";
            id(v, where + "/EndToEndId", t.endToEndId());
            if (!seen.add(t.endToEndId())) v.add(where + ": duplicate EndToEndId in message");
            if (!t.amount().currency().equals(Currency.EUR)) v.add(where + ": SEPA amounts must be EUR, got " + t.amount().currency().code());
            if (t.amount().minorUnits() < 1 || t.amount().minorUnits() > MAX_AMOUNT_MINOR) v.add(where + ": amount out of range 0.01..999999999.99: " + t.amount());
            party(v, where + "/Cdtr", t.creditor());
            if (t.remittance() != null) text(v, where + "/RmtInf/Ustrd", t.remittance(), 140);
        }
        return v;
    }

    private static void id(List<String> v, String field, String value) {
        if (value == null || value.isEmpty() || value.length() > 35) v.add(field + " must be 1..35 characters");
        else if (!LATIN.matcher(value).matches()) v.add(field + " has characters outside the SEPA set: " + firstOutside(value));
        else if (value.startsWith("/") || value.endsWith("/") || value.contains("//")) v.add(field + " must not start or end with '/' or contain '//'");
    }

    private static void text(List<String> v, String field, String value, int max) {
        if (value == null || value.isEmpty() || value.length() > max) v.add(field + " must be 1.." + max + " characters");
        else if (!LATIN.matcher(value).matches()) v.add(field + " has characters outside the SEPA set: " + firstOutside(value));
    }

    /** "U+00FC at index 1": report the code point, so logs stay ASCII and the culprit is unambiguous. */
    static String firstOutside(String value) {
        for (int i = 0; i < value.length(); i++) {
            if (!LATIN.matcher(String.valueOf(value.charAt(i))).matches()) {
                return String.format("U+%04X at index %d", (int) value.charAt(i), i);
            }
        }
        return "none";
    }

    private static void party(List<String> v, String field, Party party) {
        text(v, field + "/Nm", party.name(), 70);
        if (!Iban.isValid(party.iban())) v.add(field + "Acct/IBAN invalid: " + party.iban());
        if (party.bic() != null && !Bic.isValid(party.bic())) v.add(field + "Agt/BICFI invalid: " + party.bic());
    }
}

/** Secure DOM parsing and small helpers shared by both message types. */
final class Xml {
    static Document newDocument() {
        try {
            return factory().newDocumentBuilder().newDocument();
        } catch (Exception e) {
            throw new Iso20022Exception("cannot create XML document", e);
        }
    }

    /** XXE-safe: no DOCTYPE, no external entities, no XInclude. A payment file is never trusted input. */
    static DocumentBuilderFactory factory() {
        try {
            DocumentBuilderFactory f = DocumentBuilderFactory.newInstance();
            f.setNamespaceAware(true);
            f.setFeature("http://apache.org/xml/features/disallow-doctype-decl", true);
            f.setFeature(XMLConstants.FEATURE_SECURE_PROCESSING, true);
            f.setXIncludeAware(false);
            f.setExpandEntityReferences(false);
            return f;
        } catch (Exception e) {
            throw new Iso20022Exception("cannot configure XML parser", e);
        }
    }

    static Element parseRoot(String xml, String expectedNamespace, String expectedChild) {
        Document doc;
        try {
            DocumentBuilder builder = factory().newDocumentBuilder();
            builder.setErrorHandler(null);
            doc = builder.parse(new InputSource(new StringReader(xml)));
        } catch (Exception e) {
            throw new Iso20022Exception("malformed XML: " + e.getMessage(), e);
        }
        Element root = doc.getDocumentElement();
        if (!"Document".equals(root.getLocalName()) || !expectedNamespace.equals(root.getNamespaceURI())) {
            throw new Iso20022Exception("expected Document in " + expectedNamespace + ", got " + root.getNamespaceURI());
        }
        return child(root, expectedChild);
    }

    static Element el(Element parent, String name) {
        Element e = parent.getOwnerDocument().createElementNS(parent.getNamespaceURI(), name);
        parent.appendChild(e);
        return e;
    }

    static Element el(Element parent, String name, String text) {
        Element e = el(parent, name);
        e.setTextContent(text);
        return e;
    }

    /** Walk direct children by local name: child(e, "DbtrAcct", "Id", "IBAN"). Throws if any step is missing. */
    static Element child(Element parent, String... path) {
        Element current = parent;
        for (String name : path) {
            Element next = null;
            for (Node n = current.getFirstChild(); n != null; n = n.getNextSibling()) {
                if (n instanceof Element e && name.equals(e.getLocalName())) { next = e; break; }
            }
            if (next == null) throw new Iso20022Exception("missing element " + String.join("/", path) + " under " + parent.getLocalName());
            current = next;
        }
        return current;
    }

    static Element optChild(Element parent, String... path) {
        try {
            return child(parent, path);
        } catch (Iso20022Exception e) {
            return null;
        }
    }

    static List<Element> children(Element parent, String name) {
        List<Element> out = new ArrayList<>();
        for (Node n = parent.getFirstChild(); n != null; n = n.getNextSibling()) {
            if (n instanceof Element e && name.equals(e.getLocalName())) out.add(e);
        }
        return out;
    }

    static String text(Element parent, String... path) { return child(parent, path).getTextContent().strip(); }

    static String optText(Element parent, String... path) {
        Element e = optChild(parent, path);
        return e == null ? null : e.getTextContent().strip();
    }

    /** "1234.56" with Ccy="EUR" -> Money; rejects a third decimal and a non-EUR currency attribute mismatch. */
    static Money amount(Element amountElement) {
        String ccy = amountElement.getAttribute("Ccy");
        if (!"EUR".equals(ccy)) throw new Iso20022Exception("unsupported currency " + ccy + " (SEPA is EUR only)");
        try {
            return Money.parse(amountElement.getTextContent(), Currency.EUR);
        } catch (ArithmeticException | NumberFormatException e) {
            throw new Iso20022Exception("bad amount " + amountElement.getTextContent() + ": " + e.getMessage(), e);
        }
    }

    /** Money -> "1234.56": the ISO 20022 decimal form, '.' separator, no grouping, no currency in the text. */
    static String decimal(Money m) {
        String s = m.toString();
        return s.substring(0, s.indexOf(' '));
    }

    static void amountEl(Element parent, String name, Money m) {
        el(parent, name, decimal(m)).setAttribute("Ccy", m.currency().code());
    }

    static String serialize(Document doc) {
        try {
            Transformer t = TransformerFactory.newInstance().newTransformer();
            t.setOutputProperty(OutputKeys.INDENT, "yes");
            t.setOutputProperty("{http://xml.apache.org/xslt}indent-amount", "2");
            t.setOutputProperty(OutputKeys.OMIT_XML_DECLARATION, "yes");
            StringWriter out = new StringWriter();
            out.write("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n");
            t.transform(new DOMSource(doc), new StreamResult(out));
            return out.toString().replace("\r\n", "\n");
        } catch (Exception e) {
            throw new Iso20022Exception("cannot serialize XML", e);
        }
    }

    /** ISODateTime with seconds, no zone: 2026-10-13T09:30:00 (LocalDateTime.toString drops ":00"). */
    static final DateTimeFormatter DATE_TIME = DateTimeFormatter.ofPattern("yyyy-MM-dd'T'HH:mm:ss");

    static void agent(Element parent, String name, String bic) {
        el(el(el(parent, name), "FinInstnId"), "BICFI", bic);
    }

    static void account(Element parent, String name, String iban) {
        el(el(el(parent, name), "Id"), "IBAN", iban);
    }
}

/** pain.001.001.09: Customer Credit Transfer Initiation, the file a company sends its bank. */
final class Pain001 {
    static final String NS = "urn:iso:std:iso:20022:tech:xsd:pain.001.001.09";

    static String write(PaymentInitiation p) {
        List<String> violations = SepaRules.violations(p);
        if (!violations.isEmpty()) throw new Iso20022Exception("SEPA rule violations: " + violations);
        Document doc = Xml.newDocument();
        Element root = doc.createElementNS(NS, "Document");
        doc.appendChild(root);
        Element init = Xml.el(root, "CstmrCdtTrfInitn");

        Element grp = Xml.el(init, "GrpHdr");
        Xml.el(grp, "MsgId", p.messageId());
        Xml.el(grp, "CreDtTm", Xml.DATE_TIME.format(p.createdAt()));
        Xml.el(grp, "NbOfTxs", Integer.toString(p.numberOfTransactions()));
        Xml.el(grp, "CtrlSum", Xml.decimal(p.controlSum()));
        Xml.el(Xml.el(grp, "InitgPty"), "Nm", p.initiatingParty());

        Element pmt = Xml.el(init, "PmtInf");
        Xml.el(pmt, "PmtInfId", p.paymentInfoId());
        Xml.el(pmt, "PmtMtd", "TRF");
        Xml.el(pmt, "NbOfTxs", Integer.toString(p.numberOfTransactions()));
        Xml.el(pmt, "CtrlSum", Xml.decimal(p.controlSum()));
        Xml.el(Xml.el(Xml.el(pmt, "PmtTpInf"), "SvcLvl"), "Cd", "SEPA");
        Xml.el(Xml.el(pmt, "ReqdExctnDt"), "Dt", p.requestedExecutionDate().toString());
        Xml.el(Xml.el(pmt, "Dbtr"), "Nm", p.debtor().name());
        Xml.account(pmt, "DbtrAcct", p.debtor().iban());
        if (p.debtor().bic() != null) Xml.agent(pmt, "DbtrAgt", p.debtor().bic());
        Xml.el(pmt, "ChrgBr", "SLEV");

        for (CreditTransfer t : p.transfers()) {
            Element tx = Xml.el(pmt, "CdtTrfTxInf");
            Xml.el(Xml.el(tx, "PmtId"), "EndToEndId", t.endToEndId());
            Xml.amountEl(Xml.el(tx, "Amt"), "InstdAmt", t.amount());
            if (t.creditor().bic() != null) Xml.agent(tx, "CdtrAgt", t.creditor().bic());
            Xml.el(Xml.el(tx, "Cdtr"), "Nm", t.creditor().name());
            Xml.account(tx, "CdtrAcct", t.creditor().iban());
            if (t.remittance() != null) Xml.el(Xml.el(tx, "RmtInf"), "Ustrd", t.remittance());
        }
        return Xml.serialize(doc);
    }

    static PaymentInitiation parse(String xml) {
        Element init = Xml.parseRoot(xml, NS, "CstmrCdtTrfInitn");
        Element grp = Xml.child(init, "GrpHdr");
        List<Element> pmts = Xml.children(init, "PmtInf");
        if (pmts.size() != 1) throw new Iso20022Exception("this parser handles exactly one PmtInf, found " + pmts.size());
        Element pmt = pmts.get(0);
        if (!"TRF".equals(Xml.text(pmt, "PmtMtd"))) throw new Iso20022Exception("PmtMtd must be TRF");
        Party debtor = new Party(Xml.text(pmt, "Dbtr", "Nm"), Xml.text(pmt, "DbtrAcct", "Id", "IBAN"),
                Xml.optText(pmt, "DbtrAgt", "FinInstnId", "BICFI"));
        List<CreditTransfer> transfers = new ArrayList<>();
        for (Element tx : Xml.children(pmt, "CdtTrfTxInf")) {
            Party creditor = new Party(Xml.text(tx, "Cdtr", "Nm"), Xml.text(tx, "CdtrAcct", "Id", "IBAN"),
                    Xml.optText(tx, "CdtrAgt", "FinInstnId", "BICFI"));
            transfers.add(new CreditTransfer(Xml.text(tx, "PmtId", "EndToEndId"), Xml.amount(Xml.child(tx, "Amt", "InstdAmt")),
                    creditor, Xml.optText(tx, "RmtInf", "Ustrd")));
        }
        PaymentInitiation p = new PaymentInitiation(Xml.text(grp, "MsgId"), LocalDateTime.parse(Xml.text(grp, "CreDtTm")),
                Xml.text(grp, "InitgPty", "Nm"), Xml.text(pmt, "PmtInfId"), LocalDate.parse(Xml.text(pmt, "ReqdExctnDt", "Dt")),
                debtor, transfers);
        checkTotals("GrpHdr", grp, p);
        checkTotals("PmtInf", pmt, p);
        return p;
    }

    /** The declared NbOfTxs and CtrlSum must equal what the message actually contains. */
    private static void checkTotals(String where, Element header, PaymentInitiation p) {
        int declaredCount = Integer.parseInt(Xml.text(header, "NbOfTxs"));
        if (declaredCount != p.numberOfTransactions()) {
            throw new Iso20022Exception(where + " NbOfTxs " + declaredCount + " != " + p.numberOfTransactions() + " transactions");
        }
        String ctrl = Xml.optText(header, "CtrlSum");
        if (ctrl != null && !Money.parse(ctrl, Currency.EUR).equals(p.controlSum())) {
            throw new Iso20022Exception(where + " CtrlSum " + ctrl + " != computed " + Xml.decimal(p.controlSum()));
        }
    }
}

/** pacs.008.001.08: FI to FI Customer Credit Transfer, what the debtor bank sends to the clearing system. */
final class Pacs008 {
    static final String NS = "urn:iso:std:iso:20022:tech:xsd:pacs.008.001.08";

    /**
     * Field mapping pain.001 -> pacs.008. EndToEndId passes through unchanged; TxId is new and owned by the
     * debtor bank; ReqdExctnDt becomes IntrBkSttlmDt; InstdAmt becomes IntrBkSttlmAmt (same currency in SEPA).
     * A missing creditor BIC (IBAN-only initiation) is derived from the IBAN through the bank's directory.
     */
    static InterbankMessage fromPain001(PaymentInitiation p, String messageId, LocalDateTime createdAt,
                                        Function<String, String> bicDirectory) {
        List<InterbankTransfer> out = new ArrayList<>();
        int seq = 1;
        for (CreditTransfer t : p.transfers()) {
            Party creditor = t.creditor();
            if (creditor.bic() == null) {
                String bic = bicDirectory.apply(creditor.iban());
                if (bic == null) throw new Iso20022Exception("no BIC for IBAN " + creditor.iban() + " in directory");
                creditor = new Party(creditor.name(), creditor.iban(), bic);
            }
            String txId = messageId + "-" + String.format("%04d", seq++);
            out.add(new InterbankTransfer(t.endToEndId(), txId, t.amount(), p.debtor(), creditor, t.remittance()));
        }
        return new InterbankMessage(messageId, createdAt, p.requestedExecutionDate(), out);
    }

    static String write(InterbankMessage m) {
        Document doc = Xml.newDocument();
        Element root = doc.createElementNS(NS, "Document");
        doc.appendChild(root);
        Element msg = Xml.el(root, "FIToFICstmrCdtTrf");
        Element grp = Xml.el(msg, "GrpHdr");
        Xml.el(grp, "MsgId", m.messageId());
        Xml.el(grp, "CreDtTm", Xml.DATE_TIME.format(m.createdAt()));
        Xml.el(grp, "NbOfTxs", Integer.toString(m.transfers().size()));
        Xml.amountEl(grp, "TtlIntrBkSttlmAmt", m.totalSettlementAmount());
        Xml.el(grp, "IntrBkSttlmDt", m.settlementDate().toString());
        Xml.el(Xml.el(grp, "SttlmInf"), "SttlmMtd", "CLRG");
        for (InterbankTransfer t : m.transfers()) {
            Element tx = Xml.el(msg, "CdtTrfTxInf");
            Element pmtId = Xml.el(tx, "PmtId");
            Xml.el(pmtId, "EndToEndId", t.endToEndId());
            Xml.el(pmtId, "TxId", t.transactionId());
            Xml.el(Xml.el(Xml.el(tx, "PmtTpInf"), "SvcLvl"), "Cd", "SEPA");
            Xml.amountEl(tx, "IntrBkSttlmAmt", t.amount());
            Xml.el(tx, "ChrgBr", "SLEV");
            Xml.el(Xml.el(tx, "Dbtr"), "Nm", t.debtor().name());
            Xml.account(tx, "DbtrAcct", t.debtor().iban());
            Xml.agent(tx, "DbtrAgt", t.debtor().bic());
            Xml.agent(tx, "CdtrAgt", t.creditor().bic());
            Xml.el(Xml.el(tx, "Cdtr"), "Nm", t.creditor().name());
            Xml.account(tx, "CdtrAcct", t.creditor().iban());
            if (t.remittance() != null) Xml.el(Xml.el(tx, "RmtInf"), "Ustrd", t.remittance());
        }
        return Xml.serialize(doc);
    }

    static InterbankMessage parse(String xml) {
        Element msg = Xml.parseRoot(xml, NS, "FIToFICstmrCdtTrf");
        Element grp = Xml.child(msg, "GrpHdr");
        List<InterbankTransfer> transfers = new ArrayList<>();
        for (Element tx : Xml.children(msg, "CdtTrfTxInf")) {
            Party debtor = new Party(Xml.text(tx, "Dbtr", "Nm"), Xml.text(tx, "DbtrAcct", "Id", "IBAN"), Xml.text(tx, "DbtrAgt", "FinInstnId", "BICFI"));
            Party creditor = new Party(Xml.text(tx, "Cdtr", "Nm"), Xml.text(tx, "CdtrAcct", "Id", "IBAN"), Xml.text(tx, "CdtrAgt", "FinInstnId", "BICFI"));
            transfers.add(new InterbankTransfer(Xml.text(tx, "PmtId", "EndToEndId"), Xml.text(tx, "PmtId", "TxId"),
                    Xml.amount(Xml.child(tx, "IntrBkSttlmAmt")), debtor, creditor, Xml.optText(tx, "RmtInf", "Ustrd")));
        }
        InterbankMessage m = new InterbankMessage(Xml.text(grp, "MsgId"), LocalDateTime.parse(Xml.text(grp, "CreDtTm")),
                LocalDate.parse(Xml.text(grp, "IntrBkSttlmDt")), transfers);
        int declared = Integer.parseInt(Xml.text(grp, "NbOfTxs"));
        if (declared != transfers.size()) throw new Iso20022Exception("NbOfTxs " + declared + " != " + transfers.size() + " transactions");
        Money total = Xml.amount(Xml.child(grp, "TtlIntrBkSttlmAmt"));
        if (!total.equals(m.totalSettlementAmount())) {
            throw new Iso20022Exception("TtlIntrBkSttlmAmt " + total + " != computed " + m.totalSettlementAmount());
        }
        return m;
    }
}

public final class Main {
    static final LocalDateTime NOW = LocalDateTime.of(2026, 10, 13, 9, 30, 0);

    static PaymentInitiation demoInitiation() {
        Party debtor = new Party("Acme Robotics GmbH", "DE89370400440532013000", "COBADEFFXXX");
        return new PaymentInitiation("ACME-20261013-001", NOW, "Acme Robotics GmbH", "PAYRUN-42", LocalDate.of(2026, 10, 14), debtor, List.of(
                new CreditTransfer("INV-2026-0912", Money.parse("1250.00", Currency.EUR),
                        new Party("Fournitures Lyon SARL", "FR1420041010050500013M02606", "PSSTFRPPLYO"), "Invoice 2026-0912"),
                new CreditTransfer("INV-2026-0913", Money.parse("349.99", Currency.EUR),
                        new Party("Delft Sensors BV", "NL91ABNA0417164300", "ABNANL2A"), "Invoice 2026-0913"),
                new CreditTransfer("INV-2026-0914", Money.parse("12000.50", Currency.EUR),
                        new Party("Componentes Bilbao SL", "ES9121000418450200051332", null), "Invoice 2026-0914 batch 7")));
    }

    static Function<String, String> demoDirectory() {
        Map<String, String> byBankCode = Map.of("ES2100", "CAIXESBBXXX", "NLABNA", "ABNANL2A", "DE37040044", "COBADEFFXXX");
        return iban -> byBankCode.get(iban.substring(0, 2) + Iban.bankCode(iban));
    }

    public static void main(String[] args) {
        PaymentInitiation p = demoInitiation();
        System.out.println("== SEPA validation ==");
        System.out.println("violations: " + SepaRules.violations(p));
        System.out.println("NbOfTxs " + p.numberOfTransactions() + ", CtrlSum " + p.controlSum());

        System.out.println("\n== pain.001.001.09 (customer -> debtor bank) ==");
        String pain = Pain001.write(p);
        System.out.print(pain);
        PaymentInitiation back = Pain001.parse(pain);
        System.out.println("round trip equal: " + back.equals(p));

        System.out.println("\n== pacs.008.001.08 (debtor bank -> clearing) ==");
        InterbankMessage m = Pacs008.fromPain001(back, "COBA-20261013-7781", NOW.plusMinutes(5), demoDirectory());
        String pacs = Pacs008.write(m);
        int firstTx = pacs.indexOf("</CdtTrfTxInf>") + "</CdtTrfTxInf>".length();
        System.out.println(pacs.substring(0, firstTx));
        System.out.println("    ... 2 more CdtTrfTxInf ...");
        InterbankMessage parsed = Pacs008.parse(pacs);
        System.out.println("round trip equal: " + parsed.equals(m));
        for (InterbankTransfer t : parsed.transfers()) {
            System.out.println(t.endToEndId() + " -> TxId " + t.transactionId() + ", " + t.amount() + " to " + t.creditor().bic());
        }

        System.out.println("\n== Rejections ==");
        PaymentInitiation bad = new PaymentInitiation("ACME/BAD//1", NOW, "Acme", "PAYRUN-43", LocalDate.of(2026, 10, 12),
                new Party("Acme Robotics GmbH", "DE89370400440532013001", "COBADEFF"), List.of(
                new CreditTransfer("INV-1", Money.parse("10.00", Currency.USD), new Party("Z\u00fcrich Pr\u00e4zision AG", "NL91ABNA0417164300", null), null)));
        SepaRules.violations(bad).forEach(v -> System.out.println("rule: " + v));
        String tampered = pain.replace("<CtrlSum>13600.49</CtrlSum>", "<CtrlSum>13600.50</CtrlSum>");
        try { Pain001.parse(tampered); } catch (Iso20022Exception e) { System.out.println("rejected: " + e.getMessage()); }
        try { Pain001.parse(pain.replace("349.99", "349.999")); } catch (Iso20022Exception e) { System.out.println("rejected: " + e.getMessage()); }
        try { Pacs008.parse(pain); } catch (Iso20022Exception e) { System.out.println("rejected: " + e.getMessage()); }
        String xxe = "<?xml version=\"1.0\"?><!DOCTYPE d [<!ENTITY x SYSTEM \"file:///etc/passwd\">]><Document xmlns=\"" + Pain001.NS + "\">&x;</Document>";
        try { Pain001.parse(xxe); } catch (Iso20022Exception e) { System.out.println("rejected: DOCTYPE (XXE guard)"); }
    }
}
