package domain

import (
	"time"

	"github.com/google/uuid"
)

// WalletStatus is the lifecycle state of a Wallet.
type WalletStatus string

const (
	WalletActive WalletStatus = "ACTIVE"
	WalletFrozen WalletStatus = "FROZEN"
	WalletClosed WalletStatus = "CLOSED"
)

// Wallet is a store of value owned by one User in exactly one Currency.
type Wallet struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Balance   Money
	Status    WalletStatus
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Currency is the Wallet's Currency, carried by its Balance.
func (w Wallet) Currency() Currency { return w.Balance.Currency }
