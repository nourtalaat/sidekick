package strategystore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/strategy"
)

// DefaultReloadInterval is how often each replica reloads the runtime
// strategies when no notification arrives, in case one was missed.
const DefaultReloadInterval = time.Minute

// listenRetryInterval is the pause before listening again after the
// listen connection failed.
const listenRetryInterval = 5 * time.Second

// closeTimeout bounds closing the listen connection.
const closeTimeout = 5 * time.Second

// maxKeptVersions is how many old versions the cache keeps for the
// investigations that started with them. Past it, the cache starts over.
const maxKeptVersions = 256

// Options set up a Cache. Base and Pool are required.
type Options struct {
	// Base holds the built-in and file strategies, which the runtime ones
	// are added to.
	Base *strategy.Set
	// Pool is the database the runtime strategies are in.
	Pool *pgxpool.Pool
	// Interval is how often the strategies are reloaded without a
	// notification. Zero means DefaultReloadInterval.
	Interval time.Duration
	// Logger gets a warning for each shadowed or invalid runtime strategy,
	// once, and a line when a reload fails. Nil logs nothing.
	Logger *slog.Logger
}

// Cache holds the strategies in effect on one replica. Run keeps them
// current; until it first loads, they are those of Base alone. It is a
// strategy.Source.
type Cache struct {
	base     *strategy.Set
	store    *Store
	interval time.Duration
	log      *slog.Logger

	current atomic.Pointer[strategy.Set]

	// mu serializes reloads and guards the fields below.
	mu          sync.Mutex
	subscribers []func(context.Context, *strategy.Set) error
	// loaded is true once a reload has succeeded, and loadedKey identifies
	// the runtime strategies it applied, so a reload that finds them
	// unchanged does nothing.
	loaded    bool
	loadedKey string
	// warned are the skipped strategies already logged.
	warned map[string]bool

	// kept are old versions read from the database, by name and number.
	keptMu sync.Mutex
	kept   map[keptKey]strategy.Strategy
}

type keptKey struct {
	name   string
	number int
}

var _ strategy.Source = (*Cache)(nil)

// NewCache returns a Cache whose strategies are those of opts.Base alone.
func NewCache(opts Options) *Cache {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultReloadInterval
	}

	c := &Cache{
		base: opts.Base, store: NewStore(opts.Pool), interval: interval, log: log,
		warned: map[string]bool{}, kept: map[keptKey]strategy.Strategy{},
	}
	c.current.Store(opts.Base)

	return c
}

// Current returns the strategies in effect. The caller must not change
// them.
func (c *Cache) Current() *strategy.Set {
	return c.current.Load()
}

// Version returns the strategy name at the given version. A version of a
// runtime strategy that is no longer the current one is read from the
// database, since versions never change; so an investigation uses the
// version it started with. When that version is not available, for
// example a file strategy changed since, exact is false and the strategy in
// effect is returned. The error wraps strategy.ErrNotFound when no strategy
// has the name.
func (c *Cache) Version(ctx context.Context, name, version string) (strategy.Strategy, bool, error) {
	current, found := c.Current().Get(name)
	if found && current.Version == version {
		return current, true, nil
	}

	if n, ok := strategy.RuntimeVersionNumber(version); ok {
		st, ok, err := c.stored(ctx, name, n)
		if err != nil {
			return strategy.Strategy{}, false, err
		}

		if ok {
			return st, true, nil
		}
	}

	if !found {
		return strategy.Strategy{}, false, fmt.Errorf("strategy %s: %w", name, strategy.ErrNotFound)
	}

	return current, false, nil
}

// Subscribe calls fn with the strategies in effect after every reload that
// changes them. Call it before Run. An error from fn is logged, and the
// other subscribers still run.
func (c *Cache) Subscribe(fn func(ctx context.Context, set *strategy.Set) error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.subscribers = append(c.subscribers, fn)
}

// Run reloads the strategies at once, then on every strategies_changed
// notification and every interval, until ctx ends. It returns nil when ctx
// ends; a failed reload is logged and tried again on the next signal.
func (c *Cache) Run(ctx context.Context) error {
	wake := make(chan struct{}, 1)

	var wg sync.WaitGroup
	wg.Go(func() { c.listen(ctx, wake) })

	defer wg.Wait()

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		if err := c.Reload(ctx); err != nil && ctx.Err() == nil {
			c.log.ErrorContext(ctx, "reloading the runtime strategies failed; the strategies loaded before stay in effect", "error", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-wake:
		case <-ticker.C:
		}
	}
}

