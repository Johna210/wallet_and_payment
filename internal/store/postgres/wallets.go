package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Johna210/wallet_and_payment/internal/domain"
	"github.com/Johna210/wallet_and_payment/internal/store/postgres/sqlcgen"
)

const (
	pgUniqueViolation = "23505"

	walletsUserCurrencyKey = "wallets_user_id_currency_key"
)

// WalletStore persists Wallets.
type WalletStore struct {
	pool *pgxpool.Pool
	q    *sqlcgen.Queries
}

// NewWalletStore returns a WalletStore over pool.
func NewWalletStore(pool *pgxpool.Pool) *WalletStore {
	return &WalletStore{pool: pool, q: sqlcgen.New(pool)}
}

// Create inserts a new Active, zero-Balance Wallet. A second Wallet for the
// same User and Currency is reported as wallet_already_exists.
func (s *WalletStore) Create(ctx context.Context, userID uuid.UUID, currency domain.Currency) (domain.Wallet, error) {
	row, err := s.q.CreateWallet(ctx, sqlcgen.CreateWalletParams{UserID: userID, Currency: string(currency)})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation && pgErr.ConstraintName == walletsUserCurrencyKey {
			return domain.Wallet{}, domain.NewError(domain.CodeWalletAlreadyExists,
				fmt.Sprintf("the user already has a %s wallet", currency))
		}
		return domain.Wallet{}, fmt.Errorf("insert wallet: %w", err)
	}
	return walletFromRow(row), nil
}

// Get returns the Wallet with id, or wallet_not_found.
func (s *WalletStore) Get(ctx context.Context, id uuid.UUID) (domain.Wallet, error) {
	row, err := s.q.GetWallet(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Wallet{}, domain.NewError(domain.CodeWalletNotFound, "no wallet exists with this id")
	}
	if err != nil {
		return domain.Wallet{}, fmt.Errorf("select wallet: %w", err)
	}
	return walletFromRow(row), nil
}

// UpdateLocked applies change to the Wallet with id inside one transaction
// that holds the Wallet's row lock (SELECT … FOR UPDATE), so it serializes
// with every other operation on that Wallet (ADR-0002). change must not alter
// the Wallet's Balance. When it returns the Wallet's current status the row is
// left untouched. Returns wallet_not_found, or whatever error change returns.
func (s *WalletStore) UpdateLocked(ctx context.Context, id uuid.UUID, change func(domain.Wallet) (domain.Wallet, error)) (domain.Wallet, error) {
	var updated domain.Wallet
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		row, err := q.GetWalletForUpdate(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NewError(domain.CodeWalletNotFound, "no wallet exists with this id")
		}
		if err != nil {
			return fmt.Errorf("lock wallet: %w", err)
		}
		current := walletFromRow(row)

		next, err := change(current)
		if err != nil {
			return err
		}
		if next.Status == current.Status {
			updated = current
			return nil
		}
		row, err = q.UpdateWalletStatus(ctx, sqlcgen.UpdateWalletStatusParams{ID: id, Status: string(next.Status)})
		if err != nil {
			return fmt.Errorf("update wallet status: %w", err)
		}
		updated = walletFromRow(row)
		return nil
	})
	if err != nil {
		return domain.Wallet{}, err
	}
	return updated, nil
}

// List returns up to limit Wallets matching filter, newest first, starting
// strictly after the position when it is non-nil.
func (s *WalletStore) List(ctx context.Context, filter domain.WalletFilter, after *domain.WalletPosition, limit int) ([]domain.Wallet, error) {
	params := sqlcgen.ListWalletsParams{RowLimit: int32(limit)}
	if after != nil {
		params.AfterCreatedAt = pgtype.Timestamptz{Time: after.CreatedAt, Valid: true}
		params.AfterID = pgtype.UUID{Bytes: after.ID, Valid: true}
	}
	if filter.UserID != nil {
		params.UserID = pgtype.UUID{Bytes: *filter.UserID, Valid: true}
	}
	if filter.Status != nil {
		status := string(*filter.Status)
		params.Status = &status
	}
	if filter.Currency != nil {
		currency := string(*filter.Currency)
		params.Currency = &currency
	}

	rows, err := s.q.ListWallets(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("select wallets: %w", err)
	}
	wallets := make([]domain.Wallet, len(rows))
	for i, r := range rows {
		wallets[i] = walletFromRow(r)
	}
	return wallets, nil
}

func walletFromRow(r sqlcgen.Wallet) domain.Wallet {
	return domain.Wallet{
		ID:        r.ID,
		UserID:    r.UserID,
		Balance:   domain.Money{Minor: r.Balance, Currency: domain.Currency(r.Currency)},
		Status:    domain.WalletStatus(r.Status),
		CreatedAt: r.CreatedAt.UTC(),
		UpdatedAt: r.UpdatedAt.UTC(),
	}
}
