// Package domain holds the business vocabulary of the service: Wallets,
// Transactions, Ledger Entries, Money, and the typed errors business rules
// produce. It knows nothing about HTTP or SQL.
package domain

import (
	"errors"
	"strings"
)

// Code is a stable, machine-readable error identifier exposed to API clients.
// Codes are part of the API contract: never rename one.
type Code string

const (
	CodeValidationFailed        Code = "validation_failed"
	CodeWalletNotFound          Code = "wallet_not_found"
	CodeTransactionNotFound     Code = "transaction_not_found"
	CodeWalletAlreadyExists     Code = "wallet_already_exists"
	CodeInsufficientFunds       Code = "insufficient_funds"
	CodeWalletNotActive         Code = "wallet_not_active"
	CodeCurrencyMismatch        Code = "currency_mismatch"
	CodeSameWalletTransfer      Code = "same_wallet_transfer"
	CodeInvalidStatusTransition Code = "invalid_status_transition"
	CodeWalletBalanceNotZero    Code = "wallet_balance_not_zero"
	CodeAmountExceedsLimit      Code = "amount_exceeds_limit"
	CodeIdempotencyKeyRequired  Code = "idempotency_key_required"
	CodeIdempotencyKeyReuse     Code = "idempotency_key_reuse"
)

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error is a business-rule failure with a stable Code and a message that is
// safe to show to API clients.
type Error struct {
	Code    Code
	Message string
	Fields  []FieldError
}

func (e *Error) Error() string {
	if len(e.Fields) == 0 {
		return string(e.Code) + ": " + e.Message
	}
	parts := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		parts[i] = f.Field + " " + f.Message
	}
	return string(e.Code) + ": " + e.Message + " (" + strings.Join(parts, "; ") + ")"
}

func NewError(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

func ValidationError(fields ...FieldError) *Error {
	return &Error{Code: CodeValidationFailed, Message: "the request is invalid", Fields: fields}
}

func AsError(err error) (*Error, bool) {
	var de *Error
	ok := errors.As(err, &de)
	return de, ok
}
