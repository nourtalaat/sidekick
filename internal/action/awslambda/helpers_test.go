package awslambda

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
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

// The parameters of the rollbacks the tests propose.
const (
	paramsBack1 = `{"function":"checkout","alias":"live"}`
	paramsBack2 = `{"function":"checkout","alias":"live","revisionsBack":2}`
)

// Paths and types of the Lambda API.
const (
	functionPath = "/2015-03-31/functions/" + testFunction
	aliasPath    = functionPath + "/aliases/" + testAlias
	jsonType     = "application/json"
)

// versionsPerPage is how many versions the fake lists in a page, fewer than
// the SDK asks for, so the search has to read more than one page.
const versionsPerPage = 3

// modified is when the fake published every version.
const modified = "2026-05-04T09:30:00.000+0000"

// fakeVersion is a published version the fake Lambda knows.
type fakeVersion struct {
	number int
	// hidden leaves the version out of GetFunctionConfiguration, as if it was
	// deleted after the list was read.
	hidden bool
	image  bool
}

// fakeLambda is a fake Lambda endpoint with one function and one alias. It
// applies UpdateAlias, so a second read sees the change.
type fakeLambda struct {
	t *testing.T

	mu       sync.Mutex
	noAlias  bool
	version  string
	weights  map[string]float64
	revision int
	versions []fakeVersion
	// ops are the operations called, in order.
	ops []string
	// updates are the bodies of the UpdateAlias calls.
	updates []json.RawMessage
	// keepsSplit makes UpdateAlias leave the split as it was, as if an empty
	// map did not clear it.
	keepsSplit bool
	// changeBeforeUpdate makes the alias change, as another person would
	// change it, in the moment between a read and the next UpdateAlias.
	changeBeforeUpdate bool
	// fail makes an operation answer with the response instead.
	fail map[string]fakehttp.Response
}

// newFakeLambda returns a fake Lambda and the fake STS that goes with it. The
// alias live serves version 42 with no split. The function has published
// versions 36, 37, 38, 40, 41, 42 and 43, listed out of order, and $LATEST.
// Version 39 was deleted.
func newFakeLambda(t *testing.T) (*fakeLambda, *fakehttp.Server) {
	t.Helper()

	api := fakehttp.New(t)
	f := &fakeLambda{t: t, version: "42", revision: 1, fail: map[string]fakehttp.Response{}}

	for _, n := range []int{41, 36, 43, 38, 42, 37, 40} {
		f.versions = append(f.versions, fakeVersion{number: n})
	}

	api.HandleFunc(http.MethodGet, aliasPath, f.serve("GetAlias"))
	api.HandleFunc(http.MethodPut, aliasPath, f.serve("UpdateAlias"))
	api.HandleFunc(http.MethodGet, functionPath+"/versions", f.serve("ListVersionsByFunction"))
	api.HandleFunc(http.MethodGet, functionPath+"/configuration", f.serve("GetFunctionConfiguration"))

	return f, api
}

// hashOf is the hex SHA-256 the fake gives the code of a version.
func hashOf(number int) string {
	sum := sha256.Sum256([]byte("code-" + strconv.Itoa(number)))

	return hex.EncodeToString(sum[:])
}

// serve returns the handler of one operation.
func (f *fakeLambda) serve(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		body := readBody(f.t, req)

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
		case "GetAlias":
			out = f.getAlias(w)
		case "UpdateAlias":
			f.updates = append(f.updates, body)
			out = f.updateAlias(w, body)
		case "ListVersionsByFunction":
			out = f.listVersions(req)
		default:
			out = f.getConfiguration(w, req.URL.Query().Get("Qualifier"))
		}

		if out == nil {
			return
		}

		w.Header().Set("Content-Type", jsonType)

		if err := json.NewEncoder(w).Encode(out); err != nil {
			f.t.Errorf("write the answer: %v", err)
		}
	}
}

// readBody returns the body of a request.
func readBody(t *testing.T, req *http.Request) json.RawMessage {
	t.Helper()

	var body json.RawMessage
	if req.ContentLength != 0 {
		require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
	}

	return body
}

// apiError answers with an error of Lambda.
func apiError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", jsonType)
	w.Header().Set("X-Amzn-Errortype", code)
	w.WriteHeader(status)

	_, _ = fmt.Fprintf(w, `{"Type":"User","Message":%q}`, message)
}

// aliasJSON is the alias as Lambda describes it.
func (f *fakeLambda) aliasJSON() map[string]any {
	out := map[string]any{
		"AliasArn": "arn:aws:lambda:us-east-1:111122223333:function:checkout:live", "Name": testAlias,
		"FunctionVersion": f.version, "RevisionId": "rev-" + strconv.Itoa(f.revision),
	}
	if len(f.weights) > 0 {
		out["RoutingConfig"] = map[string]any{"AdditionalVersionWeights": f.weights}
	}

	return out
}

