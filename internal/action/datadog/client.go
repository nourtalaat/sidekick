package datadog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/datadogapi"
)

// client reads and writes monitors through the Datadog API.
type client struct {
	*datadogapi.Client
}

// get reads the monitor with id as the fields Datadog sent.
func (c *client) get(ctx context.Context, id int64) (map[string]json.RawMessage, error) {
	var live map[string]json.RawMessage
	if err := c.Do(ctx, http.MethodGet, monitorPath(id), nil, &live); err != nil {
		return nil, fmt.Errorf("read monitor %d: %w", id, err)
	}

	return live, nil
}

// validate asks Datadog to check the definition of the monitor with id.
// A 400 answer is Datadog finding the definition invalid, and the error
// says why, wrapping action.ErrLimit.
//
// POST /api/v1/monitor/{id}/validate takes the monitor definition as its
// body, answers 200 with {} when it is valid, and answers 400 with
// {"errors": ["..."]} when it is not, also for a bad option value.
func (c *client) validate(ctx context.Context, id int64, definition json.RawMessage) error {
	err := c.Do(ctx, http.MethodPost, monitorPath(id)+"/validate", definition, nil)

	var status *datadogapi.StatusError
	if errors.As(err, &status) && status.Code == http.StatusBadRequest {
		return fmt.Errorf("the new definition is not accepted by Datadog: %s: %w", status.Message, action.ErrLimit)
	}

	if err != nil {
		return fmt.Errorf("validate monitor %d: %w", id, err)
	}

	return nil
}

// update writes the fields of body to the monitor with id.
func (c *client) update(ctx context.Context, id int64, body map[string]json.RawMessage) error {
	if err := c.Do(ctx, http.MethodPut, monitorPath(id), body, nil); err != nil {
		return fmt.Errorf("update monitor %d: %w", id, err)
	}

	return nil
}

// remove deletes the monitor with id. A monitor that is gone already is not
// an error.
func (c *client) remove(ctx context.Context, id int64) error {
	err := c.Do(ctx, http.MethodDelete, monitorPath(id), nil, nil)

	var status *datadogapi.StatusError
	if errors.As(err, &status) && status.Code == http.StatusNotFound {
		return nil
	}

	if err != nil {
		return fmt.Errorf("delete monitor %d: %w", id, err)
	}

	return nil
}

// monitorPath is the path of the monitor with id.
func monitorPath(id int64) string {
	return "/api/v1/monitor/" + strconv.FormatInt(id, 10)
}
