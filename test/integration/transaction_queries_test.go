//go:build integration

package integration

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type transactionListBody struct {
	Data       []transactionBody `json:"data"`
	NextCursor *string           `json:"next_cursor"`
}

func mustWithdraw(t *testing.T, srv *httptest.Server, walletID, amount string) transactionBody {
	t.Helper()
	resp := withdraw(t, srv, walletID, uuid.NewString(), amount)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("withdraw %s = %d, want 201 (body %s)", amount, resp.StatusCode, readBody(t, resp))
	}
	var tx transactionBody
	decode(t, resp, &tx)
	return tx
}

func mustTransfer(t *testing.T, srv *httptest.Server, from, to, amount string) transactionBody {
	t.Helper()
	resp := transfer(t, srv, from, to, uuid.NewString(), amount)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("transfer %s = %d, want 201 (body %s)", amount, resp.StatusCode, readBody(t, resp))
	}
	var tx transactionBody
	decode(t, resp, &tx)
	return tx
}

func listTransactions(t *testing.T, srv *httptest.Server, path string) transactionListBody {
	t.Helper()
	resp := get(t, srv, path)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200 (body %s)", path, resp.StatusCode, readBody(t, resp))
	}
	var list transactionListBody
	decode(t, resp, &list)
	return list
}

func transactionIDs(txs []transactionBody) []string {
	ids := make([]string, len(txs))
	for i, tx := range txs {
		ids[i] = tx.ID
	}
	return ids
}

// idsNewestFirst returns the ids of txs, given oldest first, newest first.
func idsNewestFirst(txs ...transactionBody) []string {
	ids := transactionIDs(txs)
	slices.Reverse(ids)
	return ids
}

// summary strips what a listing never carries (the resulting Balance of a
// money-moving response and the Ledger Entries of a detail view), leaving what
// a list item must equal.
func summary(tx transactionBody) transactionBody {
	tx.Balance = ""
	tx.LedgerEntries = nil
	return tx
}

