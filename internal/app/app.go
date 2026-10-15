// Package app is the composition root: it wires configuration, the store, services
// and the HTTP transport into one handler, built the same way by the binary and
// the integration tests.
package app

import (
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Johna210/wallet_and_payment/internal/httpapi"
	"github.com/Johna210/wallet_and_payment/internal/platform/config"
	"github.com/Johna210/wallet_and_payment/internal/platform/metrics"
	"github.com/Johna210/wallet_and_payment/internal/service"
	"github.com/Johna210/wallet_and_payment/internal/store/postgres"
)

func NewHandler(cfg config.Config, logger *slog.Logger, pool *pgxpool.Pool, readiness *httpapi.Readiness) http.Handler {
	wallets := postgres.NewWalletStore(pool)
	m := metrics.New(pool)
	return httpapi.NewHandler(httpapi.Deps{
		Logger:         logger,
		Readiness:      readiness,
		RequestTimeout: cfg.RequestTimeout,
		MaxBodyBytes:   cfg.MaxBodyBytes,
		Wallets:        service.NewWalletService(wallets),
		Ledger:         service.NewLedgerService(postgres.NewLedgerStore(pool), wallets, logger, cfg.MaxOperationAmount, m),
		Metrics:        m.Handler(),
		Requests:       m,
	})
}
