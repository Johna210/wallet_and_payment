package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Johna210/wallet_and_payment/internal/domain"
)

// Transport-level error codes. Business-rule codes live in the domain package.
const (
	codeRouteNotFound    domain.Code = "route_not_found"
	codeMethodNotAllowed domain.Code = "method_not_allowed"
	codeInvalidJSON      domain.Code = "invalid_json"
	codePayloadTooLarge  domain.Code = "payload_too_large"
	codeRequestTimeout   domain.Code = "request_timeout"
	codeNotReady         domain.Code = "not_ready"
	codeInternal         domain.Code = "internal_error"
)

// statusByCode is the single place that decides which HTTP status each error
// code is exposed with. A code missing here is treated as an internal error.
var statusByCode = map[domain.Code]int{
	domain.CodeValidationFailed:        http.StatusBadRequest,
	domain.CodeWalletNotFound:          http.StatusNotFound,
	domain.CodeTransactionNotFound:     http.StatusNotFound,
	domain.CodeWalletAlreadyExists:     http.StatusConflict,
	domain.CodeInsufficientFunds:       http.StatusUnprocessableEntity,
	domain.CodeWalletNotActive:         http.StatusUnprocessableEntity,
	domain.CodeCurrencyMismatch:        http.StatusUnprocessableEntity,
	domain.CodeSameWalletTransfer:      http.StatusUnprocessableEntity,
	domain.CodeInvalidStatusTransition: http.StatusUnprocessableEntity,
	domain.CodeWalletBalanceNotZero:    http.StatusUnprocessableEntity,
	domain.CodeAmountExceedsLimit:      http.StatusUnprocessableEntity,
	domain.CodeIdempotencyKeyRequired:  http.StatusBadRequest,
	domain.CodeIdempotencyKeyReuse:     http.StatusConflict,

	codeRouteNotFound:    http.StatusNotFound,
	codeMethodNotAllowed: http.StatusMethodNotAllowed,
	codeInvalidJSON:      http.StatusBadRequest,
	codePayloadTooLarge:  http.StatusRequestEntityTooLarge,
	codeRequestTimeout:   http.StatusGatewayTimeout,
	codeNotReady:         http.StatusServiceUnavailable,
}

// problem is an RFC 9457 application/problem+json body with the service's
// extension members.
type problem struct {
	Type      string              `json:"type"`
	Title     string              `json:"title"`
	Status    int                 `json:"status"`
	Detail    string              `json:"detail"`
	Code      domain.Code         `json:"code"`
	RequestID string              `json:"request_id"`
	Errors    []domain.FieldError `json:"errors,omitempty"`
}

// errorWriter turns any error into a problem response. Domain errors expose
// their code and message; anything else is logged in full and reported as a
// generic internal error so internals never leak.
type errorWriter struct {
	logger *slog.Logger
}

func (ew errorWriter) write(w http.ResponseWriter, r *http.Request, err error) {
	de, ok := domain.AsError(err)
	switch {
	case ok:
	case errors.Is(err, context.DeadlineExceeded):
		de = domain.NewError(codeRequestTimeout, "the request took too long to complete")
	default:
		ew.logger.ErrorContext(r.Context(), "internal error",
			slog.String("request_id", RequestIDFrom(r.Context())),
			slog.String("error", err.Error()))
		de = domain.NewError(codeInternal, "an unexpected error occurred")
	}

	status, known := statusByCode[de.Code]
	if !known {
		ew.logger.ErrorContext(r.Context(), "error code has no HTTP status mapping",
			slog.String("request_id", RequestIDFrom(r.Context())),
			slog.String("code", string(de.Code)))
		status = http.StatusInternalServerError
		de = domain.NewError(codeInternal, "an unexpected error occurred")
	}

	writeProblem(w, r, status, de)
}

func writeProblem(w http.ResponseWriter, r *http.Request, status int, de *domain.Error) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{
		Type:      "about:blank",
		Title:     http.StatusText(status),
		Status:    status,
		Detail:    de.Message,
		Code:      de.Code,
		RequestID: RequestIDFrom(r.Context()),
		Errors:    de.Fields,
	})
}

// writeJSON writes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
