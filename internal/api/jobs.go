package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/auth"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The page sizes of GET /jobs.
const (
	defaultJobsPage = 50
	maxJobsPage     = 100
)

// ActionJobRetried is the audit action of a dead job an admin retried.
const ActionJobRetried = "job.retried"

// jobStatusDead is the only status GET /jobs lists.
const jobStatusDead = "dead"

// errBadCursor means a cursor is not one GET /jobs returned.
var errBadCursor = errors.New("not a cursor")

// firstPage is the cursor of the first page: after every job there is.
var firstPage = jobCursor{finishedAt: time.Date(9999, time.December, 31, 0, 0, 0, 0, time.UTC), id: math.MaxInt64}

// jobCursor is where a page of dead jobs starts: after the job that died
// at finishedAt with id.
type jobCursor struct {
	finishedAt time.Time
	id         int64
}

// ListJobs returns a page of dead jobs, newest first.
func (s *server) ListJobs(ctx context.Context, req apigen.ListJobsRequestObject) (apigen.ListJobsResponseObject, error) {
	params := req.Params

	if params.Status != nil && string(*params.Status) != jobStatusDead {
		return nil, problemf(http.StatusBadRequest, "Only dead jobs are listed: status must be dead.")
	}

	limit := defaultJobsPage
	if params.Limit != nil {
		limit = *params.Limit
	}

	if limit < 1 || limit > maxJobsPage {
		return nil, problemf(http.StatusBadRequest, "limit must be 1 to %d.", maxJobsPage)
	}

	from := firstPage

	if params.Cursor != nil {
		c, err := parseJobCursor(*params.Cursor)
		if err != nil {
			return nil, problemf(http.StatusBadRequest, "cursor is not one this API returned.")
		}

		from = c
	}

	rows, err := db.New(s.pool).ListDeadJobs(ctx, db.ListDeadJobsParams{
		BeforeFinishedAt: from.finishedAt, BeforeID: from.id, PageSize: int32(limit + 1), //nolint:gosec // limit is at most maxJobsPage.
	})
	if err != nil {
		return nil, fmt.Errorf("list dead jobs: %w", err)
	}

	list := apigen.ListJobs200JSONResponse{Jobs: make([]apigen.Job, 0, min(len(rows), limit))}

	for i, row := range rows {
		if i == limit {
			next := jobCursor{id: rows[i-1].ID}
			if f := rows[i-1].FinishedAt; f != nil {
				next.finishedAt = *f
			}

			cursor := next.String()
			list.NextCursor = &cursor

			break
		}

		list.Jobs = append(list.Jobs, s.jobOf(row))
	}

	return list, nil
}

// RetryJob sets a dead job back to pending and writes the retry to the
// audit log, in one transaction.
func (s *server) RetryJob(ctx context.Context, req apigen.RetryJobRequestObject) (apigen.RetryJobResponseObject, error) {
	p, _ := auth.PrincipalFrom(ctx)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	job, err := db.New(tx).GetJob(ctx, req.Id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, problemf(http.StatusNotFound, "No job has this ID.")
	}

	if err != nil {
		return nil, fmt.Errorf("read job %d: %w", req.Id, err)
	}

	err = queue.RetryDead(ctx, tx, req.Id)

	switch {
	case errors.Is(err, queue.ErrNotDead):
		return nil, problemf(http.StatusConflict, "The job is %s, not dead; only dead jobs can be retried.", job.Status)
	case errors.Is(err, queue.ErrDuplicate):
		return nil, problemf(http.StatusConflict, "Another job with the same unique key is pending or running.")
	case err != nil:
		return nil, fmt.Errorf("retry job %d: %w", req.Id, err)
	}

	err = s.audit.Write(ctx, tx, audit.Entry{
		ActorKind: actorKindOf(p), ActorID: p.ID.String(), Action: ActionJobRetried,
		TargetKind: "job", TargetID: strconv.FormatInt(job.ID, 10),
		Old: map[string]any{"kind": job.Kind, "status": job.Status, "attempts": job.Attempts},
		New: map[string]any{"status": "pending", "attempts": 0},
	})
	if err != nil {
		return nil, fmt.Errorf("audit the retry of job %d: %w", req.Id, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit the retry of job %d: %w", req.Id, err)
	}

	return apigen.RetryJob204Response{}, nil
}

// jobOf is the API's view of a dead job, with its last error masked.
func (s *server) jobOf(row db.ListDeadJobsRow) apigen.Job {
	j := apigen.Job{
		Id: row.ID, Kind: row.Kind, Route: row.Route, Attempts: int(row.Attempts), MaxAttempts: int(row.MaxAttempts),
		CreatedAt: row.CreatedAt, FinishedAt: row.FinishedAt,
	}

	if row.LastError != nil {
		text := s.mask(*row.LastError)
		j.LastError = &text
	}

	return j
}

// String encodes c for the nextCursor of a page.
func (c jobCursor) String() string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(c.finishedAt.UnixNano(), 10) + "." + strconv.FormatInt(c.id, 10)))
}

// parseJobCursor decodes what jobCursor.String encoded.
func parseJobCursor(s string) (jobCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return jobCursor{}, errBadCursor
	}

	at, id, ok := strings.Cut(string(raw), ".")
	if !ok {
		return jobCursor{}, errBadCursor
	}

	nanos, err := strconv.ParseInt(at, 10, 64)
	if err != nil {
		return jobCursor{}, errBadCursor
	}

	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return jobCursor{}, errBadCursor
	}

	return jobCursor{finishedAt: time.Unix(0, nanos).UTC(), id: n}, nil
}

// actorKindOf is the audit actor kind of a caller.
func actorKindOf(p auth.Principal) string {
	if p.Kind == auth.KindToken {
		return audit.ActorToken
	}

	return audit.ActorUser
}
