package prometheusrule

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/kubeaccess"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// testCluster returns the cluster for the fake API server.
func testCluster(api *fakehttp.Server) *kubeaccess.Cluster {
	return kubeaccess.NewCluster(restConfig(api), DefaultServiceAccount, podNamespace)
}

func TestSessionAsksForATokenAndUsesIt(t *testing.T) {
	t.Parallel()

	api, fake := newFakeAPI(t)

	sess, err := openSession(context.Background(), testCluster(api))
	require.NoError(t, err)

	res, err := sess.get(context.Background(), testNamespace, testName)
	require.NoError(t, err)
	require.Equal(t, "100", res.version)

	view, missing, err := res.groups.view([]ruleRef{{group: "checkout.rules", name: "AlwaysOn"}})
	require.NoError(t, err)
	require.Empty(t, missing)
	require.JSONEq(t, `{"checkout.rules": {"AlwaysOn": {"expr": "1"}}}`, string(view))

	requests := api.Requests()
	require.Len(t, requests, 2)
	require.Equal(t, "Bearer "+ownToken, requests[0].Header.Get("Authorization"), "Sidekick's own account asks for the token")
	require.Contains(t, string(requests[0].Body), `"expirationSeconds":600`)
	require.Equal(t, "Bearer minted-token-1", requests[1].Header.Get("Authorization"), "the rules are read with the token of the actions' account")
	require.Equal(t, rulesPath, requests[1].Path)
	require.Equal(t, 1, fake.mintedCount())
}

func TestPatch(t *testing.T) {
	t.Parallel()

	api, fake := newFakeAPI(t)
	ctx := context.Background()
	ref := []ruleRef{{group: "checkout.rules", name: "HighLatency"}}

	sess, err := openSession(ctx, testCluster(api))
	require.NoError(t, err)

	res, err := sess.get(ctx, testNamespace, testName)
	require.NoError(t, err)

	changed, err := res.groups.edit(ref, []action.Edit{{Path: action.Path{"checkout.rules", "HighLatency", "for"}, Value: []byte(`"15m"`)}})
	require.NoError(t, err)

	// A dry run changes nothing and answers with the object as it would be stored.
	got, err := sess.patch(ctx, testNamespace, testName, changed, res.version, true)
	require.NoError(t, err)
	require.Equal(t, "100", fake.version())
	require.Equal(t, "100", got.version)

	view, _, err := got.groups.view(ref)
	require.NoError(t, err)
	require.Contains(t, string(view), `"for":"15m"`)

	live, _, err := fake.groups(t).view(ref)
	require.NoError(t, err)
	require.Contains(t, string(live), `"for":"5m"`)

	dry := fake.dryRuns()
	require.Len(t, dry, 1)
	require.Equal(t, "application/merge-patch+json", dry[0].contentType)
	require.Equal(t, "sidekick", dry[0].manager)
	require.Equal(t, map[string]any{"resourceVersion": "100"}, dry[0].body["metadata"])

	// A write moves the version on.
	got, err = sess.patch(ctx, testNamespace, testName, changed, res.version, false)
	require.NoError(t, err)
	require.Equal(t, "101", got.version)
	require.Equal(t, "101", fake.version())
	require.Len(t, fake.writes(), 1)

	live, _, err = fake.groups(t).view(ref)
	require.NoError(t, err)
	require.Contains(t, string(live), `"for":"15m"`)

	// The version of the first read is stale now, and the API server says so.
	_, err = sess.patch(ctx, testNamespace, testName, changed, res.version, false)
	require.Error(t, err)
	require.True(t, isConflict(err))
	require.False(t, isRejected(err))
}

func TestPatchOfAnInvalidRuleIsRejected(t *testing.T) {
	t.Parallel()

	api, _ := newFakeAPI(t)

	sess, err := openSession(context.Background(), testCluster(api))
	require.NoError(t, err)

	res, err := sess.get(context.Background(), testNamespace, testName)
	require.NoError(t, err)

	refs := []ruleRef{{group: "checkout.rules", name: "HighLatency"}}
	changed, err := res.groups.edit(refs, []action.Edit{{Path: action.Path{"checkout.rules", "HighLatency", "for"}, Value: []byte(`"five minutes"`)}})
	require.NoError(t, err)

	_, err = sess.patch(context.Background(), testNamespace, testName, changed, res.version, true)
	require.Error(t, err)
	require.True(t, isRejected(err))
	require.ErrorContains(t, err, "duration pattern")
}

func TestOpenErrorsHoldNoToken(t *testing.T) {
	t.Parallel()

	api, fake := newFakeAPI(t)
	fake.tokenStatus = http.StatusForbidden
	fake.tokenEcho = ownToken

	_, err := openSession(context.Background(), testCluster(api))
	require.Error(t, err)
	require.NotContains(t, err.Error(), ownToken, "the server echoed the token, and the error cuts it out")
	require.Contains(t, err.Error(), "[redacted]")
	require.True(t, isRejected(err), "the error still says that the API server refused it")
}

func TestOpenWithAWrongOwnToken(t *testing.T) {
	t.Parallel()

	api, _ := newFakeAPI(t)

	c := kubeaccess.NewCluster(&rest.Config{Host: api.URL(), BearerToken: "another"}, DefaultServiceAccount, podNamespace)

	_, err := openSession(context.Background(), c)
	require.Error(t, err)
	require.True(t, isRejected(err))
	require.NotContains(t, err.Error(), "another")
}
