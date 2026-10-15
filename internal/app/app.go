// Package app is the composition root: it wires configuration, the store,
// services, and the HTTP transport into one handler. The binary and the
// integration tests both build the service through it, so tests exercise
// exactly what runs in production.
package app

import (
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Johna210/wallet_and_payment/internal/httpapi"
	"github.com/Johna210/wallet_and_payment/internal/platform/config"
	"github.com/Johna210/wallet_and_payment/internal/service"
	"github.com/Johna210/wallet_and_payment/internal/store/postgres"
)

// NewHandler builds the complete HTTP handler over pool.
func NewHandler(cfg config.Config, logger *slog.Logger, pool *pgxpool.Pool, readiness *httpapi.Readiness) http.Handler {
	return httpapi.NewHandler(httpapi.Deps{
		Logger:         logger,
		Readiness:      readiness,
		RequestTimeout: cfg.RequestTimeout,
		MaxBodyBytes:   cfg.MaxBodyBytes,
		Wallets:        service.NewWalletService(postgres.NewWalletStore(pool)),
	})
}
