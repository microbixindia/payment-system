package billing

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/shopspring/decimal"
)

// TaxJurisdiction defines the sovereign tax regime governing the transaction.
type TaxJurisdiction string

const (
	JurisdictionUSState   TaxJurisdiction = "US_STATE"
	JurisdictionEUVAT     TaxJurisdiction = "EU_VAT"
	JurisdictionUKVAT     TaxJurisdiction = "UK_VAT"
	JurisdictionCanada    TaxJurisdiction = "CANADA_GST_PST"
	JurisdictionAustralia TaxJurisdiction = "AUSTRALIA_GST"
	JurisdictionRoW       TaxJurisdiction = "REST_OF_WORLD_EXPORT"
)

var (
	ErrInvalidTaxRegistration = errors.New("tax_engine: invalid tax registration identifier format or checksum")
	ErrUnsupportedCountry     = errors.New("tax_engine: country code is unrecognized or missing")
	ErrNegativeTaxAmount      = errors.New("tax_engine: taxable base amount cannot be negative")
)

// EU Member States participating in EU VAT system
var EUCountries = map[string]bool{
	"AT": true, "BE": true, "BG": true, "HR": true, "CY": true, "CZ": true,
	"DK": true, "EE": true, "FI": true, "FR": true, "DE": true, "GR": true,
	"HU": true, "IE": true, "IT": true, "LV": true, "LT": true, "LU": true,
	"MT": true, "NL": true, "PL": true, "PT": true, "RO": true, "SK": true,
	"SI": true, "ES": true, "SE": true,
}

// Standard EU VAT Rates (as of 2026)
var EUStandardVATRates = map[string]decimal.Decimal{
	"AT": decimal.NewFromFloat(0.20),
	"BE": decimal.NewFromFloat(0.21),
	"BG": decimal.NewFromFloat(0.20),
	"CY": decimal.NewFromFloat(0.19),
	"CZ": decimal.NewFromFloat(0.21),
	"DE": decimal.NewFromFloat(0.19),
	"DK": decimal.NewFromFloat(0.25),
	"EE": decimal.NewFromFloat(0.22),
	"ES": decimal.NewFromFloat(0.21),
	"FI": decimal.NewFromFloat(0.255),
	"FR": decimal.NewFromFloat(0.20),
	"GR": decimal.NewFromFloat(0.24),
	"HR": decimal.NewFromFloat(0.25),
	"HU": decimal.NewFromFloat(0.27),
	"IE": decimal.NewFromFloat(0.23),
	"IT": decimal.NewFromFloat(0.22),
	"LT": decimal.NewFromFloat(0.21),
	"LU": decimal.NewFromFloat(0.17),
	"LV": decimal.NewFromFloat(0.21),
	"MT": decimal.NewFromFloat(0.18),
	"NL": decimal.NewFromFloat(0.21),
	"PL": decimal.NewFromFloat(0.23),
	"PT": decimal.NewFromFloat(0.23),
	"RO": decimal.NewFromFloat(0.19),
	"SE": decimal.NewFromFloat(0.25),
	"SI": decimal.NewFromFloat(0.22),
	"SK": decimal.NewFromFloat(0.20),
}

// US State Sales Tax Rates (Baseline State Rates for Digital Goods / SaaS)
var USStateTaxRates = map[string]decimal.Decimal{
	"CA": decimal.NewFromFloat(0.0725),
	"NY": decimal.NewFromFloat(0.0400),
	"TX": decimal.NewFromFloat(0.0625),
	"WA": decimal.NewFromFloat(0.0650),
	"IL": decimal.NewFromFloat(0.0625),
	"PA": decimal.NewFromFloat(0.0600),
	"FL": decimal.NewFromFloat(0.0600),
	"OH": decimal.NewFromFloat(0.0575),
	"MA": decimal.NewFromFloat(0.0625),
	"CO": decimal.NewFromFloat(0.0290),
}

