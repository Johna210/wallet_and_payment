# Pessimistic row locks and stored idempotency keys

Because the service must stay correct with multiple instances and concurrent clients, every money-moving operation runs in one Postgres transaction (READ COMMITTED) that takes `SELECT … FOR UPDATE` on the affected Wallets, always in ascending wallet-id order so concurrent Transfers cannot deadlock. Retries are made safe by a required `Idempotency-Key` header stored under a unique constraint with a hash of the request: same key and payload replays the original Transaction, same key with a different payload is rejected with 409.

## Considered Options

- **Optimistic locking (version column + retry)**: rejected; contention on hot wallets turns into retry storms and more complex code for no gain at this scale.
- **SERIALIZABLE isolation**: rejected; correct but forces serialization-failure retry loops everywhere, while explicit row locks express the actual conflict.
- **In-process mutexes**: rejected; useless across instances.

## Consequences

- Idempotency keys are global (no auth yet); in production they would be scoped per client.
- Only completed operations are stored as Transactions; Rejections are logged and counted, not persisted.
