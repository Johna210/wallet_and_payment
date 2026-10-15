# Ledger entries alongside a cached wallet balance

Every Transaction writes one signed Ledger Entry per Wallet it touches (a Transfer writes −X and +X), and the Wallet's `balance` column is updated in the same database transaction as a cached projection of those entries, guarded by `CHECK (balance >= 0)`. We chose this over a bare balance column because entries make "no money created or lost" structural and make any Transaction's effect auditable, and over balance-from-entries-only because reading and locking a single row keeps the hot path cheap.

## Consequences

- The invariant `wallet.balance = SUM(ledger_entries.amount)` must hold and is asserted in tests; code must never update `balance` without writing the matching entry.
- Deposits and Withdrawals have a single entry; the external side is implicit. A full double-entry model with system/external accounts is a future improvement.
- Amounts are `BIGINT` minor units; the API speaks decimal strings. Floats never touch money.
