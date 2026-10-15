//go:build integration

package integration

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type walletBody struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	Currency  string `json:"currency"`
	Balance   string `json:"balance"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func TestCreateWallet(t *testing.T) {
	srv := newServer(t)
	userID := uuid.NewString()

	resp := postJSON(t, srv, "/v1/wallets", `{"user_id":"`+userID+`","currency":"ETB"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	var w walletBody
	decode(t, resp, &w)

	if _, err := uuid.Parse(w.ID); err != nil {
		t.Errorf("id %q is not a UUID", w.ID)
	}
	if w.UserID != userID || w.Currency != "ETB" || w.Balance != "0.00" || w.Status != "ACTIVE" {
		t.Errorf("unexpected wallet %+v", w)
	}
	for name, ts := range map[string]string{"created_at": w.CreatedAt, "updated_at": w.UpdatedAt} {
		parsed, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			t.Errorf("%s %q is not RFC 3339: %v", name, ts, err)
			continue
		}
		if _, offset := parsed.Zone(); offset != 0 || ts[len(ts)-1] != 'Z' {
			t.Errorf("%s %q is not UTC", name, ts)
		}
	}
}

func TestCreateWalletTwiceForSameUserAndCurrencyConflicts(t *testing.T) {
	srv := newServer(t)
	userID := uuid.NewString()
	body := `{"user_id":"` + userID + `","currency":"USD"}`

	if resp := postJSON(t, srv, "/v1/wallets", body); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first create = %d, want 201", resp.StatusCode)
	}
	resp := postJSON(t, srv, "/v1/wallets", body)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second create = %d, want 409", resp.StatusCode)
	}
	if p := decodeProblem(t, resp); p.Code != "wallet_already_exists" {
		t.Errorf("code = %q, want wallet_already_exists", p.Code)
	}

	// The same User may still hold a Wallet in another Currency.
	if resp := postJSON(t, srv, "/v1/wallets", `{"user_id":"`+userID+`","currency":"EUR"}`); resp.StatusCode != http.StatusCreated {
		t.Errorf("create in another currency = %d, want 201", resp.StatusCode)
	}
}

