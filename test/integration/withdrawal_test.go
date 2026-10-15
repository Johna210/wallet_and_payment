//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// withdraw posts a Withdrawal of amount out of walletID under key.
func withdraw(t *testing.T, srv *httptest.Server, walletID, key, amount string) *http.Response {
	t.Helper()
	return postWithKey(t, srv, "/v1/wallets/"+walletID+"/withdrawals", key, `{"amount":"`+amount+`"}`)
}

// fundedWallet creates a Wallet holding amount.
func fundedWallet(t *testing.T, srv *httptest.Server, currency, amount string) walletBody {
	t.Helper()
	w := createWallet(t, srv, uuid.NewString(), currency)
	mustDeposit(t, srv, w.ID, amount)
	return w
}

func TestWithdrawalDecreasesBalanceAndWritesNegativeEntry(t *testing.T) {
	srv := newServer(t)
	w := fundedWallet(t, srv, "ETB", "100.00")
	key := uuid.NewString()

	resp := withdraw(t, srv, w.ID, key, "30.25")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	if got := resp.Header.Get("Idempotent-Replayed"); got != "" {
		t.Errorf("Idempotent-Replayed = %q on a first request, want absent", got)
	}
	var tx transactionBody
	decode(t, resp, &tx)

	if tx.Type != "WITHDRAWAL" || tx.Status != "COMPLETED" || tx.Amount != "30.25" || tx.Currency != "ETB" || tx.IdempotencyKey != key {
		t.Errorf("unexpected transaction %+v", tx)
	}
	if tx.DestinationWalletID != nil || tx.SourceWalletID == nil || *tx.SourceWalletID != w.ID {
		t.Errorf("wallets = %v -> %v, want %s -> none", tx.SourceWalletID, tx.DestinationWalletID, w.ID)
	}
	if tx.Balance != "69.75" {
		t.Errorf("balance = %s, want 69.75", tx.Balance)
	}
	if len(tx.LedgerEntries) != 1 {
		t.Fatalf("ledger entries = %+v, want exactly one", tx.LedgerEntries)
	}
	if e := tx.LedgerEntries[0]; e.WalletID != w.ID || e.Amount != "-30.25" || e.BalanceAfter != "69.75" {
		t.Errorf("entry = %+v, want -30.25 on %s leaving 69.75", e, w.ID)
	}
	if got := getWallet(t, srv, w.ID).Balance; got != "69.75" {
		t.Errorf("wallet balance = %s, want 69.75", got)
	}
	assertBalancesMatchLedger(t)
}

func TestWithdrawingTheExactBalanceLeavesZero(t *testing.T) {
	srv := newServer(t)
	w := fundedWallet(t, srv, "USD", "12.34")

	resp := withdraw(t, srv, w.ID, uuid.NewString(), "12.34")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	var tx transactionBody
	decode(t, resp, &tx)
	if tx.Balance != "0.00" {
		t.Errorf("balance = %s, want 0.00", tx.Balance)
	}
	if got := getWallet(t, srv, w.ID).Balance; got != "0.00" {
		t.Errorf("wallet balance = %s, want 0.00", got)
	}
}

func TestWithdrawalLargerThanBalanceIsRejectedAndStoresNothing(t *testing.T) {
	srv := newServer(t)
	w := fundedWallet(t, srv, "ETB", "10.00")
	transactions, entries := countRows(t, "transactions"), countRows(t, "ledger_entries")

	resp := withdraw(t, srv, w.ID, uuid.NewString(), "10.01")

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if p := decodeProblem(t, resp); p.Code != "insufficient_funds" {
		t.Errorf("code = %q, want insufficient_funds", p.Code)
	}
	if got := getWallet(t, srv, w.ID).Balance; got != "10.00" {
		t.Errorf("wallet balance = %s, want 10.00", got)
	}
	if n, e := countRows(t, "transactions"), countRows(t, "ledger_entries"); n != transactions || e != entries {
		t.Errorf("stored %d transactions and %d entries, want %d and %d", n, e, transactions, entries)
	}
}

