package protocols

import (
	"errors"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// ============================================================================
// 1. PACS.008.001.08 XML GENERATION & DESERIALIZATION ROUND-TRIP TESTS
// ============================================================================

func TestSEPAInstant_Pacs008GenerationAndRoundTrip(t *testing.T) {
	engine := NewSEPAInstantEngine()

	amount, err := decimal.NewFromString("42500.75")
	if err != nil {
		t.Fatalf("failed to parse amount: %v", err)
	}

	debtorName := "DataVendor Tech Europe GmbH"
	debtorIBAN := "DE89 3704 0044 0532 0130 00" // Formatted with spaces to test sanitization
	debtorBIC := "DBEUMM21XXX"

	creditorName := "Acme Synthetics France SAS"
	creditorIBAN := "FR14 2004 1010 0505 0001 3M02 606"
	creditorBIC := "BNPAFRPPXXX"

	e2eID := "E2E-SEPA-INST-20261015-001"
	remittance := "Invoice INV-2026-EU-9948 B2B Settlement"

	doc, err := engine.GenerateInstantPaymentInstruction(
		e2eID,
		amount,
		"EUR",
		debtorName, debtorIBAN, debtorBIC,
		creditorName, creditorIBAN, creditorBIC,
		remittance,
	)
	if err != nil {
		t.Fatalf("failed to generate pacs.008.001.08: %v", err)
	}

	// Verify Document attributes
	if doc.Xmlns != SEPANamespace {
		t.Errorf("expected xmlns %q, got %q", SEPANamespace, doc.Xmlns)
	}

	// Verify Group Header
	grpHdr := doc.CstmrCdtTrf.GrpHdr
	if !strings.HasPrefix(grpHdr.MsgId, "SCT-") {
		t.Errorf("expected MsgId starting with 'SCT-', got %q", grpHdr.MsgId)
	}
	if grpHdr.NbOfTxs != 1 {
		t.Errorf("expected NbOfTxs = 1, got %d", grpHdr.NbOfTxs)
	}
	if grpHdr.SttlmInf.SttlmMtd != "CLRG" {
		t.Errorf("expected SttlmMtd = 'CLRG', got %q", grpHdr.SttlmInf.SttlmMtd)
	}
	if grpHdr.SttlmInf.ClrSys.Prtry != "RT1" && grpHdr.SttlmInf.ClrSys.Prtry != "TIPS" {
		t.Errorf("expected clearing system RT1 or TIPS, got %q", grpHdr.SttlmInf.ClrSys.Prtry)
	}

	// Verify Transaction Information
	if len(doc.CstmrCdtTrf.CdtTrfTxInf) != 1 {
		t.Fatalf("expected 1 transaction, got %d", len(doc.CstmrCdtTrf.CdtTrfTxInf))
	}
	tx := doc.CstmrCdtTrf.CdtTrfTxInf[0]

	if tx.PmtId.EndToEndId != e2eID {
		t.Errorf("expected EndToEndId %q, got %q", e2eID, tx.PmtId.EndToEndId)
	}
	if tx.PmtId.TxId == "" {
		t.Errorf("expected non-empty TxId")
	}
	if tx.PmtId.UETR == "" {
		t.Errorf("expected non-empty UETR")
	}

	// EPC Mandatory Codes: SEPA & INST
	if tx.PmtTpInf.SvcLvl.Cd != "SEPA" {
		t.Errorf("expected SvcLvl 'SEPA', got %q", tx.PmtTpInf.SvcLvl.Cd)
	}
	if tx.PmtTpInf.LclInstrm.Cd != "INST" {
		t.Errorf("expected LclInstrm 'INST', got %q", tx.PmtTpInf.LclInstrm.Cd)
	}
	if tx.ChrgBr != "SLEV" {
		t.Errorf("expected ChrgBr 'SLEV', got %q", tx.ChrgBr)
	}

	// Amount & Currency
	if tx.IntrBkSttlmAmt.Ccy != "EUR" {
		t.Errorf("expected currency EUR, got %q", tx.IntrBkSttlmAmt.Ccy)
	}
	if tx.IntrBkSttlmAmt.Value != "42500.75" {
		t.Errorf("expected amount '42500.75', got %q", tx.IntrBkSttlmAmt.Value)
	}

	// Accounts & BICs (sanitized)
	expectedCleanDebtorIBAN := "DE89370400440532013000"
	expectedCleanCreditorIBAN := "FR1420041010050500013M02606"
	if tx.DbtrAcct.Id.IBAN != expectedCleanDebtorIBAN {
		t.Errorf("debtor IBAN mismatch: got %q, expected %q", tx.DbtrAcct.Id.IBAN, expectedCleanDebtorIBAN)
	}
	if tx.CdtrAcct.Id.IBAN != expectedCleanCreditorIBAN {
		t.Errorf("creditor IBAN mismatch: got %q, expected %q", tx.CdtrAcct.Id.IBAN, expectedCleanCreditorIBAN)
	}
	if tx.DbtrAgt.FinInstnId.BICFI != debtorBIC {
		t.Errorf("debtor BIC mismatch: got %q, expected %q", tx.DbtrAgt.FinInstnId.BICFI, debtorBIC)
	}
	if tx.CdtrAgt.FinInstnId.BICFI != creditorBIC {
		t.Errorf("creditor BIC mismatch: got %q, expected %q", tx.CdtrAgt.FinInstnId.BICFI, creditorBIC)
	}

	// Remittance Information
	if tx.RmtInf == nil || tx.RmtInf.Ustrd != remittance {
		t.Errorf("remittance mismatch: got %+v, expected %q", tx.RmtInf, remittance)
	}

	// Serialize to XML
	xmlBytes, err := engine.SerializePacs008Inst(doc)
	if err != nil {
		t.Fatalf("failed to serialize pacs.008 XML: %v", err)
	}
	xmlStr := string(xmlBytes)

	// Validate XML structure & declaration header
	if !strings.HasPrefix(xmlStr, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>") {
		t.Errorf("missing or invalid XML declaration header")
	}
	if !strings.Contains(xmlStr, "urn:iso:std:iso:20022:tech:xsd:pacs.008.001.08") {
		t.Errorf("missing pacs.008.001.08 XML namespace")
	}
	if !strings.Contains(xmlStr, "<FIToFICstmrCdtTrf>") {
		t.Errorf("missing <FIToFICstmrCdtTrf> tag")
	}
	if !strings.Contains(xmlStr, "<Cd>SEPA</Cd>") {
		t.Errorf("missing <Cd>SEPA</Cd> service level")
	}
	if !strings.Contains(xmlStr, "<Cd>INST</Cd>") {
		t.Errorf("missing <Cd>INST</Cd> local instrument")
	}
	if !strings.Contains(xmlStr, "<ChrgBr>SLEV</ChrgBr>") {
		t.Errorf("missing <ChrgBr>SLEV</ChrgBr>")
	}
	if !strings.Contains(xmlStr, "<IntrBkSttlmAmt Ccy=\"EUR\">42500.75</IntrBkSttlmAmt>") {
		t.Errorf("missing formatted settlement amount tag")
	}

	// Deserialize back into struct (full round-trip fidelity test)
	roundTripDoc, err := engine.DeserializePacs008Inst(xmlBytes)
	if err != nil {
		t.Fatalf("failed to deserialize generated XML: %v", err)
	}

	if roundTripDoc.CstmrCdtTrf.GrpHdr.MsgId != grpHdr.MsgId {
		t.Errorf("deserialized MsgId mismatch: got %q, expected %q",
			roundTripDoc.CstmrCdtTrf.GrpHdr.MsgId, grpHdr.MsgId)
	}
	if len(roundTripDoc.CstmrCdtTrf.CdtTrfTxInf) != 1 {
		t.Fatalf("deserialized transactions length mismatch")
	}
	roundTripTx := roundTripDoc.CstmrCdtTrf.CdtTrfTxInf[0]
	if roundTripTx.PmtId.EndToEndId != e2eID {
		t.Errorf("deserialized EndToEndId mismatch: got %q, expected %q", roundTripTx.PmtId.EndToEndId, e2eID)
	}
	if roundTripTx.IntrBkSttlmAmt.Value != "42500.75" {
		t.Errorf("deserialized Amount mismatch: got %q, expected '42500.75'", roundTripTx.IntrBkSttlmAmt.Value)
	}
	if roundTripTx.IntrBkSttlmAmt.Ccy != "EUR" {
		t.Errorf("deserialized Currency mismatch: got %q, expected 'EUR'", roundTripTx.IntrBkSttlmAmt.Ccy)
	}
	if roundTripTx.DbtrAcct.Id.IBAN != expectedCleanDebtorIBAN {
		t.Errorf("deserialized Debtor IBAN mismatch")
	}
	if roundTripTx.CdtrAcct.Id.IBAN != expectedCleanCreditorIBAN {
		t.Errorf("deserialized Creditor IBAN mismatch")
	}
	if roundTripTx.RmtInf == nil || roundTripTx.RmtInf.Ustrd != remittance {
		t.Errorf("deserialized Remittance mismatch")
	}
}

// ============================================================================
// 2. CURRENCY VALIDATION TESTS (EUR ENFORCEMENT)
// ============================================================================

func TestSEPAInstant_CurrencyValidation(t *testing.T) {
	engine := NewSEPAInstantEngine()
	validAmount := decimal.NewFromInt(1000)

	validDebtorIBAN := "DE89370400440532013000"
	validCreditorIBAN := "NL91ABNA0417164300"
	validDebtorBIC := "DBEUMM21XXX"
	validCreditorBIC := "INGBNL2AXXX"

	tests := []struct {
		name        string
		currency    string
		expectError bool
	}{
		{"Valid EUR currency", "EUR", false},
		{"Valid lowercase eur normalized", "eur", false},
		{"Reject USD currency", "USD", true},
		{"Reject GBP currency", "GBP", true},
		{"Reject CHF currency", "CHF", true},
		{"Reject JPY currency", "JPY", true},
		{"Reject empty currency", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := engine.GenerateInstantPaymentInstruction(
				"E2E-TEST-CURRENCY",
				validAmount,
				tc.currency,
				"Debtor Corp", validDebtorIBAN, validDebtorBIC,
				"Creditor Corp", validCreditorIBAN, validCreditorBIC,
				"Currency test",
			)
			if tc.expectError {
				if err == nil {
					t.Errorf("expected error for currency %q, but got nil", tc.currency)
				} else if !errors.Is(err, ErrNonEURCurrency) {
					t.Errorf("expected ErrNonEURCurrency, got: %v", err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error for valid currency %q: %v", tc.currency, err)
				}
			}
		})
	}
}

// ============================================================================
// 3. REGULATORY TRANSACTION CEILING (€100,000.00) & AMOUNT ENFORCEMENT
// ============================================================================

func TestSEPAInstant_CeilingAndAmountLimits(t *testing.T) {
	engine := NewSEPAInstantEngine()

	validDebtorIBAN := "DE89370400440532013000"
	validCreditorIBAN := "NL91ABNA0417164300"
	validDebtorBIC := "DBEUMM21XXX"
	validCreditorBIC := "INGBNL2AXXX"

	tests := []struct {
		name          string
		amountStr     string
		expectError   bool
		expectedErrIs error
	}{
		{"Minimum valid transfer €0.01", "0.01", false, nil},
		{"Standard amount €1,500.00", "1500.00", false, nil},
		{"Large transfer €99,999.99", "99999.99", false, nil},
		{"Exact regulatory ceiling €100,000.00", "100000.00", false, nil},
		{"Over ceiling by 1 cent €100,000.01", "100000.01", true, ErrSEPALimitExceeded},
		{"Significantly over ceiling €250,000.00", "250000.00", true, ErrSEPALimitExceeded},
		{"Zero amount €0.00 rejected", "0.00", true, ErrInvalidSEPAAmount},
		{"Negative amount rejected", "-50.00", true, ErrInvalidSEPAAmount},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			amt, err := decimal.NewFromString(tc.amountStr)
			if err != nil {
				t.Fatalf("invalid test decimal: %v", err)
			}

			_, err = engine.GenerateInstantPaymentInstruction(
				"E2E-TEST-LIMITS",
				amt,
				"EUR",
				"Debtor Corp", validDebtorIBAN, validDebtorBIC,
				"Creditor Corp", validCreditorIBAN, validCreditorBIC,
				"Limit verification",
			)

			if tc.expectError {
				if err == nil {
					t.Fatalf("expected error for amount €%s, got success", tc.amountStr)
				}
				if tc.expectedErrIs != nil && !errors.Is(err, tc.expectedErrIs) {
					t.Errorf("expected error %v, got %v", tc.expectedErrIs, err)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error for valid amount €%s: %v", tc.amountStr, err)
				}
			}
		})
	}
}

