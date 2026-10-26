package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/nourtalaat/sidekick/internal/source"
)

// testNow is the clock of every test source.
var testNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

// errConnection stands for a request that never reached the API server.
var errConnection = errors.New("dial tcp 10.0.0.1:443: connect: connection refused")

// testConsoleURL is a consoleURL template for the tests.
const testConsoleURL = "https://console.example.com/{{.Resource}}/{{.Namespace}}/{{.Name}}"

// reactor makes the fake clientset answer one verb and resource itself.
type reactor struct {
	verb     string
	resource string
	react    k8stesting.ReactionFunc
}

// newTestSource returns a source on a fake clientset that holds objects,
// with the reactors in front of it.
func newTestSource(t *testing.T, cfg Config, objects []runtime.Object, reactors ...reactor) *Source {
	t.Helper()

	client := fake.NewClientset(objects...)
	for _, r := range reactors {
		client.PrependReactor(r.verb, r.resource, r.react)
	}

	s, err := newSource("k8s", cfg, func(context.Context) (kubernetes.Interface, error) { return client, nil }, func() time.Time { return testNow })
	require.NoError(t, err)

	return s
}

// call runs one tool with input on the source.
func call(t *testing.T, s *Source, tool, input string) (source.Result, error) {
	t.Helper()

	return s.Call(t.Context(), source.Call{ID: "call-1", Tool: tool, Input: json.RawMessage(input)})
}

// failWith returns a reactor that fails every verb on resource with err.
func failWith(verb, resource string, err error) reactor {
	return reactor{verb: verb, resource: resource, react: func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, err
	}}
}

// forbidden is the API server's answer to a request the RBAC refuses.
func forbidden(group, resource string) error {
	return apierrors.NewForbidden(schema.GroupResource{Group: group, Resource: resource}, "", errors.New("RBAC: access denied"))
}

// requireToolError checks that err is a tool error whose message contains
// every part.
func requireToolError(t *testing.T, err error, parts ...string) {
	t.Helper()

	toolErr, ok := errors.AsType[*source.ToolError](err)
	require.True(t, ok, "want a tool error, got %v", err)

	for _, p := range parts {
		require.Contains(t, toolErr.Message, p)
	}
}

// at returns a Kubernetes time the given time before testNow.
func at(ago time.Duration) metav1.Time {
	return metav1.NewTime(testNow.Add(-ago))
}

// ptr returns a pointer to v.
func ptr[T any](v T) *T {
	return &v
}
