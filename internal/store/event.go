package store

import (
	"encoding/json"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// storedLink is one entry of events.links.
type storedLink struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// EventOf returns a stored event as the rest of Sidekick uses it. Every
// text in it was redacted when it was stored. The raw payload is left out,
// since nothing reads it back.
func EventOf(row db.Event) (event.Event, error) {
	ev := event.Event{
		Key:         row.Key,
		Intake:      row.Intake,
		Kind:        event.Kind(row.Kind),
		Sender:      event.Sender{Kind: event.SenderKind(text(row.SenderKind)), ID: text(row.SenderID), Name: text(row.SenderName)},
		AlertName:   text(row.AlertName),
		Title:       text(row.Title),
		Text:        text(row.Body),
		Service:     text(row.Service),
		Severity:    event.Severity(text(row.Severity)),
		Fingerprint: row.Fingerprint,
		ReceivedAt:  row.ReceivedAt,
	}

	if row.Trusted {
		ev.Trust = event.Trusted
	}

	if row.StartsAt != nil {
		ev.StartsAt = *row.StartsAt
	}

	if row.EndsAt != nil {
		ev.EndsAt = *row.EndsAt
	}

	if err := json.Unmarshal(row.Labels, &ev.Labels); err != nil {
		return event.Event{}, fmt.Errorf("decode the labels: %w", err)
	}

	if row.Links != nil {
		var links []storedLink
		if err := json.Unmarshal(row.Links, &links); err != nil {
			return event.Event{}, fmt.Errorf("decode the links: %w", err)
		}

		for _, l := range links {
			ev.Links = append(ev.Links, event.Link(l))
		}
	}

	if row.Slack != nil {
		ev.Slack = &event.SlackRef{}
		if err := json.Unmarshal(row.Slack, ev.Slack); err != nil {
			return event.Event{}, fmt.Errorf("decode the Slack reference: %w", err)
		}
	}

	return ev, nil
}

// text returns *s, or "" for NULL.
func text(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}