func TestCreateWalletRejectsInvalidInput(t *testing.T) {
	srv := newServer(t)
	validUser := uuid.NewString()
	cases := []struct {
		name       string
		body       string
		wantFields []string
	}{
		{"unsupported currency", `{"user_id":"` + validUser + `","currency":"GBP"}`, []string{"currency"}},
		{"lower-case currency", `{"user_id":"` + validUser + `","currency":"usd"}`, []string{"currency"}},
		{"malformed user_id", `{"user_id":"not-a-uuid","currency":"ETB"}`, []string{"user_id"}},
		{"user_id without hyphens", `{"user_id":"` + strings.ReplaceAll(uuid.NewString(), "-", "") + `","currency":"ETB"}`, []string{"user_id"}},
		{"missing both", `{}`, []string{"user_id", "currency"}},
		{"wrong type", `{"user_id":1,"currency":"ETB"}`, []string{"user_id"}},
		{"unknown field", `{"user_id":"` + validUser + `","currency":"ETB","balance":"5.00"}`, []string{"balance"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postJSON(t, srv, "/v1/wallets", tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
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

func TestGetWallet(t *testing.T) {
	srv := newServer(t)
	resp := postJSON(t, srv, "/v1/wallets", `{"user_id":"`+uuid.NewString()+`","currency":"EUR"}`)
	var created walletBody
	decode(t, resp, &created)

	resp = get(t, srv, "/v1/wallets/"+created.ID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got walletBody
	decode(t, resp, &got)
	if got != created {
		t.Errorf("GET = %+v, want %+v", got, created)
	}
}

func TestGetWalletNotFound(t *testing.T) {
	srv := newServer(t)

	resp := get(t, srv, "/v1/wallets/"+uuid.NewString())
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if p := decodeProblem(t, resp); p.Code != "wallet_not_found" {
		t.Errorf("code = %q, want wallet_not_found", p.Code)
	}
}

func TestGetWalletMalformedID(t *testing.T) {
	srv := newServer(t)

	resp := get(t, srv, "/v1/wallets/not-a-uuid")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	p := decodeProblem(t, resp)
	if p.Code != "validation_failed" || !slices.Equal(p.fieldNames(), []string{"id"}) {
		t.Errorf("unexpected problem %+v", p)
	}
}

type walletListBody struct {
	Data       []walletBody `json:"data"`
	NextCursor *string      `json:"next_cursor"`
}

// createWallet opens a Wallet through the API and returns it.
func createWallet(t *testing.T, srv *httptest.Server, userID, currency string) walletBody {
	t.Helper()
	resp := postJSON(t, srv, "/v1/wallets", `{"user_id":"`+userID+`","currency":"`+currency+`"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create wallet = %d, want 201 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	var w walletBody
	decode(t, resp, &w)
	return w
}

func listWallets(t *testing.T, srv *httptest.Server, query string) walletListBody {
	t.Helper()
	resp := get(t, srv, "/v1/wallets"+query)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list wallets %q = %d, want 200 (body %s)", query, resp.StatusCode, readBody(t, resp))
	}
	var list walletListBody
	decode(t, resp, &list)
	return list
}

func walletIDs(ws []walletBody) []string {
	ids := make([]string, len(ws))
	for i, w := range ws {
		ids[i] = w.ID
	}
	return ids
}

func TestListWalletsReturnsNewestFirst(t *testing.T) {
	srv := newServer(t)
	first := createWallet(t, srv, uuid.NewString(), "ETB")
	second := createWallet(t, srv, uuid.NewString(), "USD")
	third := createWallet(t, srv, uuid.NewString(), "EUR")

	list := listWallets(t, srv, "")

	if got, want := walletIDs(list.Data), []string{third.ID, second.ID, first.ID}; !slices.Equal(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
	if list.Data[2] != first {
		t.Errorf("listed wallet = %+v, want %+v", list.Data[2], first)
	}
	if list.NextCursor != nil {
		t.Errorf("next_cursor = %q, want none on the only page", *list.NextCursor)
	}
}

func TestListWalletsWhenNoneExistReturnsEmptyArray(t *testing.T) {
	srv := newServer(t)

	resp := get(t, srv, "/v1/wallets")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if body := strings.TrimSpace(readBody(t, resp)); body != `{"data":[],"next_cursor":null}` {
		t.Errorf("body = %s", body)
	}
}

func TestListWalletsFilters(t *testing.T) {
	srv := newServer(t)
	alice, bob := uuid.NewString(), uuid.NewString()
	aliceETB := createWallet(t, srv, alice, "ETB")
	aliceUSD := createWallet(t, srv, alice, "USD")
	bobETB := createWallet(t, srv, bob, "ETB")
	bobUSD := createWallet(t, srv, bob, "USD")
	// Freeze two Wallets directly: there is no status-change endpoint yet.
	if _, err := pool.Exec(context.Background(),
		`UPDATE wallets SET status = 'FROZEN' WHERE id = ANY($1::uuid[])`,
		[]string{aliceUSD.ID, bobETB.ID}); err != nil {
		t.Fatalf("freeze wallets: %v", err)
	}

	cases := []struct {
		name  string
		query string
		want  []walletBody
	}{
		{"user", "?user_id=" + alice, []walletBody{aliceUSD, aliceETB}},
		{"status", "?status=FROZEN", []walletBody{bobETB, aliceUSD}},
		{"currency", "?currency=ETB", []walletBody{bobETB, aliceETB}},
		{"user and status", "?user_id=" + bob + "&status=ACTIVE", []walletBody{bobUSD}},
		{"user and currency", "?user_id=" + alice + "&currency=ETB", []walletBody{aliceETB}},
		{"status and currency", "?status=FROZEN&currency=USD", []walletBody{aliceUSD}},
		{"all three", "?user_id=" + bob + "&status=FROZEN&currency=ETB", []walletBody{bobETB}},
		{"no match", "?user_id=" + uuid.NewString(), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			list := listWallets(t, srv, tc.query)
			got := walletIDs(list.Data)
			want := []string{}
			for _, w := range tc.want {
				want = append(want, w.ID)
			}
			if !slices.Equal(got, want) {
				t.Errorf("ids = %v, want %v", got, want)
			}
		})
	}
}

// pageThrough follows next_cursor from the first page until it runs out and
// returns each page's wallets in order.
func pageThrough(t *testing.T, srv *httptest.Server, query string) [][]walletBody {
	t.Helper()
	var pages [][]walletBody
	cursor := ""
	for range 50 {
		q := query
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		list := listWallets(t, srv, q)
		pages = append(pages, list.Data)
		if list.NextCursor == nil {
			return pages
		}
		cursor = *list.NextCursor
	}
	t.Fatal("pagination did not terminate")
	return nil
}

func pageSizes(pages [][]walletBody) []int {
	sizes := make([]int, len(pages))
	for i, p := range pages {
		sizes[i] = len(p)
	}
	return sizes
}

func TestListWalletsPaginationVisitsEveryWalletOnce(t *testing.T) {
	srv := newServer(t)
	var created []walletBody // oldest first
	for range 7 {
		created = append(created, createWallet(t, srv, uuid.NewString(), "ETB"))
	}
	var want []string // newest first
	for _, w := range slices.Backward(created) {
		want = append(want, w.ID)
	}

	pages := pageThrough(t, srv, "?limit=3")

	if got := pageSizes(pages); !slices.Equal(got, []int{3, 3, 1}) {
		t.Errorf("page sizes = %v, want [3 3 1]", got)
	}
	var got []string
	for _, p := range pages {
		got = append(got, walletIDs(p)...)
	}
	if !slices.Equal(got, want) {
		t.Errorf("paged ids = %v, want %v", got, want)
	}
}

func TestListWalletsLastPageHasNoCursorWhenCountIsAMultipleOfLimit(t *testing.T) {
	srv := newServer(t)
	for range 6 {
		createWallet(t, srv, uuid.NewString(), "ETB")
	}

	pages := pageThrough(t, srv, "?limit=3")

	if got := pageSizes(pages); !slices.Equal(got, []int{3, 3}) {
		t.Errorf("page sizes = %v, want [3 3]", got)
	}
}

func TestListWalletsPaginationIsStableWhileWalletsAreCreated(t *testing.T) {
	srv := newServer(t)
	var original []string
	for range 4 {
		original = append(original, createWallet(t, srv, uuid.NewString(), "ETB").ID)
	}
	slices.Reverse(original) // newest first

	first := listWallets(t, srv, "?limit=2")
	createWallet(t, srv, uuid.NewString(), "ETB") // arrives between pages
	if first.NextCursor == nil {
		t.Fatal("first page has no next_cursor")
	}
	second := listWallets(t, srv, "?limit=2&cursor="+*first.NextCursor)

	got := append(walletIDs(first.Data), walletIDs(second.Data)...)
	if !slices.Equal(got, original) {
		t.Errorf("ids = %v, want %v", got, original)
	}
	if second.NextCursor != nil {
		t.Errorf("second page next_cursor = %q, want none", *second.NextCursor)
	}
}

func TestListWalletsPaginationKeepsFilters(t *testing.T) {
	srv := newServer(t)
	user := uuid.NewString()
	var want []string
	for _, currency := range []string{"ETB", "USD", "EUR"} {
		want = append([]string{createWallet(t, srv, user, currency).ID}, want...)
		createWallet(t, srv, uuid.NewString(), currency) // another User's, never matches
	}

	pages := pageThrough(t, srv, "?user_id="+user+"&limit=2")

	var got []string
	for _, p := range pages {
		got = append(got, walletIDs(p)...)
	}
	if !slices.Equal(got, want) || !slices.Equal(pageSizes(pages), []int{2, 1}) {
		t.Errorf("paged ids = %v (sizes %v), want %v", got, pageSizes(pages), want)
	}
}

func TestListWalletsDefaultsToTwentyPerPage(t *testing.T) {
	srv := newServer(t)
	for range 21 {
		createWallet(t, srv, uuid.NewString(), "ETB")
	}

	list := listWallets(t, srv, "")

	if len(list.Data) != 20 || list.NextCursor == nil {
		t.Errorf("got %d wallets, next_cursor %v; want 20 and a next_cursor", len(list.Data), list.NextCursor)
	}
}

func forgedCursor(payload string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

func TestListWalletsRejectsInvalidQuery(t *testing.T) {
	srv := newServer(t)
	// A well-formed base64url string whose content is not a cursor we issued.
	notACursor := base64.RawURLEncoding.EncodeToString([]byte(`{}`))
	cases := []struct {
		name       string
		query      string
		wantFields []string
	}{
		{"limit above max", "?limit=101", []string{"limit"}},
		{"limit zero", "?limit=0", []string{"limit"}},
		{"limit negative", "?limit=-1", []string{"limit"}},
		{"limit signed", "?limit=%2B5", []string{"limit"}},
		{"limit not a number", "?limit=ten", []string{"limit"}},
		{"limit empty", "?limit=", []string{"limit"}},
		{"cursor garbage", "?cursor=!!!", []string{"cursor"}},
		{"cursor not ours", "?cursor=" + notACursor, []string{"cursor"}},
		{"cursor with out-of-range time", "?cursor=" + forgedCursor(`{"t":9223372036854775807,"id":"`+uuid.NewString()+`"}`), []string{"cursor"}},
		{"cursor with negative time", "?cursor=" + forgedCursor(`{"t":-9223372036854775808,"id":"`+uuid.NewString()+`"}`), []string{"cursor"}},
		{"cursor empty", "?cursor=", []string{"cursor"}},
		{"user_id malformed", "?user_id=not-a-uuid", []string{"user_id"}},
		{"user_id empty", "?user_id=", []string{"user_id"}},
		{"status unknown", "?status=DELETED", []string{"status"}},
		{"status lower-case", "?status=active", []string{"status"}},
		{"status empty", "?status=", []string{"status"}},
		{"currency unsupported", "?currency=GBP", []string{"currency"}},
		{"currency lower-case", "?currency=usd", []string{"currency"}},
		{"currency empty", "?currency=", []string{"currency"}},
		{"every problem reported", "?user_id=x&status=x&currency=x&limit=0&cursor=!", []string{"user_id", "status", "currency", "limit", "cursor"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := get(t, srv, "/v1/wallets"+tc.query)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
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

func TestListWalletsAcceptsLimitBounds(t *testing.T) {
	srv := newServer(t)
	for range 2 {
		createWallet(t, srv, uuid.NewString(), "ETB")
	}

	if list := listWallets(t, srv, "?limit=1"); len(list.Data) != 1 || list.NextCursor == nil {
		t.Errorf("limit=1: %d wallets, next_cursor %v", len(list.Data), list.NextCursor)
	}
	if list := listWallets(t, srv, "?limit=100"); len(list.Data) != 2 || list.NextCursor != nil {
		t.Errorf("limit=100: %d wallets, next_cursor %v", len(list.Data), list.NextCursor)
	}
}
