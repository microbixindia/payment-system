package protocols

import (
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ============================================================================
// EPC SEPA CREDIT TRANSFER INSTANT (SCT INST) SPECIFICATIONS
// ISO 20022 pacs.008.001.08
// ============================================================================

const (
	// SEPASchemeIdentifier represents EPC SEPA Instant Credit Transfer standard.
	SEPASchemeIdentifier = "pacs.008.001.08"
	SEPANamespace        = "urn:iso:std:iso:20022:tech:xsd:pacs.008.001.08"

	// SCTInstCurrency is strictly EUR for all SEPA Instant transfers.
	SCTInstCurrency = "EUR"

	// EPC SCT Inst mandatory service level, local instrument, and charge bearer codes.
	ServiceLevelSEPA    = "SEPA"
	LocalInstrumentINST = "INST"
	ChargeBearerSLEV    = "SLEV" // Service Level: each party bears own charges

	// Settlement method is Clearing System
	SettlementMethodCLRG = "CLRG"
)

// SCTInstMaxTransactionCeiling is the regulatory transaction limit of €100,000.00
// mandated under the European Payments Council (EPC) SCT Inst Rulebook.
var SCTInstMaxTransactionCeiling = decimal.NewFromInt(100000)

var (
	ErrSEPALimitExceeded      = errors.New("settlement/sepa: transfer exceeds maximum allowable SCT Inst ceiling (€100,000.00)")
	ErrNonEURCurrency          = errors.New("settlement/sepa: only EUR currency is permitted for SEPA SCT Inst transfers")
	ErrInvalidSEPAAmount       = errors.New("settlement/sepa: transfer amount must be strictly greater than zero")
	ErrInvalidIBANFormat       = errors.New("settlement/sepa: invalid IBAN structure or character set")
	ErrInvalidIBANLength       = errors.New("settlement/sepa: invalid IBAN length for designated country")
	ErrInvalidIBANChecksum     = errors.New("settlement/sepa: IBAN failed ISO 7064 Mod-97 checksum verification")
	ErrUnsupportedIBANCountry  = errors.New("settlement/sepa: unrecognized or non-European IBAN country code")
	ErrNonSEPACountry          = errors.New("settlement/sepa: country is not a participating SEPA member")
	ErrInvalidSEPABIC          = errors.New("settlement/sepa: BIC must be 8 or 11 alphanumeric characters")
	ErrInvalidClearingSystem   = errors.New("settlement/sepa: unsupported or unroutable clearing system")
	ErrEmptyEndToEndID         = errors.New("settlement/sepa: end-to-end identifier cannot be empty")
	ErrEndToEndIDTooLong       = errors.New("settlement/sepa: end-to-end identifier exceeds maximum length of 35 characters")
	ErrEmptyPartyName          = errors.New("settlement/sepa: debtor and creditor names cannot be empty")
	ErrRemittanceTooLong       = errors.New("settlement/sepa: remittance information exceeds EPC maximum of 140 characters")
)

// EuropeanIBANLengths maps ISO 3166-1 alpha-2 country codes to official IBAN lengths across Europe & SEPA.
var EuropeanIBANLengths = map[string]int{
	"AD": 24, // Andorra (SEPA)
	"AL": 28, // Albania
	"AT": 20, // Austria (SEPA)
	"BA": 20, // Bosnia and Herzegovina
	"BE": 16, // Belgium (SEPA)
	"BG": 22, // Bulgaria (SEPA)
	"CH": 21, // Switzerland (SEPA)
	"CY": 28, // Cyprus (SEPA)
	"CZ": 24, // Czech Republic (SEPA)
	"DE": 22, // Germany (SEPA)
	"DK": 18, // Denmark (SEPA)
	"EE": 20, // Estonia (SEPA)
	"ES": 24, // Spain (SEPA)
	"FI": 18, // Finland (SEPA)
	"FO": 18, // Faroe Islands
	"FR": 27, // France (SEPA)
	"GB": 22, // United Kingdom (SEPA)
	"GE": 22, // Georgia
	"GI": 23, // Gibraltar (SEPA)
	"GL": 18, // Greenland
	"GR": 27, // Greece (SEPA)
	"HR": 21, // Croatia (SEPA)
	"HU": 28, // Hungary (SEPA)
	"IE": 22, // Ireland (SEPA)
	"IS": 26, // Iceland (SEPA)
	"IT": 27, // Italy (SEPA)
	"LI": 21, // Liechtenstein (SEPA)
	"LT": 20, // Lithuania (SEPA)
	"LU": 20, // Luxembourg (SEPA)
	"LV": 21, // Latvia (SEPA)
	"MC": 27, // Monaco (SEPA)
	"MD": 24, // Moldova
	"ME": 22, // Montenegro
	"MK": 19, // North Macedonia
	"MT": 31, // Malta (SEPA)
	"NL": 18, // Netherlands (SEPA)
	"NO": 15, // Norway (SEPA)
	"PL": 28, // Poland (SEPA)
	"PT": 25, // Portugal (SEPA)
	"RO": 24, // Romania (SEPA)
	"RS": 22, // Serbia
	"SE": 24, // Sweden (SEPA)
	"SI": 19, // Slovenia (SEPA)
	"SK": 24, // Slovakia (SEPA)
	"SM": 27, // San Marino (SEPA)
	"TR": 26, // Turkey
	"UA": 29, // Ukraine
	"VA": 22, // Vatican City (SEPA)
	"XK": 20, // Kosovo
}

// SEPAMemberCountries enumerates all official jurisdictions participating in the SEPA scheme.
var SEPAMemberCountries = map[string]bool{
	"AD": true, "AT": true, "BE": true, "BG": true, "CH": true,
	"CY": true, "CZ": true, "DE": true, "DK": true, "EE": true,
	"ES": true, "FI": true, "FR": true, "GB": true, "GI": true,
	"GR": true, "HR": true, "HU": true, "IE": true, "IS": true,
	"IT": true, "LI": true, "LT": true, "LU": true, "LV": true,
	"MC": true, "MT": true, "NL": true, "NO": true, "PL": true,
	"PT": true, "RO": true, "SE": true, "SI": true, "SK": true,
	"SM": true, "VA": true,
}

var bicRegex = regexp.MustCompile(`^[A-Z]{4}[A-Z]{2}[A-Z0-9]{2}([A-Z0-9]{3})?$`)

// ============================================================================
// CLEARING SYSTEM ENUMS & ROUTER (RT1 / TIPS)
// ============================================================================

// ClearingSystemType designates the European instant settlement infrastructure.
type ClearingSystemType string

const (
	// ClearingSystemRT1 designates EBA CLEARING's pan-European instant payment system.
	ClearingSystemRT1 ClearingSystemType = "RT1"

	// ClearingSystemTIPS designates ECB's TARGET Instant Payment Settlement.
	ClearingSystemTIPS ClearingSystemType = "TIPS"

	// ClearingSystemAUTO enables automated rule-based routing between RT1 and TIPS.
	ClearingSystemAUTO ClearingSystemType = "AUTO"
)

// SEPAClearingRouter decides optimal clearing rail (RT1 or TIPS) for SEPA SCT Inst.
type SEPAClearingRouter interface {
	Route(creditorBIC string, amount decimal.Decimal) (ClearingSystemType, error)
	RegisterParticipant(bicPrefix string, system ClearingSystemType)
	SetDefaultSystem(system ClearingSystemType)
	GetParticipantClearingSystem(bic string) (ClearingSystemType, bool)
}

// DefaultSEPAClearingRouter implements thread-safe dynamic routing between RT1 and TIPS.
type DefaultSEPAClearingRouter struct {
	mu                sync.RWMutex
	participantRoutes map[string]ClearingSystemType
	defaultSystem     ClearingSystemType
	tipsThreshold     decimal.Decimal
}

// NewDefaultSEPAClearingRouter creates a router pre-configured with known central/commercial bank routes.
func NewDefaultSEPAClearingRouter(defaultSys ClearingSystemType) *DefaultSEPAClearingRouter {
	if defaultSys != ClearingSystemRT1 && defaultSys != ClearingSystemTIPS {
		defaultSys = ClearingSystemRT1
	}

	r := &DefaultSEPAClearingRouter{
		participantRoutes: make(map[string]ClearingSystemType),
		defaultSystem:     defaultSys,
		tipsThreshold:     decimal.NewFromInt(50000), // Default high-value liquidity threshold
	}

	// ECB & Eurosystem Central Banks (default TIPS)
	r.participantRoutes["MARKDEFF"] = ClearingSystemTIPS // Deutsche Bundesbank
	r.participantRoutes["BDFEFRPP"] = ClearingSystemTIPS // Banque de France
	r.participantRoutes["BIITITRR"] = ClearingSystemTIPS // Banca d'Italia
	r.participantRoutes["ESPBESMM"] = ClearingSystemTIPS // Banco de España
	r.participantRoutes["ECBFRPP1"] = ClearingSystemTIPS // European Central Bank

	// Major pan-European commercial clearing participants on RT1
	r.participantRoutes["DBEUMM21"] = ClearingSystemRT1 // Deutsche Bank AG
	r.participantRoutes["BNPAFRPP"] = ClearingSystemRT1 // BNP Paribas
	r.participantRoutes["INGBNL2A"] = ClearingSystemRT1 // ING Bank
	r.participantRoutes["SOGEFRPP"] = ClearingSystemRT1 // Société Générale
	r.participantRoutes["BCENESMM"] = ClearingSystemRT1 // Banco Santander
	r.participantRoutes["KREDBEBB"] = ClearingSystemRT1 // KBC Bank
	r.participantRoutes["HYVEDEMM"] = ClearingSystemRT1 // UniCredit Bank AG

	return r
}

// Route resolves the clearing rail based on BIC routing hierarchy, participant registration, or default.
func (r *DefaultSEPAClearingRouter) Route(creditorBIC string, amount decimal.Decimal) (ClearingSystemType, error) {
	cleanBIC := strings.ToUpper(strings.TrimSpace(creditorBIC))
	if len(cleanBIC) != 8 && len(cleanBIC) != 11 {
		return "", fmt.Errorf("%w: BIC %q", ErrInvalidSEPABIC, creditorBIC)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	// 1. Exact 11-char BIC match
	if sys, ok := r.participantRoutes[cleanBIC]; ok {
		return sys, nil
	}

	// 2. 8-char BIC match (primary office code)
	if len(cleanBIC) == 11 {
		if sys, ok := r.participantRoutes[cleanBIC[:8]]; ok {
			return sys, nil
		}
	}

	// 3. 6-char country/location match (Bank + Country)
	if sys, ok := r.participantRoutes[cleanBIC[:6]]; ok {
		return sys, nil
	}

	// 4. 4-char institution code match
	if sys, ok := r.participantRoutes[cleanBIC[:4]]; ok {
		return sys, nil
	}

	// 5. Fallback to default configured system
	return r.defaultSystem, nil
}

// RegisterParticipant associates a specific BIC or BIC prefix with a clearing rail.
func (r *DefaultSEPAClearingRouter) RegisterParticipant(bicPrefix string, system ClearingSystemType) {
	r.mu.Lock()
	defer r.mu.Unlock()
	clean := strings.ToUpper(strings.TrimSpace(bicPrefix))
	r.participantRoutes[clean] = system
}

// SetDefaultSystem configures the fallback rail when no specific participant mapping exists.
func (r *DefaultSEPAClearingRouter) SetDefaultSystem(system ClearingSystemType) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if system == ClearingSystemRT1 || system == ClearingSystemTIPS {
		r.defaultSystem = system
	}
}

// GetParticipantClearingSystem retrieves registered clearing system for a BIC, if found.
func (r *DefaultSEPAClearingRouter) GetParticipantClearingSystem(bic string) (ClearingSystemType, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	clean := strings.ToUpper(strings.TrimSpace(bic))
	sys, ok := r.participantRoutes[clean]
	return sys, ok
}

// SetTipsThreshold sets an optional threshold above which AUTO routing favors central bank TIPS.
func (r *DefaultSEPAClearingRouter) SetTipsThreshold(threshold decimal.Decimal) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tipsThreshold = threshold
}

