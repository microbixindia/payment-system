package protocols

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

// ISO 8583 Standard Message Type Identifiers (MTI)
const (
	MTIAuthorizationRequest  = "0100"
	MTIAuthorizationResponse = "0110"
	MTIFinancialRequest      = "0200"
	MTIFinancialResponse     = "0210"
	MTIReversalRequest       = "0400"
	MTIReversalResponse      = "0410"
	MTINetworkManagementReq  = "0800"
	MTINetworkManagementResp = "0810"
)

// Field Encoding Formats per ISO 8583 specification
type FieldFormat int

const (
	FormatNumericFixed FieldFormat = iota
	FormatAlphaFixed
	FormatLLVARNumeric
	FormatLLVARAlpha
	FormatLLLVARNumeric
	FormatLLLVARAlpha
	FormatLLLVARBinary
	FormatBinaryFixed
)

// ISO 8583 Error Definitions
var (
	ErrInvalidMTI             = errors.New("iso8583: invalid message type identifier length or format")
	ErrInvalidBitmap          = errors.New("iso8583: malformed primary or secondary bitmap")
	ErrFieldExceedsLimit      = errors.New("iso8583: data element length exceeds specification ceiling")
	ErrFieldMandatoryMissing  = errors.New("iso8583: mandatory data element missing from message payload")
	ErrMalformedBERTLV        = errors.New("iso8583/emv: malformed BER-TLV byte sequence")
	ErrInvalidFieldDefinition = errors.New("iso8583: unknown or undefined data element")
	ErrUnpackBufferExhausted  = errors.New("iso8583: payload truncated or insufficient bytes for unpacking")
)

// FieldDefinition specifies parsing and encoding constraints for an ISO 8583 Data Element.
type FieldDefinition struct {
	FieldID     int
	Name        string
	Format      FieldFormat
	Length      int // Fixed length or maximum length for variable fields
	PadChar     byte
	PadLeft     bool
	Description string
}

