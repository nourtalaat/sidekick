package prometheus

import (
	"net/http"
	"testing"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Paths of the discovery APIs.
const (
	seriesPath  = "/api/v1/series"
	targetsPath = "/api/v1/targets"
)

func TestListSeries(t *testing.T) {
	t.Parallel()

	const match = `up{job="api"}`

	runCalls(t, []callCase{
		{
			name:  "series past the limit are left out and the result says so",
			tool:  toolListSeries,
			input: `{"match":"up{job=\"api\"}","limit":2}`,
			routes: []route{{http.MethodGet, seriesPath, []fakehttp.Response{success(`[
				{"__name__":"up","job":"api","instance":"a:9090"},
				{"__name__":"up","job":"api","instance":"b:9090"},
				{"__name__":"up","job":"api","instance":"c:9090"}]`)}}},
			wantQueries: []string{"end=2026-05-04T10%3A00%3A00Z&limit=3&match%5B%5D=up%7Bjob%3D%22api%22%7D&start=2026-05-04T09%3A00%3A00Z"},
			want: `{"series":[{"__name__":"up","job":"api","instance":"a:9090"},
				{"__name__":"up","job":"api","instance":"b:9090"}]}`,
			wantEvidence:  []source.Evidence{graphLink("Series "+match, match, "1h", testNow)},
			wantTruncated: true,
		},
		{
			name:         "no series is no data",
			tool:         toolListSeries,
			input:        `{"match":"up"}`,
			routes:       []route{{http.MethodGet, seriesPath, []fakehttp.Response{success(`[]`)}}},
			wantNoData:   true,
			wantText:     "No series match up between 2026-05-04T09:00:00Z and 2026-05-04T10:00:00Z.",
			wantEvidence: []source.Evidence{graphLink("Series up", "up", "1h", testNow)},
		},
		{name: "a selector is required", tool: toolListSeries, input: `{}`, wantToolError: "match: required"},
		{name: "a selector that does not parse is never sent", tool: toolListSeries, input: `{"match":"up{"}`, wantToolError: "match: "},
		{name: "a query is not a selector", tool: toolListSeries, input: `{"match":"sum(up)"}`, wantToolError: "match: "},
	})
}

func TestListLabelValues(t *testing.T) {
	t.Parallel()

	runCalls(t, []callCase{
		{
			name:        "the values of a label on the series that match",
			tool:        toolListLabelValues,
			input:       `{"label":"service","match":"up"}`,
			routes:      []route{{http.MethodGet, "/api/v1/label/service/values", []fakehttp.Response{success(`["cart","checkout"]`)}}},
			wantQueries: []string{"end=2026-05-04T10%3A00%3A00Z&limit=101&match%5B%5D=up&start=2026-05-04T09%3A00%3A00Z"},
			want:        `{"label":"service","values":["cart","checkout"]}`,
		},
		{
			name:          "metric names past the limit are left out",
			tool:          toolListLabelValues,
			input:         `{"label":"__name__","limit":1}`,
			routes:        []route{{http.MethodGet, "/api/v1/label/__name__/values", []fakehttp.Response{success(`["go_goroutines","up"]`)}}},
			wantQueries:   []string{"end=2026-05-04T10%3A00%3A00Z&limit=2&start=2026-05-04T09%3A00%3A00Z"},
			want:          `{"label":"__name__","values":["go_goroutines"]}`,
			wantTruncated: true,
		},
		{
			name:       "no values is no data",
			tool:       toolListLabelValues,
			input:      `{"label":"service"}`,
			routes:     []route{{http.MethodGet, "/api/v1/label/service/values", []fakehttp.Response{success(`[]`)}}},
			wantNoData: true,
			wantText:   "The label service has no values between 2026-05-04T09:00:00Z and 2026-05-04T10:00:00Z.",
		},
		{name: "a label name that climbs out of the path", tool: toolListLabelValues, input: `{"label":"../query"}`,
			wantToolError: `label: "../query" is not a label name`},
		{name: "a selector that does not parse", tool: toolListLabelValues, input: `{"label":"service","match":"{"}`, wantToolError: "match: "},
	})
}

func TestListTargets(t *testing.T) {
	t.Parallel()

	targets := success(`{"activeTargets":[
		{"labels":{"job":"api","instance":"api:8080"},"scrapePool":"api","scrapeUrl":"http://api:8080/metrics","health":"up",
		 "lastError":"","lastScrape":"2026-05-04T09:59:50Z"},
		{"labels":{"job":"db","instance":"db:9187"},"scrapePool":"db","scrapeUrl":"http://db:9187/metrics","health":"down",
		 "lastError":"connection refused","lastScrape":"2026-05-04T09:59:45Z"},
		{"labels":{"job":"cache"},"scrapePool":"cache","scrapeUrl":"http://cache:9121/metrics","health":"unknown"}],"droppedTargets":[]}`)
	targetsLink := source.Evidence{Kind: source.EvidenceResource, Title: "Targets in Prometheus", URL: testWebURL + "/targets"}

	runCalls(t, []callCase{
		{
			name:        "only the targets that are not up, by pool",
			tool:        toolListTargets,
			input:       `{}`,
			routes:      []route{{http.MethodGet, targetsPath, []fakehttp.Response{targets}}},
			wantQueries: []string{"state=active"},
			want: `{"active":3,"unhealthy":2,"targets":[
				{"pool":"cache","url":"http://cache:9121/metrics","health":"unknown","labels":{"job":"cache"}},
				{"pool":"db","url":"http://db:9187/metrics","health":"down","lastError":"connection refused",
				 "lastScrape":"2026-05-04T09:59:45Z","labels":{"job":"db","instance":"db:9187"}}]}`,
			wantEvidence: []source.Evidence{targetsLink},
		},
		{
			name:        "a pool is asked for, and kept even when the server sends every pool",
			tool:        toolListTargets,
			input:       `{"pool":"db"}`,
			routes:      []route{{http.MethodGet, targetsPath, []fakehttp.Response{targets}}},
			wantQueries: []string{"scrapePool=db&state=active"},
			want: `{"active":1,"unhealthy":1,"targets":[{"pool":"db","url":"http://db:9187/metrics","health":"down",
				"lastError":"connection refused","lastScrape":"2026-05-04T09:59:45Z","labels":{"job":"db","instance":"db:9187"}}]}`,
			wantEvidence: []source.Evidence{targetsLink},
		},
		{
			name:         "every target up is no data",
			tool:         toolListTargets,
			input:        `{"pool":"api"}`,
			routes:       []route{{http.MethodGet, targetsPath, []fakehttp.Response{targets}}},
			wantNoData:   true,
			wantText:     "All 1 active targets are up.",
			wantEvidence: []source.Evidence{targetsLink},
		},
		{
			name:       "a server without the targets API, such as Mimir, is no data",
			tool:       toolListTargets,
			input:      `{}`,
			routes:     []route{{http.MethodGet, targetsPath, []fakehttp.Response{{Status: http.StatusNotFound, Body: []byte("404 page not found\n")}}}},
			wantNoData: true,
			wantText:   notFoundText,
		},
	})
}
