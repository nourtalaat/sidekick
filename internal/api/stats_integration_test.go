//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/api"
)

// statsNow is the clock of the servers of the stats tests, which
// fixture/stats.sql is written for.
var statsNow = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// setupStats returns a server with the clock at statsNow and the rollup
// rows of fixture/stats.sql.
func setupStats(t *testing.T) server {
	t.Helper()

	return setupWith(t, func(o *api.Options) { o.Now = func() time.Time { return statsNow } }, "fixture/stats.sql")
}

// getStats sends a GET to path as the viewer and returns the answer.
func getStats(t *testing.T, s server, path string) *httptest.ResponseRecorder {
	t.Helper()

	return s.do(t, http.MethodGet, "/api/v1"+path, viSession, viCSRF, nil)
}

// decodeStats decodes a 200 answer into v.
func decodeStats(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), v))
}

// statsRefusals are the requests every stats endpoint refuses the same way.
var statsRefusals = []struct {
	name       string
	query      string
	wantStatus int
	wantDetail string
}{
	{name: "from after to", query: "?from=2026-10-10&to=2026-10-01", wantStatus: http.StatusBadRequest, wantDetail: "from must not be after to."},
	{name: "a range of 401 days", query: "?from=2025-09-23&to=2026-10-28", wantStatus: http.StatusBadRequest, wantDetail: "The range must be at most 400 days."},
	{name: "a date that is not a date", query: "?from=yesterday", wantStatus: http.StatusBadRequest},
	{
		name: "a verdict that is not a verdict", query: "?verdict=maybe", wantStatus: http.StatusBadRequest,
		wantDetail: "verdict must be one of true_positive, false_positive, inconclusive, duplicate, known_issue, flapping.",
	},
}

// requireRefusals checks that the endpoint at path answers each of
// statsRefusals with its status and, when there is one, its detail.
func requireRefusals(t *testing.T, s server, path string) {
	t.Helper()

	for _, tt := range statsRefusals {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := getStats(t, s, path+tt.query)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))

			if tt.wantDetail != "" {
				require.JSONEq(t, problem(tt.wantStatus, tt.wantDetail, "/api/v1"+path), rec.Body.String())
			}
		})
	}
}

// qualityBody is the response of GET /stats/quality.
type qualityBody struct {
	From     string        `json:"from"`
	To       string        `json:"to"`
	Totals   qualityTotals `json:"totals"`
	Previous qualityTotals `json:"previous"`
	Days     []struct {
		Day     string `json:"day"`
		Rated   int64  `json:"rated"`
		Correct int64  `json:"correct"`
	} `json:"days"`
	Matrix []struct {
		Predicted string `json:"predicted"`
		Confirmed string `json:"confirmed"`
		N         int64  `json:"n"`
	} `json:"matrix"`
	PerVerdict []struct {
		Verdict   string   `json:"verdict"`
		Predicted int64    `json:"predicted"`
		Actual    int64    `json:"actual"`
		Hits      int64    `json:"hits"`
		Precision *float64 `json:"precision"`
		Recall    *float64 `json:"recall"`
	} `json:"perVerdict"`
	Comparison []struct {
		Model    string `json:"model"`
		Strategy string `json:"strategy"`
		Rated    int64  `json:"rated"`
		Correct  int64  `json:"correct"`
	} `json:"comparison"`
}

type qualityTotals struct {
	Rated    int64    `json:"rated"`
	Correct  int64    `json:"correct"`
	Accuracy *float64 `json:"accuracy"`
}

