use aes_gcm::{
    aead::{Aead, KeyInit, Payload},
    Aes256Gcm, Nonce, Tag,
};
use rand::{rngs::OsRng, RngCore};
use serde::{Deserialize, Serialize};
use thiserror::Error;
use zeroize::{Zeroize, ZeroizeOnDrop};

pub const AES256_KEY_SIZE: usize = 32;
pub const GCM_NONCE_SIZE: usize = 12;
pub const GCM_TAG_SIZE: usize = 16;

#[derive(Error, Debug)]
pub enum CryptoError {
    #[error("Invalid key size: expected 32 bytes (AES-256), got {0}")]
    InvalidKeySize(usize),
    #[error("Entropy generation failed")]
    EntropyFailure,
    #[error("Encryption failed: {0}")]
    EncryptionFailed(String),
    #[error("Decryption authentication failed (tampered ciphertext or invalid AAD)")]
    AuthenticationFailed,
    #[error("Hex encoding/decoding failed: {0}")]
    HexDecodeError(#[from] hex::FromHexError),
}

/// EncryptedPayload holds the serialized components of field-level encryption.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct EncryptedFieldPayload {
    pub version: String,
    pub key_id: String,
    pub nonce_hex: String,
    pub tag_hex: String,
    pub ciphertext_hex: String,
}

/// FieldCipher wraps 256-bit AES-GCM operations with automatic memory zeroization.
#[derive(Zeroize, ZeroizeOnDrop)]
pub struct FieldCipher {
    key: [u8; AES256_KEY_SIZE],
}

impl FieldCipher {
    /// Constructs a cipher instance from a 32-byte secret slice.
    pub fn new(secret: &[u8]) -> Result<Self, CryptoError> {
        if secret.len() != AES256_KEY_SIZE {
            return Err(CryptoError::InvalidKeySize(secret.len()));
        }
        let mut key = [0u8; AES256_KEY_SIZE];
        key.copy_from_slice(secret);
        Ok(Self { key })
    }

    /// Encrypts plaintext bytes with AES-256-GCM, generating a fresh 96-bit CSPRNG nonce,
    /// and cryptographically binds additional authenticated data (AAD).
    pub fn encrypt(&self, plaintext: &[u8], aad: &[u8]) -> Result<EncryptedFieldPayload, CryptoError> {
        let cipher = Aes256Gcm::new_from_slice(&self.key)
            .map_err(|e| CryptoError::EncryptionFailed(e.to_string()))?;

        let mut nonce_bytes = [0u8; GCM_NONCE_SIZE];
        OsRng.fill_bytes(&mut nonce_bytes);
        let nonce = Nonce::from_slice(&nonce_bytes);

        let payload = Payload {
            msg: plaintext,
            aad,
        };

        let ciphertext_with_tag = cipher
            .encrypt(nonce, payload)
            .map_err(|e| CryptoError::EncryptionFailed(e.to_string()))?;

        if ciphertext_with_tag.len() < GCM_TAG_SIZE {
            return Err(CryptoError::EncryptionFailed("Invalid ciphertext output".into()));
        }

        let tag_offset = ciphertext_with_tag.len() - GCM_TAG_SIZE;
        let ciphertext = &ciphertext_with_tag[..tag_offset];
        let tag = &ciphertext_with_tag[tag_offset..];

        Ok(EncryptedFieldPayload {
            version: "v1".to_string(),
            key_id: "CLIENT_DEK_RUST_V1".to_string(),
            nonce_hex: hex::encode(nonce_bytes),
            tag_hex: hex::encode(tag),
            ciphertext_hex: hex::encode(ciphertext),
        })
    }

    /// Decrypts an EncryptedFieldPayload, verifying the authentication tag and AAD.
    pub fn decrypt(&self, payload: &EncryptedFieldPayload, aad: &[u8]) -> Result<Vec<u8>, CryptoError> {
        let cipher = Aes256Gcm::new_from_slice(&self.key)
            .map_err(|e| CryptoError::EncryptionFailed(e.to_string()))?;

        let nonce_bytes = hex::decode(&payload.nonce_hex)?;
        let tag_bytes = hex::decode(&payload.tag_hex)?;
        let ciphertext_bytes = hex::decode(&payload.ciphertext_hex)?;

        if nonce_bytes.len() != GCM_NONCE_SIZE || tag_bytes.len() != GCM_TAG_SIZE {
            return Err(CryptoError::AuthenticationFailed);
        }

        let nonce = Nonce::from_slice(&nonce_bytes);

        // Reassemble ciphertext + tag for Aes256Gcm decrypt
        let mut combined = Vec::with_capacity(ciphertext_bytes.len() + tag_bytes.len());
        combined.extend_from_slice(&ciphertext_bytes);
        combined.extend_from_slice(&tag_bytes);

        let gcm_payload = Payload {
            msg: &combined,
            aad,
        };

        cipher
            .decrypt(nonce, gcm_payload)
            .map_err(|_| CryptoError::AuthenticationFailed)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_field_cipher_roundtrip() {
        let mut key = [0u8; 32];
        OsRng.fill_bytes(&mut key);

        let cipher = FieldCipher::new(&key).expect("cipher init");
        let secret_data = b"CORPORATE_PURCHASE_CARD_4111222233334444";
        let tenant_aad = b"TENANT_UUID_991188";

        let encrypted = cipher.encrypt(secret_data, tenant_aad).expect("encryption");
        let decrypted = cipher.decrypt(&encrypted, tenant_aad).expect("decryption");

        assert_eq!(secret_data.to_vec(), decrypted);
    }

    #[test]
    fn test_tamper_detection() {
        let mut key = [0u8; 32];
        OsRng.fill_bytes(&mut key);

        let cipher = FieldCipher::new(&key).expect("cipher init");
        let secret_data = b"SENSITIVE_SETTLEMENT_RECORD";
        let tenant_aad = b"TENANT_UUID_991188";

        let mut encrypted = cipher.encrypt(secret_data, tenant_aad).expect("encryption");

        // Tamper ciphertext
        let mut raw_cipher = hex::decode(&encrypted.ciphertext_hex).unwrap();
        raw_cipher[0] ^= 0xFF;
        encrypted.ciphertext_hex = hex::encode(raw_cipher);

        let res = cipher.decrypt(&encrypted, tenant_aad);
        assert!(res.is_err());
    }
}