// ============================================================================
// 4. RT1 & TIPS CLEARING ROUTING TESTS
// ============================================================================

func TestSEPAInstant_RT1AndTIPSRouting(t *testing.T) {
	router := NewDefaultSEPAClearingRouter(ClearingSystemRT1)
	engine := NewSEPAInstantEngine(router)

	amount := decimal.NewFromInt(25000)
	debtorIBAN := "DE89370400440532013000"
	debtorBIC := "DBEUMM21XXX"

	t.Run("Auto route to TIPS for Eurosystem Central Bank BIC", func(t *testing.T) {
		// MARKDEFF is Bundesbank, pre-configured as TIPS
		doc, err := engine.GenerateInstantPaymentInstruction(
			"E2E-ROUTE-001",
			amount, "EUR",
			"Debtor Corp", debtorIBAN, debtorBIC,
			"Bundesbank Clearing", "DE89370400440532013000", "MARKDEFFXXX",
			"Central Bank Settlement",
		)
		if err != nil {
			t.Fatalf("payment instruction failed: %v", err)
		}
		if doc.CstmrCdtTrf.GrpHdr.SttlmInf.ClrSys.Prtry != "TIPS" {
			t.Errorf("expected TIPS routing, got %q", doc.CstmrCdtTrf.GrpHdr.SttlmInf.ClrSys.Prtry)
		}
	})

	t.Run("Auto route to RT1 for European Commercial Bank BIC", func(t *testing.T) {
		// BNPAFRPP is BNP Paribas, pre-configured as RT1
		doc, err := engine.GenerateInstantPaymentInstruction(
			"E2E-ROUTE-002",
			amount, "EUR",
			"Debtor Corp", debtorIBAN, debtorBIC,
			"BNP Account", "FR1420041010050500013M02606", "BNPAFRPPXXX",
			"Commercial Settlement",
		)
		if err != nil {
			t.Fatalf("payment instruction failed: %v", err)
		}
		if doc.CstmrCdtTrf.GrpHdr.SttlmInf.ClrSys.Prtry != "RT1" {
			t.Errorf("expected RT1 routing, got %q", doc.CstmrCdtTrf.GrpHdr.SttlmInf.ClrSys.Prtry)
		}
	})

	t.Run("Explicit override to TIPS", func(t *testing.T) {
		// Overriding commercial bank to TIPS
		doc, err := engine.GenerateInstantPaymentInstruction(
			"E2E-ROUTE-OVERRIDE-TIPS",
			amount, "EUR",
			"Debtor Corp", debtorIBAN, debtorBIC,
			"Creditor Corp", "NL91ABNA0417164300", "INGBNL2AXXX",
			"Explicit TIPS override",
			ClearingSystemTIPS,
		)
		if err != nil {
			t.Fatalf("failed with explicit TIPS: %v", err)
		}
		if doc.CstmrCdtTrf.GrpHdr.SttlmInf.ClrSys.Prtry != "TIPS" {
			t.Errorf("expected TIPS, got %q", doc.CstmrCdtTrf.GrpHdr.SttlmInf.ClrSys.Prtry)
		}
	})

	t.Run("Explicit override to RT1", func(t *testing.T) {
		doc, err := engine.GenerateInstantPaymentInstruction(
			"E2E-ROUTE-OVERRIDE-RT1",
			amount, "EUR",
			"Debtor Corp", debtorIBAN, debtorBIC,
			"Creditor Corp", "NL91ABNA0417164300", "INGBNL2AXXX",
			"Explicit RT1 override",
			ClearingSystemRT1,
		)
		if err != nil {
			t.Fatalf("failed with explicit RT1: %v", err)
		}
		if doc.CstmrCdtTrf.GrpHdr.SttlmInf.ClrSys.Prtry != "RT1" {
			t.Errorf("expected RT1, got %q", doc.CstmrCdtTrf.GrpHdr.SttlmInf.ClrSys.Prtry)
		}
	})

	t.Run("Invalid clearing system override returns error", func(t *testing.T) {
		_, err := engine.GenerateInstantPaymentInstruction(
			"E2E-ROUTE-INVALID",
			amount, "EUR",
			"Debtor Corp", debtorIBAN, debtorBIC,
			"Creditor Corp", "NL91ABNA0417164300", "INGBNL2AXXX",
			"Invalid clearing system",
			ClearingSystemType("FEDWIRE"),
		)
		if err == nil {
			t.Fatalf("expected error with invalid clearing override")
		}
		if !errors.Is(err, ErrInvalidClearingSystem) {
			t.Errorf("expected ErrInvalidClearingSystem, got: %v", err)
		}
	})

	t.Run("Register custom participant route", func(t *testing.T) {
		customBIC := "CUSTOMXX"
		router.RegisterParticipant(customBIC, ClearingSystemTIPS)

		sys, found := router.GetParticipantClearingSystem(customBIC)
		if !found || sys != ClearingSystemTIPS {
			t.Errorf("failed to register custom participant")
		}

		resolved, err := router.Route("CUSTOMXXXXX", decimal.NewFromInt(1000))
		if err != nil {
			t.Fatalf("routing error: %v", err)
		}
		if resolved != ClearingSystemTIPS {
			t.Errorf("expected custom participant routed to TIPS, got %s", resolved)
		}
	})

	t.Run("Change default system to TIPS", func(t *testing.T) {
		router.SetDefaultSystem(ClearingSystemTIPS)
		// Unknown BIC without specific rule should now fallback to TIPS
		resolved, err := router.Route("UNKNWNDDXXX", decimal.NewFromInt(1000))
		if err != nil {
			t.Fatalf("routing error: %v", err)
		}
		if resolved != ClearingSystemTIPS {
			t.Errorf("expected fallback to TIPS, got %s", resolved)
		}
		// Reset back to RT1
		router.SetDefaultSystem(ClearingSystemRT1)
	})
}

