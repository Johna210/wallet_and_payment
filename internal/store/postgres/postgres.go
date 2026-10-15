// Package postgres is the PostgreSQL store: connection pool, migrations, and
// (in later slices) the repositories built on sqlc-generated queries.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/Johna210/wallet_and_payment/migrations"
)

// NewPool creates a connection pool. Connections are established lazily, so
// an unreachable database surfaces through Ping (and /readyz), not here.
func NewPool(ctx context.Context, databaseURL string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = maxConns
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	return pool, nil
}

// Migrate applies every pending embedded migration over the pool. Running it
// again when the schema is current is a no-op.
func Migrate(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) error {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	return migrateDB(ctx, db, logger)
}

func migrateDB(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return fmt.Errorf("create migration provider: %w", err)
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	for _, r := range results {
		logger.InfoContext(ctx, "migration applied",
			slog.Int64("version", r.Source.Version),
			slog.String("file", r.Source.Path),
			slog.Duration("duration", r.Duration))
	}
	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	logger.InfoContext(ctx, "schema is up to date",
		slog.Int64("version", version), slog.Int("applied", len(results)))
	return nil
}
