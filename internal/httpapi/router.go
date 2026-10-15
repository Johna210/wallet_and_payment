// Package httpapi is the HTTP transport: routing, middleware, request
// decoding, and the mapping from errors to RFC 9457 problem responses.
package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/Johna210/wallet_and_payment/internal/domain"
)

// Deps is everything the transport needs to build the handler.
type Deps struct {
	Logger         *slog.Logger
	Readiness      *Readiness
	RequestTimeout time.Duration
	MaxBodyBytes   int64
	Wallets        WalletService
}

// NewHandler returns the service's complete http.Handler.
func NewHandler(d Deps) http.Handler {
	mux := http.NewServeMux()
	registerRoutes(mux, d)
	return withMiddleware(mux, d)
}

// registerRoutes adds every API route to mux.
func registerRoutes(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /readyz", handleReadyz(d.Readiness, d.Logger))

	ew := errorWriter{logger: d.Logger}
	mux.HandleFunc("POST /v1/wallets", handleCreateWallet(d.Wallets, ew))
	mux.HandleFunc("GET /v1/wallets", handleListWallets(d.Wallets, ew))
	mux.HandleFunc("GET /v1/wallets/{id}", handleGetWallet(d.Wallets, ew))
	mux.HandleFunc("PATCH /v1/wallets/{id}/status", handleChangeWalletStatus(d.Wallets, ew))
}

// withMiddleware wraps mux in the middleware every route shares.
func withMiddleware(mux *http.ServeMux, d Deps) http.Handler {
	return chain(dispatch(mux),
		requestID,
		accessLog(d.Logger),
		recoverPanic(d.Logger),
		timeout(d.RequestTimeout),
		limitBody(d.MaxBodyBytes),
	)
}

// chain wraps h so the first middleware listed is the outermost.
func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// dispatch routes through mux, records the matched pattern for the access
// log, and replaces the mux's plain-text 404 and 405 responses with problems.
func dispatch(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" {
			setRoutePattern(r.Context(), pattern)
			mux.ServeHTTP(w, r)
			return
		}

		// No route matched. Let the mux decide between 404 and 405 (it also
		// computes the Allow header), then answer with a problem instead.
		p := &probe{header: http.Header{}}
		h.ServeHTTP(p, r)
		if p.status == http.StatusMethodNotAllowed {
			w.Header().Set("Allow", p.header.Get("Allow"))
			writeProblem(w, r, http.StatusMethodNotAllowed,
				domain.NewError(codeMethodNotAllowed, "the method is not allowed for this route"))
			return
		}
		writeProblem(w, r, http.StatusNotFound,
			domain.NewError(codeRouteNotFound, "no route matches the request path"))
	})
}

// probe is a ResponseWriter that only remembers the status and headers.
type probe struct {
	header http.Header
	status int
}

func (p *probe) Header() http.Header         { return p.header }
func (p *probe) Write(b []byte) (int, error) { return len(b), nil }
func (p *probe) WriteHeader(status int)      { p.status = status }
