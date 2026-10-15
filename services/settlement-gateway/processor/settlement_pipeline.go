package processor

import (
	"context"
	"errors"
	"fmt"

	"datavendor-payment-core/services/settlement-gateway/protocols"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

var (
	ErrUnsupportedRail = errors.New("settlement/pipeline: no viable settlement rail for target transaction parameters")
)

// SettlementRail designates the financial clearing network.
type SettlementRail string

const (
	RailFedNow SettlementRail = "FEDNOW_INSTANT"
	RailSWIFT  SettlementRail = "SWIFT_ISO20022"
	RailACH    SettlementRail = "ACH_NACHA"
)

// SettlementInstruction encapsulates routing instructions for a B2B payment.
type SettlementInstruction struct {
	ID             uuid.UUID
	Amount         decimal.Decimal
	Currency       string
	Urgency        string // "INSTANT", "SAME_DAY", "STANDARD"
	DebtorName     string
	DebtorAccount  string
	DebtorRouting  string // ABA or BIC
	CreditorName   string
	CreditorAccount string
	CreditorRouting string // ABA or BIC
	Memo           string
}

// SettlementExecutionResult contains generated financial message payload and chosen rail.
type SettlementExecutionResult struct {
	InstructionID uuid.UUID
	SelectedRail  SettlementRail
	PayloadFormat string // "XML" or "NACHA_ASCII"
	MessageBytes  []byte
}

// SettlementPipeline routes transactions to the optimal bank clearing rails.
type SettlementPipeline struct {
	swiftEngine  *protocols.SwiftProtocolEngine
	fedNowEngine *protocols.FedNowClearingEngine
	nachaBuilder *protocols.NACHAFileBuilder
}

// NewSettlementPipeline instantiates the settlement pipeline.
func NewSettlementPipeline() *SettlementPipeline {
	return &SettlementPipeline{
		swiftEngine:  protocols.NewSwiftProtocolEngine(),
		fedNowEngine: protocols.NewFedNowClearingEngine("121000358"),
		nachaBuilder: protocols.NewNACHAFileBuilder("121000358", "021000021", "WELLS FARGO", "DATAVENDOR CORP"),
	}
}

// ExecuteClearing selects the optimal banking network and constructs the message payload.
func (p *SettlementPipeline) ExecuteClearing(ctx context.Context, inst SettlementInstruction) (*SettlementExecutionResult, error) {
	// 1. Check FedNow Instant Eligibility: USD, INSTANT urgency, and <= $500,000 cap
	if inst.Currency == "USD" && inst.Urgency == "INSTANT" && inst.Amount.LessThanOrEqual(protocols.FedNowMaxTransactionCeiling) {
		doc, err := p.fedNowEngine.GenerateInstantPaymentInstruction(
			inst.ID.String(),
			inst.Amount,
			inst.DebtorName, inst.DebtorAccount, inst.DebtorRouting,
			inst.CreditorName, inst.CreditorAccount, inst.CreditorRouting,
			inst.Memo,
		)
		if err == nil {
			xmlBytes, err := p.fedNowEngine.SerializeXML(doc)
			if err != nil {
				return nil, err
			}
			return &SettlementExecutionResult{
				InstructionID: inst.ID,
				SelectedRail:  RailFedNow,
				PayloadFormat: "XML",
				MessageBytes:  xmlBytes,
			}, nil
		}
		// Fallthrough to standard rails if FedNow routing validation fails
	}

	// 2. Check Standard ACH Eligibility: USD and domestic US ABA routing numbers
	if inst.Currency == "USD" && inst.Urgency == "STANDARD" && len(inst.CreditorRouting) == 9 {
		detailRec, err := p.nachaBuilder.BuildEntryDetailRecord(
			"22", // Checking Credit
			inst.CreditorRouting,
			inst.CreditorAccount,
			inst.Amount,
			inst.ID.String()[:15],
			inst.CreditorName,
			1,
		)
		if err == nil {
			return &SettlementExecutionResult{
				InstructionID: inst.ID,
				SelectedRail:  RailACH,
				PayloadFormat: "NACHA_ASCII",
				MessageBytes:  []byte(detailRec),
			}, nil
		}
	}

	// 3. Global / High-Value / Cross-Border: SWIFT ISO 20022 pacs.008
	doc, err := p.swiftEngine.BuildPacs008(
		fmt.Sprintf("MSG-SWIFT-%s", inst.ID.String()[:8]),
		inst.ID.String(),
		inst.Amount,
		inst.Currency,
		inst.DebtorName, inst.DebtorRouting, inst.DebtorAccount,
		inst.CreditorName, inst.CreditorRouting, inst.CreditorAccount,
		inst.Memo,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to generate pacs.008 fallback: %v", ErrUnsupportedRail, err)
	}

	xmlBytes, err := p.swiftEngine.SerializePacs008(doc)
	if err != nil {
		return nil, err
	}

	return &SettlementExecutionResult{
		InstructionID: inst.ID,
		SelectedRail:  RailSWIFT,
		PayloadFormat: "XML",
		MessageBytes:  xmlBytes,
	}, nil
}
