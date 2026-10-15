//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

type ledgerEntryBody struct {
	ID           string `json:"id"`
	WalletID     string `json:"wallet_id"`
	Amount       string `json:"amount"`
	BalanceAfter string `json:"balance_after"`
}

type transactionBody struct {
	ID                  string            `json:"id"`
	Type                string            `json:"type"`
	Status              string            `json:"status"`
	Amount              string            `json:"amount"`
	Currency            string            `json:"currency"`
	SourceWalletID      *string           `json:"source_wallet_id"`
	DestinationWalletID *string           `json:"destination_wallet_id"`
	IdempotencyKey      string            `json:"idempotency_key"`
	CreatedAt           string            `json:"created_at"`
	Balance             string            `json:"balance"`
	LedgerEntries       []ledgerEntryBody `json:"ledger_entries"`
}

func deposit(t *testing.T, srv *httptest.Server, walletID, key, amount string) *http.Response {
	t.Helper()
	return postWithKey(t, srv, "/v1/wallets/"+walletID+"/deposits", key, `{"amount":"`+amount+`"}`)
}

func mustDeposit(t *testing.T, srv *httptest.Server, walletID, amount string) transactionBody {
	t.Helper()
	resp := deposit(t, srv, walletID, uuid.NewString(), amount)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("deposit %s = %d, want 201 (body %s)", amount, resp.StatusCode, readBody(t, resp))
	}
	var tx transactionBody
	decode(t, resp, &tx)
	return tx
}

func getWallet(t *testing.T, srv *httptest.Server, id string) walletBody {
	t.Helper()
	resp := get(t, srv, "/v1/wallets/"+id)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get wallet = %d, want 200", resp.StatusCode)
	}
	var w walletBody
	decode(t, resp, &w)
	return w
}

func TestDepositIncreasesBalance(t *testing.T) {
	srv := newServer(t)
	w := createWallet(t, srv, uuid.NewString(), "ETB")
	key := uuid.NewString()

	resp := deposit(t, srv, w.ID, key, "100.50")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	if got := resp.Header.Get("Idempotent-Replayed"); got != "" {
		t.Errorf("Idempotent-Replayed = %q on a first request, want absent", got)
	}
	var tx transactionBody
	decode(t, resp, &tx)

	if _, err := uuid.Parse(tx.ID); err != nil {
		t.Errorf("id %q is not a UUID", tx.ID)
	}
	if tx.Type != "DEPOSIT" || tx.Status != "COMPLETED" || tx.Amount != "100.50" || tx.Currency != "ETB" || tx.IdempotencyKey != key {
		t.Errorf("unexpected transaction %+v", tx)
	}
	if tx.SourceWalletID != nil || tx.DestinationWalletID == nil || *tx.DestinationWalletID != w.ID {
		t.Errorf("wallets = %v -> %v, want none -> %s", tx.SourceWalletID, tx.DestinationWalletID, w.ID)
	}
	if tx.Balance != "100.50" {
		t.Errorf("balance = %s, want 100.50", tx.Balance)
	}
	if got := getWallet(t, srv, w.ID).Balance; got != "100.50" {
		t.Errorf("wallet balance = %s, want 100.50", got)
	}

	second := mustDeposit(t, srv, w.ID, "0.25")
	if second.Balance != "100.75" {
		t.Errorf("balance after second deposit = %s, want 100.75", second.Balance)
	}
}

func TestDepositWithoutIdempotencyKeyIsRejected(t *testing.T) {
	srv := newServer(t)
	w := createWallet(t, srv, uuid.NewString(), "ETB")

	resp := postJSON(t, srv, "/v1/wallets/"+w.ID+"/deposits", `{"amount":"10.00"}`)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if p := decodeProblem(t, resp); p.Code != "idempotency_key_required" {
		t.Errorf("code = %q, want idempotency_key_required", p.Code)
	}
	if got := getWallet(t, srv, w.ID).Balance; got != "0.00" {
		t.Errorf("wallet balance = %s, want 0.00", got)
	}
}

