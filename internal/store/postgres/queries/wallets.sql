-- name: CreateWallet :one
INSERT INTO wallets (user_id, currency)
VALUES ($1, $2)
RETURNING *;

-- name: GetWallet :one
SELECT * FROM wallets
WHERE id = $1;
