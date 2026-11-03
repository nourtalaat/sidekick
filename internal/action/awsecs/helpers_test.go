package awsecs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/awsaction"
	"github.com/nourtalaat/sidekick/internal/action/awsaction/awsactiontest"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// Test account, Region, role and service.
const (
	testAccount = "111122223333"
	testRegion  = "us-east-1"
	testRole    = "arn:aws:iam::" + testAccount + ":role/sidekick-ecs-rollback"
	testCluster = "shop"
	testService = "checkout"
)

// Prefixes of the ECS API: every call is a POST to "/" whose header
// X-Amz-Target names the operation.
const (
	targetHeader = "X-Amz-Target"
	targetPrefix = "AmazonEC2ContainerServiceV20141113."
	jsonType     = "application/x-amz-json-1.1"
)

// registeredAt is the epoch second the fake registered every task definition
// at: 2026-05-04T09:30:00Z.
const registeredAt = 1777887000

// The parameters of the rollbacks the tests propose.
const (
	paramsBack1 = `{"cluster":"shop","service":"checkout"}`
	paramsBack2 = `{"cluster":"shop","service":"checkout","revisionsBack":2}`
)

// fakeTaskDefinition is a task definition the fake ECS knows.
type fakeTaskDefinition struct {
	family   string
	revision int
	status   string
	images   []string
	// hidden makes DescribeTaskDefinition fail, as it does for an INACTIVE
	// task definition that nothing uses.
	hidden bool
	// noRegistrant leaves out who registered it.
	noRegistrant bool
}

// fakeService is the service the fake ECS has.
type fakeService struct {
	status string
	// controller is the deployment controller type; empty leaves the field
	// out, as ECS does for the ECS controller.
	controller string
	// primary is the family:revision the primary deployment runs, and
	// deployment its ID. Empty primary leaves out the deployment.
	primary    string
	deployment int
}

// fakeECS is a fake ECS endpoint with one cluster and one service. It
// applies UpdateService, so a second read sees the change.
type fakeECS struct {
	t *testing.T

	mu          sync.Mutex
	noCluster   bool
	noService   bool
	service     fakeService
	definitions []fakeTaskDefinition
	// ops are the operations called, in order.
	ops []string
	// updates are the bodies of the UpdateService calls.
	updates []json.RawMessage
	// lists are the bodies of the ListTaskDefinitions calls.
	lists []json.RawMessage
	// fail makes an operation answer with the response instead.
	fail map[string]fakehttp.Response
	// describeStatus gives DescribeTaskDefinition another status for a
	// family:revision than ListTaskDefinitions saw.
	describeStatus map[string]string
}

// newFakeECS returns a fake ECS and the fake STS that goes with it. The
// service runs checkout:42 in deployment ecs-svc/100, and the family has
// ACTIVE revisions 41, 40, 38 and 37, an INACTIVE 39, and a newer ACTIVE 43. Another family,
// checkout-worker, shares the prefix.
func newFakeECS(t *testing.T) (*fakeECS, *fakehttp.Server) {
	t.Helper()

	api := fakehttp.New(t)
	f := &fakeECS{
		t:       t,
		service: fakeService{status: "ACTIVE", primary: "checkout:42", deployment: 100},
		fail:    map[string]fakehttp.Response{},

		describeStatus: map[string]string{},
	}

	for _, rev := range []int{43, 42, 41, 40, 38, 37, 36} {
		f.definitions = append(f.definitions, fakeTaskDefinition{family: "checkout", revision: rev, status: "ACTIVE", images: []string{imageFor(rev)}})
	}

	f.definitions = append(f.definitions,
		fakeTaskDefinition{family: "checkout", revision: 39, status: "INACTIVE", images: []string{imageFor(39)}, hidden: true},
		fakeTaskDefinition{family: "checkout-worker", revision: 7, status: "ACTIVE", images: []string{"registry.example.com/worker:7"}},
		fakeTaskDefinition{family: "checkout-worker", revision: 6, status: "ACTIVE", images: []string{"registry.example.com/worker:6"}},
	)

	api.HandleFunc(http.MethodPost, "/", f.serve)

	return f, api
}

// imageFor is the image of a revision of checkout.
func imageFor(revision int) string {
	return "111122223333.dkr.ecr.us-east-1.amazonaws.com/checkout:1.4." + strconv.Itoa(revision)
}

// arnOf is the ARN of a task definition.
func arnOf(family string, revision int) string {
	return fmt.Sprintf("arn:aws:ecs:%s:%s:task-definition/%s:%d", testRegion, testAccount, family, revision)
}

