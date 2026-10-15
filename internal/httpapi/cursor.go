package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/Johna210/wallet_and_payment/internal/domain"
)

// maxCursorMicros bounds a cursor's time to the year 9999. A Wallet cannot
// predate the Unix epoch, so earlier times are forged or corrupt too.
var maxCursorMicros = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC).UnixMicro()

var errInvalidCursor = errors.New("is not a valid cursor")

// walletCursor is the wire form of a domain.WalletPosition. Clients treat the
// encoded string as opaque. Time is in microseconds, the precision Postgres
// stores, so a position survives the round trip exactly.
type walletCursor struct {
	Micros int64     `json:"t"`
	ID     uuid.UUID `json:"id"`
}

func encodeWalletCursor(p domain.WalletPosition) string {
	b, _ := json.Marshal(walletCursor{Micros: p.CreatedAt.UnixMicro(), ID: p.ID})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeWalletCursor(s string) (domain.WalletPosition, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return domain.WalletPosition{}, errInvalidCursor
	}
	var c walletCursor
	if err := json.Unmarshal(b, &c); err != nil || c.ID == uuid.Nil || c.Micros < 0 || c.Micros > maxCursorMicros {
		return domain.WalletPosition{}, errInvalidCursor
	}
	return domain.WalletPosition{CreatedAt: time.UnixMicro(c.Micros).UTC(), ID: c.ID}, nil
}
