package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/Johna210/wallet_and_payment/internal/domain"
)

// maxCursorMicros bounds a cursor's time to the year 9999. Nothing stored
// predates the Unix epoch, so earlier times are forged or corrupt too.
var maxCursorMicros = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC).UnixMicro()

var errInvalidCursor = errors.New("is not a valid cursor")

// cursorPayload is the wire form of a listing position: the (created_at, id) of the
// last item on a page. Clients treat the encoded string as opaque. Time is in
// microseconds, the precision Postgres stores, so a position survives the round
// trip exactly.
type cursorPayload struct {
	Micros int64     `json:"t"`
	ID     uuid.UUID `json:"id"`
}

func encodeCursor(createdAt time.Time, id uuid.UUID) string {
	b, _ := json.Marshal(cursorPayload{Micros: createdAt.UnixMicro(), ID: id})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (createdAt time.Time, id uuid.UUID, err error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, uuid.Nil, errInvalidCursor
	}
	var c cursorPayload
	if err := json.Unmarshal(b, &c); err != nil || c.ID == uuid.Nil || c.Micros < 0 || c.Micros > maxCursorMicros {
		return time.Time{}, uuid.Nil, errInvalidCursor
	}
	return time.UnixMicro(c.Micros).UTC(), c.ID, nil
}

func encodeWalletCursor(p domain.WalletPosition) string {
	return encodeCursor(p.CreatedAt, p.ID)
}

func decodeWalletCursor(s string) (domain.WalletPosition, error) {
	createdAt, id, err := decodeCursor(s)
	return domain.WalletPosition{CreatedAt: createdAt, ID: id}, err
}

func encodeTransactionCursor(p domain.TransactionPosition) string {
	return encodeCursor(p.CreatedAt, p.ID)
}

func decodeTransactionCursor(s string) (domain.TransactionPosition, error) {
	createdAt, id, err := decodeCursor(s)
	return domain.TransactionPosition{CreatedAt: createdAt, ID: id}, err
}