// Global ISO 8583:1993 Data Element Dictionary
var ISODictionary = map[int]FieldDefinition{
	1:   {FieldID: 1, Name: "Secondary Bitmap", Format: FormatBinaryFixed, Length: 8, Description: "Bit map, extended"},
	2:   {FieldID: 2, Name: "Primary Account Number (PAN)", Format: FormatLLVARNumeric, Length: 19, Description: "Masked or tokenized card number"},
	3:   {FieldID: 3, Name: "Processing Code", Format: FormatNumericFixed, Length: 6, Description: "Transaction type and account selector"},
	4:   {FieldID: 4, Name: "Amount, Transaction", Format: FormatNumericFixed, Length: 12, Description: "Settlement amount in smallest currency unit"},
	7:   {FieldID: 7, Name: "Transmission Date & Time", Format: FormatNumericFixed, Length: 10, Description: "MMDDhhmmss format in UTC"},
	11:  {FieldID: 11, Name: "Systems Trace Audit Number (STAN)", Format: FormatNumericFixed, Length: 6, Description: "Unique terminal transaction identifier"},
	12:  {FieldID: 12, Name: "Time, Local Transaction", Format: FormatNumericFixed, Length: 6, Description: "hhmmss in merchant local time"},
	13:  {FieldID: 13, Name: "Date, Local Transaction", Format: FormatNumericFixed, Length: 4, Description: "MMDD in merchant local time"},
	14:  {FieldID: 14, Name: "Expiration Date", Format: FormatNumericFixed, Length: 4, Description: "YYMM card expiration date"},
	18:  {FieldID: 18, Name: "Merchant Category Code (MCC)", Format: FormatNumericFixed, Length: 4, Description: "ISO 18245 commercial dataset category code"},
	22:  {FieldID: 22, Name: "Point of Service Entry Mode", Format: FormatNumericFixed, Length: 3, Description: "PAN and PIN entry capability code"},
	25:  {FieldID: 25, Name: "Point of Service Condition Code", Format: FormatNumericFixed, Length: 2, Description: "Customer presence and e-commerce indicators"},
	32:  {FieldID: 32, Name: "Acquiring Institution ID Code", Format: FormatLLVARNumeric, Length: 11, Description: "Clearing bank identifier"},
	37:  {FieldID: 37, Name: "Retrieval Reference Number (RRN)", Format: FormatAlphaFixed, Length: 12, Description: "Unique interbank trace key"},
	38:  {FieldID: 38, Name: "Authorization Identification Response", Format: FormatAlphaFixed, Length: 6, Description: "Issuer authorization approval code"},
	39:  {FieldID: 39, Name: "Response Code", Format: FormatAlphaFixed, Length: 2, Description: "Action code ('00'=Approved, '05'=Do Not Honor)"},
	41:  {FieldID: 41, Name: "Card Acceptor Terminal Identification", Format: FormatAlphaFixed, Length: 8, Description: "Cloud edge gateway node identifier"},
	42:  {FieldID: 42, Name: "Card Acceptor Identification Code", Format: FormatAlphaFixed, Length: 15, Description: "DataVendor B2B Merchant ID"},
	43:  {FieldID: 43, Name: "Card Acceptor Name/Location", Format: FormatAlphaFixed, Length: 40, Description: "Corporate merchant legal identity"},
	48:  {FieldID: 48, Name: "Private Additional Data", Format: FormatLLLVARAlpha, Length: 999, Description: "B2B telemetry contract reference"},
	49:  {FieldID: 49, Name: "Currency Code, Transaction", Format: FormatNumericFixed, Length: 3, Description: "ISO 4217 numeric (840=USD, 978=EUR)"},
	52:  {FieldID: 52, Name: "Personal Identification Number (PIN) Block", Format: FormatBinaryFixed, Length: 8, Description: "Encrypted ANSI X9.8 PIN Block"},
	53:  {FieldID: 53, Name: "Security Related Control Information", Format: FormatNumericFixed, Length: 16, Description: "Key management and DUKPT registers"},
	55:  {FieldID: 55, Name: "Integrated Circuit Card (ICC) System Data", Format: FormatLLLVARBinary, Length: 999, Description: "EMV Level 2 BER-TLV chip tags"},
	62:  {FieldID: 62, Name: "Commercial Fleet / Level 3 Purchase Data", Format: FormatLLLVARAlpha, Length: 999, Description: "Dataset itemization, tax ref, SKU"},
	64:  {FieldID: 64, Name: "Message Authentication Code (MAC) Primary", Format: FormatBinaryFixed, Length: 8, Description: "HMAC-SHA256 signature block (first 8 bytes)"},
	70:  {FieldID: 70, Name: "Network Management Information Code", Format: FormatNumericFixed, Length: 3, Description: "Echo test (301) / Sign-on (001)"},
	128: {FieldID: 128, Name: "Message Authentication Code (MAC) Extended", Format: FormatBinaryFixed, Length: 8, Description: "Secondary MAC block"},
}

// ISO8583Message represents an ISO 8583 financial interchange packet.
type ISO8583Message struct {
	MTI     string
	Bitmap  [129]bool // 1-indexed for Data Elements 1 through 128
	Fields  map[int]string
	RawBits map[int][]byte // For binary elements (DE 1, 52, 55, 64, 128)
	mu      sync.RWMutex
}

// NewISO8583Message allocates a structured interchange message.
func NewISO8583Message(mti string) (*ISO8583Message, error) {
	if len(mti) != 4 {
		return nil, ErrInvalidMTI
	}
	for _, ch := range mti {
		if ch < '0' || ch > '9' {
			return nil, ErrInvalidMTI
		}
	}

	return &ISO8583Message{
		MTI:     mti,
		Fields:  make(map[int]string),
		RawBits: make(map[int][]byte),
	}, nil
}

