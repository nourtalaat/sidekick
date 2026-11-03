package datadogdowntime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/datadogapi"
)

// The limits on what the client asks of Datadog.
const (
	// listPageSize is how many downtimes one page of the list asks for.
	listPageSize = 100
	// maxListPages is how many pages Execute and Undo read before they give
	// up, so a very long list cannot keep them busy for ever.
	maxListPages = 100
	// monitorPageSize is how many monitors the preview of a tag reads.
	monitorPageSize = 100
)

// endLayout writes the end of a downtime as Datadog reads it: ISO-8601 with
// an offset of zero.
const endLayout = "2006-01-02T15:04:05+00:00"

// timeLayouts are the forms in which Datadog writes the start and the end of
// a schedule: the form of its answers to a create, and the forms of its API
// specification.
var timeLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999-0700", "2006-01-02 15:04:05Z07:00"}

var errTooManyDowntimes = errors.New("the list of downtimes is longer than this action reads")

// client reads and writes downtimes, and reads monitors, through the
// Datadog API.
type client struct {
	*datadogapi.Client
}

// downtime is a downtime as Datadog lists it.
type downtime struct {
	ID      string
	Status  string
	Scope   string
	Message string
	// MonitorID is set for a downtime of one monitor, and MonitorTags for a
	// downtime of the monitors with all of the tags.
	MonitorID   int64
	MonitorTags []string
	// Start and End are the times of a one-time schedule. They stay zero when
	// the schedule is recurring, which Recurring says, or when Datadog gives
	// no time. A downtime that never ends has a zero End.
	Start     time.Time
	End       time.Time
	Recurring bool
}

// monitor is a monitor as the preview shows it.
type monitor struct {
	ID    int64
	Name  string
	Type  string
	State string
	Tags  []string
}

// wireDowntime is a downtime as the JSON of GET /api/v2/downtime writes it.
type wireDowntime struct {
	ID         string `json:"id"`
	Attributes struct {
		Status            string `json:"status"`
		Scope             string `json:"scope"`
		Message           string `json:"message"`
		MonitorIdentifier struct {
			MonitorID   int64    `json:"monitor_id"`
			MonitorTags []string `json:"monitor_tags"`
		} `json:"monitor_identifier"`
		Schedule struct {
			Start       string `json:"start"`
			End         string `json:"end"`
			Recurrences []any  `json:"recurrences"`
		} `json:"schedule"`
	} `json:"attributes"`
}

// wireMonitor is a monitor as the JSON of the monitors API writes it.
type wireMonitor struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	OverallState string   `json:"overall_state"`
	Tags         []string `json:"tags"`
}

// downtimeBody is the body of the call that makes a downtime. It holds only
// what Execute sets; every other attribute keeps Datadog's default.
type downtimeBody struct {
	Data struct {
		Type       string `json:"type"`
		Attributes struct {
			Message           string            `json:"message"`
			MonitorIdentifier monitorIdentifier `json:"monitor_identifier"`
			Schedule          schedule          `json:"schedule"`
			Scope             string            `json:"scope"`
		} `json:"attributes"`
	} `json:"data"`
}

// monitorIdentifier is the monitor_id or the monitor_tags of a body, never
// both.
type monitorIdentifier struct {
	MonitorID   int64    `json:"monitor_id,omitempty"`
	MonitorTags []string `json:"monitor_tags,omitempty"`
}

// schedule is a one-time schedule with an end and no start, so the downtime
// starts when it is made.
type schedule struct {
	End string `json:"end"`
}

