# Bank Statement Formats: MT940, CAMT.053, BAI2, OFX

> Parse what the bank sends you before you can reconcile it.

**Type:** Build
**Languages:** Java
**Prerequisites:** Phase 10, Lesson 01 (Representing Money); Phase 11, Lesson 08 (ACH and the NACHA File Format); Phase 11, Lesson 09 (SEPA, Wires, and ISO 20022)
**Time:** ~90 minutes

## Learning Objectives

- Parse MT940, camt.053, BAI2, and OFX statements into one normalized model with signed integer amounts, booking and value dates, and both customer and bank references
- State and check the balance continuity invariant within a statement (opening plus movements equals closing) and across statements (each closing equals the next opening)
- Handle each format's traps: MT940 continuation lines, reversal marks, and two-digit years; camt.053 entry status and reversal indicator; BAI2 88 continuations and control totals; OFX's unclosed SGML tags and missing opening balance
- Detect a statement's format from its first bytes and reject anything that is not one of the four
- Explain which identifiers in each format carry your own payment reference (EndToEndId, customer reference, REFNUM) and which carry the bank's (AcctSvcrRef, bank reference, FITID), and why reconciliation needs both

## The Problem

A European e-commerce company receives an MT940 statement from its bank every morning for each of its eleven accounts. A nightly job parses the files, writes one row per statement line into the finance database, and a reconciliation job (Phase 12) matches those rows against expected payments. For months the job reports a clean match rate of 99.7%. Then the auditors ask for a proof that the balance in the finance database equals the balance at the bank on each day of the year, and nobody can produce one. When an engineer finally computes opening balance plus lines for each day and compares it with the next day's opening balance, the numbers drift apart on 14 days. On one of them the bank charged a 12.50 EUR fee, sent the statement, then reversed the fee and re-issued the next day's statement opening 12.50 higher, with no line explaining the difference. On others, the parser had treated a continuation line of a multi-line `:86:` field that happened to start with a colon as a new field, and silently dropped it. The reconciliation job matched lines; nothing checked the balances.

The company's US subsidiary has the other problem. Its bank sends BAI2. The original parser was written against a two-line sample, split each line on commas, and took the last field as the description. In production, the bank wraps long descriptions onto `88` continuation records. The parser ignored lines it did not recognise, so every description longer than about 80 characters lost its second half, including the invoice number the reconciliation job matched on. Unmatched receipts piled up in a suspense account until someone read the raw file.

A third team imports customer bank data for a lending product through OFX downloads. Its parser computed an "opening balance" by subtracting the transactions from the ledger balance and then checked continuity, which by construction always passed. The check was a tautology. OFX does not report an opening balance, and no check within one OFX file can detect a missing transaction.

Every reconciliation system starts with a parser for whatever the bank sends. If that parser loses a line, mis-signs a reversal, drops a continuation, or cannot tell you whether the balances add up, every report built on it is wrong in a way that matching rates will not reveal. This lesson builds parsers for the four formats you are most likely to receive, normalizes them into one model, and checks the one invariant that proves the statement is complete.

## The Concept

### Four formats, one job

A bank statement answers three questions for one account over one period: what was the balance at the start, what moved, and what was the balance at the end. Every format carries that information. They differ in syntax, in how much each line says, and in which redundancy they give you to check it.

| Format | Origin | Syntax | Typical use | Opening balance | Redundancy for checking |
|--------|--------|--------|-------------|-----------------|-------------------------|
| MT940 | SWIFT, 1980s | Tagged text lines (`:61:`), SWIFT X character set | Corporate statements worldwide, especially Europe | Yes (`:60F:`) | Opening and closing balances; statement number `:28C:` |
| camt.053 | ISO 20022, 2009 onward | XML | Successor to MT940; mandatory in some markets, standard in SEPA | Yes (`OPBD` or `PRCD`) | Balances, entry counts and sums (`TxsSummry`), electronic sequence number |
| BAI2 | Bank Administration Institute, 1987; now ANSI X9.121 | Comma-delimited records ending in `/` | US corporate cash reporting | Yes (type code `010`) | Control totals and record counts at account, group, and file level |
| OFX | Microsoft, Intuit, CheckFree, 1997 | SGML (1.x) or XML (2.x) | Consumer and small business downloads, accounting software | No, only `LEDGERBAL` | None within a file; `FITID` uniqueness across files |