// SetField assigns a string value to a Data Element and flips the corresponding bitmap flag.
func (msg *ISO8583Message) SetField(fieldID int, value string) error {
	msg.mu.Lock()
	defer msg.mu.Unlock()

	if fieldID < 1 || fieldID > 128 {
		return ErrInvalidFieldDefinition
	}

	def, ok := ISODictionary[fieldID]
	if !ok {
		return fmt.Errorf("%w: field DE %d is not configured in dictionary", ErrInvalidFieldDefinition, fieldID)
	}

	// Length checks
	switch def.Format {
	case FormatNumericFixed, FormatAlphaFixed:
		if len(value) != def.Length {
			return fmt.Errorf("%w: field %d requires exact length %d, got %d", ErrFieldExceedsLimit, fieldID, def.Length, len(value))
		}
	case FormatLLVARNumeric, FormatLLVARAlpha:
		if len(value) > 99 || len(value) > def.Length {
			return fmt.Errorf("%w: field %d exceeds LLVAR limit %d", ErrFieldExceedsLimit, fieldID, def.Length)
		}
	case FormatLLLVARNumeric, FormatLLLVARAlpha:
		if len(value) > 999 || len(value) > def.Length {
			return fmt.Errorf("%w: field %d exceeds LLLVAR limit %d", ErrFieldExceedsLimit, fieldID, def.Length)
		}
	}

	msg.Fields[fieldID] = value
	msg.Bitmap[fieldID] = true
	if fieldID > 64 {
		msg.Bitmap[1] = true // Enable secondary bitmap flag
	}
	return nil
}

// SetBinaryField assigns raw bytes to binary Data Elements (e.g. DE 52 PIN block, DE 55 EMV, DE 64 MAC).
func (msg *ISO8583Message) SetBinaryField(fieldID int, data []byte) error {
	msg.mu.Lock()
	defer msg.mu.Unlock()

	if fieldID < 1 || fieldID > 128 {
		return ErrInvalidFieldDefinition
	}

	def, ok := ISODictionary[fieldID]
	if !ok {
		return fmt.Errorf("%w: field DE %d not configured in dictionary", ErrInvalidFieldDefinition, fieldID)
	}

	if def.Format == FormatBinaryFixed && len(data) != def.Length {
		return fmt.Errorf("%w: binary field %d requires %d bytes, got %d", ErrFieldExceedsLimit, fieldID, def.Length, len(data))
	}

	cp := make([]byte, len(data))
	copy(cp, data)
	msg.RawBits[fieldID] = cp
	msg.Fields[fieldID] = hex.EncodeToString(cp)
	msg.Bitmap[fieldID] = true
	if fieldID > 64 {
		msg.Bitmap[1] = true
	}
	return nil
}

// GetField retrieves string value of a Data Element.
func (msg *ISO8583Message) GetField(fieldID int) (string, bool) {
	msg.mu.RLock()
	defer msg.mu.RUnlock()
	val, ok := msg.Fields[fieldID]
	return val, ok
}

// GetBinaryField retrieves raw byte slice for binary Data Elements.
func (msg *ISO8583Message) GetBinaryField(fieldID int) ([]byte, bool) {
	msg.mu.RLock()
	defer msg.mu.RUnlock()
	val, ok := msg.RawBits[fieldID]
	if ok {
		cp := make([]byte, len(val))
		copy(cp, val)
		return cp, true
	}
	return nil, false
}

// Pack serializes the complete ISO 8583 message into wire-level byte payload.
// Wire format: [4-byte MTI ASCII] [8 or 16 byte Bitmap] [Concatenated Data Elements]
func (msg *ISO8583Message) Pack() ([]byte, error) {
	msg.mu.Lock()
	defer msg.mu.Unlock()

	var buf bytes.Buffer

	// 1. Write MTI (4 ASCII bytes)
	buf.WriteString(msg.MTI)

	// 2. Compute Primary and Secondary Bitmaps
	hasSecondary := false
	for i := 65; i <= 128; i++ {
		if msg.Bitmap[i] {
			hasSecondary = true
			break
		}
	}
	msg.Bitmap[1] = hasSecondary

	primaryBitmap := make([]byte, 8)
	for i := 1; i <= 64; i++ {
		if msg.Bitmap[i] {
			byteIdx := (i - 1) / 8
			bitIdx := 7 - ((i - 1) % 8)
			primaryBitmap[byteIdx] |= (1 << bitIdx)
		}
	}
	buf.Write(primaryBitmap)

	if hasSecondary {
		secondaryBitmap := make([]byte, 8)
		for i := 65; i <= 128; i++ {
			if msg.Bitmap[i] {
				byteIdx := (i - 65) / 8
				bitIdx := 7 - ((i - 65) % 8)
				secondaryBitmap[byteIdx] |= (1 << bitIdx)
			}
		}
		buf.Write(secondaryBitmap)
	}

	// 3. Serialize active Data Elements in numerical ascending order
	maxField := 64
	if hasSecondary {
		maxField = 128
	}

	for i := 2; i <= maxField; i++ {
		if !msg.Bitmap[i] {
			continue
		}

		def, ok := ISODictionary[i]
		if !ok {
			return nil, fmt.Errorf("%w: field DE %d has no format definition", ErrInvalidFieldDefinition, i)
		}

		valStr := msg.Fields[i]
		rawVal := msg.RawBits[i]

		switch def.Format {
		case FormatNumericFixed, FormatAlphaFixed:
			buf.WriteString(valStr)

		case FormatLLVARNumeric, FormatLLVARAlpha:
			header := fmt.Sprintf("%02d", len(valStr))
			buf.WriteString(header)
			buf.WriteString(valStr)

		case FormatLLLVARNumeric, FormatLLLVARAlpha:
			header := fmt.Sprintf("%03d", len(valStr))
			buf.WriteString(header)
			buf.WriteString(valStr)

		case FormatBinaryFixed:
			if len(rawVal) != def.Length {
				return nil, fmt.Errorf("%w: fixed binary DE %d invalid length", ErrFieldExceedsLimit, i)
			}
			buf.Write(rawVal)

		case FormatLLLVARBinary:
			header := fmt.Sprintf("%03d", len(rawVal))
			buf.WriteString(header)
			buf.Write(rawVal)
		}
	}

	return buf.Bytes(), nil
}

