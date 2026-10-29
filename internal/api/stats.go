package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

const (
	// defaultStatsDays is the length of the range when the request gives
	// no `from`.
	defaultStatsDays = 30
	// maxStatsDays is the longest range a stats request may ask for.
	maxStatsDays = 400
	// statsFilterDays is how far back GET /stats/filters looks.
	statsFilterDays = 400
	// oneDay is the length of a UTC day, which has no daylight saving.
	oneDay = 24 * time.Hour
)

// statsWindow is a range of UTC days, both ends included, as midnight.
type statsWindow struct {
	from, to time.Time
}

// statsFilters are the dimension filters of the stats requests. A nil
// filter does not filter.
type statsFilters struct {
	route, service, model, verdict *string
}

// statsWindowOf checks the range of a request. A missing `to` is today and
// a missing `from` is defaultStatsDays - 1 days before `to`.
func (s *server) statsWindowOf(from, to *openapi_types.Date) (statsWindow, error) {
	end := midnight(s.now())
	if to != nil {
		end = midnight(to.Time)
	}

	start := end.Add(-(defaultStatsDays - 1) * oneDay)
	if from != nil {
		start = midnight(from.Time)
	}

	switch {
	case start.After(end):
		return statsWindow{}, problemf(http.StatusBadRequest, "from must not be after to.")
	case end.Sub(start) >= maxStatsDays*oneDay:
		return statsWindow{}, problemf(http.StatusBadRequest, "The range must be at most %d days.", maxStatsDays)
	}

	return statsWindow{from: start, to: end}, nil
}

// midnight returns the UTC date of t, as the time its day starts.
func midnight(t time.Time) time.Time {
	y, m, d := t.UTC().Date()

	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// days returns how many days the window has.
func (w statsWindow) days() int {
	return int(w.to.Sub(w.from)/oneDay) + 1
}

// before returns the window of the same length that ends the day before
// w starts.
func (w statsWindow) before() statsWindow {
	end := w.from.Add(-oneDay)

	return statsWindow{from: end.Add(-time.Duration(w.days()-1) * oneDay), to: end}
}

// end returns the first moment after the window, for the hourly rows.
func (w statsWindow) end() time.Time {
	return w.to.Add(oneDay)
}

// pgDays returns the first and last day of w as the database types them.
func (w statsWindow) pgDays() (pgtype.Date, pgtype.Date) {
	return pgtype.Date{Time: w.from, Valid: true}, pgtype.Date{Time: w.to, Valid: true}
}

// dateOf is t as the API's date type.
func dateOf(t time.Time) openapi_types.Date {
	return openapi_types.Date{Time: t}
}

// statsFiltersOf takes the filters of a request, leaving out empty ones,
// and refuses a verdict that is not one.
func statsFiltersOf(route, service, model *string, verdict *apigen.Verdict) (statsFilters, error) {
	f := statsFilters{route: nonEmpty(route), service: nonEmpty(service), model: nonEmpty(model)}

	if verdict != nil {
		if !slices.Contains(knownVerdicts, report.Verdict(*verdict)) {
			return statsFilters{}, problemf(http.StatusBadRequest, "verdict must be one of %s.", joinVerdicts())
		}

		f.verdict = new(string(*verdict))
	}

	return f, nil
}

// share returns n over total, or nil when total is 0.
func share(n, total int64) *float64 {
	if total == 0 {
		return nil
	}

	return new(float64(n) / float64(total))
}

// GetStatsFilters lists the values of the dashboard's filter menus.
func (s *server) GetStatsFilters(ctx context.Context, _ apigen.GetStatsFiltersRequestObject) (apigen.GetStatsFiltersResponseObject, error) {
	q := db.New(s.pool)
	since := midnight(s.now()).Add(-statsFilterDays * oneDay)

	routes, err := q.StatsFilterRoutes(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("list the routes: %w", err)
	}

	services, err := q.StatsFilterServices(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("list the services: %w", err)
	}

	models, err := q.StatsFilterModels(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("list the models: %w", err)
	}

	return apigen.GetStatsFilters200JSONResponse{Routes: orEmpty(routes), Services: orEmpty(services), Models: orEmpty(models)}, nil
}

// orEmpty returns s, or an empty slice for nil, so JSON shows [].
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}

	return s
}
