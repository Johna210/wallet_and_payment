-- name: InsertTransaction :one
INSERT INTO transactions (type, amount, currency, source_wallet_id, destination_wallet_id, idempotency_key, request_hash)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: InsertLedgerEntry :one
INSERT INTO ledger_entries (transaction_id, wallet_id, amount, balance_after)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetTransaction :one
SELECT * FROM transactions
WHERE id = $1;

-- name: GetTransactionByIdempotencyKey :one
SELECT * FROM transactions
WHERE idempotency_key = $1;

-- name: ListLedgerEntriesForTransaction :many
SELECT * FROM ledger_entries
WHERE transaction_id = $1
ORDER BY created_at, amount, id;

-- name: SetWalletBalance :one
UPDATE wallets
SET balance = $2, updated_at = now()
WHERE id = $1
RETURNING *;
