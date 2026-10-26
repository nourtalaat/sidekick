package investigator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/publish"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// budgetDailyCap is the budget of a daily spending cap, as steps.budget
// and the metrics name it.
const budgetDailyCap = "daily_cap"

// skipReasonDailyCap is the reason label of sidekick_events_skipped_total
// for an investigation the daily cap skipped.
const skipReasonDailyCap = "daily_cap"

// noticeTitleDailyCap is the title of the daily cap notice.
const noticeTitleDailyCap = "Daily cap reached"

// dailyCapDetail is the stop detail of a run the daily cap ended.
const dailyCapDetail = "daily cap reached"

// capped is a daily cap that is reached.
type capped struct {
	route string
	// global is the global cap; otherwise it is the route's.
	global bool
	// limit is the cap in micro-dollars.
	limit int64
	// skip ends new investigations instead of holding them.
	skip bool
}

// holdForDailyCap checks, before the first run of the investigation with
// id starts, whether a daily cap is reached. At a cap that waits, it
// moves the job to the start of the next day and posts the notice, and
// reports true: the job is done for today. At a cap that skips, it
// returns the cap for the caller to end the investigation with once it
// owns the run. An investigation that has started already is never held:
// it meets the cap before its next model call instead.
func (h *Handler) holdForDailyCap(ctx context.Context, job db.Job, id uuid.UUID) (held bool, skip *capped, err error) {
	inv, err := db.New(h.opts.Pool).GetInvestigation(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil, nil
	}

	if err != nil {
		return false, nil, fmt.Errorf("load investigation %s: %w", id, err)
	}

	if inv.Status != StatusQueued {
		return false, nil, nil
	}

	c, reached, err := h.dailyCap(ctx, inv.Route)
	if err != nil {
		return false, nil, fmt.Errorf("investigation %s: %w", id, err)
	}

	if !reached {
		return false, nil, nil
	}

	if c.skip {
		return false, &c, nil
	}

	if err := h.waitForNextDay(ctx, job, inv, c); err != nil {
		return false, nil, fmt.Errorf("hold investigation %s until the next day: %w", id, err)
	}

	return true, nil, nil
}

// dailyCap returns the daily cap that route has reached, if any: the
// route's own, then the global one.
func (h *Handler) dailyCap(ctx context.Context, route string) (capped, bool, error) {
	rc, global := h.opts.Routes[route], h.opts.Daily

	if rc.DailyMaxCostMicros <= 0 && global.MaxCostMicros <= 0 {
		return capped{}, false, nil
	}

	spent, err := db.New(h.opts.Pool).GetDailySpend(ctx, db.GetDailySpendParams{Route: route, Day: h.today()})
	if err != nil {
		return capped{}, false, fmt.Errorf("read the daily spend: %w", err)
	}

	switch {
	case rc.DailyMaxCostMicros > 0 && spent.RouteMicros >= rc.DailyMaxCostMicros:
		return capped{route: route, limit: rc.DailyMaxCostMicros, skip: rc.DailySkip}, true, nil
	case global.MaxCostMicros > 0 && spent.TotalMicros >= global.MaxCostMicros:
		return capped{route: route, global: true, limit: global.MaxCostMicros, skip: global.Skip}, true, nil
	default:
		return capped{}, false, nil
	}
}

// waitForNextDay moves the investigate job to the start of the next day
// and posts the notice, in one transaction. The job gives up its unique
// key first, so the job for the next day can take it; the handler then
// returns nil, which completes the job of today.
func (h *Handler) waitForNextDay(ctx context.Context, job db.Job, inv db.Investigation, c capped) error {
	next := h.nextDay()

	err := pgx.BeginFunc(ctx, h.opts.Pool, func(tx pgx.Tx) error {
		if err := queue.ReleaseUniqueKey(ctx, tx, job); err != nil {
			return fmt.Errorf("give up the unique key: %w", err)
		}

		if _, err := queue.Enqueue(ctx, tx, queue.NewJob{
			Kind:      queue.KindInvestigate,
			Route:     inv.Route,
			Payload:   job.Payload,
			UniqueKey: UniqueKey(inv.GroupID),
			RunAt:     next,
		}); err != nil {
			return fmt.Errorf("enqueue the investigate job for %s: %w", next.Format(time.RFC3339), err)
		}

		return h.noticeDailyCap(ctx, tx, c)
	})
	if err != nil {
		return fmt.Errorf("wait: %w", err)
	}

	h.log.InfoContext(ctx, "investigation held by the daily cap", "investigation_id", inv.ID.String(), "job_id", job.ID,
		"route", inv.Route, "global_cap", c.global, "until", next)

	return nil
}

