package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/Johna210/wallet_and_payment/internal/domain"
)

// WalletService is the Wallet use cases the transport calls.
type WalletService interface {
	Create(ctx context.Context, userID uuid.UUID, currency domain.Currency) (domain.Wallet, error)
	Get(ctx context.Context, id uuid.UUID) (domain.Wallet, error)
}

type walletResponse struct {
	ID        uuid.UUID           `json:"id"`
	UserID    uuid.UUID           `json:"user_id"`
	Currency  domain.Currency     `json:"currency"`
	Balance   string              `json:"balance"`
	Status    domain.WalletStatus `json:"status"`
	CreatedAt time.Time           `json:"created_at"`
	UpdatedAt time.Time           `json:"updated_at"`
}

func newWalletResponse(w domain.Wallet) walletResponse {
	return walletResponse{
		ID:        w.ID,
		UserID:    w.UserID,
		Currency:  w.Currency(),
		Balance:   w.Balance.String(),
		Status:    w.Status,
		CreatedAt: w.CreatedAt.UTC(),
		UpdatedAt: w.UpdatedAt.UTC(),
	}
}

type createWalletRequest struct {
	UserID   string `json:"user_id"`
	Currency string `json:"currency"`
}

func handleCreateWallet(svc WalletService, ew errorWriter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createWalletRequest
		if err := decodeJSON(r, &req); err != nil {
			ew.write(w, r, err)
			return
		}

		var fields []domain.FieldError
		userID, ok := parseUUID(req.UserID)
		switch {
		case req.UserID == "":
			fields = append(fields, domain.FieldError{Field: "user_id", Message: "is required"})
		case !ok:
			fields = append(fields, domain.FieldError{Field: "user_id", Message: "must be a UUID"})
		}
		currency, err := domain.ParseCurrency(req.Currency)
		switch {
		case req.Currency == "":
			fields = append(fields, domain.FieldError{Field: "currency", Message: "is required"})
		case err != nil:
			fields = append(fields, domain.FieldError{Field: "currency", Message: err.Error()})
		}
		if len(fields) > 0 {
			ew.write(w, r, domain.ValidationError(fields...))
			return
		}

		wallet, err := svc.Create(r.Context(), userID, currency)
		if err != nil {
			ew.write(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, newWalletResponse(wallet))
	}
}

func handleGetWallet(svc WalletService, ew errorWriter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseUUID(r.PathValue("id"))
		if !ok {
			ew.write(w, r, domain.ValidationError(domain.FieldError{Field: "id", Message: "must be a UUID"}))
			return
		}
		wallet, err := svc.Get(r.Context(), id)
		if err != nil {
			ew.write(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, newWalletResponse(wallet))
	}
}

// parseUUID accepts only the canonical hyphenated 36-character form, not the
// braced, URN, or hyphen-less forms uuid.Parse also allows.
func parseUUID(s string) (uuid.UUID, bool) {
	if len(s) != 36 {
		return uuid.UUID{}, false
	}
	id, err := uuid.Parse(s)
	return id, err == nil
}
