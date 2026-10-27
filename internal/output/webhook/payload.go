package webhook

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/report"
)

// eventFinal is the event of a callback with the final result.
const eventFinal = "investigation.final"

// The statuses of a final result.
const (
	// statusCompleted is a run that ended with a verdict.
	statusCompleted = "completed"
	// statusFailed is a run that could not finish; the report, when there
	// is one, says why in stopDetail.
	statusFailed = "failed"
)

// payload is the JSON body of a callback. Event names what it reports:
// eventFinal, with Status and Report, or a watcher event from
// output.WatcherState.EventName, with Watcher.
type payload struct {
	Event string `json:"event"`
	// DeliveryID is the same for every attempt of one callback.
	DeliveryID      string `json:"deliveryId"`
	InvestigationID string `json:"investigationId"`
	Seq             int64  `json:"seq"`
	// URL is the investigation page in the web UI, when it is enabled.
	URL     string               `json:"url,omitempty"`
	Status  string               `json:"status,omitempty"`
	Alert   alert                `json:"alert"`
	Events  int                  `json:"events"`
	Report  *report.Report       `json:"report,omitempty"`
	Watcher *output.WatcherState `json:"watcher,omitempty"`
}

// alert is the event that opened the investigation's group, as intake
// stored it, already redacted, without its text and raw payload.
type alert struct {
	Intake      string            `json:"intake"`
	Kind        event.Kind        `json:"kind"`
	AlertName   string            `json:"alertName,omitempty"`
	Title       string            `json:"title,omitempty"`
	Service     string            `json:"service,omitempty"`
	Severity    event.Severity    `json:"severity,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Fingerprint string            `json:"fingerprint,omitempty"`
	StartsAt    time.Time         `json:"startsAt,omitzero"`
	ReceivedAt  time.Time         `json:"receivedAt,omitzero"`
	Links       []link            `json:"links,omitempty"`
}

// link is a link the sender attached to the alert.
type link struct {
	Title string `json:"title,omitempty"`
	URL   string `json:"url"`
}

// encode returns p as JSON. Report text is untrusted, so <, > and & stay
// escaped for receivers that put it in a web page.
func (p payload) encode() ([]byte, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("encode the payload: %w", err)
	}

	return data, nil
}

// newPayload returns the callback of u: a watcher event for a
// PhaseWatcher update, whose Watcher must be set, and the final result
// otherwise.
func newPayload(u output.Update) payload {
	p := payload{
		InvestigationID: u.InvestigationID,
		Seq:             u.Seq,
		URL:             u.URL,
		Alert:           alertOf(u.Event),
		Events:          u.Events,
	}

	if u.Phase == output.PhaseWatcher {
		p.Event = u.Watcher.EventName()
		p.Watcher = u.Watcher
	} else {
		p.Event = eventFinal
		p.Status = finalStatus(u.Report)
		p.Report = u.Report
	}

	p.DeliveryID = u.InvestigationID + ":" + p.Event + ":" + strconv.FormatInt(u.Seq, 10)

	return p
}

// alertOf returns the parts of ev a callback carries.
func alertOf(ev event.Event) alert {
	a := alert{
		Intake:      ev.Intake,
		Kind:        ev.Kind,
		AlertName:   ev.AlertName,
		Title:       ev.Title,
		Service:     ev.Service,
		Severity:    ev.Severity,
		Labels:      ev.Labels,
		Fingerprint: ev.Fingerprint,
		StartsAt:    ev.StartsAt,
		ReceivedAt:  ev.ReceivedAt,
	}

	for _, l := range ev.Links {
		a.Links = append(a.Links, link(l))
	}

	return a
}

// finalStatus is the status of a final result with r.
func finalStatus(r *report.Report) string {
	if r == nil || r.StopReason == report.StopError {
		return statusFailed
	}

	return statusCompleted
}
