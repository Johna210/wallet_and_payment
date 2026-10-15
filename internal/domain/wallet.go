package domain

import (
	"errors"
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

// ErrInvalidWalletStatus is returned for any string that is not a Wallet Status.
var ErrInvalidWalletStatus = errors.New("must be one of ACTIVE, FROZEN, CLOSED")

// ParseWalletStatus accepts an exact, upper-case Wallet Status.
func ParseWalletStatus(s string) (WalletStatus, error) {
	switch status := WalletStatus(s); status {
	case WalletActive, WalletFrozen, WalletClosed:
		return status, nil
	}
	return "", ErrInvalidWalletStatus
}

// WalletFilter narrows a Wallet listing. A nil field matches every value.
type WalletFilter struct {
	UserID   *uuid.UUID
	Status   *WalletStatus
	Currency *Currency
}

// WalletPosition marks where a Wallet listing page ended. Listings are ordered
// newest first on (CreatedAt, ID), so the next page holds Wallets strictly
// older than this position.
type WalletPosition struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// WalletPage is one page of a Wallet listing. Next is nil on the last page.
type WalletPage struct {
	Wallets []Wallet
	Next    *WalletPosition
}

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
