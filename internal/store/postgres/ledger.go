package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Johna210/wallet_and_payment/internal/domain"
	"github.com/Johna210/wallet_and_payment/internal/service"
	"github.com/Johna210/wallet_and_payment/internal/store/postgres/sqlcgen"
)

const transactionsIdempotencyKeyKey = "transactions_idempotency_key_key"

// LedgerStore persists Transactions and Ledger Entries, and runs money-moving
// work inside one database transaction.
type LedgerStore struct {
	pool *pgxpool.Pool
	q    *sqlcgen.Queries
}

// NewLedgerStore returns a LedgerStore over pool.
func NewLedgerStore(pool *pgxpool.Pool) *LedgerStore {
	return &LedgerStore{pool: pool, q: sqlcgen.New(pool)}
}

// InTx runs fn inside one READ COMMITTED database transaction. It commits when
// fn returns nil and rolls back on any error, which is returned unchanged
// (apart from errors raised by commit itself).
func (s *LedgerStore) InTx(ctx context.Context, fn func(service.LedgerTx) error) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		return fn(&ledgerTx{q: s.q.WithTx(tx)})
	})
}

// Get returns the Transaction with id and its Ledger Entries, or
// transaction_not_found.
func (s *LedgerStore) Get(ctx context.Context, id uuid.UUID) (domain.TransactionDetail, error) {
	row, err := s.q.GetTransaction(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TransactionDetail{}, domain.NewError(domain.CodeTransactionNotFound, "no transaction exists with this id")
	}
	if err != nil {
		return domain.TransactionDetail{}, fmt.Errorf("select transaction: %w", err)
	}
	return withEntries(ctx, s.q, row)
}

// List returns up to limit Transactions across all Wallets matching filter,
// newest first, starting strictly after the position when it is non-nil.
func (s *LedgerStore) List(ctx context.Context, filter domain.TransactionFilter, after *domain.TransactionPosition, limit int) ([]domain.Transaction, error) {
	afterAt, afterID := afterParams(after)
	rows, err := s.q.ListTransactions(ctx, sqlcgen.ListTransactionsParams{
		Type:           optionalString(filter.Type),
		Currency:       optionalString(filter.Currency),
		CreatedFrom:    lowerBound(filter.From),
		CreatedTo:      upperBound(filter.To),
		AfterCreatedAt: afterAt,
		AfterID:        afterID,
		RowLimit:       int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("select transactions: %w", err)
	}
	return transactionsFromRows(rows), nil
}

// ListForWallet returns up to limit of the Wallet's Transactions matching
// filter, as source or destination, newest first, starting strictly after the
// position when it is non-nil.
func (s *LedgerStore) ListForWallet(ctx context.Context, walletID uuid.UUID, filter domain.TransactionFilter, after *domain.TransactionPosition, limit int) ([]domain.Transaction, error) {
	afterAt, afterID := afterParams(after)
	rows, err := s.q.ListWalletTransactions(ctx, sqlcgen.ListWalletTransactionsParams{
		WalletID:       pgtype.UUID{Bytes: walletID, Valid: true},
		Type:           optionalString(filter.Type),
		Currency:       optionalString(filter.Currency),
		CreatedFrom:    lowerBound(filter.From),
		CreatedTo:      upperBound(filter.To),
		AfterCreatedAt: afterAt,
		AfterID:        afterID,
		RowLimit:       int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("select wallet transactions: %w", err)
	}
	return transactionsFromRows(rows), nil
}

// afterParams returns the keyset-cursor query parameters for a position; both
// are NULL, which disables the condition, when there is none.
func afterParams(after *domain.TransactionPosition) (pgtype.Timestamptz, pgtype.UUID) {
	if after == nil {
		return pgtype.Timestamptz{}, pgtype.UUID{}
	}
	return pgtype.Timestamptz{Time: after.CreatedAt, Valid: true}, pgtype.UUID{Bytes: after.ID, Valid: true}
}

func optionalString[T ~string](v *T) *string {
	if v == nil {
		return nil
	}
	s := string(*v)
	return &s
}

// lowerBound returns t as an inclusive lower bound at the microsecond
// precision Postgres stores. A finer t is rounded up: truncating it would admit
// a row that is older than the bound.
func lowerBound(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	b := t.Truncate(time.Microsecond)
	if b.Before(*t) {
		b = b.Add(time.Microsecond)
	}
	return pgtype.Timestamptz{Time: b, Valid: true}
}

// upperBound returns t as an inclusive upper bound at the microsecond
// precision Postgres stores. A finer t is rounded down, which keeps exactly the
// rows at or before it.
func upperBound(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t.Truncate(time.Microsecond), Valid: true}
}

func transactionsFromRows(rows []sqlcgen.Transaction) []domain.Transaction {
	txs := make([]domain.Transaction, len(rows))
	for i, r := range rows {
		txs[i] = transactionFromRow(r)
	}
	return txs
}

// ledgerTx is the work a LedgerStore transaction exposes to the service.
type ledgerTx struct {
	q *sqlcgen.Queries
}

func (t *ledgerTx) FindByIdempotencyKey(ctx context.Context, key string) (domain.TransactionDetail, bool, error) {
	row, err := t.q.GetTransactionByIdempotencyKey(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TransactionDetail{}, false, nil
	}
	if err != nil {
		return domain.TransactionDetail{}, false, fmt.Errorf("select transaction by idempotency key: %w", err)
	}
	d, err := withEntries(ctx, t.q, row)
	return d, err == nil, err
}

func (t *ledgerTx) LockWallet(ctx context.Context, id uuid.UUID) (domain.Wallet, error) {
	row, err := t.q.GetWalletForUpdate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Wallet{}, domain.NewError(domain.CodeWalletNotFound, "no wallet exists with this id")
	}
	if err != nil {
		return domain.Wallet{}, fmt.Errorf("lock wallet: %w", err)
	}
	return walletFromRow(row), nil
}