func TestWithdrawalFromEmptyWalletIsRejected(t *testing.T) {
	srv := newServer(t)
	w := createWallet(t, srv, uuid.NewString(), "ETB")

	resp := withdraw(t, srv, w.ID, uuid.NewString(), "0.01")

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if p := decodeProblem(t, resp); p.Code != "insufficient_funds" {
		t.Errorf("code = %q, want insufficient_funds", p.Code)
	}
}

func TestWithdrawalFromInactiveWalletIsRejectedAndStoresNothing(t *testing.T) {
	for _, status := range []string{"FROZEN", "CLOSED"} {
		t.Run(status, func(t *testing.T) {
			srv := newServer(t)
			w := createWallet(t, srv, uuid.NewString(), "ETB")
			if status == "FROZEN" {
				mustDeposit(t, srv, w.ID, "50.00")
			}
			mustSetStatus(t, srv, w.ID, status)
			transactions, entries := countRows(t, "transactions"), countRows(t, "ledger_entries")

			resp := withdraw(t, srv, w.ID, uuid.NewString(), "10.00")

			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", resp.StatusCode)
			}
			if p := decodeProblem(t, resp); p.Code != "wallet_not_active" {
				t.Errorf("code = %q, want wallet_not_active", p.Code)
			}
			if n, e := countRows(t, "transactions"), countRows(t, "ledger_entries"); n != transactions || e != entries {
				t.Errorf("stored %d transactions and %d entries, want %d and %d", n, e, transactions, entries)
			}
		})
	}
}

func TestWithdrawalWithoutIdempotencyKeyIsRejected(t *testing.T) {
	srv := newServer(t)
	w := fundedWallet(t, srv, "ETB", "10.00")

	resp := postJSON(t, srv, "/v1/wallets/"+w.ID+"/withdrawals", `{"amount":"1.00"}`)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if p := decodeProblem(t, resp); p.Code != "idempotency_key_required" {
		t.Errorf("code = %q, want idempotency_key_required", p.Code)
	}
	if got := getWallet(t, srv, w.ID).Balance; got != "10.00" {
		t.Errorf("wallet balance = %s, want 10.00", got)
	}
}

func TestWithdrawalReplayReturnsOriginalTransaction(t *testing.T) {
	srv := newServer(t)
	w := fundedWallet(t, srv, "USD", "100.00")
	key := uuid.NewString()

	first := withdraw(t, srv, w.ID, key, "40.00")
	var original transactionBody
	decode(t, first, &original)

	replay := withdraw(t, srv, w.ID, key, "40.00")
	if replay.StatusCode != http.StatusOK {
		t.Fatalf("replay status = %d, want 200 (body %s)", replay.StatusCode, readBody(t, replay))
	}
	if got := replay.Header.Get("Idempotent-Replayed"); got != "true" {
		t.Errorf("Idempotent-Replayed = %q, want true", got)
	}
	var again transactionBody
	decode(t, replay, &again)
	if !reflect.DeepEqual(again, original) {
		t.Errorf("replay = %+v, want %+v", again, original)
	}

	// Once the Balance can no longer cover the amount, a replay is still a
	// replay, not a fresh attempt that would be refused.
	withdraw(t, srv, w.ID, uuid.NewString(), "60.00")
	later := withdraw(t, srv, w.ID, key, "40.00")
	if later.StatusCode != http.StatusOK {
		t.Fatalf("late replay = %d, want 200 (body %s)", later.StatusCode, readBody(t, later))
	}

	if got := getWallet(t, srv, w.ID).Balance; got != "0.00" {
		t.Errorf("wallet balance = %s, want 0.00 (debited once per key)", got)
	}
}

func TestWithdrawalKeyReuseWithDifferentPayloadConflicts(t *testing.T) {
	srv := newServer(t)
	w := fundedWallet(t, srv, "USD", "100.00")
	other := fundedWallet(t, srv, "USD", "100.00")
	key := uuid.NewString()
	withdraw(t, srv, w.ID, key, "40.00")

	cases := []struct {
		name     string
		walletID string
		amount   string
	}{
		{"different amount", w.ID, "41.00"},
		{"different wallet", other.ID, "40.00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := withdraw(t, srv, tc.walletID, key, tc.amount)
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409", resp.StatusCode)
			}
			if p := decodeProblem(t, resp); p.Code != "idempotency_key_reuse" {
				t.Errorf("code = %q, want idempotency_key_reuse", p.Code)
			}
		})
	}
	if got := getWallet(t, srv, other.ID).Balance; got != "100.00" {
		t.Errorf("other wallet balance = %s, want 100.00", got)
	}
}

