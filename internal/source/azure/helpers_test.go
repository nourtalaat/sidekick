package azure

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// Test IDs.
const (
	testSub         = "11111111-2222-3333-4444-555555555555"
	otherSub        = "99999999-8888-7777-6666-555555555555"
	testWorkspaceID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	testWorkspace   = "/subscriptions/" + testSub + "/resourceGroups/shop/providers/Microsoft.OperationalInsights/workspaces/prod-logs"
	testSite        = "/subscriptions/" + testSub + "/resourceGroups/shop/providers/Microsoft.Web/sites/checkout"
)

// armPlaceholder stands for the fake ARM root in canned answers, such as
// in next page links.
const armPlaceholder = "{arm}"

// testNow is the fixed time of the test clocks.
var testNow = time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)

// jsonHeaders are the headers of a JSON answer.
var jsonHeaders = map[string]string{"Content-Type": "application/json; charset=utf-8"}

// route is an API path and the answers it gives, in order.
type route struct {
	method    string
	path      string
	responses []fakehttp.Response
}

// fakeCredential gives a token per scope and counts the requests.
type fakeCredential struct {
	err error

	mu    sync.Mutex
	calls int
}

// GetToken returns "token-for-<scope>", valid for an hour from testNow.
func (f *fakeCredential) GetToken(_ context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++

	if f.err != nil {
		return azcore.AccessToken{}, f.err
	}

	return azcore.AccessToken{Token: "token-for-" + opts.Scopes[0], ExpiresOn: testNow.Add(time.Hour)}, nil
}

// newTestSource returns a source that reads testSub and the test
// workspace, with every API on api and a fixed clock.
func newTestSource(t *testing.T, api *fakehttp.Server, subscriptions ...string) *Source {
	t.Helper()

	if len(subscriptions) == 0 {
		subscriptions = []string{testSub}
	}

	tokens := newTokens(&fakeCredential{})
	tokens.now = func() time.Time { return testNow }

	return &Source{
		cfg: Config{
			Subscriptions: subscriptions,
			Workspaces:    []Workspace{{ID: testWorkspaceID, ResourceID: testWorkspace}},
		},
		client: &client{http: api.Client(), tokens: tokens, userAgent: "sidekick/test"},
		apis:   endpoints{arm: api.URL() + "/arm", logAnalytics: api.URL() + "/la/v1"},
		now:    func() time.Time { return testNow },
	}
}

// body returns a JSON answer.
func body(s string) fakehttp.Response {
	return fakehttp.Response{Headers: jsonHeaders, Body: []byte(s)}
}

// failed returns an Azure error answer.
func failed(code int, errorCode, message string) fakehttp.Response {
	return fakehttp.Response{
		Status:  code,
		Headers: jsonHeaders,
		Body:    fmt.Appendf(nil, `{"error":{"code":%q,"message":%q}}`, errorCode, message),
	}
}

// withARM returns r with armPlaceholder replaced by the fake ARM root.
func withARM(r fakehttp.Response, root string) fakehttp.Response {
	r.Body = bytes.ReplaceAll(r.Body, []byte(armPlaceholder), []byte(root))

	return r
}