// Canadian Combined GST/HST Rates by Province
var CanadaProvinceRates = map[string]decimal.Decimal{
	"ON": decimal.NewFromFloat(0.13),   // 13% HST
	"BC": decimal.NewFromFloat(0.12),   // 5% GST + 7% PST
	"QC": decimal.NewFromFloat(0.14975),// 5% GST + 9.975% QST
	"AB": decimal.NewFromFloat(0.05),   // 5% GST
	"NS": decimal.NewFromFloat(0.15),   // 15% HST
	"NB": decimal.NewFromFloat(0.15),   // 15% HST
	"NL": decimal.NewFromFloat(0.15),   // 15% HST
	"PE": decimal.NewFromFloat(0.15),   // 15% HST
	"MB": decimal.NewFromFloat(0.12),   // 5% GST + 7% PST
	"SK": decimal.NewFromFloat(0.11),   // 5% GST + 6% PST
}

// TaxCalculationRequest holds all inputs required to compute multi-jurisdiction tax.
type TaxCalculationRequest struct {
	SellerCountry          string          `json:"seller_country"`           // ISO 3166-1 alpha-2 (e.g. "US", "DE")
	SellerRegion           string          `json:"seller_region"`            // State or province
	CustomerCountry        string          `json:"customer_country"`         // ISO 3166-1 alpha-2
	CustomerRegion         string          `json:"customer_region"`          // State or province
	TaxRegistrationNumber  string          `json:"tax_registration_number"`  // VAT ID, ABN, GST/HST #
	IsB2B                  bool            `json:"is_b2b"`                   // True if verified corporate entity
	TaxExempt              bool            `json:"tax_exempt"`               // Explicit exemption certificate
	ExemptionCertificateID string          `json:"exemption_certificate_id"` // E.g. resale cert ID
	TaxableAmount          decimal.Decimal `json:"taxable_amount"`
	Currency               string          `json:"currency"`
}

// TaxCalculationResult encapsulates the assessed tax liability with compliance audit notes.
type TaxCalculationResult struct {
	Jurisdiction         TaxJurisdiction `json:"jurisdiction"`
	TaxRate              decimal.Decimal `json:"tax_rate"`
	TaxAmount            decimal.Decimal `json:"tax_amount"`
	TotalWithTax         decimal.Decimal `json:"total_with_tax"`
	ReverseChargeApplied bool            `json:"reverse_charge_applied"`
	ExemptionApplied     bool            `json:"exemption_applied"`
	JurisdictionCode     string          `json:"jurisdiction_code"`
	TaxSchemeDescription string          `json:"tax_scheme_description"`
	AuditTrailNotes      []string        `json:"audit_trail_notes"`
}

// MultiJurisdictionTaxEngine executes real-time enterprise tax evaluation.
type MultiJurisdictionTaxEngine struct {
	mu sync.RWMutex
}

// NewMultiJurisdictionTaxEngine instantiates a tax calculator.
func NewMultiJurisdictionTaxEngine() *MultiJurisdictionTaxEngine {
	return &MultiJurisdictionTaxEngine{}
}