A normalized model makes the four look the same to everything downstream:

```java
record StatementLine(LocalDate bookingDate, LocalDate valueDate, Money amount, String reference,
                     String bankReference, String description) {}

record Statement(StatementFormat source, String accountId, Currency currency, String statementId,
                 LocalDate openingDate, Money opening, LocalDate closingDate, Money closing,
                 List<StatementLine> lines, boolean openingReported) {}
```

Three design decisions are worth stating.

**Amounts are signed.** A credit to the account is positive, a debit negative. Each format encodes direction differently (a `C`/`D` mark, a `CRDT`/`DBIT` indicator, a type-code range, a minus sign), and the parser is the only place that should know. Downstream code adds amounts and never asks which way they went.

**Two references per line.** `reference` is the identifier you or your counterparty supplied: the MT940 customer reference, the camt.053 `EndToEndId`, the OFX `REFNUM`. It is what reconciliation matches on. `bankReference` is the bank's own identifier for the booking: the MT940 bank reference after `//`, the camt.053 `AcctSvcrRef`, the BAI2 bank reference, the OFX `FITID`. It is what you quote when you call the bank, and it is the key for de-duplicating a line that appears in two files.

**Booking date and value date are different.** The booking date is when the bank posted the entry to the account. The value date is when it starts or stops earning interest and, for most purposes, when the money counts as available. A cheque deposited on Friday may be booked Friday and valued Tuesday. Cash forecasting uses value dates; continuity checks use booking dates.

### The invariant

Within one statement:

```text
opening + sum(lines) = closing
```

Across consecutive statements for one account:

```text
closing(day n) = opening(day n + 1)
```

The first check proves the statement is internally complete: no line was dropped, duplicated, or mis-signed by the parser or the bank. The second proves the sequence is complete: no statement is missing and nothing changed between statements without a line. Each check catches failures the other cannot. In The Problem, the reversed fee passes the first check on both days and fails the second.

A statement whose format gives only a closing balance (OFX) cannot be checked against itself; any opening you compute from the closing balance makes the first check a tautology. The model records `openingReported = false` for such statements so that code and people do not mistake a derived opening for evidence.

### Worked example by hand

The four samples in `Samples` describe the same day on one account (EUR for the MT940 and camt.053 samples, USD for BAI2 and OFX):

```text
opening                          12500.00
INV-4411   supplier payment      -1500.00
ORDER-77   customer receipt      +3250.75
(none)     maintenance fee         -12.50
closing                          14238.25

in minor units: 1250000 - 150000 + 325075 - 1250 = 1423825
```

Now suppose the bank's closing balance is `14283.25`, a transposition of two digits. The check computes `1250000 + 173825 = 1423825`, compares with `1428325`, and reports a difference of `4500`, 45.00. A difference divisible by 9 in minor units (4500 here) is the classic signature of two transposed digits, which is worth saying in the alert.

For the chain, day 2 opens at `14250.75` instead of `14238.25`. Day 2 is internally consistent (`1425075 + 10000 = 1435075`), so the within-statement check passes. The chain check reports a gap of `1250`, exactly the fee that was reversed between statements without a line.

### MT940 in detail

An MT940 is a sequence of fields, each starting with `:tag:` at the beginning of a line. A line that does not start a tag continues the previous field.

