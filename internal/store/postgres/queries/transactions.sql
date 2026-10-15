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

-- Listing filters. Every condition on created_at and the keyset cursor is written
-- as a bound that is always present, with COALESCE standing in for an absent
-- parameter. Unlike "$n IS NULL OR created_at >= $n", such a bound is a valid
-- index condition even in a generic plan (one planned without knowing the
-- values, which a cached prepared statement may switch to), so the cursor
-- seeks into the index instead of reading from the newest row and filtering.
-- currency is char(3): it is compared as char(3) so the planner can use the
-- column's statistics, which a cast to text would hide.

-- name: ListTransactions :many
SELECT * FROM transactions
WHERE (sqlc.narg('type')::text IS NULL OR type = sqlc.narg('type'))
  AND (sqlc.narg('currency')::char(3) IS NULL OR currency = sqlc.narg('currency')::char(3))
  AND created_at >= COALESCE(sqlc.narg('created_from')::timestamptz, '-infinity')
  AND created_at <= COALESCE(sqlc.narg('created_to')::timestamptz, 'infinity')
  AND (created_at, id) < (COALESCE(sqlc.narg('after_created_at')::timestamptz, 'infinity'),
                          COALESCE(sqlc.narg('after_id')::uuid, 'ffffffff-ffff-ffff-ffff-ffffffffffff'))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('row_limit');

-- A Wallet's statement is its Transactions as source plus as destination. Each
-- side is read newest-first straight off its (wallet, created_at, id) index and
-- cut at the page size, so the work follows the page, not the Wallet's history.
-- The two sides never overlap: a Transfer's Wallets are distinct (CHECK).
-- name: ListWalletTransactions :many
SELECT * FROM (
    (SELECT s.* FROM transactions s
     WHERE s.source_wallet_id = sqlc.arg('wallet_id')
       AND (sqlc.narg('type')::text IS NULL OR s.type = sqlc.narg('type'))
       AND (sqlc.narg('currency')::char(3) IS NULL OR s.currency = sqlc.narg('currency')::char(3))
       AND s.created_at >= COALESCE(sqlc.narg('created_from')::timestamptz, '-infinity')
       AND s.created_at <= COALESCE(sqlc.narg('created_to')::timestamptz, 'infinity')
       AND (s.created_at, s.id) < (COALESCE(sqlc.narg('after_created_at')::timestamptz, 'infinity'),
                                   COALESCE(sqlc.narg('after_id')::uuid, 'ffffffff-ffff-ffff-ffff-ffffffffffff'))
     ORDER BY s.created_at DESC, s.id DESC
     LIMIT sqlc.arg('row_limit'))
    UNION ALL
    (SELECT d.* FROM transactions d
     WHERE d.destination_wallet_id = sqlc.arg('wallet_id')
       AND (sqlc.narg('type')::text IS NULL OR d.type = sqlc.narg('type'))
       AND (sqlc.narg('currency')::char(3) IS NULL OR d.currency = sqlc.narg('currency')::char(3))
       AND d.created_at >= COALESCE(sqlc.narg('created_from')::timestamptz, '-infinity')
       AND d.created_at <= COALESCE(sqlc.narg('created_to')::timestamptz, 'infinity')
       AND (d.created_at, d.id) < (COALESCE(sqlc.narg('after_created_at')::timestamptz, 'infinity'),
                                   COALESCE(sqlc.narg('after_id')::uuid, 'ffffffff-ffff-ffff-ffff-ffffffffffff'))
     ORDER BY d.created_at DESC, d.id DESC
     LIMIT sqlc.arg('row_limit'))
) AS statement
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('row_limit');
