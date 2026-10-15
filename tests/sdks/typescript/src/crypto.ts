import * as crypto from 'crypto';

export interface EncryptedFieldResult {
  version: string;
  keyVersion: string;
  nonceHex: string;
  authTagHex: string;
  ciphertextHex: string;
  serialized: string;
}

/**
 * Enterprise client-side AES-256-GCM field encryption.
 * Protects sensitive financial data (account numbers, tax IDs, PII)
 * on the client before network transmission to DataVendor Gateway.
 */
export class ClientFieldCipher {
  private readonly masterKey: Buffer;

  constructor(masterKeyHex: string) {
    const keyBuf = Buffer.from(masterKeyHex, 'hex');
    if (keyBuf.length !== 32) {
      throw new Error('ClientFieldCipher: key must be exactly 32 bytes (256 bits)');
    }
    this.masterKey = keyBuf;
  }

  public encryptField(plaintext: string, aad?: string): EncryptedFieldResult {
    const iv = crypto.randomBytes(12); // 96-bit NIST nonce
    const cipher = crypto.createCipheriv('aes-256-gcm', this.masterKey, iv);

    if (aad) {
      cipher.setAAD(Buffer.from(aad, 'utf8'));
    }

    let encrypted = cipher.update(plaintext, 'utf8', 'hex');
    encrypted += cipher.final('hex');

    const authTag = cipher.getAuthTag();
    const nonceHex = iv.toString('hex');
    const authTagHex = authTag.toString('hex');
    const serialized = `dv1$v1$${nonceHex}$${authTagHex}$${encrypted}`;

    return {
      version: 'dv1',
      keyVersion: 'v1',
      nonceHex,
      authTagHex,
      ciphertextHex: encrypted,
      serialized,
    };
  }

  public decryptField(serialized: string, aad?: string): string {
    const parts = serialized.split('$');
    if (parts.length !== 5 || parts[0] !== 'dv1') {
      throw new Error('ClientFieldCipher: malformed or unsupported ciphertext container');
    }

    const nonce = Buffer.from(parts[2], 'hex');
    const authTag = Buffer.from(parts[3], 'hex');
    const ciphertext = Buffer.from(parts[4], 'hex');

    const decipher = crypto.createDecipheriv('aes-256-gcm', this.masterKey, nonce);
    decipher.setAuthTag(authTag);

    if (aad) {
      decipher.setAAD(Buffer.from(aad, 'utf8'));
    }

    let decrypted = decipher.update(ciphertext, undefined, 'utf8');
    decrypted += decipher.final('utf8');

    return decrypted;
  }
}