```text
{1:F01COBADEFFAXXX0000000000}{2:O940...}{4:
:20:STMT261012                         transaction reference (the statement's id)
:25:DE89370400440532013000             account identification
:28C:00287/001                         statement number / sequence (page)
:60F:C261009EUR12500,00                opening balance: C/D, date YYMMDD, currency, amount
:61:2610121012D1500,00NTRFINV-4411//COBA0001
:86:Supplier payment INV-4411          information to account owner, may span lines
Fournitures Lyon SARL
:62F:C261012EUR14238,25                closing balance
:64:C261012EUR14238,25                 closing available balance
-}
```

The `:61:` statement line packs nine subfields into one string with no separators except `//`:

| Part | Example | Notes |
|------|---------|-------|
| Value date | `261012` | YYMMDD |
| Entry date | `1012` | Optional MMDD; the year is inferred from the value date, which breaks at year end if done naively |
| Debit/credit mark | `D` | `C`, `D`, `RC` (reversal of credit, which is a debit), `RD` (reversal of debit, which is a credit) |
| Funds code | (absent) | Optional third letter of the currency code |
| Amount | `1500,00` | Comma is the decimal separator; `1500,` is valid |
| Transaction type | `NTRF` | `N` plus a three-character code (`TRF`, `CHG`, `CHK`, `MSC`...) |
| Customer reference | `INV-4411` | Up to 16 characters; `NONREF` means none |
| Bank reference | `//COBA0001` | Optional, up to 16 characters |
| Supplementary details | next line | Optional, up to 34 characters |

`:60F:` and `:62F:` are the first opening and final closing balance. A statement too long for one message is split across several, each with an intermediate opening `:60M:` and closing `:62M:`, and `:28C:` numbers the pages. The `:86:` field is free text in the standard but structured by national conventions: German banks use `?20` to `?29` subfields for remittance lines, Dutch banks use `/EREF/` and `/REMI/` tags. A parser that keeps the raw text, as this one does, leaves room to add a convention-specific decoder later.

SWIFT ended the coexistence of MT and ISO 20022 for cross-border payment instructions in November 2025. Statement reporting (MT940, MT942, MT950) moves on a slower, bank-by-bank schedule, so MT940 will be in your inbound files for years.

### camt.053 in detail

camt.053 (Bank to Customer Statement) is the ISO 20022 replacement for MT940. It carries more structure: typed balances, entry status, a bank transaction code taxonomy, and the full ISO 20022 remittance and reference blocks from the original payment.

```xml
<Stmt>
  <Id>STMT261012</Id>
  <Acct><Id><IBAN>DE89370400440532013000</IBAN></Id><Ccy>EUR</Ccy></Acct>
  <Bal><Tp><CdOrPrtry><Cd>OPBD</Cd></CdOrPrtry></Tp><Amt Ccy="EUR">12500.00</Amt><CdtDbtInd>CRDT</CdtDbtInd>...</Bal>
  <Ntry>
    <Amt Ccy="EUR">1500.00</Amt><CdtDbtInd>DBIT</CdtDbtInd><Sts><Cd>BOOK</Cd></Sts>
    <BookgDt><Dt>2026-10-12</Dt></BookgDt><ValDt><Dt>2026-10-12</Dt></ValDt>
    <AcctSvcrRef>COBA0001</AcctSvcrRef>
    <NtryDtls><TxDtls><Refs><EndToEndId>INV-4411</EndToEndId></Refs></TxDtls></NtryDtls>
  </Ntry>
</Stmt>
```

Four details decide whether a camt.053 parser is correct:

- **Amounts are unsigned.** Direction is in `CdtDbtInd`. A negative balance is `DBIT`.
- **Entry status.** `BOOK` entries move the booked balance. `PDNG` (pending) and `INFO` entries are information and must be excluded from the continuity sum. The sample has a 999.00 pending entry that would break continuity if counted.
- **Reversal indicator.** `RvslInd` set to `true` marks a reversal; the `CdtDbtInd` then states the direction of the original entry, so the effect on the balance is the opposite. This is the camt.053 equivalent of MT940's `RC`/`RD`.
- **Batch entries.** One `Ntry` can contain many `TxDtls`, for example a single debit of 13,600.49 for the three pain.001 transfers in Lesson 09. Reconciliation must look inside `NtryDtls` to match individual payments; continuity uses the entry amount.