// ============================================================================
// ISO 20022 PACS.008.001.08 XML DATA STRUCTURES
// ============================================================================

// Pacs008InstDocument represents the root Document node for SEPA SCT Inst.
type Pacs008InstDocument struct {
	XMLName     xml.Name           `xml:"Document"`
	Xmlns       string             `xml:"xmlns,attr"`
	CstmrCdtTrf Pacs008InstMessage `xml:"FIToFICstmrCdtTrf"`
}

// Pacs008InstMessage encapsulates GroupHeader and Transaction details.
type Pacs008InstMessage struct {
	GrpHdr      GroupHeader008Inst           `xml:"GrpHdr"`
	CdtTrfTxInf []CreditTransferTxInf008Inst `xml:"CdtTrfTxInf"`
}

// GroupHeader008Inst contains message-level routing and clearing metadata.
type GroupHeader008Inst struct {
	MsgId    string             `xml:"MsgId"`
	CreDtTm  string             `xml:"CreDtTm"`
	NbOfTxs  int                `xml:"NbOfTxs"`
	SttlmInf SettlementInfoInst `xml:"SttlmInf"`
}

// SettlementInfoInst defines the settlement method and clearing system (RT1/TIPS).
type SettlementInfoInst struct {
	SttlmMtd string             `xml:"SttlmMtd"` // "CLRG"
	ClrSys   ClearingSystemInst `xml:"ClrSys"`
}

