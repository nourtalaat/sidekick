package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/watch"
)

// maxRecentChecks is how many checks of each watcher the API shows.
const maxRecentChecks = 50

// ListInvestigationWatchers returns the watchers of the investigation's
// group with their last checks.
func (s *server) ListInvestigationWatchers(ctx context.Context, req apigen.ListInvestigationWatchersRequestObject) (apigen.ListInvestigationWatchersResponseObject, error) {
	groupID, err := db.New(s.pool).GetInvestigationGroup(ctx, req.Id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNoInvestigation
	}

	if err != nil {
		return nil, fmt.Errorf("read investigation %s: %w", req.Id, err)
	}

	states, err := watch.States(ctx, s.pool, groupID)
	if err != nil {
		return nil, fmt.Errorf("read the watchers of investigation %s: %w", req.Id, err)
	}

	ids := make([]uuid.UUID, 0, len(states))

	for _, state := range states {
		id, err := uuid.Parse(state.ID)
		if err != nil {
			return nil, fmt.Errorf("watcher ID %q: %w", state.ID, err)
		}

		ids = append(ids, id)
	}

	checks, err := db.New(s.pool).ListRecentWatcherChecks(ctx, db.ListRecentWatcherChecksParams{WatcherIds: ids, PerWatcher: maxRecentChecks})
	if err != nil {
		return nil, fmt.Errorf("list the checks of the watchers of investigation %s: %w", req.Id, err)
	}

	return apigen.ListInvestigationWatchers200JSONResponse(watcherListOf(states, ids, checks)), nil
}

// watcherListOf is the API's view of the watchers with their checks,
// which are grouped by the watcher they belong to, newest first.
func watcherListOf(states []output.WatcherState, ids []uuid.UUID, checks []db.WatcherCheck) apigen.WatcherList {
	byWatcher := make(map[uuid.UUID][]apigen.WatcherCheck, len(ids))

	for _, c := range checks {
		byWatcher[c.WatcherID] = append(byWatcher[c.WatcherID], apigen.WatcherCheck{
			Seq: int(c.Seq), At: c.At, Result: apigen.WatcherCheckResult(c.Result), Detail: c.Detail, DurationMs: int(c.DurationMs),
		})
	}

	list := apigen.WatcherList{Watchers: make([]apigen.Watcher, 0, len(states))}

	for i, state := range states {
		recent := byWatcher[ids[i]]
		if recent == nil {
			recent = []apigen.WatcherCheck{}
		}

		list.Watchers = append(list.Watchers, watcherOf(state, ids[i], recent))
	}

	return list
}

// watcherOf is the API's view of one watcher.
func watcherOf(state output.WatcherState, id uuid.UUID, recent []apigen.WatcherCheck) apigen.Watcher {
	w := apigen.Watcher{
		Id:              id,
		Name:            state.Name,
		CheckType:       state.CheckType,
		Status:          apigen.WatcherStatus(state.Status),
		PassesInARow:    state.PassesInARow,
		PassesToRecover: state.PassesToRecover,
		IntervalSeconds: state.IntervalSeconds,
		Checks:          state.Checks,
		LastResult:      nonEmpty(&state.LastResult),
		StartedAt:       state.StartedAt,
		StopReason:      nonEmpty(&state.StopReason),
		RecentChecks:    recent,
	}

	if !state.LastCheckAt.IsZero() {
		w.LastCheckAt = &state.LastCheckAt
	}

	if !state.EndedAt.IsZero() {
		w.EndedAt = &state.EndedAt
	}

	if state.StoppedBy != nil {
		w.StoppedBy = personOf(*state.StoppedBy)
	}

	return w
}

// personOf is the API's view of someone who acted.
func personOf(p output.Person) *apigen.Person {
	out := &apigen.Person{SlackUserId: nonEmpty(&p.SlackUserID), Name: nonEmpty(&p.Name)}
	if p.UserID != uuid.Nil {
		out.UserId = &p.UserID
	}

	return out
}