// Reload reads the runtime strategies and, when they changed, merges them
// with the base set and passes the result to the subscribers.
func (c *Cache) Reload(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	current, err := c.store.Current(ctx)
	if err != nil {
		return fmt.Errorf("reload runtime strategies: %w", err)
	}

	key := keyOf(current)
	if c.loaded && key == c.loadedKey {
		return nil
	}

	e := Merge(c.base, current)
	c.warn(ctx, e)
	c.current.Store(e.Set)
	c.loaded, c.loadedKey = true, key

	for _, fn := range c.subscribers {
		if err := fn(ctx, e.Set); err != nil {
			c.log.ErrorContext(ctx, "applying the runtime strategies failed", "error", err)
		}
	}

	return nil
}

// stored returns version n of the strategy name from the database, or false
// when it is gone or no longer passes the strategy checks.
func (c *Cache) stored(ctx context.Context, name string, n int) (strategy.Strategy, bool, error) {
	key := keptKey{name: name, number: n}

	c.keptMu.Lock()
	st, ok := c.kept[key]
	c.keptMu.Unlock()

	if ok {
		return st, true, nil
	}

	v, err := c.store.Version(ctx, name, n)
	if errors.Is(err, ErrNotFound) {
		return strategy.Strategy{}, false, nil
	}

	if err != nil {
		return strategy.Strategy{}, false, fmt.Errorf("read the stored strategy: %w", err)
	}

	st, problems := strategy.ParseRuntime([]byte(v.Body), n)
	if len(problems) > 0 {
		c.log.WarnContext(ctx, "a stored strategy version no longer passes the strategy checks", "strategy", name, "version", n, "problems", joinProblems(problems))

		return strategy.Strategy{}, false, nil
	}

	c.keptMu.Lock()
	defer c.keptMu.Unlock()

	if len(c.kept) >= maxKeptVersions {
		clear(c.kept)
	}

	c.kept[key] = st

	return st, true, nil
}

// warn logs each shadowed or invalid strategy the first time it shows up.
func (c *Cache) warn(ctx context.Context, e Effective) {
	for _, s := range e.Shadowed {
		if c.markWarned("shadowed", s) {
			c.log.WarnContext(ctx, "a runtime strategy has the name of a strategy file, which is used instead", "strategy", s.Name, "version", s.Version)
		}
	}

	for _, s := range e.Invalid {
		if c.markWarned("invalid", s) {
			c.log.WarnContext(ctx, "a runtime strategy fails the strategy checks and is not used", "strategy", s.Name, "version", s.Version, "problems", s.Reason)
		}
	}
}

func (c *Cache) markWarned(what string, s Skipped) bool {
	k := fmt.Sprintf("%s\x00%s\x00%d", what, s.Name, s.Version)
	if c.warned[k] {
		return false
	}

	c.warned[k] = true

	return true
}

// listen signals wake on every strategies_changed notification, and once
// each time it starts listening, since a change may have been missed while
// no connection listened. When the connection fails it listens again after
// a pause; the interval reloads meanwhile.
func (c *Cache) listen(ctx context.Context, wake chan<- struct{}) {
	for {
		err := c.listenOnce(ctx, wake)
		if ctx.Err() != nil {
			return
		}

		c.log.WarnContext(ctx, "listening for runtime strategy changes failed; reloading every interval until it works again", "error", err)

		select {
		case <-ctx.Done():
			return
		case <-time.After(listenRetryInterval):
		}
	}
}

func (c *Cache) listenOnce(ctx context.Context, wake chan<- struct{}) error {
	pooled, err := c.store.pool.Acquire(ctx)
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
			c.log.WarnContext(ctx, "closing the strategies listen connection failed", "error", err)
		}
	}()

	if err := db.New(conn).ListenStrategiesChanged(ctx); err != nil {
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

// keyOf identifies the current versions of the runtime strategies by name
// and number. A version never changes, so the number is enough.
func keyOf(current []Version) string {
	var b strings.Builder

	for _, v := range current {
		fmt.Fprintf(&b, "%s %d\n", v.Name, v.Number)
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