// Unpack parses raw wire-level ISO 8583 bytes back into a typed ISO8583Message.
func UnpackISO8583(raw []byte) (*ISO8583Message, error) {
	if len(raw) < 12 { // 4 bytes MTI + 8 bytes Primary Bitmap
		return nil, ErrUnpackBufferExhausted
	}

	mti := string(raw[:4])
	msg, err := NewISO8583Message(mti)
	if err != nil {
		return nil, err
	}

	offset := 4

	// Read primary bitmap
	primaryBitmap := raw[offset : offset+8]
	offset += 8

	for i := 1; i <= 64; i++ {
		byteIdx := (i - 1) / 8
		bitIdx := 7 - ((i - 1) % 8)
		if (primaryBitmap[byteIdx] & (1 << bitIdx)) != 0 {
			msg.Bitmap[i] = true
		}
	}

	hasSecondary := msg.Bitmap[1]
	if hasSecondary {
		if len(raw) < offset+8 {
			return nil, ErrUnpackBufferExhausted
		}
		secondaryBitmap := raw[offset : offset+8]
		offset += 8

		for i := 65; i <= 128; i++ {
			byteIdx := (i - 65) / 8
			bitIdx := 7 - ((i - 65) % 8)
			if (secondaryBitmap[byteIdx] & (1 << bitIdx)) != 0 {
				msg.Bitmap[i] = true
			}
		}
	}

	maxField := 64
	if hasSecondary {
		maxField = 128
	}

	// Parse elements
	for i := 2; i <= maxField; i++ {
		if !msg.Bitmap[i] {
			continue
		}

		def, ok := ISODictionary[i]
		if !ok {
			return nil, fmt.Errorf("%w: field DE %d has no unpack rule", ErrInvalidFieldDefinition, i)
		}

		switch def.Format {
		case FormatNumericFixed, FormatAlphaFixed:
			if len(raw) < offset+def.Length {
				return nil, ErrUnpackBufferExhausted
			}
			msg.Fields[i] = string(raw[offset : offset+def.Length])
			offset += def.Length

		case FormatLLVARNumeric, FormatLLVARAlpha:
			if len(raw) < offset+2 {
				return nil, ErrUnpackBufferExhausted
			}
			lenVal, err := strconv.Atoi(string(raw[offset : offset+2]))
			if err != nil {
				return nil, fmt.Errorf("iso8583: invalid LLVAR length header: %w", err)
			}
			offset += 2
			if len(raw) < offset+lenVal {
				return nil, ErrUnpackBufferExhausted
			}
			msg.Fields[i] = string(raw[offset : offset+lenVal])
			offset += lenVal

		case FormatLLLVARNumeric, FormatLLLVARAlpha:
			if len(raw) < offset+3 {
				return nil, ErrUnpackBufferExhausted
			}
			lenVal, err := strconv.Atoi(string(raw[offset : offset+3]))
			if err != nil {
				return nil, fmt.Errorf("iso8583: invalid LLLVAR length header: %w", err)
			}
			offset += 3
			if len(raw) < offset+lenVal {
				return nil, ErrUnpackBufferExhausted
			}
			msg.Fields[i] = string(raw[offset : offset+lenVal])
			offset += lenVal

		case FormatBinaryFixed:
			if len(raw) < offset+def.Length {
				return nil, ErrUnpackBufferExhausted
			}
			binData := raw[offset : offset+def.Length]
			msg.RawBits[i] = append([]byte(nil), binData...)
			msg.Fields[i] = hex.EncodeToString(binData)
			offset += def.Length

		case FormatLLLVARBinary:
			if len(raw) < offset+3 {
				return nil, ErrUnpackBufferExhausted
			}
			lenVal, err := strconv.Atoi(string(raw[offset : offset+3]))
			if err != nil {
				return nil, fmt.Errorf("iso8583: invalid LLLVAR binary header: %w", err)
			}
			offset += 3
			if len(raw) < offset+lenVal {
				return nil, ErrUnpackBufferExhausted
			}
			binData := raw[offset : offset+lenVal]
			msg.RawBits[i] = append([]byte(nil), binData...)
			msg.Fields[i] = hex.EncodeToString(binData)
			offset += lenVal
		}
	}

	return msg, nil
}

