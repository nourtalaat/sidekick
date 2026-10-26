package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nourtalaat/sidekick/internal/source"
)

// maxResultSize is the most text, and the largest structured content, a
// result keeps, in bytes. Longer results are cut and marked truncated.
const maxResultSize = 256 << 10

// listPageSize is how many objects one list request asks for.
const listPageSize = 500

// apiResource names a Kubernetes resource and its API group, for permission
// messages.
type apiResource struct {
	name  string
	group string
}

// The resources the tools read.
var (
	resEvents       = apiResource{name: "events"}
	resPods         = apiResource{name: "pods"}
	resPodLogs      = apiResource{name: "pods/log"}
	resNodes        = apiResource{name: "nodes"}
	resDeployments  = apiResource{name: "deployments", group: "apps"}
	resStatefulSets = apiResource{name: "statefulsets", group: "apps"}
	resDaemonSets   = apiResource{name: "daemonsets", group: "apps"}
	resReplicaSets  = apiResource{name: "replicasets", group: "apps"}
	resAutoscalers  = apiResource{name: "horizontalpodautoscalers", group: "autoscaling"}
)

// scope returns the namespaces a list tool reads for the namespace input:
// that one namespace, every configured namespace when the input is empty,
// or every namespace (the empty string) when the config lists none.
func (s *Source) scope(ns string) ([]string, error) {
	if ns != "" {
		if err := s.allowed(ns); err != nil {
			return nil, fmt.Errorf("check the namespace: %w", err)
		}

		return []string{ns}, nil
	}

	if len(s.cfg.Namespaces) == 0 {
		return []string{metav1.NamespaceAll}, nil
	}

	return s.cfg.Namespaces, nil
}

// allowed returns a tool error when the config lists namespaces and ns is
// not one of them.
func (s *Source) allowed(ns string) error {
	if len(s.cfg.Namespaces) == 0 || slices.Contains(s.cfg.Namespaces, ns) {
		return nil
	}

	return &source.ToolError{Message: fmt.Sprintf("namespace %q is not one Sidekick may read; use one of %s", ns, strings.Join(s.cfg.Namespaces, ", "))}
}

// since parses a since input relative to the source's clock. Empty means
// no lower bound, the zero time.
func (s *Source) since(text string) (time.Time, error) {
	if text == "" {
		return time.Time{}, nil
	}

	if d, err := time.ParseDuration(text); err == nil && d >= 0 {
		return s.now().Add(-d), nil
	}

	t, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return time.Time{}, &source.ToolError{Message: fmt.Sprintf("since: %q is neither an RFC 3339 time nor a duration such as 30m or 2h", text)}
	}

	return t, nil
}

// apiError turns an error of a request to the API server into what the
// model should see. A refused permission, a missing object and a bad
// request are tool errors, because the source works and the model can try
// something else; anything else is a failure of the source.
func apiError(verb string, res apiResource, ns string, err error) error {
	where := ""

	switch {
	case res == resNodes:
	case ns == "":
		where = " in any namespace"
	default:
		where = " in namespace " + ns
	}

	group := res.group
	if group == "" {
		group = "core"
	}

	switch {
	case apierrors.IsForbidden(err):
		return &source.ToolError{Message: fmt.Sprintf(
			"Sidekick's account may not %s %s%s: its Role or ClusterRole needs the %q verb on %q in the %s API group. The API server said: %s",
			verb, res.name, where, verb, res.name, group, err.Error())}
	case apierrors.IsNotFound(err), apierrors.IsBadRequest(err), apierrors.IsInvalid(err):
		return &source.ToolError{Message: err.Error()}
	default:
		return fmt.Errorf("%s %s%s: %w", verb, res.name, where, err)
	}
}

// messageOf returns the message of a tool error, or the error's text.
func messageOf(err error) string {
	if toolErr, ok := errors.AsType[*source.ToolError](err); ok {
		return toolErr.Message
	}

	return err.Error()
}

// pagedList is a list response that may continue on another page.
type pagedList interface {
	GetContinue() string
}

// eachPage lists objects page by page, calling each with every page, so a
// large namespace is never held in memory all at once.
func eachPage[L pagedList](ctx context.Context, opts metav1.ListOptions, list func(context.Context, metav1.ListOptions) (L, error), each func(L)) error {
	opts.Limit = listPageSize

	for {
		page, err := list(ctx, opts)
		if err != nil {
			return fmt.Errorf("list a page: %w", err)
		}

		each(page)

		next := page.GetContinue()
		if next == "" {
			return nil
		}

		opts.Continue = next
	}
}

// result builds a Result from readable text and the same data as JSON.
// Text over maxResultSize is cut, and structured data over it is left out;
// either way the result is marked truncated.
func result[T any](text string, data T, evidence []source.Evidence, truncated bool) (source.Result, error) {
	structured, err := json.Marshal(data)
	if err != nil {
		return source.Result{}, fmt.Errorf("encode the result: %w", err)
	}

	out := source.Result{Text: text, Structured: structured, Evidence: evidence, Truncated: truncated}

	if len(out.Text) > maxResultSize {
		out.Text, out.Truncated = source.CutText(out.Text, maxResultSize), true
	}

	if len(out.Structured) > maxResultSize {
		out.Structured, out.Truncated = nil, true
	}

	return out, nil
}

// limit returns n within 1 and maxValue, or defaultValue when n is zero.
func limit(n, defaultValue, maxValue int) int {
	if n <= 0 {
		return defaultValue
	}

	return min(n, maxValue)
}

// timestamp formats a time for the readable text, or "unknown" for the
// zero time.
func timestamp(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}

	return t.UTC().Format(time.RFC3339)
}

// plural returns word, with an s when n is not one, after n.
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}

	return fmt.Sprintf("%d %ss", n, word)
}

// where names the namespaces of a list for the readable text.
func where(namespaces []string) string {
	if len(namespaces) == 1 && namespaces[0] == metav1.NamespaceAll {
		return "every namespace"
	}

	if len(namespaces) == 1 {
		return "namespace " + namespaces[0]
	}

	return "namespaces " + strings.Join(namespaces, ", ")
}

// commandNamespace is the namespace for a kubectl command: the only one,
// or every namespace when the tool read several.
func commandNamespace(namespaces []string) string {
	if len(namespaces) == 1 {
		return namespaces[0]
	}

	return metav1.NamespaceAll
}
