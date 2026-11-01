package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/strategy"
	"github.com/nourtalaat/sidekick/internal/strategystore"
)

// The page sizes of the list of versions.
const (
	defaultVersionPage = 50
	maxVersionPage     = 200
)

// StrategyStore saves and reads the runtime strategies. *strategystore.Store
// is one. Every save is checked, audited and announced to the other
// replicas by the store, in one transaction.
type StrategyStore interface {
	Save(ctx context.Context, in strategystore.SaveInput) (strategystore.Saved, error)
	Current(ctx context.Context) ([]strategystore.Version, error)
	Versions(ctx context.Context, name string, before *int, limit int) ([]strategystore.Version, error)
	Version(ctx context.Context, name string, n int) (strategystore.Version, error)
}

// ListStrategies returns the strategies in effect and the runtime ones that
// are not.
func (s *server) ListStrategies(ctx context.Context, _ apigen.ListStrategiesRequestObject) (apigen.ListStrategiesResponseObject, error) {
	o, err := s.strategyOverview(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the strategies: %w", err)
	}

	out := []apigen.Strategy{}

	for _, e := range o.effective.Set.Entries() {
		item := strategyView(e.Strategy)
		item.Overrides = layersOf(e.Overrides)
		item.Status = "active"

		if v, ok := o.stored(e.Strategy.Name); ok && e.Strategy.Origin == strategy.Runtime {
			item = withStoredVersion(item, v)
		}

		out = append(out, item)
	}

	for _, skipped := range o.effective.Shadowed {
		st, _ := o.effective.Set.Layer(strategy.Runtime, skipped.Name)
		item := strategyView(st)
		item.Status, item.StatusReason = "shadowed", &skipped.Reason

		if v, ok := o.stored(skipped.Name); ok {
			item = withStoredVersion(item, v)
		}

		out = append(out, item)
	}

	for _, skipped := range o.effective.Invalid {
		item := apigen.Strategy{Name: skipped.Name, Layer: string(strategy.Runtime), Status: "invalid", StatusReason: &skipped.Reason}

		if v, ok := o.stored(skipped.Name); ok {
			item = withStoredVersion(item, v)
		}

		out = append(out, item)
	}

	return apigen.ListStrategies200JSONResponse{Strategies: out}, nil
}

// GetStrategy returns the strategy in effect with a name, with its YAML.
func (s *server) GetStrategy(ctx context.Context, req apigen.GetStrategyRequestObject) (apigen.GetStrategyResponseObject, error) {
	o, err := s.strategyOverview(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the strategies: %w", err)
	}

	if st, ok := o.effective.Set.Get(req.Name); ok {
		item := strategyView(st)
		item.Status = "active"

		item.Overrides = layersOf(entryOf(o.effective.Set.Entries(), req.Name).Overrides)

		if v, ok := o.stored(req.Name); ok && st.Origin == strategy.Runtime {
			item = withStoredVersion(item, v)
		}

		return apigen.GetStrategy200JSONResponse(strategyDetail(item, st.Body)), nil
	}

	if v, ok := o.stored(req.Name); ok {
		i := slices.IndexFunc(o.effective.Invalid, func(sk strategystore.Skipped) bool { return sk.Name == req.Name })
		if i >= 0 {
			item := withStoredVersion(apigen.Strategy{Name: req.Name, Layer: string(strategy.Runtime), Status: "invalid", StatusReason: &o.effective.Invalid[i].Reason}, v)

			return apigen.GetStrategy200JSONResponse(strategyDetail(item, v.Body)), nil
		}
	}

	return nil, strategyNotFound(req.Name)
}

// ListStrategyVersions returns a page of the saved versions of a runtime
// strategy, newest first.
func (s *server) ListStrategyVersions(ctx context.Context, req apigen.ListStrategyVersionsRequestObject) (apigen.ListStrategyVersionsResponseObject, error) {
	limit := defaultVersionPage
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}

	if limit < 1 || limit > maxVersionPage {
		return nil, problemf(http.StatusBadRequest, "limit must be 1 to %d.", maxVersionPage)
	}

	o, err := s.strategyOverview(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the strategies: %w", err)
	}

	rows, err := s.strategies.Versions(ctx, req.Name, req.Params.Before, limit+1)
	if err != nil {
		return nil, fmt.Errorf("list the versions: %w", err)
	}

	if _, known := o.effective.Set.Get(req.Name); !known && len(rows) == 0 && req.Params.Before == nil {
		if _, stored := o.stored(req.Name); !stored {
			return nil, strategyNotFound(req.Name)
		}
	}

	resp := apigen.StrategyVersionList{Versions: []apigen.StrategyVersion{}}

	if len(rows) > limit {
		rows = rows[:limit]
		resp.Next = &rows[len(rows)-1].Number
	}

	current, _ := o.stored(req.Name)

	for _, v := range rows {
		resp.Versions = append(resp.Versions, versionView(v, v.Number == current.Number))
	}

	return apigen.ListStrategyVersions200JSONResponse(resp), nil
}

// SaveStrategyVersion saves the YAML as the next version of a runtime
// strategy.
func (s *server) SaveStrategyVersion(ctx context.Context, req apigen.SaveStrategyVersionRequestObject) (apigen.SaveStrategyVersionResponseObject, error) {
	if s.strategies == nil {
		return nil, errStrategiesUnavailable
	}

	actor := actorOf(ctx)

	saved, err := s.strategies.Save(ctx, strategystore.SaveInput{
		Actor: strategystore.Actor{Kind: actor.Kind, ID: actor.ID}, Name: req.Name,
		Body: req.Body.Body, Reason: req.Body.Reason, BaseVersion: req.Body.BaseVersion,
	})
	if err != nil {
		return nil, strategyError(err, req.Name)
	}

	v, err := s.strategies.Version(ctx, req.Name, saved.Version.Number)
	if err != nil {
		return nil, fmt.Errorf("read the saved version: %w", err)
	}

	return apigen.SaveStrategyVersion201JSONResponse(versionDetail(v, true)), nil
}