func TestListTransactionsAcrossWalletsNewestFirst(t *testing.T) {
	srv := newServer(t)
	etb := createWallet(t, srv, uuid.NewString(), "ETB")
	usd := createWallet(t, srv, uuid.NewString(), "USD")
	other := createWallet(t, srv, uuid.NewString(), "ETB")
	first := mustDeposit(t, srv, etb.ID, "50.00")
	second := mustDeposit(t, srv, usd.ID, "20.00")
	third := mustWithdraw(t, srv, etb.ID, "5.00")
	fourth := mustTransfer(t, srv, etb.ID, other.ID, "10.00")

	list := listTransactions(t, srv, "/v1/transactions")

	if got, want := transactionIDs(list.Data), idsNewestFirst(first, second, third, fourth); !slices.Equal(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
	for i, want := range []transactionBody{fourth, third, second, first} {
		if !reflect.DeepEqual(list.Data[i], summary(want)) {
			t.Errorf("item %d = %+v, want %+v", i, list.Data[i], summary(want))
		}
	}
	if list.NextCursor != nil {
		t.Errorf("next_cursor = %q, want none on the only page", *list.NextCursor)
	}
}

func TestListTransactionsWhenNoneExistReturnsEmptyArray(t *testing.T) {
	srv := newServer(t)

	resp := get(t, srv, "/v1/transactions")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if body := strings.TrimSpace(readBody(t, resp)); body != `{"data":[],"next_cursor":null}` {
		t.Errorf("body = %s", body)
	}
}

func TestWalletStatementIncludesDepositsWithdrawalsAndTransfersInBothDirections(t *testing.T) {
	srv := newServer(t)
	wallet := createWallet(t, srv, uuid.NewString(), "ETB")
	peer := createWallet(t, srv, uuid.NewString(), "ETB")
	stranger := createWallet(t, srv, uuid.NewString(), "ETB")
	mine := mustDeposit(t, srv, wallet.ID, "100.00")
	peers := mustDeposit(t, srv, peer.ID, "50.00")
	sent := mustTransfer(t, srv, wallet.ID, peer.ID, "10.00")
	received := mustTransfer(t, srv, peer.ID, wallet.ID, "4.00")
	withdrawn := mustWithdraw(t, srv, wallet.ID, "1.00")
	unrelated := mustTransfer(t, srv, peer.ID, stranger.ID, "5.00")

	statement := listTransactions(t, srv, "/v1/wallets/"+wallet.ID+"/transactions")

	if got, want := transactionIDs(statement.Data), idsNewestFirst(mine, sent, received, withdrawn); !slices.Equal(got, want) {
		t.Errorf("wallet statement ids = %v, want %v", got, want)
	}
	for i, want := range []transactionBody{withdrawn, received, sent, mine} {
		if !reflect.DeepEqual(statement.Data[i], summary(want)) {
			t.Errorf("item %d = %+v, want %+v", i, statement.Data[i], summary(want))
		}
	}
	if statement.NextCursor != nil {
		t.Errorf("next_cursor = %q, want none on the only page", *statement.NextCursor)
	}

	// A Transfer shows up once in each of its two Wallets' statements.
	peerStatement := listTransactions(t, srv, "/v1/wallets/"+peer.ID+"/transactions")
	if got, want := transactionIDs(peerStatement.Data), idsNewestFirst(peers, sent, received, unrelated); !slices.Equal(got, want) {
		t.Errorf("peer statement ids = %v, want %v", got, want)
	}
}

func TestWalletStatementForUnknownWalletIsNotFound(t *testing.T) {
	srv := newServer(t)
	mustDeposit(t, srv, createWallet(t, srv, uuid.NewString(), "ETB").ID, "10.00")

	resp := get(t, srv, "/v1/wallets/"+uuid.NewString()+"/transactions")

	assertProblem(t, resp, http.StatusNotFound, "wallet_not_found")
}

func TestWalletStatementOfWalletWithoutTransactionsIsEmptyNotMissing(t *testing.T) {
	srv := newServer(t)
	wallet := createWallet(t, srv, uuid.NewString(), "ETB")

	resp := get(t, srv, "/v1/wallets/"+wallet.ID+"/transactions")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if body := strings.TrimSpace(readBody(t, resp)); body != `{"data":[],"next_cursor":null}` {
		t.Errorf("body = %s", body)
	}
}

func TestWalletStatementWithMalformedWalletIDIsRejected(t *testing.T) {
	srv := newServer(t)

	resp := get(t, srv, "/v1/wallets/not-a-uuid/transactions")

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	p := decodeProblem(t, resp)
	if p.Code != "validation_failed" || !slices.Equal(p.fieldNames(), []string{"id"}) {
		t.Errorf("unexpected problem %+v", p)
	}
}

func queryString(kv ...string) string {
	v := url.Values{}
	for i := 0; i < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return "?" + v.Encode()
}

// history is a small fixed set of Transactions across two Currencies and all
// three types. txs is oldest first:
//
//	0 deposit    ETB into etb
//	1 deposit    USD into usd
//	2 withdrawal ETB out of etb
//	3 transfer   ETB etb -> etb2
//	4 transfer   USD usd -> usd2
//	5 deposit    ETB into etb2
type history struct {
	etb, etb2, usd, usd2 walletBody
	txs                  []transactionBody
}

func newHistory(t *testing.T, srv *httptest.Server) history {
	t.Helper()
	h := history{
		etb:  createWallet(t, srv, uuid.NewString(), "ETB"),
		etb2: createWallet(t, srv, uuid.NewString(), "ETB"),
		usd:  createWallet(t, srv, uuid.NewString(), "USD"),
		usd2: createWallet(t, srv, uuid.NewString(), "USD"),
	}
	h.txs = []transactionBody{
		mustDeposit(t, srv, h.etb.ID, "100.00"),
		mustDeposit(t, srv, h.usd.ID, "100.00"),
		mustWithdraw(t, srv, h.etb.ID, "5.00"),
		mustTransfer(t, srv, h.etb.ID, h.etb2.ID, "10.00"),
		mustTransfer(t, srv, h.usd.ID, h.usd2.ID, "10.00"),
		mustDeposit(t, srv, h.etb2.ID, "7.00"),
	}
	return h
}

func (h history) ids(indexes ...int) []string {
	ids := []string{}
	for _, i := range indexes {
		ids = append(ids, h.txs[i].ID)
	}
	return ids
}

func (h history) at(i int) string { return h.txs[i].CreatedAt }

// inOffset renders an RFC 3339 instant with a +03:00 offset instead of Z.
func inOffset(t *testing.T, instant string) string {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, instant)
	if err != nil {
		t.Fatalf("parse %q: %v", instant, err)
	}
	return parsed.In(time.FixedZone("", 3*60*60)).Format(time.RFC3339Nano)
}

