//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type recordedQuery struct {
	sql  string
	args []any
}

// queryRecorder is a pgx.QueryTracer that remembers every statement a pool runs.
type queryRecorder struct {
	mu      sync.Mutex
	queries []recordedQuery
}

func (r *queryRecorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queries = append(r.queries, recordedQuery{sql: data.SQL, args: append([]any(nil), data.Args...)})
	return ctx
}

func (r *queryRecorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (r *queryRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queries = nil
}

func (r *queryRecorder) reading(table string) []recordedQuery {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []recordedQuery
	for _, q := range r.queries {
		if strings.Contains(q.sql, "FROM "+table) {
			out = append(out, q)
		}
	}
	return out
}

// planNode is the part of a Postgres JSON query plan these tests read.
type planNode struct {
	NodeType      string     `json:"Node Type"`
	RelationName  string     `json:"Relation Name"`
	IndexName     string     `json:"Index Name"`
	ScanDirection string     `json:"Scan Direction"`
	IndexCond     string     `json:"Index Cond"`
	Filter        string     `json:"Filter"`
	Plans         []planNode `json:"Plans"`
}

func (n planNode) String() string {
	var b strings.Builder
	var render func(planNode, int)
	render = func(n planNode, depth int) {
		b.WriteString(strings.Repeat("  ", depth) + n.NodeType)
		if n.IndexName != "" {
			b.WriteString(" using " + n.IndexName)
		}
		if n.ScanDirection != "" {
			b.WriteString(" (" + n.ScanDirection + ")")
		}
		b.WriteString("\n")
		for label, cond := range map[string]string{"index cond": n.IndexCond, "filter": n.Filter} {
			if cond != "" {
				b.WriteString(strings.Repeat("  ", depth+1) + label + ": " + cond + "\n")
			}
		}
		for _, c := range n.Plans {
			render(c, depth+1)
		}
	}
	render(n, 0)
	return b.String()
}

func (n planNode) walk(visit func(planNode)) {
	visit(n)
	for _, c := range n.Plans {
		c.walk(visit)
	}
}

// explain returns the plan Postgres chooses for q. With generic set it is the
// generic plan a cached prepared statement can fall back to, which is planned
// without knowing the parameter values; otherwise the plan for q's own values.
func explain(t *testing.T, q recordedQuery, generic bool) planNode {
	t.Helper()
	ctx := context.Background()
	var raw []byte
	if generic {
		// Only a simple query accepts $n placeholders without values, and pgx
		// itself refuses to send one, so go down to the wire connection.
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		defer conn.Release()
		results, err := conn.Conn().PgConn().Exec(ctx, "EXPLAIN (GENERIC_PLAN, FORMAT JSON) "+q.sql).ReadAll()
		if err != nil || len(results) != 1 || len(results[0].Rows) != 1 {
			t.Fatalf("explain generic plan: %v (%d results)", err, len(results))
		}
		raw = results[0].Rows[0][0]
	} else if err := pool.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+q.sql, q.args...).Scan(&raw); err != nil {
		t.Fatalf("explain: %v", err)
	}
	var plans []struct {
		Plan planNode `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
		t.Fatalf("decode plan %s: %v", raw, err)
	}
	return plans[0].Plan
}

// seedLargeHistory bulk-inserts 30,000 Transactions across 60 Wallets and analyses
// the tables, so the planner sees the volume of a real deployment. The rows carry
// no Ledger Entries (ADR-0001): only the listing plans are examined here.
func seedLargeHistory(t *testing.T) []string {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO wallets (user_id, currency)
		SELECT gen_random_uuid(), 'ETB' FROM generate_series(1, 60)`); err != nil {
		t.Fatalf("seed wallets: %v", err)
	}
	// A third each of Deposits, Withdrawals and Transfers, one per second back
	// from now, spread evenly over the Wallets.
	if _, err := pool.Exec(ctx, `
		WITH ws AS (SELECT array_agg(id ORDER BY id) AS ids, count(*) AS n FROM wallets)
		INSERT INTO transactions (type, amount, currency, source_wallet_id, destination_wallet_id, idempotency_key, request_hash, created_at)
		SELECT k.type, 100, 'ETB',
		       CASE WHEN k.type = 'DEPOSIT' THEN NULL ELSE (ws.ids)[1 + g % ws.n] END,
		       CASE k.type WHEN 'WITHDRAWAL' THEN NULL
		                   WHEN 'DEPOSIT' THEN (ws.ids)[1 + g % ws.n]
		                   ELSE (ws.ids)[1 + (g + 1) % ws.n] END,
		       'seed-' || g, 'seed', now() - make_interval(secs => g)
		FROM ws, generate_series(1, 30000) g,
		     LATERAL (SELECT (ARRAY['DEPOSIT', 'WITHDRAWAL', 'TRANSFER'])[1 + g % 3] AS type) k`); err != nil {
		t.Fatalf("seed transactions: %v", err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE wallets, transactions`); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	rows, err := pool.Query(ctx, `SELECT id::text FROM wallets ORDER BY id`)
	if err != nil {
		t.Fatalf("list seeded wallets: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

// TestTransactionListQueriesUseIndexes asks Postgres how it would run each listing
// query against a large table, for the request's own values and as the generic
// plan a cached prepared statement can switch to.
//
// This is a deliberate exception to "tests assert external behaviour only" (spec #1):
// issue #9 requires no sequential scans on the wallet-scoped path, which a response
// body cannot show. It is coupled to no SQL text, but it reads EXPLAIN output, so
// it is pinned to the Postgres major version main_test.go starts.
func TestTransactionListQueriesUseIndexes(t *testing.T) {
	truncate(t)
	rec := &queryRecorder{}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.ConnConfig.Tracer = rec
	traced, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(traced.Close)
	srv := serve(t, traced)
	wallet := seedLargeHistory(t)[0]

	statement := "/v1/wallets/" + wallet + "/transactions"
	cursor := *listTransactions(t, srv, statement+"?limit=3").NextCursor
	now := time.Now().UTC()
	filtered := queryString(
		"type", "TRANSFER", "currency", "ETB",
		"from", now.Add(-25000*time.Second).Format(time.RFC3339Nano),
		"to", now.Add(-100*time.Second).Format(time.RFC3339Nano),
		"limit", "50", "cursor", cursor)

	walletIndexes := []string{"transactions_source_idx", "transactions_destination_idx"}
	globalIndex := []string{"transactions_created_idx"}
	requests := []struct {
		name string
		path string
		// indexes the plan must read, by name
		want []string
		// seeks is set when the request carries a cursor: the plan must then
		// seek to it in each of those indexes rather than read from the newest
		// row and filter, which would make page N cost O(N).
		seeks bool
	}{
		{"statement", statement, walletIndexes, false},
		{"statement paged", statement + queryString("cursor", cursor), walletIndexes, true},
		{"statement filtered and paged", statement + filtered, walletIndexes, true},
		{"global", "/v1/transactions", globalIndex, false},
		{"global paged", "/v1/transactions" + queryString("cursor", cursor), globalIndex, true},
		{"global filtered and paged", "/v1/transactions" + filtered, globalIndex, true},
	}
	for _, tc := range requests {
		for _, generic := range []bool{false, true} {
			name := tc.name + " (own values)"
			if generic {
				name = tc.name + " (generic plan)"
			}
			t.Run(name, func(t *testing.T) {
				rec.reset()
				listTransactions(t, srv, tc.path)

				queries := rec.reading("transactions")
				if len(queries) != 1 {
					t.Fatalf("captured %d statements reading transactions, want exactly the listing query", len(queries))
				}
				plan := explain(t, queries[0], generic)
				t.Logf("plan:\n%s", plan)
				used, seeked := map[string]bool{}, map[string]bool{}
				plan.walk(func(n planNode) {
					if n.NodeType == "Seq Scan" && n.RelationName == "transactions" {
						t.Errorf("plan scans the transactions table sequentially")
					}
					if n.IndexName != "" {
						used[n.IndexName] = true
						if strings.Contains(n.IndexCond, "ROW(created_at, id) <") {
							seeked[n.IndexName] = true
						}
					}
				})
				for _, idx := range tc.want {
					if !used[idx] {
						t.Errorf("plan does not read index %s (reads %v)", idx, used)
					}
					if tc.seeks && !seeked[idx] {
						t.Errorf("plan reads index %s but does not seek to the cursor in it", idx)
					}
				}
			})
		}
	}
}