// ClearingSystemInst identifies the proprietary clearing channel (RT1 or TIPS).
type ClearingSystemInst struct {
	Prtry string `xml:"Prtry"` // "RT1" or "TIPS"
}

// CreditTransferTxInf008Inst details the instant credit transfer instruction.
type CreditTransferTxInf008Inst struct {
	PmtId          PaymentIdentificationInst  `xml:"PmtId"`
	PmtTpInf       PaymentTypeInformationInst `xml:"PmtTpInf"`
	IntrBkSttlmAmt ActiveCurrencyAndAmount    `xml:"IntrBkSttlmAmt"`
	IntrBkSttlmDt  string                     `xml:"IntrBkSttlmDt"` // YYYY-MM-DD
	SttlmTmReq     string                     `xml:"SttlmTmReq,omitempty"`
	ChrgBr         string                     `xml:"ChrgBr"` // "SLEV"
	Dbtr           PartyIdentification        `xml:"Dbtr"`
	DbtrAcct       SEPACashAccount            `xml:"DbtrAcct"`
	DbtrAgt        FinancialInstitution       `xml:"DbtrAgt"`
	CdtrAgt        FinancialInstitution       `xml:"CdtrAgt"`
	Cdtr           PartyIdentification        `xml:"Cdtr"`
	CdtrAcct       SEPACashAccount            `xml:"CdtrAcct"`
	Purp           *PurposeInst               `xml:"Purp,omitempty"`
	RmtInf         *RemittanceInformation     `xml:"RmtInf,omitempty"`
}