func TestGetQualityStats(t *testing.T) {
	t.Parallel()

	s := setupStats(t)

	t.Run("the default range", func(t *testing.T) {
		t.Parallel()

		var got qualityBody

		decodeStats(t, getStats(t, s, "/stats/quality"), &got)

		require.Equal(t, "2026-09-29", got.From)
		require.Equal(t, "2026-10-28", got.To)

		require.Equal(t, int64(17), got.Totals.Rated)
		require.Equal(t, int64(13), got.Totals.Correct)
		require.InDelta(t, 13.0/17.0, *got.Totals.Accuracy, 1e-9)

		require.Equal(t, int64(4), got.Previous.Rated)
		require.Equal(t, int64(3), got.Previous.Correct)
		require.InDelta(t, 0.75, *got.Previous.Accuracy, 1e-9)

		require.Len(t, got.Days, 2)
		require.Equal(t, "2026-10-20", got.Days[0].Day)
		require.Equal(t, int64(13), got.Days[0].Rated)
		require.Equal(t, int64(11), got.Days[0].Correct)
		require.Equal(t, "2026-10-21", got.Days[1].Day)
		require.Equal(t, int64(4), got.Days[1].Rated)
		require.Equal(t, int64(2), got.Days[1].Correct)

		type cell struct {
			predicted, confirmed string
			n                    int64
		}

		var cells []cell
		for _, c := range got.Matrix {
			cells = append(cells, cell{c.Predicted, c.Confirmed, c.N})
		}

		require.Equal(t, []cell{
			{"false_positive", "false_positive", 8},
			{"false_positive", "true_positive", 2},
			{"false_positive", "unknown", 1},
			{"true_positive", "false_positive", 1},
			{"true_positive", "true_positive", 5},
		}, cells)

		// Unknown counts for neither precision nor recall, and verdicts
		// come in the order of a report.
		require.Len(t, got.PerVerdict, 2)

		tp, fp := got.PerVerdict[0], got.PerVerdict[1]
		require.Equal(t, "true_positive", tp.Verdict)
		require.Equal(t, [3]int64{6, 7, 5}, [3]int64{tp.Predicted, tp.Actual, tp.Hits})
		require.InDelta(t, 5.0/6.0, *tp.Precision, 1e-9)
		require.InDelta(t, 5.0/7.0, *tp.Recall, 1e-9)
		require.Equal(t, "false_positive", fp.Verdict)
		require.Equal(t, [3]int64{10, 9, 8}, [3]int64{fp.Predicted, fp.Actual, fp.Hits})
		require.InDelta(t, 0.8, *fp.Precision, 1e-9)
		require.InDelta(t, 8.0/9.0, *fp.Recall, 1e-9)

		require.Len(t, got.Comparison, 2)
		require.Equal(t, "m1", got.Comparison[0].Model)
		require.Equal(t, "default", got.Comparison[0].Strategy)
		require.Equal(t, int64(14), got.Comparison[0].Rated)
		require.Equal(t, int64(11), got.Comparison[0].Correct)
		require.Equal(t, "m2", got.Comparison[1].Model)
		require.Equal(t, "deep", got.Comparison[1].Strategy)
		require.Equal(t, int64(3), got.Comparison[1].Rated)
		require.Equal(t, int64(2), got.Comparison[1].Correct)
	})

	filters := []struct {
		name         string
		query        string
		wantRated    int64
		wantCorrect  int64
		wantPrevious int64
	}{
		{name: "a route", query: "?route=r2", wantRated: 3, wantCorrect: 2},
		{name: "a service", query: "?service=checkout", wantRated: 14, wantCorrect: 11, wantPrevious: 4},
		{name: "a model", query: "?model=m2", wantRated: 3, wantCorrect: 2},
		{name: "the verdict Sidekick gave", query: "?verdict=false_positive", wantRated: 11, wantCorrect: 8, wantPrevious: 3},
		{name: "one day, with the day before as the range before", query: "?from=2026-10-21&to=2026-10-21", wantRated: 4, wantCorrect: 2, wantPrevious: 13},
		{name: "filters together", query: "?route=r1&verdict=true_positive", wantRated: 3, wantCorrect: 3, wantPrevious: 1},
		{name: "nothing matches", query: "?route=nowhere"},
	}

	for _, tt := range filters {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got qualityBody

			decodeStats(t, getStats(t, s, "/stats/quality"+tt.query), &got)

			require.Equal(t, tt.wantRated, got.Totals.Rated)
			require.Equal(t, tt.wantCorrect, got.Totals.Correct)
			require.Equal(t, tt.wantPrevious, got.Previous.Rated)

			if tt.wantRated == 0 {
				require.Nil(t, got.Totals.Accuracy)
				require.Empty(t, got.Days)
				require.Empty(t, got.Matrix)
			}
		})
	}

	t.Run("an empty range has empty lists, not null", func(t *testing.T) {
		t.Parallel()

		rec := getStats(t, s, "/stats/quality?route=nowhere")
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), `"days":[]`)
		require.Contains(t, rec.Body.String(), `"matrix":[]`)
		require.Contains(t, rec.Body.String(), `"perVerdict":[]`)
		require.Contains(t, rec.Body.String(), `"comparison":[]`)
	})

	t.Run("no session", func(t *testing.T) {
		t.Parallel()

		rec := s.do(t, http.MethodGet, "/api/v1/stats/quality", "", "", nil)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	requireRefusals(t, s, "/stats/quality")
}

func TestGetStatsFilters(t *testing.T) {
	t.Parallel()

	s := setupStats(t)

	var got struct {
		Routes   []string `json:"routes"`
		Services []string `json:"services"`
		Models   []string `json:"models"`
	}

	decodeStats(t, getStats(t, s, "/stats/filters"), &got)

	// The empty service and model of a running investigation, and the
	// route, service and model of rows older than 400 days, are left out.
	require.Equal(t, []string{"r1", "r2"}, got.Routes)
	require.Equal(t, []string{"checkout", "payments"}, got.Services)
	require.Equal(t, []string{"m1", "m2"}, got.Models)
}
