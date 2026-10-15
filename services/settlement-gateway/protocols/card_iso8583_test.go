package protocols

import (
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/shopspring/decimal"
)

func TestISO8583_PackAndUnpackRoundTrip(t *testing.T) {
	macKey := make([]byte, 32)
	rand.Read(macKey)

	engine := NewCommercialCardGatewayEngine(
		"DATAVENDOR-001",
		"TERM-801",
		"DataVendor Inc / San Francisco CA",
		"7372", // Computer programming, data processing services
		macKey,
	)

	amount, _ := decimal.NewFromString("18750.50")
	level3 := &Level3PurchaseRecord{
		TaxAmount:       decimal.NewFromFloat(1500.04),
		TaxRate:         decimal.NewFromFloat(0.08),
		CustomerRef:     "PO-CORP-AI-2026-99",
		DatasetSKU:      "DS-FIN-LLM-CORPUS-T3",
		VolumeGigabytes: decimal.NewFromFloat(25000.0),
		UnitPricePerGB:  decimal.NewFromFloat(0.075),
		CommodityCode:   "81112005",
	}

	msg, err := engine.BuildAuthorizationRequest(
		"411111******1111",
		amount,
		840, // USD
		123456,
		"987654321012",
		level3,
	)
	if err != nil {
		t.Fatalf("failed building authorization request: %v", err)
	}

	// Pack to wire format
	packed, err := msg.Pack()
	if err != nil {
		t.Fatalf("failed packing ISO 8583 message: %v", err)
	}

	if len(packed) == 0 {
		t.Fatalf("packed payload is empty")
	}

	// Unpack from wire format
	unpacked, err := UnpackISO8583(packed)
	if err != nil {
		t.Fatalf("failed unpacking ISO 8583 message: %v", err)
	}

	// Assertions
	if unpacked.MTI != MTIAuthorizationRequest {
		t.Errorf("MTI mismatch: expected %s, got %s", MTIAuthorizationRequest, unpacked.MTI)
	}

	pan, ok := unpacked.GetField(2)
	if !ok || pan != "411111******1111" {
		t.Errorf("Field 2 (PAN) mismatch: got %q", pan)
	}

	procCode, ok := unpacked.GetField(3)
	if !ok || procCode != "000000" {
		t.Errorf("Field 3 (Processing Code) mismatch: got %q", procCode)
	}

	amtStr, ok := unpacked.GetField(4)
	if !ok || amtStr != "000001875050" {
		t.Errorf("Field 4 (Amount) mismatch: expected '000001875050', got %q", amtStr)
	}

	stan, ok := unpacked.GetField(11)
	if !ok || stan != "123456" {
		t.Errorf("Field 11 (STAN) mismatch: got %q", stan)
	}

	l3Data, ok := unpacked.GetField(62)
	if !ok || len(l3Data) == 0 {
		t.Errorf("Field 62 (Level 3 Data) missing or empty")
	}

	// Verify MAC integrity
	validMAC, err := engine.VerifyMAC(unpacked)
	if err != nil {
		t.Fatalf("MAC verification errored: %v", err)
	}
	if !validMAC {
		t.Errorf("MAC verification failed on unpacked message!")
	}
}

func TestISO8583_MACTamperDetection(t *testing.T) {
	macKey := make([]byte, 32)
	rand.Read(macKey)

	engine := NewCommercialCardGatewayEngine(
		"DATAVENDOR-001",
		"TERM-801",
		"DataVendor Inc / San Francisco CA",
		"7372",
		macKey,
	)

	amount, _ := decimal.NewFromString("5000.00")
	msg, err := engine.BuildAuthorizationRequest(
		"411111******1111",
		amount,
		840,
		998877,
		"887766554433",
		nil,
	)
	if err != nil {
		t.Fatalf("failed building request: %v", err)
	}

	packed, err := msg.Pack()
	if err != nil {
		t.Fatalf("failed packing: %v", err)
	}

	// Tamper single bit in amount field
	tampered := make([]byte, len(packed))
	copy(tampered, packed)
	tampered[len(tampered)-10] ^= 0x01

	unpackedTampered, err := UnpackISO8583(tampered)
	if err == nil {
		valid, _ := engine.VerifyMAC(unpackedTampered)
		if valid {
			t.Errorf("Security failure: Tampered ISO 8583 payload passed MAC verification!")
		}
	}
}

func TestISO8583_EMVTLVParser(t *testing.T) {
	// Sample EMV chip tags:
	// 9F26 (Application Cryptogram - 8 bytes): 1122334455667788
	// 9F27 (Cryptogram Information Data - 1 byte): 80 (ARQC)
	// 95 (Terminal Verification Results - 5 bytes): 0000000000
	// 82 (Application Interchange Profile - 2 bytes): 1800
	tag1Val, _ := hex.DecodeString("1122334455667788")
	tag2Val, _ := hex.DecodeString("80")
	tag3Val, _ := hex.DecodeString("0000000000")
	tag4Val, _ := hex.DecodeString("1800")

	originalTags := []EMVTLVTag{
		{Tag: "9F26", Value: tag1Val},
		{Tag: "9F27", Value: tag2Val},
		{Tag: "95", Value: tag3Val},
		{Tag: "82", Value: tag4Val},
	}

	encoded, err := EncodeEMVTags(originalTags)
	if err != nil {
		t.Fatalf("failed encoding EMV tags: %v", err)
	}

	parsedTags, err := ParseEMVTags(encoded)
	if err != nil {
		t.Fatalf("failed parsing EMV tags: %v", err)
	}

	if len(parsedTags) != 4 {
		t.Fatalf("expected 4 tags, parsed %d", len(parsedTags))
	}

	if parsedTags[0].Tag != "9F26" || hex.EncodeToString(parsedTags[0].Value) != "1122334455667788" {
		t.Errorf("tag 9F26 mismatch")
	}
	if parsedTags[1].Tag != "9F27" || hex.EncodeToString(parsedTags[1].Value) != "80" {
		t.Errorf("tag 9F27 mismatch")
	}
	if parsedTags[2].Tag != "95" || hex.EncodeToString(parsedTags[2].Value) != "0000000000" {
		t.Errorf("tag 95 mismatch")
	}
}