// ============================================================================
// 5. ISO 7064 MOD-97 IBAN VALIDATOR TESTS ACROSS EUROPE
// ============================================================================

func TestSEPAInstant_Mod97IBANValidator(t *testing.T) {
	// Authentic European IBAN test vectors with mathematically verified Mod-97 checksums
	validIBANs := []struct {
		country string
		iban    string
		desc    string
	}{
		{"DE", "DE89370400440532013000", "Germany: 22 chars"},
		{"FR", "FR1420041010050500013M02606", "France: 27 chars with alphanumeric BBAN"},
		{"NL", "NL91ABNA0417164300", "Netherlands: 18 chars"},
		{"BE", "BE68539007547034", "Belgium: 16 chars"},
		{"ES", "ES9121000418450200051332", "Spain: 24 chars"},
		{"AT", "AT611904300234573201", "Austria: 20 chars"},
		{"GB", "GB29NWBK60161331926819", "United Kingdom: 22 chars"},
		{"IE", "IE29AIBK93115212345678", "Ireland: 22 chars"}, // Will verify with check digits test
	}

	for _, tc := range validIBANs {
		t.Run("Valid_"+tc.country+"_"+tc.desc, func(t *testing.T) {
			// Test with spaces & lowercase to verify sanitizer robustness
			formatted := FormatIBAN(tc.iban)
			withWhitespace := " " + strings.ToLower(formatted) + " \t"

			if err := ValidateEuropeanIBAN(withWhitespace); err != nil {
				t.Errorf("expected valid IBAN for %s (%s), got error: %v", tc.country, tc.iban, err)
			}
			if err := ValidateSEPAIBAN(withWhitespace); err != nil {
				t.Errorf("expected valid SEPA IBAN for %s, got: %v", tc.country, err)
			}
		})
	}

	// Tampered and invalid IBAN test vectors
	invalidIBANs := []struct {
		name        string
		iban        string
		expectedErr error
	}{
		{
			name:        "Tampered check digit in German IBAN",
			iban:        "DE88370400440532013000", // 89 altered to 88
			expectedErr: ErrInvalidIBANChecksum,
		},
		{
			name:        "Tampered account digit in Dutch IBAN",
			iban:        "NL91ABNA0417164301", // trailing 0 altered to 1
			expectedErr: ErrInvalidIBANChecksum,
		},
		{
			name:        "Length too short for Germany (21 instead of 22)",
			iban:        "DE8937040044053201300",
			expectedErr: ErrInvalidIBANLength,
		},
		{
			name:        "Length too long for Belgium (17 instead of 16)",
			iban:        "BE685390075470349",
			expectedErr: ErrInvalidIBANLength,
		},
		{
			name:        "Invalid character '@' in BBAN",
			iban:        "DE8937040044053201300@",
			expectedErr: ErrInvalidIBANFormat,
		},
		{
			name:        "Non-numeric check digits",
			iban:        "DEXX370400440532013000",
			expectedErr: ErrInvalidIBANFormat,
		},
		{
			name:        "Unrecognized country code ZZ",
			iban:        "ZZ99123456789012345678",
			expectedErr: ErrUnsupportedIBANCountry,
		},
		{
			name:        "Non-European country US",
			iban:        "US12123456789012345678",
			expectedErr: ErrUnsupportedIBANCountry,
		},
	}

	for _, tc := range invalidIBANs {
		t.Run("Invalid_"+tc.name, func(t *testing.T) {
			err := ValidateEuropeanIBAN(tc.iban)
			if err == nil {
				t.Fatalf("expected error for %s, but validation succeeded", tc.iban)
			}
			if !errors.Is(err, tc.expectedErr) {
				t.Errorf("expected error %v, got %v", tc.expectedErr, err)
			}
		})
	}
}

