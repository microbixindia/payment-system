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
	ErrFedNowLimitExceeded = errors.New("settlement/fednow: transfer exceeds maximum allowable transaction ceiling ($500,000.00)")
	ErrInvalidRoutingNumber = errors.New("settlement/fednow: invalid 9-digit ABA routing transit number")
)

// FedNowMaxTransactionCeiling is standard $500k FedNow network cap.
var FedNowMaxTransactionCeiling = decimal.NewFromInt(500000)

// FedNowClearingEngine generates real-time settlement instructions for the Federal Reserve FedNow Service.
type FedNowClearingEngine struct {
	participantRoutingNumber string
}

// NewFedNowClearingEngine initializes the FedNow engine.
func NewFedNowClearingEngine(participantRouting string) *FedNowClearingEngine {
	return &FedNowClearingEngine{participantRoutingNumber: participantRouting}
}

// GenerateInstantPaymentInstruction builds an ISO 20022 compliant FedNow pacs.008 real-time message.
func (f *FedNowClearingEngine) GenerateInstantPaymentInstruction(
	endToEndID string,
	amount decimal.Decimal,
	debtorName, debtorAccount, debtorABA string,
	creditorName, creditorAccount, creditorABA string,
	memo string,
) (*Pacs008Document, error) {
	if amount.GreaterThan(FedNowMaxTransactionCeiling) {
		return nil, fmt.Errorf("%w: requested amount %s", ErrFedNowLimitExceeded, amount.StringFixed(2))
	}
	if len(debtorABA) != 9 || len(creditorABA) != 9 {
		return nil, ErrInvalidRoutingNumber
	}

	msgID := fmt.Sprintf("FEDNOW-%s-%s", time.Now().Format("20060102150405"), uuid.New().String()[:8])
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
					ClrSys:   &ClearingSystem{Prtry: "USFND"}, // US FedNow Service Identification
				},
			},
			CdtTrfTxInf: []CreditTransferTxInf{
				{
					PmtId: PaymentIdentification{
						EndToEndId: endToEndID,
						UETR:       uuid.New().String(),
					},
					IntrBkSttlmAmt: ActiveCurrencyAndAmount{
						Ccy:   "USD",
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
						FinInstnId: FinancialInstitutionIdentification{
							Nm:    fmt.Sprintf("FED-ABA-%s", debtorABA),
							BICFI: "USFEDNY01",
						},
					},
					CdtrAgt: FinancialInstitution{
						FinInstnId: FinancialInstitutionIdentification{
							Nm:    fmt.Sprintf("FED-ABA-%s", creditorABA),
							BICFI: "USFEDNY01",
						},
					},
					Cdtr: PartyIdentification{
						Nm: creditorName,
					},
					CdtrAcct: CashAccount{
						Id: AccountIdentification{
							Othr: &GenericAccountIdentification{Id: creditorAccount},
						},
					},
					RmtInf: &RemittanceInformation{
						Ustrd: memo,
					},
				},
			},
		},
	}

	return doc, nil
}

// SerializeXML marshals the FedNow message to compliant XML.
func (f *FedNowClearingEngine) SerializeXML(doc *Pacs008Document) ([]byte, error) {
	xmlData, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("settlement/fednow: failed to marshal XML: %w", err)
	}
	return append([]byte(xml.Header), xmlData...), nil
}
