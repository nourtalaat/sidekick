//go:generate mockgen -source catalog.go -destination mocks_catalog.go -package source -write_package_comment=false

package source

import "context"

// ServiceCatalog is a source that knows how services depend on each other,
// such as a Backstage catalog. Sidekick reads it in the background, outside
// any investigation, to group alerts of related services.
type ServiceCatalog interface {
	// ServiceDependencies returns every dependency between two services the
	// catalog knows of. It may take a while for a large catalog, so callers
	// run it away from any request and bound it with ctx.
	ServiceDependencies(ctx context.Context) ([]Dependency, error)
}

// Dependency says that Service depends on DependsOn. Both are service
// names as alerts give them.
type Dependency struct {
	Service   string
	DependsOn string
}