// ============================================================================
// 6. IBAN CHECK DIGIT CALCULATION & FORMATTING TESTS
// ============================================================================

func TestSEPAInstant_CalculateIBANCheckDigits(t *testing.T) {
	testVectors := []struct {
		countryCode string
		bban        string
		expectedChk string
	}{
		{"DE", "370400440532013000", "89"},
		{"FR", "20041010050500013M02606", "14"},
		{"NL", "ABNA0417164300", "91"},
		{"BE", "539007547034", "68"},
		{"ES", "21000418450200051332", "91"},
		{"AT", "1904300234573201", "61"},
		{"GB", "NWBK60161331926819", "29"},
	}

	for _, tc := range testVectors {
		t.Run("CheckDigits_"+tc.countryCode, func(t *testing.T) {
			calc, err := CalculateIBANCheckDigits(tc.countryCode, tc.bban)
			if err != nil {
				t.Fatalf("failed to calculate check digits for %s: %v", tc.countryCode, err)
			}
			if calc != tc.expectedChk {
				t.Errorf("check digit mismatch for %s: got %q, expected %q", tc.countryCode, calc, tc.expectedChk)
			}

			// Assemble generated IBAN and verify it passes full ISO 7064 validation
			fullIBAN := tc.countryCode + calc + tc.bban
			if err := ValidateEuropeanIBAN(fullIBAN); err != nil {
				t.Errorf("generated IBAN %s failed validation: %v", fullIBAN, err)
			}
		})
	}

	t.Run("Invalid BBAN length for check digit calculation", func(t *testing.T) {
		_, err := CalculateIBANCheckDigits("DE", "1234") // German BBAN must be 18 chars
		if err == nil {
			t.Errorf("expected error for short BBAN, got nil")
		}
		if !errors.Is(err, ErrInvalidIBANLength) {
			t.Errorf("expected ErrInvalidIBANLength, got: %v", err)
		}
	})
}

