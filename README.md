# Wallet & Payment

A backend service for a digital wallet platform. Users hold money in wallets and move it in (deposit), out (withdrawal) and between wallets (transfer). It is a Go HTTP service on PostgreSQL. This is the LakiPay Senior Go take-home.

Domain vocabulary is defined in [GLOSSARY.md](GLOSSARY.md). The two big design decisions are recorded in [ADR-0001](docs/adr/0001-ledger-entries-with-cached-balance.md) and [ADR-0002](docs/adr/0002-row-locks-and-idempotency-keys-for-concurrency.md).

## Contents

1. [Project overview](#1-project-overview)
2. [Architecture](#2-architecture)
3. [API design](#3-api-design)
4. [Database design](#4-database-design)
5. [Technical decisions](#5-technical-decisions)
6. [Error handling](#6-error-handling)
7. [Logging and observability](#7-logging-and-observability)
8. [Testing](#8-testing)
9. [Assumptions](#9-assumptions)
10. [Trade-offs](#10-trade-offs)
11. [Limitations](#11-limitations)
12. [Future improvements](#12-future-improvements)
13. [Running the application](#13-running-the-application)
14. [API walkthrough with curl](#14-api-walkthrough-with-curl)

## 1. Project overview

The service has to keep three promises, even with several instances running, many concurrent clients, and clients that retry over bad networks:

- A balance never goes negative.
- Money is never created or lost. A transfer debits one wallet and credits another, or does neither.
- Transaction records match exactly what happened.

What it offers:

- Wallet management: create, retrieve, list with filters and pagination, and freeze, reactivate or close a wallet.
- Deposits, withdrawals and transfers, each protected by a required `Idempotency-Key`.
- Transaction queries: by id, per wallet (a statement), and across all wallets, with filters and cursor pagination.
- `/healthz`, `/readyz` and a Prometheus `/metrics` endpoint, plus structured JSON logs.

Every money-moving request runs in one database transaction that locks the wallets involved, writes a transaction and its ledger entries, and updates the cached balance. A request that breaks a business rule (insufficient funds, frozen wallet, currency mismatch) is a rejection. It returns a precise error, is logged and counted, and is never stored as a transaction.

## 2. Architecture

One binary, `wallet`, with two subcommands: `serve` runs the HTTP server and `migrate` applies migrations and exits. Dependencies point inward to the domain package.

```
cmd/wallet            entry point: config, logger, pool, wiring, signals
internal/
  domain/             Wallet, Transaction, Ledger Entry, Money, status rules, typed errors.
                      No HTTP, no SQL.
  service/            use cases: WalletService, LedgerService. Owns transaction boundaries,
                      financial-operation logs and metrics.
  store/postgres/     repositories, sqlc-generated queries, DB error to domain error mapping
  httpapi/            handlers, DTOs, middleware, error to problem+json mapping
  platform/           config loading, slog setup, Prometheus registry
  app/                composition: builds the services and handler from a pool
migrations/           goose SQL migrations, embedded in the binary
test/integration/     HTTP tests against a real Postgres (testcontainers)
deploy/prometheus.yml scrape config for the compose stack
```

```
client -> httpapi (middleware, decode, validate)
       -> service (one DB transaction per money-moving use case)
       -> store/postgres (sqlc queries, row locks)
       -> PostgreSQL
```

Instances are stateless. Every piece of coordination (row locks, the idempotency key's unique constraint) lives in Postgres, so adding instances behind a load balancer is safe.

Middleware order, outermost first: request id, access log, metrics, panic recovery, request timeout, body size limit.

## 3. API design

JSON over HTTP under `/v1`. Amounts are decimal strings in both directions (`"100.50"`), never floats. The wallet's currency fixes the amount's currency. Timestamps are RFC 3339 in UTC.

| Method | Path | Purpose | Success |
| --- | --- | --- | --- |
| POST | `/v1/wallets` | Create a wallet `{user_id, currency}` | 201 |
| GET | `/v1/wallets/{id}` | Retrieve a wallet | 200 |
| GET | `/v1/wallets` | List wallets. Filters `user_id`, `status`, `currency`; `limit`, `cursor` | 200 |
| PATCH | `/v1/wallets/{id}/status` | Change status `{status}` (`ACTIVE`, `FROZEN`, `CLOSED`) | 200 |
| POST | `/v1/wallets/{id}/deposits` | Deposit `{amount}` | 201, or 200 on replay |
| POST | `/v1/wallets/{id}/withdrawals` | Withdraw `{amount}` | 201, or 200 on replay |
| POST | `/v1/transfers` | Transfer `{source_wallet_id, destination_wallet_id, amount}` | 201, or 200 on replay |
| GET | `/v1/transactions/{id}` | Any transaction, with its ledger entries | 200 |
| GET | `/v1/deposits/{id}` | A deposit (404 if the id is another type) | 200 |
| GET | `/v1/withdrawals/{id}` | A withdrawal (404 if another type) | 200 |
| GET | `/v1/transfers/{id}` | A transfer (404 if another type) | 200 |
| GET | `/v1/wallets/{id}/transactions` | Wallet statement. Transfers appear for source and destination | 200 |
| GET | `/v1/transactions` | All transactions | 200 |
| GET | `/healthz` | Liveness | 200 |
| GET | `/readyz` | Readiness: database ping and not shutting down | 200 or 503 |
| GET | `/metrics` | Prometheus metrics | 200 |

Money-moving endpoints require an `Idempotency-Key` header (1 to 255 printable ASCII characters). A replay returns the original transaction with status 200 and an `Idempotent-Replayed: true` header.

The two transaction listings accept `type` (`DEPOSIT`, `WITHDRAWAL`, `TRANSFER`), `from` and `to` (RFC 3339 created-at range), `currency`, `limit` and `cursor`. Lists are newest first and return `{"data": [...], "next_cursor": "..."}`. `next_cursor` is `null` on the last page. The default `limit` is 20 and the maximum is 100. A larger limit is rejected, not clamped, so a client never silently gets fewer rows than it asked for.

Deposit, withdrawal and transfer responses are the transaction plus its `ledger_entries` and a `balance` field. For deposits and withdrawals that is the wallet's balance after the operation. For transfers it is the source wallet's.

Every response carries `X-Request-ID`. The service reuses the incoming value when it is valid and generates one otherwise.

Wallet status rules:

- `ACTIVE` to `FROZEN`, and `FROZEN` to `ACTIVE`.
- `ACTIVE` or `FROZEN` to `CLOSED`, only at zero balance.
- `CLOSED` to anything is rejected.
- Setting the current status again is a no-op and returns the wallet with 200.

## 4. Database design

PostgreSQL 17, three tables. The SQL is in [migrations/](migrations/).

```
wallets            id, user_id, currency, balance, status, created_at, updated_at
transactions       id, type, status, amount, currency, source_wallet_id, destination_wallet_id,
                   idempotency_key, request_hash, created_at
ledger_entries     id, transaction_id, wallet_id, amount (signed), balance_after, created_at
```

- Money is `bigint` minor units (cents). Floats never touch money. The API converts to and from decimal strings.
- `wallets.balance` has `CHECK (balance >= 0)`. Even a code bug cannot commit a negative balance.
- `UNIQUE (user_id, currency)` gives each user one wallet per currency.
- `transactions.idempotency_key` is unique. `request_hash` is a hash of the canonical request, used to tell a replay from key misuse.
- A check constraint ties a transaction's type to its wallet columns: a deposit has only a destination, a withdrawal only a source, a transfer two different wallets.
- A deposit or withdrawal writes one ledger entry. A transfer writes two that sum to zero.
- Invariant: `wallets.balance = SUM(ledger_entries.amount)` per wallet. Balance is never updated without the matching entry in the same database transaction. Integration tests assert this after concurrent load.
- Indexes serve the listing queries: `(source_wallet_id, created_at, id)`, `(destination_wallet_id, created_at, id)` and `(created_at, id)` on transactions, `(wallet_id, created_at, id)` on ledger entries, `user_id` on wallets. A test runs `EXPLAIN` on the listing queries to guard against sequential scans.

## 5. Technical decisions

| Decision | Chosen | Alternatives considered | Why |
| --- | --- | --- | --- |
| Concurrency control | Pessimistic `SELECT ... FOR UPDATE`, wallets locked in ascending id order | Optimistic version column with retries; SERIALIZABLE isolation; in-process mutexes | Hot wallets turn optimistic retries into storms. SERIALIZABLE forces retry loops everywhere. Mutexes do not work across instances. Ordered locks make opposite-direction transfers deadlock-free. See ADR-0002. |
| Balance storage | Ledger entries plus a cached balance column | Balance column only; balance computed from entries only | Entries make "no money created or lost" checkable and every transaction auditable. A single row to lock and read keeps the hot path cheap. See ADR-0001. |
| Idempotency | Required header, key stored under a unique constraint with a request hash | Optional key; separate idempotency table with TTL; client-generated transaction ids | Storing the key on the transaction makes the check and the write atomic. A concurrent duplicate hits the unique constraint, rolls back and returns the winner. |
| Rejections | Not stored | Store as `FAILED` transactions | A transaction is the only evidence that money moved. Rejections are logged and counted with their reason, and the same key stays usable for a retry. |
| Isolation level | READ COMMITTED plus explicit row locks | REPEATABLE READ or SERIALIZABLE | The locks express the real conflict. Isolation level adds nothing on top. |
| HTTP | Standard library `net/http` with method and pattern routing | chi, gin, echo | Go 1.22+ routing covers everything needed. One less dependency. |
| Data access | `pgx/v5` and `sqlc` | GORM or another ORM; hand-written `database/sql` | Queries stay plain SQL and are type-checked at generation time. Locking and constraint behaviour is visible, not hidden. |
| Errors | RFC 9457 `application/problem+json` with a stable `code` | Ad hoc JSON error shapes | A standard format with one extra field clients can switch on. |
| Pagination | Opaque cursor, keyset on `(created_at, id)` | Offset and limit | Offset pages shift when rows arrive and get slower deep in the list. Keyset pages are stable and use the indexes. |
| Migrations | goose, embedded in the binary, run by `wallet migrate` | Migrations at app startup; external tool | The compose stack runs migrations once, before the app starts, so several instances never race to migrate. |
| Money type | `int64` minor units, decimal string parser per currency | `big.Rat`, shopspring decimal | Exact, fast, and matches the `bigint` column. Excess decimals are rejected rather than rounded. |

## 6. Error handling

All errors use `application/problem+json`:

```json
{
  "type": "about:blank",
  "title": "Unprocessable Entity",
  "status": 422,
  "detail": "the wallet balance is less than the amount",
  "code": "insufficient_funds",
  "request_id": "cac1d3d2-af14-4159-aa2d-c84bb6c16aa9"
}
```

`code` is stable and meant for programs. `detail` is for people. Validation errors add an `errors` array of `{field, message}`, and report every bad field at once. The request id matches the one in the logs.

Business rules raise typed domain errors with a code. One table in `internal/httpapi/problem.go` maps each code to an HTTP status. Errors are wrapped with context on the way up (`%w`), and only the transport layer decides what a client sees. Anything that is not a domain error is logged in full and returned as a generic `internal_error`, so internals never leak. A code with no mapping falls back to a 500 and logs an error.

| Code | Status | Meaning |
| --- | --- | --- |
| `validation_failed` | 400 | Malformed or out-of-range input, including unknown JSON fields. See `errors` for the fields |
| `invalid_json` | 400 | Body is empty, is not valid JSON, or is not a single JSON object |
| `idempotency_key_required` | 400 | A money-moving request had no `Idempotency-Key` |
| `wallet_not_found` | 404 | No such wallet |
| `transaction_not_found` | 404 | No such transaction, or the id belongs to another type |
| `route_not_found` | 404 | No route matches the path |
| `method_not_allowed` | 405 | The route exists but not for this method. `Allow` header is set |
| `wallet_already_exists` | 409 | The user already has a wallet in this currency |
| `idempotency_key_reuse` | 409 | The key was already used with a different request |
| `payload_too_large` | 413 | Body exceeds `MAX_BODY_BYTES` |
| `insufficient_funds` | 422 | Balance is lower than the amount |
| `wallet_not_active` | 422 | A wallet in the operation is frozen or closed |
| `currency_mismatch` | 422 | Transfer between wallets of different currencies |
| `same_wallet_transfer` | 422 | Source and destination are the same wallet |
| `invalid_status_transition` | 422 | Not allowed by the status rules (for example from `CLOSED`) |
| `wallet_balance_not_zero` | 422 | Closing a wallet that still holds money |
| `amount_exceeds_limit` | 422 | Amount is above `MAX_OPERATION_AMOUNT_MINOR` |
| `internal_error` | 500 | Unexpected failure. Details are in the logs only |
| `not_ready` | 503 | `/readyz` while the database is unreachable or the service is shutting down |
| `request_timeout` | 504 | The request exceeded `REQUEST_TIMEOUT` |

Amounts that are zero, negative, have a sign or exponent, are not digits, or have more decimal places than the currency allows return `validation_failed`. A deposit that would overflow `int64` is rejected too.

## 7. Logging and observability

### Logs

Structured JSON on stdout through `log/slog`. Level is set with `LOG_LEVEL`.

- Access log: one line per request with `request_id`, `method`, `route` (the pattern, such as `/v1/wallets/{id}`), `status`, `duration_ms` and `bytes`. 5xx responses log at error level.
- Financial operations: one `financial operation` line per deposit, withdrawal or transfer with `type`, `idempotency_key`, `wallet_id` (or `source_wallet_id` and `destination_wallet_id`), `currency`, `amount`, and an `outcome` of `completed`, `replayed`, `rejected` (with `reason`) or `failed` (with `error`). Completed and replayed lines also carry `transaction_id`. Rejections log at warn, failures at error.
- Panics are recovered, logged with the stack, and answered with a 500 problem.

To follow one transaction, search the logs for its `transaction_id` or `idempotency_key`. To follow one HTTP request, search for its `request_id`, which is also in the error body and the `X-Request-ID` header.

### Metrics

Served at `/metrics`. Route labels use the matched pattern, never the raw path, so cardinality stays bounded.

| Metric | Labels | Answers |
| --- | --- | --- |
| `http_requests_total` | `method`, `route`, `status` | Are requests succeeding? |
| `http_request_duration_seconds` | `method`, `route` | Are requests slowing down? |
| `wallet_financial_operations_total` | `type`, `outcome`, `reason` | Are financial operations failing, and why? `outcome` is `success`, `rejected` or `error` |
| `wallet_financial_operation_duration_seconds` | `type` | How long do money-moving operations take? |
| `db_pool_acquired_connections`, `db_pool_idle_connections`, `db_pool_total_connections` | none | Pool usage |
| `db_pool_acquire_wait_seconds_total` | none | Pool exhaustion: time spent waiting for a connection |

`docker compose up` also starts Prometheus on port 9090, scraping the app every 5 seconds. Useful queries:

```
sum by (route, status) (rate(http_requests_total[1m]))
histogram_quantile(0.95, sum by (le, route) (rate(http_request_duration_seconds_bucket[5m])))
sum by (type, reason) (rate(wallet_financial_operations_total{outcome!="success"}[5m]))
```

### Health and shutdown

`/healthz` says the process is up. `/readyz` pings the database and fails once shutdown starts. On SIGTERM the service flips readiness to failing, waits `SHUTDOWN_DELAY` so the load balancer stops sending traffic, stops accepting connections, gives in-flight requests up to `SHUTDOWN_TIMEOUT` to finish, then closes the pool.

## 8. Testing

Strategy: test behaviour from the outside. The main seam is the real HTTP handler on top of a real PostgreSQL started with testcontainers. Tests send HTTP requests and check the responses and the resulting database state. They never assert on mocks or SQL text, so a refactor that keeps the API behaviour the same does not break them. Repositories and services have no mock-based unit tests. The HTTP seam covers them.

What the integration tests cover:

- Every endpoint, every error code, and the full status transition table.
- Idempotency: replay, replay with different amount formatting, key reuse with a different payload, key reuse across operation types, missing key, a rejected request retried with the same key, and concurrent requests with the same key producing exactly one transaction.
- Concurrency: mixed withdrawals and opposite-direction transfers over a small set of wallets. Afterwards the tests assert no negative balance, a system total equal to deposits minus withdrawals, each balance equal to the sum of its ledger entries, and no 500s. Concurrent withdrawals never overdraw.
- Listings: filters, invalid filters and cursors, pagination that visits every row exactly once, stability while new rows arrive, tie-breaking on id, and `EXPLAIN` checks that the queries use indexes.
- Observability: health and readiness (including an unreachable database), request id handling, the access log, financial-operation logs, and metrics labels.

Pure unit tests (table-driven) cover money parsing and formatting per currency, the wallet transition rules, wallet debit and transfer arithmetic, config loading, JSON decoding and error-to-problem mapping.

Commands:

```
make test               # unit tests only, no Docker needed
make test-integration   # unit and integration tests, needs Docker
make lint               # golangci-lint
```

Integration tests carry the `integration` build tag, so `make test` stays fast.

## 9. Assumptions

- A user is an opaque UUID managed elsewhere. This service does not authenticate callers or check that a caller owns a wallet.
- Supported currencies are ETB, USD and EUR, each with two decimal places. Adding one is a one-line change in the currency table in `internal/domain/money.go`.
- Deposits and withdrawals are synchronous and complete immediately. The money's other side (a bank, a mobile-money provider) is outside the system.
- Idempotency keys are chosen by clients, are globally unique across clients, and live as long as the transaction does.
- A single per-operation maximum (default 1,000,000.00) is enough protection against obviously wrong amounts.
- Closing a wallet requires a zero balance. Money is never silently swept elsewhere.
- Replaying a request means the same operation on the same wallets with the same amount. Amount formatting differences such as `"5"` and `"5.00"` count as the same request.

## 10. Trade-offs

- Row locks over optimistic concurrency. Throughput on a single very hot wallet is limited to one operation at a time. In exchange there are no retry loops, and correctness is easy to argue.
- Cached balance plus entries means two writes per operation and an invariant to protect, instead of one. The reward is cheap balance reads and an auditable history.
- Implicit external side for deposits and withdrawals. A deposit has one ledger entry and the entries of a deposit do not sum to zero. A full double-entry model would balance everything but needs system accounts the brief did not call for.
- Rejections are not persisted. The database stays an exact record of completed money movement, but there is no queryable history of refused attempts. Logs and metrics are the record.
- Global idempotency keys. Simple and race-free now. They would need scoping per client once authentication exists.
- Keyset pagination cannot jump to page N. It is stable and fast, which matters more for a ledger.
- No framework, no ORM. A little more boilerplate (routing, DTOs) for far less magic.
- A request id is accepted from the client. It is only validated, not trusted. It is for correlation, not security.

## 11. Limitations

Each item below is out of scope for this submission. The matching production approach is in [section 12](#12-future-improvements).

- No authentication, authorization or ownership checks. Anyone who can reach the API can move any wallet's money.
- No currency conversion. Cross-currency transfers are rejected.
- Deposits and withdrawals have no external clearing account, so the ledger as a whole is not double-entry.
- No asynchronous payment rails. Transactions are only `COMPLETED`. There are no pending, failed or reversed states, and no refunds or reversals.
- No fees, per-user or per-day limits, or fraud checks. Only one per-operation maximum exists.
- No tracing backend, log shipping, alert rules or dashboards beyond a sample Prometheus scrape config.
- No rate limiting and no TLS termination.
- No event publishing. Other systems cannot react to a completed transaction.
- Idempotency keys are never expired, so the transactions table grows its key index forever.
- Wallet closure is permanent and a closed wallet's `(user_id, currency)` pair is not reusable.
- A single Postgres primary is the only store. There is no read replica, partitioning or archival.

## 12. Future improvements

Larger scale and production approach, including everything listed as out of scope.

- Authentication and authorization. Put the service behind an identity provider (JWT or mTLS between services), derive the caller from the token, check wallet ownership on every call, and give admin-only status changes their own scope. Scope idempotency keys per client at the same time (`UNIQUE (client_id, idempotency_key)`).
- Currency conversion. Add a rates source and a conversion transfer type that writes four entries (debit, credit, and two legs against a currency exchange account), storing the rate and its timestamp on the transaction.
- Full double-entry ledger. Introduce system accounts (cash in transit, per-provider clearing, fees revenue). Every transaction then balances to zero across entries, and the invariant becomes a global `SUM(amount) = 0` that a periodic job can verify.
- Asynchronous rails, refunds and reversals. Use the existing `status` column: create transactions as `PENDING`, settle from provider callbacks or a worker, and model refunds and reversals as new transactions linked to the original with compensating entries. Entries are never edited or deleted.
- Fees and limits. A fee schedule applied inside the same database transaction, with per-user and per-day limits read from a limits table. Fraud and risk checks as a pre-commit hook, or a synchronous call to a risk service with a strict timeout.
- Tracing, log shipping and alerting. Add OpenTelemetry for traces with the request id as a span attribute, ship JSON logs to a central store, and add alerts on rejection rate, p95 latency, pool wait time and readiness flaps. Provide Grafana dashboards from the metrics already exposed.
- Rate limiting and edge concerns. Do rate limiting, TLS and request authentication at an API gateway, with per-client limits and a separate tighter limit on money-moving routes.
- Event publishing. A transactional outbox: write an event row in the same database transaction as the ledger entries, and have a relay publish it to a broker (Kafka, NATS) with at-least-once delivery. Consumers deduplicate on the transaction id.
- Idempotency key expiry. Keep keys for a fixed window (for example 24 to 72 hours). Move them into a separate table with a timestamp and delete expired rows in batches, keeping the transaction itself forever.
- Scaling the database. Read replicas for statement queries, partitioning `transactions` and `ledger_entries` by month, archiving old partitions, and sharding wallets by id if one primary is no longer enough. Transfers across shards would then need a saga or two-phase approach, which is a reason to defer sharding.
- Periodic reconciliation. A job that checks `wallets.balance = SUM(ledger_entries.amount)` for every wallet and alerts on drift.
- Modular monolith structure. With more time I would reshape this into a proper Go modular monolith, like the core-backend service in [Final-Year-Project-G22/backend](https://github.com/Final-Year-Project-G22/backend/). Wallets, ledger, and later users, payments and notifications would each be a module with its own domain, service, store and transport code, wired together in one composition root. Modules would talk to each other only through small exported interfaces, so one can be pulled out into its own service later without rewriting the others. Today the layers are split by technical role (`domain`, `service`, `store`, `httpapi`), which is fine for two use cases but would not hold up as the product grows.

## 13. Running the application

### Dependencies

- Docker with Compose v2 (runs the stack, and is needed for integration tests).
- Go 1.27, only for `make test`, `make test-integration`, `make lint` and running outside Docker.
- `curl`, `jq` and `uuidgen` for the walkthrough in section 14.

### Configuration

All configuration is environment variables. [.env.example](.env.example) lists every one with its default and a comment. `.env` is git-ignored, and nothing secret is committed.

```
cp .env.example .env
```

Change `POSTGRES_PASSWORD` in `.env` if you like. `docker compose` refuses to start if the three `POSTGRES_*` values are missing.

| Variable | Default | Meaning |
| --- | --- | --- |
| `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB` | none, required by compose | Database credentials for the compose Postgres |
| `POSTGRES_PORT` | 5432 | Host port Postgres is published on |
| `PROMETHEUS_PORT` | 9090 | Host port Prometheus is published on |
| `DATABASE_URL` | none, required | Connection string for `serve` and `migrate`. Compose builds this for the containers |
| `HTTP_ADDR` | `:8080` | Listen address |
| `DB_MAX_CONNS` | 10 | Pool size |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `HTTP_READ_TIMEOUT`, `HTTP_WRITE_TIMEOUT`, `HTTP_IDLE_TIMEOUT` | 10s, 15s, 60s | Server timeouts. Write timeout must exceed `REQUEST_TIMEOUT` |
| `REQUEST_TIMEOUT` | 10s | Deadline on each request, including its database calls |
| `MAX_BODY_BYTES` | 1048576 | Request body limit |
| `MAX_OPERATION_AMOUNT_MINOR` | 100000000 | Largest single amount in minor units (1,000,000.00) |
| `SHUTDOWN_DELAY`, `SHUTDOWN_TIMEOUT` | 5s, 20s | Graceful shutdown timings |

Invalid values stop the service at startup, and the error lists every problem at once.

### Infrastructure and database initialisation

The compose stack starts four things in order: Postgres 17 (with a healthcheck), a one-shot `migrate` container that applies all migrations, the `app`, then Prometheus. There is no manual database setup. Migrations are versioned goose files in `migrations/`, embedded in the binary, and running `migrate` again is safe.

### Run

```
cp .env.example .env
docker compose up --build        # or: make up
```

Wait for the `app` container to log that it is listening, then:

```
curl localhost:8080/readyz       # {"status":"ready"}
```

- API: http://localhost:8080
- Metrics: http://localhost:8080/metrics
- Prometheus: http://localhost:9090

Stop with Ctrl+C. `make down` also deletes the database volume.

To run the service on the host instead, with only Postgres in Docker:

```
docker compose up -d postgres
make migrate
make run
```

`make` reads `.env` automatically, so `DATABASE_URL` points at the published Postgres port.

### Tests

```
make test               # unit tests, no Docker
make test-integration   # everything, needs Docker (starts a throwaway Postgres)
```

### Other commands

```
make build    # binary in bin/wallet
make sqlc     # regenerate query code after editing internal/store/postgres/queries/*.sql
make lint     # golangci-lint
```

## 14. API walkthrough with curl

Run these against `docker compose up`. They create two wallets, deposit, withdraw, transfer, replay a request, and list transactions.

```bash
BASE=http://localhost:8080

# Two wallets, one per user, in ETB
ALICE=$(curl -s -X POST $BASE/v1/wallets -H 'Content-Type: application/json' \
  -d "{\"user_id\":\"$(uuidgen)\",\"currency\":\"ETB\"}" | jq -r .id)
BOB=$(curl -s -X POST $BASE/v1/wallets -H 'Content-Type: application/json' \
  -d "{\"user_id\":\"$(uuidgen)\",\"currency\":\"ETB\"}" | jq -r .id)

# Deposit 500.00 into Alice's wallet
curl -s -X POST $BASE/v1/wallets/$ALICE/deposits \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: demo-deposit-1' \
  -d '{"amount":"500.00"}' | jq .

# Withdraw 100.25 from Alice's wallet
curl -s -X POST $BASE/v1/wallets/$ALICE/withdrawals \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: demo-withdrawal-1' \
  -d '{"amount":"100.25"}' | jq -c '{type,amount,balance}'

# Transfer 150.00 from Alice to Bob
curl -s -X POST $BASE/v1/transfers \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: demo-transfer-1' \
  -d "{\"source_wallet_id\":\"$ALICE\",\"destination_wallet_id\":\"$BOB\",\"amount\":\"150.00\"}" \
  | jq -c '{id,type,amount,balance}'

# Replay the same transfer: 200 and Idempotent-Replayed: true, no second transfer
curl -si -X POST $BASE/v1/transfers \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: demo-transfer-1' \
  -d "{\"source_wallet_id\":\"$ALICE\",\"destination_wallet_id\":\"$BOB\",\"amount\":\"150.00\"}" \
  | grep -iE '^HTTP|idempotent-replayed'

# Same key, different amount: 409 idempotency_key_reuse
curl -s -X POST $BASE/v1/transfers \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: demo-transfer-1' \
  -d "{\"source_wallet_id\":\"$ALICE\",\"destination_wallet_id\":\"$BOB\",\"amount\":\"1.00\"}" | jq .

# Overdraw Bob: 422 insufficient_funds, nothing stored
curl -s -X POST $BASE/v1/wallets/$BOB/withdrawals \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: demo-withdrawal-2' \
  -d '{"amount":"9999.00"}' | jq .

# Balances: Alice 249.75, Bob 150.00
curl -s $BASE/v1/wallets/$ALICE | jq -c '{id,balance}'
curl -s $BASE/v1/wallets/$BOB | jq -c '{id,balance}'

# Alice's statement, newest first (deposit, withdrawal and the transfer)
curl -s "$BASE/v1/wallets/$ALICE/transactions?limit=10" | jq -c '.data[] | {type,amount}'

# All transfers in ETB
curl -s "$BASE/v1/transactions?type=TRANSFER&currency=ETB" | jq '.data | length'

# Freeze Bob's wallet, then watch the counters
curl -s -X PATCH $BASE/v1/wallets/$BOB/status \
  -H 'Content-Type: application/json' -d '{"status":"FROZEN"}' | jq -c '{status}'
curl -s $BASE/metrics | grep '^wallet_financial_operations_total'
```

To page through a long list, pass the previous response's `next_cursor` as `?cursor=` until it is `null`.
