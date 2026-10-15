package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/google/uuid"

	"github.com/Johna210/wallet_and_payment/internal/domain"
)

const requestIDHeader = "X-Request-ID"

type ctxKey int

const (
	requestIDKey ctxKey = iota
	routeKey
)

// RequestIDFrom returns the request id stored by the requestID middleware.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// route carries the matched route pattern from the router back out to the
// access log. Middlewares that call r.WithContext hand inner handlers a copy
// of the request, so r.Pattern is not visible outside; a shared pointer is.
type route struct{ pattern string }

func setRoutePattern(ctx context.Context, pattern string) {
	if rt, ok := ctx.Value(routeKey).(*route); ok {
		rt.pattern = pattern
	}
}

// requestID accepts a well-formed incoming X-Request-ID or generates one,
// stores it in the context, and always echoes it on the response.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !validRequestID(id) {
			id = uuid.NewString()
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// validRequestID bounds what a client can inject into logs and headers.
func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '-' || c == '_' || c == '.' || c == ':'
		if !ok {
			return false
		}
	}
	return true
}

// statusRecorder captures the status code and body size for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (sr *statusRecorder) WriteHeader(status int) {
	if sr.status == 0 {
		sr.status = status
	}
	sr.ResponseWriter.WriteHeader(status)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if sr.status == 0 {
		sr.status = http.StatusOK
	}
	n, err := sr.ResponseWriter.Write(b)
	sr.bytes += n
	return n, err
}

func (sr *statusRecorder) Unwrap() http.ResponseWriter { return sr.ResponseWriter }

// accessLog writes one structured line per request. The route is the matched
// pattern, never the raw path, so it can be aggregated.
func accessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rt := &route{}
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), routeKey, rt)))

			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			level := slog.LevelInfo
			if status >= http.StatusInternalServerError {
				level = slog.LevelError
			}
			logger.LogAttrs(r.Context(), level, "http request",
				slog.String("request_id", RequestIDFrom(r.Context())),
				slog.String("method", r.Method),
				slog.String("route", rt.pattern),
				slog.Int("status", status),
				slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000),
				slog.Int("bytes", rec.bytes),
			)
		})
	}
}

// recoverPanic turns a handler panic into a logged stack trace and a 500
// problem response, so one bad request cannot take the process down.
func recoverPanic(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				if v == http.ErrAbortHandler {
					panic(v)
				}
				logger.ErrorContext(r.Context(), "panic recovered",
					slog.String("request_id", RequestIDFrom(r.Context())),
					slog.Any("panic", v),
					slog.String("stack", string(debug.Stack())))
				writeProblem(w, r, http.StatusInternalServerError,
					domain.NewError(codeInternal, "an unexpected error occurred"))
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// timeout bounds every request's context; DB calls made with it are
// cancelled when the deadline passes.
func timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// limitBody caps how many bytes a handler can read from the request body.
func limitBody(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}