func TestDepositKeyCannotBeReusedForWithdrawal(t *testing.T) {
	srv := newServer(t)
	w := fundedWallet(t, srv, "USD", "100.00")
	key := uuid.NewString()
	deposit(t, srv, w.ID, key, "10.00")

	resp := withdraw(t, srv, w.ID, key, "10.00")

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	if p := decodeProblem(t, resp); p.Code != "idempotency_key_reuse" {
		t.Errorf("code = %q, want idempotency_key_reuse", p.Code)
	}
}

func TestRejectedWithdrawalCanBeRetriedWithSameKey(t *testing.T) {
	srv := newServer(t)
	w := fundedWallet(t, srv, "ETB", "5.00")
	key := uuid.NewString()

	if resp := withdraw(t, srv, w.ID, key, "10.00"); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("first attempt = %d, want 422", resp.StatusCode)
	}
	mustDeposit(t, srv, w.ID, "5.00")

	resp := withdraw(t, srv, w.ID, key, "10.00")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("retry = %d, want 201 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	if got := getWallet(t, srv, w.ID).Balance; got != "0.00" {
		t.Errorf("wallet balance = %s, want 0.00", got)
	}
}

func TestWithdrawalFromUnknownWalletIsNotFound(t *testing.T) {
	srv := newServer(t)

	resp := withdraw(t, srv, uuid.NewString(), uuid.NewString(), "10.00")

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if p := decodeProblem(t, resp); p.Code != "wallet_not_found" {
		t.Errorf("code = %q, want wallet_not_found", p.Code)
	}
}

