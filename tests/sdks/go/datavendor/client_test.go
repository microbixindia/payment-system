package datavendor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	commoncrypto "datavendor-payment-core/libs/common-crypto"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestGoSDK_ClientCreationAndFieldEncryption(t *testing.T) {
	encKey := make([]byte, 32)
	rand.Read(encKey)

	cfg := Config{
		BaseURL:             "https://api.datavendor.io",
		APIKey:              "dv_live_test_api_key_8833",
		APISecret:           "dv_secret_key_super_classified_9911",
		ClientEncryptionKey: encKey,
	}

	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("failed creating SDK client: %v", err)
	}

	pan := "4111222233334444"
	tenantID := "tenant-enterprise-alpha"

	encrypted, err := client.EncryptCardData(pan, tenantID)
	if err != nil {
		t.Fatalf("client-side card encryption failed: %v", err)
	}

	if len(encrypted.EncryptedPAN) == 0 || len(encrypted.AuthTag) == 0 || len(encrypted.Nonce) == 0 {
		t.Fatalf("encrypted card payload contains empty fields")
	}

	// Verify server-side decryptability
	cipher, err := commoncrypto.NewFieldCipher(encKey)
	if err != nil {
		t.Fatalf("failed creating decryptor cipher: %v", err)
	}

	cipherBytes, _ := hex.DecodeString(encrypted.EncryptedPAN)
	tagBytes, _ := hex.DecodeString(encrypted.AuthTag)
	nonceBytes, _ := hex.DecodeString(encrypted.Nonce)
	aad := []byte("DATAVENDOR_TENANT:" + tenantID)

	payload := &commoncrypto.EncryptedPayload{
		Version:    "v1",
		KeyVersion: "v1",
		Nonce:      nonceBytes,
		AuthTag:    tagBytes,
		Ciphertext: cipherBytes,
	}

	decrypted, err := cipher.DecryptField(payload, aad)
	if err != nil {
		t.Fatalf("server-side decryption of client encrypted field failed: %v", err)
	}

	if string(decrypted) != pan {
		t.Errorf("decrypted PAN mismatch: expected %s, got %s", pan, string(decrypted))
	}
}

func TestGoSDK_IngestTelemetryWithHMACSigning(t *testing.T) {
	encKey := make([]byte, 32)
	rand.Read(encKey)

	apiKey := "dv_live_key_test"
	apiSecret := "dv_secret_test_99"

	// Mock HTTP server to capture headers and body
	var capturedKey, capturedSig, capturedTimestamp, capturedNonce string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedKey = r.Header.Get("X-DataVendor-Key")
		capturedSig = r.Header.Get("X-DataVendor-Signature")
		capturedTimestamp = r.Header.Get("X-DataVendor-Timestamp")
		capturedNonce = r.Header.Get("X-DataVendor-Nonce")

		resp := TelemetryEventResponse{
			Status:    "ENQUEUED",
			EventID:   "evt-9911-test",
			Partition: 2,
			Offset:    104928,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := Config{
		BaseURL:             server.URL,
		APIKey:              apiKey,
		APISecret:           apiSecret,
		ClientEncryptionKey: encKey,
	}

	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("failed initializing client: %v", err)
	}

	event := TelemetryEventRequest{
		EventID:       uuid.New(),
		TenantID:      "tenant-ai-101",
		DatasetID:     "ds-corpus-v2",
		DatasetSKU:    "CORPUS-V2-FIN",
		SizeBytes:     10737418240, // 10 GB
		Gigabytes:     decimal.NewFromInt(10),
		OperationType: "DATASET_DOWNLOAD",
		Timestamp:     time.Now().UTC(),
	}

	res, err := client.IngestTelemetry(context.Background(), event)
	if err != nil {
		t.Fatalf("IngestTelemetry failed: %v", err)
	}

	if res.Status != "ENQUEUED" || res.Partition != 2 {
		t.Errorf("unexpected telemetry response: %+v", res)
	}

	if capturedKey != apiKey {
		t.Errorf("captured API key mismatch: %s", capturedKey)
	}
	if len(capturedSig) != 64 { // SHA256 hex is 64 chars
		t.Errorf("expected 64-char HMAC signature, got %d", len(capturedSig))
	}
	if len(capturedTimestamp) == 0 || len(capturedNonce) == 0 {
		t.Errorf("missing replay defense timestamp or nonce headers")
	}
}
