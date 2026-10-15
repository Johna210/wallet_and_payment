//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// transfer posts a Transfer of amount from one Wallet to another under key.
func transfer(t *testing.T, srv *httptest.Server, from, to, key, amount string) *http.Response {
	t.Helper()
	return postWithKey(t, srv, "/v1/transfers", key,
		`{"source_wallet_id":"`+from+`","destination_wallet_id":"`+to+`","amount":"`+amount+`"}`)
}

func TestTransferMovesMoneyAndWritesTwoEntriesSummingToZero(t *testing.T) {
	srv := newServer(t)
	src := fundedWallet(t, srv, "ETB", "100.00")
	dst := fundedWallet(t, srv, "ETB", "5.00")
	key := uuid.NewString()

	resp := transfer(t, srv, src.ID, dst.ID, key, "30.25")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	if got := resp.Header.Get("Idempotent-Replayed"); got != "" {
		t.Errorf("Idempotent-Replayed = %q on a first request, want absent", got)
	}
	var tx transactionBody
	decode(t, resp, &tx)

	if tx.Type != "TRANSFER" || tx.Status != "COMPLETED" || tx.Amount != "30.25" || tx.Currency != "ETB" || tx.IdempotencyKey != key {
		t.Errorf("unexpected transaction %+v", tx)
	}
	if tx.SourceWalletID == nil || *tx.SourceWalletID != src.ID || tx.DestinationWalletID == nil || *tx.DestinationWalletID != dst.ID {
		t.Errorf("wallets = %v -> %v, want %s -> %s", tx.SourceWalletID, tx.DestinationWalletID, src.ID, dst.ID)
	}
	if tx.Balance != "69.75" {
		t.Errorf("balance = %s, want the source's 69.75", tx.Balance)
	}
	entries := map[string]ledgerEntryBody{}
	for _, e := range tx.LedgerEntries {
		entries[e.WalletID] = e
	}
	if len(tx.LedgerEntries) != 2 || len(entries) != 2 {
		t.Fatalf("ledger entries = %+v, want one per Wallet", tx.LedgerEntries)
	}
	if e := entries[src.ID]; e.Amount != "-30.25" || e.BalanceAfter != "69.75" {
		t.Errorf("source entry = %+v, want -30.25 leaving 69.75", e)
	}
	if e := entries[dst.ID]; e.Amount != "30.25" || e.BalanceAfter != "35.25" {
		t.Errorf("destination entry = %+v, want +30.25 leaving 35.25", e)
	}
	if got := getWallet(t, srv, src.ID).Balance; got != "69.75" {
		t.Errorf("source balance = %s, want 69.75", got)
	}
	if got := getWallet(t, srv, dst.ID).Balance; got != "35.25" {
		t.Errorf("destination balance = %s, want 35.25", got)
	}
	var sum int64
	if err := pool.QueryRow(t.Context(), `SELECT COALESCE(SUM(amount), 0)::bigint FROM ledger_entries WHERE transaction_id = $1`, tx.ID).Scan(&sum); err != nil {
		t.Fatalf("sum entries: %v", err)
	}
	if sum != 0 {
		t.Errorf("transfer entries sum to %d, want 0", sum)
	}
	assertBalancesMatchLedger(t)
}

// assertNothingStoredBeyondFunding checks that a rejected Transfer left no
// Transaction or Ledger Entry beyond the funding Deposits.
func assertNothingStoredBeyondFunding(t *testing.T, deposits int) {
	t.Helper()
	if n, e := countRows(t, "transactions"), countRows(t, "ledger_entries"); n != deposits || e != deposits {
		t.Errorf("stored %d transactions and %d ledger entries, want only the %d deposits", n, e, deposits)
	}
}

func assertProblem(t *testing.T, resp *http.Response, status int, code string) {
	t.Helper()
	if resp.StatusCode != status {
		t.Fatalf("status = %d, want %d (body %s)", resp.StatusCode, status, readBody(t, resp))
	}
	if p := decodeProblem(t, resp); p.Code != code {
		t.Errorf("code = %q, want %s", p.Code, code)
	}
}

