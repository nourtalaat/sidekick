package router

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/publish"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The statuses of an event a rate limit holds.
const (
	statusDelayed = "delayed"
	statusSkipped = "skipped"
)

// globalScope is the scope of budgets.rateLimit; a route's scope is
// routeScopePrefix and its name.
const (
	globalScope      = "global"
	routeScopePrefix = "route:"
)

// skipReasonRateLimit is the reason label of sidekick_events_skipped_total
// for an event over a rate limit.
const skipReasonRateLimit = "rate_limit"

// noticeTitleRateLimit is the title of the rate limit notice.
const noticeTitleRateLimit = "Rate limit reached"

// rateScope is one rate limit an event must pass.
type rateScope struct {
	key   string
	limit config.RateLimit
	// route is the route the limit is of; empty for the global one.
	route string
}

// heldEvent is an event a rate limit held.
type heldEvent struct {
	scope  rateScope
	status string
}

// admit checks the rate limits of rt for ev, as its own step before the
// event is grouped. When every limit has room, it records the event in
// each and reports true. Otherwise it holds the event as the first full
// limit says: delayed, with the route_event job moved to when the window
// has room, or skipped. It posts that limit's notice at most once per
// window, all in one transaction. The limits are locked in the same order
// every time, the global one first, so routers on several replicas count
// one event at a time without waiting for each other in a circle. An
// event admitted already, by a run that failed after it, is admitted
// again without counting twice.
func (r *Router) admit(ctx context.Context, job db.Job, ev db.Event, rt route, log *slog.Logger) (bool, error) {
	scopes := r.rateScopes(rt)
	if len(scopes) == 0 {
		return true, nil
	}

	now := r.now()

	var held *heldEvent

	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		queries := db.New(tx)

		for _, s := range scopes {
			if err := queries.LockRateScope(ctx, s.key); err != nil {
				return fmt.Errorf("lock rate limit %s: %w", s.key, err)
			}
		}

		for _, s := range scopes {
			w, err := queries.GetRateWindow(ctx, db.GetRateWindowParams{Since: now.Add(-s.limit.Per), EventID: ev.ID, Scope: s.key})
			if err != nil {
				return fmt.Errorf("count rate limit %s: %w", s.key, err)
			}

			if !w.Already && w.Admitted >= int64(s.limit.Events) {
				held = &heldEvent{scope: s, status: statusDelayed}
				if s.limit.OverLimit == config.OverLimitSkip {
					held.status = statusSkipped
				}

				return r.hold(ctx, tx, job, ev, rt, *held, w.Oldest.Add(s.limit.Per), now)
			}
		}

		return admitEvent(ctx, queries, ev, scopes, now)
	})
	if err != nil {
		return false, fmt.Errorf("rate limits: %w", err)
	}

	if held == nil {
		return true, nil
	}

	if held.status == statusSkipped {
		r.skipped(rt.name, skipReasonRateLimit)
	}

	log.InfoContext(ctx, "event held by a rate limit", "status", held.status, "rate_limit", held.scope.key)

	return false, nil
}

// rateScopes returns the rate limits an event of rt must pass, the global
// one first.
func (r *Router) rateScopes(rt route) []rateScope {
	var out []rateScope

	if r.globalRate != nil {
		out = append(out, rateScope{key: globalScope, limit: *r.globalRate})
	}

	if rt.rateLimit != nil {
		out = append(out, rateScope{key: routeScopePrefix + rt.name, limit: *rt.rateLimit, route: rt.name})
	}

	return out
}