The opening balance type is `OPBD` (opening booked) or, at some banks, `PRCD` (previously closed booked). Closing is `CLBD`. Available balances (`CLAV`, `FWAV`) are reported alongside but are not part of the continuity check.

### BAI2 in detail

BAI2 is a hierarchy of comma-delimited records, each ending in `/`, with control totals at every level:

```text
01,021000021,ACMEUS,261012,2200,1,80,1,2/             file header
02,ACMEUS,021000021,1,261012,2200,USD,2/              group header (as-of date 261012, currency)
03,0001234567,USD,010,1250000,,,015,1423825,,/        account: 010 opening ledger, 015 closing ledger
16,495,150000,Z,COBA0001,INV-4411,Supplier payment INV-4411
88,Fournitures Lyon SARL                              continuation of the previous record
16,195,325075,Z,COBA0002,ORDER-77,Customer receipt ORDER-77 Delft Sensors BV
16,698,1250,Z,COBA0003,,Account maintenance fee
49,3150150,6/                                         account trailer: control total, record count
98,3150150,1,8/                                       group trailer: total, accounts, records
99,3150150,1,10/                                      file trailer: total, groups, records
```

The rules a parser must follow:

- **Amounts are integers** in the currency's minor units, with no decimal point. `150000` is 1,500.00 USD.
- **Direction is in the type code.** Detail type codes 100 to 399 are credits, 400 to 699 are debits. Summary type codes in the `03` record (`010` opening ledger, `015` closing ledger, `040` opening available, `100` total credits, `400` total debits) describe balances and totals.
- **The `16` record's text field runs to the end of the record** and may contain commas. Only the fields before it are split. The number of fields before it depends on the funds type: `0`, `1`, `2`, and `Z` add none; `V` adds a value date and time; `S` adds three distributed-availability amounts.
- **`88` continues the previous record.** For a `16` it extends the text; for an `03` it adds more summary fields.
- **Control totals are sums of every amount** in the `03` and `16` records of the account, balances included, not a net movement. `1250000 + 1423825 + 150000 + 325075 + 1250 = 3150150`. The record count includes the `03`, every `16` and `88`, and the `49` itself. Group and file trailers sum the level below.

The control total is a checksum, not a balance: it detects a dropped or altered record, but a parser that also verifies `010 + net movements = 015` is checking something different and stronger.

### OFX in detail

OFX 1.x is SGML: aggregate elements have closing tags, leaf elements do not.

```text
<STMTTRN><TRNTYPE>DEBIT<DTPOSTED>20261012<TRNAMT>-1500.00<FITID>COBA0001<REFNUM>INV-4411<NAME>Fournitures Lyon SARL</STMTTRN>
<LEDGERBAL><BALAMT>14238.25<DTASOF>20261012220000</LEDGERBAL>
```

An XML parser rejects this. The parser in this lesson tokenizes tags and treats a tag followed by text as a leaf and a tag followed directly by another tag as an aggregate; a closing tag for a leaf, as OFX 2.x writes it, is ignored. Amounts are signed decimals. Dates are `YYYYMMDD` optionally followed by time and a time-zone suffix (`20261012220000[-5:EST]`). `FITID` is the bank's unique id for the transaction and is the only reliable de-duplication key when downloads overlap, which they do: a user who downloads "last 30 days" every week gets each transaction four times.

OFX gives a ledger balance as of a timestamp and no opening balance. Continuity can only be checked across downloads: the ledger balance of one download, minus transactions posted after it, should equal the earlier download's ledger balance.

### Format detection

Each format announces itself in its first bytes: `{1:` or `:20:` for MT940, `01,` for BAI2, `OFXHEADER` or `<OFX>` for OFX, and an XML root in the camt.053 namespace. `StatementFormat.detect` checks those and throws for anything else, including the CSV export a bank portal produces when someone clicks the wrong button. Guessing is how a CSV with a different column order gets loaded as statement lines.

