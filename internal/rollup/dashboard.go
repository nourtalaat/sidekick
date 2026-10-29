package rollup

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The names of the dashboard metrics. The ones that end in _30d cover the
// last DashboardWindowDays UTC days, today included.
const (
	MetricRatedVerdicts      = "sidekick_dashboard_rated_verdicts_30d"
	MetricCorrectVerdicts    = "sidekick_dashboard_correct_verdicts_30d"
	MetricVerdictAccuracy    = "sidekick_dashboard_verdict_accuracy_ratio_30d"
	MetricInvestigations     = "sidekick_dashboard_investigations_30d"
	MetricRecoveries         = "sidekick_dashboard_recoveries_30d"
	MetricWatchTimeouts      = "sidekick_dashboard_watch_timeouts_30d"
	MetricFirstFindingMedian = "sidekick_dashboard_time_to_first_finding_median_seconds_30d"
	MetricSpendToday         = "sidekick_dashboard_spend_today_usd"
	MetricRouteSpendCap      = "sidekick_dashboard_route_spend_cap_usd"
	MetricSpendCap           = "sidekick_dashboard_spend_cap_usd"
	MetricDashboardRefresh   = "sidekick_dashboard_refreshed_timestamp_seconds"
)

const (
	// DashboardWindowDays is how many UTC days, today included, the _30d
	// metrics cover. It is the default range of the stats endpoints.
	DashboardWindowDays = 30
	// DefaultDashboardInterval is how often a Dashboard reads the tables.
	DefaultDashboardInterval = time.Minute

	labelRoute   = "route"
	labelModel   = "model"
	labelVerdict = "verdict"

	// labelNone stands for an empty model or verdict, which investigations
	// without one have in the rollup tables.
	labelNone = "none"

	oneDay             = 24 * time.Hour
	microDollarsPerUSD = 1_000_000.0
	msPerSecond        = 1000.0
	// dashboardRefreshTimeout bounds one refresh.
	dashboardRefreshTimeout = 30 * time.Second
)

// DashboardOptions set up a Dashboard. Pool is required.
type DashboardOptions struct {
	Pool *pgxpool.Pool
	// Interval is how often the Dashboard reads the tables. Zero means
	// DefaultDashboardInterval.
	Interval time.Duration
	// Location is the time zone whose midnight starts a new day for the
	// daily caps and spend_daily. Nil means UTC.
	Location *time.Location
	// RouteCapsMicros are the daily spending caps of the routes that have
	// one, in micro-dollars.
	RouteCapsMicros map[string]int64
	// GlobalCapMicros is the global daily cap in micro-dollars. Zero means
	// none.
	GlobalCapMicros int64
	// Now returns the time. Nil means time.Now.
	Now func() time.Time
	// Logger gets a line when a refresh fails. Nil logs nothing.
	Logger *slog.Logger
}

// Dashboard exports the numbers of the dashboard as Prometheus gauges. It
// reads the rollup tables on a timer, in every replica, and keeps the
// result as a snapshot that a scrape sends as it is, so a scrape costs no
// query and every replica reports the same numbers to within one interval.
// A series whose data leaves the window is gone from the next snapshot.
type Dashboard struct {
	pool     *pgxpool.Pool
	interval time.Duration
	loc      *time.Location
	routeCap map[string]int64
	global   int64
	now      func() time.Time
	log      *slog.Logger

	descs    dashboardDescs
	snapshot atomic.Pointer[[]prometheus.Metric]
}

var _ prometheus.Collector = (*Dashboard)(nil)