// ============================================================================
// EMV BER-TLV (DE 55) DATA PARSER
// ============================================================================

// EMVTLVTag encapsulates a parsed EMV Chip card data tag per EMV Book 3 specification.
type EMVTLVTag struct {
	Tag   string `json:"tag"`   // Hex string identifier (e.g. "9F26", "9F27", "95")
	Value []byte `json:"value"` // Raw byte sequence
}

// ParseEMVTags parses raw BER-TLV bytes from Field 55.
func ParseEMVTags(tlvBytes []byte) ([]EMVTLVTag, error) {
	var tags []EMVTLVTag
	offset := 0
	totalLen := len(tlvBytes)

	for offset < totalLen {
		if tlvBytes[offset] == 0x00 || tlvBytes[offset] == 0xFF {
			offset++
			continue // Skip padding bytes
		}

		// Parse Tag
		firstByte := tlvBytes[offset]
		offset++

		tagHex := fmt.Sprintf("%02X", firstByte)
		// If last 5 bits of first byte are 11111 (0x1F), tag is multi-byte
		if (firstByte & 0x1F) == 0x1F {
			for offset < totalLen {
				b := tlvBytes[offset]
				offset++
				tagHex += fmt.Sprintf("%02X", b)
				if (b & 0x80) == 0 { // Bit 8 is 0 for final tag byte
					break
				}
			}
		}

		if offset >= totalLen {
			return nil, ErrMalformedBERTLV
		}

		// Parse Length
		lenByte := tlvBytes[offset]
		offset++

		var tagLen int
		if (lenByte & 0x80) == 0 {
			// Short form: 1 byte (0 to 127)
			tagLen = int(lenByte)
		} else {
			// Long form: bit 8 is 1, remaining 7 bits encode length of length field
			numLenBytes := int(lenByte & 0x7F)
			if numLenBytes == 0 || offset+numLenBytes > totalLen {
				return nil, ErrMalformedBERTLV
			}
			tagLen = 0
			for b := 0; b < numLenBytes; b++ {
				tagLen = (tagLen << 8) | int(tlvBytes[offset])
				offset++
			}
		}

		if offset+tagLen > totalLen {
			return nil, ErrMalformedBERTLV
		}

		// Read Value
		val := make([]byte, tagLen)
		copy(val, tlvBytes[offset:offset+tagLen])
		offset += tagLen

		tags = append(tags, EMVTLVTag{
			Tag:   tagHex,
			Value: val,
		})
	}

	return tags, nil
}

