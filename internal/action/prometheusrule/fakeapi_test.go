package prometheusrule

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

const (
	testNamespace = "monitoring"
	testName      = "checkout-alerts"
	podNamespace  = "sidekick"
	// ownToken is the token of Sidekick's own service account in the tests.
	ownToken = "own-token-secret-value"

	tokenPath = "/api/v1/namespaces/" + podNamespace + "/serviceaccounts/" + DefaultServiceAccount + "/token"
	rulesPath = "/apis/monitoring.coreos.com/v1/namespaces/" + testNamespace + "/prometheusrules/" + testName
)

// seedGroups is the spec.groups the fake API starts with.
const seedGroups = `[
  {"name": "checkout.rules", "interval": "30s", "rules": [
    {"alert": "HighLatency", "expr": "latency > 2", "for": "5m", "labels": {"severity": "warning", "team": "payments"}, "annotations": {"summary": "Latency is high"}},
    {"alert": "ErrorRate", "expr": "errors > 0.1", "for": "10m", "labels": {"severity": "critical"}},
    {"alert": "AlwaysOn", "expr": 1},
    {"record": "job:latency:p99", "expr": "histogram_quantile(0.99, rate(latency_bucket[5m]))"}
  ]},
  {"name": "checkout.other", "rules": [{"alert": "Other", "expr": "up == 0"}]}
]`

// durationPattern is the pattern the CRD gives for and keep_firing_for.
var durationPattern = regexp.MustCompile(`^(0|(([0-9]+)y)?(([0-9]+)w)?(([0-9]+)d)?(([0-9]+)h)?(([0-9]+)m)?(([0-9]+)s)?(([0-9]+)ms)?)$`)

// crdRuleFields are the fields of a rule in the CRD. The API server drops
// any other.
var crdRuleFields = []string{"alert", "record", "expr", "for", "keep_firing_for", "labels", "annotations"}

// patchRecord is one patch the fake API received.
type patchRecord struct {
	dryRun      bool
	contentType string
	manager     string
	body        map[string]any
}

// fakeAPI is a Kubernetes API server that keeps one PrometheusRule. It
// does what the real one does with a merge patch that matters here: lists
// are replaced whole, the resourceVersion in a patch must be the stored one
// (409 otherwise), a dry run changes nothing and answers with the object as
// it would be stored, unknown rule fields are dropped, the CRD's patterns
// are enforced (422), and every write moves the resourceVersion on.
type fakeAPI struct {
	mu  sync.Mutex
	obj map[string]any
	rv  int

	minted      []string
	patches     []patchRecord
	tokenStatus int
	// tokenEcho is put in the message of a refused token request, as a
	// misbehaving proxy could echo a header.
	tokenEcho string
	// oldCRD drops keep_firing_for, as a CRD from before the field does.
	oldCRD bool
	// beforeWrite is called before a real patch is checked, for a test that
	// has someone else write between the read and the write.
	beforeWrite func()
	// failWrites is the status that every real patch answers, when set.
	failWrites int
}

// newFakeAPI starts the fake API server for one test.
func newFakeAPI(t *testing.T) (*fakehttp.Server, *fakeAPI) {
	t.Helper()

	f := &fakeAPI{rv: 100}
	f.obj = map[string]any{
		"apiVersion": "monitoring.coreos.com/v1",
		"kind":       "PrometheusRule",
		"metadata":   map[string]any{"name": testName, "namespace": testNamespace, "generation": 1},
		"spec":       map[string]any{"groups": decodeAny(t, seedGroups)},
	}

	api := fakehttp.New(t)
	api.HandleFunc(http.MethodPost, tokenPath, f.token)
	api.HandleFunc(http.MethodGet, rulesPath, f.get)
	api.HandleFunc(http.MethodPatch, rulesPath, f.patch)

	return api, f
}

// restConfig returns the config of Sidekick's own service account for the
// fake server.
func restConfig(api *fakehttp.Server) *rest.Config {
	return &rest.Config{Host: api.URL(), BearerToken: ownToken}
}

// touch moves the resourceVersion on, as a write to the status or to
// another rule does.
func (f *fakeAPI) touch() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.rv++
}

// mintedCount returns how many tokens the fake API has given.
func (f *fakeAPI) mintedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.minted)
}

// version returns the stored resourceVersion.
func (f *fakeAPI) version() string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return fmt.Sprint(f.rv)
}

// groups returns the stored spec.groups.
func (f *fakeAPI) groups(t *testing.T) groupList {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	raw, err := json.Marshal(f.obj["spec"].(map[string]any)["groups"])
	require.NoError(t, err)

	g, err := parseGroups(raw)
	require.NoError(t, err)

	return g
}

// edit changes the stored groups the way another person would, and moves
// the resourceVersion on.
func (f *fakeAPI) edit(t *testing.T, change func(groups []any)) {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	change(f.obj["spec"].(map[string]any)["groups"].([]any))

	f.rv++
}