func shifted(t *testing.T, instant string, d time.Duration) string {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, instant)
	if err != nil {
		t.Fatalf("parse %q: %v", instant, err)
	}
	return parsed.Add(d).Format(time.RFC3339Nano)
}

type filterCase struct {
	name  string
	query string
	want  []int // indexes into history.txs, in the order expected
}

func runFilterCases(t *testing.T, srv *httptest.Server, h history, path string, cases []filterCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			list := listTransactions(t, srv, path+tc.query)
			if got, want := transactionIDs(list.Data), h.ids(tc.want...); !slices.Equal(got, want) {
				t.Errorf("ids = %v, want %v", got, want)
			}
		})
	}
}

func TestListTransactionsFilters(t *testing.T) {
	srv := newServer(t)
	h := newHistory(t, srv)

	runFilterCases(t, srv, h, "/v1/transactions", []filterCase{
		{"no filters", "", []int{5, 4, 3, 2, 1, 0}},
		{"type deposit", queryString("type", "DEPOSIT"), []int{5, 1, 0}},
		{"type withdrawal", queryString("type", "WITHDRAWAL"), []int{2}},
		{"type transfer", queryString("type", "TRANSFER"), []int{4, 3}},
		{"currency ETB", queryString("currency", "ETB"), []int{5, 3, 2, 0}},
		{"currency USD", queryString("currency", "USD"), []int{4, 1}},
		{"currency with no transactions", queryString("currency", "EUR"), nil},
		{"type and currency", queryString("type", "TRANSFER", "currency", "USD"), []int{4}},
		{"from is inclusive", queryString("from", h.at(2)), []int{5, 4, 3, 2}},
		{"to is inclusive", queryString("to", h.at(2)), []int{2, 1, 0}},
		{"from and to", queryString("from", h.at(1), "to", h.at(3)), []int{3, 2, 1}},
		{"from equal to to is one instant", queryString("from", h.at(2), "to", h.at(2)), []int{2}},
		{"from and to in a non-UTC offset", queryString("from", inOffset(t, h.at(1)), "to", inOffset(t, h.at(3))), []int{3, 2, 1}},
		// Postgres keeps microseconds; a bound finer than that must not widen the range.
		{"from a fraction of a microsecond after tx 2", queryString("from", shifted(t, h.at(2), 500*time.Nanosecond)), []int{5, 4, 3}},
		{"to a fraction of a microsecond after tx 2", queryString("to", shifted(t, h.at(2), 500*time.Nanosecond)), []int{2, 1, 0}},
		{"to a fraction of a microsecond before tx 2", queryString("to", shifted(t, h.at(2), -500*time.Nanosecond)), []int{1, 0}},
		{"from without fractional seconds", queryString("from", "2000-01-01T00:00:00Z"), []int{5, 4, 3, 2, 1, 0}},
		{"to in the past", queryString("to", "2000-01-01T00:00:00Z"), nil},
		{"from in the future", queryString("from", "2999-01-01T00:00:00Z"), nil},
		{"all four", queryString("type", "DEPOSIT", "currency", "ETB", "from", h.at(0), "to", h.at(4)), []int{0}},
		{"all four, nothing left", queryString("type", "WITHDRAWAL", "currency", "USD", "from", h.at(0), "to", h.at(5)), nil},
	})
}