func TestTransferBetweenCurrenciesIsRejectedAndStoresNothing(t *testing.T) {
	srv := newServer(t)
	src := fundedWallet(t, srv, "ETB", "100.00")
	dst := createWallet(t, srv, uuid.NewString(), "USD")

	resp := transfer(t, srv, src.ID, dst.ID, uuid.NewString(), "10.00")

	assertProblem(t, resp, http.StatusUnprocessableEntity, "currency_mismatch")
	assertNothingStoredBeyondFunding(t, 1)
	if got := getWallet(t, srv, src.ID).Balance; got != "100.00" {
		t.Errorf("source balance = %s, want 100.00", got)
	}
}

func TestTransferToSameWalletIsRejectedAndStoresNothing(t *testing.T) {
	srv := newServer(t)
	w := fundedWallet(t, srv, "ETB", "100.00")

	resp := transfer(t, srv, w.ID, w.ID, uuid.NewString(), "10.00")

	assertProblem(t, resp, http.StatusUnprocessableEntity, "same_wallet_transfer")
	assertNothingStoredBeyondFunding(t, 1)
}

func TestTransferInvolvingInactiveWalletIsRejectedAndStoresNothing(t *testing.T) {
	for _, status := range []string{"FROZEN", "CLOSED"} {
		for _, side := range []string{"source", "destination"} {
			t.Run(side+" "+status, func(t *testing.T) {
				srv := newServer(t)
				src := createWallet(t, srv, uuid.NewString(), "ETB")
				dst := createWallet(t, srv, uuid.NewString(), "ETB")
				mustDeposit(t, srv, src.ID, "100.00")
				if status == "FROZEN" {
					// A Closed Wallet must be empty, so only a Frozen one keeps its money.
					mustDeposit(t, srv, dst.ID, "1.00")
				}
				target := src
				if side == "destination" {
					target = dst
				}
				if status == "CLOSED" && side == "source" {
					// Empty the source so it can close; the Transfer is still refused for its status.
					if resp := withdraw(t, srv, src.ID, uuid.NewString(), "100.00"); resp.StatusCode != http.StatusCreated {
						t.Fatalf("empty source = %d", resp.StatusCode)
					}
				}
				mustSetStatus(t, srv, target.ID, status)
				before := countRows(t, "transactions")

				resp := transfer(t, srv, src.ID, dst.ID, uuid.NewString(), "1.00")

				assertProblem(t, resp, http.StatusUnprocessableEntity, "wallet_not_active")
				if after := countRows(t, "transactions"); after != before {
					t.Errorf("transactions = %d, want %d unchanged", after, before)
				}
			})
		}
	}
}

func TestTransferWithUnknownWalletIsNotFound(t *testing.T) {
	srv := newServer(t)
	known := fundedWallet(t, srv, "ETB", "100.00")

	assertProblem(t, transfer(t, srv, uuid.NewString(), known.ID, uuid.NewString(), "1.00"), http.StatusNotFound, "wallet_not_found")
	assertProblem(t, transfer(t, srv, known.ID, uuid.NewString(), uuid.NewString(), "1.00"), http.StatusNotFound, "wallet_not_found")
	assertNothingStoredBeyondFunding(t, 1)
}

func TestTransferLargerThanSourceBalanceIsRejectedAndStoresNothing(t *testing.T) {
	srv := newServer(t)
	src := fundedWallet(t, srv, "ETB", "10.00")
	dst := fundedWallet(t, srv, "ETB", "5.00")

	assertProblem(t, transfer(t, srv, src.ID, dst.ID, uuid.NewString(), "10.01"), http.StatusUnprocessableEntity, "insufficient_funds")

	assertNothingStoredBeyondFunding(t, 2)
	if got := getWallet(t, srv, src.ID).Balance; got != "10.00" {
		t.Errorf("source balance = %s, want 10.00", got)
	}
	if got := getWallet(t, srv, dst.ID).Balance; got != "5.00" {
		t.Errorf("destination balance = %s, want 5.00", got)
	}
}

