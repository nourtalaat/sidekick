package launchdarkly

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/source/flags"
)

// flagsPath is the feature flags API.
const flagsPath = "/api/v2/flags/"

// maxValueLen bounds the value of a variation in a result.
const maxValueLen = 128

// weightScale is what a rollout weight of 100% is: LaunchDarkly gives
// weights in thousandths of a percent.
const weightScale = 1000

// flagRep is the part of a feature flag the source reads.
type flagRep struct {
	Key          string                     `json:"key"`
	Name         string                     `json:"name"`
	Kind         string                     `json:"kind"`
	Archived     bool                       `json:"archived"`
	Variations   []variation                `json:"variations"`
	Environments map[string]flagEnvironment `json:"environments"`
}

// variation is one value a flag can serve.
type variation struct {
	Value json.RawMessage `json:"value"`
	Name  string          `json:"name"`
}

// flagEnvironment is a flag's configuration in one environment.
type flagEnvironment struct {
	On             bool              `json:"on"`
	Archived       bool              `json:"archived"`
	LastModified   int64             `json:"lastModified"`
	Version        int               `json:"version"`
	Targets        []target          `json:"targets"`
	ContextTargets []target          `json:"contextTargets"`
	Rules          []json.RawMessage `json:"rules"`
	Prerequisites  []json.RawMessage `json:"prerequisites"`
	Fallthrough    struct {
		Variation *int `json:"variation"`
		Rollout   *struct {
			Variations []struct {
				Variation int `json:"variation"`
				Weight    int `json:"weight"`
			} `json:"variations"`
			BucketBy    string `json:"bucketBy"`
			ContextKind string `json:"contextKind"`
		} `json:"rollout"`
	} `json:"fallthrough"`
	OffVariation *int `json:"offVariation"`
	Site         struct {
		Href string `json:"href"`
	} `json:"_site"`
}

// target is a list of users or contexts a flag serves one variation.
type target struct {
	Values []string `json:"values"`
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

	var f flagRep

	path := flagsPath + url.PathEscape(scope.Project) + "/" + url.PathEscape(key)
	if err := s.client.get(ctx, path, url.Values{"env": {scope.Environment}}, &f); err != nil {
		return source.Result{}, fmt.Errorf("read flag %s of %s: %w", key, scope.Project, err)
	}

	env, ok := f.Environments[scope.Environment]
	if !ok {
		return source.Result{NoData: true, Text: fmt.Sprintf("Flag %s of LaunchDarkly project %s has no configuration in environment %s.",
			key, scope.Project, scope.Environment)}, nil
	}

	link := s.siteURL(env.Site.Href)
	if link == "" {
		link = s.flagURL(scope, key)
	}

	state := flags.State{
		Project:       scope.Project,
		Environment:   scope.Environment,
		Flag:          key,
		Name:          f.Name,
		Kind:          f.Kind,
		On:            env.On,
		Archived:      f.Archived || env.Archived,
		Rules:         len(env.Rules),
		Targets:       countTargets(env.Targets) + countTargets(env.ContextTargets),
		Prerequisites: len(env.Prerequisites),
		Fallthrough:   f.fallthroughText(env),
		OffVariation:  f.variationText(env.OffVariation),
		Version:       env.Version,
		URL:           link,
	}

	if env.LastModified > 0 {
		state.LastChanged = time.UnixMilli(env.LastModified).UTC()
	}

	evidence := []source.Evidence{{
		Kind:  source.EvidenceResource,
		Title: fmt.Sprintf("LaunchDarkly flag %s in %s/%s", key, scope.Project, scope.Environment),
		URL:   link,
		At:    state.LastChanged,
	}}

	res, err := state.Result(evidence)
	if err != nil {
		return source.Result{}, fmt.Errorf("make the result: %w", err)
	}

	return res, nil
}

// fallthroughText describes what the flag serves when it is on and no
// target or rule matches: one variation, or a percentage rollout.
func (f flagRep) fallthroughText(env flagEnvironment) string {
	ft := env.Fallthrough

	if ft.Rollout == nil {
		return f.variationText(ft.Variation)
	}

	by := ft.Rollout.ContextKind
	if by == "" {
		by = "user"
	}

	if ft.Rollout.BucketBy != "" {
		by += " " + ft.Rollout.BucketBy
	}

	parts := make([]string, 0, len(ft.Rollout.Variations))
	for _, v := range ft.Rollout.Variations {
		pct := strconv.FormatFloat(float64(v.Weight)/weightScale, 'f', -1, 64)
		parts = append(parts, pct+"% "+f.variationText(&v.Variation))
	}

	return "rollout by " + by + ": " + strings.Join(parts, ", ")
}

// variationText describes the variation at index i, such as
// `variation 0: true ("On")`, or nothing when i is nil.
func (f flagRep) variationText(i *int) string {
	if i == nil {
		return ""
	}

	text := "variation " + strconv.Itoa(*i)
	if *i < 0 || *i >= len(f.Variations) {
		return text
	}

	v := f.Variations[*i]

	var value bytes.Buffer
	if json.Compact(&value, v.Value) == nil {
		text += ": " + source.CutText(value.String(), maxValueLen)
	}

	if v.Name != "" {
		text += " (" + strconv.Quote(source.CutText(v.Name, flags.MaxNameLen)) + ")"
	}

	return text
}

// countTargets returns the number of users or contexts in targets.
func countTargets(targets []target) int {
	n := 0
	for _, t := range targets {
		n += len(t.Values)
	}

	return n
}