// NewDashboard returns a Dashboard and registers it with reg. It reports
// nothing until its first refresh succeeds, except the caps.
func NewDashboard(reg prometheus.Registerer, opts DashboardOptions) (*Dashboard, error) {
	d := &Dashboard{
		pool:     opts.Pool,
		interval: opts.Interval,
		loc:      opts.Location,
		routeCap: opts.RouteCapsMicros,
		global:   opts.GlobalCapMicros,
		now:      opts.Now,
		log:      opts.Logger,
		descs:    newDashboardDescs(),
	}

	if d.interval <= 0 {
		d.interval = DefaultDashboardInterval
	}

	if d.loc == nil {
		d.loc = time.UTC
	}

	if d.now == nil {
		d.now = time.Now
	}

	if d.log == nil {
		d.log = slog.New(slog.DiscardHandler)
	}

	if err := reg.Register(d); err != nil {
		return nil, fmt.Errorf("register the dashboard metrics: %w", err)
	}

	return d, nil
}

// Run refreshes the snapshot at once and then every interval, until ctx
// ends. A failed refresh is logged and tried again at the next tick; the
// last snapshot stays.
func (d *Dashboard) Run(ctx context.Context) error {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	for {
		refreshCtx, cancel := context.WithTimeout(ctx, dashboardRefreshTimeout)
		err := d.Refresh(refreshCtx)

		cancel()

		if err != nil && ctx.Err() == nil {
			d.log.WarnContext(ctx, "refreshing the dashboard metrics failed; keeping the last values", "error", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Refresh reads the tables in one repeatable-read transaction, so the
// numbers agree with each other, and replaces the snapshot. When it fails
// the snapshot stays as it was.
func (d *Dashboard) Refresh(ctx context.Context) error {
	now := d.now()
	window := windowAt(now)
	data := dashboardData{at: now}

	err := pgx.BeginTxFunc(ctx, d.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		return data.read(ctx, db.New(tx), window, spendDay(now, d.loc))
	})
	if err != nil {
		return fmt.Errorf("read the dashboard numbers: %w", err)
	}

	metrics := d.metricsOf(data)
	d.snapshot.Store(&metrics)

	return nil
}

// Describe sends the descriptions of the metrics.
func (d *Dashboard) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range d.descs.all() {
		ch <- desc
	}
}

// Collect sends the caps and the last snapshot.
func (d *Dashboard) Collect(ch chan<- prometheus.Metric) {
	for route, micros := range d.routeCap {
		ch <- gauge(d.descs.routeCap, float64(micros)/microDollarsPerUSD, route)
	}

	if d.global > 0 {
		ch <- gauge(d.descs.cap, float64(d.global)/microDollarsPerUSD)
	}

	if snap := d.snapshot.Load(); snap != nil {
		for _, m := range *snap {
			ch <- m
		}
	}
}

// dashboardData are the rows one refresh read.
type dashboardData struct {
	at      time.Time
	quality []db.DashboardQualityRow
	stats   []db.DashboardStatsByRouteRow
	hists   []db.DashboardFirstFindingHistogramsRow
	spend   []db.DashboardSpendByDayRow
}

// read fills data from the tables.
func (data *dashboardData) read(ctx context.Context, q *db.Queries, w dashboardWindow, today pgtype.Date) error {
	var err error

	data.quality, err = q.DashboardQuality(ctx, db.DashboardQualityParams{FromDay: pgDate(w.fromDay), ToDay: pgDate(w.toDay)})
	if err != nil {
		return fmt.Errorf("read the ratings: %w", err)
	}

	data.stats, err = q.DashboardStatsByRoute(ctx, db.DashboardStatsByRouteParams{FromHour: w.fromHour, ToHour: w.toHour})
	if err != nil {
		return fmt.Errorf("read the investigations: %w", err)
	}

	data.hists, err = q.DashboardFirstFindingHistograms(ctx, db.DashboardFirstFindingHistogramsParams{FromHour: w.fromHour, ToHour: w.toHour})
	if err != nil {
		return fmt.Errorf("read the time to first finding: %w", err)
	}

	data.spend, err = q.DashboardSpendByDay(ctx, today)
	if err != nil {
		return fmt.Errorf("read today's spend: %w", err)
	}

	return nil
}

// metricsOf turns the rows into one gauge for each series. Rows that end up
// with the same labels, such as an empty model and a model named none, are
// added together.
func (d *Dashboard) metricsOf(data dashboardData) []prometheus.Metric {
	var (
		rated, correct, investigations, spend tally
		accuracyRated, accuracyCorrect        tally
		recoveries, timeouts                  tally
	)

	for _, r := range data.quality {
		model := orNone(r.Model)

		rated.add(float64(r.Rated), r.Route, model, orNone(r.Predicted))
		correct.add(float64(r.Correct), r.Route, model, orNone(r.Predicted))
		accuracyRated.add(float64(r.Rated), r.Route, model)
		accuracyCorrect.add(float64(r.Correct), r.Route, model)
	}

	for _, r := range data.stats {
		investigations.add(float64(r.Investigations), r.Route, orNone(r.Verdict))
		recoveries.add(float64(r.Recoveries), r.Route)
		timeouts.add(float64(r.WatchTimeouts), r.Route)
	}

	for _, r := range data.spend {
		spend.add(float64(r.CostMicros)/microDollarsPerUSD, r.Route, orNone(r.Model))
	}

	metrics := []prometheus.Metric{gauge(d.descs.refreshed, float64(data.at.Unix()))}
	metrics = append(metrics, rated.gauges(d.descs.rated)...)
	metrics = append(metrics, correct.gauges(d.descs.correct)...)
	metrics = append(metrics, accuracyOf(d.descs.accuracy, accuracyCorrect, accuracyRated)...)
	metrics = append(metrics, investigations.gauges(d.descs.investigations)...)
	metrics = append(metrics, recoveries.gauges(d.descs.recoveries)...)
	metrics = append(metrics, timeouts.gauges(d.descs.timeouts)...)
	metrics = append(metrics, spend.gauges(d.descs.spend)...)

	return append(metrics, d.medians(data.hists)...)
}

// medians returns the approximate median time to the first finding of each
// route that has one, in seconds.
func (d *Dashboard) medians(rows []db.DashboardFirstFindingHistogramsRow) []prometheus.Metric {
	byRoute := map[string][]db.DashboardFirstFindingHistogramsRow{}
	for _, r := range rows {
		byRoute[r.Route] = append(byRoute[r.Route], r)
	}

	var out []prometheus.Metric

	for route, routeRows := range byRoute {
		counts := HistCounts(routeRows, func(r db.DashboardFirstFindingHistogramsRow) (int32, int64) { return r.Bucket, r.N })

		if median := HistMedian(counts); median != nil {
			out = append(out, gauge(d.descs.firstFinding, *median/msPerSecond, route))
		}
	}

	return out
}

// tally adds up values by up to three label values.
type tally map[[3]string]float64

func (t *tally) add(v float64, labels ...string) {
	if *t == nil {
		*t = tally{}
	}

	var key [3]string

	copy(key[:], labels)

	(*t)[key] += v
}

// gauges returns a gauge of desc for each series of the tally, with as many
// label values as desc has labels.
func (t tally) gauges(desc *labeledDesc) []prometheus.Metric {
	out := make([]prometheus.Metric, 0, len(t))

	for key, v := range t {
		out = append(out, gauge(desc, v, key[:desc.labels]...))
	}

	return out
}

// accuracyOf returns the share of the rated verdicts that were confirmed,
// for each series that has a rated verdict.
func accuracyOf(desc *labeledDesc, correct, rated tally) []prometheus.Metric {
	out := make([]prometheus.Metric, 0, len(rated))

	for key, n := range rated {
		if n > 0 {
			out = append(out, gauge(desc, correct[key]/n, key[:desc.labels]...))
		}
	}

	return out
}

// orNone returns the label value for s, which is none when s is empty.
func orNone(s string) string {
	if s == "" {
		return labelNone
	}

	return s
}

// dashboardWindow is the range the _30d metrics cover: the days from
// fromDay to toDay, both included, as the hours from fromHour up to toHour.
type dashboardWindow struct {
	fromDay, toDay   time.Time
	fromHour, toHour time.Time
}

// windowAt returns the window that ends on the UTC day of now.
func windowAt(now time.Time) dashboardWindow {
	y, m, d := now.UTC().Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	from := today.Add(-(DashboardWindowDays - 1) * oneDay)

	return dashboardWindow{fromDay: from, toDay: today, fromHour: from, toHour: today.Add(oneDay)}
}

// spendDay returns the date of t in loc, as spend_daily stores it.
func spendDay(t time.Time, loc *time.Location) pgtype.Date {
	y, m, d := t.In(loc).Date()

	return pgDate(time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
}

func pgDate(t time.Time) pgtype.Date {
	return pgtype.Date{Time: t, Valid: true}
}

// labeledDesc is a metric description that knows how many labels it has.
type labeledDesc struct {
	*prometheus.Desc

	labels int
}

func gauge(desc *labeledDesc, v float64, labelValues ...string) prometheus.Metric {
	return prometheus.MustNewConstMetric(desc.Desc, prometheus.GaugeValue, v, labelValues...)
}

// dashboardDescs are the descriptions of the dashboard metrics.
type dashboardDescs struct {
	rated, correct, accuracy, investigations, recoveries, timeouts *labeledDesc
	firstFinding, spend, routeCap, cap, refreshed                  *labeledDesc
}

func newDashboardDescs() dashboardDescs {
	const window = "over the last 30 UTC days, today included, from the dashboard's rollup tables"

	return dashboardDescs{
		rated: newDesc(MetricRatedVerdicts, "Investigations that people rated "+window+", by the verdict Sidekick gave.",
			labelRoute, labelModel, labelVerdict),
		correct: newDesc(MetricCorrectVerdicts, "Rated investigations whose verdict people confirmed "+window+", by the verdict Sidekick gave.",
			labelRoute, labelModel, labelVerdict),
		accuracy: newDesc(MetricVerdictAccuracy, "Share of the rated investigations whose verdict people confirmed "+window+
			". No series while a route and model have none rated.", labelRoute, labelModel),
		investigations: newDesc(MetricInvestigations, "Investigations created "+window+", by verdict; none is one with no verdict yet. "+
			"The false_positive series are the false positives found.", labelRoute, labelVerdict),
		recoveries: newDesc(MetricRecoveries, "Investigations whose watchers all recovered "+window+".", labelRoute),
		timeouts:   newDesc(MetricWatchTimeouts, "Watchers that timed out "+window+".", labelRoute),
		firstFinding: newDesc(MetricFirstFindingMedian, "Approximate median time from the start of an investigation to its first finding "+window+
			", in seconds. It is exact only to the width of a histogram bucket.", labelRoute),
		spend: newDesc(MetricSpendToday, "What model calls cost today in US dollars, by route and model. "+
			"The day starts at midnight in budgets.daily.timeZone.", labelRoute, labelModel),
		routeCap:  newDesc(MetricRouteSpendCap, "A route's daily spending cap in US dollars. Only routes with a cap have a series.", labelRoute),
		cap:       newDesc(MetricSpendCap, "The global daily spending cap in US dollars. No series when there is none."),
		refreshed: newDesc(MetricDashboardRefresh, "When this replica last read the dashboard numbers, in seconds since the Unix epoch."),
	}
}

func newDesc(name, help string, labels ...string) *labeledDesc {
	return &labeledDesc{Desc: prometheus.NewDesc(name, help, labels, nil), labels: len(labels)}
}

func (d dashboardDescs) all() []*prometheus.Desc {
	var out []*prometheus.Desc

	for _, desc := range []*labeledDesc{
		d.rated, d.correct, d.accuracy, d.investigations, d.recoveries, d.timeouts,
		d.firstFinding, d.spend, d.routeCap, d.cap, d.refreshed,
	} {
		out = append(out, desc.Desc)
	}

	return out
}