func TestTransferRejectsInvalidInput(t *testing.T) {
	srv := newServer(t)
	a := fundedWallet(t, srv, "ETB", "100.00")
	b := createWallet(t, srv, uuid.NewString(), "ETB")
	cases := []struct {
		name, body string
		wantFields []string
	}{
		{"missing amount", `{"source_wallet_id":"` + a.ID + `","destination_wallet_id":"` + b.ID + `"}`, []string{"amount"}},
		{"malformed ids", `{"source_wallet_id":"x","destination_wallet_id":"y","amount":"1.00"}`, []string{"source_wallet_id", "destination_wallet_id"}},
		{"zero", `{"source_wallet_id":"` + a.ID + `","destination_wallet_id":"` + b.ID + `","amount":"0.00"}`, []string{"amount"}},
		{"negative", `{"source_wallet_id":"` + a.ID + `","destination_wallet_id":"` + b.ID + `","amount":"-1.00"}`, []string{"amount"}},
		{"too precise", `{"source_wallet_id":"` + a.ID + `","destination_wallet_id":"` + b.ID + `","amount":"1.001"}`, []string{"amount"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postWithKey(t, srv, "/v1/transfers", uuid.NewString(), tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", resp.StatusCode, readBody(t, resp))
			}
			p := decodeProblem(t, resp)
			if p.Code != "validation_failed" || !slices.Equal(p.fieldNames(), tc.wantFields) {
				t.Errorf("problem = %+v, want validation_failed on %v", p, tc.wantFields)
			}
		})
	}
	assertNothingStoredBeyondFunding(t, 1)
}

func TestTransferOverTheLimitIsRejected(t *testing.T) {
	srv := newServer(t)
	a := fundedWallet(t, srv, "ETB", "1000000.00")
	b := createWallet(t, srv, uuid.NewString(), "ETB")

	assertProblem(t, transfer(t, srv, a.ID, b.ID, uuid.NewString(), "1000000.01"), http.StatusUnprocessableEntity, "amount_exceeds_limit")
	if resp := transfer(t, srv, a.ID, b.ID, uuid.NewString(), "1000000.00"); resp.StatusCode != http.StatusCreated {
		t.Errorf("transfer of exactly the limit = %d, want 201", resp.StatusCode)
	}
}

func TestTransferWithoutIdempotencyKeyIsRejected(t *testing.T) {
	srv := newServer(t)
	a := fundedWallet(t, srv, "ETB", "100.00")
	b := createWallet(t, srv, uuid.NewString(), "ETB")

	resp := transfer(t, srv, a.ID, b.ID, "", "1.00")

	assertProblem(t, resp, http.StatusBadRequest, "idempotency_key_required")
	assertNothingStoredBeyondFunding(t, 1)
}

func TestTransferReplayReturnsOriginalTransaction(t *testing.T) {
	srv := newServer(t)
	a := fundedWallet(t, srv, "USD", "100.00")
	b := createWallet(t, srv, uuid.NewString(), "USD")
	key := uuid.NewString()
	var original transactionBody
	decode(t, transfer(t, srv, a.ID, b.ID, key, "40.00"), &original)

	replay := transfer(t, srv, a.ID, b.ID, key, "40.00")
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

	// Once the source can no longer cover the amount, a replay is still a replay.
	withdraw(t, srv, a.ID, uuid.NewString(), "60.00")
	if later := transfer(t, srv, a.ID, b.ID, key, "40.00"); later.StatusCode != http.StatusOK {
		t.Fatalf("late replay = %d, want 200 (body %s)", later.StatusCode, readBody(t, later))
	}
	if got := getWallet(t, srv, b.ID).Balance; got != "40.00" {
		t.Errorf("destination balance = %s, want 40.00 (credited once per key)", got)
	}
	assertBalancesMatchLedger(t)
}

func TestTransferKeyReuseWithDifferentPayloadConflicts(t *testing.T) {
	srv := newServer(t)
	a := fundedWallet(t, srv, "USD", "100.00")
	b := createWallet(t, srv, uuid.NewString(), "USD")
	c := createWallet(t, srv, uuid.NewString(), "USD")
	key := uuid.NewString()
	if resp := transfer(t, srv, a.ID, b.ID, key, "10.00"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first = %d", resp.StatusCode)
	}

	assertProblem(t, transfer(t, srv, a.ID, b.ID, key, "11.00"), http.StatusConflict, "idempotency_key_reuse")
	assertProblem(t, transfer(t, srv, a.ID, c.ID, key, "10.00"), http.StatusConflict, "idempotency_key_reuse")
	assertProblem(t, transfer(t, srv, b.ID, a.ID, key, "10.00"), http.StatusConflict, "idempotency_key_reuse")
	assertProblem(t, deposit(t, srv, a.ID, key, "10.00"), http.StatusConflict, "idempotency_key_reuse")

	if n := countRows(t, "transactions"); n != 2 {
		t.Errorf("transactions = %d, want 2 (the funding deposit and one transfer)", n)
	}
}