// CalculateTax processes tax rules based on seller and customer jurisdictions.
func (eng *MultiJurisdictionTaxEngine) CalculateTax(req TaxCalculationRequest) (*TaxCalculationResult, error) {
	eng.mu.RLock()
	defer eng.mu.RUnlock()

	if req.TaxableAmount.IsNegative() {
		return nil, ErrNegativeTaxAmount
	}

	sellerCtry := strings.ToUpper(strings.TrimSpace(req.SellerCountry))
	custCtry := strings.ToUpper(strings.TrimSpace(req.CustomerCountry))

	if sellerCtry == "" || custCtry == "" {
		return nil, ErrUnsupportedCountry
	}

	result := &TaxCalculationResult{
		TaxRate:              decimal.Zero,
		TaxAmount:            decimal.Zero,
		TotalWithTax:         req.TaxableAmount,
		ReverseChargeApplied: false,
		ExemptionApplied:     false,
		AuditTrailNotes:      make([]string, 0, 4),
	}

	// 1. Check Explicit Tax Exemption Certificate (e.g. Wholesale Reseller Exemption)
	if req.TaxExempt {
		result.ExemptionApplied = true
		result.Jurisdiction = JurisdictionUSState
		result.TaxSchemeDescription = "Tax Exempt: Valid Resale Certificate"
		result.AuditTrailNotes = append(result.AuditTrailNotes,
			fmt.Sprintf("Exemption Certificate ID %s recognized; zero-rated.", req.ExemptionCertificateID))
		return result, nil
	}

	// 2. European Union VAT Rules (Directive 2006/112/EC)
	if EUCountries[custCtry] {
		return eng.calculateEUVAT(req, sellerCtry, custCtry, result)
	}

	// 3. United Kingdom VAT Rules (HMRC Post-Brexit)
	if custCtry == "GB" {
		return eng.calculateUKVAT(req, sellerCtry, result)
	}

	// 4. Canada GST/HST/PST Rules (CRA Place of Supply)
	if custCtry == "CA" {
		return eng.calculateCanadaTax(req, result)
	}

	// 5. Australia GST Rules (ATO Division 84 B2B Inbound Intangible Supplies)
	if custCtry == "AU" {
		return eng.calculateAustraliaGST(req, sellerCtry, result)
	}

	// 6. United States Sales Tax Rules (Wayfair Economic Nexus)
	if custCtry == "US" {
		return eng.calculateUSTax(req, result)
	}

	// 7. Rest of World Export (Zero-Rated B2B Cross-Border Service Export)
	result.Jurisdiction = JurisdictionRoW
	result.JurisdictionCode = custCtry
	result.TaxSchemeDescription = "Zero-Rated International B2B Data Export"
	result.AuditTrailNotes = append(result.AuditTrailNotes,
		fmt.Sprintf("Cross-border dataset export from %s to %s; exempt from local sales tax.", sellerCtry, custCtry))
	return result, nil
}

// calculateEUVAT applies EU VAT rules, including Intra-Community Reverse Charge (Art. 196).
func (eng *MultiJurisdictionTaxEngine) calculateEUVAT(
	req TaxCalculationRequest,
	sellerCtry, custCtry string,
	res *TaxCalculationResult,
) (*TaxCalculationResult, error) {
	res.Jurisdiction = JurisdictionEUVAT
	res.JurisdictionCode = custCtry

	cleanVAT := SanitizeTaxNumber(req.TaxRegistrationNumber)

	// Intra-Community B2B Supply: Seller in EU (different country) and Customer has valid EU VAT ID
	if EUCountries[sellerCtry] && sellerCtry != custCtry && req.IsB2B && cleanVAT != "" {
		if err := ValidateEUVATNumber(cleanVAT, custCtry); err != nil {
			return nil, fmt.Errorf("%w: customer VAT ID %s invalid: %v", ErrInvalidTaxRegistration, cleanVAT, err)
		}

		res.ReverseChargeApplied = true
		res.TaxRate = decimal.Zero
		res.TaxAmount = decimal.Zero
		res.TotalWithTax = req.TaxableAmount
		res.TaxSchemeDescription = "EU Intra-Community B2B Supply - Reverse Charge applies (Art. 196 EU VAT Directive)"
		res.AuditTrailNotes = append(res.AuditTrailNotes,
			fmt.Sprintf("Intra-community supply from %s to %s with verified VAT ID %s.", sellerCtry, custCtry, cleanVAT))
		return res, nil
	}

	// Non-EU Seller to EU B2B customer with valid VAT ID: Reverse Charge applies
	if !EUCountries[sellerCtry] && req.IsB2B && cleanVAT != "" {
		if err := ValidateEUVATNumber(cleanVAT, custCtry); err != nil {
			return nil, fmt.Errorf("%w: customer VAT ID %s invalid: %v", ErrInvalidTaxRegistration, cleanVAT, err)
		}

		res.ReverseChargeApplied = true
		res.TaxRate = decimal.Zero
		res.TaxAmount = decimal.Zero
		res.TotalWithTax = req.TaxableAmount
		res.TaxSchemeDescription = "Reverse Charge - B2B Inbound Electronic Services"
		res.AuditTrailNotes = append(res.AuditTrailNotes,
			fmt.Sprintf("Inbound service export from %s to EU member %s with VAT ID %s.", sellerCtry, custCtry, cleanVAT))
		return res, nil
	}

	// Domestic EU Supply (Seller and Customer in same EU state) or B2C / Unregistered:
	rate, ok := EUStandardVATRates[custCtry]
	if !ok {
		rate = decimal.NewFromFloat(0.20)
	}

	res.TaxRate = rate
	res.TaxAmount = req.TaxableAmount.Mul(rate).Round(4)
	res.TotalWithTax = req.TaxableAmount.Add(res.TaxAmount)
	res.TaxSchemeDescription = fmt.Sprintf("Standard EU VAT (%s %s%%)", custCtry, rate.Mul(decimal.NewFromInt(100)).StringFixed(1))
	res.AuditTrailNotes = append(res.AuditTrailNotes,
		fmt.Sprintf("Local destination VAT applied at %s%% for %s.", rate.Mul(decimal.NewFromInt(100)), custCtry))
	return res, nil
}

