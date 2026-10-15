package billing

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func TestTaxEngine_USStateSalesTax(t *testing.T) {
	engine := NewMultiJurisdictionTaxEngine()
	amount := decimal.NewFromInt(10000)

	// California destination
	reqCA := TaxCalculationRequest{
		SellerCountry:   "US",
		SellerRegion:    "DE",
		CustomerCountry: "US",
		CustomerRegion:  "CA",
		IsB2B:           true,
		TaxableAmount:   amount,
		Currency:        "USD",
	}

	resCA, err := engine.CalculateTax(reqCA)
	if err != nil {
		t.Fatalf("failed calculating CA tax: %v", err)
	}

	expectedTaxCA := decimal.NewFromFloat(725.0) // 7.25% of 10000
	if !resCA.TaxAmount.Equal(expectedTaxCA) {
		t.Errorf("CA tax mismatch: expected %s, got %s", expectedTaxCA, resCA.TaxAmount)
	}
	if resCA.Jurisdiction != JurisdictionUSState {
		t.Errorf("expected jurisdiction US_STATE, got %s", resCA.Jurisdiction)
	}

	// Reseller Exemption
	reqExempt := reqCA
	reqExempt.TaxExempt = true
	reqExempt.ExemptionCertificateID = "CERT-RESALE-998811"

	resExempt, err := engine.CalculateTax(reqExempt)
	if err != nil {
		t.Fatalf("failed calculating exempt tax: %v", err)
	}

	if !resExempt.TaxAmount.Equal(decimal.Zero) {
		t.Errorf("expected zero tax for exempt transaction, got %s", resExempt.TaxAmount)
	}
	if !resExempt.ExemptionApplied {
		t.Errorf("expected ExemptionApplied = true")
	}
}

func TestTaxEngine_EUVATReverseChargeAndDomestic(t *testing.T) {
	engine := NewMultiJurisdictionTaxEngine()
	amount := decimal.NewFromInt(50000)

	// 1. Cross-Border Intra-Community B2B Supply (Germany -> France with valid FR VAT ID)
	reqIntra := TaxCalculationRequest{
		SellerCountry:         "DE",
		CustomerCountry:       "FR",
		IsB2B:                 true,
		TaxRegistrationNumber: "FR 14 200410100", // Valid French VAT syntax: 2 chars + 9 digits
		TaxableAmount:         amount,
		Currency:              "EUR",
	}

	resIntra, err := engine.CalculateTax(reqIntra)
	if err != nil {
		t.Fatalf("failed calculating EU intra-community tax: %v", err)
	}

	if !resIntra.ReverseChargeApplied {
		t.Errorf("expected ReverseChargeApplied = true for EU B2B cross-border")
	}
	if !resIntra.TaxAmount.Equal(decimal.Zero) {
		t.Errorf("expected 0 tax under reverse charge, got %s", resIntra.TaxAmount)
	}

	// 2. Domestic German Supply (Germany -> Germany): 19% Standard German VAT
	reqDomestic := TaxCalculationRequest{
		SellerCountry:   "DE",
		CustomerCountry: "DE",
		IsB2B:           true,
		TaxableAmount:   amount,
		Currency:        "EUR",
	}

	resDom, err := engine.CalculateTax(reqDomestic)
	if err != nil {
		t.Fatalf("failed calculating domestic German tax: %v", err)
	}

	expectedTaxDom := decimal.NewFromFloat(9500.0) // 19% of 50000
	if !resDom.TaxAmount.Equal(expectedTaxDom) {
		t.Errorf("domestic German VAT mismatch: expected %s, got %s", expectedTaxDom, resDom.TaxAmount)
	}
	if resDom.ReverseChargeApplied {
		t.Errorf("reverse charge should NOT apply to domestic transactions")
	}
}

