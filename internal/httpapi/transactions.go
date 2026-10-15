package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/Johna210/wallet_and_payment/internal/domain"
	"github.com/Johna210/wallet_and_payment/internal/service"
)

const (
	idempotencyKeyHeader     = "Idempotency-Key"
	idempotentReplayedHeader = "Idempotent-Replayed"
	maxIdempotencyKeyLength  = 255
)

// LedgerService is the money-moving and Transaction use cases the transport calls.
type LedgerService interface {
	Deposit(ctx context.Context, walletID uuid.UUID, amount, idempotencyKey string) (service.MoneyMovement, error)
	Withdraw(ctx context.Context, walletID uuid.UUID, amount, idempotencyKey string) (service.MoneyMovement, error)
	Transfer(ctx context.Context, sourceID, destinationID uuid.UUID, amount, idempotencyKey string) (service.MoneyMovement, error)
	GetTransaction(ctx context.Context, id uuid.UUID) (domain.TransactionDetail, error)
	GetTransactionOfType(ctx context.Context, id uuid.UUID, t domain.TransactionType) (domain.TransactionDetail, error)
}

type ledgerEntryResponse struct {
	ID           uuid.UUID `json:"id"`
	WalletID     uuid.UUID `json:"wallet_id"`
	Amount       string    `json:"amount"`
	BalanceAfter string    `json:"balance_after"`
	CreatedAt    time.Time `json:"created_at"`
}

type transactionResponse struct {
	ID                  uuid.UUID                `json:"id"`
	Type                domain.TransactionType   `json:"type"`
	Status              domain.TransactionStatus `json:"status"`
	Amount              string                   `json:"amount"`
	Currency            domain.Currency          `json:"currency"`
	SourceWalletID      *uuid.UUID               `json:"source_wallet_id"`
	DestinationWalletID *uuid.UUID               `json:"destination_wallet_id"`
	IdempotencyKey      string                   `json:"idempotency_key"`
	CreatedAt           time.Time                `json:"created_at"`
	LedgerEntries       []ledgerEntryResponse    `json:"ledger_entries"`
}

func newTransactionResponse(d domain.TransactionDetail) transactionResponse {
	entries := make([]ledgerEntryResponse, len(d.Entries))
	for i, e := range d.Entries {
		entries[i] = ledgerEntryResponse{
			ID:           e.ID,
			WalletID:     e.WalletID,
			Amount:       e.Amount.String(),
			BalanceAfter: e.BalanceAfter.String(),
			CreatedAt:    e.CreatedAt.UTC(),
		}
	}
	return transactionResponse{
		ID:                  d.ID,
		Type:                d.Type,
		Status:              d.Status,
		Amount:              d.Amount.String(),
		Currency:            d.Amount.Currency,
		SourceWalletID:      d.SourceWalletID,
		DestinationWalletID: d.DestinationWalletID,
		IdempotencyKey:      d.IdempotencyKey,
		CreatedAt:           d.CreatedAt.UTC(),
		LedgerEntries:       entries,
	}
}

// movementResponse is a Transaction plus the Balance it left the request's
// Wallet with, as returned by the money-moving endpoints.
type movementResponse struct {
	transactionResponse
	Balance string `json:"balance"`
}

// writeMovement answers a money-moving request: 201 for a new Transaction, 200
// with Idempotent-Replayed for a replay. balanceWallet is the Wallet whose
// resulting Balance the response reports.
func writeMovement(w http.ResponseWriter, m service.MoneyMovement, balanceWallet uuid.UUID) {
	resp := movementResponse{transactionResponse: newTransactionResponse(m.Transaction)}
	for _, e := range m.Transaction.Entries {
		if e.WalletID == balanceWallet {
			resp.Balance = e.BalanceAfter.String()
		}
	}
	status := http.StatusCreated
	if m.Replayed {
		w.Header().Set(idempotentReplayedHeader, "true")
		status = http.StatusOK
	}
	writeJSON(w, status, resp)
}

// idempotencyKey reads the required Idempotency-Key header.
func idempotencyKey(r *http.Request) (string, error) {
	key := r.Header.Get(idempotencyKeyHeader)
	if key == "" {
		return "", domain.NewError(domain.CodeIdempotencyKeyRequired,
			"the Idempotency-Key header is required for this request")
	}
	if len(key) > maxIdempotencyKeyLength || !printableASCII(key) {
		return "", domain.ValidationError(domain.FieldError{
			Field:   idempotencyKeyHeader,
			Message: "must be 1 to 255 printable ASCII characters",
		})
	}
	return key, nil
}

