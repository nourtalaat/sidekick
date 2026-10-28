package api

import (
	"context"
	"fmt"
	"regexp"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/rules"
)

// urlUserinfo matches the scheme and the user and password of a URL, such
// as "https://" and "user:token@" in https://user:token@mcp.internal/.
var urlUserinfo = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^/\s?#@]+@`)

// RuntimeRules reads the listeners and routes stored at runtime.
// *rules.Store is one.
type RuntimeRules interface {
	Load(ctx context.Context) (rules.Runtime, error)
}

// GetConfig returns the file config with its defaults and the runtime
// rules. The config holds secrets only as references; in case a
// credential was written into the file anyway, every string is masked:
// URLs lose their user and password, and what the redaction rules match
// is redacted.
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

// redacted returns doc with every string, map keys aside, masked: the
// user and password of every URL in it removed, and the rest passed
// through the redactor.
func (s *server) redacted(doc any) any {
	switch x := doc.(type) {
	case string:
		return s.mask(x)
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

// mask removes the user and password of every URL in text, which are
// credentials whatever the redaction rules say, then redacts the rest.
func (s *server) mask(text string) string {
	text = urlUserinfo.ReplaceAllString(text, "${1}")
	if s.redactor == nil {
		return text
	}

	return s.redactor.Text(text).Text
}
