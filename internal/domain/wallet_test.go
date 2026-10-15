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

func TestTransfer(t *testing.T) {
	const (
		A = WalletActive
		F = WalletFrozen
		C = WalletClosed
	)
	cases := []struct {
		name         string
		srcStatus    WalletStatus
		srcBalance   int64
		dstStatus    WalletStatus
		dstBalance   int64
		dstCurrency  Currency
		amount       int64
		wantSrc      int64
		wantDst      int64
		wantCode     Code // empty means the transfer is allowed
		sameWalletID bool
	}{
		{name: "moves money", srcStatus: A, srcBalance: 1000, dstStatus: A, dstBalance: 50, amount: 400, wantSrc: 600, wantDst: 450},
		{name: "exact balance leaves source at zero", srcStatus: A, srcBalance: 1000, dstStatus: A, amount: 1000, wantSrc: 0, wantDst: 1000},
		{name: "one over the balance", srcStatus: A, srcBalance: 1000, dstStatus: A, amount: 1001, wantCode: CodeInsufficientFunds},
		{name: "frozen source", srcStatus: F, srcBalance: 1000, dstStatus: A, amount: 1, wantCode: CodeWalletNotActive},
		{name: "closed source", srcStatus: C, srcBalance: 0, dstStatus: A, amount: 1, wantCode: CodeWalletNotActive},
		{name: "frozen destination", srcStatus: A, srcBalance: 1000, dstStatus: F, amount: 1, wantCode: CodeWalletNotActive},
		{name: "closed destination", srcStatus: A, srcBalance: 1000, dstStatus: C, amount: 1, wantCode: CodeWalletNotActive},
		{name: "inactive destination is reported before insufficient funds", srcStatus: A, srcBalance: 0, dstStatus: F, amount: 1, wantCode: CodeWalletNotActive},
		{name: "different currencies", srcStatus: A, srcBalance: 1000, dstStatus: A, dstCurrency: "USD", amount: 1, wantCode: CodeCurrencyMismatch},
		{name: "currency mismatch is reported before insufficient funds", srcStatus: A, srcBalance: 0, dstStatus: A, dstCurrency: "USD", amount: 1, wantCode: CodeCurrencyMismatch},
		{name: "same wallet", srcStatus: A, srcBalance: 1000, dstStatus: A, amount: 1, sameWalletID: true, wantCode: CodeSameWalletTransfer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := walletWith(tc.srcStatus, tc.srcBalance)
			dst := walletWith(tc.dstStatus, tc.dstBalance)
			if tc.dstCurrency != "" {
				dst.Balance.Currency = tc.dstCurrency
			}
			if tc.sameWalletID {
				dst.ID = src.ID
			}

			gotSrc, gotDst, err := Transfer(src, dst, Money{Minor: tc.amount, Currency: "ETB"})

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
			if gotSrc.Balance.Minor != tc.wantSrc || gotDst.Balance.Minor != tc.wantDst {
				t.Errorf("balances = %d, %d; want %d, %d", gotSrc.Balance.Minor, gotDst.Balance.Minor, tc.wantSrc, tc.wantDst)
			}
			if got := gotSrc.Balance.Minor + gotDst.Balance.Minor; got != tc.srcBalance+tc.dstBalance {
				t.Errorf("total after = %d, want %d: a transfer neither creates nor destroys money", got, tc.srcBalance+tc.dstBalance)
			}
			if src.Balance.Minor != tc.srcBalance || dst.Balance.Minor != tc.dstBalance {
				t.Errorf("Transfer mutated its arguments")
			}
		})
	}
}
