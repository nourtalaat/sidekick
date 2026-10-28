package backstage

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"

	"github.com/nourtalaat/sidekick/internal/source"
)

// Limits on reading the whole catalog.
const (
	// maxPageSize is how many entities one page of ServiceDependencies
	// asks for.
	maxPageSize = 500
	// maxPages bounds how many pages one ServiceDependencies call reads,
	// so a catalog that keeps sending a next page cannot keep it going
	// for ever. With maxPageSize it allows a catalog of 100,000 services
	// and resources.
	maxPages = 200
)

// dependencyFields are the entity fields ServiceDependencies asks for, so
// the pages stay small.
const dependencyFields = "kind,metadata.name,relations"

// dependencyKinds are the kinds whose entities are services in a
// dependency.
var dependencyKinds = []string{kindComponent, kindResource}

// errTooManyPages is returned when the catalog has more pages than
// maxPages.
var errTooManyPages = fmt.Errorf("the catalog has more than %d pages of services and resources", maxPages)

// entityPage is one page of the entity query API.
type entityPage struct {
	Items    []entity `json:"items"`
	PageInfo struct {
		NextCursor string `json:"nextCursor"`
	} `json:"pageInfo"`
}

// ServiceDependencies reads every component and resource of the catalog,
// page by page, and returns the dependencies their dependsOn and
// dependencyOf relations name, sorted and each once. A service is named by
// its entity name, without kind or namespace, as alerts name services.
// Relations to entities of other kinds, such as APIs, are left out.
func (s *Source) ServiceDependencies(ctx context.Context) ([]source.Dependency, error) {
	seen := map[source.Dependency]bool{}
	query := url.Values{
		"filter": {"kind=" + kindComponent, "kind=" + kindResource},
		"fields": {dependencyFields},
		"limit":  {strconv.Itoa(s.pageSize)},
	}

	for range maxPages {
		var page entityPage
		if err := s.client.get(ctx, "/entities/by-query", query, &page); err != nil {
			return nil, fmt.Errorf("read the catalog: %w", err)
		}

		for _, e := range page.Items {
			addDependencies(seen, e)
		}

		if page.PageInfo.NextCursor == "" {
			return sortedDependencies(seen), nil
		}

		query = url.Values{
			"cursor": {page.PageInfo.NextCursor},
			"fields": {dependencyFields},
			"limit":  {strconv.Itoa(s.pageSize)},
		}
	}

	return nil, errTooManyPages
}

// addDependencies adds the dependencies of e's relations to seen.
func addDependencies(seen map[source.Dependency]bool, e entity) {
	name := e.Metadata.Name
	if !validName(name) {
		return
	}

	for _, rel := range e.Relations {
		if rel.Type != relationDependsOn && rel.Type != relationDependencyOf {
			continue
		}

		kind, target, ok := targetName(rel.TargetRef)
		if !ok || !slices.Contains(dependencyKinds, kind) || target == name {
			continue
		}

		d := source.Dependency{Service: name, DependsOn: target}
		if rel.Type == relationDependencyOf {
			d = source.Dependency{Service: target, DependsOn: name}
		}

		seen[d] = true
	}
}

// sortedDependencies returns the keys of seen, sorted.
func sortedDependencies(seen map[source.Dependency]bool) []source.Dependency {
	out := make([]source.Dependency, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}

	slices.SortFunc(out, func(a, b source.Dependency) int {
		return cmp.Or(cmp.Compare(a.Service, b.Service), cmp.Compare(a.DependsOn, b.DependsOn))
	})

	return out
}
