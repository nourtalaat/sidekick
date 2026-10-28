package backstage

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/source"
)

// byQuery is the path of the entity query API.
const byQuery = "/api/catalog/entities/by-query"

func TestServiceDependencies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		responses   []fakehttp.Response
		wantQueries []string
		want        []source.Dependency
		wantErr     string
	}{
		{
			name:      "pages through the catalog and keeps each dependency once",
			responses: []fakehttp.Response{fixture("entities-page-1.json"), fixture("entities-page-2.json")},
			wantQueries: []string{
				"fields=kind%2Cmetadata.name%2Crelations&filter=kind%3Dcomponent&filter=kind%3Dresource&limit=2",
				"cursor=c2Vjb25k&fields=kind%2Cmetadata.name%2Crelations&limit=2",
			},
			want: []source.Dependency{
				{Service: "checkout-api", DependsOn: "orders-db"},
				{Service: "checkout-api", DependsOn: "payments-api"},
				{Service: "storefront", DependsOn: "checkout-api"},
			},
		},
		{
			name:        "an empty catalog has no dependencies",
			responses:   []fakehttp.Response{{Headers: jsonHeaders, Body: []byte(`{"items":[],"totalItems":0,"pageInfo":{}}`)}},
			wantQueries: []string{"fields=kind%2Cmetadata.name%2Crelations&filter=kind%3Dcomponent&filter=kind%3Dresource&limit=2"},
			want:        []source.Dependency{},
		},
		{
			name:      "a page that fails fails the whole read",
			responses: []fakehttp.Response{fixture("entities-page-1.json"), failed(http.StatusServiceUnavailable, "Service Unavailable")},
			wantQueries: []string{
				"fields=kind%2Cmetadata.name%2Crelations&filter=kind%3Dcomponent&filter=kind%3Dresource&limit=2",
				"cursor=c2Vjb25k&fields=kind%2Cmetadata.name%2Crelations&limit=2",
			},
			wantErr: "Backstage answered 503: Service Unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, byQuery, tt.responses...)
			api.ExpectAllCalled()

			s := newTestSource(t, api, tokenSecrets(t), 2)

			got, err := s.ServiceDependencies(t.Context())

			var queries []string
			for _, r := range api.Requests() {
				queries = append(queries, r.Query.Encode())
			}

			require.Equal(t, tt.wantQueries, queries)

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestServiceDependenciesStopsAtMaxPages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "a catalog that always has a next page"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.HandleFunc(http.MethodGet, byQuery, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"items":[],"pageInfo":{"nextCursor":"again"}}`))
			})

			s := newTestSource(t, api, tokenSecrets(t), 0)

			_, err := s.ServiceDependencies(t.Context())
			require.ErrorIs(t, err, errTooManyPages)
			require.Len(t, api.Requests(), maxPages)
		})
	}
}