// hold marks ev as held by h's limit and posts the limit's notice, once
// per window for each limit and route. A delayed event gets a new route_event job at until, when the oldest
// event in the window leaves it, after this job gives up its unique key.
func (r *Router) hold(ctx context.Context, tx pgx.Tx, job db.Job, ev db.Event, rt route, h heldEvent, until, now time.Time) error {
	reason := fmt.Sprintf("over the rate limit of %s: %d events per %s", h.scope.subject(), h.scope.limit.Events, duration(h.scope.limit.Per))

	if _, err := db.New(tx).MarkEventHeld(ctx, db.MarkEventHeldParams{
		Status:     h.status,
		Route:      &rt.name,
		SkipReason: &reason,
		ID:         ev.ID,
	}); err != nil {
		return fmt.Errorf("mark the event %s: %w", h.status, err)
	}

	if h.status == statusDelayed {
		if err := delay(ctx, tx, job, until); err != nil {
			return fmt.Errorf("delay the event: %w", err)
		}
	}

	key := "rate_limit:" + h.scope.key
	if h.scope.route == "" {
		key += ":" + rt.name
	}

	if _, err := publish.SendNotice(ctx, tx, publish.Notice{
		Key:       key,
		At:        now,
		NotBefore: now.Add(-h.scope.limit.Per),
		Sinks:     rt.outputs,
		Notice:    h.notice(),
	}); err != nil {
		return fmt.Errorf("post the rate limit notice: %w", err)
	}

	return nil
}

// skipped counts an event that is not investigated, for reason.
func (r *Router) skipped(route, reason string) {
	if r.skippedEvents == nil {
		return
	}

	r.skippedEvents.WithLabelValues(route, reason).Inc()
}

// notice returns the notice of the event's limit.
func (h heldEvent) notice() output.Notice {
	then := "are delayed and investigated once the window has room"
	if h.status == statusSkipped {
		then = "are skipped and not investigated"
	}

	return output.Notice{
		Kind:  output.NoticeRateLimit,
		Title: noticeTitleRateLimit,
		Text: fmt.Sprintf("%s received %d events in the last %s, the most its rate limit allows. Events over the limit %s.",
			upperFirst(h.scope.subject()), h.scope.limit.Events, duration(h.scope.limit.Per), then),
	}
}

// subject names who the limit holds, for the notice and the skip reason.
func (s rateScope) subject() string {
	if s.route == "" {
		return "all routes together"
	}

	return "route " + s.route
}

// admitEvent records ev in each scope at now, drops the admissions that
// have left their window, and sets a delayed event back to received.
func admitEvent(ctx context.Context, queries *db.Queries, ev db.Event, scopes []rateScope, now time.Time) error {
	for _, s := range scopes {
		if err := queries.AdmitEvent(ctx, db.AdmitEventParams{Scope: s.key, EventID: ev.ID, AdmittedAt: now}); err != nil {
			return fmt.Errorf("admit the event to rate limit %s: %w", s.key, err)
		}

		if err := queries.PruneRateAdmissions(ctx, db.PruneRateAdmissionsParams{Scope: s.key, Before: now.Add(-s.limit.Per)}); err != nil {
			return fmt.Errorf("prune rate limit %s: %w", s.key, err)
		}
	}

	if err := queries.MarkEventReceived(ctx, ev.ID); err != nil {
		return fmt.Errorf("mark the event received: %w", err)
	}

	return nil
}

// delay moves the route_event job of job to until: it gives up the job's
// unique key and enqueues the same job to run then.
func delay(ctx context.Context, tx pgx.Tx, job db.Job, until time.Time) error {
	if err := queue.ReleaseUniqueKey(ctx, tx, job); err != nil {
		return fmt.Errorf("give up the unique key: %w", err)
	}

	next := queue.NewJob{Kind: queue.KindRouteEvent, Payload: job.Payload, RunAt: until}
	if job.UniqueKey != nil {
		next.UniqueKey = *job.UniqueKey
	}

	if _, err := queue.Enqueue(ctx, tx, next); err != nil {
		return fmt.Errorf("enqueue the route_event job for %s: %w", until.Format(time.RFC3339), err)
	}

	return nil
}

// duration says d in words, such as "1 hour" or "90 seconds".
func duration(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return plural(int64(d/time.Hour), "hour")
	case d%time.Minute == 0:
		return plural(int64(d/time.Minute), "minute")
	case d%time.Second == 0:
		return plural(int64(d/time.Second), "second")
	default:
		return d.String()
	}
}

// plural returns n and unit, with an s unless n is 1.
func plural(n int64, unit string) string {
	if n == 1 {
		return "1 " + unit
	}

	return fmt.Sprintf("%d %ss", n, unit)
}

// upperFirst returns s with its first letter in upper case. s is ASCII.
func upperFirst(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}

	return string(s[0]-'a'+'A') + s[1:]
}
