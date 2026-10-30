# Xcloud Admission and Test Cost

## Goal

Restore Xcloud models only after exact route, price, usage and settlement evidence is verified. Report management test consumption separately without changing customer balances or double-counting supplier costs.

## Design

- Preserve the existing Yunxin channel 2 and -hc admission contract. Add a distinct, versioned Xcloud contract with exact supplier, channel, logical model, physical model and protocol identities; fail closed on mismatch and preserve immutable publication snapshots.
- Preserve the already deployed Claude usage compatibility fix, and validate native uncached input, cache read and cache write as exclusive components before converting to inclusive OpenAI accounting.
- Identify management tests explicitly by immutable request-linked finance facts. Never infer test status from an administrator user ID or a client-controlled header. Show confirmed and estimated test cost separately.
- Existing full-channel supplier attestations include tests. A test amount covered by such an attestation is a breakdown of that cost, not another deduction. Unknown actual supplier cost remains pending; public quotes and retail customer charges are not actual supplier debits.
- Do not change historical balances, rewrite immutable billing records, or publish a model before real result and billing verification. Missing quotation, supplier usage or identity stays off sale.

## Verification

Use test-first regression for correct and incorrect channel/model pairs, snapshot immutability, no cache double counting, finance idempotency, permissions, cost inclusion/exclusion, unknown evidence, and monthly profit. Verify current production baseline and backup before any deployment. Paid tests use isolated operator keys and record each request as a test expense.