// PaymentIdentificationInst holds EndToEndId, EPC TxId, and UETR.
type PaymentIdentificationInst struct {
	EndToEndId string `xml:"EndToEndId"`
	TxId       string `xml:"TxId"`
	UETR       string `xml:"UETR"` // RFC 4122 UUID v4
}

// PaymentTypeInformationInst mandates SEPA service level and INST local instrument.
type PaymentTypeInformationInst struct {
	SvcLvl   ServiceLevelInst    `xml:"SvcLvl"`
	LclInstrm LocalInstrumentInst `xml:"LclInstrm"`
	CtgyPurp *CategoryPurposeInst `xml:"CtgyPurp,omitempty"`
}

type ServiceLevelInst struct {
	Cd string `xml:"Cd"` // "SEPA"
}

type LocalInstrumentInst struct {
	Cd string `xml:"Cd"` // "INST"
}

type CategoryPurposeInst struct {
	Cd string `xml:"Cd"`
}

type PurposeInst struct {
	Cd string `xml:"Cd"`
}

// SEPACashAccount enforces strict IBAN account identification per EPC rulebook.
type SEPACashAccount struct {
	Id SEPAAccountIdentification `xml:"Id"`
}

type SEPAAccountIdentification struct {
	IBAN string `xml:"IBAN"`
}

