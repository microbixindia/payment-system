import { ClientFieldCipher } from './crypto';

export interface DataVendorClientConfig {
  baseUrl: string;
  apiKey: string;
  fieldEncryptionKeyHex?: string;
  timeoutMs?: number;
}

export interface PostJournalEntryParams {
  ledgerId: string;
  idempotencyKey: string;
  correlationId: string;
  currency: string;
  description: string;
  postings: Array<{
    accountId: string;
    direction: 'DEBIT' | 'CREDIT';
    amount: string; // "12500.5000"
    description?: string;
  }>;
}

export interface SettlementInstructionParams {
  amount: string;
  currency: string;
  urgency: 'INSTANT' | 'SAME_DAY' | 'STANDARD';
  debtorName: string;
  debtorAccount: string;
  debtorRouting: string;
  creditorName: string;
  creditorAccount: string;
  creditorRouting: string;
  memo: string;
}

/**
 * DataVendor Enterprise B2B Payment Gateway Official TypeScript SDK.
 */
export class DataVendorClient {
  private readonly baseUrl: string;
  private readonly apiKey: string;
  private readonly cipher?: ClientFieldCipher;
  private readonly timeoutMs: number;

  constructor(config: DataVendorClientConfig) {
    this.baseUrl = config.baseUrl.replace(/\/$/, '');
    this.apiKey = config.apiKey;
    this.timeoutMs = config.timeoutMs ?? 10000;

    if (config.fieldEncryptionKeyHex) {
      this.cipher = new ClientFieldCipher(config.fieldEncryptionKeyHex);
    }
  }

  /**
   * Post double-entry journal entry to the Core Ledger Engine.
   */
  public async postJournalEntry(params: PostJournalEntryParams): Promise<any> {
    const url = `${this.baseUrl}/api/v1/ledger/postings`;
    const res = await fetch(url, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${this.apiKey}`,
      },
      body: JSON.stringify({
        ledger_id: params.ledgerId,
        idempotency_key: params.idempotencyKey,
        correlation_id: params.correlationId,
        currency: params.currency,
        description: params.description,
        postings: params.postings.map((p) => ({
          account_id: p.accountId,
          direction: p.direction,
          amount: p.amount,
          description: p.description ?? '',
        })),
      }),
    });

    if (!res.ok) {
      const errText = await res.text();
      throw new Error(`DataVendor Ledger Error [HTTP ${res.status}]: ${errText}`);
    }

    return res.json();
  }

  /**
   * Execute multi-rail banking settlement with automatic field-level encryption of bank credentials.
   */
  public async executeSettlement(params: SettlementInstructionParams): Promise<any> {
    const url = `${this.baseUrl}/api/v1/settlement/execute`;

    // Optionally encrypt account numbers client-side before dispatch
    let debtorAcct = params.debtorAccount;
    let creditorAcct = params.creditorAccount;

    if (this.cipher) {
      debtorAcct = this.cipher.encryptField(params.debtorAccount, 'debtor-bank-acct').serialized;
      creditorAcct = this.cipher.encryptField(params.creditorAccount, 'creditor-bank-acct').serialized;
    }

    const res = await fetch(url, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${this.apiKey}`,
      },
      body: JSON.stringify({
        amount: params.amount,
        currency: params.currency,
        urgency: params.urgency,
        debtor_name: params.debtorName,
        debtor_account: debtorAcct,
        debtor_routing: params.debtorRouting,
        creditor_name: params.creditorName,
        creditor_account: creditorAcct,
        creditor_routing: params.creditorRouting,
        memo: params.memo,
      }),
    });

    if (!res.ok) {
      const errText = await res.text();
      throw new Error(`DataVendor Settlement Error [HTTP ${res.status}]: ${errText}`);
    }

    return res.json();
  }
}
