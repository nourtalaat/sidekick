package azurecontainerapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v5"

	"github.com/nourtalaat/sidekick/internal/action"
)

// pollInterval is how long the action waits between two looks at a running
// update, when Azure gives no Retry-After.
const pollInterval = 5 * time.Second

// maxRevisions is the most revisions of one app that the action reads.
const maxRevisions = 1000

// Statuses of an answer that repeating the call cannot fix.
var permanentStatuses = []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}

// errInProgress is returned while Azure is still changing the app, so the
// job tries again later.
var errInProgress = errors.New("the container app is still being updated in Azure; trying again later")

// statusError is an answer of Azure that is not a success, without the body
// that the SDK's own error text holds.
type statusError struct {
	operation string
	status    int
	// code is Azure's own error code, such as AuthorizationFailed. It may
	// be empty.
	code string
}

// Error returns the operation, the status and Azure's error code.
func (e *statusError) Error() string {
	text := fmt.Sprintf("%s: Azure answered %d", e.operation, e.status)
	if e.code != "" {
		text += " " + e.code
	}

	return text
}

// arms holds the clients of Azure Resource Manager, one pair for each
// subscription, made when first needed.
type arms struct {
	cred azcore.TokenCredential
	opts *arm.ClientOptions
	// pollEvery is how long to wait between two looks at a running update.
	pollEvery time.Duration

	mu    sync.Mutex
	pairs map[string]*pair
}

// pair is the clients the action calls for one subscription.
type pair struct {
	apps      *armappcontainers.ContainerAppsClient
	revisions *armappcontainers.ContainerAppsRevisionsClient
}

// scope names one container app.
type scope struct {
	subscription  string
	resourceGroup string
	app           string
}

// read returns the container app and all its revisions.
func (a *arms) read(ctx context.Context, s scope) (live, error) {
	p, err := a.pair(s.subscription)
	if err != nil {
		return live{}, fmt.Errorf("get the clients: %w", err)
	}

	app, err := p.apps.Get(ctx, s.resourceGroup, s.app, nil)
	if err != nil {
		return live{}, asStatusError("get the app", err)
	}

	l := live{app: app.ContainerApp}

	pager := p.revisions.NewListRevisionsPager(s.resourceGroup, s.app, nil)

	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return live{}, asStatusError("list the revisions", err)
		}

		l.revisions = append(l.revisions, page.Value...)

		if len(l.revisions) > maxRevisions {
			return live{}, refuse("container app %s has more than %d revisions, which is more than this action reads.", s.app, maxRevisions)
		}
	}

	return l, nil
}

// setTraffic replaces the traffic list of the app with list and waits for
// Azure to finish. The body is a JSON merge patch that holds the location
// the app has and the traffic list only, so every other setting stays.
func (a *arms) setTraffic(ctx context.Context, s scope, location *string, list []*armappcontainers.TrafficWeight) error {
	p, err := a.pair(s.subscription)
	if err != nil {
		return fmt.Errorf("get the clients: %w", err)
	}

	body := armappcontainers.ContainerApp{
		Location: location,
		Properties: &armappcontainers.ContainerAppProperties{
			Configuration: &armappcontainers.Configuration{Ingress: &armappcontainers.Ingress{Traffic: list}},
		},
	}

	poller, err := p.apps.BeginUpdate(ctx, s.resourceGroup, s.app, body, nil)
	if err != nil {
		return asStatusError("change the traffic", err)
	}

	if _, err := poller.PollUntilDone(ctx, &runtime.PollUntilDoneOptions{Frequency: a.pollEvery}); err != nil {
		return asStatusError("wait for the traffic change", err)
	}

	return nil
}

// pair returns the clients for a subscription.
func (a *arms) pair(subscription string) (*pair, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if p, ok := a.pairs[subscription]; ok {
		return p, nil
	}

	apps, err := armappcontainers.NewContainerAppsClient(subscription, a.cred, a.opts)
	if err != nil {
		return nil, fmt.Errorf("make the container apps client: %w", err)
	}

	revisions, err := armappcontainers.NewContainerAppsRevisionsClient(subscription, a.cred, a.opts)
	if err != nil {
		return nil, fmt.Errorf("make the revisions client: %w", err)
	}

	if a.pairs == nil {
		a.pairs = map[string]*pair{}
	}

	p := &pair{apps: apps, revisions: revisions}
	a.pairs[subscription] = p

	return p, nil
}

// asStatusError turns the error of the SDK into a *statusError when Azure
// answered, and wraps it otherwise.
func asStatusError(operation string, err error) error {
	if respErr, ok := errors.AsType[*azcore.ResponseError](err); ok {
		return &statusError{operation: operation, status: respErr.StatusCode, code: respErr.ErrorCode}
	}

	return fmt.Errorf("%s: %w", operation, err)
}

// statusOf returns the status of an answer of Azure, or 0.
func statusOf(err error) int {
	if se, ok := errors.AsType[*statusError](err); ok {
		return se.status
	}

	return 0
}

// fail wraps err, an error of read or setTraffic, with the operation that
// failed. An answer that repeating cannot fix, such as a refused
// permission, also wraps action.ErrPermanent. Any other error is left
// without it, so the job tries again.
func fail(operation string, err error) error {
	if slices.Contains(permanentStatuses, statusOf(err)) {
		return fmt.Errorf("%s: %w: %w", operation, action.ErrPermanent, err)
	}

	return fmt.Errorf("%s: %w", operation, err)
}