// ============================================================================
// ISO 7064 MOD-97 IBAN VALIDATOR & UTILITIES
// ============================================================================

// SanitizeIBAN strips all spaces, tabs, and hyphens and normalizes to uppercase.
func SanitizeIBAN(iban string) string {
	var b strings.Builder
	b.Grow(len(iban))
	for _, r := range iban {
		if r != ' ' && r != '\t' && r != '-' {
			if r >= 'a' && r <= 'z' {
				b.WriteRune(r - ('a' - 'A'))
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// ValidateEuropeanIBAN validates an IBAN against European national registry lengths and ISO 7064 Mod-97.
func ValidateEuropeanIBAN(iban string) error {
	clean := SanitizeIBAN(iban)
	if len(clean) < 4 {
		return fmt.Errorf("%w: IBAN is too short", ErrInvalidIBANFormat)
	}

	country := clean[:2]
	for _, r := range country {
		if r < 'A' || r > 'Z' {
			return fmt.Errorf("%w: country code %q must contain two uppercase letters", ErrInvalidIBANFormat, country)
		}
	}

	expectedLen, ok := EuropeanIBANLengths[country]
	if !ok {
		return fmt.Errorf("%w: country %s is not in the European IBAN registry", ErrUnsupportedIBANCountry, country)
	}

	if len(clean) != expectedLen {
		return fmt.Errorf("%w: country %s requires %d characters, got %d", ErrInvalidIBANLength, country, expectedLen, len(clean))
	}

	// Verify check digits are numeric
	if clean[2] < '0' || clean[2] > '9' || clean[3] < '0' || clean[3] > '9' {
		return fmt.Errorf("%w: check digits must be numeric", ErrInvalidIBANFormat)
	}

	// All remaining characters in BBAN must be alphanumeric
	for i := 4; i < len(clean); i++ {
		c := clean[i]
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z')) {
			return fmt.Errorf("%w: illegal character %c at position %d", ErrInvalidIBANFormat, c, i)
		}
	}

	// ISO 7064 Mod-97 verification
	if !verifyMod97(clean) {
		return fmt.Errorf("%w: checksum failed for %s", ErrInvalidIBANChecksum, clean)
	}

	return nil
}

// ValidateSEPAIBAN verifies that the IBAN is valid and originates from a designated SEPA member state.
func ValidateSEPAIBAN(iban string) error {
	if err := ValidateEuropeanIBAN(iban); err != nil {
		return err
	}
	country := SanitizeIBAN(iban)[:2]
	if !SEPAMemberCountries[country] {
		return fmt.Errorf("%w: country %s is not a participating SEPA jurisdiction", ErrNonSEPACountry, country)
	}
	return nil
}

// ValidateIBAN is an alias to ValidateEuropeanIBAN for general use.
func ValidateIBAN(iban string) error {
	return ValidateEuropeanIBAN(iban)
}

// verifyMod97 executes streaming ISO 7064 Mod-97-10 calculation without integer overflow.
// Returns true if and only if rearranged_number % 97 == 1.
func verifyMod97(iban string) bool {
	// Transpose first 4 characters (Country + Check Digits) to the end
	rearranged := iban[4:] + iban[:4]

	remainder := 0
	for i := 0; i < len(rearranged); i++ {
		ch := rearranged[i]
		if ch >= '0' && ch <= '9' {
			remainder = (remainder*10 + int(ch-'0')) % 97
		} else if ch >= 'A' && ch <= 'Z' {
			val := int(ch - 'A' + 10)
			// val is 10..35; expand into two decimal digits
			remainder = (remainder*10 + val/10) % 97
			remainder = (remainder*10 + val%10) % 97
		} else {
			return false
		}
	}
	return remainder == 1
}

// CalculateIBANCheckDigits computes the two-digit ISO 7064 Mod-97 check digits for a given country and BBAN.
func CalculateIBANCheckDigits(countryCode, bban string) (string, error) {
	countryCode = strings.ToUpper(strings.TrimSpace(countryCode))
	bban = SanitizeIBAN(bban)

	if len(countryCode) != 2 || countryCode[0] < 'A' || countryCode[0] > 'Z' || countryCode[1] < 'A' || countryCode[1] > 'Z' {
		return "", fmt.Errorf("%w: invalid country code %q", ErrInvalidIBANFormat, countryCode)
	}

	expectedLen, ok := EuropeanIBANLengths[countryCode]
	if ok && len(bban) != expectedLen-4 {
		return "", fmt.Errorf("%w: BBAN length %d does not match expected length %d for country %s",
			ErrInvalidIBANLength, len(bban), expectedLen-4, countryCode)
	}

	// Prepare string: BBAN + CountryCode + "00"
	testStr := bban + countryCode + "00"

	remainder := 0
	for i := 0; i < len(testStr); i++ {
		ch := testStr[i]
		if ch >= '0' && ch <= '9' {
			remainder = (remainder*10 + int(ch-'0')) % 97
		} else if ch >= 'A' && ch <= 'Z' {
			val := int(ch - 'A' + 10)
			remainder = (remainder*10 + val/10) % 97
			remainder = (remainder*10 + val%10) % 97
		} else {
			return "", fmt.Errorf("%w: invalid character %c in BBAN", ErrInvalidIBANFormat, ch)
		}
	}

	checkVal := 98 - remainder
	return fmt.Sprintf("%02d", checkVal), nil
}

// FormatIBAN formats an IBAN with electronic standard 4-character grouping.
func FormatIBAN(iban string) string {
	clean := SanitizeIBAN(iban)
	if len(clean) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, r := range clean {
		if i > 0 && i%4 == 0 {
			sb.WriteRune(' ')
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// IsSEPACountry checks whether a 2-letter ISO country code participates in SEPA.
func IsSEPACountry(countryCode string) bool {
	return SEPAMemberCountries[strings.ToUpper(strings.TrimSpace(countryCode))]
}

// ValidateSEPABIC verifies that a BIC conforms to ISO 9362 (8 or 11 alphanumeric characters).
func ValidateSEPABIC(bic string) error {
	clean := strings.ToUpper(strings.TrimSpace(bic))
	if len(clean) != 8 && len(clean) != 11 {
		return fmt.Errorf("%w: BIC %q must be 8 or 11 characters", ErrInvalidSEPABIC, bic)
	}
	if !bicRegex.MatchString(clean) {
		return fmt.Errorf("%w: BIC %q contains invalid characters or structure", ErrInvalidSEPABIC, bic)
	}
	return nil
}

// ============================================================================
// SEPA INSTANT PROTOCOL ENGINE
// ============================================================================

// SEPAInstantEngine builds, serializes, and deserializes ISO 20022 pacs.008.001.08 messages.
type SEPAInstantEngine struct {
	router SEPAClearingRouter
}

// NewSEPAInstantEngine creates a new SEPA Instant engine with clearing router.
func NewSEPAInstantEngine(router ...SEPAClearingRouter) *SEPAInstantEngine {
	var r SEPAClearingRouter
	if len(router) > 0 && router[0] != nil {
		r = router[0]
	} else {
		r = NewDefaultSEPAClearingRouter(ClearingSystemRT1)
	}
	return &SEPAInstantEngine{router: r}
}

// Router returns the configured SEPAClearingRouter.
func (e *SEPAInstantEngine) Router() SEPAClearingRouter {
	return e.router
}

// GenerateInstantPaymentInstruction constructs a validated pacs.008.001.08 payment instruction.
func (e *SEPAInstantEngine) GenerateInstantPaymentInstruction(
	endToEndID string,
	amount decimal.Decimal,
	currency string,
	debtorName, debtorIBAN, debtorBIC string,
	creditorName, creditorIBAN, creditorBIC string,
	remittanceInfo string,
	clearingOverride ...ClearingSystemType,
) (*Pacs008InstDocument, error) {
	// 1. Validate End-to-End ID
	trimmedE2E := strings.TrimSpace(endToEndID)
	if trimmedE2E == "" {
		return nil, ErrEmptyEndToEndID
	}
	if len(trimmedE2E) > 35 {
		return nil, fmt.Errorf("%w: length is %d characters", ErrEndToEndIDTooLong, len(trimmedE2E))
	}

	// 2. Validate Party Names
	if strings.TrimSpace(debtorName) == "" || strings.TrimSpace(creditorName) == "" {
		return nil, ErrEmptyPartyName
	}

	// 3. Validate Currency: strictly EUR for SEPA Instant
	if strings.ToUpper(strings.TrimSpace(currency)) != SCTInstCurrency {
		return nil, fmt.Errorf("%w: requested currency %q", ErrNonEURCurrency, currency)
	}

	// 4. Validate Regulatory Ceiling & Positivity
	if !amount.GreaterThan(decimal.Zero) {
		return nil, fmt.Errorf("%w: requested amount %s", ErrInvalidSEPAAmount, amount.StringFixed(2))
	}
	if amount.GreaterThan(SCTInstMaxTransactionCeiling) {
		return nil, fmt.Errorf("%w: requested amount €%s exceeds ceiling €%s",
			ErrSEPALimitExceeded, amount.StringFixed(2), SCTInstMaxTransactionCeiling.StringFixed(2))
	}

	// 5. Validate Debtor & Creditor IBANs
	cleanDebtorIBAN := SanitizeIBAN(debtorIBAN)
	if err := ValidateSEPAIBAN(cleanDebtorIBAN); err != nil {
		return nil, fmt.Errorf("debtor IBAN validation failed: %w", err)
	}

	cleanCreditorIBAN := SanitizeIBAN(creditorIBAN)
	if err := ValidateSEPAIBAN(cleanCreditorIBAN); err != nil {
		return nil, fmt.Errorf("creditor IBAN validation failed: %w", err)
	}

	// 6. Validate Debtor & Creditor BICs
	cleanDebtorBIC := strings.ToUpper(strings.TrimSpace(debtorBIC))
	if err := ValidateSEPABIC(cleanDebtorBIC); err != nil {
		return nil, fmt.Errorf("debtor BIC validation failed: %w", err)
	}

	cleanCreditorBIC := strings.ToUpper(strings.TrimSpace(creditorBIC))
	if err := ValidateSEPABIC(cleanCreditorBIC); err != nil {
		return nil, fmt.Errorf("creditor BIC validation failed: %w", err)
	}

	// 7. Validate Remittance Information length
	if len(remittanceInfo) > 140 {
		return nil, fmt.Errorf("%w: length is %d characters", ErrRemittanceTooLong, len(remittanceInfo))
	}

	// 8. Determine Clearing Channel (RT1 vs TIPS)
	var selectedClearing ClearingSystemType
	if len(clearingOverride) > 0 && clearingOverride[0] != ClearingSystemAUTO && clearingOverride[0] != "" {
		override := clearingOverride[0]
		if override != ClearingSystemRT1 && override != ClearingSystemTIPS {
			return nil, fmt.Errorf("%w: %q (must be RT1 or TIPS)", ErrInvalidClearingSystem, override)
		}
		selectedClearing = override
	} else {
		routed, err := e.router.Route(cleanCreditorBIC, amount)
		if err != nil {
			return nil, fmt.Errorf("clearing route resolution failed: %w", err)
		}
		selectedClearing = routed
	}

	now := time.Now().UTC()
	msgID := fmt.Sprintf("SCT-%s-%s", now.Format("20060102150405"), uuid.New().String()[:8])
	txID := fmt.Sprintf("TX-%s", uuid.New().String()[:12])
	uetr := uuid.New().String()

	doc := &Pacs008InstDocument{
		Xmlns: SEPANamespace,
		CstmrCdtTrf: Pacs008InstMessage{
			GrpHdr: GroupHeader008Inst{
				MsgId:   msgID,
				CreDtTm: now.Format(time.RFC3339),
				NbOfTxs: 1,
				SttlmInf: SettlementInfoInst{
					SttlmMtd: SettlementMethodCLRG,
					ClrSys:   ClearingSystemInst{Prtry: string(selectedClearing)},
				},
			},
			CdtTrfTxInf: []CreditTransferTxInf008Inst{
				{
					PmtId: PaymentIdentificationInst{
						EndToEndId: trimmedE2E,
						TxId:       txID,
						UETR:       uetr,
					},
					PmtTpInf: PaymentTypeInformationInst{
						SvcLvl:    ServiceLevelInst{Cd: ServiceLevelSEPA},
						LclInstrm: LocalInstrumentInst{Cd: LocalInstrumentINST},
					},
					IntrBkSttlmAmt: ActiveCurrencyAndAmount{
						Ccy:   SCTInstCurrency,
						Value: amount.StringFixed(2),
					},
					IntrBkSttlmDt: now.Format("2006-01-02"),
					SttlmTmReq:    now.Format(time.RFC3339),
					ChrgBr:        ChargeBearerSLEV,
					Dbtr: PartyIdentification{
						Nm: debtorName,
					},
					DbtrAcct: SEPACashAccount{
						Id: SEPAAccountIdentification{IBAN: cleanDebtorIBAN},
					},
					DbtrAgt: FinancialInstitution{
						FinInstnId: FinancialInstitutionIdentification{BICFI: cleanDebtorBIC},
					},
					CdtrAgt: FinancialInstitution{
						FinInstnId: FinancialInstitutionIdentification{BICFI: cleanCreditorBIC},
					},
					Cdtr: PartyIdentification{
						Nm: creditorName,
					},
					CdtrAcct: SEPACashAccount{
						Id: SEPAAccountIdentification{IBAN: cleanCreditorIBAN},
					},
				},
			},
		},
	}

	if remittanceInfo != "" {
		doc.CstmrCdtTrf.CdtTrfTxInf[0].RmtInf = &RemittanceInformation{
			Ustrd: remittanceInfo,
		}
	}

	return doc, nil
}

// SerializePacs008Inst marshals the instruction into standard XML with XML declaration header.
func (e *SEPAInstantEngine) SerializePacs008Inst(doc *Pacs008InstDocument) ([]byte, error) {
	if doc == nil {
		return nil, errors.New("settlement/sepa: cannot serialize nil document")
	}
	xmlData, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("settlement/sepa: failed to marshal pacs.008.001.08 XML: %w", err)
	}
	header := []byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	return append(header, xmlData...), nil
}

// DeserializePacs008Inst unmarshals raw XML into a Pacs008InstDocument.
func (e *SEPAInstantEngine) DeserializePacs008Inst(data []byte) (*Pacs008InstDocument, error) {
	if len(data) == 0 {
		return nil, errors.New("settlement/sepa: cannot deserialize empty XML data")
	}
	var doc Pacs008InstDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("settlement/sepa: failed to unmarshal pacs.008.001.08 XML: %w", err)
	}
	return &doc, nil
}