func TestTaxEngine_UKVATReverseCharge(t *testing.T) {
	engine := NewMultiJurisdictionTaxEngine()
	amount := decimal.NewFromInt(20000)

	// Inbound overseas B2B (US -> UK) with valid 9-digit GB VAT number
	req := TaxCalculationRequest{
		SellerCountry:         "US",
		CustomerCountry:       "GB",
		IsB2B:                 true,
		TaxRegistrationNumber: "GB 987654321",
		TaxableAmount:         amount,
		Currency:              "GBP",
	}

	res, err := engine.CalculateTax(req)
	if err != nil {
		t.Fatalf("failed calculating UK VAT: %v", err)
	}

	if !res.ReverseChargeApplied {
		t.Errorf("expected UK reverse charge applied")
	}
	if !res.TaxAmount.Equal(decimal.Zero) {
		t.Errorf("expected 0 tax under UK reverse charge, got %s", res.TaxAmount)
	}
}

func TestTaxEngine_CanadianGSTHST(t *testing.T) {
	engine := NewMultiJurisdictionTaxEngine()
	amount := decimal.NewFromInt(10000)

	// Ontario: 13% HST
	reqON := TaxCalculationRequest{
		SellerCountry:   "CA",
		CustomerCountry: "CA",
		CustomerRegion:  "ON",
		IsB2B:           true,
		TaxableAmount:   amount,
		Currency:        "CAD",
	}

	resON, err := engine.CalculateTax(reqON)
	if err != nil {
		t.Fatalf("failed calculating ON tax: %v", err)
	}

	expectedTaxON := decimal.NewFromFloat(1300.0) // 13% of 10000
	if !resON.TaxAmount.Equal(expectedTaxON) {
		t.Errorf("Ontario HST mismatch: expected %s, got %s", expectedTaxON, resON.TaxAmount)
	}

	// Alberta: 5% GST
	reqAB := reqON
	reqAB.CustomerRegion = "AB"

	resAB, err := engine.CalculateTax(reqAB)
	if err != nil {
		t.Fatalf("failed calculating AB tax: %v", err)
	}

	expectedTaxAB := decimal.NewFromFloat(500.0) // 5% of 10000
	if !resAB.TaxAmount.Equal(expectedTaxAB) {
		t.Errorf("Alberta GST mismatch: expected %s, got %s", expectedTaxAB, resAB.TaxAmount)
	}
}

func TestTaxEngine_AustralianABNMod89Verification(t *testing.T) {
	engine := NewMultiJurisdictionTaxEngine()
	amount := decimal.NewFromInt(15000)

	// Official ATO valid ABN: 51 824 753 556 (Australian Taxation Office itself)
	// (5-1)*10 + 1*1 + 8*3 + 2*5 + 4*7 + 7*9 + 5*11 + 3*13 + 5*15 + 5*17 + 6*19
	// = 40 + 1 + 24 + 10 + 28 + 63 + 55 + 39 + 75 + 85 + 114 = 534
	// 534 / 89 = 6 remainder 0!
	validABN := "51 824 753 556"

	if err := ValidateAustralianABN(validABN); err != nil {
		t.Fatalf("expected valid ABN for %s, got: %v", validABN, err)
	}

	// Inbound offshore supply to Australian B2B with valid ABN
	req := TaxCalculationRequest{
		SellerCountry:         "US",
		CustomerCountry:       "AU",
		IsB2B:                 true,
		TaxRegistrationNumber: validABN,
		TaxableAmount:         amount,
		Currency:              "AUD",
	}

	res, err := engine.CalculateTax(req)
	if err != nil {
		t.Fatalf("failed calculating AU tax: %v", err)
	}

	if !res.ReverseChargeApplied {
		t.Errorf("expected AU reverse charge under Division 84")
	}
	if !res.TaxAmount.Equal(decimal.Zero) {
		t.Errorf("expected 0 tax under AU reverse charge, got %s", res.TaxAmount)
	}

	// Tampered ABN (check digit altered)
	invalidABN := "51 824 753 557"
	err = ValidateAustralianABN(invalidABN)
	if err == nil {
		t.Fatalf("expected error for tampered ABN, but got nil")
	}

	reqTampered := req
	reqTampered.TaxRegistrationNumber = invalidABN
	_, err = engine.CalculateTax(reqTampered)
	if err == nil || !errors.Is(err, ErrInvalidTaxRegistration) {
		t.Errorf("expected ErrInvalidTaxRegistration for tampered ABN, got: %v", err)
	}
}