// writes returns the patches that were not dry runs.
func (f *fakeAPI) writes() []patchRecord {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.DeleteFunc(slices.Clone(f.patches), func(p patchRecord) bool { return p.dryRun })
}

// dryRuns returns the patches that were dry runs.
func (f *fakeAPI) dryRuns() []patchRecord {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.DeleteFunc(slices.Clone(f.patches), func(p patchRecord) bool { return !p.dryRun })
}

// token answers a TokenRequest.
func (f *fakeAPI) token(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if r.Header.Get("Authorization") != "Bearer "+ownToken {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "Unauthorized")

		return
	}

	if f.tokenStatus != 0 {
		message := "serviceaccounts \"sidekick-actions\" is forbidden"
		if f.tokenEcho != "" {
			message += " for the request with the header Authorization: Bearer " + f.tokenEcho
		}

		writeStatus(w, f.tokenStatus, "Forbidden", message)

		return
	}

	var request struct {
		Spec struct {
			ExpirationSeconds int64 `json:"expirationSeconds"`
		} `json:"spec"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Spec.ExpirationSeconds < 600 {
		writeStatus(w, http.StatusUnprocessableEntity, "Invalid", "may not specify a duration less than 10 minutes")

		return
	}

	token := fmt.Sprintf("minted-token-%d", len(f.minted)+1)
	f.minted = append(f.minted, token)

	w.Header().Set("Content-Type", "application/json")

	_ = json.NewEncoder(w).Encode(map[string]any{
		"apiVersion": "authentication.k8s.io/v1",
		"kind":       "TokenRequest",
		"spec":       map[string]any{"expirationSeconds": request.Spec.ExpirationSeconds},
		"status":     map[string]any{"token": token, "expirationTimestamp": "2099-01-01T00:00:00Z"},
	})
}

// get answers a read of the PrometheusRule.
func (f *fakeAPI) get(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.authorised(w, r) {
		return
	}

	f.writeObject(w, f.obj)
}

// patch answers a merge patch of the PrometheusRule.
func (f *fakeAPI) patch(w http.ResponseWriter, r *http.Request) {
	dryRun := r.URL.Query().Get("dryRun") == "All"

	if !dryRun && f.beforeWrite != nil {
		f.beforeWrite()
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.authorised(w, r) {
		return
	}

	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid patch")

		return
	}

	f.patches = append(f.patches, patchRecord{
		dryRun: dryRun, contentType: r.Header.Get("Content-Type"), manager: r.URL.Query().Get("fieldManager"), body: patch,
	})

	if !dryRun && f.failWrites != 0 {
		writeStatus(w, f.failWrites, "InternalError", "the server could not write")

		return
	}

	if r.Header.Get("Content-Type") != "application/merge-patch+json" {
		writeStatus(w, http.StatusUnsupportedMediaType, "UnsupportedMediaType", "unsupported patch type")

		return
	}

	current := deepCopy(f.obj).(map[string]any)
	current["metadata"].(map[string]any)["resourceVersion"] = fmt.Sprint(f.rv)

	merged := mergePatch(current, patch).(map[string]any)

	if got := fmt.Sprint(merged["metadata"].(map[string]any)["resourceVersion"]); got != fmt.Sprint(f.rv) {
		writeStatus(w, http.StatusConflict, "Conflict",
			"Operation cannot be fulfilled on prometheusrules.monitoring.coreos.com \""+testName+"\": the object has been modified; please apply your changes to the latest version and try again")

		return
	}

	if problems := f.admit(merged); len(problems) > 0 {
		writeStatus(w, http.StatusUnprocessableEntity, "Invalid", "PrometheusRule \""+testName+"\" is invalid: "+strings.Join(problems, "; "))

		return
	}

	if dryRun {
		f.writeObject(w, merged)

		return
	}

	f.rv++
	f.obj = merged

	f.writeObject(w, f.obj)
}

// authorised checks that the request carries a token that was minted, and
// answers 401 when it does not. The caller holds the lock.
func (f *fakeAPI) authorised(w http.ResponseWriter, r *http.Request) bool {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !slices.Contains(f.minted, token) {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "Unauthorized")

		return false
	}

	return true
}

// writeObject sends the object with the current resourceVersion. The caller
// holds the lock.
func (f *fakeAPI) writeObject(w http.ResponseWriter, obj map[string]any) {
	out := deepCopy(obj).(map[string]any)
	out["metadata"].(map[string]any)["resourceVersion"] = fmt.Sprint(f.rv)

	w.Header().Set("Content-Type", "application/json")

	_ = json.NewEncoder(w).Encode(out)
}

// admit does what the API server does to an object it is about to store:
// it checks the schema of the CRD and drops the fields the CRD does not
// know. It returns the problems it found. The caller holds the lock.
func (f *fakeAPI) admit(obj map[string]any) []string {
	var problems []string

	groups, _ := obj["spec"].(map[string]any)["groups"].([]any)

	for _, g := range groups {
		group, _ := g.(map[string]any)

		if name, _ := group["name"].(string); name == "" {
			problems = append(problems, "spec.groups.name: Invalid value: \"\": should be at least 1 chars long")
		}

		rules, _ := group["rules"].([]any)
		for _, r := range rules {
			rule, _ := r.(map[string]any)

			f.pruneRule(rule)
			problems = append(problems, ruleProblems(rule)...)
		}
	}

	return problems
}

// pruneRule drops the fields of a rule that the CRD does not know.
func (f *fakeAPI) pruneRule(rule map[string]any) {
	for key := range rule {
		if !slices.Contains(crdRuleFields, key) || (f.oldCRD && key == "keep_firing_for") {
			delete(rule, key)
		}
	}
}

// ruleProblems checks a rule against the CRD's types and patterns.
func ruleProblems(rule map[string]any) []string {
	var problems []string

	switch rule["expr"].(type) {
	case string, float64:
	default:
		problems = append(problems, "spec.groups.rules.expr: Required value")
	}

	for _, key := range []string{"for", "keep_firing_for"} {
		value, ok := rule[key]
		if !ok {
			continue
		}

		text, isText := value.(string)

		switch {
		case !isText:
			problems = append(problems, "spec.groups.rules."+key+": Invalid value: must be of type string")
		case !durationPattern.MatchString(text):
			problems = append(problems, fmt.Sprintf("spec.groups.rules.%s: Invalid value: %q: should match the duration pattern", key, text))
		case key == "keep_firing_for" && text == "":
			problems = append(problems, "spec.groups.rules.keep_firing_for: Invalid value: \"\": should be at least 1 chars long")
		}
	}

	for _, key := range []string{"labels", "annotations"} {
		values, ok := rule[key].(map[string]any)
		if !ok {
			if _, present := rule[key]; present {
				problems = append(problems, "spec.groups.rules."+key+": Invalid value: must be of type object")
			}

			continue
		}

		for name, v := range values {
			if _, isText := v.(string); !isText {
				problems = append(problems, fmt.Sprintf("spec.groups.rules.%s.%s: Invalid value: must be of type string", key, name))
			}
		}
	}

	return problems
}

// mergePatch applies a JSON merge patch (RFC 7386): objects are merged key
// by key, null removes a key, and anything else, a list included, replaces
// the value whole.
func mergePatch(target, patch any) any {
	patchObj, ok := patch.(map[string]any)
	if !ok {
		return patch
	}

	targetObj, ok := target.(map[string]any)
	if !ok {
		targetObj = map[string]any{}
	}

	for key, value := range patchObj {
		if value == nil {
			delete(targetObj, key)

			continue
		}

		targetObj[key] = mergePatch(targetObj[key], value)
	}

	return targetObj
}

// deepCopy copies a decoded JSON value.
func deepCopy(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = deepCopy(e)
		}

		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = deepCopy(e)
		}

		return out
	default:
		return v
	}
}

// decodeAny decodes JSON into plain values.
func decodeAny(t *testing.T, raw string) any {
	t.Helper()

	var v any

	require.NoError(t, json.Unmarshal([]byte(raw), &v))

	return v
}

// writeStatus sends a Kubernetes Status object.
func writeStatus(w http.ResponseWriter, code int, reason, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)

	_ = json.NewEncoder(w).Encode(map[string]any{
		"apiVersion": "v1", "kind": "Status", "status": "Failure",
		"reason": reason, "message": message, "code": code,
	})
}

// fakeValidator is the validator. It fails a file that holds the text
// BAD_EXPR, with an error in the form of promtool's, and keeps the requests.
type fakeValidator struct {
	mu       sync.Mutex
	requests []validateclient.Request
	// status is the status every request answers, when set.
	status int
}

// newFakeValidator starts the fake validator for one test.
func newFakeValidator(t *testing.T) (*fakehttp.Server, *fakeValidator) {
	t.Helper()

	f := &fakeValidator{}

	srv := fakehttp.New(t)
	srv.HandleFunc(http.MethodPost, "/validate", f.validate)

	return srv, f
}

// seen returns the requests the validator got.
func (f *fakeValidator) seen() []validateclient.Request {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.requests)
}

func (f *fakeValidator) validate(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var req validateclient.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)

		return
	}

	f.requests = append(f.requests, req)

	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(`{"error": "the validator is busy"}`))

		return
	}

	res := validateclient.Result{OK: true, Errors: []validateclient.Problem{}}

	for _, file := range req.Files {
		if strings.Contains(file.Content, "BAD_EXPR") {
			res.OK = false
			res.Errors = append(res.Errors, validateclient.Problem{
				Path: file.Path, Line: 12, Message: `group "checkout.rules", rule 1, "HighLatency": could not parse expression: unexpected identifier "BAD_EXPR"`,
			})
		}
	}

	_ = json.NewEncoder(w).Encode(res)
}
