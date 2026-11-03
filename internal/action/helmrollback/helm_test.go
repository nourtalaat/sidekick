package helmrollback

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"

	"github.com/nourtalaat/sidekick/internal/action/kubeaccess"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

const (
	podNamespace       = "sidekick"
	account            = "sidekick-helm-rollback"
	ownToken           = "own-token-secret-value"
	mintedToken        = "minted-token-1"
	tokenPath          = "/api/v1/namespaces/" + podNamespace + "/serviceaccounts/" + account + "/token"
	releaseRecordsPath = "/api/v1/namespaces/shop/secrets"
)

// newAPI starts a fake Kubernetes API server that gives the token of the
// account to Sidekick's own account, and answers the version call.
func newAPI(t *testing.T) *fakehttp.Server {
	t.Helper()

	api := fakehttp.New(t)
	api.HandleFunc(http.MethodPost, tokenPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Header.Get("Authorization") != "Bearer "+ownToken {
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"apiVersion": "authentication.k8s.io/v1",
			"kind":       "TokenRequest",
			"status":     map[string]any{"token": mintedToken, "expirationTimestamp": "2099-01-01T00:00:00Z"},
		})
	})
	api.HandleFunc(http.MethodGet, "/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		_ = json.NewEncoder(w).Encode(map[string]string{"major": "1", "minor": "37", "gitVersion": "v1.37.1"})
	})

	return api
}

// openerFor returns an opener for the fake API server.
func openerFor(api *fakehttp.Server, driverName string) opener {
	cluster := kubeaccess.NewCluster(&rest.Config{Host: api.URL(), BearerToken: ownToken}, account, podNamespace)

	return clusterOpener(cluster, driverName, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestOpenerActsWithTheTokenOfTheAccount(t *testing.T) {
	t.Parallel()

	api := newAPI(t)

	sess, err := openerFor(api, "secret")(t.Context(), "shop")
	require.NoError(t, err)
	require.Equal(t, []string{ownToken, mintedToken}, sess.secrets)
	require.NoError(t, sess.cfg.KubeClient.IsReachable())

	requests := api.Requests()
	require.Len(t, requests, 2)
	require.Equal(t, "Bearer "+ownToken, requests[0].Header.Get("Authorization"), "Sidekick's own account asks for the token")
	require.Equal(t, "/version", requests[1].Path)
	require.Equal(t, "Bearer "+mintedToken, requests[1].Header.Get("Authorization"), "Helm acts with the token of the account")
}

func TestOpenerFailsWithoutTheOwnAccount(t *testing.T) {
	t.Parallel()

	api := newAPI(t)
	cluster := kubeaccess.NewCluster(&rest.Config{Host: api.URL(), BearerToken: "another"}, account, podNamespace)

	_, err := clusterOpener(cluster, "secret", nil)(t.Context(), "shop")
	require.Error(t, err)
	require.True(t, apierrors.IsUnauthorized(err))
	require.NotContains(t, err.Error(), "another")
}

func TestOpenerRefusesAnUnknownDriver(t *testing.T) {
	t.Parallel()

	_, err := openerFor(newAPI(t), "nope")(t.Context(), "shop")
	require.ErrorContains(t, err, `unknown driver "nope"`)
	require.NotContains(t, err.Error(), mintedToken)
}

func TestRestGetter(t *testing.T) {
	t.Parallel()

	getter := &restGetter{config: &rest.Config{Host: "https://k.example.com", BearerToken: mintedToken}, namespace: "shop"}

	config, err := getter.ToRESTConfig()
	require.NoError(t, err)
	require.Equal(t, mintedToken, config.BearerToken)

	config.BearerToken = "changed"
	require.Equal(t, mintedToken, getter.config.BearerToken, "the caller gets a copy")

	namespace, _, err := getter.ToRawKubeConfigLoader().Namespace()
	require.NoError(t, err)
	require.Equal(t, "shop", namespace, "Helm reads the namespace of the call")

	loaded, err := getter.ToRawKubeConfigLoader().ClientConfig()
	require.NoError(t, err)
	require.Equal(t, "https://k.example.com", loaded.Host)
	require.Equal(t, mintedToken, loaded.BearerToken)
}

func TestHistoryOfAReleaseWithNoRecords(t *testing.T) {
	t.Parallel()

	api := newAPI(t)
	api.HandleFunc(http.MethodGet, releaseRecordsPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "SecretList", "items": []any{}})
	})

	sess, err := openerFor(api, "secret")(t.Context(), "shop")
	require.NoError(t, err)

	revs, err := sess.history("web")
	require.NoError(t, err)
	require.Empty(t, revs)

	requests := api.Requests()
	list := requests[len(requests)-1]
	require.Equal(t, releaseRecordsPath, list.Path)
	require.Equal(t, "name=web,owner=helm", list.Query.Get("labelSelector"))
	require.Equal(t, "Bearer "+mintedToken, list.Header.Get("Authorization"))
}

func TestHistoryReadsTheConfigMapsOfThatDriver(t *testing.T) {
	t.Parallel()

	api := newAPI(t)
	api.HandleFunc(http.MethodGet, "/api/v1/namespaces/shop/configmaps", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "ConfigMapList", "items": []any{}})
	})

	sess, err := openerFor(api, "configmap")(t.Context(), "shop")
	require.NoError(t, err)

	revs, err := sess.history("web")
	require.NoError(t, err)
	require.Empty(t, revs)
}

func TestHistoryErrorsHoldNoTokenAndSayThatTheAccountIsRefused(t *testing.T) {
	t.Parallel()

	api := newAPI(t)
	api.HandleFunc(http.MethodGet, releaseRecordsPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)

		_ = json.NewEncoder(w).Encode(map[string]any{
			"apiVersion": "v1", "kind": "Status", "status": "Failure", "reason": "Forbidden", "code": http.StatusForbidden,
			"message": "secrets is forbidden for " + r.Header.Get("Authorization"),
		})
	})

	sess, err := openerFor(api, "secret")(t.Context(), "shop")
	require.NoError(t, err)

	_, err = sess.history("web")
	require.Error(t, err)
	require.True(t, apierrors.IsForbidden(err))
	require.NotContains(t, err.Error(), mintedToken)
	require.Contains(t, err.Error(), "[redacted]")
}
