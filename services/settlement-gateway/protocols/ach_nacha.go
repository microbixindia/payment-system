package protocols

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

var (
	ErrInvalidABACheckDigit = errors.New("settlement/ach: ABA routing number failed mod-10 check digit verification")
	ErrInvalidRecordLength  = errors.New("settlement/ach: NACHA record must be exactly 94 characters")
)

// ValidateABARoutingNumber checks 9-digit ABA routing numbers using standard Federal Reserve weights (3, 7, 1).
func ValidateABARoutingNumber(aba string) error {
	if len(aba) != 9 {
		return ErrInvalidABACheckDigit
	}

	weights := []int{3, 7, 1, 3, 7, 1, 3, 7}
	sum := 0
	for i := 0; i < 8; i++ {
		d, err := strconv.Atoi(string(aba[i]))
		if err != nil {
			return ErrInvalidABACheckDigit
		}
		sum += d * weights[i]
	}

	checkDigit, err := strconv.Atoi(string(aba[8]))
	if err != nil {
		return ErrInvalidABACheckDigit
	}

	expectedCheckDigit := (10 - (sum % 10)) % 10
	if checkDigit != expectedCheckDigit {
		return fmt.Errorf("%w: got %d, expected %d", ErrInvalidABACheckDigit, checkDigit, expectedCheckDigit)
	}

	return nil
}

// NACHAFileBuilder generates standard 94-character fixed-width batch files for US ACH settlement.
type NACHAFileBuilder struct {
	immediateDestination string // 10 chars (e.g. " 121000358")
	immediateOrigin      string // 10 chars (e.g. " 123456789")
	destinationName      string // 23 chars
	originName           string // 23 chars
	referenceCode        string // 8 chars
}

// NewNACHAFileBuilder initializes NACHA parameters.
func NewNACHAFileBuilder(immDest, immOrig, destName, origName string) *NACHAFileBuilder {
	return &NACHAFileBuilder{
		immediateDestination: padRight(immDest, 10),
		immediateOrigin:      padRight(immOrig, 10),
		destinationName:      padRight(destName, 23),
		originName:           padRight(origName, 23),
		referenceCode:        "DV000001",
	}
}

// BuildFileHeaderRecord creates Record Type 1 (File Header).
func (b *NACHAFileBuilder) BuildFileHeaderRecord(creationTime time.Time) string {
	dateStr := creationTime.Format("060102") // YYMMDD
	timeStr := creationTime.Format("1504")   // HHMM
	fileIdMod := "A"                         // File ID Modifier

	// 1: Record Type (1)
	// 2-3: Priority Code (01)
	// 4-13: Immediate Destination (10)
	// 14-23: Immediate Origin (10)
	// 24-29: File Creation Date (6)
	// 30-33: File Creation Time (4)
	// 34: File ID Modifier (1)
	// 35-37: Record Size (094)
	// 38-39: Blocking Factor (10)
	// 40: Format Code (1)
	// 41-63: Immediate Destination Name (23)
	// 64-86: Immediate Origin Name (23)
	// 87-94: Reference Code (8)
	rec := fmt.Sprintf("101%s%s%s%s%s094101%s%s%s",
		b.immediateDestination[:10],
		b.immediateOrigin[:10],
		dateStr,
		timeStr,
		fileIdMod,
		b.destinationName[:23],
		b.originName[:23],
		padRight(b.referenceCode, 8)[:8],
	)

	return rec
}

// BuildBatchHeaderRecord creates Record Type 5 (Company/Batch Header).
func (b *NACHAFileBuilder) BuildBatchHeaderRecord(companyName, companyID, secCode, batchDesc string, effectiveDate time.Time, batchNum int) string {
	// 1: Record Type (5)
	// 2-4: Service Class Code (200 = Mixed, 220 = Credits Only, 225 = Debits Only)
	// 5-20: Company Name (16)
	// 21-40: Company Discretionary Data (20)
	// 41-50: Company Identification (10)
	// 51-53: Standard Entry Class Code (SEC - CCD, PPD, CTX) (3)
	// 54-63: Company Entry Description (10)
	// 64-69: Company Descriptive Date (6)
	// 70-75: Effective Entry Date (6)
	// 76-78: Settlement Date (Julian, leave blank for ODFI) (3)
	// 79: Originator Status Code (1)
	// 80-87: Originating DFI ID (8)
	// 88-94: Batch Number (7)
	rec := fmt.Sprintf("5200%s%s%s%s%s%s%s   112100035%07d",
		padRight(companyName, 16)[:16],
		padRight("", 20)[:20],
		padRight(companyID, 10)[:10],
		padRight(secCode, 3)[:3],
		padRight(batchDesc, 10)[:10],
		effectiveDate.Format("060102"),
		effectiveDate.Format("060102"),
		batchNum,
	)
	return rec
}

// BuildEntryDetailRecord creates Record Type 6 (Entry Detail CCD/PPD).
func (b *NACHAFileBuilder) BuildEntryDetailRecord(
	txCode string, // "22" = Checking Credit, "27" = Checking Debit
	routingNumber string, // 8 digits routing + 1 digit check digit
	accountNumber string,
	amount decimal.Decimal,
	individualID string,
	individualName string,
	traceNumber int64,
) (string, error) {
	if err := ValidateABARoutingNumber(routingNumber); err != nil {
		return "", err
	}

	cents := amount.Mul(decimal.NewFromInt(100)).IntPart()
	amountStr := fmt.Sprintf("%010d", cents)

	// 1: Record Type (6)
	// 2-3: Transaction Code (2)
	// 4-11: Receiving DFI Identification (8)
	// 12: Check Digit (1)
	// 13-29: DFI Account Number (17)
	// 30-39: Amount (10)
	// 40-54: Individual Identification Number (15)
	// 55-76: Individual/Company Name (22)
	// 77-78: Discretionary Data (2)
	// 79: Addenda Record Indicator (0 = No, 1 = Yes)
	// 80-94: Trace Number (15)
	rec := fmt.Sprintf("6%s%s%s%s%s%s%s  0%015d",
		txCode[:2],
		routingNumber[:8],
		string(routingNumber[8]),
		padRight(accountNumber, 17)[:17],
		amountStr,
		padRight(individualID, 15)[:15],
		padRight(individualName, 22)[:22],
		traceNumber,
	)

	return rec, nil
}

func padRight(str string, length int) string {
	if len(str) >= length {
		return str[:length]
	}
	return str + strings.Repeat(" ", length-len(str))
}
