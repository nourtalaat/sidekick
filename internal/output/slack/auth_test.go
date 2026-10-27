package slack

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

func TestAuthTest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response fakehttp.Response
		want     Identity
		wantCode string
		wantErr  bool
	}{
		{
			name:     "a valid token",
			response: fakehttp.Response{Body: []byte(`{"ok":true,"team":"Acme","user":"sidekick","team_id":"T1","user_id":"U1"}`)},
			want:     Identity{Team: "Acme", User: "sidekick"},
		},
		{
			name:     "a token Slack rejects",
			response: fakehttp.Response{Body: []byte(`{"ok":false,"error":"invalid_auth"}`)},
			wantCode: "invalid_auth",
			wantErr:  true,
		},
		{
			name:     "an HTTP error",
			response: fakehttp.Response{Status: http.StatusBadGateway},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodPost, "/api/auth.test", tt.response)
			api.ExpectAllCalled()

			got, err := AuthTest(t.Context(), api.URL()+"/api/", api.Client(), testToken)

			require.Equal(t, []string{"Bearer " + testToken}, api.Requests()[0].Header.Values("Authorization"))

			code, isSlack := ErrorCode(err)
			require.Equal(t, tt.wantCode, code)
			require.Equal(t, tt.wantCode != "", isSlack)

			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestErrorCodeOfOtherErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{name: "nil", err: nil},
		{name: "not from Slack", err: errors.New("connection refused")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			code, ok := ErrorCode(tt.err)
			require.False(t, ok)
			require.Empty(t, code)
		})
	}
}
