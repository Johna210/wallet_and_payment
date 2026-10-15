package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/Johna210/wallet_and_payment/internal/domain"
)

// Pinger checks that a dependency is reachable; *pgxpool.Pool satisfies it.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Readiness decides whether this instance should receive traffic: the
// database must answer and the service must not be shutting down.
type Readiness struct {
	db           Pinger
	shuttingDown atomic.Bool
}

// NewReadiness returns a Readiness that pings db on every check.
func NewReadiness(db Pinger) *Readiness {
	return &Readiness{db: db}
}

// SetShuttingDown makes every later readiness check fail, so load balancers
// stop routing new traffic here before the server stops accepting it.
func (rd *Readiness) SetShuttingDown() {
	rd.shuttingDown.Store(true)
}

const readinessPingTimeout = 2 * time.Second

type healthResponse struct {
	Status string `json:"status"`
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

func handleReadyz(rd *Readiness, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if rd.shuttingDown.Load() {
			writeProblem(w, r, http.StatusServiceUnavailable,
				domain.NewError(codeNotReady, "the service is shutting down"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), readinessPingTimeout)
		defer cancel()
		if err := rd.db.Ping(ctx); err != nil {
			logger.WarnContext(r.Context(), "readiness check failed: database unreachable",
				slog.String("request_id", RequestIDFrom(r.Context())),
				slog.String("error", err.Error()))
			writeProblem(w, r, http.StatusServiceUnavailable,
				domain.NewError(codeNotReady, "the database is unreachable"))
			return
		}
		writeJSON(w, http.StatusOK, healthResponse{Status: "ready"})
	}
}
