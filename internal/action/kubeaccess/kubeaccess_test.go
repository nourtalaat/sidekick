package kubeaccess

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

const (
	podNamespace = "sidekick"
	account      = "sidekick-actions"
	ownToken     = "own-token-secret-value"
	mintedToken  = "minted-token-1"
	tokenPath    = "/api/v1/namespaces/" + podNamespace + "/serviceaccounts/" + account + "/token"
)

// newTokenServer starts a fake API server that answers TokenRequests for
// the account. A request from anyone but the holder of ownToken gets 401.
// status, when not zero, is the status of every request, and echo is put in
// the message as a misbehaving proxy could echo a header.
func newTokenServer(t *testing.T, status int, echo string) *fakehttp.Server {
	t.Helper()

	api := fakehttp.New(t)
	api.HandleFunc(http.MethodPost, tokenPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Header.Get("Authorization") != "Bearer "+ownToken {
			writeStatus(w, http.StatusUnauthorized, "Unauthorized", "Unauthorized")

			return
		}

		if status != 0 {
			writeStatus(w, status, "Forbidden", "serviceaccounts is forbidden for the header Authorization: Bearer "+echo)

			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"apiVersion": "authentication.k8s.io/v1",
			"kind":       "TokenRequest",
			"status":     map[string]any{"token": mintedToken, "expirationTimestamp": "2099-01-01T00:00:00Z"},
		})
	})

	return api
}

func writeStatus(w http.ResponseWriter, code int, reason, message string) {
	w.WriteHeader(code)

	_ = json.NewEncoder(w).Encode(map[string]any{
		"apiVersion": "v1", "kind": "Status", "status": "Failure",
		"reason": reason, "message": message, "code": code,
	})
}

func testCluster(api *fakehttp.Server, token string) *Cluster {
	return NewCluster(&rest.Config{Host: api.URL(), BearerToken: token}, account, podNamespace)
}

func TestOpenAsksForATokenAndReturnsTheAccess(t *testing.T) {
	t.Parallel()

	api := newTokenServer(t, 0, "")

	access, err := testCluster(api, ownToken).Open(t.Context())
	require.NoError(t, err)
	require.Equal(t, mintedToken, access.Config.BearerToken)
	require.Equal(t, api.URL(), access.Config.Host)
	require.Equal(t, "application/json", access.Config.ContentType)
	require.Equal(t, []string{ownToken, mintedToken}, access.Secrets)

	requests := api.Requests()
	require.Len(t, requests, 1)
	require.Equal(t, "Bearer "+ownToken, requests[0].Header.Get("Authorization"), "Sidekick's own account asks for the token")
	require.Contains(t, string(requests[0].Body), `"expirationSeconds":600`)
}

func TestOpenErrorsHoldNoToken(t *testing.T) {
	t.Parallel()

	api := newTokenServer(t, http.StatusForbidden, ownToken)

	_, err := testCluster(api, ownToken).Open(t.Context())
	require.Error(t, err)
	require.NotContains(t, err.Error(), ownToken, "the server echoed the token, and the error cuts it out")
	require.Contains(t, err.Error(), redacted)
	require.True(t, apierrors.IsForbidden(err), "the error still says that the API server refused it")
}

func TestOpenWithAWrongOwnToken(t *testing.T) {
	t.Parallel()

	api := newTokenServer(t, 0, "")

	_, err := testCluster(api, "another").Open(t.Context())
	require.Error(t, err)
	require.True(t, apierrors.IsUnauthorized(err))
	require.NotContains(t, err.Error(), "another")
}

func TestRedact(t *testing.T) {
	t.Parallel()

	cause := errors.New("failed with token abc and token def")

	got := Redact(cause, []string{"abc", "def", ""})
	require.EqualError(t, got, "failed with token [redacted] and token [redacted]")
	require.ErrorIs(t, got, cause)

	require.EqualError(t, Redact(cause, []string{"zzz"}), cause.Error(), "an error with no secret reads the same")
}
