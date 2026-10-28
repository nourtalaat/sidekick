package api_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/api"
	"github.com/nourtalaat/sidekick/internal/auth"
	"github.com/nourtalaat/sidekick/internal/roles"
)

var testUserID = uuid.MustParse("01920000-0000-7000-8000-000000000001")

func TestHandler(t *testing.T) {
	t.Parallel()

	user := auth.Principal{Kind: auth.KindUser, ID: testUserID, Name: "Ana", Email: "ana@example.com", Role: roles.Viewer}
	token := auth.Principal{Kind: auth.KindToken, ID: testUserID, Name: "ci bench", Role: roles.Admin}

	tests := []struct {
		name       string
		method     string
		path       string
		principal  auth.Principal
		authErr    error
		wantStatus int
		wantType   string
		wantBody   string
		wantAllow  string
	}{
		{
			name: "a signed-in viewer reads /me", method: http.MethodGet, path: "/api/v1/me", principal: user,
			wantStatus: http.StatusOK, wantType: "application/json",
			wantBody: `{"kind":"user","id":"01920000-0000-7000-8000-000000000001","name":"Ana","email":"ana@example.com","role":"viewer"}`,
		},
		{
			name: "an API token reads /me", method: http.MethodGet, path: "/api/v1/me", principal: token,
			wantStatus: http.StatusOK, wantType: "application/json",
			wantBody: `{"kind":"token","id":"01920000-0000-7000-8000-000000000001","name":"ci bench","role":"admin"}`,
		},
		{
			name: "no session", method: http.MethodGet, path: "/api/v1/me", authErr: fmt.Errorf("read session: %w", auth.ErrUnauthenticated),
			wantStatus: http.StatusUnauthorized, wantType: "application/problem+json",
			wantBody: problem(http.StatusUnauthorized, "Sign in, or send an API token.", "/api/v1/me"),
		},
		{
			name: "no CSRF token", method: http.MethodPost, path: "/api/v1/me", authErr: auth.ErrCSRF,
			wantStatus: http.StatusForbidden, wantType: "application/problem+json",
			wantBody: problem(http.StatusForbidden, "Send the session's CSRF token in the X-CSRF-Token header.", "/api/v1/me"),
		},
		{
			name: "the authenticator fails", method: http.MethodGet, path: "/api/v1/me", authErr: errors.New("database down"),
			wantStatus: http.StatusInternalServerError, wantType: "application/problem+json",
			wantBody: problem(http.StatusInternalServerError, "Something went wrong on the server. The logs have the details.", "/api/v1/me"),
		},
		{
			name: "an unknown path", method: http.MethodGet, path: "/api/v1/nothing", principal: user,
			wantStatus: http.StatusNotFound, wantType: "application/problem+json",
			wantBody: problem(http.StatusNotFound, "No API operation has this path.", "/api/v1/nothing"),
		},
		{
			name: "a method the path does not take", method: http.MethodDelete, path: "/api/v1/me", principal: user,
			wantStatus: http.StatusMethodNotAllowed, wantType: "application/problem+json", wantAllow: "GET, HEAD",
			wantBody: problem(http.StatusMethodNotAllowed, "This path does not take this method.", "/api/v1/me"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			authn := api.NewMockAuthenticator(gomock.NewController(t))
			authn.EXPECT().Authenticate(gomock.Any()).Return(tt.principal, tt.authErr)

			h, err := api.New(api.Options{Auth: authn})
			require.NoError(t, err)

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, nil))

			require.Equal(t, tt.wantStatus, rec.Code)
			require.Equal(t, tt.wantType, rec.Header().Get("Content-Type"))
			require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			require.Equal(t, tt.wantAllow, rec.Header().Get("Allow"))
			require.JSONEq(t, tt.wantBody, rec.Body.String())
		})
	}
}

func TestNewWithoutAuthenticator(t *testing.T) {
	t.Parallel()

	_, err := api.New(api.Options{})
	require.Error(t, err)
}

// problem returns the problem details body the API answers with.
func problem(status int, detail, path string) string {
	return fmt.Sprintf(`{"type":"about:blank","title":%q,"status":%d,"detail":%q,"instance":%q}`,
		http.StatusText(status), status, detail, path)
}
