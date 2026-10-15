"""
DataVendor Enterprise B2B Payment Gateway Official Python SDK.
High-throughput transactional ledger client and multi-rail settlement executor.
"""

from typing import Dict, Any, List, Optional
import requests
from .crypto import ClientFieldCipher


class DataVendorClient:
    def __init__(
        self,
        base_url: str,
        api_key: str,
        field_encryption_key_hex: Optional[str] = None,
        timeout: int = 15,
    ):
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.timeout = timeout
        self.session = requests.Session()
        self.session.headers.update({
            "Authorization": f"Bearer {api_key}",
            "Content-Type": "application/json",
            "User-Agent": "DataVendor-Python-SDK/1.0.0",
        })
        self.cipher = ClientFieldCipher(field_encryption_key_hex) if field_encryption_key_hex else None

    def post_journal_entry(
        self,
        ledger_id: str,
        idempotency_key: str,
        correlation_id: str,
        currency: str,
        description: str,
        postings: List[Dict[str, Any]],
    ) -> Dict[str, Any]:
        """Post a GAAP-compliant balanced double-entry transaction to the Core Ledger."""
        payload = {
            "ledger_id": ledger_id,
            "idempotency_key": idempotency_key,
            "correlation_id": correlation_id,
            "currency": currency,
            "description": description,
            "postings": postings,
        }
        resp = self.session.post(
            f"{self.base_url}/api/v1/ledger/postings",
            json=payload,
            timeout=self.timeout,
        )
        resp.raise_for_status()
        return resp.json()

    def execute_settlement(
        self,
        amount: str,
        currency: str,
        urgency: str,
        debtor_name: str,
        debtor_account: str,
        debtor_routing: str,
        creditor_name: str,
        creditor_account: str,
        creditor_routing: str,
        memo: str,
    ) -> Dict[str, Any]:
        """Execute settlement clearing (FedNow / SWIFT ISO 20022 / ACH NACHA)."""
        d_acct = debtor_account
        c_acct = creditor_account
        if self.cipher:
            d_acct = self.cipher.encrypt_field(debtor_account, "debtor-bank-acct")
            c_acct = self.cipher.encrypt_field(creditor_account, "creditor-bank-acct")

        payload = {
            "amount": amount,
            "currency": currency,
            "urgency": urgency,
            "debtor_name": debtor_name,
            "debtor_account": d_acct,
            "debtor_routing": debtor_routing,
            "creditor_name": creditor_name,
            "creditor_account": c_acct,
            "creditor_routing": creditor_routing,
            "memo": memo,
        }
        resp = self.session.post(
            f"{self.base_url}/api/v1/settlement/execute",
            json=payload,
            timeout=self.timeout,
        )
        resp.raise_for_status()
        return resp.json()
