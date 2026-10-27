// Samples: one business day on one account (opening 12,500.00; -1,500.00; +3,250.75; -12.50; closing 14,238.25)
// in four formats. Fictional data; the same text as the Java `Samples` class.

package main

const SampleMT940 = `{1:F01COBADEFFAXXX0000000000}{2:O9402200261012COBADEFFAXXX00000000002610122200N}{4:
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
`

// SampleMT940Day2 is a fmt.Sprintf template: opening and closing balances are the two %s.
const SampleMT940Day2 = `{1:F01COBADEFFAXXX0000000000}{2:O9402200261013COBADEFFAXXX00000000002610132200N}{4:
:20:STMT261013
:25:DE89370400440532013000
:28C:00288/001
:60F:C261012EUR%s
:61:2610131013C100,00NTRFORDER-78//COBA0004
:86:Customer receipt ORDER-78
:62F:C261013EUR%s
-}
`

const SampleCAMT053 = `<?xml version="1.0" encoding="UTF-8"?>
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
`

const SampleBAI2 = `01,021000021,ACMEUS,261012,2200,1,80,1,2/
02,ACMEUS,021000021,1,261012,2200,USD,2/
03,0001234567,USD,010,1250000,,,015,1423825,,/
16,495,150000,Z,COBA0001,INV-4411,Supplier payment INV-4411
88,Fournitures Lyon SARL
16,195,325075,Z,COBA0002,ORDER-77,Customer receipt ORDER-77 Delft Sensors BV
16,698,1250,Z,COBA0003,,Account maintenance fee
49,3150150,6/
98,3150150,1,8/
99,3150150,1,10/
`

const SampleOFX = `OFXHEADER:100
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
`