// getAlias answers GetAlias, or writes the error itself and returns nil.
func (f *fakeLambda) getAlias(w http.ResponseWriter) any {
	if f.noAlias {
		apiError(w, http.StatusNotFound, "ResourceNotFoundException", "Cannot find alias arn: arn:aws:lambda:us-east-1:111122223333:function:checkout:live")

		return nil
	}

	return f.aliasJSON()
}

// updateAlias applies UpdateAlias: the alias gets the version and, when the
// body has one, the split. It refuses, as Lambda does, a RevisionId that is
// not the alias's.
func (f *fakeLambda) updateAlias(w http.ResponseWriter, body json.RawMessage) any {
	var in struct {
		FunctionVersion string `json:"FunctionVersion"`
		RevisionId      string `json:"RevisionId"`
		RoutingConfig   *struct {
			AdditionalVersionWeights map[string]float64 `json:"AdditionalVersionWeights"`
		} `json:"RoutingConfig"`
	}

	require.NoError(f.t, json.Unmarshal(body, &in))

	if f.changeBeforeUpdate {
		f.changeBeforeUpdate = false
		f.revision++
	}

	if in.RevisionId != "" && in.RevisionId != "rev-"+strconv.Itoa(f.revision) {
		apiError(w, http.StatusPreconditionFailed, "PreconditionFailedException", "The Revision Id provided does not match the latest Revision Id.")

		return nil
	}

	f.version = in.FunctionVersion
	if in.RoutingConfig != nil && !f.keepsSplit {
		f.weights = in.RoutingConfig.AdditionalVersionWeights
	}

	f.revision++

	return f.aliasJSON()
}

// listVersions answers ListVersionsByFunction in pages of versionsPerPage,
// with $LATEST first and the rest in the order the fake holds them.
func (f *fakeLambda) listVersions(req *http.Request) any {
	all := []map[string]any{{"Version": "$LATEST"}}
	for _, v := range f.versions {
		all = append(all, map[string]any{"Version": strconv.Itoa(v.number)})
	}

	start := 0
	if marker := req.URL.Query().Get("Marker"); marker != "" {
		var err error

		start, err = strconv.Atoi(marker)
		require.NoError(f.t, err)
	}

	end := min(start+versionsPerPage, len(all))
	out := map[string]any{"Versions": all[start:end]}

	if end < len(all) {
		out["NextMarker"] = strconv.Itoa(end)
	}

	return out
}

// getConfiguration answers GetFunctionConfiguration for a version, or writes
// the error itself and returns nil.
func (f *fakeLambda) getConfiguration(w http.ResponseWriter, qualifier string) any {
	for _, v := range f.versions {
		if strconv.Itoa(v.number) != qualifier || v.hidden {
			continue
		}

		sum := sha256.Sum256([]byte("code-" + qualifier))
		cfg := map[string]any{
			"FunctionName": testFunction, "Version": qualifier, "CodeSha256": base64.StdEncoding.EncodeToString(sum[:]),
			"Description": "build " + qualifier, "LastModified": modified, "PackageType": "Zip", "Runtime": "python3.12",
		}

		if v.image {
			cfg["PackageType"] = "Image"
			delete(cfg, "Runtime")
		}

		return cfg
	}

	apiError(w, http.StatusNotFound, "ResourceNotFoundException", "Function not found: arn:aws:lambda:us-east-1:111122223333:function:checkout:"+qualifier)

	return nil
}

// set changes the alias, as a change by someone else would.
func (f *fakeLambda) set(version string, weights map[string]float64) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.version, f.weights = version, weights
	f.revision++
}

// count returns how many times an operation was called.
func (f *fakeLambda) count(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(slices.DeleteFunc(slices.Clone(f.ops), func(o string) bool { return o != op }))
}

// opsCalled returns the operations called, in order.
func (f *fakeLambda) opsCalled() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.ops)
}

// forgetOps clears the operations called.
func (f *fakeLambda) forgetOps() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.ops = nil
}

// updateBodies returns the bodies of the UpdateAlias calls.
func (f *fakeLambda) updateBodies() []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.updates)
}

// failWith makes an operation answer with an error of Lambda.
func (f *fakeLambda) failWith(op string, status int, code string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.fail[op] = fakehttp.Response{
		Status:  status,
		Headers: map[string]string{"X-Amzn-Errortype": code},
		Body:    []byte(`{"Type":"User","Message":"no"}`),
	}
}

// newTestAction returns an action on a fake Lambda and a fake STS. edit, when
// set, changes the config first.
func newTestAction(t *testing.T, edit func(*Config)) (*Rollback, *fakeLambda) {
	t.Helper()

	f, api := newFakeLambda(t)

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

	r, err := build(t.Context(), "lambda-rollback", cfg, api.Client(), buildOptions{
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

// errorsIsPermanent reports whether err says Sidekick should not try again.
func errorsIsPermanent(err error) bool {
	return errors.Is(err, action.ErrPermanent)
}
