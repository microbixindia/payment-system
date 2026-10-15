package protocols

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestNACHAFileBuilder(t *testing.T) {
	builder := NewNACHAFileBuilder("121000358", "021000021", "WELLS FARGO", "DATAVENDOR CORP")

	now := time.Now().UTC()
	headerRec := builder.BuildFileHeaderRecord(now)

	// In NACHA specifications, every line must be strictly 94 characters
	if len(headerRec) != 94 {
		t.Fatalf("file header record length must be 94, got %d: %q", len(headerRec), headerRec)
	}

	batchRec := builder.BuildBatchHeaderRecord("DATAVENDOR CORP", "1234567890", "CCD", "DATA SALES", now, 1)
	if len(batchRec) != 94 {
		t.Fatalf("batch header record length must be 94, got %d: %q", len(batchRec), batchRec)
	}

	amount, _ := decimal.NewFromString("4500.50")
	entryRec, err := builder.BuildEntryDetailRecord(
		"22",
		"121000358", // Valid Wells Fargo ABA
		"987654321",
		amount,
		"CORP-CLI-01",
		"Acme AI Research",
		1,
	)
	if err != nil {
		t.Fatalf("failed to build entry detail: %v", err)
	}

	if len(entryRec) != 94 {
		t.Fatalf("entry detail record length must be 94, got %d: %q", len(entryRec), entryRec)
	}

	// Test invalid ABA routing number rejection
	_, err = builder.BuildEntryDetailRecord(
		"22",
		"121000359", // Invalid check digit (should be 8, not 9)
		"987654321",
		amount,
		"CORP-CLI-01",
		"Acme AI Research",
		2,
	)
	if err == nil {
		t.Fatalf("expected error on invalid ABA check digit, got nil")
	}
}
