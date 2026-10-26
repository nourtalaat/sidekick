package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/store"
)

// readyTimeout bounds the checks of one /readyz request, so a database
// that does not answer makes the probe fail instead of hang.
const readyTimeout = 5 * time.Second

const textPlain = "text/plain; charset=utf-8"

// readiness serves /readyz. The replica is ready when the config is loaded,
// the database answers and its schema has every migration this binary
// embeds. A schema that is newer is fine: during a rolling update, the
// replicas of the previous release keep serving on the schema the new one
// migrated to. Sources are never checked, so one that is down does not take
// Sidekick out of service.
type readiness struct {
	cfg  *config.Config
	pool *pgxpool.Pool
}

// check is the result of one readiness check.
type check struct {
	name    string
	ok      bool
	message string
}

// ServeHTTP answers 200 when every check passes and 503 otherwise, with one
// line per check: its name, then "ok" or what is wrong.
func (r readiness) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	ctx, cancel := context.WithTimeout(req.Context(), readyTimeout)
	defer cancel()

	checks := r.checks(ctx)

	status := http.StatusOK

	var body strings.Builder

	for _, c := range checks {
		if !c.ok {
			status = http.StatusServiceUnavailable
		}

		fmt.Fprintf(&body, "%s: %s\n", c.name, c.message)
	}

	writeText(w, status, body.String())
}

func (r readiness) checks(ctx context.Context) []check {
	cfg := check{name: "config", ok: r.cfg != nil, message: "ok"}
	if !cfg.ok {
		cfg.message = "not loaded"
	}

	db := check{name: "database", ok: true, message: "ok"}
	if err := r.pool.Ping(ctx); err != nil {
		db = check{name: "database", message: "unreachable: " + err.Error()}
	}

	schema := check{name: "schema", message: "not checked, because the database is unreachable"}
	if db.ok {
		schema = r.checkSchema(ctx)
	}

	return []check{cfg, db, schema}
}

func (r readiness) checkSchema(ctx context.Context) check {
	current, target, err := store.SchemaVersion(ctx, r.pool)
	if err != nil {
		return check{name: "schema", message: "cannot read the schema version: " + err.Error()}
	}

	if current >= target {
		return check{name: "schema", ok: true, message: "ok"}
	}

	message := fmt.Sprintf("the database has migrations up to %d but this release needs %d", current, target)
	if r.cfg.Database.Migrations == config.MigrationsManual {
		message += "; database.migrations is manual, so run `sidekick migrate up`"
	}

	return check{name: "schema", message: message}
}

// healthz serves /healthz, which answers 200 for as long as the process
// can serve requests at all. It checks nothing else, so a database outage
// never makes Kubernetes restart Sidekick.
func healthz(w http.ResponseWriter, _ *http.Request) {
	writeText(w, http.StatusOK, "ok\n")
}

func writeText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", textPlain)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body) // The client has gone away; nothing is left to tell it.
}