### Complexity

Every parser is a single pass: O(n) in the size of the file, and the continuity and chain checks are O(lines) and O(statements). The camt.053 parser uses DOM, which holds the document in memory; a year of statements for a high-volume account can run to hundreds of megabytes and should be parsed with StAX. The MT940, BAI2, and OFX parsers could stream line by line with the same logic.

### What these parsers do not do

They do not decode national `:86:` conventions or camt.053 bank transaction codes (`BkTxCd`) into categories. They handle one account per camt.053 `Stmt` and only the first `Stmt` in a message. They do not process MT942 or camt.052 intraday reports, camt.054 notifications, or BAI2 funds type `D`. They do not reconcile; they produce the input to reconciliation, which matches `reference` against expected payments (Phase 12). And they do not authenticate the file: a statement that arrives over SFTP, EBICS, or the SWIFT network carries its own integrity and authentication, which belong to the transport, not the parser.

## Build It

The code is in `code/java/`. `Money.java` is the Phase 10, Lesson 01 design with one addition, `Currency.of(code)`, because statements name their currency as text. Everything else is in `Main.java`.

### Step 1: The model and its invariant

```java
List<String> continuityErrors() {
    List<String> errors = new ArrayList<>();
    Money expected = opening.plus(movement());
    if (!expected.equals(closing)) {
        errors.add(statementId + ": opening " + opening + " + movements " + movement() + " = " + expected
                + ", but closing is " + closing + " (difference " + closing.minus(expected) + ")");
    }
    ...
    return errors;
}
```

The check returns a list of problems instead of throwing, because a statement that does not add up is still data you need to look at: you want to load it, flag it, and show the difference to an operator. `Money` guarantees that every line is in the statement's currency; a mixed-currency statement fails at `plus`.

Predict: a parser reads every MT940 amount with the wrong sign. Does the continuity check catch it?

### Step 2: Chains across statements

```java
static List<String> gaps(List<Statement> statements) {
    for (int i = 1; i < statements.size(); i++) {
        Statement prev = statements.get(i - 1), next = statements.get(i);
        ...
        if (!next.closingDate().isAfter(prev.closingDate())) { ... out of order ... }
        if (!prev.closing().equals(next.opening())) {
            errors.add("gap between " + prev.statementId() + " closing " + prev.closing() + " and "
                    + next.statementId() + " opening " + next.opening() + " (missing " + next.opening().minus(prev.closing()) + ")");
        }
    }
}
```

The gap message prints the signed difference, because the size of a gap is the best clue to its cause: a gap equal to a known fee, a gap equal to a whole missing statement's movement, or a gap of 0.01 from a bank's own rounding.

Predict: a statement is missing entirely, but the bank's next statement opens where the missing one closed. What does the chain check report?

### Step 3: MT940 fields and the `:61:` regex

```java
private static final Pattern LINE = Pattern.compile(
        "^(\\d{6})(\\d{4})?(RC|RD|C|D)([A-Z])?(\\d+,\\d{0,2})([NFS][A-Z0-9]{3})([^\\n]*?)(?://([^\\n]{1,16}))?(?:\\n(.*))?$", Pattern.DOTALL);
```

`fields()` turns the message into `(tag, value)` pairs by starting a new field on any line matching `:NN[A]:` and appending everything else to the current field with a newline. That is how a two-line `:86:` stays one field. The `:61:` regex lists `RC|RD` before `C|D` so that a reversal mark is not read as `R` plus a funds code. The amount allows at most two decimals, so `12,505` fails to match and the whole line is rejected rather than silently rounded.

The entry date has no year:

```java
if (month - value.getMonthValue() > 6) year--;   // value 2027-01-02, entry 1231 -> 2026-12-31
if (value.getMonthValue() - month > 6) year++;   // value 2026-12-31, entry 0102 -> 2027-01-02
```