func TestRejectedTransferCanBeRetriedWithSameKey(t *testing.T) {
	srv := newServer(t)
	a := fundedWallet(t, srv, "ETB", "10.00")
	b := createWallet(t, srv, uuid.NewString(), "ETB")
	key := uuid.NewString()
	assertProblem(t, transfer(t, srv, a.ID, b.ID, key, "20.00"), http.StatusUnprocessableEntity, "insufficient_funds")
	mustDeposit(t, srv, a.ID, "10.00")

	if resp := transfer(t, srv, a.ID, b.ID, key, "20.00"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("retry = %d, want 201 (body %s)", resp.StatusCode, readBody(t, resp))
	}
}

func TestGetTransferByID(t *testing.T) {
	srv := newServer(t)
	a := fundedWallet(t, srv, "EUR", "10.00")
	b := createWallet(t, srv, uuid.NewString(), "EUR")
	var created transactionBody
	decode(t, transfer(t, srv, a.ID, b.ID, uuid.NewString(), "3.00"), &created)

	resp := get(t, srv, "/v1/transfers/"+created.ID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got transactionBody
	decode(t, resp, &got)
	if got.ID != created.ID || got.Type != "TRANSFER" || len(got.LedgerEntries) != 2 {
		t.Errorf("transfer = %+v", got)
	}
	if resp := get(t, srv, "/v1/transactions/"+created.ID); resp.StatusCode != http.StatusOK {
		t.Errorf("generic route = %d, want 200", resp.StatusCode)
	}
}

func TestGetTransferForAnotherTransactionTypeIsNotFound(t *testing.T) {
	srv := newServer(t)
	w := createWallet(t, srv, uuid.NewString(), "EUR")
	deposited := mustDeposit(t, srv, w.ID, "3.00")

	assertProblem(t, get(t, srv, "/v1/transfers/"+deposited.ID), http.StatusNotFound, "transaction_not_found")
	assertProblem(t, get(t, srv, "/v1/transfers/"+uuid.NewString()), http.StatusNotFound, "transaction_not_found")
	if resp := get(t, srv, "/v1/transfers/nope"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed id = %d, want 400", resp.StatusCode)
	}
}

func TestConcurrentTransfersWithSameKeyCreateOneTransaction(t *testing.T) {
	srv := newServer(t)
	a := fundedWallet(t, srv, "ETB", "100.00")
	b := createWallet(t, srv, uuid.NewString(), "ETB")
	key := uuid.NewString()

	const n = 20
	statuses := make([]int, n)
	ids := make([]string, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			resp, err := doPostWithKey(srv, "/v1/transfers", key,
				`{"source_wallet_id":"`+a.ID+`","destination_wallet_id":"`+b.ID+`","amount":"25.00"}`)
			if err != nil {
				t.Errorf("request %d: %v", i, err)
				return
			}
			defer func() { _ = resp.Body.Close() }()
			statuses[i] = resp.StatusCode
			var tx transactionBody
			if err := json.NewDecoder(resp.Body).Decode(&tx); err != nil {
				t.Errorf("request %d: decode: %v", i, err)
			}
			ids[i] = tx.ID
		}()
	}
	close(start)
	wg.Wait()

	created := 0
	for i := range n {
		switch statuses[i] {
		case http.StatusCreated:
			created++
		case http.StatusOK:
		default:
			t.Errorf("request %d: status %d, want 201 or 200", i, statuses[i])
		}
		if ids[i] != ids[0] {
			t.Errorf("request %d returned transaction %s, want %s", i, ids[i], ids[0])
		}
	}
	if created != 1 {
		t.Errorf("%d requests created a transaction, want exactly 1", created)
	}
	if got := getWallet(t, srv, b.ID).Balance; got != "25.00" {
		t.Errorf("destination balance = %s, want 25.00 (credited once)", got)
	}
	assertBalancesMatchLedger(t)
}

