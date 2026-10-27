package backstage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Type is the source type this package registers.
const Type = "backstage"

// notFoundText is the text of a result for an entity the catalog does not
// have.
const notFoundText = "Not in the Backstage catalog: no entity has this name, or Sidekick's token may not read it."

var (
	// errNoSecrets is returned when the source is built without a way to
	// read its token.
	errNoSecrets = errors.New("no secrets resolver in deps")
	// errUnknownTool is returned for a call to a tool the source does not
	// have. The guard only sends calls for tools the source listed, so this
	// means a bug.
	errUnknownTool = errors.New("unknown tool")
)

func init() {
	registry.Sources.Register(Type, registry.Typed(New))
}

// Source reads one Backstage catalog. It is safe for concurrent use.
type Source struct {
	cfg    Config
	client *client
	// pageSize is how many entities one page of ServiceDependencies asks
	// for. Tests make it small to page through short lists.
	pageSize int
}

var (
	_ source.Source         = (*Source)(nil)
	_ source.ServiceCatalog = (*Source)(nil)
)

// Tools returns the source's tools. They never change, and every one only
// reads.
func (s *Source) Tools(context.Context) ([]source.Tool, error) {
	return slices.Clone(toolList), nil
}

// Call runs one tool. An entity the catalog does not have returns a result
// with NoData set. A request Backstage refuses as bad returns a
// *source.ToolError with its message.
func (s *Source) Call(ctx context.Context, call source.Call) (source.Result, error) {
	res, err := s.run(ctx, call)
	if err != nil {
		return failure(call.Tool, err)
	}

	return res, nil
}

// Close releases nothing: the source holds no connections of its own.
func (s *Source) Close() error {
	return nil
}

// run sends a call to its tool.
func (s *Source) run(ctx context.Context, call source.Call) (source.Result, error) {
	switch call.Tool {
	case toolGetService:
		return s.getService(ctx, call.Input)
	case toolGetOwner:
		return s.getOwner(ctx, call.Input)
	case toolGetSystem:
		return s.getSystem(ctx, call.Input)
	default:
		return source.Result{}, fmt.Errorf("tool %q: %w", call.Tool, errUnknownTool)
	}
}

// getService runs get_service.
func (s *Source) getService(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[serviceInput](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	r, err := parseRef(in.Service, kindComponent, s.cfg.namespace(), serviceKinds)
	if err != nil {
		return source.Result{}, fmt.Errorf("service: %w", err)
	}

	return s.lookup(ctx, r, func(c *cutter, e entity) any { return c.service(e, r) })
}

// getOwner runs get_owner.
func (s *Source) getOwner(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[ownerInput](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	r, err := parseRef(in.Owner, kindGroup, s.cfg.namespace(), ownerKinds)
	if err != nil {
		return source.Result{}, fmt.Errorf("owner: %w", err)
	}

	return s.lookup(ctx, r, func(c *cutter, e entity) any { return c.owner(e, r) })
}

// getSystem runs get_system.
func (s *Source) getSystem(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[systemInput](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	r, err := parseRef(in.System, kindSystem, s.cfg.namespace(), systemKinds)
	if err != nil {
		return source.Result{}, fmt.Errorf("system: %w", err)
	}

	return s.lookup(ctx, r, func(c *cutter, e entity) any { return c.system(e, r) })
}

// lookup reads the entity r names and returns the result project makes of
// it, as JSON with a link to the entity's page in Backstage.
func (s *Source) lookup(ctx context.Context, r ref, project func(c *cutter, e entity) any) (source.Result, error) {
	var e entity

	path := "/entities/by-name/" + url.PathEscape(r.kind) + "/" + url.PathEscape(r.namespace) + "/" + url.PathEscape(r.name)
	if err := s.client.get(ctx, path, nil, &e); err != nil {
		return source.Result{}, fmt.Errorf("read %s: %w", r, err)
	}

	var c cutter

	data, err := json.Marshal(project(&c, e))
	if err != nil {
		return source.Result{}, fmt.Errorf("encode the result: %w", err)
	}

	return source.Result{
		Structured: data,
		Evidence:   []source.Evidence{{Kind: source.EvidenceResource, Title: "Backstage " + r.String(), URL: s.entityURL(r)}},
		Truncated:  c.truncated,
	}, nil
}

// entityURL returns the link to r's page in the Backstage web app.
func (s *Source) entityURL(r ref) string {
	return s.cfg.appURL() + "/catalog/" + url.PathEscape(r.namespace) + "/" + url.PathEscape(r.kind) + "/" + url.PathEscape(r.name)
}

// New builds a Backstage source from its validated config. It makes no
// network call, so Backstage being down blocks neither start-up nor
// readiness.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (source.Source, error) {
	if err := cfg.Check(name); err != nil {
		return nil, fmt.Errorf("check settings: %w", err)
	}

	if deps.Secrets == nil {
		return nil, errNoSecrets
	}

	httpClient := deps.HTTP
	if httpClient == nil {
		httpClient = httpclient.New()
	}

	return &Source{
		cfg: cfg,
		client: &client{
			http:      httpClient,
			apiURL:    cfg.apiURL(),
			userAgent: "sidekick/" + buildinfo.Version,
			token:     cfg.TokenSecret,
			secrets:   deps.Secrets,
		},
		pageSize: maxPageSize,
	}, nil
}

// failure turns the error of a call into what Call returns. A 404 is a
// result with NoData set. A 400 is a tool error with Backstage's message.
// A refused token, server errors and every other failure are failures of
// the source.
func failure(tool string, err error) (source.Result, error) {
	if se, ok := errors.AsType[*statusError](err); ok {
		switch se.code {
		case http.StatusNotFound:
			return source.Result{NoData: true, Text: notFoundText}, nil
		case http.StatusBadRequest:
			return source.Result{}, fmt.Errorf("call %s: %w", tool, &source.ToolError{Message: se.Error()})
		}
	}

	return source.Result{}, fmt.Errorf("call %s: %w", tool, err)
}

// decode decodes a tool's input. The guard has checked it against the
// tool's schema already, so a failure means the schema and the input type
// disagree; it still goes back to the model as a tool error.
func decode[T any](input json.RawMessage) (T, error) {
	var in T

	if len(input) == 0 {
		return in, nil
	}

	if err := json.Unmarshal(input, &in); err != nil {
		return in, &source.ToolError{Message: "the input does not decode: " + err.Error()}
	}

	return in, nil
}