func TestWithdrawalRejectsInvalidAmounts(t *testing.T) {
	srv := newServer(t)
	w := fundedWallet(t, srv, "ETB", "10.00")
	cases := []struct {
		name string
		body string
	}{
		{"zero", `{"amount":"0.00"}`},
		{"negative", `{"amount":"-5.00"}`},
		{"excess precision", `{"amount":"1.001"}`},
		{"not a number", `{"amount":"ten"}`},
		{"numeric JSON", `{"amount":5}`},
		{"missing", `{}`},
		{"unknown field", `{"amount":"5.00","currency":"USD"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postWithKey(t, srv, "/v1/wallets/"+w.ID+"/withdrawals", uuid.NewString(), tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", resp.StatusCode, readBody(t, resp))
			}
			if p := decodeProblem(t, resp); p.Code != "validation_failed" || len(p.Errors) == 0 {
				t.Errorf("problem = %+v, want validation_failed with field errors", p)
			}
		})
	}

	if resp := withdraw(t, srv, "not-a-uuid", uuid.NewString(), "5.00"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed wallet id = %d, want 400", resp.StatusCode)
	}
	if resp := withdraw(t, srv, w.ID, strings.Repeat("k", 256), "5.00"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("over-long key = %d, want 400", resp.StatusCode)
	}
	if got := getWallet(t, srv, w.ID).Balance; got != "10.00" {
		t.Errorf("wallet balance = %s, want 10.00", got)
	}
}

func TestWithdrawalOverTheLimitIsRejected(t *testing.T) {
	srv := newServer(t)
	w := fundedWallet(t, srv, "ETB", "1000000.00")
	mustDeposit(t, srv, w.ID, "1000000.00")

	// testConfig sets the per-operation maximum to 1,000,000.00.
	resp := withdraw(t, srv, w.ID, uuid.NewString(), "1000000.01")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if p := decodeProblem(t, resp); p.Code != "amount_exceeds_limit" {
		t.Errorf("code = %q, want amount_exceeds_limit", p.Code)
	}

	resp = withdraw(t, srv, w.ID, uuid.NewString(), "1000000.00")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("withdrawing the limit = %d, want 201 (body %s)", resp.StatusCode, readBody(t, resp))
	}
}

func TestGetWithdrawalByID(t *testing.T) {
	srv := newServer(t)
	w := fundedWallet(t, srv, "EUR", "10.00")
	var created transactionBody
	decode(t, withdraw(t, srv, w.ID, uuid.NewString(), "3.00"), &created)

	resp := get(t, srv, "/v1/withdrawals/"+created.ID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got transactionBody
	decode(t, resp, &got)
	if got.ID != created.ID || got.Type != "WITHDRAWAL" || len(got.LedgerEntries) != 1 || got.LedgerEntries[0].Amount != "-3.00" {
		t.Errorf("withdrawal = %+v", got)
	}

	if resp := get(t, srv, "/v1/transactions/"+created.ID); resp.StatusCode != http.StatusOK {
		t.Errorf("generic route = %d, want 200", resp.StatusCode)
	}
}

func TestGetWithdrawalForAnotherTransactionTypeIsNotFound(t *testing.T) {
	srv := newServer(t)
	w := createWallet(t, srv, uuid.NewString(), "EUR")
	deposited := mustDeposit(t, srv, w.ID, "3.00")

	resp := get(t, srv, "/v1/withdrawals/"+deposited.ID)

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if p := decodeProblem(t, resp); p.Code != "transaction_not_found" {
		t.Errorf("code = %q, want transaction_not_found", p.Code)
	}
	if resp := get(t, srv, "/v1/withdrawals/"+uuid.NewString()); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown id = %d, want 404", resp.StatusCode)
	}
	if resp := get(t, srv, "/v1/withdrawals/nope"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed id = %d, want 400", resp.StatusCode)
	}
}

func TestConcurrentWithdrawalsNeverOverdraw(t *testing.T) {
	srv := newServer(t)
	const (
		funded     = 1000_00 // minor units
		each       = 30_00
		goroutines = 60 // 60 × 30.00 = 1800.00 asked of a 1000.00 Wallet
	)
	w := fundedWallet(t, srv, "ETB", "1000.00")

	type result struct {
		status int
		code   string
		err    error
	}
	results := make([]result, goroutines)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			resp, err := doPostWithKey(srv, "/v1/wallets/"+w.ID+"/withdrawals", uuid.NewString(), `{"amount":"30.00"}`)
			if err != nil {
				results[i].err = err
				return
			}
			defer func() { _ = resp.Body.Close() }()
			results[i].status = resp.StatusCode
			if resp.StatusCode != http.StatusCreated {
				var p problemBody
				results[i].err = json.NewDecoder(resp.Body).Decode(&p)
				results[i].code = p.Code
			}
		}()
	}
	close(start)
	wg.Wait()

	succeeded := 0
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("request %d: %v", i, r.err)
		}
		switch {
		case r.status == http.StatusCreated:
			succeeded++
		case r.status == http.StatusUnprocessableEntity && r.code == "insufficient_funds":
		default:
			t.Errorf("request %d: status %d code %q, want 201 or 422 insufficient_funds", i, r.status, r.code)
		}
	}

	var finalMinor int64
	if err := pool.QueryRow(t.Context(), `SELECT balance FROM wallets WHERE id = $1`, w.ID).Scan(&finalMinor); err != nil {
		t.Fatalf("read balance: %v", err)
	}
	if finalMinor < 0 {
		t.Errorf("final balance = %d, want ≥ 0", finalMinor)
	}
	if want := int64(funded - succeeded*each); finalMinor != want {
		t.Errorf("final balance = %d, want funded − successes × amount = %d (%d successes)", finalMinor, want, succeeded)
	}
	if want := funded / each; succeeded != want {
		t.Errorf("%d withdrawals succeeded, want %d (all the Wallet could fund)", succeeded, want)
	}
	if got := getWallet(t, srv, w.ID).Balance; got != "10.00" {
		t.Errorf("wallet balance = %s, want 10.00", got)
	}
	if n := countRows(t, "transactions"); n != 1+succeeded {
		t.Errorf("transactions = %d, want %d (deposit plus successes)", n, 1+succeeded)
	}
	assertBalancesMatchLedger(t)
}
