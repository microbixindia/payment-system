package protocols

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestFedNowClearingEngine(t *testing.T) {
	fedEngine := NewFedNowClearingEngine("121000358")

	validAmount, _ := decimal.NewFromString("125000.00") // $125,000 within $500k limit

	doc, err := fedEngine.GenerateInstantPaymentInstruction(
		"E2E-FEDNOW-001",
		validAmount,
		"DataVendor Systems", "100200300", "121000358",
		"Recipient Enterprise", "900800700", "021000021",
		"Instant Settlement Batch",
	)
	if err != nil {
		t.Fatalf("failed to generate FedNow payment: %v", err)
	}

	xmlBytes, err := fedEngine.SerializeXML(doc)
	if err != nil {
		t.Fatalf("failed to serialize FedNow XML: %v", err)
	}

	xmlStr := string(xmlBytes)

	// Verify FedNow USFND clearing code is present
	if !strings.Contains(xmlStr, "<Prtry>USFND</Prtry>") {
		t.Errorf("missing USFND clearing system identifier")
	}

	// Test exceeding $500,000 ceiling
	excessAmount, _ := decimal.NewFromString("500000.01")
	_, err = fedEngine.GenerateInstantPaymentInstruction(
		"E2E-FEDNOW-ERR",
		excessAmount,
		"DataVendor Systems", "100200300", "121000358",
		"Recipient Enterprise", "900800700", "021000021",
		"Over-limit settlement",
	)
	if err == nil {
		t.Fatalf("expected error when amount exceeds $500k FedNow ceiling, but it succeeded")
	}
}
