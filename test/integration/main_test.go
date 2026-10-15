//go:build integration

// Package integration drives the real HTTP handler against a real Postgres
// started with testcontainers. Run with: make test-integration.
package integration

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/Johna210/wallet_and_payment/internal/app"
	"github.com/Johna210/wallet_and_payment/internal/httpapi"
	"github.com/Johna210/wallet_and_payment/internal/platform/config"
	"github.com/Johna210/wallet_and_payment/internal/store/postgres"
)

var (
	// pool is shared by every test in the package; tests must not run in
	// parallel because truncate resets the shared database.
	pool *pgxpool.Pool
	// dsn is the throwaway container's connection string, known only at runtime.
	dsn string
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithPassword(rand.Text()),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres container: %v\n", err)
		return 1
	}
	defer func() { _ = testcontainers.TerminateContainer(ctr) }()

	dsn, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "postgres connection string: %v\n", err)
		return 1
	}
	pool, err = postgres.NewPool(ctx, dsn, 20)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create pool: %v\n", err)
		return 1
	}
	defer pool.Close()

	if err := postgres.Migrate(ctx, pool, slog.New(slog.DiscardHandler)); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		return 1
	}
	return m.Run()
}

// testConfig is the configuration the service runs with under test.
func testConfig() config.Config {
	return config.Config{
		RequestTimeout: 5 * time.Second,
		MaxBodyBytes:   1 << 20,
	}
}

// newServer truncates every application table and returns the real handler
// built over the shared pool, served by httptest.
func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	truncate(t)
	return serve(t, pool)
}

// serve builds the real handler over p, exactly as the binary does.
func serve(t *testing.T, p *pgxpool.Pool) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	h := app.NewHandler(testConfig(), logger, p, httpapi.NewReadiness(p))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// truncate empties every table except goose's version history.
func truncate(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx, `
		SELECT quote_ident(tablename) FROM pg_tables
		WHERE schemaname = 'public' AND tablename <> 'goose_db_version'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list tables: %v", err)
	}
	if len(tables) == 0 {
		return
	}
	if _, err := pool.Exec(ctx, "TRUNCATE "+strings.Join(tables, ", ")+" RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// get issues a GET against srv; the body is closed when the test ends.
func get(t *testing.T, srv *httptest.Server, path string) *http.Response {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// postJSON issues a POST with a JSON body; the body is closed when the test ends.
func postJSON(t *testing.T, srv *httptest.Server, path, body string) *http.Response {
	t.Helper()
	resp, err := srv.Client().Post(srv.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// patchJSON issues a PATCH with a JSON body; the body is closed when the test ends.
func patchJSON(t *testing.T, srv *httptest.Server, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPatch, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build PATCH %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("PATCH %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// decode reads resp's JSON body into dst.
func decode(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		t.Fatalf("decode body: %v", err)
	}
}

// problemBody is the subset of an RFC 9457 problem the tests assert on.
type problemBody struct {
	Status    int    `json:"status"`
	Code      string `json:"code"`
	RequestID string `json:"request_id"`
	Errors    []struct {
		Field   string `json:"field"`
		Message string `json:"message"`
	} `json:"errors"`
}

func (p problemBody) fieldNames() []string {
	names := make([]string, len(p.Errors))
	for i, e := range p.Errors {
		names[i] = e.Field
	}
	return names
}

// decodeProblem decodes a problem response and checks the fields every
// problem must carry.
func decodeProblem(t *testing.T, resp *http.Response) problemBody {
	t.Helper()
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", ct)
	}
	var p problemBody
	decode(t, resp, &p)
	if p.Status != resp.StatusCode {
		t.Errorf("problem status %d != response status %d", p.Status, resp.StatusCode)
	}
	if p.RequestID == "" || p.RequestID != resp.Header.Get("X-Request-ID") {
		t.Errorf("problem request_id %q does not match header %q", p.RequestID, resp.Header.Get("X-Request-ID"))
	}
	return p
}