// calculateUKVAT applies UK VAT rules post-Brexit.
func (eng *MultiJurisdictionTaxEngine) calculateUKVAT(
	req TaxCalculationRequest,
	sellerCtry string,
	res *TaxCalculationResult,
) (*TaxCalculationResult, error) {
	res.Jurisdiction = JurisdictionUKVAT
	res.JurisdictionCode = "GB"

	cleanVAT := SanitizeTaxNumber(req.TaxRegistrationNumber)

	// Overseas seller to UK B2B customer with valid GB VAT ID: Customer accounts for VAT under reverse charge
	if sellerCtry != "GB" && req.IsB2B && cleanVAT != "" {
		if err := ValidateUKVATNumber(cleanVAT); err != nil {
			return nil, fmt.Errorf("%w: UK VAT %s invalid: %v", ErrInvalidTaxRegistration, cleanVAT, err)
		}

		res.ReverseChargeApplied = true
		res.TaxRate = decimal.Zero
		res.TaxAmount = decimal.Zero
		res.TotalWithTax = req.TaxableAmount
		res.TaxSchemeDescription = "UK B2B Cross-Border Reverse Charge (Section 8 VATA 1994)"
		res.AuditTrailNotes = append(res.AuditTrailNotes,
			fmt.Sprintf("UK reverse charge applied for overseas seller %s to registered UK entity %s.", sellerCtry, cleanVAT))
		return res, nil
	}

	// Domestic UK or unregistered: Standard 20% UK VAT
	rate := decimal.NewFromFloat(0.20)
	res.TaxRate = rate
	res.TaxAmount = req.TaxableAmount.Mul(rate).Round(4)
	res.TotalWithTax = req.TaxableAmount.Add(res.TaxAmount)
	res.TaxSchemeDescription = "UK Standard VAT (20%)"
	res.AuditTrailNotes = append(res.AuditTrailNotes, "UK standard VAT charged at 20%.")
	return res, nil
}