func TestWalletStatementFilters(t *testing.T) {
	srv := newServer(t)
	h := newHistory(t, srv)

	t.Run("source side of a transfer", func(t *testing.T) {
		runFilterCases(t, srv, h, "/v1/wallets/"+h.etb.ID+"/transactions", []filterCase{
			{"no filters", "", []int{3, 2, 0}},
			{"type deposit", queryString("type", "DEPOSIT"), []int{0}},
			{"type withdrawal", queryString("type", "WITHDRAWAL"), []int{2}},
			{"type transfer", queryString("type", "TRANSFER"), []int{3}},
			{"its own currency", queryString("currency", "ETB"), []int{3, 2, 0}},
			{"another currency", queryString("currency", "USD"), nil},
			{"from", queryString("from", h.at(2)), []int{3, 2}},
			{"to", queryString("to", h.at(2)), []int{2, 0}},
			{"from and to", queryString("from", h.at(1), "to", h.at(3)), []int{3, 2}},
			{"from and to in a non-UTC offset", queryString("from", inOffset(t, h.at(1)), "to", inOffset(t, h.at(3))), []int{3, 2}},
			{"type, currency and range", queryString("type", "TRANSFER", "currency", "ETB", "from", h.at(0), "to", h.at(5)), []int{3}},
		})
	})
	t.Run("destination side of a transfer", func(t *testing.T) {
		runFilterCases(t, srv, h, "/v1/wallets/"+h.etb2.ID+"/transactions", []filterCase{
			{"no filters", "", []int{5, 3}},
			{"type deposit", queryString("type", "DEPOSIT"), []int{5}},
			{"type transfer", queryString("type", "TRANSFER"), []int{3}},
			{"type withdrawal", queryString("type", "WITHDRAWAL"), nil},
			{"to before its last deposit", queryString("to", h.at(4)), []int{3}},
		})
	})
}

func TestListTransactionsRejectsInvalidFilters(t *testing.T) {
	srv := newServer(t)
	h := newHistory(t, srv)
	cases := []struct {
		name       string
		query      string
		wantFields []string
	}{
		{"type unknown", queryString("type", "REFUND"), []string{"type"}},
		{"type lower-case", queryString("type", "deposit"), []string{"type"}},
		{"type empty", queryString("type", ""), []string{"type"}},
		{"from not a time", queryString("from", "yesterday"), []string{"from"}},
		{"from a date only", queryString("from", "2026-10-15"), []string{"from"}},
		{"from without a zone", queryString("from", "2026-10-15T10:00:00"), []string{"from"}},
		{"from impossible date", queryString("from", "2026-02-30T10:00:00Z"), []string{"from"}},
		{"from empty", queryString("from", ""), []string{"from"}},
		{"to not a time", queryString("to", "tomorrow"), []string{"to"}},
		{"to without a zone", queryString("to", "2026-10-15T10:00:00"), []string{"to"}},
		{"to empty", queryString("to", ""), []string{"to"}},
		{"from after to", queryString("from", h.at(3), "to", h.at(2)), []string{"from"}},
		{"from after to across offsets", queryString("from", "2026-10-15T12:00:00Z", "to", "2026-10-15T14:00:00+03:00"), []string{"from"}},
		{"currency unsupported", queryString("currency", "GBP"), []string{"currency"}},
		{"currency lower-case", queryString("currency", "etb"), []string{"currency"}},
		{"currency empty", queryString("currency", ""), []string{"currency"}},
		{"every problem reported", queryString("type", "x", "from", "x", "to", "x", "currency", "x"), []string{"type", "from", "to", "currency"}},
	}
	for _, path := range []string{"/v1/transactions", "/v1/wallets/" + h.etb.ID + "/transactions"} {
		for _, tc := range cases {
			t.Run(path+" "+tc.name, func(t *testing.T) {
				resp := get(t, srv, path+tc.query)
				if resp.StatusCode != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400 (body %s)", resp.StatusCode, readBody(t, resp))
				}
				p := decodeProblem(t, resp)
				if p.Code != "validation_failed" {
					t.Errorf("code = %q, want validation_failed", p.Code)
				}
				if got := p.fieldNames(); !slices.Equal(got, tc.wantFields) {
					t.Errorf("fields = %v, want %v", got, tc.wantFields)
				}
			})
		}
	}
}

// pageThroughTransactions follows next_cursor from the first page of path+query
// until it runs out. query is "" or a query string starting with "?".
func pageThroughTransactions(t *testing.T, srv *httptest.Server, path, query string) [][]transactionBody {
	t.Helper()
	var pages [][]transactionBody
	cursor := ""
	for range 50 {
		q := query
		if cursor != "" {
			if q == "" {
				q = "?"
			} else {
				q += "&"
			}
			q += "cursor=" + cursor
		}
		list := listTransactions(t, srv, path+q)
		pages = append(pages, list.Data)
		if list.NextCursor == nil {
			return pages
		}
		cursor = *list.NextCursor
	}
	t.Fatal("pagination did not terminate")
	return nil
}

func pagedIDs(pages [][]transactionBody) []string {
	ids := []string{}
	for _, p := range pages {
		ids = append(ids, transactionIDs(p)...)
	}
	return ids
}