// downtimes lists every downtime, in every state the list shows, a page at a
// time: GET /api/v2/downtime. It stops when the offset reaches
// meta.page.total_filtered_count, or at a page with no downtime, and when
// the answer has no count, at a short page.
func (c *client) downtimes(ctx context.Context) ([]downtime, error) {
	var all []downtime

	for page := range maxListPages {
		query := url.Values{}
		query.Set("page[limit]", strconv.Itoa(listPageSize))
		query.Set("page[offset]", strconv.Itoa(len(all)))

		var out struct {
			Data []wireDowntime `json:"data"`
			Meta struct {
				Page struct {
					Total *int `json:"total_filtered_count"`
				} `json:"page"`
			} `json:"meta"`
		}

		if err := c.Do(ctx, http.MethodGet, "/api/v2/downtime?"+query.Encode(), nil, &out); err != nil {
			return nil, fmt.Errorf("list the downtimes, page %d: %w", page+1, err)
		}

		for _, w := range out.Data {
			all = append(all, w.downtime())
		}

		switch {
		case len(out.Data) == 0:
			return all, nil
		case out.Meta.Page.Total != nil && len(all) >= *out.Meta.Page.Total:
			return all, nil
		case out.Meta.Page.Total == nil && len(out.Data) < listPageSize:
			return all, nil
		}
	}

	return nil, fmt.Errorf("list the downtimes: %w: %w", action.ErrPermanent, errTooManyDowntimes)
}

// create makes a downtime and returns its ID: POST /api/v2/downtime. The
// body has no id, so it is always a new downtime.
func (c *client) create(ctx context.Context, req request, message string, end time.Time) (string, error) {
	var body downtimeBody

	body.Data.Type = "downtime"
	body.Data.Attributes.Message = message
	body.Data.Attributes.Scope = req.scope
	body.Data.Attributes.Schedule = schedule{End: end.UTC().Format(endLayout)}

	if req.monitorTag != "" {
		body.Data.Attributes.MonitorIdentifier = monitorIdentifier{MonitorTags: []string{req.monitorTag}}
	} else {
		body.Data.Attributes.MonitorIdentifier = monitorIdentifier{MonitorID: req.monitorID}
	}

	var out struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	if err := c.Do(ctx, http.MethodPost, "/api/v2/downtime", body, &out); err != nil {
		return "", fmt.Errorf("create the downtime: %w", err)
	}

	if out.Data.ID == "" {
		return "", fmt.Errorf("create the downtime: %w: no id", action.ErrPermanent)
	}

	return out.Data.ID, nil
}

// monitor reads the monitor with id: GET /api/v1/monitor/{id}.
func (c *client) monitor(ctx context.Context, id int64) (monitor, error) {
	var w wireMonitor
	if err := c.Do(ctx, http.MethodGet, "/api/v1/monitor/"+strconv.FormatInt(id, 10), nil, &w); err != nil {
		return monitor{}, fmt.Errorf("read monitor %d: %w", id, err)
	}

	return monitor{ID: id, Name: w.Name, Type: w.Type, State: w.OverallState, Tags: w.Tags}, nil
}

// monitorsWithTag reads the first page of the monitors that have the tag:
// GET /api/v1/monitor with monitor_tags.
func (c *client) monitorsWithTag(ctx context.Context, tag string) ([]monitor, error) {
	query := url.Values{}
	query.Set("monitor_tags", tag)
	query.Set("page", "0")
	query.Set("page_size", strconv.Itoa(monitorPageSize))

	var raw []wireMonitor
	if err := c.Do(ctx, http.MethodGet, "/api/v1/monitor?"+query.Encode(), nil, &raw); err != nil {
		return nil, fmt.Errorf("list the monitors with tag %s: %w", tag, err)
	}

	out := make([]monitor, 0, len(raw))
	for _, w := range raw {
		out = append(out, monitor{ID: w.ID, Name: w.Name, Type: w.Type, State: w.OverallState, Tags: w.Tags})
	}

	return out, nil
}

// downtime turns the wire form into a downtime.
func (w wireDowntime) downtime() downtime {
	a := w.Attributes

	d := downtime{
		ID: w.ID, Status: a.Status, Scope: a.Scope, Message: a.Message,
		MonitorID: a.MonitorIdentifier.MonitorID, MonitorTags: a.MonitorIdentifier.MonitorTags,
		Recurring: len(a.Schedule.Recurrences) > 0,
	}

	d.Start = parseTime(a.Schedule.Start)
	d.End = parseTime(a.Schedule.End)

	return d
}

// parseTime reads a time Datadog wrote, or returns the zero time for text
// that is empty or not a time.
func parseTime(text string) time.Time {
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, text); err == nil {
			return t
		}
	}

	return time.Time{}
}
