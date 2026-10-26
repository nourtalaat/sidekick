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

// ErrUnknownType is returned when config names a type nobody registered.
var ErrUnknownType = errors.New("unknown type")

// Registry holds the factories of one plugin kind.
type Registry[T any] struct {
	kind      string
	mu        sync.RWMutex
	factories map[string]Factory[T]
}

// Register adds a factory. It panics on a duplicate type name, because that
// is a programming error found at start-up.
func (r *Registry[T]) Register(typeName string, f Factory[T]) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.factories[typeName]; ok {
		panic(fmt.Sprintf("registry: %s type %q registered twice", r.kind, typeName))
	}

	r.factories[typeName] = f
}

// Build creates the instance called name from its config section.
func (r *Registry[T]) Build(ctx context.Context, typeName, name string, dec Decoder, deps Deps) (T, error) {
	r.mu.RLock()
	f, ok := r.factories[typeName]
	r.mu.RUnlock()

	if !ok {
		var zero T
		return zero, fmt.Errorf("%s %q: type %q: %w", r.kind, name, typeName, ErrUnknownType)
	}

	v, err := f(ctx, name, dec, deps)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("build %s %q: %w", r.kind, name, err)
	}

	return v, nil
}

// Types lists the registered type names, sorted, for docs and error messages.
func (r *Registry[T]) Types() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.factories))
	for n := range r.factories {
		names = append(names, n)
	}

	slices.Sort(names)

	return names
}

// New returns an empty registry for a plugin kind.
func New[T any](kind string) *Registry[T] {
	return &Registry[T]{kind: kind, factories: map[string]Factory[T]{}}
}

// Typed adapts a constructor that takes a typed config struct C into a Factory.
// It decodes the section, runs C's Validate method when it has one, and then
// calls build. This is how each plugin decodes its own config section.
func Typed[C any, T any](build func(ctx context.Context, name string, cfg C, deps Deps) (T, error)) Factory[T] {
	return func(ctx context.Context, name string, dec Decoder, deps Deps) (T, error) {
		var cfg C
		var zero T
		if err := dec.Decode(&cfg); err != nil {
			return zero, fmt.Errorf("decode config: %w", err)
		}

		if v, ok := any(&cfg).(Validator); ok {
			if err := v.Validate(); err != nil {
				return zero, fmt.Errorf("validate config: %w", err)
			}
		}

		t, err := build(ctx, name, cfg, deps)
		if err != nil {
			return zero, fmt.Errorf("construct: %w", err)
		}

		return t, nil
	}
}