Predict: what booking date would a parser that always uses the value date's year assign to a line with value date `270102` and entry date `1231`?

### Step 4: MT940 envelopes and multiple statements

```java
String body = Parse.normalizeNewlines(text)
        .replaceAll("\\{1:[^}]*\\}(\\{2:[^}]*\\})?(\\{3:(\\{[^}]*\\})*\\})?\\{4:", "")
        .replaceAll("(?m)^-\\}.*$", "");
```

A file from a bank often concatenates several SWIFT messages, each wrapped in `{1:...}{2:...}{4:` and `-}`. The first version of this parser sliced the text from the first `{4:` to the first `-}` and silently dropped every statement after the first. Stripping the envelope tokens and splitting on `:20:` keeps them all. `parseAll` returns every statement; `parse` insists on exactly one.

Predict: why does the parser split statements on `:20:` rather than on `-}`?

### Step 5: camt.053 status, reversal, and currency

```java
for (Element ntry : children(stmt, "Ntry")) {
    String status = text(child(child(ntry, "Sts"), "Cd"));
    if (!status.equals("BOOK")) continue;
    Money amount = amount(ntry, currency);
    Element rvsl = optChild(ntry, "RvslInd");
    if (rvsl != null && text(rvsl).equals("true")) amount = amount.negate();
    ...
}
```

`amount()` reads the unsigned amount, checks that its `Ccy` matches the account currency, and applies `CdtDbtInd`. A camt.053 for a EUR account can contain an entry whose original instruction was in USD; the booked `Amt` is still in EUR and the original amount lives in `AmtDtls`. An `Amt` in a currency other than the account's is a malformed statement. The XML parser uses the same DOCTYPE-disallowing configuration as Lesson 09.

Predict: if the parser counted the `PDNG` entry, what would the continuity check report for the sample?

### Step 6: BAI2 logical records and control totals

```java
if (type.equals("88")) {
    LogicalRecord prev = out.remove(out.size() - 1);
    String rest = line.substring(3);
    if (prev.type().equals("16")) {
        out.add(new LogicalRecord("16", prev.fields(), (prev.text() + " " + rest).strip(), prev.physicalLines() + 1));
    } else { ... append fields ... }
}
```

Parsing BAI2 is two passes over a small structure. The first joins `88` continuations into logical records and remembers how many physical lines each one used, because the trailers count physical records. The `16` record is split into exactly as many fields as its funds type requires, and the rest is the text. The second pass walks the hierarchy and checks every trailer:

```java
case "49" -> {
    long declared = amount(f.get(0));
    int declaredRecords = Integer.parseInt(f.get(1));
    if (declared != accountTotal) throw new StatementParseException("BAI2: account " + account + " control total ...");
    if (declaredRecords != accountRecords) throw new StatementParseException("BAI2: account " + account + " record count ...");
    ...
}
```

Unlike continuity, a control-total failure is an exception: the file is structurally corrupt and its contents cannot be trusted at all.

Predict: the original parser in The Problem ignored `88` records. Would it have passed the `49` record-count check?

### Step 7: OFX tokenizing

```java
if (tag.startsWith("/")) {
    String name = tag.substring(1);
    if (stack.stream().anyMatch(n -> n.name.equals(name))) {
        while (!stack.peek().name.equals(name)) stack.pop();
        stack.pop();
    }
} else if (!value.isEmpty()) {
    stack.peek().children.add(new OfxNode(tag, value));   // leaf
} else {
    OfxNode agg = new OfxNode(tag, null);                    // aggregate
    stack.peek().children.add(agg);
    stack.push(agg);
}
```

The rule "text after the tag means leaf" handles both OFX 1.x and 2.x. A closing tag that matches nothing on the stack is a leaf's closing tag from OFX 2.x and is ignored; a closing tag that matches an ancestor pops to it, which recovers from the occasional missing close in real bank files. The parser then derives the opening balance and sets `openingReported` to false.

