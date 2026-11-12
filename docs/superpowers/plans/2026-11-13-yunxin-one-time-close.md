# Yunxin one-time customer closure

Approved scope: 1599 charges 84 quota and releases 59; 530-534 waive all 1,233 quota. One U = 500,000 quota. No supplier facts or other settlements change.

1. Add SQLite wallet, token, audit, rollback and replay tests in `server/model/ztapi_yunxin_close_test.go`; see the missing implementation fail.
2. Add a fixed six-row batch in `server/model/ztapi_yunxin_close.go`. Bind IDs, request IDs, model, attempt lineage, original hold and pending reason. Lock and check ownership, finance authority and absence of concurrent settlement proof. Use the normal wallet and token ledger helpers and the durable consumption log. Distinguish platform decisions from supplier evidence. Keep the batch atomic and the preview free of writes.
3. Add `server/cmd/ztapi-yunxin-close` for preview and explicit apply. Recompute 1599 from the saved price snapshot, refuse a mismatch, never migrate the database, and connect to the existing cache only to invalidate affected entries after commit.
4. Run focused tests, existing finance regression, and the repository financial workflow on the same commit. Build the one-time tool from that commit, preview on production, apply once, and verify ledger, original pending status and customer amounts. Stop if any gate fails.
