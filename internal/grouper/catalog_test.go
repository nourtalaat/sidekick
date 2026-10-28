package grouper

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/source"
)

// catalogSource is a source that is also a service catalog.
type catalogSource struct {
	*source.MockSource
	*source.MockServiceCatalog
}

var errCatalogDown = errors.New("catalog down")

func TestCatalogRefresh(t *testing.T) {
	t.Parallel()

	first := []source.Dependency{
		{Service: "checkout-api", DependsOn: "orders-db"},
		{Service: "storefront", DependsOn: "checkout-api"},
	}

	tests := []struct {
		name string
		// reads are what each read of the catalog returns, in order.
		reads []func() ([]source.Dependency, error)
		want  map[string][]string
	}{
		{
			name:  "one read",
			reads: []func() ([]source.Dependency, error){func() ([]source.Dependency, error) { return first, nil }},
			want: map[string][]string{
				"checkout-api": {"checkout-api", "orders-db", "storefront"},
				"orders-db":    {"orders-db", "checkout-api"},
				"storefront":   {"storefront", "checkout-api"},
				"unknown":      {"unknown"},
			},
		},
		{
			name: "a failed read keeps the last dependencies",
			reads: []func() ([]source.Dependency, error){
				func() ([]source.Dependency, error) { return first, nil },
				func() ([]source.Dependency, error) { return nil, errCatalogDown },
			},
			want: map[string][]string{
				"checkout-api": {"checkout-api", "orders-db", "storefront"},
				"orders-db":    {"orders-db", "checkout-api"},
			},
		},
		{
			name: "a later read replaces the dependencies",
			reads: []func() ([]source.Dependency, error){
				func() ([]source.Dependency, error) { return first, nil },
				func() ([]source.Dependency, error) {
					return []source.Dependency{{Service: "checkout-api", DependsOn: "payments-api"}}, nil
				},
			},
			want: map[string][]string{
				"checkout-api": {"checkout-api", "payments-api"},
				"orders-db":    {"orders-db"},
				"storefront":   {"storefront"},
			},
		},
		{
			name: "empty names and a service that depends on itself are left out",
			reads: []func() ([]source.Dependency, error){func() ([]source.Dependency, error) {
				return []source.Dependency{{Service: "a", DependsOn: "a"}, {Service: "", DependsOn: "b"}, {Service: "c", DependsOn: ""}}, nil
			}},
			want: map[string][]string{"a": {"a"}, "b": {"b"}, "c": {"c"}},
		},
		{
			name:  "a catalog that never answered knows nothing",
			reads: []func() ([]source.Dependency, error){func() ([]source.Dependency, error) { return nil, errCatalogDown }},
			want:  map[string][]string{"checkout-api": {"checkout-api"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			sc := source.NewMockServiceCatalog(ctrl)

			for _, read := range tt.reads {
				sc.EXPECT().ServiceDependencies(gomock.Any()).DoAndReturn(func(context.Context) ([]source.Dependency, error) {
					return read()
				})
			}

			c := NewCatalog(map[string]source.Source{
				"backstage": catalogSource{MockSource: source.NewMockSource(ctrl), MockServiceCatalog: sc},
				"k8s":       source.NewMockSource(ctrl),
			}, nil)
			require.False(t, c.Empty())

			for range tt.reads {
				c.Refresh(t.Context())
			}

			for service, want := range tt.want {
				require.Equal(t, want, c.Related(service), service)
			}
		})
	}
}

func TestCatalogEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		catalog func(ctrl *gomock.Controller) *Catalog
	}{
		{name: "a nil catalog", catalog: func(*gomock.Controller) *Catalog { return nil }},
		{name: "no sources", catalog: func(*gomock.Controller) *Catalog { return NewCatalog(nil, nil) }},
		{
			name: "no source is a service catalog",
			catalog: func(ctrl *gomock.Controller) *Catalog {
				return NewCatalog(map[string]source.Source{"k8s": source.NewMockSource(ctrl)}, nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := tt.catalog(gomock.NewController(t))
			require.True(t, c.Empty())
			require.Equal(t, []string{"checkout-api"}, c.Related("checkout-api"))
		})
	}
}

func TestCatalogRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "reads at once and returns when the context ends"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(t.Context())

			sc := source.NewMockServiceCatalog(gomock.NewController(t))
			sc.EXPECT().ServiceDependencies(gomock.Any()).DoAndReturn(func(context.Context) ([]source.Dependency, error) {
				cancel()

				return []source.Dependency{{Service: "checkout-api", DependsOn: "orders-db"}}, nil
			})

			c := NewCatalog(map[string]source.Source{"backstage": catalogSource{MockServiceCatalog: sc}}, nil)
			c.Run(ctx)

			require.Equal(t, []string{"checkout-api", "orders-db"}, c.Related("checkout-api"))
		})
	}
}
