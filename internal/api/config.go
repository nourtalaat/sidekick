package api

import (
	"context"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/rules"
)

// RuntimeRules reads the listeners and routes stored at runtime.
// *rules.Store is one.
type RuntimeRules interface {
	Load(ctx context.Context) (rules.Runtime, error)
}

// GetConfig returns the file config with its defaults and the runtime
// rules. The config holds secrets only as references; strings the
// redaction rules match are redacted too, in case a value that should
// have been a secret was written into the file.
func (s *server) GetConfig(ctx context.Context, _ apigen.GetConfigRequestObject) (apigen.GetConfigResponseObject, error) {
	rt, err := s.rules.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the runtime rules: %w", err)
	}

	file, _ := s.redacted(config.Document(s.cfg)).(map[string]any)

	resp := apigen.GetConfig200JSONResponse{
		File:    file,
		Runtime: apigen.RuntimeRules{Listeners: []apigen.RuntimeRule{}, Routes: []apigen.RuntimeRule{}},
	}

	for _, l := range rt.Listeners {
		resp.Runtime.Listeners = append(resp.Runtime.Listeners, apigen.RuntimeRule{
			Name: l.Name, Enabled: l.Enabled, Version: l.Version, UpdatedBy: l.UpdatedBy, UpdatedAt: l.UpdatedAt,
			Config: s.ruleDocument(l.Config),
		})
	}

	for _, r := range rt.Routes {
		resp.Runtime.Routes = append(resp.Runtime.Routes, apigen.RuntimeRule{
			Name: r.Config.Name, Enabled: r.Enabled, Version: r.Version, UpdatedBy: r.UpdatedBy, UpdatedAt: r.UpdatedAt,
			Config: s.ruleDocument(r.Config),
		})
	}

	return resp, nil
}

// ruleDocument returns a runtime rule as a redacted document.
func (s *server) ruleDocument(rule any) map[string]any {
	doc, ok := s.redacted(config.Document(rule)).(map[string]any)
	if !ok {
		return map[string]any{}
	}

	return doc
}

// redacted returns doc with every string, map keys aside, passed through
// the redactor.
func (s *server) redacted(doc any) any {
	if s.redactor == nil {
		return doc
	}

	switch x := doc.(type) {
	case string:
		return s.redactor.Text(x).Text
	case map[string]any:
		for k, v := range x {
			x[k] = s.redacted(v)
		}

		return x
	case []any:
		for i, v := range x {
			x[i] = s.redacted(v)
		}

		return x
	default:
		return x
	}
}
