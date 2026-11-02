package githubapp

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

func TestLimitTransport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		size    int
		wantErr error
	}{
		{name: "at the limit", size: maxResponseSize},
		{name: "over the limit", size: maxResponseSize + 1, wantErr: errResponseTooLarge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, "/big", fakehttp.Response{Body: bytes.Repeat([]byte("z"), tt.size)})
			api.ExpectAllCalled()

			client := &http.Client{Transport: limitTransport{next: api.Client().Transport}}

			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, api.URL()+"/big", nil)
			require.NoError(t, err)

			resp, err := client.Do(req)
			require.NoError(t, err)

			got, err := io.ReadAll(resp.Body)
			require.NoError(t, resp.Body.Close())
			require.ErrorIs(t, err, tt.wantErr)

			if tt.wantErr == nil {
				require.Len(t, got, tt.size)
			}
		})
	}
}
