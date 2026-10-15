package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

type TransactionType string

const (
	TypeDeposit    TransactionType = "DEPOSIT"
	TypeWithdrawal TransactionType = "WITHDRAWAL"
	TypeTransfer   TransactionType = "TRANSFER"
)

var ErrInvalidTransactionType = errors.New("must be one of DEPOSIT, WITHDRAWAL, TRANSFER")

// ParseTransactionType accepts an exact, upper-case Transaction type.
func ParseTransactionType(s string) (TransactionType, error) {
	switch t := TransactionType(s); t {
	case TypeDeposit, TypeWithdrawal, TypeTransfer:
		return t, nil
	}
	return "", ErrInvalidTransactionType
}

// TransactionStatus is the state of a Transaction. Only completed operations
// are stored, so COMPLETED is the only value for now.
type TransactionStatus string

const StatusCompleted TransactionStatus = "COMPLETED"

// ErrDuplicateIdempotencyKey is returned by the store when a Transaction with
// the same Idempotency Key already exists, which means a concurrent request
// with that key committed first. It never reaches API clients: the service
// turns it into a replay or an idempotency_key_reuse error.
var ErrDuplicateIdempotencyKey = errors.New("idempotency key already used")

type Transaction struct {
	ID                  uuid.UUID
	Type                TransactionType
	Status              TransactionStatus
	Amount              Money
	SourceWalletID      *uuid.UUID
	DestinationWalletID *uuid.UUID
	IdempotencyKey      string
	RequestHash         string
	CreatedAt           time.Time
}

// LedgerEntry is a signed change to one Wallet's Balance made by a Transaction.
// Amount is negative for money leaving the Wallet.
type LedgerEntry struct {
	ID            uuid.UUID
	TransactionID uuid.UUID
	WalletID      uuid.UUID
	Amount        Money
	BalanceAfter  Money
	CreatedAt     time.Time
}

// TransactionFilter narrows a Transaction listing. A nil field matches every
// value. From and To bound the creation time and are both inclusive.
type TransactionFilter struct {
	Type     *TransactionType
	Currency *Currency
	From     *time.Time
	To       *time.Time
}

// TransactionPosition marks where a Transaction listing page ended. Listings
// are ordered newest first on (CreatedAt, ID), so the next page holds
// Transactions strictly older than this position.
type TransactionPosition struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// TransactionPage is one page of a listing; Next is nil on the last page.
type TransactionPage struct {
	Transactions []Transaction
	Next         *TransactionPosition
}

type TransactionDetail struct {
	Transaction
	Entries []LedgerEntry
}

type NewTransaction struct {
	Type                TransactionType
	Amount              Money
	SourceWalletID      *uuid.UUID
	DestinationWalletID *uuid.UUID
	IdempotencyKey      string
	RequestHash         string
}

type NewLedgerEntry struct {
	TransactionID uuid.UUID
	WalletID      uuid.UUID
	Amount        Money
	BalanceAfter  Money
}
