package investigator

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// MetricDailySpend is the name of the gauge of each route's spend today.
const MetricDailySpend = "sidekick_daily_spend_usd"

// spendScrapeTimeout bounds the query a scrape of MetricDailySpend runs.
const spendScrapeTimeout = 2 * time.Second

// DailyCaps are the daily spending caps, which hold every investigation
// together, read from the config as it is now rather than fixed with each
// investigation.
type DailyCaps struct {
	// Location is the time zone whose midnight starts a new day, for every
	// cap and for spend_daily. Nil means UTC.
	Location *time.Location
	// MaxCostMicros is the global cap: what every investigation together
	// may cost in one day. Zero means none.
	MaxCostMicros int64
	// Skip ends new investigations at the global cap, and at a route's cap
	// when the route does not say, instead of holding them until the next
	// day.
	Skip bool
}

// addSpend adds the cost and tokens of a saved model step to the day's
// spend of the investigation's route. Run it in the step's transaction.
func (r *run) addSpend(ctx context.Context, tx pgx.Tx, s newStep) error {
	var in, out int64
	if s.usage != nil {
		in, out = s.usage.InputTokens, s.usage.OutputTokens
	}

	err := db.New(tx).AddSpend(ctx, db.AddSpendParams{
		Day:          r.h.today(),
		Route:        r.inv.Route,
		Connector:    s.connector,
		Model:        s.model,
		CostMicros:   deref(s.cost),
		InputTokens:  in,
		OutputTokens: out,
	})
	if err != nil {
		return fmt.Errorf("add the step to the daily spend: %w", err)
	}

	return nil
}

// today returns the day it is now in the caps' time zone, as spend_daily
// stores it.
func (h *Handler) today() pgtype.Date {
	return spendDay(h.now(), h.location())
}

// spendCollector reports MetricDailySpend from spend_daily at each scrape,
// so every replica shows the spend of the whole deployment, and a new day
// starts at zero even for a route with nothing running.
type spendCollector struct {
	pool   *pgxpool.Pool
	routes []string
	today  func() pgtype.Date
	log    *slog.Logger
	desc   *prometheus.Desc
}

var _ prometheus.Collector = (*spendCollector)(nil)

// Describe sends the gauge's description.
func (c *spendCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

// Collect sends today's spend of every configured route, zero for one
// that has spent nothing. When the query fails it sends nothing, so the
// rest of the scrape still works.
func (c *spendCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), spendScrapeTimeout)
	defer cancel()

	rows, err := db.New(c.pool).ListDailySpend(ctx, c.today())
	if err != nil {
		c.log.WarnContext(ctx, "could not read the daily spend for the metrics", "error", err)

		return
	}

	spent := make(map[string]int64, len(c.routes))
	for _, route := range c.routes {
		spent[route] = 0
	}

	for _, row := range rows {
		spent[row.Route] = row.CostMicros
	}

	for _, route := range slices.Sorted(maps.Keys(spent)) {
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(spent[route])/microDollarsPerUSD, route)
	}
}

// newSpendCollector returns the collector of MetricDailySpend for h.
func newSpendCollector(h *Handler) *spendCollector {
	return &spendCollector{
		pool:   h.opts.Pool,
		routes: slices.Sorted(maps.Keys(h.opts.Routes)),
		today:  h.today,
		log:    h.log,
		desc: prometheus.NewDesc(MetricDailySpend, "What each route's investigations have spent today on model calls, in US dollars, "+
			"from the price table. The day starts at midnight in budgets.daily.timeZone.", []string{labelRoute}, nil),
	}
}

// spendDay returns the date of t in loc, or in UTC when loc is nil.
func spendDay(t time.Time, loc *time.Location) pgtype.Date {
	if loc == nil {
		loc = time.UTC
	}

	y, m, d := t.In(loc).Date()

	return pgtype.Date{Time: time.Date(y, m, d, 0, 0, 0, 0, time.UTC), Valid: true}
}
