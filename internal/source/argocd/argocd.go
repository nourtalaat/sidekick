package argocd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	argo "github.com/nourtalaat/sidekick/internal/argocd"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Type is the source type this package registers.
const Type = "argocd"

// notFoundText is the text of a result for an application Argo CD does not
// have.
const notFoundText = "Not found in Argo CD: the application does not exist, or is not in the project named."

// forbiddenText is the message of the tool error for an application the
// token may not get.
const forbiddenText = "Argo CD refused the token's access to this application; the token needs the RBAC action " +
	"\"applications, get\" on it."

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

// Source reads one Argo CD server. It is safe for concurrent use.
type Source struct {
	cfg    Config
	client *argo.Client
}

var _ source.Source = (*Source)(nil)

// Tools returns the source's tools. They never change, and every one only
// reads.
func (s *Source) Tools(context.Context) ([]source.Tool, error) {
	return slices.Clone(toolList), nil
}

// Call runs one tool. An application Argo CD does not have, and a list
// without matches, return a result with NoData set. An application the
// config does not list, input that does not fit and a request Argo CD
// refuses return a *source.ToolError.
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
	case toolListApplications:
		return s.listApplications(ctx, call.Input)
	case toolGetApplication:
		return s.getApplication(ctx, call.Input)
	case toolGetHistory:
		return s.getHistory(ctx, call.Input)
	default:
		return source.Result{}, fmt.Errorf("tool %q: %w", call.Tool, errUnknownTool)
	}
}

// New builds an Argo CD source from its validated config. It makes no
// network call, so Argo CD being down blocks neither start-up nor
// readiness.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (source.Source, error) {
	if err := cfg.Check(name); err != nil {
		return nil, fmt.Errorf("check settings: %w", err)
	}

	if deps.Secrets == nil {
		return nil, errNoSecrets
	}

	client, err := argo.New(argo.Options{
		BaseURL:     cfg.BaseURL,
		TokenSecret: cfg.TokenSecret,
		Namespace:   cfg.Namespace,
		HTTP:        deps.HTTP,
		Secrets:     deps.Secrets,
	})
	if err != nil {
		return nil, fmt.Errorf("make the Argo CD client: %w", err)
	}

	return &Source{cfg: cfg, client: client}, nil
}

// failure turns the error of a call into what Call returns. A 404 is a
// result with NoData set. A 403 and other refusals of the call itself,
// such as a 400, are tool errors. A refused token, rate limits, server
// errors and secret failures are failures of the source.
func failure(tool string, err error) (source.Result, error) {
	if se, ok := errors.AsType[*argo.StatusError](err); ok {
		switch code := se.Code; {
		case code == http.StatusNotFound:
			return source.Result{NoData: true, Text: notFoundText}, nil
		case code == http.StatusForbidden:
			return source.Result{}, fmt.Errorf("call %s: %w", tool, &source.ToolError{Message: forbiddenText})
		case code >= http.StatusBadRequest && code < http.StatusInternalServerError &&
			code != http.StatusUnauthorized && code != http.StatusTooManyRequests:
			return source.Result{}, fmt.Errorf("call %s: %w", tool, &source.ToolError{Message: se.Error()})
		}
	}

	return source.Result{}, fmt.Errorf("call %s: %w", tool, err)
}