// skipForDailyCap ends the investigation, whose run the caller owns,
// without running it: an inconclusive report with stop reason budget and
// the cap as its reason, published with the notice in one transaction.
func (h *Handler) skipForDailyCap(ctx context.Context, inv db.Investigation, c capped, log *slog.Logger) error {
	hist, err := newHistory(nil)
	if err != nil {
		return fmt.Errorf("start an empty history: %w", err)
	}

	end := ending{report: hist.inconclusive(report.StopBudget, "Not investigated: "+c.reached()+".", dailyCapDetail)}

	raw, err := json.Marshal(end.report)
	if err != nil {
		return fmt.Errorf("encode the report: %w", err)
	}

	var finished db.Investigation

	err = pgx.BeginFunc(ctx, h.opts.Pool, func(tx pgx.Tx) error {
		finished, err = db.New(tx).FinishInvestigation(ctx, db.FinishInvestigationParams{
			Status:     StatusFinal,
			Verdict:    new(string(end.report.Verdict)),
			Confidence: new(string(end.report.Confidence)),
			Report:     store.JSON(raw),
			StopReason: new(string(end.report.StopReason)),
			ID:         inv.ID,
			Fence:      inv.Fence,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("store the report: %w", queue.ErrLeaseLost)
		}

		if err != nil {
			return fmt.Errorf("store the report: %w", err)
		}

		if err := publishFinal(ctx, tx, finished, nil, &end.report); err != nil {
			return fmt.Errorf("publish the skipped investigation: %w", err)
		}

		return h.noticeDailyCap(ctx, tx, c)
	})
	if err != nil {
		return fmt.Errorf("skip investigation %s: %w", inv.ID, err)
	}

	h.opts.Metrics.skipped(inv.Route, skipReasonDailyCap)
	h.finished(ctx, log, finished, end)

	return nil
}

// noticeDailyCap posts the notice of c to the route's outputs, once a day
// for each cap and route.
func (h *Handler) noticeDailyCap(ctx context.Context, tx pgx.Tx, c capped) error {
	key := "daily_cap:route:" + c.route
	if c.global {
		key = "daily_cap:global:" + c.route
	}

	what := "wait until"
	if c.skip {
		what = "are skipped until"
	}

	scope := "New investigations on this route"
	if c.global {
		scope = "New investigations"
	}

	now := h.now()

	_, err := publish.SendNotice(ctx, tx, publish.Notice{
		Key:       key,
		At:        now,
		NotBefore: h.startOfDay(now),
		Sinks:     h.opts.Routes[c.route].Outputs,
		Notice: output.Notice{
			Kind:  output.NoticeDailyCap,
			Title: noticeTitleDailyCap,
			Text:  fmt.Sprintf("%s. %s %s %s.", upperFirst(c.reached()), scope, what, h.dayStartText()),
		},
	})
	if err != nil {
		return fmt.Errorf("post the daily cap notice: %w", err)
	}

	return nil
}

// reached says which cap is reached, for the notice and the report.
func (c capped) reached() string {
	if c.global {
		return fmt.Sprintf("all routes together have used today's global cap of %s", usd(c.limit))
	}

	return fmt.Sprintf("route %s has used all of today's %s cap", c.route, usd(c.limit))
}

// nextDay returns when the next day starts in the caps' time zone.
func (h *Handler) nextDay() time.Time {
	return h.startOfDay(h.now()).AddDate(0, 0, 1)
}

// startOfDay returns the midnight that started t's day in the caps' time
// zone.
func (h *Handler) startOfDay(t time.Time) time.Time {
	loc := h.location()
	y, m, d := t.In(loc).Date()

	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// dayStartText says when a day starts, such as "00:00 UTC".
func (h *Handler) dayStartText() string {
	return "00:00 " + h.location().String()
}

// location returns the caps' time zone.
func (h *Handler) location() *time.Location {
	if h.opts.Daily.Location == nil {
		return time.UTC
	}

	return h.opts.Daily.Location
}

// upperFirst returns s with its first letter in upper case. s is ASCII.
func upperFirst(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}

	return string(s[0]-'a'+'A') + s[1:]
}