// EncodeEMVTags encodes an array of EMV tags into standard BER-TLV byte sequence.
func EncodeEMVTags(tags []EMVTLVTag) ([]byte, error) {
	var buf bytes.Buffer
	for _, tag := range tags {
		tagBytes, err := hex.DecodeString(tag.Tag)
		if err != nil {
			return nil, fmt.Errorf("emv: invalid tag hex %q: %w", tag.Tag, err)
		}
		buf.Write(tagBytes)

		valLen := len(tag.Value)
		if valLen < 128 {
			buf.WriteByte(byte(valLen))
		} else if valLen <= 255 {
			buf.WriteByte(0x81)
			buf.WriteByte(byte(valLen))
		} else {
			buf.WriteByte(0x82)
			var lenBuf [2]byte
			binary.BigEndian.PutUint16(lenBuf[:], uint16(valLen))
			buf.Write(lenBuf[:])
		}

		buf.Write(tag.Value)
	}
	return buf.Bytes(), nil
}

// ============================================================================
// COMMERCIAL FLEET / LEVEL 3 PURCHASE ITEMIZATION (DE 62)
// ============================================================================

// Level3PurchaseRecord represents granular itemization for B2B dataset sales.
type Level3PurchaseRecord struct {
	TaxAmount       decimal.Decimal `json:"tax_amount"`
	TaxRate         decimal.Decimal `json:"tax_rate"`
	CustomerRef     string          `json:"customer_ref"`     // Tenant ID / Purchase Order
	DatasetSKU      string          `json:"dataset_sku"`      // Unique dataset catalog identifier
	VolumeGigabytes decimal.Decimal `json:"volume_gigabytes"` // Dataset size billed
	UnitPricePerGB  decimal.Decimal `json:"unit_price_per_gb"`// Price per gigabyte
	CommodityCode   string          `json:"commodity_code"`   // UNSPSC commodity code for data services
}

// FormatLevel3PurchaseData serializes B2B purchasing card Level 3 itemization into Field 62 pipe-delimited syntax.
func FormatLevel3PurchaseData(rec Level3PurchaseRecord) string {
	return fmt.Sprintf("PO:%s|TAX:%s|RATE:%s|SKU:%s|VOL:%s|UP:%s|CC:%s",
		rec.CustomerRef,
		rec.TaxAmount.StringFixed(2),
		rec.TaxRate.StringFixed(4),
		rec.DatasetSKU,
		rec.VolumeGigabytes.StringFixed(2),
		rec.UnitPricePerGB.StringFixed(4),
		rec.CommodityCode,
	)
}

// ============================================================================
// COMMERCIAL CARD TRANSACTION FACTORY & BUILDER
// ============================================================================

// CommercialCardGatewayEngine constructs standard corporate card settlement messages.
type CommercialCardGatewayEngine struct {
	MerchantID       string
	TerminalID       string
	MerchantLocation string
	MCC              string
	MACMasterKey     []byte // 256-bit symmetric key for ISO 8583 MAC verification
}

// NewCommercialCardGatewayEngine instantiates an ISO 8583 interchange builder.
func NewCommercialCardGatewayEngine(merchantID, terminalID, location, mcc string, macKey []byte) *CommercialCardGatewayEngine {
	return &CommercialCardGatewayEngine{
		MerchantID:       merchantID,
		TerminalID:       terminalID,
		MerchantLocation: location,
		MCC:              mcc,
		MACMasterKey:     macKey,
	}
}

