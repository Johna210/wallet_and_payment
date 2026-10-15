// Package service holds the use cases and owns transaction boundaries.
package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/Johna210/wallet_and_payment/internal/domain"
)

type WalletRepository interface {
	Create(ctx context.Context, userID uuid.UUID, currency domain.Currency) (domain.Wallet, error)
	Get(ctx context.Context, id uuid.UUID) (domain.Wallet, error)
	List(ctx context.Context, filter domain.WalletFilter, after *domain.WalletPosition, limit int) ([]domain.Wallet, error)
	// UpdateLocked applies change to the Wallet under its row lock.
	UpdateLocked(ctx context.Context, id uuid.UUID, change func(domain.Wallet) (domain.Wallet, error)) (domain.Wallet, error)
}

type WalletService struct {
	repo WalletRepository
}

func NewWalletService(repo WalletRepository) *WalletService {
	return &WalletService{repo: repo}
}

func (s *WalletService) Create(ctx context.Context, userID uuid.UUID, currency domain.Currency) (domain.Wallet, error) {
	w, err := s.repo.Create(ctx, userID, currency)
	if err != nil {
		return domain.Wallet{}, fmt.Errorf("create wallet: %w", err)
	}
	return w, nil
}

func (s *WalletService) Get(ctx context.Context, id uuid.UUID) (domain.Wallet, error) {
	w, err := s.repo.Get(ctx, id)
	if err != nil {
		return domain.Wallet{}, fmt.Errorf("get wallet: %w", err)
	}
	return w, nil
}

// ChangeStatus runs under the Wallet's row lock so the Balance checked when
// closing cannot change before the status does.
func (s *WalletService) ChangeStatus(ctx context.Context, id uuid.UUID, status domain.WalletStatus) (domain.Wallet, error) {
	w, err := s.repo.UpdateLocked(ctx, id, func(current domain.Wallet) (domain.Wallet, error) {
		return current.TransitionTo(status)
	})
	if err != nil {
		return domain.Wallet{}, fmt.Errorf("change wallet status: %w", err)
	}
	return w, nil
}

func (s *WalletService) List(ctx context.Context, filter domain.WalletFilter, after *domain.WalletPosition, limit int) (domain.WalletPage, error) {
	if limit < 1 {
		return domain.WalletPage{}, fmt.Errorf("list wallets: limit must be positive, got %d", limit)
	}
	// Ask for one extra row: its presence is how we know another page exists.
	ws, err := s.repo.List(ctx, filter, after, limit+1)
	if err != nil {
		return domain.WalletPage{}, fmt.Errorf("list wallets: %w", err)
	}
	if len(ws) <= limit {
		return domain.WalletPage{Wallets: ws}, nil
	}
	ws = ws[:limit]
	last := ws[limit-1]
	return domain.WalletPage{Wallets: ws, Next: &domain.WalletPosition{CreatedAt: last.CreatedAt, ID: last.ID}}, nil
}