func transactionPageSizes(pages [][]transactionBody) []int {
	sizes := make([]int, len(pages))
	for i, p := range pages {
		sizes[i] = len(p)
	}
	return sizes
}

// busyWallet is a Wallet whose statement alternates between Transactions it is
// the destination of and ones it is the source of, so every page boundary
// falls between the two halves of the statement. history is oldest first and
// holds exactly the Transactions in the Wallet's statement.
type busyWallet struct {
	wallet, peer walletBody
	history      []transactionBody
}

func newBusyWallet(t *testing.T, srv *httptest.Server) busyWallet {
	t.Helper()
	b := busyWallet{
		wallet: createWallet(t, srv, uuid.NewString(), "ETB"),
		peer:   createWallet(t, srv, uuid.NewString(), "ETB"),
	}
	b.history = append(b.history, mustDeposit(t, srv, b.wallet.ID, "100.00"))
	mustDeposit(t, srv, b.peer.ID, "100.00") // the peer's own, not in the Wallet's statement
	for range 3 {
		b.history = append(b.history,
			mustTransfer(t, srv, b.wallet.ID, b.peer.ID, "1.00"),
			mustTransfer(t, srv, b.peer.ID, b.wallet.ID, "2.00"),
			mustWithdraw(t, srv, b.wallet.ID, "0.50"))
	}
	return b
}

func (b busyWallet) statementPath() string { return "/v1/wallets/" + b.wallet.ID + "/transactions" }

func TestListTransactionsPaginationVisitsEveryTransactionOnce(t *testing.T) {
	srv := newServer(t)
	wallets := []walletBody{createWallet(t, srv, uuid.NewString(), "ETB"), createWallet(t, srv, uuid.NewString(), "USD")}
	var created []transactionBody // oldest first
	for i := range 7 {
		created = append(created, mustDeposit(t, srv, wallets[i%2].ID, "1.00"))
	}

	pages := pageThroughTransactions(t, srv, "/v1/transactions", "?limit=3")

	if got := transactionPageSizes(pages); !slices.Equal(got, []int{3, 3, 1}) {
		t.Errorf("page sizes = %v, want [3 3 1]", got)
	}
	if got, want := pagedIDs(pages), idsNewestFirst(created...); !slices.Equal(got, want) {
		t.Errorf("paged ids = %v, want %v", got, want)
	}
}

func TestWalletStatementPaginationVisitsEveryTransactionOnce(t *testing.T) {
	srv := newServer(t)
	b := newBusyWallet(t, srv) // 10 Transactions in the statement

	cases := []struct {
		limit string
		sizes []int
	}{
		{"1", []int{1, 1, 1, 1, 1, 1, 1, 1, 1, 1}},
		{"2", []int{2, 2, 2, 2, 2}},
		{"3", []int{3, 3, 3, 1}},
		{"4", []int{4, 4, 2}},
		{"9", []int{9, 1}},
		{"10", []int{10}},
		{"11", []int{10}},
	}
	for _, tc := range cases {
		t.Run("limit "+tc.limit, func(t *testing.T) {
			pages := pageThroughTransactions(t, srv, b.statementPath(), "?limit="+tc.limit)

			if got := transactionPageSizes(pages); !slices.Equal(got, tc.sizes) {
				t.Errorf("page sizes = %v, want %v", got, tc.sizes)
			}
			if got, want := pagedIDs(pages), idsNewestFirst(b.history...); !slices.Equal(got, want) {
				t.Errorf("paged ids = %v, want %v", got, want)
			}
		})
	}
}

func TestTransactionListLastPageHasNoCursorWhenCountIsAMultipleOfLimit(t *testing.T) {
	srv := newServer(t)
	b := newBusyWallet(t, srv) // 10 Transactions in the statement, 11 overall

	if got := transactionPageSizes(pageThroughTransactions(t, srv, b.statementPath(), "?limit=5")); !slices.Equal(got, []int{5, 5}) {
		t.Errorf("statement page sizes = %v, want [5 5]", got)
	}
	if got := transactionPageSizes(pageThroughTransactions(t, srv, "/v1/transactions", "?limit=11")); !slices.Equal(got, []int{11}) {
		t.Errorf("global page sizes = %v, want [11]", got)
	}
}

