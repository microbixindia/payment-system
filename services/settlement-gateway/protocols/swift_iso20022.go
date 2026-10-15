package protocols

import (
	"encoding/xml"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

var (
	ErrInvalidSWIFTMessage = errors.New("settlement/swift: invalid ISO 20022 message structure")
	ErrInvalidBIC          = errors.New("settlement/swift: BIC must be 8 or 11 alphanumeric characters")
	ErrInvalidIBAN         = errors.New("settlement/swift: invalid IBAN structure")
)

// ============================================================================
// 1. PACS.008.001.10: Financial Institutional Customer Credit Transfer
// ============================================================================

type Pacs008Document struct {
	XMLName        xml.Name       `xml:"Document"`
	Xmlns          string         `xml:"xmlns,attr"`
	CstmrCdtTrf    Pacs008Message `xml:"FIToFICstmrCdtTrf"`
}

type Pacs008Message struct {
	GrpHdr      GroupHeader008       `xml:"GrpHdr"`
	CdtTrfTxInf []CreditTransferTxInf `xml:"CdtTrfTxInf"`
}

type GroupHeader008 struct {
	MsgId    string        `xml:"MsgId"`
	CreDtTm  string        `xml:"CreDtTm"`
	NbOfTxs  int           `xml:"NbOfTxs"`
	SttlmInf SettlementInfo `xml:"SttlmInf"`
}

type SettlementInfo struct {
	SttlmMtd string         `xml:"SttlmMtd"` // e.g. "CLRG" or "INDA"
	ClrSys   *ClearingSystem `xml:"ClrSys,omitempty"`
}

type ClearingSystem struct {
	Prtry string `xml:"Prtry"` // e.g. "SWIFT", "FEDNOW", "TARGET2"
}

type CreditTransferTxInf struct {
	PmtId          PaymentIdentification `xml:"PmtId"`
	IntrBkSttlmAmt ActiveCurrencyAndAmount `xml:"IntrBkSttlmAmt"`
	IntrBkSttlmDt  string                 `xml:"IntrBkSttlmDt"` // YYYY-MM-DD
	Dbtr           PartyIdentification    `xml:"Dbtr"`
	DbtrAcct       CashAccount            `xml:"DbtrAcct"`
	DbtrAgt        FinancialInstitution   `xml:"DbtrAgt"`
	CdtrAgt        FinancialInstitution   `xml:"CdtrAgt"`
	Cdtr           PartyIdentification    `xml:"Cdtr"`
	CdtrAcct       CashAccount            `xml:"CdtrAcct"`
	RmtInf         *RemittanceInformation `xml:"RmtInf,omitempty"`
}

type PaymentIdentification struct {
	EndToEndId string `xml:"EndToEndId"`
	UETR       string `xml:"UETR"` // 128-bit RFC 4122 UUID v4
}

type ActiveCurrencyAndAmount struct {
	Ccy   string `xml:"Ccy,attr"`
	Value string `xml:",chardata"`
}

type PartyIdentification struct {
	Nm       string   `xml:"Nm"`
	PstlAdr  *Address `xml:"PstlAdr,omitempty"`
}

type Address struct {
	StrtNm  string `xml:"StrtNm,omitempty"`
	BldgNb  string `xml:"BldgNb,omitempty"`
	PstCd   string `xml:"PstCd,omitempty"`
	TwnNm   string `xml:"TwnNm,omitempty"`
	Ctry    string `xml:"Ctry,omitempty"`
}

type CashAccount struct {
	Id AccountIdentification `xml:"Id"`
}

type AccountIdentification struct {
	IBAN  string `xml:"IBAN,omitempty"`
	Othr  *GenericAccountIdentification `xml:"Othr,omitempty"`
}

type GenericAccountIdentification struct {
	Id string `xml:"Id"`
}

type FinancialInstitution struct {
	FinInstnId FinancialInstitutionIdentification `xml:"FinInstnId"`
}

type FinancialInstitutionIdentification struct {
	BICFI string `xml:"BICFI,omitempty"`
	Nm    string `xml:"Nm,omitempty"`
}

type RemittanceInformation struct {
	Ustrd string `xml:"Ustrd"`
}

// ============================================================================
// 2. PACS.009.001.10: Financial Institution Credit Transfer (Interbank)
// ============================================================================

type Pacs009Document struct {
	XMLName   xml.Name       `xml:"Document"`
	Xmlns     string         `xml:"xmlns,attr"`
	FICdtTrf  Pacs009Message `xml:"FICdtTrf"`
}

type Pacs009Message struct {
	GrpHdr      GroupHeader008            `xml:"GrpHdr"`
	CdtTrfTxInf []InterbankCreditTransfer `xml:"CdtTrfTxInf"`
}

type InterbankCreditTransfer struct {
	PmtId          PaymentIdentification    `xml:"PmtId"`
	IntrBkSttlmAmt ActiveCurrencyAndAmount   `xml:"IntrBkSttlmAmt"`
	IntrBkSttlmDt  string                    `xml:"IntrBkSttlmDt"`
	Dbtr           FinancialInstitution      `xml:"Dbtr"`
	DbtrAgt        FinancialInstitution      `xml:"DbtrAgt"`
	CdtrAgt        FinancialInstitution      `xml:"CdtrAgt"`
	Cdtr           FinancialInstitution      `xml:"Cdtr"`
}

// ============================================================================
// 3. CAMT.053.001.10: Bank-to-Customer Statement (Reconciliation)
// ============================================================================

type Camt053Document struct {
	XMLName        xml.Name       `xml:"Document"`
	Xmlns          string         `xml:"xmlns,attr"`
	BkToCstmrStmt  Camt053Message `xml:"BkToCstmrStmt"`
}

type Camt053Message struct {
	GrpHdr GroupHeader053 `xml:"GrpHdr"`
	Stmt   []Statement053 `xml:"Stmt"`
}

type GroupHeader053 struct {
	MsgId   string `xml:"MsgId"`
	CreDtTm string `xml:"CreDtTm"`
}

type Statement053 struct {
	Id      string         `xml:"Id"`
	StmtPgnt *Pagination   `xml:"StmtPgnt,omitempty"`
	Acct    CashAccount    `xml:"Acct"`
	Bal     []BalanceRecord `xml:"Bal"`
	Ntry    []StatementEntry `xml:"Ntry,omitempty"`
}

type Pagination struct {
	PgNb      string `xml:"PgNb"`
	LastPgInd bool   `xml:"LastPgInd"`
}

type BalanceRecord struct {
	Tp    BalanceType             `xml:"Tp"`
	Amt   ActiveCurrencyAndAmount `xml:"Amt"`
	CdtDbtInd string              `xml:"CdtDbtInd"` // "CRDT" or "DBIT"
	Dt    DateRecord              `xml:"Dt"`
}

type BalanceType struct {
	CdOrPrtry CodeOrProprietary `xml:"CdOrPrtry"`
}

type CodeOrProprietary struct {
	Cd string `xml:"Cd"` // "OPBD" (Opening), "CLBD" (Closing), "PRCD"
}

type DateRecord struct {
	Dt string `xml:"Dt"` // YYYY-MM-DD
}

type StatementEntry struct {
	Amt       ActiveCurrencyAndAmount `xml:"Amt"`
	CdtDbtInd string                  `xml:"CdtDbtInd"`
	Sts       string                  `xml:"Sts"` // "BOOK", "PDNG"
	BookgDt   DateRecord              `xml:"BookgDt"`
	ValDt     DateRecord              `xml:"ValDt"`
	AcctSvcrRef string                `xml:"AcctSvcrRef,omitempty"`
}

// ============================================================================
// ISO 20022 BUILDER & SERIALIZER ENGINE
// ============================================================================

type SwiftProtocolEngine struct{}

func NewSwiftProtocolEngine() *SwiftProtocolEngine {
	return &SwiftProtocolEngine{}
}

// BuildPacs008 creates a fully compliant pacs.008 customer credit transfer.
func (s *SwiftProtocolEngine) BuildPacs008(
	msgID string,
	endToEndID string,
	amount decimal.Decimal,
	currency string,
	debtorName, debtorBIC, debtorAccount string,
	creditorName, creditorBIC, creditorAccount string,
	remittanceInfo string,
) (*Pacs008Document, error) {
	if len(debtorBIC) != 8 && len(debtorBIC) != 11 {
		return nil, fmt.Errorf("%w: debtor BIC %s", ErrInvalidBIC, debtorBIC)
	}
	if len(creditorBIC) != 8 && len(creditorBIC) != 11 {
		return nil, fmt.Errorf("%w: creditor BIC %s", ErrInvalidBIC, creditorBIC)
	}

	uetr := uuid.New().String()
	now := time.Now().UTC()

	doc := &Pacs008Document{
		Xmlns: "urn:iso:std:iso:20022:tech:xsd:pacs.008.001.10",
		CstmrCdtTrf: Pacs008Message{
			GrpHdr: GroupHeader008{
				MsgId:   msgID,
				CreDtTm: now.Format(time.RFC3339),
				NbOfTxs: 1,
				SttlmInf: SettlementInfo{
					SttlmMtd: "CLRG",
					ClrSys:   &ClearingSystem{Prtry: "SWIFT"},
				},
			},
			CdtTrfTxInf: []CreditTransferTxInf{
				{
					PmtId: PaymentIdentification{
						EndToEndId: endToEndID,
						UETR:       uetr,
					},
					IntrBkSttlmAmt: ActiveCurrencyAndAmount{
						Ccy:   currency,
						Value: amount.StringFixed(2),
					},
					IntrBkSttlmDt: now.Format("2006-01-02"),
					Dbtr: PartyIdentification{
						Nm: debtorName,
					},
					DbtrAcct: CashAccount{
						Id: AccountIdentification{
							Othr: &GenericAccountIdentification{Id: debtorAccount},
						},
					},
					DbtrAgt: FinancialInstitution{
						FinInstnId: FinancialInstitutionIdentification{BICFI: debtorBIC},
					},
					CdtrAgt: FinancialInstitution{
						FinInstnId: FinancialInstitutionIdentification{BICFI: creditorBIC},
					},
					Cdtr: PartyIdentification{
						Nm: creditorName,
					},
					CdtrAcct: CashAccount{
						Id: AccountIdentification{
							IBAN: creditorAccount,
						},
					},
					RmtInf: &RemittanceInformation{
						Ustrd: remittanceInfo,
					},
				},
			},
		},
	}

	return doc, nil
}

// BuildPacs009 creates a compliant pacs.009 interbank credit transfer.
func (s *SwiftProtocolEngine) BuildPacs009(
	msgID, endToEndID string,
	amount decimal.Decimal,
	currency string,
	debtorBIC, creditorBIC string,
) (*Pacs009Document, error) {
	now := time.Now().UTC()

	return &Pacs009Document{
		Xmlns: "urn:iso:std:iso:20022:tech:xsd:pacs.009.001.10",
		FICdtTrf: Pacs009Message{
			GrpHdr: GroupHeader008{
				MsgId:   msgID,
				CreDtTm: now.Format(time.RFC3339),
				NbOfTxs: 1,
				SttlmInf: SettlementInfo{
					SttlmMtd: "INDA",
				},
			},
			CdtTrfTxInf: []InterbankCreditTransfer{
				{
					PmtId: PaymentIdentification{
						EndToEndId: endToEndID,
						UETR:       uuid.New().String(),
					},
					IntrBkSttlmAmt: ActiveCurrencyAndAmount{
						Ccy:   currency,
						Value: amount.StringFixed(2),
					},
					IntrBkSttlmDt: now.Format("2006-01-02"),
					Dbtr: FinancialInstitution{
						FinInstnId: FinancialInstitutionIdentification{BICFI: debtorBIC},
					},
					DbtrAgt: FinancialInstitution{
						FinInstnId: FinancialInstitutionIdentification{BICFI: debtorBIC},
					},
					CdtrAgt: FinancialInstitution{
						FinInstnId: FinancialInstitutionIdentification{BICFI: creditorBIC},
					},
					Cdtr: FinancialInstitution{
						FinInstnId: FinancialInstitutionIdentification{BICFI: creditorBIC},
					},
				},
			},
		},
	}, nil
}

// SerializePacs008 outputs XML bytes with standard header.
func (s *SwiftProtocolEngine) SerializePacs008(doc *Pacs008Document) ([]byte, error) {
	xmlData, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("settlement/swift: failed to marshal pacs.008 XML: %w", err)
	}
	header := []byte(xml.Header)
	return append(header, xmlData...), nil
}

// DeserializePacs008 parses raw XML into a Pacs008Document.
func (s *SwiftProtocolEngine) DeserializePacs008(data []byte) (*Pacs008Document, error) {
	var doc Pacs008Document
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("settlement/swift: failed to unmarshal pacs.008 XML: %w", err)
	}
	return &doc, nil
}