// calculateCanadaTax assesses Canadian GST/HST by province.
func (eng *MultiJurisdictionTaxEngine) calculateCanadaTax(
	req TaxCalculationRequest,
	res *TaxCalculationResult,
) (*TaxCalculationResult, error) {
	res.Jurisdiction = JurisdictionCanada
	prov := strings.ToUpper(strings.TrimSpace(req.CustomerRegion))
	res.JurisdictionCode = prov

	rate, ok := CanadaProvinceRates[prov]
	if !ok {
		rate = decimal.NewFromFloat(0.05) // Fallback to 5% federal GST
		prov = "CA-FED"
	}

	res.TaxRate = rate
	res.TaxAmount = req.TaxableAmount.Mul(rate).Round(4)
	res.TotalWithTax = req.TaxableAmount.Add(res.TaxAmount)
	res.TaxSchemeDescription = fmt.Sprintf("Canadian GST/HST (%s: %s%%)", prov, rate.Mul(decimal.NewFromInt(100)).StringFixed(2))
	res.AuditTrailNotes = append(res.AuditTrailNotes,
		fmt.Sprintf("Canadian sales tax levied for province %s at combined rate %s%%.", prov, rate.Mul(decimal.NewFromInt(100))))
	return res, nil
}

// calculateAustraliaGST assesses Australian GST with Division 84 reverse charge.
func (eng *MultiJurisdictionTaxEngine) calculateAustraliaGST(
	req TaxCalculationRequest,
	sellerCtry string,
	res *TaxCalculationResult,
) (*TaxCalculationResult, error) {
	res.Jurisdiction = JurisdictionAustralia
	res.JurisdictionCode = "AU"

	cleanABN := SanitizeTaxNumber(req.TaxRegistrationNumber)

	// Inbound offshore B2B supplier with verified Australian Business Number (ABN): Reverse Charge
	if sellerCtry != "AU" && req.IsB2B && cleanABN != "" {
		if err := ValidateAustralianABN(cleanABN); err != nil {
			return nil, fmt.Errorf("%w: invalid Australian ABN %s: %v", ErrInvalidTaxRegistration, cleanABN, err)
		}

		res.ReverseChargeApplied = true
		res.TaxRate = decimal.Zero
		res.TaxAmount = decimal.Zero
		res.TotalWithTax = req.TaxableAmount
		res.TaxSchemeDescription = "Australian GST Reverse Charge - B2B Inbound Intangible (Division 84)"
		res.AuditTrailNotes = append(res.AuditTrailNotes,
			fmt.Sprintf("Australian B2B reverse charge applied under verified ABN %s.", cleanABN))
		return res, nil
	}

	// Domestic AU or unregistered: 10% GST
	rate := decimal.NewFromFloat(0.10)
	res.TaxRate = rate
	res.TaxAmount = req.TaxableAmount.Mul(rate).Round(4)
	res.TotalWithTax = req.TaxableAmount.Add(res.TaxAmount)
	res.TaxSchemeDescription = "Australian Goods and Services Tax (GST 10%)"
	res.AuditTrailNotes = append(res.AuditTrailNotes, "Australian GST assessed at standard 10%.")
	return res, nil
}

// calculateUSTax assesses US State Sales Tax by destination nexus state.
func (eng *MultiJurisdictionTaxEngine) calculateUSTax(
	req TaxCalculationRequest,
	res *TaxCalculationResult,
) (*TaxCalculationResult, error) {
	res.Jurisdiction = JurisdictionUSState
	state := strings.ToUpper(strings.TrimSpace(req.CustomerRegion))
	res.JurisdictionCode = state

	rate, ok := USStateTaxRates[state]
	if !ok {
		// States without state-level sales tax (NH, OR, MT, DE, AK) or unconfigured
		rate = decimal.Zero
	}

	res.TaxRate = rate
	res.TaxAmount = req.TaxableAmount.Mul(rate).Round(4)
	res.TotalWithTax = req.TaxableAmount.Add(res.TaxAmount)
	res.TaxSchemeDescription = fmt.Sprintf("US State Sales Tax (%s: %s%%)", state, rate.Mul(decimal.NewFromInt(100)).StringFixed(2))
	res.AuditTrailNotes = append(res.AuditTrailNotes,
		fmt.Sprintf("US destination economic nexus sales tax assessed for %s at %s%%.", state, rate.Mul(decimal.NewFromInt(100))))
	return res, nil
}