// serve answers one ECS call.
func (f *fakeECS) serve(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	require.NoError(f.t, err)

	op := strings.TrimPrefix(req.Header.Get(targetHeader), targetPrefix)

	f.mu.Lock()
	defer f.mu.Unlock()

	f.ops = append(f.ops, op)

	if resp, ok := f.fail[op]; ok {
		w.Header().Set("Content-Type", jsonType)

		for k, v := range resp.Headers {
			w.Header().Set(k, v)
		}

		w.WriteHeader(resp.Status)
		_, _ = w.Write(resp.Body)

		return
	}

	var out any

	switch op {
	case "DescribeServices":
		out = f.describeServices(w)
	case "ListTaskDefinitions":
		f.lists = append(f.lists, body)
		out = f.listTaskDefinitions(body)
	case "DescribeTaskDefinition":
		out = f.describeTaskDefinition(w, body)
	case "UpdateService":
		f.updates = append(f.updates, body)
		out = f.updateService(body)
	default:
		f.t.Errorf("unexpected ECS operation %q", op)

		w.WriteHeader(http.StatusNotImplemented)

		return
	}

	if out == nil {
		return
	}

	w.Header().Set("Content-Type", jsonType)
	require.NoError(f.t, json.NewEncoder(w).Encode(out))
}

// apiError answers with an error of ECS.
func apiError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", jsonType)
	w.Header().Set("X-Amzn-Errortype", code)
	w.WriteHeader(status)

	_, _ = fmt.Fprintf(w, `{"__type":%q,"message":%q}`, code, message)
}

// describeServices answers DescribeServices, or writes the error itself and
// returns nil.
func (f *fakeECS) describeServices(w http.ResponseWriter) any {
	if f.noCluster {
		apiError(w, http.StatusBadRequest, "ClusterNotFoundException", "Cluster not found.")

		return nil
	}

	if f.noService {
		return map[string]any{"services": []any{}, "failures": []any{map[string]any{"arn": "arn:aws:ecs:us-east-1:111122223333:service/shop/checkout", "reason": "MISSING"}}}
	}

	return map[string]any{"services": []any{f.serviceJSON()}, "failures": []any{}}
}

// serviceJSON is the service as ECS describes it.
func (f *fakeECS) serviceJSON() map[string]any {
	svc := map[string]any{"serviceName": testService, "status": f.service.status, "deployments": []any{}}

	if f.service.controller != "" {
		svc["deploymentController"] = map[string]any{"type": f.service.controller}
	}

	if f.service.primary != "" {
		family, revision, _ := strings.Cut(f.service.primary, ":")
		rev, _ := strconv.Atoi(revision)
		arn := arnOf(family, rev)

		svc["taskDefinition"] = arn
		svc["deployments"] = []any{map[string]any{"id": "ecs-svc/" + strconv.Itoa(f.service.deployment), "status": "PRIMARY", "taskDefinition": arn}}
	}

	return svc
}

// listTaskDefinitions answers ListTaskDefinitions as ECS does: ARNs of the
// definitions of the right status whose family starts with the prefix, in
// descending order of family and revision, in pages.
func (f *fakeECS) listTaskDefinitions(body []byte) any {
	var in struct {
		FamilyPrefix string `json:"familyPrefix"`
		Status       string `json:"status"`
		Sort         string `json:"sort"`
		MaxResults   int    `json:"maxResults"`
		NextToken    string `json:"nextToken"`
	}

	require.NoError(f.t, json.Unmarshal(body, &in))

	status := cmpDefault(in.Status, "ACTIVE")

	var matching []fakeTaskDefinition

	for _, d := range f.definitions {
		if strings.HasPrefix(d.family, in.FamilyPrefix) && d.status == status {
			matching = append(matching, d)
		}
	}

	sort.Slice(matching, func(i, j int) bool {
		if matching[i].family != matching[j].family {
			return (matching[i].family > matching[j].family) == (in.Sort == "DESC")
		}

		return (matching[i].revision > matching[j].revision) == (in.Sort == "DESC")
	})

	start := 0
	if in.NextToken != "" {
		var err error

		start, err = strconv.Atoi(in.NextToken)
		require.NoError(f.t, err)
	}

	end := min(start+in.MaxResults, len(matching))
	arns := []string{}

	for _, d := range matching[start:end] {
		arns = append(arns, arnOf(d.family, d.revision))
	}

	out := map[string]any{"taskDefinitionArns": arns}
	if end < len(matching) {
		out["nextToken"] = strconv.Itoa(end)
	}

	return out
}

