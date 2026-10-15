package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/Johna210/wallet_and_payment/internal/domain"
)

// LedgerTx is the work available inside one database transaction. A
// transaction is atomic: everything it did is kept only if the function that
// received it returns nil.
type LedgerTx interface {
	// FindByIdempotencyKey returns the Transaction stored under key, if any.
	FindByIdempotencyKey(ctx context.Context, key string) (domain.TransactionDetail, bool, error)
	// LockWallet reads the Wallet under a row lock (SELECT … FOR UPDATE) that
	// is held until the database transaction ends. wallet_not_found if absent.
	LockWallet(ctx context.Context, id uuid.UUID) (domain.Wallet, error)
	// Record stores a Transaction. A key already in use is reported as
	// domain.ErrDuplicateIdempotencyKey.
	Record(ctx context.Context, n domain.NewTransaction) (domain.Transaction, error)
	// PostEntry stores a Ledger Entry and sets the Wallet's cached Balance to
	// its BalanceAfter, so Balance never changes without an entry.
	PostEntry(ctx context.Context, n domain.NewLedgerEntry) (domain.LedgerEntry, error)
}

// LedgerRepository is the storage LedgerService needs.
type LedgerRepository interface {
	// InTx runs fn in one database transaction: committed if fn returns nil,
	// rolled back otherwise.
	InTx(ctx context.Context, fn func(LedgerTx) error) error
	// Get returns a Transaction with its Ledger Entries, or transaction_not_found.
	Get(ctx context.Context, id uuid.UUID) (domain.TransactionDetail, error)
}

// WalletReader reads Wallets outside any transaction.
type WalletReader interface {
	Get(ctx context.Context, id uuid.UUID) (domain.Wallet, error)
}

// LedgerService moves money and reads the resulting Transactions.
type LedgerService struct {
	repo      LedgerRepository
	wallets   WalletReader
	logger    *slog.Logger
	maxAmount int64 // minor units
}

// NewLedgerService returns a LedgerService that logs every financial operation
// to logger and refuses any single operation above maxAmount minor units.
func NewLedgerService(repo LedgerRepository, wallets WalletReader, logger *slog.Logger, maxAmount int64) *LedgerService {
	return &LedgerService{repo: repo, wallets: wallets, logger: logger, maxAmount: maxAmount}
}

// MoneyMovement is the outcome of a money-moving request.
type MoneyMovement struct {
	Transaction domain.TransactionDetail
	// Replayed is true when the request repeated an earlier one and the
	// original Transaction was returned instead of a new one.
	Replayed bool
}

// walletMove describes a Transaction that changes the Balance of one Wallet:
// money comes in from outside the system (a Deposit) or goes out of it (a
// Withdrawal).
type walletMove struct {
	typ      domain.TransactionType
	name     string // for error messages
	incoming bool
	// apply enforces the business rules and returns the Wallet after the move.
	apply func(domain.Wallet, domain.Money) (domain.Wallet, error)
}

var (
	depositMove    = walletMove{typ: domain.TypeDeposit, name: "deposit", incoming: true, apply: domain.Wallet.Credit}
	withdrawalMove = walletMove{typ: domain.TypeWithdrawal, name: "withdrawal", apply: domain.Wallet.Debit}
)

// Deposit adds amount (a decimal string in the Wallet's Currency) to the Wallet.
func (s *LedgerService) Deposit(ctx context.Context, walletID uuid.UUID, amount, idempotencyKey string) (MoneyMovement, error) {
	return s.moveOneWallet(ctx, depositMove, walletID, amount, idempotencyKey)
}

// Withdraw takes amount (a decimal string in the Wallet's Currency) out of the
// Wallet, refusing any amount greater than its Balance.
func (s *LedgerService) Withdraw(ctx context.Context, walletID uuid.UUID, amount, idempotencyKey string) (MoneyMovement, error) {
	return s.moveOneWallet(ctx, withdrawalMove, walletID, amount, idempotencyKey)
}

