//go:build integration

package integration

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// metricValue returns the value of the sample whose series (name plus label
// set, labels in the alphabetical order Prometheus writes them) is series, or
// 0 when the series is absent. Counters start absent, so absent means zero.
func metricValue(t *testing.T, srv *httptest.Server, series string) float64 {
	t.Helper()
	resp := get(t, srv, "/metrics")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", resp.StatusCode)
	}
	for _, line := range strings.Split(readBody(t, resp), "\n") {
		if v, ok := strings.CutPrefix(line, series+" "); ok {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				t.Fatalf("parse sample %q: %v", line, err)
			}
			return f
		}
	}
	return 0
}

func TestMetricsCountFinancialOperationsByTypeOutcomeAndReason(t *testing.T) {
	srv := newServer(t)
	a := createWallet(t, srv, uuid.NewString(), "ETB")
	b := createWallet(t, srv, uuid.NewString(), "ETB")

	const (
		depositOK  = `wallet_financial_operations_total{outcome="success",reason="none",type="deposit"}`
		withdrawNo = `wallet_financial_operations_total{outcome="rejected",reason="insufficient_funds",type="withdrawal"}`
		transferOK = `wallet_financial_operations_total{outcome="success",reason="none",type="transfer"}`
	)
	for _, s := range []string{depositOK, withdrawNo, transferOK} {
		if got := metricValue(t, srv, s); got != 0 {
			t.Fatalf("%s = %v before any operation, want 0", s, got)
		}
	}

	mustDeposit(t, srv, a.ID, "50.00")
	if resp := withdraw(t, srv, b.ID, uuid.NewString(), "10.00"); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("overdraft = %d, want 422", resp.StatusCode)
	}
	mustTransfer(t, srv, a.ID, b.ID, "20.00")

	if got := metricValue(t, srv, depositOK); got != 1 {
		t.Errorf("%s = %v, want 1", depositOK, got)
	}
	if got := metricValue(t, srv, withdrawNo); got != 1 {
		t.Errorf("%s = %v, want 1", withdrawNo, got)
	}
	if got := metricValue(t, srv, transferOK); got != 1 {
		t.Errorf("%s = %v, want 1", transferOK, got)
	}
	for _, typ := range []string{"deposit", "withdrawal", "transfer"} {
		series := `wallet_financial_operation_duration_seconds_count{type="` + typ + `"}`
		if got := metricValue(t, srv, series); got != 1 {
			t.Errorf("%s = %v, want 1", series, got)
		}
	}
}

func TestMetricsLabelRequestsByRoutePatternNotPath(t *testing.T) {
	srv := newServer(t)
	a := createWallet(t, srv, uuid.NewString(), "ETB")
	b := createWallet(t, srv, uuid.NewString(), "ETB")
	get(t, srv, "/v1/wallets/"+a.ID)
	get(t, srv, "/v1/wallets/"+b.ID)
	get(t, srv, "/v1/no-such-route/"+uuid.NewString())

	const (
		walletOK  = `http_requests_total{method="GET",route="/v1/wallets/{id}",status="200"}`
		unmatched = `http_requests_total{method="GET",route="unmatched",status="404"}`
		duration  = `http_request_duration_seconds_count{method="GET",route="/v1/wallets/{id}"}`
	)
	if got := metricValue(t, srv, walletOK); got != 2 {
		t.Errorf("%s = %v, want 2", walletOK, got)
	}
	if got := metricValue(t, srv, unmatched); got != 1 {
		t.Errorf("%s = %v, want 1", unmatched, got)
	}
	if got := metricValue(t, srv, duration); got != 2 {
		t.Errorf("%s = %v, want 2", duration, got)
	}

	body := readBody(t, get(t, srv, "/metrics"))
	for _, id := range []string{a.ID, b.ID} {
		if strings.Contains(body, id) {
			t.Errorf("/metrics leaks concrete id %s in a label", id)
		}
	}
}

func TestMetricsExposePoolGauges(t *testing.T) {
	srv := newServer(t)
	body := readBody(t, get(t, srv, "/metrics"))
	for _, name := range []string{
		"db_pool_acquired_connections",
		"db_pool_idle_connections",
		"db_pool_total_connections",
		"db_pool_acquire_wait_seconds_total",
	} {
		if !strings.Contains(body, "\n"+name+" ") {
			t.Errorf("/metrics does not expose %s", name)
		}
	}
}
