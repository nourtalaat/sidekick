package anthropic

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/model"
)

func TestCheckKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response fakehttp.Response
		wantKind model.ErrorKind
	}{
		{
			name:     "the key works",
			response: fakehttp.Response{BodyFile: filepath.Join("fixture", "models.json"), Headers: map[string]string{"Content-Type": contentTypeJSON}},
		},
		{
			name:     "the key is rejected",
			response: fakehttp.Response{Status: http.StatusUnauthorized, BodyFile: filepath.Join("fixture", "auth.json"), Headers: map[string]string{"Content-Type": contentTypeJSON}},
			wantKind: model.ErrAuth,
		},
		{
			name:     "the API is overloaded",
			response: fakehttp.Response{Status: statusOverloaded, BodyFile: filepath.Join("fixture", "overloaded.json"), Headers: map[string]string{"Content-Type": contentTypeJSON}},
			wantKind: model.ErrOverloaded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, "/v1/models", tt.response)
			api.ExpectAllCalled()

			c := newTestConnector(t, api, "claude-opus-5-5", nil)

			err := c.CheckKey(t.Context())

			sent := api.Requests()
			require.Len(t, sent, 1, "one call, never retried")
			require.Equal(t, "1", sent[0].Query.Get("limit"))
			require.Contains(t, sentKey(sent[0]), testAPIKey)

			if tt.wantKind == "" {
				require.NoError(t, err)

				return
			}

			var merr *model.Error
			require.ErrorAs(t, err, &merr)
			require.Equal(t, tt.wantKind, merr.Kind)
		})
	}
}