func (t *ledgerTx) Record(ctx context.Context, n domain.NewTransaction) (domain.Transaction, error) {
	row, err := t.q.InsertTransaction(ctx, sqlcgen.InsertTransactionParams{
		Type:                string(n.Type),
		Amount:              n.Amount.Minor,
		Currency:            string(n.Amount.Currency),
		SourceWalletID:      nullableUUID(n.SourceWalletID),
		DestinationWalletID: nullableUUID(n.DestinationWalletID),
		IdempotencyKey:      n.IdempotencyKey,
		RequestHash:         n.RequestHash,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation && pgErr.ConstraintName == transactionsIdempotencyKeyKey {
			return domain.Transaction{}, domain.ErrDuplicateIdempotencyKey
		}
		return domain.Transaction{}, fmt.Errorf("insert transaction: %w", err)
	}
	return transactionFromRow(row), nil
}

func (t *ledgerTx) PostEntry(ctx context.Context, n domain.NewLedgerEntry) (domain.LedgerEntry, error) {
	row, err := t.q.InsertLedgerEntry(ctx, sqlcgen.InsertLedgerEntryParams{
		TransactionID: n.TransactionID,
		WalletID:      n.WalletID,
		Amount:        n.Amount.Minor,
		BalanceAfter:  n.BalanceAfter.Minor,
	})
	if err != nil {
		return domain.LedgerEntry{}, fmt.Errorf("insert ledger entry: %w", err)
	}
	// The cached Balance moves in the same database transaction as the entry
	// that explains it (ADR-0001).
	if _, err := t.q.SetWalletBalance(ctx, sqlcgen.SetWalletBalanceParams{ID: n.WalletID, Balance: n.BalanceAfter.Minor}); err != nil {
		return domain.LedgerEntry{}, fmt.Errorf("update wallet balance: %w", err)
	}
	return entryFromRow(row, n.Amount.Currency), nil
}

// withEntries loads the Ledger Entries of a Transaction row.
func withEntries(ctx context.Context, q *sqlcgen.Queries, row sqlcgen.Transaction) (domain.TransactionDetail, error) {
	rows, err := q.ListLedgerEntriesForTransaction(ctx, row.ID)
	if err != nil {
		return domain.TransactionDetail{}, fmt.Errorf("select ledger entries: %w", err)
	}
	tx := transactionFromRow(row)
	entries := make([]domain.LedgerEntry, len(rows))
	for i, r := range rows {
		entries[i] = entryFromRow(r, tx.Amount.Currency)
	}
	return domain.TransactionDetail{Transaction: tx, Entries: entries}, nil
}

func transactionFromRow(r sqlcgen.Transaction) domain.Transaction {
	currency := domain.Currency(r.Currency)
	return domain.Transaction{
		ID:                  r.ID,
		Type:                domain.TransactionType(r.Type),
		Status:              domain.TransactionStatus(r.Status),
		Amount:              domain.Money{Minor: r.Amount, Currency: currency},
		SourceWalletID:      uuidPtr(r.SourceWalletID),
		DestinationWalletID: uuidPtr(r.DestinationWalletID),
		IdempotencyKey:      r.IdempotencyKey,
		RequestHash:         r.RequestHash,
		CreatedAt:           r.CreatedAt.UTC(),
	}
}

func entryFromRow(r sqlcgen.LedgerEntry, currency domain.Currency) domain.LedgerEntry {
	return domain.LedgerEntry{
		ID:            r.ID,
		TransactionID: r.TransactionID,
		WalletID:      r.WalletID,
		Amount:        domain.Money{Minor: r.Amount, Currency: currency},
		BalanceAfter:  domain.Money{Minor: r.BalanceAfter, Currency: currency},
		CreatedAt:     r.CreatedAt.UTC(),
	}
}

func nullableUUID(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

func uuidPtr(id pgtype.UUID) *uuid.UUID {
	if !id.Valid {
		return nil
	}
	u := uuid.UUID(id.Bytes)
	return &u
}