// BuildAuthorizationRequest formats a production ISO 8583:1993 MTI 0100 message.
func (eng *CommercialCardGatewayEngine) BuildAuthorizationRequest(
	maskedPAN string,
	amount decimal.Decimal,
	currencyNumeric int,
	stan int,
	rrn string,
	level3 *Level3PurchaseRecord,
) (*ISO8583Message, error) {
	msg, err := NewISO8583Message(MTIAuthorizationRequest)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	// DE 2: PAN
	if err := msg.SetField(2, maskedPAN); err != nil {
		return nil, err
	}

	// DE 3: Processing Code (000000 = Goods and Services Purchase)
	if err := msg.SetField(3, "000000"); err != nil {
		return nil, err
	}

	// DE 4: Amount in smallest subunit (cents), 12-char zero padded
	cents := amount.Mul(decimal.NewFromInt(100)).IntPart()
	if err := msg.SetField(4, fmt.Sprintf("%012d", cents)); err != nil {
		return nil, err
	}

	// DE 7: Transmission Date & Time (MMDDhhmmss)
	if err := msg.SetField(7, now.Format("0102150405")); err != nil {
		return nil, err
	}

	// DE 11: STAN (Systems Trace Audit Number, 6 digits)
	if err := msg.SetField(11, fmt.Sprintf("%06d", stan%1000000)); err != nil {
		return nil, err
	}

	// DE 12: Local Time (hhmmss)
	if err := msg.SetField(12, now.Format("150405")); err != nil {
		return nil, err
	}

	// DE 13: Local Date (MMDD)
	if err := msg.SetField(13, now.Format("0102")); err != nil {
		return nil, err
	}

	// DE 18: Merchant Category Code (MCC)
	if err := msg.SetField(18, eng.MCC); err != nil {
		return nil, err
	}

	// DE 22: POS Entry Mode (010 = Manual e-commerce card entry)
	if err := msg.SetField(22, "010"); err != nil {
		return nil, err
	}

	// DE 25: POS Condition Code (08 = Mail/telephone/e-commerce)
	if err := msg.SetField(25, "08"); err != nil {
		return nil, err
	}

	// DE 37: Retrieval Reference Number (RRN, 12 characters)
	if len(rrn) < 12 {
		rrn = fmt.Sprintf("%012s", rrn)
	} else if len(rrn) > 12 {
		rrn = rrn[:12]
	}
	if err := msg.SetField(37, rrn); err != nil {
		return nil, err
	}

	// DE 41: Terminal ID (8 chars)
	termID := fmt.Sprintf("%-8s", eng.TerminalID)
	if len(termID) > 8 {
		termID = termID[:8]
	}
	if err := msg.SetField(41, termID); err != nil {
		return nil, err
	}

	// DE 42: Card Acceptor ID Code (15 chars)
	mid := fmt.Sprintf("%-15s", eng.MerchantID)
	if len(mid) > 15 {
		mid = mid[:15]
	}
	if err := msg.SetField(42, mid); err != nil {
		return nil, err
	}

	// DE 43: Card Acceptor Name/Location (40 chars)
	loc := fmt.Sprintf("%-40s", eng.MerchantLocation)
	if len(loc) > 40 {
		loc = loc[:40]
	}
	if err := msg.SetField(43, loc); err != nil {
		return nil, err
	}

	// DE 49: Currency Code (e.g. 840 for USD)
	if err := msg.SetField(49, fmt.Sprintf("%03d", currencyNumeric)); err != nil {
		return nil, err
	}

	// DE 62: Level 3 Corporate Purchasing Data
	if level3 != nil {
		formattedL3 := FormatLevel3PurchaseData(*level3)
		if err := msg.SetField(62, formattedL3); err != nil {
			return nil, err
		}
	}

	// DE 64: Calculate HMAC-SHA256 MAC signature
	if len(eng.MACMasterKey) > 0 {
		macBlock, err := eng.ComputeMAC(msg)
		if err != nil {
			return nil, err
		}
		if err := msg.SetBinaryField(64, macBlock); err != nil {
			return nil, err
		}
	}

	return msg, nil
}

// ComputeMAC calculates an 8-byte Message Authentication Code using HMAC-SHA256 across all packed fields up to DE 63.
func (eng *CommercialCardGatewayEngine) ComputeMAC(msg *ISO8583Message) ([]byte, error) {
	msgCopy := *msg
	msgCopy.Bitmap[64] = false
	delete(msgCopy.Fields, 64)
	delete(msgCopy.RawBits, 64)

	packed, err := msgCopy.Pack()
	if err != nil {
		return nil, err
	}

	mac := hmac.New(sha256.New, eng.MACMasterKey)
	mac.Write(packed)
	fullMAC := mac.Sum(nil)

	// Return standard 8-byte truncated MAC
	return fullMAC[:8], nil
}

// VerifyMAC checks incoming response MAC against calculated HMAC-SHA256.
func (eng *CommercialCardGatewayEngine) VerifyMAC(msg *ISO8583Message) (bool, error) {
	expectedMAC, ok := msg.GetBinaryField(64)
	if !ok || len(expectedMAC) != 8 {
		return false, errors.New("iso8583: message has missing or corrupted DE 64 MAC")
	}

	computedMAC, err := eng.ComputeMAC(msg)
	if err != nil {
		return false, err
	}

	return hmac.Equal(expectedMAC, computedMAC), nil
}