// GetStrategyVersion returns one saved version of a runtime strategy.
func (s *server) GetStrategyVersion(ctx context.Context, req apigen.GetStrategyVersionRequestObject) (apigen.GetStrategyVersionResponseObject, error) {
	if s.strategies == nil {
		return nil, errStrategiesUnavailable
	}

	v, err := s.strategies.Version(ctx, req.Name, req.Version)
	if errors.Is(err, strategystore.ErrNotFound) {
		return nil, &Error{
			Status: http.StatusNotFound, Code: codeNotFound,
			Detail: fmt.Sprintf("Strategy %s has no saved version %d.", req.Name, req.Version),
		}
	}

	if err != nil {
		return nil, fmt.Errorf("read the version: %w", err)
	}

	current, err := s.currentVersion(ctx, req.Name)
	if err != nil {
		return nil, fmt.Errorf("read the current version: %w", err)
	}

	return apigen.GetStrategyVersion200JSONResponse(versionDetail(v, v.Number == current)), nil
}

// overview is the strategies in effect and the stored runtime strategies
// they were made from.
type overview struct {
	effective strategystore.Effective
	current   []strategystore.Version
}

// strategyOverview reads the stored runtime strategies and merges them with
// the built-in and file strategies, as the replicas do.
func (s *server) strategyOverview(ctx context.Context) (overview, error) {
	if s.strategies == nil {
		return overview{}, errStrategiesUnavailable
	}

	current, err := s.strategies.Current(ctx)
	if err != nil {
		return overview{}, fmt.Errorf("read the runtime strategies: %w", err)
	}

	return overview{effective: strategystore.Merge(s.strategyBase, current), current: current}, nil
}

// stored returns the current stored version of the runtime strategy name.
func (o overview) stored(name string) (strategystore.Version, bool) {
	i := slices.IndexFunc(o.current, func(v strategystore.Version) bool { return v.Name == name })
	if i < 0 {
		return strategystore.Version{}, false
	}

	return o.current[i], true
}

// currentVersion returns the number of the current version of the runtime
// strategy name, or zero when none is stored.
func (s *server) currentVersion(ctx context.Context, name string) (int, error) {
	current, err := s.strategies.Current(ctx)
	if err != nil {
		return 0, fmt.Errorf("read the runtime strategies: %w", err)
	}

	i := slices.IndexFunc(current, func(v strategystore.Version) bool { return v.Name == name })
	if i < 0 {
		return 0, nil
	}

	return current[i].Number, nil
}

// strategyView is the API's view of a strategy, without its status.
func strategyView(st strategy.Strategy) apigen.Strategy {
	return apigen.Strategy{
		Name: st.Name, Description: nonEmptyText(st.Description), Layer: string(st.Origin), ReadOnly: st.Origin != strategy.Runtime,
		Version: nonEmptyText(st.Version),
	}
}

// strategyDetail adds the YAML to item.
func strategyDetail(item apigen.Strategy, body string) apigen.StrategyDetail {
	return apigen.StrategyDetail{
		Body: body, Description: item.Description, Layer: item.Layer, Name: item.Name, Overrides: item.Overrides, ReadOnly: item.ReadOnly,
		RuntimeVersion: item.RuntimeVersion, Status: item.Status, StatusReason: item.StatusReason, UpdatedAt: item.UpdatedAt,
		UpdatedBy: item.UpdatedBy, Version: item.Version,
	}
}

// withStoredVersion adds what the stored current version of a runtime
// strategy says to item.
func withStoredVersion(item apigen.Strategy, v strategystore.Version) apigen.Strategy {
	item.RuntimeVersion = &v.Number
	item.Version = new(strategy.RuntimeVersion(v.Number))
	item.UpdatedAt = &v.CreatedAt
	item.UpdatedBy = nonEmptyText(v.AuthorName)

	return item
}

// layersOf lists layers as the API names them, or nil for none.
func layersOf(origins []strategy.Origin) *[]apigen.StrategyLayer {
	if len(origins) == 0 {
		return nil
	}

	out := make([]apigen.StrategyLayer, len(origins))
	for i, o := range origins {
		out[i] = string(o)
	}

	return &out
}

// entryOf returns the entry for name.
func entryOf(entries []strategy.Entry, name string) strategy.Entry {
	i := slices.IndexFunc(entries, func(e strategy.Entry) bool { return e.Strategy.Name == name })
	if i < 0 {
		return strategy.Entry{}
	}

	return entries[i]
}

// versionView is the API's view of a stored version, without its YAML.
func versionView(v strategystore.Version, current bool) apigen.StrategyVersion {
	return apigen.StrategyVersion{
		Name: v.Name, Version: v.Number, Reason: v.Reason, Author: v.Author, AuthorName: nonEmptyText(v.AuthorName),
		CreatedAt: v.CreatedAt, Current: current, ProposalId: v.ProposalID, BenchRunId: v.BenchRunID,
	}
}

// versionDetail is the API's view of a stored version with its YAML.
func versionDetail(v strategystore.Version, current bool) apigen.StrategyVersionDetail {
	return apigen.StrategyVersionDetail{
		Name: v.Name, Version: v.Number, Reason: v.Reason, Author: v.Author, AuthorName: nonEmptyText(v.AuthorName),
		CreatedAt: v.CreatedAt, Current: current, ProposalId: v.ProposalID, BenchRunId: v.BenchRunID, Body: v.Body,
	}
}
