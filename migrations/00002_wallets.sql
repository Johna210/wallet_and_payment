-- Wallets hold a User's money in one Currency. balance is a cached projection
-- of the Wallet's Ledger Entries in minor units (ADR-0001).

-- +goose Up
CREATE TABLE wallets (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid        NOT NULL,
    currency   char(3)     NOT NULL,
    balance    bigint      NOT NULL DEFAULT 0,
    status     text        NOT NULL DEFAULT 'ACTIVE',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT wallets_balance_non_negative CHECK (balance >= 0),
    CONSTRAINT wallets_status_valid CHECK (status IN ('ACTIVE', 'FROZEN', 'CLOSED')),
    CONSTRAINT wallets_user_id_currency_key UNIQUE (user_id, currency)
);

CREATE INDEX wallets_user_id_idx ON wallets (user_id);

-- +goose Down
DROP TABLE wallets;