func TestDepositReplayReturnsOriginalTransaction(t *testing.T) {
	srv := newServer(t)
	w := createWallet(t, srv, uuid.NewString(), "USD")
	key := uuid.NewString()

	first := deposit(t, srv, w.ID, key, "40.00")
	var original transactionBody
	decode(t, first, &original)

	replay := deposit(t, srv, w.ID, key, "40.00")
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

	// A replay after later activity still reports the original resulting Balance.
	mustDeposit(t, srv, w.ID, "10.00")
	later := deposit(t, srv, w.ID, key, "40.00")
	var afterMore transactionBody
	decode(t, later, &afterMore)
	if !reflect.DeepEqual(afterMore, original) {
		t.Errorf("late replay = %+v, want %+v", afterMore, original)
	}

	if got := getWallet(t, srv, w.ID).Balance; got != "50.00" {
		t.Errorf("wallet balance = %s, want 50.00 (credited once per key)", got)
	}
	if n := countRows(t, "transactions"); n != 2 {
		t.Errorf("transactions = %d, want 2", n)
	}
}

func TestDepositReplayIgnoresAmountFormatting(t *testing.T) {
	srv := newServer(t)
	w := createWallet(t, srv, uuid.NewString(), "USD")
	key := uuid.NewString()
	deposit(t, srv, w.ID, key, "40.5")

	replay := deposit(t, srv, w.ID, key, "40.50")

	if replay.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 replay of the same amount", replay.StatusCode)
	}
}

func TestDepositKeyReuseWithDifferentPayloadConflicts(t *testing.T) {
	srv := newServer(t)
	w := createWallet(t, srv, uuid.NewString(), "USD")
	other := createWallet(t, srv, uuid.NewString(), "USD")
	key := uuid.NewString()
	deposit(t, srv, w.ID, key, "40.00")

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
			resp := deposit(t, srv, tc.walletID, key, tc.amount)
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409", resp.StatusCode)
			}
			if p := decodeProblem(t, resp); p.Code != "idempotency_key_reuse" {
				t.Errorf("code = %q, want idempotency_key_reuse", p.Code)
			}
		})
	}
	if got := getWallet(t, srv, w.ID).Balance; got != "40.00" {
		t.Errorf("wallet balance = %s, want 40.00", got)
	}
	if got := getWallet(t, srv, other.ID).Balance; got != "0.00" {
		t.Errorf("other wallet balance = %s, want 0.00", got)
	}
}

func TestConcurrentDepositsWithSameKeyCreateOneTransaction(t *testing.T) {
	srv := newServer(t)
	w := createWallet(t, srv, uuid.NewString(), "ETB")
	key := uuid.NewString()

	const n = 20
	type result struct {
		status   int
		replayed string
		tx       transactionBody
		err      error
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			resp, err := doPostWithKey(srv, "/v1/wallets/"+w.ID+"/deposits", key, `{"amount":"25.00"}`)
			if err != nil {
				results[i].err = err
				return
			}
			defer func() { _ = resp.Body.Close() }()
			results[i].status = resp.StatusCode
			results[i].replayed = resp.Header.Get("Idempotent-Replayed")
			results[i].err = json.NewDecoder(resp.Body).Decode(&results[i].tx)
		}()
	}
	close(start)
	wg.Wait()

	created := 0
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("request %d: %v", i, r.err)
		}
		switch r.status {
		case http.StatusCreated:
			created++
		case http.StatusOK:
			if r.replayed != "true" {
				t.Errorf("request %d: 200 without Idempotent-Replayed", i)
			}
		default:
			t.Errorf("request %d: status %d, want 201 or 200", i, r.status)
		}
		if r.tx.ID != results[0].tx.ID {
			t.Errorf("request %d returned transaction %s, want %s", i, r.tx.ID, results[0].tx.ID)
		}
	}
	if created != 1 {
		t.Errorf("%d requests created a transaction, want exactly 1", created)
	}
	if got := getWallet(t, srv, w.ID).Balance; got != "25.00" {
		t.Errorf("wallet balance = %s, want 25.00 (credited once)", got)
	}
	if n := countRows(t, "transactions"); n != 1 {
		t.Errorf("transactions = %d, want 1", n)
	}
}