// TestConcurrentTransfersAndWithdrawalsConserveMoney hammers a few Wallets
// with opposite-direction Transfers and Withdrawals at once. Whatever
// interleaving the database picks, no Balance goes negative, nothing deadlocks
// or fails with a 5xx, and every unit of money is accounted for.
func TestConcurrentTransfersAndWithdrawalsConserveMoney(t *testing.T) {
	srv := newServer(t)
	const (
		walletCount   = 4
		initialMinor  = 500_00
		transferCount = 160
		withdrawCount = 40
	)
	wallets := make([]walletBody, walletCount)
	for i := range wallets {
		wallets[i] = fundedWallet(t, srv, "ETB", "500.00")
	}

	type outcome struct {
		kind   string
		minor  int64
		status int
		code   string
		err    error
	}
	outcomes := make([]outcome, transferCount+withdrawCount)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range outcomes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// Amounts are sized so that Balances regularly run dry and the
			// insufficient_funds path is exercised under contention.
			minor := int64(50_00 + (i%7)*20_00)
			amount := fmt.Sprintf("%d.%02d", minor/100, minor%100)
			var resp *http.Response
			var err error
			if i < transferCount {
				from := wallets[i%walletCount]
				to := wallets[(i%walletCount+1+(i/walletCount)%(walletCount-1))%walletCount] // never the same Wallet
				if i%2 == 1 {                                                                // half the Transfers run against the grain
					from, to = to, from
				}
				outcomes[i].kind = "transfer"
				resp, err = doPostWithKey(srv, "/v1/transfers", uuid.NewString(),
					`{"source_wallet_id":"`+from.ID+`","destination_wallet_id":"`+to.ID+`","amount":"`+amount+`"}`)
			} else {
				outcomes[i].kind = "withdrawal"
				resp, err = doPostWithKey(srv, "/v1/wallets/"+wallets[i%walletCount].ID+"/withdrawals", uuid.NewString(), `{"amount":"`+amount+`"}`)
			}
			if err != nil {
				outcomes[i].err = err
				return
			}
			defer func() { _ = resp.Body.Close() }()
			outcomes[i].minor = minor
			outcomes[i].status = resp.StatusCode
			if resp.StatusCode != http.StatusCreated {
				var p problemBody
				outcomes[i].err = json.NewDecoder(resp.Body).Decode(&p)
				outcomes[i].code = p.Code
			}
		}()
	}
	close(start)
	wg.Wait()

	var transfers, withdrawals, rejected int
	var withdrawnMinor int64
	for i, o := range outcomes {
		if o.err != nil {
			t.Fatalf("request %d (%s): %v", i, o.kind, o.err)
		}
		switch {
		case o.status == http.StatusCreated && o.kind == "transfer":
			transfers++
		case o.status == http.StatusCreated:
			withdrawals++
			withdrawnMinor += o.minor
		case o.status == http.StatusUnprocessableEntity && o.code == "insufficient_funds":
			rejected++
		default:
			t.Errorf("request %d (%s): status %d code %q, want 201 or 422 insufficient_funds", i, o.kind, o.status, o.code)
		}
	}
	if rejected == 0 {
		t.Error("no request was rejected for insufficient funds; the test did not exercise contention on empty Wallets")
	}

	var total, negative int64
	if err := pool.QueryRow(t.Context(), `SELECT COALESCE(SUM(balance), 0)::bigint, count(*) FILTER (WHERE balance < 0) FROM wallets`).Scan(&total, &negative); err != nil {
		t.Fatalf("read balances: %v", err)
	}
	if negative != 0 {
		t.Errorf("%d wallets have a negative balance", negative)
	}
	if want := int64(walletCount*initialMinor) - withdrawnMinor; total != want {
		t.Errorf("system total = %d, want initial + deposits − withdrawals = %d", total, want)
	}
	if want := walletCount + transfers + withdrawals; countRows(t, "transactions") != want {
		t.Errorf("transactions = %d, want %d (deposits plus successes)", countRows(t, "transactions"), want)
	}
	if want := walletCount + 2*transfers + withdrawals; countRows(t, "ledger_entries") != want {
		t.Errorf("ledger entries = %d, want %d", countRows(t, "ledger_entries"), want)
	}
	var unbalanced int
	if err := pool.QueryRow(t.Context(), `
		SELECT count(*) FROM (
			SELECT transaction_id FROM ledger_entries e JOIN transactions t ON t.id = e.transaction_id
			WHERE t.type = 'TRANSFER' GROUP BY transaction_id HAVING SUM(e.amount) <> 0
		) bad`).Scan(&unbalanced); err != nil {
		t.Fatalf("check transfers: %v", err)
	}
	if unbalanced != 0 {
		t.Errorf("%d transfers have entries that do not sum to zero", unbalanced)
	}
	assertBalancesMatchLedger(t)
}
