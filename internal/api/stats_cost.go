package api

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The filters the spend table cannot be filtered by, as ignoredFilters
// names them.
const (
	filterService = "service"
	filterVerdict = "verdict"
)

// GetCostStats returns what model calls cost over the range, by day, model
// and route, the cost per investigation, and today's spend against the
// caps.
func (s *server) GetCostStats(ctx context.Context, req apigen.GetCostStatsRequestObject) (apigen.GetCostStatsResponseObject, error) {
	p := req.Params

	w, err := s.statsWindowOf(p.From, p.To)
	if err != nil {
		return nil, err
	}

	f, err := statsFiltersOf(p.Route, p.Service, p.Model, p.Verdict)
	if err != nil {
		return nil, err
	}

	q := db.New(customPlan{db: s.pool})
	out := apigen.GetCostStats200JSONResponse{From: dateOf(w.from), To: dateOf(w.to), IgnoredFilters: ignoredSpendFilters(f)}

	if out.Totals, err = spendTotals(ctx, q, w, f); err != nil {
		return nil, fmt.Errorf("total the spend: %w", err)
	}

	if out.Previous, err = spendTotals(ctx, q, w.before(), f); err != nil {
		return nil, fmt.Errorf("total the spend before: %w", err)
	}

	if err := fillSpendRows(ctx, q, w, f, &out); err != nil {
		return nil, fmt.Errorf("list the spend: %w", err)
	}

	if out.PerInvestigation, out.PreviousPerInvestigation, err = costPerInvestigation(ctx, q, w, f); err != nil {
		return nil, fmt.Errorf("count the cost per investigation: %w", err)
	}

	if out.Today, err = s.spendToday(ctx, q); err != nil {
		return nil, fmt.Errorf("read today's spend: %w", err)
	}

	return out, nil
}

// ignoredSpendFilters names the filters of f that the spend table cannot
// apply.
func ignoredSpendFilters(f statsFilters) []string {
	out := []string{}

	if f.service != nil {
		out = append(out, filterService)
	}

	if f.verdict != nil {
		out = append(out, filterVerdict)
	}

	return out
}

func spendTotals(ctx context.Context, q *db.Queries, w statsWindow, f statsFilters) (apigen.Spend, error) {
	from, to := w.pgDays()

	row, err := q.SpendTotals(ctx, db.SpendTotalsParams{FromDay: from, ToDay: to, Route: f.route, Model: f.model})
	if err != nil {
		return apigen.Spend{}, fmt.Errorf("read totals: %w", err)
	}

	return apigen.Spend{CostMicros: row.CostMicros, InputTokens: row.InputTokens, OutputTokens: row.OutputTokens}, nil
}

// fillSpendRows sets the spend by day, by day and model, by model and by
// route of out from the rows of the range.
func fillSpendRows(ctx context.Context, q *db.Queries, w statsWindow, f statsFilters, out *apigen.GetCostStats200JSONResponse) error {
	from, to := w.pgDays()

	rows, err := q.SpendByDayModel(ctx, db.SpendByDayModelParams{FromDay: from, ToDay: to, Route: f.route, Model: f.model})
	if err != nil {
		return fmt.Errorf("read the days: %w", err)
	}

	out.Days, out.DayModels, out.ByModel = spendGroups(rows)

	routes, err := q.SpendByRoute(ctx, db.SpendByRouteParams{FromDay: from, ToDay: to, Route: f.route, Model: f.model})
	if err != nil {
		return fmt.Errorf("read the routes: %w", err)
	}

	out.ByRoute = make([]apigen.SpendRoute, 0, len(routes))
	for _, r := range routes {
		out.ByRoute = append(out.ByRoute, apigen.SpendRoute{
			Route: r.Route, CostMicros: r.CostMicros, InputTokens: r.InputTokens, OutputTokens: r.OutputTokens,
		})
	}

	return nil
}

// spendGroups adds the rows, which are in the order of day, connector and
// model, up by day and by connector and model.
func spendGroups(rows []db.SpendByDayModelRow) ([]apigen.SpendDay, []apigen.SpendDayModel, []apigen.SpendModel) {
	type modelKey struct{ connector, model string }

	days := []apigen.SpendDay{}
	dayModels := make([]apigen.SpendDayModel, 0, len(rows))
	byModel := map[modelKey]*apigen.SpendModel{}

	for _, r := range rows {
		day := dateOf(r.Day.Time)

		if n := len(days); n == 0 || !days[n-1].Day.Equal(day.Time) {
			days = append(days, apigen.SpendDay{Day: day})
		}

		last := &days[len(days)-1]
		last.CostMicros += r.CostMicros
		last.InputTokens += r.InputTokens
		last.OutputTokens += r.OutputTokens

		dayModels = append(dayModels, apigen.SpendDayModel{
			Day: day, Connector: r.Connector, Model: r.Model,
			CostMicros: r.CostMicros, InputTokens: r.InputTokens, OutputTokens: r.OutputTokens,
		})

		key := modelKey{r.Connector, r.Model}
		if byModel[key] == nil {
			byModel[key] = &apigen.SpendModel{Connector: r.Connector, Model: r.Model}
		}

		byModel[key].CostMicros += r.CostMicros
		byModel[key].InputTokens += r.InputTokens
		byModel[key].OutputTokens += r.OutputTokens
	}

	models := make([]apigen.SpendModel, 0, len(byModel))
	for _, key := range slices.SortedFunc(maps.Keys(byModel), func(a, b modelKey) int {
		return cmp.Or(cmp.Compare(a.connector, b.connector), cmp.Compare(a.model, b.model))
	}) {
		models = append(models, *byModel[key])
	}

	return days, dayModels, models
}

