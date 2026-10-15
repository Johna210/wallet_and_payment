package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Johna210/wallet_and_payment/internal/domain"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

type harness struct {
	handler   http.Handler
	readiness *Readiness
	logs      *bytes.Buffer
}

// newHarness builds the real middleware stack around the real routes plus any
// extra test-only routes.
func newHarness(t *testing.T, db Pinger, extra func(*http.ServeMux, Deps)) harness {
	t.Helper()
	logs := &bytes.Buffer{}
	d := Deps{
		Logger:         slog.New(slog.NewJSONHandler(logs, nil)),
		Readiness:      NewReadiness(db),
		RequestTimeout: time.Second,
		MaxBodyBytes:   64,
	}
	mux := http.NewServeMux()
	registerRoutes(mux, d)
	if extra != nil {
		extra(mux, d)
	}
	return harness{handler: withMiddleware(mux, d), readiness: d.Readiness, logs: logs}
}

func (h harness) do(method, target string, body string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func decodeProblem(t *testing.T, rec *httptest.ResponseRecorder) problem {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", ct)
	}
	var p problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem: %v (body %s)", err, rec.Body)
	}
	if p.Status != rec.Code {
		t.Errorf("problem status %d != response status %d", p.Status, rec.Code)
	}
	if p.RequestID == "" || p.RequestID != rec.Header().Get(requestIDHeader) {
		t.Errorf("problem request_id %q does not match header %q", p.RequestID, rec.Header().Get(requestIDHeader))
	}
	return p
}

