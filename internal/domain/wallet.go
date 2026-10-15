package domain

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
)

type WalletStatus string

const (
	WalletActive WalletStatus = "ACTIVE"
	WalletFrozen WalletStatus = "FROZEN"
	WalletClosed WalletStatus = "CLOSED"
)

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

type Wallet struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Balance   Money
	Status    WalletStatus
	CreatedAt time.Time
	UpdatedAt time.Time
}

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

// Credit adds amount to the Balance. Only an Active Wallet can receive money,
// and the Balance may not overflow int64.
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

// Transfer moves amount from one Wallet to another. The Wallets must be
// distinct, share a Currency and be Active, and the source must hold at least amount.
func Transfer(source, destination Wallet, amount Money) (Wallet, Wallet, error) {
	if source.ID == destination.ID {
		return Wallet{}, Wallet{}, NewError(CodeSameWalletTransfer, "the source and destination wallets must differ")
	}
	if source.Currency() != destination.Currency() {
		return Wallet{}, Wallet{}, NewError(CodeCurrencyMismatch,
			fmt.Sprintf("cannot transfer from a %s wallet to a %s wallet", source.Currency(), destination.Currency()))
	}
	// The destination is checked first so that an inactive Wallet on either
	// side is reported before the source's Balance is considered.
	credited, err := destination.Credit(amount)
	if err != nil {
		return Wallet{}, Wallet{}, err
	}
	debited, err := source.Debit(amount)
	if err != nil {
		return Wallet{}, Wallet{}, err
	}
	return debited, credited, nil
}

func canTransition(from, to WalletStatus) bool {
	switch from {
	case WalletActive:
		return to == WalletFrozen || to == WalletClosed
	case WalletFrozen:
		return to == WalletActive || to == WalletClosed
	}
	return false
}
