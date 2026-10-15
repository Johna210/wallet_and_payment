package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// TransactionType is the kind of financial operation a Transaction records.
type TransactionType string

const (
	TypeDeposit    TransactionType = "DEPOSIT"
	TypeWithdrawal TransactionType = "WITHDRAWAL"
	TypeTransfer   TransactionType = "TRANSFER"
)

// TransactionStatus is the state of a Transaction. Only completed operations
// are stored, so COMPLETED is the only value for now.
type TransactionStatus string

const StatusCompleted TransactionStatus = "COMPLETED"

// ErrDuplicateIdempotencyKey is returned by the store when a Transaction with
// the same Idempotency Key already exists, which means a concurrent request
// with that key committed first. It never reaches API clients: the service
// turns it into a replay or an idempotency_key_reuse error.
var ErrDuplicateIdempotencyKey = errors.New("idempotency key already used")

// Transaction is the record of one completed financial operation.
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

// TransactionDetail is a Transaction together with its Ledger Entries.
type TransactionDetail struct {
	Transaction
	Entries []LedgerEntry
}

// NewTransaction is a Transaction about to be recorded.
type NewTransaction struct {
	Type                TransactionType
	Amount              Money
	SourceWalletID      *uuid.UUID
	DestinationWalletID *uuid.UUID
	IdempotencyKey      string
	RequestHash         string
}

// NewLedgerEntry is a Ledger Entry about to be recorded.
type NewLedgerEntry struct {
	TransactionID uuid.UUID
	WalletID      uuid.UUID
	Amount        Money
	BalanceAfter  Money
}