func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

type amountRequest struct {
	Amount string `json:"amount"`
}

// handleDeposit and handleWithdrawal serve the single-Wallet money-moving
// endpoints, which differ only in the use case they call.
func handleDeposit(svc LedgerService, ew errorWriter) http.HandlerFunc {
	return handleWalletMovement(ew, func(ctx context.Context, id uuid.UUID, amount, key string) (service.MoneyMovement, error) {
		return svc.Deposit(ctx, id, amount, key)
	})
}

func handleWithdrawal(svc LedgerService, ew errorWriter) http.HandlerFunc {
	return handleWalletMovement(ew, func(ctx context.Context, id uuid.UUID, amount, key string) (service.MoneyMovement, error) {
		return svc.Withdraw(ctx, id, amount, key)
	})
}

func handleWalletMovement(ew errorWriter, move func(ctx context.Context, walletID uuid.UUID, amount, idempotencyKey string) (service.MoneyMovement, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, err := idempotencyKey(r)
		if err != nil {
			ew.write(w, r, err)
			return
		}
		id, ok := parseUUID(r.PathValue("id"))
		if !ok {
			ew.write(w, r, domain.ValidationError(domain.FieldError{Field: "id", Message: "must be a UUID"}))
			return
		}
		var req amountRequest
		if err := decodeJSON(r, &req); err != nil {
			ew.write(w, r, err)
			return
		}
		if req.Amount == "" {
			ew.write(w, r, domain.ValidationError(domain.FieldError{Field: "amount", Message: "is required"}))
			return
		}

		m, err := move(r.Context(), id, req.Amount, key)
		if err != nil {
			ew.write(w, r, err)
			return
		}
		writeMovement(w, m, id)
	}
}

type transferRequest struct {
	SourceWalletID      string `json:"source_wallet_id"`
	DestinationWalletID string `json:"destination_wallet_id"`
	Amount              string `json:"amount"`
}

// handleTransfer serves POST /v1/transfers. The response's balance is the
// source Wallet's, the Wallet the request spends from.
func handleTransfer(svc LedgerService, ew errorWriter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, err := idempotencyKey(r)
		if err != nil {
			ew.write(w, r, err)
			return
		}
		var req transferRequest
		if err := decodeJSON(r, &req); err != nil {
			ew.write(w, r, err)
			return
		}
		var fields []domain.FieldError
		source, ok := parseUUID(req.SourceWalletID)
		if !ok {
			fields = append(fields, domain.FieldError{Field: "source_wallet_id", Message: "must be a UUID"})
		}
		destination, ok := parseUUID(req.DestinationWalletID)
		if !ok {
			fields = append(fields, domain.FieldError{Field: "destination_wallet_id", Message: "must be a UUID"})
		}
		if req.Amount == "" {
			fields = append(fields, domain.FieldError{Field: "amount", Message: "is required"})
		}
		if len(fields) > 0 {
			ew.write(w, r, domain.ValidationError(fields...))
			return
		}

		m, err := svc.Transfer(r.Context(), source, destination, req.Amount, key)
		if err != nil {
			ew.write(w, r, err)
			return
		}
		writeMovement(w, m, source)
	}
}

func handleGetTransaction(svc LedgerService, ew errorWriter) http.HandlerFunc {
	return transactionGetter(ew, func(ctx context.Context, id uuid.UUID) (domain.TransactionDetail, error) {
		return svc.GetTransaction(ctx, id)
	})
}

// handleGetTransactionOfType serves the typed read endpoints, which answer 404
// for a Transaction of any other type.
func handleGetTransactionOfType(svc LedgerService, ew errorWriter, t domain.TransactionType) http.HandlerFunc {
	return transactionGetter(ew, func(ctx context.Context, id uuid.UUID) (domain.TransactionDetail, error) {
		return svc.GetTransactionOfType(ctx, id, t)
	})
}

func transactionGetter(ew errorWriter, load func(context.Context, uuid.UUID) (domain.TransactionDetail, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseUUID(r.PathValue("id"))
		if !ok {
			ew.write(w, r, domain.ValidationError(domain.FieldError{Field: "id", Message: "must be a UUID"}))
			return
		}
		d, err := load(r.Context(), id)
		if err != nil {
			ew.write(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, newTransactionResponse(d))
	}
}
