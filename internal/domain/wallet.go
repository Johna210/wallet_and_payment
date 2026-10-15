package domain

import (
	"errors"
	"fmt"
	"math"
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

// TransitionTo returns the Wallet moved to the status to. Moving to the
// current status is a no-op. Otherwise the move must be Active↔Frozen or
// either into Closed; Closed is terminal, and a Wallet may only be Closed at
// zero Balance.
func (w Wallet) TransitionTo(to WalletStatus) (Wallet, error) {
	if to == w.Status {
		return w, nil
	}
	if !canTransition(w.Status, to) {
		return Wallet{}, NewError(CodeInvalidStatusTransition,
			fmt.Sprintf("a %s wallet cannot become %s", w.Status, to))
	}
	if to == WalletClosed && w.Balance.Minor != 0 {
		return Wallet{}, NewError(CodeWalletBalanceNotZero, "a wallet can only be closed when its balance is zero")
	}
	w.Status = to
	return w, nil
}

// Credit returns the Wallet with amount added to its Balance. Only an Active
// Wallet can receive money, and the new Balance must fit in int64 rather than
// wrap.
func (w Wallet) Credit(amount Money) (Wallet, error) {
	if w.Status != WalletActive {
		return Wallet{}, NewError(CodeWalletNotActive,
			fmt.Sprintf("a %s wallet cannot receive money", w.Status))
	}
	if amount.Minor > math.MaxInt64-w.Balance.Minor {
		return Wallet{}, NewError(CodeAmountExceedsLimit, "the amount would overflow the wallet balance")
	}
	w.Balance.Minor += amount.Minor
	return w, nil
}

// Debit returns the Wallet with amount taken from its Balance. Only an Active
// Wallet can send money, and the Balance may never go below zero.
func (w Wallet) Debit(amount Money) (Wallet, error) {
	if w.Status != WalletActive {
		return Wallet{}, NewError(CodeWalletNotActive,
			fmt.Sprintf("a %s wallet cannot send money", w.Status))
	}
	if amount.Minor > w.Balance.Minor {
		return Wallet{}, NewError(CodeInsufficientFunds, "the wallet balance is less than the amount")
	}
	w.Balance.Minor -= amount.Minor
	return w, nil
}

// canTransition is the Wallet Status transition table for distinct statuses.
func canTransition(from, to WalletStatus) bool {
	switch from {
	case WalletActive:
		return to == WalletFrozen || to == WalletClosed
	case WalletFrozen:
		return to == WalletActive || to == WalletClosed
	}
	return false
}