func TestHealthz(t *testing.T) {
	h := newHarness(t, fakePinger{}, nil)
	rec := h.do(http.MethodGet, "/healthz", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Header().Get(requestIDHeader) == "" {
		t.Error("response has no X-Request-ID")
	}
}

func TestRequestIDIsEchoedWhenValidAndReplacedWhenNot(t *testing.T) {
	h := newHarness(t, fakePinger{}, nil)

	rec := h.do(http.MethodGet, "/healthz", "", map[string]string{requestIDHeader: "abc-123"})
	if got := rec.Header().Get(requestIDHeader); got != "abc-123" {
		t.Errorf("X-Request-ID = %q, want echoed abc-123", got)
	}

	rec = h.do(http.MethodGet, "/healthz", "", map[string]string{requestIDHeader: "bad id\nwith newline"})
	if got := rec.Header().Get(requestIDHeader); got == "" || strings.ContainsAny(got, " \n") {
		t.Errorf("X-Request-ID = %q, want a generated id", got)
	}
}

func TestReadyz(t *testing.T) {
	t.Run("database reachable", func(t *testing.T) {
		rec := newHarness(t, fakePinger{}, nil).do(http.MethodGet, "/readyz", "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	})
	t.Run("database unreachable", func(t *testing.T) {
		rec := newHarness(t, fakePinger{err: errors.New("dial tcp: refused")}, nil).do(http.MethodGet, "/readyz", "", nil)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		if p := decodeProblem(t, rec); p.Code != codeNotReady || strings.Contains(p.Detail, "refused") {
			t.Errorf("unexpected problem %+v", p)
		}
	})
	t.Run("shutting down", func(t *testing.T) {
		h := newHarness(t, fakePinger{}, nil)
		h.readiness.SetShuttingDown()
		rec := h.do(http.MethodGet, "/readyz", "", nil)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		decodeProblem(t, rec)
	})
}

func TestUnknownRouteAndWrongMethodAreProblems(t *testing.T) {
	h := newHarness(t, fakePinger{}, nil)

	rec := h.do(http.MethodGet, "/nope", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if p := decodeProblem(t, rec); p.Code != codeRouteNotFound {
		t.Errorf("code = %q, want %q", p.Code, codeRouteNotFound)
	}

	rec = h.do(http.MethodPost, "/healthz", "", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if p := decodeProblem(t, rec); p.Code != codeMethodNotAllowed {
		t.Errorf("code = %q, want %q", p.Code, codeMethodNotAllowed)
	}
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, http.MethodGet) {
		t.Errorf("Allow = %q, want it to list GET", allow)
	}
}

func TestPanicIsRecoveredAsProblemAndLogged(t *testing.T) {
	h := newHarness(t, fakePinger{}, func(mux *http.ServeMux, _ Deps) {
		mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) { panic("kaboom") })
	})

	rec := h.do(http.MethodGet, "/boom", "", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if p := decodeProblem(t, rec); p.Code != codeInternal || strings.Contains(p.Detail, "kaboom") {
		t.Errorf("unexpected problem %+v", p)
	}
	logs := h.logs.String()
	if !strings.Contains(logs, "panic recovered") || !strings.Contains(logs, "goroutine") {
		t.Errorf("panic and stack were not logged: %s", logs)
	}

	// The handler keeps serving after a panic.
	if rec := h.do(http.MethodGet, "/healthz", "", nil); rec.Code != http.StatusOK {
		t.Errorf("healthz after panic = %d, want 200", rec.Code)
	}
}

func TestAccessLogUsesRoutePattern(t *testing.T) {
	h := newHarness(t, fakePinger{}, func(mux *http.ServeMux, _ Deps) {
		mux.HandleFunc("GET /things/{id}", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"ok": "yes"})
		})
	})
	h.do(http.MethodGet, "/things/42", "", map[string]string{requestIDHeader: "req-1"})

	var line map[string]any
	if err := json.Unmarshal(h.logs.Bytes(), &line); err != nil {
		t.Fatalf("decode access log: %v (%s)", err, h.logs)
	}
	for k, want := range map[string]any{
		"msg": "http request", "request_id": "req-1", "method": "GET",
		"route": "GET /things/{id}", "status": float64(200),
	} {
		if line[k] != want {
			t.Errorf("log %s = %v, want %v", k, line[k], want)
		}
	}
	if line["bytes"].(float64) <= 0 {
		t.Errorf("log bytes = %v, want > 0", line["bytes"])
	}
	if _, ok := line["duration_ms"]; !ok {
		t.Error("log has no duration_ms")
	}
}

func TestErrorWriterMapsErrorsToProblems(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   domain.Code
	}{
		{"domain error", domain.NewError(domain.CodeInsufficientFunds, "not enough"), 422, domain.CodeInsufficientFunds},
		{"wrapped domain error", fmt.Errorf("deposit: %w", domain.NewError(domain.CodeWalletNotFound, "no such wallet")), 404, domain.CodeWalletNotFound},
		{"validation error", domain.ValidationError(domain.FieldError{Field: "amount", Message: "is required"}), 400, domain.CodeValidationFailed},
		{"deadline", fmt.Errorf("query: %w", context.DeadlineExceeded), 504, codeRequestTimeout},
		{"unknown error", errors.New("pq: password authentication failed for user secret"), 500, codeInternal},
		{"unmapped code", domain.NewError("made_up", "x"), 500, codeInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := &bytes.Buffer{}
			ew := errorWriter{logger: slog.New(slog.NewJSONHandler(logs, nil))}
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "rid"))
			rec := httptest.NewRecorder()
			rec.Header().Set(requestIDHeader, "rid")

			ew.write(rec, req, tc.err)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			p := decodeProblem(t, rec)
			if p.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", p.Code, tc.wantCode)
			}
			if tc.wantStatus == 500 && strings.Contains(p.Detail, "secret") {
				t.Errorf("internal detail leaked: %q", p.Detail)
			}
			if tc.wantCode == domain.CodeValidationFailed && len(p.Errors) != 1 {
				t.Errorf("errors = %+v, want one field error", p.Errors)
			}
		})
	}
}

func TestDecodeJSON(t *testing.T) {
	type payload struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	cases := []struct {
		name      string
		body      string
		wantCode  domain.Code // empty means success
		wantField string
	}{
		{"valid", `{"name":"a","count":1}`, "", ""},
		{"empty", ``, codeInvalidJSON, ""},
		{"syntax", `{"name":`, codeInvalidJSON, ""},
		{"not an object", `[1,2]`, codeInvalidJSON, ""},
		{"unknown field", `{"name":"a","extra":1}`, domain.CodeValidationFailed, "extra"},
		{"wrong type", `{"count":"x"}`, domain.CodeValidationFailed, "count"},
		{"trailing data", `{"name":"a"} {"name":"b"}`, codeInvalidJSON, ""},
		{"too large", `{"name":"` + strings.Repeat("x", 100) + `"}`, codePayloadTooLarge, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			req.Body = http.MaxBytesReader(httptest.NewRecorder(), req.Body, 64)
			var dst payload
			err := decodeJSON(req, &dst)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			de, ok := domain.AsError(err)
			if !ok || de.Code != tc.wantCode {
				t.Fatalf("error = %v, want code %q", err, tc.wantCode)
			}
			if tc.wantField != "" && (len(de.Fields) != 1 || de.Fields[0].Field != tc.wantField) {
				t.Errorf("fields = %+v, want %q", de.Fields, tc.wantField)
			}
		})
	}
}
