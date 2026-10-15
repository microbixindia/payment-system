use crate::crypto::{CryptoError, FieldCipher};
use hmac::{Hmac, Mac};
use reqwest::header::{HeaderMap, HeaderValue, ACCEPT, CONTENT_TYPE};
use serde::{Deserialize, Serialize};
use sha2::Sha256;
use std::time::Duration;
use thiserror::Error;
use uuid::Uuid;

type HmacSha256 = Hmac<Sha256>;

#[derive(Error, Debug)]
pub enum ClientError {
    #[error("HTTP transport error: {0}")]
    HttpError(#[from] reqwest::Error),
    #[error("Cryptography error: {0}")]
    CryptoError(#[from] CryptoError),
    #[error("Gateway returned error {status}: {body}")]
    GatewayError { status: u16, body: String },
    #[error("Serialization error: {0}")]
    SerializationError(#[from] serde_json::Error),
    #[error("Invalid configuration: {0}")]
    ConfigError(String),
}

/// Client configuration for connecting to DataVendor payment infrastructure.
#[derive(Clone, Debug)]
pub struct ClientConfig {
    pub base_url: String,
    pub api_key: String,
    pub api_secret: String,
    pub client_encryption_key: [u8; 32],
    pub timeout: Duration,
}

/// DataVendor High-Throughput Payment Client (Rust SDK).
pub struct DataVendorClient {
    config: ClientConfig,
    http: reqwest::Client,
    cipher: FieldCipher,
}

/// Inbound telemetry consumption payload for dataset sales.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TelemetryEvent {
    pub event_id: Uuid,
    pub tenant_id: String,
    pub dataset_id: String,
    pub dataset_sku: String,
    pub size_bytes: i64,
    pub gigabytes: String,
    pub operation_type: String,
    pub timestamp_utc: String,
}

/// Upstream response after processing consumption event.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TelemetryResponse {
    pub status: String,
    pub event_id: String,
    pub partition: i32,
    pub offset: i64,
    pub quota_alert: Option<String>,
}

impl DataVendorClient {
    /// Initializes a production SDK client with connection pooling and cryptographic ciphers.
    pub fn new(config: ClientConfig) -> Result<Self, ClientError> {
        let http = reqwest::Client::builder()
            .timeout(config.timeout)
            .tcp_keepalive(Some(Duration::from_secs(30)))
            .pool_idle_timeout(Some(Duration::from_secs(90)))
            .pool_max_idle_per_host(50)
            .build()?;

        let cipher = FieldCipher::new(&config.client_encryption_key)?;

        Ok(Self {
            config,
            http,
            cipher,
        })
    }

    /// Access the underlying field cipher for client-side field encryption.
    pub fn cipher(&self) -> &FieldCipher {
        &self.cipher
    }

    /// Ingests a high-velocity dataset consumption telemetry event.
    pub async fn ingest_telemetry(&self, event: &TelemetryEvent) -> Result<TelemetryResponse, ClientError> {
        let body = serde_json::to_vec(event)?;
        let path = "/v1/telemetry/events";
        let resp_bytes = self.execute_request("POST", path, &body).await?;
        let resp: TelemetryResponse = serde_json::from_slice(&resp_bytes)?;
        Ok(resp)
    }

    /// Signs and dispatches an authenticated HTTPS request with HMAC-SHA256 and replay resistance.
    async fn execute_request(&self, method: &str, path: &str, body: &[u8]) -> Result<Vec<u8>, ClientError> {
        let url = format!("{}{}", self.config.base_url, path);
        let timestamp = chrono::Utc::now().timestamp().to_string();
        let nonce = Uuid::new_v4().to_string();

        // Calculate HMAC-SHA256 signature: method + path + timestamp + nonce + body
        let mut mac = HmacSha256::new_from_slice(self.config.api_secret.as_bytes())
            .map_err(|e| ClientError::ConfigError(e.to_string()))?;
        mac.update(method.as_bytes());
        mac.update(path.as_bytes());
        mac.update(timestamp.as_bytes());
        mac.update(nonce.as_bytes());
        mac.update(body);
        let signature_hex = hex::encode(mac.finalize().into_bytes());

        let mut headers = HeaderMap::new();
        headers.insert(CONTENT_TYPE, HeaderValue::from_static("application/json"));
        headers.insert(ACCEPT, HeaderValue::from_static("application/json"));
        headers.insert("X-DataVendor-Key", HeaderValue::from_str(&self.config.api_key).unwrap());
        headers.insert("X-DataVendor-Timestamp", HeaderValue::from_str(&timestamp).unwrap());
        headers.insert("X-DataVendor-Nonce", HeaderValue::from_str(&nonce).unwrap());
        headers.insert("X-DataVendor-Signature", HeaderValue::from_str(&signature_hex).unwrap());

        let req = match method {
            "POST" => self.http.post(&url).headers(headers).body(body.to_vec()),
            "GET" => self.http.get(&url).headers(headers),
            _ => return Err(ClientError::ConfigError(format!("Unsupported method {}", method))),
        };

        let response = req.send().await?;
        let status = response.status();
        let bytes = response.bytes().await?.to_vec();

        if !status.is_success() {
            return Err(ClientError::GatewayError {
                status: status.as_u16(),
                body: String::from_utf8_lossy(&bytes).to_string(),
            });
        }

        Ok(bytes)
    }
}