func TestSEPAInstant_FormatIBAN(t *testing.T) {
	raw := "de89370400440532013000"
	formatted := FormatIBAN(raw)
	expected := "DE89 3704 0044 0532 0130 00"
	if formatted != expected {
		t.Errorf("FormatIBAN mismatch: got %q, expected %q", formatted, expected)
	}

	if FormatIBAN("") != "" {
		t.Errorf("expected empty string for empty input")
	}
}

// ============================================================================
// 7. BIC VALIDATION TESTS (ISO 9362)
// ============================================================================

func TestSEPAInstant_BICValidation(t *testing.T) {
	validBICs := []string{
		"DBEUMM21",
		"DBEUMM21XXX",
		"BNPAFRPP",
		"BNPAFRPPXXX",
		"INGBNL2A",
		"INGBNL2AXXX",
		"MARKDEFF",
		"MARKDEFFXXX",
	}

	for _, bic := range validBICs {
		t.Run("Valid_"+bic, func(t *testing.T) {
			if err := ValidateSEPABIC(bic); err != nil {
				t.Errorf("expected valid BIC %q, got error: %v", bic, err)
			}
		})
	}

	invalidBICs := []string{
		"DBEU",          // Too short (4 chars)
		"DBEUMM2",       // 7 chars
		"DBEUMM21XXXX",  // 12 chars (too long)
		"DBEU MM21",     // Contains space
		"DBEUMM2!",      // Contains punctuation
		"",              // Empty
	}

	for _, bic := range invalidBICs {
		t.Run("Invalid_"+bic, func(t *testing.T) {
			if err := ValidateSEPABIC(bic); err == nil {
				t.Errorf("expected invalid BIC for %q, but succeeded", bic)
			}
		})
	}
}

