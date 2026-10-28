package grouper

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
)

// CatalogRefreshInterval is how often a Catalog reads its service catalogs
// again.
const CatalogRefreshInterval = 10 * time.Minute

// catalogReadTimeout bounds one read of one service catalog.
const catalogReadTimeout = 2 * time.Minute

// Catalog knows which services depend on each other, from the sources that
// are service catalogs. It reads them in the background with Run and keeps
// what each one gave last, so a catalog that fails for a while still
// counts with its older dependencies. It is safe for concurrent use; a nil
// Catalog knows no dependencies.
type Catalog struct {
	catalogs map[string]source.ServiceCatalog
	log      *slog.Logger

	mu sync.RWMutex
	// dependencies are the dependencies each catalog gave last, by source
	// name.
	dependencies map[string][]source.Dependency
	// related maps a service to the services it depends on and those that
	// depend on it, sorted.
	related map[string][]string
}

// NewCatalog returns a Catalog of the sources that implement
// source.ServiceCatalog. It reads nothing until Refresh or Run. log
// receives a warning for each failed read; nil logs nothing.
func NewCatalog(sources map[string]source.Source, log *slog.Logger) *Catalog {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	c := &Catalog{catalogs: map[string]source.ServiceCatalog{}, log: log, dependencies: map[string][]source.Dependency{}}

	for name, s := range sources {
		if sc, ok := s.(source.ServiceCatalog); ok {
			c.catalogs[name] = sc
		}
	}

	return c
}

// Empty reports whether no source is a service catalog, so there is
// nothing to read.
func (c *Catalog) Empty() bool {
	return c == nil || len(c.catalogs) == 0
}

// Run reads the catalogs at once and then every CatalogRefreshInterval,
// until ctx ends.
func (c *Catalog) Run(ctx context.Context) {
	c.Refresh(ctx)

	ticker := time.NewTicker(CatalogRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.Refresh(ctx)
		}
	}
}

// Refresh reads every catalog once, one after another. A catalog whose
// read fails keeps the dependencies it gave last, and the failure is
// logged.
func (c *Catalog) Refresh(ctx context.Context) {
	for _, name := range slices.Sorted(maps.Keys(c.catalogs)) {
		deps, err := c.read(ctx, name)
		if err != nil {
			if ctx.Err() != nil {
				return
			}

			c.log.WarnContext(ctx, "cannot read the service catalog; keeping its dependencies from the last read",
				"source", name, "error", err.Error())

			continue
		}

		c.store(name, deps)
		c.log.DebugContext(ctx, "read the service catalog", "source", name, "dependencies", len(deps))
	}
}

// Related returns service and every service it depends on or that depends
// on it directly: service first, then the others sorted.
func (c *Catalog) Related(service string) []string {
	if c == nil {
		return []string{service}
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	return append([]string{service}, c.related[service]...)
}

// read reads the dependencies of the catalog called name, bounded by
// catalogReadTimeout.
func (c *Catalog) read(ctx context.Context, name string) ([]source.Dependency, error) {
	ctx, cancel := context.WithTimeout(ctx, catalogReadTimeout)
	defer cancel()

	deps, err := c.catalogs[name].ServiceDependencies(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the dependencies of source %s: %w", name, err)
	}

	return deps, nil
}

// store keeps deps as what the catalog called name gave last, and builds
// the related services of every catalog again.
func (c *Catalog) store(name string, deps []source.Dependency) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.dependencies[name] = deps
	c.related = relatedServices(c.dependencies)
}

// relatedServices maps each service in dependencies to the other services
// it has a dependency with, in either direction, sorted and each once.
func relatedServices(dependencies map[string][]source.Dependency) map[string][]string {
	sets := map[string]map[string]bool{}

	add := func(from, to string) {
		if sets[from] == nil {
			sets[from] = map[string]bool{}
		}

		sets[from][to] = true
	}

	for _, deps := range dependencies {
		for _, d := range deps {
			if d.Service == "" || d.DependsOn == "" || d.Service == d.DependsOn {
				continue
			}

			add(d.Service, d.DependsOn)
			add(d.DependsOn, d.Service)
		}
	}

	related := make(map[string][]string, len(sets))
	for service, set := range sets {
		related[service] = slices.Sorted(maps.Keys(set))
	}

	return related
}
