package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// task is a background loop registered with RegisterTask.
type task struct {
	name string
	run  func(ctx context.Context) error
}

// taskGroup is the registered tasks as Run started them. failed receives
// the error of the first task that fails before stop is called.
type taskGroup struct {
	stop   context.CancelFunc
	wg     *sync.WaitGroup
	failed chan error
}

// RegisterTask adds a background loop, such as a connection that receives
// events. Run starts every task once the listeners serve and passes it a
// context that ends when shutdown starts; the task should then return
// soon, and before the job queue stops. A task that returns an error
// before that ends Run with the error; one that returns nil has simply
// finished. Call it before Run.
func (a *App) RegisterTask(name string, run func(ctx context.Context) error) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.running {
		return fmt.Errorf("register task %s: %w", name, errAlreadyRunning)
	}

	a.tasks = append(a.tasks, task{name: name, run: run})

	return nil
}

// startTasks runs every registered task until the group's stop is called.
func (a *App) startTasks(ctx context.Context) taskGroup {
	tasksCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	g := taskGroup{stop: stop, wg: &sync.WaitGroup{}, failed: make(chan error, len(a.tasks))}

	for _, t := range a.tasks {
		g.wg.Go(func() {
			err := t.run(tasksCtx)
			if err != nil && tasksCtx.Err() == nil {
				g.failed <- fmt.Errorf("task %s: %w", t.name, err)
			}
		})
	}

	return g
}

// stopAndWait ends the tasks' context and waits for them to return, or
// for ctx to end.
func (g taskGroup) stopAndWait(ctx context.Context) error {
	g.stop()

	done := make(chan struct{})

	go func() {
		g.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errors.Join(errTasksDidNotStop, ctx.Err())
	}
}

// errTasksDidNotStop is returned when a task is still running at the end
// of the shutdown grace period.
var errTasksDidNotStop = errors.New("background tasks did not stop in time")
