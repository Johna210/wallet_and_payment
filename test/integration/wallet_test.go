//go:build integration

package integration

import (
	"net/http"
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