// ============================================================================
// TAX IDENTIFIER VALIDATORS & CHECKSUM ENGINES
// ============================================================================

// SanitizeTaxNumber strips spaces, periods, and hyphens.
func SanitizeTaxNumber(input string) string {
	var b strings.Builder
	b.Grow(len(input))
	for _, r := range input {
		if r != ' ' && r != '-' && r != '.' && r != '/' {
			b.WriteRune(r)
		}
	}
	return strings.ToUpper(b.String())
}

// ValidateEUVATNumber verifies the syntax of European VIES VAT IDs.
func ValidateEUVATNumber(vatID, countryCode string) error {
	clean := SanitizeTaxNumber(vatID)

	// If VAT starts with country code, verify match
	if len(clean) > 2 && clean[:2] == countryCode {
		clean = clean[2:]
	}

	switch countryCode {
	case "DE": // 9 digits
		if matched, _ := regexp.MatchString(`^\d{9}$`, clean); !matched {
			return errors.New("german VAT requires 9 digits")
		}
	case "FR": // 2 characters (letters or digits) + 9 digits
		if matched, _ := regexp.MatchString(`^[A-Z0-9]{2}\d{9}$`, clean); !matched {
			return errors.New("french VAT requires 2 alphanumeric chars followed by 9 digits")
		}
	case "NL": // 9 digits + 'B' + 2 digits
		if matched, _ := regexp.MatchString(`^\d{9}B\d{2}$`, clean); !matched {
			return errors.New("dutch VAT requires 9 digits, 'B', and 2 digits")
		}
	case "BE": // 10 digits (often starts with 0)
		if matched, _ := regexp.MatchString(`^0?\d{9,10}$`, clean); !matched {
			return errors.New("belgian VAT requires 10 digits")
		}
	case "IT": // 11 digits
		if matched, _ := regexp.MatchString(`^\d{11}$`, clean); !matched {
			return errors.New("italian VAT requires 11 digits")
		}
	case "ES": // 9 chars (e.g. A12345674 or 12345674A)
		if matched, _ := regexp.MatchString(`^[A-Z0-9]\d{7}[A-Z0-9]$`, clean); !matched {
			return errors.New("spanish VAT requires 9 alphanumeric characters")
		}
	default:
		if len(clean) < 6 || len(clean) > 14 {
			return errors.New("general EU VAT length must be between 6 and 14 characters")
		}
	}

	return nil
}

// ValidateUKVATNumber checks British HMRC VAT syntax (9 or 12 digits).
func ValidateUKVATNumber(vatID string) error {
	clean := SanitizeTaxNumber(vatID)
	if strings.HasPrefix(clean, "GB") {
		clean = clean[2:]
	}

	if matched, _ := regexp.MatchString(`^\d{9}$`, clean); matched {
		return nil
	}
	if matched, _ := regexp.MatchString(`^\d{12}$`, clean); matched {
		return nil
	}
	return errors.New("UK VAT must be 9 or 12 digits")
}

// ValidateAustralianABN implements the official Australian Taxation Office Mod-89 check digit algorithm.
// Formula: Subtract 1 from 1st digit, multiply by weights [10, 1, 3, 5, 7, 9, 11, 13, 15, 17, 19], sum mod 89 == 0.
func ValidateAustralianABN(abn string) error {
	clean := SanitizeTaxNumber(abn)
	if len(clean) != 11 {
		return errors.New("ABN must be exactly 11 digits")
	}

	weights := [11]int{10, 1, 3, 5, 7, 9, 11, 13, 15, 17, 19}
	sum := 0

	for i := 0; i < 11; i++ {
		digit, err := strconv.Atoi(string(clean[i]))
		if err != nil {
			return errors.New("ABN contains non-numeric characters")
		}
		if i == 0 {
			digit -= 1 // Subtract 1 from first digit
		}
		sum += digit * weights[i]
	}

	if sum%89 != 0 {
		return errors.New("ABN Mod-89 checksum verification failed")
	}

	return nil
}