func TestDepositIntoInactiveWalletIsRejectedAndStoresNothing(t *testing.T) {
	for _, status := range []string{"FROZEN", "CLOSED"} {
		t.Run(status, func(t *testing.T) {
			srv := newServer(t)
			w := createWallet(t, srv, uuid.NewString(), "ETB")
			mustSetStatus(t, srv, w.ID, status)

			resp := deposit(t, srv, w.ID, uuid.NewString(), "10.00")

			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", resp.StatusCode)
			}
			if p := decodeProblem(t, resp); p.Code != "wallet_not_active" {
				t.Errorf("code = %q, want wallet_not_active", p.Code)
			}
			if got := getWallet(t, srv, w.ID).Balance; got != "0.00" {
				t.Errorf("wallet balance = %s, want 0.00", got)
			}
			if n, e := countRows(t, "transactions"), countRows(t, "ledger_entries"); n != 0 || e != 0 {
				t.Errorf("stored %d transactions and %d ledger entries, want none", n, e)
			}
		})
	}
}

func TestRejectedDepositCanBeRetriedWithSameKey(t *testing.T) {
	srv := newServer(t)
	w := createWallet(t, srv, uuid.NewString(), "ETB")
	mustSetStatus(t, srv, w.ID, "FROZEN")
	key := uuid.NewString()

	if resp := deposit(t, srv, w.ID, key, "10.00"); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("first attempt = %d, want 422", resp.StatusCode)
	}
	mustSetStatus(t, srv, w.ID, "ACTIVE")

	resp := deposit(t, srv, w.ID, key, "10.00")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("retry = %d, want 201 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	if got := getWallet(t, srv, w.ID).Balance; got != "10.00" {
		t.Errorf("wallet balance = %s, want 10.00", got)
	}
}

func TestDepositToUnknownWalletIsNotFound(t *testing.T) {
	srv := newServer(t)

	resp := deposit(t, srv, uuid.NewString(), uuid.NewString(), "10.00")

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if p := decodeProblem(t, resp); p.Code != "wallet_not_found" {
		t.Errorf("code = %q, want wallet_not_found", p.Code)
	}
}

