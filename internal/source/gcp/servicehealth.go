package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
)

// defaultHealthRange is the time range of list_service_health_events when
// the call gives no since.
const defaultHealthRange = 24 * time.Hour

// healthEvent is the part of a Personalized Service Health event the
// source reads.
type healthEvent struct {
	Name             string    `json:"name"`
	Title            string    `json:"title"`
	Description      string    `json:"description"`
	Category         string    `json:"category"`
	DetailedCategory string    `json:"detailedCategory"`
	State            string    `json:"state"`
	DetailedState    string    `json:"detailedState"`
	Relevance        string    `json:"relevance"`
	StartTime        time.Time `json:"startTime"`
	EndTime          time.Time `json:"endTime"`
	UpdateTime       time.Time `json:"updateTime"`
	EventImpacts     []struct {
		Product struct {
			ProductName string `json:"productName"`
		} `json:"product"`
		Location struct {
			LocationName string `json:"locationName"`
		} `json:"location"`
	} `json:"eventImpacts"`
	Updates []struct {
		UpdateTime  time.Time `json:"updateTime"`
		Title       string    `json:"title"`
		Description string    `json:"description"`
	} `json:"updates"`
}

// healthItem is one event in the result of list_service_health_events.
type healthItem struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Category    string    `json:"category,omitempty"`
	State       string    `json:"state"`
	Relevance   string    `json:"relevance,omitempty"`
	Products    []string  `json:"products,omitempty"`
	Locations   []string  `json:"locations,omitempty"`
	Started     time.Time `json:"started,omitzero"`
	Ended       time.Time `json:"ended,omitzero"`
	Updated     time.Time `json:"updated,omitzero"`
	Description string    `json:"description,omitempty"`
	LastUpdate  string    `json:"lastUpdate,omitempty"`
}

// healthResult is the result of list_service_health_events.
type healthResult struct {
	Project string       `json:"project"`
	Events  []healthItem `json:"events"`
}

// listServiceHealthEvents runs list_service_health_events.
func (s *Source) listServiceHealthEvents(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Project string `json:"project"`
		State   string `json:"state"`
		Since   string `json:"since"`
		Until   string `json:"until"`
		Limit   int    `json:"limit"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	project, err := s.project(in.Project)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	w, err := s.window(in.Since, in.Until, defaultHealthRange)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	filter := "update_time>=" + timestamp(w.start) + " update_time<" + timestamp(w.end)

	switch in.State {
	case stateActive:
		filter += " state=ACTIVE"
	case stateClosed:
		filter += " state=CLOSED"
	}

	query := url.Values{"filter": {filter}, "pageSize": {strconv.Itoa(limitOf(in.Limit, defaultItems, maxItems))}}

	var page struct {
		Events        []healthEvent `json:"events"`
		NextPageToken string        `json:"nextPageToken"`
	}

	target := s.apis.serviceHealth + "/projects/" + url.PathEscape(project) + "/locations/global/events"
	if err := s.client.get(ctx, target, query, &page); err != nil {
		return source.Result{}, fmt.Errorf("list the service health events of %s: %w", project, err)
	}

	out := healthResult{Project: project, Events: make([]healthItem, 0, len(page.Events))}
	b := budget{}

	for _, e := range page.Events {
		item := healthItem{
			ID:          lastPart(e.Name),
			Title:       source.CutText(e.Title, maxNameLen),
			Category:    strings.TrimSpace(e.Category + " " + e.DetailedCategory),
			State:       strings.TrimSpace(e.State + " " + e.DetailedState),
			Relevance:   e.Relevance,
			Started:     e.StartTime,
			Ended:       e.EndTime,
			Updated:     e.UpdateTime,
			Description: source.CutText(e.Description, maxMessageLen),
		}

		for _, impact := range e.EventImpacts {
			item.Products = appendNew(item.Products, impact.Product.ProductName)
			item.Locations = appendNew(item.Locations, impact.Location.LocationName)
		}

		if n := len(e.Updates); n > 0 {
			last := e.Updates[n-1]
			item.LastUpdate = source.CutText(strings.TrimSpace(last.Title+"\n"+last.Description), maxMessageLen)
		}

		if !b.fits(item) {
			break
		}

		out.Events = append(out.Events, item)
	}

	evidence := []source.Evidence{{
		Kind:  source.EvidenceResource,
		Title: "Personalized Service Health of " + project,
		URL:   console("/servicehealth", project),
		At:    w.end,
	}}

	empty := fmt.Sprintf("Personalized Service Health has no events for %s updated between %s and %s.",
		project, timestamp(w.start), timestamp(w.end))

	return structured(out, evidence, page.NextPageToken != "" || b.cut, len(out.Events) == 0, empty)
}

// appendNew appends s to list unless it is empty or already there.
func appendNew(list []string, s string) []string {
	if s == "" {
		return list
	}

	if slices.Contains(list, s) {
		return list
	}

	return append(list, s)
}
