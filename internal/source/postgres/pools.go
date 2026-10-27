package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// Settings of every pool.
const (
	// applicationName is what pg_stat_activity shows for Sidekick's
	// sessions, so a person can tell them apart.
	applicationName = "sidekick"
	// connectTimeout bounds one connection attempt when the URL sets no
	// connect_timeout.
	connectTimeout = 10 * time.Second
	// maxConnIdleTime closes a connection no call has used for this long,
	// so an idle source holds no sessions on the database.
	maxConnIdleTime = 5 * time.Minute
)

// roleSQL reads the role a new connection runs as: its name, whether it is
// a superuser, and whether it has the privileges of pg_monitor.
const roleSQL = `SELECT current_user::text, r.rolsuper, pg_catalog.pg_has_role(current_user, 'pg_monitor', 'USAGE')
FROM pg_catalog.pg_roles r
WHERE r.rolname = current_user`

// verifyingModes are the sslmode values that verify the server's
// certificate.
var verifyingModes = []string{"verify-ca", "verify-full"}

var (
	// errNoSecrets is returned when the source is built without a way to
	// read the URL secrets.
	errNoSecrets = errors.New("no secrets resolver in deps")
	// errNotURL is returned for a secret that does not hold a postgres://
	// URL. The parse error is left out, because it can repeat the URL with
	// its password.
	errNotURL = errors.New("the URL secret must hold a postgres:// URL")
	// errInsecure is returned for a URL that does not verify the server's
	// certificate, when the database does not set allowInsecure.
	errInsecure = errors.New("the URL does not verify the server's TLS certificate")
	// errSuperuser is returned when the URL's role is a superuser.
	errSuperuser = errors.New("the role is a superuser")
	// errClosed is returned for a call after Close.
	errClosed = errors.New("the source is closed")
)

// pools holds one connection pool per database. It reads each database's
// URL secret on every call and builds a new pool only when the URL
// changed, so a rotated password needs no restart. It is safe for
// concurrent use.
type pools struct {
	secrets  registry.Secrets
	logger   *slog.Logger
	maxConns int32

	mu     sync.Mutex
	open   map[string]openPool
	closed bool
}

// openPool is the pool of one database and the hash of the URL it was
// built from.
type openPool struct {
	sum  [sha256.Size]byte
	pool *pgxpool.Pool
}

// get returns the pool for the current URL of db. A pool it replaces is
// closed in the background once the calls that use it end.
func (p *pools) get(ctx context.Context, db Database) (*pgxpool.Pool, error) {
	raw, err := secrets.ResolveText(ctx, p.secrets, db.URLSecret)
	if err != nil {
		return nil, fmt.Errorf("read the URL secret %s/%s: %w", db.URLSecret.Name, db.URLSecret.Key, err)
	}

	sum := sha256.Sum256([]byte(raw))

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil, errClosed
	}

	current, ok := p.open[db.Name]
	if ok && current.sum == sum {
		return current.pool, nil
	}

	cfg, err := p.poolConfig(raw, db)
	if err != nil {
		return nil, fmt.Errorf("database %s, URL secret %s/%s: %w", db.Name, db.URLSecret.Name, db.URLSecret.Key, err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("database %s: make the connection pool: %w", db.Name, err)
	}

	if ok {
		go current.pool.Close()
	}

	p.open[db.Name] = openPool{sum: sum, pool: pool}

	return pool, nil
}

// close closes every pool. It waits for the calls that use them to end.
func (p *pools) close() {
	p.mu.Lock()
	p.closed = true
	open := p.open
	p.open = nil
	p.mu.Unlock()

	for _, o := range open {
		o.pool.Close()
	}
}

// poolConfig parses a URL and returns the pool config Sidekick connects
// with. It refuses a URL that does not verify the server's TLS certificate
// unless every host is local or db allows it, and logs a warning when db
// allows it.
func (p *pools) poolConfig(raw string, db Database) (*pgxpool.Config, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return nil, errNotURL
	}

	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		return nil, errNotURL
	}

	mode := u.Query().Get("sslmode")
	if !slices.Contains(verifyingModes, mode) && !localHosts(cfg.ConnConfig) {
		if !db.AllowInsecure {
			return nil, fmt.Errorf("%w (sslmode=%s): set sslmode=verify-full or verify-ca in the URL, or allowInsecure on the database", errInsecure, modeName(mode))
		}

		p.logger.Warn("the connection to a Postgres source database does not verify the server's TLS certificate, so anyone on the network path can read and change its queries and results",
			"database", db.Name, "sslmode", modeName(mode))
	}

	cfg.MaxConns = p.maxConns
	cfg.MinConns = 0
	cfg.MaxConnIdleTime = maxConnIdleTime
	cfg.ConnConfig.RuntimeParams["application_name"] = applicationName

	if cfg.ConnConfig.ConnectTimeout == 0 {
		cfg.ConnConfig.ConnectTimeout = connectTimeout
	}

	cfg.AfterConnect = p.checkRole(db)

	return cfg, nil
}

// checkRole returns the check that runs on each new connection. It refuses
// a superuser, which could do far more than the tools need, and warns once
// per pool when the role lacks pg_monitor, since the tools then see only
// the role's own sessions and statements.
func (p *pools) checkRole(db Database) func(ctx context.Context, conn *pgx.Conn) error {
	var warn sync.Once

	return func(ctx context.Context, conn *pgx.Conn) error {
		var (
			role             string
			super, inMonitor bool
		)

		if err := conn.QueryRow(ctx, roleSQL).Scan(&role, &super, &inMonitor); err != nil {
			return fmt.Errorf("read the connection's role: %w", err)
		}

		if super {
			return fmt.Errorf("role %s: %w; connect as a role that has only pg_monitor", role, errSuperuser)
		}

		if !inMonitor {
			warn.Do(func() {
				p.logger.Warn("the role of a Postgres source database is not a member of pg_monitor, so the tools see only its own sessions and statements",
					"database", db.Name, "role", role)
			})
		}

		return nil
	}
}

// localHosts reports whether every host the config may connect to is a
// Unix socket or a loopback address, where TLS adds nothing.
func localHosts(cfg *pgx.ConnConfig) bool {
	hosts := []string{cfg.Host}
	for _, fb := range cfg.Fallbacks {
		hosts = append(hosts, fb.Host)
	}

	for _, host := range hosts {
		if !strings.HasPrefix(host, "/") && !httpclient.IsLoopbackHost(host) {
			return false
		}
	}

	return true
}

// modeName returns the sslmode for a message, naming the default when the
// URL sets none.
func modeName(mode string) string {
	if mode == "" {
		return "unset, so prefer"
	}

	return mode
}
