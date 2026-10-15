-- Transactions record completed financial operations; ledger_entries record
-- each one's signed effect on a Wallet's Balance (ADR-0001). Rejections are
-- never stored. idempotency_key is globally unique (ADR-0002).

-- +goose Up
CREATE TABLE transactions (
    id                    uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    type                  text        NOT NULL,
    status                text        NOT NULL DEFAULT 'COMPLETED',
    amount                bigint      NOT NULL,
    currency              char(3)     NOT NULL,
    source_wallet_id      uuid        REFERENCES wallets (id),
    destination_wallet_id uuid        REFERENCES wallets (id),
    idempotency_key       text        NOT NULL,
    request_hash          text        NOT NULL,
    created_at            timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT transactions_idempotency_key_key UNIQUE (idempotency_key),
    CONSTRAINT transactions_idempotency_key_length CHECK (char_length(idempotency_key) BETWEEN 1 AND 255),
    CONSTRAINT transactions_type_valid CHECK (type IN ('DEPOSIT', 'WITHDRAWAL', 'TRANSFER')),
    CONSTRAINT transactions_status_valid CHECK (status IN ('COMPLETED')),
    CONSTRAINT transactions_amount_positive CHECK (amount > 0),
    -- The wallet columns that are set must match the type: a Deposit has only
    -- a destination, a Withdrawal only a source, a Transfer two distinct Wallets.
    CONSTRAINT transactions_type_wallets CHECK (
        (type = 'DEPOSIT'    AND source_wallet_id IS NULL     AND destination_wallet_id IS NOT NULL) OR
        (type = 'WITHDRAWAL' AND source_wallet_id IS NOT NULL AND destination_wallet_id IS NULL) OR
        (type = 'TRANSFER'   AND source_wallet_id IS NOT NULL AND destination_wallet_id IS NOT NULL
                             AND source_wallet_id <> destination_wallet_id)
    )
);

CREATE INDEX transactions_source_idx      ON transactions (source_wallet_id, created_at, id);
CREATE INDEX transactions_destination_idx ON transactions (destination_wallet_id, created_at, id);
CREATE INDEX transactions_created_idx     ON transactions (created_at, id);

CREATE TABLE ledger_entries (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_id uuid        NOT NULL REFERENCES transactions (id),
    wallet_id      uuid        NOT NULL REFERENCES wallets (id),
    amount         bigint      NOT NULL,
    balance_after  bigint      NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT ledger_entries_amount_non_zero CHECK (amount <> 0),
    CONSTRAINT ledger_entries_balance_after_non_negative CHECK (balance_after >= 0)
);

CREATE INDEX ledger_entries_transaction_idx ON ledger_entries (transaction_id);
CREATE INDEX ledger_entries_wallet_idx      ON ledger_entries (wallet_id, created_at, id);

-- +goose Down
DROP TABLE ledger_entries;
DROP TABLE transactions;