func TestDepositRejectsInvalidAmounts(t *testing.T) {
	srv := newServer(t)
	w := createWallet(t, srv, uuid.NewString(), "ETB")
	cases := []struct {
		name string
		body string
	}{
		{"zero", `{"amount":"0"}`},
		{"zero with decimals", `{"amount":"0.00"}`},
		{"negative", `{"amount":"-5.00"}`},
		{"excess precision", `{"amount":"1.001"}`},
		{"not a number", `{"amount":"ten"}`},
		{"exponent", `{"amount":"1e3"}`},
		{"surrounding space", `{"amount":" 5.00"}`},
		{"empty", `{"amount":""}`},
		{"numeric JSON", `{"amount":5}`},
		{"missing", `{}`},
		{"unknown field", `{"amount":"5.00","currency":"USD"}`},
		{"too large for int64", `{"amount":"99999999999999999999.00"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postWithKey(t, srv, "/v1/wallets/"+w.ID+"/deposits", uuid.NewString(), tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", resp.StatusCode, readBody(t, resp))
			}
			p := decodeProblem(t, resp)
			if p.Code != "validation_failed" || len(p.Errors) == 0 {
				t.Errorf("problem = %+v, want validation_failed with field errors", p)
			}
		})
	}
	if n := countRows(t, "transactions"); n != 0 {
		t.Errorf("stored %d transactions, want none", n)
	}
}

func TestDepositRejectsMalformedWalletIDAndKey(t *testing.T) {
	srv := newServer(t)

	resp := deposit(t, srv, "not-a-uuid", uuid.NewString(), "5.00")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed wallet id = %d, want 400", resp.StatusCode)
	}

	w := createWallet(t, srv, uuid.NewString(), "ETB")
	resp = deposit(t, srv, w.ID, strings.Repeat("k", 256), "5.00")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("over-long key = %d, want 400", resp.StatusCode)
	}
	if p := decodeProblem(t, resp); p.Code != "validation_failed" {
		t.Errorf("code = %q, want validation_failed", p.Code)
	}
}

func TestDepositOverTheLimitIsRejected(t *testing.T) {
	srv := newServer(t)
	w := createWallet(t, srv, uuid.NewString(), "ETB")

	// testConfig sets the per-operation maximum to 1,000,000.00.
	resp := deposit(t, srv, w.ID, uuid.NewString(), "1000000.01")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if p := decodeProblem(t, resp); p.Code != "amount_exceeds_limit" {
		t.Errorf("code = %q, want amount_exceeds_limit", p.Code)
	}
	if n := countRows(t, "transactions"); n != 0 {
		t.Errorf("stored %d transactions, want none", n)
	}

	// The limit itself is allowed.
	if tx := mustDeposit(t, srv, w.ID, "1000000.00"); tx.Balance != "1000000.00" {
		t.Errorf("balance = %s, want 1000000.00", tx.Balance)
	}
}

func TestDepositThatWouldOverflowTheBalanceIsRejected(t *testing.T) {
	srv := newServer(t)
	w := createWallet(t, srv, uuid.NewString(), "ETB")
	if _, err := pool.Exec(context.Background(),
		`UPDATE wallets SET balance = 9223372036854775800 WHERE id = $1`, w.ID); err != nil {
		t.Fatalf("seed balance: %v", err)
	}

	resp := deposit(t, srv, w.ID, uuid.NewString(), "1.00")

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	if p := decodeProblem(t, resp); p.Code != "amount_exceeds_limit" {
		t.Errorf("code = %q, want amount_exceeds_limit", p.Code)
	}
	if n := countRows(t, "transactions"); n != 0 {
		t.Errorf("stored %d transactions, want none", n)
	}
}

func TestGetTransactionReturnsLedgerEntries(t *testing.T) {
	srv := newServer(t)
	w := createWallet(t, srv, uuid.NewString(), "USD")
	mustDeposit(t, srv, w.ID, "5.00")
	created := mustDeposit(t, srv, w.ID, "7.25")

	resp := get(t, srv, "/v1/transactions/"+created.ID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got transactionBody
	decode(t, resp, &got)

	if got.ID != created.ID || got.Type != "DEPOSIT" || got.Amount != "7.25" || got.Currency != "USD" || got.IdempotencyKey != created.IdempotencyKey {
		t.Errorf("transaction = %+v, want %+v", got, created)
	}
	if len(got.LedgerEntries) != 1 {
		t.Fatalf("ledger entries = %+v, want exactly one", got.LedgerEntries)
	}
	if e := got.LedgerEntries[0]; e.WalletID != w.ID || e.Amount != "7.25" || e.BalanceAfter != "12.25" {
		t.Errorf("entry = %+v, want +7.25 on %s leaving 12.25", e, w.ID)
	}
}

func TestGetTransactionNotFoundAndMalformed(t *testing.T) {
	srv := newServer(t)

	resp := get(t, srv, "/v1/transactions/"+uuid.NewString())
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown id = %d, want 404", resp.StatusCode)
	}
	if p := decodeProblem(t, resp); p.Code != "transaction_not_found" {
		t.Errorf("code = %q, want transaction_not_found", p.Code)
	}
	if resp := get(t, srv, "/v1/transactions/nope"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed id = %d, want 400", resp.StatusCode)
	}
}

func TestGetDepositByID(t *testing.T) {
	srv := newServer(t)
	w := createWallet(t, srv, uuid.NewString(), "EUR")
	created := mustDeposit(t, srv, w.ID, "3.00")

	resp := get(t, srv, "/v1/deposits/"+created.ID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got transactionBody
	decode(t, resp, &got)
	if got.ID != created.ID || got.Type != "DEPOSIT" || len(got.LedgerEntries) != 1 {
		t.Errorf("deposit = %+v", got)
	}
}

func TestGetDepositForAnotherTransactionTypeIsNotFound(t *testing.T) {
	srv := newServer(t)
	w := createWallet(t, srv, uuid.NewString(), "EUR")
	// Store a Withdrawal directly (bypassing the API) to prove the typed
	// route only answers for deposits.
	var id string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO transactions (type, amount, currency, source_wallet_id, idempotency_key, request_hash)
		VALUES ('WITHDRAWAL', 100, 'EUR', $1, 'seeded', 'x') RETURNING id`, w.ID).Scan(&id); err != nil {
		t.Fatalf("seed withdrawal: %v", err)
	}

	if resp := get(t, srv, "/v1/transactions/"+id); resp.StatusCode != http.StatusOK {
		t.Fatalf("generic route = %d, want 200", resp.StatusCode)
	}
	resp := get(t, srv, "/v1/deposits/"+id)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("deposit route = %d, want 404", resp.StatusCode)
	}
	if p := decodeProblem(t, resp); p.Code != "transaction_not_found" {
		t.Errorf("code = %q, want transaction_not_found", p.Code)
	}
}