// ============================================================================
// 8. REMITTANCE, END-TO-END ID & FIELD LENGTH CONSTRAINTS
// ============================================================================

func TestSEPAInstant_FieldConstraints(t *testing.T) {
	engine := NewSEPAInstantEngine()
	validAmount := decimal.NewFromInt(500)
	debtorIBAN := "DE89370400440532013000"
	creditorIBAN := "FR1420041010050500013M02606"
	debtorBIC := "DBEUMM21XXX"
	creditorBIC := "BNPAFRPPXXX"

	t.Run("Remittance info within 140 chars is accepted", func(t *testing.T) {
		memo140 := strings.Repeat("A", 140)
		_, err := engine.GenerateInstantPaymentInstruction(
			"E2E-REMIT-OK",
			validAmount, "EUR",
			"Debtor", debtorIBAN, debtorBIC,
			"Creditor", creditorIBAN, creditorBIC,
			memo140,
		)
		if err != nil {
			t.Fatalf("expected 140-char memo to succeed, got: %v", err)
		}
	})

	t.Run("Remittance info over 140 chars is rejected", func(t *testing.T) {
		memo141 := strings.Repeat("A", 141)
		_, err := engine.GenerateInstantPaymentInstruction(
			"E2E-REMIT-FAIL",
			validAmount, "EUR",
			"Debtor", debtorIBAN, debtorBIC,
			"Creditor", creditorIBAN, creditorBIC,
			memo141,
		)
		if err == nil {
			t.Fatalf("expected error for 141-char memo, got success")
		}
		if !errors.Is(err, ErrRemittanceTooLong) {
			t.Errorf("expected ErrRemittanceTooLong, got: %v", err)
		}
	})

	t.Run("Empty EndToEndId is rejected", func(t *testing.T) {
		_, err := engine.GenerateInstantPaymentInstruction(
			"",
			validAmount, "EUR",
			"Debtor", debtorIBAN, debtorBIC,
			"Creditor", creditorIBAN, creditorBIC,
			"Payment",
		)
		if err == nil {
			t.Fatalf("expected error for empty EndToEndId")
		}
		if !errors.Is(err, ErrEmptyEndToEndID) {
			t.Errorf("expected ErrEmptyEndToEndID, got: %v", err)
		}
	})

	t.Run("EndToEndId over 35 chars is rejected", func(t *testing.T) {
		longE2E := strings.Repeat("X", 36)
		_, err := engine.GenerateInstantPaymentInstruction(
			longE2E,
			validAmount, "EUR",
			"Debtor", debtorIBAN, debtorBIC,
			"Creditor", creditorIBAN, creditorBIC,
			"Payment",
		)
		if err == nil {
			t.Fatalf("expected error for long EndToEndId")
		}
		if !errors.Is(err, ErrEndToEndIDTooLong) {
			t.Errorf("expected ErrEndToEndIDTooLong, got: %v", err)
		}
	})

	t.Run("Empty party names rejected", func(t *testing.T) {
		_, err := engine.GenerateInstantPaymentInstruction(
			"E2E-PARTY-EMPTY",
			validAmount, "EUR",
			"", debtorIBAN, debtorBIC,
			"Creditor", creditorIBAN, creditorBIC,
			"Payment",
		)
		if err == nil {
			t.Fatalf("expected error for empty debtor name")
		}
		if !errors.Is(err, ErrEmptyPartyName) {
			t.Errorf("expected ErrEmptyPartyName, got: %v", err)
		}
	})
}
