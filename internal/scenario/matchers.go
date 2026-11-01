package scenario

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/nourtalaat/sidekick/internal/source/mock"
)

const (
	// maxEqualsLen is the longest string a fixture matches with equals.
	maxEqualsLen = 200
	// minRefLen and maxRefLen bound a hex string that is read as a commit ID.
	minRefLen = 7
	maxRefLen = 40
	// refPrefixLen is how much of a commit ID a fixture keeps.
	refPrefixLen = 7
	// maxQueryTerms is how many words of a query a fixture matches on.
	maxQueryTerms = 3
	// queryMarker starts the regex of a fixture that matches on the words of
	// a query until the draft is cleaned. The words are taken from the
	// cleaned query, so a regex never holds a value that was replaced.
	queryMarker = "@@query-terms@@"
)

// identifyingArgs are the argument names, lower-cased and without "_" and
// "-", that name what a call is about. A fixture keeps these and drops the
// rest, such as time ranges, limits and sort orders.
var identifyingArgs = []string{
	"service", "servicename", "app", "application", "repo", "repository", "owner", "namespace", "ns", "pod",
	"podname", "container", "deployment", "statefulset", "daemonset", "workload", "resource", "resourcename",
	"name", "cluster", "env", "environment", "project", "projectid", "region", "host", "hostname", "instance",
	"node", "nodename", "job", "metric", "metricname", "table", "database", "db", "id", "ref", "sha", "commit",
	"ticket", "issue", "incident", "incidentid", "monitor", "monitorid", "alert", "alertname",
}

// refArgs are the identifying arguments that hold a commit ID.
var refArgs = []string{"ref", "sha", "commit"}

// queryArgs are the argument names that hold free text. A fixture matches
// on their words only when a call has no identifying argument.
var queryArgs = []string{
	"query", "q", "expr", "expression", "filter", "search", "searchterm", "term", "terms", "text", "message",
	"sql", "promql", "logql", "lucene", "pattern", "keyword", "keywords",
}

var (
	hexRef    = regexp.MustCompile(`^[0-9a-fA-F]+$`)
	queryWord = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_.-]{3,}`)
)

// matchers is the loosened match of one call.
type matchers struct {
	args map[string]mock.Matcher
	// dropped are the names of the arguments the fixture does not keep.
	dropped []string
}

// loosen returns the matchers for a call's input: equals for each
// identifying argument and, when there is none, a regex of the words of its
// free-text query. Other arguments are dropped.
func loosen(input json.RawMessage) (matchers, error) {
	out := matchers{args: map[string]mock.Matcher{}}

	if len(bytes.TrimSpace(input)) == 0 {
		return out, nil
	}

	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()

	var args map[string]any
	if err := dec.Decode(&args); err != nil {
		return out, fmt.Errorf("decode the call's input: %w", err)
	}

	names := make([]string, 0, len(args))
	for name := range args {
		names = append(names, name)
	}

	sort.Strings(names)

	var queryName, query string

	for _, name := range names {
		key := normalizeArg(name)

		switch {
		case slices.Contains(identifyingArgs, key):
			if m, ok := equalsMatcher(key, args[name]); ok {
				out.args[name] = m

				continue
			}
		case slices.Contains(queryArgs, key):
			if text, ok := args[name].(string); ok && query == "" {
				queryName, query = name, text
			}
		}

		out.dropped = append(out.dropped, name)
	}

	if len(out.args) == 0 && queryWord.MatchString(query) {
		out.args[queryName] = mock.Matcher{Regex: queryMarker + query}
		out.dropped = slices.DeleteFunc(out.dropped, func(n string) bool { return n == queryName })
	}

	return out, nil
}

// equalsMatcher returns the matcher of an identifying argument's value.
func equalsMatcher(key string, value any) (mock.Matcher, bool) {
	switch v := value.(type) {
	case string:
		switch {
		case v == "" || len(v) > maxEqualsLen:
			return mock.Matcher{}, false
		case slices.Contains(refArgs, key) && len(v) >= minRefLen && len(v) <= maxRefLen && hexRef.MatchString(v):
			return mock.Matcher{Regex: "^" + v[:refPrefixLen]}, true
		}

		return mock.Matcher{Equals: v}, true
	case bool:
		return mock.Matcher{Equals: v}, true
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return mock.Matcher{Equals: i}, true
		}

		if f, err := v.Float64(); err == nil {
			return mock.Matcher{Equals: f}, true
		}
	}

	return mock.Matcher{}, false
}

// signature identifies a set of matchers, so calls with the same ones share
// a fixture.
func (m matchers) signature() string {
	names := make([]string, 0, len(m.args))
	for name := range m.args {
		names = append(names, name)
	}

	sort.Strings(names)

	var b strings.Builder

	for _, name := range names {
		a := m.args[name]
		fmt.Fprintf(&b, "%s=%v|%s;", name, a.Equals, a.Regex)
	}

	return b.String()
}

// termsRegex returns a case-insensitive regex of the longest words of a
// cleaned query.
func termsRegex(query string) string {
	var words []string

	for _, w := range queryWord.FindAllString(query, -1) {
		w = strings.ToLower(w)
		if !slices.Contains(words, w) {
			words = append(words, w)
		}
	}

	sort.SliceStable(words, func(i, j int) bool { return len(words[i]) > len(words[j]) })

	if len(words) > maxQueryTerms {
		words = words[:maxQueryTerms]
	}

	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = regexp.QuoteMeta(w)
	}

	return "(?i)" + strings.Join(quoted, "|")
}

// normalizeArg lower-cases an argument name and drops "_", "-" and ".".
func normalizeArg(name string) string {
	return strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.ToLower(name))
}
