package datavendor

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	commoncrypto "datavendor-payment-core/libs/common-crypto"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

var (
	ErrClientUnauthenticated = errors.New("datavendor/client: missing API key or client credentials")
	ErrInvalidEncryptionKey  = errors.New("datavendor/client: client-side encryption key must be 32 bytes (AES-256)")
	ErrRequestTimeout        = errors.New("datavendor/client: upstream request exceeded timeout deadline")
	ErrAPIResponseError      = errors.New("datavendor/client: gateway returned non-2xx status code")
)

// Config defines connection, authentication, and client-side encryption settings.
type Config struct {
	BaseURL               string
	APIKey                string
	APISecret             string
	ClientEncryptionKey   []byte        // 32-byte AES-256 key for zero-trust client-side encryption
	Timeout               time.Duration
	MaxIdleConns          int
	IdleConnTimeout       time.Duration
	TLSHandshakeTimeout   time.Duration
	ExpectContinueTimeout time.Duration
}

// Client provides the enterprise Go SDK for DataVendor's payment gateway.
type Client struct {
	cfg        Config
	httpClient *http.Client
	cipher     *commoncrypto.FieldCipher
	mu         sync.RWMutex
}

// NewClient instantiates a connection-pooled, zero-trust DataVendor SDK client.
func NewClient(cfg Config) (*Client, error) {
	if cfg.APIKey == "" || cfg.APISecret == "" {
		return nil, ErrClientUnauthenticated
	}
	if len(cfg.ClientEncryptionKey) != commoncrypto.AES256KeySize {
		return nil, ErrInvalidEncryptionKey
	}

	cipher, err := commoncrypto.NewFieldCipher(cfg.ClientEncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("datavendor/client: cipher initialization failed: %w", err)
	}

	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.MaxIdleConns == 0 {
		cfg.MaxIdleConns = 100
	}
	if cfg.IdleConnTimeout == 0 {
		cfg.IdleConnTimeout = 90 * time.Second
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          cfg.MaxIdleConns,
		MaxIdleConnsPerHost:   cfg.MaxIdleConns,
		IdleConnTimeout:       cfg.IdleConnTimeout,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}

	return &Client{
		cfg:    cfg,
		cipher: cipher,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   cfg.Timeout,
		},
	}, nil
}

// TelemetryEventRequest encapsulates usage events emitted from dataset pipelines.
type TelemetryEventRequest struct {
	EventID       uuid.UUID       `json:"event_id"`
	TenantID      string          `json:"tenant_id"`
	DatasetID     string          `json:"dataset_id"`
	DatasetSKU    string          `json:"dataset_sku"`
	SizeBytes     int64           `json:"size_bytes"`
	Gigabytes     decimal.Decimal `json:"gigabytes"`
	OperationType string          `json:"operation_type"`
	Timestamp     time.Time       `json:"timestamp"`
}

// TelemetryEventResponse indicates receipt and pipeline enqueue status.
type TelemetryEventResponse struct {
	Status        string `json:"status"`
	EventID       string `json:"event_id"`
	Partition     int32  `json:"partition"`
	Offset        int64  `json:"offset"`
	QuotaAlert    string `json:"quota_alert,omitempty"`
}

// IngestTelemetry reports metered data consumption with client-side timestamping.
func (c *Client) IngestTelemetry(ctx context.Context, req TelemetryEventRequest) (*TelemetryEventResponse, error) {
	if req.EventID == uuid.Nil {
		req.EventID = uuid.New()
	}
	if req.Timestamp.IsZero() {
		req.Timestamp = time.Now().UTC()
	}

	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("datavendor/client: marshal failed: %w", err)
	}

	var resp TelemetryEventResponse
	if err := c.doRequest(ctx, http.MethodPost, "/v1/telemetry/events", bodyBytes, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}

// EncryptedCardPayload holds cardholder data encrypted client-side prior to network transit.
type EncryptedCardPayload struct {
	EncryptedPAN  string `json:"encrypted_pan"`
	AuthTag       string `json:"auth_tag"`
	Nonce         string `json:"nonce"`
	KeyID         string `json:"key_id"`
	CardHolderName string `json:"cardholder_name"`
	ExpiryMMYY    string `json:"expiry_mmyy"`
}

// EncryptCardData wraps sensitive PAN data in AES-256-GCM client-side before submission.
func (c *Client) EncryptCardData(pan, tenantID string) (*EncryptedCardPayload, error) {
	// Context binding via AAD
	aad := []byte("DATAVENDOR_TENANT:" + tenantID)
	payload, err := c.cipher.EncryptField([]byte(pan), aad)
	if err != nil {
		return nil, fmt.Errorf("datavendor/client: field encryption failed: %w", err)
	}

	return &EncryptedCardPayload{
		EncryptedPAN: hex.EncodeToString(payload.Ciphertext),
		AuthTag:      hex.EncodeToString(payload.AuthTag),
		Nonce:        hex.EncodeToString(payload.Nonce),
		KeyID:        "CLIENT_DEK_V1",
	}, nil
}

// doRequest signs and executes an authenticated HTTPS call with replay defense.
func (c *Client) doRequest(ctx context.Context, method, path string, body []byte, result interface{}) error {
	fullURL := c.cfg.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, method, fullURL, bytes.NewReader(body))
	if err != nil {
		return err
	}

	timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	nonce := uuid.New().String()

	// Compute HMAC-SHA256 signature across method + path + timestamp + nonce + body
	signer := hmac.New(sha256.New, []byte(c.cfg.APISecret))
	signer.Write([]byte(method))
	signer.Write([]byte(path))
	signer.Write([]byte(timestamp))
	signer.Write([]byte(nonce))
	signer.Write(body)
	sigHex := hex.EncodeToString(signer.Sum(nil))

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-DataVendor-Key", c.cfg.APIKey)
	req.Header.Set("X-DataVendor-Timestamp", timestamp)
	req.Header.Set("X-DataVendor-Nonce", nonce)
	req.Header.Set("X-DataVendor-Signature", sigHex)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	respBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("%w: status %d: %s", ErrAPIResponseError, res.StatusCode, string(respBytes))
	}

	if result != nil && len(respBytes) > 0 {
		if err := json.Unmarshal(respBytes, result); err != nil {
			return fmt.Errorf("datavendor/client: unmarshal error: %w", err)
		}
	}

	return nil
}
