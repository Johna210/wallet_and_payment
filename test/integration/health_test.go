//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Johna210/wallet_and_payment/internal/store/postgres"
)

func TestHealthAndReadinessWithDatabase(t *testing.T) {
	srv := newServer(t)

	if resp := get(t, srv, "/healthz"); resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz = %d, want 200", resp.StatusCode)
	}
	if resp := get(t, srv, "/readyz"); resp.StatusCode != http.StatusOK {
		t.Errorf("/readyz = %d, want 200", resp.StatusCode)
	}
}

func TestReadyzFailsWhenDatabaseUnreachable(t *testing.T) {
	srv := serve(t, unreachablePool(t))

	resp := get(t, srv, "/readyz")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/readyz = %d, want 503", resp.StatusCode)
	}
	var body struct {
		Code      string `json:"code"`
		RequestID string `json:"request_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if body.Code != "not_ready" || body.RequestID != resp.Header.Get("X-Request-ID") {
		t.Errorf("unexpected problem %+v", body)
	}
}

// unreachablePool returns a pool built from the test container's real
// connection settings but aimed at a local port nothing listens on.
func unreachablePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := uint16(l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.ConnConfig.Host = "127.0.0.1"
	cfg.ConnConfig.Port = port
	cfg.ConnConfig.ConnectTimeout = time.Second
	cfg.ConnConfig.Fallbacks = nil

	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	if err := postgres.Migrate(ctx, pool, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var version int64
	if err := pool.QueryRow(ctx,
		"SELECT max(version_id) FROM goose_db_version WHERE is_applied").Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version < 1 {
		t.Errorf("schema version = %d, want >= 1", version)
	}
	// Migrating through the pool must not close it.
	if err := pool.Ping(ctx); err != nil {
		t.Errorf("pool unusable after migrate: %v", err)
	}
}
