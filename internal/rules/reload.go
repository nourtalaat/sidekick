package rules

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// DefaultReloadInterval is how often each replica reloads the runtime
// rules when no notification arrives, in case one was missed.
const DefaultReloadInterval = time.Minute

// listenRetryInterval is the pause before listening again after the
// listen connection failed.
const listenRetryInterval = 5 * time.Second

// closeTimeout bounds closing the listen connection.
const closeTimeout = 5 * time.Second

// Options set up a Cache. File and Pool are required.
type Options struct {
	// File is the loaded config, whose rules win over runtime ones.
	File *config.Config
	// Pool is the database the runtime rules are in.
	Pool *pgxpool.Pool
	// Interval is how often the rules are reloaded without a
	// notification. Zero means DefaultReloadInterval.
	Interval time.Duration
	// Logger gets a warning for each shadowed or invalid runtime rule,
	// once, and a line when a reload fails. Nil logs nothing.
	Logger *slog.Logger
}

// Cache holds the rules in effect on one replica. Run keeps them current;
// until it first loads, they are the file's rules alone.
type Cache struct {
	file     *config.Config
	pool     *pgxpool.Pool
	interval time.Duration
	log      *slog.Logger

	current atomic.Pointer[Effective]

	// mu serializes reloads and guards the fields below.
	mu          sync.Mutex
	subscribers []func(context.Context, *Effective) error
	// loadedKey identifies the runtime rules last applied, so a reload
	// that finds them unchanged does nothing.
	loadedKey string
	// warned are the skipped rules already logged, by kind, name and
	// reason.
	warned map[string]bool
}

// NewCache returns a Cache whose rules are those of the file alone.
func NewCache(ctx context.Context, opts Options) *Cache {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultReloadInterval
	}

	c := &Cache{file: opts.File, pool: opts.Pool, interval: interval, log: log, warned: map[string]bool{}}
	c.current.Store(Merge(ctx, opts.File, Runtime{}))

	return c
}

// Current returns the rules in effect. The caller must not change them.
func (c *Cache) Current() *Effective {
	return c.current.Load()
}

// Subscribe calls fn with the rules in effect after every reload that
// changes them. Call it before Run. An error from fn is logged, and the
// other subscribers still run.
func (c *Cache) Subscribe(fn func(ctx context.Context, e *Effective) error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.subscribers = append(c.subscribers, fn)
}

// Run reloads the rules at once, then on every rules_changed notification
// and every interval, until ctx ends. It returns nil when ctx ends; a
// failed reload is logged and tried again on the next signal.
func (c *Cache) Run(ctx context.Context) error {
	wake := make(chan struct{}, 1)

	var wg sync.WaitGroup
	wg.Go(func() { c.listen(ctx, wake) })

	defer wg.Wait()

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		if err := c.Reload(ctx); err != nil && ctx.Err() == nil {
			c.log.ErrorContext(ctx, "reloading the runtime rules failed; the rules loaded before stay in effect", "error", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-wake:
		case <-ticker.C:
		}
	}
}

// Reload reads the runtime rules and, when they changed, merges them with
// the file's and passes the result to the subscribers.
func (c *Cache) Reload(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	rt, err := load(ctx, c.pool)
	if err != nil {
		return fmt.Errorf("reload runtime rules: %w", err)
	}

	key := rt.key()
	if key == c.loadedKey && !c.Current().LoadedAt.IsZero() {
		return nil
	}

	e := Merge(ctx, c.file, rt)
	e.LoadedAt = time.Now()
	c.warn(ctx, e)
	c.current.Store(e)
	c.loadedKey = key

	for _, fn := range c.subscribers {
		if err := fn(ctx, e); err != nil {
			c.log.ErrorContext(ctx, "applying the runtime rules failed", "error", err)
		}
	}

	return nil
}

// warn logs each shadowed or invalid rule the first time it shows up.
func (c *Cache) warn(ctx context.Context, e *Effective) {
	for _, s := range e.Shadowed {
		if c.markWarned("shadowed", s) {
			c.log.WarnContext(ctx, "a runtime rule has the name of a rule in the config file, which is used instead", "kind", s.Kind, "name", s.Name)
		}
	}

	for _, s := range e.Invalid {
		if c.markWarned("invalid", s) {
			c.log.WarnContext(ctx, "a runtime rule fails the config checks and is not used", "kind", s.Kind, "name", s.Name, "problems", s.Reason)
		}
	}
}

func (c *Cache) markWarned(what string, s Skipped) bool {
	k := what + "\x00" + s.Kind + "\x00" + s.Name + "\x00" + s.Reason
	if c.warned[k] {
		return false
	}

	c.warned[k] = true

	return true
}

// listen signals wake on every rules_changed notification, and once each
// time it starts listening, since a change may have been missed while no
// connection listened. When the connection fails it listens again after a
// pause; the interval reloads meanwhile.
func (c *Cache) listen(ctx context.Context, wake chan<- struct{}) {
	for {
		err := c.listenOnce(ctx, wake)
		if ctx.Err() != nil {
			return
		}

		c.log.WarnContext(ctx, "listening for runtime rule changes failed; reloading every interval until it works again", "error", err)

		select {
		case <-ctx.Done():
			return
		case <-time.After(listenRetryInterval):
		}
	}
}

func (c *Cache) listenOnce(ctx context.Context, wake chan<- struct{}) error {
	pooled, err := c.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}

	// The connection leaves the pool, so it never goes back still
	// listening.
	conn := pooled.Hijack()

	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancel()

		if err := conn.Close(closeCtx); err != nil {
			c.log.WarnContext(ctx, "closing the rules listen connection failed", "error", err)
		}
	}()

	if err := db.New(conn).ListenRulesChanged(ctx); err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	signal(wake)

	for {
		if _, err := conn.WaitForNotification(ctx); err != nil {
			return fmt.Errorf("wait for notification: %w", err)
		}

		signal(wake)
	}
}

// key identifies a set of runtime rules by name, version and the time of
// their last change, which tells a listener added again apart from the one
// removed before.
func (rt Runtime) key() string {
	var b strings.Builder

	for _, l := range rt.Listeners {
		fmt.Fprintf(&b, "l %s %d %d\n", l.Name, l.Version, l.UpdatedAt.UnixNano())
	}

	for _, r := range rt.Routes {
		fmt.Fprintf(&b, "r %s %d %d\n", r.Config.Name, r.Version, r.UpdatedAt.UnixNano())
	}

	return b.String()
}

// signal wakes the reload loop without blocking; one waiting signal is
// enough.
func signal(wake chan<- struct{}) {
	select {
	case wake <- struct{}{}:
	default:
	}
}