Predict: why is the test "OFX opening is derived" checking `openingReported` and not `continuityErrors()`?

### Step 8: Run it

```bash
cd phases/11-payment-rails/14-bank-statements-and-formats/code/java
javac -d .out *.java && java -cp .out Main
java -cp .out MainTest
```

Expected output, abbreviated:

```console
== Four formats, one normalized model ==
MT940  account DE89370400440532013000  STMT261012/00287/001  2026-10-09 12500.00 EUR -> 2026-10-12 14238.25 EUR
   2026-10-12  -1500.00 EUR  ref=INV-4411  bank=COBA0001  Supplier payment INV-4411 Fournitures Lyon SARL
   2026-10-12   3250.75 EUR  ref=ORDER-77  bank=COBA0002  Customer receipt ORDER-77 Delft Sensors BV
   2026-10-12    -12.50 EUR  ref=          bank=COBA0003  Account maintenance fee
   continuity: OK (opening + 1738.25 EUR = closing)
CAMT053 ...
BAI2 ...
OFX  account 0001234567  OFX-0001234567-20261012  2026-10-12 12500.00 USD -> 2026-10-12 14238.25 USD  (opening derived)

== Same day, same minor-unit movements? ==
MT940 [-150000, 325075, -1250] closing 1423825
CAMT053 [-150000, 325075, -1250] closing 1423825
BAI2 [-150000, 325075, -1250] closing 1423825
OFX [-150000, 325075, -1250] closing 1423825

== A statement that does not add up ==
   STMT261012/00287/001: opening 12500.00 EUR + movements 1738.25 EUR = 14238.25 EUR, but closing is 14283.25 EUR (difference 45.00 EUR)

== Chains: day 2 must open where day 1 closed ==
   statements: 2, gaps: []
   each internally continuous: true
   gap between STMT261012/00287/001 closing 14238.25 EUR and STMT261013/00288/001 opening 14250.75 EUR (missing 12.50 EUR)

== Rejections ==
   BAI2: account 0001234567 control total 3150151 != computed 3150150
   MT940: bad :61: 2610121012D12,505NCHGNONREF//COBA0003
   unrecognised statement format: Date,Description,Amo
```

`MainTest` runs 49 checks across detection, normalization, each format's traps, the continuity and chain invariants, and the rejections.

### Porting notes

Python's `re` and `xml.etree.ElementTree` cover MT940 and camt.053 directly (use namespaced tags, and refuse input containing `<!DOCTYPE` or use `defusedxml`); `Decimal` converts amounts exactly before they become integer minor units. In Go, `encoding/xml` handles camt.053 with struct tags, and the BAI2 and MT940 parsers map naturally onto `bufio.Scanner`, which also gives streaming for free. Return `(Statement, error)` from each parser and keep `ContinuityErrors` returning a slice, matching the Java split between structural errors (exceptions) and balance problems (data).

## Use It

