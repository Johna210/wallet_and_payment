package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/Johna210/wallet_and_payment/internal/domain"
)

// WalletService is the Wallet use cases the transport calls.
type WalletService interface {
	Create(ctx context.Context, userID uuid.UUID, currency domain.Currency) (domain.Wallet, error)
	Get(ctx context.Context, id uuid.UUID) (domain.Wallet, error)
	List(ctx context.Context, filter domain.WalletFilter, after *domain.WalletPosition, limit int) (domain.WalletPage, error)
	ChangeStatus(ctx context.Context, id uuid.UUID, status domain.WalletStatus) (domain.Wallet, error)
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

type changeWalletStatusRequest struct {
	Status string `json:"status"`
}

func handleChangeWalletStatus(svc WalletService, ew errorWriter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseUUID(r.PathValue("id"))
		if !ok {
			ew.write(w, r, domain.ValidationError(domain.FieldError{Field: "id", Message: "must be a UUID"}))
			return
		}
		var req changeWalletStatusRequest
		if err := decodeJSON(r, &req); err != nil {
			ew.write(w, r, err)
			return
		}
		status, err := domain.ParseWalletStatus(req.Status)
		switch {
		case req.Status == "":
			ew.write(w, r, domain.ValidationError(domain.FieldError{Field: "status", Message: "is required"}))
			return
		case err != nil:
			ew.write(w, r, domain.ValidationError(domain.FieldError{Field: "status", Message: err.Error()}))
			return
		}

		wallet, err := svc.ChangeStatus(r.Context(), id, status)
		if err != nil {
			ew.write(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, newWalletResponse(wallet))
	}
}

type walletListResponse struct {
	Data       []walletResponse `json:"data"`
	NextCursor *string          `json:"next_cursor"`
}

func handleListWallets(svc WalletService, ew errorWriter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q, err := parseWalletListQuery(r.URL.Query())
		if err != nil {
			ew.write(w, r, err)
			return
		}
		page, err := svc.List(r.Context(), q.filter, q.after, q.limit)
		if err != nil {
			ew.write(w, r, err)
			return
		}
		resp := walletListResponse{Data: make([]walletResponse, len(page.Wallets))}
		for i, wallet := range page.Wallets {
			resp.Data[i] = newWalletResponse(wallet)
		}
		if page.Next != nil {
			cursor := encodeWalletCursor(*page.Next)
			resp.NextCursor = &cursor
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// Page sizes for listings. A limit above maxPageSize is rejected with
// validation_failed rather than clamped, so a client never silently receives
// fewer rows than it asked for.
const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// walletListQuery is a parsed GET /v1/wallets query string.
type walletListQuery struct {
	filter domain.WalletFilter
	after  *domain.WalletPosition
	limit  int
}

// parseWalletListQuery reads user_id, status, currency, limit, and cursor,
// reporting every invalid parameter at once.
func parseWalletListQuery(q url.Values) (walletListQuery, error) {
	var (
		out    = walletListQuery{limit: defaultPageSize}
		fields []domain.FieldError
	)
	invalid := func(field, message string) {
		fields = append(fields, domain.FieldError{Field: field, Message: message})
	}

	if q.Has("user_id") {
		if id, ok := parseUUID(q.Get("user_id")); ok {
			out.filter.UserID = &id
		} else {
			invalid("user_id", "must be a UUID")
		}
	}
	if q.Has("status") {
		if status, err := domain.ParseWalletStatus(q.Get("status")); err == nil {
			out.filter.Status = &status
		} else {
			invalid("status", err.Error())
		}
	}
	if q.Has("currency") {
		if currency, err := domain.ParseCurrency(q.Get("currency")); err == nil {
			out.filter.Currency = &currency
		} else {
			invalid("currency", err.Error())
		}
	}
	if q.Has("limit") {
		if n, ok := parsePageSize(q.Get("limit")); ok {
			out.limit = n
		} else {
			invalid("limit", fmt.Sprintf("must be a whole number from 1 to %d", maxPageSize))
		}
	}
	if q.Has("cursor") {
		if pos, err := decodeWalletCursor(q.Get("cursor")); err == nil {
			out.after = &pos
		} else {
			invalid("cursor", err.Error())
		}
	}

	if len(fields) > 0 {
		return walletListQuery{}, domain.ValidationError(fields...)
	}
	return out, nil
}

// parsePageSize accepts only plain ASCII digits (no sign, no spaces) in 1..maxPageSize.
func parsePageSize(s string) (int, bool) {
	if s == "" || len(s) > 9 {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > maxPageSize {
		return 0, false
	}
	return n, true
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