func (s *LedgerService) moveOneWallet(ctx context.Context, move walletMove, walletID uuid.UUID, amount, idempotencyKey string) (out MoneyMovement, err error) {
	op := financialOp{typ: move.typ, walletIDs: []uuid.UUID{walletID}, amount: amount, key: idempotencyKey}
	defer func() { s.logOutcome(ctx, op, out, err) }()

	// A Wallet's Currency never changes, so reading it without a lock is safe.
	wallet, err := s.wallets.Get(ctx, walletID)
	if err != nil {
		return MoneyMovement{}, fmt.Errorf("%s: %w", move.name, err)
	}
	op.currency = wallet.Currency()
	money, err := s.parseAmount(amount, wallet.Currency())
	if err != nil {
		return MoneyMovement{}, fmt.Errorf("%s: %w", move.name, err)
	}
	hash := requestHash(move.typ, walletID, money)

	out, err = s.execute(ctx, idempotencyKey, hash, func(tx LedgerTx) (domain.TransactionDetail, error) {
		locked, err := tx.LockWallet(ctx, walletID)
		if err != nil {
			return domain.TransactionDetail{}, err
		}
		// Record before the business rules: a concurrent request with this key
		// that committed while we waited for the lock must be answered as a
		// replay (via ErrDuplicateIdempotencyKey), not judged against a Wallet
		// its own Transaction has since changed. A rule failure below still
		// rolls the Transaction back, so Rejections store nothing.
		newTx := domain.NewTransaction{
			Type:           move.typ,
			Amount:         money,
			IdempotencyKey: idempotencyKey,
			RequestHash:    hash,
		}
		entryAmount := money
		if move.incoming {
			newTx.DestinationWalletID = &walletID
		} else {
			newTx.SourceWalletID = &walletID
			entryAmount.Minor = -money.Minor
		}
		rec, err := tx.Record(ctx, newTx)
		if err != nil {
			return domain.TransactionDetail{}, err
		}
		moved, err := move.apply(locked, money)
		if err != nil {
			return domain.TransactionDetail{}, err
		}
		entry, err := tx.PostEntry(ctx, domain.NewLedgerEntry{
			TransactionID: rec.ID,
			WalletID:      walletID,
			Amount:        entryAmount,
			BalanceAfter:  moved.Balance,
		})
		if err != nil {
			return domain.TransactionDetail{}, err
		}
		return domain.TransactionDetail{Transaction: rec, Entries: []domain.LedgerEntry{entry}}, nil
	})
	if err != nil {
		return MoneyMovement{}, fmt.Errorf("%s: %w", move.name, err)
	}
	return out, nil
}

// execute is the money-moving flow every operation shares (ADR-0002). Inside
// one database transaction it first looks the Idempotency Key up: a stored
// Transaction with the same request hash is replayed, one with a different
// hash is a key reuse. Otherwise apply locks the Wallets, checks the business
// rules, and records the Transaction and Ledger Entries; any error it returns
// rolls everything back, so Rejections store nothing and the key stays free.
//
// If a concurrent request with the same key commits between the lookup and
// the insert, the insert fails with ErrDuplicateIdempotencyKey; execute then
// rolls back and answers from the winner's stored Transaction.
func (s *LedgerService) execute(ctx context.Context, key, hash string, apply func(LedgerTx) (domain.TransactionDetail, error)) (MoneyMovement, error) {
	var out MoneyMovement
	err := s.repo.InTx(ctx, func(tx LedgerTx) error {
		stored, found, err := tx.FindByIdempotencyKey(ctx, key)
		if err != nil {
			return err
		}
		if found {
			out, err = replay(stored, hash)
			return err
		}
		created, err := apply(tx)
		if err != nil {
			return err
		}
		out = MoneyMovement{Transaction: created}
		return nil
	})
	if !errors.Is(err, domain.ErrDuplicateIdempotencyKey) {
		return out, err
	}

	err = s.repo.InTx(ctx, func(tx LedgerTx) error {
		stored, found, err := tx.FindByIdempotencyKey(ctx, key)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("idempotency key reported in use but no transaction found: %w", domain.ErrDuplicateIdempotencyKey)
		}
		out, err = replay(stored, hash)
		return err
	})
	return out, err
}

// replay answers a request whose Idempotency Key already has a Transaction.
func replay(stored domain.TransactionDetail, hash string) (MoneyMovement, error) {
	if stored.RequestHash != hash {
		return MoneyMovement{}, domain.NewError(domain.CodeIdempotencyKeyReuse,
			"the Idempotency-Key was already used with a different request")
	}
	return MoneyMovement{Transaction: stored, Replayed: true}, nil
}

