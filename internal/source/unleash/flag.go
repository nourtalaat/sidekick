package unleash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/source/flags"
)

// featurePath is the path of a project's features in the Admin API.
const featurePath = "/api/admin/projects/"

// featureRep is the part of a feature flag the source reads.
type featureRep struct {
	Name         string               `json:"name"`
	Type         string               `json:"type"`
	Archived     bool                 `json:"archived"`
	Dependencies []json.RawMessage    `json:"dependencies"`
	Environments []featureEnvironment `json:"environments"`
}

// featureEnvironment is a flag's configuration in one environment.
type featureEnvironment struct {
	Name       string            `json:"name"`
	Enabled    bool              `json:"enabled"`
	Strategies []json.RawMessage `json:"strategies"`
}

// getFlag runs get_flag.
func (s *Source) getFlag(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := flags.Decode[flags.FlagInput](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	scope, err := s.cfg.Projects.Scope(in.Project, in.Environment)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	key := strings.TrimSpace(in.Flag)
	if err := flags.CheckKey("flag", key, keyPattern); err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	var f featureRep

	path := featurePath + url.PathEscape(scope.Project) + "/features/" + url.PathEscape(key)
	if err := s.client.get(ctx, path, nil, &f); err != nil {
		return source.Result{}, fmt.Errorf("read flag %s of %s: %w", key, scope.Project, err)
	}

	env, ok := f.environment(scope.Environment)
	if !ok {
		return source.Result{NoData: true, Text: fmt.Sprintf("Flag %s of Unleash project %s has no configuration in environment %s.",
			key, scope.Project, scope.Environment)}, nil
	}

	changed, err := s.lastChanged(ctx, scope, key)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the last change of flag %s: %w", key, err)
	}

	link := s.flagURL(scope.Project, key)

	state := flags.State{
		Project:       scope.Project,
		Environment:   scope.Environment,
		Flag:          key,
		Kind:          f.Type,
		On:            env.Enabled,
		Archived:      f.Archived,
		Rules:         len(env.Strategies),
		Prerequisites: len(f.Dependencies),
		LastChanged:   changed,
		URL:           link,
	}

	evidence := []source.Evidence{{
		Kind:  source.EvidenceResource,
		Title: fmt.Sprintf("Unleash flag %s in %s/%s", key, scope.Project, scope.Environment),
		URL:   link,
		At:    changed,
	}}

	res, err := state.Result(evidence)
	if err != nil {
		return source.Result{}, fmt.Errorf("make the result: %w", err)
	}

	return res, nil
}

// lastChanged returns when the flag last changed in the environment of
// scope: the time of the newest event of the flag there. The flag's own
// answer holds no such time. A flag without events there, and a token that
// may not read events, give the zero time, so the state still comes back;
// other failures fail the call.
func (s *Source) lastChanged(ctx context.Context, scope flags.Scope, key string) (time.Time, error) {
	query := url.Values{
		"project":     {"IS:" + scope.Project},
		"environment": {"IS:" + scope.Environment},
		"feature":     {"IS:" + key},
		"limit":       {"1"},
	}

	var page eventPage
	if err := s.client.get(ctx, searchEventsPath, query, &page); err != nil {
		if se, ok := errors.AsType[*statusError](err); ok && (se.code == http.StatusForbidden || se.code == http.StatusNotFound) {
			return time.Time{}, nil
		}

		return time.Time{}, fmt.Errorf("read the newest event: %w", err)
	}

	if len(page.Events) == 0 {
		return time.Time{}, nil
	}

	return page.Events[0].CreatedAt.UTC(), nil
}

// environment returns the flag's configuration in the environment called
// name.
func (f featureRep) environment(name string) (featureEnvironment, bool) {
	for _, e := range f.Environments {
		if e.Name == name {
			return e, true
		}
	}

	return featureEnvironment{}, false
}