func TestListTransactionsPaginationIsStableWhileTransactionsArrive(t *testing.T) {
	srv := newServer(t)
	wallet := createWallet(t, srv, uuid.NewString(), "ETB")
	var original []transactionBody
	for range 4 {
		original = append(original, mustDeposit(t, srv, wallet.ID, "1.00"))
	}

	first := listTransactions(t, srv, "/v1/transactions?limit=2")
	mustDeposit(t, srv, wallet.ID, "1.00") // arrives between page requests
	if first.NextCursor == nil {
		t.Fatal("first page has no next_cursor")
	}
	second := listTransactions(t, srv, "/v1/transactions?limit=2&cursor="+*first.NextCursor)

	got := append(transactionIDs(first.Data), transactionIDs(second.Data)...)
	if want := idsNewestFirst(original...); !slices.Equal(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
	if second.NextCursor != nil {
		t.Errorf("second page next_cursor = %q, want none", *second.NextCursor)
	}
}

func TestWalletStatementPaginationIsStableWhileTransactionsArrive(t *testing.T) {
	srv := newServer(t)
	b := newBusyWallet(t, srv)

	first := listTransactions(t, srv, b.statementPath()+"?limit=4")
	if first.NextCursor == nil {
		t.Fatal("first page has no next_cursor")
	}
	// One as destination and one as source, both newer than the first page.
	mustTransfer(t, srv, b.peer.ID, b.wallet.ID, "1.00")
	mustWithdraw(t, srv, b.wallet.ID, "1.00")
	var rest []transactionBody
	cursor := *first.NextCursor
	for cursor != "" {
		page := listTransactions(t, srv, b.statementPath()+"?limit=4&cursor="+cursor)
		rest = append(rest, page.Data...)
		cursor = ""
		if page.NextCursor != nil {
			cursor = *page.NextCursor
		}
	}

	got := append(transactionIDs(first.Data), transactionIDs(rest)...)
	if want := idsNewestFirst(b.history...); !slices.Equal(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
}

func TestTransactionPaginationKeepsFilters(t *testing.T) {
	srv := newServer(t)
	b := newBusyWallet(t, srv)
	var transfers []transactionBody
	for _, tx := range b.history {
		if tx.Type == "TRANSFER" {
			transfers = append(transfers, tx)
		}
	}

	for name, path := range map[string]string{"statement": b.statementPath(), "global": "/v1/transactions"} {
		t.Run(name, func(t *testing.T) {
			pages := pageThroughTransactions(t, srv, path, queryString("type", "TRANSFER", "limit", "4"))

			if got, want := pagedIDs(pages), idsNewestFirst(transfers...); !slices.Equal(got, want) {
				t.Errorf("paged ids = %v, want %v", got, want)
			}
			if got := transactionPageSizes(pages); !slices.Equal(got, []int{4, 2}) {
				t.Errorf("page sizes = %v, want [4 2]", got)
			}
		})
	}
}

func TestTransactionListsDefaultToTwentyPerPage(t *testing.T) {
	srv := newServer(t)
	wallet := createWallet(t, srv, uuid.NewString(), "ETB")
	for range 21 {
		mustDeposit(t, srv, wallet.ID, "1.00")
	}

	for _, path := range []string{"/v1/transactions", "/v1/wallets/" + wallet.ID + "/transactions"} {
		t.Run(path, func(t *testing.T) {
			list := listTransactions(t, srv, path)
			if len(list.Data) != 20 || list.NextCursor == nil {
				t.Errorf("got %d transactions, next_cursor %v; want 20 and a next_cursor", len(list.Data), list.NextCursor)
			}
		})
	}
}

func TestTransactionListsAcceptLimitBounds(t *testing.T) {
	srv := newServer(t)
	wallet := createWallet(t, srv, uuid.NewString(), "ETB")
	for range 2 {
		mustDeposit(t, srv, wallet.ID, "1.00")
	}

	for _, path := range []string{"/v1/transactions", "/v1/wallets/" + wallet.ID + "/transactions"} {
		t.Run(path, func(t *testing.T) {
			if list := listTransactions(t, srv, path+"?limit=1"); len(list.Data) != 1 || list.NextCursor == nil {
				t.Errorf("limit=1: %d transactions, next_cursor %v", len(list.Data), list.NextCursor)
			}
			if list := listTransactions(t, srv, path+"?limit=100"); len(list.Data) != 2 || list.NextCursor != nil {
				t.Errorf("limit=100: %d transactions, next_cursor %v", len(list.Data), list.NextCursor)
			}
		})
	}
}

func TestTransactionListsRejectInvalidLimitAndCursor(t *testing.T) {
	srv := newServer(t)
	wallet := createWallet(t, srv, uuid.NewString(), "ETB")
	cases := []struct {
		name       string
		query      string
		wantFields []string
	}{
		{"limit above max", queryString("limit", "101"), []string{"limit"}},
		{"limit zero", queryString("limit", "0"), []string{"limit"}},
		{"limit negative", queryString("limit", "-1"), []string{"limit"}},
		{"limit signed", queryString("limit", "+5"), []string{"limit"}},
		{"limit not a number", queryString("limit", "ten"), []string{"limit"}},
		{"limit empty", queryString("limit", ""), []string{"limit"}},
		{"cursor garbage", queryString("cursor", "!!!"), []string{"cursor"}},
		{"cursor not ours", queryString("cursor", forgedCursor(`{}`)), []string{"cursor"}},
		{"cursor with a nil id", queryString("cursor", forgedCursor(`{"t":1,"id":"00000000-0000-0000-0000-000000000000"}`)), []string{"cursor"}},
		{"cursor with out-of-range time", queryString("cursor", forgedCursor(`{"t":9223372036854775807,"id":"`+uuid.NewString()+`"}`)), []string{"cursor"}},
		{"cursor with negative time", queryString("cursor", forgedCursor(`{"t":-9223372036854775808,"id":"`+uuid.NewString()+`"}`)), []string{"cursor"}},
		{"cursor empty", queryString("cursor", ""), []string{"cursor"}},
		{"every problem reported", queryString("type", "x", "from", "x", "to", "x", "currency", "x", "limit", "0", "cursor", "!"),
			[]string{"type", "from", "to", "currency", "limit", "cursor"}},
	}
	for _, path := range []string{"/v1/transactions", "/v1/wallets/" + wallet.ID + "/transactions"} {
		for _, tc := range cases {
			t.Run(path+" "+tc.name, func(t *testing.T) {
				resp := get(t, srv, path+tc.query)
				if resp.StatusCode != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400 (body %s)", resp.StatusCode, readBody(t, resp))
				}
				p := decodeProblem(t, resp)
				if p.Code != "validation_failed" {
					t.Errorf("code = %q, want validation_failed", p.Code)
				}
				if got := p.fieldNames(); !slices.Equal(got, tc.wantFields) {
					t.Errorf("fields = %v, want %v", got, tc.wantFields)
				}
			})
		}
	}
}

// TestTransactionPaginationBreaksTiesOnID gives every Transaction the same
// created_at, so only the id decides the order and the cursor position. Paging
// by created_at alone would skip or repeat rows here.
func TestTransactionPaginationBreaksTiesOnID(t *testing.T) {
	srv := newServer(t)
	b := newBusyWallet(t, srv) // 10 in the statement (both halves), 11 overall
	if _, err := pool.Exec(t.Context(), `UPDATE transactions SET created_at = '2026-01-02T03:04:05.678901Z'`); err != nil {
		t.Fatalf("make created_at equal: %v", err)
	}
	newestFirst := func(ids []string) []string {
		sorted := slices.Clone(ids)
		slices.Sort(sorted) // canonical lower-case UUIDs order like their bytes
		slices.Reverse(sorted)
		return sorted
	}
	all := func() []string {
		rows, err := pool.Query(t.Context(), `SELECT id::text FROM transactions`)
		if err != nil {
			t.Fatalf("list ids: %v", err)
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
	}()
	if len(all) != 11 {
		t.Fatalf("setup left %d transactions, want 11", len(all))
	}

	for name, tc := range map[string]struct {
		path string
		want []string
	}{
		"global":    {"/v1/transactions", newestFirst(all)},
		"statement": {b.statementPath(), newestFirst(transactionIDs(b.history))},
	} {
		for _, limit := range []string{"1", "3"} {
			t.Run(name+" limit "+limit, func(t *testing.T) {
				pages := pageThroughTransactions(t, srv, tc.path, "?limit="+limit)

				if got := pagedIDs(pages); !slices.Equal(got, tc.want) {
					t.Errorf("paged ids = %v, want %v", got, tc.want)
				}
			})
		}
	}
}
