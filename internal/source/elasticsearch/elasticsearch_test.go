package elasticsearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Paths of the fake APIs.
const (
	searchPath    = "/logs-*/_search"
	fieldCapsPath = "/logs-*/_field_caps"
)

// The ends of the last hour at testNow, as the requests send them.
const (
	lastHourFilter = `{"range":{"@timestamp":{"format":"strict_date_optional_time",` +
		`"gte":"2026-05-04T09:00:00.000Z","lt":"2026-05-04T10:00:00.000Z"}}}`
	sortByTime = `"sort":[{"@timestamp":{"order":"desc","unmapped_type":"date"}}]`
)

// productPlaceholder stands for the flavor's product name in the wanted
// texts of a case.
const productPlaceholder = "{product}"

// lastHourLink is the discoverURL link of a search of logs-* for
// level:error in the last hour.
const lastHourLink = "https://kibana.example.com/app/discover#/?_g=(time:(from:'2026-05-04T09%3A00%3A00.000Z',to:'2026-05-04T10%3A00%3A00.000Z'))" +
	"&_a=(index:'logs-%2A',query:(language:lucene,query:'level%3Aerror'))"

func TestSourceCall(t *testing.T) {
	t.Parallel()

	hourStart := testNow.Add(-time.Hour).UnixMilli()

	tests := []struct {
		name     string
		patterns []string
		tool     string
		input    string
		routes   []route
		// wantBodies are the JSON bodies of the requests, in order.
		wantBodies []string
		// wantQueries are the encoded queries of the requests, in order.
		wantQueries   []string
		want          string
		wantEvidence  []source.Evidence
		wantTruncated bool
		wantNoData    bool
		wantText      string
		wantToolError string
		wantErr       string
	}{
		{
			name:  "search_logs returns the newest hits with the default fields",
			tool:  toolSearchLogs,
			input: `{"query":"level:error","since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodPost, searchPath, []fakehttp.Response{body(`{"took":3,"timed_out":false,
				"_shards":{"total":2,"successful":2,"skipped":0,"failed":0},
				"hits":{"total":{"value":2,"relation":"eq"},"max_score":null,"hits":[
					{"_index":"logs-2026.05.04","_id":"a1","_score":null,"sort":[1777888680000],"_source":{
						"@timestamp":"2026-05-04T09:58:00.000Z","message":"payment timeout\n","log":{"level":"error"},
						"service":{"name":"checkout"},"http":{"status":503,"bytes":12345678901234567}}},
					{"_index":"logs-2026.05.04","_id":"a2","_source":{
						"@timestamp":"2026-05-04T09:57:00.000Z","message":"db down","log.level":"error","tags":["db","prod"]}}
				]}}`)}}},
			wantBodies: []string{`{"size":50,"query":{"bool":{"filter":[` + lastHourFilter + `,{"query_string":{"query":"level:error"}}]}},` +
				sortByTime + `,"_source":{"includes":["@timestamp","message","log.level","service.name","host.name","error.message","trace.id"]},` +
				`"track_total_hits":true}`},
			want: `{"index":"logs-*","total":2,"hits":[
				{"index":"logs-2026.05.04","id":"a1","fields":{"@timestamp":"2026-05-04T09:58:00.000Z","message":"payment timeout",
					"log.level":"error","service.name":"checkout","http.status":503,"http.bytes":12345678901234567}},
				{"index":"logs-2026.05.04","id":"a2","fields":{"@timestamp":"2026-05-04T09:57:00.000Z","message":"db down",
					"log.level":"error","tags":["db","prod"]}}
			]}`,
			wantEvidence: []source.Evidence{{
				Kind:  source.EvidenceLogQuery,
				Title: `{product} search of logs-* for "level:error" from 2026-05-04T09:00:00.000Z to 2026-05-04T10:00:00.000Z`,
				URL:   lastHourLink,
				At:    testNow,
			}},
		},
		{
			name:     "search_logs reads an index an allowed pattern covers, with the chosen fields, and says when hits are left",
			patterns: []string{"logs-*", "audit-*"},
			tool:     toolSearchLogs,
			input: `{"index":"logs-checkout-*","query":"_index:logs-checkout-2026.05.04 AND level:error",` +
				`"since":"2026-05-04T09:30:00Z","until":"2026-05-04T09:45:00Z","fields":["kubernetes.*","message"],"limit":1}`,
			routes: []route{{http.MethodPost, "/logs-checkout-*/_search", []fakehttp.Response{body(`{"hits":{
				"total":{"value":10000,"relation":"gte"},
				"hits":[{"_index":"logs-checkout-2026.05.04","_id":"b1","_source":{"@timestamp":"2026-05-04T09:44:00.000Z",
					"message":"retrying","kubernetes":{"pod":{"name":"checkout-7d9"}}}}]}}`)}}},
			wantBodies: []string{`{"size":1,"query":{"bool":{"filter":[{"range":{"@timestamp":{"format":"strict_date_optional_time",` +
				`"gte":"2026-05-04T09:30:00.000Z","lt":"2026-05-04T09:45:00.000Z"}}},` +
				`{"query_string":{"query":"_index:logs-checkout-2026.05.04 AND level:error"}}]}},` +
				sortByTime + `,"_source":{"includes":["@timestamp","message","kubernetes.*"]},"track_total_hits":true}`},
			want: `{"index":"logs-checkout-*","total":10000,"totalAtLeast":true,"hits":[{"index":"logs-checkout-2026.05.04","id":"b1",
				"fields":{"@timestamp":"2026-05-04T09:44:00.000Z","message":"retrying","kubernetes.pod.name":"checkout-7d9"}}]}`,
			wantEvidence: []source.Evidence{{
				Kind: source.EvidenceLogQuery,
				Title: `{product} search of logs-checkout-* for "_index:logs-checkout-2026.05.04 AND level:error" ` +
					`from 2026-05-04T09:30:00.000Z to 2026-05-04T09:45:00.000Z`,
				URL: "https://kibana.example.com/app/discover#/?_g=(time:(from:'2026-05-04T09%3A30%3A00.000Z',to:'2026-05-04T09%3A45%3A00.000Z'))" +
					"&_a=(index:'logs-checkout-%2A',query:(language:lucene,query:'_index%3Alogs-checkout-2026.05.04%20AND%20level%3Aerror'))",
				At: testNow.Add(-15 * time.Minute),
			}},
			wantTruncated: true,
		},
		{
			name:  "search_logs says when shards failed",
			tool:  toolSearchLogs,
			input: `{"since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodPost, searchPath, []fakehttp.Response{body(`{"_shards":{"total":2,"failed":1},
				"hits":{"total":{"value":1,"relation":"eq"},"hits":[{"_index":"logs-a","_id":"c1","_source":{"message":"x"}}]}}`)}}},
			wantBodies: []string{`{"size":50,"query":{"bool":{"filter":[` + lastHourFilter + `]}},` + sortByTime +
				`,"_source":{"includes":["@timestamp","message","log.level","service.name","host.name","error.message","trace.id"]},` +
				`"track_total_hits":true}`},
			want: `{"index":"logs-*","total":1,"partial":true,"hits":[{"index":"logs-a","id":"c1","fields":{"message":"x"}}]}`,
			wantEvidence: []source.Evidence{{
				Kind:  source.EvidenceLogQuery,
				Title: "{product} search of logs-* from 2026-05-04T09:00:00.000Z to 2026-05-04T10:00:00.000Z",
				URL: "https://kibana.example.com/app/discover#/?_g=(time:(from:'2026-05-04T09%3A00%3A00.000Z',to:'2026-05-04T10%3A00%3A00.000Z'))" +
					"&_a=(index:'logs-%2A',query:(language:lucene,query:''))",
				At: testNow,
			}},
			wantTruncated: true,
		},
		{
			name:       "search_logs with no hits is no data with a link to the search",
			tool:       toolSearchLogs,
			input:      `{"query":"level:error","since":"2026-05-04T09:00:00Z","until":"2026-05-04T10:00:00Z"}`,
			routes:     []route{{http.MethodPost, searchPath, []fakehttp.Response{body(`{"hits":{"total":{"value":0,"relation":"eq"},"hits":[]}}`)}}},
			wantNoData: true,
			wantText:   "No logs in logs-* matched between 2026-05-04T09:00:00.000Z and 2026-05-04T10:00:00.000Z.",
			wantEvidence: []source.Evidence{{
				Kind:  source.EvidenceLogQuery,
				Title: `{product} search of logs-* for "level:error" from 2026-05-04T09:00:00.000Z to 2026-05-04T10:00:00.000Z`,
				URL:   lastHourLink,
				At:    testNow,
			}},
		},
		{
			name:  "a query the server cannot parse is a tool error with its cause",
			tool:  toolSearchLogs,
			input: `{"query":"level:(","since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodPost, searchPath, []fakehttp.Response{{Status: http.StatusBadRequest, Headers: jsonHeaders,
				Body: []byte(`{"error":{"root_cause":[{"type":"query_shard_exception","reason":"Failed to parse query [level:(]"}],` +
					`"type":"search_phase_execution_exception","reason":"all shards failed"},"status":400}`)}}}},
			wantToolError: "{product} answered 400 search_phase_execution_exception: all shards failed; " +
				"caused by query_shard_exception: Failed to parse query [level:(]",
		},
		{
			name:  "a privilege the role lacks is a tool error",
			tool:  toolSearchLogs,
			input: `{"since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodPost, searchPath, []fakehttp.Response{failed(http.StatusForbidden, "security_exception",
				"action [indices:data/read/search] is unauthorized for user [sidekick]")}}},
			wantToolError: "{product} answered 403 security_exception: action [indices:data/read/search] is unauthorized for user [sidekick]",
		},
		{
			name:  "an error given as text is a tool error",
			tool:  toolSearchLogs,
			input: `{"since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodPost, searchPath, []fakehttp.Response{{Status: http.StatusMethodNotAllowed, Headers: jsonHeaders,
				Body: []byte(`{"error":"Incorrect HTTP method","status":405}`)}}}},
			wantToolError: "{product} answered 405: Incorrect HTTP method",
		},
		{
			name:  "an index that does not exist is no data",
			tool:  toolSearchLogs,
			input: `{"since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodPost, searchPath, []fakehttp.Response{failed(http.StatusNotFound, "index_not_found_exception",
				"no such index [logs-*]")}}},
			wantNoData: true,
			wantText:   "Not found: {product} answered 404 index_not_found_exception: no such index [logs-*]",
		},
		{
			name:  "refused credentials are a failure of the source",
			tool:  toolSearchLogs,
			input: `{"since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodPost, searchPath, []fakehttp.Response{failed(http.StatusUnauthorized, "security_exception",
				"missing authentication credentials")}}},
			wantErr: "{product} answered 401 security_exception: missing authentication credentials",
		},
		{
			name:  "a rate limit is a failure of the source",
			tool:  toolSearchLogs,
			input: `{"since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodPost, searchPath, []fakehttp.Response{failed(http.StatusTooManyRequests,
				"es_rejected_execution_exception", "rejected execution")}}},
			wantErr: "{product} answered 429 es_rejected_execution_exception: rejected execution",
		},
		{
			name:    "a server error without a body is a failure of the source",
			tool:    toolSearchLogs,
			input:   `{"since":"2026-05-04T09:00:00Z"}`,
			routes:  []route{{http.MethodPost, searchPath, []fakehttp.Response{{Status: http.StatusBadGateway, Body: []byte("<html>bad gateway</html>")}}}},
			wantErr: "{product} answered 502",
		},
		{
			name:    "an answer that is not JSON is a failure of the source",
			tool:    toolSearchLogs,
			input:   `{"since":"2026-05-04T09:00:00Z"}`,
			routes:  []route{{http.MethodPost, searchPath, []fakehttp.Response{body("{")}}},
			wantErr: "decode the answer",
		},
		{
			name:  "count_logs counts in buckets over the range",
			tool:  toolCountLogs,
			input: `{"query":"level:error","since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodPost, searchPath, []fakehttp.Response{body(fmt.Sprintf(`{"hits":{"total":{"value":7,"relation":"eq"},"hits":[]},
				"aggregations":{"over_time":{"buckets":[
					{"key_as_string":"2026-05-04T09:00:00.000Z","key":%d,"doc_count":0},
					{"key":%d,"doc_count":5},{"key":%d,"doc_count":2}]}}}`, hourStart, hourStart+60000, hourStart+180000))}}},
			wantBodies: []string{fmt.Sprintf(`{"size":0,"query":{"bool":{"filter":[`+lastHourFilter+`,{"query_string":{"query":"level:error"}}]}},`+
				`"track_total_hits":true,"aggs":{"over_time":{"date_histogram":{"field":"@timestamp","fixed_interval":"60s","min_doc_count":0,`+
				`"extended_bounds":{"min":%d,"max":%d}}}}}`, hourStart, testNow.UnixMilli()-1)},
			want: `{"index":"logs-*","total":7,"intervalSeconds":60,"start":"2026-05-04T09:00:00Z","counts":[0,5,0,2]}`,
			wantEvidence: []source.Evidence{{
				Kind:  source.EvidenceLogQuery,
				Title: `{product} search of logs-* for "level:error" from 2026-05-04T09:00:00.000Z to 2026-05-04T10:00:00.000Z`,
				URL:   lastHourLink,
				At:    testNow,
			}},
		},
		{
			name:  "count_logs splits the counts by the most common values of a field",
			tool:  toolCountLogs,
			input: `{"since":"2026-05-04T09:00:00Z","until":"2026-05-04T09:30:00Z","interval":600,"splitBy":"service.name"}`,
			routes: []route{{http.MethodPost, searchPath, []fakehttp.Response{body(fmt.Sprintf(`{"hits":{"total":{"value":12,"relation":"eq"},"hits":[]},
				"aggregations":{
					"over_time":{"buckets":[{"key":%[1]d,"doc_count":4},{"key":%[2]d,"doc_count":8}]},
					"split":{"sum_other_doc_count":3,"buckets":[
						{"key":"checkout","doc_count":9,"over_time":{"buckets":[{"key":%[1]d,"doc_count":1},{"key":%[2]d,"doc_count":8}]}},
						{"key":"cart","doc_count":0,"over_time":{"buckets":[]}}]}}}`, hourStart, hourStart+600000))}}},
			wantBodies: []string{fmt.Sprintf(`{"size":0,"query":{"bool":{"filter":[{"range":{"@timestamp":{"format":"strict_date_optional_time",`+
				`"gte":"2026-05-04T09:00:00.000Z","lt":"2026-05-04T09:30:00.000Z"}}}]}},"track_total_hits":true,"aggs":{`+
				`"over_time":{"date_histogram":{"field":"@timestamp","fixed_interval":"600s","min_doc_count":0,"extended_bounds":{"min":%[1]d,"max":%[2]d}}},`+
				`"split":{"terms":{"field":"service.name","size":10},"aggs":{"over_time":{"date_histogram":{"field":"@timestamp",`+
				`"fixed_interval":"600s","min_doc_count":0,"extended_bounds":{"min":%[1]d,"max":%[2]d}}}}}}}`,
				hourStart, hourStart+1800000-1)},
			want: `{"index":"logs-*","total":12,"intervalSeconds":600,"start":"2026-05-04T09:00:00Z","counts":[4,8],"splitBy":"service.name",
				"values":[{"value":"checkout","total":9,"start":"2026-05-04T09:00:00Z","counts":[1,8]},
					{"value":"cart","total":0,"counts":[]}],"other":3}`,
			wantEvidence: []source.Evidence{{
				Kind:  source.EvidenceLogQuery,
				Title: "{product} search of logs-* from 2026-05-04T09:00:00.000Z to 2026-05-04T09:30:00.000Z",
				URL: "https://kibana.example.com/app/discover#/?_g=(time:(from:'2026-05-04T09%3A00%3A00.000Z',to:'2026-05-04T09%3A30%3A00.000Z'))" +
					"&_a=(index:'logs-%2A',query:(language:lucene,query:''))",
				At: testNow.Add(-30 * time.Minute),
			}},
		},
		{
			name:       "count_logs with no logs is no data",
			tool:       toolCountLogs,
			input:      `{"query":"level:error","since":"2026-05-04T09:00:00Z"}`,
			routes:     []route{{http.MethodPost, searchPath, []fakehttp.Response{body(`{"hits":{"total":{"value":0,"relation":"eq"},"hits":[]}}`)}}},
			wantNoData: true,
			wantText:   "No logs in logs-* matched between 2026-05-04T09:00:00.000Z and 2026-05-04T10:00:00.000Z.",
			wantEvidence: []source.Evidence{{
				Kind:  source.EvidenceLogQuery,
				Title: `{product} search of logs-* for "level:error" from 2026-05-04T09:00:00.000Z to 2026-05-04T10:00:00.000Z`,
				URL:   lastHourLink,
				At:    testNow,
			}},
		},
		{
			name:  "count_logs split by a text field is a tool error",
			tool:  toolCountLogs,
			input: `{"since":"2026-05-04T09:00:00Z","splitBy":"message"}`,
			routes: []route{{http.MethodPost, searchPath, []fakehttp.Response{failed(http.StatusBadRequest, "illegal_argument_exception",
				"Fielddata is disabled on [message] in [logs-a]")}}},
			wantToolError: "{product} answered 400 illegal_argument_exception: Fielddata is disabled on [message] in [logs-a]",
		},
		{
			name:  "list_fields lists the fields with their types, without metadata and object fields",
			tool:  toolListFields,
			input: `{}`,
			routes: []route{{http.MethodGet, fieldCapsPath, []fakehttp.Response{body(`{"indices":["logs-2026.05.04"],"fields":{
				"_id":{"_id":{"type":"_id","searchable":true,"aggregatable":false}},
				"service":{"object":{"type":"object","searchable":false,"aggregatable":false}},
				"service.name":{"keyword":{"type":"keyword","searchable":true,"aggregatable":true}},
				"message":{"text":{"type":"text","searchable":true,"aggregatable":false}},
				"status":{"long":{"type":"long","searchable":true,"aggregatable":true},"keyword":{"type":"keyword","searchable":true,"aggregatable":true}}
			}}`)}}},
			wantQueries: []string{"fields=%2A"},
			want: `{"index":"logs-*","fields":[
				{"name":"message","types":["text"],"searchable":true,"aggregatable":false},
				{"name":"service.name","types":["keyword"],"searchable":true,"aggregatable":true},
				{"name":"status","types":["keyword","long"],"searchable":true,"aggregatable":true}]}`,
		},
		{
			name:        "list_fields with no matching field is no data",
			patterns:    []string{"logs-*", "audit-*"},
			tool:        toolListFields,
			input:       `{"index":"audit-*","fields":"kubernetes.*"}`,
			routes:      []route{{http.MethodGet, "/audit-*/_field_caps", []fakehttp.Response{body(`{"indices":[],"fields":{}}`)}}},
			wantQueries: []string{"fields=kubernetes.%2A"},
			wantNoData:  true,
			wantText:    "No fields in audit-* match kubernetes.*: the pattern matches no index, or no index has such a field.",
		},
		{
			name:  "list_fields of an index that does not exist is no data",
			tool:  toolListFields,
			input: `{"index":"logs-2026.05.04"}`,
			routes: []route{{http.MethodGet, "/logs-2026.05.04/_field_caps", []fakehttp.Response{failed(http.StatusNotFound,
				"index_not_found_exception", "no such index [logs-2026.05.04]")}}},
			wantQueries: []string{"fields=%2A"},
			wantNoData:  true,
			wantText:    "Not found: {product} answered 404 index_not_found_exception: no such index [logs-2026.05.04]",
		},
	}

	for _, tt := range tests {
		for _, flavor := range flavors {
			t.Run(flavor+"/"+tt.name, func(t *testing.T) {
				t.Parallel()

				name := product(flavor)
				api := fakehttp.New(t)

				for _, r := range tt.routes {
					api.Handle(r.method, r.path, r.responses...)
				}

				api.ExpectAllCalled()

				s := newTestSource(t, api, testConfig(api, flavor, tt.patterns...))

				res, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(tt.input)})

				for i, req := range api.Requests() {
					require.Equal(t, wantAuthorization(flavor), req.Header.Get("Authorization"))

					if i < len(tt.wantBodies) {
						require.JSONEq(t, tt.wantBodies[i], string(req.Body))
					}

					if i < len(tt.wantQueries) {
						require.Equal(t, tt.wantQueries[i], req.Query.Encode())
					}
				}

				switch {
				case tt.wantToolError != "":
					toolErr, ok := errors.AsType[*source.ToolError](err)
					require.True(t, ok, "want a tool error, got %v", err)
					require.Equal(t, withProduct(tt.wantToolError, name), toolErr.Message)

					return
				case tt.wantErr != "":
					require.ErrorContains(t, err, withProduct(tt.wantErr, name))

					_, isToolErr := errors.AsType[*source.ToolError](err)
					require.False(t, isToolErr, "a failure of the source is not a tool error")

					return
				}

				require.NoError(t, err)
				require.Equal(t, tt.wantNoData, res.NoData)
				require.Equal(t, withProduct(tt.wantText, name), res.Text)
				require.Equal(t, tt.wantTruncated, res.Truncated)
				require.Equal(t, evidenceWithProduct(tt.wantEvidence, name), res.Evidence)

				if tt.want != "" {
					require.JSONEq(t, tt.want, string(res.Structured))
				}
			})
		}
	}
}

func TestSourceCallRefuses(t *testing.T) {
	t.Parallel()

	const since = `"since":"2026-05-04T09:00:00Z"`

	tests := []struct {
		name     string
		patterns []string
		tool     string
		input    string
		want     string
	}{
		{name: "an index outside the allowlist", tool: toolSearchLogs, input: `{"index":"secrets",` + since + `}`,
			want: `index "secrets" is not one Sidekick may read`},
		{name: "a pattern wider than the allowlist", tool: toolSearchLogs, input: `{"index":"log*",` + since + `}`,
			want: `index "log*" is not one Sidekick may read`},
		{name: "a list of indexes", tool: toolSearchLogs, input: `{"index":"logs-a,secrets",` + since + `}`,
			want: `index "logs-a,secrets" is not one Sidekick may read`},
		{name: "a remote cluster", tool: toolCountLogs, input: `{"index":"other:logs-a",` + since + `}`,
			want: `index "other:logs-a" is not one Sidekick may read`},
		{name: "every index", tool: toolListFields, input: `{"index":"_all"}`, want: `index "_all" is not one Sidekick may read`},
		{name: "an index that climbs out of the path", patterns: []string{".*"}, tool: toolListFields, input: `{"index":".."}`,
			want: `index ".." is not one Sidekick may read`},
		{name: "no index with two patterns", patterns: []string{"logs-*", "audit-*"}, tool: toolSearchLogs, input: `{` + since + `}`,
			want: "give index; allowed: logs-*, audit-*"},
		{name: "an _index clause outside the allowlist", tool: toolSearchLogs, input: `{"query":"level:error OR _index:secrets",` + since + `}`,
			want: "query: _index:secrets is not an index Sidekick may read; allowed: logs-*"},
		{name: "an escaped _index clause", tool: toolCountLogs, input: `{"query":"\\_index:secrets",` + since + `}`,
			want: "query: _index:secrets is not an index Sidekick may read"},
		{name: "an _index clause through a wildcard field", tool: toolSearchLogs, input: `{"query":"(_ind*: \"secrets\")",` + since + `}`,
			want: "query: _index:secrets is not an index Sidekick may read"},
		{name: "an _index clause with a group", tool: toolSearchLogs, input: `{"query":"_index:(logs-a OR secrets)",` + since + `}`,
			want: "query: give _index one index name or pattern"},
		{name: "an _index clause with a range", tool: toolSearchLogs, input: `{"query":"_index:[a TO z]",` + since + `}`,
			want: "query: give _index one index name or pattern"},
		{name: "a query that is too long", tool: toolSearchLogs, input: `{"query":"` + strings.Repeat("a", maxQueryLen+1) + `",` + since + `}`,
			want: "longer than the 4096 one call may send"},
		{name: "no since", tool: toolSearchLogs, input: `{"query":"level:error"}`, want: "since: required"},
		{name: "a range longer than seven days", tool: toolCountLogs, input: `{"since":"2026-04-01T00:00:00Z"}`,
			want: "longer than the 168h0m0s one call may read"},
		{name: "since after until", tool: toolSearchLogs, input: `{"since":"2026-05-04T11:00:00Z","until":"2026-05-04T10:00:00Z"}`,
			want: "since must be before until"},
		{name: "a time that does not parse", tool: toolCountLogs, input: `{"since":"yesterday"}`,
			want: `since: "yesterday" is not an RFC 3339 time`},
		{name: "a field that is not a name", tool: toolSearchLogs, input: `{"fields":["a b"],` + since + `}`,
			want: `"a b" is not a field name`},
		{name: "too many fields", tool: toolSearchLogs, input: `{"fields":[` + strings.Repeat(`"a",`, maxFields) + `"b"],` + since + `}`,
			want: "fields: give at most 50 fields"},
		{name: "a split field with a wildcard", tool: toolCountLogs, input: `{"splitBy":"service.*",` + since + `}`,
			want: `"service.*" is not a field name`},
		{name: "an interval that makes too many buckets", tool: toolCountLogs, input: `{"interval":10,` + since + `}`,
			want: "interval: 10s gives 360 buckets, more than 200; use at least 18s"},
		{name: "a field pattern that is not a name", tool: toolListFields, input: `{"fields":"a,b"}`, want: `"a,b" is not a field name`},
		{name: "input that does not decode", tool: toolSearchLogs, input: `{"limit":"ten"}`, want: "the input does not decode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			s := newTestSource(t, api, testConfig(api, FlavorElasticsearch, tt.patterns...))

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(tt.input)})

			toolErr, ok := errors.AsType[*source.ToolError](err)
			require.True(t, ok, "want a tool error, got %v", err)
			require.Contains(t, toolErr.Message, tt.want)
			require.Empty(t, api.Requests(), "the call never reaches the server")
		})
	}
}

func TestSourceCallCutsLongResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		hits    int
		message int
	}{
		{name: "a long message is cut", hits: 1, message: maxMessageLen + 100},
		{name: "a result stops before it grows too large", hits: maxHits, message: maxMessageLen * 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hits := make([]map[string]any, tt.hits)
			for i := range hits {
				hits[i] = map[string]any{
					"_index": "logs-a",
					"_id":    fmt.Sprint(i),
					"_source": map[string]any{
						"message": strings.Repeat("é", tt.message/2),
						"error":   map[string]any{"stack_trace": []any{strings.Repeat("x", tt.message)}},
					},
				}
			}

			data, err := json.Marshal(map[string]any{"hits": map[string]any{"total": map[string]any{"value": tt.hits}, "hits": hits}})
			require.NoError(t, err)

			api := fakehttp.New(t)
			api.Handle(http.MethodPost, searchPath, fakehttp.Response{Headers: jsonHeaders, Body: data})

			s := newTestSource(t, api, testConfig(api, FlavorElasticsearch))

			res, err := s.Call(t.Context(), source.Call{Tool: toolSearchLogs, Input: json.RawMessage(`{"since":"2026-05-04T09:00:00Z","limit":200}`)})
			require.NoError(t, err)
			require.True(t, res.Truncated)
			// The budget counts the hits; the commas between them and the
			// fields around them come on top.
			require.LessOrEqual(t, len(res.Structured), maxResultSize+tt.hits+len(`{"index":"logs-*","total":200,"hits":[]}`))

			var got searchResult
			require.NoError(t, json.Unmarshal(res.Structured, &got))
			require.NotEmpty(t, got.Hits)

			for _, h := range got.Hits {
				message, ok := h.Fields["message"].(string)
				require.True(t, ok)
				require.LessOrEqual(t, len(message), maxMessageLen)

				trace, ok := h.Fields["error.stack_trace"].([]any)
				require.True(t, ok)
				require.Len(t, trace, 1)
				require.Len(t, trace[0], maxMessageLen)
			}
		})
	}
}

func TestSourceCredentialFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		flavor  string
		secret  []byte
		err     error
		wantErr string
	}{
		{name: "an API key that cannot be read", flavor: FlavorElasticsearch, err: errors.New("secret not found"),
			wantErr: "read the API key: resolve secret sidekick-elasticsearch/apiKey: secret not found"},
		{name: "an empty password", flavor: FlavorOpenSearch, secret: []byte(" \n"),
			wantErr: "read the password: secret sidekick-opensearch/password: the secret is empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			secrets := registry.NewMockSecrets(gomock.NewController(t))
			secrets.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(tt.secret, tt.err)

			src, err := New(t.Context(), "logs", testConfig(api, tt.flavor), registry.Deps{HTTP: api.Client(), Secrets: secrets})
			require.NoError(t, err)

			_, err = src.Call(t.Context(), source.Call{Tool: toolListFields, Input: json.RawMessage(`{}`)})
			require.ErrorContains(t, err, tt.wantErr)

			_, isToolErr := errors.AsType[*source.ToolError](err)
			require.False(t, isToolErr)
			require.Empty(t, api.Requests(), "no request is sent without credentials")
		})
	}
}

func TestSourceTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "a server slower than the call's deadline fails the call"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, fieldCapsPath, fakehttp.Response{Delay: 5 * time.Second, Headers: jsonHeaders, Body: []byte(`{}`)})

			s := newTestSource(t, api, testConfig(api, FlavorElasticsearch))

			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()

			_, err := s.Call(ctx, source.Call{Tool: toolListFields, Input: json.RawMessage(`{}`)})
			require.ErrorIs(t, err, context.DeadlineExceeded)
		})
	}
}

func TestSourceUnknownTool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tool string
	}{
		{name: "a tool that writes", tool: "delete_by_query"},
		{name: "an empty name", tool: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			s := newTestSource(t, api, testConfig(api, FlavorElasticsearch))

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(`{}`)})
			require.ErrorIs(t, err, errUnknownTool)
			require.NoError(t, s.Close())
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cfg         func(api *fakehttp.Server) Config
		withSecrets bool
		wantErr     string
	}{
		{name: "no secrets resolver", cfg: func(api *fakehttp.Server) Config { return testConfig(api, FlavorElasticsearch) },
			wantErr: errNoSecrets.Error()},
		{name: "settings that do not check", withSecrets: true, cfg: func(api *fakehttp.Server) Config {
			cfg := testConfig(api, FlavorOpenSearch)
			cfg.Concurrency = -1

			return cfg
		}, wantErr: "concurrency: must not be negative"},
		{name: "a link template that does not render", withSecrets: true, cfg: func(api *fakehttp.Server) Config {
			cfg := testConfig(api, FlavorElasticsearch)
			cfg.DiscoverURL = "{{.Nope}}"

			return cfg
		}, wantErr: "discoverURL: render the template"},
		{name: "no link template", withSecrets: true, cfg: func(api *fakehttp.Server) Config {
			cfg := testConfig(api, FlavorElasticsearch)
			cfg.DiscoverURL = ""

			return cfg
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)

			var deps registry.Deps
			if tt.withSecrets {
				deps.Secrets = registry.NewMockSecrets(gomock.NewController(t))
			}

			s, err := New(t.Context(), "logs", tt.cfg(api), deps)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)

			tools, err := s.Tools(t.Context())
			require.NoError(t, err)
			require.Len(t, tools, len(toolList))
			require.NoError(t, s.Close())
		})
	}
}

func TestEvidenceWithoutTemplate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query string
		want  string
	}{
		{name: "a search with a query", query: `msg:"it's down!"`,
			want: `Elasticsearch search of logs-* for "msg:\"it's down!\"" from 2026-05-04T09:00:00.000Z to 2026-05-04T10:00:00.000Z`},
		{name: "a search without a query", want: "Elasticsearch search of logs-* from 2026-05-04T09:00:00.000Z to 2026-05-04T10:00:00.000Z"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := &Source{cfg: Config{}}

			got := s.evidence("logs-*", tt.query, window{start: testNow.Add(-time.Hour), end: testNow})
			require.Equal(t, []source.Evidence{{Kind: source.EvidenceLogQuery, Title: tt.want, At: testNow}}, got)
		})
	}
}

func TestEscapeRison(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text", in: "level:error", want: "level%3Aerror"},
		{name: "quotes, bangs and spaces", in: "it's down! a+b", want: "it%21%27s%20down%21%21%20a%2Bb"},
		{name: "an empty string", in: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, escapeRison(tt.in))
		})
	}
}

// withProduct returns text with the product placeholder replaced.
func withProduct(text, name string) string {
	return strings.ReplaceAll(text, productPlaceholder, name)
}

// evidenceWithProduct returns a copy of evidence with the product
// placeholder in each title replaced.
func evidenceWithProduct(evidence []source.Evidence, name string) []source.Evidence {
	if evidence == nil {
		return nil
	}

	out := make([]source.Evidence, len(evidence))
	for i, e := range evidence {
		e.Title = withProduct(e.Title, name)
		out[i] = e
	}

	return out
}
