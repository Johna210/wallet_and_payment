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
