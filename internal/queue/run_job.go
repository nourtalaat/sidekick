package queue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// errLeaseEnded is the cause of a short job's cancelled context when its
// lease runs out.
var errLeaseEnded = errors.New("the job's lease ran out")

// runJob runs the handler for a claimed job, renews a long job's lease
// while it runs, and then completes, fails or hands back the job. workCtx
// ends when the Runner shuts down.
func (r *Runner) runJob(workCtx context.Context, w *worker, job db.Job) {
	defer func() {
		r.active.remove(job.ID)
		<-w.slots
		signal(w.wake)
	}()

	ctx, cancel := context.WithCancelCause(workCtx)
	defer cancel(nil)

	if !w.opts.Long {
		var stop context.CancelFunc

		ctx, stop = context.WithTimeoutCause(ctx, w.opts.Lease, errLeaseEnded)
		defer stop()
	}

	stopHeartbeat := r.startHeartbeat(w, job, cancel)
	err := callHandler(ctx, w.handler, job)

	stopHeartbeat()
	r.finish(w, job, err, context.Cause(ctx))
}

// startHeartbeat renews a long job's lease every heartbeat interval until
// the returned function is called. When the lease turns out to be lost, it
// cancels the handler's context with ErrLeaseLost. The heartbeat goes on
// while the Runner shuts down, until the job is handed back.
func (r *Runner) startHeartbeat(w *worker, job db.Job, cancel context.CancelCauseFunc) (stop func()) {
	if !w.opts.Long {
		return func() {}
	}

	done := make(chan struct{})

	var beats sync.WaitGroup

	beats.Go(func() {
		ticker := time.NewTicker(w.opts.HeartbeatInterval)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
			}

			err := r.renew(job, w.opts.Lease)
			if errors.Is(err, ErrLeaseLost) {
				r.log.Warn("a job's lease was lost while it ran", jobAttrs(job)...)
				cancel(ErrLeaseLost)

				return
			}

			if err != nil {
				r.log.Warn("renewing a job's lease failed, trying again at the next heartbeat", append(jobAttrs(job), "error", err)...)
			}
		}
	})

	return func() {
		close(done)
		beats.Wait()
	}
}

// renew extends job's lease.
func (r *Runner) renew(job db.Job, lease time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	if err := renew(ctx, r.pool, job, lease); err != nil {
		return fmt.Errorf("renew lease: %w", err)
	}

	return nil
}

// finish records the result of a job's run: err from the handler, and the
// cause with which the handler's context was cancelled, if it was.
func (r *Runner) finish(w *worker, job db.Job, err, cause error) {
	switch {
	case errors.Is(cause, ErrLeaseLost):
		// Another worker may own the job now, and any write would fail.
		return
	case err == nil:
		r.complete(job)
	case errors.Is(cause, errShuttingDown):
		r.handBack(job)
	default:
		r.fail(w, job, err)
	}
}

// complete marks job done.
func (r *Runner) complete(job db.Job) {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	r.logWriteError("completing", job, complete(ctx, r.pool, job))
}

// fail records a failed run of job, and runs the kind's OnDead hook when the
// job became dead.
func (r *Runner) fail(w *worker, job db.Job, err error) {
	giveUp := errors.Is(err, ErrGiveUp)

	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	failed, failErr := fail(ctx, r.pool, job, err, giveUp, r.backoff(job.Attempts))
	if failErr != nil {
		r.logWriteError("failing", job, failErr)

		return
	}

	if failed.Status != StatusDead {
		r.log.Warn("a job failed and will be retried", append(jobAttrs(failed), "error", err, "run_at", failed.RunAt)...)

		return
	}

	r.log.Error("a job failed and is now dead", append(jobAttrs(failed), "error", err, "gave_up", giveUp)...)
	r.jobDied(w, failed)
}

// handBack returns job to pending with its attempts unchanged.
func (r *Runner) handBack(job db.Job) {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	r.logWriteError("handing back", job, handBack(ctx, r.pool, job))
}

// jobDied counts a job that just became dead and runs w's OnDead hook. w is
// nil when this Runner has no handler for the job's kind.
func (r *Runner) jobDied(w *worker, job db.Job) {
	r.opts.Metrics.jobDied(job.Kind)

	if w == nil {
		r.log.Warn("a job of a kind this replica does not run became dead, so its OnDead hook did not run", jobAttrs(job)...)

		return
	}

	if w.opts.OnDead == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), onDeadTimeout)
	defer cancel()

	if err := callOnDead(ctx, w.opts.OnDead, job); err != nil {
		r.log.Error("the OnDead hook of a dead job failed", append(jobAttrs(job), "error", err)...)
	}
}

// logWriteError logs a failed write to a claimed job, if err is not nil.
func (r *Runner) logWriteError(action string, job db.Job, err error) {
	switch {
	case err == nil:
	case errors.Is(err, ErrLeaseLost):
		r.log.Warn(action+" a job found its lease lost; another worker may own it", jobAttrs(job)...)
	default:
		r.log.Error(action+" a job failed; the reaper will retry it when its lease expires", append(jobAttrs(job), "error", err)...)
	}
}

// callHandler runs the handler, and turns a panic into an error so that one
// bad job cannot stop the Runner.
func callHandler(ctx context.Context, handler Handler, job db.Job) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%s handler panicked: %v", job.Kind, p)
		}
	}()

	if err := handler.Handle(ctx, job); err != nil {
		return fmt.Errorf("%s handler: %w", job.Kind, err)
	}

	return nil
}

// callOnDead runs an OnDead hook and turns a panic into an error.
func callOnDead(ctx context.Context, onDead func(context.Context, db.Job) error, job db.Job) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%s OnDead hook panicked: %v", job.Kind, p)
		}
	}()

	if err := onDead(ctx, job); err != nil {
		return fmt.Errorf("%s OnDead hook: %w", job.Kind, err)
	}

	return nil
}
