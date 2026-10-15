"""
DataVendor Enterprise Client-Side Field Encryption (Python)
Implements AES-256-GCM field encryption with 96-bit CSPRNG nonces
matching the Go commoncrypto engine format (dv1$v1$nonce$tag$ciphertext).
"""

import os
from typing import Optional, Tuple
from cryptography.hazmat.primitives.ciphers.aead import AESGCM


class ClientFieldCipher:
    def __init__(self, master_key_hex: str):
        key_bytes = bytes.fromhex(master_key_hex)
        if len(key_bytes) != 32:
            raise ValueError("ClientFieldCipher: key must be exactly 32 bytes (256 bits)")
        self._key = key_bytes
        self._aesgcm = AESGCM(self._key)

    def encrypt_field(self, plaintext: str, aad: Optional[str] = None) -> str:
        nonce = os.urandom(12) # 96-bit NIST standard
        aad_bytes = aad.encode("utf-8") if aad else b""
        data_bytes = plaintext.encode("utf-8")

        # AESGCM.encrypt appends 16-byte tag to the ciphertext
        sealed = self._aesgcm.encrypt(nonce, data_bytes, aad_bytes)
        ciphertext = sealed[:-16]
        tag = sealed[-16:]

        return f"dv1$v1${nonce.hex()}${tag.hex()}${ciphertext.hex()}"

    def decrypt_field(self, serialized: str, aad: Optional[str] = None) -> str:
        parts = serialized.split("$")
        if len(parts) != 5 or parts[0] != "dv1":
            raise ValueError("ClientFieldCipher: malformed or unsupported ciphertext format")

        nonce = bytes.fromhex(parts[2])
        tag = bytes.fromhex(parts[3])
        ciphertext = bytes.fromhex(parts[4])
        aad_bytes = aad.encode("utf-8") if aad else b""

        sealed = ciphertext + tag
        decrypted = self._aesgcm.decrypt(nonce, sealed, aad_bytes)
        return decrypted.decode("utf-8")