// describeTaskDefinition answers DescribeTaskDefinition, or writes the error
// itself and returns nil.
func (f *fakeECS) describeTaskDefinition(w http.ResponseWriter, body []byte) any {
	var in struct {
		TaskDefinition string `json:"taskDefinition"`
	}

	require.NoError(f.t, json.Unmarshal(body, &in))

	for _, d := range f.definitions {
		if fmt.Sprintf("%s:%d", d.family, d.revision) != in.TaskDefinition || d.hidden {
			continue
		}

		containers := []any{}
		for i, image := range d.images {
			containers = append(containers, map[string]any{"name": "c" + strconv.Itoa(i), "image": image})
		}

		status := cmpDefault(f.describeStatus[in.TaskDefinition], d.status)

		td := map[string]any{
			"taskDefinitionArn": arnOf(d.family, d.revision), "family": d.family, "revision": d.revision, "status": status,
			"registeredAt": registeredAt, "containerDefinitions": containers,
		}
		if !d.noRegistrant {
			td["registeredBy"] = "arn:aws:iam::111122223333:user/ana"
		}

		return map[string]any{"taskDefinition": td}
	}

	apiError(w, http.StatusBadRequest, "ClientException", "Unable to describe task definition.")

	return nil
}

// updateService applies UpdateService: the service gets a new primary
// deployment on the task definition.
func (f *fakeECS) updateService(body []byte) any {
	var in struct {
		TaskDefinition string `json:"taskDefinition"`
	}

	require.NoError(f.t, json.Unmarshal(body, &in))

	f.service.primary = in.TaskDefinition
	f.service.deployment++

	return map[string]any{"service": f.serviceJSON()}
}

// cmpDefault returns v, or def when v is empty.
func cmpDefault(v, def string) string {
	if v == "" {
		return def
	}

	return v
}

// setPrimary sets the task definition and deployment of the primary
// deployment, as a deploy by someone else would.
func (f *fakeECS) setPrimary(taskDefinition string, deployment int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.service.primary, f.service.deployment = taskDefinition, deployment
}

// count returns how many times an operation was called.
func (f *fakeECS) count(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(slices.DeleteFunc(slices.Clone(f.ops), func(o string) bool { return o != op }))
}

// updateBodies returns the bodies of the UpdateService calls.
func (f *fakeECS) updateBodies() []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.updates)
}

// failWith makes an operation answer with an error of ECS.
func (f *fakeECS) failWith(op string, status int, code string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.fail[op] = fakehttp.Response{
		Status:  status,
		Headers: map[string]string{"X-Amzn-Errortype": code},
		Body:    fmt.Appendf(nil, `{"__type":%q,"message":"no"}`, code),
	}
}

// testConfig is the config the tests use: one account with a role, one
// Region, the cluster shop and the services checkout and cart-*.
func testConfig() Config {
	return Config{
		Config: awsaction.Config{Accounts: []awsaction.Account{{ID: testAccount, RoleARN: testRole}}, Regions: []string{testRegion}},
		Limits: Limits{Clusters: []string{testCluster}, Services: []string{testService, "cart-*"}},
	}
}

// newTestAction returns an action on a fake ECS and a fake STS. edit, when
// set, changes the config first.
func newTestAction(t *testing.T, edit func(*Config)) (*Rollback, *fakeECS) {
	t.Helper()

	f, api := newFakeECS(t)

	sts := fakehttp.New(t)
	sts.HandleFunc(http.MethodPost, "/", func(w http.ResponseWriter, _ *http.Request) {
		resp := awsactiontest.AssumedRole(testAccount)

		w.Header().Set("Content-Type", resp.Headers["Content-Type"])
		_, _ = w.Write(resp.Body)
	})

	cfg := testConfig()
	if edit != nil {
		edit(&cfg)
	}

	r, err := build(t.Context(), "ecs-rollback", cfg, api.Client(), buildOptions{
		aws:      []awsaction.Option{awsaction.WithLoader(retrying(awsactiontest.Loader)), awsaction.WithSTSEndpoint(sts.URL())},
		endpoint: api.URL(),
	})
	require.NoError(t, err)

	return r, f
}

// retrying makes a loader give the SDK's standard retryer, with three
// attempts and no wait, so a test can see which calls the SDK repeats.
func retrying(load func(context.Context, ...func(*config.LoadOptions) error) (aws.Config, error)) func(context.Context, ...func(*config.LoadOptions) error) (aws.Config, error) {
	return func(ctx context.Context, optFns ...func(*config.LoadOptions) error) (aws.Config, error) {
		cfg, err := load(ctx, optFns...)
		if err != nil {
			return aws.Config{}, fmt.Errorf("load: %w", err)
		}

		cfg.Retryer = func() aws.Retryer {
			return retry.NewStandard(func(o *retry.StandardOptions) {
				o.MaxAttempts = 3
				o.Backoff = retry.BackoffDelayerFunc(func(int, error) (time.Duration, error) { return 0, nil })
			})
		}

		return cfg, nil
	}
}

// invocation returns the Invocation of a proposal that has the snapshot.
func invocation(params string, snap action.Snapshot) action.Invocation {
	return action.Invocation{ProposalID: "p1", Params: json.RawMessage(params), IdempotencyKey: "key-1", ApprovedBy: "u1", Snapshot: &snap}
}
