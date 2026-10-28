package prometheus

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/source"
)

// resultOverhead is more than the bytes a query result holds besides its
// series.
const resultOverhead = 1 << 10

// Paths of the Prometheus query API.
const (
	queryPath      = "/api/v1/query"
	queryRangePath = "/api/v1/query_range"
)

func TestQuery(t *testing.T) {
	t.Parallel()

	const upQuery = `up{job="api"}`

	runCalls(t, []callCase{
		{
			name:  "an instant query runs now and returns each series' value",
			tool:  toolQuery,
			input: `{"query":"up{job=\"api\"}"}`,
			routes: []route{{http.MethodGet, queryPath, []fakehttp.Response{success(`{"resultType":"vector","result":[
				{"metric":{"__name__":"up","job":"api","instance":"a:9090"},"value":[1777888800,"1"]},
				{"metric":{"__name__":"up","job":"api","instance":"b:9090"},"value":[1777888800,"NaN"]}]}`)}}},
			wantQueries: []string{"query=up%7Bjob%3D%22api%22%7D&time=2026-05-04T10%3A00%3A00Z"},
			want: `{"resultType":"vector","time":"2026-05-04T10:00:00Z","seriesTotal":2,"series":[
				{"labels":{"__name__":"up","job":"api","instance":"a:9090"},"value":1},
				{"labels":{"__name__":"up","job":"api","instance":"b:9090"},"value":"NaN"}]}`,
			wantEvidence: []source.Evidence{tableLink("PromQL query", upQuery, testNow)},
		},
		{
			name:  "a scalar at a given time, with the server's warnings and infos",
			tool:  toolQuery,
			input: `{"query":"scalar(sum(up))","time":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodGet, queryPath, []fakehttp.Response{body(`{"status":"success",
				"data":{"resultType":"scalar","result":[1777885200,"12"]},"warnings":["partial response"],"infos":["metric might not be a counter"]}`)}}},
			wantQueries: []string{"query=scalar%28sum%28up%29%29&time=2026-05-04T09%3A00%3A00Z"},
			want: `{"resultType":"scalar","time":"2026-05-04T09:00:00Z","value":12,
				"warnings":["partial response"],"infos":["metric might not be a counter"]}`,
			wantEvidence: []source.Evidence{tableLink("PromQL query", "scalar(sum(up))", lastHour.start)},
		},
		{
			name:  "a range selector returns each series' points",
			tool:  toolQuery,
			input: `{"query":"up[1m]"}`,
			routes: []route{{http.MethodGet, queryPath, []fakehttp.Response{success(`{"resultType":"matrix","result":[
				{"metric":{"job":"api"},"values":[[1777888740.123,"1"],[1777888770,"0"]]}]}`)}}},
			want: `{"resultType":"matrix","time":"2026-05-04T10:00:00Z","seriesTotal":1,"series":[
				{"labels":{"job":"api"},"points":[[1777888740.123,1],[1777888770,0]]}]}`,
			wantEvidence: []source.Evidence{tableLink("PromQL query", "up[1m]", testNow)},
		},
		{
			name:         "an instant query with no series is no data with a link to the query",
			tool:         toolQuery,
			input:        `{"query":"up{job=\"api\"}"}`,
			routes:       []route{{http.MethodGet, queryPath, []fakehttp.Response{success(`{"resultType":"vector","result":[]}`)}}},
			wantNoData:   true,
			wantText:     "The query returned no series at 2026-05-04T10:00:00Z.",
			wantEvidence: []source.Evidence{tableLink("PromQL query", upQuery, testNow)},
		},
		{
			name:  "a query that fails while it runs is a tool error",
			tool:  toolQuery,
			input: `{"query":"sum(rate(http_requests_total[30d]))"}`,
			routes: []route{{http.MethodGet, queryPath, []fakehttp.Response{
				failed(http.StatusUnprocessableEntity, "execution", "query processing would load too many samples into memory"),
			}}},
			wantToolError: "Prometheus answered 422 execution: query processing would load too many samples into memory",
		},
		{
			name:          "an error answer with status 200 is a tool error",
			tool:          toolQuery,
			input:         `{"query":"up"}`,
			routes:        []route{{http.MethodGet, queryPath, []fakehttp.Response{body(`{"status":"error","errorType":"bad_data","error":"bad"}`)}}},
			wantToolError: "the server answered with an error: bad_data: bad",
		},
		{name: "a query that does not parse is never sent", tool: toolQuery, input: `{"query":"sum("}`, wantToolError: "query: 1:5: parse error"},
		{name: "a query is required", tool: toolQuery, input: `{"query":" "}`, wantToolError: "query: required"},
		{name: "a time that does not parse", tool: toolQuery, input: `{"query":"up","time":"now"}`,
			wantToolError: `time: "now" is not an RFC 3339 time`},
		{name: "input that does not decode", tool: toolQuery, input: `{"query":1}`, wantToolError: "the input does not decode"},
	})
}

func TestQueryRange(t *testing.T) {
	t.Parallel()

	const errorsQuery = `sum by (service) (rate(http_requests_total{code=~"5.."}[5m]))`

	runCalls(t, []callCase{
		{
			name:  "a range query over the last hour picks a 15s step and sums up each series",
			tool:  toolQueryRange,
			input: `{"query":` + mustJSON(t, errorsQuery) + `}`,
			routes: []route{{http.MethodGet, queryRangePath, []fakehttp.Response{success(`{"resultType":"matrix","result":[
				{"metric":{"service":"checkout"},"values":[[1777885200,"1"],[1777885215,"3"],[1777885230,"2"],[1777885245,"+Inf"]]},
				{"metric":{"service":"cart"},"values":[[1777885200,"NaN"]]}]}`)}}},
			wantQueries: []string{"end=2026-05-04T10%3A00%3A00Z&query=" + url.QueryEscape(errorsQuery) + "&start=2026-05-04T09%3A00%3A00Z&step=15s"},
			want: `{"start":"2026-05-04T09:00:00Z","end":"2026-05-04T10:00:00Z","stepSeconds":15,"seriesTotal":2,"series":[
				{"labels":{"service":"checkout"},"stats":{"min":1,"max":3,"avg":2,"last":2},
				 "points":[[1777885200,1],[1777885215,3],[1777885230,2],[1777885245,"+Inf"]]},
				{"labels":{"service":"cart"},"points":[[1777885200,"NaN"]]}]}`,
			wantEvidence: []source.Evidence{graphLink("PromQL range query", errorsQuery, "1h", testNow)},
		},
		{
			name:  "seven days keep within 250 points",
			tool:  toolQueryRange,
			input: `{"query":"up","since":"2026-04-27T10:00:00Z","until":"2026-05-04T10:00:00Z"}`,
			routes: []route{{http.MethodGet, queryRangePath, []fakehttp.Response{success(`{"resultType":"matrix","result":[
				{"metric":{"job":"api"},"values":[[1777284000,"1"]]}]}`)}}},
			wantQueries: []string{"end=2026-05-04T10%3A00%3A00Z&query=up&start=2026-04-27T10%3A00%3A00Z&step=2429s"},
			want: `{"start":"2026-04-27T10:00:00Z","end":"2026-05-04T10:00:00Z","stepSeconds":2429,"seriesTotal":1,"series":[
				{"labels":{"job":"api"},"stats":{"min":1,"max":1,"avg":1,"last":1},"points":[[1777284000,1]]}]}`,
			wantEvidence: []source.Evidence{graphLink("PromQL range query", "up", "7d", testNow)},
		},
		{
			name:         "a range query with no series is no data with a link to the query",
			tool:         toolQueryRange,
			input:        `{"query":"up"}`,
			routes:       []route{{http.MethodGet, queryRangePath, []fakehttp.Response{success(`{"resultType":"matrix","result":[]}`)}}},
			wantNoData:   true,
			wantText:     "The range query returned no series between 2026-05-04T09:00:00Z and 2026-05-04T10:00:00Z.",
			wantEvidence: []source.Evidence{graphLink("PromQL range query", "up", "1h", testNow)},
		},
		{name: "a range vector is refused", tool: toolQueryRange, input: `{"query":"up[5m]"}`,
			wantToolError: "a range query needs an expression that returns an instant vector or a scalar, not a range vector"},
		{name: "a query that does not parse is never sent", tool: toolQueryRange, input: `{"query":"rate(up[5m]"}`, wantToolError: "query: "},
		{name: "since after until", tool: toolQueryRange, input: `{"query":"up","since":"2026-05-04T11:00:00Z","until":"2026-05-04T10:00:00Z"}`,
			wantToolError: "since must be before until"},
		{name: "a range longer than seven days", tool: toolQueryRange, input: `{"query":"up","since":"2026-04-01T00:00:00Z"}`,
			wantToolError: "longer than the 168h0m0s one call may read"},
		{name: "a since that does not parse", tool: toolQueryRange, input: `{"query":"up","since":"yesterday"}`,
			wantToolError: `since: "yesterday" is not an RFC 3339 time`},
	})
}

func TestQueryCutsLongResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		tool       string
		resultType string
		series     int
		points     int
		wantSeries int
		wantPoints int
	}{
		{name: "a range query keeps 50 series", tool: toolQueryRange, resultType: "matrix", series: 60, points: 3, wantSeries: maxSeries, wantPoints: 3},
		{name: "an instant query keeps 50 series", tool: toolQuery, resultType: "vector", series: 51, wantSeries: maxSeries},
		{name: "a range selector keeps the newest 250 points", tool: toolQuery, resultType: "matrix", series: 1, points: 300,
			wantSeries: 1, wantPoints: maxPoints},
		{name: "a range query stops before it grows too large", tool: toolQueryRange, resultType: "matrix", series: maxSeries, points: maxPoints},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := make([]map[string]any, tt.series)
			for i := range result {
				metric := map[string]string{"pod": fmt.Sprintf("checkout-%03d", i), "path": strings.Repeat("p", 200)}

				if tt.resultType == "vector" {
					result[i] = map[string]any{"metric": metric, "value": []any{1777888800, "1"}}

					continue
				}

				values := make([][]any, tt.points)
				for j := range values {
					values[j] = []any{1777885200 + j, fmt.Sprintf("%d.123456789", 1000000+j)}
				}

				result[i] = map[string]any{"metric": metric, "values": values}
			}

			data, err := json.Marshal(map[string]any{"status": "success", "data": map[string]any{"resultType": tt.resultType, "result": result}})
			require.NoError(t, err)

			path := queryPath
			if tt.tool == toolQueryRange {
				path = queryRangePath
			}

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, path, fakehttp.Response{Headers: jsonHeaders, Body: data})

			s := newTestSource(t, api, Config{}, nil)

			res, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(`{"query":"up"}`)})
			require.NoError(t, err)
			require.True(t, res.Truncated)
			// The budget counts the series; the commas between them and the
			// fields around them come on top.
			require.LessOrEqual(t, len(res.Structured), maxResultSize+resultOverhead)

			var got struct {
				SeriesTotal int `json:"seriesTotal"`
				Series      []struct {
					Points []point `json:"points"`
				} `json:"series"`
			}

			require.NoError(t, json.Unmarshal(res.Structured, &got))
			require.Equal(t, tt.series, got.SeriesTotal)

			if tt.wantSeries == 0 {
				require.Less(t, len(got.Series), tt.series, "the size budget leaves series out")

				return
			}

			require.Len(t, got.Series, tt.wantSeries)

			for _, series := range got.Series {
				require.Len(t, series.Points, tt.wantPoints)
			}

			if tt.wantPoints == maxPoints {
				require.Equal(t, json.Number("1777885250"), series0First(t, res.Structured), "the oldest points are left out")
			}
		})
	}
}

// series0First returns the time of the first point of the first series.
func series0First(t *testing.T, data json.RawMessage) json.Number {
	t.Helper()

	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()

	var got struct {
		Series []struct {
			Points [][2]any `json:"points"`
		} `json:"series"`
	}

	require.NoError(t, dec.Decode(&got))

	n, ok := got.Series[0].Points[0][0].(json.Number)
	require.True(t, ok)

	return n
}

// mustJSON returns s as a JSON string.
func mustJSON(t *testing.T, s string) string {
	t.Helper()

	data, err := json.Marshal(s)
	require.NoError(t, err)

	return string(data)
}