**Bank connectivity.** Corporate statements arrive over SFTP, EBICS (Germany, France, Switzerland), SWIFT FileAct, or a bank API. Treasury management systems (Kyriba, FIS Quantum, SAP Bank Communication Management) and ERPs (SAP's electronic bank statement, Oracle Cash Management) ship parsers for all four formats plus country variants, and map lines to accounting entries through rules keyed on references and bank transaction codes.

**Libraries.** Prowide Core parses MT940 and the other SWIFT MT messages in Java; Prowide ISO 20022 covers camt.053. Python has `mt940` and `ofxparse` on PyPI. Moov's open source projects cover Nacha and ISO 20022 in Go. Each handles format detail this lesson skips; none checks continuity across files, which is your job.

**Aggregators.** Open banking and aggregation APIs (Lesson 13) return transactions as JSON, normalized by the aggregator from whatever the bank exposes. The same invariant applies: if the API reports balances, check them against the transactions, because aggregator pipelines drop and duplicate transactions too.

**The trap.** Every reconciliation team eventually loads the same transaction twice: from an MT940 and an intraday MT942, from overlapping OFX downloads, from a re-sent camt.053. De-duplicate on `bankReference` plus account plus booking date, and treat a duplicate with a different amount as an incident, not a merge.

## Ship It

`outputs/runbook-statement-ingestion.md` is a runbook for the job that ingests bank statements: what to check on arrival, what to do when continuity fails or a chain has a gap, how to de-duplicate, and when to call the bank. Use it as the on-call guide for a reconciliation pipeline and as the review list when adding a new bank or format.

## Exercises

1. **Reinforce.** Add MT940 `:86:` decoding for the German `?20`-`?29` convention: extract the `EREF+` value from the remittance subfields as the `reference` when the `:61:` customer reference is `NONREF`. Add tests with a two-line `:86:` where the `?2x` subfield is split across lines.
2. **Apply.** camt.053 can carry `TxsSummry/TtlNtries` with `NbOfNtries` and `Sum`, plus `TtlCdtNtries` and `TtlDbtNtries`. Parse them when present and verify them against the booked entries, reporting mismatches through `continuityErrors()`. Then add a statement that contains one batch `Ntry` of 13,600.49 with three `TxDtls`, and expose the transaction details so the reconciliation layer can match each pain.001 transfer from Lesson 09.
3. **Extend.** Build OFX continuity across downloads: given two OFX files for the same account with overlapping date ranges, de-duplicate on `FITID`, then check that the earlier file's ledger balance plus the transactions posted after its `DTASOF` equals the later file's ledger balance. Then combine with Phase 10's ledger lessons: post every normalized line into a double-entry ledger (bank account versus suspense) and prove that the ledger balance of the bank account equals the statement's closing balance on every day.

## Key Terms

| Term | What people say | What it actually means |
|------|-----------------|------------------------|
| Balance continuity | "the statement balances" | Opening plus the sum of booked lines equals closing, and each closing equals the next opening; the proof that nothing is missing |
| Booking date | "the date" | When the bank posted the entry to the account |
| Value date | "also the date" | When the entry takes effect for interest and availability; may differ from the booking date by days |
| Customer reference | "our reference" | The identifier the payer or payee supplied (EndToEndId, :61: reference, REFNUM); what reconciliation matches on |
| Bank reference | "their reference" | The bank's id for the booking (AcctSvcrRef, //ref, FITID); what you quote to the bank and de-duplicate on |
| Reversal | "a refund line" | An entry that cancels an earlier one; MT940 marks it RC/RD, camt.053 with RvslInd, and its effect is opposite to its stated direction |
| Control total | "the checksum" | BAI2's sum of every amount at a level, used to detect a dropped or altered record; not a balance |
| Ledger balance | "the balance" | Booked balance; distinct from available balance, which subtracts holds and adds credit lines |

## Further Reading

- [SWIFT Standards MT November 2024, Category 9 Cash Management (MT940/MT942/MT950)](https://www2.swift.com/knowledgecentre/products/Standards%20MT) — the field definitions, including the full `:61:` subfield grammar; requires a free SWIFT account
- [ISO 20022 camt.053 Bank to Customer Statement](https://www.iso20022.org/iso-20022-message-definitions) — the message definition and XSD, with balance type and entry status code lists
- [Common Global Implementation Market Practice (CGI-MP) camt.053 guidelines](https://www.swift.com/standards/market-practice/common-global-implementation) — how banks across markets actually populate camt.053, including batch entries and references
- [ANSI X9.121 Balance and Transaction Reporting Standard (successor to BAI2)](https://x9.org/) — the current US standard for BAI2-style reporting, with type code tables
- [OFX Specification 2.x and 1.0.2](https://financialdataexchange.org/FDX/About/OFX-Work-Group.aspx) — the full OFX specifications, now maintained by the Financial Data Exchange
- [EBICS specification](https://www.ebics.org/en/technical-information/ebics-specification) — how European corporates download statements and upload payment files over a signed, encrypted channel