// capOf returns a cap in micro-dollars, or nil for none.
func capOf(micros int64) *int64 {
	if micros <= 0 {
		return nil
	}

	return &micros
}

// costPerInvestigation returns the investigations created on each day of
// the range and what they cost, and the same totals for the range before.
func costPerInvestigation(ctx context.Context, q *db.Queries, w statsWindow, f statsFilters) ([]apigen.InvestigationCostDay, apigen.InvestigationCost, error) {
	rows, err := q.CostPerInvestigationByDay(ctx, db.CostPerInvestigationByDayParams{
		FromHour: w.from, ToHour: w.end(), Route: f.route, Service: f.service, Model: f.model, Verdict: f.verdict,
	})
	if err != nil {
		return nil, apigen.InvestigationCost{}, fmt.Errorf("read the days: %w", err)
	}

	days := make([]apigen.InvestigationCostDay, 0, len(rows))
	for _, r := range rows {
		days = append(days, apigen.InvestigationCostDay{Day: dateOf(r.Day.Time), Investigations: r.Investigations, CostMicros: r.CostMicros})
	}

	before := w.before()

	prev, err := q.CostPerInvestigationTotals(ctx, db.CostPerInvestigationTotalsParams{
		FromHour: before.from, ToHour: before.end(), Route: f.route, Service: f.service, Model: f.model, Verdict: f.verdict,
	})
	if err != nil {
		return nil, apigen.InvestigationCost{}, fmt.Errorf("read the range before: %w", err)
	}

	return days, apigen.InvestigationCost{Investigations: prev.Investigations, CostMicros: prev.CostMicros}, nil
}

// spendToday returns what was spent today, in the time zone of the daily
// caps, against the global cap and each route's.
func (s *server) spendToday(ctx context.Context, q *db.Queries) (apigen.SpendToday, error) {
	loc := time.UTC

	if s.cfg != nil {
		var err error
		if loc, err = s.cfg.Budgets.Daily.Location(); err != nil {
			return apigen.SpendToday{}, fmt.Errorf("find the time zone of the caps: %w", err)
		}
	}

	y, m, d := s.now().In(loc).Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)

	spent, err := q.ListDailySpend(ctx, pgtype.Date{Time: today, Valid: true})
	if err != nil {
		return apigen.SpendToday{}, fmt.Errorf("read the spend of the day: %w", err)
	}

	caps, err := s.routeCaps(ctx)
	if err != nil {
		return apigen.SpendToday{}, fmt.Errorf("read the caps: %w", err)
	}

	out := apigen.SpendToday{Day: dateOf(today), Routes: []apigen.SpendTodayRoute{}}
	if s.cfg != nil {
		out.CapMicros = capOf(s.cfg.Budgets.Daily.MaxCostMicros())
	}

	byRoute := map[string]int64{}

	for _, r := range spent {
		byRoute[r.Route] = r.CostMicros
		out.SpentMicros += r.CostMicros
	}

	for _, route := range slices.Sorted(maps.Keys(mergeKeys(byRoute, caps))) {
		out.Routes = append(out.Routes, apigen.SpendTodayRoute{Route: route, SpentMicros: byRoute[route], CapMicros: capOf(caps[route])})
	}

	return out, nil
}

// mergeKeys returns a map with the keys of a and b.
func mergeKeys(a, b map[string]int64) map[string]struct{} {
	out := make(map[string]struct{}, len(a)+len(b))

	for k := range a {
		out[k] = struct{}{}
	}

	for k := range b {
		out[k] = struct{}{}
	}

	return out
}

// routeCaps returns the daily cap of each route that has one, in
// micro-dollars, from the routes in effect: the file's and the runtime ones.
func (s *server) routeCaps(ctx context.Context) (map[string]int64, error) {
	caps := map[string]int64{}

	if s.cfg == nil {
		return caps, nil
	}

	routes := s.cfg.Routes

	if s.rules != nil {
		_, effective, err := s.currentRules(ctx)
		if err != nil {
			return nil, err
		}

		routes = effective.RouteConfigs()
	}

	for _, r := range routes {
		if limit := r.Budgets.Daily.MaxCostMicros(); limit > 0 {
			caps[r.Name] = limit
		}
	}

	return caps, nil
}