func TestClosingWalletWithBalanceIsRejected(t *testing.T) {
	srv := newServer(t)
	w := createWallet(t, srv, uuid.NewString(), "ETB")
	mustDeposit(t, srv, w.ID, "1.00")

	resp := setStatus(t, srv, w.ID, "CLOSED")

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if p := decodeProblem(t, resp); p.Code != "wallet_balance_not_zero" {
		t.Errorf("code = %q, want wallet_balance_not_zero", p.Code)
	}
	if got := getWallet(t, srv, w.ID).Status; got != "ACTIVE" {
		t.Errorf("wallet status = %s, want ACTIVE", got)
	}
}

// assertBalancesMatchLedger checks the ADR-0001 invariant: every Wallet's
// Balance equals the sum of its Ledger Entries.
func assertBalancesMatchLedger(t *testing.T) {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT w.id, w.balance, COALESCE(SUM(e.amount), 0)::bigint
		FROM wallets w LEFT JOIN ledger_entries e ON e.wallet_id = w.id
		GROUP BY w.id, w.balance`)
	if err != nil {
		t.Fatalf("sum ledger entries: %v", err)
	}
	defer rows.Close()
	checked := 0
	for rows.Next() {
		var id string
		var balance, sum int64
		if err := rows.Scan(&id, &balance, &sum); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if balance != sum {
			t.Errorf("wallet %s balance %d != ledger sum %d", id, balance, sum)
		}
		checked++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("sum ledger entries: %v", err)
	}
	if checked == 0 {
		t.Fatal("no wallets to check")
	}
}

func TestWalletBalanceEqualsSumOfLedgerEntries(t *testing.T) {
	srv := newServer(t)
	wallets := []walletBody{
		createWallet(t, srv, uuid.NewString(), "ETB"),
		createWallet(t, srv, uuid.NewString(), "USD"),
		createWallet(t, srv, uuid.NewString(), "EUR"),
	}

	var wg sync.WaitGroup
	for _, w := range wallets {
		for range 10 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				resp, err := doPostWithKey(srv, "/v1/wallets/"+w.ID+"/deposits", uuid.NewString(), `{"amount":"12.34"}`)
				if err != nil {
					t.Errorf("deposit: %v", err)
					return
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusCreated {
					t.Errorf("deposit = %d, want 201", resp.StatusCode)
				}
			}()
		}
	}
	wg.Wait()

	assertBalancesMatchLedger(t)
	for _, w := range wallets {
		if got := getWallet(t, srv, w.ID).Balance; got != "123.40" {
			t.Errorf("wallet %s balance = %s, want 123.40", w.ID, got)
		}
	}
	if n, e := countRows(t, "transactions"), countRows(t, "ledger_entries"); n != 30 || e != 30 {
		t.Errorf("stored %d transactions and %d entries, want 30 and 30", n, e)
	}
}
