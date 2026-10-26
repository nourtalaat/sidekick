//go:generate mockgen -source registry.go -destination mocks_registry.go -package registry -write_package_comment=false

// Package registry maps plugin type names to factories. Each plugin package
// registers itself in an init function, and the config loader builds the
// named instances from the YAML sections.
package registry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/source"
)

// The registries, one per plugin kind.
var (
	Models  = New[model.Connector]("model")
	Sources = New[source.Source]("source")
	Intakes = New[intake.Adapter]("intake")
	Outputs = New[output.Sink]("output")
	Actions = New[action.Action]("action")
)

// Decoder decodes one plugin's own config section into a typed struct.
// *yaml.Node satisfies it; the target is always a pointer to the plugin's config struct.
type Decoder interface {
	Decode(target any) error
}

// Validator is implemented by config structs that check themselves.
type Validator interface {
	Validate() error
}

// SecretRef names a key in a Kubernetes Secret (CFG-3).
type SecretRef struct {
	Name string `yaml:"name"`
	Key  string `yaml:"key"`
}

// Secrets resolves secret references. The default reads files at
// <secrets.dir>/<name>/<key>, where secrets.dir defaults to the Helm chart's
// mount path /var/run/secrets/sidekick and can point at /run/secrets for
// Docker and Podman. Rotated secrets are picked up without a restart.
type Secrets interface {
	Resolve(ctx context.Context, ref SecretRef) ([]byte, error)
}

// Deps are shared services handed to every factory.
type Deps struct {
	Secrets Secrets
	HTTP    *http.Client
	Logger  *slog.Logger
}

// Factory builds one named instance of a plugin from its config section.
type Factory[T any] func(ctx context.Context, name string, dec Decoder, deps Deps) (T, error)

// Checker decodes one instance's config section and runs its Validate
// method without building the instance, so config validation can check a
// section without network calls or secret files. It returns the decoded
// config, a pointer to the plugin's config struct, so the caller can inspect
// it, for example for secret references or declared model capabilities.
type Checker func(ctx context.Context, name string, dec Decoder) (any, error)

// Plugin is what a plugin type registers: how to check a config section and
// how to build an instance from it. Typed makes both from one constructor.
// Check may be nil, in which case Registry.Check accepts every section.
type Plugin[T any] struct {
	Check Checker
	Build Factory[T]
}

// ErrUnknownType is returned when config names a type nobody registered.
var ErrUnknownType = errors.New("unknown type")

// Registry holds the plugin types of one plugin kind.
type Registry[T any] struct {
	kind    string
	mu      sync.RWMutex
	plugins map[string]Plugin[T]
}

// Register adds a plugin type. It panics on a duplicate type name or a
// plugin without a Build function, because both are programming errors found
// at start-up.
func (r *Registry[T]) Register(typeName string, p Plugin[T]) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.plugins[typeName]; ok {
		panic(fmt.Sprintf("registry: %s type %q registered twice", r.kind, typeName))
	}

	if p.Build == nil {
		panic(fmt.Sprintf("registry: %s type %q has no Build function", r.kind, typeName))
	}

	r.plugins[typeName] = p
}

// Check decodes and validates the config section of the instance called
// name, without building it. It returns the decoded config, or nil when the
// plugin has no Check function.
func (r *Registry[T]) Check(ctx context.Context, typeName, name string, dec Decoder) (any, error) {
	p, err := r.plugin(typeName)
	if err != nil {
		return nil, fmt.Errorf("check %s %q: %w", r.kind, name, err)
	}

	if p.Check == nil {
		return nil, nil
	}

	cfg, err := p.Check(ctx, name, dec)
	if err != nil {
		return nil, fmt.Errorf("check %s %q: %w", r.kind, name, err)
	}

	return cfg, nil
}

// Build creates the instance called name from its config section.
func (r *Registry[T]) Build(ctx context.Context, typeName, name string, dec Decoder, deps Deps) (T, error) {
	var zero T

	p, err := r.plugin(typeName)
	if err != nil {
		return zero, fmt.Errorf("build %s %q: %w", r.kind, name, err)
	}

	v, err := p.Build(ctx, name, dec, deps)
	if err != nil {
		return zero, fmt.Errorf("build %s %q: %w", r.kind, name, err)
	}

	return v, nil
}

// Types lists the registered type names, sorted, for docs and error messages.
func (r *Registry[T]) Types() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.plugins))
	for n := range r.plugins {
		names = append(names, n)
	}

	slices.Sort(names)

	return names
}

// plugin returns the plugin registered as typeName.
func (r *Registry[T]) plugin(typeName string) (Plugin[T], error) {
	r.mu.RLock()
	p, ok := r.plugins[typeName]
	r.mu.RUnlock()

	if !ok {
		return Plugin[T]{}, fmt.Errorf("type %q: %w", typeName, ErrUnknownType)
	}

	return p, nil
}

// New returns an empty registry for a plugin kind.
func New[T any](kind string) *Registry[T] {
	return &Registry[T]{kind: kind, plugins: map[string]Plugin[T]{}}
}

// Typed adapts a constructor that takes a typed config struct C into a
// Plugin. Its Check and its Build both decode the section and run C's
// Validate method when it has one; Build then calls build. This is how each
// plugin decodes its own config section.
func Typed[C any, T any](build func(ctx context.Context, name string, cfg C, deps Deps) (T, error)) Plugin[T] {
	return Plugin[T]{
		Check: func(_ context.Context, _ string, dec Decoder) (any, error) {
			cfg, err := decodeChecked[C](dec)
			if err != nil {
				return nil, fmt.Errorf("check typed config: %w", err)
			}

			return cfg, nil
		},
		Build: func(ctx context.Context, name string, dec Decoder, deps Deps) (T, error) {
			var zero T

			cfg, err := decodeChecked[C](dec)
			if err != nil {
				return zero, fmt.Errorf("build from typed config: %w", err)
			}

			t, err := build(ctx, name, *cfg, deps)
			if err != nil {
				return zero, fmt.Errorf("construct: %w", err)
			}

			return t, nil
		},
	}
}

// decodeChecked decodes a section into a new C and runs C's Validate method
// when it has one.
func decodeChecked[C any](dec Decoder) (*C, error) {
	var cfg C
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}

	if v, ok := any(&cfg).(Validator); ok {
		if err := v.Validate(); err != nil {
			return nil, fmt.Errorf("validate config: %w", err)
		}
	}

	return &cfg, nil
}
