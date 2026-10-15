//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/Johna210/wallet_and_payment/internal/app"
	"github.com/Johna210/wallet_and_payment/internal/httpapi"
)

// syncBuffer is a bytes.Buffer safe for slog to write from request goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// financialLines returns the parsed "financial operation" log records.
func (b *syncBuffer) financialLines(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.buf.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		if rec["msg"] == "financial operation" {
			out = append(out, rec)
		}
	}
	return out
}

func TestFinancialOperationsAreLogged(t *testing.T) {
	truncate(t)
	logs := &syncBuffer{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	srv := httptest.NewServer(app.NewHandler(testConfig(), logger, pool, httpapi.NewReadiness(pool)))
	t.Cleanup(srv.Close)

	w := createWallet(t, srv, uuid.NewString(), "ETB")
	key := uuid.NewString()
	var created transactionBody
	decode(t, deposit(t, srv, w.ID, key, "10.00"), &created) // completed
	deposit(t, srv, w.ID, key, "10.00")                      // replayed
	mustSetStatus(t, srv, w.ID, "FROZEN")
	frozenKey := uuid.NewString()
	if resp := deposit(t, srv, w.ID, frozenKey, "5.00"); resp.StatusCode != http.StatusUnprocessableEntity { // rejected
		t.Fatalf("frozen deposit = %d, want 422", resp.StatusCode)
	}

	lines := logs.financialLines(t)
	if len(lines) != 3 {
		t.Fatalf("got %d financial operation lines, want 3: %v", len(lines), lines)
	}
	common := func(i int, outcome, wantKey string) map[string]any {
		t.Helper()
		l := lines[i]
		if l["type"] != "DEPOSIT" || l["wallet_id"] != w.ID || l["currency"] != "ETB" ||
			l["idempotency_key"] != wantKey || l["outcome"] != outcome {
			t.Errorf("line %d = %v, want DEPOSIT on %s with key %s and outcome %s", i, l, w.ID, wantKey, outcome)
		}
		return l
	}
	for i, outcome := range []string{"completed", "replayed"} {
		l := common(i, outcome, key)
		if l["transaction_id"] != created.ID || l["amount"] != "10.00" {
			t.Errorf("line %d = %v, want transaction %s and amount 10.00", i, l, created.ID)
		}
	}
	l := common(2, "rejected", frozenKey)
	if l["reason"] != "wallet_not_active" || l["amount"] != "5.00" {
		t.Errorf("rejected line = %v, want reason wallet_not_active and amount 5.00", l)
	}
	if _, has := l["transaction_id"]; has {
		t.Errorf("rejected line carries a transaction_id: %v", l)
	}
}
