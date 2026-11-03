package awsauth

import (
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

func TestHTTPClientLimitsResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		size    int
		wantErr bool
	}{
		{name: "a body of exactly the limit", size: MaxResponseSize},
		{name: "a body one byte over the limit", size: MaxResponseSize + 1, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, "/", fakehttp.Response{Body: make([]byte, tt.size)})

			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, api.URL()+"/", http.NoBody)
			require.NoError(t, err)

			resp, err := HTTPClient(api.Client()).Do(req)
			require.NoError(t, err)

			defer func() { require.NoError(t, resp.Body.Close()) }()

			body, err := io.ReadAll(resp.Body)
			if tt.wantErr {
				require.ErrorIs(t, err, errResponseTooLarge)

				return
			}

			require.NoError(t, err)
			require.Len(t, body, tt.size)
		})
	}
}

func TestHTTPClientWithoutBase(t *testing.T) {
	t.Parallel()

	require.NotNil(t, HTTPClient(nil))
}
