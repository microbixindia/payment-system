package protocols

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestSwiftProtocolEngine_Pacs008Generation(t *testing.T) {
	engine := NewSwiftProtocolEngine()

	amount, _ := decimal.NewFromString("850000.50")
	doc, err := engine.BuildPacs008(
		"MSG-20261015-001",
		"E2E-DATASET-883",
		amount,
		"USD",
		"DataVendor Global Inc", "CHASUS33XXX", "9988776655",
		"AI Synthetics Corp", "BNPAFRPPXXX", "FR7630006000011234567890189",
		"Dataset Subscription Settlement October 2026",
	)
	if err != nil {
		t.Fatalf("failed to build pacs.008: %v", err)
	}

	xmlBytes, err := engine.SerializePacs008(doc)
	if err != nil {
		t.Fatalf("failed to serialize XML: %v", err)
	}

	xmlStr := string(xmlBytes)

	// Validate standard ISO 20022 namespace
	if !strings.Contains(xmlStr, "urn:iso:std:iso:20022:tech:xsd:pacs.008.001.10") {
		t.Errorf("missing pacs.008.001.10 XML namespace")
	}

	// Validate key tags
	if !strings.Contains(xmlStr, "<FIToFICstmrCdtTrf>") {
		t.Errorf("missing FIToFICstmrCdtTrf tag")
	}
	if !strings.Contains(xmlStr, "<IntrBkSttlmAmt Ccy=\"USD\">850000.50</IntrBkSttlmAmt>") {
		t.Errorf("missing formatted settlement amount tag")
	}
	if !strings.Contains(xmlStr, "<BICFI>CHASUS33XXX</BICFI>") {
		t.Errorf("missing debtor agent BIC")
	}
	if !strings.Contains(xmlStr, "<BICFI>BNPAFRPPXXX</BICFI>") {
		t.Errorf("missing creditor agent BIC")
	}

	// Validate deserialization round-trip
	deserializedDoc, err := engine.DeserializePacs008(xmlBytes)
	if err != nil {
		t.Fatalf("failed to deserialize generated XML: %v", err)
	}

	if deserializedDoc.CstmrCdtTrf.GrpHdr.MsgId != "MSG-20261015-001" {
		t.Errorf("deserialized MsgId mismatch: got %s", deserializedDoc.CstmrCdtTrf.GrpHdr.MsgId)
	}
}

func TestSwiftProtocolEngine_Pacs009Generation(t *testing.T) {
	engine := NewSwiftProtocolEngine()

	amount, _ := decimal.NewFromString("5000000.00") // $5M interbank liquidity
	doc, err := engine.BuildPacs009(
		"MSG-INTERBANK-009",
		"E2E-LIQUIDITY-99",
		amount,
		"USD",
		"CHASUS33XXX",
		"CITIUS33XXX",
	)
	if err != nil {
		t.Fatalf("failed to build pacs.009: %v", err)
	}

	if doc.Xmlns != "urn:iso:std:iso:20022:tech:xsd:pacs.009.001.10" {
		t.Errorf("invalid pacs.009 XML namespace: %s", doc.Xmlns)
	}
}
