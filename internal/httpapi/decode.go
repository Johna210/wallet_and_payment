package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Johna210/wallet_and_payment/internal/domain"
)

// decodeJSON decodes the request body, which must be exactly one JSON object,
// into dst. Unknown fields are rejected.
func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return decodeError(err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err != nil {
			if de := decodeError(err); isCode(de, codePayloadTooLarge) {
				return de
			}
		}
		return domain.NewError(codeInvalidJSON, "the request body must contain a single JSON object")
	}
	return nil
}

func decodeError(err error) error {
	var (
		syntaxErr *json.SyntaxError
		typeErr   *json.UnmarshalTypeError
		maxErr    *http.MaxBytesError
	)
	switch {
	case errors.As(err, &maxErr):
		return domain.NewError(codePayloadTooLarge,
			fmt.Sprintf("the request body must not exceed %d bytes", maxErr.Limit))
	case errors.Is(err, io.EOF):
		return domain.NewError(codeInvalidJSON, "the request body must not be empty")
	case errors.As(err, &syntaxErr), errors.Is(err, io.ErrUnexpectedEOF):
		return domain.NewError(codeInvalidJSON, "the request body is not valid JSON")
	case errors.As(err, &typeErr):
		field := typeErr.Field
		if field == "" {
			return domain.NewError(codeInvalidJSON, "the request body must be a JSON object")
		}
		return domain.ValidationError(domain.FieldError{
			Field:   field,
			Message: fmt.Sprintf("must be of type %s", typeErr.Type),
		})
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		field := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
		return domain.ValidationError(domain.FieldError{Field: field, Message: "is not a recognised field"})
	default:
		return domain.NewError(codeInvalidJSON, "the request body could not be decoded")
	}
}

func isCode(err error, code domain.Code) bool {
	de, ok := domain.AsError(err)
	return ok && de.Code == code
}
