package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func walletWith(status WalletStatus, minor int64) Wallet {
	return Wallet{
		ID:        uuid.New(),
		UserID:    uuid.New(),
		Balance:   Money{Minor: minor, Currency: "ETB"},
		Status:    status,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestWalletTransitionTo(t *testing.T) {
	const (
		A = WalletActive
		F = WalletFrozen
		C = WalletClosed
	)
	cases := []struct {
		name     string
		from, to WalletStatus
		minor    int64
		wantCode Code // empty means the transition is allowed
	}{
		{"freeze", A, F, 0, ""},
		{"freeze with balance", A, F, 500, ""},
		{"reactivate", F, A, 0, ""},
		{"reactivate with balance", F, A, 500, ""},
		{"close active", A, C, 0, ""},
		{"close frozen", F, C, 0, ""},

		{"close active with balance", A, C, 500, CodeWalletBalanceNotZero},
		{"close frozen with balance", F, C, 500, CodeWalletBalanceNotZero},

		{"reopen closed as active", C, A, 0, CodeInvalidStatusTransition},
		{"reopen closed as frozen", C, F, 0, CodeInvalidStatusTransition},

		{"active to active", A, A, 500, ""},
		{"frozen to frozen", F, F, 500, ""},
		{"closed to closed", C, C, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := walletWith(tc.from, tc.minor)

			got, err := w.TransitionTo(tc.to)

			if tc.wantCode != "" {
				de, ok := AsError(err)
				if !ok || de.Code != tc.wantCode {
					t.Fatalf("err = %v, want code %s", err, tc.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Status != tc.to {
				t.Errorf("status = %s, want %s", got.Status, tc.to)
			}
			if got.ID != w.ID || got.UserID != w.UserID || got.Balance != w.Balance || !got.CreatedAt.Equal(w.CreatedAt) {
				t.Errorf("transition changed more than the status: %+v -> %+v", w, got)
			}
		})
	}
}

func TestWalletTransitionToRejectsUnknownStatus(t *testing.T) {
	_, err := walletWith(WalletActive, 0).TransitionTo("SUSPENDED")
	if de, ok := AsError(err); !ok || de.Code != CodeInvalidStatusTransition {
		t.Fatalf("err = %v, want code %s", err, CodeInvalidStatusTransition)
	}
}

func TestWalletDebit(t *testing.T) {
	cases := []struct {
		name        string
		status      WalletStatus
		balance     int64
		amount      int64
		wantBalance int64
		wantCode    Code // empty means the debit is allowed
	}{
		{"partial", WalletActive, 1000, 400, 600, ""},
		{"exact balance leaves zero", WalletActive, 1000, 1000, 0, ""},
		{"one over the balance", WalletActive, 1000, 1001, 0, CodeInsufficientFunds},
		{"empty wallet", WalletActive, 0, 1, 0, CodeInsufficientFunds},
		{"frozen", WalletFrozen, 1000, 1, 0, CodeWalletNotActive},
		{"closed", WalletClosed, 0, 1, 0, CodeWalletNotActive},
		{"frozen is reported before insufficient funds", WalletFrozen, 0, 1, 0, CodeWalletNotActive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := walletWith(tc.status, tc.balance)

			got, err := w.Debit(Money{Minor: tc.amount, Currency: "ETB"})

			if tc.wantCode != "" {
				de, ok := AsError(err)
				if !ok || de.Code != tc.wantCode {
					t.Fatalf("err = %v, want code %s", err, tc.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Balance.Minor != tc.wantBalance {
				t.Errorf("balance = %d, want %d", got.Balance.Minor, tc.wantBalance)
			}
			if w.Balance.Minor != tc.balance {
				t.Errorf("Debit mutated the receiver: balance = %d, want %d", w.Balance.Minor, tc.balance)
			}
		})
	}
}