// financialOp describes a money-moving request for the financial-operation log.
type financialOp struct {
	typ       domain.TransactionType
	walletIDs []uuid.UUID
	amount    string // as requested, before parsing
	currency  domain.Currency
	key       string
}

// logOutcome writes the one "financial operation" line every money-moving
// request produces, so an operator can follow what happened to a Transaction:
// completed, replayed, rejected (a business rule refused it, with the reason),
// or failed (an unexpected error).
func (s *LedgerService) logOutcome(ctx context.Context, op financialOp, m MoneyMovement, err error) {
	attrs := []slog.Attr{
		slog.String("type", string(op.typ)),
		slog.String("idempotency_key", op.key),
	}
	if len(op.walletIDs) == 1 {
		attrs = append(attrs, slog.String("wallet_id", op.walletIDs[0].String()))
	}
	if op.currency != "" {
		attrs = append(attrs, slog.String("currency", string(op.currency)))
	}

	level, outcome := slog.LevelInfo, "completed"
	switch de, isDomain := domain.AsError(err); {
	case err == nil:
		attrs = append(attrs,
			slog.String("transaction_id", m.Transaction.ID.String()),
			slog.String("amount", m.Transaction.Amount.String()))
		if m.Replayed {
			outcome = "replayed"
		}
	case isDomain:
		level, outcome = slog.LevelWarn, "rejected"
		attrs = append(attrs, slog.String("amount", op.amount), slog.String("reason", string(de.Code)))
	default:
		level, outcome = slog.LevelError, "failed"
		attrs = append(attrs, slog.String("amount", op.amount), slog.String("error", err.Error()))
	}
	attrs = append(attrs, slog.String("outcome", outcome))
	s.logger.LogAttrs(ctx, level, "financial operation", attrs...)
}

// GetTransaction returns the Transaction with id and its Ledger Entries.
func (s *LedgerService) GetTransaction(ctx context.Context, id uuid.UUID) (domain.TransactionDetail, error) {
	d, err := s.repo.Get(ctx, id)
	if err != nil {
		return domain.TransactionDetail{}, fmt.Errorf("get transaction: %w", err)
	}
	return d, nil
}

// GetTransactionOfType is GetTransaction for a Transaction that must be of
// type t; one of any other type is reported as not found.
func (s *LedgerService) GetTransactionOfType(ctx context.Context, id uuid.UUID, t domain.TransactionType) (domain.TransactionDetail, error) {
	d, err := s.GetTransaction(ctx, id)
	if err != nil {
		return domain.TransactionDetail{}, err
	}
	if d.Type != t {
		return domain.TransactionDetail{}, fmt.Errorf("get transaction: %w",
			domain.NewError(domain.CodeTransactionNotFound, "no transaction of this type exists with this id"))
	}
	return d, nil
}

// parseAmount parses a request amount in currency c and checks it against the
// per-operation limit. Zero is rejected here because ParseMoney only rules out
// negative and malformed input.
func (s *LedgerService) parseAmount(amount string, c domain.Currency) (domain.Money, error) {
	m, err := domain.ParseMoney(amount, c)
	if err != nil {
		return domain.Money{}, domain.ValidationError(domain.FieldError{Field: "amount", Message: err.Error()})
	}
	if m.Minor == 0 {
		return domain.Money{}, domain.ValidationError(domain.FieldError{Field: "amount", Message: "must be greater than zero"})
	}
	if m.Minor > s.maxAmount {
		return domain.Money{}, domain.NewError(domain.CodeAmountExceedsLimit,
			fmt.Sprintf("the amount must not exceed %s per operation", domain.Money{Minor: s.maxAmount, Currency: c}))
	}
	return m, nil
}

// requestHash fingerprints a money-moving request over its canonical payload,
// so the same Idempotency Key with a different request can be told apart.
func requestHash(t domain.TransactionType, wallet uuid.UUID, amount domain.Money) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s|%s|%d|%s", t, wallet, amount.Minor, amount.Currency))
	return hex.EncodeToString(sum[:])
}
