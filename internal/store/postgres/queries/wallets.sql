-- name: CreateWallet :one
INSERT INTO wallets (user_id, currency)
VALUES ($1, $2)
RETURNING *;

-- name: GetWallet :one
SELECT * FROM wallets
WHERE id = $1;

-- name: ListWallets :many
SELECT * FROM wallets
WHERE (sqlc.narg('user_id')::uuid IS NULL OR user_id = sqlc.narg('user_id'))
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('currency')::text IS NULL OR currency = sqlc.narg('currency'))
  AND (sqlc.narg('after_created_at')::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg('after_created_at')::timestamptz, sqlc.narg('after_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('row_limit');
