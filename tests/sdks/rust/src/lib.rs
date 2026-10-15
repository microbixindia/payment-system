//! DataVendor Official Rust Client SDK
//!
//! Enterprise-grade, high-throughput, hyper-secure Rust SDK for the DataVendor Payment Core.
//! Enforces 256-bit AES-GCM field-level zero-trust encryption, HMAC-SHA256 replay defense,
//! and native telemetry pipeline integration.

pub mod client;
pub mod crypto;

pub use client::{ClientConfig, DataVendorClient, TelemetryEvent, TelemetryResponse};
pub use crypto::{EncryptedFieldPayload, FieldCipher};
