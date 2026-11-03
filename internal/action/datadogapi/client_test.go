package datadogapi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// fakeKeys resolves each secret to its own fake value, with a line break
// that the client must trim.
type fakeKeys struct{}

func (fakeKeys) Resolve(_ context.Context, ref registry.SecretRef) ([]byte, error) {
	return []byte("fake-" + ref.Key + "\n"), nil
}

func TestDoSendsTheKeys(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodGet, "/api/v1/thing", fakehttp.Response{Body: []byte(`{"name":"x"}`)})

	c := NewClient(api.Client(), api.URL(), apiRef, appRef, fakeKeys{})

	var out struct {
		Name string `json:"name"`
	}

	require.NoError(t, c.Do(t.Context(), http.MethodGet, "/api/v1/thing", nil, &out))
	require.Equal(t, "x", out.Name)

	got := api.Requests()
	require.Len(t, got, 1)
	require.Equal(t, "fake-apiKey", got[0].Header.Get("DD-API-KEY"))
	require.Equal(t, "fake-appKey", got[0].Header.Get("DD-APPLICATION-KEY"))
}

func TestDoFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    int
		body      string
		permanent bool
		message   string
	}{
		{name: "not found", status: http.StatusNotFound, body: `{"errors":["Monitor not found"]}`, permanent: true, message: "Monitor not found"},
		{name: "forbidden", status: http.StatusForbidden, body: `{"errors":["Forbidden"]}`, permanent: true, message: "Forbidden"},
		{name: "too many requests", status: http.StatusTooManyRequests, body: `{"errors":["slow down"]}`, message: "slow down"},
		{name: "server error with no message", status: http.StatusBadGateway, body: `<html>`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, "/api/v1/thing", fakehttp.Response{Status: tt.status, Body: []byte(tt.body)})

			c := NewClient(api.Client(), api.URL(), apiRef, appRef, fakeKeys{})

			err := c.Do(t.Context(), http.MethodGet, "/api/v1/thing", nil, nil)

			var status *StatusError

			require.ErrorAs(t, err, &status)
			require.Equal(t, tt.status, status.Code)
			require.Equal(t, tt.message, status.Message)
			require.Equal(t, tt.permanent, errors.Is(err, action.ErrPermanent))
		})
	}
}
